package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/atrxia/mus/internal/config"
	"codeberg.org/atrxia/mus/internal/dataproject"
	"codeberg.org/atrxia/mus/internal/defaults"
	"codeberg.org/atrxia/mus/internal/hashcache"
	"codeberg.org/atrxia/mus/internal/iron"
	"codeberg.org/atrxia/mus/internal/sidecar"
	"github.com/spf13/cobra"
)

func newIRODSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "irods",
		Aliases: []string{"mango"},
		Short:   "iRODS sync via the IRON CLI",
	}
	cmd.AddCommand(newIRODSUploadCmd(), newIRODSCheckCmd(), newIRODSGetCmd(), newIRODSScanCmd())
	return cmd
}

// resolveIRODSCollection computes where on iRODS uploads from this folder
// land. The layout is now MANDATORY:
//
//	<irods_home>/project/<data_project>/<safe_experiment_name>/
//
// Components:
//   - irods_home          required, from .env cascade
//   - data_project        required; must pass dataproject.ValidateName.
//     Override via --data-project flag.
//   - safe_experiment_name reproducible sanitisation of eln_experiment_name.
//     Override via --remote-name flag.
//
// Refuses (no fallbacks, no auto-derivation) if any piece is missing or
// invalid. Users see a clear message pointing at `mus eln tag <id>` /
// `mus config set data_project ...` as remediations.
// firstPathSegment returns the first non-empty path component of an absolute
// iRODS path. For `/gbiomed/home/BADS/file.csv` it returns `gbiomed`. Used to
// derive the zone for PURL construction.
func firstPathSegment(p string) string {
	for _, seg := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if seg != "" {
			return seg
		}
	}
	return ""
}

func resolveIRODSCollection(env *config.Env, overrideDataProject, overrideRemoteName string) (string, error) {
	home := env.String("irods_home")
	if home == "" {
		home = defaults.IRODSHome // baked-in fallback; overridable via -ldflags
	}
	if home == "" {
		return "", fmt.Errorf("irods_home not set — `mus config set irods_home /zone/home/...`")
	}
	home = strings.TrimRight(home, "/")

	dp := overrideDataProject
	if dp == "" {
		dp = env.String("data_project")
	}
	if dp == "" {
		return "", fmt.Errorf("data_project not set.\n" +
			"  - `mus config set data_project Fiers2025` (or pick your own NameYear), OR\n" +
			"  - `mus eln tag <experiment_id>` (prompts for a data_project), OR\n" +
			"  - pass --data-project NAME on the command line.")
	}
	if err := dataproject.ValidateName(dp); err != nil {
		return "", err
	}

	expName := overrideRemoteName
	if expName == "" {
		expName = dataproject.SanitizeForPath(env.String("eln_experiment_name"))
	}
	if expName == "" {
		return "", fmt.Errorf("no experiment-name component for remote path.\n" +
			"  - `mus eln tag <experiment_id>` populates eln_experiment_name in .env, OR\n" +
			"  - pass --remote-name NAME on the command line.")
	}

	return home + "/project/" + dp + "/" + expName, nil
}

func newIRODSUploadCmd() *cobra.Command {
	// IRON's upload is idempotent by default: matching size+modtime → skip;
	// different → overwrite. There is no "force" concept to expose.
	var exclusive, newer, dryRun bool
	var dataProject, remoteName string
	verify := true // verify-checksum on by default; opt-out below
	cmd := &cobra.Command{
		Use:   "upload FILE [FILE...]",
		Short: "upload via IRON; writes/refreshes *.mus sidecars",
		Long: "Uploads land at <irods_home>/project/<data_project>/<exp_name>/.\n" +
			"data_project and a non-empty experiment-name are MANDATORY.\n" +
			"data_project comes from .env (set via `mus config set` or `mus eln tag`);\n" +
			"the experiment-name component is a sanitised eln_experiment_name unless\n" +
			"overridden with --remote-name.\n\n" +
			"IRON handles recursion automatically when given a directory and is\n" +
			"idempotent for already-uploaded files. The --verify-checksum flag\n" +
			"(default on) re-hashes both sides after transfer.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIRODSUpload(cmd, args, iron.UploadOpts{
				VerifyChecksum: verify,
				Exclusive:      exclusive,
				Newer:          newer,
				DryRun:         dryRun,
			}, dataProject, remoteName)
		},
	}
	cmd.Flags().BoolVar(&verify, "verify-checksum", true, "re-hash both sides after upload (default: on)")
	cmd.Flags().BoolVar(&exclusive, "exclusive", false, "refuse to overwrite existing remote objects")
	cmd.Flags().BoolVar(&newer, "newer", false, "only upload files newer than the remote copy")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print actions without uploading")
	cmd.Flags().StringVar(&dataProject, "data-project", "",
		"override the data_project component (NameYear format, e.g. Fiers2025)")
	cmd.Flags().StringVar(&remoteName, "remote-name", "",
		"override the experiment-name component of the remote path")
	return cmd
}

func runIRODSUpload(cmd *cobra.Command, args []string, opts iron.UploadOpts, overrideDataProject, overrideRemoteName string) error {
	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}
	remoteCollection, err := resolveIRODSCollection(env, overrideDataProject, overrideRemoteName)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "→ remote collection: %s\n", remoteCollection)

	client, err := iron.New()
	if err != nil {
		return err
	}
	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	if err := client.Mkdir(ctx, remoteCollection); err != nil {
		return fmt.Errorf("imkdir %s: %w", remoteCollection, err)
	}

	for _, raw := range args {
		if sidecar.IsSidecar(raw) {
			fmt.Fprintf(os.Stderr, "mus: skipping sidecar %s\n", raw)
			continue
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return err
		}
		st, err := os.Stat(abs)
		if err != nil {
			return err
		}
		// IRON auto-detects file vs directory; no special handling needed.
		remote := remoteCollection + "/" + filepath.Base(abs)
		fmt.Printf("→ %s\n", remote)
		if err := client.Upload(ctx, abs, remote, opts); err != nil {
			return err
		}

		var sum string
		if !st.IsDir() {
			sum, err = cache.Sum(abs)
			if err != nil {
				return err
			}
		}

		// write/update sidecar
		scPath := sidecar.SidecarPath(abs)
		doc, err := readMaybe(scPath)
		if err != nil {
			return err
		}
		if doc == nil {
			doc = &sidecar.Doc{}
		}
		if !st.IsDir() {
			doc.File = sidecar.FileInfo{
				Sha256:  sum,
				Size:    st.Size(),
				Mtime:   st.ModTime().UTC().Truncate(time.Second),
				Hashed:  time.Now().UTC().Truncate(time.Second),
				AbsPath: abs,
			}
		}
		if doc.IRODS == nil {
			doc.IRODS = &sidecar.IRODS{}
		}
		doc.IRODS.Path = remote
		doc.IRODS.Status = "uploaded"
		doc.IRODS.UploadedAt = time.Now().UTC().Truncate(time.Second)

		// Path-based browse URL (`irods_url`). Convenient but does NOT
		// survive moves — see irods_purl for the location-independent one.
		web := env.String("irods_web")
		if web == "" {
			web = defaults.IRODSWeb // baked-in fallback
		}
		if web != "" {
			doc.IRODS.URL = strings.TrimRight(web, "/") + remote
		}

		// Persistent URL (`irods_purl`) — keyed on iRODS catalog ID, stable
		// across renames/moves. Built from <pid_base>/<zone>/<id>/. Best-
		// effort: if `iron stat -j` fails for any reason we just skip it.
		if stat, statErr := client.Stat(ctx, remote); statErr == nil {
			zone := firstPathSegment(remote)
			pidBase := env.String("irods_pid_base")
			if pidBase == "" {
				pidBase = defaults.IRODSPIDBase
			}
			if zone != "" && pidBase != "" && stat.ID != 0 {
				doc.IRODS.PURL = fmt.Sprintf("%s/%s/%d/",
					strings.TrimRight(pidBase, "/"), zone, stat.ID)
			}
		}
		// Stamp the data_project we used into the sidecar so the file
		// carries the membership label even after the .env cascade changes.
		if dp := overrideDataProject; dp != "" {
			doc.DataProject = dp
		} else if dp := env.String("data_project"); dp != "" {
			doc.DataProject = dp
		}
		// Stamp ELN context onto the sidecar. Only the experiment ID is
		// load-bearing for the new tag-folder flow; the *_name / *_id fields
		// are best-effort (populated only if some earlier ELN API call had
		// filled them into .mus).
		if env.Has("eln_experiment_id") {
			if doc.ELN == nil {
				doc.ELN = &sidecar.ELN{}
			}
			doc.ELN.ExperimentID = env.String("eln_experiment_id")
			if v := env.String("eln_experiment_name"); v != "" {
				doc.ELN.ExperimentName = v
			}
			if v := env.String("eln_study_id"); v != "" {
				doc.ELN.StudyID = v
			}
			if v := env.String("eln_study_name"); v != "" {
				doc.ELN.StudyName = v
			}
			if v := env.String("eln_project_id"); v != "" {
				doc.ELN.ProjectID = v
			}
			if v := env.String("eln_project_name"); v != "" {
				doc.ELN.ProjectName = v
			}
		}
		if err := sidecar.Write(scPath, doc); err != nil {
			return err
		}
	}
	return nil
}

func newIRODSCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check [SIDECAR_OR_FILE...]",
		Short: "verify local sha256 against the remote checksum reported by IRON",
		Args:  cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIRODSCheck(cmd, args)
		},
	}
	return cmd
}

func runIRODSCheck(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		args = []string{"."}
	}
	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	client, err := iron.New()
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	var sidecars []string
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return err
		}
		if st.IsDir() {
			more, err := walkForSidecars(a, true)
			if err != nil {
				return err
			}
			sidecars = append(sidecars, more...)
		} else if sidecar.IsSidecar(a) {
			sidecars = append(sidecars, a)
		} else {
			sc := sidecar.SidecarPath(a)
			if _, err := os.Stat(sc); err == nil {
				sidecars = append(sidecars, sc)
			}
		}
	}
	if len(sidecars) == 0 {
		return fmt.Errorf("no sidecars with iRODS info to check")
	}

	checked, failed := 0, 0
	for _, sc := range sidecars {
		doc, err := sidecar.Read(sc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR %s: %v\n", sc, err)
			failed++
			continue
		}
		if doc.IRODS == nil || doc.IRODS.Path == "" {
			continue
		}
		dataPath := sidecar.DataPath(sc)
		localSum, err := cache.Sum(dataPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR %s: %v\n", dataPath, err)
			failed++
			continue
		}
		remoteSum, err := client.Checksum(ctx, doc.IRODS.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR %s remote checksum: %v\n", dataPath, err)
			failed++
			continue
		}
		checked++
		if strings.EqualFold(localSum, remoteSum) {
			fmt.Printf("OK       %s\n", dataPath)
		} else {
			fmt.Fprintf(os.Stderr, "MISMATCH %s  local=%s remote=%s\n",
				dataPath, short(localSum), short(remoteSum))
			failed++
		}
	}
	fmt.Printf("checked %d, failed %d\n", checked, failed)
	if failed > 0 {
		return fmt.Errorf("%d failure(s)", failed)
	}
	return nil
}

func newIRODSGetCmd() *cobra.Command {
	var force, verify bool
	cmd := &cobra.Command{
		Use:   "get SIDECAR [SIDECAR...]",
		Short: "download the file each *.mus sidecar refers to",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := iron.New()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			for _, a := range args {
				if !sidecar.IsSidecar(a) {
					return fmt.Errorf("%s is not a *.mus sidecar", a)
				}
				doc, err := sidecar.Read(a)
				if err != nil {
					return err
				}
				if doc.IRODS == nil || doc.IRODS.Path == "" {
					return fmt.Errorf("%s has no irods path", a)
				}
				localPath := sidecar.DataPath(a)
				if _, err := os.Stat(localPath); err == nil && !force {
					return fmt.Errorf("%s already exists — use --force", localPath)
				}
				if err := client.Download(ctx, doc.IRODS.Path, localPath, iron.DownloadOpts{
					Exclusive:      !force,
					VerifyChecksum: verify,
				}); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite local file if present")
	cmd.Flags().BoolVar(&verify, "verify-checksum", true, "re-hash both sides after download")
	return cmd
}

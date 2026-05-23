package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/atrxia/mus/internal/config"
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
	cmd.AddCommand(newIRODSUploadCmd(), newIRODSCheckCmd(), newIRODSGetCmd())
	return cmd
}

// resolveIRODSCollection computes where on iRODS uploads from this folder land.
//
// Resolution order:
//  1. `irods_path` from .mus (explicit subpath under irods_home) — wins.
//  2. `eln_experiment_id` from .mus — fall back to "exp_<id>" subfolder.
//  3. Otherwise: a clear error.
//
// We deliberately do NOT depend on eln_project_name / study_name /
// experiment_name (those need an ELN API call to populate, and the eLabNext
// API situation is unsettled). The experiment ID alone gives every upload a
// stable, traceable identifier.
func resolveIRODSCollection(env *config.Env) (string, error) {
	home := env.String("irods_home")
	if home == "" {
		return "", fmt.Errorf("irods_home not set — `mus config set irods_home /zone/home/...`")
	}
	home = strings.TrimRight(home, "/")

	if sub := env.String("irods_path"); sub != "" {
		return home + "/" + strings.Trim(sub, "/"), nil
	}
	if expID := env.String("eln_experiment_id"); expID != "" {
		return home + "/exp_" + sanitize(expID), nil
	}
	return "", fmt.Errorf("no remote path resolvable.\n" +
		"Either:\n" +
		"  - `mus config set irods_path <subfolder>` for an explicit path, OR\n" +
		"  - `mus eln tag-folder -x EXPERIMENT_ID` to derive exp_<id>/")
}

func sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "_")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		}
	}
	out := b.String()
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	return out
}

func newIRODSUploadCmd() *cobra.Command {
	// IRON's upload is idempotent by default: matching size+modtime → skip;
	// different → overwrite. There is no "force" concept to expose.
	var exclusive, newer, dryRun bool
	verify := true // verify-checksum on by default; opt-out below
	cmd := &cobra.Command{
		Use:   "upload FILE [FILE...]",
		Short: "upload via IRON; writes/refreshes *.mus sidecars",
		Long: "IRON handles recursion automatically when given a directory and is\n" +
			"idempotent for already-uploaded files. The --verify-checksum flag\n" +
			"(default on) re-hashes both sides after transfer.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIRODSUpload(cmd, args, iron.UploadOpts{
				VerifyChecksum: verify,
				Exclusive:      exclusive,
				Newer:          newer,
				DryRun:         dryRun,
			})
		},
	}
	cmd.Flags().BoolVar(&verify, "verify-checksum", true, "re-hash both sides after upload (default: on)")
	cmd.Flags().BoolVar(&exclusive, "exclusive", false, "refuse to overwrite existing remote objects")
	cmd.Flags().BoolVar(&newer, "newer", false, "only upload files newer than the remote copy")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print actions without uploading")
	return cmd
}

func runIRODSUpload(cmd *cobra.Command, args []string, opts iron.UploadOpts) error {
	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}
	remoteCollection, err := resolveIRODSCollection(env)
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
		if web := env.String("irods_web"); web != "" {
			doc.IRODS.URL = strings.TrimRight(web, "/") + remote
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

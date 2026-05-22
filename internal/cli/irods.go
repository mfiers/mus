package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mfiers/mus/internal/config"
	"github.com/mfiers/mus/internal/hashcache"
	"github.com/mfiers/mus/internal/iron"
	"github.com/mfiers/mus/internal/sidecar"
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
	var force, verify bool
	var recursive bool
	cmd := &cobra.Command{
		Use:   "upload FILE [FILE...]",
		Short: "upload via IRON; writes/refreshes *.mus sidecars",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIRODSUpload(cmd, args, force, verify, recursive)
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "force overwrite remote")
	cmd.Flags().BoolVar(&verify, "verify", true, "ask IRON to verify after upload")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "upload directories recursively")
	return cmd
}

func runIRODSUpload(cmd *cobra.Command, args []string, force, verify, recursive bool) error {
	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}
	home := env.String("irods_home")
	if home == "" {
		return fmt.Errorf("irods_home not set — add to .mus or `mus config set irods_home /zone/home/...`")
	}
	for _, k := range []string{"eln.project_name", "eln.study_name", "eln.experiment_name"} {
		if !env.Has(k) {
			return fmt.Errorf("missing %s — run `mus eln tag-folder -x ID`", k)
		}
	}
	remoteCollection := strings.Join([]string{
		strings.TrimRight(home, "/"),
		sanitize(env.String("eln.project_name")),
		sanitize(env.String("eln.study_name")),
		sanitize(env.String("eln.experiment_name")),
	}, "/")

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
		if st.IsDir() && !recursive {
			return fmt.Errorf("%s is a directory — use -r/--recursive", abs)
		}

		remote := remoteCollection + "/" + filepath.Base(abs)
		fmt.Printf("→ %s\n", remote)
		if err := client.Put(ctx, abs, remote, iron.PutOpts{
			Recursive: st.IsDir(),
			Force:     force,
			Verify:    verify,
		}); err != nil {
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
		// stamp ELN context onto the sidecar
		if env.Has("eln.experiment_id") {
			if doc.ELN == nil {
				doc.ELN = &sidecar.ELN{}
			}
			doc.ELN.ExperimentID = env.String("eln.experiment_id")
			doc.ELN.ExperimentName = env.String("eln.experiment_name")
			doc.ELN.StudyID = env.String("eln.study_id")
			doc.ELN.StudyName = env.String("eln.study_name")
			doc.ELN.ProjectID = env.String("eln.project_id")
			doc.ELN.ProjectName = env.String("eln.project_name")
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
	var force bool
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
				if err := client.Get(ctx, doc.IRODS.Path, localPath, force); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite local file if present")
	return cmd
}

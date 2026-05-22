package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/atrxia/mus/internal/hashcache"
	"codeberg.org/atrxia/mus/internal/sidecar"
	"github.com/spf13/cobra"
)

func newCheckCmd() *cobra.Command {
	var recursive bool
	var quiet bool
	cmd := &cobra.Command{
		Use:   "check [FILE_OR_DIR...]",
		Short: "verify *.mus sidecars against current file sha256",
		Long: "Each argument may be a data file, a *.mus sidecar, or a directory.\n" +
			"With -r/--recursive, directories are walked. Exit code is non-zero if any\n" +
			"sidecar reports a mismatch or its data file is missing.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheck(cmd, args, recursive, quiet)
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "walk directories")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "only print failures")
	return cmd
}

type checkResult struct {
	path   string
	status string
	detail string
}

func runCheck(cmd *cobra.Command, args []string, recursive, quiet bool) error {
	if len(args) == 0 {
		args = []string{"."}
	}
	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	// collect sidecar paths
	var sidecars []string
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return fmt.Errorf("stat %s: %w", a, err)
		}
		switch {
		case st.IsDir():
			more, err := walkForSidecars(a, recursive)
			if err != nil {
				return err
			}
			sidecars = append(sidecars, more...)
		case sidecar.IsSidecar(a):
			sidecars = append(sidecars, a)
		default:
			sc := sidecar.SidecarPath(a)
			if _, err := os.Stat(sc); err == nil {
				sidecars = append(sidecars, sc)
			} else if !os.IsNotExist(err) {
				return err
			} else {
				return fmt.Errorf("no sidecar at %s", sc)
			}
		}
	}
	if len(sidecars) == 0 {
		return fmt.Errorf("no *.mus sidecars found")
	}

	var ok, mismatch, missing, errored int
	for _, scPath := range sidecars {
		res := checkOne(cache, scPath)
		switch res.status {
		case "ok":
			ok++
			if !quiet {
				fmt.Printf("OK       %s\n", res.path)
			}
		case "mismatch":
			mismatch++
			fmt.Fprintf(os.Stderr, "MISMATCH %s  %s\n", res.path, res.detail)
		case "missing":
			missing++
			fmt.Fprintf(os.Stderr, "MISSING  %s  (no data file at %s)\n", scPath, res.path)
		case "error":
			errored++
			fmt.Fprintf(os.Stderr, "ERROR    %s  %s\n", res.path, res.detail)
		case "stale":
			mismatch++
			fmt.Fprintf(os.Stderr, "STALE    %s  %s — run `mus tag` to refresh\n", res.path, res.detail)
		}
	}

	fmt.Printf("checked %d  ok %d  mismatch %d  missing %d  error %d\n",
		len(sidecars), ok, mismatch, missing, errored)
	if mismatch+missing+errored > 0 {
		return fmt.Errorf("%d issue(s)", mismatch+missing+errored)
	}
	return nil
}

func checkOne(cache *hashcache.Cache, scPath string) checkResult {
	doc, err := sidecar.Read(scPath)
	if err != nil {
		return checkResult{path: scPath, status: "error", detail: err.Error()}
	}
	data := sidecar.DataPath(scPath)
	st, err := os.Stat(data)
	if err != nil {
		if os.IsNotExist(err) {
			return checkResult{path: data, status: "missing"}
		}
		return checkResult{path: data, status: "error", detail: err.Error()}
	}
	if doc.File.Sha256 == "" {
		return checkResult{path: data, status: "stale", detail: "sidecar has no sha256"}
	}
	if doc.Stale(st) {
		return checkResult{path: data, status: "stale", detail: "size or mtime changed"}
	}
	sum, err := cache.Sum(data)
	if err != nil {
		return checkResult{path: data, status: "error", detail: err.Error()}
	}
	if !strings.EqualFold(sum, doc.File.Sha256) {
		return checkResult{path: data, status: "mismatch",
			detail: fmt.Sprintf("file=%s sidecar=%s", short(sum), short(doc.File.Sha256))}
	}
	return checkResult{path: data, status: "ok"}
}

func walkForSidecars(root string, recursive bool) ([]string, error) {
	var out []string
	if !recursive {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if sidecar.IsSidecar(name) {
				out = append(out, filepath.Join(root, name))
			}
		}
		return out, nil
	}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if sidecar.IsSidecar(d.Name()) {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/atrxia/mus/internal/config"
	"codeberg.org/atrxia/mus/internal/hashcache"
	"codeberg.org/atrxia/mus/internal/sidecar"
	"github.com/spf13/cobra"
)

func newTagCmd() *cobra.Command {
	var note string
	var tags []string
	var force bool
	cmd := &cobra.Command{
		Use:   "tag FILE [FILE...]",
		Short: "create/refresh a *.mus sidecar with checksum and metadata",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTag(cmd, args, note, tags, force)
		},
	}
	cmd.Flags().StringVarP(&note, "note", "m", "", "free-text note stored in the sidecar")
	cmd.Flags().StringSliceVarP(&tags, "tag", "t", nil, "tag(s) to add (repeatable)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "rehash even if sidecar appears fresh")
	return cmd
}

func runTag(cmd *cobra.Command, args []string, note string, tags []string, force bool) error {
	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}
	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	host, _ := os.Hostname()

	for _, raw := range args {
		if sidecar.IsSidecar(raw) {
			fmt.Fprintf(os.Stderr, "mus: skipping sidecar %s\n", raw)
			continue
		}
		path, err := filepath.Abs(raw)
		if err != nil {
			return err
		}
		st, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if st.IsDir() {
			return fmt.Errorf("%s is a directory — `mus tag` only takes files (for now)", path)
		}

		scPath := sidecar.SidecarPath(path)
		existing, err := readMaybe(scPath)
		if err != nil {
			return err
		}

		var sum string
		switch {
		case force || existing == nil || existing.Stale(st):
			sum, err = cache.Sum(path)
			if err != nil {
				return fmt.Errorf("hash %s: %w", path, err)
			}
		default:
			sum = existing.File.Sha256
		}

		doc := existing
		if doc == nil {
			doc = &sidecar.Doc{}
		}
		doc.File = sidecar.FileInfo{
			Sha256:  sum,
			Size:    st.Size(),
			Mtime:   st.ModTime().UTC().Truncate(time.Second),
			Hashed:  time.Now().UTC().Truncate(time.Second),
			Host:    host,
			AbsPath: path,
		}
		if note != "" {
			doc.Note = note
		}
		if len(tags) > 0 {
			doc.Tags = mergeTags(doc.Tags, tags)
		} else if t := env.List("tag"); len(t) > 0 && len(doc.Tags) == 0 {
			doc.Tags = t
		}
		// project / study / experiment context from cascade
		if env.Has("eln.experiment_id") || env.Has("eln.project_id") {
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
		fmt.Printf("%s  %s\n", short(sum), filepath.Base(path))
	}
	return nil
}

func readMaybe(scPath string) (*sidecar.Doc, error) {
	doc, err := sidecar.Read(scPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return doc, nil
}

func mergeTags(existing, added []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(existing)+len(added))
	for _, t := range existing {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, t := range added {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func short(sum string) string {
	if len(sum) >= 12 {
		return sum[:12]
	}
	return sum
}

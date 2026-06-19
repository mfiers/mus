// Package vark is a small wrapper around the `vark` CLI (a sibling
// of mus + anthive — see https://codeberg.org/mfiers/vark).
//
// mus uses vark for ONE thing today: a best-effort, opt-in cross-
// check that the de_dataset_id (or de_dataset_h5ad) the depositor
// declared in their mus.cfg actually resolves to a dataset in the
// anthive catalog BEFORE the upload happens. The catalog itself is
// the authoritative resolver at ingest time; vark just lets the
// depositor catch a typo earlier.
//
// The wrapper is intentionally minimal: shell out to `vark db ls
// --group <data_project> --json`, parse the JSON, return the list.
// Mus never modifies catalog state through vark; queries only.
package vark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultBinary is the executable name looked up on PATH.
const DefaultBinary = "vark"

// DefaultQueryTimeout caps a single vark query. The cross-check is
// best-effort; if vark / the catalog are slow we'd rather time out
// quickly than block the depositor's check.
const DefaultQueryTimeout = 10 * time.Second

// Dataset is one entry returned by `vark db ls -o json`. Shape as
// emitted by vark 0.x:
//
//	id        composite "<group_id>/<file_id>" — e.g. "AZ5/NST"
//	group_id  3-char project code              — e.g. "AZ5"
//	file_id   3-char dataset code              — e.g. "NST"
//	title     human-readable basename          — e.g. "NovaST_lecMicro_combined"
//	path      "<data_project>/<file>.duckdb"   — e.g. "AlbertiniZielonka2025/foo.duckdb"
//
// The data_project string the depositor sets in mus.cfg is the
// first path segment (e.g. "AlbertiniZielonka2025"). vark does not
// emit it as a separate field; derive via DataProject().
//
// FileID is what the depositor types into mus.cfg as
// de_dataset_id; group_id is one level up (project code, not used
// by mus directly today but useful for diagnostics).
type Dataset struct {
	ID      string `json:"id"`
	GroupID string `json:"group_id"`
	FileID  string `json:"file_id"`
	Title   string `json:"title"`
	Path    string `json:"path"`
}

// DataProject returns the first segment of Path — the data_project
// string mus stamps in mus.cfg. Empty string when Path is malformed.
func (d *Dataset) DataProject() string {
	if i := strings.Index(d.Path, "/"); i > 0 {
		return d.Path[:i]
	}
	return ""
}

// Available reports whether the `vark` binary is on PATH. Cheap
// LookPath; callers use this to decide whether to attempt a
// cross-check at all.
func Available() bool {
	_, err := exec.LookPath(DefaultBinary)
	return err == nil
}

// ListDatasets queries vark for every dataset across every
// reachable server (vark dedupes by id). The vark CLI itself
// doesn't accept a `--group` filter; we fetch everything and
// FilterByDataProject() client-side.
//
//   - ([], nil) when vark is not on PATH (silently skipped — caller
//     should use Available() first if it wants to differentiate)
//   - (datasets, nil) on a successful query
//   - (nil, err) on any failure mode (timeout, network, auth,
//     non-zero exit, malformed JSON)
//
// Errors are surfaced verbatim so the caller can render a clear
// SKIP / WARN line; mus never blocks a check on a vark failure.
func ListDatasets(ctx context.Context) ([]Dataset, error) {
	if !Available() {
		return nil, nil
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultQueryTimeout)
		defer cancel()
	}
	out, err := exec.CommandContext(ctx, DefaultBinary,
		"db", "ls", "-o", "json").Output()
	if err != nil {
		return nil, formatVarkError(err)
	}
	out = []byte(strings.TrimSpace(string(out)))
	if len(out) == 0 {
		return nil, nil
	}
	var datasets []Dataset
	if err := json.Unmarshal(out, &datasets); err != nil {
		return nil, fmt.Errorf("decoding vark output: %w (raw=%.200s)", err, out)
	}
	return datasets, nil
}

// FilterByDataProject keeps only datasets whose Path's first
// segment matches `dataProject`. Returns a new slice; never
// mutates the input.
func FilterByDataProject(all []Dataset, dataProject string) []Dataset {
	if dataProject == "" {
		return nil
	}
	out := make([]Dataset, 0, len(all))
	for _, d := range all {
		if d.DataProject() == dataProject {
			out = append(out, d)
		}
	}
	return out
}

// FindByFileID returns the Dataset whose FileID matches `id`
// (the 3-char dataset code the depositor types into
// de_dataset_id). Case-sensitive — anthive's catalog codes are
// canonical. Returns nil if not found.
func FindByFileID(datasets []Dataset, id string) *Dataset {
	if id == "" {
		return nil
	}
	for i := range datasets {
		if datasets[i].FileID == id {
			return &datasets[i]
		}
	}
	return nil
}

// FindByH5ad returns the Dataset whose Title matches the given
// basename, with or without a `.h5ad` suffix. Anthive stores
// datasets as .duckdb internally but Title is the source
// basename minus extension. Returns nil if not found.
func FindByH5ad(datasets []Dataset, basename string) *Dataset {
	if basename == "" {
		return nil
	}
	target := strings.TrimSuffix(basename, ".h5ad")
	for i := range datasets {
		if datasets[i].Title == basename || datasets[i].Title == target {
			return &datasets[i]
		}
	}
	return nil
}

// formatVarkError turns the various exec.Command failure modes
// into one error that's worth showing the depositor. ExitError
// stderr (when present) usually carries the actual vark-side
// message (auth failure, no catalog, …).
func formatVarkError(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		stderr := strings.TrimSpace(string(ee.Stderr))
		if stderr != "" {
			return fmt.Errorf("vark exit %d: %s", ee.ExitCode(), stderr)
		}
		return fmt.Errorf("vark exit %d (no stderr)", ee.ExitCode())
	}
	return fmt.Errorf("vark: %w", err)
}

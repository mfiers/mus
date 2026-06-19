package cli

// DE upload-gate. Mirrors irods_h5ad_gate.go: enumerates every DE
// file in `args` (recursing into folder args), reads each sidecar,
// and reports failures when the cached `mus de check` PASS is
// missing or stale (file sha drifted or de.CheckVersion bumped).
//
// The function accumulates every failure so the user sees the
// full list in one go — no first-failure bail.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/mfiers/mus/internal/de"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// deGateFailure is one row of the gate-error report.
type deGateFailure struct {
	Path   string
	Reason string
}

// findDEFiles walks `args` and returns every DE candidate
// (.tsv / .csv / .tsv.gz / .csv.gz). Folder args are recursed.
// `.mus` sidecars are skipped.
func findDEFiles(args []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		abs, err := filepath.Abs(p)
		if err != nil || seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if st.IsDir() {
			if err := filepath.WalkDir(a, func(p string, d os.DirEntry, werr error) error {
				if werr != nil {
					return werr
				}
				if d.IsDir() {
					return nil
				}
				if isDECandidate(p) {
					add(p)
				}
				return nil
			}); err != nil {
				return nil, fmt.Errorf("walk %s: %w", a, err)
			}
			continue
		}
		if isDECandidate(a) {
			add(a)
		}
	}
	return out, nil
}

// gateDEForUpload runs the cached-PASS check across every DE file
// in `args`. Returns the list of failures; empty when all pass or
// when there are no DE candidates in the args at all.
func gateDEForUpload(cache *hashcache.Cache, args []string) ([]deGateFailure, error) {
	paths, err := findDEFiles(args)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	var failures []deGateFailure
	for _, p := range paths {
		reason, ok := checkDECacheKey(cache, p)
		if !ok {
			failures = append(failures, deGateFailure{Path: p, Reason: reason})
		}
	}
	return failures, nil
}

// checkDECacheKey returns ("", true) when the sidecar carries a
// cached PASS at the file's current sha256 AND the recorded
// check_version is up-to-date; otherwise (reason, false).
func checkDECacheKey(cache *hashcache.Cache, path string) (string, bool) {
	scPath := sidecar.SidecarPath(path)
	doc, err := readMaybe(scPath)
	if err != nil {
		return fmt.Sprintf("sidecar read error: %v", err), false
	}
	if doc == nil || doc.DE == nil || doc.DE.InspectedSha256 == "" {
		return "no `mus de check` PASS recorded — run `mus de check -m \"...\" " +
			filepath.Base(path) + "`", false
	}
	sum, err := cache.Sum(path)
	if err != nil {
		return fmt.Sprintf("sha256 compute error: %v", err), false
	}
	if !strings.EqualFold(sum, doc.DE.InspectedSha256) {
		return fmt.Sprintf("file sha changed since check on %s",
			doc.DE.InspectedAt.UTC().Format("2006-01-02")), false
	}
	if doc.DE.InspectedCheckVersion < de.CheckVersion {
		return fmt.Sprintf("mus de-check code version bumped (sidecar=%d, current=%d)",
			doc.DE.InspectedCheckVersion, de.CheckVersion), false
	}
	return "", true
}

// formatDEGateError builds the user-facing rejection message.
// Mirrors formatH5adGateError so the depositor sees the same shape
// for both types in a mixed-batch upload.
func formatDEGateError(failures []deGateFailure) error {
	var b strings.Builder
	b.WriteString("DE upload gate refused the batch — these files have stale or missing checks:\n\n")
	for _, f := range failures {
		b.WriteString(fmt.Sprintf("  ✘ %s\n", f.Path))
		b.WriteString(fmt.Sprintf("     %s\n", f.Reason))
	}
	b.WriteString("\n")
	b.WriteString("Fix each with `mus de check -m \"...\" <FILE>` (cached pass is stat-fast).\n")
	return fmt.Errorf("%s", b.String())
}

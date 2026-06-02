package cli

// h5ad upload gate for `mus irods upload`.
//
// Before any bytes leave the local machine, walk the upload args
// (files AND folders) for `.h5ad` files. For each one, check that
// the sidecar carries a fresh PASS — file sha + vocab sha + check
// version all match the current state. If any h5ad is stale or
// unchecked, refuse the whole batch with a per-file reason; the
// depositor can run `mus h5ad check` to fix each, or pass
// `--force-h5ad-upload` to bypass.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/mfiers/mus/internal/h5ad"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/sidecar"
	"codeberg.org/mfiers/mus/internal/vocab"
)

// h5adGateFailure is one .h5ad file that fails the cache-key check.
type h5adGateFailure struct {
	Path   string
	Reason string
}

// findH5adFiles walks args (files OR folders) and returns the
// absolute paths of every .h5ad data file found.
func findH5adFiles(args []string) ([]string, error) {
	var out []string
	for _, raw := range args {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if d.IsDir() {
					return nil
				}
				if isH5adData(p) {
					out = append(out, p)
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("walk %s: %w", abs, err)
			}
			continue
		}
		if isH5adData(abs) {
			out = append(out, abs)
		}
	}
	return out, nil
}

// gateH5adForUpload scans args for .h5ad files and returns a list of
// gate failures. If `force` is true the gate is bypassed (returns
// nil) and the cache check is skipped entirely.
//
// All h5ad files in the args are checked; the function does NOT bail
// on first failure — it accumulates every failure so the user sees
// the full list with one re-run.
func gateH5adForUpload(ctx context.Context, cache *hashcache.Cache,
	args []string, force bool) ([]h5adGateFailure, error) {

	if force {
		return nil, nil
	}
	paths, err := findH5adFiles(args)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	// Load the active vocabulary once for the batch.
	vocabRes, err := vocab.Load(ctx, vocab.LoadOptions{})
	if err != nil {
		return nil, fmt.Errorf("load vocabulary for h5ad gate: %w", err)
	}

	var failures []h5adGateFailure
	for _, p := range paths {
		reason, ok := checkH5adCacheKey(cache, p,
			vocabRes.ContentSha256, h5ad.CheckVersion)
		if !ok {
			failures = append(failures, h5adGateFailure{Path: p, Reason: reason})
		}
	}
	return failures, nil
}

// checkH5adCacheKey returns ("", true) when the sidecar's triple
// cache key (file sha + vocab sha + check version) matches the
// current state; otherwise a human-readable reason + false.
func checkH5adCacheKey(cache *hashcache.Cache, h5adPath string,
	currentVocabSha string, currentCheckVersion int) (string, bool) {

	scPath := sidecar.SidecarPath(h5adPath)
	doc, err := readMaybe(scPath)
	if err != nil {
		return fmt.Sprintf("sidecar read error: %v", err), false
	}
	if doc == nil || doc.H5ad == nil || doc.H5ad.InspectedSha256 == "" {
		return "no h5ad inspection recorded", false
	}
	sum, err := cache.Sum(h5adPath)
	if err != nil {
		return fmt.Sprintf("sha256 compute error: %v", err), false
	}
	if !strings.EqualFold(sum, doc.H5ad.InspectedSha256) {
		return fmt.Sprintf("file sha changed since inspection on %s",
			doc.H5ad.InspectedAt.UTC().Format("2006-01-02")), false
	}
	if doc.H5ad.InspectedVocabSha256 == "" ||
		!strings.EqualFold(doc.H5ad.InspectedVocabSha256, currentVocabSha) {
		return "vocabulary changed since last check", false
	}
	if doc.H5ad.InspectedCheckVersion < currentCheckVersion {
		return fmt.Sprintf("mus h5ad-check code version bumped (sidecar=%d, current=%d)",
			doc.H5ad.InspectedCheckVersion, currentCheckVersion), false
	}
	return "", true
}

// formatH5adGateError builds the multi-line error message the user
// sees when the gate refuses the upload. Lists every failing file
// with its reason and points at the fix.
func formatH5adGateError(failures []h5adGateFailure) error {
	var b strings.Builder
	b.WriteString("h5ad upload gate refused the batch — these files have stale or missing checks:\n\n")
	for _, f := range failures {
		b.WriteString(fmt.Sprintf("  ✘ %s\n", f.Path))
		b.WriteString(fmt.Sprintf("     %s\n", f.Reason))
	}
	b.WriteString("\n")
	b.WriteString("Fix each with `mus h5ad check <FILE>` (cheap on a cached pass; ~30s on a fresh inspection).\n")
	b.WriteString("Or re-run with --force-h5ad-upload to bypass the gate.\n")
	return errors.New(b.String())
}

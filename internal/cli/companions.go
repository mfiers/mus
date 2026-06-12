package cli

// Helpers for finding the depositor-side companion files that
// travel with a primary data file:
//
//   <foo>.h5ad.md        per-file prose description (siblings)
//   <foo>.h5ad.mus       the sidecar (sibling)
//   index.md             folder-level prose (one per containing dir)
//   mus.cfg              folder-level mus config (one per containing dir)
//
// We upload these to both S3 and iRODS alongside the primary file,
// so the catalog is self-describing without consulting the
// depositor's tree.

import (
	"os"
	"path/filepath"

	"codeberg.org/mfiers/mus/internal/sidecar"
)

// companionUpload pairs a local path with the basename it should
// land at on the remote (S3 key or iRODS basename).
type companionUpload struct {
	LocalPath  string
	RemoteName string
}

// companionsForFile returns every file the upload paths should
// push alongside `primary` (a data file like foo.h5ad). The
// returned list does NOT include the primary itself.
//
// The set is deduplicated per containing folder: if the caller
// loops over multiple primary files in the same directory, the
// folder-level companions (mus.cfg, index.md) appear in the
// returned slice for each call — caller should track its own
// "already uploaded this invocation" set to avoid re-uploads
// when an upload tool isn't already idempotent.
//
// File-level companions (the sidecar and basename.md) are
// resolved per-primary.
func companionsForFile(primary string) []companionUpload {
	abs, err := filepath.Abs(primary)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(abs)
	out := make([]companionUpload, 0, 4)

	// 1) Sidecar (foo.h5ad.mus) — should always be there for an
	//    h5ad-gated upload; emit if present anyway.
	if sc := sidecar.SidecarPath(abs); fileExists(sc) {
		out = append(out, companionUpload{
			LocalPath:  sc,
			RemoteName: filepath.Base(sc),
		})
	}

	// 2) Per-file markdown (foo.h5ad.md) — depositor's prose.
	if md := abs + ".md"; fileExists(md) {
		out = append(out, companionUpload{
			LocalPath:  md,
			RemoteName: filepath.Base(md),
		})
	}

	// 3) Folder's own mus.cfg — only the deepest one (the file's
	//    immediate container). Parent cascade levels are NOT
	//    uploaded; they belong to the depositor's tree, not to
	//    this deposit's documentation layer.
	if cfg := filepath.Join(dir, "mus.cfg"); fileExists(cfg) {
		out = append(out, companionUpload{
			LocalPath:  cfg,
			RemoteName: "mus.cfg",
		})
	}

	// 4) Folder's index.md.
	if idx := filepath.Join(dir, "index.md"); fileExists(idx) {
		out = append(out, companionUpload{
			LocalPath:  idx,
			RemoteName: "index.md",
		})
	}

	return out
}

// folderLevelCompanionKeys returns the remote basenames that are
// shared across multiple primaries in the same folder. Callers use
// this to dedupe — e.g. uploading 10 .h5ad files from one folder
// shouldn't re-PUT the same mus.cfg + index.md ten times.
func folderLevelCompanionKeys() []string {
	return []string{"mus.cfg", "index.md"}
}

// isFolderLevel reports whether the given companion remote-name
// is one of the shared, folder-level companions.
func isFolderLevel(remoteName string) bool {
	for _, k := range folderLevelCompanionKeys() {
		if remoteName == k {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}


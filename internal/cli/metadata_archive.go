package cli

// Bundles the depositor-side documentation files (*.h5ad.mus,
// *.h5ad.md, folder mus.cfg, folder index.md) into a single
// gzipped tar archive per source folder per `mus … upload`
// invocation. Both `mus s3 upload` and `mus irods upload` (file
// mode) upload that ONE archive instead of pushing the metadata
// files individually — keeps the destination collection tidy and
// makes the documentation a single round-trip restore.
//
// Archive name: mus_metadata_<UTC-timestamp>.tar.gz. Timestamp is
// generated once per `mus … upload` invocation so a batch of files
// from the same source folder shares one archive name.
//
// Archive contents (flat, no nested directories):
//
//   mus.cfg                      (if present in source folder)
//   index.md                     (if present in source folder)
//   <basename>.h5ad.mus          (one per primary uploaded from this folder)
//   <basename>.h5ad.md           (one per primary, if present)
//
// Cell metadata still lives on the iRODS object as AVU — the
// archive does NOT replace the AVU layer, it complements it with
// the human-readable docs.

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/sidecar"
)

// requireSingleSourceFolder returns an error if `args` resolve to
// more than one distinct source folder. Each source folder produces
// its own `mus_metadata_<stamp>.tar.gz` archive with an identical
// basename inside one `mus … upload` invocation. They'd collide on
// the remote (same key under the same data_project prefix on S3,
// same name in the same iRODS collection), with the second upload
// silently overwriting the first. Refusing up-front avoids the
// silent data loss — caller runs the upload once per source folder.
func requireSingleSourceFolder(args []string) error {
	folders := map[string]bool{}
	var examples []string
	for _, a := range args {
		abs, err := filepath.Abs(a)
		if err != nil {
			return fmt.Errorf("resolve path %s: %w", a, err)
		}
		dir := filepath.Dir(abs)
		if folders[dir] {
			continue
		}
		folders[dir] = true
		examples = append(examples, dir)
	}
	if len(folders) <= 1 {
		return nil
	}
	sort.Strings(examples)
	return fmt.Errorf(
		"upload spans %d source folders — refusing.\n"+
			"  The per-folder metadata archive (mus_metadata_<stamp>.tar.gz) would\n"+
			"  share a basename between folders and clobber on the remote.\n"+
			"  Re-run `mus ... upload` once per source folder.\n"+
			"  folders seen:\n    %s",
		len(folders), strings.Join(examples, "\n    "))
}

// MetadataArchiveName returns the canonical filename for the
// metadata archive a single `mus … upload` invocation produces.
// Caller picks one timestamp at the start of the invocation and
// passes it here for every folder so multiple archives in one
// batch share a stamp (handy for grouping in a remote listing).
func MetadataArchiveName(stamp time.Time) string {
	return fmt.Sprintf("mus_metadata_%s.tar.gz",
		stamp.UTC().Format("2006-01-02T15-04-05Z"))
}

// buildMetadataArchive collects every metadata file for `primaries`
// — which MUST all live in the same source folder — and writes a
// gzipped tar to a fresh temp path. Returns the local archive path,
// a cleanup func (caller must call after upload), and an error.
//
// Folder-level companions (mus.cfg, index.md) are included once.
// Per-file companions (sidecar, sibling .md) are included for each
// primary. Missing optional files are silently skipped — the
// archive carries whatever exists at archive time.
//
// Returns ("", noop, nil) when the archive would be empty (no
// metadata files anywhere). Caller treats that as "nothing to
// upload" rather than uploading an empty archive.
func buildMetadataArchive(folder string, primaries []string, stamp time.Time) (string, func(), error) {
	files := collectMetadataFiles(folder, primaries)
	if len(files) == 0 {
		return "", func() {}, nil
	}

	tmpDir, err := os.MkdirTemp("", "mus-metadata-archive-")
	if err != nil {
		return "", func() {}, fmt.Errorf("temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	archivePath := filepath.Join(tmpDir, MetadataArchiveName(stamp))
	if err := writeTarGz(archivePath, files); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return archivePath, cleanup, nil
}

// archiveEntry is one file to embed in the tar. `RemoteName` is
// the entry name inside the tar; `LocalPath` is where to read the
// bytes from.
type archiveEntry struct {
	RemoteName string
	LocalPath  string
}

// fileExists reports whether `p` is an existing regular file.
// Local helper so we don't depend on whatever the rest of the
// package picks for the same check.
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// collectMetadataFiles returns the deterministic set of metadata
// files to put in the per-folder archive, sorted for reproducible
// archive bytes (helps debugging diffs).
func collectMetadataFiles(folder string, primaries []string) []archiveEntry {
	seen := map[string]bool{}
	var out []archiveEntry

	add := func(localPath, remoteName string) {
		if !fileExists(localPath) {
			return
		}
		if seen[remoteName] {
			return
		}
		seen[remoteName] = true
		out = append(out, archiveEntry{RemoteName: remoteName, LocalPath: localPath})
	}

	// Folder-level companions (once).
	add(filepath.Join(folder, "mus.cfg"), "mus.cfg")
	add(filepath.Join(folder, "index.md"), "index.md")

	// Per-primary companions (sidecar + sibling .md).
	for _, p := range primaries {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		base := filepath.Base(abs)
		add(sidecar.SidecarPath(abs), base+".mus")
		add(abs+".md", base+".md")
	}

	sort.Slice(out, func(i, j int) bool { return out[i].RemoteName < out[j].RemoteName })
	return out
}

// writeTarGz creates path as a gzipped tar containing `entries`.
// File permissions are normalised to 0o644 inside the archive —
// the original perms are irrelevant once these files travel to
// iRODS or S3 (where the object permission model is different).
func writeTarGz(path string, entries []archiveEntry) (retErr error) {
	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
	}()

	gz := gzip.NewWriter(out)
	defer func() {
		if cerr := gz.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
	}()

	tw := tar.NewWriter(gz)
	defer func() {
		if cerr := tw.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
	}()

	now := time.Now().UTC()
	for _, e := range entries {
		st, err := os.Stat(e.LocalPath)
		if err != nil {
			return fmt.Errorf("stat %s: %w", e.LocalPath, err)
		}
		hdr := &tar.Header{
			Name:    e.RemoteName,
			Mode:    0o644,
			Size:    st.Size(),
			ModTime: now, // archive-time, not source mtime — keeps reproducibility within an invocation
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("tar header for %s: %w", e.RemoteName, err)
		}
		f, err := os.Open(e.LocalPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", e.LocalPath, err)
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			return fmt.Errorf("copy %s: %w", e.LocalPath, err)
		}
		f.Close()
	}
	return nil
}

// groupPrimariesByFolder returns primaries bucketed by their
// containing absolute folder path. Used by upload paths so each
// distinct source folder gets its own metadata archive.
func groupPrimariesByFolder(primaries []string) map[string][]string {
	out := map[string][]string{}
	for _, p := range primaries {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		dir := filepath.Dir(abs)
		out[dir] = append(out[dir], abs)
	}
	return out
}

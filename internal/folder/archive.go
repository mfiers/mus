package folder

// tar.gz packaging for the "many small files → bundle and upload" path.
//
// Produces an archive whose entries are relative to the source folder, so
// unpacking on the other side recreates the original layout (`scripts/...`)
// without the absolute path of the source machine baked in.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// ArchiveResult is what TarGzip returns about the produced archive.
type ArchiveResult struct {
	Path   string // absolute path of the archive on disk
	Size   int64
	Sha256 string
}

// TarGzip walks srcDir and packs every regular file into a gzip-compressed
// tarball at destPath. Entries inside the tar are relative to srcDir's
// parent, so e.g. archiving /home/foo/scripts/ produces entries like
// scripts/a.py, scripts/sub/b.py, ...
//
// Returns the size in bytes + sha256 of the resulting archive (computed
// while writing — single pass).
//
// Skips symlinks. Directories are emitted as tar headers (so empty subdirs
// survive a round-trip).
func TarGzip(srcDir, destPath string) (*ArchiveResult, error) {
	srcAbs, err := filepath.Abs(srcDir)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(srcAbs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", srcAbs)
	}
	parent := filepath.Dir(srcAbs) // entries are relative to here

	out, err := os.Create(destPath)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	// Write through a sha256-and-tee pipeline so the archive's sha256 is
	// available without re-reading the file.
	hasher := sha256.New()
	mw := io.MultiWriter(out, hasher)

	gz := gzip.NewWriter(mw)
	tw := tar.NewWriter(gz)

	// Walk and collect first so the archive's file order is stable
	// (sorted by path) — useful for reproducibility.
	type entry struct {
		Abs  string
		Rel  string
		Info fs.FileInfo
	}
	var entries []entry
	err = filepath.WalkDir(srcAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // skip symlinks; iRODS / IRON don't preserve them either
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(parent, p)
		if err != nil {
			return err
		}
		// Forward slashes inside tar entries (portable).
		rel = filepath.ToSlash(rel)
		entries = append(entries, entry{Abs: p, Rel: rel, Info: info})
		return nil
	})
	if err != nil {
		tw.Close()
		gz.Close()
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Rel < entries[j].Rel })

	for _, e := range entries {
		hdr, err := tar.FileInfoHeader(e.Info, "")
		if err != nil {
			return nil, err
		}
		hdr.Name = e.Rel
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if !e.Info.Mode().IsRegular() {
			continue
		}
		in, err := os.Open(e.Abs)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(tw, in); err != nil {
			in.Close()
			return nil, err
		}
		in.Close()
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if err := out.Sync(); err != nil {
		return nil, err
	}

	finalSt, err := os.Stat(destPath)
	if err != nil {
		return nil, err
	}
	return &ArchiveResult{
		Path:   destPath,
		Size:   finalSt.Size(),
		Sha256: hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

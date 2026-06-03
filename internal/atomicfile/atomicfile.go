// Package atomicfile implements crash-safe file replacement.
//
// The textbook temp+rename pattern (CreateTemp, Write, Rename) leaves
// two durability gaps on a system that crashes between operations:
//
//  1. The temp file's bytes may still sit in the kernel page cache
//     unflushed to disk. After the rename, the inode points at the
//     new name but its content can be lost on a crash.
//  2. The directory entry change from `rename` is itself a metadata
//     update on the parent directory. Without an explicit fsync on
//     the parent dir, the rename can be lost while the old file
//     remains visible — a torn write across the rename boundary.
//
// Replace closes both gaps: it Syncs the temp file's data before
// rename, then opens and Syncs the parent directory after rename.
// This is what e.g. SQLite, etcd, and the Go module download cache
// all do for the same reason.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Replace writes `data` to `targetPath` atomically and durably.
//
// Atomicity: target is updated by os.Rename, which on POSIX is atomic
// within a single filesystem.
//
// Durability: the temp file's content is fsynced before rename; the
// parent directory is fsynced after rename, so the directory entry
// change survives crashes.
//
// On error the temp file is removed (best-effort) so the directory
// doesn't accumulate `.tmp.*` debris from failed writes.
//
// `mode` controls the final file permissions (e.g. 0o644).
func Replace(targetPath string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, filepath.Base(targetPath)+".tmp.*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// On any error past this point, remove the temp file so it
	// doesn't litter the directory.
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("atomicfile: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("atomicfile: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicfile: close temp: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("atomicfile: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("atomicfile: rename: %w", err)
	}
	cleanup = false // rename succeeded — temp is gone

	// Fsync the parent dir so the directory entry change survives.
	// Errors here aren't fatal for the user — the rename already
	// took effect; we just have weaker durability on this one file.
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

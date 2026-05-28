// Package folder provides folder analysis and packaging helpers for the
// `mus irods upload <folder>` workflow.
//
// Three pieces:
//
//  1. Profile(path) — walk a directory, return file count + total bytes
//     + median file size. Cheap (stat only, no hashing).
//
//  2. DensityOK(profile, minDensity, minCount) — the heuristic that
//     decides whether direct upload is OK. The rule is:
//
//     allow if  N < minCount   (few files → always allowed)
//     else      median_bytes / N² ≥ minDensity
//
//     Defaults: minCount=20, minDensity=10. So a 5000-file folder with
//     median 50 KB has 51200/25e6 = 0.002, which is below 10 → not OK.
//     A 100-file folder with median 100 MB has 1e8/1e4 = 1e4 → OK.
//
//  3. RecursiveSHA256(path, sum) — Merkle-style hash of (relpath, sha256)
//     lines sorted by relpath. Used by the folder sidecar so `mus check
//     folder.mus` can detect drift without per-file sidecars.
//
// TarGzip + the IRON shellout live in cli/irods.go, not here, because they
// need the iron client.
package folder

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Profile is the cheap summary of a folder produced by walking + stat.
type Profile struct {
	Path       string  // absolute path of the folder
	FileCount  int64   // total number of regular files (recursive)
	TotalBytes int64   // sum of sizes
	MedianSize int64   // median file size in bytes; 0 if FileCount == 0
	Density    float64 // MedianSize / FileCount²; +Inf if FileCount == 0
}

// Profile walks path and returns a Profile. Skips symlinks (does not follow,
// does not count them as files) and skips the *.mus sidecar files themselves
// to avoid the chicken-and-egg of a sidecar describing the folder counting
// itself.
func ProfileFolder(path string) (*Profile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", abs)
	}

	var sizes []int64
	var total int64
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // skip symlinks
		}
		// Don't count the folder's own sidecar (if any) — keeps profile stable.
		base := filepath.Base(p)
		if len(base) > 4 && base[len(base)-4:] == ".mus" {
			// Only skip the sidecar that sits at the FOLDER's level (sibling),
			// not arbitrary *.mus inside subfolders. WalkDir descends into the
			// folder we were given; the folder-level sidecar is at the parent,
			// so we don't actually encounter it here. But if someone has put
			// a *.mus file as DATA inside their folder, we DO want to count it.
			// Leaving this branch as a no-op for now; revisit if needed.
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		sizes = append(sizes, info.Size())
		total += info.Size()
		return nil
	})
	if err != nil {
		return nil, err
	}

	p := &Profile{
		Path:       abs,
		FileCount:  int64(len(sizes)),
		TotalBytes: total,
	}
	if len(sizes) == 0 {
		p.Density = posInf()
		return p, nil
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] < sizes[j] })
	p.MedianSize = median(sizes)
	p.Density = float64(p.MedianSize) / (float64(p.FileCount) * float64(p.FileCount))
	return p, nil
}

func median(sorted []int64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func posInf() float64 {
	var inf float64 = 1
	for i := 0; i < 1024; i++ {
		inf *= 2
	}
	return inf
}

// DensityOK returns nil if the folder profile clears the density check, else
// an error describing exactly what failed (so the caller can include it in a
// user-facing prompt).
//
// minCount is the lower bound for engaging the density check at all — below
// it, every folder is allowed (no point packaging a 5-file folder). Default
// 20.
//
// minDensity is the threshold for median_bytes/N². Default 10.
func DensityOK(p *Profile, minDensity float64, minCount int64) error {
	if p == nil {
		return fmt.Errorf("nil profile")
	}
	if p.FileCount < minCount {
		return nil // too few files to bother checking
	}
	if p.Density >= minDensity {
		return nil
	}
	return fmt.Errorf(
		"density check failed: median (%d B) / count² (%d²) = %.4f, need ≥ %.0f",
		p.MedianSize, p.FileCount, p.Density, minDensity)
}

// FormatHumanBytes returns a human-friendly size string (KiB, MiB, ...).
func FormatHumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Hasher is the function used to hash individual files in RecursiveSHA256.
// In production this is hashcache.Cache.Sum; tests use a stub.
type Hasher func(absPath string) (string, error)

// RecursiveSHA256 computes a Merkle-style hash of folder contents:
//
//  1. Walk the folder, collect (relpath, sha256(file)) pairs.
//  2. Sort by relpath (so the result is reproducible across runs / hosts).
//  3. For each pair, write "<sha256>  <relpath>\n" (gnu-style).
//  4. sha256 the concatenated bytes; return hex.
//
// Empty folders hash to sha256("") — the conventional empty hash.
//
// Symlinks are skipped. The folder's own sibling sidecar is irrelevant
// (it lives at the parent, not inside the folder). Subfolder paths use
// forward slashes regardless of host OS for cross-platform stability.
func RecursiveSHA256(root string, hash Hasher) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	type entry struct {
		Rel string
		Sum string
	}
	var entries []entry

	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		sum, err := hash(p)
		if err != nil {
			return fmt.Errorf("hash %s: %w", p, err)
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return err
		}
		// Forward slashes for cross-platform reproducibility.
		rel = filepath.ToSlash(rel)
		entries = append(entries, entry{Rel: rel, Sum: sum})
		return nil
	})
	if err != nil {
		return "", err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Rel < entries[j].Rel })

	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s  %s\n", e.Sum, e.Rel)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

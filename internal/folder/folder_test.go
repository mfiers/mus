package folder

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = 'x'
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProfileFolderEmpty(t *testing.T) {
	dir := t.TempDir()
	p, err := ProfileFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.FileCount != 0 || p.TotalBytes != 0 || p.MedianSize != 0 {
		t.Errorf("expected zeros, got %+v", p)
	}
	if !math.IsInf(p.Density, 1) {
		t.Errorf("empty folder density should be +Inf, got %v", p.Density)
	}
}

func TestProfileFolderBasic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a"), 100)
	writeFile(t, filepath.Join(dir, "b"), 200)
	writeFile(t, filepath.Join(dir, "c"), 300)
	writeFile(t, filepath.Join(dir, "sub", "d"), 400)

	p, err := ProfileFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.FileCount != 4 {
		t.Errorf("FileCount = %d, want 4", p.FileCount)
	}
	if p.TotalBytes != 1000 {
		t.Errorf("TotalBytes = %d, want 1000", p.TotalBytes)
	}
	// median of [100,200,300,400] = (200+300)/2 = 250
	if p.MedianSize != 250 {
		t.Errorf("MedianSize = %d, want 250", p.MedianSize)
	}
	// 250 / 16 = 15.625
	if got := p.Density; got != 250.0/16.0 {
		t.Errorf("Density = %v, want %v", got, 250.0/16.0)
	}
}

func TestDensityOK(t *testing.T) {
	cases := []struct {
		name        string
		p           *Profile
		minDensity  float64
		minCount    int64
		expectError bool
	}{
		{
			name:       "few files, always allowed",
			p:          &Profile{FileCount: 5, MedianSize: 1, Density: 0.04},
			minDensity: 10, minCount: 20,
			expectError: false,
		},
		{
			name:       "100 large files passes",
			p:          &Profile{FileCount: 100, MedianSize: 100 * 1024 * 1024, Density: 10485.76},
			minDensity: 10, minCount: 20,
			expectError: false,
		},
		{
			name:       "5000 small files fails",
			p:          &Profile{FileCount: 5000, MedianSize: 50 * 1024, Density: 0.002},
			minDensity: 10, minCount: 20,
			expectError: true,
		},
		{
			name:       "borderline 100/100KB just passes",
			p:          &Profile{FileCount: 100, MedianSize: 100 * 1024, Density: 10.24},
			minDensity: 10, minCount: 20,
			expectError: false,
		},
		{
			name:       "exactly at threshold counts as pass",
			p:          &Profile{FileCount: 100, MedianSize: 100_000, Density: 10.0},
			minDensity: 10, minCount: 20,
			expectError: false,
		},
	}
	for _, tc := range cases {
		err := DensityOK(tc.p, tc.minDensity, tc.minCount)
		if tc.expectError && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
		if !tc.expectError && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
	}
}

func TestRecursiveSHA256Stable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a"), 10)
	writeFile(t, filepath.Join(dir, "b"), 20)
	writeFile(t, filepath.Join(dir, "sub", "c"), 30)

	stub := func(path string) (string, error) {
		// Real-ish: hash the file contents.
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		h := sha256.Sum256(data)
		return hex.EncodeToString(h[:]), nil
	}

	h1, err := RecursiveSHA256(dir, stub)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := RecursiveSHA256(dir, stub)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("non-deterministic: %s vs %s", h1, h2)
	}
}

func TestRecursiveSHA256DetectsChanges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a"), 10)
	writeFile(t, filepath.Join(dir, "b"), 20)

	stub := func(path string) (string, error) {
		data, _ := os.ReadFile(path)
		h := sha256.Sum256(data)
		return hex.EncodeToString(h[:]), nil
	}

	h1, _ := RecursiveSHA256(dir, stub)

	// Add a new file
	writeFile(t, filepath.Join(dir, "c"), 30)
	h2, _ := RecursiveSHA256(dir, stub)
	if h1 == h2 {
		t.Errorf("adding a file did not change the hash")
	}

	// Remove the new file, modify an existing one
	os.Remove(filepath.Join(dir, "c"))
	writeFile(t, filepath.Join(dir, "a"), 15)
	h3, _ := RecursiveSHA256(dir, stub)
	if h1 == h3 {
		t.Errorf("modifying a file did not change the hash")
	}
}

func TestRecursiveSHA256Empty(t *testing.T) {
	dir := t.TempDir()
	stub := func(path string) (string, error) { return "shouldnt-be-called", nil }
	h, err := RecursiveSHA256(dir, stub)
	if err != nil {
		t.Fatal(err)
	}
	// sha256("") = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	if h != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("empty folder hash = %s", h)
	}
}

func TestFormatHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:                         "0 B",
		512:                       "512 B",
		1024:                      "1.0 KiB",
		1024 * 1024:               "1.0 MiB",
		int64(1024) * 1024 * 1024: "1.0 GiB",
		2_500_000_000:             "2.3 GiB",
	}
	for in, want := range cases {
		if got := FormatHumanBytes(in); got != want {
			t.Errorf("FormatHumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
	// Sanity: %v lifts to GiB / TiB without crashing.
	_ = fmt.Sprintf("%s", FormatHumanBytes(int64(5)*1024*1024*1024*1024))
}

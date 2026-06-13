package cli

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readTarGzNames returns the entry names from a gzipped tar, sorted.
func readTarGzNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
	}
	sort.Strings(names)
	return names
}

func TestMetadataArchiveName_ShapeAndStamp(t *testing.T) {
	stamp := time.Date(2026, 6, 13, 10, 12, 0, 0, time.UTC)
	got := MetadataArchiveName(stamp)
	want := "mus_metadata_2026-06-13T10-12-00Z.tar.gz"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildMetadataArchive_IncludesEveryRelevantFile(t *testing.T) {
	folder := t.TempDir()
	// Per-folder companions.
	writeTestFile(t, filepath.Join(folder, "mus.cfg"),
		"data_project=Fiers2025\norganism=human\n")
	writeTestFile(t, filepath.Join(folder, "index.md"),
		"# Test folder\nA prose description.\n")
	// Two primaries with their sidecars + per-file md.
	for _, base := range []string{"a.h5ad", "b.h5ad"} {
		writeTestFile(t, filepath.Join(folder, base), "fake h5ad")
		writeTestFile(t, filepath.Join(folder, base+".mus"), "sha256=abc\n")
		writeTestFile(t, filepath.Join(folder, base+".md"),
			"# "+base+"\nDataset description.\n")
	}
	primaries := []string{
		filepath.Join(folder, "a.h5ad"),
		filepath.Join(folder, "b.h5ad"),
	}
	stamp := time.Date(2026, 6, 13, 10, 12, 0, 0, time.UTC)
	archivePath, cleanup, err := buildMetadataArchive(folder, primaries, stamp)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if archivePath == "" {
		t.Fatal("expected an archive path; got empty")
	}
	got := readTarGzNames(t, archivePath)
	want := []string{
		"a.h5ad.md",
		"a.h5ad.mus",
		"b.h5ad.md",
		"b.h5ad.mus",
		"index.md",
		"mus.cfg",
	}
	if !equalStrings(got, want) {
		t.Errorf("entries:\n  got  %v\n  want %v", got, want)
	}
}

func TestBuildMetadataArchive_SkipsMissingOptionals(t *testing.T) {
	folder := t.TempDir()
	// Only sidecar, no md, no folder cfg/index.
	writeTestFile(t, filepath.Join(folder, "a.h5ad"), "fake")
	writeTestFile(t, filepath.Join(folder, "a.h5ad.mus"), "sha256=abc\n")
	primaries := []string{filepath.Join(folder, "a.h5ad")}
	archivePath, cleanup, err := buildMetadataArchive(folder, primaries, time.Now())
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	got := readTarGzNames(t, archivePath)
	want := []string{"a.h5ad.mus"}
	if !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBuildMetadataArchive_EmptyReturnsNoPath(t *testing.T) {
	folder := t.TempDir()
	// Primary exists but no sidecar, no companions.
	writeTestFile(t, filepath.Join(folder, "a.h5ad"), "fake")
	primaries := []string{filepath.Join(folder, "a.h5ad")}
	archivePath, cleanup, err := buildMetadataArchive(folder, primaries, time.Now())
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if archivePath != "" {
		t.Errorf("expected empty archive path; got %q", archivePath)
	}
}

func TestBuildMetadataArchive_FolderLevelOnceAcrossPrimaries(t *testing.T) {
	folder := t.TempDir()
	writeTestFile(t, filepath.Join(folder, "mus.cfg"), "data_project=X2025\n")
	// Three primaries, each with sidecar — mus.cfg must appear ONCE.
	for _, base := range []string{"a.h5ad", "b.h5ad", "c.h5ad"} {
		writeTestFile(t, filepath.Join(folder, base), "x")
		writeTestFile(t, filepath.Join(folder, base+".mus"), "sha256=abc\n")
	}
	primaries := []string{
		filepath.Join(folder, "a.h5ad"),
		filepath.Join(folder, "b.h5ad"),
		filepath.Join(folder, "c.h5ad"),
	}
	archivePath, cleanup, err := buildMetadataArchive(folder, primaries, time.Now())
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	names := readTarGzNames(t, archivePath)
	count := 0
	for _, n := range names {
		if n == "mus.cfg" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("mus.cfg appeared %d times, want 1; entries=%v", count, names)
	}
}

func TestGroupPrimariesByFolder(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	a := filepath.Join(dir1, "a.h5ad")
	b := filepath.Join(dir1, "b.h5ad")
	c := filepath.Join(dir2, "c.h5ad")
	for _, p := range []string{a, b, c} {
		writeTestFile(t, p, "x")
	}
	got := groupPrimariesByFolder([]string{a, b, c})
	if len(got) != 2 {
		t.Errorf("expected 2 folders, got %d (%v)", len(got), got)
	}
	if len(got[dir1]) != 2 || len(got[dir2]) != 1 {
		t.Errorf("folder grouping wrong: %v", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// _ keeps the strings import alive for any future test that adds
// a string-shape assertion.
var _ = strings.Contains

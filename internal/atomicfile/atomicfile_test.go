package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplace_CreatesFileWithContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data.txt")
	if err := Replace(target, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want hello", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 0644", info.Mode().Perm())
	}
}

func TestReplace_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Replace(target, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new content" {
		t.Errorf("got %q, want new content", got)
	}
}

func TestReplace_LeavesNoTempOnSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data.txt")
	if err := Replace(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp.") {
			t.Errorf("leftover temp file: %s", entry.Name())
		}
	}
}

func TestReplace_LeavesNoTempOnFailure(t *testing.T) {
	dir := t.TempDir()
	// target points into a non-existent subdirectory — rename will
	// fail and we should clean up the temp file (which gets created
	// in the parent of the failing target).
	if err := Replace(filepath.Join(dir, "nope", "data.txt"), []byte("x"), 0o644); err == nil {
		t.Fatal("expected error for missing parent dir")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp.") {
			t.Errorf("leftover temp file on failure: %s", entry.Name())
		}
	}
}

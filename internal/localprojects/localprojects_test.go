package localprojects

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStoreInDir(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "local_projects.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAll_MissingFileIsEmpty(t *testing.T) {
	store := newStoreInDir(t)
	got, err := store.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map; got %v", got)
	}
}

func TestRecord_PersistsAndOverwrites(t *testing.T) {
	store := newStoreInDir(t)
	if err := store.Record("Fiers2025", "/tmp/a"); err != nil {
		t.Fatal(err)
	}
	got, err := store.All()
	if err != nil {
		t.Fatal(err)
	}
	if got["Fiers2025"].Folder != "/tmp/a" {
		t.Errorf("first folder: got %q", got["Fiers2025"].Folder)
	}
	if got["Fiers2025"].LastUsed.IsZero() {
		t.Error("LastUsed not stamped")
	}

	// Sleep just enough for a different timestamp, then overwrite.
	time.Sleep(10 * time.Millisecond)
	if err := store.Record("Fiers2025", "/tmp/b"); err != nil {
		t.Fatal(err)
	}
	got, _ = store.All()
	if got["Fiers2025"].Folder != "/tmp/b" {
		t.Errorf("overwrite folder: got %q", got["Fiers2025"].Folder)
	}
}

func TestRecord_NormalisesAbsolute(t *testing.T) {
	store := newStoreInDir(t)
	dir := t.TempDir()
	// Use a relative path that exists.
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := store.Record("X2025", "."); err != nil {
		t.Fatal(err)
	}
	got, _ := store.All()
	if !filepath.IsAbs(got["X2025"].Folder) {
		t.Errorf("folder not absolute: %q", got["X2025"].Folder)
	}
}

func TestRecord_RejectsEmptyProject(t *testing.T) {
	store := newStoreInDir(t)
	if err := store.Record("", "/tmp"); err == nil {
		t.Error("expected error for empty project name")
	}
}

func TestRecordBestEffort_SwallowsErrors(t *testing.T) {
	// Nil store must not panic.
	var s *Store
	s.RecordBestEffort("Fiers2025", "/tmp")
}

func TestDefaultPath_HonoursXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/xdg")
	got := DefaultPath()
	if !strings.HasSuffix(got, "/custom/xdg/mus/local_projects.json") {
		t.Errorf("XDG override ignored: %q", got)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/test")
	got = DefaultPath()
	if !strings.HasSuffix(got, "/home/test/.local/share/mus/local_projects.json") {
		t.Errorf("home fallback wrong: %q", got)
	}
}

func TestAll_ReturnsCopy(t *testing.T) {
	store := newStoreInDir(t)
	if err := store.Record("Fiers2025", "/tmp"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.All()
	got["Fiers2025"] = Entry{Folder: "TAMPERED"}
	// Re-read; should be the original, not the tampered copy.
	got2, _ := store.All()
	if got2["Fiers2025"].Folder == "TAMPERED" {
		t.Error("All() must return a copy; original was mutated by caller")
	}
}

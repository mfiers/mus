package elnmap

import (
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mappings.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestRememberAndLookup(t *testing.T) {
	s, _ := newStore(t)

	got, err := s.Lookup("1234")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("Lookup empty store returned %+v, want nil", got)
	}

	if err := s.Remember("1234", "Fiers2025", "Single-cell pilot"); err != nil {
		t.Fatal(err)
	}
	got, err = s.Lookup("1234")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("Lookup after Remember = nil")
	}
	if got.DataProject != "Fiers2025" || got.ExperimentName != "Single-cell pilot" {
		t.Errorf("got %+v", got)
	}
	if got.SetAt.IsZero() {
		t.Errorf("SetAt not populated")
	}
}

func TestRememberOverwrites(t *testing.T) {
	s, _ := newStore(t)
	_ = s.Remember("1", "Old2024", "old")
	if err := s.Remember("1", "New2025", "new"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Lookup("1")
	if got.DataProject != "New2025" {
		t.Errorf("not overwritten: %+v", got)
	}
}

func TestRememberRejectsEmpty(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Remember("", "Foo2025", ""); err == nil {
		t.Errorf("accepted empty experiment ID")
	}
	if err := s.Remember("1", "", ""); err == nil {
		t.Errorf("accepted empty data_project")
	}
}

func TestForget(t *testing.T) {
	s, _ := newStore(t)
	_ = s.Remember("1", "Foo2025", "")
	if err := s.Forget("1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Lookup("1")
	if got != nil {
		t.Errorf("Lookup after Forget = %+v, want nil", got)
	}
	// Forget on a missing ID should not error.
	if err := s.Forget("nonexistent"); err != nil {
		t.Errorf("Forget unknown ID errored: %v", err)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	s, path := newStore(t)
	_ = s.Remember("9", "Persisted2025", "remember me")

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s2.Lookup("9")
	if got == nil || got.DataProject != "Persisted2025" {
		t.Errorf("did not persist: got %+v", got)
	}
}

func TestEmptyFileTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mappings.json")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.All()) != 0 {
		t.Errorf("empty file should produce empty store")
	}
}

func TestFilePermissionsOnWrite(t *testing.T) {
	s, path := newStore(t)
	_ = s.Remember("1", "Foo2025", "")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Atomic-write produces a fresh file via Chmod 0600 before Rename; final
	// file should be 0600.
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", st.Mode().Perm())
	}
}

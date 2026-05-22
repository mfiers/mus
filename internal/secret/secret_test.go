package secret

import (
	"os"
	"sort"
	"testing"
)

func TestAgeBackendRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MUS_CONFIG_DIR", dir)

	s, err := openAge()
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != "age" {
		t.Fatalf("backend = %s", s.Backend())
	}
	if err := s.Set("eln_apikey", "abc123"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("irods_home", "/zone/home/lab"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("eln_apikey")
	if err != nil || got != "abc123" {
		t.Errorf("Get eln_apikey = %q, %v", got, err)
	}
	if _, err := s.Get("missing"); err != ErrNotFound {
		t.Errorf("missing key: err = %v", err)
	}
	names, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "eln_apikey" || names[1] != "irods_home" {
		t.Errorf("List = %v", names)
	}
	if err := s.Delete("eln_apikey"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("eln_apikey"); err != ErrNotFound {
		t.Errorf("after delete, err = %v", err)
	}

	// reopen — key should persist via the on-disk key file
	s2, err := openAge()
	if err != nil {
		t.Fatal(err)
	}
	got, err = s2.Get("irods_home")
	if err != nil || got != "/zone/home/lab" {
		t.Errorf("after reopen: got %q err %v", got, err)
	}
	// confirm permissions on the key file
	st, err := os.Stat(dir + "/secrets.key")
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("secrets.key perms = %o, want 0600", st.Mode().Perm())
	}
}

func TestAgeRejectInvalid(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MUS_CONFIG_DIR", dir)
	s, err := openAge()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("bad\nname", "x"); err == nil {
		t.Errorf("expected error for newline in name")
	}
	if err := s.Set("good", "value\nwith\nnewlines"); err == nil {
		t.Errorf("expected error for newline in value")
	}
}

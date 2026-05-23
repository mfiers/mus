package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCascadeOverride(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".env"), "irods_home=/zone/home/lab\n")
	sub := filepath.Join(root, "project", "study")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "project", ".env"),
		"irods_home=/zone/home/lab/project\n")
	writeFile(t, filepath.Join(sub, ".env"),
		"eln_experiment_id=E123\n")

	env, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := env.String("irods_home"), "/zone/home/lab/project"; got != want {
		t.Errorf("irods_home = %q, want %q", got, want)
	}
	if got, want := env.String("eln_experiment_id"), "E123"; got != want {
		t.Errorf("eln_experiment_id = %q, want %q", got, want)
	}
}

func TestListMerging(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".env"), "tag=alpha,beta\n")
	sub := filepath.Join(root, "deeper")
	writeFile(t, filepath.Join(sub, ".env"), "tag=gamma,-alpha\n")

	env, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	got := env.List("tag")
	sort.Strings(got)
	want := []string{"beta", "gamma"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tag = %v, want %v", got, want)
	}
}

func TestSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, map[string]string{
		"eln_experiment_id": "E999",
		"irods_home":        "/zone/home/x",
	}); err != nil {
		t.Fatal(err)
	}
	env, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if env.String("eln_experiment_id") != "E999" {
		t.Errorf("got %q", env.String("eln_experiment_id"))
	}
	if env.String("irods_home") != "/zone/home/x" {
		t.Errorf("got %q", env.String("irods_home"))
	}
}

func TestSaveAppendsToList(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "tag=existing\n")
	if err := Save(dir, map[string]string{"tag": "added"}); err != nil {
		t.Fatal(err)
	}
	env, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := env.List("tag")
	sort.Strings(got)
	want := []string{"added", "existing"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tag = %v, want %v", got, want)
	}
}

func TestLoadLocalMissingFile(t *testing.T) {
	dir := t.TempDir()
	env, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if env.Has("anything") {
		t.Errorf("empty env should have no keys")
	}
}

func TestCommentsIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"),
		"# a comment\nirods_home=/zone/home\n\n# trailing\n")
	env, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if env.String("irods_home") != "/zone/home" {
		t.Errorf("got %q", env.String("irods_home"))
	}
}

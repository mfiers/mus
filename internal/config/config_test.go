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
	writeFile(t, filepath.Join(root, ".mus"), `irods_home = "/zone/home/lab"`+"\n")
	sub := filepath.Join(root, "project", "study")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "project", ".mus"),
		`irods_home = "/zone/home/lab/project"`+"\n")
	writeFile(t, filepath.Join(sub, ".mus"),
		"[eln]\nexperiment_id = \"E123\"\n")

	env, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := env.String("irods_home"), "/zone/home/lab/project"; got != want {
		t.Errorf("irods_home = %q, want %q", got, want)
	}
	if got, want := env.String("eln.experiment_id"), "E123"; got != want {
		t.Errorf("eln.experiment_id = %q, want %q", got, want)
	}
}

func TestListMerging(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".mus"),
		`tag = ["alpha", "beta"]`+"\n")
	sub := filepath.Join(root, "deeper")
	writeFile(t, filepath.Join(sub, ".mus"),
		`tag = ["gamma", "-alpha"]`+"\n")

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
		"eln.experiment_id": "E999",
		"irods_home":        "/zone/home/x",
	}); err != nil {
		t.Fatal(err)
	}
	env, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if env.String("eln.experiment_id") != "E999" {
		t.Errorf("got %q", env.String("eln.experiment_id"))
	}
	if env.String("irods_home") != "/zone/home/x" {
		t.Errorf("got %q", env.String("irods_home"))
	}
}

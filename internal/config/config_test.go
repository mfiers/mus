package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

func TestSaveReplacesListKey(t *testing.T) {
	// `mus config set` uses shell-style set semantics: a new value for
	// a list-valued key REPLACES the existing list, not appends to it.
	// (Older mus appended; this is the behaviour change in 0.2.6.)
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
	want := []string{"added"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tag = %v, want %v (REPLACE semantics)", got, want)
	}
}

func TestSaveListKeyParsesCommaSeparated(t *testing.T) {
	// A comma-separated string passed to Save for a list-key should be
	// split into items, not stored as one quoted blob.
	dir := t.TempDir()
	if err := Save(dir, map[string]string{"tag": "a,b,c"}); err != nil {
		t.Fatal(err)
	}
	env, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := env.List("tag")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tag = %v, want %v", got, want)
	}
}

func TestSaveListKeyOtherKeysSurvive(t *testing.T) {
	// Replacing one list-key does NOT clear unrelated keys.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"),
		"tag=old1,old2\nirods_home=/some/place\norganism=human\n")
	if err := Save(dir, map[string]string{"tag": "new"}); err != nil {
		t.Fatal(err)
	}
	env, _ := LoadLocal(dir)
	if !reflect.DeepEqual(env.List("tag"), []string{"new"}) {
		t.Errorf("tag = %v, want [new]", env.List("tag"))
	}
	if env.String("irods_home") != "/some/place" {
		t.Errorf("irods_home lost: %q", env.String("irods_home"))
	}
	if env.String("organism") != "human" {
		t.Errorf("organism lost: %q", env.String("organism"))
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

func TestUnsetRemovesKey(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, map[string]string{
		"eln_disabled": "true",
		"irods_home":   "/zone/home/x",
	}); err != nil {
		t.Fatal(err)
	}
	if err := Unset(dir, "eln_disabled"); err != nil {
		t.Fatal(err)
	}
	env, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if env.Has("eln_disabled") {
		t.Errorf("expected eln_disabled removed, got: %v", env.All())
	}
	if env.String("irods_home") != "/zone/home/x" {
		t.Errorf("other keys disturbed: %v", env.All())
	}
}

func TestUnsetMissingFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := Unset(dir, "eln_disabled"); err != nil {
		t.Errorf("Unset on missing .env should be no-op, got: %v", err)
	}
}

func TestUnsetEmptiesAndRemovesFile(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, map[string]string{"eln_disabled": "true"}); err != nil {
		t.Fatal(err)
	}
	if err := Unset(dir, "eln_disabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Errorf("expected .env removed once empty, stat err = %v", err)
	}
}

func TestUnsetPreservesCommentsWhenLastKeyRemoved(t *testing.T) {
	// User has hand-edited a .env with a comment block. Unset must
	// not delete the file just because it would otherwise be
	// keyless — the comment is documentation the user typed.
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	contents := "# project notes\n# important context\neln_disabled=true\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Unset(dir, "eln_disabled"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file deleted despite surviving comments: %v", err)
	}
	if !strings.Contains(string(got), "project notes") ||
		!strings.Contains(string(got), "important context") {
		t.Errorf("comments lost on Unset: %q", got)
	}
	if strings.Contains(string(got), "eln_disabled") {
		t.Errorf("key not removed: %q", got)
	}
}

func TestUnsetPreservesCommentsWhenOtherKeysRemain(t *testing.T) {
	// A keys-and-comments file. Unset removes the key but must
	// preserve the comments in the file body.
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	contents := "# header\neln_disabled=true\nirods_home=/zone/home/x\n# trailer\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Unset(dir, "eln_disabled"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# header", "# trailer", "irods_home=/zone/home/x"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %q after Unset:\n%s", want, got)
		}
	}
	if strings.Contains(string(got), "eln_disabled") {
		t.Errorf("key still present: %q", got)
	}
}

func TestUnsetKeepsWhitespaceOnlyAsEmpty(t *testing.T) {
	// Pure-whitespace remainder (no comments, no keys) → delete.
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("\n\neln_disabled=true\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Unset(dir, "eln_disabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("whitespace-only file should be deleted; stat err=%v", err)
	}
}

func TestStripKeyLines_HandlesMultipleAssignments(t *testing.T) {
	// List-valued keys can appear multiple times. All assignments
	// to the unset key must be removed; other keys untouched.
	in := "# header\ntag=a\nname=alice\ntag=b,c\n# end\n"
	got, removed := stripKeyLines(in, "tag")
	if !removed {
		t.Error("expected removed=true")
	}
	for _, want := range []string{"# header", "name=alice", "# end"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "tag=") {
		t.Errorf("not all tag lines removed:\n%s", got)
	}
}

func TestGlobalCascadeInjection(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	xdg := filepath.Join(tmp, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)

	gp := GlobalPath()
	want := filepath.Join(xdg, "mus", ".env")
	if gp != want {
		t.Fatalf("GlobalPath = %q, want %q", gp, want)
	}
	if err := SaveToFile(gp, map[string]string{"eln_disabled": "true"}); err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(tmp, "isolated", "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	env, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if env.String("eln_disabled") != "true" {
		t.Errorf("global eln_disabled not surfaced; values=%v", env.All())
	}
}

func TestSaveToFileCreatesParent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "deep", "deeper", ".env")
	if err := SaveToFile(path, map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
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

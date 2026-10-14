package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// qdbTestCmd returns a bare cobra command carrying buffers for IO, so
// runQDB can be driven without the full root wiring.
func qdbTestCmd() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	return cmd, &out
}

// writeFile is a tiny helper for seeding a temp folder.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// All of these must FAIL before any ELN/network call. They assert qdb's
// local preconditions, which run ahead of openELN().

// baseOpts returns a valid-ish opts set; tests tweak one field at a time.
func baseOpts() qdbOpts {
	return qdbOpts{
		studyID:     qdbDefaultStudyID,
		maxSizeGB:   qdbDefaultMaxSizeGB,
		dataProject: "OldPilot2026",
		noExclude:   true, // tests exercising other gates skip the cruft prompt
	}
}

func TestQDB_RefusesMissingDataProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "data")
	t.Chdir(dir)
	cmd, _ := qdbTestCmd()
	o := baseOpts()
	o.dataProject = ""
	err := runQDB(cmd, "old pilot", o)
	if err == nil || !strings.Contains(err.Error(), "data-project") {
		t.Fatalf("want data-project-required error, got: %v", err)
	}
}

func TestQDB_RefusesInvalidDataProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "data")
	t.Chdir(dir)
	cmd, _ := qdbTestCmd()
	o := baseOpts()
	o.dataProject = "../bad/name"
	err := runQDB(cmd, "old pilot", o)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("want invalid-data_project error, got: %v", err)
	}
}

func TestQDB_RefusesAlreadyLinked(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "data")
	writeFile(t, filepath.Join(dir, "mus.cfg"), "eln_experiment_id=12345\n")
	t.Chdir(dir)
	cmd, _ := qdbTestCmd()
	err := runQDB(cmd, "old pilot", baseOpts())
	if err == nil || !strings.Contains(err.Error(), "already linked") {
		t.Fatalf("want already-linked refusal, got: %v", err)
	}
}

func TestQDB_RefusesExistingDataProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "data")
	writeFile(t, filepath.Join(dir, "mus.cfg"), "data_project=Existing2025\n")
	t.Chdir(dir)
	cmd, _ := qdbTestCmd()
	err := runQDB(cmd, "old pilot", baseOpts())
	if err == nil || !strings.Contains(err.Error(), "already has data_project") {
		t.Fatalf("want existing-data_project refusal, got: %v", err)
	}
}

func TestQDB_RefusesEmptyFolder(t *testing.T) {
	dir := t.TempDir() // no data files
	t.Chdir(dir)
	cmd, _ := qdbTestCmd()
	err := runQDB(cmd, "old pilot", baseOpts())
	if err == nil || !strings.Contains(err.Error(), "no files") {
		t.Fatalf("want empty-folder refusal, got: %v", err)
	}
}

func TestQDB_RefusesOverSizeCap(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "big.bin"), strings.Repeat("x", 2<<20))
	t.Chdir(dir)
	cmd, _ := qdbTestCmd()
	o := baseOpts()
	o.maxSizeGB = 1.0 / 1024.0 // 1 MB cap → 2 MB folder exceeds it
	err := runQDB(cmd, "old pilot", o)
	if err == nil || !strings.Contains(err.Error(), "safety cap") {
		t.Fatalf("want size-cap refusal, got: %v", err)
	}
}

// --- cruft detection -------------------------------------------------------

func TestQDBFindCruft(t *testing.T) {
	dir := t.TempDir()
	// Data we must KEEP.
	writeFile(t, filepath.Join(dir, "data.csv"), "keep")
	writeFile(t, filepath.Join(dir, "notes.md"), "keep")
	writeFile(t, filepath.Join(dir, "x.mus"), "keep") // sidecar — never cruft
	writeFile(t, filepath.Join(dir, "mus.cfg"), "keep")
	// Cruft we must FIND.
	writeFile(t, filepath.Join(dir, ".git", "config"), "x")
	writeFile(t, filepath.Join(dir, "sub", "__pycache__", "m.cpython.pyc"), "x")
	writeFile(t, filepath.Join(dir, ".ipynb_checkpoints", "nb.ipynb"), "x")
	writeFile(t, filepath.Join(dir, "node_modules", "pkg", "index.js"), "x")
	writeFile(t, filepath.Join(dir, "loose.pyc"), "x")
	writeFile(t, filepath.Join(dir, ".DS_Store"), "x")

	hits, err := qdbFindCruft(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, h := range hits {
		rel, _ := filepath.Rel(dir, h)
		got[rel] = true
	}
	wantPresent := []string{
		".git", filepath.Join("sub", "__pycache__"), ".ipynb_checkpoints",
		"node_modules", "loose.pyc", ".DS_Store",
	}
	for _, w := range wantPresent {
		if !got[w] {
			t.Errorf("expected cruft hit %q, missing from %v", w, hits)
		}
	}
	// Keepers must NOT be flagged, and a matching DIR must not list its
	// children separately.
	for _, keep := range []string{"data.csv", "notes.md", "x.mus", "mus.cfg",
		filepath.Join("node_modules", "pkg", "index.js"),
		filepath.Join("sub", "__pycache__", "m.cpython.pyc")} {
		if got[keep] {
			t.Errorf("%q must not be listed (either a keeper or inside an already-listed dir)", keep)
		}
	}
}

func TestQDBFindCruft_NoneWhenClean(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "data.csv"), "keep")
	writeFile(t, filepath.Join(dir, "x.mus"), "keep")
	hits, err := qdbFindCruft(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("clean folder should have no cruft, got %v", hits)
	}
}

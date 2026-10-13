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

func TestQDB_RefusesMissingDataProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "data")
	t.Chdir(dir)
	cmd, _ := qdbTestCmd()
	err := runQDB(cmd, "old pilot", qdbDefaultStudyID, qdbDefaultMaxSizeGB, "")
	if err == nil || !strings.Contains(err.Error(), "data-project") {
		t.Fatalf("want data-project-required error, got: %v", err)
	}
}

func TestQDB_RefusesInvalidDataProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "data")
	t.Chdir(dir)
	cmd, _ := qdbTestCmd()
	err := runQDB(cmd, "old pilot", qdbDefaultStudyID, qdbDefaultMaxSizeGB, "../bad/name")
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
	err := runQDB(cmd, "old pilot", qdbDefaultStudyID, qdbDefaultMaxSizeGB, "OldPilot2026")
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
	err := runQDB(cmd, "old pilot", qdbDefaultStudyID, qdbDefaultMaxSizeGB, "OldPilot2026")
	if err == nil || !strings.Contains(err.Error(), "already has data_project") {
		t.Fatalf("want existing-data_project refusal, got: %v", err)
	}
}

func TestQDB_RefusesEmptyFolder(t *testing.T) {
	dir := t.TempDir() // no data files
	t.Chdir(dir)
	cmd, _ := qdbTestCmd()
	err := runQDB(cmd, "old pilot", qdbDefaultStudyID, qdbDefaultMaxSizeGB, "OldPilot2026")
	if err == nil || !strings.Contains(err.Error(), "no files") {
		t.Fatalf("want empty-folder refusal, got: %v", err)
	}
}

func TestQDB_RefusesOverSizeCap(t *testing.T) {
	dir := t.TempDir()
	// One 2 MB file; cap at a tiny value so it trips.
	writeFile(t, filepath.Join(dir, "big.bin"), strings.Repeat("x", 2<<20))
	t.Chdir(dir)
	cmd, _ := qdbTestCmd()
	// maxSizeGB tiny (1 MB worth) → 2 MB folder exceeds it.
	tinyCap := 1.0 / 1024.0 // 1 MB in GB
	err := runQDB(cmd, "old pilot", qdbDefaultStudyID, tinyCap, "OldPilot2026")
	if err == nil || !strings.Contains(err.Error(), "safety cap") {
		t.Fatalf("want size-cap refusal, got: %v", err)
	}
}

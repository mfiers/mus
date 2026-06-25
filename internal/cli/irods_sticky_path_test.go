package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExistingIRODSPathFromSidecar — happy path: sidecar with an
// irods_path returns the recorded value. Empty / missing sidecar
// returns "".
func TestExistingIRODSPathFromSidecar(t *testing.T) {
	dir := t.TempDir()
	dataFile := filepath.Join(dir, "data.tsv")
	if err := os.WriteFile(dataFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No sidecar yet → "".
	if got := existingIRODSPathFromSidecar(dataFile); got != "" {
		t.Errorf("no sidecar: got %q, want \"\"", got)
	}

	// Sidecar without irods_path → "".
	scContent := "sha256=abc\nsize=1\n"
	if err := os.WriteFile(dataFile+".mus", []byte(scContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := existingIRODSPathFromSidecar(dataFile); got != "" {
		t.Errorf("sidecar without IRODS: got %q, want \"\"", got)
	}

	// Sidecar WITH irods_path → returned verbatim.
	scContent2 := "sha256=abc\nsize=1\n" +
		"irods_path=/gbiomed/home/BADS/project/Fiers2025/MyExp/data.tsv\n" +
		"irods_status=uploaded\n"
	if err := os.WriteFile(dataFile+".mus", []byte(scContent2), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "/gbiomed/home/BADS/project/Fiers2025/MyExp/data.tsv"
	if got := existingIRODSPathFromSidecar(dataFile); got != want {
		t.Errorf("with irods_path: got %q, want %q", got, want)
	}
}

// TestExistingIRODSPathFromSidecar_LegacyLayoutSticks — the
// load-bearing scenario: a file uploaded under the old
// /project/<dp>/ layout has a sidecar pointing there. The helper
// surfaces that path so the upload-loop will REUSE it instead of
// computing the new /projects/<YEAR>/<dp>/ path. End-to-end
// behaviour is covered by the live smoke on real iRODS; this is
// the unit guarantee that the helper returns the right string.
func TestExistingIRODSPathFromSidecar_LegacyLayoutSticks(t *testing.T) {
	dir := t.TempDir()
	dataFile := filepath.Join(dir, "ancient.h5ad")
	if err := os.WriteFile(dataFile, []byte("h5"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataFile+".mus",
		[]byte("sha256=def\nirods_path=/gbiomed/home/BADS/project/Old2019/Exp/ancient.h5ad\nirods_status=uploaded\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	got := existingIRODSPathFromSidecar(dataFile)
	if got != "/gbiomed/home/BADS/project/Old2019/Exp/ancient.h5ad" {
		t.Errorf("legacy path not preserved: %q", got)
	}
}

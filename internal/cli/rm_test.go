package cli

// Unit tests for `mus rm`. The verifier itself shells out to iron and
// is covered by the integration test (real upload → real rm); the
// tests here exercise the path-resolution and tombstone-write logic.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/mfiers/mus/internal/sidecar"
)

// writeUploadedFileSidecar creates a data file `name` under `dir` and
// writes a paired sidecar that claims it was uploaded.
func writeUploadedFileSidecar(t *testing.T, dir, name, sha string) (string, string) {
	t.Helper()
	dataPath := filepath.Join(dir, name)
	if err := os.WriteFile(dataPath, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	scPath := sidecar.SidecarPath(dataPath)
	doc := &sidecar.Doc{
		File: sidecar.FileInfo{Sha256: sha, Size: 7},
		IRODS: &sidecar.IRODS{
			Path:       "/zone/home/u/" + name,
			URL:        "https://example/" + name,
			PURL:       "https://example/PID/" + name,
			Status:     "uploaded",
			UploadedAt: time.Now().UTC(),
		},
	}
	if err := sidecar.Write(scPath, doc); err != nil {
		t.Fatal(err)
	}
	return dataPath, scPath
}

func TestResolveRmTargets_DataPathInput(t *testing.T) {
	dir := t.TempDir()
	dataPath, scPath := writeUploadedFileSidecar(t, dir, "foo.txt", "abc")
	got, errs := resolveRmTargets([]string{dataPath})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 1 {
		t.Fatalf("got %d targets, want 1", len(got))
	}
	if got[0].dataPath != dataPath {
		t.Errorf("dataPath = %q, want %q", got[0].dataPath, dataPath)
	}
	if got[0].sidecarPath != scPath {
		t.Errorf("sidecarPath = %q, want %q", got[0].sidecarPath, scPath)
	}
	if got[0].kind != "file" {
		t.Errorf("kind = %q, want 'file'", got[0].kind)
	}
}

func TestResolveRmTargets_SidecarPathInput(t *testing.T) {
	// User points at the sidecar directly — should resolve the same.
	dir := t.TempDir()
	dataPath, scPath := writeUploadedFileSidecar(t, dir, "foo.txt", "abc")
	got, errs := resolveRmTargets([]string{scPath})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 1 || got[0].dataPath != dataPath {
		t.Fatalf("resolution wrong: %+v", got)
	}
}

func TestResolveRmTargets_MissingSidecarIsError(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "lonely.txt")
	if err := os.WriteFile(data, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errs := resolveRmTargets([]string{data})
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "cannot read sidecar") {
		t.Errorf("unexpected error wording: %s", errs[0])
	}
}

func TestResolveRmTargets_MissingDataIsError(t *testing.T) {
	// Sidecar exists, data file deleted manually. resolveRmTargets
	// must refuse — we want the data file present so we can verify
	// its current hash before deletion.
	dir := t.TempDir()
	_, scPath := writeUploadedFileSidecar(t, dir, "foo.txt", "abc")
	if err := os.Remove(filepath.Join(dir, "foo.txt")); err != nil {
		t.Fatal(err)
	}
	_, errs := resolveRmTargets([]string{scPath})
	if len(errs) != 1 || !strings.Contains(errs[0], "data path") {
		t.Errorf("expected 'data path missing' error, got: %v", errs)
	}
}

func TestResolveRmTargets_FileKindMismatchOnFolder(t *testing.T) {
	// Folder where the sidecar says it's a file → error. Catches
	// stale or hand-edited sidecars.
	dir := t.TempDir()
	subdir := filepath.Join(dir, "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	scPath := sidecar.SidecarPath(subdir)
	doc := &sidecar.Doc{
		// No Kind="folder" — looks like a file sidecar pointed at a dir.
		File:  sidecar.FileInfo{Sha256: "abc"},
		IRODS: &sidecar.IRODS{Path: "/zone/x", Status: "uploaded"},
	}
	if err := sidecar.Write(scPath, doc); err != nil {
		t.Fatal(err)
	}
	_, errs := resolveRmTargets([]string{subdir})
	if len(errs) != 1 || !strings.Contains(errs[0], "directory but sidecar is a file") {
		t.Errorf("expected kind-mismatch error, got: %v", errs)
	}
}

func TestPerformRm_TombstonesSidecarByDefault(t *testing.T) {
	dir := t.TempDir()
	dataPath, scPath := writeUploadedFileSidecar(t, dir, "foo.txt", "abc")
	doc, err := sidecar.Read(scPath)
	if err != nil {
		t.Fatal(err)
	}
	target := rmTarget{
		sidecarPath: scPath,
		dataPath:    dataPath,
		doc:         doc,
		kind:        "file",
	}
	if err := performRm(target, rmOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataPath); !os.IsNotExist(err) {
		t.Errorf("data file should be removed; stat err=%v", err)
	}
	// Sidecar should still be there as a tombstone.
	doc2, err := sidecar.Read(scPath)
	if err != nil {
		t.Fatalf("tombstone sidecar missing: %v", err)
	}
	if doc2.IRODS == nil || doc2.IRODS.Status != "local_deleted" {
		t.Errorf("expected irods_status=local_deleted, got %+v", doc2.IRODS)
	}
	// Pointer fields preserved so `mus irods get` can still restore.
	if doc2.IRODS.Path == "" || doc2.IRODS.PURL == "" {
		t.Errorf("tombstone should preserve iRODS pointers: %+v", doc2.IRODS)
	}
}

func TestPerformRm_DeleteSidecarFlag(t *testing.T) {
	dir := t.TempDir()
	dataPath, scPath := writeUploadedFileSidecar(t, dir, "foo.txt", "abc")
	doc, err := sidecar.Read(scPath)
	if err != nil {
		t.Fatal(err)
	}
	target := rmTarget{
		sidecarPath: scPath, dataPath: dataPath, doc: doc, kind: "file",
	}
	if err := performRm(target, rmOptions{deleteSidecar: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataPath); !os.IsNotExist(err) {
		t.Error("data file not removed")
	}
	if _, err := os.Stat(scPath); !os.IsNotExist(err) {
		t.Errorf("sidecar should also be removed with --delete-sidecar; stat err=%v", err)
	}
}

func TestPerformRm_FolderArchivedAlsoRemovesArchive(t *testing.T) {
	// folder-archived target: dataPath is the unpacked folder; the
	// archive sibling lives next to the sidecar. Both must go away.
	dir := t.TempDir()
	folderPath := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(folderPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folderPath, "a.sh"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "scripts.tar.gz")
	if err := os.WriteFile(archive, []byte("tar bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	scPath := sidecar.SidecarPath(folderPath)
	doc := &sidecar.Doc{
		Kind: "folder",
		Folder: &sidecar.FolderInfo{
			FileCount: 1, TotalBytes: 1, RecursiveSha256: "deadbeef",
		},
		Archive: &sidecar.ArchiveInfo{
			Format: "tar.gz", Filename: "scripts.tar.gz",
			Size: 9, Sha256: "tarsha",
		},
		IRODS: &sidecar.IRODS{
			Path: "/zone/scripts.tar.gz", Status: "uploaded",
		},
	}
	if err := sidecar.Write(scPath, doc); err != nil {
		t.Fatal(err)
	}
	target := rmTarget{
		sidecarPath: scPath, dataPath: folderPath, doc: doc, kind: "folder-archived",
	}
	if err := performRm(target, rmOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folderPath); !os.IsNotExist(err) {
		t.Error("unpacked folder should be removed")
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Error("archive sibling should be removed for folder-archived")
	}
	// Sidecar still present as tombstone.
	if _, err := os.Stat(scPath); err != nil {
		t.Errorf("tombstone sidecar missing: %v", err)
	}
}

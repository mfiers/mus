package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"codeberg.org/mfiers/mus/internal/iron"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// These tests exercise the verify→delete GATE ORDERING that
// uploadFolderDirect implements: cleanupSourceFolder must run only after
// verification succeeds, and must NOT run when verification fails. Rather
// than drive the whole cobra+iron+ELN upload stack (many unrelated moving
// parts), they compose the exact two production functions the gate wires
// together — verifyFolderUpload and cleanupSourceFolder — in the same
// order, and assert the on-disk outcome. This is the behaviour that makes
// --verify-and-clean safe: a bad upload never deletes local data.

// runVerifyThenCleanup mirrors the production gate in uploadFolderDirect:
//
//	if verify(...) fails -> return error, do NOT clean
//	else                 -> cleanupSourceFolder(...)
func runVerifyThenCleanup(t *testing.T, client *iron.Client, srcAbs, remoteBase string) error {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := verifyFolderUpload(context.Background(), client, srcAbs, remoteBase, localSum); err != nil {
		return err // production returns here WITHOUT deleting
	}
	return cleanupSourceFolder(srcAbs, &out, &errOut)
}

// writeUploadedFolderWithSidecar creates a source folder plus a sibling
// <folder>.mus sidecar marked uploaded, matching the state finalize left
// behind just before the gate runs.
func writeUploadedFolderWithSidecar(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	src := filepath.Join(root, "src")
	writeTree(t, src, files)
	doc := &sidecar.Doc{IRODS: &sidecar.IRODS{Status: "uploaded", Path: "/zone/coll/src"}}
	if err := sidecar.Write(src+sidecar.Suffix, doc); err != nil {
		t.Fatal(err)
	}
	return src
}

// GATE PASSES: every remote file matches → source folder is deleted and
// the sidecar is tombstoned (local_deleted).
func TestVerifyGate_MatchDeletesSource(t *testing.T) {
	root := t.TempDir()
	src := writeUploadedFolderWithSidecar(t, root, map[string]string{
		"a.txt":     "alpha",
		"sub/b.txt": "beta",
	})
	remoteBase := "/zone/coll/src"
	// fake iron: checksum mirrors the real local bytes → always matches.
	bin := writeFakeIron(t, `case "$1" in
  checksum)
    rel="${2#`+remoteBase+`/}"
    shasum -a 256 "`+src+`/$rel" | awk '{print $1}';;
  *) echo "unexpected $*" >&2; exit 2;;
esac`)
	client := &iron.Client{Bin: bin}

	if err := runVerifyThenCleanup(t, client, src, remoteBase); err != nil {
		t.Fatalf("gate should pass and clean, got: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source folder must be deleted after a passing verify; still present")
	}
	// Sidecar survives (sibling of the folder) and is tombstoned.
	doc, err := readMaybe(src + sidecar.Suffix)
	if err != nil || doc == nil {
		t.Fatalf("sidecar should remain readable, got doc=%v err=%v", doc, err)
	}
	if doc.IRODS == nil || doc.IRODS.Status != "local_deleted" {
		t.Errorf("sidecar should be tombstoned local_deleted, got %+v", doc.IRODS)
	}
}

// GATE FAILS (mismatch): the source folder MUST remain and an error is
// returned. This is the data-safety guarantee.
func TestVerifyGate_MismatchKeepsSource(t *testing.T) {
	root := t.TempDir()
	src := writeUploadedFolderWithSidecar(t, root, map[string]string{"a.txt": "alpha"})
	remoteBase := "/zone/coll/src"
	// fake iron returns a wrong-but-stable checksum → mismatch.
	bin := writeFakeIron(t, `case "$1" in
  checksum) echo "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef";;
esac`)
	client := &iron.Client{Bin: bin}

	err := runVerifyThenCleanup(t, client, src, remoteBase)
	if err == nil {
		t.Fatal("gate must FAIL on checksum mismatch")
	}
	if _, statErr := os.Stat(src); statErr != nil {
		t.Errorf("source folder MUST be kept on a failed verify; got stat err: %v", statErr)
	}
	// Sidecar must still say uploaded, not tombstoned.
	doc, _ := readMaybe(src + sidecar.Suffix)
	if doc == nil || doc.IRODS == nil || doc.IRODS.Status != "uploaded" {
		t.Errorf("sidecar must remain 'uploaded' (not tombstoned) on failed verify, got %+v", doc)
	}
}

// GATE FAILS (missing remote file): same guarantee — keep the source.
func TestVerifyGate_MissingRemoteKeepsSource(t *testing.T) {
	root := t.TempDir()
	src := writeUploadedFolderWithSidecar(t, root, map[string]string{
		"a.txt": "alpha",
		"b.txt": "beta",
	})
	remoteBase := "/zone/coll/src"
	// a.txt OK, b.txt reported missing.
	bin := writeFakeIron(t, `case "$1" in
  checksum)
    case "$2" in
      *"/a.txt") shasum -a 256 "`+src+`/a.txt" | awk '{print $1}';;
      *) echo "not found" >&2; exit 4;;
    esac;;
esac`)
	client := &iron.Client{Bin: bin}

	if err := runVerifyThenCleanup(t, client, src, remoteBase); err == nil {
		t.Fatal("gate must FAIL when a remote file is missing")
	}
	if _, statErr := os.Stat(src); statErr != nil {
		t.Errorf("source folder MUST be kept when a remote file is missing; got: %v", statErr)
	}
}

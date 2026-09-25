package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/mfiers/mus/internal/iron"
)

// --- verifyArchiveUpload (packed path) ------------------------------------

// A matching remote checksum must pass (the caller is then allowed to
// delete local data).
func TestVerifyArchiveUpload_Match(t *testing.T) {
	localSum := "abcDEF1234567890"
	// fake iron echoes the same sum (upper-cased) — EqualFold must match.
	bin := writeFakeIron(t, `case "$1" in
  checksum) echo "ABCDEF1234567890";;
  *) echo "unexpected $*" >&2; exit 2;;
esac`)
	client := &iron.Client{Bin: bin}
	if err := verifyArchiveUpload(context.Background(), client,
		"/zone/coll/x.tar.gz", localSum); err != nil {
		t.Fatalf("matching checksum should verify, got: %v", err)
	}
}

// A differing remote checksum must fail — the caller MUST keep local data.
func TestVerifyArchiveUpload_Mismatch(t *testing.T) {
	bin := writeFakeIron(t, `case "$1" in
  checksum) echo "0000000000000000";;
esac`)
	client := &iron.Client{Bin: bin}
	err := verifyArchiveUpload(context.Background(), client,
		"/zone/coll/x.tar.gz", "abcdef1234567890")
	if err == nil {
		t.Fatal("mismatched checksum must fail")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("expected a mismatch error, got: %v", err)
	}
}

// An iron error (object missing/unreadable) must fail closed.
func TestVerifyArchiveUpload_IronError(t *testing.T) {
	bin := writeFakeIron(t, `exit 3`)
	client := &iron.Client{Bin: bin}
	if err := verifyArchiveUpload(context.Background(), client,
		"/zone/coll/x.tar.gz", "abcdef"); err == nil {
		t.Fatal("iron failure must produce a verify error (fail closed)")
	}
}

// --- verifyFolderUpload (unpacked path) -----------------------------------

// writeTree creates files under root and returns a map relpath→content.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// localSum matches verifyFolderUpload's sum() contract (hex sha256).
func localSum(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

// When every remote file's checksum matches its local file, verification
// passes. The fake iron computes the sha256 of the LOCAL file at the
// mirrored remote path — we mirror the tree locally under the fake remote
// root so `iron checksum <remote>` can hash the real bytes.
func TestVerifyFolderUpload_AllMatch(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"a.txt":       "alpha",
		"sub/b.txt":   "beta",
		"data.mus":    "SIDECAR — should be skipped", // must be ignored
	})

	// Fake iron: `iron checksum <remote>` where <remote> is remoteBase/<rel>.
	// We map it back to the local file under src by stripping remoteBase and
	// hashing the corresponding source file. This models a correct upload.
	remoteBase := "/zone/coll/src"
	script := `case "$1" in
  checksum)
    remote="$2"
    rel="${remote#` + remoteBase + `/}"
    # hash the matching local source file
    if [ -f "` + src + `/$rel" ]; then
      shasum -a 256 "` + src + `/$rel" | awk '{print $1}'
    else
      echo "not found" >&2; exit 4
    fi
    ;;
  *) echo "unexpected $*" >&2; exit 2;;
esac`
	client := &iron.Client{Bin: writeFakeIron(t, script)}

	if err := verifyFolderUpload(context.Background(), client, src, remoteBase, localSum); err != nil {
		t.Fatalf("all-match folder should verify, got: %v", err)
	}
}

// A missing remote file must fail verification (fail closed): the fake
// iron returns non-zero for the second file.
func TestVerifyFolderUpload_MissingRemote(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"a.txt":     "alpha",
		"sub/b.txt": "beta",
	})
	remoteBase := "/zone/coll/src"
	// iron reports a.txt fine but b.txt missing.
	script := `case "$1" in
  checksum)
    case "$2" in
      *"/a.txt") shasum -a 256 "` + src + `/a.txt" | awk '{print $1}';;
      *) echo "does not exist" >&2; exit 4;;
    esac
    ;;
esac`
	client := &iron.Client{Bin: writeFakeIron(t, script)}

	err := verifyFolderUpload(context.Background(), client, src, remoteBase, localSum)
	if err == nil {
		t.Fatal("a missing remote file must fail verification")
	}
	if !strings.Contains(err.Error(), "b.txt") {
		t.Errorf("error should name the failing file, got: %v", err)
	}
}

// A remote checksum that differs from local must fail (silent corruption).
func TestVerifyFolderUpload_ChecksumMismatch(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.txt": "alpha"})
	remoteBase := "/zone/coll/src"
	// iron returns a bogus, stable checksum that won't match local.
	script := `case "$1" in
  checksum) echo "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef";;
esac`
	client := &iron.Client{Bin: writeFakeIron(t, script)}

	err := verifyFolderUpload(context.Background(), client, src, remoteBase, localSum)
	if err == nil {
		t.Fatal("checksum mismatch must fail verification")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("expected a mismatch error, got: %v", err)
	}
}

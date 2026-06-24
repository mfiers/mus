package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"codeberg.org/mfiers/mus/internal/iron"
)

// writeFakeIron writes a shell script that mimics whatever subset of
// the `iron` CLI a single test needs. The script reads its first arg
// (the subcommand) and prints whatever the test asked for, or exits
// non-zero on a sentinel marker.
//
// Returns the script path; caller assigns it to client.Bin.
func writeFakeIron(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-iron shell-script harness assumes a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "iron")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestIRODSRemoteMatchesLocal_Match — happy path: iron returns the
// same checksum (case-different) as the local sum. Should match.
func TestIRODSRemoteMatchesLocal_Match(t *testing.T) {
	binPath := writeFakeIron(t, `case "$1" in
  checksum) echo "ABCDEF1234567890";;
  *) echo "fake iron called with $*" >&2; exit 2;;
esac`)
	client := &iron.Client{Bin: binPath}
	if !irodsRemoteMatchesLocal(context.Background(), client,
		"/zone/coll/x.tsv", "abcdef1234567890") {
		t.Errorf("expected case-insensitive match")
	}
}

// TestIRODSRemoteMatchesLocal_NoMatch — iron returns a different sum
// → false. Caller proceeds with real upload.
func TestIRODSRemoteMatchesLocal_NoMatch(t *testing.T) {
	binPath := writeFakeIron(t, `case "$1" in
  checksum) echo "0000000000000000";;
esac`)
	client := &iron.Client{Bin: binPath}
	if irodsRemoteMatchesLocal(context.Background(), client,
		"/zone/coll/x.tsv", "abcdef1234567890") {
		t.Errorf("different sums should not match")
	}
}

// TestIRODSRemoteMatchesLocal_RemoteMissing — iron exits non-zero
// (e.g. object doesn't exist yet — first upload). We must return
// false so the caller actually uploads.
func TestIRODSRemoteMatchesLocal_RemoteMissing(t *testing.T) {
	binPath := writeFakeIron(t, `echo "ERROR: object not found" >&2
exit 4`)
	client := &iron.Client{Bin: binPath}
	if irodsRemoteMatchesLocal(context.Background(), client,
		"/zone/coll/new.tsv", "abcdef1234567890") {
		t.Errorf("missing remote should NOT signal match — would block first upload")
	}
}

// TestIRODSRemoteMatchesLocal_EmptyLocalSum — defensive: if we don't
// have a local sum (cache failure, empty file edge case), we MUST
// NOT skip — we can't prove equivalence.
func TestIRODSRemoteMatchesLocal_EmptyLocalSum(t *testing.T) {
	binPath := writeFakeIron(t, `case "$1" in
  checksum) echo "ABCDEF1234567890";;
esac`)
	client := &iron.Client{Bin: binPath}
	if irodsRemoteMatchesLocal(context.Background(), client,
		"/zone/coll/x.tsv", "") {
		t.Errorf("empty local sum should never signal match")
	}
}

// TestIRODSRemoteMatchesLocal_EmptyRemoteResponse — iron returns 0
// but no checksum line (parser couldn't extract one). Treat as
// "unknown" → false.
func TestIRODSRemoteMatchesLocal_EmptyRemoteResponse(t *testing.T) {
	binPath := writeFakeIron(t, `case "$1" in
  checksum) echo "";;
esac`)
	client := &iron.Client{Bin: binPath}
	// iron.Checksum will fail to parse "" → returns an error. Either
	// way irodsRemoteMatchesLocal must return false.
	if irodsRemoteMatchesLocal(context.Background(), client,
		"/zone/coll/x.tsv", "abcdef1234567890") {
		t.Errorf("empty remote response should not signal match")
	}
}

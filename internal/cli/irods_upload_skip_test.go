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

// readFileOrEmpty returns the bytes at path, or empty + nil if it
// doesn't exist (so tests can grep an "iron wasn't called" log).
func readFileOrEmpty(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

// TestInspectRemoteForUpload_SameBytes — iron returns the same
// checksum (case-different) as the local sum. State: remoteSameBytes,
// caller skips iron.
func TestInspectRemoteForUpload_SameBytes(t *testing.T) {
	binPath := writeFakeIron(t, `case "$1" in
  checksum) echo "ABCDEF1234567890";;
  *) echo "fake iron called with $*" >&2; exit 2;;
esac`)
	client := &iron.Client{Bin: binPath}
	if got := inspectRemoteForUpload(context.Background(), client,
		"/zone/coll/x.tsv", "abcdef1234567890"); got != remoteSameBytes {
		t.Errorf("expected remoteSameBytes (case-insensitive match), got %v", got)
	}
}

// TestInspectRemoteForUpload_Differs — iron returns a different
// sum. State: remoteDiffers — caller must prompt before overwrite.
func TestInspectRemoteForUpload_Differs(t *testing.T) {
	binPath := writeFakeIron(t, `case "$1" in
  checksum) echo "0000000000000000";;
esac`)
	client := &iron.Client{Bin: binPath}
	if got := inspectRemoteForUpload(context.Background(), client,
		"/zone/coll/x.tsv", "abcdef1234567890"); got != remoteDiffers {
		t.Errorf("expected remoteDiffers, got %v", got)
	}
}

// TestInspectRemoteForUpload_Absent — iron exits non-zero (object
// doesn't exist yet — first upload). State: remoteAbsent so caller
// proceeds with real upload, no prompt.
func TestInspectRemoteForUpload_Absent(t *testing.T) {
	binPath := writeFakeIron(t, `echo "ERROR: object not found" >&2
exit 4`)
	client := &iron.Client{Bin: binPath}
	if got := inspectRemoteForUpload(context.Background(), client,
		"/zone/coll/new.tsv", "abcdef1234567890"); got != remoteAbsent {
		t.Errorf("expected remoteAbsent (would block first upload), got %v", got)
	}
}

// TestInspectRemoteForUpload_EmptyLocalSum — defensive: we MUST
// NOT claim a match when we have nothing to compare.
func TestInspectRemoteForUpload_EmptyLocalSum(t *testing.T) {
	binPath := writeFakeIron(t, `case "$1" in
  checksum) echo "ABCDEF1234567890";;
esac`)
	client := &iron.Client{Bin: binPath}
	if got := inspectRemoteForUpload(context.Background(), client,
		"/zone/coll/x.tsv", ""); got != remoteAbsent {
		t.Errorf("empty local sum should yield remoteAbsent (no claim), got %v", got)
	}
}

// TestInspectRemoteForUpload_EmptyRemoteResponse — iron returns 0
// but no checksum line. Defensively treat as absent.
func TestInspectRemoteForUpload_EmptyRemoteResponse(t *testing.T) {
	binPath := writeFakeIron(t, `case "$1" in
  checksum) echo "";;
esac`)
	client := &iron.Client{Bin: binPath}
	if got := inspectRemoteForUpload(context.Background(), client,
		"/zone/coll/x.tsv", "abcdef1234567890"); got != remoteAbsent {
		t.Errorf("empty remote response should yield remoteAbsent, got %v", got)
	}
}

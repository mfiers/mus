package iron

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeIronBin writes a shell-script stand-in for the `iron`
// binary at a temp path and returns the path. Tests assign it to
// Client.Bin to exercise the shellout machinery without needing a
// real iron install.
func writeFakeIronBin(t *testing.T, content string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-iron shell-script harness assumes a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "iron")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestEnrichIronError_UnixFileOpenErr — the load-bearing case from
// the bug report: a colleague tries to overwrite a file someone else
// uploaded; iRODS refuses with UNIX_FILE_OPEN_ERR because the
// underlying replica is owned by a different unix uid.
func TestEnrichIronError_UnixFileOpenErr(t *testing.T) {
	stderr := `/lustre1/project/.../exp5244.tar.gz FAILED: IRODS error UNIX_FILE_OPEN_ERR
Error: 1 errors
Usage:
  iron upload <local file> [target path] [flags]
`
	got := enrichIronError(stderr, "/gbiomed/.../exp5244.tar.gz")
	if got == "" {
		t.Fatal("expected enriched message, got empty")
	}
	wantBits := []string{
		"refused to overwrite",
		"owned by another user",
		"iron chmod write",
		"/gbiomed/.../exp5244.tar.gz", // target path threaded through
	}
	for _, b := range wantBits {
		if !strings.Contains(got, b) {
			t.Errorf("enriched message missing %q\nfull: %s", b, got)
		}
	}
}

// TestEnrichIronError_UnknownError — generic failure (e.g. auth
// error, transient network) returns "" so caller falls back to the
// raw iron-exec error. We don't want to swallow unknown errors with
// a misleading rewrite.
func TestEnrichIronError_UnknownError(t *testing.T) {
	stderr := "CAT_INVALID_AUTHENTICATION: token expired\n"
	if got := enrichIronError(stderr, "/zone/coll/x.tsv"); got != "" {
		t.Errorf("unknown error should yield empty; got: %s", got)
	}
}

// TestEnrichIronError_EmptyStderr — defensive: a hard exit with no
// stderr should not trigger enrichment.
func TestEnrichIronError_EmptyStderr(t *testing.T) {
	if got := enrichIronError("", "/zone/coll/x.tsv"); got != "" {
		t.Errorf("empty stderr should yield empty; got: %s", got)
	}
}

// TestUpload_UnixFileOpenErr_EndToEnd — integration: a fake iron
// script prints the UNIX_FILE_OPEN_ERR marker on stderr and exits
// non-zero. Upload() should return the enriched error, not the raw
// "exit status 1" wrap.
func TestUpload_UnixFileOpenErr_EndToEnd(t *testing.T) {
	// Fake iron: print the error on stderr, exit non-zero. Mimics
	// the real iron's behavior on UNIX_FILE_OPEN_ERR.
	binPath := writeFakeIronBin(t, `#!/bin/sh
echo "/lustre1/exp5244.tar.gz FAILED: IRODS error UNIX_FILE_OPEN_ERR" >&2
exit 1
`)
	client := &Client{Bin: binPath}
	err := client.Upload(nil, "/tmp/local", "/gbiomed/remote/exp5244.tar.gz",
		UploadOpts{})
	if err == nil {
		t.Fatal("expected an error from the fake iron")
	}
	msg := err.Error()
	if !strings.Contains(msg, "refused to overwrite") {
		t.Errorf("expected enriched message; got: %s", msg)
	}
	if !strings.Contains(msg, "/gbiomed/remote/exp5244.tar.gz") {
		t.Errorf("expected remote path in enriched message; got: %s", msg)
	}
	// Should NOT contain the raw "exit status 1" — that's the
	// fallback wrap that we DIDN'T want.
	if strings.Contains(msg, "exit status") {
		t.Errorf("enriched path should suppress the raw exit-status wrap; got: %s", msg)
	}
}

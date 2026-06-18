package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestCheckPrinter_VerboseShowsAll(t *testing.T) {
	var out, err bytes.Buffer
	cp := &checkPrinter{silent: false, stdout: &out, stderr: &err}

	cp.ok("file-vs-iRODS", "/p/a.h5ad", "matches")
	cp.skip("source-merkle", "/p/b", "source removed")
	cp.mismatch("file-vs-iRODS", "/p/c.csv", "local=abc remote=def")
	cp.errf("file-vs-iRODS", "/p/d", "transport: %s", "timeout")
	cp.missing("archive-vs-iRODS", "/p/e.tar.gz", "archive absent")

	stdout := out.String()
	stderr := err.String()

	for _, line := range []string{"OK", "SKIP"} {
		if !strings.Contains(stdout, line) {
			t.Errorf("stdout missing %q:\n%s", line, stdout)
		}
	}
	for _, line := range []string{"MISMATCH", "ERROR", "MISSING"} {
		if !strings.Contains(stderr, line) {
			t.Errorf("stderr missing %q:\n%s", line, stderr)
		}
	}
	// Failures must NOT bleed into stdout.
	for _, line := range []string{"MISMATCH", "ERROR", "MISSING"} {
		if strings.Contains(stdout, line) {
			t.Errorf("failure %q leaked into stdout:\n%s", line, stdout)
		}
	}
	// Every line should carry the check name.
	for _, label := range []string{"file-vs-iRODS", "source-merkle", "archive-vs-iRODS"} {
		if !strings.Contains(stdout+stderr, label) {
			t.Errorf("missing check label %q", label)
		}
	}
}

func TestCheckPrinter_SilentSuppressesOKAndSkip(t *testing.T) {
	var out, err bytes.Buffer
	cp := &checkPrinter{silent: true, stdout: &out, stderr: &err}

	cp.ok("file-vs-iRODS", "/p/a", "matches")
	cp.skip("source-merkle", "/p/b", "source removed")
	cp.mismatch("file-vs-iRODS", "/p/c", "drifted")
	cp.errf("file-vs-iRODS", "/p/d", "boom")
	cp.missing("archive-vs-iRODS", "/p/e", "absent")

	if out.Len() != 0 {
		t.Errorf("--silent leaked OK/SKIP to stdout:\n%s", out.String())
	}
	for _, want := range []string{"MISMATCH", "ERROR", "MISSING"} {
		if !strings.Contains(err.String(), want) {
			t.Errorf("--silent dropped failure %q:\n%s", want, err.String())
		}
	}
}

func TestCheckPrinter_NoteOptional(t *testing.T) {
	var out, e bytes.Buffer
	cp := &checkPrinter{silent: false, stdout: &out, stderr: &e}

	cp.ok("file-vs-iRODS", "/p/a", "")
	got := out.String()
	if strings.Contains(got, "()") {
		t.Errorf("empty note rendered as empty parens: %q", got)
	}
	if !strings.Contains(got, "/p/a") {
		t.Errorf("target missing: %q", got)
	}
}

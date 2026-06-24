package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/iron"
)

// TestPromptOverwrite_NonTTY_RefusesAlways — non-TTY stdin (any
// io.Reader that isn't *os.File pointing at a tty) must NEVER
// return true. CI and `mus serve` callers rely on this safety.
func TestPromptOverwrite_NonTTY_RefusesAlways(t *testing.T) {
	for _, ans := range []string{"y\n", "yes\n", "Y\n", "YES\n", "\n", ""} {
		stdin := strings.NewReader(ans)
		var stdout bytes.Buffer
		if promptOverwrite(stdin, &stdout, "/zone/x.tsv") {
			t.Errorf("non-TTY with answer %q should refuse overwrite", ans)
		}
		// And nothing should have been written to stdout — we don't
		// even render the prompt on non-TTY.
		if stdout.Len() != 0 {
			t.Errorf("non-TTY should not render the prompt; got: %q", stdout.String())
		}
	}
}

// TestIsStdinTerminal_FalseForReader — a bare io.Reader (not
// *os.File) is not a terminal. The test confirms our type assertion
// rejects the obvious non-TTY case.
func TestIsStdinTerminal_FalseForReader(t *testing.T) {
	if isStdinTerminal(strings.NewReader("yes\n")) {
		t.Errorf("strings.Reader should not be classified as a TTY")
	}
	if isStdinTerminal(&bytes.Buffer{}) {
		t.Errorf("bytes.Buffer should not be classified as a TTY")
	}
}

// TestApplyIRODSGroupACL_DefaultGroup — env doesn't set
// irods_group → uses defaults.IRODSGroup (BADS). Fake iron records
// the args so we can confirm chmod was called with the right group.
func TestApplyIRODSGroupACL_DefaultGroup(t *testing.T) {
	root := t.TempDir()
	env, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "iron.log")
	binPath := writeFakeIron(t, `echo "$@" >> `+logPath+`
exit 0`)
	client := &iron.Client{Bin: binPath}
	var stderr bytes.Buffer
	applyIRODSGroupACL(context.Background(), client, env, "/zone/x.tsv", &stderr)
	logBytes, _ := readFileOrEmpty(logPath)
	logged := string(logBytes)
	if !strings.Contains(logged, "chmod own BADS /zone/x.tsv") {
		t.Errorf("expected default BADS chmod; iron got: %q", logged)
	}
}

// TestApplyIRODSGroupACL_CustomGroup — irods_group=MyLab in the
// cascade overrides the default.
func TestApplyIRODSGroupACL_CustomGroup(t *testing.T) {
	root := t.TempDir()
	writeMusCfg(t, root, "irods_group=MyLab")
	env, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "iron.log")
	binPath := writeFakeIron(t, `echo "$@" >> `+logPath+`
exit 0`)
	client := &iron.Client{Bin: binPath}
	var stderr bytes.Buffer
	applyIRODSGroupACL(context.Background(), client, env, "/zone/x.tsv", &stderr)
	logBytes, _ := readFileOrEmpty(logPath)
	if !strings.Contains(string(logBytes), "chmod own MyLab /zone/x.tsv") {
		t.Errorf("expected MyLab chmod; iron got: %q", logBytes)
	}
}

// TestApplyIRODSGroupACL_ExplicitEmptyOptsOut — irods_group= (empty
// string explicitly) is the documented opt-out path. iron must NOT
// be invoked.
func TestApplyIRODSGroupACL_ExplicitEmptyOptsOut(t *testing.T) {
	root := t.TempDir()
	writeMusCfg(t, root, "irods_group=")
	env, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "iron.log")
	binPath := writeFakeIron(t, `echo "$@" >> `+logPath+`
exit 0`)
	client := &iron.Client{Bin: binPath}
	var stderr bytes.Buffer
	applyIRODSGroupACL(context.Background(), client, env, "/zone/x.tsv", &stderr)
	logBytes, _ := readFileOrEmpty(logPath)
	if len(logBytes) > 0 {
		t.Errorf("explicit empty irods_group should opt out; iron got: %q", logBytes)
	}
}

// TestApplyIRODSGroupACL_FailureWarnsButDoesntBlock — iron exits
// non-zero (e.g. group doesn't exist, no own perm). We must WARN on
// stderr but NOT bubble an error — the bytes are already on iRODS.
func TestApplyIRODSGroupACL_FailureWarnsButDoesntBlock(t *testing.T) {
	root := t.TempDir()
	env, _ := config.Load(root)
	binPath := writeFakeIron(t, `echo "ERROR: CAT_NO_ACCESS_PERMISSION" >&2
exit 1`)
	client := &iron.Client{Bin: binPath}
	var stderr bytes.Buffer
	// No panic, no return value — must just print a warning.
	applyIRODSGroupACL(context.Background(), client, env, "/zone/x.tsv", &stderr)
	if !strings.Contains(stderr.String(), "group ACL not stamped") {
		t.Errorf("expected ACL-failure warning on stderr; got: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "iron chmod own BADS /zone/x.tsv") {
		t.Errorf("warning should include the manual-fix command; got: %s", stderr.String())
	}
}

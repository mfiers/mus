package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteBashCompletionFile_PrependsPolyfill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mus-completion.bash")

	if err := writeBashCompletionFile(NewRootCmd(), path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	// Polyfill present and guarded by a declare-F check.
	if !strings.Contains(body, "if ! declare -F _get_comp_words_by_ref") {
		t.Errorf("polyfill guard missing — installed script will break on systems without bash-completion")
	}
	if !strings.Contains(body, "_get_comp_words_by_ref()") {
		t.Errorf("polyfill function definition missing")
	}

	// Cobra V2 body present after the polyfill.
	if !strings.Contains(body, "__mus_get_completion_results") {
		t.Errorf("cobra completion body appears missing: %d bytes", len(body))
	}

	// The polyfill MUST come before cobra's body — otherwise the
	// cobra script's first reference to _get_comp_words_by_ref
	// would still fire before our guard is installed.
	polyfillAt := bytes.Index(raw, []byte("_get_comp_words_by_ref()"))
	cobraAt := bytes.Index(raw, []byte("__mus_get_completion_results"))
	if polyfillAt < 0 || cobraAt < 0 {
		t.Fatalf("expected both markers; polyfill=%d cobra=%d", polyfillAt, cobraAt)
	}
	if polyfillAt > cobraAt {
		t.Errorf("polyfill (offset %d) must come before cobra body (offset %d)", polyfillAt, cobraAt)
	}
}

func TestWriteBashCompletionFile_BashSyntaxValid(t *testing.T) {
	// Only run if /bin/bash exists.
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mus-completion.bash")
	if err := writeBashCompletionFile(NewRootCmd(), path); err != nil {
		t.Fatal(err)
	}
	// `bash -n` parses the script without executing — catches
	// syntax errors in the polyfill or anywhere else.
	out, err := exec.Command(bash, "-n", path).CombinedOutput()
	if err != nil {
		t.Errorf("bash -n rejected the generated script:\n%s", out)
	}
}

func TestWriteBashCompletionFile_PolyfillIsNoopWhenInstalled(t *testing.T) {
	// Source the script in a bash subshell where _get_comp_words_by_ref
	// is pre-defined as a sentinel. The polyfill MUST detect this and
	// leave the sentinel intact — otherwise it would shadow a working
	// bash-completion install.
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mus-completion.bash")
	if err := writeBashCompletionFile(NewRootCmd(), path); err != nil {
		t.Fatal(err)
	}
	script := strings.Join([]string{
		"set -e",
		// Sentinel _get_comp_words_by_ref present BEFORE sourcing.
		"_get_comp_words_by_ref() { echo MUS_TEST_SENTINEL; }",
		"source " + path,
		// After sourcing, call it and assert the sentinel still runs.
		"out=$(_get_comp_words_by_ref 2>&1)",
		"[[ \"$out\" == \"MUS_TEST_SENTINEL\" ]] || { echo \"sentinel was shadowed: '$out'\"; exit 1; }",
	}, "\n")
	out, err := exec.Command(bash, "-c", script).CombinedOutput()
	if err != nil {
		t.Errorf("polyfill shadowed a pre-existing definition:\n%s", out)
	}
}

package cli

import (
	"os"
	"strings"
	"testing"
)

// TestUploadErrorSignaturesArePinned guards the coupling between the
// serve UI's diagnoseUploadOutput() and the CLI error strings it scans
// for. The UI turns raw upload output into an actionable dialog by
// pattern-matching these phrases; if a CLI reword drops them, the UI
// silently falls back to "see the log", which is exactly the bad UX we
// removed. Keep these phrases in sync with diagnoseUploadOutput in
// internal/cli/ui/index.html.
func TestUploadErrorSignaturesArePinned(t *testing.T) {
	cases := []struct {
		file   string
		phrase string
		why    string
	}{
		{"irods.go", "exists with different bytes", "UI 'overwrite changed files' detection"},
		{"irods_folder.go", "density check failed", "UI 'pack folders' detection"},
		{"irods_folder.go", "no TTY available for prompt", "UI 'pack folders' detection (fallback phrase)"},
	}
	for _, c := range cases {
		src, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("read %s: %v", c.file, err)
		}
		if !strings.Contains(string(src), c.phrase) {
			t.Errorf("%s no longer contains %q — this breaks %s in the serve UI. "+
				"Update diagnoseUploadOutput() in internal/cli/ui/index.html to match the new wording.",
				c.file, c.phrase, c.why)
		}
	}

	// And the mirror side: the UI must actually scan for them.
	ui := string(serveIndexHTML)
	for _, needle := range []string{"different bytes", "density check failed", "diagnoseUploadOutput"} {
		if !strings.Contains(ui, needle) {
			t.Errorf("embedded UI missing %q — upload-error diagnostics are wired to CLI phrases", needle)
		}
	}
}

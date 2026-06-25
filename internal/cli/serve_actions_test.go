package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAPIPreflight_EmptyRoot — fresh cwd, nothing set → every blocking
// check is red. data_project, eln, files all fail; secrets is detail-
// only (won't block by design).
func TestAPIPreflight_EmptyRoot(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	r := httptest.NewRequest(http.MethodGet, "/api/preflight?dir=", nil)
	w := httptest.NewRecorder()
	apiPreflight(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d; body: %s", w.Code, w.Body)
	}
	var resp struct {
		Checks []preflightCheck
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	mp := map[string]preflightCheck{}
	for _, c := range resp.Checks {
		mp[c.Key] = c
	}
	for _, key := range []string{"data_project", "eln", "files"} {
		if mp[key].OK {
			t.Errorf("expected %q check to fail on empty root; got OK", key)
		}
	}
}

// TestAPIPreflight_HappyPath — set everything; checks pass (except the
// irods_secret check, which depends on a real keyring we don't mock).
func TestAPIPreflight_HappyPath(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	if err := os.WriteFile(filepath.Join(root, "mus.cfg"),
		[]byte("data_project=Fiers2025\neln_disabled=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.tsv"), []byte("a\tb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/preflight?dir=", nil)
	w := httptest.NewRecorder()
	apiPreflight(w, r)
	var resp struct {
		Checks []preflightCheck
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	mp := map[string]preflightCheck{}
	for _, c := range resp.Checks {
		mp[c.Key] = c
	}
	if !mp["data_project"].OK {
		t.Errorf("data_project should be OK; got: %+v", mp["data_project"])
	}
	if !mp["eln"].OK {
		t.Errorf("eln should be OK (disabled counts as decided); got: %+v", mp["eln"])
	}
	if !mp["files"].OK {
		t.Errorf("files should be OK (data.tsv present); got: %+v", mp["files"])
	}
}

// TestAPIPreflight_RefusesEscape — same path-safety as the read API.
func TestAPIPreflight_RefusesEscape(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	r := httptest.NewRequest(http.MethodGet, "/api/preflight?dir=../etc", nil)
	w := httptest.NewRecorder()
	apiPreflight(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("escape attempt: status %d, want 400", w.Code)
	}
}

// TestAPIUpload_RequiresAtLeastOnePath — empty paths array must
// 400, not silently upload nothing.
func TestAPIUpload_RequiresAtLeastOnePath(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiUpload, map[string]any{
		"paths": []string{}, "message": "x",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty paths: status %d, want 400", w.Code)
	}
}

// TestAPIUpload_RequiresMessage — -m / --message is mandatory.
func TestAPIUpload_RequiresMessage(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiUpload, map[string]any{
		"paths": []string{""},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing message: status %d, want 400; body: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "message is required") {
		t.Errorf("error should mention message; got: %s", w.Body)
	}
}

// TestAPIUpload_RejectsMultilineMessage — newlines would split arg
// parsing in surprising ways.
func TestAPIUpload_RejectsMultilineMessage(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiUpload, map[string]any{
		"paths": []string{""}, "message": "line1\nline2",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("multiline message: status %d, want 400", w.Code)
	}
}

// TestAPIUpload_RejectsAbsolutePath — any path with leading slash
// must be refused, even if other paths in the batch are valid.
func TestAPIUpload_RejectsAbsolutePath(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiUpload, map[string]any{
		"paths": []string{"", "/etc/passwd"}, "message": "x",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("absolute path: status %d, want 400", w.Code)
	}
}

// TestAPIUpload_AcceptsDryRun — schema check only: the new dry_run
// field is accepted by the decoder + reaches the validation block
// without 400. We can't end-to-end-test the actual `--dry-run`
// shellout without iron, but the live smoke (curl against a real
// `mus serve`) covers that.
func TestAPIUpload_AcceptsDryRun(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	// Empty message still fails — confirms the body decoded and the
	// existing validation chain still runs, just with dry_run=true.
	w := postJSON(apiUpload, map[string]any{
		"paths": []string{""}, "dry_run": true,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected message-required error even with dry_run; got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "message is required") {
		t.Errorf("body should still require message; got: %s", w.Body)
	}
}

// TestAPIUpload_RejectsGetMethod — POST only.
func TestAPIUpload_RejectsGetMethod(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	r := httptest.NewRequest(http.MethodGet, "/api/upload", nil)
	w := httptest.NewRecorder()
	apiUpload(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should be rejected; got %d", w.Code)
	}
}

// TestAPIUpload_RefusesEscape — escape attempt via `..` in any
// path of the batch must refuse the whole batch.
func TestAPIUpload_RefusesEscape(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiUpload, map[string]any{
		"paths": []string{"../etc"}, "message": "x",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("escape attempt: status %d, want 400", w.Code)
	}
}

// TestServeIndex_EmbeddedHTMLIsPresent — confirms the go:embed went
// through. Catches the failure mode where the html file gets deleted
// and the embed silently yields an empty []byte.
func TestServeIndex_EmbeddedHTMLIsPresent(t *testing.T) {
	if len(serveIndexHTML) < 1000 {
		t.Fatalf("embedded index.html is suspiciously small (%d bytes) — "+
			"go:embed may have failed silently", len(serveIndexHTML))
	}
	s := string(serveIndexHTML)
	// Spot-check load-bearing IDs the JS reaches for.
	for _, id := range []string{"id=\"tree\"", "id=\"cfg-form\"",
		"id=\"cfg-key\"", "id=\"cfg-value-host\"", "id=\"upload-go\"",
		"id=\"upload-dryrun\"",
		"id=\"checks\"", "id=\"upload-log\"",
		"/api/preflight", "/api/upload", "/api/known-keys",
		"/api/config/unset", "chip-x",
	} {
		if !strings.Contains(s, id) {
			t.Errorf("embedded UI missing required element / endpoint: %q", id)
		}
	}
	// Should NOT mention S3 upload — that path is intentionally
	// excluded from the GUI per the 0.2.31 scope.
	for _, banned := range []string{"upload-s3", "destination", `"s3"`} {
		if strings.Contains(s, banned) {
			t.Errorf("embedded UI should NOT mention %q — S3 upload was removed", banned)
		}
	}
}

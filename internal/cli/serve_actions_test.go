package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// TestAPIUpload_SkipsAlreadyUploaded — when every selected file
// has a sidecar recording irods_uploaded_at, the response should
// be a friendly "nothing to upload" note (200), not a refusal.
// No iron is invoked.
func TestAPIUpload_SkipsAlreadyUploaded(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	// File + sidecar with an upload recorded.
	if err := os.WriteFile(filepath.Join(root, "a.tsv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecarContent := "" +
		"sha256=abc\nsize=1\n" +
		"irods_uploaded_at=2026-06-20T10:30:00Z\n" +
		"irods_path=/zone/coll/a.tsv\nirods_status=uploaded\n"
	if err := os.WriteFile(filepath.Join(root, "a.tsv.mus"),
		[]byte(sidecarContent), 0o644); err != nil {
		t.Fatal(err)
	}
	w := postJSON(apiUpload, map[string]any{
		"paths": []string{"a.tsv"}, "message": "smoke",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with friendly note; got %d; body: %s",
			w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "nothing to upload") {
		t.Errorf("expected 'nothing to upload' note; got: %s", w.Body)
	}
	if !strings.Contains(w.Body.String(), "a.tsv") {
		t.Errorf("expected the skipped path named in the response; got: %s",
			w.Body)
	}
}

// TestFilterAlreadyUploaded_Unit — the pure helper, in isolation.
// Covers four cases: an unchanged uploaded file (skipped), a
// never-uploaded file (uploaded), a directory (always passes), and an
// uploaded-then-EDITED file whose sidecar is now stale (must upload —
// the regression this test guards).
func TestFilterAlreadyUploaded_Unit(t *testing.T) {
	root := t.TempDir()

	// writeSidecarMatching writes a .mus recording the file's CURRENT
	// size + mtime plus an upload timestamp, so doc.Stale() sees an
	// unchanged file.
	writeUploadedSidecar := func(file string) {
		st, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		mtime := st.ModTime().UTC().Truncate(time.Second).Format(time.RFC3339)
		body := "sha256=a\n" +
			"size=" + strconv.FormatInt(st.Size(), 10) + "\n" +
			"mtime=" + mtime + "\n" +
			"irods_uploaded_at=2026-06-20T10:00:00Z\n"
		if err := os.WriteFile(file+".mus", []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// (1) uploaded + unchanged → skipped.
	unchanged := filepath.Join(root, "u.tsv")
	if err := os.WriteFile(unchanged, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeUploadedSidecar(unchanged)

	// (2) never uploaded → toUpload.
	fresh := filepath.Join(root, "f.tsv")
	if err := os.WriteFile(fresh, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	// (3) directory → always passes through.
	dirPath := filepath.Join(root, "d")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}

	// (4) uploaded, THEN edited so size/mtime no longer match the
	// sidecar → must upload, not skip.
	edited := filepath.Join(root, "e.tsv")
	if err := os.WriteFile(edited, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeUploadedSidecar(edited)
	// Change the contents (different length) + bump mtime so Stale trips
	// on either signal.
	if err := os.WriteFile(edited, []byte("edited-and-longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(edited, future, future); err != nil {
		t.Fatal(err)
	}

	toUpload, skipped := filterAlreadyUploaded([]string{unchanged, fresh, dirPath, edited})

	if len(skipped) != 1 || skipped[0] != unchanged {
		t.Errorf("expected only the unchanged uploaded file skipped; got skipped=%v", skipped)
	}
	wantUpload := map[string]bool{fresh: true, dirPath: true, edited: true}
	if len(toUpload) != len(wantUpload) {
		t.Fatalf("expected %d in toUpload; got %v", len(wantUpload), toUpload)
	}
	for _, p := range toUpload {
		if !wantUpload[p] {
			t.Errorf("unexpected path in toUpload: %s", p)
		}
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

// TestAPIUpload_AcceptsForce — the force field decodes and the request
// still runs the shared validation chain (empty message → 400).
func TestAPIUpload_AcceptsForce(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiUpload, map[string]any{
		"paths": []string{""}, "force": true,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected message-required error even with force; got %d", w.Code)
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
	// Spot-check load-bearing IDs the JS reaches for. The 0.2.47 header
	// rework decouples linking from data_project: the experiment is
	// linked first (hdr-link → "Link ELN experiment" / "Update ELN
	// data"), then data_project is set separately (hdr-setdp → "Set /
	// Update data_project"). The old one-shot "Suggest" button is gone;
	// suggestion now fires automatically after linking.
	for _, id := range []string{
		"id=\"tree\"", "id=\"filter\"", "id=\"cfg-form\"",
		"id=\"cfg-key\"", "id=\"cfg-value-host\"",
		"id=\"upload-go\"", "id=\"upload-dryrun\"", "id=\"upload-pack\"", "id=\"upload-force\"",
		"id=\"hdr-expid\"", "id=\"hdr-dp\"", "id=\"hdr-link\"", "id=\"hdr-setdp\"",
		"id=\"hdr-hint\"",
		"id=\"auth-banner\"", "id=\"conn-overlay\"",
		"id=\"eln-hdr-badge\"", "id=\"irods-hdr-badge\"",
		"id=\"upload-log\"", "id=\"tree-refresh\"",
		"id=\"upload-block-dialog\"", "id=\"upload-block-list\"",
		"id=\"statusbar\"", "id=\"statusbar-msg\"",
		"Upload to MANGO/iRODS", "class=\"info\"",
		"/api/preflight", "/api/upload", "/api/known-keys?manual=true",
		"/api/config/unset", "/api/eln/suggest-dp", "/api/eln/set-dp",
		"/api/eln/link", "chip-x",
		"/api/auth-status", "/api/auth-refresh", "/api/ping",
	} {
		if !strings.Contains(s, id) {
			t.Errorf("embedded UI missing required element / endpoint: %q", id)
		}
	}
	// Removed elements — regression guard against a partial revert
	// bringing them back. The old ELN block + disable/enable buttons
	// are gone by design; hdr-suggest was replaced by hdr-setdp.
	for _, gone := range []string{
		"id=\"eln-block\"", "id=\"eln-disable\"", "id=\"eln-enable\"",
		"id=\"hdr-suggest\"", "id=\"checks\"", "Upload to iRODS",
	} {
		if strings.Contains(s, gone) {
			t.Errorf("embedded UI still contains removed element: %q", gone)
		}
	}
	// The [hidden] override must survive — without it authed users see
	// the auth banner and a dead connection overlay (both use `hidden`).
	if !strings.Contains(s, "[hidden]") {
		t.Errorf("embedded UI missing the `[hidden] { display:none }` override")
	}
	// Should NOT mention S3 upload — that path is intentionally
	// excluded from the GUI per the 0.2.31 scope.
	for _, banned := range []string{"upload-s3", "destination", `"s3"`} {
		if strings.Contains(s, banned) {
			t.Errorf("embedded UI should NOT mention %q — S3 upload was removed", banned)
		}
	}
}

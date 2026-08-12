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

// withServeRoot temporarily points the package-level serveRoot at
// `root` and returns a cleanup. Tests that touch the api/* handlers
// need this because serve_handlers.go gates every path-resolving
// call against the captured root.
func withServeRoot(t *testing.T, root string) func() {
	t.Helper()
	prev := serveRoot
	serveRoot = root
	return func() { serveRoot = prev }
}

func TestResolveDirParam_AcceptsRoot(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	for _, in := range []string{"", "."} {
		abs, rel, err := resolveDirParam(in)
		if err != nil {
			t.Errorf("resolveDirParam(%q): %v", in, err)
			continue
		}
		if abs != root {
			t.Errorf("resolveDirParam(%q) abs: %q, want %q", in, abs, root)
		}
		if rel != "" {
			t.Errorf("resolveDirParam(%q) rel: %q, want empty", in, rel)
		}
	}
}

func TestResolveDirParam_RefusesAbsolutePath(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	for _, in := range []string{"/", "/etc", "/etc/passwd"} {
		_, _, err := resolveDirParam(in)
		if err == nil {
			t.Errorf("resolveDirParam(%q): expected absolute-path rejection", in)
		}
	}
}

func TestResolveDirParam_RefusesEscape(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	for _, in := range []string{"..", "../..", "../etc"} {
		_, _, err := resolveDirParam(in)
		if err == nil {
			t.Errorf("resolveDirParam(%q): expected escape error", in)
		}
	}
}

func TestResolveDirParam_AcceptsSubdir(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "data", "raw")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer withServeRoot(t, root)()
	abs, rel, err := resolveDirParam("data/raw")
	if err != nil {
		t.Fatal(err)
	}
	if abs != subdir {
		t.Errorf("abs: got %q, want %q", abs, subdir)
	}
	if rel != "data/raw" {
		t.Errorf("rel: got %q, want %q", rel, "data/raw")
	}
}

func TestAPITree_HappyPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.h5ad"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.h5ad.mus"), []byte("sha=x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "mus.cfg"),
		[]byte("data_project=Fiers2025\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer withServeRoot(t, root)()

	r := httptest.NewRequest(http.MethodGet, "/api/tree?dir=", nil)
	w := httptest.NewRecorder()
	apiTree(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp struct {
		Dir     string      `json:"dir"`
		Entries []treeEntry `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// scripts/ is a dir with mus.cfg → HasMusCfg true.
	// dirs come first by sort policy.
	if len(resp.Entries) != 3 {
		t.Fatalf("expected 3 entries; got %d: %+v", len(resp.Entries), resp.Entries)
	}
	if resp.Entries[0].Name != "scripts" || resp.Entries[0].Kind != "dir" {
		t.Errorf("dir-first ordering broken: %+v", resp.Entries)
	}
	if !resp.Entries[0].HasMusCfg {
		t.Errorf("scripts/ should be flagged has_mus_cfg=true")
	}
	// sidecar classified separately.
	var sawSidecar bool
	for _, e := range resp.Entries {
		if e.Kind == "sidecar" && e.Name == "data.h5ad.mus" {
			sawSidecar = true
		}
	}
	if !sawSidecar {
		t.Errorf(".mus sidecar should be kind=sidecar in the listing")
	}
}

// TestAPITree_UploadedBadge — a file whose sidecar has
// irods.uploaded_at set should surface that on the tree entry,
// driving the UI's ✓ badge. Plain files (no sidecar) and files
// with an empty IRODS block must NOT carry the badge.
func TestAPITree_UploadedBadge(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	// File 1: data.tsv + a sidecar recording an upload.
	if err := os.WriteFile(filepath.Join(root, "data.tsv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecarContent := "" +
		"sha256=abc\n" +
		"size=1\n" +
		"irods_uploaded_at=2026-06-20T10:30:00Z\n" +
		"irods_path=/zone/coll/data.tsv\n" +
		"irods_status=uploaded\n"
	if err := os.WriteFile(filepath.Join(root, "data.tsv.mus"),
		[]byte(sidecarContent), 0o644); err != nil {
		t.Fatal(err)
	}
	// File 2: plain.tsv, no sidecar → no badge.
	if err := os.WriteFile(filepath.Join(root, "plain.tsv"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/tree?dir=", nil)
	w := httptest.NewRecorder()
	apiTree(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d; body: %s", w.Code, w.Body)
	}
	var resp struct {
		Entries []treeEntry `json:"entries"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	got := map[string]treeEntry{}
	for _, e := range resp.Entries {
		got[e.Name] = e
	}
	if got["data.tsv"].UploadedAt == "" {
		t.Errorf("data.tsv should surface uploaded_at; got %+v", got["data.tsv"])
	}
	if got["data.tsv"].IRODSPath != "/zone/coll/data.tsv" {
		t.Errorf("data.tsv irods_path: got %q", got["data.tsv"].IRODSPath)
	}
	if got["plain.tsv"].UploadedAt != "" {
		t.Errorf("plain.tsv has no sidecar — must not have uploaded_at; got %+v", got["plain.tsv"])
	}
}

func TestAPITree_RefusesEscape(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	r := httptest.NewRequest(http.MethodGet, "/api/tree?dir=../etc", nil)
	w := httptest.NewRecorder()
	apiTree(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("escape attempt: status %d, want 400", w.Code)
	}
}

func TestAPICascade_FlatRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mus.cfg"),
		[]byte("data_project=Fiers2025\norganism=human\ntissue=cortex,hippocampus\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	defer withServeRoot(t, root)()

	r := httptest.NewRequest(http.MethodGet, "/api/cascade?dir=", nil)
	w := httptest.NewRecorder()
	apiCascade(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp struct {
		Values []cascadeKey `json:"values"`
		Files  []string     `json:"files"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	keys := map[string]cascadeKey{}
	for _, v := range resp.Values {
		keys[v.Key] = v
	}
	if keys["data_project"].Value != "Fiers2025" {
		t.Errorf("data_project: %+v", keys["data_project"])
	}
	if keys["organism"].Value != "human" {
		t.Errorf("organism: %+v", keys["organism"])
	}
	tv := keys["tissue"]
	if !tv.IsList {
		t.Errorf("tissue should be is_list=true: %+v", tv)
	}
	if tv.Value != "cortex,hippocampus" {
		t.Errorf("tissue value: got %q, want comma-joined", tv.Value)
	}
	if len(resp.Files) == 0 {
		t.Error("expected at least one contributing cfg file")
	}
	// Single-file cascade → every key is local.
	for _, v := range resp.Values {
		if !v.Local {
			t.Errorf("flat-root cfg: %q should be local=true; got false", v.Key)
		}
	}
}

// TestAPICascade_LocalVsInherited — two-level cascade with the same
// keys defined at both levels in different combinations. The local
// chip is the deepest file; ancestor-only keys are inherited.
func TestAPICascade_LocalVsInherited(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Parent sets data_project + organism; sub overrides organism +
	// adds tissue. Effective cascade at sub: data_project (inherited),
	// organism (local — overridden), tissue (local — new).
	if err := os.WriteFile(filepath.Join(root, "mus.cfg"),
		[]byte("data_project=Fiers2025\norganism=mouse\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "mus.cfg"),
		[]byte("organism=human\ntissue=cortex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer withServeRoot(t, root)()

	r := httptest.NewRequest(http.MethodGet, "/api/cascade?dir=sub", nil)
	w := httptest.NewRecorder()
	apiCascade(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d; body: %s", w.Code, w.Body)
	}
	var resp struct {
		Values []cascadeKey `json:"values"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	got := map[string]bool{}
	for _, v := range resp.Values {
		got[v.Key] = v.Local
	}
	if got["data_project"] {
		t.Errorf("data_project should be inherited (local=false); got local=true")
	}
	if !got["organism"] {
		t.Errorf("organism should be local (overridden in sub/mus.cfg); got local=false")
	}
	if !got["tissue"] {
		t.Errorf("tissue should be local (only in sub/mus.cfg); got local=false")
	}
}

func TestShouldHideInTree(t *testing.T) {
	hide := []string{".git", ".DS_Store", ".vscode"}
	show := []string{"data.tsv", "mus.cfg", ".env" /* legacy mus config */, "data.h5ad.mus"}
	for _, n := range hide {
		if !shouldHideInTree(n) {
			t.Errorf("expected %q to be hidden", n)
		}
	}
	for _, n := range show {
		if shouldHideInTree(n) {
			t.Errorf("expected %q to be visible", n)
		}
	}
}

func TestAPIVocab_KnownField(t *testing.T) {
	// Use the embedded vocab — works even when offline.
	r := httptest.NewRequest(http.MethodGet, "/api/vocab/organism", nil)
	w := httptest.NewRecorder()
	apiVocab(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var resp struct {
		Field  string   `json:"field"`
		Vocab  bool     `json:"vocab"`
		Values []string `json:"values"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Vocab {
		t.Errorf("organism should be a vocab-controlled field")
	}
	if len(resp.Values) == 0 {
		t.Errorf("expected non-empty values for organism")
	}
}

func TestAPIVocab_UncontrolledFieldEmptyOK(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/vocab/note", nil)
	w := httptest.NewRecorder()
	apiVocab(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"vocab": false`) {
		t.Errorf("expected vocab:false for uncontrolled field; got %s", w.Body.String())
	}
}

func TestAPIKnownKeys_ListsKnownCfgKeys(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/known-keys", nil)
	w := httptest.NewRecorder()
	apiKnownKeys(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d; body: %s", w.Code, w.Body)
	}
	var resp struct {
		Keys []struct {
			Key   string `json:"key"`
			Vocab bool   `json:"vocab"`
		} `json:"keys"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Keys) < 10 {
		t.Errorf("expected many known keys; got %d", len(resp.Keys))
	}
	// Spot-check a few load-bearing ones.
	want := map[string]bool{"data_project": false, "organism": true, "de_dataset_id": false}
	for _, k := range resp.Keys {
		if expectedVocab, ok := want[k.Key]; ok {
			if k.Vocab != expectedVocab {
				t.Errorf("%s.vocab = %v, want %v", k.Key, k.Vocab, expectedVocab)
			}
			delete(want, k.Key)
		}
	}
	for k := range want {
		t.Errorf("expected known-key %q in response", k)
	}
}

// TestAPIKnownKeys_GroupingAssignsLoadBearingKeys — every manual
// key gets a UI group label. Verifies the load-bearing keys land
// in the right buckets so the <select> renders sensibly.
func TestAPIKnownKeys_GroupingAssignsLoadBearingKeys(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/known-keys?manual=true", nil)
	w := httptest.NewRecorder()
	apiKnownKeys(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var resp struct {
		Keys []struct {
			Key   string `json:"key"`
			Group string `json:"group"`
		} `json:"keys"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	groupOf := map[string]string{}
	for _, k := range resp.Keys {
		groupOf[k.Key] = k.Group
	}
	want := map[string]string{
		"organism":                    "Biology",
		"cell_type":                   "Biology",
		"tissue":                      "Biology",
		"title":                       "Provenance",
		"access":                      "Provenance",
		"doi":                         "References",
		"tag":                         "Tags & free text",
		"note":                        "Tags & free text",
		"cell_type_ontology_term_id":  "Ontology IDs",
	}
	for k, expected := range want {
		if got := groupOf[k]; got != expected {
			t.Errorf("%s: group = %q, want %q", k, got, expected)
		}
	}
	// Every key must have a non-empty group — defensive against
	// future keys that bypass groupForConfigKey.
	for _, k := range resp.Keys {
		if k.Group == "" {
			t.Errorf("%s: group is empty (groupForConfigKey returned no value)", k.Key)
		}
	}
}

// TestAPIKnownKeys_ManualFilter — with ?manual=true the response
// must hide auto-managed keys (eln_*/de_*/irods_*/h5ad_*/data_project)
// and surface only the biology / provenance fields a depositor
// realistically sets by hand.
func TestAPIKnownKeys_ManualFilter(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/known-keys?manual=true", nil)
	w := httptest.NewRecorder()
	apiKnownKeys(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d; body: %s", w.Code, w.Body)
	}
	var resp struct {
		Keys []struct {
			Key   string `json:"key"`
			Vocab bool   `json:"vocab"`
		} `json:"keys"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	got := map[string]bool{}
	for _, k := range resp.Keys {
		got[k.Key] = true
	}
	// MUST be present.
	for _, want := range []string{"organism", "tissue", "cell_type", "technology", "tag", "note"} {
		if !got[want] {
			t.Errorf("manual filter missing %q", want)
		}
	}
	// MUST NOT be present.
	for _, banned := range []string{
		"data_project",
		"eln_experiment_id", "eln_disabled",
		"de_dataset_id", "de_format",
		"irods_home", "irods_group",
		"h5ad_n_cells",
	} {
		if got[banned] {
			t.Errorf("manual filter should hide %q (auto-managed by another panel)", banned)
		}
	}
}

// TestAPIELNSuggestDataProject_RejectsBadInputs — the suggest
// endpoint shares experiment-id validation with /api/eln/link.
// Don't hit the real ELN API in unit tests (no creds); just
// confirm input validation refuses the wrong shapes.
func TestAPIELNSuggestDataProject_RejectsBadInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"missing-id", map[string]any{}, http.StatusBadRequest},
		{"bad-id", map[string]any{"experiment_id": "not-a-number"}, http.StatusBadRequest},
		{"get-method", nil, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r *http.Request
			if tc.body == nil {
				r = httptest.NewRequest(http.MethodGet, "/api/eln/suggest-dp", nil)
			} else {
				buf, _ := json.Marshal(tc.body)
				r = httptest.NewRequest(http.MethodPost, "/api/eln/suggest-dp",
					strings.NewReader(string(buf)))
				r.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			apiELNSuggestDataProject(w, r)
			if w.Code != tc.want {
				t.Errorf("%s: status %d, want %d; body: %s",
					tc.name, w.Code, tc.want, w.Body)
			}
		})
	}
}

func TestAPIVocab_RefusesSlashesInField(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/vocab/foo/bar", nil)
	w := httptest.NewRecorder()
	apiVocab(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for slash in field; got %d", w.Code)
	}
}

// --- write endpoints ---------------------------------------------------------

// postJSON is a tiny helper for invoking the write handlers from tests.
func postJSON(handler http.HandlerFunc, body any) *httptest.ResponseRecorder {
	buf, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(buf)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func TestAPIConfigSet_WritesUncontrolledKey(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiConfigSet, map[string]any{
		"dir": "", "key": "data_project", "value": "Fiers2025",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d; body: %s", w.Code, w.Body)
	}
	data, err := os.ReadFile(filepath.Join(root, "mus.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "data_project=Fiers2025") {
		t.Errorf("mus.cfg missing expected key=value; got: %s", data)
	}
}

func TestAPIConfigSet_NormalisesVocab(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	// "HuMaN" should round-trip to "human" via the vocab resolver.
	w := postJSON(apiConfigSet, map[string]any{
		"dir": "", "key": "organism", "value": "HuMaN",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d; body: %s", w.Code, w.Body)
	}
	var resp struct {
		Key, Value string
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Value != "human" {
		t.Errorf("value normalisation: got %q, want %q", resp.Value, "human")
	}
}

func TestAPIConfigSet_RejectsOffVocabWithoutForce(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiConfigSet, map[string]any{
		"dir": "", "key": "organism", "value": "klingon",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for off-vocab; got %d; body: %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "mus.cfg")); err == nil {
		t.Errorf("mus.cfg should NOT have been written on vocab rejection")
	}
}

func TestAPIConfigSet_RejectsGetMethod(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	r := httptest.NewRequest(http.MethodGet, "/api/config/set", nil)
	w := httptest.NewRecorder()
	apiConfigSet(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should be rejected; got %d", w.Code)
	}
}

func TestAPIConfigSet_RefusesEscape(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiConfigSet, map[string]any{
		"dir": "../etc", "key": "data_project", "value": "Fiers2025",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("escape attempt: status %d, want 400", w.Code)
	}
}

func TestAPIConfigUnset_RemovesKey(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mus.cfg"),
		[]byte("data_project=Fiers2025\norganism=human\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer withServeRoot(t, root)()
	w := postJSON(apiConfigUnset, map[string]any{"dir": "", "key": "organism"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d; body: %s", w.Code, w.Body)
	}
	data, _ := os.ReadFile(filepath.Join(root, "mus.cfg"))
	if strings.Contains(string(data), "organism=") {
		t.Errorf("organism should have been removed; got: %s", data)
	}
	if !strings.Contains(string(data), "data_project=Fiers2025") {
		t.Errorf("data_project should remain; got: %s", data)
	}
}

func TestAPIConfigUnset_RejectsEmptyKey(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiConfigUnset, map[string]any{"dir": "", "key": ""})
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty key: status %d, want 400", w.Code)
	}
}

func TestAPIELNDisable_WritesFlag(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiELNDisable, map[string]any{"dir": ""})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d; body: %s", w.Code, w.Body)
	}
	data, err := os.ReadFile(filepath.Join(root, "mus.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "eln_disabled=true") {
		t.Errorf("eln_disabled flag missing; got: %s", data)
	}
}

// data_project is OPTIONAL for linking as of 0.2.47 — the GUI links the
// experiment first and sets data_project separately. A missing
// data_project must therefore NOT be rejected at the validation gate; the
// request proceeds to the ELN lookup (which fails here with no real ELN,
// proving it got past the old 400).
func TestAPIELNLink_DataProjectOptional(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiELNLink, map[string]any{
		"dir":           "",
		"experiment_id": "12345",
	})
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "data_project is required") {
		t.Fatalf("data_project should be optional now; got 400: %s", w.Body)
	}
}

// set-dp requires a data_project.
func TestAPIELNSetDataProject_RequiresValue(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiELNSetDataProject, map[string]any{"dir": "", "experiment_id": "12345"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (data_project missing); got %d; body: %s", w.Code, w.Body)
	}
}

// set-dp rejects a path-traversal data_project before writing anything.
func TestAPIELNSetDataProject_RejectsBadValue(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiELNSetDataProject, map[string]any{
		"dir": "", "experiment_id": "12345", "data_project": "../etc/passwd",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (bad data_project); got %d; body: %s", w.Code, w.Body)
	}
}

func TestAPIELNLink_RejectsBadDataProject(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiELNLink, map[string]any{
		"dir":           "",
		"experiment_id": "12345",
		"data_project":  "../etc/passwd",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (bad data_project); got %d; body: %s", w.Code, w.Body)
	}
}

func TestAPIELNLink_RejectsGetMethod(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	r := httptest.NewRequest(http.MethodGet, "/api/eln/link", nil)
	w := httptest.NewRecorder()
	apiELNLink(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should be rejected; got %d", w.Code)
	}
}

func TestAPIELNLink_RejectsBadExperimentID(t *testing.T) {
	root := t.TempDir()
	defer withServeRoot(t, root)()
	w := postJSON(apiELNLink, map[string]any{
		"dir":           "",
		"experiment_id": "not-a-number",
		"data_project":  "Fiers2025",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (bad experiment id); got %d; body: %s", w.Code, w.Body)
	}
}

func TestAPIELNEnable_RemovesFlag(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mus.cfg"),
		[]byte("eln_disabled=true\ndata_project=Fiers2025\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer withServeRoot(t, root)()
	w := postJSON(apiELNEnable, map[string]any{"dir": ""})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d; body: %s", w.Code, w.Body)
	}
	data, _ := os.ReadFile(filepath.Join(root, "mus.cfg"))
	if strings.Contains(string(data), "eln_disabled") {
		t.Errorf("eln_disabled should have been removed; got: %s", data)
	}
}

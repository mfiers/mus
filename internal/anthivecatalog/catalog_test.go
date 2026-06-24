package anthivecatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeFetcher returns canned bytes and/or a sentinel error for the
// next GetObject. Tests assert against the arguments captured into
// LastBucket/LastKey.
type fakeFetcher struct {
	body       []byte
	err        error
	LastBucket string
	LastKey    string
	Calls      int
}

func (f *fakeFetcher) GetObject(_ context.Context, bucket, key string) ([]byte, error) {
	f.Calls++
	f.LastBucket = bucket
	f.LastKey = key
	if f.err != nil {
		return nil, f.err
	}
	return f.body, nil
}

// sampleManifest is the schema from anthive4's message #209, slightly
// trimmed but with the exact shape we'll see in production.
const sampleManifest = `{
  "version": 1,
  "updated_at": "2026-06-24T16:53:23Z",
  "data_projects": {
    "Sun2026": {
      "group_id": "S26",
      "datasets": {
        "AQR": {"basename": "adata_qc_removed_clusters.h5ad", "title": "...", "inactive": false},
        "AQB": {"basename": "adata_qc_removed_clusters_blob.h5ad", "title": "...", "inactive": false},
        "AQP": {"basename": "adata_qc_removed_clusters_blob_PDT.h5ad", "title": "...", "inactive": false},
        "AQN": {"basename": "adata_qc_removed_clusters_nocycling.h5ad", "title": "...", "inactive": false},
        "DUN": {"basename": "Di_2026_unprocessed.h5ad", "title": "...", "inactive": false},
        "OLD": {"basename": "old_dataset.h5ad", "title": "...", "inactive": true}
      }
    },
    "OtherProj": {
      "group_id": "OP1",
      "datasets": {
        "FOO": {"basename": "foo.h5ad", "title": "...", "inactive": false}
      }
    }
  }
}`

func newLoadOpts(t *testing.T, fetcher Fetcher, body string) LoadOptions {
	cache := t.TempDir()
	return LoadOptions{
		Fetcher:  fetcher,
		Bucket:   "anthive4a",
		CacheDir: cache,
	}
}

// TestLoad_FetchOnFirstCall — no cache, fetcher returns valid bytes
// → manifest parsed correctly.
func TestLoad_FetchOnFirstCall(t *testing.T) {
	fetcher := &fakeFetcher{body: []byte(sampleManifest)}
	opts := newLoadOpts(t, fetcher, sampleManifest)
	mf, err := Load(context.Background(), opts, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if fetcher.Calls != 1 {
		t.Errorf("expected 1 fetch call, got %d", fetcher.Calls)
	}
	if fetcher.LastBucket != "anthive4a" {
		t.Errorf("bucket: got %q", fetcher.LastBucket)
	}
	if fetcher.LastKey != DefaultManifestKey {
		t.Errorf("key: got %q, want %q", fetcher.LastKey, DefaultManifestKey)
	}
	if mf.Version != 1 {
		t.Errorf("version: got %d, want 1", mf.Version)
	}
	if len(mf.DataProjects) != 2 {
		t.Errorf("expected 2 data_projects; got %d", len(mf.DataProjects))
	}
	if mf.DataProjects["Sun2026"].GroupID != "S26" {
		t.Errorf("group_id wrong")
	}
}

// TestLoad_UsesFreshCache — second call within TTL should NOT
// hit the fetcher.
func TestLoad_UsesFreshCache(t *testing.T) {
	fetcher := &fakeFetcher{body: []byte(sampleManifest)}
	opts := newLoadOpts(t, fetcher, sampleManifest)
	_, err := Load(context.Background(), opts, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), opts, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if fetcher.Calls != 1 {
		t.Errorf("expected fresh cache to satisfy second call; got %d fetch calls", fetcher.Calls)
	}
}

// TestLoad_StaleFallback — fetcher fails on refetch but a stale
// cache exists → use stale + emit warning.
func TestLoad_StaleFallback(t *testing.T) {
	cache := t.TempDir()
	cachePath := filepath.Join(cache, "anthive_catalog.json")
	if err := os.WriteFile(cachePath, []byte(sampleManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force the cache to look stale.
	stale := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(cachePath, stale, stale); err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{err: errors.New("simulated network blip")}
	var warn bytes.Buffer
	mf, err := Load(context.Background(), LoadOptions{
		Fetcher:  fetcher,
		Bucket:   "anthive4a",
		CacheDir: cache,
	}, &warn)
	if err != nil {
		t.Fatalf("expected stale-fallback success, got: %v", err)
	}
	if mf.Version != 1 {
		t.Errorf("stale manifest version wrong")
	}
	if !strings.Contains(warn.String(), "stale cache") {
		t.Errorf("expected stale-cache warning; got: %s", warn.String())
	}
}

// TestLoad_NoCacheNoFetch_Errors — clean state + fetcher fails →
// must return a useful error (not a stale-cache success).
func TestLoad_NoCacheNoFetch_Errors(t *testing.T) {
	fetcher := &fakeFetcher{err: errors.New("S3 unreachable")}
	opts := newLoadOpts(t, fetcher, "")
	_, err := Load(context.Background(), opts, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error when no cache + fetch fails")
	}
	if !strings.Contains(err.Error(), "S3 unreachable") {
		t.Errorf("error should propagate fetcher cause; got: %v", err)
	}
}

// TestLoad_RejectsUnknownVersion — manifest has version: 2, mus knows
// only v1. Refuse with upgrade hint.
func TestLoad_RejectsUnknownVersion(t *testing.T) {
	mf := Manifest{Version: 2, DataProjects: map[string]DataProject{}}
	body, _ := json.Marshal(mf)
	fetcher := &fakeFetcher{body: body}
	opts := newLoadOpts(t, fetcher, "")
	_, err := Load(context.Background(), opts, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected version-mismatch error")
	}
	if !strings.Contains(err.Error(), "upgrade mus") {
		t.Errorf("error should hint at upgrade; got: %v", err)
	}
}

// TestResolveDatasetID_ExactMatch — happy path.
func TestResolveDatasetID_ExactMatch(t *testing.T) {
	mf := mustParse(t, sampleManifest)
	r := mf.ResolveDatasetID("Sun2026", "AQR")
	if r.Status != ResolveOK {
		t.Errorf("status: got %v, want ResolveOK", r.Status)
	}
	if r.Match == nil || r.Match.Basename != "adata_qc_removed_clusters.h5ad" {
		t.Errorf("match wrong: %+v", r.Match)
	}
}

// TestResolveDatasetID_Inactive — match exists but flagged inactive.
func TestResolveDatasetID_Inactive(t *testing.T) {
	mf := mustParse(t, sampleManifest)
	r := mf.ResolveDatasetID("Sun2026", "OLD")
	if r.Status != ResolveInactive {
		t.Errorf("status: got %v, want ResolveInactive", r.Status)
	}
	if r.Match == nil || !r.Match.Inactive {
		t.Errorf("expected an Inactive match; got %+v", r.Match)
	}
}

// TestResolveDatasetID_IsGroupID — the bug from the report: depositor
// wrote S26 (the data_project's group_id) instead of a file_id.
func TestResolveDatasetID_IsGroupID(t *testing.T) {
	mf := mustParse(t, sampleManifest)
	r := mf.ResolveDatasetID("Sun2026", "S26")
	if r.Status != ResolveIsGroupID {
		t.Errorf("status: got %v, want ResolveIsGroupID", r.Status)
	}
	if r.Hint.GroupID != "S26" {
		t.Errorf("hint group_id: got %q", r.Hint.GroupID)
	}
	// Should list the file_ids the depositor probably meant.
	want := []string{"AQB", "AQN", "AQP", "AQR", "DUN", "OLD"}
	if got := r.Hint.AvailableInProj; !equalSlice(got, want) {
		t.Errorf("available_in_proj: got %v, want %v", got, want)
	}
}

// TestResolveDatasetID_CrossProject — id exists, but in a different
// data_project.
func TestResolveDatasetID_CrossProject(t *testing.T) {
	mf := mustParse(t, sampleManifest)
	r := mf.ResolveDatasetID("Sun2026", "FOO")
	if r.Status != ResolveCrossProject {
		t.Errorf("status: got %v, want ResolveCrossProject", r.Status)
	}
	if r.Hint.ForeignProject != "OtherProj" {
		t.Errorf("foreign_project: got %q", r.Hint.ForeignProject)
	}
}

// TestResolveDatasetID_Unknown_Suggests — id matches nothing; we
// should get nearest-by-Levenshtein suggestions.
func TestResolveDatasetID_Unknown_Suggests(t *testing.T) {
	mf := mustParse(t, sampleManifest)
	r := mf.ResolveDatasetID("Sun2026", "AQX") // typo of AQR / AQB / AQP / AQN
	if r.Status != ResolveUnknown {
		t.Errorf("status: got %v, want ResolveUnknown", r.Status)
	}
	if len(r.Hint.Suggestions) == 0 {
		t.Errorf("expected suggestions, got none")
	}
	// AQR/AQB/AQP/AQN are all distance-1 from AQX; one of them
	// should be in the top 3.
	hit := false
	for _, s := range r.Hint.Suggestions {
		if strings.HasPrefix(s, "AQ") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected an AQ* in suggestions; got %v", r.Hint.Suggestions)
	}
}

// TestResolveDatasetID_DataProjectNew — data_project absent entirely.
func TestResolveDatasetID_DataProjectNew(t *testing.T) {
	mf := mustParse(t, sampleManifest)
	r := mf.ResolveDatasetID("BrandNewProj2027", "AQR")
	if r.Status != ResolveDataProjectNew {
		t.Errorf("status: got %v, want ResolveDataProjectNew", r.Status)
	}
}

// TestResolveBasename — co-ingest-style lookup by basename.
func TestResolveBasename(t *testing.T) {
	mf := mustParse(t, sampleManifest)
	r := mf.ResolveBasename("Sun2026", "adata_qc_removed_clusters.h5ad")
	if r.Status != ResolveOK {
		t.Errorf("status: got %v, want ResolveOK", r.Status)
	}
}

func TestResolveBasename_Unknown(t *testing.T) {
	mf := mustParse(t, sampleManifest)
	r := mf.ResolveBasename("Sun2026", "doesnt_exist.h5ad")
	if r.Status != ResolveUnknown {
		t.Errorf("status: got %v, want ResolveUnknown", r.Status)
	}
}

// TestLevenshtein — sanity check on the inline distance impl.
func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"AQR", "AQX", 1},
		{"kitten", "sitting", 3},
		{"", "abc", 3},
		{"abc", "", 3},
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// --- helpers ---------------------------------------------------------------

func mustParse(t *testing.T, raw string) *Manifest {
	t.Helper()
	mf, err := parseManifest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return mf
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

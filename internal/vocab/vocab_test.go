package vocab

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedVocabularyParses(t *testing.T) {
	v, err := Parse(embeddedYAML)
	if err != nil {
		t.Fatalf("embedded vocabulary failed to parse: %v", err)
	}
	if v.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", v.SchemaVersion)
	}
	// Spot-check the required-field list matches §4 of the spec.
	for _, want := range []string{"organism", "celltype", "technology",
		"preparation", "tissue", "internal", "accession"} {
		if !v.IsRequired(want) {
			t.Errorf("required field %q missing; got Required=%v",
				want, v.Required)
		}
	}
	if !v.ValueAllowed("organism", "human") {
		t.Errorf("organism=human should be allowed")
	}
}

// TestEmbeddedAndCanonicalAreIdentical pins the no-drift invariant:
// vocabulary/main.yaml at the repo root and the build-time
// internal/vocab/embedded.yaml MUST be byte-equal. If you edit the
// canonical, run `make sync-vocab` (or just copy the file).
func TestEmbeddedAndCanonicalAreIdentical(t *testing.T) {
	canonical, err := os.ReadFile("../../vocabulary/main.yaml")
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if !bytes.Equal(canonical, embeddedYAML) {
		t.Fatal("vocabulary/main.yaml and internal/vocab/embedded.yaml " +
			"differ — run `make sync-vocab` (or `cp vocabulary/main.yaml " +
			"internal/vocab/embedded.yaml`).")
	}
}

func TestValueAllowed(t *testing.T) {
	v := &Vocabulary{
		SchemaVersion: 1,
		Required:      []string{"organism"},
		Vocab: map[string][]string{
			"organism": {"human", "mouse"},
		},
	}
	cases := []struct {
		field, value string
		want         bool
	}{
		{"organism", "human", true},
		{"organism", "HUMAN", true},
		{"organism", " human ", true},
		{"organism", "homo_sapiens", false},
		{"freeform", "anything", true},
		{"organism", "human_being", false},
	}
	for _, tc := range cases {
		got := v.ValueAllowed(tc.field, tc.value)
		if got != tc.want {
			t.Errorf("ValueAllowed(%q, %q) = %v, want %v", tc.field, tc.value, got, tc.want)
		}
	}
}

func TestParseRejectsBadSchema(t *testing.T) {
	cases := map[string]string{
		"missing schema_version": "required: [organism]\nvocabulary: {organism: [human]}\n",
		"empty required":         "schema_version: 1\nrequired: []\nvocabulary: {organism: [human]}\n",
		"malformed":              "not: valid:: yaml:",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(raw)); err == nil {
				t.Errorf("expected error for %s", name)
			}
		})
	}
}

func TestLocalCachePathRespectsXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "xdg-data"))
	got := LocalCachePath()
	want := filepath.Join(tmp, "xdg-data", "mus", "h5ad_vocabulary.yaml")
	if got != want {
		t.Errorf("LocalCachePath() = %q, want %q (XDG_DATA_HOME honoured)", got, want)
	}
}

func TestLocalCachePathFallsBackToHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_DATA_HOME", "")
	got := LocalCachePath()
	want := filepath.Join(tmp, ".local", "share", "mus", "h5ad_vocabulary.yaml")
	if got != want {
		t.Errorf("LocalCachePath() = %q, want %q", got, want)
	}
}

// --- Load() — paths through the cascade ------------------------------------

func TestLoadFallsBackToEmbedded(t *testing.T) {
	tmp := t.TempDir()
	res, err := Load(context.Background(), LoadOptions{
		URL:       "http://127.0.0.1:1/never-listens", // unreachable
		CachePath: filepath.Join(tmp, "no-cache.yaml"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "embedded" {
		t.Errorf("Source = %q, want embedded", res.Source)
	}
	if res.Vocabulary == nil || res.Vocabulary.SchemaVersion < 1 {
		t.Errorf("expected non-nil vocabulary, got %+v", res.Vocabulary)
	}
}

func TestLoadUsesCacheWhenFresh(t *testing.T) {
	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "vocab.yaml")
	yaml := `schema_version: 1
required: [organism]
vocabulary:
  organism: [cache_only_marker]
`
	if err := os.WriteFile(cachePath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Load(context.Background(), LoadOptions{
		CachePath: cachePath,
		// No URL → never tries network when cache is fresh.
		URL: "http://127.0.0.1:1/never-listens",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "cache" {
		t.Errorf("Source = %q, want cache", res.Source)
	}
	if !res.Vocabulary.ValueAllowed("organism", "cache_only_marker") {
		t.Errorf("did not use cache: %+v", res.Vocabulary)
	}
}

func TestLoadFetchesFromHTTPSWhenCacheStale(t *testing.T) {
	served := []byte(`schema_version: 1
required: [organism]
vocabulary:
  organism: [served_marker]
`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write(served)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "vocab.yaml")

	res, err := Load(context.Background(), LoadOptions{
		URL:       srv.URL,
		CachePath: cachePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "codeberg" {
		t.Errorf("Source = %q, want codeberg", res.Source)
	}
	if !res.Vocabulary.ValueAllowed("organism", "served_marker") {
		t.Errorf("did not get served content: %+v", res.Vocabulary)
	}
	// And the cache file should now exist with the served body.
	cached, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached, served) {
		t.Errorf("cache not updated; got %s", cached)
	}
}

func TestLoadStaleCacheBeatsEmbedded(t *testing.T) {
	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "vocab.yaml")
	yaml := `schema_version: 1
required: [organism]
vocabulary:
  organism: [stale_cache_marker]
`
	if err := os.WriteFile(cachePath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(cachePath, old, old); err != nil {
		t.Fatal(err)
	}

	res, err := Load(context.Background(), LoadOptions{
		URL:       "http://127.0.0.1:1/never-listens", // network unreachable
		CachePath: cachePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "cache" {
		t.Errorf("Source = %q, want cache (stale-cache > embedded)", res.Source)
	}
	if !res.Vocabulary.ValueAllowed("organism", "stale_cache_marker") {
		t.Errorf("did not use stale cache: %+v", res.Vocabulary)
	}
	hasStaleWarning := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "stale cache") {
			hasStaleWarning = true
		}
	}
	if !hasStaleWarning {
		t.Errorf("expected stale-cache warning, got %v", res.Warnings)
	}
}

func TestLoadRefreshSkipsCacheTTL(t *testing.T) {
	served := []byte(`schema_version: 1
required: [organism]
vocabulary:
  organism: [refreshed_marker]
`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write(served)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "vocab.yaml")
	old := []byte(`schema_version: 1
required: [organism]
vocabulary:
  organism: [old_cache_marker]
`)
	if err := os.WriteFile(cachePath, old, 0o644); err != nil {
		t.Fatal(err)
	}
	// Fresh cache, but caller asks for refresh.
	res, err := Load(context.Background(), LoadOptions{
		URL:         srv.URL,
		CachePath:   cachePath,
		IgnoreCache: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "codeberg" {
		t.Errorf("Source = %q, want codeberg (--refresh should skip cache)", res.Source)
	}
	if !res.Vocabulary.ValueAllowed("organism", "refreshed_marker") {
		t.Errorf("did not fetch refreshed content: %+v", res.Vocabulary)
	}
}

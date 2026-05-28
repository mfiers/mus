package h5ad

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedVocabularyParses(t *testing.T) {
	v, err := ParseVocabulary(embeddedYAML)
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
			t.Errorf("required field %q missing from embedded vocabulary; got Required=%v",
				want, v.Required)
		}
	}
	// Spot-check a value.
	if !v.ValueAllowed("organism", "human") {
		t.Errorf("organism=human should be allowed")
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
		{"organism", "HUMAN", true},        // case-insensitive
		{"organism", " human ", true},      // whitespace trimmed
		{"organism", "homo_sapiens", false}, // not in list
		{"freeform", "anything", true},     // no vocab → allowed
		{"organism", "human_being", false}, // partial match not allowed
	}
	for _, tc := range cases {
		got := v.ValueAllowed(tc.field, tc.value)
		if got != tc.want {
			t.Errorf("ValueAllowed(%q, %q) = %v, want %v", tc.field, tc.value, got, tc.want)
		}
	}
}

func TestParseVocabularyRejectsBadSchema(t *testing.T) {
	cases := map[string]string{
		"missing schema_version": "required: [organism]\nvocabulary: {organism: [human]}\n",
		"empty required":         "schema_version: 1\nrequired: []\nvocabulary: {organism: [human]}\n",
		"malformed":              "not: valid:: yaml:",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseVocabulary([]byte(raw)); err == nil {
				t.Errorf("expected error for %s", name)
			}
		})
	}
}

func TestRemoteVocabPath(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"/zone/home":             "/zone/home/project/h5ad_vocabulary.yaml",
		"/zone/home/":            "/zone/home/project/h5ad_vocabulary.yaml", // trim trailing /
		"/gbiomed/home/BADS":     "/gbiomed/home/BADS/project/h5ad_vocabulary.yaml",
	}
	for home, want := range cases {
		if got := RemoteVocabPath(home); got != want {
			t.Errorf("RemoteVocabPath(%q) = %q, want %q", home, got, want)
		}
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

// --- Load() with no iRODS path: cache → embedded fallback path ---------

func TestLoadFallsBackToEmbeddedWhenNoCacheNoIRODS(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "xdg"))

	res, err := Load(context.Background(), LoadOptions{
		IRODSHome: "", // no iRODS pull
		CachePath: filepath.Join(tmp, "nonexistent.yaml"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "embedded" {
		t.Errorf("Source = %q, want %q", res.Source, "embedded")
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
  organism: [human, mouse]
`
	if err := os.WriteFile(cachePath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Load(context.Background(), LoadOptions{
		CachePath: cachePath,
		Now:       func() time.Time { return time.Now() }, // cache is fresh
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "cache" {
		t.Errorf("Source = %q, want %q (warnings=%v)", res.Source, "cache", res.Warnings)
	}
	// Cached YAML had fewer organisms — confirm we used the cache, not embedded.
	if !res.Vocabulary.ValueAllowed("organism", "mouse") {
		t.Errorf("mouse should be allowed via cache")
	}
	if res.Vocabulary.ValueAllowed("organism", "macaque") {
		t.Errorf("macaque should NOT be in our tiny cache fixture (got embedded by mistake)")
	}
}

func TestLoadFallsThroughCacheWhenStale(t *testing.T) {
	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "vocab.yaml")
	// Use a vocabulary file with unique markers so we can detect which
	// source was actually used.
	yaml := `schema_version: 1
required: [organism]
vocabulary:
  organism: [stale_marker_only]
`
	if err := os.WriteFile(cachePath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	// Backdate the cache to 48h ago — past the 24h TTL.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(cachePath, old, old); err != nil {
		t.Fatal(err)
	}

	res, err := Load(context.Background(), LoadOptions{
		IRODSHome: "",       // no iRODS available
		CachePath: cachePath, // stale
	})
	if err != nil {
		t.Fatal(err)
	}
	// Stale cache is preferred over embedded (better-than-nothing).
	if res.Source != "cache" {
		t.Errorf("Source = %q, want %q (stale cache should still win over embedded)",
			res.Source, "cache")
	}
	hasStaleWarning := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "stale cache") {
			hasStaleWarning = true
			break
		}
	}
	if !hasStaleWarning {
		t.Errorf("expected 'stale cache' warning, got: %v", res.Warnings)
	}
}

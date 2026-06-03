package upgradecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.2.1", -1},   // patch behind
		{"0.2.1", "0.2.0", 1},    // patch ahead
		{"0.2.1", "0.2.1", 0},    // equal
		{"0.2.1", "0.3.0", -1},   // minor behind
		{"1.0.0", "0.99.99", 1},  // major ahead
		{"0.10.0", "0.2.0", 1},   // numeric, not lexicographic
		{"0.2.1-dev", "0.2.1", 0}, // dev marker not "behind" the bare release
		{"0.2.1-dev", "0.2.0", 1}, // dev marker still wins over older
		{"0.2.1-dev", "0.3.0", -1}, // dev doesn't save you on an older minor
		{"0.2.0", "0.2.0-dev", 0},  // symmetric — dev/bare on same X.Y.Z is equal
	}
	for _, tc := range cases {
		got := compareVersions(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestStripV(t *testing.T) {
	cases := map[string]string{
		"v0.2.0": "0.2.0",
		"V1.2.3": "1.2.3",
		"0.2.0":  "0.2.0",
		"":       "",
	}
	for in, want := range cases {
		if got := stripV(in); got != want {
			t.Errorf("stripV(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNotify_SkipsDevBuild(t *testing.T) {
	// A "dev" Version must never trigger a network call. Point the
	// endpoint at a server that fails the test if hit.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("dev build hit the endpoint at %s", r.URL.Path)
	}))
	defer srv.Close()
	orig := Endpoint
	Endpoint = srv.URL
	defer func() { Endpoint = orig }()

	var stderr bytes.Buffer
	Notify(context.Background(), "dev", &stderr)
	if stderr.Len() != 0 {
		t.Errorf("dev build should not print any warning, got: %s", stderr.String())
	}
}

func TestNotify_PrintsWarningWhenBehind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name": "v0.3.0"}`))
	}))
	defer srv.Close()
	withEndpoint(t, srv.URL)
	withCacheHome(t, t.TempDir())

	var stderr bytes.Buffer
	Notify(context.Background(), "0.2.0", &stderr)
	out := stderr.String()
	if !strings.Contains(out, "newer release is available") {
		t.Errorf("expected 'newer release is available' warning, got: %q", out)
	}
	if !strings.Contains(out, "0.2.0") || !strings.Contains(out, "v0.3.0") {
		t.Errorf("warning should mention both versions, got: %q", out)
	}
}

func TestNotify_SilentWhenAheadOrEqual(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name": "v0.2.0"}`))
	}))
	defer srv.Close()
	withEndpoint(t, srv.URL)
	withCacheHome(t, t.TempDir())

	var stderr bytes.Buffer
	Notify(context.Background(), "0.2.0", &stderr)
	if stderr.Len() != 0 {
		t.Errorf("equal versions should produce no warning, got: %s", stderr.String())
	}
	Notify(context.Background(), "0.3.0", &stderr)
	if stderr.Len() != 0 {
		t.Errorf("ahead-of-upstream should produce no warning, got: %s", stderr.String())
	}
}

func TestNotify_SilentOnNetworkError(t *testing.T) {
	// An endpoint that immediately closes the connection — emulates
	// network failure.
	withEndpoint(t, "http://127.0.0.1:1") // unroutable
	withCacheHome(t, t.TempDir())

	var stderr bytes.Buffer
	Notify(context.Background(), "0.2.0", &stderr)
	if stderr.Len() != 0 {
		t.Errorf("network error must be silent, got: %s", stderr.String())
	}
}

func TestNotify_UsesCachedTagWhenFresh(t *testing.T) {
	// Pre-write a fresh cache record claiming the latest tag is
	// v0.5.0. Then point the endpoint at a server that fails if hit
	// — the cache should serve the answer without a network call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("fresh cache should not hit the endpoint")
	}))
	defer srv.Close()
	withEndpoint(t, srv.URL)
	cacheHome := t.TempDir()
	withCacheHome(t, cacheHome)

	rec := cacheRecord{LatestTag: "v0.5.0", CheckedAt: time.Now().UTC()}
	if err := os.MkdirAll(filepath.Join(cacheHome, "mus"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(cacheHome, "mus", "upgrade_check.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	Notify(context.Background(), "0.2.0", &stderr)
	if !strings.Contains(stderr.String(), "v0.5.0") {
		t.Errorf("expected cached v0.5.0 in warning, got: %q", stderr.String())
	}
}

func TestNotify_RefreshesStaleCache(t *testing.T) {
	// Stale cache → server is hit, cache is rewritten with the new
	// tag.
	served := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		_, _ = w.Write([]byte(`{"tag_name": "v0.4.0"}`))
	}))
	defer srv.Close()
	withEndpoint(t, srv.URL)
	cacheHome := t.TempDir()
	withCacheHome(t, cacheHome)

	rec := cacheRecord{
		LatestTag: "v0.3.0",
		CheckedAt: time.Now().Add(-48 * time.Hour),
	}
	if err := os.MkdirAll(filepath.Join(cacheHome, "mus"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rec)
	cachePath := filepath.Join(cacheHome, "mus", "upgrade_check.json")
	if err := os.WriteFile(cachePath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	Notify(context.Background(), "0.2.0", &stderr)
	if !served {
		t.Error("stale cache should trigger a refresh")
	}
	if !strings.Contains(stderr.String(), "v0.4.0") {
		t.Errorf("warning should use the refreshed tag, got: %q", stderr.String())
	}
	// Cache file should now hold v0.4.0.
	got, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip cacheRecord
	if err := json.Unmarshal(got, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.LatestTag != "v0.4.0" {
		t.Errorf("cache not rewritten; got tag=%q", roundtrip.LatestTag)
	}
}

func TestNotify_FallsBackToStaleCacheOnNetworkFailure(t *testing.T) {
	// Stale cache + unroutable endpoint → keep using the stale
	// value rather than going silent. Better signal than none.
	withEndpoint(t, "http://127.0.0.1:1")
	cacheHome := t.TempDir()
	withCacheHome(t, cacheHome)

	rec := cacheRecord{
		LatestTag: "v0.3.0",
		CheckedAt: time.Now().Add(-48 * time.Hour),
	}
	if err := os.MkdirAll(filepath.Join(cacheHome, "mus"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(cacheHome, "mus", "upgrade_check.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	Notify(context.Background(), "0.2.0", &stderr)
	if !strings.Contains(stderr.String(), "v0.3.0") {
		t.Errorf("expected fallback to stale v0.3.0, got: %q", stderr.String())
	}
}

// --- helpers ---

func withEndpoint(t *testing.T, url string) {
	t.Helper()
	orig := Endpoint
	Endpoint = url
	t.Cleanup(func() { Endpoint = orig })
}

func withCacheHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", dir)
	// Also isolate HOME so the home-based fallback doesn't leak.
	t.Setenv("HOME", dir)
}

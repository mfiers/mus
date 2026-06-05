package cli

// Unit tests for resolveConfigValue — the vocab-aware normaliser
// that backs `mus config set`. Verifies that case-only differences
// auto-canonicalise silently-with-note, that off-vocab values
// warn but pass through, and that fields with no controlled
// vocabulary are unaffected.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedEmbeddedVocab points the vocab loader at the build-time
// embedded YAML so the test is hermetic (no network, no shared
// cache leakage between runs).
func seedEmbeddedVocab(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))

	cache := filepath.Join(dir, "xdg-data", "mus", "h5ad_vocabulary.yaml")
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadFile("../vocab/embedded.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, embedded, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveConfigValue_CanonicalCasingNormalisesSilently(t *testing.T) {
	// "human" in the vocab; "HuMaN" matches modulo case → canonical
	// "human" returned, with a "note:" line on stderr.
	seedEmbeddedVocab(t)
	var stderr bytes.Buffer
	got, err := resolveConfigValue(context.Background(), "organism", "HuMaN", &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if got != "human" {
		t.Errorf("got %q, want canonical %q", got, "human")
	}
	if !strings.Contains(stderr.String(), "normalised") {
		t.Errorf("expected normalisation note on stderr, got: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "⚠") {
		t.Errorf("normalisation should not warn — value matches vocab: %q", stderr.String())
	}
}

func TestResolveConfigValue_AlreadyCanonicalIsSilent(t *testing.T) {
	seedEmbeddedVocab(t)
	var stderr bytes.Buffer
	got, _ := resolveConfigValue(context.Background(), "organism", "human", &stderr)
	if got != "human" {
		t.Errorf("got %q", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no stderr output for already-canonical value, got: %q", stderr.String())
	}
}

func TestResolveConfigValue_UnknownValueWarnsButSaves(t *testing.T) {
	// "hooman" — clearly off-vocab. Should warn (⚠) but still return
	// the value verbatim so `mus config set` writes it.
	seedEmbeddedVocab(t)
	var stderr bytes.Buffer
	got, err := resolveConfigValue(context.Background(), "organism", "hooman", &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hooman" {
		t.Errorf("unknown value should pass through verbatim, got %q", got)
	}
	if !strings.Contains(stderr.String(), "⚠") || !strings.Contains(stderr.String(), "hooman") {
		t.Errorf("expected warning mentioning the value, got: %q", stderr.String())
	}
}

func TestResolveConfigValue_FreeformKeyUntouched(t *testing.T) {
	// `note` has no controlled vocabulary — should pass straight
	// through with no stderr at all (no vocab even loaded).
	var stderr bytes.Buffer
	got, err := resolveConfigValue(context.Background(), "note", "Some Mixed CASE Text",
		&stderr)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Some Mixed CASE Text" {
		t.Errorf("got %q, want verbatim", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("non-vocab field should produce no stderr: %q", stderr.String())
	}
}

func TestResolveConfigValue_ListNormalisesEachItem(t *testing.T) {
	// tissue is list-valued. Mix one canonical, one needs-normalising,
	// one unknown. Result is comma-joined canonical forms; unknown
	// preserved with its warning.
	seedEmbeddedVocab(t)
	var stderr bytes.Buffer
	got, _ := resolveConfigValue(context.Background(), "tissue",
		"hippocampus,PREFRONTAL_CORTEX,squidgyland", &stderr)

	// Expect canonical "hippocampus", "prefrontal cortex" (the vocab
	// stores the spaced form; the input was case+underscore variant),
	// and the unknown "squidgyland" passed through verbatim.
	parts := strings.Split(got, ",")
	if len(parts) != 3 {
		t.Fatalf("expected 3 items, got %d (%q)", len(parts), got)
	}
	if parts[0] != "hippocampus" {
		t.Errorf("first item: %q", parts[0])
	}
	if parts[1] != "prefrontal cortex" {
		t.Errorf("second item should canonicalise to 'prefrontal cortex', got %q", parts[1])
	}
	if parts[2] != "squidgyland" {
		t.Errorf("unknown item should pass through, got %q", parts[2])
	}
	if !strings.Contains(stderr.String(), "squidgyland") {
		t.Errorf("expected warning mentioning the unknown item: %q", stderr.String())
	}
}

func TestResolveConfigValue_AccessAndSourceAreVocabControlled(t *testing.T) {
	// Make sure the lab-specific scalars (access/source) are checked.
	seedEmbeddedVocab(t)
	var stderr bytes.Buffer

	_, _ = resolveConfigValue(context.Background(), "access", "Public", &stderr)
	if !strings.Contains(stderr.String(), "normalised") {
		t.Errorf("expected 'Public' to normalise to 'public' with note, got: %q", stderr.String())
	}
}

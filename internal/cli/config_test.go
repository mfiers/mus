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
	got, err := resolveConfigValue(context.Background(), "organism", "HuMaN", false, &stderr)
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
	got, _ := resolveConfigValue(context.Background(), "organism", "human", false, &stderr)
	if got != "human" {
		t.Errorf("got %q", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no stderr output for already-canonical value, got: %q", stderr.String())
	}
}

func TestResolveConfigValue_OffVocabRejectsByDefault(t *testing.T) {
	// "hooman" — clearly off-vocab. Without --force, must return an
	// error so the value never reaches config.Save.
	seedEmbeddedVocab(t)
	var stderr bytes.Buffer
	got, err := resolveConfigValue(context.Background(), "organism", "hooman", false, &stderr)
	if err == nil {
		t.Fatalf("expected error for off-vocab value, got value=%q", got)
	}
	if !strings.Contains(err.Error(), "controlled vocabulary") {
		t.Errorf("expected vocab-rejection message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error must mention --force as the escape hatch: %v", err)
	}
}

func TestResolveConfigValue_OffVocabSavesWithForce(t *testing.T) {
	// Same off-vocab value, but with force=true → returns verbatim
	// + visible ⚠ warning on stderr so the override is auditable.
	seedEmbeddedVocab(t)
	var stderr bytes.Buffer
	got, err := resolveConfigValue(context.Background(), "organism", "hooman", true, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hooman" {
		t.Errorf("forced off-vocab should pass through verbatim, got %q", got)
	}
	if !strings.Contains(stderr.String(), "⚠") || !strings.Contains(stderr.String(), "hooman") {
		t.Errorf("expected ⚠ warning mentioning the value, got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--force") {
		t.Errorf("warning should acknowledge --force, got: %q", stderr.String())
	}
}

func TestResolveConfigValue_FreeformKeyUntouched(t *testing.T) {
	// `note` has no controlled vocabulary — should pass straight
	// through with no stderr at all (no vocab even loaded).
	var stderr bytes.Buffer
	got, err := resolveConfigValue(context.Background(), "note",
		"Some Mixed CASE Text", false, &stderr)
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

func TestResolveConfigValue_ListAllInVocabSavesCanonical(t *testing.T) {
	// All items match (modulo case / underscore) → comma-joined
	// canonical forms returned.
	seedEmbeddedVocab(t)
	var stderr bytes.Buffer
	got, err := resolveConfigValue(context.Background(), "tissue",
		"hippocampus,PREFRONTAL_CORTEX", false, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(got, ",")
	if len(parts) != 2 || parts[0] != "hippocampus" || parts[1] != "prefrontal cortex" {
		t.Errorf("got %v, want [hippocampus, prefrontal cortex]", parts)
	}
	if !strings.Contains(stderr.String(), "PREFRONTAL_CORTEX") {
		t.Errorf("expected per-item normalisation note, got: %q", stderr.String())
	}
}

func TestResolveConfigValue_ListOffVocabRejectsAtomically(t *testing.T) {
	// One bad item → error mentions ALL bad items, nothing is saved.
	seedEmbeddedVocab(t)
	var stderr bytes.Buffer
	_, err := resolveConfigValue(context.Background(), "tissue",
		"hippocampus,squidgyland,otherland", false, &stderr)
	if err == nil {
		t.Fatal("expected error for off-vocab list items")
	}
	if !strings.Contains(err.Error(), "squidgyland") || !strings.Contains(err.Error(), "otherland") {
		t.Errorf("error should list BOTH bad items, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force: %v", err)
	}
}

func TestResolveConfigValue_ListWithForceSavesEverything(t *testing.T) {
	seedEmbeddedVocab(t)
	var stderr bytes.Buffer
	got, err := resolveConfigValue(context.Background(), "tissue",
		"hippocampus,squidgyland", true, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(got, ",")
	if len(parts) != 2 || parts[0] != "hippocampus" || parts[1] != "squidgyland" {
		t.Errorf("got %v, want [hippocampus, squidgyland] under --force", parts)
	}
}

func TestResolveConfigValue_AccessAndSourceAreVocabControlled(t *testing.T) {
	// Lab-specific scalars (access/source) — case-only fix must work.
	seedEmbeddedVocab(t)
	var stderr bytes.Buffer
	got, err := resolveConfigValue(context.Background(), "access", "Public", false, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if got != "public" {
		t.Errorf("got %q, want canonical 'public'", got)
	}
	if !strings.Contains(stderr.String(), "normalised") {
		t.Errorf("expected normalisation note, got: %q", stderr.String())
	}
}

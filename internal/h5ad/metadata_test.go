package h5ad

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

func mustParseEmbeddedVocab(t *testing.T) *Vocabulary {
	t.Helper()
	v, err := ParseVocabulary(embeddedYAML)
	if err != nil {
		t.Fatalf("parse embedded vocab: %v", err)
	}
	return v
}

// envFromBody writes the given .env body to a temp dir and returns
// the loaded Env.
func envFromBody(t *testing.T, body string) *config.Env {
	t.Helper()
	dir := t.TempDir()
	// Isolate from any real $HOME .env / global config.
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func findIssue(issues []Issue, severity Severity, field string) *Issue {
	for i := range issues {
		if issues[i].Severity == severity && issues[i].Field == field {
			return &issues[i]
		}
	}
	return nil
}

func summary(issues []Issue) []string {
	out := make([]string, len(issues))
	for i, is := range issues {
		out[i] = is.Severity.String() + " " + is.Field + ": " + is.Message
	}
	return out
}

// A minimal, valid metadata set declared via .env keys.
const minimalEnvYAML = `
h5ad_organism=human
h5ad_celltype=microglia
h5ad_technology=10x_chromium_3p_v3
h5ad_preparation=single_nucleus
h5ad_tissue=brain
h5ad_internal=no
h5ad_accession=GSE123456
h5ad_contributor=Jane <jane@example.com>
h5ad_year=2026
h5ad_doi=10.1038/foo
`

func TestValidMetadataFromCascade(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, minimalEnvYAML), nil)
	issues := ValidateMetadata(m, v)
	for _, i := range issues {
		if i.Severity == SeverityHard {
			t.Errorf("unexpected HARD: %s %s", i.Field, i.Message)
		}
	}
	if m.Organism != "human" || len(m.Celltype) != 1 || m.Celltype[0] != "microglia" {
		t.Errorf("not loaded cleanly: %+v", m)
	}
}

func TestMissingRequiredFieldsAreHard(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, ""), nil)
	issues := ValidateMetadata(m, v)
	for _, field := range []string{"organism", "celltype", "technology",
		"preparation", "tissue", "internal", "accession"} {
		if findIssue(issues, SeverityHard, field) == nil {
			t.Errorf("expected HARD for missing %q; issues=%v",
				field, summary(issues))
		}
	}
}

func TestBadVocabValueIsHard(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, `
h5ad_organism=martian
h5ad_celltype=microglia
h5ad_technology=10x_chromium_3p_v3
h5ad_preparation=single_nucleus
h5ad_tissue=brain
h5ad_internal=no
h5ad_accession=GSE1
`), nil)
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeverityHard, "organism") == nil {
		t.Errorf("expected HARD for organism=martian; got %v", summary(issues))
	}
}

func TestCaseInsensitiveVocab(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, `
h5ad_organism=Human
h5ad_celltype=microglia
h5ad_technology=10x_chromium_3p_v3
h5ad_preparation=single_nucleus
h5ad_tissue=brain
h5ad_internal=no
h5ad_accession=GSE1
`), nil)
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeverityHard, "organism") != nil {
		t.Errorf("organism=Human should pass case-insensitively; got %v", summary(issues))
	}
}

func TestListKeysParseAsLists(t *testing.T) {
	// h5ad_celltype is registered as a list key in internal/config.
	env := envFromBody(t, `h5ad_celltype=microglia,macrophage`)
	m := MetadataFromCascade(env, nil)
	if len(m.Celltype) != 2 || m.Celltype[0] != "microglia" || m.Celltype[1] != "macrophage" {
		t.Errorf("expected [microglia macrophage], got %v", m.Celltype)
	}
}

func TestSidecarOverridesCascade(t *testing.T) {
	env := envFromBody(t, `
h5ad_organism=mouse
h5ad_celltype=neuron
`)
	sc := &sidecar.H5ad{
		Organism: "human",
		Celltype: []string{"microglia"},
	}
	m := MetadataFromCascade(env, sc)
	if m.Organism != "human" {
		t.Errorf("sidecar should override; got %q", m.Organism)
	}
	if len(m.Celltype) != 1 || m.Celltype[0] != "microglia" {
		t.Errorf("sidecar list should override; got %v", m.Celltype)
	}
}

func TestSidecarOnlyEmptyStringDoesNotOverride(t *testing.T) {
	env := envFromBody(t, `h5ad_organism=mouse`)
	sc := &sidecar.H5ad{} // empty
	m := MetadataFromCascade(env, sc)
	if m.Organism != "mouse" {
		t.Errorf("empty sidecar must not erase cascade; got %q", m.Organism)
	}
}

func TestPublicIDPattern(t *testing.T) {
	cases := map[string]bool{
		"GSE12345":              true,
		"gse12345":              true,
		"SRP123":                true,
		"SRR1234567":            true,
		"PRJNA12345":            true,
		"phs001345.v1.p1":       true,
		"EGAS00001000123":       true,
		"10.5281/zenodo.123456": true,
		"10.1038/s41586-024-1":  true,
		"my_lab_internal":       false,
		"":                      false,
		"random string":         false,
	}
	for in, want := range cases {
		if got := looksPublicID(in); got != want {
			t.Errorf("looksPublicID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestInternalNoNeedsPublicID(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, `
h5ad_organism=human
h5ad_celltype=microglia
h5ad_technology=10x_chromium_3p_v3
h5ad_preparation=single_nucleus
h5ad_tissue=brain
h5ad_internal=no
h5ad_accession=my_lab_2026_pilot
`), nil)
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeveritySoft, "accession") == nil {
		t.Errorf("internal=no + non-public accession should SOFT-warn; got %v",
			summary(issues))
	}
}

func TestInternalYesAllowsFreeformAccession(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, `
h5ad_organism=human
h5ad_celltype=microglia
h5ad_technology=10x_chromium_3p_v3
h5ad_preparation=single_nucleus
h5ad_tissue=brain
h5ad_internal=yes
h5ad_accession=lab_2026_AD_microglia_v1
`), nil)
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeveritySoft, "accession") != nil {
		t.Errorf("internal=yes should allow freeform accession; got %v", summary(issues))
	}
}

// silence "unused" lint on strings if a test branch removes its use.
var _ = strings.Contains

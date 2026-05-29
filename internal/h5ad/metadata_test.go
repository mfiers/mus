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
// the loaded Env. Isolates HOME/XDG so the global config doesn't
// leak in.
func envFromBody(t *testing.T, body string) *config.Env {
	t.Helper()
	dir := t.TempDir()
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

// A minimal, valid metadata set declared via .env (bare keys).
const minimalEnvBody = `
organism=human
celltype=microglia
technology=10x_chromium_3p_v3
preparation=single_nucleus
tissue=brain
internal=no
accession=GSE123456
contributor=Jane <jane@example.com>
year=2026
doi=10.1038/foo
`

func TestValidMetadataFromCascade(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, minimalEnvBody), nil, nil)
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
	m := MetadataFromCascade(envFromBody(t, ""), nil, nil)
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
organism=martian
celltype=microglia
technology=10x_chromium_3p_v3
preparation=single_nucleus
tissue=brain
internal=no
accession=GSE1
`), nil, nil)
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeverityHard, "organism") == nil {
		t.Errorf("expected HARD for organism=martian; got %v", summary(issues))
	}
}

func TestCaseInsensitiveVocab(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, `
organism=Human
celltype=microglia
technology=10x_chromium_3p_v3
preparation=single_nucleus
tissue=brain
internal=no
accession=GSE1
`), nil, nil)
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeverityHard, "organism") != nil {
		t.Errorf("organism=Human should pass case-insensitively; got %v", summary(issues))
	}
}

func TestListKeysParseAsLists(t *testing.T) {
	env := envFromBody(t, `celltype=microglia,macrophage`)
	m := MetadataFromCascade(env, nil, nil)
	if len(m.Celltype) != 2 || m.Celltype[0] != "microglia" || m.Celltype[1] != "macrophage" {
		t.Errorf("expected [microglia macrophage], got %v", m.Celltype)
	}
}

func TestBioSidecarOverridesCascade(t *testing.T) {
	env := envFromBody(t, `
organism=mouse
celltype=neuron
`)
	bio := &sidecar.Bio{
		Organism: "human",
		Celltype: []string{"microglia"},
	}
	m := MetadataFromCascade(env, bio, nil)
	if m.Organism != "human" {
		t.Errorf("Bio override should win; got %q", m.Organism)
	}
	if len(m.Celltype) != 1 || m.Celltype[0] != "microglia" {
		t.Errorf("Bio override list should win; got %v", m.Celltype)
	}
}

func TestEmptyBioDoesNotEraseCascade(t *testing.T) {
	env := envFromBody(t, `organism=mouse`)
	bio := &sidecar.Bio{} // empty
	m := MetadataFromCascade(env, bio, nil)
	if m.Organism != "mouse" {
		t.Errorf("empty Bio must not erase cascade; got %q", m.Organism)
	}
}

func TestH5adSidecarSuppliesFileSpecific(t *testing.T) {
	env := envFromBody(t, "")
	h5 := &sidecar.H5ad{
		NObs:       1234,
		NVars:      5678,
		GeneIDType: "hgnc_symbol",
	}
	m := MetadataFromCascade(env, nil, h5)
	if m.NCells != 1234 || m.NGenes != 5678 {
		t.Errorf("file-specific overrides not applied; got NCells=%d NGenes=%d",
			m.NCells, m.NGenes)
	}
	if m.GeneIDType != "hgnc_symbol" {
		t.Errorf("GeneIDType from H5ad not applied; got %q", m.GeneIDType)
	}
}

func TestPublicIDPattern(t *testing.T) {
	cases := map[string]bool{
		"GSE12345":              true,
		"gse12345":              true,
		"SRR1234567":            true,
		"PRJNA12345":            true,
		"phs001345.v1.p1":       true,
		"EGAS00001000123":       true,
		"10.5281/zenodo.123456": true,
		"my_lab_internal":       false,
		"":                      false,
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
organism=human
celltype=microglia
technology=10x_chromium_3p_v3
preparation=single_nucleus
tissue=brain
internal=no
accession=my_lab_2026_pilot
`), nil, nil)
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeveritySoft, "accession") == nil {
		t.Errorf("internal=no + non-public accession should SOFT-warn; got %v",
			summary(issues))
	}
}

func TestInternalYesAllowsFreeformAccession(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, `
organism=human
celltype=microglia
technology=10x_chromium_3p_v3
preparation=single_nucleus
tissue=brain
internal=yes
accession=lab_2026_AD_microglia_v1
`), nil, nil)
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeveritySoft, "accession") != nil {
		t.Errorf("internal=yes should allow freeform accession; got %v", summary(issues))
	}
}

// silence "unused" lint on strings if a test branch removes its use.
var _ = strings.Contains

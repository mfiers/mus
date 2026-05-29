package h5ad

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/sidecar"
	"codeberg.org/mfiers/mus/internal/vocab"
)

func mustParseEmbeddedVocab(t *testing.T) *vocab.Vocabulary {
	t.Helper()
	v, err := vocab.Embedded()
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

// minimalEnvBody hits every joint-vocabulary required field with
// schema-v2 names + CellxGene-aligned values.
const minimalEnvBody = `
organism=human
tissue=prefrontal cortex
cell_type=microglial cell
technology=10x_chromium_3p_v3
suspension_type=nucleus
access=internal
source=external
contributor=Jane <jane@example.com>
year=2026
doi=10.1038/foo
accession=lab_internal_2026
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
	if m.Organism != "human" {
		t.Errorf("Organism = %q", m.Organism)
	}
	if len(m.CellType) != 1 || m.CellType[0] != "microglial cell" {
		t.Errorf("CellType = %v", m.CellType)
	}
	if m.SuspensionType != "nucleus" {
		t.Errorf("SuspensionType = %q", m.SuspensionType)
	}
}

func TestMissingRequiredFieldsAreHard(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, ""), nil, nil)
	issues := ValidateMetadata(m, v)
	for _, field := range []string{"organism", "tissue", "cell_type",
		"technology", "suspension_type", "access", "source"} {
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
tissue=brain
cell_type=microglial cell
technology=10x_chromium_3p_v3
suspension_type=nucleus
access=internal
source=external
`), nil, nil)
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeverityHard, "organism") == nil {
		t.Errorf("expected HARD for organism=martian; got %v", summary(issues))
	}
}

func TestRelaxedMatchingHandlesUnderscoresAndSpaces(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	// h5adstore-style `prefrontal cortex` with a space, and the
	// underscored form, should both validate against the same
	// canonical entry.
	m := MetadataFromCascade(envFromBody(t, `
organism=human
tissue=prefrontal_cortex
cell_type=microglial_cell
technology=10x chromium 3p v3
suspension_type=nucleus
access=internal
source=external
`), nil, nil)
	issues := ValidateMetadata(m, v)
	for _, i := range issues {
		if i.Severity == SeverityHard && (i.Field == "tissue" || i.Field == "cell_type" || i.Field == "technology") {
			t.Errorf("relaxed-matching value should pass; got HARD on %q: %s", i.Field, i.Message)
		}
	}
}

func TestListKeysParseAsLists(t *testing.T) {
	env := envFromBody(t, `cell_type=microglial cell,astrocyte`)
	m := MetadataFromCascade(env, nil, nil)
	if len(m.CellType) != 2 || m.CellType[0] != "microglial cell" || m.CellType[1] != "astrocyte" {
		t.Errorf("expected [microglial cell, astrocyte], got %v", m.CellType)
	}
}

func TestQuotedListItemSurvivesCascade(t *testing.T) {
	// MONDO label with literal commas, double-quoted in .env.
	env := envFromBody(t, `disease="Alzheimer disease","leukoencephalopathy, diffuse hereditary, with spheroids 1",normal`)
	m := MetadataFromCascade(env, nil, nil)
	want := []string{"Alzheimer disease",
		"leukoencephalopathy, diffuse hereditary, with spheroids 1",
		"normal"}
	if len(m.Disease) != 3 || m.Disease[1] != want[1] {
		t.Errorf("MONDO comma-label not preserved; got %v", m.Disease)
	}
}

func TestBioSidecarOverridesCascade(t *testing.T) {
	env := envFromBody(t, `
organism=mouse
cell_type=neuron
`)
	bio := &sidecar.Bio{
		Organism: "human",
		CellType: []string{"microglial cell"},
	}
	m := MetadataFromCascade(env, bio, nil)
	if m.Organism != "human" {
		t.Errorf("Bio override should win; got %q", m.Organism)
	}
	if len(m.CellType) != 1 || m.CellType[0] != "microglial cell" {
		t.Errorf("Bio override list should win; got %v", m.CellType)
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

// TestPublicAccessNeedsPublicID — schema v2 renamed
// `internal: no` → `access: public`. A public-access deposit should
// have a recognised public ID; freeform internal labels are SOFT.
func TestPublicAccessNeedsPublicID(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, `
organism=human
tissue=brain
cell_type=microglial cell
technology=10x_chromium_3p_v3
suspension_type=nucleus
access=public
source=external
accession=my_lab_2026_pilot
`), nil, nil)
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeveritySoft, "accession") == nil {
		t.Errorf("access=public + non-public accession should SOFT-warn; got %v",
			summary(issues))
	}
}

func TestInternalAccessAllowsFreeformAccession(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m := MetadataFromCascade(envFromBody(t, `
organism=human
tissue=brain
cell_type=microglial cell
technology=10x_chromium_3p_v3
suspension_type=nucleus
access=internal
source=internal
accession=lab_2026_AD_microglia_v1
`), nil, nil)
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeveritySoft, "accession") != nil {
		t.Errorf("access=internal should allow freeform accession; got %v", summary(issues))
	}
}

// silence "unused" lint on strings if a test branch removes its use.
var _ = strings.Contains

package h5ad

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustParseEmbeddedVocab(t *testing.T) *Vocabulary {
	t.Helper()
	v, err := ParseVocabulary(embeddedYAML)
	if err != nil {
		t.Fatalf("parse embedded vocab: %v", err)
	}
	return v
}

func writeMetadata(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "x.metadata.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func findIssue(issues []Issue, severity Severity, field string) *Issue {
	for i := range issues {
		if issues[i].Severity == severity && issues[i].Field == field {
			return &issues[i]
		}
	}
	return nil
}

// A minimal, valid metadata YAML (passes the §4 hard requirements).
const minimalValidYAML = `
organism: human
celltype:
  - microglia
technology: 10x_chromium_3p_v3
preparation: single_nucleus
tissue:
  - brain
internal: "no"
accession: GSE123456
embeddings:
  - X_umap
contributor: "Jane <jane@example.com>"
year: 2026
doi: 10.1038/foo
`

func TestValidMetadataHasNoHardIssues(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m, err := LoadMetadata(writeMetadata(t, minimalValidYAML))
	if err != nil {
		t.Fatal(err)
	}
	issues := ValidateMetadata(m, v)
	for _, i := range issues {
		if i.Severity == SeverityHard {
			t.Errorf("unexpected HARD issue: field=%q msg=%q", i.Field, i.Message)
		}
	}
}

func TestMissingRequiredFieldsAreHard(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m, err := LoadMetadata(writeMetadata(t, `# completely empty`))
	if err != nil {
		t.Fatal(err)
	}
	issues := ValidateMetadata(m, v)
	for _, field := range []string{"organism", "celltype", "technology",
		"preparation", "tissue", "internal", "accession"} {
		if findIssue(issues, SeverityHard, field) == nil {
			t.Errorf("expected HARD issue for missing required field %q; got issues=%v",
				field, summary(issues))
		}
	}
}

func TestBadVocabValueIsHard(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m, err := LoadMetadata(writeMetadata(t, `
organism: martian
celltype: [microglia]
technology: 10x_chromium_3p_v3
preparation: single_nucleus
tissue: [brain]
internal: "no"
accession: GSE1
embeddings: [X_umap]
`))
	if err != nil {
		t.Fatal(err)
	}
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeverityHard, "organism") == nil {
		t.Errorf("expected HARD issue for organism=martian; got %v", summary(issues))
	}
}

func TestCaseInsensitiveVocab(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	// "Human" (capital H) must be accepted.
	m, err := LoadMetadata(writeMetadata(t, `
organism: Human
celltype: [microglia]
technology: 10x_chromium_3p_v3
preparation: single_nucleus
tissue: [brain]
internal: "no"
accession: GSE1
embeddings: [X_umap]
`))
	if err != nil {
		t.Fatal(err)
	}
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeverityHard, "organism") != nil {
		t.Errorf("organism=Human should pass case-insensitively; got %v", summary(issues))
	}
}

func TestUnderscoreSpaceStrict(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	// "prefrontal cortex" (with space) should NOT match "prefrontal_cortex".
	m, err := LoadMetadata(writeMetadata(t, `
organism: human
celltype: [microglia]
technology: 10x_chromium_3p_v3
preparation: single_nucleus
tissue: [brain]
region: ["prefrontal cortex"]
internal: "no"
accession: GSE1
embeddings: [X_umap]
`))
	if err != nil {
		t.Fatal(err)
	}
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeverityHard, "region") == nil {
		t.Errorf("space-separated value should fail vocab check; got %v", summary(issues))
	}
}

func TestScalarOrListBothParse(t *testing.T) {
	cases := map[string]string{
		"scalar":   `celltype: microglia`,
		"sequence": `celltype: [microglia]`,
	}
	for name, snippet := range cases {
		t.Run(name, func(t *testing.T) {
			full := `
organism: human
` + snippet + `
technology: 10x_chromium_3p_v3
preparation: single_nucleus
tissue: [brain]
internal: "no"
accession: GSE1
embeddings: [X_umap]
`
			m, err := LoadMetadata(writeMetadata(t, full))
			if err != nil {
				t.Fatal(err)
			}
			if len(m.Celltype) != 1 || m.Celltype[0] != "microglia" {
				t.Errorf("celltype = %v, want [microglia]", m.Celltype)
			}
		})
	}
}

func TestEmbeddingsRequireUMAPOrTSNE(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m, err := LoadMetadata(writeMetadata(t, `
organism: human
celltype: [microglia]
technology: 10x_chromium_3p_v3
preparation: single_nucleus
tissue: [brain]
internal: "no"
accession: GSE1
embeddings: [X_pca]
`))
	if err != nil {
		t.Fatal(err)
	}
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeverityHard, "embeddings") == nil {
		t.Errorf("X_pca alone should fail; got %v", summary(issues))
	}
}

func TestPublicIDPattern(t *testing.T) {
	cases := map[string]bool{
		"GSE12345":              true,
		"gse12345":              true, // case-insensitive
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
	m, err := LoadMetadata(writeMetadata(t, `
organism: human
celltype: [microglia]
technology: 10x_chromium_3p_v3
preparation: single_nucleus
tissue: [brain]
internal: "no"
accession: my_lab_2026_pilot
embeddings: [X_umap]
`))
	if err != nil {
		t.Fatal(err)
	}
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeveritySoft, "accession") == nil {
		t.Errorf("internal=no + non-public accession should SOFT-warn; got %v",
			summary(issues))
	}
}

func TestInternalYesAllowsFreeformAccession(t *testing.T) {
	v := mustParseEmbeddedVocab(t)
	m, err := LoadMetadata(writeMetadata(t, `
organism: human
celltype: [microglia]
technology: 10x_chromium_3p_v3
preparation: single_nucleus
tissue: [brain]
internal: "yes"
accession: lab_2026_AD_microglia_v1
embeddings: [X_umap]
`))
	if err != nil {
		t.Fatal(err)
	}
	issues := ValidateMetadata(m, v)
	if findIssue(issues, SeveritySoft, "accession") != nil {
		t.Errorf("internal=yes should allow freeform accession; got %v", summary(issues))
	}
}

// summary trims an issue list down to a one-liner-per-issue for nicer
// test diagnostics.
func summary(issues []Issue) []string {
	out := make([]string, len(issues))
	for i, is := range issues {
		out[i] = strings.TrimSpace(is.Severity.String() + " " + is.Field + ": " + is.Message)
	}
	return out
}

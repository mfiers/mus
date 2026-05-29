package h5ad

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goodReport returns a fresh InspectionReport that satisfies every
// structural check in §1.1–§1.4. Tests mutate it to drive specific
// failures.
func goodReport() *InspectionReport {
	tr, fa := true, false
	xmin, cmin := 0.0, 0.0
	return &InspectionReport{
		SchemaVersion:  1,
		NObs:           1000,
		NVars:          20000,
		X:              MatrixReport{Present: true, IsSparse: &tr, Min: &xmin, IsInteger: &fa, ExitReason: "matrix_exhausted", Examined: 500_000},
		Counts:         MatrixReport{Present: true, IsSparse: &tr, Min: &cmin, IsInteger: &tr, ExitReason: "matrix_exhausted", Examined: 500_000},
		Layers:         []string{"counts"},
		Obsm:           ObsmReport{Keys: []string{"X_umap"}, HasUMAP: true},
		ObsNamesUnique: true,
		VarNamesUnique: true,
		ObsColumns:     []string{"cell_type", "donor"},
		VarColumns:     []string{"highly_variable"},
		HighCardObs:    map[string]int64{},
		VarNamesSample: []string{"AKT1", "MAP1A", "GAPDH", "MYC"},
	}
}

// goodMetadata returns a Metadata that satisfies every required-field
// check (schema v2). Tests mutate to drive specific failures.
func goodMetadata() *Metadata {
	return &Metadata{
		Organism:       "human",
		Tissue:         []string{"brain"},
		CellType:       []string{"microglial cell"},
		Technology:     "10x_chromium_3p_v3",
		SuspensionType: "nucleus",
		Access:         "public",
		Source:         "external",
		Accession:      "GSE123456",
		DOI:            []string{"10.1038/foo"},
		Year:           2026,
		Contributor:    "Jane",
		NCells:         1000,
		NGenes:         20000,
	}
}

func runWith(t *testing.T, rep *InspectionReport, meta *Metadata) *Report {
	t.Helper()
	v := mustParseEmbeddedVocab(t)
	dir := t.TempDir()
	h5adPath := filepath.Join(dir, "mydata.h5ad")
	if err := os.WriteFile(h5adPath, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(h5adPath)
	stub := &StubInspector{Reports: map[string]*InspectionReport{abs: rep, h5adPath: rep}}
	r, err := RunChecks(context.Background(), h5adPath, CheckOptions{
		Inspector:  stub,
		Vocabulary: v,
		Metadata:   meta,
	})
	if err != nil {
		t.Fatalf("RunChecks: %v", err)
	}
	return r
}

func hasHardField(r *Report, field string) bool {
	for _, i := range r.Hard() {
		if i.Field == field {
			return true
		}
	}
	return false
}

func hasSoftField(r *Report, field string) bool {
	for _, i := range r.Soft() {
		if i.Field == field {
			return true
		}
	}
	return false
}

func joinIssues(issues []Issue) string {
	parts := make([]string, len(issues))
	for i, is := range issues {
		parts[i] = is.Severity.String() + " " + is.Field + ": " + is.Message
	}
	return strings.Join(parts, "\n  ")
}

func TestRunChecksGoodPath(t *testing.T) {
	r := runWith(t, goodReport(), goodMetadata())
	if r.HasHard() {
		t.Errorf("good pair should have no HARD findings, got:\n  %s",
			joinIssues(r.Hard()))
	}
}

// --- §1.1 X matrix --------------------------------------------------------

func TestXMissing(t *testing.T) {
	rep := goodReport()
	rep.X.Present = false
	r := runWith(t, rep, goodMetadata())
	if !hasHardField(r, "X") {
		t.Errorf("X missing should be HARD; got %s", joinIssues(r.Hard()))
	}
}

func TestXNegative(t *testing.T) {
	rep := goodReport()
	neg := -0.5
	rep.X.Min = &neg
	r := runWith(t, rep, goodMetadata())
	if !hasHardField(r, "X") {
		t.Errorf("X<0 should be HARD; got %s", joinIssues(r.Hard()))
	}
}

func TestXLooksLikeRawCounts(t *testing.T) {
	rep := goodReport()
	yes := true
	rep.X.IsInteger = &yes
	r := runWith(t, rep, goodMetadata())
	if !hasHardField(r, "X") {
		t.Errorf("X integer-valued should be HARD; got %s", joinIssues(r.Hard()))
	}
}

// --- §1.1 layers["counts"] -------------------------------------------------

func TestCountsMissing(t *testing.T) {
	rep := goodReport()
	rep.Counts.Present = false
	r := runWith(t, rep, goodMetadata())
	if !hasHardField(r, "counts") {
		t.Errorf("counts missing should be HARD; got %s", joinIssues(r.Hard()))
	}
}

func TestCountsNotInteger(t *testing.T) {
	rep := goodReport()
	no := false
	rep.Counts.IsInteger = &no
	r := runWith(t, rep, goodMetadata())
	if !hasHardField(r, "counts") {
		t.Errorf("counts non-integer should be HARD; got %s", joinIssues(r.Hard()))
	}
}

// --- §1.2 embeddings -------------------------------------------------------

func TestNoUMAPNoTSNE(t *testing.T) {
	rep := goodReport()
	rep.Obsm.HasUMAP = false
	rep.Obsm.HasTSNE = false
	r := runWith(t, rep, goodMetadata())
	if !hasHardField(r, "obsm") {
		t.Errorf("no UMAP/TSNE should be HARD; got %s", joinIssues(r.Hard()))
	}
}

func TestTSNEAloneIsOK(t *testing.T) {
	rep := goodReport()
	rep.Obsm.HasUMAP = false
	rep.Obsm.HasTSNE = true
	r := runWith(t, rep, goodMetadata())
	if hasHardField(r, "obsm") {
		t.Errorf("X_tsne alone should pass §1.2; got %s", joinIssues(r.Hard()))
	}
}

// --- §1.3 identifiers ------------------------------------------------------

func TestDuplicateObsNames(t *testing.T) {
	rep := goodReport()
	rep.ObsNamesUnique = false
	r := runWith(t, rep, goodMetadata())
	if !hasHardField(r, "obs_names") {
		t.Errorf("duplicate obs_names should be HARD; got %s", joinIssues(r.Hard()))
	}
}

func TestReservedObsColumn(t *testing.T) {
	rep := goodReport()
	rep.ReservedInObs = []string{"cell_id"}
	r := runWith(t, rep, goodMetadata())
	if !hasHardField(r, "obs") {
		t.Errorf("reserved column should be HARD; got %s", joinIssues(r.Hard()))
	}
}

// --- §1.4 file format -----------------------------------------------------

func TestUppercaseExtensionIsHard(t *testing.T) {
	dir := t.TempDir()
	h5adPath := filepath.Join(dir, "mydata.H5AD")
	if err := os.WriteFile(h5adPath, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := mustParseEmbeddedVocab(t)
	abs, _ := filepath.Abs(h5adPath)
	stub := &StubInspector{Reports: map[string]*InspectionReport{abs: goodReport(), h5adPath: goodReport()}}
	r, err := RunChecks(context.Background(), h5adPath, CheckOptions{
		Inspector:  stub,
		Vocabulary: v,
		Metadata:   goodMetadata(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasHardField(r, "file") {
		t.Errorf("uppercase .H5AD should be HARD; got %s", joinIssues(r.Hard()))
	}
}

// --- cross-consistency -----------------------------------------------------

func TestNCellsMismatch(t *testing.T) {
	m := goodMetadata()
	m.NCells = 5000 // mismatches goodReport().NObs == 1000
	r := runWith(t, goodReport(), m)
	if !hasHardField(r, "n_cells") {
		t.Errorf("n_cells mismatch should be HARD; got %s", joinIssues(r.Hard()))
	}
}

func TestNGenesMismatch(t *testing.T) {
	m := goodMetadata()
	m.NGenes = 999 // mismatches goodReport().NVars == 20000
	r := runWith(t, goodReport(), m)
	if !hasHardField(r, "n_genes") {
		t.Errorf("n_genes mismatch should be HARD; got %s", joinIssues(r.Hard()))
	}
}

func TestSha256MismatchIsSoft(t *testing.T) {
	m := goodMetadata()
	m.Sha256 = "deadbeef"
	v := mustParseEmbeddedVocab(t)
	dir := t.TempDir()
	h5adPath := filepath.Join(dir, "mydata.h5ad")
	if err := os.WriteFile(h5adPath, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(h5adPath)
	stub := &StubInspector{Reports: map[string]*InspectionReport{abs: goodReport(), h5adPath: goodReport()}}
	r, err := RunChecks(context.Background(), h5adPath, CheckOptions{
		Inspector:     stub,
		Vocabulary:    v,
		Metadata:      m,
		ComputeSha256: func(p string) (string, error) { return "0123abcd", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.HasHard() {
		t.Errorf("sha256 mismatch should NOT be HARD; got %s", joinIssues(r.Hard()))
	}
	if !hasSoftField(r, "sha256") {
		t.Errorf("sha256 mismatch should be SOFT; soft=%s", joinIssues(r.Soft()))
	}
}

// --- §2.1 gene-symbol heuristic --------------------------------------------

func TestEnsemblOnlyTriggersSoftRemapWarning(t *testing.T) {
	rep := goodReport()
	rep.VarNamesSample = []string{
		"ENSG00000141510", "ENSG00000133703", "ENSG00000146648",
		"ENSG00000232810", "ENSG00000115414",
	}
	r := runWith(t, rep, goodMetadata())
	if !hasSoftField(r, "var_names") {
		t.Errorf("Ensembl-only var_names should SOFT-warn; soft=%s",
			joinIssues(r.Soft()))
	}
}

func TestSymbolOnlyDoesNotWarn(t *testing.T) {
	rep := goodReport()
	rep.VarNamesSample = []string{"AKT1", "TP53", "MYC", "BRCA1", "GAPDH"}
	r := runWith(t, rep, goodMetadata())
	if hasSoftField(r, "var_names") {
		t.Errorf("HGNC-style var_names shouldn't warn; soft=%s",
			joinIssues(r.Soft()))
	}
}

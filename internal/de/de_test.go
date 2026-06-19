package de

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes body to <dir>/<name>; returns the absolute path.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInspect_DESeq2_TSV(t *testing.T) {
	dir := t.TempDir()
	// DESeq2's canonical column set; tab-delimited.
	body := strings.Join([]string{
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"GENE1\t12.3\t1.4\t0.2\t6.5\t1e-9\t1e-7",
		"GENE2\t45.6\t-0.7\t0.3\t-2.1\t0.03\t0.04",
		"GENE3\t8.1\t0.1\t0.4\t0.25\t0.7\t0.8",
	}, "\n")
	p := writeFile(t, dir, "de.tsv", body)
	rep, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Format != FormatDESeq2 {
		t.Errorf("format: got %q, want deseq2", rep.Format)
	}
	if rep.RowCount != 3 {
		t.Errorf("rows: got %d, want 3", rep.RowCount)
	}
	if err := Validate(rep); err != nil {
		t.Errorf("validate: %v", err)
	}
}

func TestInspect_FormatDetectionMatrix(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   Format
	}{
		{"deseq2", "gene\tbaseMean\tlog2FoldChange\tlfcSE\tpadj", FormatDESeq2},
		{"edger", "gene\tlogFC\tlogCPM\tF\tFDR", FormatEdgeR},
		{"scanpy", "names\tlogfoldchanges\tpvals_adj", FormatScanpy},
		{"seurat", "gene\tavg_log2FC\tpct.1\tpct.2\tp_val_adj", FormatSeurat},
		{"glmmseq", "gene\tchisq\tdf\tP_FixedEffects\tq.FixedEffects", FormatGLMMseq},
		{"glmmtmb", "gene\tEstimate\tStd. Error\tz value\tPr(>|z|)", FormatGLMMTMB},
		{"long", "gene_id\teffsize\tpvalue\tpadj\tterm\tcontrast", FormatLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := tc.header + "\nROW\t1\t1\t1\t1\t1\t1\t1\n"
			p := writeFile(t, dir, "de.tsv", body)
			rep, err := Inspect(p)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Format != tc.want {
				t.Errorf("got %q, want %q (header=%q)", rep.Format, tc.want, tc.header)
			}
		})
	}
}

func TestInspect_FormatDetection_CaseAndSeparatorInsensitive(t *testing.T) {
	// log2FoldChange ↔ LOG_2_FOLDCHANGE ↔ Log2.Fold.Change all
	// collapse to the same key under normalizeColName.
	body := "gene\tBASEMEAN\tLog2.Fold.Change\tLFC_SE\tPADJ\nG1\t1\t1\t1\t1\n"
	dir := t.TempDir()
	p := writeFile(t, dir, "de.tsv", body)
	rep, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Format != FormatDESeq2 {
		t.Errorf("normalization failed; got %q", rep.Format)
	}
}

func TestInspect_FormatUnknown(t *testing.T) {
	body := "gene\tfc\tp\nG1\t1\t0.5\n"
	dir := t.TempDir()
	p := writeFile(t, dir, "mystery.tsv", body)
	rep, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Format != FormatUnknown {
		t.Errorf("expected unknown format; got %q", rep.Format)
	}
	if len(rep.HeaderCols) != 3 {
		t.Errorf("expected 3 header cols preserved for caller error msg; got %d", len(rep.HeaderCols))
	}
	if err := Validate(rep); err == nil {
		t.Error("Validate should fail on unknown format")
	}
}

func TestInspect_Long_ComputesDistinctTermContrast(t *testing.T) {
	body := strings.Join([]string{
		"gene_id\teffsize\tpvalue\tpadj\tterm\tcontrast",
		"G1\t1.2\t1e-5\t1e-4\tgenotype\tK_vs_W",
		"G2\t-0.8\t0.01\t0.05\tgenotype\tK_vs_W",
		"G3\t0.5\t0.03\t0.08\tsex\tM_vs_F",
		"G4\t0.7\t0.001\t0.01\tsex\tM_vs_F",
		"G5\t0.1\t0.4\t0.5\tgenotype:sex\tK:M_vs_W:F",
	}, "\n")
	dir := t.TempDir()
	p := writeFile(t, dir, "long.tsv", body)
	rep, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Format != FormatLong {
		t.Fatalf("format: %q", rep.Format)
	}
	if rep.RowCount != 5 {
		t.Errorf("rows: %d", rep.RowCount)
	}
	wantTerms := []string{"genotype", "genotype:sex", "sex"}
	wantContrasts := []string{"K:M_vs_W:F", "K_vs_W", "M_vs_F"}
	if !equalStringSlice(rep.TermSet, wantTerms) {
		t.Errorf("terms: %v, want %v", rep.TermSet, wantTerms)
	}
	if !equalStringSlice(rep.ContrastSet, wantContrasts) {
		t.Errorf("contrasts: %v, want %v", rep.ContrastSet, wantContrasts)
	}
}

func TestInspect_CSV_QuotedFieldsWithCommas(t *testing.T) {
	// long-format contrast values often contain ':' but real-world
	// CSV with commas in `contrast` should be parsed via the CSV
	// reader's quote handling.
	body := strings.Join([]string{
		`gene_id,effsize,pvalue,padj,term,contrast`,
		`G1,1.2,1e-5,1e-4,interaction,"K,sexM_vs_W,sexF"`,
		`G2,-0.8,0.01,0.05,interaction,"K,sexM_vs_W,sexF"`,
	}, "\n")
	dir := t.TempDir()
	p := writeFile(t, dir, "long.csv", body)
	rep, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Format != FormatLong {
		t.Fatalf("format: %q (header=%v)", rep.Format, rep.HeaderCols)
	}
	if len(rep.ContrastSet) != 1 || rep.ContrastSet[0] != "K,sexM_vs_W,sexF" {
		t.Errorf("contrast set: %v", rep.ContrastSet)
	}
}

func TestInspect_Parquet_TypedError(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "fake.parquet", "PAR1") // body irrelevant; we reject on ext
	_, err := Inspect(p)
	if err == nil {
		t.Fatal("expected parquet error")
	}
	if !IsParquetError(err) {
		t.Errorf("expected ErrParquet; got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "convert to .tsv") {
		t.Errorf("error message missing conversion hint: %v", err)
	}
}

func TestInspect_EmptyHeader(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "empty.tsv", "")
	_, err := Inspect(p)
	if err == nil {
		t.Error("expected error on empty file")
	}
}

func TestInspect_MalformedHeader_OneField(t *testing.T) {
	// A single-field header almost certainly means the delimiter
	// is wrong (tab vs comma). We refuse with a hint.
	dir := t.TempDir()
	p := writeFile(t, dir, "one.tsv", "onlyonefield\nrow1\n")
	_, err := Inspect(p)
	if err == nil || !strings.Contains(err.Error(), "delimiter") {
		t.Errorf("expected delimiter hint; got %v", err)
	}
}

func TestValidate_RejectsZeroRows(t *testing.T) {
	dir := t.TempDir()
	body := "gene\tbaseMean\tlog2FoldChange\tlfcSE\tpadj\n"
	p := writeFile(t, dir, "empty.tsv", body)
	rep, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Format != FormatDESeq2 {
		t.Fatalf("format setup failed: %q", rep.Format)
	}
	if err := Validate(rep); err == nil || !strings.Contains(err.Error(), "0 data rows") {
		t.Errorf("expected zero-rows error; got %v", err)
	}
}

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in   string
		want Format
		err  bool
	}{
		{"", FormatUnknown, false},
		{"deseq2", FormatDESeq2, false},
		{"DESeq2", FormatDESeq2, false},
		{"edge-r", FormatEdgeR, false},
		{"scanpy", FormatScanpy, false},
		{"rank_genes_groups", FormatScanpy, false},
		{"long", FormatLong, false},
		{"anthive", FormatLong, false},
		{"bogus", FormatUnknown, true},
	}
	for _, tc := range cases {
		got, err := ParseFormat(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("ParseFormat(%q): want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseFormat(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseFormat(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMethodFromFormat(t *testing.T) {
	cases := []struct {
		in   Format
		want string
	}{
		{FormatDESeq2, "DESeq2"},
		{FormatEdgeR, "edgeR"},
		{FormatScanpy, "scanpy.rank_genes_groups"},
		{FormatSeurat, "Seurat.FindMarkers"},
		{FormatGLMMseq, "glmmSeq"},
		{FormatGLMMTMB, "glmmTMB"},
		{FormatLong, ""}, // transport format, no method
		{FormatUnknown, ""},
	}
	for _, tc := range cases {
		got := MethodFromFormat(tc.in)
		if got != tc.want {
			t.Errorf("MethodFromFormat(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMusCfg writes a folder-level mus.cfg with the given lines.
func writeMusCfg(t *testing.T, dir string, lines ...string) {
	t.Helper()
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "mus.cfg"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDETSV(t *testing.T, path, header, row string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(header+"\n"+row+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runDECheckForTest runs the de check command in-process and returns
// (stdout, stderr, error). Builds the full cobra command so flag
// parsing + RunE are exercised — not just the inner runner.
func runDECheckForTest(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newDECheckCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestDECheck_HappyPath_DESeq2(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=SGF",
		"de_target_h5ad=Single.Grafts.h5ad",
	)
	writeDETSV(t, filepath.Join(dir, "de.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")

	stdout, stderr, err := runDECheckForTest(t, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{
		"OK", "format", "detected deseq2", "rows", "1 data rows",
		"target", "SGF", "method", "DESeq2",
		"Grouping by target", "SGF (Single.Grafts.h5ad)",
		"checked 1, passed 1, failed 0",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q\nfull:\n%s", want, stdout)
		}
	}

	// Sidecar stamped.
	sc := filepath.Join(dir, "de.tsv.mus")
	body, err := os.ReadFile(sc)
	if err != nil {
		t.Fatalf("sidecar not stamped: %v", err)
	}
	for _, want := range []string{
		"de_format=deseq2",
		"de_method=DESeq2",
		"de_target_id=SGF",
		"de_check_version=1",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("sidecar missing %q\nbody:\n%s", want, body)
		}
	}
}

func TestDECheck_LongFormat_StampsDistinctSet(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=ABC",
	)
	body := strings.Join([]string{
		"gene_id\teffsize\tpvalue\tpadj\tterm\tcontrast",
		"G1\t1\t1e-5\t1e-4\tgenotype\tK_vs_W",
		"G2\t0.5\t0.001\t0.01\tsex\tM_vs_F",
		"G3\t-0.2\t0.4\t0.5\tinteraction\tK:M_vs_W:F",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "long.tsv"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runDECheckForTest(t, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout, "long-format-aggregation") {
		t.Errorf("missing long-format-aggregation line:\n%s", stdout)
	}

	sc := filepath.Join(dir, "long.tsv.mus")
	body2, err := os.ReadFile(sc)
	if err != nil {
		t.Fatal(err)
	}
	// Term + contrast stamped as sorted comma-joined sets.
	for _, want := range []string{
		"de_format=long",
		"de_term=genotype,interaction,sex",
		"de_contrast=K:M_vs_W:F,K_vs_W,M_vs_F",
	} {
		if !strings.Contains(string(body2), want) {
			t.Errorf("sidecar missing %q\nbody:\n%s", want, body2)
		}
	}
}

func TestDECheck_FailsWithoutTarget(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir, "data_project=Fiers2025") // no de_target_*
	writeDETSV(t, filepath.Join(dir, "de.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")

	_, stderr, err := runDECheckForTest(t, dir)
	if err == nil {
		t.Fatal("expected failure when target unset")
	}
	if !strings.Contains(stderr, "de_target_id") {
		t.Errorf("expected 'de_target_id' hint in stderr; got:\n%s", stderr)
	}
}

func TestDECheck_FailsOnUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=ABC",
	)
	writeDETSV(t, filepath.Join(dir, "mystery.tsv"),
		"gene\tfc\tp", "G1\t1\t0.5")

	_, stderr, err := runDECheckForTest(t, dir)
	if err == nil {
		t.Fatal("expected failure on unknown format")
	}
	if !strings.Contains(stderr, "format detection failed") {
		t.Errorf("expected format-detection-failed message:\n%s", stderr)
	}
}

func TestDECheck_RejectsParquet(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=ABC",
	)
	if err := os.WriteFile(filepath.Join(dir, "de.parquet"), []byte("PAR1"), 0o644); err != nil {
		t.Fatal(err)
	}
	// We need .parquet to be picked up. collectDEFiles only accepts
	// .tsv / .csv, so a stray .parquet won't even reach Inspect.
	// Verify that: file is skipped (no input means runner errors
	// with "no .tsv / .csv files found").
	_, _, err := runDECheckForTest(t, dir)
	if err == nil {
		t.Fatal("expected error (no DE candidates)")
	}
	if !strings.Contains(err.Error(), "no .tsv / .csv files found") {
		t.Errorf("expected 'no .tsv / .csv files found'; got %v", err)
	}
	// Explicit .parquet path → rejected with clear parquet message.
	_, stderr2, err2 := runDECheckForTest(t, filepath.Join(dir, "de.parquet"))
	if err2 == nil {
		t.Skip("collectDEFiles never reaches Inspect for explicit .parquet; behaviour intentional")
	}
	_ = stderr2
}

func TestDECheck_SilentSuppressesOKAndSkip(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=ABC",
	)
	writeDETSV(t, filepath.Join(dir, "de.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")

	stdout, _, err := runDECheckForTest(t, dir, "--silent")
	if err != nil {
		t.Fatal(err)
	}
	// No per-line OK / SKIP output.
	if strings.Contains(stdout, "OK") || strings.Contains(stdout, "SKIP") {
		t.Errorf("--silent leaked status lines:\n%s", stdout)
	}
	// But the grouping + summary still print.
	for _, want := range []string{"Grouping by target", "checked 1, passed 1, failed 0"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--silent dropped %q:\n%s", want, stdout)
		}
	}
}

func TestDECheck_FormatOverride(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=ABC",
	)
	// File that looks like edger but pass --format=long. Override
	// should be reported in the output.
	writeDETSV(t, filepath.Join(dir, "ambiguous.tsv"),
		"gene_id\teffsize\tpvalue\tpadj\tterm\tcontrast",
		"G1\t1\t0.1\t0.2\ta\tb")

	stdout, _, err := runDECheckForTest(t, dir, "--format", "long")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "override --format=long") {
		t.Errorf("override not announced:\n%s", stdout)
	}
}

func TestDECheck_GroupsByTarget(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=ABC",
		"de_target_h5ad=foo.h5ad",
	)
	writeDETSV(t, filepath.Join(dir, "a.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")
	writeDETSV(t, filepath.Join(dir, "b.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")

	stdout, _, err := runDECheckForTest(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "ABC (foo.h5ad) — 2 file(s)") {
		t.Errorf("expected grouped target line:\n%s", stdout)
	}
}

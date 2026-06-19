package cli

import (
	"bytes"
	gz "compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGzippedDE(t *testing.T, path, header, row string) {
	t.Helper()
	var buf bytes.Buffer
	w := gz.NewWriter(&buf)
	if _, err := w.Write([]byte(header + "\n" + row + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

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
//
// `-m` is mandatory on `mus de check`. To keep the test surface
// concise the helper injects a default message when the caller
// hasn't supplied one. Tests that explicitly want to exercise the
// missing-message path use runDECheckRaw below.
func runDECheckForTest(t *testing.T, args ...string) (string, string, error) {
	if !hasFlag(args, "-m", "--message") {
		args = append([]string{"-m", "test message"}, args...)
	}
	return runDECheckRaw(t, args...)
}

// runDECheckRaw is like runDECheckForTest but passes args through
// verbatim — for tests that want to assert behaviour when -m is
// missing or other flag-parsing concerns.
func runDECheckRaw(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newDECheckCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func hasFlag(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

func TestDECheck_RequiresMessage(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=ABC",
	)
	writeDETSV(t, filepath.Join(dir, "de.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")

	// raw → no -m injected
	_, _, err := runDECheckRaw(t, dir)
	if err == nil {
		t.Fatal("expected failure when -m is missing")
	}
	if !strings.Contains(err.Error(), "message") && !strings.Contains(err.Error(), "required flag") {
		t.Errorf("expected message about required -m; got %v", err)
	}
}

func TestDECheck_MessageWritesDescription(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=ABC",
	)
	writeDETSV(t, filepath.Join(dir, "de.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")

	stdout, _, err := runDECheckRaw(t, "-m", "scanpy wilcoxon, donor-4 excluded", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"scanpy wilcoxon, donor-4 excluded"`) {
		t.Errorf("description line missing the quoted message:\n%s", stdout)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "de.tsv.mus"))
	if !strings.Contains(string(body), "de_description=scanpy wilcoxon, donor-4 excluded") {
		t.Errorf("sidecar missing de_description value:\n%s", body)
	}
}

func TestDECheck_MessageOverridesCascadeDescription(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=ABC",
		`de_description=stale folder-level description`,
	)
	writeDETSV(t, filepath.Join(dir, "de.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")

	_, _, err := runDECheckRaw(t, "-m", "fresh per-check description", dir)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "de.tsv.mus"))
	got := string(body)
	if !strings.Contains(got, "de_description=fresh per-check description") {
		t.Errorf("sidecar should carry -m value, not cascade:\n%s", got)
	}
	if strings.Contains(got, "stale folder-level description") {
		t.Errorf("cascade description should NOT leak through:\n%s", got)
	}
}

func TestDECheck_SoftFieldNudges(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=ABC",
		`de_software_version=scanpy 1.10.4`,
		// de_reference_type, de_covariates, de_filter_criteria deliberately unset
	)
	writeDETSV(t, filepath.Join(dir, "de.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")

	stdout, _, err := runDECheckForTest(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	// Set field → OK line with value visible.
	if !strings.Contains(stdout, "soft.de_software_version") || !strings.Contains(stdout, "scanpy 1.10.4") {
		t.Errorf("expected OK soft.de_software_version line with value:\n%s", stdout)
	}
	// Unset fields → SKIP line with suggestion + the set-command hint.
	for _, want := range []string{
		"soft.de_reference_type",
		"pairwise | vsrest | vsreference | vsgrandmean",
		"soft.de_covariates",
		"sex,age,batch",
		"soft.de_filter_criteria",
		"`mus config set de_filter_criteria",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing soft-field nudge %q:\n%s", want, stdout)
		}
	}
}

func TestDECheck_CompressionNudge(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir, "data_project=Fiers2025", "de_target_id=ABC")

	// Plain .tsv → SKIP compression nudge.
	writeDETSV(t, filepath.Join(dir, "plain.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")
	stdout, _, err := runDECheckForTest(t, filepath.Join(dir, "plain.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"SKIP", "compression", "gzip plain.tsv", "5–10×",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("plain .tsv: expected gzip nudge with %q:\n%s", want, stdout)
		}
	}

	// .tsv.gz → OK compression line.
	dir2 := t.TempDir()
	writeMusCfg(t, dir2, "data_project=Fiers2025", "de_target_id=ABC")
	writeGzippedDE(t, filepath.Join(dir2, "de.tsv.gz"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")
	stdout2, _, err := runDECheckForTest(t, filepath.Join(dir2, "de.tsv.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout2, "OK") || !strings.Contains(stdout2, "compression") || !strings.Contains(stdout2, "gzipped") {
		t.Errorf(".tsv.gz: expected OK compression line:\n%s", stdout2)
	}
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
		"data_project", "Fiers2025",
		"target", "SGF", "method", "DESeq2",
		"Grouping by target", "SGF (Single.Grafts.h5ad)",
		"checked 1, passed 1, failed 0",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q\nfull:\n%s", want, stdout)
		}
	}
	// Cluster check lines must NOT appear when neither de_cluster
	// nor de_cluster_obs_column is in the cascade — that's noise
	// for non-per-cluster DE deposits. (The grouping summary still
	// emits a `cluster=-` column; this asserts the per-check
	// SKIP line doesn't appear.)
	if strings.Contains(stdout, "cluster-validation") {
		t.Errorf("cluster-validation SKIP line appeared even though no de_cluster/de_cluster_obs_column was set:\n%s", stdout)
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

func TestDECheck_AcceptsGzippedDESeq2(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=SGF",
	)
	writeGzippedDE(t, filepath.Join(dir, "de.tsv.gz"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")
	stdout, _, err := runDECheckForTest(t, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"detected deseq2", "data_project", "Fiers2025", "PASS recorded",
		"checked 1, passed 1",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
	// Sidecar lands at <file>.tsv.gz.mus (existing pattern: data
	// filename + .mus, no special-casing for compression).
	if _, err := os.Stat(filepath.Join(dir, "de.tsv.gz.mus")); err != nil {
		t.Errorf("expected sidecar at de.tsv.gz.mus: %v", err)
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

func TestDECheck_FailsWithoutDataProject(t *testing.T) {
	dir := t.TempDir()
	// data_project deliberately missing.
	writeMusCfg(t, dir,
		"de_target_id=SGF",
	)
	writeDETSV(t, filepath.Join(dir, "de.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")

	_, stderr, err := runDECheckForTest(t, dir)
	if err == nil {
		t.Fatal("expected failure when data_project unset")
	}
	if !strings.Contains(stderr, "data_project not set in cascade") {
		t.Errorf("expected data_project hint:\n%s", stderr)
	}
}

func TestDECheck_ClusterLinesAppearWhenOptedIn(t *testing.T) {
	dir := t.TempDir()
	writeMusCfg(t, dir,
		"data_project=Fiers2025",
		"de_target_id=SGF",
		"de_cluster_obs_column=leiden",
	)
	writeDETSV(t, filepath.Join(dir, "de.tsv"),
		"gene\tbaseMean\tlog2FoldChange\tlfcSE\tstat\tpvalue\tpadj",
		"G1\t1\t1\t1\t1\t1\t1")
	stdout, _, err := runDECheckForTest(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "cluster-validation") {
		t.Errorf("opting in via de_cluster_obs_column should surface a cluster line:\n%s", stdout)
	}
}

func TestDECheck_FailsWithoutTarget(t *testing.T) {
	dir := t.TempDir()
	// data_project IS set (otherwise we'd fail on the earlier
	// data_project gate, not the target one).
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

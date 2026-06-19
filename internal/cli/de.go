package cli

// `mus de` subtree — validates differential-expression result
// files for deposit into the anthive catalog.
//
//   mus de check [FOLDER|FILE]    validate + stamp <basename>.mus
//   mus de howto                  depositor primer (formats, fields)
//
// The check is verbose by default (every inference / fallback /
// skip is announced on stdout) with --silent for CI use. Format
// detection is always-on; --format X overrides if the depositor
// knows better than the column-signature matcher.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/de"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

func newDECmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "de",
		Short: "validate differential-expression result files for catalog deposit",
	}
	cmd.AddCommand(newDECheckCmd(), newDEHowtoCmd())
	return cmd
}

// --- mus de check -----------------------------------------------------------

func newDECheckCmd() *cobra.Command {
	var formatOverride string
	var silent bool
	cmd := &cobra.Command{
		Use:   "check [FOLDER|FILE...]",
		Short: "validate DE result files; stamp <basename>.mus on PASS",
		Long: "Walks the given folder(s) and/or file(s), validates every\n" +
			".tsv / .csv against one of the recognised DE formats\n" +
			"(deseq2 / edger / scanpy / seurat / glmmseq / glmmtmb / long),\n" +
			"and stamps a <basename>.mus sidecar carrying the de_* metadata\n" +
			"on PASS. Files without a passing sidecar are refused by\n" +
			"`mus s3 upload`.\n\n" +
			"Folder-level metadata (de_target_id, de_cluster_obs_column, …)\n" +
			"lives in the cascading mus.cfg — no separate de.cfg.\n\n" +
			"Format detection is always-on (column-name signature match).\n" +
			"--format X overrides detection when the depositor knows better.\n\n" +
			"Verbose by default — every inference (format match, auto-filled\n" +
			"method, computed long-format distinct sets, skipped checks) is\n" +
			"announced on stdout. --silent suppresses everything except\n" +
			"failures and the final summary.\n\n" +
			"Output ends with a 'Grouping by target' section that groups\n" +
			"every passed file by its de_target_id / de_target_h5ad —\n" +
			"useful for catching a typo before deposit.\n\n" +
			"Parquet input is REJECTED (no DE tool writes parquet natively;\n" +
			"convert to .tsv via pandas/pyarrow if you have parquet upstream).",
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDECheck(cmd, args, formatOverride, silent)
		},
	}
	cmd.Flags().StringVar(&formatOverride, "format", "",
		"override format detection (one of: "+strings.Join(de.FormatNames(), ", ")+")")
	cmd.Flags().BoolVar(&silent, "silent", false,
		"suppress OK / SKIP lines; only print failures + final summary")
	return cmd
}

// --- mus de howto -----------------------------------------------------------

func newDEHowtoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "howto",
		Short: "print the depositor primer for DE deposits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := cmd.OutOrStdout().Write([]byte(deHowtoText))
			return err
		},
	}
}

// --- runner -----------------------------------------------------------------

// dePassFile is one row of the per-file PASS table for the
// grouping summary. Collected as we walk; emitted at the end.
type dePassFile struct {
	Path       string
	Format     de.Format
	RowCount   int64
	TargetID   string
	TargetH5ad string
	Cluster    string
}

func runDECheck(cmd *cobra.Command, args []string, formatOverride string, silent bool) error {
	if len(args) == 0 {
		args = []string{"."}
	}
	override, err := de.ParseFormat(formatOverride)
	if err != nil {
		return err
	}

	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	out := newDEReporter(stdout, stderr, silent)

	cache, err := hashcache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	// Walk arguments → list of (file, declared-context). Each unique
	// folder is loaded ONCE and its cascade re-used across that
	// folder's files (folders deeper down get their own load).
	files, err := collectDEFiles(args)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no .tsv / .csv files found in %s", strings.Join(args, ", "))
	}

	// Pre-load cascades by folder, lazily.
	envByDir := map[string]*config.Env{}
	loadEnv := func(dir string) (*config.Env, error) {
		if e, ok := envByDir[dir]; ok {
			return e, nil
		}
		e, err := config.Load(dir)
		if err != nil {
			return nil, fmt.Errorf("load cascade for %s: %w", dir, err)
		}
		envByDir[dir] = e
		return e, nil
	}

	var passed []dePassFile
	checked, failed := 0, 0
	for _, f := range files {
		envForFile, err := loadEnv(filepath.Dir(f))
		if err != nil {
			out.errf("load-cascade", f, "%v", err)
			failed++
			continue
		}
		ok, info := checkOneDEFile(cache, f, envForFile, override, out)
		checked++
		if !ok {
			failed++
			continue
		}
		passed = append(passed, info)
	}

	// Grouping by target — the killer feature anthive4 asked for.
	if len(passed) > 0 {
		out.section("Grouping by target")
		groups := groupPassedByTarget(passed)
		keys := make([]string, 0, len(groups))
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			rows := groups[k]
			var total int64
			for _, r := range rows {
				total += r.RowCount
			}
			fmt.Fprintf(stdout, "  %s — %d file(s), %d rows total\n",
				k, len(rows), total)
			for _, r := range rows {
				fmt.Fprintf(stdout, "      %s   format=%s rows=%d cluster=%s\n",
					filepath.Base(r.Path), r.Format, r.RowCount, or(r.Cluster, "-"))
			}
		}
	}

	fmt.Fprintf(stdout, "\nchecked %d, passed %d, failed %d\n",
		checked, checked-failed, failed)
	if failed > 0 {
		return fmt.Errorf("%d DE file(s) failed", failed)
	}
	return nil
}

// checkOneDEFile validates one file and (on PASS) writes its
// sidecar. Returns (true, info) on PASS, (false, _) on failure.
// All output (verbose / failure / skip) goes through `out`.
func checkOneDEFile(cache *hashcache.Cache, path string, env *config.Env,
	formatOverride de.Format, out *deReporter) (bool, dePassFile) {

	out.fileHeader(path)

	// 1. Inspect (format detect + body scan).
	rep, err := de.Inspect(path)
	if err != nil {
		if de.IsParquetError(err) {
			out.errf("format", path, "%v", err)
		} else {
			out.errf("format", path, "%v", err)
		}
		return false, dePassFile{}
	}
	chosen := rep.Format
	if formatOverride != de.FormatUnknown {
		chosen = formatOverride
		rep.Format = formatOverride
		out.ok("format", path,
			fmt.Sprintf("override --format=%s", formatOverride))
	} else if chosen == de.FormatUnknown {
		// Validate will produce the right error; let it speak.
	} else {
		out.ok("format", path, fmt.Sprintf(
			"detected %s (matched: %s)",
			chosen, strings.Join(rep.MatchedCols, ", ")))
	}

	// 2. Validate.
	if err := de.Validate(rep); err != nil {
		out.errf("format", path, "%v", err)
		return false, dePassFile{}
	}
	out.ok("rows", path, fmt.Sprintf("%d data rows", rep.RowCount))

	// 3. Long-format auto: stamp the distinct set.
	if rep.Format == de.FormatLong {
		out.ok("long-format-aggregation", path, fmt.Sprintf(
			"terms=%s; contrasts=%s",
			strings.Join(rep.TermSet, ", "),
			strings.Join(rep.ContrastSet, ", ")))
	}

	// 4a. data_project (HARD) — anthive's grouping key. Without it,
	//     the upload gate can't compute an S3 destination prefix
	//     and ant-ingest can't locate the deposit's catalog home.
	dataProject := env.String("data_project")
	if dataProject == "" {
		out.errf("data_project", path,
			"data_project not set in cascade — "+
				"`mus config set data_project NameYear` (or `mus eln link <id>`)")
		return false, dePassFile{}
	}
	out.ok("data_project", path, dataProject)

	// 4b. Target dataset (HARD-one-of).
	target := env.String("de_target_id")
	targetH5ad := env.String("de_target_h5ad")
	if target == "" && targetH5ad == "" {
		out.errf("target", path,
			"neither de_target_id nor de_target_h5ad set in cascade — "+
				"`mus config set de_target_id <ID>` (preferred) or "+
				"`mus config set de_target_h5ad <BASENAME>`")
		return false, dePassFile{}
	}
	out.ok("target", path, describeTarget(target, targetH5ad))

	// 5. Method (auto-fill from format).
	method := env.String("de_method")
	if method == "" {
		method = de.MethodFromFormat(chosen)
		if method != "" {
			out.ok("method", path,
				fmt.Sprintf("auto-filled %q from de_format=%s", method, chosen))
		} else {
			out.skip("method", path, "no canonical name for de_format=long")
		}
	} else {
		out.ok("method", path, fmt.Sprintf("set in cascade: %q", method))
	}

	// 6. Cluster validation (light: just report; deep validation
	//    against target h5ad obs comes in a later commit).
	//
	// Only surface cluster-related lines when the depositor has
	// opted into per-cluster DE — either by declaring a cluster
	// value or by naming an obs column. Most deposits (e.g. a
	// single wilcoxon across the whole dataset) aren't per-cluster
	// and don't need this noise.
	cluster := env.String("de_cluster") // can also be per-file in sidecar (future)
	clusterCol := env.String("de_cluster_obs_column")
	if cluster != "" || clusterCol != "" {
		switch {
		case cluster == "":
			out.skip("cluster-validation", path,
				"de_cluster_obs_column=\""+clusterCol+"\" set but no de_cluster on this file — nothing to validate")
		case clusterCol == "":
			out.skip("cluster-validation", path,
				"de_cluster=\""+cluster+"\" set but de_cluster_obs_column unset — "+
					"OK for multi-factorial designs; set the obs column to enable validation")
		default:
			// Real validation lands in a follow-up commit (task #91).
			out.skip("cluster-validation", path, fmt.Sprintf(
				"deferred (will check %q is in target_h5ad.obs[%s]) — "+
					"placeholder until task #91 wires the inspector", cluster, clusterCol))
		}
	}

	// 7. Stamp the sidecar.
	if err := stampDESidecar(cache, path, rep, env, chosen, method, target, targetH5ad); err != nil {
		out.errf("sidecar", path, "%v", err)
		return false, dePassFile{}
	}
	out.ok("sidecar", sidecar.SidecarPath(path), "PASS recorded")

	return true, dePassFile{
		Path:       path,
		Format:     chosen,
		RowCount:   rep.RowCount,
		TargetID:   target,
		TargetH5ad: targetH5ad,
		Cluster:    cluster,
	}
}

// stampDESidecar writes / refreshes the per-file sidecar at
// <path>.mus with the DE block populated from the report +
// cascade.
func stampDESidecar(cache *hashcache.Cache, path string, rep *de.Report,
	env *config.Env, chosen de.Format, method, target, targetH5ad string) error {

	scPath := sidecar.SidecarPath(path)
	doc, err := readMaybe(scPath)
	if err != nil {
		return err
	}
	if doc == nil {
		doc = &sidecar.Doc{}
	}

	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	sha, err := cache.Sum(path)
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(path)
	doc.File = sidecar.FileInfo{
		Sha256:  sha,
		Size:    st.Size(),
		Mtime:   st.ModTime().UTC().Truncate(time.Second),
		Hashed:  time.Now().UTC().Truncate(time.Second),
		AbsPath: abs,
	}

	if dp := env.String("data_project"); dp != "" {
		doc.DataProject = dp
	}
	doc.DE = &sidecar.DE{
		Format:                string(chosen),
		Method:                method,
		Cluster:               env.String("de_cluster"),
		TargetID:              target,
		TargetH5ad:            targetH5ad,
		ClusterObsColumn:      env.String("de_cluster_obs_column"),
		SoftwareVersion:       env.String("de_software_version"),
		ReferenceType:         env.String("de_reference_type"),
		Covariates:            env.List("de_covariates"),
		FilterCriteria:        env.String("de_filter_criteria"),
		Description:           env.String("de_description"),
		RowCount:              rep.RowCount,
		InspectedAt:           time.Now().UTC().Truncate(time.Second),
		InspectedSha256:       sha,
		InspectedCheckVersion: de.CheckVersion,
	}
	// Long-format: stamp the computed distinct sets; otherwise fall
	// back to the cascade-declared single value(s).
	if chosen == de.FormatLong {
		doc.DE.Term = append([]string(nil), rep.TermSet...)
		doc.DE.Contrast = append([]string(nil), rep.ContrastSet...)
	} else {
		if v := env.String("de_term"); v != "" {
			doc.DE.Term = []string{v}
		}
		if v := env.String("de_contrast"); v != "" {
			doc.DE.Contrast = []string{v}
		}
	}
	return sidecar.Write(scPath, doc)
}

// collectDEFiles walks `args` recursively, returning every .tsv /
// .csv that is NOT a sidecar (*.mus) and NOT an explicit skip name
// (none today; reserved for future). Each result is a clean
// absolute path.
func collectDEFiles(args []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		if seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if st.IsDir() {
			if err := filepath.WalkDir(a, func(p string, d os.DirEntry, werr error) error {
				if werr != nil {
					return werr
				}
				if d.IsDir() {
					return nil
				}
				if isDECandidate(p) {
					add(p)
				}
				return nil
			}); err != nil {
				return nil, err
			}
			continue
		}
		if isDECandidate(a) {
			add(a)
		}
	}
	sort.Strings(out)
	return out, nil
}

func isDECandidate(path string) bool {
	if sidecar.IsSidecar(path) {
		return false
	}
	// Accept plain + gzipped TSV/CSV. The gz path is decompressed
	// transparently in internal/de.Inspect; from mus's perspective
	// .tsv.gz is just .tsv that takes less S3 quota.
	lower := strings.ToLower(path)
	for _, ext := range []string{".tsv", ".csv", ".tsv.gz", ".csv.gz"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func describeTarget(id, h5ad string) string {
	switch {
	case id != "" && h5ad != "":
		return fmt.Sprintf("de_target_id=%s (h5ad=%s)", id, h5ad)
	case id != "":
		return fmt.Sprintf("de_target_id=%s", id)
	default:
		return fmt.Sprintf("de_target_h5ad=%s", h5ad)
	}
}

func groupPassedByTarget(rows []dePassFile) map[string][]dePassFile {
	out := map[string][]dePassFile{}
	for _, r := range rows {
		var key string
		switch {
		case r.TargetID != "" && r.TargetH5ad != "":
			key = fmt.Sprintf("%s (%s)", r.TargetID, r.TargetH5ad)
		case r.TargetID != "":
			key = r.TargetID
		default:
			key = r.TargetH5ad
		}
		out[key] = append(out[key], r)
	}
	return out
}

// --- reporter (verbose / silent) -------------------------------------------

// deReporter mirrors irods checkPrinter's role: one per-check
// line, suppressible OK / SKIP via --silent, failures always print
// on stderr.
type deReporter struct {
	stdout io.Writer
	stderr io.Writer
	silent bool
}

func newDEReporter(stdout, stderr io.Writer, silent bool) *deReporter {
	return &deReporter{stdout: stdout, stderr: stderr, silent: silent}
}

func (r *deReporter) fileHeader(path string) {
	if r.silent {
		return
	}
	fmt.Fprintf(r.stdout, "\n[%s]\n", path)
}

func (r *deReporter) section(name string) {
	fmt.Fprintf(r.stdout, "\n%s\n", name)
}

func (r *deReporter) printLine(w io.Writer, status, check, target, note string) {
	if note == "" {
		fmt.Fprintf(w, "  %-9s %-26s %s\n", status, check, target)
		return
	}
	fmt.Fprintf(w, "  %-9s %-26s %s  (%s)\n", status, check, target, note)
}

func (r *deReporter) ok(check, target, note string) {
	if r.silent {
		return
	}
	r.printLine(r.stdout, "OK", check, target, note)
}
func (r *deReporter) skip(check, target, note string) {
	if r.silent {
		return
	}
	r.printLine(r.stdout, "SKIP", check, target, note)
}
func (r *deReporter) errf(check, target, format string, args ...any) {
	r.printLine(r.stderr, "ERROR", check, target, fmt.Sprintf(format, args...))
}

// deHowtoText is the depositor primer for DE deposits — printed
// by `mus de howto`. Kept brief; the tutorial covers the full
// workflow.
const deHowtoText = `Preparing a differential-expression deposit (mus + anthive)
============================================================

This is a depositor-side primer. Run 'mus de check <folder>' to
validate; the catalog (anthive) accepts only files that pass it.

1. WHAT MUS RECOGNISES
   - File extensions: .tsv  and  .csv
   - Parquet is REFUSED at the deposit gate (convert with pandas /
     pyarrow first; no DE tool writes parquet natively).
   - Format detection is by column-name signature, case- and
     separator-insensitive (log2FoldChange == LOG_2_FC == Log2.Fold.Change).
   - Supported formats (mus picks one based on the header):
       deseq2   baseMean + log2FoldChange + lfcSE + padj
       edger    logFC + logCPM + FDR
       scanpy   names + logfoldchanges + pvals_adj
       seurat   avg_log2FC + pct.1 + pct.2 + p_val_adj
       glmmseq  chisq + df + P_FixedEffects
       glmmtmb  Estimate + Std. Error + z value + Pr(>|z|)
       long     gene_id + effsize + pvalue + padj + term + contrast
   - --format X overrides if mus picks the wrong one.

2. FOLDER METADATA — mus.cfg with de_* keys
   Cascades the existing way; one mus.cfg can carry h5ad biology
   fields (organism, tissue, …) AND DE fields side by side.
   HARD-required at the deposit gate:
       data_project        e.g. Fiers2025
       de_format           (or inferable from the columns)
       de_target_id        3-char anthive id of the target h5ad   PREFERRED
       de_target_h5ad      basename within data_project           (escape hatch
                                                                   for co-ingest)
   AUTO-FILLED FROM de_format IF UNSET:
       de_method           DESeq2 / edgeR / scanpy.rank_genes_groups / …
   SOFT (passed through to anthive):
       de_term             single value for one-term presets
       de_contrast         single value for one-contrast presets
       de_cluster_obs_column   obs col in target h5ad to validate de_cluster against
       de_software_version
       de_reference_type   vsreference | pairwise | vsgrandmean
       de_covariates       e.g. sex,age,batch
       de_filter_criteria
       de_description

3. PER-FILE SIDECAR — <basename>.mus (mus writes it)
   The sidecar gets the de_* block stamped automatically on PASS:
       de_format / de_method / de_term / de_contrast / de_cluster
       de_row_count / de_check_passed_at / de_inspected_sha256 /
       de_check_version
   Don't hand-edit — mus refreshes it every time you re-run
   'mus de check'.

4. LONG FORMAT ('de_format=long')
   The body carries 'term' and 'contrast' columns per row. Set
   de_term=auto and de_contrast=auto in mus.cfg; 'mus de check'
   computes the distinct {term × contrast} set across the body and
   stamps it sorted+comma-joined into the sidecar. Saves anthive a
   re-parse and gives you a visible audit of what's in the file.

5. CO-INGEST (h5ad + DE in one upload)
   Layout your upload folder as:
       upload/
       ├── mus.cfg                 # data_project + h5ad biology + de_target_id
       ├── <name>.h5ad
       ├── <name>.h5ad.mus
       └── de/                     # optional; flat alongside h5ad also works
           ├── <files>.tsv
           └── <files>.tsv.mus
   Then 'mus s3 upload upload/' runs BOTH gates (mus h5ad check +
   mus de check) and refuses the WHOLE batch on any failure.

6. SHORTCUTS
       mus de check FOLDER         # validate everything in the folder
       mus de check FOLDER --silent   # CI mode: only failures + summary
       mus de check FILE --format long  # force a specific format
       mus s3 upload FOLDER        # gate runs again at upload time

Anthive ingests from __incoming/<data_project>/de/<sub>/. Files
without a passing .mus sidecar are refused at upload.
`

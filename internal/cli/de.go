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
	"context"
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
	"codeberg.org/mfiers/mus/internal/vark"
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
	var formatOverride, message string
	var silent bool
	cmd := &cobra.Command{
		Use:   "check -m MESSAGE [FOLDER|FILE...]",
		Short: "validate DE result files; stamp <basename>.mus on PASS",
		Long: "Walks the given folder(s) and/or file(s), validates every\n" +
			".tsv / .csv (plus .tsv.gz / .csv.gz) against one of the\n" +
			"recognised DE formats (deseq2 / edger / scanpy / seurat /\n" +
			"glmmseq / glmmtmb / long), and stamps a <basename>.mus sidecar\n" +
			"carrying the de_* metadata on PASS. Files without a passing\n" +
			"sidecar are refused by `mus s3 upload`.\n\n" +
			"A -m / --message is REQUIRED. Like a git commit message —\n" +
			"describes what this DE analysis is, why it was run, anything\n" +
			"a future reader would want to know. The message is stamped as\n" +
			"`de_description` on every sidecar this check writes, overriding\n" +
			"any `de_description=` from the mus.cfg cascade. No silent\n" +
			"checks.\n\n" +
			"Folder-level metadata (de_dataset_id, de_cluster_obs_column,\n" +
			"de_software_version, …) lives in the cascading mus.cfg — no\n" +
			"separate de.cfg. Use `mus config set de_<key> <value>` to write\n" +
			"each field once and have it cascade to every file underneath.\n\n" +
			"Format detection is always-on (column-name signature match).\n" +
			"--format X overrides detection when the depositor knows better.\n\n" +
			"Verbose by default — every inference (format match, auto-filled\n" +
			"method, computed long-format distinct sets, soft-field nudges,\n" +
			"skipped checks) is announced on stdout. --silent suppresses\n" +
			"everything except failures and the final summary.\n\n" +
			"Output ends with a 'Grouping by dataset' section that groups\n" +
			"every passed file by its de_dataset_id / de_dataset_h5ad —\n" +
			"useful for catching a typo before deposit.\n\n" +
			"Parquet input is REJECTED (no DE tool writes parquet natively;\n" +
			"convert to .tsv via pandas/pyarrow if you have parquet upstream).",
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg := strings.TrimSpace(message)
			if msg == "" {
				return fmt.Errorf("`mus de check` requires a -m / --message describing the analysis")
			}
			return runDECheck(cmd, args, formatOverride, msg, silent)
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "",
		"describe what this DE analysis is / why this check is running (REQUIRED) — "+
			"stamped as de_description on every sidecar")
	_ = cmd.MarkFlagRequired("message")
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
	DatasetID   string
	DatasetH5ad string
	Cluster    string
}

func runDECheck(cmd *cobra.Command, args []string, formatOverride, message string, silent bool) error {
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
	anyUnsetSoft := false
	for _, f := range files {
		envForFile, err := loadEnv(filepath.Dir(f))
		if err != nil {
			out.fileHeader(f)
			out.errf("load-cascade", "%v", err)
			failed++
			continue
		}
		ok, info, unsetSoft := checkOneDEFile(cache, f, envForFile, override, message, out)
		checked++
		if unsetSoft {
			anyUnsetSoft = true
		}
		if !ok {
			failed++
			continue
		}
		passed = append(passed, info)
	}

	// Print the soft-parameter hint ONCE, only when at least one
	// file had an unset soft field. Keeps the output compact for
	// fully-annotated deposits; gives the depositor a one-line
	// pointer when something's missing.
	if anyUnsetSoft && !silent {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Set soft parameters via: mus config set <NAME> <VALUE>")
	}

	// Grouping by dataset — the killer feature anthive4 asked for.
	if len(passed) > 0 {
		out.section("Grouping by dataset")
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
// sidecar. Returns (ok, info, unsetSoft) — `ok` is the PASS flag,
// `info` populates the grouping summary, `unsetSoft` is true when
// at least one soft cascade field was missing (used by the caller
// to print the one-shot "Set soft parameters …" hint).
//
// All output (verbose / failure / skip) goes through `out`. The
// `message` arg is the mandatory -m / --message value; it lands
// as `de_description` on the stamped sidecar, overriding any
// cascade-level de_description.
//
// Status lines are compact: status + check label + (note). The
// file path is in the per-file header above the lines, never
// repeated per row.
func checkOneDEFile(cache *hashcache.Cache, path string, env *config.Env,
	formatOverride de.Format, message string, out *deReporter) (bool, dePassFile, bool) {

	out.fileHeader(path)

	// 1. Inspect (format detect + body scan).
	rep, err := de.Inspect(path)
	if err != nil {
		out.errf("format", "%v", err)
		return false, dePassFile{}, false
	}
	chosen := rep.Format
	if formatOverride != de.FormatUnknown {
		chosen = formatOverride
		rep.Format = formatOverride
		out.ok("format", fmt.Sprintf("override --format=%s", formatOverride))
	} else if chosen == de.FormatUnknown {
		// Validate will produce the right error; let it speak.
	} else {
		out.ok("format", fmt.Sprintf(
			"detected %s (matched: %s)",
			chosen, strings.Join(rep.MatchedCols, ", ")))
	}

	// 2. Validate.
	if err := de.Validate(rep); err != nil {
		out.errf("format", "%v", err)
		return false, dePassFile{}, false
	}
	out.ok("rows", fmt.Sprintf("%d data rows", rep.RowCount))

	// 3. Long-format auto: stamp the distinct set.
	if rep.Format == de.FormatLong {
		out.ok("long-format-aggregation", fmt.Sprintf(
			"terms=%s; contrasts=%s",
			strings.Join(rep.TermSet, ", "),
			strings.Join(rep.ContrastSet, ", ")))
	}

	// 4a. data_project (HARD).
	dataProject := env.String("data_project")
	if dataProject == "" {
		out.errf("data_project",
			"data_project not set in cascade — "+
				"`mus config set data_project NameYear` (or `mus eln link <id>`)")
		return false, dePassFile{}, false
	}
	out.ok("data_project", dataProject)

	// 4b. Target dataset (HARD-one-of).
	datasetID := env.String("de_dataset_id")
	datasetH5ad := env.String("de_dataset_h5ad")
	if datasetID == "" && datasetH5ad == "" {
		out.errf("dataset",
			"neither de_dataset_id nor de_dataset_h5ad set — "+
				"`mus config set de_dataset_id <ID>` (preferred) or "+
				"`mus config set de_dataset_h5ad <BASENAME>`")
		return false, dePassFile{}, false
	}
	out.ok("dataset", describeDataset(datasetID, datasetH5ad))

	// 4c. Optional vark cross-check — when the `vark` binary is on
	//     PATH, query the catalog for this data_project and verify
	//     the declared de_dataset_id / de_dataset_h5ad resolves to
	//     a real entry. Warns on no match (typo or first-time
	//     deposit). Never blocks the check — anthive is the
	//     authoritative resolver at ingest.
	if vark.Available() {
		reportVarkCheck(env, dataProject, datasetID, datasetH5ad, out)
	} else {
		out.skip("vark-check",
			"`vark` not on PATH — install vark to enable pre-deposit "+
				"catalog cross-check (https://codeberg.org/mfiers/vark)")
	}

	// 5. Method (auto-fill from format).
	method := env.String("de_method")
	if method == "" {
		method = de.MethodFromFormat(chosen)
		if method != "" {
			out.ok("method", fmt.Sprintf("auto-filled %q from de_format=%s", method, chosen))
		} else {
			out.skip("method", "no canonical name for de_format=long")
		}
	} else {
		out.ok("method", fmt.Sprintf("set in cascade: %q", method))
	}

	// 6. Cluster validation (light: just report; deep validation
	//    against datasetID h5ad obs comes in a later commit).
	//
	// Only surface cluster-related lines when the depositor has
	// opted into per-cluster DE — either by declaring a cluster
	// value or by naming an obs column.
	cluster := env.String("de_cluster")
	clusterCol := env.String("de_cluster_obs_column")
	if cluster != "" || clusterCol != "" {
		switch {
		case cluster == "":
			out.skip("cluster-validation",
				`de_cluster_obs_column set but no de_cluster on this file — nothing to validate`)
		case clusterCol == "":
			out.skip("cluster-validation",
				"de_cluster set but de_cluster_obs_column unset — "+
					"OK for multi-factorial designs")
		default:
			out.skip("cluster-validation", fmt.Sprintf(
				"deferred (will check %q is in dataset h5ad obs[%s])", cluster, clusterCol))
		}
	}

	// 7. Description (from the mandatory -m flag).
	out.ok("description", fmt.Sprintf("%q", message))

	// 8. Soft-field nudges. Returns true when at least one soft
	//    field was unset — propagates to the caller for the
	//    one-shot bottom hint.
	unsetSoft := reportSoftFields(env, out)

	// 9. Storage nudge: suggest gzipping plain .tsv / .csv files.
	if !isGzippedFile(path) {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
		out.skip("compression",
			"plain "+ext+" — `gzip` for ~5–10× S3 savings")
	} else {
		out.ok("compression", "gzipped — good for S3 storage / bandwidth")
	}

	// 10. Stamp the sidecar.
	if err := stampDESidecar(cache, path, rep, env, chosen, method, datasetID, datasetH5ad, message); err != nil {
		out.errf("sidecar", "%v", err)
		return false, dePassFile{}, unsetSoft
	}
	out.ok("sidecar", "PASS recorded")

	return true, dePassFile{
		Path:       path,
		Format:     chosen,
		RowCount:   rep.RowCount,
		DatasetID:   datasetID,
		DatasetH5ad: datasetH5ad,
		Cluster:    cluster,
	}, unsetSoft
}

// softFieldDef describes one optional de_* cascade key for the
// reportSoftFields nudge surface. `Suggestion` is the short
// example / value menu shown when the key is unset.
type softFieldDef struct {
	Key        string
	Suggestion string // value menu / example — kept compact for one-line display
}

// softFieldList enumerates the cascade keys reportSoftFields walks
// per file. de_method is intentionally OMITTED here — it's auto-
// filled from de_format and printed by its own check earlier.
// de_cluster / de_cluster_obs_column are separate (printed by the
// cluster-validation block when opted in).
var softFieldList = []softFieldDef{
	{"de_software_version", `scanpy 1.10.4 / DESeq2 1.42.0`},
	{"de_reference_type", `pairwise | vsrest | vsreference | vsgrandmean`},
	{"de_covariates", `sex,age,batch`},
	{"de_filter_criteria", `expressed in ≥3 cells per sample`},
}

// reportSoftFields walks softFieldList and emits one check line per
// field. Set → OK with the value; unset → SKIP with a compact
// "suggest: <values>" note. Returns true when at least one field
// was unset, so the caller can print the once-per-run
// "Set soft parameters via …" hint at the bottom of the output.
//
// Labels render as "<key> (soft)" so the depositor reads the
// status as belonging to the named cascade key directly.
func reportSoftFields(env *config.Env, out *deReporter) (anyUnset bool) {
	for _, f := range softFieldList {
		label := f.Key + " (soft)"
		v := env.String(f.Key)
		if v != "" {
			out.ok(label, v)
		} else {
			out.skip(label, "unset — suggest: "+f.Suggestion)
			anyUnset = true
		}
	}
	return
}

// isGzippedFile reports whether path ends in `.gz` (case-insensitive).
// Distinct from internal/de.isGzipped because that one is unexported;
// duplicating one-line predicate here is cheaper than exporting the
// other.
func isGzippedFile(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".gz")
}

// reportVarkCheck runs the optional vark pre-flight against the
// anthive catalog. Surfaces ONE status line:
//
//   - OK    when de_dataset_id or de_dataset_h5ad resolves to a
//           dataset in the data_project
//   - SKIP  when no match (typo or first-time deposit — anthive
//           is the final authority; mus does not block)
//   - SKIP  when the vark query itself errors out (timeout, auth,
//           network — best-effort, never blocks)
//
// Only invoked when vark.Available() — caller already checked.
func reportVarkCheck(env *config.Env, dataProject, datasetID, datasetH5ad string, out *deReporter) {
	ctx, cancel := context.WithTimeout(context.Background(), vark.DefaultQueryTimeout)
	defer cancel()

	all, err := vark.ListDatasets(ctx)
	if err != nil {
		out.skip("vark-check",
			fmt.Sprintf("vark query failed: %v — anthive will resolve at ingest", err))
		return
	}
	datasets := vark.FilterByDataProject(all, dataProject)

	// Try id first (preferred), then h5ad basename.
	var hit *vark.Dataset
	if datasetID != "" {
		hit = vark.FindByFileID(datasets, datasetID)
	}
	if hit == nil && datasetH5ad != "" {
		hit = vark.FindByH5ad(datasets, datasetH5ad)
	}
	if hit != nil {
		out.ok("vark-check",
			fmt.Sprintf("%s resolves to %s (%q) in catalog",
				or(datasetID, datasetH5ad), hit.ID, hit.Title))
		return
	}

	// No match — almost certainly a typo OR a first-time deposit
	// against a not-yet-cataloged dataset. Make the depositor see
	// it; anthive will reject on the ingest side if it's a typo.
	hint := datasetID
	if hint == "" {
		hint = datasetH5ad
	}
	out.skip("vark-check",
		fmt.Sprintf("%q not in catalog under data_project=%s "+
			"(typo? first-time deposit? — anthive resolves at ingest)",
			hint, dataProject))
}

// stampDESidecar writes / refreshes the per-file sidecar at
// <path>.mus with the DE block populated from the report +
// cascade. `message` is the mandatory -m / --message value;
// it lands as `de_description`, overriding any cascade value.
func stampDESidecar(cache *hashcache.Cache, path string, rep *de.Report,
	env *config.Env, chosen de.Format, method, datasetID, datasetH5ad, message string) error {

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
		DatasetID:              datasetID,
		DatasetH5ad:            datasetH5ad,
		ClusterObsColumn:      env.String("de_cluster_obs_column"),
		SoftwareVersion:       env.String("de_software_version"),
		ReferenceType:         env.String("de_reference_type"),
		Covariates:            env.List("de_covariates"),
		FilterCriteria:        env.String("de_filter_criteria"),
		Description:           message, // mandatory -m flag value; overrides cascade de_description
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

func describeDataset(id, h5ad string) string {
	switch {
	case id != "" && h5ad != "":
		return fmt.Sprintf("de_dataset_id=%s (h5ad=%s)", id, h5ad)
	case id != "":
		return fmt.Sprintf("de_dataset_id=%s", id)
	default:
		return fmt.Sprintf("de_dataset_h5ad=%s", h5ad)
	}
}

func groupPassedByTarget(rows []dePassFile) map[string][]dePassFile {
	out := map[string][]dePassFile{}
	for _, r := range rows {
		var key string
		switch {
		case r.DatasetID != "" && r.DatasetH5ad != "":
			key = fmt.Sprintf("%s (%s)", r.DatasetID, r.DatasetH5ad)
		case r.DatasetID != "":
			key = r.DatasetID
		default:
			key = r.DatasetH5ad
		}
		out[key] = append(out[key], r)
	}
	return out
}

// --- reporter (verbose / silent) -------------------------------------------

// deReporter mirrors irods checkPrinter's role: one per-check
// line, suppressible OK / SKIP via --silent, failures always print
// on stderr.
//
// Compact format: status + check name + parenthesised note. The
// file path is printed ONCE in the per-file header above the
// status lines, never repeated per row.
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

// printLine writes one status row. Width 30 on the check column
// accommodates the longest label we use ("de_software_version (soft)"
// at 26 chars + buffer).
func (r *deReporter) printLine(w io.Writer, status, check, note string) {
	if note == "" {
		fmt.Fprintf(w, "  %-5s %s\n", status, check)
		return
	}
	fmt.Fprintf(w, "  %-5s %-30s (%s)\n", status, check, note)
}

func (r *deReporter) ok(check, note string) {
	if r.silent {
		return
	}
	r.printLine(r.stdout, "OK", check, note)
}
func (r *deReporter) skip(check, note string) {
	if r.silent {
		return
	}
	r.printLine(r.stdout, "SKIP", check, note)
}
func (r *deReporter) errf(check, format string, args ...any) {
	r.printLine(r.stderr, "ERROR", check, fmt.Sprintf(format, args...))
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
       de_dataset_id        3-char anthive id of the dataset h5ad   PREFERRED
       de_dataset_h5ad      basename within data_project           (escape hatch
                                                                   for co-ingest)
   AUTO-FILLED FROM de_format IF UNSET:
       de_method           DESeq2 / edgeR / scanpy.rank_genes_groups / …
   SOFT (passed through to anthive):
       de_term             single value for one-term presets
       de_contrast         single value for one-contrast presets
       de_cluster_obs_column   obs col in dataset h5ad to validate de_cluster against
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
       ├── mus.cfg                 # data_project + h5ad biology + de_dataset_id
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

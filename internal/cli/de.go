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

	"codeberg.org/mfiers/mus/internal/anthivecatalog"
	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/de"
	"codeberg.org/mfiers/mus/internal/h5ad"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/s3client"
	"codeberg.org/mfiers/mus/internal/secret"
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
	var silent, noCatalogCheck bool
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
			return runDECheck(cmd, args, formatOverride, msg, silent, noCatalogCheck)
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
	cmd.Flags().BoolVar(&noCatalogCheck, "no-catalog-check", false,
		"skip the anthive catalog manifest cross-check (use offline, "+
			"or when S3 creds aren't set up yet)")
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

func runDECheck(cmd *cobra.Command, args []string, formatOverride, message string, silent, noCatalogCheck bool) error {
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

	// Load the anthive catalog manifest once for the whole run. nil
	// when --no-catalog-check, when S3 creds aren't configured, or
	// when the fetch fails irrecoverably (no cache + no network);
	// downstream falls back to the vark check in that case.
	manifest := loadCatalogForDECheck(cmd.Context(), noCatalogCheck, stderr)

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
		ok, info, unsetSoft := checkOneDEFile(cache, f, envForFile, override, message, manifest, out)
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
	formatOverride de.Format, message string, manifest *anthivecatalog.Manifest,
	out *deReporter) (bool, dePassFile, bool) {

	out.fileHeader(path)

	// 1. Inspect (format detect + body scan). InspectWithOverride
	//    threads the --format flag into the inspection so the body-
	//    scan can do long-specific work (TermIdx / ContrastIdx /
	//    TermSet / ContrastSet) even when the file's header doesn't
	//    auto-detect as long (renamed columns case).
	rep, err := de.InspectWithOverride(path, formatOverride)
	if err != nil {
		out.errf("format", "%v", err)
		return false, dePassFile{}, false
	}
	chosen := rep.Format
	switch {
	case formatOverride != de.FormatUnknown:
		out.ok("format", fmt.Sprintf("override --format=%s", formatOverride))
	case chosen == de.FormatUnknown:
		// Validate will produce the right error; let it speak.
	default:
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

	// 4c. Co-ingest detection: if de_dataset_h5ad is set AND the
	//     file is present locally (next to the DE file OR one
	//     level up — the two documented co-ingest layouts), this
	//     is a NEW h5ad being uploaded alongside the DE. No catalog
	//     id exists yet — anthive will assign one at ingest by
	//     matching basename. Report it explicitly so the depositor
	//     knows the link is established locally.
	coIngest := ""
	if datasetH5ad != "" {
		coIngest = findCoIngestH5ad(path, datasetH5ad)
		if coIngest != "" {
			out.ok("co-ingest", fmt.Sprintf(
				"local h5ad found at %s — anthive will resolve at ingest", coIngest))
		}
	}

	// 4d. Anthive catalog cross-check. Authoritative — the manifest
	//     is anthive's source of truth, so a HARD-fail here matches
	//     what ant-ingest will do at deposit time.
	//
	//     Skips:
	//       - co-ingest case (anthive resolves at ingest)
	//       - manifest unavailable (--no-catalog-check, no creds,
	//         transient fetch failure with no cache)
	//
	//     Falls back to the vark CLI as an informational check when
	//     the catalog itself is unavailable AND vark is installed —
	//     covers the offline / first-time-setup case.
	switch {
	case coIngest != "":
		out.skip("catalog-check",
			"co-ingest deposit — anthive assigns the dataset id at ingest, no pre-check needed")
	case manifest != nil:
		if !reportCatalogCheck(manifest, dataProject, datasetID, datasetH5ad, out) {
			return false, dePassFile{}, false
		}
	case vark.Available():
		// Manifest unavailable but vark is on PATH — soft fallback.
		out.skip("catalog-check",
			"anthive manifest unavailable — falling back to local `vark` query")
		reportVarkCheck(env, dataProject, datasetID, datasetH5ad, out)
	default:
		out.skip("catalog-check",
			"anthive manifest unavailable AND `vark` not on PATH — "+
				"`mus s3 login` to enable manifest, or `--no-catalog-check` to silence")
	}

	// 5. Method (auto-fill from format for non-long; HARD-required
	//    for long — anthive's ant-de-ingest needs --method on every
	//    invocation and `long` is the one preset with no default).
	method := env.String("de_method")
	if method == "" {
		method = de.MethodFromFormat(chosen)
		if method != "" {
			out.ok("method", fmt.Sprintf("auto-filled %q from de_format=%s", method, chosen))
		} else {
			// long has no default — fail loudly with a fix-it hint.
			out.errf("method",
				"de_method unset and de_format=long has no default — "+
					"`mus config set de_method <NAME>` (e.g. DESeq2, edgeR, custom-pipeline)")
			return false, dePassFile{}, false
		}
	} else {
		out.ok("method", fmt.Sprintf("set in cascade: %q", method))
	}

	// 6. Cluster validation.
	//
	// Only surface cluster-related lines when the depositor has
	// opted into per-cluster DE — either by declaring a cluster
	// value or by naming an obs column.
	//
	// Deep validation (cluster value ∈ obs[col]) runs ONLY when
	// the target h5ad is locally present (co-ingest path). For
	// catalog-resident datasets (only de_dataset_id known) we
	// SKIP with an explicit "anthive validates at ingest" — the
	// catalog query layer doesn't expose per-column unique values
	// so mus can't pre-check without a local file.
	cluster := env.String("de_cluster")
	clusterCol := env.String("de_cluster_obs_column")
	if cluster != "" || clusterCol != "" {
		clusterFail := false
		switch {
		case cluster == "":
			out.skip("cluster-validation",
				`de_cluster_obs_column set but no de_cluster on this file — nothing to validate`)
		case clusterCol == "":
			out.skip("cluster-validation",
				"de_cluster set but de_cluster_obs_column unset — "+
					"OK for multi-factorial designs")
		case coIngest == "":
			out.skip("cluster-validation", fmt.Sprintf(
				"target h5ad not locally present — anthive validates %q ∈ obs[%s] at ingest",
				cluster, clusterCol))
		default:
			clusterFail = !validateClusterAgainstH5ad(coIngest, clusterCol, cluster, out)
		}
		if clusterFail {
			return false, dePassFile{}, false
		}
	}

	// 7. Description (from the mandatory -m flag).
	out.ok("description", fmt.Sprintf("%q", message))

	// 8. Soft-field nudges. Returns true when at least one soft
	//    field was unset — propagates to the caller for the
	//    one-shot bottom hint.
	unsetSoft := reportSoftFields(env, out)

	// 8.5. Per-format required de_* fields. Hard-fail BEFORE the
	//      sidecar is stamped — otherwise the file would land in
	//      __incoming/ only to be refused by ant-de-ingest with
	//      "preset 'deseq2' requires --term, --contrast" or similar.
	if !reportRequiredFormatFields(env, chosen, out) {
		return false, dePassFile{}, unsetSoft
	}

	// 8.6. Post-mapping column reachability. After applying any
	//      de_map_<TARGET>=<source> overrides (or falling back to
	//      the preset's identity defaults), every required canonical
	//      column must resolve to a source column actually present
	//      in the file's header. Catches the remaining edge case
	//      from anthive4 #222: depositor uses --format override
	//      with renamed source columns and either forgets a
	//      de_map_* or supplies a wrong source name. Without this
	//      gate the file would slip through and fail at
	//      ant-de-ingest's DuckDB binder.
	if !reportPostMapColumns(env, chosen, rep.HeaderCols, out) {
		return false, dePassFile{}, unsetSoft
	}

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

// requiredCanonicalColumns maps each format to the anthive __de
// CANONICAL column names that MUST be reachable (= present in the
// file post-de_map_* application) for ant-de-ingest's binder to
// succeed. Source of truth: anthive4 lib/de_presets.py preset
// definitions (the `required_in_file` lists, mirrored here).
var requiredCanonicalColumns = map[de.Format][]string{
	de.FormatDESeq2:  {"gene", "log2fc", "pvalue", "padj"},
	de.FormatEdgeR:   {"gene", "log2fc", "pvalue", "padj"},
	de.FormatScanpy:  {"gene", "contrast", "log2fc", "pvalue", "padj"},
	de.FormatSeurat:  {"gene", "log2fc", "pvalue", "padj"},
	de.FormatGLMMseq: {"gene", "log2fc", "pvalue", "padj"},
	de.FormatGLMMTMB: {"gene", "estimate", "pvalue"},
	de.FormatLong:    {"gene", "term", "contrast"},
}

// presetSourceDefaults maps a (format, canonical-column) pair to
// the SOURCE column name the preset expects when no de_map_* is
// supplied. Derived from inverting anthive's preset column_map
// (which goes source → anthive); we pick the canonical source for
// each anthive column.
//
// When a preset's source format admits multiple variants for the
// same canonical (e.g. glmmseq accepts both `P_FixedEffects` and
// `pvalue` for canonical `pvalue`), we list the most common name.
// Depositors using a non-default name set de_map_<canonical> to
// their actual source column.
var presetSourceDefaults = map[de.Format]map[string]string{
	de.FormatDESeq2: {
		"gene":   "gene",
		"log2fc": "log2FoldChange",
		"pvalue": "pvalue",
		"padj":   "padj",
	},
	de.FormatEdgeR: {
		"gene":   "gene",
		"log2fc": "logFC",
		"pvalue": "PValue",
		"padj":   "FDR",
	},
	de.FormatScanpy: {
		"gene":     "names",
		"contrast": "group",
		"log2fc":   "logfoldchanges",
		"pvalue":   "pvals",
		"padj":     "pvals_adj",
	},
	de.FormatSeurat: {
		"gene":   "gene",
		"log2fc": "avg_log2FC",
		"pvalue": "p_val",
		"padj":   "p_val_adj",
	},
	de.FormatGLMMseq: {
		"gene":   "gene",
		"log2fc": "log2fc",
		"pvalue": "P_FixedEffects",
		"padj":   "q.FixedEffects",
	},
	de.FormatGLMMTMB: {
		"gene":     "gene",
		"estimate": "Estimate",
		"pvalue":   "Pr(>|z|)",
	},
	de.FormatLong: {
		// long has identity defaults for every canonical; depositors
		// must set de_map_gene at minimum (enforced by the per-format
		// required-fields gate) AND de_map_term / de_map_contrast if
		// their source columns aren't already named term / contrast.
		"gene":     "gene",
		"term":     "term",
		"contrast": "contrast",
	},
}

// reportPostMapColumns verifies that every required canonical
// column maps to a source column actually present in the data
// file. Source column resolves to:
//
//	source = env.String("de_map_" + canonical)  // explicit override
//	if source == "" {
//	    source = presetSourceDefaults[format][canonical]  // identity-ish default
//	}
//
// Then check `de.HasColumn(headerCols, source)` (normalised compare).
//
// Returns true when every required canonical is reachable; false
// (with one ERROR line per missing target) otherwise.
//
// Catches the gap that motivated this check: depositor uses
// `--format` override AND/OR renamed source columns AND fails to
// supply (or supplies a wrong) de_map_*. Without this gate the
// file would slip through `mus de check`, land in __incoming/,
// and break at ant-de-ingest's DuckDB binder.
func reportPostMapColumns(env *config.Env, format de.Format,
	headerCols []string, out *deReporter) bool {

	required := requiredCanonicalColumns[format]
	defaults := presetSourceDefaults[format]
	if len(required) == 0 {
		return true // no canonical requirements for this format (defensive)
	}
	type miss struct {
		canonical string
		source    string
		viaMap    bool
	}
	var missing []miss
	for _, canonical := range required {
		var source string
		var viaMap bool
		if v := env.String("de_map_" + canonical); v != "" {
			source = v
			viaMap = true
		} else {
			source = defaults[canonical]
		}
		if source == "" || !de.HasColumn(headerCols, source) {
			missing = append(missing, miss{canonical, source, viaMap})
		}
	}
	if len(missing) == 0 {
		out.ok("columns (post-map)",
			fmt.Sprintf("all required canonicals reachable for %s preset", format))
		return true
	}
	for _, m := range missing {
		var hint string
		if m.viaMap {
			hint = fmt.Sprintf(
				"canonical %q is mapped to source column %q (via de_map_%s) but that column is not in the file. "+
					"Actual columns: %s. Update `mus config set de_map_%s <correct-source>`.",
				m.canonical, m.source, m.canonical,
				strings.Join(headerCols, ", "), m.canonical)
		} else {
			// Always tell depositor the escape hatch (de_map_<canonical>);
			// add a specific source-column suggestion when one is close
			// enough by Levenshtein.
			suggestion := nearestColumnByDistance(m.source, headerCols)
			fix := fmt.Sprintf("`mus config set de_map_%s <source-column>`", m.canonical)
			if suggestion != "" {
				fix = fmt.Sprintf("`mus config set de_map_%s %s` (best guess)",
					m.canonical, suggestion)
			}
			hint = fmt.Sprintf(
				"%s preset expects source column %q (canonical %q) but it's not in the file. "+
					"Actual columns: %s. Fix with %s.",
				format, m.source, m.canonical,
				strings.Join(headerCols, ", "), fix)
		}
		out.errf("columns (required for "+string(format)+")", "%s", hint)
	}
	return false
}

// nearestColumnByDistance returns the closest column from `actual`
// to `query` by Levenshtein distance, or "" when none is meaningfully
// close. Uses the same normalisation mus's column detection uses,
// so case + separator differences don't dominate the score.
func nearestColumnByDistance(query string, actual []string) string {
	if len(actual) == 0 {
		return ""
	}
	q := normalize(query)
	bestScore := -1
	best := ""
	for _, col := range actual {
		d := levenshteinDist(q, normalize(col))
		if bestScore == -1 || d < bestScore {
			bestScore = d
			best = col
		}
	}
	// Only suggest if the match is meaningfully close — within
	// ceil(len(query) / 2) edits. Otherwise it's noise.
	if bestScore > (len(q)+1)/2 {
		return ""
	}
	return best
}

// normalize collapses ASCII case + strips {`_`, `-`, `.`, ` `} —
// mirrors de.normalizeColName (kept package-local here to avoid
// adding another exported symbol for the rare suggestion path).
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '.', '_', '-', ' ':
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// levenshteinDist is the standard 2-row DP edit distance. Used
// once per missing column, on small strings — speed isn't critical.
func levenshteinDist(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			ins := curr[j-1] + 1
			del := prev[j] + 1
			sub := prev[j-1] + cost
			m := ins
			if del < m {
				m = del
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// requiredFormatFields returns the de_* cascade keys that MUST be
// set for the given DE format. Mirrors ant-de-ingest's per-preset
// CLI-flag requirements so a deposit refused at ingest is caught
// upstream at `mus de check` time.
//
// One-file-one-contrast presets (deseq2 / edger / seurat / glmmseq
// / glmmtmb) need BOTH de_term + de_contrast. scanpy needs only
// de_term — its de_contrast comes from the data file's group column
// at ingest. long needs neither (term + contrast are computed from
// the body by `mus de check` itself).
func requiredFormatFields(f de.Format) []string {
	switch f {
	case de.FormatDESeq2, de.FormatEdgeR, de.FormatSeurat,
		de.FormatGLMMseq, de.FormatGLMMTMB:
		return []string{"de_term", "de_contrast"}
	case de.FormatScanpy:
		return []string{"de_term"}
	case de.FormatLong:
		// term + contrast are body-derived (set de_term=auto /
		// de_contrast=auto). But the long preset needs a column
		// MAP to translate the depositor's TSV columns into
		// anthive's canonical schema. Only de_map_gene is hard-
		// required — the others (de_map_log2fc / de_map_padj /
		// de_map_pvalue / de_map_zeff / de_map_estimate / de_map_SE
		// / de_map_ci_low / de_map_ci_high / de_map_term /
		// de_map_contrast) fall through to identity if the source
		// TSV column names already match.
		return []string{"de_map_gene"}
	}
	return nil
}

// reportRequiredFormatFields emits OK / ERROR lines for each
// per-format required field and returns true when every required
// field is set. Missing fields ERROR with a precise fix-it command.
func reportRequiredFormatFields(env *config.Env, f de.Format, out *deReporter) bool {
	missing := []string{}
	for _, k := range requiredFormatFields(f) {
		if v := env.String(k); v == "" {
			missing = append(missing, k)
		} else {
			out.ok(k+" (required for "+string(f)+")", v)
		}
	}
	if len(missing) == 0 {
		return true
	}
	for _, k := range missing {
		fix := fmt.Sprintf("`mus config set %s <VALUE>`", k)
		out.errf(k+" (required for "+string(f)+")",
			"unset — %s preset requires it (ant-de-ingest would refuse). %s", f, fix)
	}
	return false
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

// validateClusterAgainstH5ad runs the deep cluster-validation
// check: read the unique values of target_h5ad.obs[col] via the
// embedded Python helper, confirm the depositor's `cluster` value
// is one of them. Returns true on PASS.
//
// HARD failure on mismatch (reports a stable subset of obs values
// for context). Helper failures (uv missing, file unreadable, obs
// column absent) are SOFT — surfaced as SKIP so the depositor sees
// the reason but the upload isn't blocked. Rationale: cluster-
// value typos are caught at anthive ingest anyway; mus's role is
// to catch them earlier when easy, not to block the deposit when
// the inspection layer has trouble.
func validateClusterAgainstH5ad(h5adPath, col, cluster string, out *deReporter) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	values, err := h5ad.ObsValues(ctx, h5adPath, col, nil)
	if err != nil {
		// Surface as SKIP — we don't block on inspection failure.
		// anthive ingest is the authoritative resolver if mus
		// couldn't pre-check.
		out.skip("cluster-validation",
			fmt.Sprintf("could not read obs[%s] from %s: %v — anthive validates at ingest",
				col, filepath.Base(h5adPath), err))
		return true
	}
	if containsString(values, cluster) {
		out.ok("cluster-validation",
			fmt.Sprintf("%q ∈ obs[%s] of %s (%d distinct values)",
				cluster, col, filepath.Base(h5adPath), len(values)))
		return true
	}
	// Mismatch: HARD fail with a helpful sample of what IS in obs[col].
	preview := previewStrings(values, 8)
	out.errf("cluster-validation",
		"de_cluster=%q is NOT in obs[%s] of %s (have %d values; e.g. %s)",
		cluster, col, filepath.Base(h5adPath), len(values), preview)
	return false
}

// containsString — case-sensitive set-membership for the cluster
// value. anthive treats obs values as opaque strings, so we do too.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// previewStrings returns up to n items, comma-joined, with an
// elided "...(N more)" suffix when there are more.
func previewStrings(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(", ...(%d more)", len(items)-n)
}

// findCoIngestH5ad looks for a co-ingested h5ad sibling. Returns
// the absolute path of the matching file or "" when not found.
// Walks the two documented layouts:
//
//   - flat:       upload/<basename>.h5ad           +  upload/<de-file>
//                 → check same dir as `dePath`
//
//   - nested:     upload/<basename>.h5ad           +  upload/de/.../<de-file>
//                 → walk up from dePath's dir, max 4 levels (covers
//                   upload/de/<analysis>/<de-file>)
//
// Match is by exact basename comparison; we accept the basename
// as the depositor wrote it in de_dataset_h5ad (with or without
// the .h5ad suffix). Sibling sidecar (*.mus) presence is NOT
// required — `mus de check` may run before `mus h5ad check`.
func findCoIngestH5ad(dePath, basename string) string {
	// Normalise: depositor might have written it with or without
	// the .h5ad suffix. Try both.
	candidates := []string{basename}
	if !strings.HasSuffix(basename, ".h5ad") {
		candidates = append(candidates, basename+".h5ad")
	}
	dir := filepath.Dir(dePath)
	for level := 0; level < 4; level++ {
		for _, c := range candidates {
			candidate := filepath.Join(dir, c)
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // hit filesystem root
		}
		dir = parent
	}
	return ""
}

// isGzippedFile reports whether path ends in `.gz` (case-insensitive).
// Distinct from internal/de.isGzipped because that one is unexported;
// duplicating one-line predicate here is cheaper than exporting the
// other.
func isGzippedFile(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".gz")
}

// loadCatalogForDECheck loads the anthive catalog manifest for the
// run, or returns nil when:
//   - --no-catalog-check was passed (explicit opt-out)
//   - S3 creds aren't configured (`mus s3 login` not run)
//   - manifest fetch + cache both fail
//
// All non-fatal — the caller decides what to do without a manifest
// (typically: fall back to vark, or skip the check entirely).
// Always prints an informational line to stderr so the depositor
// knows what happened.
func loadCatalogForDECheck(ctx context.Context, noCatalogCheck bool,
	stderr io.Writer) *anthivecatalog.Manifest {
	if noCatalogCheck {
		fmt.Fprintln(stderr, "note: --no-catalog-check — anthive catalog cross-check skipped")
		return nil
	}
	store, err := secret.Open()
	if err != nil {
		fmt.Fprintf(stderr, "note: secret store unavailable (%v) — catalog cross-check skipped\n", err)
		return nil
	}
	cfg, err := loadS3Config(store)
	if err != nil {
		fmt.Fprintln(stderr,
			"note: S3 credentials not set up — catalog cross-check skipped. "+
				"Run `mus s3 login` to enable, or pass --no-catalog-check to silence.")
		return nil
	}
	client, err := s3client.New(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "note: S3 client init failed (%v) — catalog cross-check skipped\n", err)
		return nil
	}
	mf, err := anthivecatalog.Load(ctx, anthivecatalog.LoadOptions{
		Fetcher: client, Bucket: cfg.Bucket,
	}, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "note: anthive catalog manifest unavailable (%v) — cross-check skipped\n", err)
		return nil
	}
	return mf
}

// reportCatalogCheck runs the manifest-based validation. Returns
// true when the file should be allowed through, false when it must
// HARD-fail. The classification covers:
//
//   - ResolveOK              → OK line, return true
//   - ResolveInactive        → warn line, return true (depositor decides)
//   - ResolveDataProjectNew  → soft warn, return true (first deposit case)
//   - ResolveIsGroupID       → HARD-fail with the S26/file_id hint
//   - ResolveCrossProject    → HARD-fail (id in other data_project)
//   - ResolveUnknown         → HARD-fail with Levenshtein suggestions
//
// When de_dataset_id is set, that's the primary key. When only
// de_dataset_h5ad is set, falls back to basename lookup.
func reportCatalogCheck(manifest *anthivecatalog.Manifest,
	dataProject, datasetID, datasetH5ad string, out *deReporter) bool {

	if datasetID != "" {
		return reportCatalogByID(manifest, dataProject, datasetID, out)
	}
	if datasetH5ad != "" {
		return reportCatalogByBasename(manifest, dataProject, datasetH5ad, out)
	}
	// Should be unreachable — the earlier "either id OR h5ad must
	// be set" gate (4b) refuses files where both are empty.
	out.skip("catalog-check", "neither de_dataset_id nor de_dataset_h5ad set")
	return true
}

func reportCatalogByID(manifest *anthivecatalog.Manifest,
	dataProject, datasetID string, out *deReporter) bool {

	r := manifest.ResolveDatasetID(dataProject, datasetID)
	switch r.Status {
	case anthivecatalog.ResolveOK:
		out.ok("catalog-check", fmt.Sprintf(
			"de_dataset_id=%s resolves to %q in data_project=%s",
			datasetID, r.Match.Title, dataProject))
		return true

	case anthivecatalog.ResolveInactive:
		out.skip("catalog-check", fmt.Sprintf(
			"de_dataset_id=%s exists in %s but is marked inactive (%q) — confirm intended",
			datasetID, dataProject, r.Match.Title))
		return true

	case anthivecatalog.ResolveDataProjectNew:
		out.skip("catalog-check", fmt.Sprintf(
			"data_project=%s not yet in anthive catalog — first deposit? (anthive resolves at ingest)",
			dataProject))
		return true

	case anthivecatalog.ResolveIsGroupID:
		out.errf("catalog-check",
			"de_dataset_id=%s is the GROUP_ID of data_project=%s, not a file_id. "+
				"Did you mean one of: %s? "+
				"(`mus config set de_dataset_id <FILE_ID>` to fix)",
			datasetID, dataProject,
			strings.Join(r.Hint.AvailableInProj, ", "))
		return false

	case anthivecatalog.ResolveCrossProject:
		out.errf("catalog-check",
			"de_dataset_id=%s exists in data_project=%s, not %s. "+
				"The DE file's upload location determines data_project — "+
				"move the file or pick a different file_id.",
			datasetID, r.Hint.ForeignProject, dataProject)
		return false

	case anthivecatalog.ResolveUnknown:
		hint := ""
		if len(r.Hint.Suggestions) > 0 {
			hint = fmt.Sprintf(" Did you mean: %s?", strings.Join(r.Hint.Suggestions, ", "))
		}
		out.errf("catalog-check",
			"de_dataset_id=%s not found in data_project=%s.%s "+
				"All file_ids in %s: %s",
			datasetID, dataProject, hint, dataProject,
			strings.Join(r.Hint.AvailableInProj, ", "))
		return false
	}
	// Unreachable — every ResolveStatus is covered above. Fail
	// closed if a new status is added without updating this switch.
	out.errf("catalog-check",
		"unexpected resolver status %d for de_dataset_id=%s — please file an issue",
		r.Status, datasetID)
	return false
}

func reportCatalogByBasename(manifest *anthivecatalog.Manifest,
	dataProject, basename string, out *deReporter) bool {

	r := manifest.ResolveBasename(dataProject, basename)
	switch r.Status {
	case anthivecatalog.ResolveOK:
		out.ok("catalog-check", fmt.Sprintf(
			"de_dataset_h5ad=%s resolves to %q in data_project=%s",
			basename, r.Match.Title, dataProject))
		return true
	case anthivecatalog.ResolveInactive:
		out.skip("catalog-check", fmt.Sprintf(
			"de_dataset_h5ad=%s exists in %s but is marked inactive — confirm intended",
			basename, dataProject))
		return true
	case anthivecatalog.ResolveDataProjectNew:
		out.skip("catalog-check", fmt.Sprintf(
			"data_project=%s not yet in anthive catalog — first deposit? (anthive resolves at ingest)",
			dataProject))
		return true
	case anthivecatalog.ResolveUnknown:
		hint := ""
		if len(r.Hint.Suggestions) > 0 {
			hint = fmt.Sprintf(" Did you mean: %s?", strings.Join(r.Hint.Suggestions, ", "))
		}
		out.errf("catalog-check",
			"de_dataset_h5ad=%s not in data_project=%s.%s "+
				"(no local sibling .h5ad found either — set `de_dataset_id` to an "+
				"existing file_id, or co-ingest the h5ad alongside this DE)",
			basename, dataProject, hint)
		return false
	}
	out.errf("catalog-check",
		"unexpected resolver status %d for de_dataset_h5ad=%s — please file an issue",
		r.Status, basename)
	return false
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
   HARD-required at the deposit gate (always):
       data_project        e.g. Fiers2025
       de_format           (or inferable from the columns)
       de_dataset_id        3-char anthive id of the dataset h5ad   PREFERRED
       de_dataset_h5ad      basename within data_project           (escape hatch
                                                                   for co-ingest)
   HARD-required PER FORMAT (mirrors ant-de-ingest preset flags):
       deseq2 / edger / seurat / glmmseq / glmmtmb
           → BOTH de_term + de_contrast required
       scanpy
           → de_term required; de_contrast is inferred from the
             data file's group column at ingest
       long
           → de_term + de_contrast are body-derived (set
             de_term=auto / de_contrast=auto)
           → de_map_gene REQUIRED (TSV-column → 'gene' rename);
             other de_map_<TARGET> keys optional, fall through to
             identity. Acceptable TARGETs: gene, term, contrast,
             log2fc, zeff, pvalue, padj, estimate, SE, ci_low,
             ci_high.
   HARD-required (always; auto-filled from format for most presets):
       de_method           DESeq2 / edgeR / scanpy.rank_genes_groups / …
                           Auto-fills for all presets EXCEPT long.
                           For long → no default, MUST be set explicitly.
   SOFT (passed through to anthive):
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

5. ANTHIVE CATALOG CROSS-CHECK (mus 0.2.28+)
   'mus de check' fetches anthive's catalog manifest from S3
   (s3://<bucket>/_catalog/manifest.json) and cross-checks every
   deposit's de_dataset_id / de_dataset_h5ad against it. The
   manifest is cached at $XDG_CACHE_HOME/mus/anthive_catalog.json
   for 5 minutes; stale-fallback-with-warn on transient fetch
   failures. Outcomes:
       OK     - exact id resolves to a real dataset in this data_project
       ERROR  - id is the GROUP_ID of this data_project, not a file_id
                ('S26 is the group_id of Sun2026... did you mean AQR/AQB/...?')
       ERROR  - id not found; surfaces top-3 Levenshtein suggestions
                ('AQX not found... did you mean: AQB, AQN, AQP?')
       ERROR  - id exists, but in a DIFFERENT data_project
       SKIP   - id resolves but flagged inactive (warn, not block)
       SKIP   - data_project not yet in catalog (first deposit?)
       SKIP   - co-ingest case (local sibling .h5ad found)
   Pass '--no-catalog-check' to skip the cross-check (offline mode
   or pre-S3-setup); mus falls back to a soft 'vark' query if that
   CLI is on PATH.

5b. POST-MAPPING COLUMN GATE (mus 0.2.30+)
    'mus de check' verifies every preset's required CANONICAL columns
    actually resolve to a source column present in the file. The
    resolution is:
        source-col = env.de_map_<canonical>   (explicit override), OR
                     preset's default for that canonical (e.g. deseq2:
                     log2fc → log2FoldChange; scanpy: contrast → group).
    Catches the case where you pass --format override on a file with
    renamed columns and forgot the de_map_* — the file would slip mus
    and break at ant-de-ingest's DuckDB binder.

    Required canonicals per preset:
        deseq2 / edger / seurat / glmmseq → gene, log2fc, pvalue, padj
        scanpy                            → gene, contrast, log2fc, pvalue, padj
        glmmtmb                           → gene, estimate, pvalue
        long                              → gene, term, contrast

    Missing column → ERROR with the depositor's actual columns + the
    exact de_map_<canonical> fix (with a 'best guess' suggestion when
    edit-distance is close enough).

6. CO-INGEST (h5ad + DE in one upload)
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

7. SHORTCUTS
       mus de check FOLDER         # validate everything in the folder
       mus de check FOLDER --silent   # CI mode: only failures + summary
       mus de check FILE --format long  # force a specific format
       mus de check FOLDER --no-catalog-check  # skip the anthive manifest cross-check
       mus s3 upload FOLDER        # gate runs again at upload time

Anthive ingests from __incoming/<data_project>/de/<sub>/. Files
without a passing .mus sidecar are refused at upload.
`

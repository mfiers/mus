package cli

// `mus irods find` — discover iRODS objects by mus AVU.
//
// Wraps `iron query` over genquery to surface every object that
// matches a given combination of mus_* AVU triplets. Each --FIELD
// VALUE flag adds an AND clause; results are intersected on the
// client because genquery can't express "object has attr A=X AND
// attr B=Y" in one statement.
//
// Output: one iRODS path per line, suitable for `xargs mus irods get`.
// JSON output via --json for programmatic callers (e.g. anthive's
// ingestor walking the catalog).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/iron"
)

// findFilters bundles the per-flag values for the find command.
// Keep one Go field per AVU key the depositor would want to filter
// on — the most common ingestion-time predicates from the h5ad
// ingestion spec (organism, tissue, cell_type, technology,
// suspension_type, access, source), plus data_project /
// eln_experiment_id for project-scoped lookups.
type findFilters struct {
	organism        string
	tissue          string
	cellType        string
	technology      string
	suspensionType  string
	access          string
	source          string
	dataProject     string
	elnExperimentID string
	year            string
	extra           []string // --avu KEY=VALUE arbitrary catch-all
}

// filterPairs returns the (AVU-attr-name, value) pairs implied by
// the parsed flags. Empty-value flags are skipped — they don't
// constrain the query.
func (f findFilters) filterPairs() ([][2]string, error) {
	var out [][2]string
	add := func(attr, val string) {
		if val != "" {
			out = append(out, [2]string{attr, val})
		}
	}
	add("mus_organism", f.organism)
	add("mus_tissue", f.tissue)
	add("mus_cell_type", f.cellType)
	add("mus_technology", f.technology)
	add("mus_suspension_type", f.suspensionType)
	add("mus_access", f.access)
	add("mus_source", f.source)
	add("mus_data_project", f.dataProject)
	add("mus_eln_experiment_id", f.elnExperimentID)
	add("mus_year", f.year)
	for _, kv := range f.extra {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			return nil, fmt.Errorf("--avu %q: expected KEY=VALUE", kv)
		}
		key := strings.TrimSpace(kv[:idx])
		val := strings.TrimSpace(kv[idx+1:])
		if key == "" || val == "" {
			return nil, fmt.Errorf("--avu %q: empty key or value", kv)
		}
		// Allow the user to omit the mus_ prefix; add it if absent
		// so the common case stays terse.
		if !strings.HasPrefix(key, "mus_") {
			key = "mus_" + key
		}
		add(key, val)
	}
	return out, nil
}

func newIRODSFindCmd() *cobra.Command {
	var f findFilters
	var asJSON, h5adOnly bool
	cmd := &cobra.Command{
		Use:   "find [FLAGS]",
		Short: "find iRODS objects by mus_* AVU metadata",
		Long: "Query the iRODS catalog for objects whose AVU matches the given\n" +
			"filters. Each --FIELD VALUE flag adds an AND constraint; an object\n" +
			"appears in the output only when ALL filters match.\n\n" +
			"At least one filter is required. Use --avu KEY=VALUE for AVU keys\n" +
			"not covered by the dedicated flags (the `mus_` prefix is added\n" +
			"automatically if you omit it).\n\n" +
			"Output: one iRODS path per line, sorted. Pipe to `xargs mus irods\n" +
			"get` to fetch every match.\n\n" +
			"Examples:\n" +
			"  mus irods find --organism human --cell_type 'microglial cell'\n" +
			"  mus irods find --data-project Fiers2025 --json | jq -r '.[].path'\n" +
			"  mus irods find --organism human --h5ad-only | xargs -I{} mus irods get {}",
		RunE: func(cmd *cobra.Command, args []string) error {
			pairs, err := f.filterPairs()
			if err != nil {
				return err
			}
			if len(pairs) == 0 {
				return fmt.Errorf("at least one filter required (--organism, --tissue, etc.)")
			}
			client, err := iron.New()
			if err != nil {
				return err
			}
			paths, err := findByAVU(cmd.Context(), client, pairs, h5adOnly)
			if err != nil {
				return err
			}
			return emitFindResults(cmd.OutOrStdout(), paths, asJSON)
		},
	}
	cmd.Flags().StringVar(&f.organism, "organism", "", "filter by mus_organism")
	cmd.Flags().StringVar(&f.tissue, "tissue", "", "filter by mus_tissue (matches any single value in a multi-tissue object)")
	cmd.Flags().StringVar(&f.cellType, "cell-type", "", "filter by mus_cell_type")
	cmd.Flags().StringVar(&f.technology, "technology", "", "filter by mus_technology")
	cmd.Flags().StringVar(&f.suspensionType, "suspension-type", "", "filter by mus_suspension_type (cell / nucleus / na)")
	cmd.Flags().StringVar(&f.access, "access", "", "filter by mus_access (internal / public)")
	cmd.Flags().StringVar(&f.source, "source", "", "filter by mus_source (internal / external)")
	cmd.Flags().StringVar(&f.dataProject, "data-project", "", "filter by mus_data_project (NameYear)")
	cmd.Flags().StringVar(&f.elnExperimentID, "eln-experiment-id", "", "filter by mus_eln_experiment_id")
	cmd.Flags().StringVar(&f.year, "year", "", "filter by mus_year")
	cmd.Flags().StringArrayVar(&f.extra, "avu", nil, "extra AVU constraint, repeatable (KEY=VALUE; `mus_` prefix auto-added)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON [{\"path\":...}] instead of a path-per-line list")
	cmd.Flags().BoolVar(&h5adOnly, "h5ad-only", false, "restrict results to objects ending in .h5ad")
	return cmd
}

// findByAVU intersects per-filter `iron query` results. Each filter
// becomes one genquery call; the path sets are intersected on the
// client because genquery doesn't support multi-attribute AND in a
// single statement. Order of filters is irrelevant — we sort by
// estimated selectivity (rarest values queried first would be a
// nice optimisation later; for now we just hit them all and let the
// intersection shrink the working set).
func findByAVU(ctx context.Context, client *iron.Client,
	pairs [][2]string, h5adOnly bool) ([]string, error) {

	if len(pairs) == 0 {
		return nil, fmt.Errorf("findByAVU: no filters provided")
	}

	var working map[string]bool
	for i, p := range pairs {
		got, err := queryPathsByAVU(ctx, client, p[0], p[1])
		if err != nil {
			return nil, fmt.Errorf("query %s=%s: %w", p[0], p[1], err)
		}
		if i == 0 {
			working = sliceToSet(got)
			continue
		}
		// Intersect.
		next := map[string]bool{}
		for _, p := range got {
			if working[p] {
				next[p] = true
			}
		}
		working = next
		if len(working) == 0 {
			return nil, nil // early-out, no possible matches
		}
	}

	paths := make([]string, 0, len(working))
	for p := range working {
		if h5adOnly && !strings.HasSuffix(p, ".h5ad") {
			continue
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

// queryPathsByAVU returns iRODS paths whose AVU contains
// (attr, value). Returns absolute paths joining COLL_NAME and
// DATA_NAME from iron's genquery output.
func queryPathsByAVU(ctx context.Context, client *iron.Client,
	attr, value string) ([]string, error) {

	sql := fmt.Sprintf(
		"select COLL_NAME, DATA_NAME where META_DATA_ATTR_NAME = '%s' and META_DATA_ATTR_VALUE = '%s'",
		escapeGenquery(attr), escapeGenquery(value))
	raw, err := client.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	// iron query --json emits [][]string — one row per match, two
	// cells per row (COLL_NAME, DATA_NAME). Decode strict-ly so a
	// future schema change doesn't silently corrupt results.
	var rows [][]string
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		// On no-matches, some iron versions return empty body; treat
		// that as zero rows rather than an error.
		if strings.TrimSpace(raw) == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("decode iron query result: %w", err)
	}
	paths := make([]string, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		paths = append(paths, strings.TrimRight(row[0], "/")+"/"+row[1])
	}
	return paths, nil
}

// escapeGenquery doubles single quotes in genquery values, which is
// how iRODS expects strings inside `'...'` literals. mus AVU values
// don't generally contain quotes, but tissue / disease labels
// sometimes do (e.g. CellxGene's "'fibroblast of breast'").
func escapeGenquery(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func sliceToSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}

// emitFindResults writes the result list either as a path-per-line
// stream (default — pipeable) or as a JSON array of objects
// (--json — easier to parse from another tool).
func emitFindResults(w io.Writer, paths []string, asJSON bool) error {
	if asJSON {
		type entry struct {
			Path string `json:"path"`
		}
		out := make([]entry, len(paths))
		for i, p := range paths {
			out[i] = entry{Path: p}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for _, p := range paths {
		fmt.Fprintln(w, p)
	}
	return nil
}

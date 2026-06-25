package cli

// Key-name guardrail for `mus config set` / `mus config unset`.
//
// Without this, typing `mus config set celltype microglial cell`
// silently invents a new `.env` key called `celltype` — which then
// never propagates anywhere because every consumer reads
// `cell_type`. The depositor only notices when the data is gone.
//
// resolveConfigKey() catches three error classes:
//
//   1. case / punctuation drift (Cell_Type, CELL_TYPE, cell-type,
//      celltype, cell type) → silently rewritten to the canonical
//      `cell_type` with a one-line note
//   2. genuinely-typoed keys (organisme, tisue, eln_exp_id) → rejected
//      with a "did you mean X?" suggestion based on edit distance
//   3. unknown but plausible keys (my_lab_thing) → rejected too,
//      but with no suggestion; user can pass --force to write them
//      verbatim if they really mean it

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// knownConfigKeys is the canonical list of `.env` / sidecar keys
// mus recognises. Anything not on this list (or matching after
// normalisation) is a typo until proven otherwise.
//
// Keep in sync with:
//   - internal/sidecar/sidecar.go::sidecarListKeys + scalar fields
//   - internal/config/config.go::listKeys
//   - internal/h5ad/metadata.go::MetadataFromCascade
//   - vocabulary/main.yaml (top-level keys)
var knownConfigKeys = []string{
	// data_project + ELN linkage
	"data_project",
	"eln_experiment_id", "eln_experiment_name",
	"eln_study_id", "eln_study_name",
	"eln_project_id", "eln_project_name",
	"eln_journal_id",
	"eln_disabled",

	// iRODS site config (usually inherited from baked-in defaults).
	"irods_home", "irods_web", "irods_pid_base",
	// iRODS access-control group — applied to every uploaded object.
	// Default lives in defaults.IRODSGroup (baked in as "BADS"); override
	// with `mus config set irods_group <name>` in mus.cfg.
	"irods_group",

	// Cross-experiment tagging.
	"tag", "collaborator", "note",

	// Biology — scalars.
	"organism", "technology", "suspension_type",
	"sex", "age", "strain",
	"title", "contributor", "year",
	"source", "license", "source_description",
	"access", "accession",

	// Biology — lists (vocab-controlled).
	"tissue", "cell_type", "disease", "condition",
	"doi", "pmid", "link",

	// Ontology-ID companions.
	"organism_ontology_term_id", "tissue_ontology_term_id",
	"cell_type_ontology_term_id", "assay_ontology_term_id",
	"sex_ontology_term_id", "disease_ontology_term_id",

	// h5ad-specific cascade hints (override the inspector).
	"h5ad_gene_id_type", "h5ad_n_cells", "h5ad_n_genes",
	"h5ad_sha256",

	// DE-deposit cascade fields (anthive DE-ingest workflow).
	// `mus de check` reads these from the cascading mus.cfg; the
	// per-file <basename>.mus sidecar then carries the same de_*
	// keys after a passing check.
	"de_dataset_id", "de_dataset_h5ad",
	"de_format", "de_method",
	"de_cluster", "de_cluster_obs_column",
	"de_term", "de_contrast",
	"de_software_version", "de_reference_type",
	"de_covariates", "de_filter_criteria", "de_description",
	// long-format column-mapping (mus 0.2.30+). Translates the
	// depositor's TSV columns into anthive's canonical __de schema.
	// Only de_map_gene is hard-required; the rest are optional and
	// fall through to identity if the source column name matches.
	"de_map_gene", "de_map_term", "de_map_contrast",
	"de_map_log2fc", "de_map_zeff", "de_map_pvalue", "de_map_padj",
	"de_map_estimate", "de_map_SE", "de_map_ci_low", "de_map_ci_high",
}

// resolveConfigKey returns the canonical form of `key`. Three paths:
//
//   - exact match against knownConfigKeys → returned verbatim, silent
//   - matches modulo case / `_` / `-` / space (e.g. CellType,
//     cell-type, celltype) → rewritten to the canonical form with
//     one stderr note
//   - no normalised match → REJECTED with a "did you mean X?" hint
//     drawn from edit-distance suggestions, unless `force` is true
//     (in which case the verbatim key is saved with a ⚠ warning)
func resolveConfigKey(key string, force bool, stderr io.Writer) (string, error) {
	// 1. exact match.
	for _, k := range knownConfigKeys {
		if k == key {
			return key, nil
		}
	}
	// 2. normalisation match.
	normalized := normalizeConfigKey(key)
	for _, k := range knownConfigKeys {
		if normalizeConfigKey(k) == normalized {
			fmt.Fprintf(stderr,
				"  note: normalised key %q → %q (canonical form)\n",
				key, k)
			return k, nil
		}
	}
	// 3. unknown key.
	if !force {
		return "", offSchemaKeyError(key)
	}
	fmt.Fprintf(stderr,
		"  ⚠ %q is not a recognised mus key — saving anyway (--force).\n",
		key)
	return key, nil
}

// normalizeConfigKey lower-cases and strips _, -, and whitespace so
// that `celltype`, `cell_type`, `cell-type`, `Cell Type`, and
// `cellType` all collapse to one canonical form for matching.
func normalizeConfigKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '_', '-', ' ', '\t':
			// drop
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// offSchemaKeyError is the rejection error for a key that doesn't
// match anything known. Includes a "did you mean X?" suggestion when
// an edit-distance candidate is close enough.
func offSchemaKeyError(key string) error {
	msg := fmt.Sprintf(
		"%q is not a recognised mus key — refusing to save.\n", key)
	if suggested := nearestKnownKey(key); suggested != "" {
		msg += fmt.Sprintf("  Did you mean %q?\n", suggested)
	}
	msg += "  See `mus config show` for the keys mus knows about, or pass\n"
	msg += "  --force to save anyway (intentional lab-specific extension)."
	return fmt.Errorf("%s", msg)
}

// nearestKnownKey returns the closest knownConfigKeys entry to
// `input` by Levenshtein distance, or "" if no candidate is close
// enough (threshold: distance ≤ 3, normalised on both sides).
// Tie-breaks alphabetically for determinism.
func nearestKnownKey(input string) string {
	normalizedInput := normalizeConfigKey(input)
	if normalizedInput == "" {
		return ""
	}
	// Worst-case threshold: at most 3 edits OR 40 % of the input
	// length, whichever is smaller. So short keys are matched
	// tightly (no false positives) and long keys still tolerate
	// the occasional missed underscore.
	threshold := 3
	if alt := len(normalizedInput) * 2 / 5; alt < threshold {
		threshold = alt
	}
	if threshold < 1 {
		threshold = 1
	}

	type cand struct {
		key  string
		dist int
	}
	var hits []cand
	for _, k := range knownConfigKeys {
		d := levenshtein(normalizedInput, normalizeConfigKey(k))
		if d <= threshold {
			hits = append(hits, cand{k, d})
		}
	}
	if len(hits) == 0 {
		return ""
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].dist != hits[j].dist {
			return hits[i].dist < hits[j].dist
		}
		return hits[i].key < hits[j].key
	})
	return hits[0].key
}

// levenshtein returns the classic edit distance between two strings.
// Iterative O(len(a)·len(b)) time, O(min(len(a),len(b))) space — fine
// for the short identifiers we compare here (≤ 32 chars).
func levenshtein(a, b string) int {
	if len(a) < len(b) {
		a, b = b, a
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
			curr[j] = min3(
				prev[j]+1,      // delete
				curr[j-1]+1,    // insert
				prev[j-1]+cost, // substitute
			)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

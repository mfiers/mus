package h5ad

// Orchestrator: combines the cascade/sidecar-derived Metadata, the
// h5ad structural inspector (inspect.go), and the cross-consistency
// checks into a single tiered Report.
//
// Structural §1.1–§1.4 rules are hard-coded here — they are facts
// about a valid scRNA-seq deposit, not vocabulary.

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"codeberg.org/mfiers/mus/internal/vocab"
)

// CheckVersion is the running version of mus's h5ad-check code path.
//
// Bump this whenever a structural rule, soft-check threshold, or
// derived field changes in a way that could flip a previously-passed
// file. A sidecar with H5ad.InspectedCheckVersion < this value is
// treated as a stale cache → mus re-runs the inspection regardless
// of the file's sha.
//
// Bump-worthy changes (examples):
//   - new HARD rule added or relaxed
//   - soft-warning threshold changed (e.g. Ensembl-id %)
//   - inspection-derived field renamed/added in the report
//   - matching-rule change (case / whitespace normalisation)
//
// Non-bump-worthy changes:
//   - error-message wording, log formatting, comments.
const CheckVersion = 2

// Report is the combined finding list plus the inputs that produced
// it (handy for CLI display and sidecar writes).
type Report struct {
	Issues     []Issue
	H5adPath   string
	Metadata   *Metadata
	Inspection *InspectionReport
}

// Hard returns the HARD findings.
func (r *Report) Hard() []Issue { return r.filter(SeverityHard) }

// Soft returns the SOFT findings.
func (r *Report) Soft() []Issue { return r.filter(SeveritySoft) }

// Info returns the INFO findings.
func (r *Report) Info() []Issue { return r.filter(SeverityInfo) }

// HasHard reports whether the report has at least one HARD finding —
// i.e. whether the deposit is rejected.
func (r *Report) HasHard() bool { return len(r.Hard()) > 0 }

func (r *Report) filter(s Severity) []Issue {
	out := make([]Issue, 0, len(r.Issues))
	for _, i := range r.Issues {
		if i.Severity == s {
			out = append(out, i)
		}
	}
	return out
}

// CheckOptions are the dependencies RunChecks needs. The Metadata is
// built by the caller from the cascade + sidecar (use
// MetadataFromCascade) — keeps RunChecks free of I/O concerns.
type CheckOptions struct {
	Inspector     Inspector // production: PythonInspector{}; tests: StubInspector{}
	Vocabulary    *vocab.Vocabulary
	Metadata      *Metadata
	ComputeSha256 func(path string) (string, error) // optional; nil → skip sha256 cross-check
}

// RunChecks validates the .h5ad at `h5adPath` against the spec.
// Always returns a Report — inspector failures surface as HARD Issues
// rather than Go errors so CLI display is uniform.
func RunChecks(ctx context.Context, h5adPath string, opts CheckOptions) (*Report, error) {
	r := &Report{H5adPath: h5adPath, Metadata: opts.Metadata}

	// 1. Extension check (cheap).
	if !strings.HasSuffix(filepath.Base(h5adPath), ".h5ad") {
		r.Issues = append(r.Issues, Issue{
			Severity: SeverityHard,
			Field:    "file",
			Message:  fmt.Sprintf("path %q must end in lowercase .h5ad (spec §1.4)", h5adPath),
		})
	}

	// 2. Metadata validation against the vocabulary.
	if opts.Metadata != nil && opts.Vocabulary != nil {
		r.Issues = append(r.Issues, ValidateMetadata(opts.Metadata, opts.Vocabulary)...)
	}

	// 3. Inspector — the structural side.
	if opts.Inspector == nil {
		r.Issues = append(r.Issues, Issue{
			Severity: SeverityHard,
			Field:    "inspector",
			Message:  "no h5ad inspector wired (internal error)",
		})
		return r, nil
	}
	report, ierr := opts.Inspector.Inspect(ctx, h5adPath)
	if ierr != nil {
		r.Issues = append(r.Issues, Issue{
			Severity: SeverityHard,
			Field:    "h5ad",
			Message:  fmt.Sprintf("cannot inspect %s: %v", h5adPath, ierr),
		})
		return r, nil
	}
	r.Inspection = report
	r.Issues = append(r.Issues, structuralChecks(report)...)

	// 4. Cross-consistency between user-declared and inspector findings.
	if opts.Metadata != nil {
		r.Issues = append(r.Issues, crossChecks(opts.Metadata, report, h5adPath, opts.ComputeSha256)...)
	}

	// 5. §2.1 gene-symbol heuristic.
	r.Issues = append(r.Issues, geneSymbolHeuristic(report)...)

	// 6. §1.2 — embedding present in the FILE (not just declared).
	if !report.Obsm.HasUMAP && !report.Obsm.HasTSNE {
		// already covered by structuralChecks; this is the
		// authoritative source — user-declared embeddings can't
		// substitute for the actual file contents.
	}

	return r, nil
}

// --- Structural §1.1–§1.4 rules --------------------------------------------

func structuralChecks(rep *InspectionReport) []Issue {
	var issues []Issue

	// §1.1 — adata.X
	if !rep.X.Present {
		issues = append(issues, Issue{
			Severity: SeverityHard, Field: "X",
			Message: "adata.X is None — populate with log-normalized expression before export (spec §1.1)",
		})
	} else {
		if rep.X.Min != nil && *rep.X.Min < 0 {
			issues = append(issues, Issue{
				Severity: SeverityHard, Field: "X",
				Message: fmt.Sprintf("adata.X has negative values (min=%g); not log-normalized expression (spec §1.1, §6 \"scale.data / z-scored / Pearson residuals\")", *rep.X.Min),
			})
		}
		if rep.X.IsInteger != nil && *rep.X.IsInteger {
			issues = append(issues, Issue{
				Severity: SeverityHard, Field: "X",
				Message: "adata.X looks integer-valued — raw counts are in X but expected log-normalized expression (spec §1.1, §6 \"counts in X\")",
			})
		}
		if rep.X.ExitReason == "max_examined" {
			issues = append(issues, Issue{
				Severity: SeverityInfo, Field: "X",
				Message: fmt.Sprintf("X check sampled %d of ~%s values before hitting cap — min/integer-ness reflect that sample",
					rep.X.Examined, totalEstStr(rep.X.TotalEstimate)),
			})
		}
	}

	// §1.1 — layers["counts"]
	if !rep.Counts.Present {
		issues = append(issues, Issue{
			Severity: SeverityHard, Field: "counts",
			Message: "layers[\"counts\"] missing — raw integer counts must be stored there (spec §1.1)",
		})
	} else {
		if rep.Counts.Min != nil && *rep.Counts.Min < 0 {
			issues = append(issues, Issue{
				Severity: SeverityHard, Field: "counts",
				Message: fmt.Sprintf("layers[\"counts\"] has negative values (min=%g); not raw counts (spec §1.1, §6 \"SCT residuals\")", *rep.Counts.Min),
			})
		}
		if rep.Counts.IsInteger != nil && !*rep.Counts.IsInteger {
			issues = append(issues, Issue{
				Severity: SeverityHard, Field: "counts",
				Message: "layers[\"counts\"] is not integer-valued — re-export the actual integer counts (spec §1.1, §6)",
			})
		}
		if rep.Counts.ExitReason == "max_examined" {
			issues = append(issues, Issue{
				Severity: SeverityInfo, Field: "counts",
				Message: fmt.Sprintf("counts check sampled %d of ~%s values before hitting cap",
					rep.Counts.Examined, totalEstStr(rep.Counts.TotalEstimate)),
			})
		}
	}

	// §1.2 — Embeddings
	if !rep.Obsm.HasUMAP && !rep.Obsm.HasTSNE {
		issues = append(issues, Issue{
			Severity: SeverityHard, Field: "obsm",
			Message: "no X_umap or X_tsne in obsm — every viewer assumes one of these (spec §1.2)",
		})
	}

	// §1.3 — Identifiers
	if !rep.ObsNamesUnique {
		issues = append(issues, Issue{
			Severity: SeverityHard, Field: "obs_names",
			Message: "obs_names are not unique — call adata.obs_names_make_unique() before export (spec §1.3)",
		})
	}
	if !rep.VarNamesUnique {
		issues = append(issues, Issue{
			Severity: SeverityHard, Field: "var_names",
			Message: "var_names are not unique — call adata.var_names_make_unique() before export (spec §1.3)",
		})
	}
	if len(rep.ReservedInObs) > 0 {
		issues = append(issues, Issue{
			Severity: SeverityHard, Field: "obs",
			Message: fmt.Sprintf("reserved column names in obs: %s (spec §1.3)",
				strings.Join(rep.ReservedInObs, ", ")),
		})
	}
	if len(rep.ReservedInVar) > 0 {
		issues = append(issues, Issue{
			Severity: SeverityHard, Field: "var",
			Message: fmt.Sprintf("reserved column names in var: %s (spec §1.3)",
				strings.Join(rep.ReservedInVar, ", ")),
		})
	}

	// §6 / §2 — high-cardinality obs columns (cell-id-like leftovers)
	for col, n := range rep.HighCardObs {
		issues = append(issues, Issue{
			Severity: SeveritySoft, Field: "obs[" + col + "]",
			Message: fmt.Sprintf("%d unique values — likely a cell-barcode column left in obs; consider dropping or moving to uns (spec §6)", n),
		})
	}

	return issues
}

// --- Cross-consistency: user-declared ⇄ inspection -------------------------

func crossChecks(meta *Metadata, rep *InspectionReport,
	h5adPath string, computeSha func(string) (string, error)) []Issue {

	var issues []Issue

	if meta.NCells > 0 && int64(meta.NCells) != rep.NObs {
		issues = append(issues, Issue{
			Severity: SeverityHard, Field: "n_cells",
			Message: fmt.Sprintf("declared n_cells=%d but actual h5ad has %d cells",
				meta.NCells, rep.NObs),
		})
	}
	if meta.NGenes > 0 && int64(meta.NGenes) != rep.NVars {
		issues = append(issues, Issue{
			Severity: SeverityHard, Field: "n_genes",
			Message: fmt.Sprintf("declared n_genes=%d but actual h5ad has %d genes",
				meta.NGenes, rep.NVars),
		})
	}
	// sha256 cross-check is SOFT per spec (§4). Only computed when the
	// caller wires a computer.
	if meta.Sha256 != "" && computeSha != nil {
		actual, err := computeSha(h5adPath)
		if err != nil {
			issues = append(issues, Issue{
				Severity: SeveritySoft, Field: "sha256",
				Message: fmt.Sprintf("could not compute sha256 of %s for cross-check: %v",
					h5adPath, err),
			})
		} else if !strings.EqualFold(actual, meta.Sha256) {
			issues = append(issues, Issue{
				Severity: SeveritySoft, Field: "sha256",
				Message: fmt.Sprintf("declared sha256=%s but file sha256=%s",
					meta.Sha256, actual),
			})
		}
	}
	return issues
}

// --- §2.1 gene-symbol heuristic --------------------------------------------

// Ensembl-ID shapes (ENSG, ENSMUSG, ENSRNOG, etc.).
var ensemblPattern = regexp.MustCompile(`^ENS[A-Z]{0,4}G\d{6,}(?:\.\d+)?$`)

func geneSymbolHeuristic(rep *InspectionReport) []Issue {
	if len(rep.VarNamesSample) == 0 {
		return nil
	}
	ensemblCount := 0
	for _, name := range rep.VarNamesSample {
		if ensemblPattern.MatchString(name) {
			ensemblCount++
		}
	}
	if ensemblCount == 0 {
		return nil
	}
	frac := float64(ensemblCount) / float64(len(rep.VarNamesSample))
	if frac >= 0.95 {
		return []Issue{{
			Severity: SeveritySoft, Field: "var_names",
			Message: fmt.Sprintf("%.0f%% of sampled var_names look like Ensembl IDs — spec §2.1 expects canonical gene symbols (HGNC/MGI); remap with mygene before deposit",
				frac*100),
		}}
	}
	if frac >= 0.05 {
		return []Issue{{
			Severity: SeveritySoft, Field: "var_names",
			Message: fmt.Sprintf("%.0f%% of sampled var_names look like Ensembl IDs — mixed symbols/Ensembl tolerated but flagged (spec §2.1)",
				frac*100),
		}}
	}
	return nil
}

// --- small helpers ---------------------------------------------------------

func totalEstStr(p *int64) string {
	if p == nil {
		return "?"
	}
	return fmt.Sprintf("%d", *p)
}

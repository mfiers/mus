package h5ad

// Metadata source: cascading .env + per-file sidecar overrides.
//
// The H5AD ingestion spec (§4) lists the metadata an h5ad deposit
// must carry. mus does NOT use the sibling YAML the spec describes —
// instead it sources the same fields from the existing mus cascade
// (project-wide values in .env, file-specific in <file>.h5ad.mus).
//
// Resolution order (lowest → highest precedence):
//
//   1. .env cascade  (h5ad_organism / h5ad_celltype / …)  → project defaults
//   2. sidecar H5ad{} block                               → per-file override
//
// Inspection-derived fields (n_obs, n_vars, layers, …) come from the
// Python inspector and are layered in by check.go, not here.

import (
	"fmt"
	"regexp"
	"strings"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// Metadata mirrors the §4 template. Everything in the user-supplied
// fields layer is what comes from `.env` + sidecar; check.go layers
// in inspection-derived fields after.
type Metadata struct {
	// Biology
	Organism    string
	Celltype    []string
	Technology  string
	Preparation string
	Tissue      []string
	Region      []string
	Disease     []string
	Sex         string

	// Provenance
	Internal    string // "yes" / "no"
	Accession   string
	DOI         string
	Source      string
	License     string
	Year        int
	Contributor string
	Title       string
	Sha256      string // user-declared sha256 (soft cross-check vs the file)

	// File contents (some user-declared, some inspector-overwritten)
	NCells     int
	NGenes     int
	GeneIDType string
	Embeddings []string

	Notes string
}

// MetadataFromCascade builds a Metadata by reading the h5ad_* keys
// from the .env cascade, then applying any per-file overrides from
// the sidecar's H5ad{} block.
//
// Either argument may be nil — that just contributes nothing.
func MetadataFromCascade(env *config.Env, sc *sidecar.H5ad) *Metadata {
	m := &Metadata{}
	// Layer 1: cascade.
	if env != nil {
		m.Organism = env.String("h5ad_organism")
		m.Celltype = env.List("h5ad_celltype")
		m.Technology = env.String("h5ad_technology")
		m.Preparation = env.String("h5ad_preparation")
		m.Tissue = env.List("h5ad_tissue")
		m.Region = env.List("h5ad_region")
		m.Disease = env.List("h5ad_disease")
		m.Sex = env.String("h5ad_sex")

		m.Internal = env.String("h5ad_internal")
		m.Accession = env.String("h5ad_accession")
		m.DOI = env.String("h5ad_doi")
		m.Source = env.String("h5ad_source")
		m.License = env.String("h5ad_license")
		m.Year = atoiOrZero(env.String("h5ad_year"))
		m.Contributor = env.String("h5ad_contributor")
		m.Title = env.String("h5ad_title")
		m.Sha256 = env.String("h5ad_sha256")

		m.NCells = atoiOrZero(env.String("h5ad_n_cells"))
		m.NGenes = atoiOrZero(env.String("h5ad_n_genes"))
		m.GeneIDType = env.String("h5ad_gene_id_type")
		m.Notes = env.String("h5ad_notes")
	}
	// Layer 2: sidecar overrides — only when set.
	if sc != nil {
		if sc.Organism != "" {
			m.Organism = sc.Organism
		}
		if len(sc.Celltype) > 0 {
			m.Celltype = append([]string(nil), sc.Celltype...)
		}
		if sc.Technology != "" {
			m.Technology = sc.Technology
		}
		if sc.Preparation != "" {
			m.Preparation = sc.Preparation
		}
		if len(sc.Tissue) > 0 {
			m.Tissue = append([]string(nil), sc.Tissue...)
		}
		if len(sc.Region) > 0 {
			m.Region = append([]string(nil), sc.Region...)
		}
		if len(sc.Disease) > 0 {
			m.Disease = append([]string(nil), sc.Disease...)
		}
		if sc.Sex != "" {
			m.Sex = sc.Sex
		}
		if sc.Internal != "" {
			m.Internal = sc.Internal
		}
		if sc.Accession != "" {
			m.Accession = sc.Accession
		}
		if sc.DOI != "" {
			m.DOI = sc.DOI
		}
		if sc.Year != 0 {
			m.Year = sc.Year
		}
		if sc.Contributor != "" {
			m.Contributor = sc.Contributor
		}
		if sc.GeneIDType != "" {
			m.GeneIDType = sc.GeneIDType
		}
		if sc.NObs > 0 {
			m.NCells = int(sc.NObs)
		}
		if sc.NVars > 0 {
			m.NGenes = int(sc.NVars)
		}
	}
	return m
}

func atoiOrZero(s string) int {
	if s == "" {
		return 0
	}
	var n int
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// Severity classifies a validation issue.
type Severity int

const (
	SeverityInfo Severity = iota
	SeveritySoft
	SeverityHard
)

func (s Severity) String() string {
	switch s {
	case SeverityHard:
		return "HARD"
	case SeveritySoft:
		return "SOFT"
	default:
		return "INFO"
	}
}

// Issue is one finding from validation.
type Issue struct {
	Severity Severity
	Field    string // metadata field name, or "" for cross-cutting issues
	Message  string
}

// ValidateMetadata runs the user-side checks: required fields present
// (sourced from .env cascade + sidecar), controlled-vocab values
// match, internal/accession consistent, provenance-soft warnings.
// Cross-consistency with the h5ad inspection report (n_cells /
// n_genes / sha256) is in check.go.
func ValidateMetadata(m *Metadata, v *Vocabulary) []Issue {
	if m == nil {
		return []Issue{{
			Severity: SeverityHard,
			Message:  "metadata is nil — internal error",
		}}
	}
	var issues []Issue

	// Required-field presence.
	for _, f := range v.Required {
		if !fieldPresent(m, f) {
			issues = append(issues, Issue{
				Severity: SeverityHard,
				Field:    f,
				Message: fmt.Sprintf(
					"required field %q is missing — set h5ad_%s=… in .env "+
						"(project-wide) or in <file>.h5ad.mus (file-specific)",
					f, f),
			})
		}
	}

	// Controlled-vocabulary value checks. HARD whether the field is
	// required or not — spec §3: "do not invent".
	for field, values := range fieldValues(m) {
		if v.AllowedValues(field) == nil {
			continue
		}
		for _, val := range values {
			if val == "" {
				continue
			}
			if !v.ValueAllowed(field, val) {
				issues = append(issues, Issue{
					Severity: SeverityHard,
					Field:    field,
					Message: fmt.Sprintf(
						"value %q not in controlled vocabulary for %q (allowed: %s)",
						val, field, strings.Join(v.AllowedValues(field), ", ")),
				})
			}
		}
	}

	// internal / accession consistency.
	if m.Internal != "" && strings.EqualFold(m.Internal, "no") {
		if m.Accession != "" && !looksPublicID(m.Accession) {
			issues = append(issues, Issue{
				Severity: SeveritySoft,
				Field:    "accession",
				Message: fmt.Sprintf(
					"internal=no but accession=%q does not look like a public ID "+
						"(GSE/SRA/PRJ/phs/EGA/Zenodo/DOI)",
					m.Accession),
			})
		}
	}

	// Soft provenance recommendations.
	if m.DOI == "" {
		issues = append(issues, Issue{
			Severity: SeveritySoft,
			Field:    "doi",
			Message:  "no doi — provenance is harder to trace",
		})
	}
	if m.Year == 0 {
		issues = append(issues, Issue{
			Severity: SeveritySoft,
			Field:    "year",
			Message:  "no year — provenance is harder to trace",
		})
	}
	if m.Contributor == "" {
		issues = append(issues, Issue{
			Severity: SeveritySoft,
			Field:    "contributor",
			Message:  "no contributor — depositor unknown",
		})
	}

	return issues
}

// fieldPresent reports whether the metadata field named `name` has a
// non-empty value.
func fieldPresent(m *Metadata, name string) bool {
	switch name {
	case "title":
		return m.Title != ""
	case "contributor":
		return m.Contributor != ""
	case "year":
		return m.Year != 0
	case "organism":
		return m.Organism != ""
	case "celltype":
		return len(m.Celltype) > 0
	case "technology":
		return m.Technology != ""
	case "preparation":
		return m.Preparation != ""
	case "tissue":
		return len(m.Tissue) > 0
	case "region":
		return len(m.Region) > 0
	case "disease":
		return len(m.Disease) > 0
	case "sex":
		return m.Sex != ""
	case "internal":
		return m.Internal != ""
	case "accession":
		return m.Accession != ""
	case "doi":
		return m.DOI != ""
	case "source":
		return m.Source != ""
	case "license":
		return m.License != ""
	}
	return false
}

// fieldValues returns the set of (field → values) pairs that are
// under controlled-vocabulary check.
func fieldValues(m *Metadata) map[string][]string {
	return map[string][]string{
		"organism":    {m.Organism},
		"celltype":    m.Celltype,
		"technology":  {m.Technology},
		"preparation": {m.Preparation},
		"tissue":      m.Tissue,
		"region":      m.Region,
		"disease":     m.Disease,
		"sex":         {m.Sex},
		"internal":    {m.Internal},
	}
}

// publicIDPattern captures the public-repository accession ID shapes
// recognised by ingestion: GEO, SRA, BioProject, dbGaP, EGA, Zenodo
// DOIs, generic DOIs.
var publicIDPattern = regexp.MustCompile(
	`^(?i)(GSE\d+|GSM\d+|SR[APRSXZ]\d+|PRJ[A-Z]+\d+|phs\d+(\.v\d+)?(\.p\d+)?|EGA[SDF]\d+|10\.\d{4,}/[^\s]+|zenodo[._-]?\d+)$`,
)

// looksPublicID reports whether s matches any of the recognised
// public-repo accession ID patterns.
func looksPublicID(s string) bool {
	return publicIDPattern.MatchString(strings.TrimSpace(s))
}

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
	"codeberg.org/mfiers/mus/internal/vocab"
)

// Metadata mirrors the joint-vocabulary proposal §3.1 / §3.2. Bare
// fields come from the .env cascade + sidecar Bio block; check.go
// layers in inspection-derived fields after.
type Metadata struct {
	// Biology (CellxGene-aligned, schema v2).
	Organism       string
	Tissue         []string // UBERON labels
	CellType       []string // CL labels
	Technology     string   // protocol/chemistry
	SuspensionType string   // cell / nucleus / na
	Disease        []string // MONDO labels
	Condition      []string // freeform
	Sex            string
	Age            string // freeform with unit: 87y, 18m, P30
	Strain         string

	// Provenance.
	Title             string
	Contributor       string
	Year              int
	DOI               []string
	PMID              []string
	Link              []string
	Source            string // internal / external
	License           string
	SourceDescription string

	// Deposit status.
	Access    string // internal / public
	Accession string

	// User-declared file shape (cross-checked against inspector).
	NCells     int
	NGenes     int
	GeneIDType string
	Sha256     string

	// Optional ontology IDs (not validated today; recorded).
	OrganismOntologyTermID string
	TissueOntologyTermID   []string
	CellTypeOntologyTermID []string
	AssayOntologyTermID    string
	SexOntologyTermID      string
	DiseaseOntologyTermID  []string

	Notes string
}

// MetadataFromCascade builds a Metadata by reading bare experiment
// keys from the .env cascade (organism, tissue, cell_type, …) plus
// the h5ad-specific keys (h5ad_gene_id_type, h5ad_n_cells, …), then
// applying per-file overrides from the sidecar's Bio + H5ad blocks.
//
// Sidecar lists always REPLACE the cascade list for that key (the
// sidecar is per-file authority). To merge, set both at the right
// cascade depth.
//
// Either argument may be nil — that just contributes nothing.
func MetadataFromCascade(env *config.Env, bio *sidecar.Bio, h5 *sidecar.H5ad) *Metadata {
	m := &Metadata{}
	// Layer 1a: cascade — bare keys (broadly applicable).
	if env != nil {
		m.Organism = env.String("organism")
		m.Tissue = env.List("tissue")
		m.CellType = env.List("cell_type")
		m.Technology = env.String("technology")
		m.SuspensionType = env.String("suspension_type")
		m.Disease = env.List("disease")
		m.Condition = env.List("condition")
		m.Sex = env.String("sex")
		m.Age = env.String("age")
		m.Strain = env.String("strain")

		m.Title = env.String("title")
		m.Contributor = env.String("contributor")
		m.Year = atoiOrZero(env.String("year"))
		m.DOI = env.List("doi")
		m.PMID = env.List("pmid")
		m.Link = env.List("link")
		m.Source = env.String("source")
		m.License = env.String("license")
		m.SourceDescription = env.String("source_description")

		m.Access = env.String("access")
		m.Accession = env.String("accession")

		m.OrganismOntologyTermID = env.String("organism_ontology_term_id")
		m.TissueOntologyTermID = env.List("tissue_ontology_term_id")
		m.CellTypeOntologyTermID = env.List("cell_type_ontology_term_id")
		m.AssayOntologyTermID = env.String("assay_ontology_term_id")
		m.SexOntologyTermID = env.String("sex_ontology_term_id")
		m.DiseaseOntologyTermID = env.List("disease_ontology_term_id")

		// Layer 1b: cascade — h5ad-specific keys.
		m.NCells = atoiOrZero(env.String("h5ad_n_cells"))
		m.NGenes = atoiOrZero(env.String("h5ad_n_genes"))
		m.GeneIDType = env.String("h5ad_gene_id_type")
		m.Sha256 = env.String("h5ad_sha256")
	}
	// Layer 2a: sidecar Bio override — non-empty wins per field.
	if bio != nil {
		if bio.Organism != "" {
			m.Organism = bio.Organism
		}
		if len(bio.Tissue) > 0 {
			m.Tissue = append([]string(nil), bio.Tissue...)
		}
		if len(bio.CellType) > 0 {
			m.CellType = append([]string(nil), bio.CellType...)
		}
		if bio.Technology != "" {
			m.Technology = bio.Technology
		}
		if bio.SuspensionType != "" {
			m.SuspensionType = bio.SuspensionType
		}
		if len(bio.Disease) > 0 {
			m.Disease = append([]string(nil), bio.Disease...)
		}
		if len(bio.Condition) > 0 {
			m.Condition = append([]string(nil), bio.Condition...)
		}
		if bio.Sex != "" {
			m.Sex = bio.Sex
		}
		if bio.Age != "" {
			m.Age = bio.Age
		}
		if bio.Strain != "" {
			m.Strain = bio.Strain
		}
		if bio.Title != "" {
			m.Title = bio.Title
		}
		if bio.Contributor != "" {
			m.Contributor = bio.Contributor
		}
		if bio.Year != 0 {
			m.Year = bio.Year
		}
		if len(bio.DOI) > 0 {
			m.DOI = append([]string(nil), bio.DOI...)
		}
		if len(bio.PMID) > 0 {
			m.PMID = append([]string(nil), bio.PMID...)
		}
		if len(bio.Link) > 0 {
			m.Link = append([]string(nil), bio.Link...)
		}
		if bio.Source != "" {
			m.Source = bio.Source
		}
		if bio.License != "" {
			m.License = bio.License
		}
		if bio.SourceDescription != "" {
			m.SourceDescription = bio.SourceDescription
		}
		if bio.Access != "" {
			m.Access = bio.Access
		}
		if bio.Accession != "" {
			m.Accession = bio.Accession
		}
		if bio.OrganismOntologyTermID != "" {
			m.OrganismOntologyTermID = bio.OrganismOntologyTermID
		}
		if len(bio.TissueOntologyTermID) > 0 {
			m.TissueOntologyTermID = append([]string(nil), bio.TissueOntologyTermID...)
		}
		if len(bio.CellTypeOntologyTermID) > 0 {
			m.CellTypeOntologyTermID = append([]string(nil), bio.CellTypeOntologyTermID...)
		}
		if bio.AssayOntologyTermID != "" {
			m.AssayOntologyTermID = bio.AssayOntologyTermID
		}
		if bio.SexOntologyTermID != "" {
			m.SexOntologyTermID = bio.SexOntologyTermID
		}
		if len(bio.DiseaseOntologyTermID) > 0 {
			m.DiseaseOntologyTermID = append([]string(nil), bio.DiseaseOntologyTermID...)
		}
	}
	// Layer 2b: sidecar H5ad override (file-shape facts).
	if h5 != nil {
		if h5.GeneIDType != "" {
			m.GeneIDType = h5.GeneIDType
		}
		if h5.NObs > 0 {
			m.NCells = int(h5.NObs)
		}
		if h5.NVars > 0 {
			m.NGenes = int(h5.NVars)
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
func ValidateMetadata(m *Metadata, v *vocab.Vocabulary) []Issue {
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
					"required field %q is missing — set %s=… in a .env "+
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

	// access / accession consistency: `access: public` means the
	// dataset is in a public DB; the accession should look like a
	// known public ID. `access: internal` allows freeform.
	if m.Access != "" && strings.EqualFold(m.Access, "public") {
		if m.Accession != "" && !looksPublicID(m.Accession) {
			issues = append(issues, Issue{
				Severity: SeveritySoft,
				Field:    "accession",
				Message: fmt.Sprintf(
					"access=public but accession=%q does not look like a public ID "+
						"(GSE/SRA/PRJ/phs/EGA/Zenodo/DOI)",
					m.Accession),
			})
		}
	}

	// Soft provenance recommendations.
	if len(m.DOI) == 0 {
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
	case "tissue":
		return len(m.Tissue) > 0
	case "cell_type":
		return len(m.CellType) > 0
	case "technology":
		return m.Technology != ""
	case "suspension_type":
		return m.SuspensionType != ""
	case "disease":
		return len(m.Disease) > 0
	case "condition":
		return len(m.Condition) > 0
	case "sex":
		return m.Sex != ""
	case "age":
		return m.Age != ""
	case "strain":
		return m.Strain != ""
	case "access":
		return m.Access != ""
	case "source":
		return m.Source != ""
	case "accession":
		return m.Accession != ""
	case "doi":
		return len(m.DOI) > 0
	case "pmid":
		return len(m.PMID) > 0
	case "link":
		return len(m.Link) > 0
	case "license":
		return m.License != ""
	case "source_description":
		return m.SourceDescription != ""
	}
	return false
}

// fieldValues returns the set of (field → values) pairs that are
// under controlled-vocabulary check.
func fieldValues(m *Metadata) map[string][]string {
	return map[string][]string{
		"organism":        {m.Organism},
		"tissue":          m.Tissue,
		"cell_type":       m.CellType,
		"technology":      {m.Technology},
		"suspension_type": {m.SuspensionType},
		"disease":         m.Disease,
		"sex":             {m.Sex},
		"access":          {m.Access},
		"source":          {m.Source},
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

package h5ad

// Metadata YAML parser + validator.
//
// Each .h5ad deposit ships with a sibling <basename>.metadata.yaml
// (spec §1.5, §4). This file parses it into a Metadata struct, applies
// the required-field check + controlled-vocabulary check, and surfaces
// findings as Issues that the orchestrator (check.go) sorts into HARD
// vs SOFT tiers.

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Metadata mirrors the §4 template. Each field is yaml-tagged. List
// fields use flexList so a scalar (`celltype: microglia`) or a list
// (`celltype: [microglia, macrophage]`) both parse cleanly — users get
// the scalar form wrong often enough that silent-tolerate is kinder
// than refusing to read the file.
type Metadata struct {
	// Identity
	Title       string `yaml:"title"`
	Contributor string `yaml:"contributor"`
	Year        int    `yaml:"year"`
	Date        string `yaml:"date"`

	// Biology
	Organism    string   `yaml:"organism"`
	Celltype    flexList `yaml:"celltype"`
	Technology  string   `yaml:"technology"`
	Preparation string   `yaml:"preparation"`
	Tissue      flexList `yaml:"tissue"`
	Region      flexList `yaml:"region"`
	Disease     flexList `yaml:"disease"`
	Sex         string   `yaml:"sex"`

	// Provenance
	Internal  string `yaml:"internal"`
	Accession string `yaml:"accession"`
	DOI       string `yaml:"doi"`
	Source    string `yaml:"source"`
	License   string `yaml:"license"`
	Sha256    string `yaml:"sha256"`

	// File contents
	NCells     int    `yaml:"n_cells"`
	NGenes     int    `yaml:"n_genes"`
	GeneIDType string `yaml:"gene_id_type"`
	Expression struct {
		XLayer      string `yaml:"X_layer"`
		CountsLayer string `yaml:"counts_layer"`
	} `yaml:"expression"`
	Embeddings []string `yaml:"embeddings"`

	Notes string `yaml:"notes"`
}

// flexList accepts either a YAML scalar or a YAML sequence; both
// surface as a []string on the Go side.
type flexList []string

func (l *flexList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		// "celltype: microglia" → ["microglia"]
		if node.Value == "" {
			*l = nil
			return nil
		}
		*l = []string{node.Value}
		return nil
	}
	if node.Kind == yaml.SequenceNode {
		out := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("expected string list, got non-scalar item")
			}
			out = append(out, item.Value)
		}
		*l = out
		return nil
	}
	return fmt.Errorf("expected scalar or sequence, got node kind %d", node.Kind)
}

// LoadMetadata reads, parses, and structurally validates the sibling
// metadata file at path. Returns the parsed Metadata + a list of
// PARSE-time issues (malformed YAML, unknown keys, etc.). Semantic
// validation (required fields, vocab) belongs to ValidateMetadata.
func LoadMetadata(path string) (*Metadata, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read metadata yaml %s: %w", path, err)
	}
	var m Metadata
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse metadata yaml %s: %w", path, err)
	}
	return &m, nil
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

// ValidateMetadata runs the YAML-side checks: required fields present,
// controlled-vocab values match, internal/accession consistent,
// embeddings list includes X_umap or X_tsne. Cross-consistency with
// the h5ad inspection report (n_cells / n_genes / sha256 mirroring) is
// done separately in check.go, since that needs an InspectionReport.
func ValidateMetadata(m *Metadata, v *Vocabulary) []Issue {
	if m == nil {
		return []Issue{{
			Severity: SeverityHard,
			Message:  "metadata is nil — file not loaded",
		}}
	}
	var issues []Issue

	// Required-field presence.
	required := map[string]bool{}
	for _, f := range v.Required {
		required[f] = true
	}
	for _, f := range v.Required {
		if !fieldPresent(m, f) {
			issues = append(issues, Issue{
				Severity: SeverityHard,
				Field:    f,
				Message:  fmt.Sprintf("required field %q is missing or empty", f),
			})
		}
	}

	// Controlled-vocabulary value checks. Per spec §3 these are HARD
	// regardless of whether the FIELD is required: bad values for an
	// optional field are still a deposit failure ("do not invent").
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

	// embeddings must include X_umap or X_tsne (spec §1.2 + §4).
	if required["embeddings"] || len(m.Embeddings) > 0 {
		hasUMAP, hasTSNE := false, false
		for _, e := range m.Embeddings {
			switch e {
			case "X_umap":
				hasUMAP = true
			case "X_tsne":
				hasTSNE = true
			}
		}
		if !hasUMAP && !hasTSNE {
			issues = append(issues, Issue{
				Severity: SeverityHard,
				Field:    "embeddings",
				Message:  "embeddings list must include X_umap or X_tsne (spec §1.2)",
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
	case "embeddings":
		return len(m.Embeddings) > 0
	}
	return false
}

// fieldValues returns the set of (field → values) pairs that are
// under controlled-vocabulary check. List fields surface their full
// list; scalar fields surface a single-element slice.
func fieldValues(m *Metadata) map[string][]string {
	return map[string][]string{
		"organism":    {m.Organism},
		"celltype":    []string(m.Celltype),
		"technology":  {m.Technology},
		"preparation": {m.Preparation},
		"tissue":      []string(m.Tissue),
		"region":      []string(m.Region),
		"disease":     []string(m.Disease),
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

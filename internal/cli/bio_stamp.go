// Package cli — bio-field stamping from the .env cascade onto a
// sidecar Doc.
//
// Why this exists: biology fields (organism, tissue, cell_type, …)
// are useful for ANY data file, not just .h5ad. The h5ad inspector
// already pulls them from the file's anndata metadata + the cascade
// (see internal/h5ad/metadata.go::MetadataFromCascade), but for a
// non-h5ad upload — a fastq, a counts.tsv, a BAM — that path doesn't
// run, and without this helper the sidecar's Bio block stays empty
// and the iRODS AVU never gets those fields.
//
// Gap-fill semantics: stampBioFromEnv only fills fields that are
// EMPTY in the existing Bio block. So the precedence is:
//
//   1. h5ad-inspector values (read from the actual file metadata)
//   2. existing sidecar values (preserved from earlier runs)
//   3. .env cascade (fills the remaining gaps)
//
// This means for an h5ad file where `mus h5ad check` already ran,
// nothing the user puts in `.env` can clobber what the file itself
// declared — the file is the source of truth for biology. For a
// non-h5ad file, `.env` is the only source, and it fully populates
// the Bio block.

package cli

import (
	"strconv"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// bioEnvKeys is the set of .env keys that map to Bio fields. Used
// to decide whether to lazy-allocate doc.Bio (don't allocate an
// empty block just because the env has totally unrelated keys).
var bioEnvKeys = []string{
	"organism", "tissue", "cell_type", "technology",
	"suspension_type", "disease", "condition", "sex", "age", "strain",
	"title", "contributor", "year", "doi", "pmid", "link",
	"source", "license", "source_description", "access", "accession",
	"organism_ontology_term_id", "tissue_ontology_term_id",
	"cell_type_ontology_term_id", "assay_ontology_term_id",
	"sex_ontology_term_id", "disease_ontology_term_id",
}

// stampBioFromEnv folds the .env cascade's biology fields into
// doc.Bio with gap-fill semantics: only empty fields are touched.
//
// If env carries no biology key at all, doc.Bio is left untouched
// (including stays-nil-if-nil).
func stampBioFromEnv(doc *sidecar.Doc, env *config.Env) {
	if env == nil || !envHasAnyBioKey(env) {
		return
	}
	if doc.Bio == nil {
		doc.Bio = &sidecar.Bio{}
	}
	b := doc.Bio

	fillScalar(&b.Organism, env, "organism")
	fillScalar(&b.Technology, env, "technology")
	fillScalar(&b.SuspensionType, env, "suspension_type")
	fillScalar(&b.Sex, env, "sex")
	fillScalar(&b.Age, env, "age")
	fillScalar(&b.Strain, env, "strain")
	fillScalar(&b.Title, env, "title")
	fillScalar(&b.Contributor, env, "contributor")
	fillScalar(&b.License, env, "license")
	fillScalar(&b.Source, env, "source")
	fillScalar(&b.SourceDescription, env, "source_description")
	fillScalar(&b.Access, env, "access")
	fillScalar(&b.Accession, env, "accession")
	fillScalar(&b.OrganismOntologyTermID, env, "organism_ontology_term_id")
	fillScalar(&b.AssayOntologyTermID, env, "assay_ontology_term_id")
	fillScalar(&b.SexOntologyTermID, env, "sex_ontology_term_id")

	if b.Year == 0 {
		if year, err := strconv.Atoi(env.String("year")); err == nil && year > 0 {
			b.Year = year
		}
	}

	fillList(&b.Tissue, env, "tissue")
	fillList(&b.CellType, env, "cell_type")
	fillList(&b.Disease, env, "disease")
	fillList(&b.Condition, env, "condition")
	fillList(&b.DOI, env, "doi")
	fillList(&b.PMID, env, "pmid")
	fillList(&b.Link, env, "link")
	fillList(&b.TissueOntologyTermID, env, "tissue_ontology_term_id")
	fillList(&b.CellTypeOntologyTermID, env, "cell_type_ontology_term_id")
	fillList(&b.DiseaseOntologyTermID, env, "disease_ontology_term_id")
}

func fillScalar(target *string, env *config.Env, key string) {
	if *target == "" {
		if v := env.String(key); v != "" {
			*target = v
		}
	}
}

func fillList(target *[]string, env *config.Env, key string) {
	if len(*target) == 0 {
		if vs := env.List(key); len(vs) > 0 {
			*target = append([]string(nil), vs...)
		}
	}
}

func envHasAnyBioKey(env *config.Env) bool {
	for _, k := range bioEnvKeys {
		if env.Has(k) {
			return true
		}
	}
	return false
}

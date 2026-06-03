package cli

// Tests for stampBioFromEnv — pulls .env biology into doc.Bio with
// gap-fill semantics. Critical contract: doc.Bio values already set
// (typically by mus h5ad check, which reads the file's anndata
// metadata) must NOT be clobbered by .env.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// envFromKV writes a .env file with the given key=value pairs into a
// fresh temp dir and returns a loaded config.Env over it.
func envFromKV(t *testing.T, kv map[string]string) *config.Env {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	var body string
	for k, v := range kv {
		body += k + "=" + v + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestStampBioFromEnv_EmptyEnvLeavesDocAlone(t *testing.T) {
	// .env with only non-bio keys → doc.Bio stays nil.
	doc := &sidecar.Doc{}
	env := envFromKV(t, map[string]string{
		"data_project":      "Fiers2025",
		"eln_experiment_id": "123",
	})
	stampBioFromEnv(doc, env)
	if doc.Bio != nil {
		t.Errorf("Bio block allocated despite no bio keys: %+v", doc.Bio)
	}
}

func TestStampBioFromEnv_FillsNilBio(t *testing.T) {
	doc := &sidecar.Doc{}
	env := envFromKV(t, map[string]string{
		"organism":   "human",
		"tissue":     "frontal cortex,hippocampus",
		"technology": "10x_chromium_3p_v3",
		"year":       "2025",
	})
	stampBioFromEnv(doc, env)
	if doc.Bio == nil {
		t.Fatal("Bio block not lazy-allocated")
	}
	if doc.Bio.Organism != "human" {
		t.Errorf("Organism: %q", doc.Bio.Organism)
	}
	if !reflect.DeepEqual(doc.Bio.Tissue, []string{"frontal cortex", "hippocampus"}) {
		t.Errorf("Tissue: %v", doc.Bio.Tissue)
	}
	if doc.Bio.Technology != "10x_chromium_3p_v3" {
		t.Errorf("Technology: %q", doc.Bio.Technology)
	}
	if doc.Bio.Year != 2025 {
		t.Errorf("Year: %d", doc.Bio.Year)
	}
}

func TestStampBioFromEnv_DoesNotClobberExisting(t *testing.T) {
	// Critical: h5ad inspector already populated Organism from the
	// anndata. .env disagrees. The inspector wins.
	doc := &sidecar.Doc{
		Bio: &sidecar.Bio{
			Organism: "Mus musculus", // from h5ad inspector
			Tissue:   []string{"hippocampus"},
		},
	}
	env := envFromKV(t, map[string]string{
		"organism":   "human",
		"tissue":     "frontal cortex",
		"technology": "10x_chromium_3p_v3", // doc didn't have this
	})
	stampBioFromEnv(doc, env)
	if doc.Bio.Organism != "Mus musculus" {
		t.Errorf("Organism clobbered: %q", doc.Bio.Organism)
	}
	if !reflect.DeepEqual(doc.Bio.Tissue, []string{"hippocampus"}) {
		t.Errorf("Tissue clobbered: %v", doc.Bio.Tissue)
	}
	if doc.Bio.Technology != "10x_chromium_3p_v3" {
		t.Errorf("Gap-fill missed Technology: %q", doc.Bio.Technology)
	}
}

func TestStampBioFromEnv_GapFillBehaviour(t *testing.T) {
	// Inspector set Organism + Tissue from the h5ad file. .env adds
	// Disease and CellType. Both end up in the final Bio block.
	doc := &sidecar.Doc{
		Bio: &sidecar.Bio{
			Organism: "human",
			Tissue:   []string{"frontal cortex"},
		},
	}
	env := envFromKV(t, map[string]string{
		"disease":   "Alzheimer disease",
		"cell_type": "microglial cell,astrocyte",
		"access":    "internal",
	})
	stampBioFromEnv(doc, env)
	if !reflect.DeepEqual(doc.Bio.Disease, []string{"Alzheimer disease"}) {
		t.Errorf("Disease: %v", doc.Bio.Disease)
	}
	if !reflect.DeepEqual(doc.Bio.CellType, []string{"microglial cell", "astrocyte"}) {
		t.Errorf("CellType: %v", doc.Bio.CellType)
	}
	if doc.Bio.Access != "internal" {
		t.Errorf("Access: %q", doc.Bio.Access)
	}
	// Originals preserved.
	if doc.Bio.Organism != "human" || len(doc.Bio.Tissue) != 1 {
		t.Errorf("inspector values not preserved: %+v", doc.Bio)
	}
}

func TestStampBioFromEnv_InvalidYearSkipped(t *testing.T) {
	doc := &sidecar.Doc{}
	env := envFromKV(t, map[string]string{
		"year":     "not-a-number",
		"organism": "human", // non-bio key check is satisfied so we proceed
	})
	stampBioFromEnv(doc, env)
	if doc.Bio == nil {
		t.Fatal("Bio block not allocated")
	}
	if doc.Bio.Year != 0 {
		t.Errorf("malformed year should not be parsed: %d", doc.Bio.Year)
	}
	if doc.Bio.Organism != "human" {
		t.Errorf("Organism should still be set: %q", doc.Bio.Organism)
	}
}

func TestStampBioFromEnv_OntologyTermIDs(t *testing.T) {
	doc := &sidecar.Doc{}
	env := envFromKV(t, map[string]string{
		"organism":                   "human",
		"organism_ontology_term_id":  "NCBITaxon:9606",
		"tissue":                     "frontal cortex",
		"tissue_ontology_term_id":    "UBERON:0009834",
		"disease_ontology_term_id":   "MONDO:0004975,MONDO:0007739",
	})
	stampBioFromEnv(doc, env)
	if doc.Bio.OrganismOntologyTermID != "NCBITaxon:9606" {
		t.Errorf("OrganismOntologyTermID: %q", doc.Bio.OrganismOntologyTermID)
	}
	if !reflect.DeepEqual(doc.Bio.TissueOntologyTermID, []string{"UBERON:0009834"}) {
		t.Errorf("TissueOntologyTermID: %v", doc.Bio.TissueOntologyTermID)
	}
	if !reflect.DeepEqual(doc.Bio.DiseaseOntologyTermID,
		[]string{"MONDO:0004975", "MONDO:0007739"}) {
		t.Errorf("DiseaseOntologyTermID: %v", doc.Bio.DiseaseOntologyTermID)
	}
}

func TestStampBioFromEnv_NilEnvIsNoOp(t *testing.T) {
	doc := &sidecar.Doc{Bio: &sidecar.Bio{Organism: "preserved"}}
	stampBioFromEnv(doc, nil)
	if doc.Bio.Organism != "preserved" {
		t.Errorf("nil env should not touch existing Bio: %q", doc.Bio.Organism)
	}
}

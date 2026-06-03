package cli

// Tests for mergeAVUIntoDoc — the inverse of avuFromDoc that pulls
// AVU back from iRODS on `mus irods get` and folds it into the
// local sidecar. Net-additions silent, value changes warn.

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"codeberg.org/mfiers/mus/internal/sidecar"
)

func TestMergeAVU_NetAdditionsAreSilent(t *testing.T) {
	doc := &sidecar.Doc{} // empty sidecar
	avu := map[string][]string{
		"mus_data_project": {"Fiers2025"},
		"mus_organism":     {"Homo sapiens"},
		"mus_tissue":       {"frontal cortex", "hippocampus"},
		"mus_year":         {"2025"},
	}
	var stderr bytes.Buffer
	mutated := mergeAVUIntoDoc(doc, avu, &stderr)

	if !mutated {
		t.Error("expected mutated=true for net-additions")
	}
	if stderr.Len() != 0 {
		t.Errorf("net-additions should be silent, got stderr: %s", stderr.String())
	}
	if doc.DataProject != "Fiers2025" {
		t.Errorf("DataProject not filled: %q", doc.DataProject)
	}
	if doc.Bio == nil {
		t.Fatal("Bio block not lazy-allocated")
	}
	if doc.Bio.Organism != "Homo sapiens" {
		t.Errorf("Organism: %q", doc.Bio.Organism)
	}
	if !reflect.DeepEqual(doc.Bio.Tissue, []string{"frontal cortex", "hippocampus"}) {
		t.Errorf("Tissue: %v", doc.Bio.Tissue)
	}
	if doc.Bio.Year != 2025 {
		t.Errorf("Year: %d", doc.Bio.Year)
	}
}

func TestMergeAVU_NoOpWhenMatching(t *testing.T) {
	doc := &sidecar.Doc{
		DataProject: "Fiers2025",
		Bio: &sidecar.Bio{
			Organism: "Homo sapiens",
			Tissue:   []string{"hippocampus", "frontal cortex"}, // different order
		},
	}
	avu := map[string][]string{
		"mus_data_project": {"Fiers2025"},
		"mus_organism":     {"Homo sapiens"},
		"mus_tissue":       {"frontal cortex", "hippocampus"},
	}
	var stderr bytes.Buffer
	mutated := mergeAVUIntoDoc(doc, avu, &stderr)
	if mutated {
		t.Error("expected mutated=false when AVU matches sidecar")
	}
	if stderr.Len() != 0 {
		t.Errorf("matching values should be silent: %s", stderr.String())
	}
}

func TestMergeAVU_MutationWarnsAndOverwrites(t *testing.T) {
	doc := &sidecar.Doc{
		Bio: &sidecar.Bio{
			Organism: "Mus musculus", // mismatch with AVU
			Tissue:   []string{"old tissue"},
		},
	}
	avu := map[string][]string{
		"mus_organism": {"Homo sapiens"},
		"mus_tissue":   {"frontal cortex"},
	}
	var stderr bytes.Buffer
	mutated := mergeAVUIntoDoc(doc, avu, &stderr)
	if !mutated {
		t.Error("expected mutated=true")
	}
	out := stderr.String()
	if !strings.Contains(out, "~ mus_organism: Mus musculus → Homo sapiens") {
		t.Errorf("missing organism warning, got:\n%s", out)
	}
	if !strings.Contains(out, "~ mus_tissue: old tissue → frontal cortex") {
		t.Errorf("missing tissue warning, got:\n%s", out)
	}
	if doc.Bio.Organism != "Homo sapiens" {
		t.Errorf("Organism not overwritten: %q", doc.Bio.Organism)
	}
	if !reflect.DeepEqual(doc.Bio.Tissue, []string{"frontal cortex"}) {
		t.Errorf("Tissue not overwritten: %v", doc.Bio.Tissue)
	}
}

func TestMergeAVU_MixedNetAdditionAndMutation(t *testing.T) {
	// Local has organism set, AVU has different organism + a new tissue field.
	// Mutation warns; new field is silent.
	doc := &sidecar.Doc{
		Bio: &sidecar.Bio{Organism: "Mus musculus"},
	}
	avu := map[string][]string{
		"mus_organism": {"Homo sapiens"}, // mutation
		"mus_tissue":   {"hippocampus"},  // net addition
	}
	var stderr bytes.Buffer
	mergeAVUIntoDoc(doc, avu, &stderr)

	out := stderr.String()
	if !strings.Contains(out, "~ mus_organism") {
		t.Errorf("expected mutation warning for organism")
	}
	if strings.Contains(out, "~ mus_tissue") {
		t.Errorf("net-addition tissue should not warn, got:\n%s", out)
	}
}

func TestMergeAVU_IgnoresContentIdentityFields(t *testing.T) {
	// mus_sha256, mus_recursive_sha256, mus_archive_sha256,
	// mus_uploaded_at are content identity — they describe what was
	// uploaded, not what the dataset IS. The merger must NOT touch them.
	doc := &sidecar.Doc{
		File: sidecar.FileInfo{Sha256: "local-sha"},
		Folder: &sidecar.FolderInfo{
			RecursiveSha256: "local-merkle",
			FileCount:       10,
		},
		Archive: &sidecar.ArchiveInfo{
			Sha256: "local-archive-sha",
			Format: "tar.gz",
		},
		IRODS: &sidecar.IRODS{UploadedAt: time.Now()},
	}
	avu := map[string][]string{
		"mus_sha256":           {"remote-sha-different"},
		"mus_recursive_sha256": {"remote-merkle-different"},
		"mus_archive_sha256":   {"remote-archive-different"},
		"mus_uploaded_at":      {"2030-01-01T00:00:00Z"},
		// Add a non-identity field so we know merge actually ran.
		"mus_archive_format": {"tar.gz"}, // same → no-op
	}
	var stderr bytes.Buffer
	mergeAVUIntoDoc(doc, avu, &stderr)

	if doc.File.Sha256 != "local-sha" {
		t.Errorf("mus_sha256 was overwritten: %q", doc.File.Sha256)
	}
	if doc.Folder.RecursiveSha256 != "local-merkle" {
		t.Errorf("mus_recursive_sha256 was overwritten: %q", doc.Folder.RecursiveSha256)
	}
	if doc.Archive.Sha256 != "local-archive-sha" {
		t.Errorf("mus_archive_sha256 was overwritten: %q", doc.Archive.Sha256)
	}
	if stderr.Len() != 0 {
		t.Errorf("content-identity fields should not trigger warnings: %s", stderr.String())
	}
}

func TestMergeAVU_H5adShapeFields(t *testing.T) {
	doc := &sidecar.Doc{} // no H5ad block
	avu := map[string][]string{
		"mus_h5ad_n_obs":                  {"12345"},
		"mus_h5ad_n_vars":                 {"30000"},
		"mus_h5ad_inspected_check_version": {"3"},
		"mus_h5ad_inspected_at":            {"2026-05-28T12:00:00Z"},
		"mus_h5ad_layers":                  {"counts", "logcounts"},
	}
	var stderr bytes.Buffer
	mutated := mergeAVUIntoDoc(doc, avu, &stderr)

	if !mutated {
		t.Error("expected mutated=true")
	}
	if doc.H5ad == nil {
		t.Fatal("H5ad block not lazy-allocated")
	}
	if doc.H5ad.NObs != 12345 {
		t.Errorf("NObs: %d", doc.H5ad.NObs)
	}
	if doc.H5ad.NVars != 30000 {
		t.Errorf("NVars: %d", doc.H5ad.NVars)
	}
	if doc.H5ad.InspectedCheckVersion != 3 {
		t.Errorf("InspectedCheckVersion: %d", doc.H5ad.InspectedCheckVersion)
	}
	expectedTime := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	if !doc.H5ad.InspectedAt.Equal(expectedTime) {
		t.Errorf("InspectedAt: %v", doc.H5ad.InspectedAt)
	}
}

func TestMergeAVU_DoesNotAllocateEmptyBlocks(t *testing.T) {
	// AVU has only DataProject; Bio / H5ad / ELN should remain nil.
	doc := &sidecar.Doc{}
	avu := map[string][]string{"mus_data_project": {"Fiers2025"}}
	mergeAVUIntoDoc(doc, avu, &bytes.Buffer{})
	if doc.Bio != nil {
		t.Errorf("Bio allocated despite no bio fields in AVU: %+v", doc.Bio)
	}
	if doc.H5ad != nil {
		t.Errorf("H5ad allocated despite no h5ad fields in AVU: %+v", doc.H5ad)
	}
	if doc.ELN != nil {
		t.Errorf("ELN allocated despite no eln fields in AVU: %+v", doc.ELN)
	}
}

func TestMergeAVU_InvalidIntIsSilentSkip(t *testing.T) {
	// Garbage in the int-typed AVU should be silently skipped — better
	// than crashing the download path on a single bad field.
	doc := &sidecar.Doc{Bio: &sidecar.Bio{}}
	avu := map[string][]string{
		"mus_year":   {"not-a-number"},
		"mus_tissue": {"hippocampus"},
	}
	mutated := mergeAVUIntoDoc(doc, avu, &bytes.Buffer{})
	if doc.Bio.Year != 0 {
		t.Errorf("malformed year should be skipped: %d", doc.Bio.Year)
	}
	if !mutated {
		t.Error("expected mutation from tissue addition")
	}
}

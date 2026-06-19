package sidecar

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRoundtrip(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "sample.h5ad")
	if err := os.WriteFile(data, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(data)

	in := &Doc{
		Note: "test sidecar",
		Tags: []string{"raw", "wt"},
		File: FileInfo{
			Sha256:  "deadbeef",
			Size:    st.Size(),
			Mtime:   st.ModTime().UTC().Truncate(time.Second),
			Hashed:  time.Now().UTC().Truncate(time.Second),
			Host:    "test-host",
			AbsPath: data,
		},
		IRODS: &IRODS{
			URL:    "https://mango/data-object/view/zone/home/x",
			Path:   "/zone/home/x",
			Status: "ok",
		},
		ELN: &ELN{ExperimentID: "12345"},
	}
	path := SidecarPath(data)
	if err := Write(path, in); err != nil {
		t.Fatal(err)
	}
	// Sanity-check the file IS flat env-format, not TOML.
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "[file]") || strings.Contains(string(raw), "[irods]") {
		t.Errorf("sidecar still has TOML sections:\n%s", raw)
	}

	out, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.File.Sha256 != "deadbeef" || out.IRODS == nil || out.IRODS.Status != "ok" {
		t.Errorf("roundtrip mismatch: %+v", out)
	}
	if len(out.Tags) != 2 || out.Tags[0] != "raw" || out.Tags[1] != "wt" {
		t.Errorf("tags = %v", out.Tags)
	}
	if out.Created.IsZero() || out.Updated.IsZero() {
		t.Errorf("created/updated not set")
	}
	if out.ELN == nil || out.ELN.ExperimentID != "12345" {
		t.Errorf("eln experiment_id roundtrip: %+v", out.ELN)
	}
}

func TestH5adInfoRoundtrip(t *testing.T) {
	dir := t.TempDir()
	scPath := filepath.Join(dir, "mydata.h5ad.mus")

	now := time.Now().UTC().Truncate(time.Second)
	in := &Doc{
		File: FileInfo{Sha256: "abc"},
		Bio: &Bio{
			Organism:       "human",
			Tissue:         []string{"prefrontal cortex", "hippocampus"},
			CellType:       []string{"microglial cell", "astrocyte"},
			Technology:     "10x_chromium_3p_v3",
			SuspensionType: "nucleus",
			Disease:        []string{"Alzheimer disease"},
			Sex:            "mixed",
			Access:         "public",
			Source:         "external",
			Accession:      "GSE123456",
			DOI:            []string{"10.1038/foo"},
			Year:           2026,
			Contributor:    "Jane",
		},
		H5ad: &H5ad{
			GeneIDType:     "hgnc_symbol",
			NObs:           73888,
			NVars:          24940,
			Layers:         []string{"counts"},
			Embedding:      "X_umap",
			AnnDataVersion: "0.10.7",
			InspectedAt:    now,
		},
	}
	if err := Write(scPath, in); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(scPath)
	// Spot-check schema-v2 serialisation: Bio fields are bare with
	// CellxGene-style values; H5ad fields are h5ad_-prefixed.
	for _, want := range []string{
		"organism=human",
		"cell_type=microglial cell,astrocyte",
		"tissue=prefrontal cortex,hippocampus",
		"technology=10x_chromium_3p_v3",
		"suspension_type=nucleus",
		"access=public",
		"h5ad_n_obs=73888",
		"h5ad_n_vars=24940",
		"h5ad_layers=counts",
		"h5ad_embedding=X_umap",
		"h5ad_anndata_version=0.10.7",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %q in:\n%s", want, raw)
		}
	}
	// Confirm legacy schema-v1 key shapes are gone.
	for _, banned := range []string{"celltype=", "preparation=", "internal=", "h5ad_organism", "h5ad_celltype", "h5ad_tissue"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("found legacy %q key in:\n%s", banned, raw)
		}
	}

	out, err := Read(scPath)
	if err != nil {
		t.Fatal(err)
	}
	if out.Bio == nil {
		t.Fatalf("Bio block lost on read: %+v", out)
	}
	if out.Bio.Organism != "human" {
		t.Errorf("Bio.Organism = %q", out.Bio.Organism)
	}
	if len(out.Bio.CellType) != 2 || out.Bio.CellType[0] != "microglial cell" {
		t.Errorf("Bio.CellType = %v", out.Bio.CellType)
	}
	if out.Bio.SuspensionType != "nucleus" {
		t.Errorf("Bio.SuspensionType = %q", out.Bio.SuspensionType)
	}
	if out.Bio.Access != "public" || out.Bio.Source != "external" {
		t.Errorf("access/source roundtrip wrong: %+v", out.Bio)
	}
	if out.H5ad == nil {
		t.Fatalf("H5ad block lost on read: %+v", out)
	}
	if out.H5ad.NObs != 73888 || out.H5ad.NVars != 24940 {
		t.Errorf("dims = %d/%d", out.H5ad.NObs, out.H5ad.NVars)
	}
	if !out.H5ad.InspectedAt.Equal(now) {
		t.Errorf("InspectedAt = %v, want %v", out.H5ad.InspectedAt, now)
	}
}

// TestBioMONDORoundtrip exercises the quoted-list-item path: a MONDO
// disease label with embedded commas must survive Write → Read.
func TestBioMONDORoundtrip(t *testing.T) {
	dir := t.TempDir()
	scPath := filepath.Join(dir, "x.h5ad.mus")
	in := &Doc{
		File: FileInfo{Sha256: "abc"},
		Bio: &Bio{
			Disease: []string{
				"Alzheimer disease",
				"leukoencephalopathy, diffuse hereditary, with spheroids 1",
				"normal",
			},
		},
	}
	if err := Write(scPath, in); err != nil {
		t.Fatal(err)
	}
	out, err := Read(scPath)
	if err != nil {
		t.Fatal(err)
	}
	if out.Bio == nil {
		t.Fatalf("Bio block missing")
	}
	want := []string{"Alzheimer disease",
		"leukoencephalopathy, diffuse hereditary, with spheroids 1",
		"normal"}
	if len(out.Bio.Disease) != 3 || out.Bio.Disease[1] != want[1] {
		t.Errorf("MONDO comma-label roundtrip failed; got %v", out.Bio.Disease)
	}
}

func TestH5adInspectedSha256Roundtrip(t *testing.T) {
	dir := t.TempDir()
	scPath := filepath.Join(dir, "x.h5ad.mus")
	sha := "deadbeefcafebabe1234567890abcdef1234567890abcdef1234567890abcdef"
	in := &Doc{
		File: FileInfo{Sha256: sha},
		H5ad: &H5ad{
			InspectedAt:     time.Now().UTC().Truncate(time.Second),
			InspectedSha256: sha,
		},
	}
	if err := Write(scPath, in); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(scPath)
	if !strings.Contains(string(raw), "h5ad_inspected_sha256="+sha) {
		t.Errorf("inspected_sha256 not serialised, got:\n%s", raw)
	}
	out, err := Read(scPath)
	if err != nil {
		t.Fatal(err)
	}
	if out.H5ad == nil || out.H5ad.InspectedSha256 != sha {
		t.Errorf("InspectedSha256 roundtrip failed: %+v", out.H5ad)
	}
}

func TestBioAndH5adAbsentWhenNoFields(t *testing.T) {
	// Doc without Bio / H5ad set should NOT serialise any of their keys.
	dir := t.TempDir()
	scPath := filepath.Join(dir, "x.mus")
	in := &Doc{File: FileInfo{Sha256: "abc"}}
	if err := Write(scPath, in); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(scPath)
	for _, banned := range []string{"organism=", "celltype=", "technology=", "h5ad_"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("unexpected key %q in empty-block sidecar:\n%s", banned, raw)
		}
	}
}

func TestFolderInfoRoundtrip(t *testing.T) {
	dir := t.TempDir()
	scPath := filepath.Join(dir, "scripts.mus")

	in := &Doc{
		Kind: "folder",
		Folder: &FolderInfo{
			FileCount:       42,
			DirCount:        7,
			TotalBytes:      123456,
			MedianBytes:     2048,
			RecursiveSha256: "abc123",
		},
	}
	if err := Write(scPath, in); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(scPath)
	if !strings.Contains(string(raw), "dir_count=7") {
		t.Errorf("dir_count not serialised, got:\n%s", raw)
	}
	out, err := Read(scPath)
	if err != nil {
		t.Fatal(err)
	}
	if out.Folder == nil {
		t.Fatalf("folder info missing on read: %+v", out)
	}
	if out.Folder.FileCount != 42 {
		t.Errorf("FileCount roundtrip: got %d, want 42", out.Folder.FileCount)
	}
	if out.Folder.DirCount != 7 {
		t.Errorf("DirCount roundtrip: got %d, want 7", out.Folder.DirCount)
	}
}

func TestStale(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "x.bin")
	if err := os.WriteFile(data, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(data)
	d := &Doc{
		File: FileInfo{Size: st.Size(), Mtime: st.ModTime().UTC().Truncate(time.Second)},
	}
	if d.Stale(st) {
		t.Errorf("fresh sidecar reported stale")
	}
	if err := os.WriteFile(data, []byte("longer payload now"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(3 * time.Second)
	_ = os.Chtimes(data, future, future)
	st2, _ := os.Stat(data)
	if !d.Stale(st2) {
		t.Errorf("modified file should be stale")
	}
}

func TestIsSidecar(t *testing.T) {
	cases := map[string]bool{
		"foo.h5ad.mus": true,
		"data.tsv.mus": true,
		".env":         false, // folder config, not a sidecar
		"foo":          false,
		"foo.mus":      true,
	}
	for in, want := range cases {
		if got := IsSidecar(in); got != want {
			t.Errorf("IsSidecar(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSidecarFlatFormat(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "demo.csv")
	os.WriteFile(data, []byte("x"), 0o644)
	d := &Doc{
		File: FileInfo{Sha256: "abc123", Size: 1, Mtime: time.Now().UTC().Truncate(time.Second)},
		ELN:  &ELN{ExperimentID: "42"},
	}
	path := SidecarPath(data)
	if err := Write(path, d); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	// Spot-check we see flat key=value lines.
	checks := []string{"sha256=abc123", "size=1", "eln_experiment_id=42"}
	for _, want := range checks {
		if !strings.Contains(string(raw), want) {
			t.Errorf("sidecar missing %q\nfile contents:\n%s", want, raw)
		}
	}
}

func TestDEBlockRoundtrip(t *testing.T) {
	dir := t.TempDir()
	scPath := filepath.Join(dir, "carlo_mg_DAM_vs_homeo.tsv.mus")

	now := time.Now().UTC().Truncate(time.Second)
	in := &Doc{
		File: FileInfo{Sha256: "abc"},
		DE: &DE{
			Format:                "deseq2",
			Method:                "DESeq2",
			Term:                  []string{"cluster"},
			Contrast:              []string{"DAM_vs_homeo"},
			Cluster:               "microglia.DAM",
			DatasetID:              "SGF",
			DatasetH5ad:            "Single.Grafts.h5ad",
			ClusterObsColumn:      "leiden_cluster",
			SoftwareVersion:       "DESeq2 1.42.0",
			ReferenceType:         "pairwise",
			Covariates:            []string{"sex", "batch"},
			FilterCriteria:        "expressed in ≥3 cells per sample",
			Description:           "Per-cluster DE for the DAM vs homeostatic contrast.",
			RowCount:              18569,
			InspectedAt:           now,
			InspectedSha256:       "deadbeef",
			InspectedCheckVersion: 1,
		},
	}
	if err := Write(scPath, in); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(scPath)
	for _, want := range []string{
		"de_format=deseq2",
		"de_method=DESeq2",
		"de_term=cluster",
		"de_contrast=DAM_vs_homeo",
		"de_cluster=microglia.DAM",
		"de_dataset_id=SGF",
		"de_dataset_h5ad=Single.Grafts.h5ad",
		"de_cluster_obs_column=leiden_cluster",
		"de_software_version=DESeq2 1.42.0",
		"de_reference_type=pairwise",
		"de_covariates=sex,batch",
		"de_row_count=18569",
		"de_inspected_sha256=deadbeef",
		"de_check_version=1",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %q in:\n%s", want, raw)
		}
	}

	// Roundtrip via Read.
	got, err := Read(scPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.DE == nil {
		t.Fatal("DE block lost on roundtrip")
	}
	if got.DE.Format != "deseq2" {
		t.Errorf("Format: got %q", got.DE.Format)
	}
	if !equalStringSlice(got.DE.Term, []string{"cluster"}) {
		t.Errorf("Term: %v", got.DE.Term)
	}
	if got.DE.Cluster != "microglia.DAM" {
		t.Errorf("Cluster: %q", got.DE.Cluster)
	}
	if got.DE.DatasetID != "SGF" {
		t.Errorf("DatasetID: %q", got.DE.DatasetID)
	}
	if got.DE.RowCount != 18569 {
		t.Errorf("RowCount: %d", got.DE.RowCount)
	}
	if got.DE.InspectedCheckVersion != 1 {
		t.Errorf("CheckVersion: %d", got.DE.InspectedCheckVersion)
	}
}

func TestDEBlock_LongFormatMultiValueTermContrast(t *testing.T) {
	// de_format=long with multi-value term/contrast should
	// roundtrip as proper lists thanks to the list-key registration.
	dir := t.TempDir()
	scPath := filepath.Join(dir, "long.tsv.mus")
	in := &Doc{
		File: FileInfo{Sha256: "x"},
		DE: &DE{
			Format:   "long",
			Term:     []string{"genotype", "genotype:sex", "sex"},
			Contrast: []string{"K:M_vs_W:F", "K_vs_W", "M_vs_F"},
			DatasetID: "ABC",
			RowCount: 55707,
		},
	}
	if err := Write(scPath, in); err != nil {
		t.Fatal(err)
	}
	got, err := Read(scPath)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStringSlice(got.DE.Term, []string{"genotype", "genotype:sex", "sex"}) {
		t.Errorf("Term list lost on roundtrip: %v", got.DE.Term)
	}
	if !equalStringSlice(got.DE.Contrast, []string{"K:M_vs_W:F", "K_vs_W", "M_vs_F"}) {
		t.Errorf("Contrast list lost on roundtrip: %v", got.DE.Contrast)
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

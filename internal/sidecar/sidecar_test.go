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
		H5ad: &H5ad{
			Organism:       "human",
			Celltype:       []string{"microglia", "macrophage"},
			Technology:     "10x_chromium_3p_v3",
			Preparation:    "single_nucleus",
			Tissue:         []string{"brain"},
			Region:         []string{"prefrontal_cortex", "hippocampus"},
			Disease:        []string{"alzheimer"},
			Sex:            "mixed",
			Internal:       "no",
			Accession:      "GSE123456",
			DOI:            "10.1038/foo",
			Year:           2026,
			Contributor:    "Jane",
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
	// Spot-check key serialisation forms.
	for _, want := range []string{
		"h5ad_organism=human",
		"h5ad_celltype=microglia,macrophage",
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

	out, err := Read(scPath)
	if err != nil {
		t.Fatal(err)
	}
	if out.H5ad == nil {
		t.Fatalf("H5ad block lost on read: %+v", out)
	}
	h := out.H5ad
	if h.Organism != "human" {
		t.Errorf("Organism = %q", h.Organism)
	}
	if len(h.Celltype) != 2 || h.Celltype[0] != "microglia" || h.Celltype[1] != "macrophage" {
		t.Errorf("Celltype = %v", h.Celltype)
	}
	if h.NObs != 73888 || h.NVars != 24940 {
		t.Errorf("dims = %d/%d", h.NObs, h.NVars)
	}
	if h.Year != 2026 {
		t.Errorf("Year = %d", h.Year)
	}
	if !h.InspectedAt.Equal(now) {
		t.Errorf("InspectedAt = %v, want %v", h.InspectedAt, now)
	}
}

func TestH5adAbsentWhenNoFields(t *testing.T) {
	// Doc with no H5ad set should NOT serialise any h5ad_* keys.
	dir := t.TempDir()
	scPath := filepath.Join(dir, "x.mus")
	in := &Doc{File: FileInfo{Sha256: "abc"}}
	if err := Write(scPath, in); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(scPath)
	if strings.Contains(string(raw), "h5ad_") {
		t.Errorf("h5ad_* keys present despite empty H5ad block:\n%s", raw)
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

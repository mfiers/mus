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
			Organism:    "human",
			Celltype:    []string{"microglia", "macrophage"},
			Technology:  "10x_chromium_3p_v3",
			Preparation: "single_nucleus",
			Tissue:      []string{"brain"},
			Region:      []string{"prefrontal_cortex", "hippocampus"},
			Disease:     []string{"alzheimer"},
			Sex:         "mixed",
			Internal:    "no",
			Accession:   "GSE123456",
			DOI:         "10.1038/foo",
			Year:        2026,
			Contributor: "Jane",
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
	// Spot-check serialisation: Bio fields are bare, H5ad fields are
	// h5ad_-prefixed.
	for _, want := range []string{
		"organism=human",
		"celltype=microglia,macrophage",
		"technology=10x_chromium_3p_v3",
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
	// Confirm we DIDN'T emit the old h5ad_organism style.
	for _, banned := range []string{"h5ad_organism", "h5ad_celltype", "h5ad_tissue"} {
		if strings.Contains(string(raw), banned+"=") {
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
	if len(out.Bio.Celltype) != 2 || out.Bio.Celltype[0] != "microglia" {
		t.Errorf("Bio.Celltype = %v", out.Bio.Celltype)
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

package sidecar

import (
	"os"
	"path/filepath"
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
	}
	path := SidecarPath(data)
	if err := Write(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.File.Sha256 != "deadbeef" || out.IRODS == nil || out.IRODS.Status != "ok" {
		t.Errorf("roundtrip mismatch: %+v", out)
	}
	if out.Tags[0] != "raw" || out.Tags[1] != "wt" {
		t.Errorf("tags = %v", out.Tags)
	}
	if out.Created.IsZero() || out.Updated.IsZero() {
		t.Errorf("created/updated not set")
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
	// modify content + mtime
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
		".mus":         false, // folder config, not a sidecar
		"foo":          false,
		"foo.mus":      true,
	}
	for in, want := range cases {
		if got := IsSidecar(in); got != want {
			t.Errorf("IsSidecar(%q) = %v, want %v", in, got, want)
		}
	}
}

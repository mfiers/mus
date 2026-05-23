package irodscache

import (
	"path/filepath"
	"testing"
	"time"
)

func newCache(t *testing.T) *Cache {
	t.Helper()
	tmp := t.TempDir()
	c, err := Open(filepath.Join(tmp, "scans.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestReplaceAndList(t *testing.T) {
	c := newCache(t)
	objects := []Object{
		{Path: "/zone/home/lab", IsObject: false, Creator: "rods"},
		{Path: "/zone/home/lab/raw.csv", IsObject: true, Size: 1024, Checksum: "abc123",
			Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Creator: "alice"},
		{Path: "/zone/home/lab/processed.parquet", IsObject: true, Size: 5000, Checksum: "def456",
			Modified: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Creator: "bob"},
	}
	info, err := c.Replace("/zone/home/lab", objects)
	if err != nil {
		t.Fatal(err)
	}
	if info.ObjectCount != 2 {
		t.Errorf("ObjectCount = %d, want 2", info.ObjectCount)
	}
	if info.BytesTotal != 6024 {
		t.Errorf("BytesTotal = %d, want 6024", info.BytesTotal)
	}

	got, err := c.ListObjects("/zone/home/lab", ListObjectsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("len(got) = %d, want 3", len(got))
	}
	// Verify checksum + creator preserved
	for _, o := range got {
		if o.Path == "/zone/home/lab/raw.csv" {
			if o.Checksum != "abc123" || o.Creator != "alice" || o.Size != 1024 {
				t.Errorf("raw.csv = %+v", o)
			}
			if !o.IsObject {
				t.Errorf("raw.csv should be a data object")
			}
		}
	}
}

func TestReplaceIsAtomic(t *testing.T) {
	c := newCache(t)
	first := []Object{
		{Path: "/x", IsObject: false},
		{Path: "/x/a.txt", IsObject: true, Size: 10, Checksum: "old"},
	}
	if _, err := c.Replace("/x", first); err != nil {
		t.Fatal(err)
	}
	// Re-scan with different contents
	second := []Object{
		{Path: "/x", IsObject: false},
		{Path: "/x/b.txt", IsObject: true, Size: 20, Checksum: "new"},
	}
	if _, err := c.Replace("/x", second); err != nil {
		t.Fatal(err)
	}
	got, _ := c.ListObjects("/x", ListObjectsOpts{})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	for _, o := range got {
		if o.Path == "/x/a.txt" {
			t.Errorf("old object not cleared on replace")
		}
	}
}

func TestFindByChecksum(t *testing.T) {
	c := newCache(t)
	c.Replace("/a", []Object{
		{Path: "/a/one", IsObject: true, Checksum: "shared", Size: 1},
		{Path: "/a/two", IsObject: true, Checksum: "unique-a", Size: 2},
	})
	c.Replace("/b", []Object{
		{Path: "/b/three", IsObject: true, Checksum: "shared", Size: 3},
	})

	hits, err := c.FindByChecksum("shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Errorf("len(hits) = %d, want 2", len(hits))
	}
	paths := map[string]bool{}
	for _, h := range hits {
		paths[h.Path] = true
	}
	if !paths["/a/one"] || !paths["/b/three"] {
		t.Errorf("missing expected paths: %v", paths)
	}
}

func TestFilters(t *testing.T) {
	c := newCache(t)
	c.Replace("/zone", []Object{
		{Path: "/zone", IsObject: false},
		{Path: "/zone/sub", IsObject: false},
		{Path: "/zone/sub/f1.csv", IsObject: true, Size: 1},
		{Path: "/zone/sub/f2.csv", IsObject: true, Size: 2},
		{Path: "/zone/other.csv", IsObject: true, Size: 3},
	})

	dataOnly, _ := c.ListObjects("/zone", ListObjectsOpts{OnlyDataObjects: true})
	if len(dataOnly) != 3 {
		t.Errorf("OnlyDataObjects len = %d, want 3", len(dataOnly))
	}
	colsOnly, _ := c.ListObjects("/zone", ListObjectsOpts{OnlyCollections: true})
	if len(colsOnly) != 2 {
		t.Errorf("OnlyCollections len = %d, want 2", len(colsOnly))
	}
	prefixed, _ := c.ListObjects("/zone", ListObjectsOpts{PathPrefix: "/zone/sub"})
	if len(prefixed) != 3 {
		t.Errorf("PathPrefix len = %d, want 3 (sub itself + two children)", len(prefixed))
	}
}

func TestDelete(t *testing.T) {
	c := newCache(t)
	c.Replace("/x", []Object{{Path: "/x", IsObject: false}})
	if err := c.Delete("/x"); err != nil {
		t.Fatal(err)
	}
	if info, _ := c.GetScan("/x"); info != nil {
		t.Errorf("scan still present after delete")
	}
	if err := c.Delete("/nonexistent"); err == nil {
		t.Errorf("expected error deleting unknown scan")
	}
}

func TestListScans(t *testing.T) {
	c := newCache(t)
	c.Replace("/a", []Object{{Path: "/a", IsObject: false}})
	time.Sleep(10 * time.Millisecond) // ensure different scanned_at
	c.Replace("/b", []Object{{Path: "/b", IsObject: false}})

	scans, err := c.ListScans()
	if err != nil {
		t.Fatal(err)
	}
	if len(scans) != 2 {
		t.Fatalf("len(scans) = %d, want 2", len(scans))
	}
	// most-recent first
	if scans[0].ScannedAt.Before(scans[1].ScannedAt) {
		t.Errorf("not sorted by scanned_at desc")
	}
}

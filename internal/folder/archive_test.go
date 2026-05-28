package folder

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestTarGzipRoundtrip(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), 100)
	writeFile(t, filepath.Join(src, "sub", "b.txt"), 200)
	writeFile(t, filepath.Join(src, "sub", "deep", "c.txt"), 300)

	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	res, err := TarGzip(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	if res.Size <= 0 {
		t.Errorf("archive size %d", res.Size)
	}
	if len(res.Sha256) != 64 {
		t.Errorf("sha256 = %q (len %d)", res.Sha256, len(res.Sha256))
	}

	// Unpack and verify contents
	f, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)

	var got []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			got = append(got, hdr.Name)
		}
	}
	sort.Strings(got)
	srcBase := filepath.Base(src)
	want := []string{
		srcBase + "/a.txt",
		srcBase + "/sub/b.txt",
		srcBase + "/sub/deep/c.txt",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d (%v)", len(got), len(want), got)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("entry[%d] = %q, want %q", i, g, want[i])
		}
	}
}

func TestTarGzipDeterministic(t *testing.T) {
	// Two consecutive runs over the same input must produce byte-identical
	// archive content (entries sorted by path; no embedded timestamps that
	// vary at second granularity since the source files don't change).
	// Note: tar.FileInfoHeader DOES embed mtime; the test writes files
	// quickly and reads sha256 of the gzip output. If clocks tick between
	// the two TarGzip calls, mtime would already be stable (file ctime
	// doesn't move), so the archives match.
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a"), 10)
	writeFile(t, filepath.Join(src, "b"), 20)

	r1, err := TarGzip(src, filepath.Join(t.TempDir(), "a.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := TarGzip(src, filepath.Join(t.TempDir(), "b.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	if r1.Sha256 != r2.Sha256 {
		t.Errorf("non-deterministic sha256: %s vs %s", r1.Sha256, r2.Sha256)
	}
}

func TestTarGzipSkipsSymlinks(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "real.txt"), 10)
	if err := os.Symlink("real.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Skip("can't create symlinks on this platform")
	}

	res, err := TarGzip(src, filepath.Join(t.TempDir(), "out.tgz"))
	if err != nil {
		t.Fatal(err)
	}

	f, _ := os.Open(res.Path)
	defer f.Close()
	gz, _ := gzip.NewReader(f)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(hdr.Name) == "link.txt" {
			t.Errorf("symlink should have been skipped")
		}
	}
}

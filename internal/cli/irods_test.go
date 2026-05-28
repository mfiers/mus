package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/mfiers/mus/internal/folder"
	"codeberg.org/mfiers/mus/internal/hashcache"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// --- parseMangoFile ---------------------------------------------------------

func TestParseMangoFile(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "bare URL (modern Python mus)",
			in:   "https://mango.kuleuven.be/data-object/view/gbiomed/home/BADS/foo.h5ad",
			want: "/gbiomed/home/BADS/foo.h5ad",
		},
		{
			name: "bare URL with surrounding whitespace",
			in:   "  https://mango.kuleuven.be/data-object/view/gbiomed/home/BADS/foo.h5ad\n",
			want: "/gbiomed/home/BADS/foo.h5ad",
		},
		{
			name: "legacy JSON form (older Python mus)",
			in:   `{"url": "https://mango.kuleuven.be/data-object/view/gbiomed/home/BADS/foo.h5ad", "checksum": "abc"}`,
			want: "/gbiomed/home/BADS/foo.h5ad",
		},
		{
			name: "JSON without url field falls back to plain-URL path (treats the JSON itself as the URL → no marker → error)",
			in:   `{"checksum": "abc"}`,
			// Falls back to using the raw content as a URL; no marker → error.
			wantErr: true,
		},
		{
			name: "URL without the data-object/view marker",
			in:   "https://example.com/some/other/path/foo.h5ad",
			wantErr: true,
		},
		{
			name: "empty file",
			in:   "",
			wantErr: true,
		},
		{
			name: "marker present but path empty",
			in:   "https://mango.kuleuven.be/data-object/view/",
			want: "/",
			// "/" is what's after the marker; the caller (parseMangoFile)
			// rejects empty + "/" — verified below.
			wantErr: true,
		},
		{
			name: "marker present but nothing after",
			in:   "https://mango.kuleuven.be/data-object/view",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMangoFile([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseMangoFile(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Errorf("parseMangoFile(%q): unexpected error %v", tc.in, err)
				return
			}
			if got != tc.want {
				t.Errorf("parseMangoFile(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- firstPathSegment -------------------------------------------------------

func TestFirstPathSegment(t *testing.T) {
	cases := map[string]string{
		"/gbiomed/home/BADS/file.csv": "gbiomed",
		"gbiomed/home/BADS":           "gbiomed",
		"//gbiomed///home":            "gbiomed",
		"/":                           "",
		"":                            "",
		"/single":                     "single",
	}
	for in, want := range cases {
		if got := firstPathSegment(in); got != want {
			t.Errorf("firstPathSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- resolvePackMode --------------------------------------------------------

func TestResolvePackMode(t *testing.T) {
	cases := []struct {
		in   string
		want packMode
		err  bool
	}{
		{"", packAuto, false},
		{"auto", packAuto, false},
		{"AUTO", packAuto, false}, // case-insensitive
		{"tar.gz", packTarGz, false},
		{"targz", packTarGz, false},
		{"tar", packTarGz, false},
		{"none", packNone, false},
		{"no", packNone, false},
		{"asis", packNone, false},
		{"as-is", packNone, false},
		{"bz2", "", true},
		{"junk", "", true},
	}
	for _, tc := range cases {
		got, err := resolvePackMode(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("resolvePackMode(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolvePackMode(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("resolvePackMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- checkFolder ------------------------------------------------------------

// writeBytes writes a file under dir, creating parents.
func writeBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newHashcache(t *testing.T) *hashcache.Cache {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "hc.db")
	c, err := hashcache.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestCheckFolderClean(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "scripts")
	writeBytes(t, filepath.Join(data, "a"), []byte("hello"))
	writeBytes(t, filepath.Join(data, "b"), []byte("world"))

	cache := newHashcache(t)
	recursive, err := folder.RecursiveSHA256(data, cache.Sum)
	if err != nil {
		t.Fatal(err)
	}

	doc := &sidecar.Doc{
		Kind: "folder",
		Folder: &sidecar.FolderInfo{
			RecursiveSha256: recursive,
		},
	}
	scPath := data + ".mus"

	res := checkFolder(cache, scPath, data, doc)
	if res.status != "ok" {
		t.Errorf("checkFolder clean = %q (%s), want ok", res.status, res.detail)
	}
}

func TestCheckFolderDrift(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "scripts")
	writeBytes(t, filepath.Join(data, "a"), []byte("hello"))

	doc := &sidecar.Doc{
		Kind: "folder",
		Folder: &sidecar.FolderInfo{
			RecursiveSha256: "0000000000000000000000000000000000000000000000000000000000000000",
		},
	}
	scPath := data + ".mus"
	res := checkFolder(newHashcache(t), scPath, data, doc)
	if res.status != "mismatch" {
		t.Errorf("status = %q, want mismatch (detail: %s)", res.status, res.detail)
	}
	if !strings.Contains(res.detail, "Merkle drift") {
		t.Errorf("detail = %q, want Merkle drift", res.detail)
	}
}

func TestCheckFolderSourceMissingArchivePresent(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "scripts.tar.gz")
	payload := []byte("not-really-a-tar-but-fine-for-the-sum")
	writeBytes(t, archive, payload)

	cache := newHashcache(t)
	archiveSum, err := cache.Sum(archive)
	if err != nil {
		t.Fatal(err)
	}

	scPath := filepath.Join(dir, "scripts.mus")
	dataPath := filepath.Join(dir, "scripts") // not present

	doc := &sidecar.Doc{
		Kind: "folder",
		Folder: &sidecar.FolderInfo{
			RecursiveSha256: "deadbeef",
		},
		Archive: &sidecar.ArchiveInfo{
			Filename: "scripts.tar.gz",
			Sha256:   archiveSum,
		},
	}
	res := checkFolder(cache, scPath, dataPath, doc)
	if res.status != "ok" {
		t.Errorf("source-missing/archive-present status = %q (%s), want ok",
			res.status, res.detail)
	}
}

func TestCheckFolderBothMissing(t *testing.T) {
	dir := t.TempDir()
	doc := &sidecar.Doc{
		Kind: "folder",
		Folder: &sidecar.FolderInfo{
			RecursiveSha256: "deadbeef",
		},
	}
	scPath := filepath.Join(dir, "scripts.mus")
	dataPath := filepath.Join(dir, "scripts")

	res := checkFolder(newHashcache(t), scPath, dataPath, doc)
	if res.status != "missing" {
		t.Errorf("status = %q, want missing (detail: %s)", res.status, res.detail)
	}
}

func TestCheckFolderNoRecursiveSha256(t *testing.T) {
	doc := &sidecar.Doc{
		Kind:   "folder",
		Folder: &sidecar.FolderInfo{},
	}
	res := checkFolder(newHashcache(t), "/x.mus", "/x", doc)
	if res.status != "stale" {
		t.Errorf("status = %q, want stale", res.status)
	}
}

// --- localStateVsSidecar via checkOne ---------------------------------------

func TestLocalStateVsSidecarAbsent(t *testing.T) {
	dir := t.TempDir()
	scPath := filepath.Join(dir, "data.csv.mus")
	doc := &sidecar.Doc{File: sidecar.FileInfo{Sha256: "abc", Size: 5,
		Mtime: time.Now().UTC().Truncate(time.Second)}}
	if err := sidecar.Write(scPath, doc); err != nil {
		t.Fatal(err)
	}

	state, err := localStateVsSidecar(scPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state != localAbsent {
		t.Errorf("state = %v, want localAbsent", state)
	}
}

func TestLocalStateVsSidecarMatch(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data.csv")
	payload := []byte("hello mus")
	writeBytes(t, data, payload)

	cache := newHashcache(t)
	sum, _ := cache.Sum(data)
	st, _ := os.Stat(data)

	scPath := data + ".mus"
	doc := &sidecar.Doc{File: sidecar.FileInfo{
		Sha256: sum, Size: st.Size(),
		Mtime: st.ModTime().UTC().Truncate(time.Second),
	}}
	if err := sidecar.Write(scPath, doc); err != nil {
		t.Fatal(err)
	}

	state, err := localStateVsSidecar(scPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state != localMatch {
		t.Errorf("state = %v, want localMatch", state)
	}
}

func TestLocalStateVsSidecarMismatch(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data.csv")
	writeBytes(t, data, []byte("hello"))
	st, _ := os.Stat(data)

	scPath := data + ".mus"
	doc := &sidecar.Doc{File: sidecar.FileInfo{
		Sha256: "deadbeef-not-the-real-sum",
		Size:   st.Size(),
		Mtime:  st.ModTime().UTC().Truncate(time.Second),
	}}
	if err := sidecar.Write(scPath, doc); err != nil {
		t.Fatal(err)
	}

	state, err := localStateVsSidecar(scPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state != localMismatch {
		t.Errorf("state = %v, want localMismatch", state)
	}
}

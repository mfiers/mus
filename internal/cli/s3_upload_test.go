package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestS3KeyFor_AllShapes(t *testing.T) {
	cases := []struct {
		name      string
		dp        string
		subfolder string
		basename  string
		want      string
	}{
		{"root", "Fiers2025", "", "data.h5ad",
			"__incoming/Fiers2025/data.h5ad"},
		{"static", "Fiers2025", "static", "logo.png",
			"__incoming/Fiers2025/static/logo.png"},
		{"custom-subfolder", "Fiers2025", "rev2", "data.h5ad",
			"__incoming/Fiers2025/rev2/data.h5ad"},
		{"collapsed-sidecar-root", "X2025", "", "data.h5ad.mus",
			"__incoming/X2025/data.h5ad.mus"},
		{"collapsed-sidecar-in-subfolder", "X2025", "rev2", "data.h5ad.mus",
			"__incoming/X2025/rev2/data.h5ad.mus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s3KeyFor(tc.dp, tc.subfolder, tc.basename)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestS3KeyForDE(t *testing.T) {
	cases := []struct {
		name, dp, deFolder, basename, want string
	}{
		{"basic", "Fiers2025", "main_model", "results.tsv",
			"__incoming/Fiers2025/de/main_model/results.tsv"},
		{"gz", "Fiers2025", "sex_strat", "results.tsv.gz",
			"__incoming/Fiers2025/de/sex_strat/results.tsv.gz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s3KeyForDE(tc.dp, tc.deFolder, tc.basename)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestS3KeyForDE_PanicsOnPathSeparators(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on path in de_folder")
		}
	}()
	_ = s3KeyForDE("F25", "a/b", "x.tsv")
}

func TestClassifyFileExt(t *testing.T) {
	cases := []struct {
		path string
		want uploadKind
		ok   bool
	}{
		{"foo.h5ad", uploadKindH5ad, true},
		{"foo.tsv", uploadKindDE, true},
		{"foo.csv", uploadKindDE, true},
		{"foo.tsv.gz", uploadKindDE, true},
		{"foo.csv.gz", uploadKindDE, true},
		{"foo.TSV.GZ", uploadKindDE, true},
		{"foo.parquet", 0, false},
		{"foo.txt", 0, false},
		{"foo", 0, false},
	}
	for _, tc := range cases {
		got, ok := classifyFileExt(tc.path)
		if ok != tc.ok || got != tc.want {
			t.Errorf("classifyFileExt(%q) = (%v,%v), want (%v,%v)",
				tc.path, got, ok, tc.want, tc.ok)
		}
	}
}

func TestClassifyUploadArgs_FileArgs(t *testing.T) {
	dir := t.TempDir()
	h5ad := filepath.Join(dir, "data.h5ad")
	tsv := filepath.Join(dir, "results.tsv")
	for _, p := range []string{h5ad, tsv} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := classifyUploadArgs([]string{h5ad, tsv})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries; got %d: %+v", len(got), got)
	}
	if got[0].Kind != uploadKindH5ad || got[1].Kind != uploadKindDE {
		t.Errorf("kinds: got %+v", got)
	}
	// Parent dir basename → t.TempDir()'s last segment.
	if got[1].ParentDir != filepath.Base(dir) {
		t.Errorf("DE ParentDir: got %q, want %q", got[1].ParentDir, filepath.Base(dir))
	}
}

func TestClassifyUploadArgs_FolderArg(t *testing.T) {
	root := t.TempDir()
	// Mix: h5ad at root, DE inside de/, ignore .mus sidecars.
	if err := os.WriteFile(filepath.Join(root, "data.h5ad"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.h5ad.mus"), []byte("sha=x"), 0o644); err != nil {
		t.Fatal(err)
	}
	deDir := filepath.Join(root, "de", "main_model")
	if err := os.MkdirAll(deDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deDir, "results.tsv"), []byte("h\nr\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := classifyUploadArgs([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries (1 h5ad + 1 DE; sidecar skipped); got %d: %+v", len(got), got)
	}
	kinds := map[uploadKind]int{}
	for _, e := range got {
		kinds[e.Kind]++
	}
	if kinds[uploadKindH5ad] != 1 || kinds[uploadKindDE] != 1 {
		t.Errorf("kind tally: %+v; want 1 h5ad + 1 DE", kinds)
	}
	// DE ParentDir should be the immediate folder ("main_model").
	for _, e := range got {
		if e.Kind == uploadKindDE && e.ParentDir != "main_model" {
			t.Errorf("DE ParentDir: got %q, want main_model", e.ParentDir)
		}
	}
}

func TestClassifyUploadArgs_RejectsUnknownExt(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "data.parquet")
	if err := os.WriteFile(bad, []byte("PAR1"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := classifyUploadArgs([]string{bad})
	if err == nil {
		t.Error("expected error on .parquet positional")
	}
	if !strings.Contains(err.Error(), "unsupported file extension") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestClassifyUploadArgs_EmptyFolderErrors(t *testing.T) {
	dir := t.TempDir() // no .h5ad / .tsv / .csv inside
	_, err := classifyUploadArgs([]string{dir})
	if err == nil {
		t.Error("expected error on folder with no supported files")
	}
}

func TestS3KeysFor(t *testing.T) {
	cases := []struct {
		ent           uploadEntry
		dp, sub       string
		wantData      string
		wantSidecar   string
		errExpected   bool
	}{
		{
			ent:         uploadEntry{AbsPath: "/p/d.h5ad", Kind: uploadKindH5ad, ParentDir: "p"},
			dp:          "F25",
			wantData:    "__incoming/F25/d.h5ad",
			wantSidecar: "__incoming/F25/d.h5ad.mus",
		},
		{
			ent:         uploadEntry{AbsPath: "/p/d.h5ad", Kind: uploadKindH5ad, ParentDir: "p"},
			dp:          "F25",
			sub:         "rev2",
			wantData:    "__incoming/F25/rev2/d.h5ad",
			wantSidecar: "__incoming/F25/rev2/d.h5ad.mus",
		},
		{
			ent:         uploadEntry{AbsPath: "/p/de/main_model/r.tsv", Kind: uploadKindDE, ParentDir: "main_model"},
			dp:          "F25",
			wantData:    "__incoming/F25/de/main_model/r.tsv",
			wantSidecar: "__incoming/F25/de/main_model/r.tsv.mus",
		},
	}
	for i, tc := range cases {
		d, s, err := s3KeysFor(tc.ent, tc.dp, tc.sub)
		if tc.errExpected {
			if err == nil {
				t.Errorf("case %d: expected error", i)
			}
			continue
		}
		if err != nil {
			t.Errorf("case %d: %v", i, err)
			continue
		}
		if d != tc.wantData || s != tc.wantSidecar {
			t.Errorf("case %d: got (%q,%q), want (%q,%q)", i, d, s, tc.wantData, tc.wantSidecar)
		}
	}
}

func TestS3KeyFor_PanicsOnPathSeparatorInSubfolder(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic; got nil")
		}
	}()
	_ = s3KeyFor("X2025", "deep/sub", "f.txt")
}

func TestS3KeyFor_PanicsOnNonBasename(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic; got nil")
		}
	}()
	_ = s3KeyFor("X2025", "static", "sub/dir/file.txt")
}

func TestValidateSubfolder(t *testing.T) {
	ok := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		{"rev2", "rev2"},
		{"  draft  ", "draft"},
		{"v1.0", "v1.0"},
	}
	for _, tc := range ok {
		got, err := validateSubfolder(tc.in)
		if err != nil {
			t.Errorf("validateSubfolder(%q) errored: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("validateSubfolder(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	bad := []struct{ in, mustContain string }{
		{"a/b", "single path component"},
		{"a\\b", "single path component"},
		{".", "'.'"},
		{"..", "'..'"},
		{"static", "reserved"},
		{"  static  ", "reserved"},
	}
	for _, tc := range bad {
		_, err := validateSubfolder(tc.in)
		if err == nil {
			t.Errorf("validateSubfolder(%q) should have errored", tc.in)
			continue
		}
		if !strings.Contains(err.Error(), tc.mustContain) {
			t.Errorf("validateSubfolder(%q) error missing %q: %s",
				tc.in, tc.mustContain, err.Error())
		}
	}
}

func TestValidateStaticFiles_AcceptsRegularFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "logo.png")
	b := filepath.Join(dir, "notes.pdf")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateStaticFiles([]string{a, b}); err != nil {
		t.Errorf("rejected valid static files: %v", err)
	}
	if err := validateStaticFiles(nil); err != nil {
		t.Errorf("rejected empty slice: %v", err)
	}
}

func TestValidateStaticFiles_RejectsMissing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.png")
	err := validateStaticFiles([]string{missing})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestValidateStaticFiles_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	err := validateStaticFiles([]string{dir})
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("expected directory-rejection error, got %v", err)
	}
}

func TestValidateStaticFiles_RejectsDuplicateBasename(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	a := filepath.Join(dir1, "logo.png")
	b := filepath.Join(dir2, "logo.png")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := validateStaticFiles([]string{a, b})
	if err == nil {
		t.Fatal("expected duplicate-basename rejection")
	}
	for _, want := range []string{"collision", "logo.png", "static"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %s", want, err.Error())
		}
	}
}

func TestBuildCollapsedSidecar_MergesCascadeAndSidecar(t *testing.T) {
	dir := t.TempDir()
	// Folder-level mus.cfg sets data_project + organism + a list field.
	if err := os.WriteFile(filepath.Join(dir, "mus.cfg"),
		[]byte("data_project=Fiers2025\norganism=human\ntissue=brain,cortex\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	// Primary + sidecar. Sidecar overrides organism and adds an
	// s3 block.
	h5ad := filepath.Join(dir, "carlo.h5ad")
	if err := os.WriteFile(h5ad, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecarBody := strings.Join([]string{
		"sha256=abc",
		"size=12345",
		"organism=mouse",
		"s3_bucket=anthive4a",
		"s3_key=__incoming/Fiers2025/carlo.h5ad",
		"s3_etag=etag123",
		"",
	}, "\n")
	if err := os.WriteFile(h5ad+".mus", []byte(sidecarBody), 0o644); err != nil {
		t.Fatal(err)
	}

	collapsed, err := buildCollapsedSidecar(h5ad)
	if err != nil {
		t.Fatal(err)
	}
	text := string(collapsed)

	// Sidecar wins for scalars.
	if !strings.Contains(text, "organism=mouse") {
		t.Errorf("sidecar override missing: %s", text)
	}
	// Cascade-only keys preserved.
	if !strings.Contains(text, "data_project=Fiers2025") {
		t.Errorf("data_project missing: %s", text)
	}
	// Sidecar S3 block present.
	if !strings.Contains(text, "s3_etag=etag123") {
		t.Errorf("s3 block missing: %s", text)
	}
	// File-level sha256 from sidecar present.
	if !strings.Contains(text, "sha256=abc") {
		t.Errorf("file sha256 missing: %s", text)
	}
	// List-typed key from cascade present (any order, comma-joined).
	if !strings.Contains(text, "tissue=") {
		t.Errorf("tissue list missing: %s", text)
	}
}

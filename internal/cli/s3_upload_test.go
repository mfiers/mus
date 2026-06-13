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
		{"root", "Fiers2025", "", "data.h5ad", "Fiers2025/data.h5ad"},
		{"static", "Fiers2025", "static", "logo.png", "Fiers2025/static/logo.png"},
		{"custom-subfolder", "Fiers2025", "rev2", "data.h5ad", "Fiers2025/rev2/data.h5ad"},
		{"archive-root", "X2025", "", "mus_metadata_2026-06-13T10-12-00Z.tar.gz",
			"X2025/mus_metadata_2026-06-13T10-12-00Z.tar.gz"},
		{"archive-in-subfolder", "X2025", "rev2", "mus_metadata_2026-06-13T10-12-00Z.tar.gz",
			"X2025/rev2/mus_metadata_2026-06-13T10-12-00Z.tar.gz"},
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

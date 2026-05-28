package cli

import (
	"strings"
	"testing"
)

func TestBuildELNCommentTitle(t *testing.T) {
	cases := []struct {
		name  string
		items []uploadedItem
		want  string
	}{
		{
			name:  "single file",
			items: []uploadedItem{{Kind: "file"}},
			want:  "mus: uploaded 1 file to iRODS",
		},
		{
			name:  "two files",
			items: []uploadedItem{{Kind: "file"}, {Kind: "file"}},
			want:  "mus: uploaded 2 files to iRODS",
		},
		{
			name:  "mixed",
			items: []uploadedItem{{Kind: "file"}, {Kind: "folder"}, {Kind: "archive"}, {Kind: "archive"}},
			want:  "mus: uploaded 1 file, 1 folder, 2 archives to iRODS",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildELNCommentTitle(tc.items); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestBuildELNCommentHTMLContainsItem(t *testing.T) {
	items := []uploadedItem{
		{
			Kind:       "file",
			LocalPath:  "/data/raw/sample.csv",
			RemotePath: "/zone/home/lab/project/Fiers2025/exp/sample.csv",
			URL:        "https://example/data-object/view/zone/home/lab/project/Fiers2025/exp/sample.csv",
			PURL:       "https://example/PID/zone/12345/",
			Size:       2048,
		},
	}
	out := buildELNCommentHTML(items)
	for _, want := range []string{
		"/data/raw/sample.csv",
		"/zone/home/lab/project/Fiers2025/exp/sample.csv",
		"https://example/PID/zone/12345/",
		"PURL",
		"<table",
		"<code>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("HTML missing %q\nfull HTML:\n%s", want, out)
		}
	}
}

func TestBuildELNCommentHTMLEscapesAngleBrackets(t *testing.T) {
	items := []uploadedItem{
		{Kind: "file", LocalPath: "/x/<script>alert(1)</script>"},
	}
	out := buildELNCommentHTML(items)
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatalf("HTML did not escape user-supplied angle brackets:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("expected escaped form in output:\n%s", out)
	}
}

func TestBuildELNCommentHTMLBrowseFallback(t *testing.T) {
	items := []uploadedItem{
		{Kind: "folder", LocalPath: "/x", RemotePath: "/r", URL: "https://example/browse", PURL: ""},
	}
	out := buildELNCommentHTML(items)
	if !strings.Contains(out, "https://example/browse") {
		t.Errorf("expected URL fallback link, got:\n%s", out)
	}
	if !strings.Contains(out, ">browse</a>") {
		t.Errorf("expected browse link label, got:\n%s", out)
	}
}

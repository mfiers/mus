package cli

import (
	"strings"
	"testing"

	"codeberg.org/mfiers/mus/internal/config"
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

// emptyEnv returns a zero-state config.Env for tests that don't need cascade
// data — the HTML builder reads data_project / experiment name from it.
func emptyEnv(t *testing.T) *config.Env {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp+"/xdg")
	env, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("load empty env: %v", err)
	}
	return env
}

func TestBuildELNCommentHTML_PURLLinkedPath(t *testing.T) {
	items := []uploadedItem{{
		Kind:       "file",
		LocalPath:  "/data/raw/sample.csv",
		RemotePath: "/zone/home/lab/project/Fiers2025/exp/sample.csv",
		URL:        "https://example/data-object/view/zone/home/lab/project/Fiers2025/exp/sample.csv",
		PURL:       "https://example/PID/zone/12345/",
		Sha256:     "abcdef0123456789abcdef0123456789",
		Size:       2048,
	}}
	out := buildELNCommentHTML(emptyEnv(t), items)

	// Basename is the link text; PURL is the href; full path lives in title=.
	wantAnchor := `<a href="https://example/PID/zone/12345/" title="/zone/home/lab/project/Fiers2025/exp/sample.csv"><code>sample.csv</code></a>`
	if !strings.Contains(out, wantAnchor) {
		t.Errorf("missing PURL-linked basename anchor.\nwant substring:\n  %s\nfull HTML:\n%s",
			wantAnchor, out)
	}
	// Sha column shows the short form (first 12), with full hash in title=.
	if !strings.Contains(out, `title="abcdef0123456789abcdef0123456789"`) {
		t.Errorf("sha title attribute (full hash) missing in:\n%s", out)
	}
	if !strings.Contains(out, ">abcdef012345<") {
		t.Errorf("short-sha display missing in:\n%s", out)
	}
	// Dropped columns must NOT appear in headers.
	for _, dropped := range []string{">Local<", ">Link<"} {
		if strings.Contains(out, dropped) {
			t.Errorf("dropped column header %q still in output:\n%s", dropped, out)
		}
	}
}

func TestBuildELNCommentHTML_PathFallbackMarker(t *testing.T) {
	items := []uploadedItem{{
		Kind:       "folder",
		RemotePath: "/zone/home/lab/folder",
		URL:        "https://example/browse",
		PURL:       "",
	}}
	out := buildELNCommentHTML(emptyEnv(t), items)
	if !strings.Contains(out, `<a href="https://example/browse" title="/zone/home/lab/folder"><code>folder</code></a> *`) {
		t.Errorf("expected URL-fallback basename anchor with ` *` marker, got:\n%s", out)
	}
	if !strings.Contains(out, "path-based link only") {
		t.Errorf("expected footnote explaining fallback, got:\n%s", out)
	}
}

func TestBuildELNCommentHTML_NoFallbackFootnoteWhenAllPURL(t *testing.T) {
	items := []uploadedItem{{Kind: "file", RemotePath: "/r", PURL: "https://example/PID/x/1/"}}
	out := buildELNCommentHTML(emptyEnv(t), items)
	if strings.Contains(out, "path-based link only") {
		t.Errorf("fallback footnote should be absent when every item has a PURL:\n%s", out)
	}
}

func TestBuildELNCommentHTML_EscapesAngleBrackets(t *testing.T) {
	items := []uploadedItem{{Kind: "file", RemotePath: "/x/<script>alert(1)</script>", PURL: "https://example/PID/x/1/"}}
	out := buildELNCommentHTML(emptyEnv(t), items)
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatalf("HTML did not escape user-supplied angle brackets:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("expected escaped form in output:\n%s", out)
	}
}

func TestBuildELNCommentHTML_ContentsColumnFolderArchive(t *testing.T) {
	items := []uploadedItem{
		{Kind: "file", RemotePath: "/r/a.csv", PURL: "https://example/PID/x/1/"},
		{Kind: "folder", RemotePath: "/r/scripts", PURL: "https://example/PID/x/2/", FileCount: 23, DirCount: 4},
		{Kind: "archive", RemotePath: "/r/data.tar.gz", PURL: "https://example/PID/x/3/", FileCount: 1000, DirCount: 0},
	}
	out := buildELNCommentHTML(emptyEnv(t), items)
	if !strings.Contains(out, "<th>Contents</th>") {
		t.Errorf("expected Contents column header, got:\n%s", out)
	}
	// folder row: 23 files + 4 subdirs
	if !strings.Contains(out, "23 files, 4 subdirs") {
		t.Errorf("folder contents cell missing 23 files / 4 subdirs:\n%s", out)
	}
	// archive row: 1000 files, no subdirs → DirCount=0 should be omitted
	if !strings.Contains(out, "1000 files") {
		t.Errorf("archive contents cell missing 1000 files:\n%s", out)
	}
	if strings.Contains(out, "0 subdirs") {
		t.Errorf("zero DirCount should be omitted, got:\n%s", out)
	}
}

func TestBuildELNCommentHTML_NoContentsColumnForFileOnly(t *testing.T) {
	items := []uploadedItem{
		{Kind: "file", RemotePath: "/r/a.csv", PURL: "https://example/PID/x/1/"},
		{Kind: "file", RemotePath: "/r/b.csv", PURL: "https://example/PID/x/2/"},
	}
	out := buildELNCommentHTML(emptyEnv(t), items)
	if strings.Contains(out, "Contents") {
		t.Errorf("Contents column should be omitted on file-only batch:\n%s", out)
	}
}

func TestBuildELNCommentHTML_SummaryIncludesContextWhenSet(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp+"/xdg")
	if err := config.Save(tmp, map[string]string{
		"data_project":        "Fiers2025",
		"eln_experiment_name": "MyPilot",
	}); err != nil {
		t.Fatal(err)
	}
	env, err := config.Load(tmp)
	if err != nil {
		t.Fatal(err)
	}
	out := buildELNCommentHTML(env, []uploadedItem{{Kind: "file", RemotePath: "/r", PURL: "https://example/PID/x/1/"}})
	// data_project must appear; experiment must NOT (it's obvious from the
	// experiment page the comment lives on).
	for _, want := range []string{"data_project=", "Fiers2025"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q in:\n%s", want, out)
		}
	}
	for _, banned := range []string{"experiment=", "MyPilot"} {
		if strings.Contains(out, banned) {
			t.Errorf("summary should not mention experiment name (it's obvious from context); found %q in:\n%s", banned, out)
		}
	}
}

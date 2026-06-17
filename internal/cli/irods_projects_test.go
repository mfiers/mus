package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseIronListJSON_NDJSON(t *testing.T) {
	raw := strings.Join([]string{
		`{"name": "/gbiomed/home/BADS/project/Fiers2025", "type": "collection"}`,
		`{"name": "/gbiomed/home/BADS/project/DeWulf2024", "type": "collection"}`,
		`{"name": "/gbiomed/home/BADS/project/orphan.txt", "type": "data", "checksum": "sha2:abc"}`,
	}, "\n")
	entries, err := parseIronListJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries; got %d: %+v", len(entries), entries)
	}
	if entries[0].IsObject {
		t.Errorf("first entry should be a collection: %+v", entries[0])
	}
	if !entries[2].IsObject {
		t.Errorf("third entry should be a data object: %+v", entries[2])
	}
}

func TestParseIronListJSON_SingleArray(t *testing.T) {
	raw := `[
		{"name": "/p/Fiers2025"},
		{"name": "/p/DeWulf2024"}
	]`
	entries, err := parseIronListJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries; got %d", len(entries))
	}
}

func TestParseIronListJSON_GarbageReturnsError(t *testing.T) {
	if _, err := parseIronListJSON("this is not json"); err == nil {
		t.Error("expected error on non-JSON input")
	}
}

func TestParseIronListJSON_EmptyIsNoError(t *testing.T) {
	got, err := parseIronListJSON("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty input should yield no entries; got %v", got)
	}
}

func TestNewestModifiedInTreeJSON(t *testing.T) {
	raw := strings.Join([]string{
		`{"name": "/p/Fiers2025/exp1", "modified": "2025-01-01T00:00:00Z"}`,
		`{"name": "/p/Fiers2025/exp1/a.h5ad", "checksum": "x", "modified": "2025-06-10T12:00:00Z"}`,
		`{"name": "/p/Fiers2025/exp1/b.h5ad", "checksum": "y", "modified": "2026-06-14T08:00:00Z"}`,
		`{"name": "/p/Fiers2025/exp1/c.h5ad", "checksum": "z", "modified": "2025-12-31T23:59:00Z"}`,
	}, "\n")
	got := newestModifiedInTreeJSON(raw)
	if got != "2026-06-14" {
		t.Errorf("expected newest=2026-06-14, got %q", got)
	}
}

func TestNewestModifiedInTreeJSON_IgnoresCollections(t *testing.T) {
	// Collection (no checksum) with an even newer modified field
	// should NOT be picked — we only care about data objects.
	raw := strings.Join([]string{
		`{"name": "/p/Fiers2025/exp1", "modified": "2099-12-31T00:00:00Z"}`,
		`{"name": "/p/Fiers2025/exp1/a.h5ad", "checksum": "x", "modified": "2025-06-10T12:00:00Z"}`,
	}, "\n")
	got := newestModifiedInTreeJSON(raw)
	if got != "2025-06-10" {
		t.Errorf("collection mtime leaked in; got %q", got)
	}
}

func TestEscapeGenquerySingleQuotes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"/gbiomed/home/BADS/mus/project", "/gbiomed/home/BADS/mus/project"},
		{"O'Reilly", "O''Reilly"},
		{"a'b'c", "a''b''c"},
	}
	for _, tc := range cases {
		got := escapeGenquerySingleQuotes(tc.in)
		if got != tc.want {
			t.Errorf("escapeGenquerySingleQuotes(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWriteProjects_PlainList(t *testing.T) {
	var buf bytes.Buffer
	rows := []projectRow{
		{Name: "Fiers2025"},
		{Name: "DeWulf2024"},
		{Name: "Sun2024"},
	}
	writeProjects(&buf, rows, false)
	got := buf.String()
	want := "Fiers2025\nDeWulf2024\nSun2024\n"
	if got != want {
		t.Errorf("plain list:\n  got  %q\n  want %q", got, want)
	}
}

func TestWriteProjects_DetailsAligned(t *testing.T) {
	var buf bytes.Buffer
	rows := []projectRow{
		{Name: "Fiers2025", Owner: "u0089478", CreateTime: "2026-04-12",
			LastUpload: "2026-06-14", LocalFolder: "/data/1/users/mark/microglia (2026-06-14)"},
		{Name: "DeWulf2024", Owner: "u9999999", CreateTime: "2024-09-01",
			LastUpload: "2025-01-12", LocalFolder: ""},
	}
	writeProjects(&buf, rows, true)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 { // header + sep + 2 rows
		t.Fatalf("expected 4 lines; got %d:\n%s", len(lines), out)
	}
	for _, want := range []string{
		"name", "owner", "created", "last upload", "local folder",
		"Fiers2025", "u0089478", "DeWulf2024",
		"2026-06-14", "/data/1/users/mark/microglia",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("details output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteProjects_EmptyDetailsTable(t *testing.T) {
	var buf bytes.Buffer
	writeProjects(&buf, nil, true)
	out := buf.String()
	for _, want := range []string{"name", "owner", "created", "last upload", "local folder"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-table header missing %q:\n%s", want, out)
		}
	}
}

func TestFormatICATTime(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"2026-04-12T10:11:12Z", "2026-04-12"},
		{"2026-04-12T10:11:12", "2026-04-12"},
		{"1718000000", "2024-06-10"}, // unix seconds
		{"garbage", "garbage"},
	}
	for _, tc := range cases {
		got := formatICATTime(tc.in)
		if got != tc.want {
			t.Errorf("formatICATTime(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

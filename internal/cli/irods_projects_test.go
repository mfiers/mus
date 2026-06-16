package cli

import (
	"bytes"
	"strings"
	"testing"
)

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
		{Name: "Fiers2025", Owner: "u0089478", CreateTime: "2026-04-12T10:11:12"},
		{Name: "DeWulf2024", Owner: "u9999999", CreateTime: "2024-09-01T08:00:00"},
	}
	writeProjects(&buf, rows, true)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 { // header + sep + 2 rows
		t.Fatalf("expected 4 lines; got %d:\n%s", len(lines), out)
	}
	for _, want := range []string{"name", "owner", "created", "Fiers2025", "u0089478", "DeWulf2024"} {
		if !strings.Contains(out, want) {
			t.Errorf("details output missing %q:\n%s", want, out)
		}
	}
	// Header line shouldn't be longer than the body (loose alignment sanity check).
	if len(lines[0]) > len(lines[2])+10 {
		t.Errorf("header longer than body row — alignment looks wrong:\n%s", out)
	}
}

func TestWriteProjects_EmptyDetailsTable(t *testing.T) {
	var buf bytes.Buffer
	// writeProjects is only called with non-empty rows by runIRODSProjects,
	// but the function shouldn't panic on empty input either.
	writeProjects(&buf, nil, true)
	out := buf.String()
	// At least a header should render; concrete shape isn't load-bearing.
	if !strings.Contains(out, "name") || !strings.Contains(out, "owner") {
		t.Errorf("empty-table header missing:\n%s", out)
	}
}

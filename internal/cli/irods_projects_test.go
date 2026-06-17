package cli

import (
	"bytes"
	"path"
	"sort"
	"strings"
	"testing"
)

// dedupeProjectRowsForTest mirrors the dedup logic used by
// queryProjects on the parsed [][]string. Lets us unit-test the
// dedupe behaviour without needing an iron client.
func dedupeProjectRowsForTest(cells [][]string, details bool) []projectRow {
	seen := make(map[string]int, len(cells))
	rows := make([]projectRow, 0, len(cells))
	for _, row := range cells {
		if len(row) == 0 {
			continue
		}
		name := path.Base(row[0])
		if name == "" || name == "." || name == "/" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		entry := projectRow{Name: name}
		if details {
			if len(row) > 1 {
				entry.Owner = row[1]
			}
			if len(row) > 2 {
				entry.CreateTime = row[2]
			}
		}
		seen[name] = len(rows)
		rows = append(rows, entry)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

func TestQueryProjects_DedupesACLExplosion(t *testing.T) {
	// iCAT can return one row per (collection, owner) tuple — same
	// COLL_NAME repeated with different COLL_OWNER_NAME values.
	cells := [][]string{
		{"/gbiomed/home/BADS/mus/project/Fiers2025", "u0089478", "2026-04-12T10:11:12"},
		{"/gbiomed/home/BADS/mus/project/Fiers2025", "u9999999", "2026-04-12T10:11:12"},
		{"/gbiomed/home/BADS/mus/project/DeWulf2024", "u0089478", "2024-09-01T08:00:00"},
		{"/gbiomed/home/BADS/mus/project/Fiers2025", "groupBADS", "2026-04-12T10:11:12"},
	}
	got := dedupeProjectRowsForTest(cells, true)
	if len(got) != 2 {
		t.Fatalf("expected 2 distinct projects after dedupe; got %d: %+v", len(got), got)
	}
	want := []string{"DeWulf2024", "Fiers2025"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("row %d: got %q, want %q", i, got[i].Name, w)
		}
	}
	// First-seen-wins for owner: Fiers2025's first row had u0089478.
	for _, r := range got {
		if r.Name == "Fiers2025" && r.Owner != "u0089478" {
			t.Errorf("Fiers2025 should keep first-seen owner u0089478; got %q", r.Owner)
		}
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

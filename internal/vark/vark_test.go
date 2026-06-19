package vark

import (
	"testing"
)

var fixture = []Dataset{
	{ID: "F25/SGF", GroupID: "F25", FileID: "SGF", Title: "Single.Grafts",
		Path: "Fiers2025/Single.Grafts.duckdb"},
	{ID: "F25/CMR", GroupID: "F25", FileID: "CMR", Title: "Combined.Microglia",
		Path: "Fiers2025/Combined.Microglia.duckdb"},
	{ID: "F25/DAM", GroupID: "F25", FileID: "DAM", Title: "DAM.subset",
		Path: "Fiers2025/DAM.subset.duckdb"},
	{ID: "S25/M01", GroupID: "S25", FileID: "M01", Title: "Microglia.set1",
		Path: "Sierksma2025/Microglia.set1.duckdb"},
}

func TestDataset_DataProject(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"Fiers2025/foo.duckdb", "Fiers2025"},
		{"Sierksma2025/Microglia.set1.duckdb", "Sierksma2025"},
		{"nopath", ""},
		{"", ""},
	}
	for _, tc := range cases {
		d := Dataset{Path: tc.path}
		if got := d.DataProject(); got != tc.want {
			t.Errorf("Path=%q: got %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestFilterByDataProject(t *testing.T) {
	got := FilterByDataProject(fixture, "Fiers2025")
	if len(got) != 3 {
		t.Errorf("Fiers2025: got %d, want 3", len(got))
	}
	got = FilterByDataProject(fixture, "Sierksma2025")
	if len(got) != 1 || got[0].FileID != "M01" {
		t.Errorf("Sierksma2025: got %+v", got)
	}
	got = FilterByDataProject(fixture, "Missing2099")
	if len(got) != 0 {
		t.Errorf("Missing2099: expected empty, got %+v", got)
	}
	if got := FilterByDataProject(fixture, ""); got != nil {
		t.Errorf("empty group: expected nil, got %+v", got)
	}
}

func TestFindByFileID(t *testing.T) {
	scoped := FilterByDataProject(fixture, "Fiers2025")
	cases := []struct {
		in   string
		want string // wantFileID — "" means nil
	}{
		{"SGF", "SGF"},
		{"DAM", "DAM"},
		{"M01", ""}, // belongs to a different data_project, filtered out
		{"missing", ""},
		{"sgf", ""}, // case-sensitive
		{"", ""},
	}
	for _, tc := range cases {
		got := FindByFileID(scoped, tc.in)
		if tc.want == "" {
			if got != nil {
				t.Errorf("FindByFileID(%q): expected nil, got %+v", tc.in, got)
			}
			continue
		}
		if got == nil || got.FileID != tc.want {
			t.Errorf("FindByFileID(%q): got %+v, want file_id=%q", tc.in, got, tc.want)
		}
	}
}

func TestFindByH5ad(t *testing.T) {
	scoped := FilterByDataProject(fixture, "Fiers2025")
	cases := []struct {
		in   string
		want string
	}{
		{"Single.Grafts", "SGF"},
		{"Single.Grafts.h5ad", "SGF"}, // trailing .h5ad stripped
		{"DAM.subset", "DAM"},
		{"DAM.subset.h5ad", "DAM"},
		{"missing", ""},
		{"missing.h5ad", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := FindByH5ad(scoped, tc.in)
		if tc.want == "" {
			if got != nil {
				t.Errorf("FindByH5ad(%q): expected nil, got %+v", tc.in, got)
			}
			continue
		}
		if got == nil || got.FileID != tc.want {
			t.Errorf("FindByH5ad(%q): got %+v, want file_id=%q", tc.in, got, tc.want)
		}
	}
}

func TestAvailable_RobustToMissing(t *testing.T) {
	// vark is presumably NOT on PATH in CI. Just confirm Available()
	// doesn't panic regardless of presence.
	_ = Available()
}

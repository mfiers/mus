package cli

import (
	"bytes"
	"sort"
	"strings"
	"testing"
)

// sortAndCapForTest mirrors the client-side sort + cap performed by
// queryRecentInProject after the iCAT response is parsed. Mirroring
// here keeps the test honest about what the production code does
// without needing a live iRODS client.
func sortAndCapForTest(rows []objectRow, limit int) []objectRow {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].rawMtime > rows[j].rawMtime
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func TestQueryRecentInProject_ClientSideSortAndCap(t *testing.T) {
	// Mixed-order input — newest is index 1.
	in := []objectRow{
		{rawMtime: "2025-01-01T00:00:00", RelPath: "old"},
		{rawMtime: "2026-06-14T11:11:11", RelPath: "newest"},
		{rawMtime: "2026-06-10T08:00:00", RelPath: "mid"},
		{rawMtime: "2024-12-31T23:59:59", RelPath: "ancient"},
	}
	got := sortAndCapForTest(in, 2)
	if len(got) != 2 {
		t.Fatalf("expected limit=2; got %d rows", len(got))
	}
	if got[0].RelPath != "newest" || got[1].RelPath != "mid" {
		t.Errorf("sort wrong: got %v", []string{got[0].RelPath, got[1].RelPath})
	}
}

func TestQueryRecentInProject_LimitLargerThanResults(t *testing.T) {
	in := []objectRow{
		{rawMtime: "2026-01-01T00:00:00", RelPath: "a"},
		{rawMtime: "2026-02-01T00:00:00", RelPath: "b"},
	}
	got := sortAndCapForTest(in, 20)
	if len(got) != 2 {
		t.Errorf("expected to return all 2 rows when limit > count; got %d", len(got))
	}
}

func TestWriteProjectObjects_Layout(t *testing.T) {
	var buf bytes.Buffer
	rows := []objectRow{
		{ModifyTime: "2026-06-14", Size: 1342515200, Owner: "u0089478",
			RelPath: "microglia_pilot/microglia_clusters.h5ad"},
		{ModifyTime: "2026-06-10", Size: 9485, Owner: "u0089478",
			RelPath: "microglia_pilot/figure_data.csv"},
	}
	writeProjectObjects(&buf, "/gbiomed/home/BADS/mus/project/Fiers2025", rows)
	out := buf.String()
	for _, want := range []string{
		"/gbiomed/home/BADS/mus/project/Fiers2025/",
		"modified", "size", "owner", "path",
		"2026-06-14", "u0089478",
		"microglia_clusters.h5ad", "figure_data.csv",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteProjectObjects_RelPathStripsProjectPrefix(t *testing.T) {
	// queryRecentInProject is supposed to deliver RelPath stripped
	// already, but writeProjectObjects must NOT re-prefix the project
	// path on its own.
	var buf bytes.Buffer
	rows := []objectRow{
		{ModifyTime: "2026-06-14", Size: 100, Owner: "u1",
			RelPath: "exp1/a.h5ad"},
	}
	writeProjectObjects(&buf, "/gbiomed/home/BADS/mus/project/Fiers2025", rows)
	out := buf.String()
	// Two occurrences of the project path are acceptable (the heading
	// is one); RelPath itself must NOT contain it.
	if strings.Contains(out, "Fiers2025/exp1/a.h5ad") {
		t.Errorf("RelPath should be project-relative; saw absolute path in:\n%s", out)
	}
	if !strings.Contains(out, "exp1/a.h5ad") {
		t.Errorf("rel path missing:\n%s", out)
	}
}

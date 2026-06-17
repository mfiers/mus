package cli

import (
	"bytes"
	"strings"
	"testing"
)

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

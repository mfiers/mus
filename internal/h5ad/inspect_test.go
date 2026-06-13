package h5ad

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestInspectionReportRoundtrip(t *testing.T) {
	// Schema lock-in: synthetic JSON matching what mus_h5ad_inspector.py emits must
	// parse cleanly into InspectionReport, including the optional pointer
	// fields (Min/Max/IsInteger/IsSparse/TotalEstimate).
	raw := []byte(`{
  "schema_version": 1,
  "n_obs": 73888,
  "n_vars": 24940,
  "x": {
    "present": true,
    "dtype": "float32",
    "is_sparse": true,
    "examined": 10000000,
    "total_estimate": 125000000,
    "min": 0.0,
    "max": 6.78,
    "is_integer": false,
    "exit_reason": "max_examined",
    "shape": [73888, 24940]
  },
  "counts": {
    "present": true,
    "dtype": "float32",
    "is_sparse": true,
    "examined": 5212000,
    "total_estimate": 125000000,
    "min": 0.0,
    "max": 1024.0,
    "is_integer": true,
    "exit_reason": "matrix_exhausted",
    "shape": [73888, 24940]
  },
  "layers": ["counts"],
  "obsm": {"keys": ["X_pca", "X_umap"], "has_umap": true, "has_tsne": false},
  "obs_names_unique": true,
  "var_names_unique": true,
  "obs_columns": ["cell_type", "donor"],
  "var_columns": ["highly_variable"],
  "reserved_in_obs": [],
  "reserved_in_var": [],
  "high_card_obs": {"cell_barcode": 73888},
  "anndata_version": "0.10.7",
  "var_names_sample": ["AKT1", "MAP1A"]
}`)
	var report InspectionReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.NObs != 73888 || report.NVars != 24940 {
		t.Errorf("dims wrong: %+v", report)
	}
	if report.X.IsInteger == nil || *report.X.IsInteger != false {
		t.Errorf("x.is_integer not unmarshalled correctly: %+v", report.X)
	}
	if report.Counts.IsInteger == nil || *report.Counts.IsInteger != true {
		t.Errorf("counts.is_integer not unmarshalled correctly: %+v", report.Counts)
	}
	if !report.Obsm.HasUMAP {
		t.Errorf("obsm.has_umap = false")
	}
	if report.HighCardObs["cell_barcode"] != 73888 {
		t.Errorf("high_card_obs not roundtripped: %v", report.HighCardObs)
	}
}

func TestPythonInspectorMissingUV(t *testing.T) {
	// Force a binary name that's certainly not on PATH.
	p := &PythonInspector{UVBinary: "does-not-exist-mus-test"}
	_, err := p.Inspect(context.Background(), "/tmp/anything.h5ad")
	if err == nil {
		t.Fatal("expected error when uv is missing")
	}
	if !strings.Contains(err.Error(), "uv") {
		t.Errorf("error should mention uv, got: %v", err)
	}
}

func TestStubInspector(t *testing.T) {
	want := &InspectionReport{NObs: 100, NVars: 200}
	stub := &StubInspector{
		Reports: map[string]*InspectionReport{
			"/data/sample.h5ad": want,
		},
	}
	got, err := stub.Inspect(context.Background(), "/data/sample.h5ad")
	if err != nil {
		t.Fatal(err)
	}
	if got.NObs != 100 || got.NVars != 200 {
		t.Errorf("stub returned wrong report: %+v", got)
	}
	if _, err := stub.Inspect(context.Background(), "/data/not-registered.h5ad"); err == nil {
		t.Error("expected error for unregistered path")
	}
}

func TestEmbeddedScriptIsValidPython(t *testing.T) {
	// Sanity: the embedded script starts with the uv shebang and contains
	// the canonical `if __name__ == "__main__":` so it's runnable.
	body := string(inspectScript)
	if !strings.HasPrefix(body, "#!/usr/bin/env -S uv run --quiet --script") {
		t.Errorf("mus_h5ad_inspector.py missing the uv-run-script shebang")
	}
	if !strings.Contains(body, `if __name__ == "__main__":`) {
		t.Errorf("mus_h5ad_inspector.py missing the standard __main__ guard")
	}
	if !strings.Contains(body, "anndata>=0.10") {
		t.Errorf("mus_h5ad_inspector.py missing anndata dep declaration in the script block")
	}
}

func TestParseInspectorErrorJSON_FindsLastJSONLine(t *testing.T) {
	// Real-world shape: progress lines + JSON blob.
	buf := []byte("[mus h5ad] opening /tmp/a.h5ad in backed='r' mode…\n" +
		`{"error": "Unable to synchronously open file (truncated file: eof = 112205824, sblock->base_addr = 0, stored_eof = 4510700254)", "type": "OSError"}` + "\n")
	info := parseInspectorErrorJSON(buf)
	if info == nil {
		t.Fatal("expected error info; got nil")
	}
	if info.Type != "OSError" {
		t.Errorf("type: got %q, want %q", info.Type, "OSError")
	}
	if !strings.Contains(info.Error, "truncated file") {
		t.Errorf("error: missing 'truncated file': %q", info.Error)
	}
}

func TestParseInspectorErrorJSON_ReturnsNilOnNoJSON(t *testing.T) {
	if info := parseInspectorErrorJSON([]byte("[mus h5ad] just progress\n")); info != nil {
		t.Errorf("expected nil; got %+v", info)
	}
	if info := parseInspectorErrorJSON(nil); info != nil {
		t.Errorf("expected nil for empty input; got %+v", info)
	}
}

func TestParseInspectorErrorJSON_SkipsMalformedJSON(t *testing.T) {
	buf := []byte("[mus h5ad] opening x\n{not json}\n{\"error\":\"\",\"type\":\"X\"}\n{\"error\":\"hit\",\"type\":\"Y\"}\n")
	info := parseInspectorErrorJSON(buf)
	if info == nil {
		t.Fatal("expected info")
	}
	if info.Error != "hit" || info.Type != "Y" {
		t.Errorf("picked wrong line: %+v", info)
	}
}

func TestProgressOnlyWriter_PassesThroughProgressAndDropsJSON(t *testing.T) {
	var out strings.Builder
	w := &progressOnlyWriter{out: &out}
	chunks := []string{
		"[mus h5ad] opening /tmp/a.h5ad…\n",
		`{"error": "truncated", "type": "OSError"}` + "\n",
		"[mus h5ad] aborting\n",
	}
	for _, c := range chunks {
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	got := out.String()
	if !strings.Contains(got, "opening /tmp/a.h5ad") {
		t.Errorf("progress line missing: %q", got)
	}
	if !strings.Contains(got, "aborting") {
		t.Errorf("trailing progress missing: %q", got)
	}
	if strings.Contains(got, "truncated") || strings.Contains(got, "OSError") {
		t.Errorf("JSON leaked into live stream: %q", got)
	}
}

func TestProgressOnlyWriter_BuffersPartialLines(t *testing.T) {
	var out strings.Builder
	w := &progressOnlyWriter{out: &out}
	// Split a progress line across two Write calls.
	if _, err := w.Write([]byte("[mus h5ad] partial ")); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("unterminated line should not have flushed: %q", out.String())
	}
	if _, err := w.Write([]byte("rest\n")); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "[mus h5ad] partial rest\n" {
		t.Errorf("got %q", got)
	}
}

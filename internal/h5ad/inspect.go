package h5ad

// Go wrapper around the embedded `inspect.py` h5ad inspector.
//
// The script (inspect.py) is the only thing in mus that needs Python +
// anndata at runtime. We embed it via go:embed, write it to a temp
// file at first use, run it via `uv run --script`, and parse the JSON
// report on stdout.
//
// Tests use the `Inspector` interface with a `StubInspector` so the
// rest of the validator (check.go) can be tested without Python.

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

//go:embed inspect.py
var inspectScript []byte

// MatrixReport is the per-matrix subreport for X or layers["counts"].
type MatrixReport struct {
	Present       bool     `json:"present"`
	Dtype         string   `json:"dtype"`
	IsSparse      *bool    `json:"is_sparse"`
	Examined      int64    `json:"examined"`
	TotalEstimate *int64   `json:"total_estimate"`
	Min           *float64 `json:"min"`
	Max           *float64 `json:"max"`
	IsInteger     *bool    `json:"is_integer"`
	ExitReason    string   `json:"exit_reason"`
	Shape         []int64  `json:"shape"`
}

// ObsmReport summarises the obsm slot (embeddings).
type ObsmReport struct {
	Keys     []string `json:"keys"`
	HasUMAP  bool     `json:"has_umap"`
	HasTSNE  bool     `json:"has_tsne"`
}

// InspectionReport is the JSON contract between inspect.py and the Go side.
type InspectionReport struct {
	SchemaVersion   int              `json:"schema_version"`
	NObs            int64            `json:"n_obs"`
	NVars           int64            `json:"n_vars"`
	X               MatrixReport     `json:"x"`
	Counts          MatrixReport     `json:"counts"`
	Layers          []string         `json:"layers"`
	Obsm            ObsmReport       `json:"obsm"`
	ObsNamesUnique  bool             `json:"obs_names_unique"`
	VarNamesUnique  bool             `json:"var_names_unique"`
	ObsColumns      []string         `json:"obs_columns"`
	VarColumns      []string         `json:"var_columns"`
	ReservedInObs   []string         `json:"reserved_in_obs"`
	ReservedInVar   []string         `json:"reserved_in_var"`
	HighCardObs     map[string]int64 `json:"high_card_obs"`
	AnnDataVersion  string           `json:"anndata_version"`
	VarNamesSample  []string         `json:"var_names_sample"`
}

// Inspector is the production-vs-test seam. The real implementation
// shells out to Python; tests inject a StubInspector returning canned
// reports.
type Inspector interface {
	Inspect(ctx context.Context, h5adPath string) (*InspectionReport, error)
}

// --- Python inspector -------------------------------------------------------

// PythonInspector runs the embedded inspect.py via `uv run --script`.
// First call lazily extracts the script to a temp file kept for the
// process lifetime.
type PythonInspector struct {
	UVBinary string // "" → resolved via PATH lookup ("uv")

	mu         sync.Mutex
	scriptPath string
}

// NewPythonInspector returns a ready-to-use inspector with default config.
func NewPythonInspector() *PythonInspector {
	return &PythonInspector{UVBinary: "uv"}
}

// ensureScript writes the embedded script to a temp file on first use.
// Subsequent calls are no-ops. The file is removed when the OS cleans
// up the temp dir; we don't aggressively delete to keep `uv`'s package
// cache warm across invocations within a single mus process.
func (p *PythonInspector) ensureScript() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.scriptPath != "" {
		if _, err := os.Stat(p.scriptPath); err == nil {
			return p.scriptPath, nil
		}
	}
	dir, err := os.MkdirTemp("", "mus-h5ad-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "inspect.py")
	if err := os.WriteFile(path, inspectScript, 0o755); err != nil {
		return "", err
	}
	p.scriptPath = path
	return path, nil
}

// Inspect runs the Python helper against h5adPath and parses the JSON
// report. Non-zero exit is converted to an error containing the stderr
// JSON body if it parses, else the raw text.
func (p *PythonInspector) Inspect(ctx context.Context, h5adPath string) (*InspectionReport, error) {
	script, err := p.ensureScript()
	if err != nil {
		return nil, fmt.Errorf("stage inspector script: %w", err)
	}
	uv := p.UVBinary
	if uv == "" {
		uv = "uv"
	}
	if _, err := exec.LookPath(uv); err != nil {
		return nil, fmt.Errorf("`uv` not found on PATH. Install from https://docs.astral.sh/uv/ — mus s3 inspect needs it to run the embedded h5ad reader")
	}
	cmd := exec.CommandContext(ctx, uv, "run", "--quiet", "--script", script, h5adPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Try to parse the stderr JSON.
			var errInfo struct {
				Error string `json:"error"`
				Type  string `json:"type"`
			}
			if jsonErr := json.Unmarshal(stderr.Bytes(), &errInfo); jsonErr == nil && errInfo.Error != "" {
				return nil, fmt.Errorf("h5ad inspect failed (%s): %s",
					errInfo.Type, errInfo.Error)
			}
			return nil, fmt.Errorf("h5ad inspect failed (exit %d): %s",
				exitErr.ExitCode(), bytes.TrimSpace(stderr.Bytes()))
		}
		return nil, fmt.Errorf("h5ad inspect run: %w", err)
	}
	report := &InspectionReport{}
	if err := json.Unmarshal(stdout.Bytes(), report); err != nil {
		return nil, fmt.Errorf("parse inspector JSON: %w\nstdout: %s\nstderr: %s",
			err, stdout.String(), stderr.String())
	}
	if report.SchemaVersion != 1 {
		return nil, fmt.Errorf("inspector schema_version mismatch: got %d, want 1",
			report.SchemaVersion)
	}
	return report, nil
}

// --- Stub inspector for tests ----------------------------------------------

// StubInspector returns pre-canned reports keyed by absolute h5ad
// path. Use in tests to drive check.go without needing Python.
type StubInspector struct {
	Reports map[string]*InspectionReport
	Errors  map[string]error
}

func (s *StubInspector) Inspect(ctx context.Context, h5adPath string) (*InspectionReport, error) {
	abs, _ := filepath.Abs(h5adPath)
	if err, ok := s.Errors[abs]; ok {
		return nil, err
	}
	if r, ok := s.Reports[abs]; ok {
		return r, nil
	}
	// Also accept the unmodified path key for convenience.
	if err, ok := s.Errors[h5adPath]; ok {
		return nil, err
	}
	if r, ok := s.Reports[h5adPath]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("StubInspector: no report registered for %q", h5adPath)
}

package h5ad

// obsvalues.go — Go wrapper around the mus_h5ad_obs_values.py
// helper. Returns the sorted unique values of one obs column in
// a target h5ad, used by `mus de check` to verify that a
// depositor's `de_cluster` value actually appears in the cluster
// column they declared.
//
// Independent of the structural inspector (inspect.go). The
// helper is fast — opens the file in backed='r' mode, reads one
// column's uniques, exits. No matrix streaming.

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed mus_h5ad_obs_values.py
var obsValuesScript []byte

// ObsValues runs the obs-values helper against `h5adPath` and
// returns the sorted unique stringified values of obs[col].
//
// Errors:
//   - missing `uv` on PATH (`uv` is the script runner mus relies on)
//   - file unreadable / unsupported h5ad version
//   - obs column missing in the file (typed as ErrObsColumnMissing)
//
// progressOut, when non-nil, receives the "[mus h5ad obs] …" lines
// the helper emits to stderr. Pass io.Discard to silence.
func ObsValues(ctx context.Context, h5adPath, col string, progressOut io.Writer) ([]string, error) {
	if h5adPath == "" {
		return nil, errors.New("ObsValues: empty h5ad path")
	}
	if col == "" {
		return nil, errors.New("ObsValues: empty obs column name")
	}

	// Stage the embedded script to a tempfile. uv reads its shebang
	// + dependency block from disk, so we need a real path.
	tmpDir, err := os.MkdirTemp("", "mus-h5ad-obs-*")
	if err != nil {
		return nil, fmt.Errorf("stage obs-values script: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	scriptPath := filepath.Join(tmpDir, "mus_h5ad_obs_values.py")
	if err := os.WriteFile(scriptPath, obsValuesScript, 0o755); err != nil {
		return nil, fmt.Errorf("stage obs-values script: %w", err)
	}

	uv, err := exec.LookPath("uv")
	if err != nil {
		return nil, fmt.Errorf("`uv` not found on PATH — needed to run the embedded h5ad obs-values helper (https://docs.astral.sh/uv/)")
	}

	cmd := exec.CommandContext(ctx, uv, "run", "--quiet", "--script", scriptPath, h5adPath, col)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if progressOut != nil {
		cmd.Stderr = io.MultiWriter(&stderr, progressOut)
	} else {
		cmd.Stderr = &stderr
	}

	if err := cmd.Run(); err != nil {
		// Helper emits a JSON error blob on stderr; parse to surface
		// the typed `KeyError` for missing-column.
		if info := parseInspectorErrorJSON(stderr.Bytes()); info != nil {
			if info.Type == "KeyError" {
				return nil, &ErrObsColumnMissing{Column: col, H5ad: h5adPath, Detail: info.Error}
			}
			return nil, fmt.Errorf("obs-values failed (%s): %s", info.Type, info.Error)
		}
		return nil, fmt.Errorf("obs-values failed: %v (stderr: %s)", err, bytes.TrimSpace(stderr.Bytes()))
	}

	var values []string
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &values); err != nil {
		return nil, fmt.Errorf("decode obs-values output: %w (raw=%.200s)", err, stdout.String())
	}
	return values, nil
}

// ErrObsColumnMissing is returned by ObsValues when the requested
// obs column is not present in the target h5ad.
type ErrObsColumnMissing struct {
	Column string
	H5ad   string
	Detail string // the helper's friendly error including the list of available columns
}

func (e *ErrObsColumnMissing) Error() string { return e.Detail }

#!/usr/bin/env -S uv run --quiet --script
# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "anndata>=0.10",
#   "numpy>=1.26",
#   "scipy>=1.11",
# ]
# ///
"""
mus h5ad inspector — emits a JSON report to stdout.

Designed to fingerprint a `.h5ad` for the mus sanity checks defined in
/data/1/users/mark/anthive4/doc/H5AD_INGESTION_SPEC.md without loading
the expression matrix into memory:

  * open in backed='r' mode → adata.X and adata.layers["counts"] stay
    on disk as h5py-backed datasets,
  * stream them in slabs to compute min + integer-ness exactly, with
    pragmatic early-exits (negative seen, integer-ness locked-in,
    hard cap at MAX_EXAMINED values),
  * obs / var / obsm load eagerly (they are small).

stdout  → one line of JSON on success.
stderr  → human-readable error JSON ({"error": "...", "type": "..."}) plus exit 1
          when the file can't be read by anndata at all.

A successful run does NOT mean the file passes sanity — only that the
report was produced. mus (Go side) reads the JSON and decides
HARD/SOFT findings.
"""

from __future__ import annotations

import json
import sys
import warnings

import numpy as np
import scipy.sparse as sp

warnings.filterwarnings("ignore")

import anndata as ad  # noqa: E402  (kept after warnings filter)

# --- Streaming thresholds ---------------------------------------------------
# Hard-coded by design (mus CLAUDE.md / discussion 2026-05-28). These are
# tuning knobs, not policy.
MAX_EXAMINED = 10_000_000      # stop after this many values total
INTEGER_LOCK_THRESHOLD = 100_000  # values seen before integer-ness flag is locked
SPARSE_CHUNK = 1_000_000       # non-zeros per slab when reading sparse .data
DENSE_ROW_CHUNK = 1_000        # rows per slab for dense matrices
GENE_SYMBOL_SAMPLE = 500       # var_names to sample for the heuristic


def looks_integer(arr: np.ndarray) -> bool:
    """True if every value in arr is integer-valued (within float tolerance)."""
    if arr.size == 0:
        return True
    if np.issubdtype(arr.dtype, np.integer):
        return True
    return bool(np.allclose(arr, np.rint(arr)))


def stream_matrix(matrix) -> dict:
    """Stream `matrix` in slabs, returning min/max/is_integer + exit reason.

    Works for:
      - anndata-backed sparse  (slicing returns scipy.sparse matrices)
      - anndata-backed dense   (slicing returns numpy arrays)
      - in-memory sparse / dense (test path)

    Returns a dict with the keys the Go side expects.
    """
    out = {
        "present": matrix is not None,
        "dtype": None,
        "is_sparse": None,
        "examined": 0,
        "total_estimate": None,
        "min": None,
        "max": None,
        "is_integer": None,
        "exit_reason": None,
        "shape": None,
    }
    if matrix is None:
        out["exit_reason"] = "not_present"
        return out

    out["dtype"] = str(matrix.dtype) if hasattr(matrix, "dtype") else None
    out["is_sparse"] = _is_sparse(matrix)

    try:
        out["shape"] = list(matrix.shape) if hasattr(matrix, "shape") else None
    except Exception:
        out["shape"] = None

    if out["is_sparse"]:
        out["total_estimate"] = _nnz_estimate(matrix)
        _stream_sparse(matrix, out)
    else:
        _stream_dense(matrix, out)

    return out


def _is_sparse(matrix) -> bool:
    if sp.issparse(matrix):
        return True
    # anndata 0.10's backed SparseDataset
    cls = type(matrix).__name__
    if "Sparse" in cls and ("Dataset" in cls or "View" in cls):
        return True
    return False


def _nnz_estimate(matrix) -> int | None:
    """Best-effort total non-zero count without materialising the matrix."""
    if hasattr(matrix, "nnz"):
        try:
            return int(matrix.nnz)
        except Exception:
            pass
    # backed SparseDataset exposes .group['data'].shape
    try:
        return int(matrix.shape[0] * matrix.shape[1])  # upper bound
    except Exception:
        return None


def _stream_sparse(matrix, out: dict) -> None:
    """Iterate `matrix` in row-chunks; for each chunk pull .data and update stats."""
    n_rows = matrix.shape[0]
    rows_per_chunk = max(1, min(n_rows, SPARSE_CHUNK // max(1, matrix.shape[1] // 50)))
    running_min: float | None = None
    running_max: float | None = None
    integer_flag: bool | None = None
    examined = 0

    for start in range(0, n_rows, rows_per_chunk):
        stop = min(start + rows_per_chunk, n_rows)
        slab = matrix[start:stop]
        # anndata slicing of backed sparse yields a scipy sparse matrix in memory.
        data = slab.data if hasattr(slab, "data") else np.asarray(slab).ravel()
        if data.size == 0:
            continue
        local_min = float(data.min())
        local_max = float(data.max())
        if running_min is None or local_min < running_min:
            running_min = local_min
        if running_max is None or local_max > running_max:
            running_max = local_max
        if integer_flag is not False:
            if looks_integer(data):
                integer_flag = True if integer_flag is None else integer_flag
            else:
                integer_flag = False
        examined += int(data.size)

        if running_min is not None and running_min < 0:
            out["exit_reason"] = "negative_value_seen"
            break
        if examined >= MAX_EXAMINED:
            out["exit_reason"] = "max_examined"
            break
        if (examined >= INTEGER_LOCK_THRESHOLD and integer_flag is False):
            # We've already settled the integer question; keep going for min
            # confidence up to MAX_EXAMINED, but don't break early here —
            # the min check is the load-bearing one.
            pass
    else:
        out["exit_reason"] = "matrix_exhausted"

    out["min"] = running_min
    out["max"] = running_max
    out["is_integer"] = integer_flag
    out["examined"] = examined


def _stream_dense(matrix, out: dict) -> None:
    """Same logic as sparse but row-wise on a dense backed/in-memory matrix."""
    try:
        n_rows = matrix.shape[0]
        n_cols = matrix.shape[1]
    except Exception:
        # Mystery matrix — best-effort: pull the whole thing and hope it's small.
        arr = np.asarray(matrix)
        out["min"] = float(arr.min()) if arr.size else None
        out["max"] = float(arr.max()) if arr.size else None
        out["is_integer"] = looks_integer(arr)
        out["examined"] = int(arr.size)
        out["exit_reason"] = "matrix_exhausted"
        return

    out["total_estimate"] = int(n_rows * n_cols)
    running_min: float | None = None
    running_max: float | None = None
    integer_flag: bool | None = None
    examined = 0

    for start in range(0, n_rows, DENSE_ROW_CHUNK):
        stop = min(start + DENSE_ROW_CHUNK, n_rows)
        arr = np.asarray(matrix[start:stop])
        if arr.size == 0:
            continue
        local_min = float(arr.min())
        local_max = float(arr.max())
        if running_min is None or local_min < running_min:
            running_min = local_min
        if running_max is None or local_max > running_max:
            running_max = local_max
        if integer_flag is not False:
            if looks_integer(arr):
                integer_flag = True if integer_flag is None else integer_flag
            else:
                integer_flag = False
        examined += int(arr.size)

        if running_min is not None and running_min < 0:
            out["exit_reason"] = "negative_value_seen"
            break
        if examined >= MAX_EXAMINED:
            out["exit_reason"] = "max_examined"
            break
    else:
        out["exit_reason"] = "matrix_exhausted"

    out["min"] = running_min
    out["max"] = running_max
    out["is_integer"] = integer_flag
    out["examined"] = examined


RESERVED = {"cell_id", "cell_name", "gene_id", "gene_name"}


def inspect(path: str) -> dict:
    """Open the file, return the JSON-ready report dict."""
    a = ad.read_h5ad(path, backed="r")
    try:
        report = {
            "schema_version": 1,
            "n_obs": int(a.n_obs),
            "n_vars": int(a.n_vars),
            "x": stream_matrix(a.X),
            "counts": stream_matrix(a.layers["counts"] if "counts" in a.layers else None),
            "layers": list(a.layers.keys()),
            "obsm": {
                "keys": list(a.obsm.keys()),
                "has_umap": "X_umap" in a.obsm,
                "has_tsne": "X_tsne" in a.obsm,
            },
            "obs_names_unique": bool(a.obs_names.is_unique),
            "var_names_unique": bool(a.var_names.is_unique),
            "obs_columns": list(a.obs.columns),
            "var_columns": list(a.var.columns),
            "reserved_in_obs": sorted(RESERVED & set(a.obs.columns)),
            "reserved_in_var": sorted(RESERVED & set(a.var.columns)),
            "high_card_obs": {
                c: int(a.obs[c].nunique())
                for c in a.obs.columns
                if _safe_nunique(a.obs[c]) > 10_000
            },
            "anndata_version": _anndata_version(a),
            "var_names_sample": [str(x) for x in list(a.var_names[:GENE_SYMBOL_SAMPLE])],
        }
    finally:
        try:
            a.file.close()
        except Exception:
            pass
    return report


def _safe_nunique(series) -> int:
    try:
        return int(series.nunique())
    except Exception:
        return 0


def _anndata_version(a) -> str | None:
    try:
        # anndata stores its version in uns["_anndata_version"] (newer) or
        # uns["anndata_version"] (older). Be permissive.
        for k in ("_anndata_version", "anndata_version"):
            if k in a.uns:
                return str(a.uns[k])
    except Exception:
        pass
    return None


def main() -> int:
    if len(sys.argv) != 2:
        print(json.dumps({"error": "usage: inspect.py PATH", "type": "UsageError"}),
              file=sys.stderr)
        return 2
    path = sys.argv[1]
    try:
        report = inspect(path)
    except Exception as e:
        print(json.dumps({"error": str(e), "type": type(e).__name__}),
              file=sys.stderr)
        return 1
    json.dump(report, sys.stdout, default=str)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())

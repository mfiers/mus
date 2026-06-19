#!/usr/bin/env -S uv run --quiet --script
# /// script
# requires-python = ">=3.10"
# dependencies = ["anndata>=0.10", "numpy", "pandas"]
# ///
"""Minimal helper: dump the sorted unique values of one obs column.

Usage:
    mus_h5ad_obs_values.py PATH COL

Stdout (on success):  JSON array of distinct values as strings, sorted lexicographically.
Stderr:               progress lines prefixed with "[mus h5ad obs]" and (on error) a
                      single JSON line with {"error": "...", "type": "..."}.
Exit code:            0 on success, 1 on error, 2 on usage error.

Called by `mus de check` when validating per-file de_cluster against the
target h5ad's obs[de_cluster_obs_column]. Kept separate from the full
mus_h5ad_inspector.py — obs lookup is fast (single column, no body
streaming) and shouldn't pull in the structural inspector's machinery.
"""

import json
import sys

import anndata as ad


def progress(msg: str) -> None:
    sys.stderr.write(f"[mus h5ad obs] {msg}\n")
    sys.stderr.flush()


def collect(path: str, col: str) -> list[str]:
    progress(f"opening {path} in backed='r' mode…")
    a = ad.read_h5ad(path, backed="r")
    if col not in a.obs.columns:
        raise KeyError(
            f"obs column {col!r} not found in {path}; "
            f"available columns: {sorted(a.obs.columns)}"
        )
    progress(f"reading obs[{col!r}] values…")
    # Stringify every value: anndata stores categoricals as pandas
    # Categorical, mixed types are possible. anthive will compare
    # against the depositor's string from mus.cfg.
    values = sorted({str(v) for v in a.obs[col].dropna().unique()})
    return values


def main() -> int:
    if len(sys.argv) != 3:
        print(
            json.dumps(
                {"error": "usage: mus_h5ad_obs_values.py PATH COL", "type": "UsageError"}
            ),
            file=sys.stderr,
        )
        return 2
    path, col = sys.argv[1], sys.argv[2]
    try:
        values = collect(path, col)
    except Exception as e:
        print(
            json.dumps({"error": str(e), "type": type(e).__name__}),
            file=sys.stderr,
        )
        return 1
    json.dump(values, sys.stdout)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())

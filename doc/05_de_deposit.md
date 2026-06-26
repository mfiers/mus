# 05 — Differential-expression deposit

Preparing differential-expression (DE) result tables for ingest into the anthive single-cell catalog.

A DE result is a per-gene statistics table from a tool like DESeq2, edgeR, scanpy's `rank_genes_groups`, or Seurat's `FindMarkers`. mus checks the table's shape, stamps a sidecar, and uploads to S3 under a `de/` prefix the anthive ingester knows to pick up.

This doc assumes [01](01_install_and_setup.md), [02](02_annotation_and_vocabularies.md), and that `mus s3 login` has been done (see [04](04_h5ad_deposit.md) §1).

---

## 1. Concepts

### Always required

| Field | What |
|---|---|
| **`data_project`** | `<NAME><YEAR>` (e.g. `Fiers2025`). Path component on S3. Same regex as elsewhere in mus. |
| **`de_dataset_id`** OR **`de_dataset_h5ad`** | Identifies the target h5ad. `de_dataset_id` = 3-char anthive code (preferred, e.g. `CMR`). `de_dataset_h5ad` = h5ad basename (e.g. `Single.Grafts.h5ad`) — use for co-ingest when no catalog code exists yet. |
| **`de_format`** | One of: `deseq2` / `edger` / `scanpy` / `seurat` / `glmmseq` / `glmmtmb` / `long`. Auto-detected from column names; `--format X` overrides. |
| **`de_method`** | Auto-filled from preset for everything EXCEPT `long`. For `long` you MUST set it explicitly (e.g. `DESeq2`, `edgeR`, `custom-pipeline`). |

### Required per preset (matches anthive's ingest-side `--map` flags)

| Preset | Also required |
|---|---|
| `deseq2` / `edger` / `seurat` / `glmmseq` / `glmmtmb` | both `de_term` + `de_contrast` |
| `scanpy` | `de_term` only (de_contrast comes from the file's `group` column at ingest) |
| `long` | `de_map_gene=<source-column>` (TSV column holding the gene id). Other `de_map_<TARGET>` keys are optional; see §3. |

### Optional but valuable (passed through to anthive)

| Field | What |
|---|---|
| `de_description` | Required `-m` message becomes this. Like a git commit message. |
| `de_software_version` | e.g. `"scanpy 1.10.4"` / `"DESeq2 1.42.0"` |
| `de_reference_type` | `pairwise` / `vsrest` / `vsreference` / `vsgrandmean` |
| `de_covariates` | CSV list, e.g. `sex,age,batch` |
| `de_filter_criteria` | Free text describing the per-gene filter. |
| `de_cluster` | (per-file) Cluster label for per-cluster DE (e.g. `microglia.DAM`). Omit for whole-dataset DE. |
| `de_cluster_obs_column` | (folder-level) Which `obs` column in the target h5ad holds cluster labels (e.g. `leiden`). |

### Long-format column mapping

`long` reads from arbitrary TSV columns and maps them onto anthive's canonical `__de` schema. Set `de_map_<TARGET>=<source-column>` per canonical you need to translate. Acceptable TARGETs:

`gene` (required), `term`, `contrast`, `log2fc`, `zeff`, `pvalue`, `padj`, `estimate`, `SE`, `ci_low`, `ci_high`

Source columns that already match the canonical name don't need a map entry — only declare overrides. Missing-but-optional canonicals are auto-dropped at ingest.

### Co-ingest

New h5ad + new DE in the SAME upload. mus links them locally by matching `de_dataset_h5ad` to a sibling file; the anthive catalog cross-check is skipped (anthive ingests h5ads BEFORE DEs in one ingest run, so the dataset exists by the time the DE is processed).

`de_*` keys live in the same cascading `mus.cfg` as everything else. No separate `de.cfg`.

---

## 2. Find the anthive dataset id (or co-ingest)

### 2a. h5ad already in anthive

You need its 3-char `dataset_id`. Look it up with the `vark` CLI:

```bash
# All datasets, dedup'd across reachable servers:
vark db ls -o table

# Filter by author / year / project:
vark db ls -o table | grep -i your-dataset-title

# Full metadata for one (when you have the id but want to verify):
vark db info CMR

# DE studies already attached to one dataset (avoid duplicate contrasts):
vark de CMR
vark de CMR <STUDY> <TERM>     # specific contrasts within a study

# obs columns of the target (for de_cluster_obs_column):
vark fields CMR
```

If `vark` isn't installed, browse the anthive web view, or ask anthive4 for the id (it lives in the file's `<basename>.md` frontmatter on iRODS).

Then in your DE folder's `mus.cfg`:

```bash
mus config set de_dataset_id CMR        # 3-char code
```

### 2b. Co-ingest (h5ad doesn't exist on anthive yet)

You're uploading the h5ad in the same deposit as the DE. No catalog code exists yet — anthive will assign one at ingest by matching basename. Set:

```bash
mus config set de_dataset_h5ad Single.Grafts.h5ad
```

mus will look for the h5ad locally (same dir as the DE file, or in any ancestor directory) and report it as a co-ingest deposit.

---

## 3. Set the rest of the DE annotation

### Folder-level (cascading)

```bash
mus config set data_project Fiers2025                  # mandatory, see doc 02
mus config set de_dataset_id CMR                       # OR de_dataset_h5ad

# REQUIRED per preset (anthive's ingest-side --term, --contrast flags):

# deseq2 / edger / seurat / glmmseq / glmmtmb:
mus config set de_term condition                       # model term name
mus config set de_contrast treated_vs_control          # level identifier

# scanpy: de_term only (de_contrast comes from the file's `group` column)
mus config set de_term condition

# long: needs a column map AND an explicit method
mus config set de_method custom-pipeline               # no preset default for long
mus config set de_map_gene gene_id                     # source TSV column for gene
# Optional long maps (only set when source != canonical name):
# mus config set de_map_log2fc lfc
# mus config set de_map_pvalue pval
# mus config set de_map_padj fdr
# mus config set de_map_term variable
# mus config set de_map_contrast level

# Recommended (passed to anthive as-is):
mus config set de_software_version "scanpy 1.10.4"     # e.g. "DESeq2 1.42.0"
mus config set de_reference_type pairwise              # pairwise | vsrest | vsreference | vsgrandmean
mus config set de_covariates sex,age,batch             # CSV list
mus config set de_filter_criteria "expressed in ≥3 cells per sample"

# Only if you're doing per-cluster DE:
mus config set de_cluster_obs_column leiden            # column in target h5ad
```

### Per-file (only when needed)

If the DE is per-cluster (one file per cluster), set the cluster label on each file via a subfolder's `mus.cfg`:

```
upload/
├── mus.cfg                       # data_project + de_dataset_id + de_cluster_obs_column
└── per_cluster/
    ├── DAM/
    │   ├── mus.cfg               # de_cluster=microglia.DAM
    │   └── results.tsv
    ├── homeostatic/
    │   ├── mus.cfg               # de_cluster=microglia.homeostatic
    │   └── results.tsv
    └── TR/
        ├── mus.cfg               # de_cluster=microglia.TR
        └── results.tsv
```

For whole-dataset DE (one global contrast across all cells), leave `de_cluster` unset everywhere.

---

## 4. Validate with `mus de check`

```bash
mus de check -m "wilcoxon rerun after donor-4 exclusion" final/de_results.tsv
```

The `-m` message is REQUIRED. It writes `de_description` on the sidecar — like a git commit message for the analysis.

You can point at:
- a single file: `mus de check -m "..." de/results.tsv`
- multiple files: `mus de check -m "..." de/a.tsv de/b.tsv`
- a folder: `mus de check -m "..." de/` (walks recursively)

Accepted extensions: `.tsv`, `.csv`, `.tsv.gz`, `.csv.gz`. Compressed variants are decompressed transparently. **Parquet is REJECTED** (no major DE tool writes parquet natively; convert via pandas/pyarrow if you have parquet upstream).

### What it checks

For each file, in order. A failure on any HARD step stops that file (no sidecar written).

1. **Format detection** — by column-name signature (case- and separator-insensitive). The seven formats:
   | Format | Source columns (auto-detect signature) |
   |---|---|
   | `deseq2` | baseMean, log2FoldChange, lfcSE, padj |
   | `edger` | logFC, logCPM, FDR |
   | `scanpy` | names, logfoldchanges, pvals_adj (from `rank_genes_groups`) |
   | `seurat` | avg_log2FC, pct.1, pct.2, p_val_adj (from `FindMarkers`) |
   | `glmmseq` | chisq, df, P_FixedEffects |
   | `glmmtmb` | Estimate, Std. Error, z value, Pr(>\|z\|) |
   | `long` | gene_id, effsize, pvalue, padj, term, contrast |

   Override with `--format <name>` when mus picks the wrong one (e.g. renamed columns + `de_map_*`).

2. **Row count** (HARD) — refuses files with zero data rows.

3. **For `long` format** — computes the distinct {term × contrast} set from the body and stamps it sorted+comma-joined into `de_term` / `de_contrast` on the sidecar. Set `de_term=auto` and `de_contrast=auto` in `mus.cfg` to opt in.

4. **`data_project`** (HARD) — must resolve from the cascade. Must pass `<Name><Year>` regex.

5. **Dataset target** (HARD) — `de_dataset_id` OR `de_dataset_h5ad` must be set.

6. **Co-ingest detection** — if `de_dataset_h5ad` is set AND the file is locally present (same dir or one level up), mus reports it as a co-ingest deposit and skips the catalog cross-check (step 7).

7. **Anthive catalog cross-check** (HARD, mus 0.2.28+) — fetches `s3://<bucket>/_catalog/manifest.json` (cached at `$XDG_CACHE_HOME/mus/anthive_catalog.json`, 5-min TTL, stale-fallback-with-warn) and validates `de_dataset_id` against it:
   - exact match → OK with the dataset title
   - matches data_project's `group_id` → HARD-FAIL ("S26 is the GROUP_ID of Sun2026, not a file_id. Did you mean AQB, AQN, AQP, AQR, DUN?")
   - exists in a DIFFERENT data_project → HARD-FAIL with the foreign project named
   - no match → HARD-FAIL with top-3 Levenshtein suggestions + full file_id list
   - matches but `inactive=true` → SKIP warn (depositor decides)
   - data_project not yet in catalog → SKIP "first deposit?" (no block)

   Pass `--no-catalog-check` to skip (offline mode, or pre-S3-creds setup). Falls back to a soft `vark` query if that CLI is on PATH.

8. **`de_method`** (HARD for `long`) — auto-fills from preset for all formats EXCEPT `long`. For `long` you must set it explicitly.

9. **Per-format required `de_*` fields** (HARD, mus 0.2.29+) — mirrors anthive's ingest-side `--term`, `--contrast`, and `--map gene` flags:
   - `deseq2` / `edger` / `seurat` / `glmmseq` / `glmmtmb` → both `de_term` + `de_contrast` required
   - `scanpy` → `de_term` required (de_contrast comes from the file's `group` column)
   - `long` → `de_map_gene` required (other `de_map_*` keys optional)

10. **Post-mapping column reachability** (HARD, mus 0.2.30+) — every required CANONICAL column must resolve to a source column actually present in the file's header. Resolution: `de_map_<canonical>` if set, else the preset's identity-default source name. Required canonicals per preset:
    - `deseq2` / `edger` / `seurat` / `glmmseq` → gene, log2fc, pvalue, padj
    - `scanpy` → gene, contrast, log2fc, pvalue, padj
    - `glmmtmb` → gene, estimate, pvalue
    - `long` → gene, term, contrast

    Catches the case where `--format` is used as an override and the file's columns don't actually contain what the preset's binder expects.

11. **Cluster validation** (when `de_cluster` is set) — deep validation reads the target h5ad's `obs[de_cluster_obs_column]` and verifies `de_cluster` ∈ that set. Runs only when the target h5ad is locally present (co-ingest path); SKIPs with "anthive validates at ingest" for catalog-resident datasets.

12. **Soft-field nudges** — OK / SKIP line for every recommended field. Bottom hint: `Set soft parameters via: mus config set <NAME> <VALUE>`.

13. **Compression nudge** — suggests `gzip <file>` for plain TSV/CSV; says OK for already-gzipped.

14. **Stamp the sidecar** — `<file>.mus` next to the data file, carrying the full `de_*` block + cached PASS marker (sha256 + check version).

### Sample output

```
$ mus de check -m "wilcoxon rerun, donor-4 excluded" de/

[/path/de/results.tsv]
  OK    format                         (detected scanpy (matched: names, logfoldchanges, pvals_adj))
  OK    rows                           (1000 data rows)
  OK    data_project                   (Fiers2025)
  OK    dataset                        (de_dataset_id=CMR)
  OK    method                         (auto-filled "scanpy.rank_genes_groups" from de_format=scanpy)
  OK    description                    ("wilcoxon rerun, donor-4 excluded")
  OK    de_software_version (soft)     (scanpy 1.10.4)
  SKIP  de_reference_type (soft)       (unset — suggest: pairwise | vsrest | vsreference | vsgrandmean)
  SKIP  de_covariates (soft)           (unset — suggest: sex,age,batch)
  SKIP  de_filter_criteria (soft)      (unset — suggest: expressed in ≥3 cells per sample)
  OK    compression                    (gzipped — good for S3 storage / bandwidth)
  OK    sidecar                        (PASS recorded)

Set soft parameters via: mus config set <NAME> <VALUE>

Grouping by dataset
  CMR — 1 file(s), 1000 rows total
      results.tsv   format=scanpy rows=1000 cluster=-

checked 1, passed 1, failed 0
```

The "Grouping by dataset" line at the end is the killer feature — catches a typo in `de_dataset_id` before deposit, because you can see at a glance which target your DE files are grouped under.

`--silent` suppresses OK / SKIP lines (keeps failures + summary). `--format X` overrides format detection.

---

## 5. Upload to S3

```bash
mus s3 upload -m "ingest into anthive" de/results.tsv
```

Or point at a folder:

```bash
mus s3 upload -m "..." de/
```

Or mix DE + h5ad in one shot (co-ingest):

```bash
mus s3 upload -m "initial deposit + DE" \
    final/Single.Grafts.h5ad final/de/results.tsv
```

Multiple files across different parent dirs work too (no source-folder restriction for DE files — each lands at its own S3 prefix):

```bash
mus s3 upload -m "..." run1/results.tsv run2/results.tsv
```

### Where it lands

```
s3://<bucket>/__incoming/<data_project>/de/<parent-dir>/<basename>
s3://<bucket>/__incoming/<data_project>/de/<parent-dir>/<basename>.mus    ← collapsed sidecar
```

`<parent-dir>` is the basename of the local directory the DE file came from (e.g. `main_model`, `sex_stratified`, or just `de` for flat layouts). Two DE runs against the same target h5ad land at different prefixes when they live in different local folders — preserves chain of custody.

The **collapsed sidecar** mirrors the h5ad path's pattern: single KEY=VALUE file holding the merged cascade + per-file sidecar + freshly-stamped `s3_*` block.

### Gates

`mus s3 upload` refuses if:
- The DE sidecar doesn't carry a fresh `mus de check` PASS (file sha + check version both current).
- `data_project` is unset.
- ELN status is undecided (link or disable).
- The configured bucket doesn't exist.
- No `-m` message.

Fix each, re-run. No `--force` for the DE gate — re-running `mus de check` is cheap (stat-fast cached pass).

### Re-uploading (mus 0.2.39+)

`mus s3 upload` is **skip-if-same**: before each PutObject mus HeadObjects the destination and reads `x-amz-meta-mus_sha256` (mus stamps this on every successful upload). Three outcomes:

| Remote state | Behavior |
|---|---|
| Same `mus_sha256` as local | Silent skip; sidecar still refreshes |
| Different sha | Prompt `⚠ s3://… exists with different bytes — overwrite? [y/N]`. `--force` bypasses; non-TTY refuses |
| Absent / no `mus_sha256` (legacy / non-mus upload) | Treated as differs (prompt) — mus can't prove same-bytes without its stamp |

Matches the iRODS-side `mus irods upload` contract from 0.2.27 — no more silent overwrites when re-running a deposit.

---

## 6. End-to-end examples

### Pure DE deposit (h5ad already in anthive)

```bash
cd ~/projects/microglia_de_analyses

# Annotate
mus config set data_project Fiers2025
mus config set de_dataset_id CMR
mus config set de_software_version "scanpy 1.10.4"

# Per-analysis layout
mkdir -p sex_stratified male_vs_female
# (write your DE result tables into the analysis folders)

# Check
mus de check -m "wilcoxon, sex-stratified analysis" sex_stratified/

# Upload
mus s3 upload -m "ingest into anthive" sex_stratified/
```

S3 result:
```
s3://anthive4a/__incoming/Fiers2025/de/sex_stratified/male_de.tsv
s3://anthive4a/__incoming/Fiers2025/de/sex_stratified/male_de.tsv.mus
s3://anthive4a/__incoming/Fiers2025/de/sex_stratified/female_de.tsv
s3://anthive4a/__incoming/Fiers2025/de/sex_stratified/female_de.tsv.mus
```

### Co-ingest (h5ad + DE in one upload)

```bash
cd ~/projects/microglia_pilot

# Annotate
mus eln link 12345                                      # writes data_project + eln_*
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set cell_type 'microglial cell'
mus config set technology 10x_chromium_3p_v3
mus config set suspension_type nucleus
mus config set source internal
mus config set access internal
mus config set de_dataset_h5ad Single.Grafts.h5ad      # co-ingest pointer

# Sanity-check both sides
mus h5ad check final/Single.Grafts.h5ad
mus de check  -m "post-QC clustering DE" final/de/results.tsv

# Deposit in one go
mus s3 upload -m "initial anthive deposit + DE" \
    final/Single.Grafts.h5ad final/de/results.tsv
```

S3 result:
```
s3://anthive4a/__incoming/Fiers2025/Single.Grafts.h5ad
s3://anthive4a/__incoming/Fiers2025/Single.Grafts.h5ad.mus
s3://anthive4a/__incoming/Fiers2025/de/de/results.tsv
s3://anthive4a/__incoming/Fiers2025/de/de/results.tsv.mus
```

Anthive's ingester sees the h5ad first, assigns a 3-char code, then ingests the DE — resolving `de_dataset_h5ad=Single.Grafts.h5ad` against that newly-assigned code.

---

## 7. Per-cluster DE example

```bash
cd ~/projects/sala_frigerio_2019/de_per_cluster

# Folder-level: where cluster labels come from
mus config set data_project SalaFrigerio2019
mus config set de_dataset_id CMR
mus config set de_cluster_obs_column leiden            # in target h5ad's obs

# Per-subfolder cluster labels
mus -C DAM        config set de_cluster microglia.DAM
mus -C homeostat  config set de_cluster microglia.homeostatic
mus -C TR         config set de_cluster microglia.TR

# Validate; mus checks each cluster value is in target_h5ad.obs[leiden] if vark is available
mus de check -m "per-cluster DE for Sun lab review" .

# Upload everything
mus s3 upload -m "per-cluster DE deposit" .
```

If `de_cluster_obs_column` is unset and you HAVE `de_cluster` declared per file, mus warns but doesn't block — multi-factorial designs (genotype × sex × cluster) often don't have a single obs column that captures the model.

---

## Common pitfalls

- **DESeq2 default export (R) — handled automatically** — `write.csv(res, "file.csv")` puts gene IDs in the row index, so the file's header line begins with a comma and the first column has no name. mus 0.2.38+ auto-detects this case for R-native presets (deseq2 / edger / seurat / glmmseq / glmmtmb), accepts the file, and stamps `de_map_gene=column0` on the sidecar (the name DuckDB assigns to an unnamed column when it reads the file). No depositor action needed — just deposit. The check shows:

  ```
  OK    columns (auto-mapped)    (DESeq2-style row-index convention detected — first column
                                  has no header. Auto-mapped gene → column0 …)
  ```

  If you'd rather have a named gene column anyway (cleaner files), the upstream re-export options are:

  ```r
  # R:
  res$gene <- rownames(res)
  write.table(res, "file.tsv", sep="\t", row.names=FALSE, quote=FALSE)
  ```

  ```python
  # Python (pydeseq2 / pandas):
  df.index.name = "gene"
  df.to_csv("file.tsv", sep="\t")
  ```

- **`format detection failed (saw columns: …)`** — your tool's output doesn't match any signature. Rename columns to match one of the seven formats OR pass `--format long` if the schema is anthive-native (gene_id + effsize + pvalue + padj + term + contrast).
- **`parquet not accepted in DE deposits`** — convert via `pandas.read_parquet().to_csv(sep="\t")`. Anthive accepts parquet on the receiving end but mus enforces tsv/csv at the deposit gate.
- **`0 data rows after header — empty DE table is refused`** — the file has only a header. Re-run your DE tool with at least one gene passing the filter.
- **`neither de_dataset_id nor de_dataset_h5ad set`** — set one in `mus.cfg`. For new datasets (no anthive code yet), use `de_dataset_h5ad=<basename>.h5ad` and co-ingest.
- **vark cross-check says "not in catalog"** — typo OR first-time deposit. mus doesn't block; anthive's ingester is the authoritative resolver.
- **Re-running `mus de check` after editing the file** — old sidecar is invalidated automatically (file sha changed). Re-check is cheap if you didn't change the file.

For the depositor primer with deeper detail per format, run:

```bash
mus de howto
```

---

## What's next

You've reached the end of the tutorial sequence. For deeper reference:

- [quickstart.md](quickstart.md) — full reference manual with every flag.
- `mus h5ad howto` — h5ad-specific structural rules.
- `mus de howto` — DE-format signatures + field reference.
- [joint vocabulary](https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml) — the controlled-value lists.

# 05 — Differential-expression deposit

Preparing differential-expression (DE) result tables for ingest into the anthive single-cell catalog.

A DE result is a per-gene statistics table from a tool like DESeq2, edgeR, scanpy's `rank_genes_groups`, or Seurat's `FindMarkers`. mus checks the table's shape, stamps a sidecar, and uploads to S3 under a `de/` prefix the anthive ingester knows to pick up.

This doc assumes [01](01_install_and_setup.md), [02](02_annotation_and_vocabularies.md), and that `mus s3 login` has been done (see [04](04_h5ad_deposit.md) §1).

---

## 1. Concepts

| Field | What |
|---|---|
| **`de_dataset_id`** | 3-char anthive code for the h5ad your DE is against (e.g. `CMR`). Preferred. |
| **`de_dataset_h5ad`** | h5ad basename (e.g. `Single.Grafts.h5ad`). Use when the h5ad is being co-ingested in the same upload and doesn't have a catalog code yet. |
| **`de_format`** | One of: `deseq2` / `edger` / `scanpy` / `seurat` / `glmmseq` / `glmmtmb` / `long`. Mus auto-detects from column names; `--format X` overrides. |
| **`de_cluster`** | (per-file) For per-cluster DE: the cluster label (e.g. `microglia.DAM`). Omit for whole-dataset DE. |
| **`de_cluster_obs_column`** | (folder-level) Which `obs` column in the target h5ad holds cluster labels (e.g. `leiden`). Enables cluster validation. |
| **Co-ingest** | New h5ad + new DE in the SAME upload. mus links them locally by matching `de_dataset_h5ad` to a sibling file. |

`de_*` keys live in the same cascading `mus.cfg` as everything else. No separate `de.cfg`.

---

## 2. Find the anthive dataset id (or co-ingest)

### 2a. h5ad already in anthive

You need its 3-char `dataset_id`. Look it up:

```bash
# If you have `vark` installed:
vark db ls -o table | grep -i your-dataset-title

# Or browse the anthive web view.
# Or ask anthive4 for the id (it lives in the file's <basename>.md frontmatter).
```

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

For each file:

1. **Format detection** — by column-name signature (case- and separator-insensitive). The seven formats:
   | Format | Required columns |
   |---|---|
   | `deseq2` | baseMean, log2FoldChange, lfcSE, padj |
   | `edger` | logFC, logCPM, FDR |
   | `scanpy` | names, logfoldchanges, pvals_adj (from `rank_genes_groups`) |
   | `seurat` | avg_log2FC, pct.1, pct.2, p_val_adj (from `FindMarkers`) |
   | `glmmseq` | chisq, df, P_FixedEffects |
   | `glmmtmb` | Estimate, Std. Error, z value, Pr(>\|z\|) |
   | `long` | gene_id, effsize, pvalue, padj, term, contrast |

   Override with `--format <name>` when mus picks the wrong one.

2. **Row count** — refuses files with zero data rows.

3. **For `long` format** — computes the distinct {term × contrast} set from the body and stamps it sorted+comma-joined into `de_term` / `de_contrast` on the sidecar. Set `de_term=auto` and `de_contrast=auto` in `mus.cfg` to opt in.

4. **`data_project` + `de_dataset_id`/`de_dataset_h5ad`** — both must resolve from the cascade.

5. **Co-ingest detection** — if `de_dataset_h5ad` is set AND the file is locally present, mus reports it as a co-ingest deposit and skips the catalog cross-check.

6. **Vark cross-check** (when `vark` is on PATH) — verifies the declared dataset against the live anthive catalog. Best-effort: never blocks; warns on no match (typo or first-time deposit).

7. **Method auto-fill** — if `de_method` is unset, mus stamps the canonical name derived from `de_format` (deseq2 → `DESeq2`, edger → `edgeR`, etc).

8. **Soft-field nudges** — explicit OK / SKIP line for every recommended field. Bottom hint: `Set soft parameters via: mus config set <NAME> <VALUE>`.

9. **Compression nudge** — suggests `gzip <file>` for plain TSV/CSV; says OK for already-gzipped.

10. **Stamp the sidecar** — `<file>.mus` next to the data file, carrying the full `de_*` block + cached PASS marker.

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

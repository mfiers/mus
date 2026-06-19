# 04 — h5ad deposit (for anthive ingest)

Preparing a single-cell `.h5ad` for the lab's single-cell catalog (anthive). This is on top of the regular iRODS backup workflow in [03](03_eln_and_irods.md) — for h5ads going into anthive, you also push to a dedicated S3 staging bucket after a strict structural + vocabulary check.

**Anthive** is the lab's browse-and-query interface for single-cell datasets (its catalog reads from a S3 bucket called `anthive4a` by default; ingestion picks up new deposits from a fixed `__incoming/` staging prefix). You don't need to know more about anthive to deposit — but the constraints below come from its ingestion spec.

This doc assumes [01](01_install_and_setup.md), [02](02_annotation_and_vocabularies.md), and that you've already set `data_project`, `eln_*` (or disabled ELN), and the basic biology fields in `mus.cfg`.

---

## 1. S3 authentication (once per machine)

```bash
mus s3 login
```

Interactive wizard. Asks for:
- **Endpoint** — the S3 host (e.g. `fsn1.your-objectstorage.com` for Hetzner). Mus auto-prepends `https://`.
- **Region** — the provider's region string (e.g. `eu-central`). For S3-compatible providers, this often doesn't matter for routing but the SDK requires SOMETHING; the value travels in signatures.
- **Bucket** — typically `anthive4a` for the BADS lab. mus only writes to this ONE bucket.
- **Access key** + **Secret key** — IAM credentials with `PutObject` on the bucket.

Stored via the secret backend (OS keyring on desktop, age-encrypted file on headless). `mus s3 login` validates by calling `HeadBucket` before persisting — bad creds / wrong bucket → nothing saved.

Confirm:

```bash
mus s3 whoami     # prints endpoint + region + bucket
```

---

## 2. Set the h5ad-specific annotations

The seven HARD-required fields for anthive. If you did [02](02_annotation_and_vocabularies.md) thoroughly, they're already in your `mus.cfg`. Confirm:

```bash
mus config show | grep -E 'organism|tissue|cell_type|technology|suspension_type|access|source'
```

If any are missing:

```bash
mus config set organism human
mus config set tissue 'middle temporal gyrus,hippocampus'        # comma list ok
mus config set cell_type 'microglial cell'                       # comma list ok
mus config set technology 10x_chromium_3p_v3                     # see `mus vocab show technology`
mus config set suspension_type nucleus                           # cell | nucleus | na
mus config set source internal                                   # internal | external
mus config set access internal                                   # internal | public
```

Soft (recommended but not blocking) — set what you can:

```bash
mus config set disease 'Alzheimer disease'
mus config set sex pooled                                        # male | female | pooled | unknown
mus config set age '65y-90y'
mus config set doi 10.1234/microglia.2025
mus config set year 2026
mus config set contributor "Jane Doe"
mus config set license CC-BY-4.0
mus config set accession GSE123456
```

---

## 3. Sanity-check the .h5ad

```bash
mus h5ad check final/microglia_clusters.h5ad
```

What it does:
1. Reads the cascading `mus.cfg` + the file's existing `.h5ad.mus` sidecar.
2. Opens the file in backed-read mode via an embedded Python helper (uses `uv` + `anndata`).
3. Applies the §1.1–§1.4 structural rules.
4. Validates every metadata value against the joint vocabulary.
5. Prints a tiered HARD / SOFT / INFO report.
6. On PASS, stamps the sidecar with `h5ad_*` shape fields + the file's sha256 as `h5ad_inspected_sha256` (cached-PASS key).

Exit code is 0 on PASS, 1 on any HARD finding.

### Structural rules (HARD — checked mechanically)

| Slot | Requirement |
|---|---|
| `adata.X` | log-normalised expression. float32/float64. Min value ≥ 0 (no negatives, no Pearson / SCT residuals). |
| `adata.layers["counts"]` | raw integer counts. int OR float-typed but integer-valued. Min value ≥ 0. |
| `adata.obsm["X_umap"]` OR `adata.obsm["X_tsne"]` | at least one 2-D embedding for visualisation. |
| `adata.obs_names` | unique strings. |
| `adata.var_names` | unique strings carrying gene symbols (HGNC for human, MGI for mouse). Call `adata.var_names_make_unique()` before export. |
| Reserved columns | don't use `cell_id`, `cell_name`, `gene_id`, or `gene_name` as obs/var column names. |

**Common pitfall:** counts left in `adata.X`, `.layers["counts"]` missing → re-export with both populated.

### Soft fields (warnings, never blockers)

`doi`, `year`, `contributor`, `disease`, `sex`, `condition`, `age`, `accession`, `license`, `title`, `source_description`.

### Re-runs are cached

First pass is ~30s for a typical h5ad. The sidecar records (file sha256, vocab sha256, check-version constant). Subsequent runs are stat-fast unless ANY of those three changes:

```bash
mus h5ad check final/microglia_clusters.h5ad     # second run: ✓ cached pass
mus h5ad check final/* --force                   # re-run inspection unconditionally
```

If the file changes, vocab changes, or mus's check-version bumps, the cached pass is invalidated and mus re-inspects.

### Hide soft warnings

```bash
mus h5ad check --hide-soft mydata.h5ad
```

HARD + INFO still print; SOFT suppressed. Useful for CI.

---

## 4. (Optional) Write human-readable docs

mus does NOT generate these — but if you write them, they get uploaded.

**Folder-level: `index.md`** — what's in the project as a whole.

```markdown
# Microglia pilot (Fiers2025)

snRNA-seq of human prefrontal cortex microglia from the Sun lab,
sorted PU.1+ nuclei, 10x Multiome v2.

Datasets:
- microglia_clusters.h5ad   full 10x snRNA-seq output
- mydata_qc.h5ad            after doublet removal + filtering
```

**File-level: `<basename>.md`** — what's special about THIS file.

```markdown
# microglia_clusters.h5ad

Microglia-only subset of the full PFC cohort. PU.1+ sorted nuclei,
n=73,888 cells across 7 donors. Normalised with scanpy.pp.normalize_total → log1p.
UMAP from PCA(30) + Harmony on donor.

Caveats:
- donor 4 has high mito% — consider filtering.
- clusters 8 and 9 are likely doublets.
```

Free prose. No required schema. mus uploads them alongside the h5ad on S3 (see step 5).

---

## 5. Upload to S3 (anthive staging)

```bash
mus s3 upload -m "ingest into anthive" final/microglia_clusters.h5ad
```

The `-m` message is required. Stamped on the S3 object as `x-amz-meta-mus-upload-message`.

### Where it lands

```
s3://<bucket>/__incoming/<data_project>/<basename>.h5ad
s3://<bucket>/__incoming/<data_project>/<basename>.h5ad.mus   ← collapsed sidecar
```

`__incoming/` is a fixed staging prefix. Anthive's ingester picks files up from there and moves them to their final catalog location. mus NEVER writes outside `__incoming/`.

The **collapsed sidecar** is a single KEY=VALUE file holding the EFFECTIVE metadata at upload time — every cascading `mus.cfg` key (root → deepest, deeper overrides) overlaid with the file's own sidecar. Includes the freshly-stamped `s3_*` block. A downstream `mv` keeps data + sidecar paired; the catalog ingester reads one file to know everything mus knew at upload time.

### Gates

`mus s3 upload` refuses to push unless ALL of these pass:
- `data_project` is set in the cascade.
- ELN status is decided (linked or disabled).
- The configured S3 bucket exists.
- Filename ends in `.h5ad` (positional args).
- The sidecar carries a fresh `mus h5ad check` PASS (file sha + vocab sha + check version all current).

If the gate fails, you'll see a multi-line error listing what's stale. Fix each, re-run.

Bypass the h5ad gate (NOT recommended):

```bash
mus s3 upload --force-h5ad-upload -m "..." mydata.h5ad
```

### Multiple files in one upload

```bash
mus s3 upload -m "post-QC drops" final/a.h5ad final/b.h5ad final/c.h5ad
```

All must be from the same source folder (the collapsed-sidecar key would collide across folders sharing `data_project`).

---

## 6. (Optional) Upload to a sub-revision

If you're depositing a revision / draft / embargoed release of an h5ad, use `--subfolder`:

```bash
mus s3 upload -m "rev2 after reviewer comments" \
    --subfolder rev2  final/microglia_clusters.h5ad
```

Lands at `s3://<bucket>/__incoming/<data_project>/rev2/microglia_clusters.h5ad`. Single path segment only — `static`, `.`, `..` reserved.

Anthive's ingester sees this as a separate version. Useful for keeping draft+final or v1+v2 distinct without overwriting.

---

## 7. (Optional) Static assets

Auxiliary files (figures, JSON, PDFs) that the anthive web view needs but aren't biology data themselves:

```bash
mus s3 upload -m "site banner" --static figures/banner.png
mus s3 upload -m "QC plots" --static figures/umap.png --static figures/qc.json
```

Lands at `s3://<bucket>/__incoming/<data_project>/static/<basename>`. ALWAYS under `static/`, regardless of `--subfolder`.

Static-only invocations (no `.h5ad` positionals) are accepted:

```bash
mus s3 upload -m "..." --static figures/umap.png
```

Static objects carry only `mus_upload_message` as x-amz-meta — no biology AVU, no h5ad gate, no sidecar update.

---

## 8. Also push to iRODS (recommended)

The S3 upload feeds the anthive catalog; the iRODS upload is the lab's permanent archive. Most h5ad deposits go to both:

```bash
mus s3 upload    -m "ingest into anthive"  final/microglia_clusters.h5ad
mus irods upload -m "post-QC h5ad"         final/microglia_clusters.h5ad
```

The two paths share the same gates (`mus h5ad check` PASS, ELN decision, `data_project`), so once one passes the other will too.

---

## End-to-end example

```bash
cd ~/projects/microglia_pilot

# Annotate (one-time, see doc 02)
mus eln link 12345
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set cell_type 'microglial cell'
mus config set technology 10x_chromium_3p_v3
mus config set suspension_type nucleus
mus config set source internal
mus config set access internal
mus config set disease 'Alzheimer disease'

# Validate (cached after the first run)
mus h5ad check final/microglia_clusters.h5ad

# Deposit
mus s3 upload    -m "ingest into anthive" final/microglia_clusters.h5ad
mus irods upload -m "post-QC h5ad for anthive" final/microglia_clusters.h5ad
```

---

## Common pitfalls

- **`mus h5ad check` cannot inspect the file** — corrupt / truncated .h5ad, missing `uv` on PATH. Fix the file first; the rest of the gate runs only when the inspector succeeds.
- **counts in `adata.X`, `.layers["counts"]` missing** — re-export with both layers populated.
- **`var_names` are Ensembl IDs** — remap to gene symbols (HGNC / MGI) before export. The check refuses Ensembl-only.
- **`X_umap` missing** — embedding stored under a non-standard slot. Rename to `X_umap` or `X_tsne`.
- **Mixed nuclei + whole-cell in one file** — split into two .h5ad with different `suspension_type`.
- **"value not in vocabulary"** — `mus vocab show <field>` to see allowed values; file an issue to extend the vocab if it's a real missing term.
- **Sidecar value silently ignored** — the sidecar filename must be exactly `<basename>.h5ad.mus`. Common typo: `foo.mus` instead of `foo.h5ad.mus`.

For the structural rules in deeper detail, run `mus h5ad howto`.

---

## What's next

- **[05 — DE deposit](05_de_deposit.md)** — differential-expression result tables, often co-ingested with an h5ad.

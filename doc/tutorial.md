# mus tutorial

Two pathways. Pick the one that matches what you're doing today.

| Pathway | When to use it | What it does |
| --- | --- | --- |
| **A. Back up data** | You have files (any format) you need on the lab archive. | Tag → annotate → upload to iRODS. Done. |
| **B. Prep h5ad for anthive** | Submit a single-cell `.h5ad` to the lab catalog. | Tag → annotate → strict-validate → upload to S3 staging + iRODS. |

If you're not sure, you want **A**. **B** only kicks in when something specifically asks for an anthive-ready h5ad.

The two pathways share the same setup section (`§ Setup`). After that they're independent — skip straight to A or B.

---

## Setup (once per machine, ≈ 2 min)

```bash
# 1. Install
curl -sSL https://codeberg.org/mfiers/mus/raw/branch/main/install.sh | bash

# 2. iRODS auth — verifies you're logged into the lab archive
mus irods login          # if not yet → prints the exact `iron auth …` to run
                         # → then re-run `mus irods login` to confirm ✓

# 3. ELN auth — pastes a token from the eLab UI; verifies it works
mus eln login            # OR `mus eln disable --global` if you don't have eLab access

# 4. Shell completion
mus completion install
```

For full setup details (where keys land, how the keyring works, completion paths, …) see [quickstart.md](quickstart.md).

---

## Pathway A — Back up data to iRODS

**Goal.** Get your files onto the lab archive with provenance so you (or a colleague) can find and restore them later.

### A1. cd to the project folder

```bash
cd ~/projects/microglia_pilot
```

### A2. Link the folder to an eLab experiment

This sets `data_project` + `eln_experiment_id` in one shot.

```bash
mus eln link 12345                    # 12345 = numeric ID from the eLab URL
```

Don't have eLab? Run these instead:

```bash
mus config set data_project Fiers2025           # NameYear format
mus config set eln_experiment_name microglia    # path-safe label
mus eln disable                                 # once per folder
```

### A3. Annotate the folder (so every upload inherits the metadata)

These all go in the folder's `mus.cfg` (mus creates it on first write):

```bash
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set technology 10x_chromium_3p_v3
mus config set source internal              # internal | external
mus config set access internal              # internal | public
```

Subfolder overrides — use `-C SUBDIR`:

```bash
mus -C final config set access public
mus -C final config set doi 10.1234/microglia.2025
```

Inspect with `mus config show` (effective cascade) or `mus config show --local` (this folder only).

### A4. Upload

```bash
# Single files
mus irods upload -m "raw fastq drop"  raw/sample01.fastq.gz raw/sample02.fastq.gz

# A whole folder (mus packs many small files into a tar.gz if it makes sense)
mus irods upload -m "analysis scripts"  scripts/

# Multiple things in one shot — one shared `-m` message
mus irods upload -m "initial deposit"  scripts/  raw/  final/figure_data.csv
```

`-m / --message` is **required** — it's stamped on the sidecar, the iRODS AVU, and the ELN comment. No silent uploads.

After upload each file has a `<file>.mus` sidecar next to it. **Keep those.** They carry the sha256, the iRODS URL, the AVU snapshot — they're how `mus check`, `mus rm`, and `mus irods get` find their way back.

### A5. (optional) Verify + free local disk

```bash
mus check                           # every *.mus in this dir
mus check -r .                      # recurse
mus rm -y final/microglia_clusters.h5ad   # only after a passing remote-check
```

`mus rm` refuses to delete unless three things pass: sidecar says uploaded, local sha matches the sidecar, remote checksum matches too. After deletion the sidecar stays as a **tombstone** so `mus irods get final/microglia_clusters.h5ad.mus` can pull the data back any time.

### A6. End-to-end (copy-paste-able)

```bash
cd ~/projects/microglia_pilot

mus eln link 12345
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set technology 10x_chromium_3p_v3
mus config set source internal
mus config set access internal

mus irods upload -m "scripts snapshot" scripts/
mus irods upload -m "raw fastq" raw/sample01.fastq.gz raw/sample02.fastq.gz
mus irods upload -m "post-QC final + figure-3 source" \
    final/microglia_clusters.h5ad  final/figure_data.csv
```

That's pathway A. Done.

---

## Pathway B — Prep an h5ad for anthive

**Goal.** Submit a single-cell `.h5ad` to the lab catalog so it appears in anthive4.

Same setup as A (`mus eln link …` + folder annotation), **plus** two things:

1. `mus h5ad check` must PASS — anthive only ingests files mus has structurally + vocab-validated.
2. Files go to **both** S3 (anthive ingest channel) AND iRODS (lab archive).

### B1. Setup the folder (same as A, plus a few h5ad-specific fields)

```bash
cd ~/projects/microglia_pilot
mus eln link 12345

# the seven HARD-required fields for h5ad — all from controlled vocab
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set cell_type 'microglial cell'
mus config set technology 10x_chromium_3p_v3
mus config set suspension_type nucleus        # cell | nucleus | na
mus config set source internal
mus config set access internal
```

`mus config set` validates each value against the joint vocabulary as you type it. Off-vocab values are rejected with a "did you mean?" suggestion. Browse with:

```bash
mus vocab show tissue       # one field's allowed values
mus vocab show              # everything
```

### B2. Sanity-check the h5ad

```bash
mus h5ad check final/microglia_clusters.h5ad
```

This reads the cascade + the file, checks:

- **Structural rules** — `adata.X` is log-normalised, `adata.layers["counts"]` carries raw integer counts, at least one of `X_umap` / `X_tsne` is present, `var_names` are gene symbols (not Ensembl IDs).
- **Required metadata** — all seven fields above are set and in vocab.
- **Soft warnings** — `doi`, `disease`, `sex`, `age`, `condition`, etc. — strongly recommended but not blocking.

On PASS the sidecar is stamped. Subsequent runs are stat-fast (cached on `file_sha + vocab_sha + check_version`). Re-runs only happen when one of those changes.

If it fails, fix what it tells you and re-run. The `mus h5ad howto` command prints a depositor-side primer with the structural rules, the vocab fields, and the common failure modes:

```bash
mus h5ad howto              # full primer
```

`--hide-soft` on the check suppresses the SOFT warnings group if you only want to see blockers.

### B3. (optional) Write human-readable docs

mus does NOT generate these — but if you write them, they get uploaded.

- `index.md` in the folder — what's in the project, who put it together
- `<basename>.h5ad.md` next to each h5ad — what's special about THIS file, what processing it went through, caveats

Plain markdown, no schema mus enforces. See `mus h5ad howto` §5 for examples.

### B4. Upload to S3 (anthive staging) + iRODS (lab archive)

```bash
mus s3 login                                                  # one-time per machine
mus s3 upload -m "ingest into anthive"  final/microglia_clusters.h5ad
mus irods upload -m "post-QC h5ad for anthive"  final/microglia_clusters.h5ad
```

What lands where:

| Channel | Path | Per-h5ad metadata |
| --- | --- | --- |
| S3 (anthive staging) | `s3://<bucket>/__incoming/<data_project>/<basename>.h5ad` | A **collapsed `.h5ad.mus`** next to the data — single KEY=VALUE file with the merged cascade + sidecar + freshly-stamped `s3_*` block. `mv` keeps data + .mus paired. |
| iRODS (lab archive) | `<irods_home>/project/<data_project>/<exp_name>/<basename>.h5ad` | `mus_*` AVU triplets on the object (queryable via `iron query`) + a per-folder `mus_metadata_<stamp>.tar.gz` holding `mus.cfg` + the `.h5ad.mus` sidecar. |

The S3 prefix `__incoming/` is a staging area — the depositor (or a downstream process) later promotes the data to its final location. mus never writes outside `__incoming/`.

### B5. (optional) Other things on the S3 side

- **`--subfolder NAME`** — places the h5ad under `__incoming/<dp>/NAME/` instead of the data_project root. Useful for revisions / drafts / embargoed releases. Single segment; `static`, `.`, `..` refused.

- **`--static FILE`** — uploads auxiliary assets (figures, JSON, PDFs) to `__incoming/<dp>/static/<basename>`. Repeatable. Always under `static/` regardless of `--subfolder`. Use for things the anthive web view needs that aren't biology data.

  ```bash
  mus s3 upload -m "QC plots for the web view" \
      --static figures/umap.png --static figures/qc.json
  ```

### B6. End-to-end (copy-paste-able)

```bash
cd ~/projects/microglia_pilot

# Folder annotation
mus eln link 12345
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set cell_type 'microglial cell'
mus config set technology 10x_chromium_3p_v3
mus config set suspension_type nucleus
mus config set source internal
mus config set access internal

# Sanity check (gates the upload)
mus h5ad check final/microglia_clusters.h5ad

# Deposit: anthive staging + lab archive
mus s3 upload    -m "ingest into anthive"        final/microglia_clusters.h5ad
mus irods upload -m "post-QC h5ad for anthive"   final/microglia_clusters.h5ad
```

That's pathway B. Done.

---

## Shared concepts (quick reference)

### Annotation lives in two places

- **`mus.cfg`** in a folder. Applies to that folder and every file beneath it. Cascades root-down; deeper overrides shallower. Set once at the project root; refine in subfolders.
- **`<file>.mus`** sidecar next to one specific file. Applies to that file only. Created by `mus tag` / `mus h5ad check` / `mus irods upload` — don't hand-edit.

Per-key precedence: file sidecar > deepest mus.cfg > shallower mus.cfg > root.

### ELN status is mandatory

Every folder mus operates on must be in ONE of two states:

| State | How to set it | Effect |
| --- | --- | --- |
| **linked** | `mus eln link <ID>` | folder ↔ eLab experiment; auto-comments on upload |
| **disabled** | `mus eln disable` (per-folder) or `mus eln disable --global` (once) | upload proceeds without eLab |

A folder with neither set is **undecided** and `mus irods upload` refuses until you pick.

### Vocabulary

The controlled-value lists for `organism`, `tissue`, `cell_type`, `technology`, `disease`, etc. live in [`vocabulary/main.yaml`](https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml). Local copy auto-refreshes every 24 h in the background. Inspect:

```bash
mus vocab show              # everything
mus vocab show tissue       # one field
mus vocab refresh           # force a pull right now
```

Labels, not IDs (`tissue=middle temporal gyrus`, never `UBERON:0002771`). Case + separator variants are silently canonicalised (`HuMaN` → `human`, `prefrontal_cortex` ≡ `Prefrontal Cortex`). Off-vocab values are rejected with a "did you mean?" suggestion; `--force` saves anyway.

Need a new term? File an issue on the mus repo — don't invent locally.

---

## Common pitfalls

- **`data_project not set`** — run `mus eln link 12345` or `mus config set data_project Fiers2025`. Format: `NameYear` (capital first, ends in 4 digits).
- **`no ELN status declared`** — every folder picks consciously: link or disable.
- **`vocabulary changed since last check`** — joint vocab was updated upstream; re-run `mus h5ad check`.
- **`value not in vocabulary`** — `mus vocab show <field>` to see allowed values, or browse <https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml>.
- **`local drift since upload`** on `mus rm` — you (or a process) modified the file after upload. Re-upload, or restore original, then retry.
- **`upload spans N source folders — refusing`** — run mus once per source folder. Each folder has its own cascade.
- **Many small files** — `mus irods upload` will offer to pack into `tar.gz` interactively. Say `t`.
- **Deleted `*.mus` by accident** — recover from git, or `mus irods scan find <sha256>` if you noted the hash.

---

## See also

- [quickstart.md](quickstart.md) — full reference manual: every flag, every env var, every edge case
- `mus h5ad howto` — depositor primer for pathway B (structural rules, vocab, soft fields, failure modes)
- [joint vocabulary](https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml) — controlled-value lists
- UBERON (anatomy): <https://www.ebi.ac.uk/ols4/ontologies/uberon>
- CL (cell types): <https://www.ebi.ac.uk/ols4/ontologies/cl>
- MONDO (diseases): <https://www.ebi.ac.uk/ols4/ontologies/mondo>
- EFO (assays / protocols): <https://www.ebi.ac.uk/ols4/ontologies/efo>

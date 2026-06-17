# mus tutorial

Step-by-step, copy-paste-friendly. The first 5 steps are the same regardless of where the data is going. The 6th step is the only place the two pathways diverge:

| Pathway | When | Final step |
| --- | --- | --- |
| **A. Back up data** | Files (any format) need to land on the lab archive. | `mus irods upload` |
| **B. Prep h5ad for anthive** | A single-cell `.h5ad` needs to enter the lab catalog. | `mus h5ad check` → `mus s3 upload` + `mus irods upload` |

If you don't know which you need, you want **A**. **B** only kicks in when something explicitly asks for an anthive-ready h5ad.

---

## Setup (once per machine, ≈ 2 minutes)

These you only run once on each computer you use.

1. **Install.**
   ```bash
   curl -sSL https://codeberg.org/mfiers/mus/raw/branch/main/install.sh | bash
   ```

2. **iRODS auth.** Mus verifies; if not authenticated yet, it prints the exact `iron auth` command for your account.
   ```bash
   mus irods login
   ```

3. **ELN auth.** Skip with `mus eln disable --global` if you don't have eLab access.
   ```bash
   mus eln login
   ```

4. **Shell completion.**
   ```bash
   mus completion install
   ```

Details on each setup step (XDG paths, secret backends, completion fallbacks): [quickstart.md](quickstart.md).

---

## Step 1 — `cd` into the project folder

Mus reads the cascading `mus.cfg` files in your folder tree. Everything below is run from the project root.

```bash
cd ~/projects/microglia_pilot
```

---

## Step 2 — Link the ELN experiment (recommended)

`mus eln link <ID>` does three things at once: fetches experiment / study / project metadata from the eLab API, sets `eln_experiment_id` + `eln_experiment_name` in `mus.cfg`, and suggests a `data_project` name (see Step 3).

```bash
mus eln link 12345         # 12345 = the numeric ID at the end of the eLab URL
```

To find the ID: open the experiment in eLab. The URL ends in `…/experiment/12345` — the digits are the ID.

**No eLab access?** Skip this step and run once:

```bash
mus eln disable            # this folder only
# OR
mus eln disable --global   # every folder on this machine
```

Without ELN you'll set `eln_experiment_name` by hand in Step 3.

---

## Step 3 — Set `data_project`

`data_project` is the **collection name** under which every upload from this folder will live on the lab archive (`<irods_home>/project/<data_project>/…`). It groups all the deposits from one piece of research into one place.

### 3a. Criteria

- **Format:** `NameYear` — capital first letter, ends in 4 digits. mus rejects anything else.
- **Convention:** first-author surname + publication / acquisition year. Examples: `Fiers2025`, `DeWulf2024`, `Sun2024`.
- **One per project, not per experiment.** Multiple experiments in one paper share the same `data_project`; multiple papers from the same lab use different ones.
- **Unique across the lab.** Two unrelated projects sharing a `data_project` collide on iRODS — they'd write into the same subtree.

### 3b. Avoid duplicates — check what's already in use

Before picking a name, list the projects that already exist on iRODS:

```bash
mus irods projects                # plain list, sorted alphabetically
mus irods projects --details      # adds owner, creation date, last
                                  # upload time (iRODS), and the most
                                  # recent local folder you used for
                                  # each project on this machine
```

Output looks like:

```
DeWulf2024
Fiers2024
Fiers2025
Sun2024
…
```

If your intended name is already there and it's not yours, pick a different one (e.g. add a discriminator: `FiersMicroglia2025`).

To see what's actually IN a project (most recent 20 uploads):

```bash
mus irods project Fiers2025
mus irods project Fiers2025 -n 50    # last 50 instead of 20
```

### 3c. Set it

If you ran `mus eln link` in Step 2, it already prompted you for a `data_project` and wrote it to `mus.cfg`. Verify with `mus config show`.

If you skipped ELN, set it (and the experiment-name label) by hand:

```bash
mus config set data_project Fiers2025
mus config set eln_experiment_name microglia_pilot   # path-safe label for the iRODS subdir
```

---

## Step 4 — Annotate the folder

Annotation cascades. Set fields once at the project root; every file beneath inherits them. `mus.cfg` files in subfolders override the parent for that branch.

### 4a. Minimum recommended (both pathways)

```bash
mus config set organism human
mus config set source internal                # internal  | external
mus config set access internal                # internal  | public
```

### 4b. Biology fields (use them — the more, the better)

These help colleagues find data later via `iron query` and (for pathway B) are required for anthive ingest.

```bash
mus config set tissue 'middle temporal gyrus'
mus config set cell_type 'microglial cell'
mus config set technology 10x_chromium_3p_v3
mus config set suspension_type nucleus        # cell | nucleus | na
mus config set disease 'Alzheimer disease'
mus config set sex pooled
mus config set age '65y-90y'
```

Lists work — comma-separated:

```bash
mus config set tissue 'middle temporal gyrus,hippocampus,frontal cortex'
mus config set cell_type 'microglial cell,astrocyte,oligodendrocyte'
```

### 4c. Subfolder overrides

Use `-C SUBDIR` to write a subfolder's `mus.cfg` without `cd`-ing:

```bash
mus -C final config set access public
mus -C final config set doi 10.1234/microglia.2025
```

### 4d. Pick values from the vocabulary

All controlled-vocab fields are checked against the joint vocabulary as you type. Off-vocab values are rejected with a "did you mean?" suggestion.

```bash
mus vocab show tissue              # one field's allowed values
mus vocab show                     # everything, every field
mus vocab refresh                  # force a fresh pull from upstream
```

Use **labels, not ontology IDs** — `tissue=middle temporal gyrus`, never `UBERON:0002771`. Case + separator variants are silently canonicalised: `HuMaN` → `human`, `prefrontal_cortex` ≡ `Prefrontal Cortex`.

Need a term that isn't in the vocab yet? File an issue on the mus repo — don't invent it locally.

### 4e. Verify

```bash
mus config show                    # full effective cascade
mus config show --local            # only this folder's mus.cfg
```

---

## Step 5 — (Pathway B only) Sanity-check the `.h5ad`

Skip to Step 6 if you're doing pathway A.

For pathway B, `mus h5ad check` is mandatory before upload. It reads the cascade + the file, then checks:

- **Structural rules.** `adata.X` is log-normalised, `adata.layers["counts"]` carries raw integer counts, at least one of `X_umap` / `X_tsne` is present, `var_names` are gene symbols.
- **Required metadata.** Seven fields must be set + in vocab: `organism`, `tissue`, `cell_type`, `technology`, `suspension_type`, `access`, `source`. If you completed Step 4 thoroughly, they're already there.
- **Soft warnings.** Recommended-but-not-blocking: `doi`, `disease`, `sex`, `age`, `condition`, `accession`, `license`, `title`, `contributor`.

```bash
mus h5ad check final/microglia_clusters.h5ad
```

On PASS, the sidecar is stamped (cached on file_sha + vocab_sha + check_version). Re-runs are stat-fast unless one of those changes.

Fail? Fix what it tells you and re-run. `mus h5ad howto` prints the depositor primer (structural rules, vocab field list, common failure modes).

---

## Step 6 — Upload

### Pathway A — Back up to iRODS

```bash
# Single files
mus irods upload -m "raw fastq drop" \
    raw/sample01.fastq.gz raw/sample02.fastq.gz

# A whole folder (mus offers to pack many small files into a tar.gz)
mus irods upload -m "analysis scripts" scripts/

# Several things in one shot — one shared -m message, one ELN comment
mus irods upload -m "initial deposit" \
    scripts/ raw/ final/figure_data.csv
```

- `-m / --message` is **required**. It's stamped on the sidecar, on the iRODS object as an AVU, and prepended to the ELN comment. No silent uploads.
- After upload, each file has a `<file>.mus` sidecar next to it. Keep those — they're how `mus check`, `mus rm`, and `mus irods get` find their way back.

Lands at `<irods_home>/project/<data_project>/<eln_experiment_name>/<basename>`.

### Pathway B — Prep h5ad for anthive (S3 + iRODS)

```bash
mus s3 login                                                  # one-time setup per machine
mus s3 upload    -m "ingest into anthive"  final/microglia_clusters.h5ad
mus irods upload -m "post-QC h5ad"         final/microglia_clusters.h5ad
```

What lands where:

| Channel | Path | Per-h5ad metadata |
| --- | --- | --- |
| S3 (anthive staging) | `s3://<bucket>/__incoming/<data_project>/<basename>.h5ad` | A **collapsed `.h5ad.mus`** next to the data — single KEY=VALUE file with the merged cascade + sidecar + s3_* block. |
| iRODS (lab archive) | `<irods_home>/project/<data_project>/<eln_experiment_name>/<basename>.h5ad` | `mus_*` AVU triplets on the object + a per-folder `mus_metadata_<stamp>.tar.gz` (mus.cfg + sidecar). |

S3 uploads always land under `__incoming/` — a staging area. A downstream process (or you, later) promotes from there to the catalog home.

Optional S3 extras:

```bash
# Place this revision under __incoming/<dp>/rev2/ instead of the data_project root
mus s3 upload -m "rev2 after reviewer comments" \
    --subfolder rev2  final/microglia_clusters.h5ad

# Static assets the anthive web view needs (figures, JSON, PDFs)
mus s3 upload -m "QC figures for the web view" \
    --static figures/umap.png  --static figures/qc.json
```

---

## Step 7 (optional) — Verify, restore, delete

```bash
mus check                           # every *.mus in this dir
mus check -r .                      # recurse

# Once a file is safely uploaded AND remote-checked, free local disk:
mus rm -y final/microglia_clusters.h5ad

# Restore later (sidecar stays as a tombstone):
mus irods get final/microglia_clusters.h5ad.mus
```

`mus rm` refuses to delete unless: sidecar says uploaded, local sha matches, remote checksum matches. After deletion the sidecar is a tombstone — keep it.

---

## End-to-end transcripts

### Pathway A — Back up

```bash
cd ~/projects/microglia_pilot

# Step 2 — ELN link (writes data_project + eln_*)
mus eln link 12345

# Step 3 — verify what's already on the catalog before naming
mus irods projects --details | head -20

# Step 4 — annotate
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set technology 10x_chromium_3p_v3
mus config set source internal
mus config set access internal

# Step 6 — upload
mus irods upload -m "scripts snapshot" scripts/
mus irods upload -m "raw fastq" raw/sample01.fastq.gz raw/sample02.fastq.gz
mus irods upload -m "post-QC h5ad + figure-3 source" \
    final/microglia_clusters.h5ad  final/figure_data.csv
```

### Pathway B — Anthive prep

```bash
cd ~/projects/microglia_pilot

# Step 2 — ELN link
mus eln link 12345

# Step 3
mus irods projects | grep -i fiers      # ensure my chosen name is free

# Step 4 — annotate (includes all seven h5ad-required fields)
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set cell_type 'microglial cell'
mus config set technology 10x_chromium_3p_v3
mus config set suspension_type nucleus
mus config set source internal
mus config set access internal
# soft, but please do
mus config set disease 'Alzheimer disease'
mus config set sex pooled

# Step 5 — sanity check
mus h5ad check final/microglia_clusters.h5ad

# Step 6 — deposit (anthive staging + lab archive)
mus s3 upload    -m "ingest into anthive"  final/microglia_clusters.h5ad
mus irods upload -m "post-QC h5ad"         final/microglia_clusters.h5ad
```

---

## Concepts reference

### Annotation lives in two places

- **`mus.cfg`** in a folder. Applies to that folder and every file beneath it. Cascades root-down; deeper overrides shallower.
- **`<file>.mus`** sidecar next to one specific file. Applies to that file only. Created by `mus tag` / `mus h5ad check` / `mus irods upload` — don't hand-edit.

Per-key precedence: file sidecar > deepest mus.cfg > shallower mus.cfg > root.

### ELN status is mandatory

| State | How to set it |
| --- | --- |
| **linked** | `mus eln link <ID>` |
| **disabled** | `mus eln disable` (per-folder) or `mus eln disable --global` (once per machine) |

A folder with neither set is **undecided** and `mus irods upload` refuses until you pick.

### Vocabulary

| Field | Ontology | Browse |
| --- | --- | --- |
| `tissue` | UBERON labels | <https://www.ebi.ac.uk/ols4/ontologies/uberon> |
| `cell_type` | CL labels | <https://www.ebi.ac.uk/ols4/ontologies/cl> |
| `disease` | MONDO labels | <https://www.ebi.ac.uk/ols4/ontologies/mondo> |
| `technology` | EFO-aligned protocol/chemistry | <https://www.ebi.ac.uk/ols4/ontologies/efo> |

Inspect locally:

```bash
mus vocab show              # everything
mus vocab show tissue       # one field
mus vocab refresh           # force a pull
```

Mus auto-refreshes the cache every 24 h in the background. Set `MUS_SKIP_VOCAB_REFRESH=1` to opt out (CI, sandboxes).

---

## Common pitfalls

- **`data_project not set`** — Step 3 wasn't done. Run `mus eln link 12345` or `mus config set data_project Fiers2025`.
- **`data_project` doesn't match format** — must be `NameYear`: capital first letter, ends in 4 digits.
- **`no ELN status declared`** — every folder picks consciously: `mus eln link <ID>` or `mus eln disable`.
- **`upload spans N source folders — refusing`** — Run mus once per source folder. Each folder has its own cascade.
- **`vocabulary changed since last check`** — Joint vocab was updated upstream; re-run `mus h5ad check`.
- **`value not in vocabulary`** — `mus vocab show <field>` to see allowed values.
- **`local drift since upload`** on `mus rm` — file modified after upload. Re-upload, or restore original, then retry.
- **Many small files** — `mus irods upload` will offer to pack into `tar.gz` interactively. Say `t`.
- **Deleted `*.mus` by accident** — recover from git, or `mus irods scan find <sha256>` if you noted the hash.

---

## See also

- [quickstart.md](quickstart.md) — full reference manual: every flag, env var, edge case
- `mus h5ad howto` — depositor primer for pathway B (structural rules, vocab fields, failure modes)
- [joint vocabulary](https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml) — controlled-value lists
- UBERON: <https://www.ebi.ac.uk/ols4/ontologies/uberon>
- CL: <https://www.ebi.ac.uk/ols4/ontologies/cl>
- MONDO: <https://www.ebi.ac.uk/ols4/ontologies/mondo>
- EFO: <https://www.ebi.ac.uk/ols4/ontologies/efo>

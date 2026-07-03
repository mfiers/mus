# 03 — ELN linking & iRODS upload

Backing data up to the KU Leuven Mango iRODS catalog with provenance + an audit trail in eLabJournal.

This doc assumes you've done [01 — install & setup](01_install_and_setup.md) and read [02 — annotation](02_annotation_and_vocabularies.md). You have a project folder, a `mus.cfg` with `data_project` + biology fields, and `iron` is authenticated.

---

## 1. Link the folder to an eLab experiment

Skip to Step 1b if you don't use eLab.

```bash
cd ~/projects/microglia_pilot
mus eln link 12345
```

`12345` is the numeric ID at the end of the eLab URL (`https://vib.elabjournal.com/.../experiment/12345`).

`mus eln link` does three things:

1. Calls the eLab API to fetch the experiment's name, study, project, and collaborators.
2. Asks you for a `data_project` name (suggesting `<first-author-surname><year>` from the eLab data, OR re-using whatever a teammate already set for this experiment — mus caches eln→data_project mappings across folders).
3. Writes six keys to the folder's `mus.cfg`:
   - `eln_experiment_id` / `eln_experiment_name`
   - `eln_study_id` / `eln_study_name`
   - `eln_project_id` / `eln_project_name`
   - plus `data_project` (if you didn't already have it)

Refuses to overwrite an existing linkage; use `mus eln update` to refresh after a server-side rename, or `mus eln link <NEW_ID> --force` to relink to a different experiment.

### 1b. No eLab access

Set the two fields `mus eln link` would have written by hand:

```bash
mus config set data_project Fiers2025          # NameYear format, mandatory
mus config set eln_experiment_name microglia_pilot   # path-safe label, mandatory
mus eln disable                                 # marks THIS folder as non-ELN
```

The key is called `eln_experiment_name` for historical reasons; for non-ELN users it's just a folder label. Will become the last path component on iRODS (`<irods_home>/project/<data_project>/<eln_experiment_name>/<basename>`).

If you never have eLab access:

```bash
mus eln disable --global         # once per machine, in ~/.config/mus/mus.cfg
```

### 1c. Register file locations as ELN samples (`mus eln sample`)

For data files that live on the NAS / local disk and shouldn't be duplicated inside the notebook, register each file as a **Sample** record in eLabNext. The sample carries the file's ABSOLUTE PATH + sha256 + size in its `note` field — the data itself never leaves the disk.

```bash
mus eln sample -m "raw sequencing lanes, run 42" *.fastq.gz
# → creates one Sample per file, links all to the current experiment
#   via a new SAMPLESOUT section
```

Per file:
- `name` = file basename
- `description` = your `-m` message
- `note` = `path=<abs>\nsha256=<hex>\nsize=<n>\nmtime=<iso>`

Flags:
- `--type NAME` — sample type name on the tenant (default `Data`); mus refuses if the tenant doesn't have it, listing what's available
- `--section samplesout | samplesin` — SAMPLESOUT (results, outputs — default) or SAMPLESIN (inputs the experiment consumed)

Idempotent — the sidecar records `eln_sample_id`, so re-runs skip already-registered files.

### 1d. Attach file bytes to the ELN experiment (`mus eln attach`)

For protocols, figures, small documents that belong INSIDE the notebook page, upload the file's actual bytes to a new FILE section on the current experiment:

```bash
mus eln attach -m "post-QC UMAP + supplementary methods" umap.pdf methods.docx
```

One FILE section per invocation; every file in the batch attaches to that section. Sidecar stamps `eln_attachment_journal_id`.

**Which one to use:**
- `mus eln sample` — data files that stay on the NAS; ELN just records where they live
- `mus eln attach` — documents/figures small enough to duplicate in the notebook

---

## 2. Upload to iRODS

Every upload needs a `-m / --message` describing what / why. Like a git commit message — stamped on the sidecar, on the iRODS object as a `mus_upload_message` AVU, and prepended to the auto-comment on the eLab experiment.

### 2a. Single file

```bash
mus irods upload -m "raw fastq from VIB seq core" raw/sample01.fastq.gz
```

What mus does, in order:
1. Reads / refreshes `<file>.mus` sidecar (computes sha256 if new or stale).
2. Builds the remote path: `<irods_home>/project/<data_project>/<safe_experiment_name>/<basename>`.
3. Calls `iron upload --verify-checksum` (transport-integrity check).
4. Calls `iron stat -j` to grab the iRODS catalog ID.
5. Writes the sidecar with `irods_path`, `irods_url` (path-based browse), and `irods_purl` (persistent URL keyed on catalog ID — survives renames + moves).
6. Stamps `mus_*` AVU metadata on the iRODS object (so it's self-describing without the local sidecar).
7. Posts a one-line comment to the linked eLab experiment with the persistent URL.

### 2b. Multiple files in one shot

```bash
mus irods upload -m "raw fastq drop" \
    raw/sample01.fastq.gz raw/sample02.fastq.gz raw/sample03.fastq.gz
```

One message, one ELN comment, three uploads. Each gets its own sidecar.

### 2c. Folder upload

```bash
mus irods upload -m "analysis scripts" scripts/
```

Mus profiles the folder first (file count + total bytes + median file size + a density score = `median_bytes / file_count²`).

If the density check fails — too many small files for direct upload to be efficient on iRODS — mus prompts:

```
scripts/ — 47 files, total 84 KB, median 1.2 KB
  density check: 0.4 (need ≥ 10) — too many small files
  → too many small files for direct upload; pick:
    [a] archive: pack as tar.gz and upload the archive
    [u] unarchived: upload the folder as-is anyway
    [c] cancel
  Choice [a/u/c]:
```

- **`a`** (archive) — mus tar+gzips locally to `scripts.tar.gz`, uploads the archive. Recommended for many small files; iRODS per-object overhead dominates for tiny things.
- **`u`** (unarchived) — uploads as a collection. The directory tree is browsable in the Mango web UI. Use for a small number of larger files.

Force either non-interactively:

```bash
mus irods upload --pack tar.gz -m "..." scripts/    # always archive
mus irods upload --pack none   -m "..." scripts/    # always upload as collection
mus irods upload -A            -m "..." scripts/    # short flag for --pack tar.gz
```

After a folder upload, ONE sidecar at `scripts.mus` (sibling of the folder, NOT inside). Carries a Merkle hash so `mus check scripts.mus` later can detect any drift in the source.

If you packed, the local `scripts.tar.gz` is **kept by default** so a re-upload doesn't need to re-pack. Pass `--cleanup-archive` to delete it after.

### 2d. Whole project at once

```bash
mus irods upload -m "initial deposit" scripts/ raw/ final/data.h5ad
```

One message applies to all three; one ELN comment lists everything.

---

## 3. Where the data lands

```
<irods_home>/project/<data_project>/<safe_experiment_name>/<basename>
```

For BADS: `<irods_home>` defaults to `/gbiomed/home/BADS`. `<safe_experiment_name>` is `eln_experiment_name` sanitised to `[A-Za-z0-9_]+`, capped at 60 characters.

Example: `final/microglia_clusters.h5ad` → `/gbiomed/home/BADS/mus/project/Fiers2025/microglia_pilot/microglia_clusters.h5ad`.

Each iRODS object carries `mus_*` AVU triplets — biology + provenance — queryable via `iron meta ls` or `iron query`. AVU is iRODS-speak for "attribute-value-unit" key-value metadata.

---

## 4. Verify integrity, anytime

```bash
mus check                           # every *.mus in this dir
mus check -r .                      # recurse
mus irods check                     # local sha256 vs iron's remote checksum
mus irods check --deep scripts.mus  # walk the iRODS tree for as-is folders
```

`mus check` is fast (stat + cached sha256 lookup). `mus irods check --deep` is slow for big as-is folders but is the only way to confirm bytes-on-disk match bytes-on-iRODS file-by-file.

The output is one line per check (status + check name + concise note). `--silent` suppresses OK / SKIP, leaving failures + summary.

---

## 5. Safely delete local data

After data is on iRODS, free local disk. Use `mus rm`, not `rm`. It refuses to delete unless three checks pass:

1. Sidecar says the file is uploaded (`irods_status=uploaded`).
2. Fresh local sha256 matches the sidecar.
3. Fresh local sha256 matches the remote checksum (no remote drift / loss).

```bash
mus rm --dry-run final/microglia_clusters.h5ad   # show plan, touch nothing
mus rm final/microglia_clusters.h5ad             # prompts y/N
mus rm -y scripts/ raw/                          # skip prompt, batch
```

By default, `mus rm` keeps the `*.mus` sidecar as a **tombstone** (`irods_status` flips to `local_deleted`). The tombstone is your handle to restore the data later. Don't delete sidecars unless you're sure.

---

## 6. Restore from a tombstone

A tombstoned sidecar carries `irods_path`, `irods_purl`, and sha256 — everything needed to download the data back.

```bash
mus irods get final/microglia_clusters.h5ad.mus
mus irods get scripts.mus
mus irods get final/*.mus              # all sidecars in final/
```

Mus recomputes the hash on download and compares to the sidecar. Mismatch → loud `PROVENANCE MISMATCH` error, file left in place for you to inspect.

`mus irods get` also pulls the current AVU back from iRODS and folds it into the local sidecar — catches the case where a colleague enriched metadata on the catalog after the original upload.

---

## 7. Inventory of iRODS

```bash
mus irods projects                  # alphabetical list of every data_project on iRODS
mus irods projects --details        # + files, size, uploaders, last upload, local folder
mus irods project Fiers2025         # drill into one project; most recent 20 uploads
mus irods project Fiers2025 -n 50   # last 50

mus irods find --organism human --cell-type 'microglial cell'    # AVU query
mus irods scan run /gbiomed/home/BADS    # cache a whole-tree snapshot (slow)
mus irods scan find <sha256>             # find a file across the lab tree by hash
```

---

## End-to-end example

```bash
cd ~/projects/microglia_pilot

# Step 1 — link experiment (writes mus.cfg with eln_* + data_project)
mus eln link 12345

# Step 2 — annotate (recap from doc 02)
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set technology 10x_chromium_3p_v3
mus config set cell_type 'microglial cell'
mus config set source internal
mus config set access internal

# Step 3 — sanity check what's already in the project
mus irods projects --details | grep Fiers2025 || echo "Fiers2025 is fresh"

# Step 4 — upload (one message per logical batch)
mus irods upload -m "scripts snapshot for figure 3" scripts/                  # offers to pack
mus irods upload -m "raw fastq drop"                 raw/*.fastq.gz
mus irods upload -m "post-QC final h5ad + figure-3 source data" \
    final/microglia_clusters.h5ad final/figure_data.csv

# Step 5 — free local disk after a clean remote-check
mus rm -y final/microglia_clusters.h5ad
ls final/
# microglia_clusters.h5ad.mus    ← tombstone, keep it
# figure_data.csv
# figure_data.csv.mus
# mus.cfg

# Step 6 — restore months later when you need the h5ad back
mus irods get final/microglia_clusters.h5ad.mus
```

---

## Common pitfalls

- **"data_project not set"** — `mus eln link 12345` or `mus config set data_project Fiers2025`.
- **"no ELN status declared"** — every folder picks consciously: link or disable. No implicit non-ELN.
- **"upload spans N source folders — refusing"** — Run mus once per source folder. The collapsed-sidecar key would collide across folders sharing the same data_project; the gate exists to prevent silent overwrites.
- **Many small files** — let mus pack into `tar.gz`. iRODS per-object overhead dominates for tiny objects.
- **`local drift since upload`** on `mus rm` — you (or a process) modified the file after upload. Re-upload, or restore the original from iRODS, then retry.
- **Deleted `*.mus` by accident** — recover from git, or find the iRODS object via `mus irods scan find <sha256>` if you noted the hash.

---

## What's next

- **[04 — h5ad deposit](04_h5ad_deposit.md)** — prep single-cell `.h5ad` files for the anthive catalog (extra structural + vocab checks, S3 upload).
- **[05 — DE deposit](05_de_deposit.md)** — prep differential-expression result tables.

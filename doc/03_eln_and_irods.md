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

**Goal:** point the ELN at data files on disk without ever copying the bytes. Every file becomes a Sample record; every batch invocation groups them under one fresh SAMPLESOUT (or SAMPLESIN) section on the linked experiment.

```bash
mus eln sample -m "raw sequencing lanes, run 42" *.fastq.gz
```

Output:

```
sample type: Data (id=1234)
  storage: BADS Mango (storageID=42, root layer=1001)
  ✓ lane_01.fastq.gz → sample 17890001
  ✓ lane_02.fastq.gz → sample 17890002
  ✓ lane_03.fastq.gz → sample 17890003
created section (expJournalID=16148199) on experiment 1955928
linked 3 sample(s) to section 16148199 on experiment 1955928

registered 3 (fresh 3 + recreated 0 + relinked orphans 0), skipped 0, failed 0
```

Each Sample carries:

| Sample field | Value written |
| --- | --- |
| `name` | file basename |
| `description` | your `-m` message |
| `note` | fallback provenance dump — `path=<abs>\nsha256=<hex>\nsize=<n>\nmtime=<iso>` (still written for tenants without meta-field setup — see below) |
| `sampleMetas` | **auto-fills any of the tenant's declared meta fields** (see [meta autofill](#meta-field-autofill)) |
| `storageLayerID` | root compartment of the configured [storage unit](#storage-location) — omitted if not configured |

**Requires** `eln_experiment_id` in the mus.cfg cascade — set by `mus eln link <ID>` (see §1). The command refuses if unset.

**Flags:**

| Flag | Purpose |
| --- | --- |
| `-m / --message` | REQUIRED. Becomes the sample description AND the section header (first line). |
| `--type NAME` | Sample type name on the ELN tenant. Default `Data`. Case-insensitive. Refuses with the full list of available types if the name is unknown. |
| `--section samplesout \| samplesin` | Section kind. **SAMPLESOUT** (default) = outputs the experiment produced; **SAMPLESIN** = inputs the experiment consumed. |

Multiple files → one invocation → one section, all sharing the same `-m` message. Every file gets its own sample record.

#### Meta-field auto-fill

If the sample type on your tenant has custom meta fields (managed by an ELN admin in Sample Types → Settings), mus fills any it recognises automatically — case-insensitive match against the tenant's declared keys.

For `mus eln sample` (local-file flow), the values are:

| Meta key | Value mus fills |
| --- | --- |
| `path` | absolute local path |
| `upload_from_path` | absolute local path (same as `path` for this flow — see §2a for the iRODS flow, where they differ) |
| `sha256sum` / `sha256` | file's sha256 hex |
| `size_bytes` / `size` | file size in bytes |
| `mtime` | file mtime ISO-8601 (UTC) |
| `upload_data` / `upload_date` | invocation timestamp (RFC3339 UTC) |
| `upload_user` | OS username |
| `upload_from_server` | machine hostname |
| `kind` | always `file` for this flow |
| `mus_version` | version of mus that ran the invocation |
| `purl` / `mango_purl` | **not set** for `mus eln sample` — only populated by the iRODS flow (both keys accept the same value) |

Fields the tenant hasn't declared are silently skipped. mus never creates ad-hoc meta fields (they'd be unsearchable in the ELN inventory per eLabNext's docs). Fields the tenant HAS declared but mus doesn't know about pass through untouched.

To add these to your sample type: ELN → Sample Types → [Data] → Custom Meta → +Add Meta Field (Type TEXT, key exactly as above). One-time admin setup.

#### Storage location

You can place every mus-created sample inside a named **storage unit** (freezer / cabinet / rack) on the ELN — the samples land in the unit's root compartment; no fixed position. Aid for discoverability: mus samples end up grouped in the ELN's storage browser instead of drifting rootless.

`mus eln sample` reads the key `eln_sample_location`. Resolution order (first non-empty wins):

1. `eln_sample_location=<name>` in the mus.cfg cascade (per-folder overrides `~/.config/mus/mus.cfg`).
2. `eln_sample_location` in the secret store (sits next to `eln_url` / `eln_apikey`).

**Nothing configured → nothing sent.** No compile-time default — samples are created without a placement, and the ELN shows them in the flat inventory listing. Labs that want the placement opt in explicitly.

If a name IS configured but doesn't match any storage unit on the tenant (typo, unit archived), mus prints one stderr warning and creates the samples anyway — unplaced. The command never fails on this.

mus never CREATES a storage unit — an ELN admin needs to set it up in the UI once (Inventory → Storage → +New Storage). mus only looks it up by name.

> **iRODS uploads use a DIFFERENT key** — `eln_irods_sample_location` — so a lab can route iRODS-archived samples to a permanent-archive freezer while local samples stay elsewhere. See §2a.

#### Idempotency: safe to re-run

After a successful run the sidecar stamps four keys:

```
eln_sample_id=17890001
eln_sample_type=Data
eln_sample_created_at=2026-06-24T12:34:56Z
eln_sample_section_id=16148199
```

On a re-run, mus makes ONE `GET /samples/{id}` per file to check the current server-side status and classifies each file into one of five buckets:

| Situation | mus does | Output marker |
| --- | --- | --- |
| Sample active + section link recorded | Skip | `↷ file.tsv already registered as sample 17890001 — skipping` |
| Sample active, section link missing | **Re-link** existing sample to a new section | `↺ file.tsv: re-linking orphan sample 17890001` |
| Sample archived on the ELN | **Recreate** — create a fresh sample, replace the sidecar SampleID | `↻ file.tsv: prior sample 17890001 archived → new sample 17892500` |
| Sample deleted from the ELN (404) | **Recreate** as above | `↻ file.tsv: prior sample 17890001 gone from ELN → new sample 17892500` |
| No sidecar / no SampleID | Create fresh | `✓ file.tsv → sample 17892500` |

The summary line breaks these down:

```
registered N (fresh A + recreated B + relinked orphans C), skipped D, failed E
```

If EVERY file is in the "skip" bucket, mus does not create an empty section on the experiment — it prints `nothing to do — no section created` and exits.

#### Why the archived-check matters

Depositors will occasionally archive samples from the ELN UI (say, after fixing a mislabeled file) and then re-run `mus eln sample` expecting a clean re-register. Without the archived-check mus would happily skip those files — the sidecar carries a `SampleID` and looks fully-registered. mus catches this by querying the sample's current state on every re-run.

If you archive a sample AND want mus to recreate it, that's now automatic. If you want to force a fresh sample without archiving the old one, delete the `eln_sample_id` line from the file's `.mus` sidecar and re-run.

#### Recovery from partial failures

If the batch-link step fails mid-run (network glitch, HTTP error, section-creation succeeded but link didn't), mus writes:
- `eln_sample_id` (the sample was created — that's committed on the server)
- but NOT `eln_sample_section_id` (the link never landed)

That combination is the **orphan marker**. On the next `mus eln sample` invocation mus detects it, does NOT create a new sample, and re-uses the existing sample ID in the new section's batch-link. Failed batch-links are fully self-healing — just run the command again.

#### Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `no eln_experiment_id in cascade` | Folder not linked yet | `mus eln link <ID>` (see §1) |
| `no sample type named "X"` | `--type` doesn't match tenant | Command lists what's available; pick one or ask an ELN admin to add "Data" |
| `HTTP 405: … does not support http method 'POST'` | Binary is mus < 0.2.42 | `mus upgrade` — the link endpoint requires PUT |
| Duplicate sample records in Inventory → Samples | Binary is mus < 0.2.41 (double-POST bug on non-object response shapes) | `mus upgrade`. The duplicates left behind are orphan samples with no experiment link — safe to bulk-archive from the ELN UI. |
| Section created but empty on the ELN | Everything got skipped as "already registered"; from 0.2.41 the classification pass prevents this by NOT creating a section when there's nothing to link | Upgrade; or delete the sidecar's `eln_sample_id` line and re-run |
| `lookup sample N: HTTP 401` | ELN API key expired | `mus eln login` |
| `⚠ ELN storage unit "…" not found` | Typo in `eln_sample_location`, or the unit was archived | `mus secret set eln_sample_location "<exact name>"` — check spelling; ELN unit names are case-sensitive on lookup |
| Repeated re-runs create new sections every time | Working as intended — each invocation carries its own message, so it becomes its own section. Group your samples per intended section. | Don't split one batch across multiple commands |

### 1d. Attach file bytes to the ELN experiment (`mus eln attach`)

**Goal:** copy a file INTO the notebook — the bytes are uploaded to a FILE section on the current experiment. Use this for the artefacts that belong on the notebook page itself (protocols, figures, small tables, PDFs).

```bash
mus eln attach -m "post-QC UMAP + supplementary methods" umap.pdf methods.docx
```

Output:

```
created FILE section (expJournalID=16148200) on experiment 1955928
  → umap.pdf
  → methods.docx
```

**Per invocation:** one new FILE section on the experiment. Every file in the batch attaches to that section. The section header is `mus eln attach: <first line of -m>`.

**Requires** `eln_experiment_id` in the cascade — same as `mus eln sample`.

#### Idempotency

Sidecar stamps `eln_attachment_journal_id` (the section's expJournalID) after each successful upload. Re-runs skip any file already carrying that stamp:

```
  ↷ umap.pdf already attached (section 16148200) — skipping
✓ nothing to upload — every file already has an attachment recorded
```

To force a re-upload, remove `eln_attachment_journal_id` from the sidecar. (There is currently no `--force` flag; the deliberate friction is to prevent accidental duplicates on the notebook page.)

### 1e. `sample` vs `attach` — which one?

|   | `mus eln sample` | `mus eln attach` |
| --- | --- | --- |
| Data location | Stays on disk (NAS) | Uploaded into the ELN |
| Bytes touched | `sha256` only | Full read → HTTP upload |
| Fits well for | Raw sequencing, h5ad, count matrices, anything > ~100 MB | Protocols, small figures, PDF reports, presentations |
| Section type created | SAMPLESOUT or SAMPLESIN | FILE |
| Sidecar keys stamped | `eln_sample_id`, `eln_sample_type`, `eln_sample_created_at`, `eln_sample_section_id` | `eln_attachment_journal_id`, `eln_attachment_uploaded_at` |
| Round-trip verifiable | Yes — `sha256` in the sample note | No — ELN doesn't expose an integrity hash |
| Right choice if… | The file will be uploaded to iRODS / stays on NAS | The file is documentation ABOUT the experiment |

Rule of thumb: **data → `sample`; documentation → `attach`**. When in doubt, `sample` — the data-on-disk model is closer to how the lab actually works.

---

## 2. Upload to iRODS

Every upload needs a `-m / --message` describing what / why. Like a git commit message — stamped on the sidecar, on the iRODS object as a `mus_upload_message` AVU, and carried into the auto-created ELN sample records as the sample description + SAMPLESOUT section header.

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
7. **Registers each uploaded file as a Sample record on the linked eLab experiment** — one fresh SAMPLESOUT section per invocation, one sample per file. Same shape and same command output as `mus eln sample` (§1c) — same idempotency, orphan-recovery, archived-detection, and empty-section suppression.

The **only differences** between the two flows land in the sample's meta fields:

| Meta key | `mus eln sample` | `mus irods upload` |
| --- | --- | --- |
| `path` | absolute local path | **iRODS path** — the archived copy is the authoritative location |
| `upload_from_path` | absolute local path (same as `path`) | absolute local path (the source, distinct from `path`) |
| `purl` / `mango_purl` | not set | **iRODS persistent URL** (survives moves on Mango) — both keys accept the same value; use `mango_purl` on BADS Mango, `purl` is the generic alias |
| `kind` | always `file` | `file` / `folder` / `archive` — matches what actually landed on iRODS |
| everything else | (see §1c meta-autofill table) | same as `mus eln sample` |

Step 7 is best-effort — a failure leaves the iRODS upload untouched (data is safe; the sample record is what didn't happen). Re-run to retry; mus's orphan-recovery picks up mid-batch failures automatically.

`--no-eln` (deprecated alias: `--no-eln-comment`) opts out of the sample-registration entirely.

**Storage-unit placement:** iRODS-uploaded samples use the key `eln_irods_sample_location` — **distinct from** `eln_sample_location`. Same cascade > secret resolution, same "nothing configured → no placement" semantic. The two keys do NOT cross-fall-back: a lab can route iRODS-archived samples to a permanent-archive freezer while local samples stay in a working freezer — set only one of the two, or set both.

```sh
# Recommended: set both to the same value if you want unified placement
mus secret set eln_irods_sample_location "BADS Mango"
mus secret set eln_sample_location       "BADS Mango"
```

### 2b. Multiple files in one shot

```bash
mus irods upload -m "raw fastq drop" \
    raw/sample01.fastq.gz raw/sample02.fastq.gz raw/sample03.fastq.gz
```

One message, three uploads, one SAMPLESOUT section on the experiment with three sample records. Each file gets its own sidecar; each sample carries the file's individual iRODS path + sha256 in its meta fields.

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

One message applies to all items; one SAMPLESOUT section on the experiment lists every uploaded file as its own sample record. Folder / archive uploads become individual samples too, with the `kind` meta field set to `folder` or `archive` so they're distinguishable in the ELN inventory.

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

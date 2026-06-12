# mus tutorial — backing up research data

A short, opinionated walkthrough for BADS depositors. Companion to
[quickstart.md](quickstart.md) (which is the reference manual).

---

## 1. Install & log in (once per machine)

```bash
curl -sSL https://codeberg.org/mfiers/mus/raw/branch/main/install.sh | bash
```

Then let mus walk you through each login.

**iRODS auth:**

```bash
mus irods login
```

If you're already authenticated, it prints ✓ and exits. Otherwise it
prints the exact command to run — for the BADS Mango zone that's:

```bash
iron auth <u-or-r-number> gbiomed gbiomed.irods.icts.kuleuven.be
```

(`u0123456` for staff, `r0123456` for students.) `iron auth` prompts
for password / SSO, writes the environment file, and caches the
credential under `~/.irods/`. Re-run `mus irods login` to confirm ✓.

**ELN auth:**

```bash
mus eln login
```

Interactive wizard — it tells you how to get a token from the eLab
UI (Apps & Connections → Manage authentication → Generate access
token), verifies it works, then stores it via mus's secret backend
(OS keyring on a desktop, age-encrypted file on headless hosts).

No ELN access? Run `mus eln disable --global` once and skip this.

---

## 2. What to back up

Three categories, regardless of how your project tree is shaped:

- **scripts / code** — the pipeline that turns raw → final. If lost
  you cannot reproduce the analysis.
- **raw data** — primary measurements you cannot regenerate: fastq,
  proteomics raw files, microscope acquisitions, instrument exports.
- **final / publication-ready data** — the curated h5ad, the figure
  source tables, the manuscript supplement.

Skip anything you can regenerate from those three with a `make` run
(BAMs, count matrices, intermediate normalisations, plots). The point
of mus is to back up what's irreplaceable, not to mirror your whole
working tree.

---

## 3. A worked example

For the rest of the tutorial we'll use a toy single-cell pilot. Layout:

```
~/projects/microglia_pilot/
  mus.cfg                     ← project-level annotation
  README.md
  scripts/
    01_qc.py
    02_normalize.py
    03_cluster.py
  raw/
    mus.cfg                   ← override: raw-specific annotation
    sample01.fastq.gz
    sample02.fastq.gz
  intermediate/               ← NOT backed up (regeneratable from raw)
    sample01.bam
    sample01.counts
  final/
    mus.cfg                   ← override: published-ready annotation
    microglia_clusters.h5ad
    figure_data.csv
```

Adapt the directory names to your own conventions — mus doesn't care
about the layout, only about which `mus.cfg` files it finds and which
files you ask it to upload.

---

## 4. How annotations work — folder vs file

Two places annotation lives:

- A **`mus.cfg`** file inside a folder. It applies to **that folder
  AND every file and subfolder beneath it, recursively** — set it
  once at the project root and it covers the entire tree. Subfolders
  can add their own `mus.cfg` to refine the parent's values for just
  that branch.
- A **`*.mus` sidecar** sitting next to one specific data file
  (`foo.h5ad` → `foo.h5ad.mus`). It applies to **only that one
  file** — never spreads, never inherits to siblings.

Don't edit either by hand — mus has commands for both.

### Folder annotation — set with `mus config set`

The primary command. Each call writes one key into the local folder's
`mus.cfg` (creating the file if needed):

```bash
cd ~/projects/microglia_pilot

mus config set data_project Fiers2025
mus config set eln_experiment_id 12345
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set technology 10x_chromium_3p_v3
mus config set suspension_type nucleus
mus config set source internal
mus config set access internal
```

What ends up on disk (you can also hand-edit, but the command is
safer — it validates `data_project` against the `NameYear` pattern
and uses atomic writes so a Ctrl-C can't leave a half-written file):

```
# ~/projects/microglia_pilot/mus.cfg
access=internal
data_project=Fiers2025
eln_experiment_id=12345
organism=human
source=internal
suspension_type=nucleus
technology=10x_chromium_3p_v3
tissue=middle temporal gyrus
```

Inspect what's effective at any point in the tree:

```bash
mus config show              # merged cascade
mus config show --local      # just this folder's mus.cfg
mus config files             # paths of every mus.cfg that contributes
```

**Legacy `.env` support.** Before 0.2.12 mus used `.env` as the
folder-config file. That name collided with the dotenv ecosystem
(direnv, docker-compose, python-dotenv, …). `mus.cfg` is the new
canonical name; `.env` is still read for backwards compatibility.
Move existing projects over with:

```bash
mus config migrate            # run once per project root
```

Mus-recognised keys move to `mus.cfg`. Anything that looks like a
secret (`DATABASE_URL`, `AWS_*`, `*_TOKEN`, …) or that mus doesn't
recognise stays in `.env` with a loud warning so you can review it.
Running `mus config set` in a folder with a legacy `.env` triggers
this migration automatically the first time, with the same report.

**Cascade rule:** mus collects every `mus.cfg` from `/` down to your
working directory and merges them in that order; closer-to-cwd
`mus.cfg` files override shallower ones. Every file under
`microglia_pilot/` inherits the values above without you having to
repeat them in `raw/` or `final/`.

Subfolders override the parent — same command, run from the subfolder:

```bash
# Override access for the published version in final/
mus -C final config set access public
mus -C final config set doi 10.1234/microglia.2025
```

The `-C DIR` flag works like `git -C` — run mus as if it were
invoked from `DIR`. The resulting `final/mus.cfg` overrides
`access=internal` from the parent; `organism`, `tissue`, …
cascade through unchanged.

Lists are comma-separated:

```bash
mus config set tissue 'middle temporal gyrus,hippocampus,frontal cortex'
mus config set disease 'Alzheimer disease'
```

Remove a key:

```bash
mus config unset access
```

`unset` preserves any comments in the `mus.cfg` and only deletes the
file if it ends up truly empty (no keys, no comments).

### File annotation — set with `mus tag` (and auto by upload)

The `*.mus` sidecar lives next to the data file (`foo.h5ad` →
`foo.h5ad.mus`). It records the file's sha256 + size + mtime and a
snapshot of the resolved cascade at the moment the file was tagged.
Applies to **only that one file.**

Create or refresh a sidecar:

```bash
mus tag final/microglia_clusters.h5ad
mus tag final/microglia_clusters.h5ad \
    -m "post-QC final figure source" \
    -t qc-pass -t figure3
```

What ends up on disk (mus generates this; do not hand-edit):

```
# final/microglia_clusters.h5ad.mus
access=internal
data_project=Fiers2025
eln_experiment_id=12345
hashed=2026-06-03T10:00:00Z
mtime=2026-06-03T09:55:12Z
note=post-QC final figure source
organism=human
sha256=acb5475ab89a05d813c8dc0c987c4d2198d319fc21b8df1551cde4e3316fd2d7
size=1342515200
suspension_type=nucleus
tags=qc-pass,figure3
technology=10x_chromium_3p_v3
tissue=middle temporal gyrus
version=1
```

`mus tag` is also what `mus irods upload` does first — it makes sure
the sidecar exists with a fresh sha256 before uploading.

`mus tag` flags for sidecar-only content (rest comes from the
cascade):

| Flag | Purpose |
| --- | --- |
| `-m / --note` | free-text note on this file |
| `-t / --tag` | tag (repeatable) |
| `-f / --force` | rehash even if the cached sha is still valid |

For a per-file annotation that isn't a note or tag (a single sample
with a different `tissue`, one file you want `access=public` while
the rest are `internal`), today the cleanest path is a per-subfolder
`mus.cfg` + move the file there. A future `mus tag --set key=value`
flag is on the roadmap.

Rule of thumb: **the mus.cfg is your recipe; the `*.mus` sidecar is what
actually happened.**

### Where they live in version control

- `mus.cfg` files at the project root SHOULD be committed to git — they
  describe the dataset.
- `*.mus` sidecars SHOULD be committed too. They're tiny (~1 KB), plain
  text, and after `mus rm` they're the only handle that can restore
  the data (see §12). `mus tag` and `mus irods upload` update them in
  place without clobbering fields they don't touch — diff-friendly.

---

## 5. The required minimum

A folder mus uploads from needs exactly two facts:

| Required | Why |
| --- | --- |
| `data_project` (e.g. `Fiers2025`) | Path component on iRODS; groups all data from one project under one collection. Format: `NameYear` (capital first letter, ends in 4 digits). |
| `eln_experiment_id` (or `eln_disabled=true`) | Links each upload back to a lab notebook entry so anyone can find context. |

The easiest way to set both is `mus eln link <experiment_id>`:

```bash
cd ~/projects/microglia_pilot
mus eln link 12345
```

This:
1. Fetches the experiment's name, study, project, and collaborators
   from eLab.
2. Suggests a `data_project` name (collaborator surname + year, or
   the one your colleagues already used for this experiment if mus
   can find it in the lab's iRODS catalog).
3. Writes `eln_experiment_id`, `eln_experiment_name`, `data_project`,
   and the rest into `~/projects/microglia_pilot/mus.cfg`.

No ELN access? Set them by hand:

```bash
mus config set data_project Fiers2025
mus config set eln_experiment_name microglia_pilot
mus eln disable
```

---

## 6. Recommended: source & access

Two orthogonal axes. Set both at the folder level.

| Key | Values | Meaning |
| --- | --- | --- |
| `source` | `internal` | Produced in our lab. |
|  | `external` | Downloaded from a public dataset (CellxGene, GEO, ArrayExpress, …). |
| `access` | `internal` | Lab-only — unpublished or under embargo. |
|  | `public` | Published, shareable, no embargo. |

```bash
mus config set source internal
mus config set access internal
```

For the published version in `final/`:

```bash
mus -C final config set access public
mus -C final config set doi 10.1234/microglia.2025
```

---

## 7. Biology annotation (use it everywhere)

These fields are useful for **any data file**, not only h5ad — a BAM
from a single sample carries the same organism / tissue / disease
that the h5ad downstream of it carries. Set them once at the project
root and they cascade to every upload.

All values are drawn from a controlled vocabulary:

- Live: <https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml>
- Local: `mus vocab show`

Field → ontology mapping:

| Field | Ontology | Browse |
| --- | --- | --- |
| `tissue` | UBERON labels | <https://www.ebi.ac.uk/ols4/ontologies/uberon> |
| `cell_type` | CL labels | <https://www.ebi.ac.uk/ols4/ontologies/cl> |
| `disease` | MONDO labels | <https://www.ebi.ac.uk/ols4/ontologies/mondo> |
| `technology` | EFO-aligned protocol/chemistry | <https://www.ebi.ac.uk/ols4/ontologies/efo> |

Use **labels**, not IDs. `tissue=middle temporal gyrus` — not
`UBERON:0002771`. mus validates against the vocabulary; labels are
the primary key, IDs are optional companion fields.

Example for the microglia pilot:

```bash
mus config set organism human
mus config set tissue 'middle temporal gyrus,hippocampus'
mus config set cell_type 'microglial cell'
mus config set disease 'Alzheimer disease'
mus config set technology 10x_chromium_3p_v3
mus config set suspension_type nucleus    # cell / nucleus / na
mus config set sex pooled
mus config set age 65y-90y
```

Lists are comma-separated. Multi-word labels with embedded commas
(`"leukoencephalopathy, diffuse hereditary, with spheroids 1"`) get
double-quoted automatically.

The more you fill in, the easier it is to find this dataset later via
`iron query` (e.g. "all internal AD-microglia data the lab has") and
the richer the AVU metadata stamped on the iRODS object.

---

## 8. Pack & upload

Every `mus irods upload` requires a **`-m / --message`** describing
what / why — like a git commit message. The message is stamped on
the sidecar (`irods_message`), on the iRODS object as a
`mus_upload_message` AVU triplet, and prepended to the ELN comment.
No silent uploads.

### Single file

```bash
cd ~/projects/microglia_pilot
mus h5ad check final/microglia_clusters.h5ad    # validates structure + vocab
mus irods upload -m "post-QC clustering for figure 3" \
    final/microglia_clusters.h5ad
```

`.h5ad` files are gated: `mus h5ad check` must have validated them
before `mus irods upload` will accept them. The check stamps a pass
into the sidecar. `--force-h5ad-upload` bypasses; you'll regret it.

### Folder with many small files

```bash
mus irods upload -m "analysis scripts as of 2026-06-04" scripts/
# scripts/ — 3 files, total 8 KB, median 2 KB
# Many small files — pack into scripts.tar.gz? [t/a/c]
```

- **`t` (pack)** — recommended when you have many small files. iRODS
  per-object overhead dominates for tiny things. mus tar+gzips
  locally, uploads one archive.
- **`a` (as-is)** — preserves the directory tree on iRODS (browsable
  in the Mango web UI). Use for a small number of larger files.

Non-interactive callers (CI, scripts) must pass `--pack tar.gz` or
`--pack none` explicitly.

### Whole project at once

```bash
cd ~/projects/microglia_pilot
mus irods upload -m "initial deposit of microglia pilot" \
    scripts/ raw/ final/
```

One message applies to all three; one ELN comment lists everything.
The `intermediate/` subfolder is simply not in the argument list —
it's not backed up.

---

## 9. Where the data lands + the `.mus` sidecars

Layout on iRODS:

```
<irods_home>/project/<data_project>/<eln_experiment_name>/<basename>
```

So `final/microglia_clusters.h5ad` lands at e.g.
`/gbiomed/home/BADS/mus/project/Fiers2025/microglia_pilot/microglia_clusters.h5ad`.

After upload, three things exist locally next to each uploaded file:

- the original file (unchanged)
- a `*.mus` sidecar with iRODS path, persistent URL, sha256, and a
  snapshot of the resolved cascade
- for folder uploads: one folder-level `<foldername>.mus` sidecar
  with the recursive Merkle hash, not per-file sidecars

A linked ELN experiment receives a one-line comment per upload with
clickable persistent URLs — so future-you (or a colleague) can jump
from the ELN entry to the data. `--no-eln-comment` opts out per call.

All resolved metadata (organism, tissue, technology, source, access,
sha256, …) is also attached to the iRODS object as **AVU triplets** —
visible in `iron meta ls` and queryable via `iron query`. AVU is iRODS
jargon for "attribute-value-unit" key-value metadata.

**What travels with the upload:**
- `<file>.h5ad` — the data
- `<file>.h5ad.mus` — the sidecar (sha256 + iRODS path / URL / PURL + cascade snapshot)
- `<file>.h5ad.md` — your prose description of this file (if present)
- The folder's own `mus.cfg` and `index.md` — the immediate-folder config + folder-level prose (parent cascade entries stay local)
- AVU on the iRODS object itself — the strict-vocab metadata, queryable via `iron query` and `mus irods find`

The legacy `.env` file is the one thing mus does NOT upload — it can carry direnv-style shell secrets that don't belong on the catalog.

### Leave the `*.mus` sidecars in place

This is important. The sidecar is your handle to the data:

- It records the sha256 — needed by `mus check` and `mus rm` to
  detect drift.
- It records the iRODS path and persistent URL — needed by
  `mus irods get` to download the file back.
- After a `mus rm` it becomes a **tombstone** (see §11) — the only
  remaining pointer to where the data lives.

Do not `rm *.mus`. Do not gitignore them in shared lab projects.
Treat them like the data file's passport.

---

## 10. Verify, anytime

```bash
mus check                           # every *.mus in this dir
mus check -r .                      # recurse
mus irods check                     # local hash vs iron's remote checksum
mus irods check --deep scripts.mus  # walk the iRODS tree for as-is folders
```

`mus check` is fast (it uses a local sha256 cache keyed on size +
mtime). `mus irods check --deep` is slow for large folder-as-is
uploads but it's the only way to confirm bytes-on-disk match
bytes-on-iRODS file-by-file.

---

## 11. Safely delete local data

Use `mus rm`, not `rm`. It refuses to delete unless three checks pass:

1. sidecar says the file is uploaded (`irods_status=uploaded`)
2. fresh local hash matches what was uploaded (no local drift)
3. fresh local hash matches the remote checksum (no remote drift /
   loss)

```bash
mus rm --dry-run final/microglia_clusters.h5ad    # show plan, touch nothing
mus rm final/microglia_clusters.h5ad              # prompts y/N
mus rm -y scripts/ raw/                           # skip prompt, batch
```

For folders uploaded as-is, `mus rm` walks `iron tree --json` and
compares every file. Slow on big trees, but it's the only way to be
sure.

**By default `mus rm` keeps the `*.mus` sidecar** as a **tombstone**
(`irods_status` flips to `local_deleted`). Use `--delete-sidecar` to
remove the sidecar too — but you usually don't want to.

---

## 12. Restore from a tombstone

A tombstoned sidecar still carries everything you need:
`irods_path`, `irods_purl`, sha256. You can restore the data any time.
`mus irods get` accepts multiple sidecars; shell globs work:

```bash
mus irods get final/microglia_clusters.h5ad.mus
mus irods get scripts.mus
mus irods get final/*.mus                      # all sidecars in final/
mus irods get final/*.mus raw/*.mus            # multi-folder
```

Verification on restore is automatic: mus recomputes the hash and
compares to the sidecar. Mismatch → loud `PROVENANCE MISMATCH` error,
file left in place for inspection.

### Metadata refresh on get

`mus irods get` also pulls the current AVU back from iRODS and folds
it into the local sidecar. This catches the case where a colleague
enriched the metadata on iRODS after the original upload (e.g. added
`disease` or fixed a `cell_type` label).

| Situation | Behaviour on get |
| --- | --- |
| AVU has a field the sidecar didn't | silently added to the sidecar |
| AVU agrees with the sidecar | silent no-op |
| AVU differs from the sidecar | `~ key: old → new` warning line, sidecar overwritten |

The refresh runs even when the file is already local and matches the
sidecar (download is skipped, AVU refresh isn't). So `mus irods get
*.mus` is a safe idempotent way to bring local annotation up to date
with what the lab has put on the catalog.

A few teammate-facing properties of tombstones:

- They're tiny (~1 KB), worth keeping in git for shared projects.
- They survive iRODS-side moves and renames because the `irods_purl`
  is a persistent identifier, not a path.
- If you `--delete-sidecar`d, you can still find the object with
  `mus irods scan find <sha256>` if you noted the hash, or by
  browsing under `<irods_home>/project/<data_project>/`.

---

## 13. End-to-end transcript

```bash
# Setup ------------------------------------------------------------
cd ~/projects/microglia_pilot

mus eln link 12345                                  # writes mus.cfg

mus config set source internal
mus config set access internal
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set cell_type 'microglial cell'
mus config set disease 'Alzheimer disease'
mus config set technology 10x_chromium_3p_v3
mus config set suspension_type nucleus

# Per-subfolder overrides
mus -C final config set access public
mus -C final config set doi 10.1234/microglia.2025

# Validate, then back up -------------------------------------------
mus h5ad check final/microglia_clusters.h5ad

mus irods upload -m "scripts snapshot" scripts/                           # pack? → t
mus irods upload -m "raw fastq from VIB seq core" raw/sample01.fastq.gz raw/sample02.fastq.gz
mus irods upload -m "post-QC final h5ad + figure-3 source data" \
    final/microglia_clusters.h5ad final/figure_data.csv

# Free local disk after a clean check ------------------------------
mus rm -y final/microglia_clusters.h5ad
ls final/
# microglia_clusters.h5ad.mus    ← tombstone
# figure_data.csv
# figure_data.csv.mus
# mus.cfg

# Restore months later when you need the h5ad back -----------------
mus irods get final/microglia_clusters.h5ad.mus
```

---

## 14. Common pitfalls

- **"data_project not set"** — `mus eln link 12345` or
  `mus config set data_project Fiers2025`. Format is `NameYear`:
  capital first letter, ends in 4 digits.
- **"no ELN status declared"** — every folder must explicitly link
  or `mus eln disable`. No implicit non-ELN — pick consciously.
- **"vocabulary changed since last check"** — the joint vocab was
  updated. Re-run `mus h5ad check`.
- **"value not in vocabulary"** — check the spelling against
  <https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml>
  or open an issue to extend the vocab.
- **"local drift since upload"** on `mus rm` — you (or a process)
  modified the file after upload. Re-upload, or restore the
  original, then retry.
- **Many small files** — always pack. iRODS metadata overhead
  dominates for tiny objects.
- **Deleted `*.mus` by accident** — recover from git, or find the
  object via `mus irods scan find <sha256>` if you noted the hash.

---

## See also

- [quickstart.md](quickstart.md) — full reference manual with every flag
- [joint vocabulary](https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml) — the controlled-value lists
- UBERON: <https://www.ebi.ac.uk/ols4/ontologies/uberon> — anatomy
- CL: <https://www.ebi.ac.uk/ols4/ontologies/cl> — cell types
- MONDO: <https://www.ebi.ac.uk/ols4/ontologies/mondo> — diseases
- EFO: <https://www.ebi.ac.uk/ols4/ontologies/efo> — assays / protocols
- `mus <command> --help` — every subcommand documents its flags

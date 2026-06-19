# 02 — Annotation & vocabularies

How mus knows what your data is about: cascading folder config (`mus.cfg`) + per-file sidecars (`*.mus`) + a controlled vocabulary for biology fields.

This doc is about the data-description layer. It's the same model whether you'll eventually back the data up to iRODS, push it to anthive, or both.

---

## The mental model

Three kinds of files:

| File | Scope | Who writes it |
|---|---|---|
| `mus.cfg` | Folder + every subfolder beneath it (recursive, cascades) | You, via `mus config set` |
| `<file>.mus` (per-file sidecar) | One specific data file | Mus, automatically (`mus tag`, `mus h5ad check`, `mus de check`, `mus irods upload`, …) |
| Joint vocabulary | Lab-wide controlled values for biology fields | Maintained centrally; fetched + cached by mus |

The rule of thumb: **`mus.cfg` is your recipe** (intent — what fields apply to everything in this folder); **`*.mus` is the receipt** (what mus actually stamped on this specific file).

You write the `mus.cfg`. Mus writes the `*.mus`. Don't hand-edit the sidecars.

---

## 1. Pick a project folder

The folder is your unit of annotation. It can be flat or nested — mus walks the directory tree looking for `mus.cfg` files.

```bash
cd ~/projects/microglia_pilot
```

---

## 2. Set the project-wide annotation

Each `mus config set` writes one key into the local folder's `mus.cfg` (creating it if missing). Values are validated as you type — case differences silently canonicalise, off-vocab values are rejected with a "did you mean?" suggestion.

```bash
mus config set data_project Fiers2025         # mandatory for any upload — see Step 3
mus config set organism human
mus config set tissue 'middle temporal gyrus'
mus config set technology 10x_chromium_3p_v3
mus config set source internal                # internal | external
mus config set access internal                # internal | public
```

What ends up on disk (you CAN read it; don't hand-edit when mus would do it for you):

```
# ~/projects/microglia_pilot/mus.cfg
access=internal
data_project=Fiers2025
organism=human
source=internal
technology=10x_chromium_3p_v3
tissue=middle temporal gyrus
```

Inspect at any point:

```bash
mus config show              # effective cascade (everything that applies here)
mus config show --local      # only THIS folder's mus.cfg
mus config files             # paths of every mus.cfg + legacy .env contributing
```

---

## 3. `data_project` is mandatory before any upload

`data_project` is the **collection name** on the lab archive. Every upload from this folder lands under `<archive-root>/project/<data_project>/…`. It groups all data from one piece of research into one place.

**Criteria:**
- Format: `NameYear` — capital first letter, ends in 4 digits. mus rejects anything else.
- Convention: first-author surname + publication / acquisition year. Examples: `Fiers2025`, `DeWulf2024`, `Sun2024`.
- One per project, not per experiment. Multiple experiments under one publication share the same `data_project`.
- Unique across the lab. Two unrelated projects sharing a `data_project` would write into the same iRODS subtree.

**Check what's taken before you pick:**

```bash
mus irods projects                 # plain alphabetical list
mus irods projects --details       # adds file count, total size, uploaders, last upload
mus irods project Fiers2025        # drill into one project's most-recent uploads
```

If your intended name is already there and it isn't yours, pick a different one (add a discriminator: `FiersMicroglia2025`).

---

## 4. Biology fields (use them — the more, the better)

These are the controlled-vocabulary fields. They're useful for ANY data file (not just h5ad) — a BAM from a single sample carries the same organism / tissue / disease that the h5ad downstream of it carries. Set them once at the project root and they cascade to every upload.

```bash
mus config set cell_type 'microglial cell'      # comma-separated list ok
mus config set disease 'Alzheimer disease'
mus config set suspension_type nucleus          # cell | nucleus | na
mus config set sex pooled                       # male | female | pooled | unknown
mus config set age '65y-90y'
mus config set source internal
mus config set access internal
mus config set license CC-BY-4.0                # for public datasets
mus config set doi 10.1234/microglia.2025       # comma-separated list
mus config set pmid 38912345
mus config set year 2026
mus config set contributor "Jane Doe, John Smith"
```

### Inspect the vocabulary

```bash
mus vocab show                       # every field's allowed values
mus vocab show tissue                # just one field
mus vocab refresh                    # force a fresh pull from Codeberg
```

Use **labels, not ontology IDs** — `tissue=middle temporal gyrus`, never `UBERON:0002771`. Case + separator variants are silently canonicalised (`HuMaN` → `human`, `prefrontal_cortex` ≡ `Prefrontal Cortex`).

### Field → ontology mapping

| Field | Ontology | Browse |
|---|---|---|
| `tissue` | UBERON labels | <https://www.ebi.ac.uk/ols4/ontologies/uberon> |
| `cell_type` | CL labels | <https://www.ebi.ac.uk/ols4/ontologies/cl> |
| `disease` | MONDO labels | <https://www.ebi.ac.uk/ols4/ontologies/mondo> |
| `technology` | EFO-aligned protocol / chemistry | <https://www.ebi.ac.uk/ols4/ontologies/efo> |

Need a term that isn't there? File an issue on the mus repo — don't invent locally. Mus's vocab is the lab-wide source of truth; new terms get added centrally.

---

## 5. Subfolder overrides

Subfolders inherit the parent's `mus.cfg` and can override any field. Use `-C <DIR>` to write a subfolder's config without `cd`-ing:

```bash
# Project root: internal
mus config set access internal

# Final / published version: override to public + add DOI
mus -C final config set access public
mus -C final config set doi 10.1234/microglia.2025
```

The cascade order is deepest-wins, so `final/mus.cfg`'s `access=public` overrides `mus.cfg`'s `access=internal` for everything under `final/`.

---

## 6. List-valued fields

Some fields are lists — comma-separated:

```bash
mus config set tissue 'middle temporal gyrus,hippocampus,frontal cortex'
mus config set cell_type 'microglial cell,astrocyte,oligodendrocyte'
mus config set doi 10.1234/foo,10.5678/bar
```

The list keys: `tissue`, `cell_type`, `disease`, `condition`, `doi`, `pmid`, `link`, `tag`, `collaborator`, `tissue_ontology_term_id`, `cell_type_ontology_term_id`, `disease_ontology_term_id`.

To **remove** an inherited list item in a subfolder, prefix with `-`:

```bash
# Project: tag=lab,shared
# Subfolder: drop "shared", add "exp42"
mus config set tag exp42,-shared
```

---

## 7. Per-file annotation: the sidecar

When mus creates / refreshes a `<file>.mus` sidecar (via `mus tag` / `mus h5ad check` / `mus de check` / any `mus … upload`), it freezes a snapshot of the cascade plus file-specific fields:

```bash
mus tag final/microglia_clusters.h5ad
```

Result (mus generates this; don't hand-edit):

```
# final/microglia_clusters.h5ad.mus
access=internal
data_project=Fiers2025
organism=human
sha256=acb5475ab89a05d813c8dc0c987c4d2198d319fc21b8df1551cde4e3316fd2d7
size=1342515200
tissue=middle temporal gyrus
technology=10x_chromium_3p_v3
hashed=2026-06-19T10:00:00Z
mtime=2026-06-19T09:55:12Z
...
```

The sidecar is the file's **passport** — sha256 for integrity, plus the resolved metadata at tag time. Keep it. Commit to git. Don't delete.

For one-off notes / tags:

```bash
mus tag final/microglia_clusters.h5ad -m "post-QC for figure 3" -t qc-pass -t figure3
```

---

## 8. ELN status is mandatory

Every folder mus uploads from must be in ONE of two states:

| State | How to set | Effect |
|---|---|---|
| **linked** | `mus eln link <experiment-id>` | folder ↔ eLab experiment; mus auto-posts comments to the experiment on every upload |
| **disabled** | `mus eln disable` (per-folder) OR `mus eln disable --global` (once per machine, see [01](01_install_and_setup.md)) | upload proceeds without eLab |

A folder in NEITHER state is **undecided** and `mus irods upload` / `mus s3 upload` refuse until you pick. This is deliberate: no implicit non-ELN behavior.

Full details on linking (and what `mus eln link` does to your `mus.cfg`) live in [03 — ELN & iRODS](03_eln_and_irods.md).

---

## 9. Legacy `.env` files

Pre-0.2.12 mus stored folder config in `.env`. That name collided with the dotenv ecosystem (direnv, docker-compose, python-dotenv). The canonical name as of 0.2.12 is **`mus.cfg`**. `.env` files are STILL READ for backwards compatibility, but every new write goes to `mus.cfg`.

Migrate an existing project once:

```bash
mus config migrate              # this folder
mus -C subdir config migrate    # one subfolder
```

mus moves mus-recognised keys to `mus.cfg` and leaves anything that looks like a secret (`DATABASE_URL`, `AWS_*`, `*_TOKEN`, …) or that mus doesn't recognise in `.env`, with a warning. Safe to run; the safety gate refuses to touch a `.env` that doesn't carry at least one mus-recognised key OR a `data_project=…` line — so an unrelated dotenv (e.g. a docker-compose env) is left alone.

---

## Common pitfalls

- **"`<key>` is not a recognised mus key"** — typed a key mus doesn't know about. Use `mus config show` to see canonical names, or pass `--force` if you really want a custom key.
- **"value not in controlled vocabulary"** — your value isn't in the joint vocab. `mus vocab show <field>` to see allowed values, or file an issue to extend the vocab.
- **`data_project` not in `NameYear` format** — must start with a capital letter and end with 4 digits. `MyDataset` → no. `MyDataset2025` → yes.
- **Subfolder override not picked up** — confirm with `mus -C subdir config show --local`. If empty, the `-C` write went elsewhere.
- **Forgot to keep sidecars** — `*.mus` files are tiny (~1 KB) text. Commit them to git in shared projects.

---

## What's next

- **[03 — ELN linking & iRODS upload](03_eln_and_irods.md)** — back up annotated data to the lab archive.
- **[04 — h5ad deposit](04_h5ad_deposit.md)** — additional fields and structural checks for single-cell .h5ad files going to anthive.
- **[05 — DE deposit](05_de_deposit.md)** — differential-expression result deposits.

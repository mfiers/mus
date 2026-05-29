# Joint vocabulary proposal — mus ⇄ h5adstore ⇄ anthive

**Status:** DRAFT, not implemented.
**Date:** 2026-05-29.
**Author:** mark.fiers@kuleuven.be (drafted with Claude Code).
**Scope:** unify the controlled vocabulary used by `mus h5ad check` and
the metadata frontmatter of `*.md` files in `/data/1/project/h5adstore`,
so anthive can read mus-stamped sidecars without translation. Adhere
to [CellxGene Discover schema](https://github.com/chanzuckerberg/single-cell-curation/blob/main/schema/5.3.0/schema.md)
conventions so the joint vocabulary is portable to the wider
single-cell ecosystem (CellxGene, HuBMAP, HCA, …).

---

## 1. Background

Two systems independently grew vocabularies for single-cell annotation:

| | `mus` (`vocabulary/main.yaml`) | `h5adstore` (`CONVENTIONS.md` + 156 `*.md` files) |
|---|---|---|
| Storage | flat `KEY=VALUE` in `.env` cascade + `.h5ad.mus` sidecar | YAML frontmatter `annotation:` block in `.md` |
| Inheritance | walk `.env` files up the tree | walk `index.md` files up the tree |
| Allowed values | short underscored tokens (`prefrontal_cortex`, `microglia`) | CellxGene-style labels with spaces (`prefrontal cortex`, `microglial cell`) |
| Validation | machine-enforced (`mus h5ad check`) | by-eye + script length-check (`validate_md.py`) |
| Ontology IDs | none | none |

Nine fields overlap by name (`organism`, `celltype`, `tissue`, `region`,
`disease`, `sex`, `technology`, `accession`, `doi`) — same purpose,
incompatible values. Three fields exist in h5adstore but not mus
(`condition`, `age`, `strain`). One pair (`internal: yes/no` vs
`access: internal/public` + `source: internal/external`) describes
the same axis with different names.

Neither side aligns with CellxGene Discover today. CellxGene is the
de-facto standard for single-cell deposit metadata and uses
ontology-pinned human-readable labels (`tissue = "prefrontal cortex"` +
`tissue_ontology_term_id = "UBERON:0000451"`).

**Goal:** one canonical vocabulary served from
`https://codeberg.org/mfiers/mus/raw/branch/main/vocabulary/main.yaml`,
shaped after CellxGene, that h5adstore migrates to in a one-off pass.
Future anthive validation reads the same `*.h5ad.mus` sidecars mus
produces.

---

## 2. Design principles

1. **CellxGene-first.** Use CellxGene's field names, value forms, and
   (where applicable) ontology IDs. Diverge only where CellxGene
   doesn't cover what the lab needs.
2. **Human-readable labels are the primary string.** `prefrontal cortex`
   (spaces, lowercase except proper nouns) — not `prefrontal_cortex`.
   Drop mus's "underscores vs spaces is strict" rule.
3. **Ontology IDs are optional but encouraged.** Each controlled field
   gets a sibling `<field>_ontology_term_id` slot for the canonical
   ID. Validation today is on the label; future work can extend to
   ID resolution.
4. **One controlled vocabulary file.** Lists are versioned with a
   `schema_version` bump (1 → 2) when this proposal lands.
5. **Mus's per-file artifact remains `.h5ad.mus`.** anthive learns to
   read it; h5adstore's old `.md` files keep narrative content + a
   slimmer preamble, and emit a sidecar that's the source of truth
   for vocabulary-controlled metadata.
6. **Cross-experiment defaults stay in `.env` cascade.** `organism`,
   `tissue` etc. that apply to a whole project go in a top-level
   `.env`; the sidecar overrides per file. (Existing mus model — no
   change.)

---

## 3. Proposed joint vocabulary (`schema_version: 2`)

### 3.1 Required fields (HARD on missing, per spec §1.5 / mus's required list)

| Field | Type | Allowed values (excerpt) | CellxGene field | Ontology |
|---|---|---|---|---|
| `organism` | string | `human`, `mouse`, `rat`, `macaque`, `zebrafish`, `drosophila` | `organism_ontology_term_id` | NCBITaxon |
| `tissue` | string OR list | UBERON labels: `brain`, `prefrontal cortex`, `hippocampus`, `white matter of frontal lobe`, … | `tissue_ontology_term_id` | UBERON |
| `cell_type` | string OR list | CL labels: `microglial cell`, `astrocyte`, `excitatory neuron`, `oligodendrocyte`, `oligodendrocyte precursor cell`, `mixed`, … | `cell_type_ontology_term_id` | CL |
| `technology` | string | Protocol/chemistry: `10x_chromium_3p_v2`, `10x_chromium_3p_v3`, `10x_chromium_5p`, `10x_chromium` (generic), `10x_multiome`, `10x_visium`, `10x_xenium`, `Smart-seq2`, `Smart-seq3`, `Drop-seq`, `CEL-seq2`, `Parse_Evercode`, `BD_Rhapsody`, `Slide-tags`, `Slide-seq`, `MERFISH`, `CosMx`, `Stereo-seq`, `bulk_RNA-seq`, … | `assay_ontology_term_id` (EFO) when set | EFO label is derivable |
| `suspension_type` | string | `cell`, `nucleus`, `na` (for bulk / spatial / non-cellular) | `suspension_type` (same name) | n/a |
| `access` | string | `internal`, `public` | n/a | n/a |
| `source` | string | `internal`, `external` | n/a | n/a |

Notes on renames + the two-axis encoding:
- mus's **`celltype`** → **`cell_type`** (CellxGene two-word form).
- mus's **`preparation`** → **`suspension_type`** with values
  `cell` / `nucleus` / `na`. Matches CellxGene field name verbatim.
- mus keeps **`technology`** as a first-class field (NOT renamed to
  `assay`). The lab needs to record the underlying chemistry —
  `10x_chromium_3p_v3` — independently of whether the suspension was
  cells or nuclei. CellxGene's `assay` field bundles those two into
  one string (e.g. "10x 3' v3"); the joint vocab keeps them
  orthogonal because the lab uses the same chemistry for both whole-
  cell and nucleus preps and wants to query each axis separately.
  An `assay_ontology_term_id` companion is still allowed if the
  depositor wants the unambiguous EFO ID.
- mus's **`internal: yes/no`** → split into two fields matching
  h5adstore's existing pair: **`access: internal | public`** +
  **`source: internal | external`**. h5adstore has 156 files using
  exactly this dichotomy. Cleaner than collapsing.

#### 3.1.1 `suspension_type` is derived during migration

For existing h5adstore data, the migration script derives
`suspension_type` from these cues, in order:

1. **Explicit CellxGene metadata** — if the original CellxGene-curated
   AnnData has `obs['suspension_type']`, take the unique value(s).
2. **Technology hint** — `snRNA-seq` → `nucleus`; `scRNA-seq` → `cell`;
   `bulk-RNA-seq` → `na`; spatial techs (Visium, MERFISH, Slide-tags,
   Xenium, Stereo-seq, CosMx) → `na`.
3. **Narrative keyword match** in the dataset's markdown narrative —
   `single-nuclear`, `single-nucleus`, `snRNA`, `snATAC`, `nuclei
   isolation` → `nucleus`; `single-cell`, `scRNA`, `whole-cell`,
   `intact cells` → `cell`.
4. **Default** → `cell`, flagged in the audit for human review.

`technology` itself stays as the depositor declares it (after
normalisation per §6.4); the `snRNA-seq`/`scRNA-seq` label is
discarded once the two orthogonal axes are populated.

### 3.2 Soft fields (warned on missing)

| Field | Type | Allowed values (excerpt) | CellxGene field | Ontology |
|---|---|---|---|---|
| `disease` | string OR list | MONDO labels: `Alzheimer disease`, `Parkinson disease`, `multiple sclerosis`, `glioblastoma`, `normal` (instead of `healthy` / `control`), … | `disease_ontology_term_id` | MONDO |
| `condition` | string OR list | free text — `control`, `treated`, `knockout`, `EAE`, `GvHD`, … | n/a | n/a (lab-specific) |
| `sex` | string | `female`, `male`, `unknown`, `mixed` (mixed = per-dataset aggregation, not a per-cell label) | `sex_ontology_term_id` | PATO |
| `age` | string | freeform string with unit: `87y`, `18m`, `8w`, `P30`. CellxGene's `development_stage` is HsapDv-pinned; we keep age as freeform for usability + add `development_stage_ontology_term_id` for those who use it. | (paired field) | HsapDv / MmusDv |
| `strain` | string | freeform: `C57BL/6`, `BALB/c`, etc. Only relevant when `organism != human`. | n/a | n/a |
| `doi` | string OR list | DOI without prefix: `10.1038/s41593-022-01246-0` | n/a | n/a |
| `pmid` | string OR list | PubMed numeric ID. Alternative to doi. | n/a | n/a |
| `accession` | string | GSE/SRA/PRJ/phs/EGA/Zenodo — public ID when `access: public`; freeform internal label when `access: internal` | n/a | n/a |
| `year` | integer | publication / generation year | n/a | n/a |
| `contributor` | string | depositor name + email | n/a | n/a |
| `title` | string | dataset title | `uns['title']` | n/a |
| `link` | string OR list | generic URLs (CellxGene Discover dataset page, supplementary data) | n/a | n/a |
| `license` | string | CC-BY-4.0, CC0, restricted, etc. | n/a | n/a |
| `source_description` | string | "downloaded from CELLxGENE 2026-04-12" — provenance note | n/a | n/a |

### 3.3 Per-file shape (h5ad-specific, not vocabulary)

Stays as-is in `*.h5ad.mus` under the `h5ad_` prefix:

```
h5ad_n_obs, h5ad_n_vars, h5ad_layers, h5ad_embedding,
h5ad_anndata_version, h5ad_gene_id_type,
h5ad_inspected_at, h5ad_inspected_sha256
```

### 3.4 Per-file integrity / catalog (anthive-specific)

These don't belong in mus's vocab — they're anthive-catalog concerns.
Stay in the `.md` preamble after migration:

```
title, id, version, sha256, date, year,
default_layer, has_lognorm,
obsnum_include, obscat_include, obs_exclude,
var_include, var_exclude,
layers_include, layers_exclude,
obsm_include, obsm_exclude,
layer_description, embedding_description, layer_stats,
top_genes, top_cat, top_num,
cellxgene_collection_id, cellxgene_dataset_id, cellxgene_schema_version,
is_primary_data, mean_genes_per_cell, x_approximate_distribution
```

### 3.5 Renamed / restructured

| Field | Status | Reason |
|---|---|---|
| `region` | REMOVED | CellxGene puts anatomical regions under `tissue` (UBERON is flat-ish). h5adstore mixed usage: 8 files used `region`, others put the same value under `tissue`. Collapse into `tissue`. |
| `preparation` | RENAMED → `suspension_type` | CellxGene field name; reduced to cell/nucleus/na. |
| `celltype` | RENAMED → `cell_type` | CellxGene two-word form. |
| `technology` | KEPT (NOT renamed) | The lab queries chemistry + suspension separately. The two-axis encoding (`technology` + `suspension_type`) is more useful than CellxGene's bundled `assay`. |
| `internal` (yes/no) | RENAMED → `access` + `source` | Splits into two axes matching h5adstore's existing dichotomy. |

### 3.6 mus's matching rule change

Current: case-insensitive lookup, but underscores vs spaces are strict.

Proposed: **case-insensitive lookup, AND `_` ≡ ` ` ≡ `-`**. So
`prefrontal_cortex`, `prefrontal cortex`, `prefrontal-cortex`,
`Prefrontal Cortex` all match a single canonical entry. This makes
migration painless for existing h5adstore data and removes a footgun.

Open question: stricter behaviour might catch typos. My recommendation:
relax. The ontology-ID companion (when present) is the strict-match
backstop.

### 3.7 List semantics — how multi-valued fields work

Several fields can carry multiple values for a single dataset:
**`tissue`**, **`cell_type`**, **`disease`**, **`condition`**, **`doi`**,
**`pmid`**, **`link`**. The rest are single-valued.

#### Storage format

In `.env` (cascade) and `*.h5ad.mus` (sidecar) — both flat `KEY=VALUE`
env-format — list values are comma-separated:

```
# .env or .h5ad.mus
tissue=prefrontal cortex,hippocampus,white matter of frontal lobe
cell_type=microglial cell,astrocyte
disease=Alzheimer disease,normal
```

**Problem**: some controlled labels contain commas (e.g. the MONDO term
`leukoencephalopathy, diffuse hereditary, with spheroids 1`). The
current envformat parser splits on commas with no escape syntax — it
would mis-parse that as three list items.

**Fix**: envformat learns **double-quoted list items** for values
containing commas. Unquoted items continue to parse as today (backward
compatible). Marshal automatically quotes when a value contains a
comma or double-quote.

```
# Hand-written, mus parses correctly:
disease="Alzheimer disease","leukoencephalopathy, diffuse hereditary, with spheroids 1"
```

Both quoted and unquoted items can mix in the same list:
```
disease=Alzheimer disease,"leukoencephalopathy, diffuse hereditary, with spheroids 1",normal
```

When mus writes a sidecar, it quotes only the items that need it
(comma-or-quote-containing).

#### Cascade merge semantics (unchanged)

When the same list-key appears at multiple levels of the cascade, the
lists merge (deeper additions appended, deduplicated). A `-prefix`
removes a previously-added entry — same as the existing `tag` and
`collaborator` list keys.

```
# /project/.env
tissue=brain,prefrontal cortex,hippocampus

# /project/specific_experiment/.env
tissue=-prefrontal cortex,striatum
```

→ effective `tissue` for the deeper folder: `brain, hippocampus, striatum`.

The sidecar (`*.h5ad.mus`) is the deepest layer — its list value
**always replaces** the cascade's. If you want a value to inherit
from a cascade layer plus add file-specific items, set both at the
right cascade depth (the cascade itself supports `-prefix` removal
and append-on-merge); the sidecar is the per-file authoritative
override.

#### Which fields are list-typed?

These are registered as list-keys in `internal/config/listKeys` and
`internal/sidecar/sidecarListKeys`:

| Field | Why a list |
|---|---|
| `tissue` | multi-tissue datasets (multiple brain regions, brain + spinal cord, …) |
| `cell_type` | sorted/enriched populations may still contain multiple types |
| `disease` | mixed-cohort datasets (AD + control) |
| `condition` | multi-condition experiments |
| `doi`, `pmid` | dataset described across multiple publications |
| `link` | multiple external URLs (CellxGene, GEO, supplementary) |
| (also pre-existing: `tag`, `collaborator`) | mus's existing list keys, unchanged |

Everything else (`organism`, `technology`, `suspension_type`, `sex`,
`access`, `source`, `accession`, `year`, `contributor`, `title`,
`license`, `age`, `strain`) is single-valued.

For datasets that genuinely span multiple values of a normally-
single-valued field (e.g. xenograft = human cells in mouse host):
- `organism`: use `mixed` as a controlled value, and add narrative.
- `sex`: use `mixed`.
- `accession`: pick the primary; record others under `link`.

---

## 4. Changes required in `mus`

### 4.1 `vocabulary/main.yaml`

- Bump `schema_version` from `1` to `2`.
- Replace the controlled-value lists with CellxGene-aligned labels
  where applicable (UBERON for `tissue`, CL for `cell_type`, MONDO
  for `disease`, PATO for `sex`). `technology` keeps mus-style
  underscored values (a small curated set) since the lab queries
  by chemistry and CellxGene's bundled `assay` labels obscure the
  technology axis.
- Update `required:` list: `[organism, tissue, cell_type, technology,
  suspension_type, access, source]` (was: `[organism, celltype,
  technology, preparation, tissue, internal, accession]`).
- Update `soft:` list to match §3.2.
- Add commented sections pointing at the CellxGene schema URL and the
  source ontologies.

### 4.2 `internal/vocab/`

- Loosen `ValueAllowed` matching: normalise both sides via `lower(s)`
  and `s = strings.NewReplacer("_", " ", "-", " ").Replace(s)` before
  comparison.
- Drop the comment "underscores vs spaces matter".
- Bump the embedded copy.
- Update tests for the new matching rule.

### 4.3 `internal/h5ad/metadata.go`

- Rename `Metadata` fields to match new schema:
  - `Celltype` → `CellType`
  - `Preparation` → `SuspensionType`
  - `Internal` → split into `Access` + `Source`
- `Technology` stays as a Go field (NOT renamed to `Assay`).
- Add new fields: `Condition` (`[]string`), `Age string`, `Strain string`.
- `MetadataFromCascade` reads the new bare keys; remove the old
  underscore-name reads (clean break, no compatibility shim).
- `Region` field removed from `Metadata`.
- Optional ontology-ID fields added: `OrganismOntologyTermID`,
  `TissueOntologyTermID`, `CellTypeOntologyTermID`,
  `AssayOntologyTermID` (yes — EFO ID for the combined
  technology+suspension when set), `SexOntologyTermID`,
  `DiseaseOntologyTermID`. Validation today is label-only; ontology
  IDs are recorded but not enforced.

### 4.4 `internal/sidecar/sidecar.go`

- Rename `Bio.Celltype` → `Bio.CellType` etc.
- `Bio.Technology` stays (not renamed to `Assay`); add `Bio.SuspensionType`.
- Add `Bio.Condition`, `Bio.Age`, `Bio.Strain`, `Bio.PMID`, `Bio.Link`,
  `Bio.SourceDescription`.
- Add optional ontology-ID fields on `Bio`.
- Drop `Bio.Internal` (replaced by `Access` + `Source`).
- Update `sidecarListKeys`: `cell_type, tissue, disease, condition,
  doi, pmid, link` (NOT `technology` — single-valued).
- Update marshal/unmarshal accordingly.

### 4.9 `internal/envformat/envformat.go` — quoted-list support

Add double-quoted list items so values containing commas survive
parse/marshal round-trips (the MONDO label
`leukoencephalopathy, diffuse hereditary, with spheroids 1` is the
motivating case). See §3.7 for the storage format.

- **Parse**: split on commas, but a comma inside a `"…"` pair is a
  literal character. `\"` inside a quoted item is a literal `"`. `\\`
  is a literal `\`. Whitespace immediately after the splitting
  comma is trimmed (so `disease=a, b` still parses as `[a, b]` like
  today).
- **Marshal**: a list item is quoted iff it contains a comma or a
  double-quote; embedded `"` and `\` get escape-prefixed. Items
  without commas are emitted unquoted (no churn on existing fixtures).
- Unquoted items still parse exactly as today — backward compatible.
- Three new test cases:
  - MONDO label round-trip
  - mixed quoted + unquoted list (`a,"b, c",d`)
  - escaped quote inside a quoted item (`"he said ""hi"""` → `he said "hi"`)

### 4.5 `internal/config/config.go`

- Update list keys to match the new bare-key names.

### 4.6 `internal/cli/h5ad.go`

- `stampSidecar` populates the renamed `Bio` fields.
- No behaviour change to the CLI surface.

### 4.7 Documentation

- Update `vocabulary/main.yaml` header comment to point at this proposal
  + the CellxGene schema URL.
- Update `CLAUDE.md` (the mus project doc) with the new field-name list.
- Update `README.md` quickstart examples.

### 4.8 Tests

- All tests in `internal/h5ad/`, `internal/vocab/`, `internal/sidecar/`,
  `internal/cli/h5ad_test.go`, `internal/cli/check_h5ad_test.go` get
  their hard-coded field names rewritten.
- New test cases: space/underscore/dash equivalence in `ValueAllowed`.

---

## 5. Changes required in `h5adstore`

### 5.1 `CONVENTIONS.md`

- Replace the "Standard annotation fields" table with a one-line pointer
  to mus's vocabulary URL:
  `https://codeberg.org/mfiers/mus/raw/branch/main/vocabulary/main.yaml`
  and a one-paragraph note that mus is now the source of truth.
- Drop the "Format conventions" section (e.g. age suffix rules) — those
  are part of the mus vocab now.
- Keep the inheritance / index.md / freeform-fields sections — those are
  h5adstore-specific catalog concerns.

### 5.2 `VALIDATION.md`

- Replace the local validator with `mus h5ad check`. The pre-commit hook
  shells out to `mus h5ad check` on every modified `.h5ad.mus` file.
- The length-check on `group_id` / `id` (3 chars) stays — that's
  catalog-specific structure.

### 5.3 `scripts/validate_md.py`

- Trimmed to only enforce the catalog-structural rules (group_id and id
  length, uniqueness). Vocab validation delegated to `mus h5ad check`.

### 5.4 `hooks/`

- Pre-commit hook also runs `mus h5ad check` on every `.h5ad.mus` that
  was touched in the commit.

### 5.5 anthive software (out of scope here)

The user will update anthive to read `*.h5ad.mus` sidecars instead of
the annotation block in `.md` files. That's outside this proposal —
the proposal stops at producing the sidecars.

---

## 6. One-off migration of h5adstore `*.md` files

156 markdown files in `/data/1/project/h5adstore` carry an
`annotation:` block plus catalog/shape fields in the same frontmatter.
We split each:

### 6.1 Output for a per-h5ad markdown file (e.g. `Pan2024/Microglia.md`)

**Before** (current shape; 138 lines of frontmatter):

```markdown
---
title: Microglia
version: 0.1.0
sha256: 54a844f9...
year: 2026
date: '2026-03-29'
annotation:
  organism: human
  tissue:
    - prefrontal cortex
    - white matter of frontal lobe
  celltype: microglia
  disease:
    - Alzheimer disease
    - leukoencephalopathy
  condition: 53-year-old stage; …
  sex: mixed
  technology: 10x_chromium
  doi:
    - 10.1186/s40478-024-01853-5
  link:
    - https://cellxgene…
  access: internal
  source: external
  technology_category: single-cell/nucleus transcriptomics
obsnum_include: […]
obscat_include: […]
layer_stats: {…}
n_obs: 1621
…
default_layer: X
---

# Microglia

… narrative …
```

**After** (slimmer .md; the catalog/shape fields stay because anthive
needs them):

```markdown
---
title: Microglia
id: MIC
version: 0.1.0
sha256: 54a844f9...
date: '2026-03-29'
default_layer: X
has_lognorm: true
n_obs: 1621
n_vars: 60305
obsnum_include: […]
obscat_include: […]
layer_stats: {…}
layers_include: [X]
obsm_include: […]
…
---

# Microglia

… narrative …
```

**Sibling** `Pan2024/Microglia.h5ad.mus` (the new authoritative
vocabulary record):

```
created=2026-05-29T10:30:00Z
updated=2026-05-29T10:30:00Z

sha256=54a844f93af39291ce4ae3a8ff2401669bf70b18c1f334bcb1f915934c8ed7af

organism=human
tissue=prefrontal cortex,white matter of frontal lobe,white matter of occipital lobe,white matter of parietal lobe,white matter of temporal lobe
cell_type=microglial cell
disease=Alzheimer disease,"leukoencephalopathy, diffuse hereditary, with spheroids 1"
technology=10x_chromium
suspension_type=nucleus
sex=mixed
condition=53-year-old stage; 68-year-old stage; 70-year-old stage; …
access=internal
source=external
doi=10.1186/s40478-024-01853-5
link=https://cellxgene.cziscience.com/datasets/fe2eecbc-977a-4aec-9196-f89c3281d11c,https://www.ncbi.nlm.nih.gov/geo/query/acc.cgi?acc=GSE267301
contributor=
title=Microglia
year=2026
```

Notes on the example:
- `disease` carries a MONDO label with commas, double-quoted per §3.7.
- `technology=10x_chromium` (the generic 10x value) + `suspension_type=nucleus`
  (derived from the narrative's "single-nuclear" cue).
- The `h5ad_*` block doesn't appear yet — stamped the first time
  `mus h5ad check` runs against the actual `.h5ad` data file.

### 6.2 Output for an `index.md` (e.g. `Pan2024/index.md`)

**Before**:

```markdown
---
group: Pan2024
experiment: na
authors: [Pan J, Fores-Martos J, Delpirou Nouh C, …]
year: 2024
group_id: P24
cellxgene_collection_id: 7c4552fd-…
annotation:
  doi:
    - 10.1186/s40478-024-01853-5
  access: internal
  source: external
---

# Deciphering glial contributions …
```

**After** — split into two files in the same folder:

`Pan2024/index.md` (catalog-specific only):
```markdown
---
group: Pan2024
group_id: P24
experiment: na
authors: [Pan J, Fores-Martos J, …]
year: 2024
cellxgene_collection_id: 7c4552fd-…
---

# Deciphering glial contributions …
```

`Pan2024/.env` (the cascade entry point — applies to every sidecar in
this folder and below; per-folder defaults that individual `.h5ad.mus`
files override):
```
doi=10.1186/s40478-024-01853-5
access=internal
source=external
contributor=Pan J, Fores-Martos J, Delpirou Nouh C, Jensen T, Vallejo K, Cayrol R, Ahmadian S, Ashley E, Greicius M, Cobos I
year=2024
```

### 6.3 Field-level migration rules

| h5adstore source | New location | Transformation |
|---|---|---|
| `annotation.organism` | sidecar `organism=` | as-is (already CellxGene-lowercase) |
| `annotation.tissue` | sidecar `tissue=` | as-is (already CellxGene-style strings with spaces) |
| `annotation.celltype` | sidecar `cell_type=` | rename key; values mapped: `microglia` → `microglial cell`, `astrocyte` → `astrocyte` (already CellxGene), `excitatory neuron` → `excitatory neuron`, … (see §6.4 for the full map) |
| `annotation.technology` | sidecar `technology=` + derived `suspension_type=` | technology value normalised (see §6.4); suspension_type derived per §3.1.1. Example: `snRNA-seq` → `technology=10x_chromium` (default for snRNA-seq) + `suspension_type=nucleus`. `10x_chromium` alone → `technology=10x_chromium` + `suspension_type=cell` (default, flagged). |
| `annotation.technology_category` | discarded | derived from `assay` |
| `annotation.disease` | sidecar `disease=` | as-is (some already MONDO-style, others lowercase short — see §6.4) |
| `annotation.condition` | sidecar `condition=` | as-is, freeform |
| `annotation.region` | sidecar `tissue=` (merge) | combined with `tissue` since CellxGene treats them as flat UBERON. If both present, concatenate. |
| `annotation.sex` | sidecar `sex=` | as-is |
| `annotation.age` | sidecar `age=` | as-is |
| `annotation.strain` | sidecar `strain=` | as-is |
| `annotation.accession` | sidecar `accession=` | as-is |
| `annotation.doi` | sidecar `doi=` | as-is |
| `annotation.link` | sidecar `link=` | as-is |
| `annotation.access` | sidecar `access=` | as-is |
| `annotation.source` | sidecar `source=` | as-is |
| frontmatter `title` | sidecar `title=` + stays in `.md` | dual-write so the .md still self-describes |
| frontmatter `year` | sidecar `year=` (only on index.md; or cascade .env) | promoted to cascade |
| frontmatter `authors` | cascade .env `contributor=` (joined with `, `) | on `index.md` only |
| frontmatter `sha256` | sidecar `[file]` `sha256=` | dual-write |
| frontmatter `version` | stays in `.md` only | catalog-specific |
| Everything else (catalog/shape) | stays in `.md` | unchanged |

### 6.4 Value normalisation maps

Migration script applies these mappings as it walks the tree:

**`celltype` → `cell_type`** (CellxGene CL labels):

| h5adstore | joint vocab |
|---|---|
| microglia | microglial cell |
| astrocyte | astrocyte |
| oligodendrocyte | oligodendrocyte |
| oligodendrocyte precursor cell | oligodendrocyte precursor cell |
| excitatory neuron | excitatory neuron |
| inhibitory neuron | inhibitory neuron |
| endothelial cell | endothelial cell |
| vascular leptomeningeal cell | vascular leptomeningeal cell |
| microglial cell | microglial cell (already canonical) |
| mixed | mixed (non-CL aggregator label — keep) |

**`disease`** (MONDO labels):

| h5adstore | joint vocab |
|---|---|
| alzheimer | Alzheimer disease |
| Alzheimer disease | Alzheimer disease |
| parkinson | Parkinson disease |
| multiple sclerosis | multiple sclerosis |
| huntington | Huntington disease |
| ALS | amyotrophic lateral sclerosis |
| glioblastoma | glioblastoma |
| glioma | glioma |
| epilepsy | epilepsy |
| temporal lobe epilepsy | temporal lobe epilepsy |
| stroke | stroke disorder |
| GvHD | graft-versus-host disease |
| EAE | experimental autoimmune encephalomyelitis (or move to `condition`) |
| healthy | normal (CellxGene's term for control) |
| viral encephalitis | viral encephalitis |
| hippocampal sclerosis | hippocampal sclerosis |
| focal cortical dysplasia | focal cortical dysplasia |
| leukoencephalopathy, diffuse hereditary, with spheroids 1 | leukoencephalopathy, diffuse hereditary, with spheroids 1 |
| aicardi-goutieres syndrome | Aicardi-Goutières syndrome |

**`technology` value normalisation + `suspension_type` derivation**:

| h5adstore raw value | `technology` (joint vocab) | `suspension_type` (derived) |
|---|---|---|
| `10x_chromium` | `10x_chromium` (kept generic; refine to `10x_chromium_3p_v3` etc. in audit if version known) | from narrative: `nucleus` if `snRNA-seq`/`single-nuclear` cues present, else default `cell` (flagged) |
| `snRNA-seq` | `10x_chromium` (default; specific version inferred from narrative if possible) | `nucleus` |
| `scRNA-seq` (rare) | `10x_chromium` | `cell` |
| `bulk-RNA-seq` | `bulk_RNA-seq` | `na` |
| `10x_visium` | `10x_visium` | `na` |
| `Slide-tags` | `Slide-tags` | `na` |
| `MERFISH` | `MERFISH` | `na` |
| `CosMx` | `CosMx` | `na` |
| `Stereo-seq` | `Stereo-seq` | `na` |
| `NovaST` | `NovaST` | `na` |
| `10x_xenium` | `10x_xenium` | `na` |
| `mCEL-Seq2` | `CEL-seq2` | `cell` |
| `Smart-seq2` | `Smart-seq2` | `cell` |
| `Drop-seq` | `Drop-seq` | `cell` |
| `mass-spectrometry` | flag in audit (out of vocab scope; proteomics) | `na` |

Where the narrative gives an unambiguous suspension cue (`single-nuclear`,
`snRNA-seq`, `nuclei isolation` → `nucleus`; `single-cell`, `whole-cell`,
`intact cells` → `cell`), the script uses it. Otherwise it picks the
default in the table and flags the file in the audit report for human
confirmation.

### 6.5 Migration script outline

A one-off Python script in `h5adstore/scripts/migrate_to_mus_sidecars.py`:

1. Walk every `*.md` under the h5adstore root (skip `_papers`, `_`-prefixed
   folders, `CONVENTIONS.md`, `VALIDATION.md`).
2. For each file:
   - Parse YAML frontmatter.
   - Distinguish `index.md` (folder-level) vs per-file `*.md`.
   - For index.md: extract `annotation:` → write `<folder>/.env`. Strip
     `annotation:` and `cellxgene_collection_id` from the .md
     frontmatter (the latter could optionally migrate to the sidecar
     too).
   - For per-file: extract `annotation:` + `title` + `year` + `sha256`
     → write `<basename>.h5ad.mus`. Strip `annotation:` from the .md
     frontmatter.
   - Apply value normalisation maps from §6.4.
   - Print warnings for ambiguous mappings (the script proceeds; the
     human triages from the audit log).
3. Emit a single audit report at
   `h5adstore/migration_audit.md` with every file touched, every value
   normalised, and every warning. Reviewable in PR.
4. Run `mus h5ad check` (with `--no-data` flag — we don't have the
   `.h5ad` files locally) on every emitted sidecar to confirm the
   vocab-only validation passes.

Estimated runtime: seconds (no anndata involved, just YAML parsing).
Estimated PR size: ~3000 lines of `.env` + `.h5ad.mus` additions, plus
~3000 lines of `.md` deletions for the annotation blocks.

### 6.6 Reversibility

The migration script preserves the original `.md` content (only strips
the `annotation:` block + dual-writes a few top-level keys). Git history
contains the pre-migration form. No data is destroyed.

---

## 7. Risks / open questions

1. **Ambiguous suspension_type mapping for `10x_chromium`** (83 files).
   Many of these are snRNA-seq but tagged generically as `10x_chromium`.
   The migration script needs a heuristic (search .md narrative for
   "nucle", "single-nucle", "snRNA") and flag uncertain cases for the
   human. **Recommendation:** I'll mark every uncertain file as
   `suspension_type=cell` (the safer default for CellxGene-style data —
   most 10x lab work is whole-cell) and flag for manual confirmation.

2. **`region` collapse into `tissue`.** Today 74 h5adstore files have
   `region:` set. Collapsing into `tissue:` may surface duplicates
   (e.g. tissue=brain + region=prefrontal_cortex → tissue=[brain,
   prefrontal cortex]). UBERON IDs would disambiguate but we don't
   require them. **Recommendation:** keep both in `tissue:` as a list;
   human can dedupe in a follow-up.

3. **`condition` is freeform** (53 files use it for things like
   "53-year-old stage; 68-year-old stage; …"). Some of those entries
   are really development_stage, not experimental condition.
   **Recommendation:** keep freeform for now; CellxGene's
   `development_stage_ontology_term_id` can be added as a soft
   companion field in a follow-up.

4. **The matching-rule relaxation (`_` ≡ ` ` ≡ `-`)** may mask typos.
   E.g. `prefrontalcortex` would still fail, but `prefrontal-cortex`
   would silently match `prefrontal cortex`. Acceptable trade-off
   given the friction of strict matching, and the ontology-ID
   companion (when set) catches real mismatches at deposit time.

5. **CellxGene schema versions.** CellxGene 5.x is current; 4.x is
   widely deployed. The proposal targets the schema's stable concepts
   (UBERON, CL, MONDO, EFO, PATO) which are stable across versions.
   Bumping `schema_version` in `vocabulary/main.yaml` decouples mus's
   version from CellxGene's.

6. **Backward compatibility.** Sidecars stamped under the old mus
   vocab (`celltype=microglia`) won't roundtrip after the rename
   (`cell_type=microglial cell`). No production deployment has those
   sidecars yet (all sidecars in the wild are from the last week of
   this session's commits) — clean break is safe.

7. **anthive doesn't read `.h5ad.mus` files yet.** Until anthive learns
   this, the migration produces sidecars that don't serve the catalog.
   The user has signalled they'll handle the anthive side independently.

---

## 8. Execution plan once approved

1. **Land the mus changes** (§4) — `schema_version: 2`, renamed fields,
   relaxed matching, added fields. One commit, tested.
2. **Push the updated `vocabulary/main.yaml`** to Codeberg `main`. mus
   users worldwide pick it up on the next `mus h5ad check`.
3. **Land the h5adstore migration script** + run it once. Commit the
   resulting `.h5ad.mus` + slimmer `.md` files + the audit report. The
   user reviews the audit + the diff in PR.
4. **Update h5adstore `CONVENTIONS.md` / `VALIDATION.md` / `hooks/`**
   to delegate vocab validation to `mus h5ad check`.
5. **Hand-off to anthive software** — out of scope for this proposal.

Estimated effort: half a day for mus changes + test rewriting, an
hour for the migration script + run, half a day to review the audit
and fix the flagged ambiguities. Total ~1 day of focused work.

---

## 9. Out of scope (deliberately deferred)

- Ontology-ID validation (resolving `tissue_ontology_term_id: UBERON:0000451`
  against the actual UBERON OWL). Sidecars carry the IDs when set; mus
  validates the labels, not the IDs.
- Cross-references between mus, h5adstore, and CellxGene Discover's
  curated dataset list (i.e. "is this h5ad already in CellxGene under a
  different label set"). That's a downstream search problem.
- Migration of the `_papers` folder and other `_`-prefixed staging
  areas. Per `CONVENTIONS.md` those are excluded from validation
  anyway.
- Versioning of the joint vocabulary itself beyond the
  `schema_version: 1 → 2` bump (e.g. how to deprecate a value gracefully
  later). For now, additions don't bump; removals/renames do.

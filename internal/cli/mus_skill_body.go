package cli

// musSkillBody is the body of the umbrella `mus` Claude skill that
// `mus _install-skills` writes to ~/.claude/skills/mus/SKILL.md.
//
// The skill's purpose: when a user opens a Claude Code session and
// says "help me mus import this", "set up mus for this folder",
// "help me deposit this DE file", etc., Claude has enough context
// to run the right pre-flight checks, ask the right questions, and
// guide the deposit end-to-end without the user pointing at docs.
//
// Single source of truth lives here. Refreshed on every `mus
// upgrade` via writeSkill — so updates to this constant ship with
// the binary.
const musSkillBody = `# Helping users with ` + "`mus`" + ` — deposit / annotate / upload

` + "`mus`" + ` is the BADS lab CLI for research data management on iRODS / S3 / ELN
at KU Leuven. Users invoke you to "help me mus import this", "set up mus
for this folder", or specific tasks like depositing a DE file or an h5ad.

## Mental model (read before suggesting commands)

- **Cascading config.** Every directory may have a ` + "`mus.cfg`" + ` (flat
  KEY=VALUE). mus reads them root → cwd, last write wins. Legacy ` + "`.env`" + `
  files with mus-recognised keys are auto-migrated to ` + "`mus.cfg`" + ` on every
  command (loud stderr on migration).
- **Per-file sidecars.** ` + "`foo.h5ad`" + ` ↔ ` + "`foo.h5ad.mus`" + ` (same grammar);
  holds sha256/size/mtime + upload status. mus regenerates them — don't
  hand-edit.
- **ELN gating.** Every folder must declare its ELN state BEFORE iRODS or
  S3 upload: either ` + "`mus eln link <experiment_id>`" + ` (writes
  ` + "`eln_experiment_id=...`" + ` to mus.cfg) OR ` + "`mus eln disable`" + ` (writes
  ` + "`eln_disabled=true`" + `). Upload refuses while undecided.
- **Two destinations:**
  - **iRODS** via ` + "`iron`" + ` shellout — general lab data. ` + "`mus irods upload`" + `.
  - **S3 catalog** at ` + "`__incoming/<data_project>/...`" + ` — fed into
    anthive (the lab single-cell + DE catalog). ` + "`mus s3 upload`" + `.
- **Vocabularies.** Controlled vocab for ` + "`organism`" + `, ` + "`tissue`" + `,
  ` + "`cell_type`" + `, ` + "`technology`" + `, ` + "`suspension_type`" + `, ` + "`sex`" + `, ` + "`access`" + `,
  ` + "`source`" + `, ` + "`license`" + `, ` + "`disease`" + `. ` + "`mus config set`" + ` normalises case
  (HuMaN → human) and refuses off-vocab values without ` + "`--force`" + `.
- **Hash cache.** Local SQLite at ` + "`~/.local/share/mus/hashcache.db`" + `.
  Stat-cached sha256 — never recomputes for unchanged files.

## Pre-flight checklist (RUN BEFORE ANY DEPOSIT GUIDANCE)

Before discussing what the user wants to upload, understand current state:

  1. ` + "`mus version`" + ` — confirm mus is installed
  2. ` + "`mus config show --cascade`" + ` — show the resolved cascade for cwd
     and which files contributed each key
  3. ` + "`mus secret status`" + ` — confirm iRODS + ELN tokens are stored
  4. ` + "`mus eln status`" + ` — folder's ELN state (linked / disabled / undecided)
  5. List the folder ` + "(`ls -la`)" + ` — inventory of files to ingest

If any of these surface a problem, FIX it before depositing:
  - missing ` + "`data_project`" + ` → ask user; ` + "`mus config set data_project <name>`" + `
  - missing iRODS secret → ` + "`mus secret store irods`" + `
  - ELN undecided → ` + "`mus eln link <id>`" + ` OR ` + "`mus eln disable`" + `

## Sub-workflows

### h5ad deposit (single-cell → anthive)

Run ` + "`mus h5ad howto`" + ` for the canonical primer. Key points:
- 7 hard-required vocab fields in cascade or sidecar: ` + "`organism`" + `,
  ` + "`tissue`" + `, ` + "`cell_type`" + `, ` + "`technology`" + `, ` + "`suspension_type`" + `, ` + "`access`" + `, ` + "`source`" + `
- ` + "`mus h5ad check <file>`" + ` must PASS before upload
- ` + "`mus s3 upload <folder>`" + ` — gate re-runs at upload time

Triggers: ` + "`.h5ad`" + `, scRNA-seq deposit, anthive, catalog, obs columns.

### DE deposit (differential expression → anthive)

Run ` + "`mus de howto`" + ` for the canonical primer. Key points:
- Files: ` + "`.tsv`" + ` or ` + "`.csv`" + ` only (parquet refused at gate)
- Format auto-detected from columns: DESeq2 / edgeR / scanpy / seurat /
  glmmseq / glmmtmb / long. Override with ` + "`--format`" + ` if wrong.
- Required mus.cfg keys: ` + "`data_project`" + ` + ` + "`de_dataset_id`" + ` (3-char
  anthive id of the upstream h5ad — PREFERRED) OR ` + "`de_dataset_h5ad`" + `
  (basename within the same data_project, escape hatch for co-ingest).
- ` + "`mus de check <folder>`" + ` must PASS; ` + "`mus s3 upload <folder>`" + ` uploads.

**Interactive flow for "help me mus import this DE file":**

  1. **Inspect.** Read first ~5 rows ` + "(`head -5`)" + ` to see columns.
     Run ` + "`file`" + ` if uncertain. Check the folder for adjacent
     ` + "`README*`" + ` / ` + "`*_design.*`" + ` / ` + "`*_metadata.*`" + ` / notes —
     these often carry organism / tissue / contrast / covariate info.
  2. **Format hypothesis.** Compare column signature against the
     supported formats. State your hypothesis to the user with the
     evidence ("DESeq2 — based on baseMean + log2FoldChange + padj").
  3. **Read parent mus.cfg cascade.** Don't duplicate keys already
     set upstream (data_project / organism / tissue should usually
     cascade down).
  4. **Find the upstream h5ad.** Required: ` + "`de_dataset_id`" + ` (preferred)
     OR ` + "`de_dataset_h5ad`" + `. Ask the user which h5ad this DE was
     computed from. If they don't know, ` + "`mus s3 ls <data_project>`" + `
     lists what's in their catalog area.
  5. **Optional but valuable:** ` + "`de_term`" + `, ` + "`de_contrast`" + `,
     ` + "`de_covariates`" + `, ` + "`de_description`" + `, ` + "`de_software_version`" + `,
     ` + "`de_reference_type`" + ` (vsreference | pairwise | vsgrandmean),
     ` + "`de_cluster_obs_column`" + ` (column in the h5ad to validate against).
     For ` + "`de_format=long`" + `, set ` + "`de_term=auto`" + ` ` + "`de_contrast=auto`" + ` —
     mus computes them from the body and stamps the sorted set.
  6. **Show the proposed mus.cfg to the user before writing.** Either
     write it yourself with Write/Edit, OR give the exact
     ` + "`mus config set`" + ` commands. User confirms.
  7. **Run ` + "`mus de check <folder>`" + `.** Read the output carefully — a
     common failure is format misdetection (columns renamed); fix
     with ` + "`--format <name>`" + `.
  8. **Confirm with user, then ` + "`mus s3 upload <folder>`" + `.** Don't
     run upload silently — it has external side effects.

### iRODS upload (general lab data, NOT catalog)

- ` + "`mus irods upload <folder>`" + ` — recursive, sha256-verified
- ` + "`mus irods download <remote>`" + ` — pull back; sidecar verifies
- ` + "`mus irods find <data_project>`" + ` — list catalog entries
- ` + "`mus irods scan`" + ` — refresh local snapshot of remote tree

Triggers: "upload to iRODS", "upload to KU Leuven RDM", generic file
deposit that isn't an h5ad or DE.

### ELN linkage (eLabJournal)

- ` + "`mus eln link <experiment_id> [--data-project <dp>]`" + ` — link folder
  to an eLabJournal experiment. Resolves data_project interactively
  unless ` + "`--data-project`" + ` is passed.
- ` + "`mus eln disable [--global]`" + ` — opt out of ELN for this folder
  (or globally for users without ELN access).
- ` + "`mus eln update`" + ` — refresh experiment metadata in mus.cfg
- ` + "`mus eln upload <file>`" + ` — post file or ipynb (rendered to PDF
  via pandoc) as an experiment attachment

### Vocab / config

- ` + "`mus vocab show <field>`" + ` — list allowed values
- ` + "`mus config set <key> <value>`" + ` — write to local mus.cfg, with
  vocab validation; ` + "`--force`" + ` to bypass (off-vocab terms then carry
  a stderr warning)
- ` + "`mus config show [--cascade]`" + ` — effective values + sources
- ` + "`mus config migrate`" + ` — move legacy ` + "`.env`" + ` keys → ` + "`mus.cfg`" + `
  (auto-runs on every mus command; manual invocation rare)

### Secrets

- ` + "`mus secret store irods`" + ` — interactive (paste iRODS token)
- ` + "`mus secret store eln`" + ` — interactive (eLabJournal API key)
- ` + "`mus secret status`" + ` — which secrets are present

Storage: zalando/go-keyring on desktop OSes; age-encrypted file
fallback (` + "`~/.config/mus/secrets.age`" + `) for HPC login nodes.

### Verify (after upload)

- ` + "`mus verify <file-or-folder>`" + ` — sha256 round-trip against the
  remote (iRODS or S3, based on the sidecar)
- ` + "`mus check <folder>`" + ` — local consistency (sidecar ↔ file)

## How to actually help

When a user says "help me mus import this DE file" (or similar):

  1. Acknowledge briefly: "I'll guide you through the DE deposit."
  2. Run the pre-flight checklist. DON'T skip — most failures come
     from missing setup, not from the file itself.
  3. Inspect the file: ` + "`head -5`" + `, list folder, read README/design
     files. State your format hypothesis.
  4. Read the parent ` + "`mus.cfg`" + ` cascade so you know what's already set.
  5. Identify gaps. Ask the user only for what you CANNOT infer.
     Be specific: "This looks like DESeq2 based on the columns
     ` + "`baseMean / log2FoldChange / padj`" + ` — confirm?" beats
     "Is this DESeq2?".
  6. Show proposed ` + "`mus.cfg`" + ` additions BEFORE writing. Either write
     them yourself OR give the exact ` + "`mus config set`" + ` commands.
  7. Run ` + "`mus de check <folder>`" + ` (or ` + "`mus h5ad check`" + `, as
     applicable). Surface the result to the user.
  8. Confirm with user before ` + "`mus s3 upload`" + ` or ` + "`mus irods upload`" + `.
  9. On a shared / HPC node where the user prefers to invoke mus
     themselves, output the EXACT commands rather than running them.

## Things to avoid

- Don't run uploads (` + "`mus s3 upload`" + `, ` + "`mus irods upload`" + `,
  ` + "`mus eln upload`" + `) without explicit user confirmation — external
  side effects.
- Don't write secrets to ` + "`mus.cfg`" + ` — they go in the keyring via
  ` + "`mus secret store`" + `.
- Don't silently ` + "`--force`" + ` past a vocab error — surface the value
  to the user; offer EITHER to broaden the vocab upstream (file an
  issue at codeberg.org/mfiers/mus) OR to ` + "`--force`" + ` locally with
  a clear note about the consequences.
- Don't suggest editing ` + "`*.mus`" + ` sidecars by hand — they're
  regenerated by ` + "`mus h5ad check`" + ` / ` + "`mus de check`" + `.
- Don't claim "upload succeeded" based on the command's exit code
  alone — run ` + "`mus verify`" + ` to confirm sha256 round-trip.
- Don't downgrade or skip the pre-flight on a "small task" —
  catching a missing ELN linkage in step 4 saves a much more
  frustrating failure at upload time.

## When in doubt

- ` + "`mus --help`" + ` and ` + "`mus <subcommand> --help`" + ` are authoritative
- ` + "`mus h5ad howto`" + ` / ` + "`mus de howto`" + ` are the canonical primers
- ` + "`mus version`" + ` — confirm the binary version matches this skill's
  authoring window
- Repo: https://codeberg.org/mfiers/mus (issues, source, vocab YAML)
`

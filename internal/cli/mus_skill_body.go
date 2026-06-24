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
    Skips re-uploading when local sha256 matches the remote's already-recorded
    checksum (avoids the UNIX_FILE_OPEN_ERR you get when a colleague tries to
    overwrite a file owned by another user). Stamps the configured iRODS group
    (` + "`irods_group=BADS`" + ` by default; override in mus.cfg) with ` + "`own`" + `
    permission after every upload, so group members can read AND overwrite
    each other's files — subject to the overwrite prompt.
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
  2. ` + "`mus config show`" + ` — effective cascade for cwd (default mode;
     pass ` + "`--local`" + ` for just-this-folder, ` + "`-j`" + ` for JSON).
     ` + "`mus config files`" + ` lists the cfg files that contributed.
  3. ` + "`mus secret list`" + ` — names of stored secrets (e.g. ` + "`eln`" + `,
     ` + "`s3_*`" + `). iRODS auth lives in iron's own store, not mus's —
     check it via ` + "`iron auth status`" + ` (or just try ` + "`iron ls`" + `).
  4. Look at the ` + "`mus config show`" + ` output for ELN state:
     ` + "`eln_experiment_id=...`" + ` → linked; ` + "`eln_disabled=true`" + ` →
     disabled; neither → undecided (upload will refuse).
  5. List the folder ` + "(`ls -la`)" + ` — inventory of files to ingest.

If any of these surface a problem, FIX it before depositing:
  - missing ` + "`data_project`" + ` → ` + "`mus config set data_project <name>`" + `
  - iRODS auth not set up → ` + "`iron auth`" + ` (with the gbiomed zone +
    host pre-filled; ` + "`mus irods login`" + ` prints the canonical
    command for you)
  - missing ELN token → ` + "`mus eln login`" + ` (interactive wizard)
  - missing S3 config → ` + "`mus s3 login`" + ` (interactive wizard)
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
- **Catalog cross-check.** ` + "`mus de check`" + ` fetches anthive's manifest
  from S3 (cached at ` + "`$XDG_CACHE_HOME/mus/anthive_catalog.json`" + `, 5-min
  TTL) and validates ` + "`de_dataset_id`" + ` / ` + "`de_dataset_h5ad`" + ` against
  it. Hard-fails surface specifically:
    - "S26 is the GROUP_ID of Sun2026, not a file_id. Did you mean AQR / AQB / AQP / AQN / DUN?"
    - "AQX not found in Sun2026. Did you mean: AQB, AQN, AQP?" (Levenshtein top-3)
    - "AQR exists in OtherProj, not Sun2026" (cross-project)
    - "AQR is marked inactive — confirm intended" (soft warn, not block)
  - Brand-new data_project → soft SKIP "first deposit? (anthive resolves at ingest)" — not a block.
  - Pass ` + "`--no-catalog-check`" + ` for offline mode or first-time setup
    before S3 creds exist.

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
     computed from. If they don't know, ` + "`mus irods find <data_project>`" + `
     lists iRODS catalog entries; the anthive web catalog and the
     ` + "`vark`" + ` CLI (if installed) browse the live datasets.
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

- ` + "`mus irods upload <FILE-OR-FOLDER> -m <message>`" + ` — recursive, sha256-verified
- ` + "`mus irods get <SIDECAR>`" + ` — download what each ` + "`*.mus`" + `
  sidecar refers to (sidecar holds the remote path + sha256). Pass
  ` + "`-f`" + ` to overwrite local; ` + "`--verify-checksum`" + ` is on by default.
- ` + "`mus irods check <SIDECAR-OR-FOLDER>`" + ` — verify local sha256
  against iron's reported remote checksum (round-trip sanity).
- ` + "`mus irods find <data_project>`" + ` — list catalog entries by AVU
- ` + "`mus irods scan`" + ` — refresh local snapshot of remote tree
- ` + "`mus irods projects`" + ` / ` + "`mus irods project <name>`" + ` —
  list data_projects on iRODS / most recent uploads in one

**Upload behaviours worth knowing (mus 0.2.27+):**
- **Skip-if-same.** Before invoking iron, mus runs ` + "`iron checksum <remote>`" + `.
  Identical sha256 → silent no-op (sidecar still refreshes with current
  cascade). Avoids the UNIX_FILE_OPEN_ERR you'd see when overwriting a
  file owned by a different user on the iRODS server.
- **Overwrite prompt.** If sha256 differs AND the remote exists, mus prompts:
  ` + "`⚠ remote object exists with DIFFERENT bytes — overwrite? [y/N]`" + `.
  Non-TTY callers refuse without ` + "`--force`" + `. Pass ` + "`--force`" + ` to
  bypass the prompt; required for ` + "`mus serve`" + ` and any scripted batch.
- **Group ACL.** After every successful upload, ` + "`iron chmod own <group> <remote>`" + `
  runs automatically. Group from ` + "`irods_group`" + ` cascade key (default
  ` + "`BADS`" + `). Failure WARNS but doesn't roll back the upload. Set
  ` + "`irods_group=`" + ` (explicit empty) to opt out.
- **Better errors.** UNIX_FILE_OPEN_ERR is rewritten to: "iRODS refused
  to overwrite <path> — the existing replica is owned by another user…
  Options: ask the original uploader to ` + "`iron chmod write <user> <path>`" + `,
  or upload under a different remote path."

Triggers: "upload to iRODS", "upload to KU Leuven RDM", generic file
deposit that isn't an h5ad or DE.

### ELN linkage (eLabJournal)

- ` + "`mus eln login`" + ` — interactive: obtain + verify + store an
  ELN API token (run once per user).
- ` + "`mus eln link <experiment_id> [--data-project <dp>]`" + ` — link folder
  to an eLabJournal experiment. Resolves data_project interactively
  unless ` + "`--data-project`" + ` is passed.
- ` + "`mus eln disable [--global]`" + ` — opt out of ELN for this folder
  (or globally for users without ELN access).
- ` + "`mus eln enable [--global]`" + ` — undo a previous disable.
- ` + "`mus eln update`" + ` — refresh experiment metadata in mus.cfg from
  the server.
- ` + "`mus eln whoami`" + ` — print the user the stored token authenticates as.

(Post-upload ELN commenting is automatic on a successful
` + "`mus irods upload`" + ` to a linked folder; there's no standalone
` + "`mus eln upload`" + ` command — attachments and comments piggyback
on the upload + tag flows.)

### Vocab / config

- ` + "`mus vocab show [<field>]`" + ` — list allowed values for one
  or all controlled-vocab fields. ` + "`mus vocab refresh`" + ` forces
  a fresh fetch from Codeberg (bypasses the 24h cache).
- ` + "`mus config set <key> <value>`" + ` — write to local mus.cfg, with
  vocab validation; ` + "`--force`" + ` to bypass (off-vocab terms then carry
  a stderr warning)
- ` + "`mus config unset <key>`" + ` — remove a key from local mus.cfg
- ` + "`mus config show`" + ` — effective cascade (default).
  ` + "`--local`" + ` shows only this folder; ` + "`-j`" + ` for JSON.
- ` + "`mus config files`" + ` — list every cfg file that contributes
  to the cascade for cwd (root-most first).
- ` + "`mus config migrate`" + ` — move legacy ` + "`.env`" + ` keys → ` + "`mus.cfg`" + `
  (auto-runs on every mus command; manual invocation rare)

### Credentials / auth

Each subsystem owns its own auth path — there's no single
` + "`mus secret store …`" + ` command. Use the per-system wizard:

- ` + "`mus eln login`" + ` — interactive: ELN API token (stored in
  mus's keyring).
- ` + "`mus s3 login`" + ` — interactive: endpoint + region + bucket +
  access key + secret key (5 fields, all stored in mus's keyring).
- ` + "`iron auth`" + ` — iRODS auth (NOT a mus command; iron has its
  own credentials file). ` + "`mus irods login`" + ` just prints the
  canonical ` + "`iron auth`" + ` command with the gbiomed zone + host
  pre-filled.

To inspect what's in mus's keyring:

- ` + "`mus secret list`" + ` — names of stored secrets
- ` + "`mus secret get <name>`" + ` — print one (used in scripts)
- ` + "`mus secret backend`" + ` — print active backend (keyring or age)

Storage: zalando/go-keyring on desktop OSes; age-encrypted file
fallback (` + "`~/.config/mus/secrets.age`" + `) for HPC login nodes.

### Verify (after upload)

- ` + "`mus check <FILE-OR-FOLDER>`" + ` — local consistency
  (sidecar ↔ file sha256). ` + "`-r/--recursive`" + ` walks dirs.
- ` + "`mus irods check <SIDECAR-OR-FOLDER>`" + ` — sha256 round-trip
  against the remote via ` + "`iron checksum`" + `. The post-upload
  truth-check.
- ` + "`mus s3 check`" + ` — verify S3 credentials + bucket
  reachability (config-level check, not per-file).

### Local web GUI

- ` + "`mus serve`" + ` — boots a single-user localhost web GUI on a random
  port and prints/opens the URL with a session token. Designed for
  non-CLI lab depositors: folder tree (left), annotation form with
  vocab autocomplete (middle), pre-flight + upload trigger with
  streaming log pane (right). Ctrl-C to stop. Single-user, token in
  URL → HttpOnly cookie, refuses non-loopback by design. To use from
  a remote machine, port-forward over SSH:
  ` + "`ssh -L <port>:127.0.0.1:<port> <host>`" + `.
- Flags: ` + "`--port <N>`" + ` (default: random), ` + "`--no-browser`" + `
  (print URL only), ` + "`--bind`" + ` (loopback-only — non-loopback refused).
- Trigger this when the user says "I want a UI", "non-techie
  colleague needs to deposit", or "the CLI is too much."

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

- Don't run uploads (` + "`mus s3 upload`" + `, ` + "`mus irods upload`" + `)
  without explicit user confirmation — external side effects.
- Don't write secrets to ` + "`mus.cfg`" + ` — they go in the keyring via
  ` + "`mus eln login`" + ` / ` + "`mus s3 login`" + ` (iRODS auth lives in
  iron's own store; use ` + "`iron auth`" + `).
- Don't silently ` + "`--force`" + ` past a vocab error — surface the value
  to the user; offer EITHER to broaden the vocab upstream (file an
  issue at codeberg.org/mfiers/mus) OR to ` + "`--force`" + ` locally with
  a clear note about the consequences.
- Don't suggest editing ` + "`*.mus`" + ` sidecars by hand — they're
  regenerated by ` + "`mus h5ad check`" + ` / ` + "`mus de check`" + ` /
  ` + "`mus tag`" + ` / ` + "`mus irods upload`" + `.
- Don't claim "upload succeeded" based on the command's exit code
  alone — run ` + "`mus irods check`" + ` (or ` + "`mus check`" + ` for
  local-only) to confirm sha256 round-trip.
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

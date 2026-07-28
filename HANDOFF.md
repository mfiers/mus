# mus — dev handoff

Last updated: 2026-07-28 (end of `mus serve v2.1` work).
Working repo: `codeberg.org/mfiers/mus` (SSH: `ssh://git@codeberg.org/mfiers/mus.git`).

## Snapshot

- **Released:** `v0.2.46` (tag `v0.2.46` on `main`, commit `cd319a8`).
- **VERSION file:** `0.2.47-dev` (next `make ship` → **v0.2.47**).
- **Working tree:** clean on `main`, in sync with `origin/main`.
- **Local binary:** `./bin/mus` (rebuild with `make build`; installed system-wide via `sudo install -m 755 bin/mus /usr/local/bin/mus`).

## Getting set up on a new machine

### Prerequisites

- **Go 1.24+** on `$PATH`. Older Go on the host is common; the Makefile prefers `/usr/local/go/bin/go` if present, so a self-installed Go there is picked up automatically.
- **Codeberg SSH key** in `~/.ssh/` — the `origin` remote is SSH-only.
- **Repo clone:**
  ```sh
  git clone ssh://git@codeberg.org/mfiers/mus.git
  cd mus
  ```
- **First build:** `make build` → `./bin/mus`. Zero external Go deps to fetch beyond `go.mod` (all vendored via modules).

### External CLIs mus shells out to

- `iron` — KU Leuven Mango CLI. Every `mus irods *` command shells to it. Install per <https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html>.
- `uv` — Python bootstrapper. Only touched by `mus h5ad check` (drives the `mus_h5ad_inspector.py` script via a `uv`-shebang). Not needed for the Go work.
- `pandoc` + `texlive-xetex` — only if you touch `mus eln upload` (which is currently deleted — `mus eln attach` doesn't need it).

### mus's own auth

Everything below writes to the user's OS keyring (or the age-encrypted file fallback on HPC):

```sh
mus eln login                                        # ELN token — interactive
mus secret set irods_username u0089478               # your KU Leuven u-number
                                                     #   used by `mus serve`'s auto iron auth
mus secret set eln_sample_location 'BADS Mango'      # optional; ELN storage unit for
                                                     #   `mus eln sample`-created samples
mus secret set eln_irods_sample_location 'BADS Mango'#   ... and for `mus irods upload`
                                                     #   auto-registered samples
```

Verify with `mus secret list`. `mus eln whoami` confirms the ELN token works.

### For maintainer-only work (releases)

- **Pika signing key** in `~/.config/pika/` — required for `make ship` (ed25519-signs every release binary). No CI reproduction of releases without this.
- **SSH commit signing** — the repo pins `commit.gpgsign=true` locally via its own git config (no global mutation). Uses `~/.ssh/id_ed25519`.
- **`CODEBERG_TOKEN`** in the shell env — needed for `make publish` (uploads dist artifacts via the Codeberg API).

## Recent activity (last ~2 weeks)

Development has clustered around **ELN integration** and the **serve GUI**. Rough chronology:

### v0.2.40 — v0.2.42 (early July)
Fixed a cascade of ELN sample-registration bugs found by live testing on the BADS Mango tenant:
- `mus eln sample` and the auto-register on `mus irods upload` now properly use `PUT` (not `POST`) on `/experiments/sections/{jid}/samples`.
- `CreateSample`'s double-POST retry loop was creating duplicate samples for every upload. Now: single POST, both response-shape parsers run locally against the same body.
- Server-side experiment-linkage check: every re-run of `mus eln sample` does `GET /samples/{id}?$expand=experiments` and treats a sample-no-longer-linked-to-this-experiment as an orphan (re-link into a fresh section). Catches the case where a depositor manually deleted a SAMPLESOUT section.
- `UploadFile` was sending multipart-wrapped body to a `POST /experiments/sections/{jid}/files` endpoint that expects raw octet-stream. Server returned HTTP 500 with empty body — very opaque failure. Now raw body + `Content-Type: application/octet-stream`.
- Meta-field autofill: `mus_data_project`, `sha256sum`, `mtime`, `upload_data`, `upload_user`, `upload_from_server`, `upload_from_path`, `purl`, `mango_purl`, `kind`, `mus_version` — populated on samples whose sample-type declares any of these fields (case-insensitive match).
- Diagnostic subcommand: `mus eln sample-type [Data]` prints the tenant's meta-field definitions with a mus-can-autofill mapping column.
- Two independent storage-location keys (`eln_sample_location` for local flow, `eln_irods_sample_location` for iRODS flow) so labs can route local drafts vs archived samples to different storage units.

### v0.2.43 — v0.2.44 (mid-July)
- `mus irods upload` prompts extended with `[A]` (archive + clean archive) and `[X]` (archive + clean archive + clean source). Corresponding flags `--cleanup-archive` and `--cleanup-source`. Source cleanup flips the sidecar's `irods_status=local_deleted` — same tombstone semantic as `mus rm`.

### v0.2.45 — v0.2.46 (past week)
- **`mus eln retrofit` subcommand** — the big one. Converts legacy `mus: uploaded ...` HTML COMMENTs on an ELN experiment into structured SAMPLESOUT sections with proper sample records. Two HTML formats supported (modern Go-mus + pre-Go Python mus). Dry-run by default; `--apply` to execute. Extensive flags:
  - `--tree PATH` — iron-tree NDJSON dump for sha256 lookup fallback
  - `--data-project NAME` + `--force-data-project` — safety-halt if declared value disagrees with HTML/AVU/elnmap
  - `--move-danglers` — reclaim leftover files whose AVU says they belong to this experiment
  - `--sweep-legacy` + `--legacy-source PATH` — scan the predicted `lab_data_archive/<study>/<experiment>/` collection, prompt, move stragglers (files + subcollections), create sample records
  - `--archive-comments` (default true) — annotate + soft-delete source COMMENTs after retrofit
  - Full report structure: `▶ retrofit plan` / `▶ side effects` / `▶ danglers` / `▶ legacy sweep` / `✓ or ⚠ verdict`
  - Post-move leftover check: iron ls each source parent, warn about unaccounted files.
- **`mus serve` v2** — same-file rewrite of the UI:
  - Full-width top header with SVG logo, `experiment_id` + `data_project` inputs, Link/Suggest buttons, ELN + iRODS status badges.
  - 3 equal-width panels below: Metadata | Files | Submit & upload.
  - **Auto iron auth at startup** if `irods_username` is stored + stdin is a TTY. Iron prompts for password/SSO in the terminal; probe re-runs on completion.
  - Live auth banner refresh: browser polls `/api/auth-refresh` (force re-probe) every 3s while unauthed, every 30s while authed. `visibilitychange` + `window.focus` handlers re-probe on tab return.
  - Resizable dividers between the 3 panels; widths persist to `localStorage`; double-click any divider to reset to equal thirds.
  - Path guard hardened: `EvalSymlinks` on both root and target before the `filepath.Rel` check.
  - Regression guard on the embedded HTML (`TestServeIndex_EmbeddedHTMLIsPresent`) pins the new IDs and refuses the removed ones.

## Threads still open

### Definitely more work needed

- **Explorer / Finder / Nautilus right-click integration for `mus serve`.** Design mapped out in the earlier conversation; not implemented. Needs a Windows/Mac machine to test. Rough plan:
  - Windows: `mus install-shell` writes registry entries under `HKCU\Software\Classes\Directory\...\shell\Mus\command` using `golang.org/x/sys/windows/registry`.
  - macOS: Automator Quick Action workflow bundle installed to `~/Library/Services/`.
  - Linux: per-DE scripts (Nautilus / KDE / XFCE).

- **Windows real-world testing.** The path-guard hardening + `openInBrowser` support Windows, but no one's actually run mus serve on Windows yet. Priorities in order:
  1. Try a fresh `go build` for `windows/amd64` cross-compile; confirm no CGO surprises.
  2. Run `./bin/mus.exe serve` from PowerShell in a real folder — check auth probe, browser open, path navigation, upload path.
  3. Try `mus serve` from a Cygwin/Git-Bash shell too — different stdin type may confuse the TTY check.

### Nice-to-have

- **`mus irods projects --details` and `mus eln retrofit` lab-wide sweep** could use the `iron_tree` DuckDB pattern from the retrofit design — see the DuckDB build described in earlier conversation (loaded at `tmp/retrofit/mango.duckdb`). ~200 lines to expose as a `mus irods audit` subcommand that finds pre-0.2.33 layout files across the whole BADS tree.
- **PATCH-existing-samples on iRODS-side rename.** `mus eln retrofit` currently only touches SAMPLES on the retrofit run itself. If someone `iron mv`s a file later, the sample's `path` meta goes stale. Deferred design: `mus eln sync-samples` — reconcile all mus-created samples against current iRODS state.

## Where things live (cheat sheet)

| Topic | File |
|---|---|
| CLI root, subcommand registration | `internal/cli/root.go` |
| `mus serve` bootstrap + auth probe + auto-iron-auth | `internal/cli/serve.go` |
| `mus serve` API endpoints (tree / cascade / config / eln / preflight / upload) | `internal/cli/serve_handlers.go`, `internal/cli/serve_actions.go` |
| `mus serve` UI (single-file HTML + CSS + JS, `go:embed`'d) | `internal/cli/ui/index.html` |
| `mus eln retrofit` — planner + apply + parsers + dangler / legacy-sweep | `internal/cli/eln_retrofit.go` |
| `mus eln sample` / `mus eln attach` / `mus eln login` etc. | `internal/cli/eln.go`, `internal/cli/eln_sample.go`, `internal/cli/eln_sampletype.go` |
| `mus irods upload` + folder / archive / dry-run flow | `internal/cli/irods.go`, `internal/cli/irods_folder.go` |
| iRODS post-upload auto-register-as-sample | `internal/cli/eln_postupload.go` |
| ELN HTTP client (samples, sections, experiment info, storage) | `internal/eln/eln.go` |
| Iron CLI wrapper (upload / mv / stat / query / chmod / meta / auth probe) | `internal/iron/iron.go` |
| Sidecar reader / writer (`*.mus`) | `internal/sidecar/sidecar.go` |
| Cascading config (`mus.cfg`, legacy `.env`) | `internal/config/config.go`, `internal/config/migrate.go` |
| Secret store (keyring / age) | `internal/secret/secret.go` |
| Site defaults (baked-in fallbacks) | `internal/defaults/defaults.go` |

## Build / test / release commands

```sh
# Everyday work
make build                # host binary → ./bin/mus
make test                 # go test ./... (no network)
make show-version         # print VERSION file + next planned version
go vet ./...              # cross-check

# Release (maintainer only)
make ship                 # LEVEL=patch default; strip -dev, sign, tag, push, publish
make ship LEVEL=minor
make ship LEVEL=major
# Prompts for pika passphrase once (per-binary ed25519 signing).

# Diagnostics
mus config show           # effective cascade in the current folder
mus config files          # every mus.cfg that contributes (root-most first)
mus eln whoami            # ELN token round-trip
mus irods login           # nudge-only; runs `iron pwd` under the hood
mus secret list           # what's in the keyring
```

## Style / conventions in this repo

Written in the project `CLAUDE.md` — worth re-reading before working. Key points that come up a lot:

- **No `-dev` in released VERSION.** VERSION file always ends in `-dev` when working; `make ship` strips it, then bumps + re-adds after.
- **Exported funcs / types in `internal/` get short doc comments.** Unexported helpers usually don't.
- **File writes go through temp-file + `os.Rename`** for atomicity (sidecars, config files).
- **Concurrency: use a worker pool sized to `runtime.NumCPU()`.** No goroutine-per-file explosions.
- **Errors wrap with `fmt.Errorf("...: %w", err)`.**
- **Interactive prompts must handle non-TTY gracefully** — refuse loudly, don't hang.
- **`make doc-precheck`** runs before every ship and asserts the bundled Claude-skill text still contains the load-bearing phrases. Update those when you change user-facing behaviour (`internal/cli/mus_skill_body.go` + `internal/cli/skills_test.go`).
- **Sidecar and mus.cfg share the KEY=VALUE grammar** (`internal/envformat`).

## To resume `mus serve` polish on another machine

1. `git pull` (should be no-op — everything's on `main`).
2. `make build`; `./bin/mus serve` — sanity check the new UI loads.
3. If iron auth is missing: `mus secret set irods_username u<yours>`; re-run `mus serve` to see the auto-auth path fire.
4. Try the resizable dividers (drag them; double-click to reset).
5. Load an ELN-linked folder; fill the header experiment_id + data_project; click Link.
6. Next targets, in priority:
   - Windows Explorer right-click integration (`mus install-shell` subcommand).
   - macOS Automator Quick Action.
   - Real Windows/Mac smoke-test of the whole flow (auth probe, tree nav, upload).

## Who else is working on this

Solo — Mark Fiers is the only committer on `origin/main`. `.gitsigners` trust root has one entry (`mark.fiers@kuleuven.be`). If that changes, add the new key + rebuild `dist/SHA256SUMS.sig` accordingly.

## When you're stuck

- `/data/1/users/mark/_backup_mus/` — the legacy Python mus. Reference freely for iRODS / ELN protocol details, but do NOT port the activity-log / `mus tag` history features (intentionally dropped).
- `~/.local/share/mus/` — mus's per-user state (hashcache, elnmap, local_projects, irods-scans, vocab cache).
- The eLabNext docs live at `https://developerdocs.elabnext.com/llms.txt` (index) — actual reference pages are `.md` files under `/reference/`. Use `curl` + grep against llms.txt to find endpoint names; then WebFetch or curl the specific `/reference/foo.md`.

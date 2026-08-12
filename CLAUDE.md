# mus — research data management CLI (Go rewrite)

## What this project does

`mus` is a fast, cross-platform CLI for research data management at the BADS lab. It:

- Reads cascading `mus.cfg` files (flat KEY=VALUE) to discover project/study/experiment metadata for the working directory. Legacy `.env` is still read for backwards compatibility; `mus config migrate` moves mus-recognised keys to `mus.cfg`, leaving secrets/unknowns in `.env` with loud warnings. Format matches the legacy Python `mus`.
- Writes `*.mus` sidecar files (same KEY=VALUE grammar) next to each data file, holding sha256 + size + mtime + upload status.
- Maintains a local SQLite hash cache so repeated checksum checks are stat-fast.
- Uploads / downloads / verifies files on the KU Leuven Mango iRODS instance via the `iron` CLI.
- Synchronises with the eLabJournal-based ELN (link a folder to an experiment, post comments, upload files).
- (Planned) Syncs `*.h5ad` files to S3.

This is a ground-up Go rewrite of the Python `mus` at `/data/1/users/mark/_backup_mus/`. Reference the old code freely but do **not** port the activity-log / `mus tag` history / `mus search` features — they are intentionally dropped.

## Status

Bootstrap: 2026-05-22. Released: `v0.2.46`. VERSION: `0.2.47-dev` (next `make ship` → v0.2.47).

As of the last session there is a **large uncommitted stack on `main`** (all in
the working tree, not yet committed) centred on `mus serve`:

- Serve UI fixes: `[hidden]` CSS banner bug; connection-lost heartbeat overlay;
  cascade/known-keys JSON-shape drift; status bar; two-step ELN link → set
  data_project flow; info-icon tooltips; red/green upload button; upload-block
  and upload-error dialogs; file-list refresh + auto-poll.
- Skip-already-uploaded now checks `doc.Stale()` (size/mtime) so an edited file
  re-uploads; `--force` (overwrite) + `--pack` surfaced as UI checkboxes.
- `mus eln rebuild-map` — new subcommand; rebuilds the elnmap store from iRODS
  AVUs, read-only (guarded by `TestRebuildMap_IsReadOnlyAgainstIRODS`).
- `elnmap` gained tiered experiment→study→project data_project lookup.

Untracked cruft in the tree: `test/busy/` (a stray partial source snapshot —
breaks `go test ./...`), `.omp/`, `.playwright-mcp/`, `internal/mus.cfg`.

See `HANDOFF.md` for the prior (v0.2.46) handover and `report/` for status.

## Key design decisions (do not silently revisit)

| Concern | Decision |
| --- | --- |
| Language | Go 1.24+ (builds run on 1.26.x), pure-Go (no CGO) so cross-compile works for darwin/arm64, linux/amd64, linux/arm64 |
| CLI | `spf13/cobra`, git-style subcommands |
| iRODS | Shell out to `iron` (KU Leuven RDM CLI); no native iRODS client |
| Folder config | `mus.cfg` (flat KEY=VALUE), cascading up the directory tree — same grammar as legacy Python mus. Legacy `.env` still read; `mus config migrate` moves mus-recognised keys to `mus.cfg`. |
| Per-file metadata | `foo.h5ad.mus` (same KEY=VALUE grammar; flat keys with prefix conventions: `irods_*`, `eln_*`, `s3_*`) |
| Hash cache | SQLite via `modernc.org/sqlite` at `~/.local/share/mus/hashcache.db` |
| Secrets | `zalando/go-keyring` with `filippo.io/age` encrypted-file fallback for HPC nodes |
| ELN | eLabJournal REST API |
| ELN enforcement | every folder declares status: `mus eln link <id>` OR `mus eln disable [--global]`. `mus irods upload` refuses while undecided. Global state at `$XDG_CONFIG_HOME/mus/mus.cfg` (default `~/.config/mus/mus.cfg`; legacy `~/.config/mus/.env` still read), injected as root-most cascade entry. |
| Activity log | **removed** — not part of the new tool |

## Layout

```
mus/
  CLAUDE.md            (this file)
  Makefile             cross-compile + ship + publish targets
  VERSION              single source of truth; `X.Y.Z-dev` form
  go.mod / go.sum
  install.sh           one-liner installer (signature-verifies before placing on PATH)
  cmd/mus/             main.go entrypoint (cobra root)
  internal/
    envformat/         KEY=VALUE parser used by both config and sidecar
    config/            mus.cfg / legacy .env cascade reader (folder-level)
    secret/            keyring + age fallback
    hashcache/         SQLite-backed sha256 cache (file-level)
    sidecar/           *.mus reader / writer (file + folder variants)
    iron/              shellout wrapper around the iron CLI
    eln/               eLabJournal REST client
    elnmap/            JSON-backed experiment_id → data_project mapping store
                       (local cache + Stage 2 iRODS-shared file)
    dataproject/       data_project regex validator + path sanitiser
    defaults/          ldflags-overridable site defaults (irods_home / web / pid_base)
    folder/            folder profile, density check, tar.gz packer, Merkle hash
    irodscache/        SQLite-backed iron-tree snapshots (mus irods scan)
    signing/           ed25519 verifier with embedded pubkey (mus upgrade / install)
    cli/               cobra subcommand definitions
      ui/index.html    the `mus serve` browser GUI — single go:embed'd file
                       (HTML + CSS + JS). See "mus serve — GUI notes" below.
  scripts/             release helper scripts (publish-release.sh, next-version.sh)
  doc/                 user-facing documentation (quickstart, this README target)
  data/                test fixtures (gitignored beyond manifest)
  report/              status reports
  .forgejo/workflows/  Codeberg CI (test + cross-build matrix)
```

## Build / test / release

```
make build              # host binary -> ./bin/mus
make build-all          # darwin/arm64 + linux/amd64 + linux/arm64 -> ./dist/
make test               # go test ./...  (but see caveat below)
make show-version       # print VERSION file + next planned version
make bump [LEVEL=minor] # bump VERSION file (no release)
make ship [LEVEL=minor] # full release pipeline (interactive — passphrase once)
make release-verify     # verify dist/SHA256SUMS.sig against .gitsigners
make clean
```

`make ship` is the maintainer's release pipeline:
  1. Strip `-dev` from VERSION → that's the released version.
  2. Commit the pin.
  3. `make release` (cross-build, ed25519-sign with pika, ssh-sign SHA256SUMS,
     signed git tag). Pika prompts for passphrase ONCE here.
  4. Bump VERSION to next `LEVEL`-dev and commit.
  5. Push main + tag.
  6. `make publish` (Codeberg API uploads via CODEBERG_TOKEN).

Lower-level targets (`make release VERSION=…`, `make publish VERSION=…`) are
still available for partial reruns.

VERSION file convention: holds the version that the *next* `make ship` will
ship, plus a `-dev` suffix during development. Single source of truth — no
separate `Version` constant to update.

The host `go` may be too old — Makefile uses `/usr/local/go/bin/go` if present.
On the maintainer's Mac that path does NOT exist; plain `go` (1.26.x) is used.

**`go test ./...` is currently polluted** by the untracked `test/busy/` stray
tree (its vocab test fails on a missing `../../vocabulary/main.yaml`). Until
`test/busy/` is deleted, run `go test ./internal/... ./cmd/...` for a clean run.

`make doc-precheck` (runs before every `make ship`) asserts the bundled
Claude-skill text (`internal/cli/mus_skill_body.go`) still contains its
load-bearing phrases, pinned by `internal/cli/skills_test.go`. Update both when
you change user-facing serve/CLI behaviour.

## Signing

Releases use **two complementary signing schemes**. Both run automatically
inside `make release`:

1. **Per-binary ed25519** (`*.sig` next to each binary) — for the
   programmatic upgrade path. `make sign` shells out to `pika _sign` from the
   sibling project; pika owns the only private key file. `mus upgrade`
   verifies every downloaded binary against the pubkey embedded in
   `internal/signing.PubkeyB64`. The same key signs every Go binary in this
   maintainer's projects — do NOT regenerate per-project. See
   `/data/1/users/mark/pika/doc/signing.md` for the full design + threat model.
2. **SSH-signed `SHA256SUMS`** — for the manual verification path. The
   maintainer's `~/.ssh/id_ed25519` signs `dist/SHA256SUMS` via
   `ssh-keygen -Y sign -n file` → `dist/SHA256SUMS.sig`. `.gitsigners` is the
   trust root, cross-checked against `https://codeberg.org/mfiers.keys`.

Tags and commits are also SSH-signed via the repo-local
`commit.gpgsign=true`, `tag.gpgsign=true`, `gpg.format=ssh`,
`user.signingkey=~/.ssh/id_ed25519.pub` (no `~/.gitconfig` mutation).

Verifier workflow:

```
git -c gpg.ssh.allowedSignersFile=.gitsigners verify-tag v0.1.0
ssh-keygen -Y verify -f .gitsigners \
    -I mark.fiers@kuleuven.be \
    -n file -s dist/SHA256SUMS.sig < dist/SHA256SUMS
sha256sum -c dist/SHA256SUMS
```

If `.gitsigners` ever grows multiple maintainers, give each an entry of the
form `principal namespaces="git,file" keytype base64`. Empty lines in the file
trigger an "invalid key" parser warning — keep it dense.

## Remotes

`origin` → `ssh://git@codeberg.org/mfiers/mus.git`. Pushes are deliberately
manual — `make release` only prepares artifacts and prints the push command.

## mus serve — GUI notes (learned the hard way)

`mus serve` boots a localhost web GUI (`internal/cli/ui/index.html`, go:embed'd)
for annotate + upload. Gotchas that each cost real debugging time:

- **PATH shadowing / testing the right binary.** On the maintainer's Mac, `mus`
  resolves to `~/bin/mus`, which precedes `/usr/local/bin`. After `make build`,
  reinstall with `install -m 755 bin/mus ~/bin/mus` (no sudo) OR test via
  `./bin/mus` directly. Skipping this makes "fixed" bugs still reproduce because
  the shell runs the OLD binary — the cause of two separate false
  "Authentication required" reports.
- **Test it live**, don't eyeball the HTML: `./bin/mus serve --no-browser --port
  8199`, then drive the printed `?t=<token>` URL (Playwright MCP works well).
- **JS ↔ handler contract drift** is the recurring bug class. The embedded JS
  must match the JSON shape the Go handlers emit; it has silently broken twice
  (cascade `values:[{key,value,local}]` array; `/api/known-keys` group names).
  When touching either side, curl the endpoint and compare.
- **`hidden` attribute vs CSS `display`.** Any element with an explicit CSS
  `display` defeats the HTML `hidden` attribute. The UI depends on
  `[hidden]{display:none!important}` — keep it. (This was the original
  "Authentication required" banner-won't-hide bug.)
- **Non-TTY subprocess.** `mus serve` shells out to `mus` subcommands with no
  TTY, so any interactive prompt (overwrite-differing-bytes, pack-density) hard-
  fails. Every such prompt needs a non-interactive flag (`--force`,
  `--pack tar.gz`) surfaced as a UI control, not a terminal prompt.
- **Auth banner.** `/api/auth-status` / `/api/auth-refresh` just run `iron pwd` +
  an ELN whoami. Both green ⇒ banner must stay hidden. Banner showing while
  authed ⇒ suspect a stale binary or stale browser tab, not the probe.
- **data_project auto-propose** keys on the elnmap experiment→study→project
  tiers, which only populate for experiments linked/uploaded AFTER those fields
  existed. `mus eln rebuild-map` backfills from iRODS AVUs (read-only).

## Conventions

- All exported funcs / types in `internal/` have a short doc comment. Unexported helpers usually don't.
- No package-level state except for explicit caches (hashcache, config). No globals for secrets.
- Errors wrap with `fmt.Errorf("...: %w", err)`; user-facing CLI errors go through a single formatter in `internal/cli`.
- Paths: always absolute internally. Convert at the CLI boundary.
- File writes that update a sidecar or config: temp-file + `os.Rename` for atomicity.
- Concurrency: when scanning many files, use a worker pool sized to `runtime.NumCPU()`. Don't spin one goroutine per file.
- `mus serve` UI is one go:embed'd `internal/cli/ui/index.html`; its JS must track the handlers' JSON shapes (see "mus serve — GUI notes"). The embedded-HTML regression test in `serve_actions_test.go` pins load-bearing element IDs / endpoints — update it when you add or rename them.

## External tools required at runtime

- `iron` — KU Leuven iRODS CLI. `mus irods *` commands fail loudly if missing.
- `pandoc` + `texlive-xetex` — only if using `mus eln upload` on `.ipynb` files (PDF conversion). Optional.

## Things deliberately NOT done

- No activity log / search over a history table.
- No plugin system. Subsystems live in `internal/` and are wired into cobra explicitly.
- No remote DB / collaboration features. The hash cache is local-only.
- No symlink chasing by default — sidecar covers the symlink itself unless `--follow-symlinks`.

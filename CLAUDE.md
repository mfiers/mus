# mus — research data management CLI (Go rewrite)

## What this project does

`mus` is a fast, cross-platform CLI for research data management at the BADS lab. It:

- Reads cascading `.env` files (flat KEY=VALUE) to discover project/study/experiment metadata for the working directory. Format intentionally matches the legacy Python `mus`.
- Writes `*.mus` sidecar files (same KEY=VALUE grammar) next to each data file, holding sha256 + size + mtime + upload status.
- Maintains a local SQLite hash cache so repeated checksum checks are stat-fast.
- Uploads / downloads / verifies files on the KU Leuven Mango iRODS instance via the `iron` CLI.
- Synchronises with the eLabJournal-based ELN (link a folder to an experiment, post comments, upload files).
- (Planned) Syncs `*.h5ad` files to S3.

This is a ground-up Go rewrite of the Python `mus` at `/data/1/users/mark/_backup_mus/`. Reference the old code freely but do **not** port the activity-log / `mus tag` history / `mus search` features — they are intentionally dropped.

## Status

Bootstrap: 2026-05-22. See task list / `report/` for current state.

## Key design decisions (do not silently revisit)

| Concern | Decision |
| --- | --- |
| Language | Go 1.24, pure-Go (no CGO) so cross-compile works for darwin/arm64, linux/amd64, linux/arm64 |
| CLI | `spf13/cobra`, git-style subcommands |
| iRODS | Shell out to `iron` (KU Leuven RDM CLI); no native iRODS client |
| Folder config | `.env` (flat KEY=VALUE), cascading up the directory tree — same grammar as legacy Python mus |
| Per-file metadata | `foo.h5ad.mus` (same KEY=VALUE grammar; flat keys with prefix conventions: `irods_*`, `eln_*`, `s3_*`) |
| Hash cache | SQLite via `modernc.org/sqlite` at `~/.local/share/mus/hashcache.db` |
| Secrets | `zalando/go-keyring` with `filippo.io/age` encrypted-file fallback for HPC nodes |
| ELN | eLabJournal REST API |
| Activity log | **removed** — not part of the new tool |

## Layout

```
mus/
  CLAUDE.md            (this file)
  Makefile             cross-compile targets
  go.mod / go.sum
  cmd/mus/             main.go entrypoint (cobra root)
  internal/
    envformat/         KEY=VALUE parser used by both config and sidecar
    config/            .env cascade reader (folder-level)
    secret/            keyring + age fallback
    hashcache/         SQLite-backed sha256 cache
    sidecar/           *.mus reader / writer
    iron/              shellout wrapper around the iron CLI
    eln/               eLabJournal REST client
    cli/               cobra subcommand definitions
  data/                test fixtures (gitignored beyond manifest)
  doc/                 design notes
  report/              status reports
```

## Build / test / release

```
make build              # host binary -> ./bin/mus
make build-all          # darwin/arm64 + linux/amd64 + linux/arm64 -> ./dist/
make test               # go test ./...
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
   trust root, cross-checked against `https://codeberg.org/atrxia.keys`.

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

`origin` → `ssh://git@codeberg.org/atrxia/mus.git`. Pushes are deliberately
manual — `make release` only prepares artifacts and prints the push command.

## Conventions

- All exported funcs / types in `internal/` have a short doc comment. Unexported helpers usually don't.
- No package-level state except for explicit caches (hashcache, config). No globals for secrets.
- Errors wrap with `fmt.Errorf("...: %w", err)`; user-facing CLI errors go through a single formatter in `internal/cli`.
- Paths: always absolute internally. Convert at the CLI boundary.
- File writes that update a sidecar or config: temp-file + `os.Rename` for atomicity.
- Concurrency: when scanning many files, use a worker pool sized to `runtime.NumCPU()`. Don't spin one goroutine per file.

## External tools required at runtime

- `iron` — KU Leuven iRODS CLI. `mus irods *` commands fail loudly if missing.
- `pandoc` + `texlive-xetex` — only if using `mus eln upload` on `.ipynb` files (PDF conversion). Optional.

## Things deliberately NOT done

- No activity log / search over a history table.
- No plugin system. Subsystems live in `internal/` and are wired into cobra explicitly.
- No remote DB / collaboration features. The hash cache is local-only.
- No symlink chasing by default — sidecar covers the symlink itself unless `--follow-symlinks`.

# mus — research data management CLI (Go rewrite)

## What this project does

`mus` is a fast, cross-platform CLI for research data management at the BADS lab. It:

- Reads cascading `.mus` TOML files to discover project/study/experiment metadata for the working directory.
- Writes `*.mus` TOML sidecar files holding sha256 + size + mtime + upload status for individual data files.
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
| Folder config | `.mus` (TOML), cascading up the directory tree |
| Per-file metadata | `foo.h5ad.mus` (TOML sidecar) |
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
    config/            .mus TOML cascade reader
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
make release VERSION=x.y.z   # signed cross-build + signed tag
make release-verify     # verify dist/SHA256SUMS.sig against .gitsigners
make clean
```

The host `go` may be too old — Makefile uses `/usr/local/go/bin/go` if present.

## Signing

Releases are signed end-to-end with the maintainer's SSH key (no GPG):

- **Tags + commits**: `git config commit.gpgsign=true tag.gpgsign=true` with
  `gpg.format=ssh` and `user.signingkey=~/.ssh/id_ed25519.pub` (configured at
  the *repo* level — does not touch `~/.gitconfig`).
- **Release artifacts**: `make release` cross-builds, generates
  `dist/SHA256SUMS`, and signs it with `ssh-keygen -Y sign -n file` →
  `dist/SHA256SUMS.sig`.
- **Trust root**: `.gitsigners` (checked into the repo) lists the allowed
  signer keys. Verifiers should cross-check those keys against
  `https://codeberg.org/atrxia.keys` before trusting blind.

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

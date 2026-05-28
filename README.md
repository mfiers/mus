# mus

> Research data management CLI — tag files with checksums, sync to iRODS via IRON, push metadata to eLabJournal.

`mus` walks a directory tree, reads `.env` files cascading up the tree (project / study / experiment context), and writes one `*.mus` sidecar per data file holding its sha256, size, mtime, and remote-storage status. Both file types use the same flat `KEY=VALUE` format. The CLI then uses those sidecars to sync to the KU Leuven Mango iRODS service via the `iron` CLI and to stamp metadata for eLabJournal.

Ground-up Go rewrite of the previous Python `mus`. Statically-linked, no CGO, ~10 MB per platform.

## Quick Start

**Install** (Linux x86_64 / Linux ARM64 / macOS Apple Silicon):

```bash
curl -sSL https://codeberg.org/atrxia/mus/raw/branch/main/install.sh | bash
```

The installer auto-detects your platform, downloads the latest release binary, **verifies its ed25519 signature** against the embedded pubkey, and places it in the first writable directory it finds in your `$PATH` — preferring `~/bin`, then `~/.local/bin`, then `/usr/local/bin`. If `mus` is already installed somewhere writable it'll upgrade in place.

Pin a specific version or override the location:

```bash
MUS_VERSION=v0.1.0 MUS_INSTALL_DIR=/opt/mus \
  curl -sSL https://codeberg.org/atrxia/mus/raw/branch/main/install.sh | bash
```

**Upgrade** an already-installed mus:

```bash
mus upgrade           # update to the latest release
mus upgrade --check   # only report if an update is available
mus upgrade --tag v0.2.0   # install a specific tag
mus version           # print the installed version
```

The `upgrade` command refuses unsigned releases — if a release has no `.sig` asset, the upgrade aborts.

**Manual install** (or for unsupported platforms — build from source):

Download from the [latest release](https://codeberg.org/atrxia/mus/releases/latest) and put the binary somewhere on `$PATH`:

| Platform | File |
|---|---|
| Linux x86_64 | `mus-linux-amd64` |
| Linux ARM64 | `mus-linux-arm64` |
| macOS Apple Silicon (M1–M4) | `mus-darwin-arm64` |

Each binary ships with a matching `.sig` file. Verify before running:

```bash
# Using mus itself (if you already have a trusted copy):
mus _verify mus-linux-amd64 mus-linux-amd64.sig

# Or using sha256sum + the ssh-signed SHA256SUMS:
ssh-keygen -Y verify -f .gitsigners -I mark.fiers@kuleuven.be \
  -n file -s SHA256SUMS.sig < SHA256SUMS
sha256sum -c SHA256SUMS
```

See [Signature verification](#signature-verification) below.

**Configure credentials** (one-time):

```bash
mus secret set eln_url    https://your-eln-server/api/v1
mus secret set eln_apikey <your-api-key>
mus secret backend         # prints "keyring" or "age" — see Secrets below
```

**Use:**

```bash
# One-time setup: store an ELN API key (interactive wizard with on-screen
# instructions for generating the token in the eLabJournal web UI).
mus eln login

# Link the current folder to an ELN experiment. Fetches project/study/
# experiment names from the API and writes them into the local .env.
mus eln tag-folder -x 12345

# Tag data files with sha256 + metadata:
mus tag data1.csv data2.csv -m "raw sequencing data" -t qc-pass

# Verify integrity later:
mus check               # checks every *.mus in the current dir
mus check -r .          # recurse
mus check data1.csv     # one file

# Upload to iRODS via IRON (requires the iron CLI on PATH):
mus irods upload data1.csv data2.csv --verify
mus irods check         # verify local sha256 against remote
mus irods get data1.csv.mus   # download from the sidecar's recorded path
```

### ELN integration

`mus eln login` is the interactive entry point. It walks you through
generating an API token in your ELN instance (default tenant
`vib.elabjournal.com`; other VIB / KU Leuven hosts also work), accepts the
full `host;key` form the web UI emits, **verifies the token** with a
`GET /users/getCurrentUserInfo` round-trip before storing it, then writes
`eln_url` + `eln_apikey` to the OS keyring (or the age-encrypted fallback
file on hosts without a keyring).

Once stored:

```bash
mus eln whoami                 # who does the stored token authenticate as?
mus eln tag-folder -x 12345    # link folder to experiment; fetches names from API
mus eln update                 # refresh names if renamed on the server
```

`tag-folder` refuses to overwrite an existing linkage in the same folder —
use `mus eln update` to refresh, or `tag-folder -x ID --force` to relink.

## How it works

### Folder config: cascading `.env`

`mus` walks the directory tree upward looking for `.env` files and merges them, root-most first. Closer (deeper) files override or extend ones higher up. The grammar is a flat `KEY=VALUE` format — one pair per line, `#` for comments, blank lines ignored. Two keys (`tag`, `collaborator`) are list-valued: comma-separated, and a value prefixed with `-` removes a previously-added entry.

Typical lab layout:

```sh
# /lab/projects/.env  — lab-wide defaults
irods_home=/zone/home/lab
irods_web=https://mango.kuleuven.be/data-object/view
tag=lab,shared

# /lab/projects/project_alpha/exp_42/.env  — per-experiment override
tag=exp42,-lab                  # drops the inherited "lab" tag
eln_experiment_id=12345         # `mus eln tag-folder -x 12345` writes this
# irods_path=alpha/raw/exp_42   # optional; defaults to exp_<eln_experiment_id>
```

Inspect:

```bash
mus config show          # effective (cascaded) config
mus config show --local  # only the .env in the current folder
mus config files         # paths of every .env contributing to the cascade
mus config set tag exp42 # writes to the local .env
```

### Per-file sidecars: `<datafile>.mus`

`mus tag data.csv -m "raw"` writes `data.csv.mus` next to the data file. Same flat `KEY=VALUE` format as `.env`. Fields recorded:

- File: `sha256`, `size`, `mtime`, `hashed`, `host`, `abspath`
- iRODS (after `mus irods upload`): `irods_url`, `irods_path`, `irods_status`, `irods_uploaded_at`
- ELN (from the `.env` cascade): `eln_experiment_id` (and `eln_*_name` / `eln_*_id` if available)
- S3 (planned): `s3_url`, `s3_bucket`, `s3_key`, `s3_etag`, `s3_uploaded_at`
- Free-form: `note`, `tags` (comma-separated)
- Bookkeeping: `version`, `created`, `updated`

A local SQLite cache at `~/.local/share/mus/hashcache.db` (override with `MUS_HASHCACHE_DB`) makes repeat sha256 lookups stat-fast: entries are reused only when both size and mtime match. Modify a file and the next `mus check` rehashes it.

### Secrets

`mus secret` uses the OS keyring (Linux Secret Service, macOS Keychain, Windows Credential Manager) when available. On HPC compute nodes and headless containers that have no Secret Service running, it falls back to an age-encrypted file at `~/.config/mus/secrets.age` with the identity key at `~/.config/mus/secrets.key` (mode 0600). The first call to a secret command decides which backend the process uses; force a backend with `MUS_SECRET_BACKEND=keyring|age`.

```bash
mus secret list
mus secret get eln_apikey
mus secret delete eln_apikey
```

### Signature verification

Releases carry **two complementary signatures**:

1. **Per-binary ed25519 `.sig`** (`<binary>.sig` next to each release binary) — for the programmatic upgrade path. Signed by the shared maintainer key (same key signs all of `atrxia`'s Go projects); pubkey embedded in `internal/signing.PubkeyB64`. `mus upgrade` and `mus _verify` verify against this.
2. **SSH-signed `SHA256SUMS`** (`SHA256SUMS` + `SHA256SUMS.sig`) — for the manual verification path. Signed with the maintainer's `~/.ssh/id_ed25519`. Trust root is `.gitsigners` in this repo (cross-check against `https://codeberg.org/atrxia.keys` before treating signatures as authoritative).

Tags and commits in this repo are also SSH-signed; verify any tag with:

```bash
git -c gpg.ssh.allowedSignersFile=.gitsigners verify-tag v0.1.0
```

## Subcommand reference

```
mus version                    print version + build info
mus config show [--json|--local]
mus config set KEY VALUE       write a key to the local .env
mus config files               list .env files in the cascade
mus secret set NAME [VALUE]    set a secret (reads stdin if VALUE omitted)
mus secret get NAME
mus secret list
mus secret delete NAME
mus secret backend             print active backend (keyring|age)
mus tag FILE...                write/refresh sidecars
                               -m note  -t tag  -f force-rehash
mus check [FILE_OR_DIR...]     verify sidecar sha256 against current files
                               -r recursive  -q quiet
mus eln login                  interactive: obtain + verify + store API token
mus eln tag-folder -x EXPID    link current dir to an ELN experiment (calls API)
mus eln update                 refresh ELN metadata from the server
mus eln whoami                 verify the stored token still works
mus irods upload FILE...       upload via IRON; writes/refreshes sidecars
                               -f force  -r recursive  --verify
mus irods check                verify local sha256 against IRON checksum
mus irods get SIDECAR...       download files referred to by sidecars
mus irods scan run PATH        snapshot a remote collection (with checksums) to local cache
mus irods scan show PATH       print every cached entry (--data-only, --prefix, etc.)
mus irods scan list            list cached scans
mus irods scan find CHECKSUM   locate iRODS objects by server-side checksum
mus irods scan rm PATH         drop a cached scan
mus s3 upload FILE...          stub — coming later
mus upgrade [--tag] [--check]  self-update from Codeberg
mus -C DIR ...                 run as if from DIR (like git -C)
```

## Building from source

Go 1.24+ required.

```bash
git clone ssh://git@codeberg.org/atrxia/mus.git
cd mus
make build              # ./bin/mus for your host
make build-all          # cross-compile darwin/arm64 + linux/{amd64,arm64} → ./dist/
make test               # go test ./...
```

Maintainer-only:

```bash
make show-version               # show what's in VERSION + what `ship` would do
make bump                       # bump patch in VERSION (no release)
make bump LEVEL=minor           # or minor / major
make ship                       # release + push + publish — one command
make ship LEVEL=minor           # ship as a minor bump (overrides patch default)
make ship SIGN=0                # skip ed25519 binary signing (fast debug;
                                # mus upgrade will REFUSE the resulting release)
```

`make ship` is the maintainer's full publishing pipeline. It reads VERSION
(e.g. `0.1.1-dev`), strips `-dev`, and ships that. It then auto-bumps VERSION
to the next `-dev` so the next dev cycle has a clean version. The only
interactive step is pika's passphrase prompt during signing. Requires
`CODEBERG_TOKEN` (or `CODEBERG_GENERIC_TOKEN`) in the environment.

Lower-level targets (each piece of `ship`) are still available if you need
to debug a stuck release:

```bash
make release VERSION=0.2.0      # cross-build, sign binaries via pika, ssh-sign
                                # SHA256SUMS, create signed git tag
git push origin main v0.2.0
make publish VERSION=0.2.0      # uses CODEBERG_TOKEN to upload assets
make sign                       # re-sign existing dist/ binaries (no rebuild)
make release-verify             # verify dist/SHA256SUMS.sig
```

See [doc/quickstart.md](doc/quickstart.md) for a longer worked example, and [CLAUDE.md](CLAUDE.md) for the architecture, signing scheme, and design decisions.

## Requirements

**Client:** None — single static binary.

**Optional integrations:**
- [`iron`](https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html) on `$PATH` for `mus irods *` commands.
- A reachable eLabJournal instance for `mus eln *` commands.
- A working OS keyring (or `age` fallback) for `mus secret`.

## License

MIT

## Links

- Source: https://codeberg.org/atrxia/mus
- Issues: https://codeberg.org/atrxia/mus/issues
- Releases: https://codeberg.org/atrxia/mus/releases

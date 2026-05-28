# mus

> Tag, verify, and ship research data. From your terminal, in under a minute.

`mus` is a fast little CLI that records SHA-256 checksums + provenance in sidecar files next to your data, syncs to KU Leuven Mango iRODS via the [iron](https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html) tool, and stamps every upload with metadata pulled from your eLabJournal experiments.

Single static binary, no Python, no dependencies. Linux x86_64 / Linux ARM64 / macOS Apple Silicon.

---

## Install

```bash
curl -sSL https://codeberg.org/mfiers/mus/raw/branch/main/install.sh | bash
```

The installer:
- detects your platform,
- downloads the latest release binary,
- **verifies its ed25519 signature** against the embedded maintainer pubkey,
- drops it in the first writable directory in `$PATH` (prefers `~/bin`, then `~/.local/bin`, then `/usr/local/bin`).

Re-run any time to upgrade. Or from inside mus:

```bash
mus upgrade
```

`mus upgrade` refuses unsigned releases by design — if the signature can't be verified, the install is aborted.

---

## First-run setup (≈ 2 minutes)

Three commands. Each is interactive and tells you exactly what to do.

```bash
mus irods login        # checks iron, points at the Mango portal if missing, runs `iron auth`
mus eln login          # prints the steps to generate a VIB ELN token, then stores it
mus completion install # tab-completion in bash/zsh/fish
```

After this, every command is keyring-backed and shell-aware. You'll likely never run these again on this machine.

---

## Daily use

```bash
# 1. In a project folder, link it to an ELN experiment.
#    mus prompts for a `data_project` name (NameYear, e.g. Fiers2025).
mus eln tag 12345

# 2. Tag individual data files with sha256 + metadata.
mus tag data.csv -m "raw sequencing reads" -t qc-pass

# 3. Sanity-check what's tagged at any later point.
mus check                  # verifies every *.mus in cwd
mus check -r .             # recursive
mus check data.csv         # single file

# 4. Push to iRODS. Each file gets a sidecar with sha256 + persistent
#    URL (PURL — survives renames + moves).
mus irods upload data.csv data2.csv

# 5. Push a whole folder. mus profiles it and offers to pack into
#    `<folder>.tar.gz` if there are many small files (configurable).
mus irods upload scripts/

# 6. Download later — skips the network round-trip if the local copy
#    already matches the sidecar's checksum; refuses to overwrite
#    a divergent local copy without --force.
mus irods get data.csv.mus
mus irods get scripts.mus
```

`mus irods upload` writes/updates two artifacts per upload:
- a `*.mus` sidecar next to the data (or next to the folder, for folder uploads),
- a set of `mus_*` AVU metadata triplets on the iRODS object itself, so the object stays self-describing even if the local sidecar is lost.

---

## Subcommand reference

| Command | What it does |
|---|---|
| `mus version` | Print version + commit + build date |
| `mus upgrade [--check] [--tag vX.Y.Z]` | Self-update from the latest signed Codeberg release |
| `mus config show/set/files` | Inspect or write `.env` (cascading folder config) |
| `mus secret set/get/list/delete/backend` | Manage credentials (OS keyring or age-encrypted file) |
| `mus tag FILE [-m NOTE] [-t TAG]` | Write/refresh a sidecar with sha256, size, mtime, ELN context |
| `mus check [FILE / DIR] [-r]` | Verify sidecars against current file/folder contents |
| `mus eln login` | Interactive: get + verify + store an ELN API token |
| `mus eln tag EXPERIMENT_ID` | Link the folder to an ELN experiment + pick `data_project` |
| `mus eln update` | Re-fetch ELN names from the server (after a rename) |
| `mus eln whoami` | Print the user the stored token authenticates as |
| `mus irods login` | Interactive: walk through iron auth |
| `mus irods upload FILE/FOLDER` | Upload, stamp sidecar + AVU metadata, write PURL |
| `mus irods get SIDECAR` | Download a file/folder/archive referenced by a sidecar |
| `mus irods check` | Compare local sha256 to iron-reported remote checksum |
| `mus irods scan run / list / show / find` | Cache an iRODS folder inventory for offline browsing |
| `mus completion install` | Drop a shell completion script in the conventional location |
| `mus s3 …` | (placeholder — coming) |

`mus -C DIR ...` runs as if invoked from `DIR` (like `git -C`).

---

## How it works (1-minute tour)

**`.env` files cascade.** Walk up from your working directory; every `.env` is merged top-down so deeper files override shallower ones. List-valued keys (`tag`, `collaborator`) accept comma-separated values; a `-foo` entry removes a previously-added `foo`. Grammar matches the legacy Python `mus` — one `KEY=VALUE` per line, `#` for comments, blank lines ignored.

**Per-file sidecars.** `mus tag data.csv` writes `data.csv.mus` next to the data. Same flat `KEY=VALUE` format. Fields include sha256, size, mtime, host, data_project, eln_*, and (after `mus irods upload`) irods_path / irods_url / irods_purl / irods_status.

**Folder sidecars.** `mus irods upload scripts/` writes one `scripts.mus` next to the folder (not per-file). Carries a Merkle-style `recursive_sha256` so `mus check scripts.mus` can detect drift later. If the folder has too many small files (`median / N² < 10`), `mus` offers to pack it into `scripts.tar.gz` and upload the archive instead.

**iRODS layout.** Uploads land at `<irods_home>/project/<data_project>/<safe_experiment_name>/`. Three baked-in defaults make BADS uploads zero-config: `irods_home=/gbiomed/home/BADS`, `irods_web=https://mango.kuleuven.be/data-object/view`, `irods_pid_base=https://mango.kuleuven.be/PID`. Override any of them via `.env` or build with custom `-ldflags`.

**Persistent URLs.** Every successful upload calls `iron stat -j` to grab the iRODS catalog ID, then builds `irods_purl=https://mango.kuleuven.be/PID/<zone>/<id>/` — keyed on the stable ID, so the URL survives renames + moves.

**Signatures everywhere.** Release binaries are signed with a shared ed25519 key (same key signs `mfiers`'s other Go projects). Commits and tags in this repo are SSH-signed with the same key. Both `mus upgrade` and `install.sh` refuse unsigned releases.

**Secrets.** Stored in the OS keyring when available (Linux Secret Service, macOS Keychain, Windows Credential Manager), with an [age](https://age-encryption.org)-encrypted file fallback for HPC compute nodes / headless containers.

---

## Build from source

Requires Go 1.24+. Pure Go, no CGO.

```bash
git clone https://codeberg.org/mfiers/mus.git
cd mus
make install            # build and place bin/mus on your PATH
```

Or just `make build` to get `./bin/mus` without installing.

Other targets: `make test` (no network), `make build-all` (cross-compile darwin/arm64 + linux/{amd64,arm64}), `make show-version`, `make bump LEVEL=minor`, `make ship` (maintainer-only — interactive, signs + tags + publishes).

Site defaults (`irods_home`, `irods_web`, `irods_pid_base`) live in `internal/defaults/`. To ship a binary for a different lab, override at build time:

```bash
go build -trimpath \
  -ldflags="-X codeberg.org/mfiers/mus/internal/defaults.IRODSHome=/your/zone/home/lab \
            -X codeberg.org/mfiers/mus/internal/defaults.IRODSWeb=https://your-mango/data-object/view \
            -X codeberg.org/mfiers/mus/internal/defaults.IRODSPIDBase=https://your-mango/PID" \
  ./cmd/mus
```

---

## More documentation

- [Quickstart](doc/quickstart.md) — longer walkthrough with concrete examples.
- [CLAUDE.md](CLAUDE.md) — architecture, design decisions, where things live in the tree.

---

## License

MIT. See [LICENSE](LICENSE).

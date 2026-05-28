# mus quickstart

A longer walkthrough than the README. Concrete commands, expected output, common gotchas.

If you just want to install and go: see the [README](../README.md). This document picks up after that, with more "why" and more "what happens when".

---

## 1. Install

```bash
curl -sSL https://codeberg.org/mfiers/mus/raw/branch/main/install.sh | bash
```

The installer:
1. Detects your platform (`uname -s / uname -m`).
2. Looks up the latest release tag via the Codeberg API.
3. Downloads `mus-{linux-amd64,linux-arm64,darwin-arm64}` and `mus-*.sig`.
4. Calls the just-downloaded binary's `mus _verify` to confirm the ed25519 signature against the embedded pubkey.
5. Places it in the first writable PATH directory (`~/bin` → `~/.local/bin` → `/usr/local/bin`).

If verification fails, the install aborts and no binary is left on disk. If your platform isn't published (e.g. Windows, macOS Intel), build from source instead.

Re-run the same `curl … | bash` any time to upgrade.

Once installed, future upgrades can also go through `mus upgrade`:

```bash
mus upgrade           # update to latest
mus upgrade --check   # just report whether an update is available
mus upgrade --tag v0.1.5
```

---

## 2. First-run setup

Three short interactive commands.

### 2a. iRODS auth

```bash
mus irods login
```

`mus` does NOT manage iRODS credentials itself — it bridges to the [iron](https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html) CLI. `mus irods login` walks you through:

1. **Is iron installed?** If not, prints the install URL and stops.
2. **Is `~/.irods/irods_environment.json` present?** That file tells iron which iRODS zone + host to talk to. If missing, mus points you at https://mango.kuleuven.be/ where you download yours from the top-right user menu (one-time per machine).
3. **Are you already authenticated?** If `iron pwd` works → ✓, done.
4. **Otherwise**, `mus` spawns `iron auth` interactively. Enter your password (or SSO credential — depends on your iRODS setup).
5. **Verify.** `iron pwd` is re-run; on success → ✓.

You typically run this once per machine. The credential is cached in `~/.irods/` (TTL is whatever iron is configured for, often a week).

### 2b. ELN auth

```bash
mus eln login
```

The wizard tells you exactly how to get a token from the eLabJournal web UI (Apps & Connections → Manage authentication → Generate access token). You paste in the `host;key` form the UI emits — mus splits it, infers the base URL, and calls `GET /users/getCurrentUserInfo` to confirm the token works before storing it. If verification fails, nothing is persisted; you can retry.

Token is stored via mus's `secret` backend (OS keyring on a desktop, [age](https://age-encryption.org)-encrypted file on headless hosts).

Confirm the stored token still works:

```bash
mus eln whoami
```

### 2c. Shell completion

```bash
mus completion install
```

Detects your shell (`$SHELL`) and writes the completion script to the conventional XDG path:

| Shell | Path |
|---|---|
| bash | `$XDG_DATA_HOME/bash-completion/completions/mus` (or `~/.local/share/bash-completion/completions/mus`) |
| zsh | `$XDG_DATA_HOME/zsh/site-functions/_mus` |
| fish | `$XDG_CONFIG_HOME/fish/completions/mus.fish` |

Override with `--shell` or `--dest`. Bash users may need to `source` the file in their `~/.bashrc`; zsh users may need to ensure the install dir is in `$fpath` and run `compinit`. Fish picks up changes automatically.

---

## 3. The mental model

Three kinds of files:

- **`.env`** — cascading folder config. Format: flat `KEY=VALUE`, one per line, `#` for comments, blank lines ignored. `mus` walks from the working directory up to `/`, merging every `.env` it finds; deeper files override shallower ones.

  List-valued keys (`tag`, `collaborator`) accept comma-separated values. A value prefixed with `-` (e.g. `tag=-lab`) removes a previously-added entry from the cumulative list.

- **`*.mus` per-file sidecar** — same flat `KEY=VALUE` grammar, written by `mus tag data.csv` next to `data.csv` as `data.csv.mus`. Records sha256, size, mtime, host, plus iRODS / ELN / data_project context.

- **`*.mus` folder-level sidecar** — for `mus irods upload <folder>/`. Sits next to the folder (e.g. `scripts/` → `scripts.mus`), NOT inside it. Carries a Merkle-style `recursive_sha256` of the folder's contents plus all the iRODS/ELN bookkeeping, but only ONE sidecar per folder — not per file inside.

The `*.mus` extension is unambiguous because a folder-config `.env` is always literally `.env` (the dot file) while sidecars always have a non-empty basename.

---

## 4. Example folder layout

```sh
# /lab/projects/.env  — lab-wide defaults
# (For BADS users these three match the baked-in defaults, so this section
#  is usually empty unless you're at a different institution.)
# irods_home=/gbiomed/home/BADS
# irods_web=https://mango.kuleuven.be/data-object/view
# irods_pid_base=https://mango.kuleuven.be/PID
tag=lab,shared

# /lab/projects/project_alpha/exp_42/.env  — per-experiment override
tag=exp42,-lab                  # adds exp42; drops the inherited "lab"
data_project=Fiers2025          # mandatory for iRODS upload (NameYear)
eln_experiment_id=12345         # `mus eln tag 12345` writes this
# irods_path=alpha/raw/exp_42   # optional; default is <home>/project/<data_project>/<exp_name>
```

Inspect:

```bash
mus config show          # effective cascade
mus config show --local  # only this folder's .env
mus config files         # paths of every .env contributing
mus config set tag exp42 # writes to local .env (validates if data_project)
```

---

## 5. Per-file workflow

```bash
mus tag data1.csv data2.csv -m "raw sequencing data" -t qc-pass
```

Produces `data1.csv.mus` + `data2.csv.mus` next to the inputs. Each contains:

- File: `sha256`, `size`, `mtime`, `hashed`, `host`, `abspath`
- ELN (from the cascade): `eln_experiment_id` and any `eln_*_name` / `eln_*_id` available
- `data_project` (if set in cascade)
- Free-form: `note`, `tags`
- Bookkeeping: `version`, `created`, `updated`

Verify later:

```bash
mus check data1.csv      # one file
mus check                # every *.mus in this dir
mus check -r .           # recurse
```

`mus check` returns non-zero on any mismatch / missing data. The sha256 lookup uses a local SQLite cache at `~/.local/share/mus/hashcache.db` (`MUS_HASHCACHE_DB` to override), so repeat checks against unchanged files are stat-fast.

---

## 6. ELN linkage

`mus eln tag 12345` connects the current folder to ELN experiment 12345:

1. Fetches the experiment's name, study, project, and collaborators via the API.
2. Asks you for a `data_project` name (a `NameYear` label, e.g. `Fiers2025`). The suggestion comes from:
   - A previous-folder cache (`~/.local/share/mus/eln_mappings.json`) if you've linked this experiment before.
   - A pull from the shared iRODS file (`<irods_home>/project/eln_mappings.json`) so a teammate's previous choice for this experiment is reused automatically — best-effort, no failure if iRODS is unreachable.
   - The first collaborator's surname + current year (e.g. `Fiers2025`) as the fallback derivation.
3. Persists your choice locally + pushes it back to the shared iRODS file for your teammates.
4. Writes all six `eln_*` keys + `data_project` into the folder's `.env`.

Refuses to overwrite an existing linkage; use `mus eln update` to refresh, or `mus eln tag NEW_ID --force` to relink.

---

## 7. iRODS upload

Requires `iron` on PATH + authenticated (`mus irods login` if not yet).

```bash
mus irods upload data1.csv data2.csv
```

Lands at:

```
<irods_home>/project/<data_project>/<safe_experiment_name>/<basename>
```

where `<safe_experiment_name>` is `eln_experiment_name` with non-alphanumerics replaced by underscores, capped at 60 chars.

Each upload:
1. Calls `iron upload --verify-checksum` (transport integrity).
2. Calls `iron stat -j` to grab the iRODS catalog ID.
3. Writes the local sidecar with `sha256` + `irods_path` + `irods_url` (path-based browse URL) + `irods_purl` (persistent URL keyed on catalog ID — survives renames + moves).
4. Stamps `mus_*` AVU metadata on the iRODS object (so the object stays self-describing without the local sidecar).

CLI overrides:
- `--data-project NAME` — override the .env value (one-off).
- `--remote-name NAME` — override the experiment-name path component.
- `--no-metadata` — skip AVU stamping.
- `--verify-checksum=false` — skip iron's transport-checksum verification.

### Folder upload

```bash
mus irods upload scripts/
```

`mus` profiles the folder first (count + total size + median size). If the density check (`median_bytes / N² ≥ 10`, ignored for N < 20) fails — i.e. too many small files for efficient iRODS — `mus` prompts you to:

- `[t]` pack into `scripts.tar.gz` and upload the archive,
- `[a]` upload as-is anyway,
- `[c]` cancel.

Non-TTY callers (CI / scripts) must pass `--pack tar.gz` or `--pack none` explicitly.

After upload, ONE folder sidecar at `scripts.mus` (sibling, not inside the folder). Carries `recursive_sha256` so `mus check scripts.mus` later can detect any drift in the source folder, even though there are no per-file sidecars.

If you packed:
- The local `scripts.tar.gz` is **kept by default** — `mus` prints a loud `⚠ local archive kept` warning.
- Pass `--cleanup-archive` to delete it after successful upload.
- The archive's own `sha256` is recorded in the sidecar so `mus check` can validate it against the local archive if the source folder is gone later.

---

## 8. iRODS download (provenance-verified)

```bash
mus irods get data.csv.mus
mus irods get scripts.mus
```

The interesting bits:

1. **Skip-existing default.** If the local copy already matches the sidecar (sha256 for files, Merkle for folders, archive sha256 for archived uploads), `mus` skips the network round-trip and prints `✓ X already matches sidecar; skipping download`.
2. **Refuse-overwrite default.** If the local copy exists but DIFFERS from the sidecar, `mus` refuses without `--force` and points you at `mus check` for the diff.
3. **Provenance verification.** After every successful download, `mus` recomputes the sha256/Merkle locally and compares to the sidecar. If the iRODS object drifted from what was uploaded (someone overwrote it), you get a loud `PROVENANCE MISMATCH` error and the downloaded file is LEFT in place for inspection.

Legacy `.mango` files (from the old Python `mus`) are accepted too — `mus` prints a deprecation note and parses the iRODS path from the embedded URL.

---

## 9. iRODS folder inventory

```bash
mus irods scan run /gbiomed/home/BADS
mus irods scan list
mus irods scan show /gbiomed/home/BADS --data-only
mus irods scan find <sha256>
```

Caches an `iron tree --json` snapshot to a local SQLite DB (`~/.local/share/mus/irods-scans.db`). Subsequent queries are local + fast. Default `--max-age 24h`; use `--refresh` to force re-walk. Useful for finding duplicates across the lab's iRODS tree without re-querying.

---

## 10. Environment variables

| Var | Purpose |
| --- | --- |
| `MUS_CONFIG_DIR` | Override `~/.config/mus` |
| `MUS_HASHCACHE_DB` | Override the hashcache path |
| `MUS_IRODS_SCAN_DB` | Override the iRODS-scan DB path |
| `MUS_ELN_MAPPINGS` | Override the eln-mappings JSON path |
| `MUS_SECRET_BACKEND` | Force `keyring` or `age` |
| `CODEBERG_TOKEN` (maintainer) | Used by `make publish` to create the Codeberg release |

---

## 11. Cross-platform notes

- Pure-Go build, no CGO. Binaries cross-compile cleanly.
- The Linux ELF binaries are statically linked — no glibc dependency.
- The darwin/arm64 binary is for Apple Silicon Macs (M-series).
- Tested on RHEL/Rocky/Ubuntu (HPC nodes), macOS Sonoma+.

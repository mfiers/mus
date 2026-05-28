# mus quickstart

## Install

```bash
make build           # ./bin/mus for your host
make build-all       # dist/ for darwin-arm64, linux-{amd64,arm64}
# copy bin/mus into ~/.local/bin or wherever you keep CLIs
```

`mus version` confirms the install.

## One-time secret setup

```bash
mus secret backend            # prints "keyring" or "age"
mus secret set eln_url https://eln.example.com/api/v1
mus secret set eln_apikey YOUR_KEY
```

If the OS keyring is unavailable (HPC compute node, headless container) mus
falls back to an age-encrypted file at `~/.config/mus/secrets.age` with the
identity key at `~/.config/mus/secrets.key` (chmod 600). The first call to a
secret command decides which backend the process uses; force a backend with
`MUS_SECRET_BACKEND=keyring|age`.

## Folder-level config (cascading `.env`)

`mus` walks the directory tree upward, merging every `.env` it finds. Closer
files override / extend ones higher up. Format is flat `KEY=VALUE` (one per
line; `#` for comments; blank lines OK). Two keys are list-valued:
`tag` and `collaborator` — comma-separated, `-prefix` removes a prior entry.

`/proj/.env`:
```sh
irods_home=/zone/home/lab
irods_web=https://mango.kuleuven.be/data-object/view
tag=lab
```

`/proj/exp1/.env`:
```sh
tag=exp1,-lab               # adds exp1; drops the inherited "lab"

# Recommended: record the ELN experiment ID for traceability. Use
# `mus eln tag-folder -x 1234` to write this for you (no API call).
eln_experiment_id=1234

# Optional: explicit iRODS subpath under irods_home. Default is
# `exp_<eln_experiment_id>/`.
# irods_path=alpha/exp_1
```

Inspect:
```bash
mus config show          # effective (cascaded) config
mus config show --local  # only the .env in the current folder
mus config files         # paths of every .env contributing to the cascade
mus config set tag exp1  # writes to the local .env
```

## Per-file sidecars (`*.mus`)

Tag a data file:
```bash
mus tag data1.csv data2.csv -m "raw sequencing data"
```

This writes `data1.csv.mus` and `data2.csv.mus` next to each file. Same flat
`KEY=VALUE` grammar as `.env`. The sidecar records:

- File: `sha256`, `size`, `mtime`, `hashed`, `host`, `abspath`
- iRODS (after `mus irods upload`): `irods_url`, `irods_path`, `irods_status`, `irods_uploaded_at`
- ELN (from the `.env` cascade): `eln_experiment_id` (+ optional name/study/project fields if available)
- S3 (planned): `s3_url`, `s3_bucket`, `s3_key`, `s3_etag`, `s3_uploaded_at`
- Free-form: `note`, `tags`
- Bookkeeping: `version`, `created`, `updated`

Verify integrity later:
```bash
mus check data1.csv          # one file
mus check                    # every *.mus in the current dir
mus check -r .               # recurse
```

Exit code is non-zero on any mismatch / missing data file.

The local sha256 cache lives at `~/.local/share/mus/hashcache.db` (override
with `MUS_HASHCACHE_DB`). Entries are reused only when both size and mtime
match — modify a file and the next `mus check` will rehash it.

## ELN setup + linking

One-time per machine:

```bash
mus eln login
```

The wizard walks you through generating an API token in the ELN web UI
(`Apps & Connections → Manage authentication → Generate`), accepts the
`host;key` form the web UI emits, verifies the token by calling
`GET /users/getCurrentUserInfo`, then stores `eln_url` + `eln_apikey` in
your OS keyring (or the age-encrypted fallback on headless hosts).

Then per project folder:

```bash
mus eln tag-folder -x 1234   # fetches project/study/experiment names from API
mus eln update               # refresh if anything was renamed server-side
mus eln whoami               # confirm the stored token still authenticates
```

`tag-folder` writes six keys to the local `.env`: `eln_experiment_id`,
`eln_experiment_name`, `eln_study_id`, `eln_study_name`, `eln_project_id`,
`eln_project_name`. Subsequent `mus tag` / `mus irods upload` invocations
stamp those into each sidecar.

**Multiple tenants on one machine?** Today `eln_url`/`eln_apikey` are
per-user (keyring scope), so one credential set across all your projects.
Different ELN instances would need a follow-up to override via `.env` —
not implemented yet.

## Uploading to iRODS via IRON

Requires the `iron` CLI on PATH (see
https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html).

```bash
mus irods upload data1.csv data2.csv --verify
```

The remote path is resolved (in order):

1. `irods_path` from the `.env` cascade — explicit subpath under `irods_home`.
2. Otherwise, if `eln_experiment_id` is set: `<irods_home>/exp_<id>/`.
3. Otherwise: error.

After upload, the sidecar `[irods]` section is populated with the remote path,
URL (using `irods_web`), and a timestamp.

Verify after the fact:
```bash
mus irods check               # compares each sidecar's sha256 to IRON's checksum
mus irods get data1.csv.mus   # downloads using the path recorded in the sidecar
```

## Cross-platform notes

- Pure-Go build, no CGO. Binaries cross-compile cleanly.
- The Linux ELF binaries are statically linked — no glibc dependency.
- The darwin/arm64 binary is for Apple Silicon Macs (M-series).

## Environment variables

| Var | Purpose |
| --- | --- |
| `MUS_CONFIG_DIR` | override `~/.config/mus` |
| `MUS_HASHCACHE_DB` | override the hashcache path |
| `MUS_SECRET_BACKEND` | force `keyring` or `age` |
| `IRON_NO_INTERACTIVE` | set by mus when shelling out to iron |

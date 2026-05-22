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

## Folder-level config (cascading `.mus`)

`mus` walks the directory tree upward, merging every `.mus` it finds. Closer
files override / extend ones higher up.

`/proj/.mus`:
```toml
irods_home = "/zone/home/lab"
irods_web  = "https://mango.kuleuven.be/data-object/view"
tag        = ["lab"]

[eln]
project_name = "Project Alpha"
```

`/proj/exp1/.mus`:
```toml
tag = ["exp1"]   # adds; use "-lab" to drop an inherited tag

[eln]
experiment_name = "Experiment 1"
```

Inspect:
```bash
mus config show          # effective (cascaded) config
mus config show --local  # only the .mus in the current folder
mus config files         # paths of every .mus contributing to the cascade
mus config set tag exp1  # writes to the local .mus
```

## Per-file sidecars (`*.mus`)

Tag a data file:
```bash
mus tag data1.csv data2.csv -m "raw sequencing data"
```

This writes `data1.csv.mus` and `data2.csv.mus` next to each file. The sidecar
records:

- `[file]` — sha256, size, mtime, hashed time, host, abspath
- `[irods]` — populated after `mus irods upload`
- `[eln]` — populated from the `[eln]` section of the `.mus` cascade
- `[s3]` — populated by `mus s3 upload` (planned)
- `tags`, `note` — free-form metadata

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

## Linking a folder to ELN

```bash
mus eln tag-folder -x 1234           # 1234 = ELN experiment ID
mus eln update                       # refresh names from the server
```

Writes `eln.experiment_id`, `eln.experiment_name`, `eln.study_*`,
`eln.project_*` into the local `.mus`.

## Uploading to iRODS via IRON

Requires the `iron` CLI on PATH (see
https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html).

```bash
mus irods upload data1.csv data2.csv --verify
```

The remote path is computed from the cascading `.mus`:

```
<irods_home>/<project_name>/<study_name>/<experiment_name>/<basename>
```

(folder names are lowercased and sanitised to `[a-z0-9_]`).

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

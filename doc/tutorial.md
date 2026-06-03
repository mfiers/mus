# mus tutorial — backing up research data

A short, opinionated walkthrough for BADS depositors. Companion to
[quickstart.md](quickstart.md) (which is the reference manual).

---

## 1. Install & log in (once per machine)

```bash
curl -sSL https://codeberg.org/mfiers/mus/raw/branch/main/install.sh | bash
mus irods login         # bridges to KU Leuven Mango via `iron`
mus eln login           # if you have eLabJournal access
```

`mus irods login` walks you through downloading
`irods_environment.json` from <https://mango.kuleuven.be/> the first
time. `mus eln login` prompts for a token from the eLab UI
(Apps & Connections → Manage authentication → Generate access token).

No ELN access? Run `mus eln disable --global` once and skip the
second login.

---

## 2. What to back up

**Yes, back up:**

- **raw data** — primary measurements, sequencing reads, microscope
  acquisitions, instrument exports. Things you cannot regenerate.
- **final / publication-ready data** — the curated h5ad, the figure
  source CSVs, the manuscript-supporting tables.

**No, don't back up:**

- **intermediate** files — everything in `data/intermediate/` that
  can be regenerated from raw via `make`. Re-derive them on demand.

Rule of thumb: if losing it would force you to repeat wet-lab work
or rerun a 12-hour pipeline, back it up.

---

## 3. Annotate, then upload

Every folder mus uploads from needs at minimum two facts:

| Required | Why |
| --- | --- |
| `data_project` (e.g. `Fiers2025`) | Path component on iRODS; groups all data from one project under one collection. |
| `eln_experiment_id` (or `eln_disabled=true`) | Links each upload back to a lab notebook entry so anyone can find context. |

`mus eln link 12345` sets both (it pulls the experiment, infers
`data_project` from collaborators if it can, asks you to confirm,
writes the folder's `.env`).

If you have no ELN:

```bash
mus config set data_project Fiers2025
mus config set eln_experiment_name MyPilot
mus eln disable                              # this folder
```

### Recommended annotation

These two are recommended for every dataset:

| Key | Values |
| --- | --- |
| `source` | `internal` (made here) / `external` (downloaded) |
| `access` | `internal` (unpublished, lab-only) / `public` (published) |

```bash
mus config set source internal
mus config set access internal
```

### Biology (optional but useful for h5ad files)

`organism`, `tissue`, `cell_type`, `disease`, `technology`,
`suspension_type`, `sex`, `age`, … are all controlled-vocabulary
fields. Values must come from the joint vocabulary:

- **Live:**
  <https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml>
- **Local:** `mus vocab show` (your current fetched copy)

```bash
mus config set organism human
mus config set tissue 'frontal cortex,hippocampus'
mus config set cell_type 'microglial cell'
mus config set technology 10x_chromium_3p_v3
mus config set suspension_type nucleus
```

Tissue must be **UBERON**, cell_type must be **CL**, disease must
be **MONDO** — labels (not IDs) from the joint vocabulary. mus
validates on `mus h5ad check`.

---

## 4. Pack & upload

### Single files

```bash
mus irods upload my_analysis.h5ad
```

### Folders

mus profiles the folder and decides whether to pack:

```bash
mus irods upload scripts/
# scripts/ — 412 files, total 1.8 MB, median 4 KB
# Many small files — pack into scripts.tar.gz? [t/a/c]
```

- **`t` (pack)** — recommended when you have many small files
  (iRODS overhead per object dominates). mus tar+gzips locally,
  uploads one archive, keeps the local archive by default.
- **`a` (as-is)** — preserves the directory structure on iRODS
  (browsable from the Mango web UI). Use for small numbers of
  large files.

Non-interactive callers must pass `--pack tar.gz` or `--pack none`.

### h5ad files

`.h5ad` files are gated. Before upload, run `mus h5ad check` so the
inspector validates structure, vocabulary, and stamps the result
into the sidecar:

```bash
mus h5ad check my_analysis.h5ad
mus irods upload my_analysis.h5ad
```

`--force-h5ad-upload` bypasses the gate but you'll regret it.

---

## 5. Where the data lands

Layout on iRODS:

```
<irods_home>/project/<data_project>/<eln_experiment_name>/<basename>
```

For example: `/gbiomed/home/BADS/mus/project/Fiers2025/sea_ad_pilot/my_analysis.h5ad`

After upload, two things exist locally:

- the original file (untouched)
- a **`*.mus` sidecar** next to it with the iRODS path, persistent
  URL, sha256, and (for folders) the recursive Merkle hash

A linked ELN experiment gets a one-line comment with a clickable
PURL link, so future-you (or a colleague) can find the data from
the ELN entry. Skip the comment with `--no-eln-comment`.

All resolved metadata (organism, tissue, source, access, sha256,
…) is also attached to the iRODS object as AVU triplets — visible
in `iron meta ls` and queryable via `iquest`.

---

## 6. Safely delete local data

Use `mus rm`, not `rm`. It refuses to delete unless three checks
pass:

1. sidecar says the file is uploaded (`irods_status=uploaded`)
2. fresh local hash matches what was uploaded (no local drift)
3. fresh local hash matches the remote checksum (no remote drift /
   loss)

```bash
mus rm --dry-run my_analysis.h5ad     # show plan, touch nothing
mus rm my_analysis.h5ad               # prompts y/N
mus rm -y my_analysis.h5ad scripts/   # skip prompt, batch
```

For folders uploaded as-is, `mus rm` walks `iron tree --json` and
compares every file. Slow on big trees, but it's the only way to
be sure.

By default `mus rm` keeps the `.mus` sidecar as a **tombstone**
(irods_status flips to `local_deleted`). Use `--delete-sidecar` to
remove it too.

---

## 7. The sidecar after deletion — how to restore

A tombstoned sidecar still carries everything you need:
`irods_path`, `irods_purl`, sha256. You can restore the original
file (or folder) any time:

```bash
mus irods get my_analysis.h5ad.mus
mus irods get scripts.mus
```

Verification on restore is automatic: mus recomputes the hash and
compares to the sidecar; mismatch → loud `PROVENANCE MISMATCH`
error.

A few teammate-facing properties of tombstones:

- They're tiny (~1 KB), worth keeping in git for shared projects.
- They survive iRODS-side moves and renames because the `irods_purl`
  is a persistent identifier, not a path.
- If you also `--delete-sidecar`d, you can still find the object
  with `mus irods scan find <sha256>` if you noted the hash, or
  by browsing under `<irods_home>/project/<data_project>/`.

---

## 8. End-to-end example

```bash
cd /data/projects/sea_ad_pilot
mus eln link 12345                    # writes .env with eln_* + data_project
mus config set source external        # SEA-AD is published data
mus config set access public
mus config set organism human
mus config set tissue 'middle temporal gyrus,prefrontal cortex'
mus config set technology 10x_chromium_3p_v3
mus config set suspension_type nucleus

mus h5ad check sea_ad.h5ad            # inspect + stamp pass
mus irods upload sea_ad.h5ad          # → /gbiomed/.../Fiers2025/sea_ad_pilot/sea_ad.h5ad
                                      # → ELN comment with clickable PURL

# … later, freeing local disk:
mus rm sea_ad.h5ad                    # verifies, then deletes, keeps tombstone
ls
# sea_ad.h5ad.mus    ← tombstone

# … much later, need it back:
mus irods get sea_ad.h5ad.mus         # downloads, verifies sha256
```

---

## 9. Common pitfalls

- **"data_project not set"** — `mus eln link` or `mus config set
  data_project Fiers2025`. Format is `NameYear`: capital first
  letter, ends in 4 digits.
- **"no ELN status declared"** — every folder must explicitly link
  or `mus eln disable`. There is no implicit non-ELN state — pick
  one consciously.
- **"vocabulary changed since last check"** — the joint vocab was
  updated. Re-run `mus h5ad check`.
- **"local drift since upload"** on `mus rm` — you (or a process)
  modified the file after upload. Re-upload or restore the
  original, then retry.
- **Many small files** — always pack. iRODS metadata overhead
  dominates for tiny objects.

---

## See also

- [quickstart.md](quickstart.md) — full reference manual
- [joint vocabulary](https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml)
- `mus <command> --help` — every subcommand documents its flags

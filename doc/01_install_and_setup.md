# 01 — Install & first-run setup

One-time work per machine. Takes ≈ 3 minutes.

After this, mus knows how to talk to the lab's iRODS catalog, your eLab notebook (if you use one), and the S3 bucket that feeds the anthive single-cell catalog. You'll never run these commands again on this machine.

---

## 1. Install

One-liner installer (Linux x86_64 / Linux ARM64 / macOS Apple Silicon):

```bash
curl -sSL https://codeberg.org/mfiers/mus/raw/branch/main/install.sh | bash
```

The script:
1. Detects your platform.
2. Downloads the latest signed release binary.
3. Verifies the ed25519 signature against the embedded maintainer pubkey (refuses to install on signature failure).
4. Drops the binary in the first writable directory in `$PATH` — prefers `~/bin`, then `~/.local/bin`, then `/usr/local/bin`.

Confirm it landed:

```bash
mus version
```

**macOS on Intel:** not published; build from source with Go 1.24+.

**Upgrades:** re-run the installer any time, or `mus upgrade` from inside mus.

### Windows

`install.sh` is POSIX-only — Windows users install mus manually.

1. Download the binary + signature from the [latest release](https://codeberg.org/mfiers/mus/releases/latest):
   - `mus-windows-amd64.exe`
   - `mus-windows-amd64.exe.sig`

2. (Recommended) verify the signature against the ed25519 public key embedded in mus. The simplest path: drop the binary somewhere temporary, run `mus _verify <path-to-binary>` against itself, then move it to its final location only on a clean verify. Mus refuses to self-verify a tampered binary.

3. Place `mus-windows-amd64.exe` somewhere on `PATH`. Conventional locations:
   - `%USERPROFILE%\bin\mus.exe` (create the directory if needed and add it to your PATH via *System Properties → Advanced → Environment Variables*).
   - `%LOCALAPPDATA%\Programs\mus\mus.exe`.

4. Open a fresh terminal and run `mus version` to confirm it's on PATH.

**Subsequent upgrades** go through `mus upgrade` like any other platform — mus does a `.exe → .exe.old` swap (Windows locks running executables), and cleans up the `.old` file on the next launch.

**External tools mus depends on:**

| Tool | Needed for | Windows install |
|---|---|---|
| [`uv`](https://docs.astral.sh/uv/) | `mus h5ad check`, `mus de check` (anndata reader) | `powershell -c "irm https://astral.sh/uv/install.ps1 \| iex"` |
| `iron` | `mus irods *` commands (any iRODS / Mango interaction) | Check the [KU Leuven RDM docs](https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html) for current Windows availability. If unavailable, run iRODS commands from WSL2 or a Linux jump host instead. |
| `pandoc` + TeX | `mus eln upload` of `.ipynb` files (PDF conversion only — optional) | Skip unless you use ELN PDF upload. |
| `vark` | optional anthive catalog cross-check (see [05](05_de_deposit.md)) | Cross-compiles (Go); check the [vark repo](https://codeberg.org/mfiers/vark) for Windows binaries. |

**Caveat:** Windows binary is cross-compiled and ed25519-signed but full runtime smoke-tests on Windows are pending. Lab depositors who run Windows: please report any issue you hit. Most paths work because mus uses Go's `path/filepath` (portable separator) end to end; the rough edges are likely around external tool integration (iron especially).

**WSL2 alternative.** If you have Windows 11 with WSL2 (Ubuntu / Debian), mus works there exactly like on a native Linux box — same installer, same external-tool story. For mus + iRODS in particular, WSL2 is the lowest-friction path on Windows today.

---

## 2. iRODS authentication

mus talks to the lab archive via the [`iron`](https://rdm-docs.icts.kuleuven.be/mango/clients/iron.html) CLI. You need it on `PATH` and authenticated.

```bash
mus irods login
```

Outcomes:

- **Already authenticated** → ✓ printed, you're done with this step.
- **Not authenticated** → mus prints the exact `iron auth` command for the BADS Mango zone:

  ```bash
  iron auth <u-or-r-number> gbiomed gbiomed.irods.icts.kuleuven.be
  ```

  Substitute your KU Leuven account number (`u0123456` for staff, `r0123456` for students). `iron auth` prompts for password / SSO and caches the credential under `~/.irods/` for ~1 week.

  Then re-run `mus irods login` to confirm ✓.

mus does NOT handle the iRODS login itself — `iron auth` does. mus's `irods login` subcommand only verifies and nudges.

---

## 3. eLab Journal authentication

The lab notebook lives at <https://vib.elabjournal.com>. mus uses it to:
- read experiment metadata (project / study / title) so you don't re-type it,
- auto-post a one-line comment to the experiment after every iRODS upload (with persistent URLs to the data).

If you have eLab access:

```bash
mus eln login
```

Interactive wizard. The token comes from the eLab UI:
1. **Apps & Connections → Manage authentication → Generate access token**.
2. Copy the `host;key` string the UI emits.
3. Paste it into mus.

mus splits the token, calls `GET /users/getCurrentUserInfo` to confirm it works, and stores it via the secret backend below. Confirm anytime:

```bash
mus eln whoami
```

If you don't have eLab access (most engineers / sysadmins), opt out once:

```bash
mus eln disable --global
```

This writes `eln_disabled=true` to `~/.config/mus/mus.cfg` so mus stops asking from every working directory.

---

## 4. Shell completion

```bash
mus completion install
```

Auto-detects `$SHELL` and writes the completion script to the right XDG location:

| Shell | Path |
|---|---|
| bash | `~/.local/share/bash-completion/completions/mus` |
| zsh | `~/.local/share/zsh/site-functions/_mus` |
| fish | `~/.config/fish/completions/mus.fish` |

**Bash needs the `bash-completion` package.** Mus's generated script includes a polyfill that activates when `bash-completion` isn't installed, but if you have it system-wide (recommended), completion is richer:

```bash
sudo apt install bash-completion        # Debian / Ubuntu
sudo dnf install bash-completion        # RHEL / Fedora / Rocky
brew install bash-completion@2          # macOS Homebrew
```

Reload your shell. Type `mus i<TAB>` to verify (should expand to `mus irods`).

---

## 5. (Optional) Install `vark` for catalog cross-checks

When `vark` is on `$PATH` and configured, `mus de check` and `mus h5ad check` cross-check your declared dataset ID against the live anthive catalog before deposit — catches typos before the upload happens. Strictly optional; mus works fine without it.

```bash
# install instructions: https://codeberg.org/mfiers/vark
```

---

## 6. Where mus stores things on your machine

| File | What |
|---|---|
| `~/.config/mus/mus.cfg` | Global config (XDG-aware). Today: holds `eln_disabled=true` if you set it `--global`. |
| `~/.local/share/mus/h5ad_vocabulary.yaml` | Controlled-vocabulary cache. Auto-refreshes every 24h. |
| `~/.local/share/mus/local_projects.json` | Tracks "most recent local folder per data_project" for `mus irods projects --details`. |
| `~/.local/share/mus/hashcache.db` | SQLite cache of file sha256s, keyed on (path, size, mtime). |
| `~/.local/share/mus/eln_mappings.json` | Caches eLab experiment → data_project mapping so the second `mus eln link` doesn't re-prompt. |
| `~/.local/share/mus/irods-scans.db` | Cached iRODS-tree snapshots (`mus irods scan`). |
| OS keyring or `~/.local/share/mus/secrets.age` | eLab token + S3 credentials. Keyring on desktop OSes; age-encrypted file on headless HPC nodes. |

---

## What's next

- **[02 — Annotation & vocabularies](02_annotation_and_vocabularies.md)** — how to describe your data so mus + anthive know what it is.
- **[03 — ELN linking & iRODS upload](03_eln_and_irods.md)** — back up data to the lab archive.
- **[04 — h5ad deposit](04_h5ad_deposit.md)** — prepare single-cell .h5ad files for anthive.
- **[05 — DE deposit](05_de_deposit.md)** — prepare differential-expression results for anthive.
- [quickstart.md](quickstart.md) — full reference manual (every flag, every env var).

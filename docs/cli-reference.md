# CLI Command Reference

Complete reference for all mus commands.

## Global Options

Available for all commands:

```bash
mus [OPTIONS] COMMAND [ARGS]...
```

### Options

| Option | Description |
|--------|-------------|
| `-v, --verbose` | Increase verbosity (INFO level) |
| `-vv` | Extra verbose (DEBUG level) |
| `--profile` | Enable profiling (performance analysis) |
| `--help` | Show help message |

### Examples

```bash
# Normal operation
mus tag file.txt -m "Message"

# Verbose output
mus -v tag file.txt -m "Message"

# Debug output
mus -vv tag file.txt -m "Message"

# Profile performance
mus --profile irods upload large_file.bin -m "Test"
```

---

## Core Commands

### `mus version`

Display mus version.

```bash
mus version
```

**Output:**
```
0.5.56
```

---

### `mus tag`

Tag one or more files with a message and metadata.

```bash
mus tag [OPTIONS] FILENAME...
```

#### Options

| Option | Type | Description |
|--------|------|-------------|
| `-m, --message TEXT` | string | Message to attach to files |
| `-e, --editor` | flag | Open editor for message |
| `-E, --eln` | flag | Upload to ELN (requires ELN setup) |
| `-I, --irods` | flag | Upload to iRODS (requires iRODS setup) |
| `-F, --irods-force` | flag | Force overwrite on iRODS |
| `--ignore-symlinks` | flag | Ignore symbolic links in directories |
| `-d, --dry-run` | flag | Dry run (ELN only, no actual upload) |
| `-x, --eln-experimentid INT` | integer | Override ELN experiment ID |

#### Examples

```bash
# Tag single file
mus tag data.csv -m "Raw data from experiment"

# Tag multiple files
mus tag file1.txt file2.txt file3.txt -m "Data files"

# Tag with editor (opens $EDITOR)
mus tag analysis.ipynb --editor

# Tag all CSV files
mus tag *.csv -m "All CSV files"

# Tag and upload to ELN
mus tag results.txt -m "Results" -E

# Tag and upload to both iRODS and ELN
mus tag dataset.csv -m "Dataset" -I -E

# Specify experiment ID directly
mus tag file.txt -m "Test" -E -x 12345
```

#### Notes

- At least one filename required
- Message required (via `-m` or `--editor`)
- `.mango` files are automatically filtered out
- With `-I` (iRODS), `-E` (ELN) is required
- Supports glob patterns (`*.csv`, `data/*.txt`)

---

### `mus file`

Find information about a file, including history and checksums.

```bash
mus file FILENAME
```

#### Arguments

| Argument | Description |
|----------|-------------|
| `FILENAME` | File to search for |

#### Examples

```bash
# Find file history
mus file data.csv

# Works with paths
mus file /home/user/project/data.csv
```

#### Output

Shows:
- File checksum (SHA256)
- Recent operations on this file
- Records with matching checksum (finds renamed/moved files)
- Operation time, type, message
- Path differences (if file was moved)

---

### `mus log`

Create a log entry in the database.

```bash
mus log [OPTIONS] [MESSAGE]
```

#### Options

| Option | Type | Description |
|--------|------|-------------|
| `-E, --eln` | flag | Post to ELN |
| `-x, --eln-experimentid INT` | integer | ELN experiment ID |

#### Examples

```bash
# Simple log
mus log "Started data analysis"

# Multi-line log
mus log "First line
Second line
Third line"

# Log to ELN
mus log -E "Completed preprocessing step"

# Log to specific experiment
mus log -E -x 12345 "Experiment completed"
```

#### Notes

- If message not provided, opens $EDITOR
- With `-E`, posts to ELN experiment as comment
- Requires ELN experiment ID (from `.env` or `-x` option)

---

### `mus search`

Search the database for records.

```bash
mus search [OPTIONS]
```

#### Options

| Option | Type | Description |
|--------|------|-------------|
| `-l, --long` | flag | Long format (multi-line) |
| `--type TYPE` | string | Filter by record type |
| `--user USER` | string | Filter by username |
| `--host HOST` | string | Filter by hostname |
| `--cwd PATH` | string | Filter by working directory |
| `-n, --limit INT` | integer | Limit number of results |

#### Examples

```bash
# Recent records
mus search

# Detailed view
mus search -l

# Filter by type
mus search --type tag
mus search --type log

# Filter by user
mus search --user jsmith

# Filter by host
mus search --host compute-01

# Combine filters
mus search --type tag --user jsmith -n 20

# Search from specific directory
mus search --cwd /home/user/project
```

#### Output Format

**Short format** (default):
```
T abc123  5m:23s Analysis results
L def456  2h:15m Started processing
```

**Long format** (`-l`):
```
T        5m:23s | jsmith@compute-01:/home/jsmith/project
Analysis results

L        2h:15m | jsmith@compute-01:/home/jsmith/project
Started processing
```

**Type markers:**
- `T` = tag
- `L` = log
- `H` = history
- `m` = macro
- `j` = job

---

## Configuration Commands

### `mus config`

Configuration management.

```bash
mus config SUBCOMMAND [OPTIONS]
```

#### Subcommands

##### `mus config show`

Display current configuration (from all `.env` files).

```bash
mus config show
```

Shows merged configuration from all parent directories.

##### `mus config secret-set`

Store a secret in the keyring.

```bash
mus config secret-set KEY VALUE
```

**Examples:**
```bash
mus config secret-set eln_apikey "your-api-key"
mus config secret-set eln_url "https://your-institution.elabjournal.com"
mus config secret-set irods_home "/zone/home/username"
mus config secret-set irods_web "https://mango.site.com/data-object/view"
mus config secret-set irods_group "your_group"
```

##### `mus config secret-get`

Retrieve a secret from the keyring.

```bash
mus config secret-get KEY
```

**Examples:**
```bash
mus config secret-get eln_apikey
mus config secret-get irods_home
```

#### Configuration Keys

**Standard keys:**
- `project_name` - Project name
- `eln_experiment_id` - ELN experiment ID
- `eln_experiment_name` - ELN experiment name
- `eln_project_id` - ELN project ID
- `eln_project_name` - ELN project name
- `eln_study_id` - ELN study ID
- `eln_study_name` - ELN study name
- `eln_collaborator` - Collaborator list

**List keys** (accumulate across hierarchy):
- `tag` - Tags for this directory
- `collaborator` - Collaborators

**Secret keys** (stored in keyring):
- `eln_apikey` - ELN API key
- `eln_url` - ELN base URL
- `irods_home` - iRODS base path
- `irods_web` - Mango web interface URL
- `irods_group` - iRODS group name
- `icmd_prefix` - iCommands prefix (for Docker on macOS)

---

## Database Commands

### `mus db`

Database operations.

```bash
mus db SUBCOMMAND
```

#### Subcommands

##### `mus db stats`

Show database statistics.

```bash
mus db stats
```

Shows:
- Total records
- Records by type
- Database size
- Database location

##### `mus db info`

Show database information.

```bash
mus db info
```

---

## ELN Commands

### `mus eln`

ELabJournal integration commands.

```bash
mus eln SUBCOMMAND [OPTIONS]
```

#### Subcommands

##### `mus eln tag-folder`

Link current folder to an ELN experiment.

```bash
mus eln tag-folder -x EXPERIMENT_ID
```

**Options:**
- `-x, --experimentID INT` (required) - ELN experiment ID

**Examples:**
```bash
# Tag folder with experiment 12345
mus eln tag-folder -x 12345

# Creates/updates .env with ELN metadata:
# - eln_experiment_id
# - eln_experiment_name
# - eln_project_id
# - eln_project_name
# - eln_study_id
# - eln_study_name
```

##### `mus eln upload`

Upload files to ELN.

```bash
mus eln upload [OPTIONS] FILENAME...
```

**Options:**
- `-m, --message TEXT` - Message (required)
- `-e, --editor` - Use editor for message
- `-x, --experimentid INT` - Override experiment ID

**Examples:**
```bash
# Upload single file
mus eln upload results.txt -m "Analysis results"

# Upload multiple files
mus eln upload data.csv plot.png notebook.ipynb -m "Complete analysis"

# Jupyter notebooks are automatically converted to PDF!
```

**File Handling:**
- `.ipynb` files → automatically converted to timestamped PDF
- Both `.ipynb` and `.pdf` are uploaded
- PDF timestamp format: `YYYYMMDD_HHMMSS_filename.pdf`

##### `mus eln update`

Update local ELN metadata from server.

```bash
mus eln update
```

Refreshes metadata in `.env` from ELN based on stored experiment ID.

---

## iRODS Commands

### `mus irods`

iRODS/Mango integration commands.

```bash
mus irods SUBCOMMAND [OPTIONS]
```

#### Subcommands

##### `mus irods upload`

Upload files to iRODS and ELN.

```bash
mus irods upload [OPTIONS] FILENAME...
```

**Options:**
- `-m, --message TEXT` - Message (required)
- `-e, --editor` - Use editor for message
- `-F, --irods-force` - Force overwrite on iRODS
- `--ignore-symlinks` - Ignore symlinks in directories

**Examples:**
```bash
# Upload single file
mus irods upload data.csv -m "Raw data"

# Upload multiple files
mus irods upload file1.txt file2.txt -m "Data files"

# Upload directory
mus irods upload data_folder/ -m "Complete dataset"

# Force overwrite existing files
mus irods upload data.csv -m "Updated data" -F

# Upload everything (safe - .mango files filtered)
mus irods upload * -m "All files"
```

**What happens:**
1. Checks ELN metadata is configured
2. Determines iRODS path from project/study/experiment
3. Compares checksums (local vs remote)
4. Uploads new/changed files
5. Applies Mango metadata schema
6. Sets permissions (`ichmod`)
7. Creates `.mango` files locally
8. Verifies checksums
9. Posts summary to ELN with iRODS links

**Requirements:**
- Must have run `mus eln tag-folder` first
- Requires iRODS iCommands installed and authenticated
- ELN integration is mandatory (uploads link to ELN)

##### `mus irods check`

Verify checksums between local and iRODS.

```bash
mus irods check [FILENAME...]
```

**Arguments:**
- `FILENAME` - One or more `.mango` files (optional)

**Examples:**
```bash
# Check all .mango files in current directory
mus irods check

# Check specific file
mus irods check data.csv.mango

# Check multiple files
mus irods check file1.txt.mango file2.csv.mango

# If you forget .mango extension, it's added automatically
mus irods check data.csv
```

**Output:**
```
Checking: data.csv
Checksum ok: data.csv
All checksums (1) are ok.
```

Or if there's a problem:
```
Checksum mismatch: data.csv
Failed 1 out of 1 checksums!
```

##### `mus irods get`

Download files from iRODS.

```bash
mus irods get [OPTIONS] FILENAME...
```

**Options:**
- `-f, --force` - Force overwrite existing local files

**Arguments:**
- `FILENAME` - One or more `.mango` files

**Examples:**
```bash
# Download file
mus irods get data.csv.mango

# Force overwrite
mus irods get data.csv.mango -f

# Download multiple files
mus irods get file1.txt.mango file2.csv.mango
```

---

## Command Shortcuts

### `mus irods upload` Shortcut

This is actually a shortcut for:

```bash
mus tag FILENAME... -m "MESSAGE" -I -E
```

The `-E` (ELN) flag is implicitly included because iRODS uploads require ELN integration.

### `mus eln upload` Shortcut

This is a shortcut for:

```bash
mus tag FILENAME... -m "MESSAGE" -E
```

---

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Error |
| 2 | Command line usage error |

---

## Environment Variables

These environment variables affect mus behavior:

| Variable | Purpose |
|----------|---------|
| `EDITOR` | Editor for `--editor` flag (default: `vi`) |
| `KEYRING_CRYPTFILE_PASSWORD` | Password for keyring (Linux) |
| `ELN_APIKEY` | Override ELN API key from keyring |
| `ELN_URL` | Override ELN URL from keyring |
| `IRODS_HOME` | Override iRODS home from keyring |
| `IRODS_WEB` | Override Mango URL from keyring |
| `IRODS_GROUP` | Override iRODS group from keyring |
| `IRODS_ENVIRONMENT_FILE` | Path to iRODS environment file |

---

## Special Files

### `.env` Files

Configuration files in INI-like format:

```bash
# Comments allowed
key=value
tag=tag1,tag2,tag3
collaborator=alice,bob

# Remove items from lists
tag=-old_tag

# Multi-word values
project_name=My Project Name
```

### `.mango` Files

Metadata files pointing to iRODS locations:

```
/zone/home/project/study/experiment/data.csv
```

- Created automatically by `mus irods upload`
- Used by `mus irods check` and `mus irods get`
- Plain text files
- Do NOT upload to iRODS (automatically filtered)

---

## Tips

### Tab Completion

Set up shell completion for better UX:

```bash
# Bash
eval "$(_MUS_COMPLETE=bash_source mus)"

# Zsh
eval "$(_MUS_COMPLETE=zsh_source mus)"

# Add to ~/.bashrc or ~/.zshrc for persistence
```

### Aliases

Useful aliases:

```bash
alias mt='mus tag'
alias ml='mus log'
alias ms='mus search'
alias mf='mus file'
alias mei='mus eln tag-folder -x'  # mei 12345
alias meu='mus eln upload'
alias miu='mus irods upload'
alias mic='mus irods check'
```

### Editor Configuration

Set your preferred editor:

```bash
export EDITOR=nano
export EDITOR=emacs
export EDITOR="code --wait"  # VS Code
export EDITOR="subl -w"      # Sublime Text
```

---

## Examples by Use Case

### Daily Research Work

```bash
# Morning: link to experiment
cd ~/experiments/exp-2024-01
mus eln tag-folder -x 12345

# During work: tag files as you create them
mus tag raw_data.csv -m "Raw data from instrument"
mus tag analysis.py -m "Analysis script v1"

# Evening: log progress
mus log -E "Completed preprocessing, ready for analysis"
```

### Data Upload Workflow

```bash
# 1. Process data
python analyze_data.py

# 2. Upload intermediate results to ELN
mus eln upload intermediate.csv -m "Intermediate results"

# 3. Upload final results to archive
mus irods upload final_results.csv figures/ -m "Final results for publication"

# 4. Verify upload
mus irods check *.mango
```

### Finding Files

```bash
# Find by name
mus file data.csv

# Find by checksum (even if renamed)
mus file current_name.csv  # Shows all instances of this file

# Search recent operations
mus search -n 50

# Search your tags only
mus search --type tag --user $USER
```

---

## See Also

- [Quick Start Guide](quickstart.md) - Get started quickly
- [Configuration Guide](configuration.md) - Detailed configuration
- [ELN Plugin Guide](eln-plugin.md) - ELN integration details
- [iRODS Plugin Guide](irods-plugin.md) - iRODS integration details
- [Workflows](workflows.md) - Real-world examples

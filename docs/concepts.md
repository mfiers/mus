# Core Concepts

Understanding these core concepts will help you use mus effectively and troubleshoot issues.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                      mus CLI                                 │
├─────────────────────────────────────────────────────────────┤
│  Commands: tag, log, file, search, config, eln, irods       │
└────────────────┬────────────────────────────────────────────┘
                 │
    ┌────────────┴────────────┐
    │                         │
┌───▼────┐            ┌───────▼──────┐
│  Core  │            │   Plugins    │
│ System │            │              │
├────────┤            ├──────────────┤
│ • DB   │            │ • ELN        │
│ • Conf │            │ • iRODS      │
│ • Hooks│            │ • History    │
└───┬────┘            └──────┬───────┘
    │                        │
    └────────┬───────────────┘
             │
    ┌────────▼─────────┐
    │  SQLite Database │
    │  ~/.local/mus/   │
    └──────────────────┘
```

## Records

### What is a Record?

A **Record** is the fundamental data unit in mus. Every operation (tagging a file, uploading to ELN, logging a message) creates a record in the database.

### Record Structure

Each record contains:

| Field | Description | Example |
|-------|-------------|---------|
| `type` | Operation type | `'tag'`, `'log'`, `'history'`, `'job'` |
| `message` | User-provided description | `"Analysis results"` |
| `filename` | File path (if applicable) | `/home/user/data.csv` |
| `checksum` | SHA256 hash (if file) | `a3b4c5d6...` |
| `host` | Machine hostname | `compute-node-01` |
| `user` | Username | `jsmith` |
| `cwd` | Working directory | `/home/jsmith/project` |
| `time` | Unix timestamp | `1704067200.0` |
| `uid` | Unique ID | `f47ac10b-...` |
| `child_of` | Parent record UID | `a1b2c3d4-...` |
| `data` | JSON metadata | `{"irods_url": "..."}` |
| `status` | Exit code | `0` (success) |
| `cl` | Command line | `mus tag file.txt -m "..."` |

### Record Types

- **`tag`**: File tagged with metadata
- **`log`**: Log message without file
- **`history`**: Shell command history (deprecated)
- **`job`**: Batch job tracking
- **`macro`**: Macro execution

### Why Records?

Records provide:
1. **Provenance**: Know when, where, and by whom a file was created/modified
2. **Traceability**: Track file movement and changes
3. **Reproducibility**: Record the context of each operation
4. **Collaboration**: See what team members have done

### Example Record

```python
Record:
  type: tag
  filename: /home/jsmith/analysis/results.csv
  checksum: a3b4c5d6e7f8...
  message: Final analysis results
  host: compute-01
  user: jsmith
  cwd: /home/jsmith/analysis
  time: 1704067200.0
  uid: f47ac10b-58cc-4372-a567-0e02b2c3d479
  data: {
    "eln_experiment_id": 12345,
    "irods_url": "/zone/home/project/results.csv"
  }
```

## Database

### Location

```
~/.local/mus/mus.db
```

### Tables

#### `muslog` Table
Stores all records with full metadata.

#### `hashcache` Table
Caches SHA256 checksums indexed by filename and mtime:

```
filename              | mtime      | hash
/path/to/file.csv    | 1704067200 | a3b4c5d6...
```

**How caching works:**
1. When you tag a file, mus calculates its SHA256
2. The hash is stored with the file's modification time (mtime)
3. Next time: if mtime hasn't changed, use cached hash
4. This makes repeated operations on large files very fast

### Querying the Database

While mus provides CLI commands, you can also query directly:

```bash
sqlite3 ~/.local/mus/mus.db

-- Recent tags
SELECT datetime(time, 'unixepoch'), filename, message
FROM muslog
WHERE type='tag'
ORDER BY time DESC
LIMIT 10;

-- Files by checksum
SELECT filename, datetime(time, 'unixepoch')
FROM muslog
WHERE checksum='a3b4c5d6...'
ORDER BY time DESC;
```

## Configuration System

### Hierarchical Configuration

mus uses a **hierarchical configuration system** where `.env` files cascade up the directory tree.

```
/home/jsmith/
    .env                    # Root config
        project_name=Lab Work
        collaborator=alice,bob
    │
    └── projects/
        .env                # Project config
            tag=project
        │
        └── experiment1/
            .env            # Experiment config
                tag=experiment1
                eln_experiment_id=12345
```

When you run mus from `/home/jsmith/projects/experiment1/`, it loads configuration in order:

1. `/home/jsmith/.env`
2. `/home/jsmith/projects/.env`
3. `/home/jsmith/projects/experiment1/.env`

**Result:**
```
project_name = "Lab Work"
collaborator = ["alice", "bob"]
tag = ["project", "experiment1"]
eln_experiment_id = 12345
```

### Configuration Types

#### 1. Regular Keys (Last Wins)
```
# parent/.env
project_name=Old Name

# child/.env
project_name=New Name

# Result: "New Name"
```

#### 2. List Keys (Accumulate)

Special keys that accumulate values: `tag`, `collaborator`

```
# parent/.env
tag=parent_tag,common_tag

# child/.env
tag=child_tag

# Result: ["parent_tag", "common_tag", "child_tag"]
```

#### 3. List Key Removal Syntax

Use `-` to remove values:

```
# parent/.env
tag=draft,temp,final

# child/.env
tag=-draft,-temp

# Result: ["final"]
```

### Secret Storage

Sensitive data (API keys, passwords) are stored in the system keyring, **not** in `.env` files.

```bash
# Store secret
mus config secret-set eln_apikey YOUR_KEY

# Secrets are stored in system keyring
# Linux: ~/.local/share/python_keyring/
# macOS: Keychain
# Windows: Credential Manager
```

Secrets can also come from environment variables:

```bash
export ELN_APIKEY="your-key"
# mus will use this automatically
```

**Priority**: Environment variables > Keyring > Not found

## Checksums

### Why Checksums?

Checksums (SHA256) provide:
1. **Integrity verification**: Ensure files haven't been corrupted
2. **Change detection**: Know when a file has been modified
3. **Deduplication**: Identify identical files regardless of name/location
4. **Provenance**: Track the same logical file across renames/moves

### How mus Uses Checksums

#### On Tagging
```bash
mus tag data.csv -m "Raw data"
```
1. Calculate SHA256 of data.csv
2. Store in hashcache with mtime
3. Save in record

#### On Searching
```bash
mus file data.csv
```
1. Calculate checksum (or use cache)
2. Search database for all records with this checksum
3. Show complete history regardless of filename changes

#### On Upload
```bash
mus irods upload data.csv -m "Upload"
```
1. Calculate local checksum
2. Upload to iRODS
3. Calculate remote checksum (iRODS)
4. Compare local vs remote
5. Store both for verification

#### On Verification
```bash
mus irods check data.csv.mango
```
1. Read local file checksum
2. Read remote checksum from iRODS
3. Compare both
4. Report differences

### Checksum Performance

For large files, checksum calculation can be slow. mus optimizes this:

**First time:**
```bash
mus tag large_file.bin -m "Tag"  # Calculates checksum: 5 seconds
```

**Subsequent times (unchanged file):**
```bash
mus tag large_file.bin -m "Tag again"  # Uses cache: < 0.1 seconds
```

**After modification:**
```bash
# Edit large_file.bin
mus tag large_file.bin -m "Modified"  # Recalculates: 5 seconds
```

The cache uses mtime (modification time), so any change triggers recalculation.

## Hooks System

### What are Hooks?

Hooks are events that plugins can subscribe to. This allows plugins to extend mus functionality without modifying core code.

### Available Hooks

| Hook | When Called | Purpose |
|------|-------------|---------|
| `plugin_init` | Startup | Register commands, add options |
| `prepare_record` | Before record save | Add metadata to records |
| `save_record` | During record save | React to operations |
| `finish_filetag` | After file tagging | Post-processing (ELN, iRODS) |

### Hook Priority

Hooks can specify priority (default: 10). Higher priority runs first.

```python
# This hook runs first (priority 20)
register_hook('prepare_record', add_metadata, priority=20)

# This hook runs second (priority 10)
register_hook('prepare_record', add_more_metadata, priority=10)
```

### Example: ELN Plugin Hooks

The ELN plugin uses hooks to integrate seamlessly:

```python
# 1. Add CLI options to existing commands
def init_eln(cli):
    files.filetag.params.append(
        click.Option(['-E', '--eln'], is_flag=True, help='Save to ELN'))

# 2. Add ELN metadata to records
def add_eln_data_to_record(record):
    env = get_env()
    if 'eln_experiment_id' in env:
        record.data['eln_experiment_id'] = env['eln_experiment_id']

# 3. Upload to ELN when file tagged
def eln_save_record(record):
    if should_upload_to_eln:
        eln_file_upload(record.filename)

# Register hooks
register_hook('plugin_init', init_eln)
register_hook('prepare_record', add_eln_data_to_record)
register_hook('save_record', eln_save_record)
```

## Plugins

### Built-in Plugins

#### ELN Plugin (`mus.plugins.eln`)
- Integrates with ELabJournal
- Commands: `mus eln tag-folder`, `mus eln upload`
- Adds `-E` flag to tag and log commands

#### iRODS Plugin (`mus.plugins.irods`)
- Integrates with iRODS/Mango
- Commands: `mus irods upload`, `mus irods check`, `mus irods get`
- Adds `-I` flag to tag command

#### History Plugin (`mus.plugins.history`)
- **Deprecated** - was for shell history tracking

### Plugin Architecture

Plugins are Python modules that:
1. Register hooks at import time
2. Add commands to the CLI
3. Extend existing commands with new options
4. Process records with additional metadata

## Mango Files

### What is a .mango File?

A `.mango` file is a **metadata file** that points to data stored in iRODS.

```
data.csv           # Your actual data file
data.csv.mango     # Metadata file pointing to iRODS location
```

**Contents of data.csv.mango:**
```
/zone/home/project/study/experiment/data.csv
```

This is the iRODS path where the actual file is stored.

### Why .mango Files?

1. **Track remote location**: Know where files are in iRODS
2. **Enable verification**: `mus irods check` uses .mango files
3. **Enable retrieval**: `mus irods get` uses .mango files
4. **Prevent re-upload**: mus knows file is already in iRODS

### Important: Don't Upload .mango Files!

.mango files are metadata, not data. mus automatically filters them:

```bash
# Safe - .mango files are automatically skipped
mus irods upload *
```

## Parent-Child Relationships

Records can form parent-child relationships to track workflows.

### Example: Macro Execution

```
Parent Record (type=macro):
  uid: abc123
  message: "Run analysis pipeline"

Child Record 1 (type=job):
  uid: def456
  child_of: abc123
  message: "Preprocessing step"

Child Record 2 (type=job):
  uid: ghi789
  child_of: abc123
  message: "Analysis step"
```

This allows you to see all jobs that were part of a macro execution.

## Data Flow Example

Let's trace a complete upload workflow:

```bash
mus irods upload analysis.csv -m "Final analysis"
```

**What happens:**

1. **CLI Layer**: Parse command, validate options
2. **File Processing**:
   - Check file exists
   - Calculate SHA256 checksum
   - Check cache first (mtime-based)
3. **Record Creation**:
   - Create Record object
   - Fill in user, host, cwd, timestamp
4. **Hook: prepare_record**:
   - ELN plugin adds experiment metadata
   - iRODS plugin adds metadata
5. **iRODS Upload**:
   - Determine target path from ELN metadata
   - Check if file already exists (by checksum)
   - Upload if new/changed
   - Apply Mango schema metadata
   - Verify checksums match
6. **Create .mango File**:
   - Write iRODS path to data.csv.mango
7. **ELN Upload**:
   - Create file section in experiment
   - Upload file to ELN
   - Post comment with iRODS link
8. **Hook: save_record**:
   - Save record to database
9. **Hook: finish_filetag**:
   - Finalize any post-processing

## Performance Considerations

### Checksum Caching
- First tag of large file: slow (full SHA256)
- Subsequent tags: fast (cached if unchanged)
- Clear cache: modify file or wait for mtime change

### Database Size
- SQLite handles millions of records efficiently
- Each record is ~1-2 KB
- 10,000 records ≈ 10-20 MB database

### Network Operations
- ELN API calls: ~100-500ms each
- iRODS uploads: network-dependent
- Concurrent uploads: not currently supported

## Security Model

### Secret Storage
- API keys in system keyring (encrypted)
- NOT in .env files (plain text)
- Environment variables override keyring

### File Permissions
- Database: `~/.local/mus/` (user-only)
- Config files: Standard file permissions
- iRODS: Uses iRODS authentication

### Network Security
- HTTPS for ELN API calls
- iRODS SSL/TLS if configured
- No credential logging

## Common Patterns

### Pattern 1: Progressive Metadata

Add metadata as you go:

```bash
# Initial tag
mus tag data.csv -m "Raw data"

# Process data...

# Tag intermediate result
mus tag processed.csv -m "After cleaning"

# Final archive
mus irods upload final.csv -m "Publication dataset"
```

### Pattern 2: Hierarchical Projects

```
research/
  .env (lab-wide config)
  project-a/
    .env (project config)
    experiment-1/
      .env (experiment config)
      data/
```

Each level inherits and extends configuration.

### Pattern 3: Batch Operations

```bash
# Tag many files
mus tag *.csv -m "Raw data files"

# Upload directory
mus irods upload data_folder/ -m "Complete dataset"
```

## Next Steps

- **Practical usage**: [Quick Start Guide](quickstart.md)
- **All commands**: [CLI Reference](cli-reference.md)
- **Configuration details**: [Configuration Guide](configuration.md)
- **ELN integration**: [ELN Plugin Guide](eln-plugin.md)
- **iRODS integration**: [iRODS Plugin Guide](irods-plugin.md)

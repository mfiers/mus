# iRODS Plugin Guide

Complete guide to using mus with iRODS institutional data storage and Mango metadata schema.

## Overview

The iRODS plugin integrates mus with [iRODS](https://irods.org/) (Integrated Rule-Oriented Data System) for institutional data archival. It provides:

- Structured data upload to iRODS
- Automatic Mango metadata schema application
- Checksum verification (local ↔ remote)
- `.mango` tracking files
- Integration with ELN for documentation
- Permission management

## Prerequisites

### 1. iRODS Access

You need:
- iRODS account on your institution's iRODS server
- Zone, username, and password
- iRODS iCommands installed

### 2. iRODS iCommands

#### Linux (Ubuntu/Debian)

```bash
# Add iRODS repository
wget -qO - https://packages.irods.org/irods-signing-key.asc | sudo apt-key add -
echo "deb [arch=amd64] https://packages.irods.org/apt/ $(lsb_release -sc) main" | \
  sudo tee /etc/apt/sources.list.d/renci-irods.list

# Install icommands
sudo apt-get update
sudo apt-get install irods-icommands
```

#### macOS

Use Docker (iCommands don't run natively on macOS):

```bash
# Configure mus to use Docker
mus config secret-set icmd_prefix \
  "docker run --platform linux/amd64 -i --rm -v $HOME:$HOME -v $HOME/.irods/:/root/.irods ghcr.io/utrechtuniversity/docker_icommands:0.2"
```

### 3. iRODS Authentication

Initialize iRODS connection:

```bash
iinit
```

Enter your iRODS credentials:
- Host
- Port
- Zone
- Username
- Password

Test connection:

```bash
ils  # List your iRODS home directory
```

### 4. ELN Configuration

**Important**: iRODS plugin requires ELN plugin configured!

```bash
# Configure ELN first
mus config secret-set eln_apikey "YOUR_API_KEY"
mus config secret-set eln_url "https://your-eln.com"

# Tag folder with experiment
mus eln tag-folder -x 12345
```

This is because iRODS paths are structured based on ELN project/study/experiment hierarchy.

### 5. Installation

Install mus with iRODS plugin:

```bash
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[irods]"
```

Or with all plugins:

```bash
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"
```

## Configuration

### Required Settings

```bash
# iRODS base path (your home directory in iRODS)
mus config secret-set irods_home "/your_zone/home/your_username"

# Mango web interface URL
mus config secret-set irods_web "https://mango.yoursite.com/data-object/view"

# iRODS group for permissions
mus config secret-set irods_group "your_research_group"
```

### Find Your iRODS Home

```bash
# Check your iRODS home directory
ienv | grep irods_home

# Or list root
ils /
```

### Verify Configuration

```bash
# Check all secrets are set
mus config secret-get irods_home
mus config secret-get irods_web
mus config secret-get irods_group
```

## How It Works

### Path Structure

iRODS paths are automatically structured based on ELN metadata:

```
{irods_home}/{project_name}/{study_name}/{experiment_name}/
```

**Example:**

With ELN metadata:
- Project: "Cancer Research 2024"
- Study: "Drug Screening"
- Experiment: "Compound Library Test"

Files uploaded to:
```
/zone/home/user/cancer_research_2024/drug_screening/compound_library_test/
```

Names are sanitized (lowercase, underscores, alphanumeric only).

### Upload Process

When you run `mus irods upload file.txt -m "Description"`:

1. **Validate ELN metadata** - Ensures project/study/experiment info exists
2. **Determine iRODS path** - Constructs path from ELN metadata
3. **Check existing files** - Queries iRODS for files already there
4. **Compare checksums** - Identifies new/changed files
5. **Upload files** - Transfers new/modified files to iRODS
6. **Apply metadata** - Adds Mango schema metadata
7. **Set permissions** - Runs `ichmod` for group access
8. **Verify checksums** - Confirms upload integrity
9. **Create .mango files** - Creates local tracking files
10. **Post to ELN** - Creates ELN comment with iRODS links

### Mango Schema

The Mango metadata schema tracks:

```json
{
  "path": "/local/path/to/file",
  "server": "compute-node-01",
  "upload_date": "2024-03-15",
  "__version__": "7.0.0",
  "experiment_id": "12345",
  "experiment_name": "My Experiment",
  "project_id": "100",
  "project_name": "My Project",
  "study_id": "200",
  "study_name": "My Study",
  "collaborator": "alice,bob",
  "description": "User-provided description",
  "sha256": "a3b4c5d6e7f8..."
}
```

This metadata is attached to each file in iRODS and searchable.

## Commands

### Upload to iRODS

```bash
mus irods upload FILE... -m "MESSAGE"
```

**Examples:**

```bash
# Single file
mus irods upload data.csv -m "Raw data from experiment"

# Multiple files
mus irods upload data.csv results.txt plot.png -m "Experiment results"

# Directory
mus irods upload analysis_folder/ -m "Complete analysis"

# Everything in current directory (safe - .mango files filtered!)
mus irods upload * -m "All experiment files"

# Force overwrite existing files
mus irods upload data.csv -m "Updated data" --irods-force

# Ignore symbolic links
mus irods upload folder/ -m "Data" --ignore-symlinks
```

### Check File Integrity

Verify checksums between local and iRODS:

```bash
mus irods check [FILE.mango...]
```

**Examples:**

```bash
# Check all .mango files in current directory
mus irods check

# Check specific file
mus irods check data.csv.mango

# Check multiple files
mus irods check file1.txt.mango file2.csv.mango data_folder.mango

# Extension is optional (automatically added)
mus irods check data.csv
```

**Output:**

```
Checking: data.csv
Checksum ok: data.csv
All checksums (1) are ok.
```

Or if mismatched:

```
Checking: data.csv
Checksum mismatch: data.csv
Failed 1 out of 1 checksums!
```

### Download from iRODS

Retrieve files from iRODS:

```bash
mus irods get FILE.mango...
```

**Examples:**

```bash
# Download single file
mus irods get data.csv.mango

# Download multiple files
mus irods get file1.txt.mango file2.csv.mango

# Force overwrite local file
mus irods get data.csv.mango --force
```

## `.mango` Files

### What are .mango Files?

`.mango` files are small text files that track where data is stored in iRODS.

**Example: `data.csv.mango`**

```
/zone/home/user/project/study/experiment/data.csv
```

Just a single line with the iRODS path.

### Why .mango Files?

1. **Track location**: Remember where files are in iRODS
2. **Enable verification**: `mus irods check` uses them
3. **Enable retrieval**: `mus irods get` uses them
4. **Prevent duplicates**: mus knows file is already uploaded

### Important: Don't Upload .mango Files!

`.mango` files are metadata, not data. mus automatically filters them:

```bash
# Safe - .mango files automatically skipped
mus irods upload *
```

### Where are .mango Files Created?

In the same directory as the uploaded file:

```
data.csv           # Original file
data.csv.mango     # Created after upload

results/           # Directory
results.mango      # Created after directory upload
```

## Advanced Features

### Force Overwrite

By default, mus won't overwrite files in iRODS:

```bash
# This fails if file exists
mus irods upload data.csv -m "Data"
```

To force overwrite:

```bash
# This overwrites existing file
mus irods upload data.csv -m "Updated data" --irods-force
```

**Use cases:**
- Correcting uploaded data
- Updating analysis results
- Replacing corrupted files

**Warning**: Overwrites are permanent and lose the previous version!

### Symbolic Links

By default, mus follows symbolic links:

```bash
# Uploads the target of the symlink
mus irods upload linked_file -m "Data"
```

To ignore symlinks:

```bash
# Skips symbolic links
mus irods upload * -m "Data" --ignore-symlinks
```

### Directory Uploads

Directories are uploaded recursively:

```bash
mus irods upload data_folder/ -m "Complete dataset"
```

**What happens:**
1. Entire directory structure uploaded to iRODS
2. Permissions applied recursively
3. Metadata applied to all files (with special note for folders)
4. Single `.mango` file created: `data_folder.mango`

**Checksum note**: For directory uploads, individual files get `sha256: "n.d. (folder upload)"` because calculating all checksums is slow. You can verify specific files after upload by checking them individually.

### Permissions

After upload, mus sets permissions:

```bash
ichmod -r own <irods_group> <path>
```

This gives your research group ownership of the data.

**If this fails**, mus prints a warning but continues. This can happen if:
- Group doesn't exist
- You lack permission to set permissions
- iRODS configuration issues

You may need to ask your iRODS administrator to set permissions.

## Integration with ELN

iRODS and ELN work together seamlessly:

### File Upload Decision Tree

When you run `mus irods upload file.ext -m "Message"`:

**For these extensions:**
- `.ipynb`, `.pdf`, `.png`, `.xlsx`, `.xls`, `.doc`, `.docx`

**Both:**
1. Upload to iRODS (archive)
2. Upload to ELN (lab notebook)

**For other extensions:**
1. Upload to iRODS (archive)
2. Post link to ELN (documentation)

### ELN Comment Format

After iRODS upload, ELN receives an HTML comment:

```html
Upload by jsmith
from compute-01:/home/jsmith/project
at 2024-03-15 14:30:22.

<ol>
<li><b>data.csv</b>
    <a href='https://mango.site.com/data-object/view/zone/home/user/project/data.csv'>
      [IRODS]
    </a>
    <span style='font-size: small; color: #444;'>(shasum: a3b4c5d6...)</span>
</li>
</ol>
```

The link goes directly to the file in the Mango web interface.

## Workflow Example

### Complete Research Data Workflow

```bash
# 1. Setup project
cd ~/experiments/cancer-screen-2024
mus eln tag-folder -x 12345

# 2. Work on experiment
python run_experiment.py

# 3. Quick check - tag intermediate files
mus tag intermediate_results.csv -m "After preprocessing"

# 4. Upload final results to archive
mus irods upload raw_data.csv \
                 processed_data.csv \
                 analysis.ipynb \
                 figures/ \
  -m "Complete dataset for publication"

# Output shows:
# - Files not uploaded yet: 4
# - Files already uploaded: 0
# - Checksums mismatched: 0
# - Folders: 1

# 5. Verify upload
mus irods check *.mango

# Output:
# Checking: raw_data.csv
# Checksum ok: raw_data.csv
# Checking: processed_data.csv
# Checksum ok: processed_data.csv
# ...
# All checksums (4) are ok.

# 6. Document in ELN
mus log -E "All data archived to iRODS, ready for publication"
```

### Collaboration Workflow

```bash
# Colleague A: Upload data
mus irods upload experiment_data.tar.gz -m "Complete experiment dataset"

# Colleague A: Share .mango file
git add experiment_data.tar.gz.mango
git commit -m "Add data location"
git push

# Colleague B: Clone repository
git clone <repo>
cd <repo>

# Colleague B: Download data
mus irods get experiment_data.tar.gz.mango

# Colleague B: Verify integrity
mus irods check experiment_data.tar.gz.mango
```

## Troubleshooting

### Error: "You MUST also upload to ELN (-E)"

```
UsageError: You MUST also upload to ELN (-E)
```

**Cause:** Using `mus tag -I` without `-E`.

**Solution:** iRODS uploads require ELN integration. Use the shortcut:

```bash
# Wrong
mus tag file.txt -m "Test" -I

# Right (use shortcut which includes -E)
mus irods upload file.txt -m "Test"
```

### Error: "Missing ELN records"

```
UsageError: Missing ELN records, please run `mus eln tag-folder`
```

**Cause:** No ELN experiment linked.

**Solution:**

```bash
mus eln tag-folder -x 12345
```

### Error: "File exists"

```
File exists: data.csv
```

**Cause:** File already in iRODS and you didn't use `--irods-force`.

**Solution:**

```bash
# Check if checksums match
mus irods check data.csv.mango

# If you want to overwrite
mus irods upload data.csv -m "Updated" --irods-force
```

### Error: "ichmod failed"

```
Warning: ichmod command failed
```

**Cause:** Permission setting failed (not critical).

**Solutions:**
1. Ignore if warning only (upload succeeded)
2. Ask iRODS admin to set permissions
3. Check your group name is correct:
   ```bash
   mus config secret-get irods_group
   ```

### Error: "Connection refused"

```
ConnectionRefusedError: [Errno 111] Connection refused
```

**Cause:** Can't connect to iRODS.

**Solutions:**

```bash
# Check iRODS authentication
iinit

# Test connection
ils

# Check environment
ienv
```

### Checksum Mismatch

```
Checksum mismatch: data.csv
```

**Possible causes:**
1. File modified after upload
2. Upload corrupted
3. Local file modified

**Solutions:**

```bash
# Re-upload with force
mus irods upload data.csv -m "Re-upload" --irods-force

# Or check what changed
ils -L /zone/path/to/data.csv  # Show iRODS checksum
mus file data.csv               # Show local checksum
```

### macOS: Command not found

```
docker: command not found
```

**Cause:** Docker not installed or `icmd_prefix` not set.

**Solution:**

```bash
# Install Docker Desktop
# https://www.docker.com/products/docker-desktop

# Configure mus
mus config secret-set icmd_prefix \
  "docker run --platform linux/amd64 -i --rm -v $HOME:$HOME -v $HOME/.irods/:/root/.irods ghcr.io/utrechtuniversity/docker_icommands:0.2"
```

## Performance Considerations

### Large Files

For large files:
- First upload: Checksum calculated (slow)
- Subsequent operations: Cached checksum (fast)
- Network speed is bottleneck for transfer

**Tips:**
1. Use compression: `tar -czf data.tar.gz data/`
2. Upload during off-hours
3. Consider resumable transfer (not currently supported by mus)

### Many Small Files

Uploading many small files is slower than few large files:
- Each file is a separate iRODS operation
- Metadata applied to each file
- Consider archiving: `tar -czf archive.tar.gz files/`

### Parallel Uploads

Not currently supported. Files are uploaded sequentially.

## Best Practices

### 1. Tag Folder First

```bash
# Always start with this
mus eln tag-folder -x 12345
```

### 2. Descriptive Messages

```bash
# Bad
mus irods upload data.tar.gz -m "data"

# Good
mus irods upload data.tar.gz -m "Raw LC-MS data, all replicates, experiment 2024-03"
```

### 3. Verify After Upload

```bash
# Always verify!
mus irods check *.mango
```

### 4. Keep .mango Files

- Commit them to Git
- Share with collaborators
- Needed for verification and download

```bash
git add *.mango
git commit -m "Add iRODS locations"
```

### 5. Archive, Don't Delete

Once uploaded to iRODS:
- Keep local copy until verified
- Don't delete local files immediately
- Verify checksums first

```bash
# Upload
mus irods upload data.csv -m "Data"

# Verify
mus irods check data.csv.mango

# Only then consider removing local copy
# (but better to keep both!)
```

### 6. Organize by Experiment

```
project/
  experiment-001/
    .env (eln_experiment_id=12345)
    data.csv
    data.csv.mango
  experiment-002/
    .env (eln_experiment_id=12346)
    data.csv
    data.csv.mango
```

Each experiment has its own iRODS path.

## Security Considerations

### Data Access

- iRODS permissions controlled by iRODS administrator
- Group ownership set via `ichmod`
- Check with: `ils -A /zone/path/to/file`

### Checksums

- SHA256 used throughout
- Detects corruption and tampering
- Verified on upload and download
- Stored in iRODS metadata

### Audit Trail

- All operations logged in mus database
- iRODS maintains its own audit trail
- ELN keeps permanent record with links

## See Also

- [Quick Start Guide](quickstart.md) - Basic iRODS usage
- [CLI Reference](cli-reference.md) - All iRODS commands
- [ELN Plugin Guide](eln-plugin.md) - ELN integration
- [Workflows](workflows.md) - Complete examples

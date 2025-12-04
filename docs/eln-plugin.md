# ELN Plugin Guide

Complete guide to using mus with ELabJournal electronic lab notebooks.

## Overview

The ELN plugin integrates mus with [ELabJournal](https://www.elabjournal.com/), allowing you to:

- Link folders to ELN experiments
- Upload files to experiments with metadata
- Post log entries as experiment comments
- Auto-convert Jupyter notebooks to PDF
- Track file provenance with checksums

## Prerequisites

### 1. ELabJournal Account

You need:
- Access to an ELabJournal instance
- Login credentials
- Permission to create API tokens

### 2. API Token

Generate an API token in ELabJournal:

1. Log in to your ELabJournal
2. Click your profile → **Account Settings**
3. Navigate to **API Tokens**
4. Click **Generate New Token**
5. Copy the token (you won't see it again!)

See: https://www.elabjournal.com/doc/GetanAPIToken.html

### 3. Installation

Install mus with ELN plugin support:

```bash
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[eln]"
```

Or with all plugins:

```bash
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"
```

## Configuration

### Store Credentials

```bash
# Store API key (required)
mus config secret-set eln_apikey "YOUR_API_TOKEN"

# Store ELN URL (required)
mus config secret-set eln_url "https://your-institution.elabjournal.com"
```

**Security Note**: Credentials are stored in your system keyring, NOT in plain text.

### Verify Configuration

```bash
# Check secrets are set
mus config secret-get eln_apikey
mus config secret-get eln_url
```

## Workflow

### Step 1: Create an Experiment in ELN

1. Log in to ELabJournal
2. Create or open a Project
3. Create or open a Study
4. Create a new Experiment
5. Note the Experiment ID (shown in the URL or experiment details)

**Example URL:**
```
https://your-institution.elabjournal.com/experiments/12345
                                                      ^^^^^
                                             Experiment ID: 12345
```

### Step 2: Link Your Folder

```bash
cd ~/projects/my-experiment
mus eln tag-folder -x 12345
```

**What this does:**
1. Queries ELabJournal API for experiment metadata
2. Retrieves Project, Study, and Experiment information
3. Stores metadata in local `.env` file

**Output:**
```
eln_project_id             : 100
eln_project_name           : Research Project Alpha
eln_study_id               : 200
eln_study_name             : Phase 1 Study
eln_experiment_id          : 12345
eln_experiment_name        : Experiment XYZ
eln_collaborator           : alice,bob
```

**Generated `.env` file:**
```
eln_project_id=100
eln_project_name=Research Project Alpha
eln_study_id=200
eln_study_name=Phase 1 Study
eln_experiment_id=12345
eln_experiment_name=Experiment XYZ
eln_collaborator=alice,bob
```

### Step 3: Use ELN-Enabled Commands

Now all mus commands in this directory (and subdirectories) know which experiment to use.

## Commands

### Upload Files

```bash
mus eln upload FILE... -m "MESSAGE"
```

**Examples:**

```bash
# Single file
mus eln upload results.txt -m "Initial results"

# Multiple files
mus eln upload data.csv plot.png analysis.py -m "Complete analysis"

# With editor for long message
mus eln upload report.pdf --editor
```

**What happens:**
1. Files are tagged in local database
2. Checksums are calculated and stored
3. Files are uploaded to ELN experiment
4. A comment is posted with:
   - Upload metadata (user, host, path, time)
   - List of files with checksums
   - Links to files

### Post Log Entry

```bash
mus log -E "MESSAGE"
```

**Examples:**

```bash
# Simple log
mus log -E "Started preprocessing step"

# Multi-line log
mus log -E "Phase 1 complete.

Results look promising. Moving to phase 2."

# Log without ELN
mus log "Local note only"
```

**Output in ELN:**
- Creates a comment on the experiment
- Includes metadata (user, host, cwd, timestamp)
- Formatted with HTML

### Tag Files (with ELN upload)

```bash
mus tag FILE... -m "MESSAGE" -E
```

This is the underlying command that `mus eln upload` uses.

**Examples:**

```bash
# Tag and upload to ELN
mus tag data.csv -m "Data file" -E

# Tag locally only (no -E flag)
mus tag data.csv -m "Data file"
```

### Update Metadata

If experiment metadata changes in ELN:

```bash
mus eln update
```

This refreshes your local `.env` with current data from ELabJournal.

## Special Features

### Jupyter Notebook Conversion

When you upload `.ipynb` files, mus automatically converts them to PDF:

```bash
mus eln upload analysis.ipynb -m "Analysis notebook"
```

**What happens:**
1. `analysis.ipynb` is uploaded as-is
2. PDF is generated: `20240315_143022_analysis.pdf`
3. PDF is uploaded to ELN
4. PDF is tagged in local database
5. Both files appear in ELN

**Requirements:**
- `pandoc` installed
- `texlive-xetex` installed (for LaTeX → PDF)

**Install on Ubuntu/Debian:**
```bash
sudo apt-get install pandoc texlive-xetex
```

**Install on macOS:**
```bash
brew install pandoc basictex
```

### File Types Always Uploaded to ELN

These file types are always uploaded to ELN, even during iRODS uploads:

- `.ipynb` - Jupyter notebooks
- `.pdf` - PDF documents
- `.png` - PNG images
- `.xlsx`, `.xls` - Excel spreadsheets
- `.doc`, `.docx` - Word documents

Other files only create ELN comments with links to iRODS.

## Advanced Usage

### Override Experiment ID

You can override the experiment ID for a single operation:

```bash
# Upload to different experiment
mus eln upload file.txt -m "Test" -x 54321

# Log to different experiment
mus log -E "Message" -x 54321
```

**Use case**: Working with multiple experiments in the same directory.

### Dry Run

Test upload without actually uploading:

```bash
mus tag file.txt -m "Test" -E --dry-run
```

**What happens:**
- Local database record created
- Checksums calculated
- NO upload to ELN
- Shows what would be uploaded

### Multiple Experiments

To work with multiple experiments:

**Option 1: Subdirectories**
```
project/
  .env (eln_project_id, eln_study_id)
  experiment-1/
    .env (eln_experiment_id=12345)
  experiment-2/
    .env (eln_experiment_id=12346)
```

**Option 2: Override with -x**
```bash
mus eln upload file.txt -m "To exp 1" -x 12345
mus eln upload file.txt -m "To exp 2" -x 12346
```

## Metadata Format

### Comment Format

When you upload files, mus posts an HTML comment to ELN:

```html
Upload by jsmith
from compute-01:/home/jsmith/project
at 2024-03-15 14:30:22.

<ol>
<li><b>data.csv</b> <span style='font-size: small; color: #444;'>
    (shasum: a3b4c5d6...)</span></li>
<li><b>results.txt</b> <span style='font-size: small; color: #444;'>
    (shasum: f1e2d3c4...)</span></li>
</ol>
```

### File Section

For uploaded files, mus creates a "File Section" in ELN with:
- Title: "{message} files"
- Files attached to the section
- Original filenames preserved

## Troubleshooting

### Error: "No experiment ID given"

```
ElnNoExperimentId: No experiment ID given
```

**Solution:**
```bash
# Either tag the folder
mus eln tag-folder -x 12345

# Or specify with each command
mus log -E "Message" -x 12345
```

### Error: "Conflicting experiment ID"

```
ElnConflictingExperimentId: Conflicting experiment IDs
```

**Cause:** You specified `-x` when `.env` already has `eln_experiment_id`.

**Solution:**
```bash
# Either remove from command
mus log -E "Message"

# Or remove from .env
nano .env  # Delete eln_experiment_id line
```

### Error: "Please specify the eln_apikey key"

```
MusSecretNotDefined: eln_apikey not defined
```

**Solution:**
```bash
mus config secret-set eln_apikey "YOUR_API_KEY"
```

### Error: "Invalid API token"

**Possible causes:**
1. Token expired
2. Token revoked
3. Wrong token copied

**Solution:**
1. Generate new token in ELabJournal
2. Update mus configuration:
   ```bash
   mus config secret-set eln_apikey "NEW_TOKEN"
   ```

### Upload is Slow

**Cause:** Large files or slow network.

**Solutions:**
1. Check network connection
2. Consider uploading to iRODS for large files
3. ELN is meant for documents, not large datasets

### Jupyter Conversion Fails

**Error:**
```
nbconvert error: ...
```

**Solution:**
```bash
# Install dependencies
sudo apt-get install pandoc texlive-xetex

# Or on macOS
brew install pandoc basictex

# Test manually
jupyter nbconvert --to pdf notebook.ipynb
```

## API Details

### Endpoints Used

The ELN plugin uses these ELabJournal API endpoints:

| Endpoint | Purpose |
|----------|---------|
| `GET /api/v1/experiments/{id}` | Get experiment metadata |
| `POST /api/v1/experiments/{id}/journal` | Create journal entry |
| `POST /api/v1/experiments/{id}/journal/{jid}/file` | Upload file |

### Rate Limiting

ELabJournal has API rate limits:
- Typical: 100 requests per minute
- May vary by institution

mus batches operations where possible, but uploading many files individually can hit limits.

### Authentication

All API calls use Bearer token authentication:

```
Authorization: Bearer YOUR_API_TOKEN
```

## Security Considerations

### API Token Storage

- Stored in system keyring (encrypted)
- Never logged to console or files
- Can override with environment variable: `ELN_APIKEY`

### Network Security

- All communication over HTTPS
- Validates SSL certificates
- No credential transmission in URLs

### File Permissions

- `.env` files have standard permissions
- Consider using `chmod 600 .env` for sensitive projects
- Database at `~/.local/mus/` is user-only

## Integration with iRODS

For large datasets, combine ELN and iRODS:

```bash
# Upload to both iRODS (archive) and ELN (lab notebook)
mus irods upload large_dataset.tar.gz -m "Complete dataset"
```

**What happens:**
1. File uploaded to iRODS
2. `.mango` file created locally
3. ELN receives:
   - Comment with file metadata
   - Link to file in iRODS/Mango
   - Checksum for verification

**Benefits:**
- Large files in institutional storage (iRODS)
- Documentation in lab notebook (ELN)
- Links between both systems
- Checksums for integrity

See: [iRODS Plugin Guide](irods-plugin.md)

## Best Practices

### 1. One Experiment Per Directory

```
research/
  project-a/
    experiment-001/  # eln_experiment_id=12345
    experiment-002/  # eln_experiment_id=12346
```

### 2. Tag Folders Early

```bash
# First thing when starting an experiment
cd new-experiment
mus eln tag-folder -x 12345
```

### 3. Descriptive Messages

```bash
# Bad
mus eln upload data.csv -m "data"

# Good
mus eln upload data.csv -m "Raw data from LC-MS run, replicate 3"
```

### 4. Log Progress

```bash
# Regular logging keeps good records
mus log -E "Completed sample preparation (n=24)"
mus log -E "Started instrument run, expected completion: 6h"
mus log -E "Run complete, all samples processed successfully"
```

### 5. Upload Intermediate Results

```bash
# Don't wait until the end
mus eln upload preliminary_results.csv -m "After preprocessing"
mus eln upload intermediate_plot.png -m "Initial visualization"
mus eln upload final_analysis.ipynb -m "Complete analysis"
```

## Examples

### Complete Research Workflow

```bash
# Day 1: Setup
cd ~/experiments/2024-03-exp-alpha
mus eln tag-folder -x 12345
mus log -E "Experiment started"

# Day 2: Data collection
mus eln upload raw_data.csv -m "Raw data from instrument"
mus log -E "Data collection complete, 100 samples"

# Day 3: Analysis
python analysis.py
mus eln upload analysis.ipynb -m "Initial analysis"
mus log -E "Found significant effect in group A"

# Day 4: Results
mus eln upload figures/figure1.png figures/figure2.png \
  -m "Figures for paper"
mus eln upload manuscript_draft.docx -m "First draft"
mus log -E "Manuscript draft complete"

# Day 5: Archive
mus irods upload raw_data.csv processed_data.csv figures/ \
  -m "Complete dataset for publication"
mus log -E "Experiment complete, all data archived"
```

## See Also

- [Quick Start Guide](quickstart.md) - Basic ELN usage
- [CLI Reference](cli-reference.md) - All ELN commands
- [iRODS Plugin Guide](irods-plugin.md) - Combine with iRODS
- [Workflows](workflows.md) - Real-world examples

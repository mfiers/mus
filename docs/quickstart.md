# Quick Start Guide

This guide will get you up and running with mus in 10 minutes.

## Prerequisites

- mus installed ([Installation Guide](installation.md))
- Python 3.8+
- (Optional) ELabJournal account with API key
- (Optional) iRODS access

## Basic Usage (No External Services)

### 1. Tag Your First File

Create a test file and tag it:

```bash
# Create a test file
echo "Hello, mus!" > test.txt

# Tag the file with a message
mus tag test.txt -m "My first tagged file"
```

Output:
```
Tagged: test.txt
```

### 2. Find File Information

```bash
mus file test.txt
```

Output shows:
- File checksum (SHA256)
- Recent operations on this file
- When it was tagged, by whom, from which host

### 3. Log a Message

Record a log entry in the database:

```bash
mus log "Started data analysis"
```

This creates a timestamped record in the database.

### 4. Search Your History

```bash
# Show recent records
mus search

# Show more details
mus search -l

# Filter by type
mus search --type tag
```

## Working with Configuration

### 1. Set Up a Project Directory

```bash
mkdir my-project
cd my-project

# Create local configuration
echo "project_name=My First Project" > .env
echo "tag=draft,analysis" >> .env
```

### 2. Check Configuration

```bash
# View current configuration
mus config show
```

The configuration will show your local settings plus any inherited from parent directories.

## Using ELN Integration (Optional)

If you have an ELabJournal account, you can integrate mus with your electronic lab notebook.

### 1. Get Your API Key

1. Log in to your ELabJournal
2. Go to Account Settings → API Tokens
3. Generate a new API token
4. Copy the token

See: https://www.elabjournal.com/doc/GetanAPIToken.html

### 2. Configure ELN Credentials

```bash
# Store your ELN credentials securely
mus config secret-set eln_apikey YOUR_API_KEY
mus config secret-set eln_url https://your-institution.elabjournal.com
```

### 3. Link a Folder to an Experiment

```bash
# Get your experiment ID from ELabJournal (shown in the experiment URL)
# Example: https://your-institution.elabjournal.com/experiments/12345
# The experiment ID is: 12345

cd my-project
mus eln tag-folder -x 12345
```

This stores the experiment metadata in `.env` and all future operations will reference this experiment.

### 4. Upload a File to ELN

```bash
# Create a results file
echo "Analysis results" > results.txt

# Upload to ELN
mus eln upload results.txt -m "Analysis results for experiment"
```

The file is uploaded to your ELabJournal experiment with metadata.

### 5. Post a Log Entry to ELN

```bash
# Log directly to ELN
mus log -E "Completed data preprocessing"
```

This creates both a local record and a comment on your ELN experiment.

## Using iRODS Integration (Optional)

If you have access to iRODS institutional storage, you can archive data with full metadata.

### 1. Configure iRODS

First, ensure iRODS icommands are installed and you're authenticated (`iinit`).

```bash
# Set up iRODS configuration
mus config secret-set irods_home /your/irods/zone/home/username
mus config secret-set irods_web https://mango.yoursite.com/data-object/view
mus config secret-set irods_group your_irods_group
```

### 2. Upload to iRODS

**Important**: iRODS uploads require ELN tagging first!

```bash
# Make sure you've tagged your folder with an ELN experiment
mus eln tag-folder -x 12345

# Upload to both iRODS and ELN
mus irods upload results.txt analysis.csv -m "Final results"
```

What happens:
1. Files are uploaded to iRODS in a structured path based on your ELN project/study/experiment
2. Checksums are calculated and stored as metadata
3. A `.mango` file is created locally to track the iRODS location
4. A summary with iRODS links is posted to ELN

### 3. Verify Upload

```bash
# Check that files match between local and iRODS
mus irods check results.txt.mango
```

Output:
```
Checking: results.txt
Checksum ok: results.txt
All checksums (1) are ok.
```

### 4. Download from iRODS

If you need to retrieve files:

```bash
# Download using the .mango file
mus irods get results.txt.mango
```

## Common Workflows

### Workflow 1: Simple File Tracking

```bash
# Tag multiple files at once
mus tag data1.csv data2.csv report.txt -m "Initial dataset"

# Tag with an editor for longer messages
mus tag analysis.ipynb --editor

# Find files by pattern
mus file data1.csv
```

### Workflow 2: ELN-Only Workflow

```bash
# Link folder to experiment
mus eln tag-folder -x 12345

# Upload various file types
mus eln upload notebook.ipynb figure.png data.xlsx -m "Analysis results"

# Jupyter notebooks are automatically converted to PDF and uploaded too!
```

### Workflow 3: Full Research Data Management

```bash
# 1. Set up project
mkdir experiment-2024-01
cd experiment-2024-01
mus eln tag-folder -x 12345

# 2. Work on your analysis
python analyze_data.py

# 3. Tag intermediate results
mus tag intermediate_results.csv -m "After preprocessing"

# 4. Upload final results to archive
mus irods upload final_results.csv analysis.ipynb figures/ \
  -m "Final analysis for publication"

# 5. Verify everything is backed up
mus irods check *.mango

# 6. Log completion
mus log -E "Analysis complete, all files archived"
```

## Understanding the Database

All mus operations are recorded in a SQLite database at `~/.local/mus/mus.db`.

```bash
# View recent activity
mus search -l

# Search for specific files
mus file my_data.csv

# View database statistics
mus db stats
```

## Tips and Tricks

### 1. Wildcard Uploads

```bash
# Upload all CSV files
mus eln upload *.csv -m "All data files"

# mus automatically filters out .mango files!
mus irods upload * -m "Everything"  # Safe - won't upload .mango files
```

### 2. Verbose Mode

```bash
# See what's happening under the hood
mus -v tag file.txt -m "Test"

# Extra verbose
mus -vv irods upload file.txt -m "Test"
```

### 3. Hierarchical Configuration

```bash
# Parent directory
cd ~/projects
echo "collaborator=alice,bob" > .env

# Child directory
cd ~/projects/experiment1
echo "tag=experiment1" > .env

# Configuration is merged!
mus config show
# Shows: collaborator=alice,bob and tag=experiment1
```

### 4. Checking File Integrity

```bash
# Find all records of a file (by checksum)
mus file my_data.csv

# If you moved or renamed it, mus can still find it by checksum!
```

## Next Steps

Now that you've learned the basics:

- **Understand the concepts**: Read [Core Concepts](concepts.md)
- **Learn all commands**: See [CLI Reference](cli-reference.md)
- **Advanced configuration**: Check [Configuration Guide](configuration.md)
- **ELN deep dive**: Read [ELN Plugin Guide](eln-plugin.md)
- **iRODS details**: See [iRODS Plugin Guide](irods-plugin.md)
- **Real-world examples**: Browse [Workflow Examples](workflows.md)

## Getting Help

```bash
# General help
mus --help

# Command help
mus tag --help
mus eln --help
mus irods --help

# Sub-command help
mus eln upload --help
mus irods check --help
```

## Troubleshooting

### "Please provide a message"

Tags and uploads require a message:

```bash
# Wrong
mus tag file.txt

# Right
mus tag file.txt -m "Description"

# Or use editor
mus tag file.txt --editor
```

### "No experiment ID given"

ELN and iRODS commands need an experiment ID:

```bash
# Set it once per directory
mus eln tag-folder -x 12345

# Or specify each time
mus log -x 12345 -E "My message"
```

### "You MUST also upload to ELN (-E)"

iRODS uploads require ELN integration:

```bash
# Wrong
mus irods upload file.txt -m "Test"

# Right (note: -I for irods, -E for eln)
mus irods upload file.txt -m "Test"  # actually uses shortcut, -E is implicit

# Or use the full command
mus tag file.txt -m "Test" -I -E
```

Actually, the `mus irods upload` shortcut automatically includes ELN upload, so just use:

```bash
mus irods upload file.txt -m "Test"
```

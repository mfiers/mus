# Workflow Examples

Real-world examples of using mus in research workflows.

## Table of Contents

- [Daily Research Workflow](#daily-research-workflow)
- [Computational Analysis Pipeline](#computational-analysis-pipeline)
- [Multi-User Collaboration](#multi-user-collaboration)
- [Data Publication Workflow](#data-publication-workflow)
- [Instrument Data Management](#instrument-data-management)
- [Jupyter Notebook Workflow](#jupyter-notebook-workflow)
- [Long-Term Storage Archive](#long-term-storage-archive)
- [Quality Control Workflow](#quality-control-workflow)

## Daily Research Workflow

### Scenario

Researcher working on daily experiments, needs to track progress and link to lab notebook.

### Setup

```bash
# Once: Configure ELN
mus config secret-set eln_apikey "YOUR_KEY"
mus config secret-set eln_url "https://your-eln.com"

# Per experiment: Link folder
cd ~/experiments/2024-03-experiment-alpha
mus eln tag-folder -x 12345
```

### Daily Workflow

```bash
# Morning: Log start
mus log -E "Starting experiment alpha, preparing samples"

# During work: Tag files as you create them
python preprocess_data.py
mus tag raw_data.csv -m "Raw data from instrument run 1"

python analyze_data.py
mus tag analysis_results.csv -m "Analysis results, preliminary"

# Create figures
python plot_results.py
mus eln upload figure1.png figure2.png -m "Result figures"

# Afternoon: Log progress
mus log -E "Completed runs 1-5, all successful. Starting run 6."

# Evening: Upload notebook
mus eln upload analysis.ipynb -m "Today's analysis notebook"

# End of day: Final log
mus log -E "Day complete. 6 runs finished, results look promising."
```

### Benefits

- Complete audit trail in database
- Lab notebook updated automatically
- Files linked to experiments
- Easy to review history

## Computational Analysis Pipeline

### Scenario

Bioinformatics pipeline with multiple stages, need to track intermediate results and archive final data.

### Directory Structure

```
rna-seq-analysis/
  .env                      # Project config
  00-raw/                   # Raw data
  01-qc/                    # Quality control
  02-aligned/               # Aligned reads
  03-counts/                # Count matrices
  04-dge/                   # Differential expression
  05-figures/               # Final figures
  pipeline.sh               # Analysis script
```

### Setup

```bash
cd rna-seq-analysis
cat > .env << 'EOF'
eln_experiment_id=12345
tag=rnaseq,cancer,2024
collaborator=alice,bob
EOF

# Configure iRODS for archival
mus config secret-set irods_home "/zone/home/user"
mus config secret-set irods_web "https://mango.site.com/data-object/view"
mus config secret-set irods_group "cancer_research"
```

### Pipeline Script

```bash
#!/bin/bash
# pipeline.sh

set -e  # Exit on error

# Log pipeline start
mus log -E "RNA-seq pipeline started"

# Stage 1: Quality Control
echo "Stage 1: QC"
fastqc *.fastq -o 01-qc/
mus tag 01-qc/*.html -m "QC reports for all samples"
mus log -E "QC complete, all samples passed"

# Stage 2: Alignment
echo "Stage 2: Alignment"
for file in 00-raw/*.fastq; do
    sample=$(basename "$file" .fastq)
    hisat2 -x genome_index -U "$file" -S "02-aligned/${sample}.sam"
done
mus tag 02-aligned/*.sam -m "Aligned reads (SAM format)"
mus log -E "Alignment complete for all samples"

# Stage 3: Count Matrix
echo "Stage 3: Counting"
featureCounts -a genes.gtf -o 03-counts/counts.txt 02-aligned/*.sam
mus tag 03-counts/counts.txt -m "Raw count matrix"
mus log -E "Counting complete"

# Stage 4: Differential Expression
echo "Stage 4: DGE analysis"
Rscript dge_analysis.R
mus tag 04-dge/dge_results.csv -m "Differential expression results"
mus tag 04-dge/significant_genes.csv -m "Significantly DE genes (FDR < 0.05)"
mus log -E "DGE analysis complete, found 453 significant genes"

# Stage 5: Figures
echo "Stage 5: Generating figures"
Rscript plot_results.R
mus eln upload 05-figures/*.pdf -m "Final figures for publication"

# Archive everything to iRODS
echo "Archiving to iRODS"
mus irods upload \
  00-raw/ \
  03-counts/counts.txt \
  04-dge/dge_results.csv \
  05-figures/ \
  -m "Complete RNA-seq analysis results"

# Verify archive
mus irods check *.mango

# Final log
mus log -E "Pipeline complete. All data archived."
```

### Run Pipeline

```bash
chmod +x pipeline.sh
./pipeline.sh
```

### Review Results

```bash
# View pipeline history
mus search -l | head -20

# Find specific file
mus file 04-dge/dge_results.csv

# Check what's archived
ls -1 *.mango
```

## Multi-User Collaboration

### Scenario

Team of 3 researchers working on shared project, need to coordinate and track who did what.

### Setup

```bash
# Team lead: Set up project structure
mkdir cancer-drug-screening
cd cancer-drug-screening

# Create shared configuration
cat > .env << 'EOF'
eln_project_id=100
eln_study_id=200
tag=drug_screening,2024,team_alpha
collaborator=alice,bob,charlie
EOF

git init
git add .env
git commit -m "Initial project setup"
git remote add origin git@github.com:lab/cancer-screening.git
git push -u origin main

# Share repository
# Team members clone
```

### Alice: Screening Experiment 1

```bash
git pull
cd cancer-drug-screening
mkdir experiment-001
cd experiment-001

# Link to her experiment
mus eln tag-folder -x 12345

# Run screening
python screen_compounds.py --library A

# Tag results
mus tag screen_results_A.csv -m "Screening results for compound library A (Alice)"
mus log -E "Screening library A complete - 96 compounds tested"

# Share
git add .env screen_results_A.csv.mango
git commit -m "Alice: Library A screening complete"
git push
```

### Bob: Screening Experiment 2

```bash
git pull
cd cancer-drug-screening
mkdir experiment-002
cd experiment-002

# Link to his experiment
mus eln tag-folder -x 12346

# Run screening
python screen_compounds.py --library B

# Tag results
mus tag screen_results_B.csv -m "Screening results for compound library B (Bob)"
mus log -E "Screening library B complete - 96 compounds tested"

# Share
git add .env screen_results_B.csv.mango
git commit -m "Bob: Library B screening complete"
git push
```

### Charlie: Combined Analysis

```bash
git pull
cd cancer-drug-screening
mkdir analysis
cd analysis

# Link to analysis experiment
mus eln tag-folder -x 12347

# Get data from colleagues
mus irods get ../experiment-001/screen_results_A.csv.mango
mus irods get ../experiment-002/screen_results_B.csv.mango

# Combined analysis
python combine_screens.py
mus tag combined_results.csv -m "Combined analysis of libraries A+B (Charlie)"

# Identify hits
python find_hits.py
mus tag hit_compounds.csv -m "Top 10 hit compounds (IC50 < 1 μM)"

# Upload results
mus irods upload combined_results.csv hit_compounds.csv \
  -m "Combined screening results and hits"

# Share findings
mus log -E "Combined analysis complete. Found 10 strong hits. See hit_compounds.csv"

git add .env *.mango
git commit -m "Charlie: Combined analysis and hits identified"
git push
```

### Team Review

```bash
# Each team member can see full history
mus search --user alice -n 20
mus search --user bob -n 20
mus search --user charlie -n 20

# Track specific files
mus file hit_compounds.csv
```

## Data Publication Workflow

### Scenario

Preparing data for journal publication, need to ensure integrity and provide supplementary materials.

### Directory Structure

```
publication-2024/
  manuscript/
    draft.docx
    references.bib
  data/
    raw/                   # Raw data
    processed/             # Processed data
    supplementary/         # Supplementary data
  figures/
    figure1.pdf
    figure2.pdf
    figure3.pdf
  code/
    analysis.R
    plots.R
```

### Workflow

```bash
cd publication-2024

# Link to publication experiment
mus eln tag-folder -x 12345

# Tag raw data
mus tag data/raw/*.csv -m "Raw experimental data for publication"

# Process and tag
Rscript code/analysis.R
mus tag data/processed/*.csv -m "Processed data (normalized, outliers removed)"

# Generate figures
Rscript code/plots.R
mus tag figures/*.pdf -m "Manuscript figures"

# Create supplementary materials
python create_supplementary.py
mus tag data/supplementary/*.xlsx -m "Supplementary data tables"

# Upload to ELN for lab records
mus eln upload \
  manuscript/draft.docx \
  figures/*.pdf \
  -m "Manuscript draft v3 and figures"

# Archive complete dataset to iRODS
mus irods upload \
  data/raw/ \
  data/processed/ \
  data/supplementary/ \
  code/ \
  figures/ \
  -m "Complete dataset for publication: Nature Protocols 2024"

# Verify integrity
mus irods check *.mango
echo "All checksums verified ✓"

# Generate data availability statement
cat > data_availability.txt << EOF
Data Availability

All data associated with this study are available at:
$(mus config secret-get irods_web | cut -d/ -f1-3)/collection/browse$(cat data/raw.mango)

Raw data: SHA256 checksums
$(grep -A1 "raw/" *.mango | xargs -I {} cat {} | xargs ils -L | grep checksum)
EOF

# Tag manuscript with data location
mus log -E "Manuscript submitted. All data archived with verified checksums."
```

### Benefits

- All data checksummed and verified
- Complete audit trail
- Data available for reviewers
- Reproducible from archived data
- Data availability statement generated automatically

## Instrument Data Management

### Scenario

Managing data from analytical instruments (LC-MS, sequencer, microscope, etc.) with automatic upload.

### Setup

```bash
# Create instrument data directory
mkdir -p ~/instrument-data/2024-03

# Configure
cd ~/instrument-data/2024-03
cat > .env << 'EOF'
eln_project_id=100
tag=lcms,instrument_data,2024
EOF
```

### Automated Upload Script

```bash
#!/bin/bash
# auto_upload.sh - Run after instrument acquisition

INSTRUMENT="LC-MS-01"
DATA_DIR="/mnt/instrument/data"
DATE=$(date +%Y%m%d)

# Create daily directory
mkdir -p "${DATE}"
cd "${DATE}"

# Link to today's experiment
# (Get experiment ID from lab calendar or prompt)
read -p "Enter ELN experiment ID: " EXPID
mus eln tag-folder -x "$EXPID"

# Copy data from instrument
echo "Copying data from instrument..."
cp -r "${DATA_DIR}/"*.raw .

# Tag all files
echo "Tagging files..."
mus tag *.raw -m "Raw data from ${INSTRUMENT} - ${DATE}"

# Upload to archive
echo "Uploading to archive..."
mus irods upload *.raw -m "Instrument data: ${INSTRUMENT}, ${DATE}"

# Verify
echo "Verifying checksums..."
mus irods check *.mango

# Log completion
mus log -E "Automatic data upload complete: ${INSTRUMENT}, ${DATE}, $(ls -1 *.raw | wc -l) files"

echo "Done! Data safely archived."
```

### Usage

```bash
# Run after each instrument session
cd ~/instrument-data/2024-03/
./auto_upload.sh
```

### Scheduled Backup

```bash
# Add to crontab for daily backup
crontab -e

# Run every day at 18:00
0 18 * * * cd ~/instrument-data/2024-03 && ./auto_upload.sh >> upload.log 2>&1
```

## Jupyter Notebook Workflow

### Scenario

Data scientist using Jupyter notebooks for analysis, need to track versions and share with collaborators.

### Workflow

```bash
cd analysis-project
mus eln tag-folder -x 12345

# Work on notebook
jupyter lab

# Periodically: Save checkpoints
mus tag analysis.ipynb -m "Checkpoint: completed preprocessing"
# (Creates PDF automatically when uploaded to ELN)

# Major version: Upload to ELN
mus eln upload analysis.ipynb -m "Analysis v1: Initial results"
# -> Uploads both .ipynb and timestamped PDF

# Continue working
jupyter lab

# Another checkpoint
mus tag analysis.ipynb -m "Checkpoint: added statistical tests"

# Final version
mus eln upload analysis.ipynb -m "Analysis v2: Final version with stats"
mus log -E "Analysis complete, notebook finalized"

# Archive for publication
mus irods upload analysis.ipynb -m "Final analysis notebook for paper"
```

### Benefits

- Every version tracked with checksum
- PDF automatically generated and uploaded
- Easy to find previous versions
- Notebooks archived for reproducibility

### Find Previous Version

```bash
# See all versions
mus file analysis.ipynb

# Shows all checkpoints with checksums
# Can identify and retrieve specific version if needed
```

## Long-Term Storage Archive

### Scenario

Archiving completed project for long-term storage (7+ years institutional requirement).

### Workflow

```bash
cd completed-project-2024

# Final organization
mkdir archive
cd archive

# Collect final data
cp ../final_data/*.csv data/
cp ../manuscript/*.pdf manuscript/
cp ../code/*.R code/
cp ../figures/*.pdf figures/

# Create README
cat > README.txt << EOF
Project: Cancer Drug Screening 2024
PI: Dr. Smith
Date: 2024-03-15
ELN Project: 100
ELN Study: 200
ELN Experiment: 12345

Contents:
- data/: Final processed datasets
- manuscript/: Published manuscript
- code/: Analysis code
- figures/: Publication figures

All files checksummed and verified.
EOF

# Tag README
mus tag README.txt -m "Archive README"

# Upload everything
mus irods upload \
  data/ \
  manuscript/ \
  code/ \
  figures/ \
  README.txt \
  -m "Long-term archive: Cancer Drug Screening 2024 (7-year retention)"

# Verify
mus irods check *.mango

# Create archive manifest
cat > MANIFEST.txt << EOF
Archive Manifest
Created: $(date)
Location: $(cat data.mango | head -1 | xargs dirname)

Files:
$(ls -1 *.mango | while read f; do
  base=$(basename "$f" .mango)
  checksum=$(mus file "$base" | grep Checksum | awk '{print $2}')
  echo "- $base (SHA256: $checksum)"
done)

Archived by: $(whoami)
Host: $(hostname)
EOF

# Final log
mus log -E "Project archived for long-term storage. All data checksummed and verified."

# Save archive metadata
git add .env *.mango MANIFEST.txt README.txt
git commit -m "Final archive - 2024-03-15"
git tag "archive-2024-03-15"
git push --tags
```

### 7 Years Later: Retrieve Archive

```bash
# Clone repository
git clone git@github.com:lab/cancer-screening-2024.git
cd cancer-screening-2024/archive

# Check manifest
cat MANIFEST.txt

# Download data
mus irods get data.mango
mus irods get manuscript.mango
mus irods get code.mango
mus irods get figures.mango

# Verify integrity
mus irods check *.mango
# All checksums should still match!
```

## Quality Control Workflow

### Scenario

Quality control lab processing samples with accept/reject decisions.

### Workflow

```bash
cd qc-lab
mus eln tag-folder -x 12345

# Process batch of samples
./process_samples.sh batch_001

# QC analysis
python qc_analysis.py batch_001/*.dat

# Tag with results
for sample in batch_001/*.dat; do
  result=$(python check_qc.py "$sample")

  if [ "$result" == "PASS" ]; then
    mus tag "$sample" -m "QC: PASS - meets specifications"
  else
    mus tag "$sample" -m "QC: FAIL - $result"
  fi
done

# Generate QC report
python generate_qc_report.py batch_001/ > qc_report_batch_001.pdf

# Upload report to ELN
mus eln upload qc_report_batch_001.pdf -m "QC report for batch 001"

# Log summary
passed=$(grep "PASS" logs/batch_001.log | wc -l)
failed=$(grep "FAIL" logs/batch_001.log | wc -l)
mus log -E "Batch 001 QC complete: ${passed} passed, ${failed} failed"

# Archive approved samples
mus irods upload batch_001/*_PASS.dat -m "Batch 001: approved samples"
```

### Query QC History

```bash
# Find all QC failures
mus search --type tag | grep "QC: FAIL"

# Find specific sample history
mus file sample_042.dat

# Monthly QC summary
mus search --type log | grep "QC complete" | tail -30
```

## See Also

- [Quick Start Guide](quickstart.md) - Basic workflows
- [CLI Reference](cli-reference.md) - All commands
- [ELN Plugin Guide](eln-plugin.md) - ELN features
- [iRODS Plugin Guide](irods-plugin.md) - iRODS features

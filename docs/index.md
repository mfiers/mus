# mus Documentation

**Version 0.5.56**

mus (Mark's utilities) is a Python CLI tool for research data management that provides file tracking, metadata management, and seamless integration with ELabJournal (ELN) and iRODS institutional data storage.

## Quick Links

- [Installation Guide](installation.md)
- [Quick Start](quickstart.md)
- [Core Concepts](concepts.md)
- [CLI Reference](cli-reference.md)
- [Configuration Guide](configuration.md)
- [ELN Plugin Guide](eln-plugin.md)
- [iRODS Plugin Guide](irods-plugin.md)
- [Developer Guide](developer-guide.md)
- [Workflow Examples](workflows.md)

## What is mus?

mus is designed for researchers who need to:

- **Track data provenance** with automatic checksums and metadata
- **Integrate with Electronic Lab Notebooks** (ELabJournal)
- **Archive data to institutional storage** (iRODS/Mango)
- **Maintain data integrity** with verification and audit trails
- **Collaborate** with proper permissions and sharing

## Key Features

### 🗄️ Local Database Tracking
- SQLite database tracks all file operations
- SHA256 checksums with intelligent caching
- Full history of tags, uploads, and modifications
- Parent-child relationships between operations

### 📓 ELabJournal Integration
- Link folders to ELN experiments
- Upload files with metadata
- Auto-convert Jupyter notebooks to PDF
- Post log entries directly to experiments

### 🏛️ iRODS/Mango Integration
- Upload to institutional iRODS storage
- Automatic metadata application (Mango schema)
- Checksum verification (local ↔ remote)
- Generate `.mango` files for tracking

### ⚙️ Flexible Configuration
- Hierarchical `.env` files cascade up directory tree
- Secure secret storage via keyring
- Per-project settings
- Environment variable overrides

### 🔌 Plugin Architecture
- Hook-based plugin system
- Easy to extend functionality
- Clean separation of concerns

## System Requirements

- Python 3.8 or higher
- SQLite 3
- (Optional) iRODS iCommands for iRODS integration
- (Optional) pandoc and texlive-xetex for Jupyter notebook conversion

## Installation

Quick install with pipx (recommended):

```bash
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"
```

See the [Installation Guide](installation.md) for detailed instructions.

## Quick Example

```bash
# Tag a folder with ELN experiment
mus eln tag-folder -x 12345

# Tag local files
mus tag data.csv results.txt -m "Initial analysis results"

# Upload to iRODS and ELN
mus irods upload analysis.ipynb -m "Final analysis notebook"

# Check integrity
mus irods check analysis.ipynb.mango

# Find file history
mus file data.csv
```

## Getting Help

```bash
# General help
mus --help

# Command-specific help
mus tag --help
mus eln --help
mus irods --help

# Version info
mus version
```

## Support

- **Issues**: [GitHub Issues](https://github.com/mfiers/mus/issues)
- **Source**: [GitHub Repository](https://github.com/mfiers/mus)

## License

MIT License - see LICENSE file for details.

---

**Next Steps:**
- New user? Start with the [Quick Start Guide](quickstart.md)
- Setting up? Read the [Installation Guide](installation.md)
- Want to understand how it works? Check [Core Concepts](concepts.md)
- Need command details? See the [CLI Reference](cli-reference.md)

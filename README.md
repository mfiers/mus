# mus

**Research data management CLI tool**

mus (Mark's utilities) is a Python CLI tool for research data management that provides file tracking, metadata management, and seamless integration with ELabJournal (ELN) and iRODS institutional data storage.

**Version**: 0.5.56
**Python**: 3.8+
**License**: MIT

## Features

- **Local Database Tracking** - SQLite database tracks all file operations with SHA256 checksums
- **ELabJournal Integration** - Link folders to experiments, upload files, auto-convert Jupyter notebooks
- **iRODS/Mango Integration** - Archive data with metadata, checksum verification, `.mango` tracking files
- **Hierarchical Configuration** - `.env` files cascade up directory tree for flexible project settings
- **Plugin Architecture** - Hook-based system for easy extension
- **Data Integrity** - SHA256 checksums throughout with intelligent caching

## Quick Start

### Installation

```bash
# Install with pipx (recommended)
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"

# Or with pip
pip install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"
```

### Basic Usage

```bash
# Tag a file
mus tag data.csv -m "Raw data from experiment"

# Upload to ELN
mus eln tag-folder -x 12345  # Link to experiment
mus eln upload results.txt -m "Analysis results"

# Upload to iRODS (requires ELN)
mus irods upload dataset.tar.gz -m "Complete dataset"

# Verify integrity
mus irods check dataset.tar.gz.mango

# Search history
mus search
mus file data.csv
```

## Documentation

**Full documentation available in [docs/](docs/)**

### Getting Started
- **[Installation Guide](docs/installation.md)** - Detailed installation instructions
- **[Quick Start](docs/quickstart.md)** - Get up and running in 10 minutes
- **[Core Concepts](docs/concepts.md)** - Understand how mus works

### User Guides
- **[CLI Reference](docs/cli-reference.md)** - Complete command reference
- **[Configuration Guide](docs/configuration.md)** - Configure mus for your environment
- **[Workflow Examples](docs/workflows.md)** - Real-world usage examples

### Plugin Guides
- **[ELN Plugin](docs/eln-plugin.md)** - ELabJournal integration guide
- **[iRODS Plugin](docs/irods-plugin.md)** - iRODS/Mango integration guide

### Advanced
- **[Developer Guide](docs/developer-guide.md)** - Contributing and plugin development

## Quick Reference

### Basic Commands

```bash
# Version
mus version

# Tag files
mus tag FILE... -m "Message"

# Log entry
mus log "Message"

# Search history
mus search
mus search --type tag --user alice

# File info
mus file data.csv
```

### ELN Commands

```bash
# Configure (once)
mus config secret-set eln_apikey "YOUR_KEY"
mus config secret-set eln_url "https://your-eln.com"

# Link folder to experiment
mus eln tag-folder -x EXPERIMENT_ID

# Upload files
mus eln upload FILE... -m "Message"

# Log to ELN
mus log -E "Message"
```

### iRODS Commands

```bash
# Configure (once)
mus config secret-set irods_home "/zone/home/user"
mus config secret-set irods_web "https://mango.site.com/data-object/view"
mus config secret-set irods_group "your_group"

# Upload (requires ELN setup)
mus irods upload FILE... -m "Message"

# Verify integrity
mus irods check FILE.mango

# Download
mus irods get FILE.mango
```

## System Requirements

- Python 3.8+
- SQLite 3
- (Optional) iRODS iCommands for iRODS integration
- (Optional) pandoc and texlive-xetex for Jupyter notebook conversion

## Development

### Development Install

```bash
git clone git@github.com:mfiers/mus.git
cd mus
pipx install -e .[all]
```

### Running Tests

```bash
pytest test/
```

Current test status: **93 tests, 100% passing** ✅

See [Developer Guide](docs/developer-guide.md) for details.

## Support

- **Documentation**: [docs/](docs/)
- **Issues**: [GitHub Issues](https://github.com/mfiers/mus/issues)
- **Source**: [GitHub Repository](https://github.com/mfiers/mus)

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Citation

If you use mus in your research, please cite:

```
mus: Research Data Management CLI Tool
https://github.com/mfiers/mus
```

---

**New to mus?** Start with the [Quick Start Guide](docs/quickstart.md)


# mus Documentation

Welcome to the mus documentation!

## Documentation Structure

### Getting Started
- **[Installation Guide](installation.md)** - Install mus on your system
- **[Quick Start](quickstart.md)** - Get up and running in 10 minutes
- **[Core Concepts](concepts.md)** - Understand how mus works

### User Guides
- **[CLI Reference](cli-reference.md)** - Complete command reference
- **[Configuration Guide](configuration.md)** - Configure mus for your environment
- **[Workflow Examples](workflows.md)** - Real-world usage examples

### Plugin Guides
- **[ELN Plugin](eln-plugin.md)** - ELabJournal integration
- **[iRODS Plugin](irods-plugin.md)** - iRODS/Mango integration

### Advanced
- **[Developer Guide](developer-guide.md)** - Contributing and plugin development

## Quick Links

### New Users
1. [Install mus](installation.md)
2. [Quick Start Tutorial](quickstart.md)
3. [Learn Core Concepts](concepts.md)

### Setting Up Plugins
- [ELN Setup](eln-plugin.md#configuration)
- [iRODS Setup](irods-plugin.md#configuration)

### Common Tasks
- [Tag files](cli-reference.md#mus-tag)
- [Upload to ELN](eln-plugin.md#upload-files)
- [Upload to iRODS](irods-plugin.md#upload-to-irods)
- [Search history](cli-reference.md#mus-search)
- [Check file integrity](irods-plugin.md#check-file-integrity)

### Examples
- [Daily Research Workflow](workflows.md#daily-research-workflow)
- [Computational Pipeline](workflows.md#computational-analysis-pipeline)
- [Team Collaboration](workflows.md#multi-user-collaboration)
- [Data Publication](workflows.md#data-publication-workflow)

## Documentation Format

All documentation is in Markdown format and can be:
- Read on GitHub
- Converted to HTML with tools like MkDocs or Sphinx
- Read directly in any text editor
- Printed as PDF

## Contributing to Documentation

Found an error or want to improve the docs?

1. Fork the repository
2. Edit the Markdown files in `docs/`
3. Submit a pull request

See the [Developer Guide](developer-guide.md#contributing) for details.

## Getting Help

- **Issues**: [GitHub Issues](https://github.com/mfiers/mus/issues)
- **Questions**: Use GitHub Discussions
- **In-app help**: `mus --help` or `mus COMMAND --help`

## Documentation Version

This documentation is for **mus version 0.5.56**.

Check your version: `mus version`

---

**Start here**: [Installation Guide](installation.md) → [Quick Start](quickstart.md) → [Your first workflow](workflows.md)

# Installation Guide

## Prerequisites

### Python Version
mus requires **Python 3.8 or higher**. Check your Python version:

```bash
python3 --version
```

### System Dependencies

#### For All Users
- **SQLite 3** (usually included with Python)

#### For ELN Plugin (optional)
If you want to convert Jupyter notebooks to PDF:
```bash
# Ubuntu/Debian
sudo apt-get install pandoc texlive-xetex

# macOS
brew install pandoc basictex
```

#### For iRODS Plugin (optional)
- **iRODS iCommands**: Required for iRODS integration
  - See [iRODS Documentation](https://docs.irods.org/master/icommands/user/)

**macOS Users**: iCommands can run via Docker (see [Docker Setup](#macos-docker-setup))

## Installation Methods

### Method 1: pipx (Recommended)

pipx installs Python applications in isolated environments:

```bash
# Install pipx if you don't have it
python3 -m pip install --user pipx
python3 -m pipx ensurepath

# Install mus with all plugins
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"
```

#### Plugin Options

Install with specific plugins only:

```bash
# Core only (no ELN or iRODS)
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus"

# With ELN plugin only
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[eln]"

# With iRODS plugin only
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[irods]"

# With development tools
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[dev]"

# With everything
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"
```

#### Upgrading

```bash
# Upgrade to latest version
pipx upgrade mus

# Upgrade and change plugin options
pipx upgrade "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"
```

### Method 2: Development Installation

For contributors or if you want to modify the code:

```bash
# Clone the repository
git clone git@github.com:mfiers/mus.git
cd mus

# Install in editable mode with all plugins
pipx install -e .[all]

# Or use pip if you prefer
pip install -e .[all]
```

### Method 3: pip (Not Recommended)

If you must use pip directly:

```bash
pip install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"
```

⚠️ **Warning**: This installs into your global Python environment and may cause conflicts.

## Post-Installation Setup

### 1. Verify Installation

```bash
mus version
```

Expected output: `0.5.56` (or current version)

### 2. Configure Keyring

mus uses [keyring](https://github.com/jaraco/keyring) to store secrets securely.

#### Linux Headless Server Setup

On headless Linux servers, set a keyring password:

```bash
# Add to ~/.bashrc or ~/.zshrc
export KEYRING_CRYPTFILE_PASSWORD="your-secure-password"
```

**Security Note**: Choose a strong password and protect your shell configuration file.

### 3. Test Basic Functionality

```bash
# Show help
mus --help

# Try a simple command
mus log "mus installation successful"
```

## Platform-Specific Instructions

### macOS Docker Setup

iRODS iCommands on macOS can be run via Docker:

```bash
# Set up Docker-based icommands
mus config secret-set icmd_prefix "docker run --platform linux/amd64 -i --rm -v $HOME:$HOME -v $HOME/.irods/:/root/.irods ghcr.io/utrechtuniversity/docker_icommands:0.2"
```

This tells mus to run all iRODS commands inside a Docker container.

### Linux (Ubuntu/Debian)

Standard installation should work without issues. For iRODS:

```bash
# Add iRODS repository and install icommands
wget -qO - https://packages.irods.org/irods-signing-key.asc | sudo apt-key add -
echo "deb [arch=amd64] https://packages.irods.org/apt/ $(lsb_release -sc) main" | sudo tee /etc/apt/sources.list.d/renci-irods.list
sudo apt-get update
sudo apt-get install irods-icommands
```

### Windows

Windows support is not officially tested. Consider using:
- **WSL2** (Windows Subsystem for Linux) with the Linux instructions
- **Docker Desktop** for iRODS commands

## Troubleshooting

### "Command not found: mus"

The pipx binary directory is not in your PATH. Run:

```bash
pipx ensurepath
```

Then restart your shell or run:

```bash
source ~/.bashrc  # or ~/.zshrc
```

### "No module named 'mus'"

If installed with pip, you may have a Python version conflict:

```bash
python3 -m pip install --upgrade "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"
```

### Keyring Errors

If you see keyring-related errors:

```bash
# Check keyring backend
python3 -c "import keyring; print(keyring.get_keyring())"

# Set environment variable to use plaintext keyring (less secure)
export PYTHON_KEYRING_BACKEND=keyring.backends.null.Keyring
```

### Import Errors for Optional Dependencies

If you see import errors for `nbconvert`, `ipython`, `python-irodsclient`, etc.:

```bash
# Reinstall with the correct plugin options
pipx reinstall "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"
```

### Permission Errors

If you get permission errors when running mus:

```bash
# Check database directory permissions
ls -la ~/.local/mus/

# Fix if needed
chmod 700 ~/.local/mus/
chmod 600 ~/.local/mus/mus.db
```

## Uninstallation

### With pipx:
```bash
pipx uninstall mus
```

### With pip:
```bash
pip uninstall mus
```

### Removing Data

mus stores data in:
- **Database**: `~/.local/mus/mus.db`
- **Secrets**: System keyring (location varies by platform)
- **Config**: `.env` files in your project directories

To completely remove all data:

```bash
# Remove database
rm -rf ~/.local/mus/

# Remove secrets (requires manual keyring cleanup)
# On Linux with PlaintextKeyring:
rm ~/.local/share/python_keyring/keyring_pass.cfg

# .env files must be removed manually from your projects
```

## Next Steps

- [Quick Start Guide](quickstart.md) - Get started with basic usage
- [Configuration Guide](configuration.md) - Set up ELN and iRODS
- [Core Concepts](concepts.md) - Understand how mus works

## Getting Help

If you encounter issues not covered here:

1. Check the [GitHub Issues](https://github.com/mfiers/mus/issues)
2. Open a new issue with:
   - Your operating system
   - Python version (`python3 --version`)
   - mus version (`mus version`)
   - Full error message
   - Steps to reproduce

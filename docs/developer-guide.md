# Developer Guide

Guide for developers who want to contribute to mus or create plugins.

## Table of Contents

- [Development Setup](#development-setup)
- [Project Structure](#project-structure)
- [Running Tests](#running-tests)
- [Code Style](#code-style)
- [Plugin Development](#plugin-development)
- [Core Architecture](#core-architecture)
- [Contributing](#contributing)

## Development Setup

### Prerequisites

- Python 3.8+
- Git
- (Optional) pytest for testing

### Clone and Install

```bash
# Clone repository
git clone git@github.com:mfiers/mus.git
cd mus

# Install in development mode with all dependencies
pipx install -e .[all]

# Or with pip
pip install -e .[all]

# Install development dependencies
pip install -e .[dev]
```

### Verify Installation

```bash
# Check mus is installed from your local directory
mus version
which mus

# Run tests
pytest test/
```

## Project Structure

```
mus/
├── src/mus/                 # Main source code
│   ├── __init__.py
│   ├── __about__.py         # Version info
│   ├── cli/                 # CLI commands
│   │   ├── __init__.py      # Main CLI entry point
│   │   ├── config.py        # Configuration commands
│   │   ├── db.py            # Database commands
│   │   ├── files.py         # File operations
│   │   ├── log.py           # Logging commands
│   │   ├── macro.py         # Macro system
│   │   └── search.py        # Search commands
│   ├── plugins/             # Plugin system
│   │   ├── __init__.py
│   │   ├── eln/             # ELabJournal plugin
│   │   │   ├── __init__.py
│   │   │   └── util.py
│   │   ├── irods/           # iRODS plugin
│   │   │   ├── __init__.py
│   │   │   ├── util.py
│   │   │   └── data/        # Mango schema data
│   │   ├── history/         # History plugin (deprecated)
│   │   └── iotracker.py     # I/O tracker
│   ├── util/                # Utility modules
│   │   ├── __init__.py
│   │   ├── cli.py           # CLI utilities
│   │   ├── files.py         # File utilities
│   │   └── log.py           # Logging utilities
│   ├── config.py            # Configuration management
│   ├── db.py                # Database operations
│   ├── exceptions.py        # Custom exceptions
│   ├── hooks.py             # Hook system
│   └── macro/               # Macro system
├── test/                    # Test suite
│   ├── conftest.py          # Shared fixtures
│   ├── test_*.py            # Test files
│   └── data/                # Test data
├── docs/                    # Documentation
├── pyproject.toml           # Package configuration
├── pytest.ini               # Test configuration
└── README.md                # Main README
```

## Running Tests

### Basic Testing

```bash
# Run all tests
pytest test/

# Run with verbose output
pytest test/ -v

# Run specific test file
pytest test/test_db.py

# Run specific test
pytest test/test_db.py::test_record_creation

# Run tests matching pattern
pytest test/ -k "checksum"
```

### Test Coverage

```bash
# Install coverage tool
pip install pytest-cov

# Run with coverage
pytest test/ --cov=mus --cov-report=html

# View coverage report
open htmlcov/index.html
```

### Current Test Status

- **93 tests, 100% passing** ✅
- Test execution time: ~2.8-3.1 seconds
- Components covered:
  - Database operations
  - Hook system
  - File utilities
  - Configuration management
  - CLI commands
  - ELN utilities
  - Exception handling

## Code Style

### Python Style Guide

Follow PEP 8 with these specifics:

- **Indentation**: 4 spaces
- **Line length**: 88 characters (Black default)
- **Imports**: Standard library, third-party, local (separated by blank lines)
- **Docstrings**: Use for public functions and classes
- **Type hints**: Encouraged but not required

### Example

```python
import logging
from pathlib import Path
from typing import Optional

import click

from mus.config import get_env
from mus.db import Record, get_db_connection


lg = logging.getLogger(__name__)


def process_file(filename: Path, message: Optional[str] = None) -> Record:
    """
    Process a file and create a database record.

    Args:
        filename: Path to file to process
        message: Optional message to attach

    Returns:
        Created Record object

    Raises:
        FileNotFoundError: If file doesn't exist
    """
    if not filename.exists():
        raise FileNotFoundError(f"File not found: {filename}")

    rec = Record()
    rec.prepare(filename=filename, rectype='tag')
    if message:
        rec.message = message
    rec.save()

    return rec
```

### Linting

```bash
# Install linting tools
pip install flake8 black isort

# Check style
flake8 src/mus/

# Auto-format
black src/mus/
isort src/mus/
```

## Plugin Development

### Plugin Structure

Plugins are Python modules that register hooks and add commands.

**Basic plugin template:**

```python
# src/mus/plugins/myplugin/__init__.py

import logging
import click
from mus.hooks import register_hook

lg = logging.getLogger(__name__)


# Define your CLI command group
@click.group("myplugin")
def cmd_myplugin():
    """My plugin commands"""
    pass


@cmd_myplugin.command("hello")
def hello():
    """Say hello"""
    click.echo("Hello from myplugin!")


# Hook: Add commands to main CLI
def init_myplugin(cli):
    """Initialize plugin - add commands to CLI"""
    cli.add_command(cmd_myplugin)
    lg.info("myplugin initialized")


# Hook: Process records before save
def process_record(record):
    """Add metadata to records"""
    env = get_env()
    if 'myplugin_enabled' in env:
        record.data['myplugin'] = 'processed'


# Register hooks
register_hook('plugin_init', init_myplugin)
register_hook('prepare_record', process_record)
```

### Available Hooks

#### `plugin_init`

Called during startup. Use to register commands and add options.

**Parameters:**
- `cli`: Click CLI object

**Example:**

```python
def init_plugin(cli):
    # Add command group
    cli.add_command(my_command_group)

    # Add option to existing command
    from mus.cli import files
    files.filetag.params.append(
        click.Option(['--my-option'], is_flag=True, help='My option'))

register_hook('plugin_init', init_plugin)
```

#### `prepare_record`

Called before a record is saved. Use to add metadata.

**Parameters:**
- `record`: Record object (can be modified)

**Example:**

```python
def add_metadata(record):
    from mus.config import get_env
    env = get_env()

    # Add custom metadata
    if 'project_id' in env:
        record.data['project_id'] = env['project_id']

register_hook('prepare_record', add_metadata)
```

#### `save_record`

Called during record save. Use to react to operations.

**Parameters:**
- `record`: Record object (read-only at this point)

**Example:**

```python
def on_save(record):
    import click
    ctx = click.get_current_context()

    # Check if plugin should process
    if not ctx.params.get('my_option'):
        return

    # Do something with the record
    if record.filename:
        print(f"Processing: {record.filename}")
        # ... custom logic ...

register_hook('save_record', on_save)
```

#### `finish_filetag`

Called after file tagging completes. Use for post-processing.

**Parameters:**
- `message`: User-provided message

**Example:**

```python
def finish_processing(message):
    import click
    ctx = click.get_current_context()

    if not ctx.params.get('my_option'):
        return

    # Access tagged files from context or database
    click.echo(f"Post-processing complete: {message}")

register_hook('finish_filetag', finish_processing, priority=5)
```

### Hook Priority

Hooks execute in priority order (highest first):

```python
# This runs first
register_hook('prepare_record', important_func, priority=20)

# This runs second
register_hook('prepare_record', normal_func, priority=10)

# This runs last
register_hook('prepare_record', cleanup_func, priority=1)
```

### Accessing Click Context

Many plugins need access to CLI options:

```python
def my_hook(record):
    import click

    try:
        ctx = click.get_current_context()
        if ctx.params.get('my_option'):
            # Option was provided
            pass
    except RuntimeError:
        # No context (e.g., during testing)
        pass
```

### Plugin Configuration

Add plugin-specific configuration:

```python
def init_plugin(cli):
    from mus.cli import files

    # Add plugin-specific option
    files.filetag.params.append(
        click.Option(
            ['--upload-to-service'],
            is_flag=True,
            help='Upload to my service'))

    files.filetag.params.append(
        click.Option(
            ['--service-id'],
            type=int,
            help='Service ID'))
```

### Plugin Testing

Create tests for your plugin:

```python
# test/test_myplugin.py

import pytest
from click.testing import CliRunner
from mus.cli import cli


def test_myplugin_command():
    runner = CliRunner()
    result = runner.invoke(cli, ['myplugin', 'hello'])

    assert result.exit_code == 0
    assert 'Hello' in result.output


def test_myplugin_hook(temp_db, clean_hooks, mock_click_context):
    from mus.db import Record
    from mus.hooks import call_hook

    # Your hook should be registered
    rec = Record()
    rec.prepare(rectype='tag', message='Test')

    call_hook('prepare_record', record=rec)

    # Check your metadata was added
    assert 'myplugin' in rec.data
```

## Core Architecture

### Database Layer (db.py)

**Key classes:**
- `Record`: Dataclass representing operations
- `get_db_connection()`: Returns SQLite connection
- `get_db_path()`: Returns database path

**Adding new record fields:**

1. Modify `muslog` table schema in `init_muslog_table()`
2. Update `Record` dataclass
3. Update `record_factory()` if needed
4. Add migration (for existing databases)

### Configuration Layer (config.py)

**Key functions:**
- `get_env()`: Get merged configuration
- `get_secret()`: Get secret from keyring
- `save_kv_to_local_config()`: Update `.env` file

**Adding new configuration keys:**

1. For list keys: Add to `LIST_KEYS` in config.py
2. Document in [Configuration Guide](configuration.md)
3. Add tests

### Hook System (hooks.py)

**Implementation:**

```python
HOOKS = defaultdict(list)  # Global hook registry

def register_hook(name, func, priority=10):
    HOOKS[name].append((priority, func))

def call_hook(name, **kwargs):
    for priority, func in sorted(HOOKS[name], key=lambda x: -x[0]):
        func(**kwargs)
```

Simple but effective for plugin system.

### CLI Layer (cli/__init__.py)

**Entry point:**

```python
@click.group(cls=AliasedGroup)
@click.option('-v', '--verbose', count=True)
def cli(ctx, verbose):
    # Setup logging
    if verbose == 1:
        lg.setLevel(logging.INFO)
    elif verbose > 1:
        lg.setLevel(logging.DEBUG)

# Add commands
cli.add_command(search.cmd_search)
cli.add_command(files.filetag)
# ... etc

# Load plugins
import mus.plugins.eln
import mus.plugins.irods

# Initialize plugins
call_hook('plugin_init', cli=cli)
```

## Contributing

### Workflow

1. **Fork the repository**

```bash
# Fork on GitHub, then clone your fork
git clone git@github.com:YOUR_USERNAME/mus.git
cd mus
git remote add upstream git@github.com:mfiers/mus.git
```

2. **Create a branch**

```bash
git checkout -b feature/my-new-feature
# or
git checkout -b fix/bug-description
```

3. **Make changes**

```bash
# Edit code
# Add tests
# Update documentation
```

4. **Run tests**

```bash
pytest test/
```

5. **Commit**

```bash
git add .
git commit -m "Add feature: description"
```

6. **Push and create PR**

```bash
git push origin feature/my-new-feature
# Create pull request on GitHub
```

### Commit Message Format

```
<type>: <short description>

<longer description if needed>

<footer>
```

**Types:**
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation
- `test`: Tests
- `refactor`: Code refactoring
- `style`: Code style (formatting)
- `chore`: Maintenance

**Examples:**

```
feat: add support for checksum verification in uploads

Implements SHA256 verification between local files and iRODS.
Adds 'mus irods check' command to verify integrity.

Closes #42
```

```
fix: handle missing .env files gracefully

Previously crashed with FileNotFoundError when .env missing.
Now returns empty dict as expected.

Fixes #123
```

### Pull Request Guidelines

- **Clear description**: Explain what and why
- **Tests included**: Add tests for new features
- **Documentation updated**: Update relevant docs
- **One feature per PR**: Keep PRs focused
- **Pass CI**: Ensure tests pass

### Code Review

PRs are reviewed for:
- Correctness
- Test coverage
- Documentation
- Code style
- Performance
- Security

## Debugging

### Enable Debug Logging

```bash
# Debug mode
mus -vv tag file.txt -m "Test"

# Shows detailed logs including:
# - Hook execution
# - Database queries
# - API calls
# - File operations
```

### Interactive Debugging

```python
# Add breakpoint in code
import pdb; pdb.set_trace()

# Or use IPython
import IPython; IPython.embed()

# Then run normally
mus tag file.txt -m "Test"
```

### Database Inspection

```bash
# Open database
sqlite3 ~/.local/mus/mus.db

# Inspect schema
.schema muslog
.schema hashcache

# Query records
SELECT * FROM muslog ORDER BY time DESC LIMIT 10;

# Check hooks (not in DB, but can add logging)
```

### Testing Hooks

```python
# test/test_my_feature.py

from mus.hooks import HOOKS, call_hook


def test_hook_registered():
    """Check hook is registered"""
    assert 'prepare_record' in HOOKS
    hook_names = [func.__name__ for _, func in HOOKS['prepare_record']]
    assert 'my_hook_function' in hook_names


def test_hook_execution(clean_hooks):
    """Test hook execution"""
    called = []

    def test_hook(**kwargs):
        called.append(kwargs)

    from mus.hooks import register_hook
    register_hook('test_hook', test_hook)

    call_hook('test_hook', data='test')

    assert len(called) == 1
    assert called[0]['data'] == 'test'
```

## Resources

### Internal Documentation

- [Core Concepts](concepts.md) - Architecture overview
- [API Reference](api-reference.md) - (To be created)
- Code comments and docstrings

### External Resources

- [Click Documentation](https://click.palletsprojects.com/)
- [SQLite Documentation](https://www.sqlite.org/docs.html)
- [Python Keyring](https://github.com/jaraco/keyring)
- [iRODS](https://irods.org/)
- [ELabJournal API](https://www.elabjournal.com/doc/API.html)

### Community

- **GitHub Issues**: https://github.com/mfiers/mus/issues
- **Discussions**: Use GitHub Discussions for questions

## Release Process

(For maintainers)

### Version Bump

```bash
# Edit src/mus/__about__.py
__version__ = "0.5.57"

# Commit
git add src/mus/__about__.py
git commit -m "version bump"
git push
```

### Tagging

```bash
git tag v0.5.57
git push --tags
```

### Distribution

```bash
# Build package
python -m build

# Upload to PyPI (if configured)
twine upload dist/*
```

## See Also

- [Core Concepts](concepts.md) - Understand the architecture
- [Plugin Examples](../src/mus/plugins/) - Study existing plugins
- [Test Suite](../test/) - Learn from tests

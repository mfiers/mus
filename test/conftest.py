"""
Pytest configuration and shared fixtures
"""
import tempfile
from pathlib import Path
from unittest.mock import Mock, patch

import pytest
import click


@pytest.fixture
def temp_db(monkeypatch, tmp_path):
    """
    Create a temporary database for testing.

    This fixture ensures each test uses an isolated database
    to prevent test interference.
    """
    db_path = tmp_path / "test_mus.db"

    def mock_get_db_path():
        return str(db_path)

    monkeypatch.setattr("mus.db.get_db_path", mock_get_db_path)
    return db_path


@pytest.fixture
def temp_files(tmp_path):
    """
    Create a set of temporary test files.

    Returns a dictionary with paths to various test files.
    """
    files = {}

    # Create text files
    files['text1'] = tmp_path / "test1.txt"
    files['text1'].write_text("Hello, World!")

    files['text2'] = tmp_path / "test2.txt"
    files['text2'].write_text("Another test file")

    # Create empty file
    files['empty'] = tmp_path / "empty.txt"
    files['empty'].write_text("")

    # Create binary file
    files['binary'] = tmp_path / "binary.bin"
    files['binary'].write_bytes(bytes(range(256)))

    # Create a subdirectory with files
    subdir = tmp_path / "subdir"
    subdir.mkdir()
    files['subdir'] = subdir

    files['nested'] = subdir / "nested.txt"
    files['nested'].write_text("Nested file content")

    return files


@pytest.fixture
def temp_env_setup(tmp_path, monkeypatch):
    """
    Create a temporary directory structure with .env files
    for testing configuration loading.
    """
    # Create directory structure
    root = tmp_path / "project"
    root.mkdir()

    subfolder = root / "subfolder"
    subfolder.mkdir()

    # Create root .env
    root_env = root / ".env"
    root_env.write_text("root_var=root_value\ntag=tag1, tag2\n")

    # Create subfolder .env
    sub_env = subfolder / ".env"
    sub_env.write_text("sub_var=sub_value\ntag=-tag1, tag3\n")

    # Change to subfolder for testing
    monkeypatch.chdir(subfolder)

    return {
        'root': root,
        'subfolder': subfolder,
        'root_env': root_env,
        'sub_env': sub_env
    }


@pytest.fixture
def mock_eln_config(monkeypatch):
    """
    Mock ELN configuration secrets.
    """
    def mock_get_secret(name, hint=None):
        secrets = {
            'eln_apikey': 'test_api_key',
            'eln_url': 'https://test.elab.example.com/api',
            'irods_home': '/test/irods/home',
            'irods_group': 'test_group',
            'irods_web': 'https://test.mango.example.com',
        }
        return secrets.get(name, 'mock_secret')

    monkeypatch.setattr("mus.config.get_secret", mock_get_secret)
    monkeypatch.setattr("mus.plugins.eln.util.get_secret", mock_get_secret)


@pytest.fixture
def mock_env(monkeypatch):
    """
    Mock environment configuration.
    """
    def mock_get_env(wd=None):
        return {
            'eln_experiment_id': '12345',
            'eln_experiment_name': 'Test Experiment',
            'eln_project_id': '678',
            'eln_project_name': 'Test Project',
            'eln_study_id': '910',
            'eln_study_name': 'Test Study',
            'eln_collaborator': ['John Doe', 'Jane Smith'],
        }

    monkeypatch.setattr("mus.config.get_env", mock_get_env)
    return mock_get_env


@pytest.fixture
def clean_hooks():
    """
    Clear the hooks registry before and after each test.

    This ensures hooks registered in one test don't affect others.
    """
    from mus.hooks import HOOKS

    HOOKS.clear()
    yield
    HOOKS.clear()


@pytest.fixture
def sample_record(temp_db):
    """
    Create a sample Record object for testing.
    """
    from mus.db import Record

    record = Record()
    record.prepare(
        rectype='test',
        message='Test record'
    )
    return record


@pytest.fixture
def sample_file_record(temp_db, tmp_path):
    """
    Create a sample Record object with a file for testing.
    """
    from mus.db import Record

    test_file = tmp_path / "test.txt"
    test_file.write_text("test content")

    record = Record()
    record.prepare(
        filename=test_file,
        rectype='tag',
        message='Test file record'
    )
    return record


@pytest.fixture
def cli_runner():
    """
    Create a Click CLI test runner.
    """
    from click.testing import CliRunner
    return CliRunner()


@pytest.fixture
def isolated_filesystem():
    """
    Create an isolated filesystem for CLI testing.
    """
    from click.testing import CliRunner

    runner = CliRunner()
    with runner.isolated_filesystem():
        yield Path.cwd()


@pytest.fixture(autouse=True)
def reset_lru_caches():
    """
    Clear LRU caches between tests to prevent cache pollution.
    """
    from mus.config import get_env, load_single_env

    get_env.cache_clear()
    load_single_env.cache_clear()

    yield

    get_env.cache_clear()
    load_single_env.cache_clear()


@pytest.fixture(autouse=True)
def mock_click_context(request):
    """
    Automatically provide a mock Click context for all tests.

    This prevents RuntimeError when ELN hooks try to access the context
    outside of a CLI invocation.

    Skip this fixture for CLI tests that use CliRunner.
    """
    # Skip for tests that use CliRunner (they provide their own context)
    if 'cli_runner' in request.fixturenames or 'runner' in request.fixturenames:
        yield None
        return

    mock_ctx = Mock(spec=click.Context)
    mock_ctx.params = {
        'eln': False,
        'irods': False,
        'eln_experimentid': None,
        'dry_run': False,
        'irods_force': False,
        'ignore_symlinks': False,
    }

    with patch('click.get_current_context', return_value=mock_ctx):
        yield mock_ctx


@pytest.fixture
def mock_irods_session(monkeypatch):
    """
    Mock iRODS session for testing without actual iRODS connection.
    """
    from unittest.mock import Mock

    mock_session = Mock()
    mock_collection = Mock()
    mock_session.collections.get.return_value = mock_collection

    def mock_get_irods_session():
        return mock_session

    monkeypatch.setattr(
        "mus.plugins.irods.util.get_irods_session",
        mock_get_irods_session
    )

    return mock_session

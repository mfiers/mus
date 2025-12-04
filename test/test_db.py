"""
Test database operations
"""
import os
import sqlite3
import tempfile
from pathlib import Path

import pytest

from mus.db import (
    Record,
    find_by_uid,
    get_db_connection,
    get_db_path,
    init_hashcache_table,
    init_muslog_table,
)


@pytest.fixture
def temp_db(monkeypatch, tmp_path):
    """Create a temporary database for testing"""
    db_path = tmp_path / "test_mus.db"

    def mock_get_db_path():
        return str(db_path)

    monkeypatch.setattr("mus.db.get_db_path", mock_get_db_path)
    return db_path


def test_get_db_path():
    """Test that database path is created correctly"""
    db_path = get_db_path()
    assert db_path.endswith('mus.db')
    assert '.local/mus' in db_path


def test_init_muslog_table(temp_db):
    """Test that muslog table is created correctly"""
    conn = sqlite3.connect(temp_db)
    init_muslog_table(conn)

    # Check that table exists
    cursor = conn.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name='muslog'"
    )
    result = cursor.fetchone()
    assert result is not None
    assert result[0] == 'muslog'

    # Check columns
    cursor = conn.execute("PRAGMA table_info(muslog)")
    columns = [row[1] for row in cursor.fetchall()]
    expected_columns = [
        'host', 'cwd', 'user', 'cl', 'time', 'type',
        'message', 'status', 'uid', 'filename', 'checksum',
        'child_of', 'data'
    ]
    assert columns == expected_columns
    conn.close()


def test_init_hashcache_table(temp_db):
    """Test that hashcache table is created correctly"""
    conn = sqlite3.connect(temp_db)
    init_hashcache_table(conn)

    # Check that table exists
    cursor = conn.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name='hashcache'"
    )
    result = cursor.fetchone()
    assert result is not None
    assert result[0] == 'hashcache'

    # Check columns
    cursor = conn.execute("PRAGMA table_info(hashcache)")
    columns = [row[1] for row in cursor.fetchall()]
    assert columns == ['filename', 'mtime', 'hash']

    # Check index exists
    cursor = conn.execute(
        "SELECT name FROM sqlite_master WHERE type='index' "
        "AND name='hashcache_filename'"
    )
    result = cursor.fetchone()
    assert result is not None
    conn.close()


def test_get_db_connection(temp_db):
    """Test getting a database connection"""
    conn = get_db_connection()
    assert conn is not None
    assert isinstance(conn, sqlite3.Connection)

    # Check that both tables were created
    # Note: We need to query with a different connection without the custom row_factory
    test_conn = sqlite3.connect(temp_db)
    cursor = test_conn.execute(
        "SELECT name FROM sqlite_master WHERE type='table'"
    )
    tables = [row[0] for row in cursor.fetchall()]
    assert 'muslog' in tables
    assert 'hashcache' in tables
    conn.close()
    test_conn.close()


def test_record_creation():
    """Test creating a Record object"""
    record = Record()
    assert record.data == {}
    assert record.status == 0


def test_record_prepare_with_file(temp_db, tmp_path):
    """Test preparing a record with a file"""
    # Create a test file
    test_file = tmp_path / "test_file.txt"
    test_file.write_text("test content")

    record = Record()
    record.prepare(
        filename=test_file,
        rectype='tag',
        message='Test message'
    )

    assert record.filename == str(test_file.resolve())
    assert record.checksum is not None
    assert len(record.checksum) == 64  # SHA256 hex length
    assert record.type == 'tag'
    assert record.message == 'Test message'
    assert record.uid is not None
    assert record.time is not None
    assert record.cwd is not None
    assert record.user is not None
    assert record.host is not None


def test_record_prepare_with_directory(temp_db, tmp_path):
    """Test preparing a record with a directory"""
    test_dir = tmp_path / "test_dir"
    test_dir.mkdir()

    record = Record()
    record.prepare(
        filename=test_dir,
        rectype='tag',
        message='Test directory'
    )

    assert record.filename == str(test_dir.resolve())
    assert record.checksum is None  # Directories don't have checksums
    assert record.type == 'tag'


def test_record_prepare_without_file(temp_db):
    """Test preparing a record without a file"""
    record = Record()
    record.prepare(
        filename=None,
        rectype='log',
        message='Test log'
    )

    assert record.filename is None
    assert record.checksum is None
    assert record.type == 'log'
    assert record.message == 'Test log'


def test_record_add_message(temp_db):
    """Test adding messages to a record"""
    record = Record()
    record.prepare(rectype='log')

    record.add_message("First message")
    assert record.message == "First message"

    record.add_message("Second message")
    assert record.message == "First message\nSecond message"


def test_record_save_and_retrieve(temp_db, tmp_path):
    """Test saving and retrieving a record"""
    test_file = tmp_path / "test_file.txt"
    test_file.write_text("test content")

    # Create and save a record
    record = Record()
    record.prepare(
        filename=test_file,
        rectype='tag',
        message='Test save'
    )
    original_uid = record.uid
    record.save()

    # Retrieve the record
    retrieved = find_by_uid(original_uid)
    assert retrieved is not None
    assert retrieved.uid == original_uid
    assert retrieved.message == 'Test save'
    assert retrieved.type == 'tag'
    assert retrieved.filename == str(test_file.resolve())


def test_record_save_with_data(temp_db, tmp_path):
    """Test saving a record with additional data"""
    test_file = tmp_path / "test_file.txt"
    test_file.write_text("test content")

    record = Record()
    record.prepare(
        filename=test_file,
        rectype='job',
        message='Test job'
    )
    record.data = {'runtime': 123.45, 'status': 'completed'}
    original_uid = record.uid
    record.save()

    # Retrieve and verify data
    retrieved = find_by_uid(original_uid)
    assert retrieved is not None
    assert retrieved.data == {'runtime': 123.45, 'status': 'completed'}


def test_record_with_child_of(temp_db, tmp_path):
    """Test creating a record with child_of relationship"""
    test_file = tmp_path / "test_file.txt"
    test_file.write_text("test content")

    # Create parent record
    parent = Record()
    parent.prepare(rectype='macro', message='Parent')
    parent_uid = parent.uid
    parent.save()

    # Create child record
    child = Record()
    child.prepare(
        filename=test_file,
        rectype='job',
        message='Child',
        child_of=parent_uid
    )
    child.save()

    # Verify relationship
    retrieved = find_by_uid(child.uid)
    assert retrieved.child_of == parent_uid


def test_find_by_uid_partial_match(temp_db, tmp_path):
    """Test finding a record by partial UID"""
    test_file = tmp_path / "test_file.txt"
    test_file.write_text("test content")

    record = Record()
    record.prepare(
        filename=test_file,
        rectype='tag',
        message='Test partial'
    )
    full_uid = record.uid
    record.save()

    # Find by first 6 characters
    partial_uid = full_uid[:6]
    retrieved = find_by_uid(partial_uid)
    assert retrieved is not None
    assert retrieved.uid == full_uid


def test_record_str_representation(temp_db, tmp_path):
    """Test string representation of records"""
    test_file = tmp_path / "test_file.txt"
    test_file.write_text("test content")

    record = Record()
    record.prepare(
        filename=test_file,
        rectype='tag',
        message='Test string'
    )

    str_repr = str(record)
    assert 'tag' in str_repr
    assert 'Test string' in str_repr
    assert record.host in str_repr

"""
Test file utility functions
"""
import hashlib
from pathlib import Path

import pytest

from mus.util.files import get_checksum


@pytest.fixture
def temp_db(monkeypatch, tmp_path):
    """Create a temporary database for testing"""
    db_path = tmp_path / "test_mus.db"

    def mock_get_db_path():
        return str(db_path)

    monkeypatch.setattr("mus.db.get_db_path", mock_get_db_path)
    return db_path


def test_get_checksum_basic(temp_db, tmp_path):
    """Test getting checksum of a file"""
    test_file = tmp_path / "test.txt"
    test_content = b"Hello, World!"
    test_file.write_bytes(test_content)

    checksum = get_checksum(test_file)

    # Calculate expected checksum
    expected = hashlib.sha256(test_content).hexdigest()

    assert checksum == expected
    assert len(checksum) == 64  # SHA256 hex length


def test_get_checksum_empty_file(temp_db, tmp_path):
    """Test getting checksum of an empty file"""
    test_file = tmp_path / "empty.txt"
    test_file.write_bytes(b"")

    checksum = get_checksum(test_file)

    # SHA256 of empty string
    expected = hashlib.sha256(b"").hexdigest()

    assert checksum == expected


def test_get_checksum_large_file(temp_db, tmp_path):
    """Test getting checksum of a large file"""
    test_file = tmp_path / "large.bin"

    # Create a file larger than the read block size (65536 bytes)
    content = b"A" * 100000

    test_file.write_bytes(content)

    checksum = get_checksum(test_file)

    # Calculate expected checksum
    expected = hashlib.sha256(content).hexdigest()

    assert checksum == expected


def test_get_checksum_binary_file(temp_db, tmp_path):
    """Test getting checksum of a binary file"""
    test_file = tmp_path / "binary.bin"
    content = bytes(range(256))
    test_file.write_bytes(content)

    checksum = get_checksum(test_file)

    expected = hashlib.sha256(content).hexdigest()

    assert checksum == expected


def test_get_checksum_caching(temp_db, tmp_path):
    """Test that checksums are cached based on mtime"""
    test_file = tmp_path / "cache_test.txt"
    test_file.write_text("original content")

    # First call - should calculate and cache
    checksum1 = get_checksum(test_file)

    # Second call without modifying file - should use cache
    checksum2 = get_checksum(test_file)

    assert checksum1 == checksum2


def test_get_checksum_cache_invalidation(temp_db, tmp_path):
    """Test that cache is invalidated when file is modified"""
    import time

    test_file = tmp_path / "cache_invalidate.txt"
    test_file.write_text("original content")

    checksum1 = get_checksum(test_file)

    # Wait to ensure mtime changes (filesystems have varying precision)
    time.sleep(1.1)

    # Modify the file
    test_file.write_text("modified content")

    checksum2 = get_checksum(test_file)

    # Checksums should be different
    assert checksum1 != checksum2, f"Checksums should differ: {checksum1} vs {checksum2}"


def test_get_checksum_nonexistent_file(temp_db, tmp_path):
    """Test that getting checksum of nonexistent file raises error"""
    test_file = tmp_path / "nonexistent.txt"

    with pytest.raises(FileExistsError):
        get_checksum(test_file)


def test_get_checksum_same_content_different_files(temp_db, tmp_path):
    """Test that files with same content have same checksum"""
    content = "identical content"

    file1 = tmp_path / "file1.txt"
    file2 = tmp_path / "file2.txt"

    file1.write_text(content)
    file2.write_text(content)

    checksum1 = get_checksum(file1)
    checksum2 = get_checksum(file2)

    assert checksum1 == checksum2


def test_get_checksum_unicode_content(temp_db, tmp_path):
    """Test getting checksum of file with unicode content"""
    test_file = tmp_path / "unicode.txt"
    content = "Hello 世界 🌍 café"
    test_file.write_text(content, encoding='utf-8')

    checksum = get_checksum(test_file)

    # Calculate expected checksum
    expected = hashlib.sha256(content.encode('utf-8')).hexdigest()

    assert checksum == expected


def test_get_checksum_with_newlines(temp_db, tmp_path):
    """Test getting checksum of file with various newline styles"""
    test_file = tmp_path / "newlines.txt"
    content = "line1\nline2\r\nline3\r"
    test_file.write_text(content, newline='')

    checksum = get_checksum(test_file)

    # Checksum should be deterministic
    assert len(checksum) == 64


def test_get_checksum_preserves_file_content(temp_db, tmp_path):
    """Test that calculating checksum doesn't modify the file"""
    test_file = tmp_path / "preserve.txt"
    original_content = "Don't modify me!"
    test_file.write_text(original_content)

    original_mtime = test_file.stat().st_mtime

    get_checksum(test_file)

    # Content should be unchanged
    assert test_file.read_text() == original_content


def test_get_checksum_known_values(temp_db, tmp_path):
    """Test checksum against known SHA256 values"""
    import time

    test_file = tmp_path / "known.txt"

    # Empty file
    test_file.write_bytes(b"")
    assert get_checksum(test_file) == \
        "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

    # Wait to ensure mtime changes so cache is invalidated
    time.sleep(1.1)

    # "hello"
    test_file.write_bytes(b"hello")
    assert get_checksum(test_file) == \
        "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"


def test_checksum_cache_in_database(temp_db, tmp_path):
    """Test that checksums are actually stored in the database"""
    from mus.db import get_db_connection

    test_file = tmp_path / "db_cache.txt"
    test_file.write_text("cache this")

    checksum = get_checksum(test_file)

    # Check database for cached entry
    conn = get_db_connection()
    cursor = conn.execute(
        "SELECT filename, hash FROM hashcache WHERE filename=?",
        (str(test_file.resolve()),)
    )
    result = cursor.fetchone()

    assert result is not None
    assert result.hash == checksum
    conn.close()

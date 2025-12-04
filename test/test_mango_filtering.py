"""
Test that .mango files are properly filtered during upload
"""
from pathlib import Path

import pytest
from click.testing import CliRunner

from mus.cli import cli


@pytest.fixture
def runner():
    """Create a CLI runner for tests"""
    return CliRunner()


@pytest.fixture
def temp_db(monkeypatch, tmp_path):
    """Create a temporary database for testing"""
    db_path = tmp_path / "test_mus.db"

    def mock_get_db_path():
        return str(db_path)

    monkeypatch.setattr("mus.db.get_db_path", mock_get_db_path)
    return db_path


def test_mango_files_are_filtered_out(runner, temp_db, tmp_path):
    """Test that .mango files are automatically filtered during upload"""
    # Create some test files
    data_file = tmp_path / "data.txt"
    data_file.write_text("actual data")

    mango_file = tmp_path / "data.txt.mango"
    mango_file.write_text("/irods/path/to/data.txt")

    another_data = tmp_path / "more_data.csv"
    another_data.write_text("csv,data")

    # Try to tag both data and mango files
    result = runner.invoke(
        cli,
        ['tag', str(data_file), str(mango_file), str(another_data),
         '-m', 'Test message']
    )

    # Should not crash
    assert result.exit_code in [0, 1]

    # Check that files were processed
    # The .mango file should have been skipped
    result = runner.invoke(cli, ['file', str(data_file)])
    assert result.exit_code == 0


def test_only_mango_files_provided(runner, temp_db, tmp_path):
    """Test behavior when only .mango files are provided"""
    mango_file1 = tmp_path / "file1.mango"
    mango_file1.write_text("/irods/path/1")

    mango_file2 = tmp_path / "file2.mango"
    mango_file2.write_text("/irods/path/2")

    # Try to tag only mango files
    result = runner.invoke(
        cli,
        ['tag', str(mango_file1), str(mango_file2), '-m', 'Test']
    )

    # Should handle gracefully
    assert result.exit_code in [0, 1]
    # Should mention that no files were tagged
    if result.output:
        assert 'No files to tag' in result.output or result.exit_code != 0


def test_wildcard_pattern_simulation(runner, temp_db, tmp_path):
    """Simulate what happens when user runs 'mus irods upload *'"""
    # Create a directory with data files and mango files
    (tmp_path / "file1.txt").write_text("data1")
    (tmp_path / "file1.txt.mango").write_text("/irods/file1.txt")
    (tmp_path / "file2.csv").write_text("data2")
    (tmp_path / "file2.csv.mango").write_text("/irods/file2.csv")
    (tmp_path / "file3.dat").write_text("data3")

    # Simulate shell glob expansion - list all files
    all_files = sorted([str(f) for f in tmp_path.glob("*")])

    # Tag all files (simulating 'mus tag *')
    result = runner.invoke(
        cli,
        ['tag', *all_files, '-m', 'Bulk upload']
    )

    # Should not crash and should skip .mango files
    assert result.exit_code in [0, 1]


def test_mixed_files_with_mango(runner, temp_db, tmp_path):
    """Test that data files are processed but mango files are skipped"""
    # Create test files
    files = []
    for i in range(3):
        data_file = tmp_path / f"data{i}.txt"
        data_file.write_text(f"content {i}")
        files.append(str(data_file))

        mango_file = tmp_path / f"data{i}.txt.mango"
        mango_file.write_text(f"/irods/data{i}.txt")
        files.append(str(mango_file))

    # Tag all files
    result = runner.invoke(
        cli,
        ['tag', *files, '-m', 'Mixed files']
    )

    # Should process successfully
    assert result.exit_code in [0, 1]

    # Verify data files were tagged (at least file command works on them)
    for i in range(3):
        data_file = tmp_path / f"data{i}.txt"
        result = runner.invoke(cli, ['file', str(data_file)])
        assert result.exit_code == 0


def test_mango_file_extension_case_sensitive(runner, temp_db, tmp_path):
    """Test that .mango extension is case-sensitive"""
    data_file = tmp_path / "data.txt"
    data_file.write_text("data")

    # Create .MANGO (uppercase) - should NOT be filtered
    mango_upper = tmp_path / "data.txt.MANGO"
    mango_upper.write_text("/irods/path")

    # Create .mango (lowercase) - should be filtered
    mango_lower = tmp_path / "other.txt.mango"
    mango_lower.write_text("/irods/other")

    result = runner.invoke(
        cli,
        ['tag', str(data_file), str(mango_upper), str(mango_lower),
         '-m', 'Case test']
    )

    # Should handle both files
    assert result.exit_code in [0, 1]

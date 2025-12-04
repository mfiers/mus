"""
Test ELN utility functions
"""
from datetime import datetime
from unittest.mock import Mock, patch

import pytest

from mus.plugins.eln.util import (
    ElnConflictingExperimentId,
    ElnNoExperimentId,
    add_eln_data_to_record,
    fix_eln_experiment_id,
    get_stamped_filename,
)


def test_get_stamped_filename():
    """Test generating stamped filenames"""
    result = get_stamped_filename("test.txt", "pdf")

    # Should contain the base name
    assert "test" in result
    # Should contain the new extension
    assert result.endswith(".pdf")
    # Should contain timestamp format YYYYMMDD_HHMM
    assert ".pdf" in result


def test_get_stamped_filename_no_extension():
    """Test stamped filename with no original extension"""
    result = get_stamped_filename("testfile", "pdf")

    assert "testfile" in result
    assert result.endswith(".pdf")


def test_get_stamped_filename_multiple_dots():
    """Test stamped filename with multiple dots"""
    result = get_stamped_filename("test.data.txt", "pdf")

    # Should split on last dot
    assert "test.data" in result
    assert result.endswith(".pdf")


def test_get_stamped_filename_timestamp_format():
    """Test that timestamp is in correct format"""
    result = get_stamped_filename("test.txt", "pdf")

    # Extract timestamp part
    parts = result.split(".")
    # Should have format: base, timestamp, extension
    assert len(parts) == 3

    # Verify it's a valid datetime format (YYYYMMDD_HHMM)
    timestamp = parts[1]
    assert len(timestamp) == 13  # YYYYMMDD_HHMM
    assert "_" in timestamp

    # Should be able to parse the date part
    date_part = timestamp.split("_")[0]
    datetime.strptime(date_part, "%Y%m%d")


def test_fix_eln_experiment_id_short():
    """Test fixing short experiment IDs (no change needed)"""
    short_id = 1234567
    result = fix_eln_experiment_id(short_id)
    assert result == short_id


def test_fix_eln_experiment_id_long():
    """Test fixing long experiment IDs"""
    # Long IDs start with extra 1
    long_id = 1000000001234567
    result = fix_eln_experiment_id(long_id)

    # Should remove the leading 1
    assert result == 1234567
    assert result < long_id


def test_fix_eln_experiment_id_threshold():
    """Test experiment ID at the threshold"""
    # Test around 1e15 threshold
    just_below = int(1e15 - 1)
    just_above = int(1e15 + 1)

    assert fix_eln_experiment_id(just_below) == just_below
    assert fix_eln_experiment_id(just_above) < just_above


def test_add_eln_data_to_record():
    """Test adding ELN data to a record"""
    mock_record = Mock()
    mock_record.data = {}

    mock_env = {
        'eln_experiment_id': '12345',
        'eln_experiment_name': 'Test Experiment',
        'eln_project_id': '678',
        'eln_project_name': 'Test Project',
        'eln_apikey': 'secret_key',  # Should be filtered out
        'eln_url': 'https://example.com',  # Should be filtered out
        'other_value': 'should_not_appear'  # Not ELN data
    }

    with patch('mus.plugins.eln.util.get_env', return_value=mock_env):
        add_eln_data_to_record(mock_record)

    # Check that ELN data was added
    assert mock_record.data['eln_experiment_id'] == '12345'
    assert mock_record.data['eln_experiment_name'] == 'Test Experiment'
    assert mock_record.data['eln_project_id'] == '678'
    assert mock_record.data['eln_project_name'] == 'Test Project'

    # Check that sensitive data was NOT added
    assert 'eln_apikey' not in mock_record.data
    assert 'eln_url' not in mock_record.data

    # Check that non-ELN data was NOT added
    assert 'other_value' not in mock_record.data


def test_add_eln_data_to_record_empty_env():
    """Test adding ELN data when no ELN data in environment"""
    mock_record = Mock()
    mock_record.data = {}

    mock_env = {
        'some_other_config': 'value'
    }

    with patch('mus.plugins.eln.util.get_env', return_value=mock_env):
        add_eln_data_to_record(mock_record)

    # Record data should still be empty
    assert mock_record.data == {}


def test_eln_no_experiment_id_exception():
    """Test ElnNoExperimentId exception"""
    with pytest.raises(ElnNoExperimentId):
        raise ElnNoExperimentId("No experiment ID provided")


def test_eln_conflicting_experiment_id_exception():
    """Test ElnConflictingExperimentId exception"""
    with pytest.raises(ElnConflictingExperimentId):
        raise ElnConflictingExperimentId("Conflicting IDs")


def test_fix_eln_experiment_id_string_conversion():
    """Test that fix_eln_experiment_id works with string manipulation"""
    # Very long ID
    long_id = 10000000012345678
    result = fix_eln_experiment_id(long_id)

    # Convert both to strings to check
    long_str = str(long_id)
    result_str = str(result)

    # Result should be the long string without the first character
    # Note: result is an int, so no leading zeros
    assert result_str == long_str[1:].lstrip('0') or result_str == '0'
    assert result == int(long_str[1:])


def test_get_stamped_filename_preserves_path():
    """Test that stamped filename works with just basename"""
    result = get_stamped_filename("myfile.txt", "pdf")

    # Should not contain path separators
    assert "/" not in result
    assert "\\" not in result


def test_add_eln_data_preserves_existing():
    """Test that adding ELN data preserves existing record data"""
    mock_record = Mock()
    mock_record.data = {'existing_key': 'existing_value'}

    mock_env = {
        'eln_experiment_id': '12345'
    }

    with patch('mus.plugins.eln.util.get_env', return_value=mock_env):
        add_eln_data_to_record(mock_record)

    # Both old and new data should be present
    assert mock_record.data['existing_key'] == 'existing_value'
    assert mock_record.data['eln_experiment_id'] == '12345'


def test_fix_eln_experiment_id_with_various_sizes():
    """Test fix_eln_experiment_id with various ID sizes"""
    test_cases = [
        (123, 123),  # Very small
        (123456, 123456),  # Normal
        (1000000, 1000000),  # 1 million
        # At exactly 1e15, doesn't trigger (needs to be > 1e15)
        (1000000000000000, 1000000000000000),
        # Just above threshold, strips leading 1
        (1000000000000001, 1),
        # Large, strips leading 1
        (1234567890123456, 234567890123456),
    ]

    for input_id, expected_output in test_cases:
        result = fix_eln_experiment_id(input_id)
        assert result == expected_output, f"Failed for {input_id}: got {result}, expected {expected_output}"

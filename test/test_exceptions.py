"""
Test custom exceptions
"""
import pytest

from mus.exceptions import MusInvalidConfigFileEntry, MusSecretNotDefined


def test_mus_invalid_config_file_entry():
    """Test MusInvalidConfigFileEntry exception"""
    with pytest.raises(MusInvalidConfigFileEntry):
        raise MusInvalidConfigFileEntry("Invalid line")


def test_mus_invalid_config_file_entry_with_message():
    """Test MusInvalidConfigFileEntry with custom message"""
    try:
        raise MusInvalidConfigFileEntry("Invalid config: missing equals sign")
    except MusInvalidConfigFileEntry as e:
        assert "Invalid config" in str(e)
        assert "missing equals sign" in str(e)


def test_mus_secret_not_defined():
    """Test MusSecretNotDefined exception"""
    with pytest.raises(MusSecretNotDefined):
        raise MusSecretNotDefined("api_key not defined")


def test_mus_secret_not_defined_with_message():
    """Test MusSecretNotDefined with custom message"""
    try:
        raise MusSecretNotDefined("Secret 'database_password' not found")
    except MusSecretNotDefined as e:
        assert "database_password" in str(e)
        assert "not found" in str(e)


def test_exceptions_are_catchable():
    """Test that exceptions can be caught as Exception"""
    with pytest.raises(Exception):
        raise MusInvalidConfigFileEntry("test")

    with pytest.raises(Exception):
        raise MusSecretNotDefined("test")


def test_exception_inheritance():
    """Test that custom exceptions inherit from Exception"""
    assert issubclass(MusInvalidConfigFileEntry, Exception)
    assert issubclass(MusSecretNotDefined, Exception)

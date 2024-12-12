
import os
from functools import lru_cache
from pathlib import Path
from textwrap import dedent
from typing import Any, Dict

import click
import keyring

from mus.exceptions import MusInvalidConfigFileEntry, MusSecretNotDefined

# Define a list of keys that are expected to be lists in the configuration.
# These keys will be treated differently when processing the configuration file.
# If a key is not in this list, its value will be treated as a single value.
# keys in this list are expected to be comma separated
LIST_KEYS = ['tag', 'collaborator']


def list_add(lst, val):

    def add_one(v2):
        # Check if the value begins with a '-', indicating a removal operation.
        if v2.startswith('-'):
            rmval = v2[1:]  # If so, remove the '-' to determine the value to remove.
            assert len(rmval) > 0
            # Remove all instances of rmval from the list.
            while rmval in lst:
                lst.remove(rmval)
        else:
            # Add the original value to the list.
            assert len(v2) > 0
            lst.append(v2)

    if isinstance(val, list):
        for v in val:
            add_one(v)
    else:
        add_one(val)



@lru_cache(32)
def load_single_env(fn):
    """
    Reads an environment configuration file and returns its contents as a dictionary.

    This function processes a file where each non-empty line is expected to be in the format 'KEY=VALUE'.
    It trims any whitespace from the keys and values, ensuring clean entries in the resulting dictionary.
    Lines that do not contain an '=' character are considered invalid and will raise an InvalidConfigFileEntry exception.

    Args:
        fn (str): The file name or path to the environment file to be read.

    Returns:
        dict: A dictionary containing key-value pairs parsed from the file.

    Raises:
        InvalidConfigFileEntry: If a line does not contain an '=' character, indicating an invalid entry.
    """
    rv = {}
    with open(fn, 'rt') as F:
        for line in F:
            line = line.strip()
            if not line:
                # do not process empty lines!
                continue
            if '=' not in line:
                raise MusInvalidConfigFileEntry(line)
            key, value = line.split('=', 1)
            if key in LIST_KEYS:
                rv[key.strip()] = [x.strip() for x in value.split(',')]
            else:
                rv[key.strip()] = value.strip()

    return rv


@lru_cache(32)
def get_env(wd: None | str | Path = None) -> dict:
    """Get recursive config"""

    config: Dict[str, Any] = {}

    # find configs
    if wd is None:
        wd = Path().resolve()
    else:
        wd = Path(wd).resolve()

    config_files = []
    while len(str(wd)) > 3:
        loco = wd / '.env'
        if loco.exists():
            config_files.append(loco)
        wd = wd.parent

    config_files.reverse()
    for loco in config_files:
        conf_ = load_single_env(loco)
        for key, val in conf_.items():
            if key not in LIST_KEYS:
                config[key] = val
            else:
                curval = config.get(key, [])
                assert isinstance(val, list)
                assert isinstance(curval, list)
                for one_val in val:
                    list_add(curval, one_val)
                config[key] = curval
    return config


def get_local_config(wd: None | str | Path = None) -> dict:
    if wd is None:
        wd = Path().resolve()
    else:
        wd = Path(wd).resolve()

    if os.path.exists(wd / '.env'):
        return load_single_env(wd / '.env')
    else:
        return {}


def save_env(conf: Dict,
             wd: None | str | Path = None):
    if wd is None:
        wd = Path().resolve()
    else:
        wd = Path(wd).resolve()

    with open('.env', 'wt') as F:
        for k, v in sorted(conf.items()):
            if isinstance(v, list):
                v = ', '.join(v)
            F.write(f'{k}={v}\n')


def save_kv_to_local_config(
        key: str | None = None,
        val: Any | None = None,
        data: Dict[str, Any] | None = None,
        wd: None | str | Path = None) -> None:
    """ Save a key-value pair to the local config file.

    This function updates the local configuration file with a new
    key-value pair. If the key is in the LIST_KEYS, the value is
    treated as a list and appended to the existing list. Otherwise,
    the value is treated as a single value.

    Args:
        key (str): The key to be added or updated in the configuration.
        val (Any): The value to be associated with the key.
        wd (None | str | Path, optional): The working directory where the
            configuration file is located. If None, the current working
            directory is used. Defaults to None.
    """
    if wd is None:
        wd = Path().resolve()
    else:
        wd = Path(wd).resolve()

    conf = get_local_config(wd)

    def apply_keyval(key, val):
        if key in LIST_KEYS:
            curval = conf.get(key, [])
            assert isinstance(curval, list)
            list_add(curval, val)
            conf[key] = curval
        else:
            conf[key] = val

    if key is not None and val is not None:
        apply_keyval(key, val)

    if data is not None:
        for k, v in data.items():
            apply_keyval(k, v)

    save_env(conf, wd)


@lru_cache(1)
def get_keyring():
    import keyring
    import platform
    from keyring.errors import KeyringError

    if platform.system() == "Linux":
        # Not ideal - but fast, plaintext
        from keyrings.alt.file import PlaintextKeyring
        try:
            return PlaintextKeyring()
        except KeyringError as e:
            print(f"Error configuring PlaintextKeyring: {e}")
            raise
    else:
        return keyring


def get_secret(name: str,
               hint: str | None = None):
    """Find the secret in the keyring or provide a useful error message"""

    # check keyring
    kr = get_keyring()
    rv = kr.get_password('mus', name)
    if rv is not None:
        return rv

    # check environment
    if name.upper() in os.environ:
        return os.environ[name.upper()]

    # not found!
    if hint is None:
        hint = ""
    else:
        hint = f"\n{hint}\n"

    click.echo(dedent(
        f"""
        Please specify the {name} key.
        {hint}
        Please make available using:

        `mus secret set {name} <VALUE>`

        or in your environment:

        `export {name.upper()}="<VALUE>"`

        """))
    raise MusSecretNotDefined(f"{name} not defined")


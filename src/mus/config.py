
import json
import os
from functools import lru_cache
from pathlib import Path
from typing import Any, Dict

from mus.exceptions import InvalidConfigFileEntry

LIST_KEYS = ['tag']

def list_add(lst, val):
    # Check if the value begins with a '-', indicating a removal operation.
    if val.startswith('-'):
        rmval = val[1:]  # If so, remove the '-' to determine the value to remove.
    else:
        rmval = '-' + val  # Otherwise, prepend '-' to create the removal value.

    assert len(val) > 0  # Ensure that the value is not an empty string.

    # Remove all instances of rmval from the list.
    while rmval in lst:
        lst.remove(rmval)

    # Add the original value to the list.
    lst.append(val)

def load_env(fn):
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
            if not '=' in line:
                raise InvalidConfigFileEntry(line)
            key, value = line.split('=', 1)
            rv[key.strip()] = value.strip()
    return rv


@lru_cache(1)
def get_config(wd: None | str | Path = None) -> dict:
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
        conf_ = load_env(loco)
        for key, val in conf_.items():
            if key not in LIST_KEYS:
                config[key] = val
            else:
                curval = config.get(key, [])
                lval = val.split()
                assert isinstance(curval, list)
                for one_val in lval:
                    list_add(curval, one_val)
                config[key] = curval

    return config


def get_local_config() -> dict:
    if os.path.exists('mus.config'):
        with open('mus.config') as F:
            rv = json.load(F)
            return rv
    else:
        return {}


def save_local_config(conf: dict):
    with open('mus.config', 'w') as F:
        json.dump(conf, F)


def save_kv_to_local_config(key, val):
    conf = get_local_config()

    if key in LIST_KEYS:

        curval = conf.get(key, [])
        assert isinstance(curval, list)
        list_add(curval, val)
        conf[key] = curval

    else:
        conf[key] = val

    save_local_config(conf)

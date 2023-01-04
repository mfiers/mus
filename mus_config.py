
import json
import os
from functools import lru_cache
from pathlib import Path
from typing import List, TypedDict

LIST_KEYS = ['project', 'tag']


def list_add(lst, val):
    if val.startswith('-'):
        rmval = val[1:]
    else:
        rmval = '-' + val

    assert len(val) > 0

    while rmval in lst:
        lst.remove(rmval)

    lst.append(val)



@lru_cache(1)
def get_config() -> dict:
    """Get recursive config"""

    config = {}

    # find configs
    config_files = []
    cwd = Path().resolve()
    while len(str(cwd)) > 3:
        loco = cwd / 'mus.config'
        if loco.exists():
            config_files.append(loco)
        cwd = cwd.parent

    config_files.reverse()
    for loco in config_files:
        with open(loco) as F:
            conf_ = json.load(F)
            for key, val in conf_.items():
                if key in LIST_KEYS:
                    curval = config.get(key, [])
                    assert isinstance(val, list)
                    assert isinstance(curval, list)
                    for one_val in val:
                        list_add(curval, one_val)
                    config[key] = curval
                else:
                    config[key] = val

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


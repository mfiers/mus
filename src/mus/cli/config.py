
import json

import click

from mus.config import (  # NOQA: E402
    get_config,
    get_local_config,
    save_kv_to_local_config,
)


# CONFIGURATION
@click.group("config")
def cmd_config():
    pass


@cmd_config.command("set", context_settings=dict(ignore_unknown_options=True))
@click.argument("key")
@click.argument("val")
def conf_set(key, val):
    save_kv_to_local_config(key, val)


@cmd_config.command("show")
@click.option("-j", '--json', "json_output",
              is_flag=True, default=False,
              help="Print json.")
@click.option("-l", '--local', is_flag=True, default=False,
              help="show only local config.")
def conf_show(json_output, local):
    if local:
        conf = get_local_config()
    else:
        conf = get_config()
    if json_output:
        print(json.dumps(conf, indent=2))
    else:
        for k, v in sorted(conf.items()):
            if isinstance(v, list):
                print(f"{k}\t{', '.join(v)}")
            else:
                print(f"{k}\t{v}")

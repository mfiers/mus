
import json

import click
import keyring

from mus.config import get_env, get_local_config, save_kv_to_local_config  # NOQA: E402


# CONFIGURATION
@click.group("config")
def cmd_config():
    """Manage key/values in .env"""
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
        conf = get_env()
    if json_output:
        print(json.dumps(conf, indent=2))
    else:
        for k, v in sorted(conf.items()):
            if isinstance(v, list):
                print(f"{k}\t{', '.join(v)}")
            else:
                print(f"{k}\t{v}")


# SECRETS - store in keychaing
@click.group("secret")
def cmd_secrets():
    """Manage passwords & config in keychain"""
    pass


@cmd_secrets.command("set")
@click.argument("key")
@click.argument("val")
def secret_set(key, val):
    """Set key/val in the keychain"""
    keyring.set_password("mus", key, val)



@cmd_secrets.command("get")
@click.argument("key")
def secret_get(key):
    """Get key from the keychain"""
    print(keyring.get_password("mus", key))


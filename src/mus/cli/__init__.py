#!/usr/bin/env python3

import logging

import click
import colorama
from click import echo, style

from mus import hooks

# plugins
from mus.plugins import iotracker, job_run_check
from mus.util import pts
from mus.util.cli import AliasedGroup  # NOQA: E402
from mus.util.log import ColorLogger  # NOQA: E402

# load plugins - need to automate this somehow

# Set up color logging
colorama.init(autoreset=True)


logging.setLoggerClass(ColorLogger)
lg = logging.getLogger('mus')
lg.setLevel(logging.WARNING)
logging.getLogger('asyncio').setLevel(logging.WARNING)

# late import to make sure logging is set properly.
from mus.config import (  # NOQA: E402
    get_config,
    get_local_config,
    save_kv_to_local_config,
)
from mus.db import Record  # NOQA: E402
from mus.db import get_db_connection, get_db_path  # NOQA: E402


@click.group(cls=AliasedGroup)
@click.option('-v', '--verbose', count=True)
def cli(verbose):
    if verbose == 1:
        lg.setLevel(logging.INFO)
    elif verbose > 1:
        lg.setLevel(logging.DEBUG)


from mus.cli import db as dbcli  # NOQA: E402
from mus.cli import files  # NOQA: E402
from mus.cli import macro  # NOQA: E402
from mus.cli import search  # NOQA: E402

cli.add_command(search.cmd_search)
cli.add_command(files.tag)
cli.add_command(files.file_)
cli.add_command(macro.cli_macro)
cli.add_command(dbcli.db)


@cli.command("version")
def print_version():
    from importlib.metadata import version
    print(version('mus'))


@cli.command("log")
@click.argument("message", nargs=-1)
def log(message):
    """Store a log message"""
    rec = Record()
    rec.prepare()
    rec.message = " ".join(message)
    rec.type = 'log'
    rec.save()


# CONFIGURATION
@cli.group()
def conf():
    pass

@conf.command("set", context_settings=dict(ignore_unknown_options=True))
@click.argument("key")
@click.argument("val")
def conf_set(key, val):
    save_kv_to_local_config(key, val)


@cli.command("histon")
def histon():
    """Store history for this folder, and below"""
    save_kv_to_local_config("store-history", "yes")


@cli.command("histoff")
def histoff():
    """Store history for this folder, and below"""
    save_kv_to_local_config("store-history", "no")


@conf.command("show")
@click.option("-l", '--local', is_flag=True, default=False,
              help="show only local config.")
def conf_show(local):
    if local:
        conf = get_local_config()
    else:
        conf = get_config()

    print(conf)


if __name__ == "__main__":
    cli()

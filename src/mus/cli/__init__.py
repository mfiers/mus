#!/usr/bin/env python3

import logging
from typing import List

import click
import colorama
from click import echo, style

import mus
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


from mus.db import Record  # NOQA: E402
from mus.db import get_db_connection, get_db_path  # NOQA: E402


@click.group(cls=AliasedGroup)
@click.option('-v', '--verbose', count=True)
def cli(verbose):
    if verbose == 1:
        lg.setLevel(logging.INFO)
    elif verbose > 1:
        lg.setLevel(logging.DEBUG)


from mus.cli import config  # NOQA: E402
from mus.cli import files  # NOQA: E402
from mus.cli import macro  # NOQA: E402
from mus.cli import search  # NOQA: E402
from mus.cli import db as dbcli  # NOQA: E402

cli.add_command(search.cmd_search)
cli.add_command(files.tag)
cli.add_command(files.file_)
cli.add_command(macro.cli_macro)
cli.add_command(dbcli.db)
cli.add_command(config.cmd_config)


@cli.command("version")
def print_version():
    print(mus.__version__)


@cli.command("log")
@click.argument("message", nargs=-1)
def log(message: List[str]):
    """Store a log message"""
    smessage = " ".join(message).strip()
    if smessage == "":
        echo('No message specified')
        return

    rec = Record()
    rec.prepare()
    rec.message = " ".join(message)
    rec.type = 'log'
    rec.save()



@cli.command("histon")
def histon():
    """Store history for this folder, and below"""
    save_kv_to_local_config("store-history", "yes")


@cli.command("histoff")
def histoff():
    """Store history for this folder, and below"""
    save_kv_to_local_config("store-history", "no")



if __name__ == "__main__":
    cli()

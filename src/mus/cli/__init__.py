#!/usr/bin/env python3

import logging

import click
import colorama

import mus
from mus.hooks import call_hook  # NOQA: E402
from mus.util.cli import AliasedGroup  # NOQA: E402
from mus.util.log import ColorLogger  # NOQA: E402

# Set up color logging
colorama.init(autoreset=True)


logging.setLoggerClass(ColorLogger)
lg = logging.getLogger('mus')
lg.setLevel(logging.WARNING)
logging.getLogger('asyncio').setLevel(logging.WARNING)


@click.group(cls=AliasedGroup)
@click.pass_context
@click.option('-v', '--verbose', count=True)
@click.option('--profile', help=None, is_flag=True)
def cli(ctx, verbose, profile):
    if verbose == 1:
        lg.setLevel(logging.INFO)
    elif verbose > 1:
        lg.setLevel(logging.DEBUG)

    profiler_ = None

    if profile:
        from cProfile import Profile
        from pstats import Stats
        profiler_ = Profile()
        profiler_.enable()

    @ctx.call_on_close
    def close():
        if profile:
            profiler_.disable()
            stats = Stats(profiler_)
            stats.sort_stats('cumulative')
            stats.print_stats(100)


# commands to hook into the cli:
from mus.cli import config  # NOQA: E402
from mus.cli import files  # NOQA: E402
from mus.cli import macro  # NOQA: E402
from mus.cli import search  # NOQA: E402
from mus.cli import db as dbcli  # NOQA: E402
from mus.cli import log as muslog  # NOQA: E402

cli.add_command(search.cmd_search)
cli.add_command(files.filetag)
cli.add_command(files.findfile)
cli.add_command(macro.cli_macro)
cli.add_command(dbcli.db)
cli.add_command(config.cmd_config)
cli.add_command(muslog.log)


# TODO: need an automated way to specify and load plugins
# for now, jus timport
import mus.plugins.eln  # NOQA: E402
import mus.plugins.irods  # NOQA: E402

# give all the plugins time to register themselves
call_hook('plugin_init', cli=cli)


@cli.command("version")
def print_version():
    print(mus.__version__)


# temporarilly disabled
#
# @cli.command("histon")
# def histon():
#     """Store history for this folder, and below"""
#     save_kv_to_local_config("store-history", "yes")


# @cli.command("histoff")
# def histoff():
#     """Store history for this folder, and below"""
#     save_kv_to_local_config("store-history", "no")




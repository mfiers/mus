
import logging
import os
import sys
from pathlib import Path
from typing import Optional

import click
from click import echo, style

from mus.macro import Macro
from mus.util.cli import AliasedGroup  # NOQA: E402

lg = logging.getLogger("mus")


@click.group("macro", cls=AliasedGroup)
def cli_macro():
    """Run a cl macro on multiple input files

    To use macro's you need to prepare your shell. See the README.

    \b
    Arguments:
        -v, -vv  : increase verbosity
        -j<INT>  : no parallel processes to use
        -s<NAME> : save macro for later use
        -l<NAME> : load and run macro
        -s<INT>  : No jobs to run at most
        -d       : dry-run - show what would be executed
        -D       : as dry-run - but show more information

    Note, no spaces between the option flag and value are allowed!
    Note, no combinations of flags are allowed (such as -vd)
    """


@cli_macro.command("list")
def macro_list():
    from mus.macro import find_saved_macros
    for name, val in find_saved_macros().items():
        echo(style(name, fg="green") + ": " + val)


# @macro.group('wrapper')
# def wrapper():
#     pass


# @wrapper.command("list")
# def wrapper_list():
#     from mus.macro import find_saved_wrappers
#     for name, val in find_saved_wrappers().items():
#         echo(style(name, fg="green") + ": " + val)


@cli_macro.command("edit")
@click.argument('name')
def macro_edit(name):
    """Drop into EDITOR with the macro of choice"""
    from subprocess import call

    from mus.macro import MACRO_SAVE_PATH

    if not os.path.exists(MACRO_SAVE_PATH):
        os.makedirs(MACRO_SAVE_PATH)

    filename = Path(MACRO_SAVE_PATH).expanduser() / f"{name}.mmc"
    editor = os.environ.get('EDITOR', 'vi')
    cl = f"{editor} {filename}"
    call(cl, shell=True)


# @wrapper.command("edit")
# @click.argument('name')
# def wrapper_edit(name: str):
#     """Drop into an EDITOR with the wrapper of choice"""

#     from subprocess import call

#     from mus.macro import WRAPPER_SAVE_PATH

#     if not os.path.exists(WRAPPER_SAVE_PATH):
#         os.makedirs(WRAPPER_SAVE_PATH)

#     filename = Path(WRAPPER_SAVE_PATH).expanduser() / f"{name}.mwr"
#     editor = os.environ.get('EDITOR', 'vi')
#     cl = f"{editor} {filename}"
#     call(cl, shell=True)


@cli_macro.command("stdin-exe", hidden=False)
@click.pass_context
def cli_exe(ctx) -> None:
    """Take a line hijacked from bash/zsh history and execute it as a macro

    Raises:
        click.UsageError: Invalid macro/configuration
    """
    import multiprocessing
    import re

    # from mus.macro import Macro, load_wrapper
    raw_macro = sys.stdin.read()
    _macro_split = raw_macro.strip().split(None, 2)
    if len(_macro_split) < 3:
        echo("Please specify something to execute")
        return

    raw_macro = _macro_split[2].strip()
    lg.info(f"Raw macro: {raw_macro}")

    # find & define parameters by parsing start of string
    # stop parsing as soon as no args are being recognized anymore
    no_threads: int = multiprocessing.cpu_count()
    save_name = None
    load_name = None
    force = False
    dry_run = False
    dry_run_extra = False
    max_no_jobs = -1
    # wrapper_name = None

    rex = (
        r'^-(?P<flagbool>[vhdDfM]+)'
        r'|-(?P<flagint>[nj])\s*(?P<intval>[0-9]+)'
        r'|-(?P<flagstr>[lsw])\s*(?P<strval>[A-Za-z0-9]+)'
    )

    while raw_macro:
        pmatch = re.match(rex, raw_macro)
        if pmatch:
            pgroups = pmatch.groupdict()
            flagbool = pgroups.get('flagbool', '')
            if flagbool is not None:
                for flag in flagbool:
                    if flag == 'v':
                        lg.debug("Increasing verbosity")
                        if lg.getEffectiveLevel() > logging.INFO:
                            lg.setLevel(logging.INFO)
                        elif lg.getEffectiveLevel() > logging.DEBUG:
                            lg.setLevel(logging.DEBUG)
                    elif flag == 'd':
                        lg.debug("Changing to dry run mode")
                        dry_run = True
                    elif flag == 'h':
                        lg.debug("Help")
                        print(ctx.get_help())
                        return
                    elif flag == 'f':
                        lg.debug("Force run - ignore advise")
                        force = True
                    elif flag == 'D':
                        lg.debug("Setting very dry run mode")
                        dry_run = dry_run_extra = True
            elif pgroups['flagint'] is not None:
                flag = pgroups['flagint']
                value = int(pgroups['intval'])
                if flag == 'j':
                    no_threads = value
                    lg.debug(f"Using {no_threads}")
                elif flag == 'n':
                    max_no_jobs = value
                    lg.debug(f"Max jobs to run {max_no_jobs}")
            if pgroups['flagstr'] is not None:
                flag = pgroups['flagstr']
                value = pgroups['strval']
                if flag == 's':
                    save_name = value
                    lg.debug(f"Saving to '{save_name}'")
                elif flag == 'w':
                    wrapper_name = value
                    lg.debug(f"Apply wrapper {wrapper_name}")
                elif flag == 'l':
                    load_name = value
                    lg.debug(f"Loading from '{load_name}'")

            raw_macro = raw_macro[pmatch.end():].strip()
            continue  # maybe there are more parameters?
        break

    if raw_macro and load_name:
        lg.warning("specified both a macro to load and macro string"
                   " - unsure what to do")
        return

    macro_args = dict(dry_run=dry_run,
                      force=force,
                      dry_run_extra=dry_run_extra,
                      max_no_jobs=max_no_jobs)

    if raw_macro:
        macro = Macro(raw=raw_macro, **macro_args)
    elif load_name:
        macro = Macro(name=load_name, **macro_args)
    else:
        raise click.UsageError("No macro defined")

    if save_name is not None:
        macro.save(save_name)

    lg.info(f"Executing macro: {macro.raw}")
    lg.info(f"No threads: {no_threads}")

    macro.execute(no_threads)


import logging
import os
import sys
from pathlib import Path

import click
from click import echo, style

from mus.util.cli import AliasedGroup  # NOQA: E402

lg = logging.getLogger("mus")


@click.group(cls=AliasedGroup)
def macro():
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


@macro.command("list")
def macro_list():
    from mus.macro import find_saved_macros
    for name, val in find_saved_macros().items():
        echo(style(name, fg="green") + ": " + val)


@macro.group('wrapper')
def wrapper():
    pass


@wrapper.command("list")
def wrapper_list():
    from mus.macro import find_saved_wrappers
    for name, val in find_saved_wrappers().items():
        echo(style(name, fg="green") + ": " + val)


@macro.command("edit")
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


@wrapper.command("edit")
@click.argument('name')
def wrapper_edit(name: str):
    """Drop into an EDITOR with the wrapper of choice"""

    from subprocess import call

    from mus.macro import WRAPPER_SAVE_PATH

    if not os.path.exists(WRAPPER_SAVE_PATH):
        os.makedirs(WRAPPER_SAVE_PATH)

    filename = Path(WRAPPER_SAVE_PATH).expanduser() / f"{name}.mwr"
    editor = os.environ.get('EDITOR', 'vi')
    cl = f"{editor} {filename}"
    call(cl, shell=True)


@macro.command("stdin-exe", hidden=True)
def macro_cli_exe():
    """Take a line hijacked from bash/zsh history and execute it as a macro

    Raises:
        click.UsageError: Invalid macro/configuration
    """
    import multiprocessing
    import re

    from mus.macro import Macro, load_wrapper
    raw_macro = sys.stdin.read()
    _ = raw_macro.strip().split(None, 2)
    if len(_) < 3:
        echo("Please specify something to execute")
        return

    raw_macro = _[2].strip()
    lg.info(f"Raw macro: {raw_macro}")
    # find & define parameters by parsing start of string
    # stop parsing as soon as no args are being recognized anymore
    no_threads = multiprocessing.cpu_count()
    save_name = None
    load_name = None
    dry_run = False
    dry_run_extra = False
    max_no_jobs = -1
    explain_macro = False
    wrapper_name = None

    while raw_macro:
        # Flags
        print('x', raw_macro)
        flag_match = re.match('^-([vdDM]+ )', raw_macro)
        if flag_match:
            for flag in flag_match.groups()[0]:
                if flag == 'v':
                    lg.debug("Increasing verbosity")
                    if lg.getEffectiveLevel() > logging.INFO:
                        lg.setLevel(logging.INFO)
                    elif lg.getEffectiveLevel() > logging.DEBUG:
                        lg.setLevel(logging.DEBUG)
                elif flag == 'd':
                    lg.debug("Changing to dry run mode")
                    dry_run = True
                elif flag == 'D':
                    lg.debug("Setting very dry run mode")
                    dry_run = dry_run_extra = True
                elif flag == 'M':
                    lg.debug("Explain macro mode")
                    explain_macro = True

            raw_macro = raw_macro[flag_match.end():].strip()
            continue

        # Integer Numerical arguments
        num_match = re.match(r'-([jn])\s*([0-9]+)', raw_macro)
        if num_match:
            flag, value = num_match.groups()
            value = int(value)
            if flag == 'j':
                no_threads = value
                lg.debug(f"Using {no_threads}")
            elif flag == 'n':
                no_threads = value
                lg.debug(f"Max jobs to run {no_threads}")
            raw_macro = raw_macro[num_match.end():].strip()
            continue

        # String arguments
        str_match = re.match(r'-([swl])\s*([A-Za-z0-9_]+)', raw_macro)
        # Save macro to {save_name}
        if str_match:
            flag, value = str_match.groups()
            if flag == 's':
                save_name = value
                lg.debug(f"Saving to '{save_name}'")
            elif flag == 'w':
                wrapper_name = value
                lg.debug(f"Apply wrapper {wrapper_name}")
            elif flag == 'l':
                load_name = value
                lg.debug(f"Loading from '{load_name}'")
            raw_macro = raw_macro[str_match.end():].strip()
            continue
        break

    if raw_macro and load_name:
        lg.warning("specified both a macro to load and macro string"
                   " - unsure what to do")
        return

    wrapper = None
    if wrapper_name:
        wrapper = load_wrapper(wrapper_name)

    macro_args = dict(dry_run=dry_run,
                      dry_run_extra=dry_run_extra,
                      max_no_jobs=max_no_jobs,
                      wrapper=wrapper)

    if raw_macro:
        macro = Macro(raw=raw_macro, **macro_args)
    elif load_name:
        macro = Macro(name=load_name, **macro_args)
    else:
        raise click.UsageError("No macro defined")

    if save_name is not None:
        macro.save(save_name)

    if explain_macro:
        macro.explain()
        return

    lg.info(f"Executing macro: {macro.raw}")
    lg.info(f"No threads: {no_threads}")

    macro.execute(no_threads)

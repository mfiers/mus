
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
        -d       : dry-run - show what would be executed

    Note, no spaces between the option flag and value are allowed!
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
    cl = f"{editor} {filename}"
    call(cl, shell=True)


@wrapper.command("edit")
@click.argument('name')
def wrapper_edit(name):
    """Drop into EDITOR with the wrapper of choice"""
    from subprocess import call

    from mus.macro import WRAPPER_SAVE_PATH

    if not os.path.exists(WRAPPER_SAVE_PATH):
        os.makedirs(WRAPPER_SAVE_PATH)

    filename = Path(WRAPPER_SAVE_PATH).expanduser() / f"{name}.mwr"
    editor = os.environ.get('EDITOR', 'vim')
    cl = f"{editor} {filename}"
    print(cl)
    call(cl, shell=True)


@macro.command("stdin-exe", hidden=True)
def macro_cli_exe():

    import multiprocessing
    import re

    from mus.macro import Macro, load_wrapper

    raw_macro = sys.stdin.read()
    _ = raw_macro.strip().split(None, 2)
    if len(_) < 3:
        echo("Please specify something to execute")
        return
    raw_macro = _[2]

    # find & define parameters by parsing start of string
    # stop parsing as soon as no args are found
    no_threads = multiprocessing.cpu_count()
    save_name = None
    load_name = None
    dry_run = False
    explain_macro = False
    wrapper_name = None

    while raw_macro:

        if ' ' in raw_macro:
            maybe_arg, macro_rest = raw_macro.split(None, 1)
        else:
            maybe_arg, macro_rest = raw_macro, ''


        if re.match('-j([0-9]+)', maybe_arg):
            m = re.match('-j([0-9]+)', maybe_arg)
            no_threads = int(m.groups()[0])
        elif re.match(r'-s([a-zA-Z]\w*)', maybe_arg):
            m = re.match(r'-s([a-zA-Z]\w*)', maybe_arg)
            save_name = m.groups()[0]
        elif re.match(r'-w([a-zA-Z]\w*)', maybe_arg):
            m = re.match(r'-w([a-zA-Z]\w*)', maybe_arg)
            wrapper_name = m.groups()[0]
        elif re.match(r'-l([a-zA-Z]\w*)', maybe_arg):
            m = re.match(r'-l([a-zA-Z]\w*)', maybe_arg)
            load_name = m.groups()[0]
        elif maybe_arg == '-d':
            dry_run = True
        elif maybe_arg == '-M':
            explain_macro = True
        elif re.match(r'-(vv*)', maybe_arg):
            m = re.match(r'-(vv*)', maybe_arg)
            verbose = len(m.groups()[0])
            if verbose == 1:
                lg.setLevel(logging.INFO)
            elif verbose > 1:
                lg.setLevel(logging.DEBUG)
        else:
            # did not recognize - must not be a mus/macro argument
            # stop parsing
            break

        # we reach this line only if an argument matched
        raw_macro = macro_rest.strip()
        # continue parsing.


    if raw_macro and load_name:
        lg.warning("specified both a macro to load and macro string"
                   " - unsure what to do")
        return

    wrapper = None
    if wrapper_name:
        wrapper = load_wrapper(wrapper_name)

    macro_args = dict(dry_run=dry_run)

    if raw_macro:
        macro = Macro(raw=raw_macro, wrapper=wrapper, **macro_args)
    elif load_name:
        macro = Macro(name=load_name, wrapper=wrapper, **macro_args)
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

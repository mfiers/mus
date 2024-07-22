
import getpass
import itertools
import logging
import os
import re
import socket
import time
from datetime import datetime
from io import TextIOWrapper
from itertools import product
from pathlib import Path
from typing import Dict, List, Optional, Type
from uuid import uuid4

import click

from mus.db import Record
from mus.hooks import call_hook, register_hook
from mus.macro.executors import AsyncioExecutor, Executor
from mus.macro.job import MacroJob
from mus.util import msec2nice

lg = logging.getLogger(__name__)
lg.propagate = False  # I do not know why this is required?

DEBUG = True
MACRO_SAVE_PATH = "~/.local/mus/macro"
#wrapperWRAPPER_SAVE_PATH = "~/.local/mus/wrappers"

# Example macros
#
###  file expansion
# $ ls {*.txt}
# $ ls {file?.txt}


def find_saved_macros() -> Dict[str, str]:
    """Return a list of saved macro's.

    Returns:
        Dict[str, str]: Dict of macro name: first 100 characters
    """
    save_folder = Path(MACRO_SAVE_PATH).expanduser()
    if not save_folder.exists():
        return {}

    rv = {}
    for macrofile in save_folder.glob('*.mmc'):
        name = macrofile.name[:-4]
        peek = open(macrofile).read()
        peek = " ".join(peek.split())[:100]
        rv[name] = peek
    return rv


class Expandable:
    def __init__(self, varname: str, text: str):
        self.text = text
        self.varname = varname
        self.expander = None


    def yielder(self, iterator):
        return [(self.varname, x) for x in iterator]


    def expand(self):

        # simplistic for now
        if m := re.match(r'\s*range\(([\d\,]+)\)\s*', self.text):
            match = m.groups()[0]
            args = list(map(int, match.split(',')))
            return self.yielder(range(*args))

        elif '*' in self.text or '?' in self.text:
            # assume glob
            lg.debug(f"glob {self.text}")
            return self.yielder(Path('.').glob(self.text))


def render2jinja(text: str):
    """
    Convert a shortcut renderable element to a jinja2 template

    1 letter:
    '%'    -> {{ a|output }}

    any other case:

    %b%  -> {{ b|output }}

    >>> render2jinja("%")
    'a|fmt("%")|output'
    >>> render2jinja("%.gz")
    'a|fmt("%.gz")|output'
    >>> render2jinja("%b%")
    'b|fmt("%")|output'
    >>> render2jinja("%q.%")
    'q|basename|fmt("%")|output'

    """
    text = text.strip()

    if '"' in text:
        text = text.replace('"', r'\"')

    if text.count('%') == 1:
        return 'a|fmt("'+text+'")|output'

    match = re.match(r'%([a-z]?)([\./]?)%', text)

    if match is None:
        raise Exception("Invalid template?")

    upto = text[:match.start()]
    after = text[match.end():]

    pname, pmod = match.groups()
    if pname == '':
        pname = 'a'

    pfilt = ''
    if pmod == '.':
        pfilt = '|basename'
    elif pmod == '/':
        pfilt = '|resolve'
    return f'{pname}{pfilt}|fmt("{upto}%{after}")|output'

# def find_saved_wrappers() -> Dict[str, str]:
#     """Find a list of all wrappers saved and return first 100 characters for
#     display.

#     Returns:
#         Dict[str, str]: Dictionary of name: first parts of the wrappers
#     """
#     save_folder = Path(WRAPPER_SAVE_PATH).expanduser()
#     if not save_folder.exists():
#         return {}

#     rv = {}
#     for wrapperfile in save_folder.glob('*.mwr'):
#         name = wrapperfile.name[:-4]
#         peek = open(wrapperfile, encoding='utf-8').read()
#         peek = " ".join(peek.split())[:100]
#         rv[name] = peek
#     return rv


def load_macro(macro_name: str) -> str:
    """Load a Macro and return it's contents

    Args:
        macro_name (str): Name of the macro

    Raises:
        FileExistsError: If the {macro_name}.mmc file is not found

    Returns:
        str: Macro contents
    """
    filename = Path(MACRO_SAVE_PATH).expanduser()
    filename /= f"{macro_name}.mmc"

    if not filename.exists():
        raise FileExistsError(filename)

    with open(filename, encoding='utf-8') as F:
        macro = F.read()
        mpeek = " ".join(macro.split())[:50]
        lg.info(f"load macro {macro_name}: {mpeek}")
        return macro


def delete_macro(macro_name: str) -> bool:
    """Delete a saved macro

    Args:
        macro_name (str): Name of the macro

    Returns:
        bool: True if succesfully deleted. False if the macro did not exists.

    """
    filename = Path(MACRO_SAVE_PATH).expanduser()
    filename /= f"{macro_name}.mmc"

    if filename.exists():
        filename.unlink()
        return True
    else:
        return False


class Macro:
    def __init__(self,
                 raw: Optional[str] = None,
                 name: Optional[str] = None,
                 # wrapper: Optional[str] = None,
                 max_no_jobs: int = -1,
                 dry_run: bool = False,
                 force: bool = False,
                 dry_run_extra: bool = False,
                 executor: Type[Executor] = AsyncioExecutor
                 ) -> None:
        """
        Class representing a macro run

        Args:
            raw (Optional[str], optional): Raw macro string. Defaults to
                None.
            name (Optional[str], optional): Load name of the macro.
                Defaults to None.
            max_no_jobs (Int, optional): Do not run more jobs than this.
                Defaults to -1.
            dry_run (bool, optional): Only print what is to be executed.
                Defaults to False.
            executor (Type[Executor], optional): Executor taking care of
                actual run. Defaults to AsyncioExecutor.
        """

        self.executor = executor
        self.max_no_jobs = max_no_jobs
        self.LogScript: Optional[TextIOWrapper] = None
        self.dry_run = dry_run
        self.force = force
        self.expandables = []
        self.dry_run_extra = dry_run_extra

        if not name and not raw:
            click.UsageError("Invalid macro instantation - "
                             "no macro specified")

        if name and raw:
            click.UsageError("Invalid macro instantation - "
                             "both macro & loadname specified")

        if raw:
            self.raw = raw
        else:
            self.load(name)

        self.record = Record()
        self.record.prepare(
            cl=self.raw,
            rectype="macro")
        self.record.save()

        self.process_raw()

        call_hook('create_job', job=self)

    def get_save_file(self, save_name):
        save_folder = Path(MACRO_SAVE_PATH).expanduser()
        if not save_folder.exists():
            save_folder.mkdir(parents=True)
        return save_folder / f"{save_name}.mmc"

    def save(self, save_name):
        save_file = self.get_save_file(save_name)
        if DEBUG:
            mpeek = " ".join(self.raw.split())[:50]
            lg.info(f"Save macro {save_name}: {mpeek}")

        with open(save_file, 'wt') as F:
            F.write(self.raw)

    def load(self, name):
        macro_file = self.get_save_file(name)
        lg.debug(f"Load macro from {macro_file}")
        with open(macro_file, 'rt') as F:
            self.raw = F.read()


    def process_raw(self):
        """
        Process the raw macro string

        Find expandable & renderable items
        Raises:
            click.UsageError: Invalid macro
        """

        lg.info("Parsing raw macro")
        # Find expandable elements (not Jinja)
        # find by {}

        self.expandables = []
        macro_parts = []
        upto = 0
        for i, match in enumerate(
                re.finditer(
                    r'(?<!{){(?![%#])\s*([^{}]*?)\s*}',
                    self.raw)):

            # get a variable name
            assert i < 27
            varname = chr(i+97)

            # add the previous bit
            macro_parts.append(self.raw[upto: match.start()])
            fragment = match.groups()[0]


            filters = ''
            if '|' in fragment:
                fragment, filters = fragment.split('|',1)
                filters = '|' + filters

            if '%' in fragment:
                # assume this snippet will be rendered (not expanded)
                renderable = render2jinja(fragment)
                macro_parts.append(f"{{{{ {renderable}{filters} }}}}")
            else:
                # for now assuming globs - but could be any expandable feature
                # we'll think about formatting later
                lg.debug(f"Expandable: {varname} {fragment}")
                self.expandables.append(Expandable(varname, fragment))
                macro_parts.append(f"{{{{ {varname}{filters} }}}}")
            upto = match.end()

        # add the last bit:
        macro_parts.append(self.raw[upto:])
        self.macro = ''.join(macro_parts)
        print(self.macro)

    def expand(self):

        ##
        ## Singleton
        ##
        if len(self.expandables) == 0:
            lg.info('Executing singleton command')
            # if there is nothing to expand -
            job = MacroJob(
                macro=self,
                data={})

            job.prepare()
            run_advise, reason = job.get_run_advise()
            if (not self.force) and run_advise is False:
                lg.warning(
                    f"skipping job '{job.cl}' because: '{reason}'")
            else:
                job.record.add_message(f"Run because '{run_advise}'")
                yield job
            return

        all_macro_elements = [x.expand() for x in
                              self.expandables]

        i = 0
        for _ in product(*all_macro_elements):
            lg.info(f"{_}")
            job = MacroJob(macro=self, data=dict(_))
            job.prepare()
            run_advise, reason = job.get_run_advise()
            if (not self.force) and run_advise is False:
                lg.warning(
                    f"skipping job '{job.cl}' because: '{reason}'")
            else:
                i += 1
                if self.max_no_jobs > 0 and \
                        i > self.max_no_jobs:
                    return
                job.record.add_message(f"Run because '{run_advise}'")
                yield job

    def open_script_log(self, mode):
        assert self.LogScript is None
        env_hostname = os.environ.get('MUS_HOST', "")
        env_user = os.environ.get('MUS_USER', "")
        if env_hostname:
            env_hostname = f" ({env_hostname})"
        if env_user:
            env_user = f" ({env_user})"
        self.LogScript = open("./mus.log", "a")
        self.LogScript.write("\n\n" + "#" * 80 + "\n")
        self.LogScript.write(
            "# Time  : " + datetime.now().strftime("%Y-%m-%d %H:%M" + "\n"))
        self.LogScript.write(f"# Modus : {mode}\n")
        self.LogScript.write(
            f"# Host  : {socket.gethostname()}{env_hostname}\n")
        self.LogScript.write(f"# User  : {getpass.getuser()}{env_user}\n")
        self.LogScript.write(f"# Cwd   : {os.getcwd()}\n")

        self.LogScript.write(f"# Macro : {self.raw}\n\n")

    def close_script_log(self):
        assert self.LogScript is not None   # TODO - figure out what it is supposed to be
        self.LogScript.close()
        self.LogScript = None

    def add_to_script_log(self, job):
        assert self.LogScript is not None   # TODO - figure out what it is supposed to be
        ts = datetime.fromtimestamp(job.starttime).strftime("%Y-%m-%d %H:%M")
        self.LogScript.write(f"# Start : {ts} - {job.uid}\n")
        if job.inputfile is not None:
            self.LogScript.write(f"# File  : {job.inputfile}\n")
        self.LogScript.write(f"{job.cl}\n")
        self.LogScript.write(f"# Time  : {job.runtime:.4f}s\n")
        self.LogScript.write(f"# RC    : {job.returncode}\n\n")

    def execute(self, no_threads):
        # TODO: Reimplenebt Dry run
        # if self.dry_run:
        #     for job in self.expand():
        #         print(job.cl)
        #         if self.dry_run_extra:
        #             for d in job.data:
        #                 dval = job.data[d]
        #                 dren = job.rendered[d]
        #                 if isinstance(dval, Atom):
        #                     tags = " - tags " + dval.tagstr()
        #                 if dval == dren:
        #                     print("  +" + d, job.data[d], tags)
        #                 else:
        #                     print("  +" + d, dval, '->', dren, tags)
        #     return

        # if not dry run:
        call_hook('start_execution', macro=self)
        self.open_script_log(mode='map')

        xct = self.executor(no_threads)
        xct.execute(self.expand)

        call_hook('finish_execution', macro=self)

        self.close_script_log()

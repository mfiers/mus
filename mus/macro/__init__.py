

import getpass
import logging
import os
import re
import socket
import time
from datetime import datetime
from io import TextIOWrapper
from pathlib import Path
from typing import Dict, List, Optional, Type
from uuid import uuid4

import click

import mus.macro.elements as mme
from mus.db import Record
from mus.macro.executors import AsyncioExecutor, Executor
from mus.macro.job import MacroJob
from mus.util import msec2nice

lg = logging.getLogger("mus")

MACRO_SAVE_PATH = "~/.local/mus/macro"
WRAPPER_SAVE_PATH = "~/.local/mus/wrappers"


def find_saved_macros() -> Dict[str, str]:
    """Return a list of saved macro's."""
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


def find_saved_wrappers() -> Dict[str, str]:
    """Return a list of saved macro's."""
    save_folder = Path(WRAPPER_SAVE_PATH).expanduser()
    if not save_folder.exists():
        return {}

    rv = {}
    for wrapperfile in save_folder.glob('*.mwr'):
        name = wrapperfile.name[:-4]
        peek = open(wrapperfile).read()
        peek = " ".join(peek.split())[:100]
        rv[name] = peek
    return rv


def load_wrapper(wrapper_name: str) -> str:
    filename = Path(WRAPPER_SAVE_PATH).expanduser() \
            / f"{wrapper_name}.mwr"

    if not filename.exists():
        raise FileExistsError(filename)

    with open(filename) as F:
        return F.read()


class Macro:
    def __init__(self,
                 raw: Optional[str] = None,
                 name: Optional[str] = None,
                 wrapper: Optional[str] = None,
                 dry_run: bool = False,
                 executor: Type[Executor] = AsyncioExecutor
                 ) -> None:

        self._globField = None
        self.executor = executor
        self.wrapper = wrapper
        self.segments: List[mme.MacroElementBase] = []
        self.LogScript: Optional[TextIOWrapper] = None
        self.dry_run = dry_run

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
            message=self.raw,
            rectype="macro")
        self.record.save()

        self.process_raw()


    def explain(self):
        for seg in self.segments:
            print(seg)


    def get_save_file(self, save_name):
        save_folder = Path(MACRO_SAVE_PATH).expanduser()
        if not save_folder.exists():
            save_folder.mkdir(parents=True)
        return save_folder / f"{save_name}.mmc"


    def save(self, save_name):
        save_file = self.get_save_file(save_name)
        with open(save_file, 'wt') as F:
            F.write(self.raw)


    def load(self, name):
        macro_file = self.get_save_file(name)
        lg.debug(f"Load macro from {macro_file}")
        with open(macro_file, 'rt') as F:
            self.raw = F.read()


    @property
    def globField(self):
        return self._globField

    @globField.setter
    def globField(self, value):
        # allow only one glob
        assert self._globField is None
        self._globField = value

    def add_segment(self, subclass, fragment):
        lg.debug(f'segment add {subclass.__name__} {fragment}')
        if len(self.segments) > 0 \
                and subclass == mme.MacroElementText \
                and type(self.segments[-1]) is mme.MacroElementText:
            self.segments[-1].fragment += fragment
        else:
            self.segments.append(subclass(self, fragment))

            if subclass == mme.MacroElementGlob:
                self.globField = self.segments[-1]


    def process_raw(self):
        lg.debug("Parsing raw macro")
        # find patterns:
        up_until = 0
        raw = self.raw

        # parse macro - find expandable parts
        for pat in re.finditer(r'\{.*?\}', raw):
            # store whatever leads up to this match
            self.add_segment(mme.MacroElementText, raw[up_until:pat.start()])

            fragment = raw[pat.start() + 1:pat.end() - 1]

            if fragment[0] in '<>':
                fragtype = fragment[0]
                fragment = fragment[1:]
            elif '*' in fragment:
                # shortcut - if no <> is specified - fragments
                # with a * must be a glob
                fragtype = '<'
            elif '%' in fragment:
                fragtype = '>'
            elif Path(fragment).exists() and self.globField is None:
                # assume a single input file
                fragtype = '<'
            else:
                fragtype = '>'

            if fragtype == '<':
                # input file/files
                self.add_segment(
                    mme.MacroElementGlob, fragment)
            elif fragtype == '>':
                self.add_segment(
                    mme.MacroElementOutput, fragment)
            else:
                raise click.UsageError(f"do not know fragment type {fragtype}")

            up_until = pat.end()

        # ensure the last bit of the macro is added!
        self.add_segment(
            mme.MacroElementText, raw[up_until:])


    def expand(self):
        if self.globField is None:
            lg.debug('Executing singleton command')
            # if there is no glob to expand -
            yield MacroJob(
                cl=self.raw,
                inputfile=None,
                macro=self)
            return

        for fn in self.globField.expand():
            job = MacroJob(inputfile=fn, macro=self)
            _cl = [sg.render(job, fn) for sg in self.segments]
            job.cl = "".join(_cl)
            yield job

    def open_script_log(self, mode):
        assert self.LogScript is None
        env_hostname = os.environ.get('MUS_HOST', "")
        env_user = os.environ.get('MUS_USER', "")
        if env_hostname:
            env_hostname = f" ({env_hostname})"
        if env_user:
            env_user = f" ({env_user})"
        self.LogScript = open("./mus.sh", "a")
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
        self.LogScript.close()
        self.LogScript = None

    def add_to_script_log(self, job):
        ts = datetime.fromtimestamp(job.starttime).strftime("%Y-%m-%d %H:%M")
        self.LogScript.write(f"# Start : {ts} - {job.uid}\n")
        if job.inputfile is not None:
            self.LogScript.write(f"# File  : {job.inputfile}\n")
        self.LogScript.write(f"{job.cl}\n")
        self.LogScript.write(f"# Time  : {job.runtime:.4f}s\n")
        self.LogScript.write(f"# RC    : {job.returncode}\n\n")

    def execute(self, no_threads):

        if self.dry_run:
            for job in self.expand():
                print(job.cl)
                if job.inputfile:
                    print(f"  < {job.inputfile}")
                for o in job.outputfiles:
                    print(f"  > {o}")
            return

        # if not dry run:
        self.open_script_log(mode='map')

        xct = self.executor(no_threads)
        xct.execute(self.expand)

        self.close_script_log()



import getpass
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

import mus.macro.elements as mme
from mus.db import Record
from mus.hooks import call_hook, register_hook
from mus.macro.executors import AsyncioExecutor, Executor
from mus.macro.job import MacroJob
from mus.util import msec2nice

lg = logging.getLogger(__name__)

DEBUG = True
MACRO_SAVE_PATH = "~/.local/mus/macro"
WRAPPER_SAVE_PATH = "~/.local/mus/wrappers"


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


def find_saved_wrappers() -> Dict[str, str]:
    """Find a list of all wrappers saved and return first 100 characters for
    display.

    Returns:
        Dict[str, str]: Dictionary of name: first parts of the wrappers
    """
    save_folder = Path(WRAPPER_SAVE_PATH).expanduser()
    if not save_folder.exists():
        return {}

    rv = {}
    for wrapperfile in save_folder.glob('*.mwr'):
        name = wrapperfile.name[:-4]
        peek = open(wrapperfile, encoding='utf-8').read()
        peek = " ".join(peek.split())[:100]
        rv[name] = peek
    return rv


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


def load_wrapper(wrapper_name: str) -> str:
    filename = Path(WRAPPER_SAVE_PATH).expanduser()
    filename /= f"{wrapper_name}.mwr"

    if not filename.exists():
        raise FileExistsError(filename)

    with open(filename) as F:
        return F.read()


class Macro:
    def __init__(self,
                 raw: Optional[str] = None,
                 name: Optional[str] = None,
                 wrapper: Optional[str] = None,
                 max_no_jobs: int = -1,
                 dry_run: bool = False,
                 executor: Type[Executor] = AsyncioExecutor
                 ) -> None:
        """
        Class representing a macro run

        Args:
            raw (Optional[str], optional): Raw macro string. Defaults to
                None.
            name (Optional[str], optional): Load name of the macro.
                Defaults to None.
            wrapper (Optional[str], optional): Wrapper script name.
                Defaults to None.
            max_no_jobs (Int, optional): Do not run more jobs than this.
                Defaults to -1.
            dry_run (bool, optional): Only print what is to be executed.
                Defaults to False.
            executor (Type[Executor], optional): Executor taking care of
                actual run. Defaults to AsyncioExecutor.
        """
        # self._globField = None

        self.generators = {}

        self.executor = executor
        self.wrapper = wrapper
        self.max_no_jobs = max_no_jobs
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

        call_hook('create_job', job=self)

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

    def add_segment(self,
                    element_class: mme.MacroElementBase,
                    fragment: str,
                    name: Optional[str]):
        """
        Add a segment to this macro.

        Args:
            name (Optional(str)): Name of the element - only relevant for
                generators
            ElementClass (Subclass of MacroElementBase): Type of element
            fragment (str): macro element contents
        """
        lg.debug(f'segment add {element_class} {fragment}')

        # Automatically merge multiple text elements.
        # do this when
        #    - there is already one other element,
        #    - this element is a text element, and
        #    - the previous element is a text element
        if len(self.segments) > 0 \
                and element_class == mme.MacroElementText \
                and type(self.segments[-1]) is mme.MacroElementText:
            self.segments[-1].fragment += fragment
        else:
            # otherwise, simply append the element
            self.segments.append(
                element_class(macro=self,
                              fragment=fragment,
                              name=name))

            if element_class != mme.MacroElementText:
                assert name is not None
                self.generators[name] = self.segments[-1]

    def process_raw(self):
        """
        Process the raw macro string into elements

        Macro language defines a number of element types - indicated by
        the first character in the definition. Note this character can be
        optional

        Generators:

             : glob - if unspecified when a * is in the
            f: read in from a file (space separated)
            r : range

        File tracking:
            < : input file - MacroElementGlob
            > : output file - (MacroElementOutputFile)


        For certain output macro elements the element might need to refer to
        an input element - so - it's possible to name these.

        Input elements are automatically numbered in order of appearance:

            ls {*.txt} {*.py}


        Will have:

            1 : {*.txt}
            2 : {*.py}

        Output elements can refer to specific element.

        Raises:
            click.UsageError: Invalid macro
        """

        lg.debug("Parsing raw macro")
        # find patterns:
        up_until = 0
        raw = self.raw
        generator_number = 0
        # element_name = None

        if re.match(r'\![0-9a-f]+', raw):
            # recognize !`{UID}` as a request to get
            # command back from mus history
            from mus.db import find_by_uid
            rec = find_by_uid(raw.lstrip('!'))
            if rec is None:
                raise click.UsageError('Not found in history')
            raw = self.raw = rec.message

        # parse macro - find expandable parts
        # all expandable parts are inbetween {}
        # types:
        #    < inputfile
        #    > outputfile
        for pat in re.finditer(r'\{.*?\}', raw
                               ):
            # store whatever leads up to this match
            self.add_segment(element_class=mme.MacroElementText,
                             fragment=raw[up_until:pat.start()],
                             name=None)

            # matched fragment excluding {}
            generator_number += 1
            fragment = raw[pat.start() + 1:pat.end() - 1]

            if '&' not in fragment:
                # if functinos are defined - we'll trust the writer
                # to be correct

                if '*' in fragment or '?' in fragment:
                    # assume glob of type {*.txt}
                    fragment += '&glob'  # for ssp
            self.add_segment(element_class=mme.MacroElementSSP,
                             fragment=fragment,
                             name=str(generator_number))

            up_until = pat.end()

        # ensure the last bit of the matcro is added!
        self.add_segment(
            element_class=mme.MacroElementText,
            fragment=raw[up_until:],
            name=None)

    def expand(self):
        if len(self.generators) == 0:
            lg.debug('Executing singleton command')
            # if there is no glob to expand -
            yield MacroJob(
                cl=self.raw,
                data={},
                macro=self)
            return

        generators = [x.expand() for x in self.generators.values()]

        for _ in product(*generators):
            job = MacroJob(macro=self, data=dict(_))
            _cl = [sg.render(job) for sg in self.segments]
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
                #for name, inputfile in job.inputfiles.items():
                #    print(f"  <{name:2} | {inputfile}")
                #for o in job.outputfiles:
                #    print(f"  >   | {o}")
            return

        # if not dry run:
        call_hook('start_execution', macro=self)
        self.open_script_log(mode='map')

        xct = self.executor(no_threads)
        xct.execute(self.expand, max_no_jobs=self.max_no_jobs)

        call_hook('finish_execution', macro=self)

        self.close_script_log()

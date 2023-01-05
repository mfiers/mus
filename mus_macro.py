
import getpass
import logging
import os
import re
import socket
import time
from datetime import datetime
from io import TextIOWrapper
from pathlib import Path
from typing import Dict, List, Optional
from uuid import uuid4

import click

from mus_db import Record
from mus_util import msec2nice

lg = logging.getLogger("mus")

MACRO_SAVE_PATH = "~/.local/mus/macro"


def find_saved_macros() -> Dict[str, str]:
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


class MacroElement():
    """ Base element - just returns the elements as a string"""
    def __init__(self, macro, fragment: str) -> None:
        self.fragment = fragment
        self.macro = macro

    def expand(self):
        raise Exception("Only for glob segments")

    def render(self, filename: Path):
        return self.fragment


def getBasenameNoExtension(filename: Path) -> str:
    rv = filename.name
    if '.' in rv:
        return rv.rsplit('.', 1)[0]
    else:
        return rv


TEMPLATE_ELEMENTS = {
    '%%': lambda x: '%%',
    '%f': lambda x: str(x),
    '%F': lambda x: str(x.resolve()),
    '%n': lambda x: str(x.name),
    '%p': lambda x: str(x.resolve().parent),
    '%P': lambda x: str(x.resolve().parent.parent),
    '%.': getBasenameNoExtension,
}


def resolve_template(
        filename: Path,
        template: str) -> str:
    "Expand a % template based on a filename"
    for k, v in TEMPLATE_ELEMENTS.items():
        template = template.replace(
            k, str(v(filename)))
    return template


class MacroElementTemplate(MacroElement):
    """Expand % in a macro"""
    def render(self, filename: Path) -> str:
        template = self.fragment.lstrip('{').rstrip('}')
        return resolve_template(filename, template)


class MacroElementGlob(MacroElement):
    def expand(self):
        """Convert the glob to a list of files"""
        gfield = self.fragment.lstrip('{').rstrip('}')
        for fn in Path('.').glob(gfield):
            yield fn

    def render(self, filename):
        return str(filename)


class MacroJob:
    def __init__(self,
                 cl: str,
                 inputfile: Optional[Path] = None):
        self.uid = str(uuid4()).split('-')[0]
        self.cl = cl
        self.inputfile = inputfile

    def start(self):
        self.rec = Record()
        self.starttime = self.rec.time = time.time()
        self.rec.prepare()
        self.rec.type = 'macro-exe'

    def stop(self, returncode):
        self.stoptime = time.time()
        self.runtime = self.stoptime - self.starttime
        self.runtimeNice = msec2nice(self.runtime)
        self.returncode = returncode

        self.rec.time = self.starttime
        self.rec.message = self.cl
        self.rec.status = returncode
        self.rec.data['runtime'] = self.runtime
        self.rec.save()


class Macro:
    def __init__(self,
                 raw: Optional[str] = None,
                 name: Optional[str] = None,
                 dry_run: bool = False) -> None:

        self._globField = None
        self.segments: List[MacroElement] = []
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

        self.process_raw()

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

    def process_raw(self):
        lg.debug("Parsing raw macro")
        # find patterns:
        up_until = 0
        no_segments = 0

        raw = self.raw
        parse_fail = False
        # parse macro - find expandable parts
        for pat in re.finditer(r'\{.*?\}|%.', raw):
            no_segments += 1

            # store string segment up until this element
            self.segments.append(
                MacroElement(self, raw[up_until:pat.start()]))

            segment_raw = raw[pat.start():pat.end()]
            if '%' in segment_raw:
                self.segments.append(
                    MacroElementTemplate(self, segment_raw))
            elif '*' in segment_raw:
                self.globField = MacroElementGlob(self, segment_raw)
                self.segments.append(self.globField)
            else:
                lg.warning(f"Unsure about: {segment_raw}")
                parse_fail = True

            up_until = pat.end()

        # ensure the last bit of the macro is added!
        self.segments.append(
            MacroElement(self, raw[up_until:]))

        if parse_fail:
            raise click.UsageError("Failed to parse macro")

    def expand(self):
        if self.globField is None:
            # if there is no glob to expand -
            raise Exception("i don't think this code is ever reached?")
            yield self.raw

        for fn in self.globField.expand():
            _cl = [x.render(fn) for x in self.segments]
            cl = "".join(_cl)
            yield MacroJob(
                cl=cl,
                inputfile=fn)

    def execute(self, no_threads):
        if self.globField is None:
            self.execute_singleton()
        else:
            self.execute_glob(no_threads)

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

    def execute_singleton(self):
        # nothing to expand? Execute the raw string

        if self.dry_run:
            print(self.raw)
        else:
            import subprocess as sp
            self.open_script_log(mode='singleton')
            job = MacroJob(cl=self.raw)
            job.start()
            P = sp.Popen(job.cl, shell=True)
            P.communicate()
            job.stop(P.returncode)
            self.add_to_script_log(job)
            self.close_script_log()

    def execute_glob(self, no_threads):

        if self.dry_run:
            for job in self.expand():
                print(job.cl)
            return

        # if not dry run:
        import asyncio

        self.open_script_log(mode='map')

        # to ensure the max no subprocesses
        sem = asyncio.Semaphore(no_threads)

        async def run_one(job):
            async with sem:
                lg.info(f"Executing {job.uid}: {job.cl}")
                job.start()
                P = await asyncio.create_subprocess_shell(job.cl)
                await P.communicate()
                job.stop(P.returncode)
                self.add_to_script_log(job)
                lg.debug(f"Finished {job.uid}: {job.cl}")

        async def run_all():
            await asyncio.gather(
                *[run_one(job) for job in self.expand()]
            )

        asyncio.run(run_all())
        self.close_script_log()

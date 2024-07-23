
import logging
import time
from pathlib import Path
from typing import TYPE_CHECKING, Any, Dict, List, Optional, Tuple, Union
from uuid import uuid4

from jinja2 import Environment, pass_context

if TYPE_CHECKING:
    import mus.macro

from mus.db import Record
from mus.hooks import call_hook
from mus.util import msec2nice

lg = logging.getLogger("mus")

jenv = Environment()

def basename(x):
    rv = Path(x).name
    if '.' in rv:
        rv = rv.rsplit('.')[0]
    return rv
jenv.filters['basename'] = basename


def resolve(x):
    rv = Path(x).resolve()
    return str(rv)

jenv.filters['resolve'] = resolve

def fmt(x, fmt):
    return fmt.replace('%', x)
jenv.filters['fmt'] = fmt

@pass_context
def input(context, x):
    context['job'].tag_file(x, 'input')
    return x
jenv.filters['input'] = input

@pass_context
def output(context, x):
    context['job'].tag_file(x, 'output')
    return x
jenv.filters['output'] = output


class MacroJob:
    def __init__(self,
                 macro:"mus.macro.Macro",
                 data: dict):

        self.macro = macro
        self.record = Record()

        # internal data for plugins & other systems
        self.sysdata: Dict[str, Any] = {}

        # job data / data on IO elements
        self.data = data

        self.file_meta = {}

        # render the jinja template macro into an executable cl
        self.cl = jenv\
            .from_string(macro.macro)\
            .render(**self.data, job=self)

        self.record.prepare(
            rectype='job',
            message=macro.macro)

        self.record.child_of = self.macro.record.uid

        # these are extraneous input files - should be
        # present, but not taken into account for the
        # mapping.
        self.extrafiles: List[Path] = []
        self.run_advises: List[Tuple[bool, str]] = []

    def tag_file(self, filename, tag):
        if tag not in  self.file_meta:
             self.file_meta[tag] = set()
        self.file_meta[tag].add(filename)

    def prepare(self):
        call_hook('prepare_job', job=self)

    def set_run_advise(self,
                       advise: bool,
                       reason: str):
        """
        Set a run advice.

        Allows plugins to provide suggestions whether or not to run this job.
        For example based on whether outputfiles are newer than input files.

        Args:
            advice (bool): True - run, False - do not run
            reason (str): Why not to run?
        """
        lg.debug(f"set advise to run '{advise}' beacuse '{reason}'")
        self.run_advises.append((advise, reason))

    def get_run_advise(self) -> Tuple[bool, str]:
        final_advise = True
        final_reason_list = []
        for (advise, reason) in self.run_advises:
            final_advise &= advise
            if advise is False:
                final_reason_list.append(reason)
                lg.info(f"Do not run: {reason}")
        return final_advise, ", ".join(final_reason_list)


    def start(self):
        lg.debug("job start")
        self.starttime = self.record.time = time.time()
        call_hook('start_job', job=self)

    def set_returncode(self, returncode):
        self.returncode = self.record.status = returncode

    def stop(self):
        self.stoptime = time.time()
        self.runtime = self.stoptime - self.starttime
        self.runtimeNice = msec2nice(self.runtime)
        call_hook('stop_job', job=self)

        self.record.time = self.starttime
        self.record.message = self.cl
        self.record.data['runtime'] = self.runtime
        self.record.save()

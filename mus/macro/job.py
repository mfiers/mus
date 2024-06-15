
import logging
import time
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple, Union
from uuid import uuid4

import mus.macro
from mus.db import Record
from mus.hooks import call_hook
from mus.util import msec2nice
from mus.util.ssp import Atom

lg = logging.getLogger("mus")


class MacroJob:
    def __init__(self,
                 cl: Optional[str] = None,
                 data: dict = {},
                 macro=None):
        self.macro = macro
        self.record = Record()
        self.record.prepare(
            rectype='job',
            cl=cl)

        # all inputfiles for a job
        # as we need to refer to them later - this is a dict
        # keys are strings: '1', '2', '3', etc...

        #print(f'job {self.record.uid} is child of ', self.macro.record.uid)
        self.record.child_of = self.macro.record.uid
        self.cl = self.record.cl

        # if the macro defined a wrapper - apply it here.
        if self.macro.wrapper:
            self.cl = self.macro.wrapper.format(cl=self.cl)

        # job data / data on IO elements
        self.data = data

        # internal data for plugins & other systems
        self.sysdata: Dict[str, Any] = {}

        # in/output
        self.rendered: Dict[str, Union[Atom, str]] = {}

        # TODO: fix this - move to tag based IO tracking
        self.inputfiles: List[Path] = []
        self.outputfiles: List[Path] = []
        # these are extraneous input files - should be
        # present, but not taken into account for the
        # mapping.
        self.extrafiles: List[Path] = []
        self.run_advises: List[Tuple[bool, str]] = []

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

        # for o in self.outputfiles:
        #     if o.is_file():
        #         orec = Record()
        #         orec.prepare(
        #             filename=o,
        #             rectype='outputfile',
        #             child_of=self.record.uid)
        #         orec.save()

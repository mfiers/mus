
import logging
import time
from pathlib import Path
from typing import List, Optional
from uuid import uuid4

import mus.macro
from mus.db import Record
from mus.hooks import call_hook
from mus.util import msec2nice

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
            message=cl)

        # all inputfiles for a job
        # as we need to refer to them later - this is a dict
        # keys are strings: '1', '2', '3', etc...
        self.inputfiles = {}

        self.outputfiles = []

        self.record.child_of = self.macro.record.uid
        self.cl = cl

        # if the macro defined a wrapper - apply it here.
        if self.macro.wrapper:
            self.cl = self.macro.wrapper.format(cl=self.cl)

        # only one real i nputfile
        self.data = data
        self.outputfiles: List[Path] = []
        # these are extraneous input files - should be
        # present, but not taken into account for the
        # mapping.
        self.extrafiles: List[Path] = []

    def start(self):
        from mus.util.files import get_checksum
        if False and self.inputfile:
            lg.debug(f"job start, map: {self.inputfile}")

            # record input files prior to run - we assume they do not change
            if self.inputfile.is_file():
                self.inputfile_rec = Record()
                self.inputfile_rec.prepare(
                    filename=self.inputfile,
                    rectype='inputfile',
                    child_of=self.record.uid)
                self.inputfile_rec.save()
            elif self.inputfile.is_dir():
                lg.debug(f"No checksum for input folders {self.inputfile}?")
        else:
            lg.debug("job start - singleton")

        lg.debug("refer start")
        lg.debug("job start")
        self.starttime = self.record.time = time.time()
        self.record.type = 'macro-exe'

    def stop(self, returncode):
        self.stoptime = time.time()
        self.runtime = self.stoptime - self.starttime
        self.runtimeNice = msec2nice(self.runtime)
        self.returncode = returncode

        self.record.time = self.starttime
        self.record.message = self.cl
        self.record.status = returncode
        self.record.data['runtime'] = self.runtime
        self.record.save()

        for o in self.outputfiles:
            if o.is_file():
                orec = Record()
                orec.prepare(
                    filename=o,
                    rectype='outputfile',
                    child_of=self.record.uid)
                orec.save()

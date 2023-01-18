
import logging
import time
from pathlib import Path
from typing import List, Optional
from uuid import uuid4

import mus.macro
from mus.db import Record
from mus.util import msec2nice

lg = logging.getLogger("mus")


class MacroJob:
    def __init__(self,
                 cl: Optional[str] = None,
                 inputfile: Optional[Path] = None,
                 macro=None):
        self.macro = macro
        self.record = Record()
        self.record.prepare(
            rectype='job',
            message=cl)
        self.record.child_of = self.macro.record.uid
        self.cl = cl

        # only one real inputfile
        self.inputfile = inputfile
        self.outputfiles: List[Path] = []
        # these are extraneous input files - should be
        # present, but not taken into account for the
        # mapping.
        self.extrafiles: List[Path] = []

    def start(self):
        if self.inputfile:
            lg.debug(f"job start, map: {self.inputfile}")
            if self.inputfile.is_file():
                filerec = Record()
                filerec.prepare(
                    filename=self.inputfile,
                    rectype='inputfile',
                    child_of=self.record.uid)
                filerec.save()
            elif self.inputfile.is_dir():
                self.record.data

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

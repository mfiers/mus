
from pathlib import Path
from typing import List, Optional, Type

from mus.macro.job import MacroJob


class MacroElementBase():
    """ Base element - just returns the elements as a string"""
    def __init__(self, macro, fragment: str) -> None:
        self.fragment = fragment
        self.macro = macro

    def expand(self):
        raise Exception("Only for glob segments")


def getBasenameNoExtension(filename: Path) -> str:
    rv = filename.name
    if '.' in rv:
        return rv.rsplit('.', 1)[0]
    else:
        return rv


TEMPLATE_ELEMENTS = {
    '%%': lambda x: '##PERCENT##PLACEHOLDER##',
    '%f': lambda x: str(x),
    '%F': lambda x: str(x.resolve()),
    '%n': lambda x: str(x.name),
    '%p': lambda x: str(x.resolve().parent),
    '%P': lambda x: str(x.resolve().parent.parent),
    '%.': getBasenameNoExtension, }


def resolve_template(
        filename: Path,
        template: str) -> str:
    """Expand a % template based on a filename."""
    for k, v in TEMPLATE_ELEMENTS.items():
        template = template.replace(
            k, str(v(filename)))
    template = template.replace(
        '##PERCENT##PLACEHOLDER##', '%')
    return template


class MacroElementText(MacroElementBase):
    """Expand % in a macro"""
    def render(self,
               job: Type[MacroJob],
               filename: Path) -> str:
        return resolve_template(filename, self.fragment)

    def __str__(self):
        return f"Text   : '{self.fragment}'"


class MacroElementGlob(MacroElementBase):

    def expand(self):
        """If there is a glob, expand - otherwise assume
           it is just one file"""
        gfield = self.fragment.lstrip('{').rstrip('}')
        for fn in Path('.').glob(gfield):
            yield fn

    def render(self, job, filename):
        job.inputfile = filename
        return str(filename)

    def __str__(self):
        return f"InGlob : '{self.fragment}'"


class MacroElementOutput(MacroElementText):

    def __str__(self):
        return f"Output : '{self.fragment}'"

    def render(self,
               job: Type[MacroJob],
               filename: Path) -> str:
        rv = super().render(job, filename)
        job.outputfiles.append(Path(rv))
        return rv


class MacroElementExtrafile(MacroElementText):

    def __str__(self):
        return f"Output : '{self.fragment}'"

    def render(self,
               job: Type[MacroJob],
               filename: Path) -> str:
        rv = super().render(job, filename)
        job.extrafiles.append(Path(rv))
        return rv

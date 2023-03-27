
import re
from functools import partial
from pathlib import Path
from typing import List, Optional, Type

from mus.macro.job import MacroJob


class MacroElementBase():
    """ Base element - just returns the elements as a string"""
    def __init__(self,
                 macro,
                 fragment: str,
                 name: Optional[str]) -> None:
        self.fragment = fragment
        self.macro = macro
        self.name = name

    def expand(self):
        raise Exception("Only for glob segments")


def getBasenameNoExtension(filename: Path) -> str:
    rv = filename.name
    if '.' in rv:
        return rv.rsplit('.', 1)[0]
    else:
        return rv


def fn_resolver(match: re.Match,
                job: MacroJob,
                resfunc: callable) -> str:
    """
    Helper function to resolve the different filename
    based template functions.

    Args:
        match (re.Match): re match object for template element
        job (MacroJob): Job containing expansion data
        resfunc (callable): Function converting filename

    Returns:
        str: resolved template elements
    """

    mg0 = match.groups()[0]
    matchno = '1' if not mg0 else mg0
    filename = job.data[matchno]
    return resfunc(filename)


# expandable template elements
TEMPLATE_ELEMENTS = [
    ('%([1-9]?)f', lambda x: str(x)),
    ('%([1-9]?)F', lambda x: str(x.resolve())),
    ('%([1-9]?)n', lambda x: str(x.name)),
    ('%([1-9]?)s', lambda x: str(x.stem)),
    ('%([1-9]?)p', lambda x: str(x.resolve().parent)),
    ('%([1-9]?)P', lambda x: str(x.resolve().parent.parent)),
]


def resolve_template(
        template: str,
        job: MacroJob) -> str:
    """
    Expand a % template based on a filename.

    Args:
        template (str): Template to expand
        job (MacroJob): Job containing relevant data

    Returns:
        str: resolved template
    """
    # parse over all template elements
    for rex, resfunc in TEMPLATE_ELEMENTS:
        # Prepare function to expand, pickling with job & resolving function
        resfunc_p = partial(fn_resolver, resfunc=resfunc, job=job)
        template = re.sub(rex, resfunc_p, template)

    return template


class MacroElementText(MacroElementBase):
    """Text based macro element"""

    def render(self,
               job: Type[MacroJob]) -> str:
        """
        Render this text element, expand template

        Args:
            job (Type[MacroJob]): Job

        Returns:
            str: Rendered fragment
        """
        rv = resolve_template(self.fragment, job)
        return rv

    def __str__(self):
        return f"Text   : '{self.fragment}'"


class MacroElementGenerator(MacroElementBase):
    """All elements that are able to expand into a list.
    """
    def expand(self):
        raise NotImplementedError


class MacroElementGlob(MacroElementGenerator):
    """File glob expands into a list of files"""

    def expand(self):
        """If there is a glob, expand - otherwise assume
           it is just one file"""
        gfields = self.fragment.lstrip('{').rstrip('}')
        for gfield in gfields.split(';'):
            for fn in Path('.').glob(gfield):
                yield (self.name, fn)

    def render(self, job):
        filename = job.data[self.name]
        job.inputfile = filename
        return str(filename)

    def __str__(self):
        return f"InGlob : '{self.fragment}'"


class MacroElementOutputFile(MacroElementText):

    def __str__(self):
        return f"Output : '{self.fragment}'"

    def render(self,
               job: Type[MacroJob]) -> str:
        rv = super().render(job)
        job.outputfiles.append(Path(rv))
        return rv


class MacroElementExtrafile(MacroElementText):

    def __str__(self):
        return f"Output : '{self.fragment}'"

    def render(self,
               job: Type[MacroJob]) -> str:
        rv = super().render(job)
        job.extrafiles.append(Path(rv))
        return rv

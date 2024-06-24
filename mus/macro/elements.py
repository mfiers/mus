
import logging
import re
from functools import partial
from pathlib import Path
from typing import Callable, List, Optional, Type, TypeVar, Union

from mus.macro.job import MacroJob
from mus.util import ssp
from mus.util.ssp import Atom

lg = logging.getLogger()

def getBasenameNoExtension(filename: Path) -> str:
    rv = filename.name
    if '.' in rv:
        return rv.rsplit('.', 1)[0]
    else:
        return rv


def fn_resolver(match: re.Match,
                job: MacroJob,
                resfunc: Callable) -> str:
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
    #lg.warning(job.data)
    rv = resfunc(filename)
    return rv


# # expandable template elements (% element)
# TEMPLATE_ELEMENTS = [
#     ('%([1-9]?)', lambda x: str(x)),
#     ('%([1-9]?)f', lambda x: str(x)),
#     ('%([1-9]?)F', lambda x: str(Path(x).resolve())),
#     ('%([1-9]?)n', lambda x: str(Path(x).name)),
#     ('%([1-9]?)s', lambda x: str(Path(x).stem)),
#     ('%([1-9]?)S', lambda x: str(Path(Path(x).stem).stem)),
#     ('%([1-9]?)p', lambda x: str(Path(x).resolve().parent)),
#     ('%([1-9]?)P', lambda x: str(Path(x).resolve().parent.parent)),
# ]


def resolve_template(
        template: Union[str, Atom],
        job: MacroJob) -> Union[str, Atom]:
    """
    Expand a % template based on a filename.

    Args:
        template (str|Atom): Template to expand
        job (MacroJob): Job containing relevant data

    Returns:
        str|Atom: resolved template
    """

    # parse over all expandable %\d elements
    print('      ## 111 finding exp', template)
    find_expansion = re.search(r'^%([0-9]+)', template)
    if find_expansion:
        print('      ## found!')
        fno = find_expansion.groups()[0]
        replace_by = job.data[fno]
        template = \
            replace_by + template[len(find_expansion.group()):]
    print('      ## now: 2', template)

    if isinstance(template, Atom):
        return template.update(template)
    else:
        return template


class MacroElementBase():
    """ Base element - just returns the elements as a string"""
    def __init__(self,
                 macro,
                 fragment: str,
                 name: str,
                 expandable: bool = False,
                 ) -> None:
        self.fragment = fragment
        self.expandable = expandable
        self.macro = macro
        self.name = name

        self.expanded = False

    def render(self,
               job: MacroJob) -> str:
        raise NotImplementedError

    def expand(self):
        raise NotImplementedError


class MacroElementText(MacroElementBase):
    """Just a piece of text

        This used to expand %f elements - but I'm not sure
        that is a good idea
    """

    def render(self,
               job: MacroJob) -> str:
        """
        Render this text element, expand template

        Args:
            job (Type[MacroJob]): Job

        Returns:
            str: Rendered fragment
        """
        #return resolve_template(self.fragment, job)
        return self.fragment

    def __str__(self):
        return f"Text   : '{self.fragment}'"


class MacroElementSSP(MacroElementText):

    def __str__(self):
        return f"Output : '{self.fragment}'"

    def render(self,
               job: MacroJob) -> str:

        rv = list(ssp.SSP(self.fragment).stack)
        print('x' * 80)
        print(rv)

#             print(self.fragment)
#             fragment = resolve_template(self.fragment, job)

#             ssp_expand = ssp.SSP(self.fragment, )
#             exit()
# #                        ssp_expand = ssp.SSP(self.fragment, )

            #result = from [(self.name, x)
            #            for x in ssp_expand.stack]

        # lg.warning(f'render {self.fragment}')
        item = job.data[self.name]
        item = resolve_template(item, job)
        job.rendered[self.name] = item
        return item

    def expand(self):
        if not self.expandable:
            yield (self.name, self)
        else:
            ssp_expand = ssp.SSP(self.fragment, )
            yield from [(self.name, MacroElementText(macro=self.macro,
                                                     name=self.name,
                                                     fragment=str(x),
                                                     expandable=False))
                        for x in ssp_expand.stack]

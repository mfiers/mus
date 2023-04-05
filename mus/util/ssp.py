
#  *.py|g - glob(*.py)
#

import re
from functools import partial
from pathlib import Path
from typing import Callable, Dict, List


class Atom(str):
    pass


def glob(stack: list) -> None:
    """
    Expand the last item of the stack as a path glob

    >>> stack = ["./test/data/*.txt"]
    >>> glob(stack)
    >>> 'test/data/test01.txt' in stack
    True
    >>> 'test/data/test02.txt' in stack
    True
    >>> 'test/data/test03.txt' in stack
    False


    Args:
        stack (list): Stack
    """
    assert len(stack) > 0
    expand = map(str, Path('.').glob(str(stack.pop())))
    stack.extend(list(expand))


def stringfilter(
        stack: List[Atom],
        negative: bool = False) -> None:
    """
    Filter a list based on the presenence of a
    filter string

    >>> stack="a|b|c|ab|b".split('|')
    >>> len(stack) == 5
    True
    >>> stringfilter(stack)
    >>> stack
    ['b', 'ab']
    >>> stack="a|b|c|ab|b".split('|')
    >>> stringfilter(stack, negative=True)
    >>> stack
    ['a', 'c']


    Args:
        stack (list): Stack to filter
        negative (bool): keep all items with the substring (False)
            or remove (True)
    """
    assert len(stack) > 2
    filter_string = stack.pop()
    atoms_to_delete = []

    for stack_item in stack:
        if negative:
            # remove all matching items
            if filter_string in stack_item:
                atoms_to_delete.append(stack_item)
        else:
            # remove all non matching items
            if filter_string not in stack_item:
                atoms_to_delete.append(stack_item)

    for tod in atoms_to_delete:
        while tod in stack:
            stack.remove(tod)


class SSP:
    """
    Simple String Stack Processor


    >>> x=SSP("test/data/*.txt&g")
    >>> 'test/data/test01.txt' in x.stack
    True
    >>> len(x.stack) == 4
    True
    >>> x=SSP("test/data/*.txt&g|other&f")
    >>> 'test/data/other01.txt' in x.stack
    True
    >>> x=SSP("test/data/*.txt&g|other&f")
    >>> 'test/data/other01.txt' in x.stack
    True

    """
    def __init__(self, raw: str):
        self.raw = raw
        self.stack: List[Atom] = []

        self.functions: Dict[str, Callable] = dict(
            g=glob,
            f=stringfilter,
            r=partial(stringfilter, negative=True), )

        up_until = 0
        next_is_func = False

        def process_item(next_is_func, last_element):
            if next_is_func:
                func = self.functions[last_element]
                func(self.stack)
            else:
                self.stack.append(Atom(last_element))

        for sepa in re.finditer(r'([\|&])', self.raw):
            last_element = self.raw[up_until:sepa.start()]
            process_item(next_is_func, last_element)
            next_is_func = sepa.group() == '&'
            up_until = sepa.end()

        last_element = self.raw[up_until:].strip()
        if last_element:
            if next_is_func:
                self.functions[last_element](self.stack)
            else:
                self.stack.append(Atom(last_element))

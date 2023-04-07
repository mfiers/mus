
#  *.py|g - glob(*.py)
#

import re
from functools import partial, wraps
from pathlib import Path
from typing import Callable, Dict, List


class Atom(str):
    pass


SSP_FUNCTIONS: Dict[str, Callable] = {}


def register_function(name):
    def register_decorator(func):
        @wraps(func)
        def wrapped_function(*args, **kwargs):
            return func(*args, **kwargs)
        SSP_FUNCTIONS[name] = func
        return wrapped_function
    return register_decorator


@register_function('read')
def open_file(stack: list) -> None:
    """
    Open file & read space separated contents

    Args:
        stack (list): Stack
    """
    assert len(stack) > 0
    to_open = stack.pop()
    to_inject = open(to_open).read().split()
    stack.extend(to_inject)


@register_function('glob')
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

@register_function('filter')
def string_filter(
        stack: List[Atom],
        negative: bool = False) -> None:
    """
    Filter a list based on the presenence of a
    filter string

    >>> stack="a|b|c|ab|b".split('|')
    >>> len(stack) == 5
    True
    >>> string_filter(stack)
    >>> stack
    ['b', 'ab']
    >>> stack="a|b|c|ab|b".split('|')
    >>> string_filter(stack, negative=True)
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

@register_function('remove')
def string_filter_discard(stack: List[Atom]):
    return string_filter(stack, negative=True)


class SSP:
    """
    Simple String Stack Processor


    >>> x=SSP("test/data/*.txt&glob")
    >>> 'test/data/test01.txt' in x.stack
    True
    >>> len(x.stack) == 4
    True
    >>> x=SSP("test/data/*.txt&glob|other&filter")
    >>> 'test/data/other01.txt' in x.stack
    True
    >>> x=SSP("test/data/*.txt&glob|other&filter")
    >>> 'test/data/other01.txt' in x.stack
    True

    """
    def __init__(self, raw: str):
        self.raw = raw
        self.stack: List[Atom] = []

        up_until = 0
        next_is_func = False

        def process_item(next_is_func, last_element):
            if next_is_func:
                func = SSP_FUNCTIONS[last_element]
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
            process_item(next_is_func, last_element)


#print(SSP('*&g').stack)

#  *.py|g - glob(*.py)
#

import re
from functools import partial, wraps
from pathlib import Path
from typing import Callable, Dict, List


class Atom(str):

    def tag(self, tag):
        if not hasattr(self, '_mus_tag'):
            self._mus_tag = set()
        self._mus_tag.add(tag)

    def has_tag(self, tag) -> bool:
        if not hasattr(self, '_mus_tag'):
            return False
        if tag in self._mus_tag:
            return True
        return False

    def update(self, new_string):
        """Create a new string updating atom tags"""
        new = Atom(new_string)
        new._mus_tag = getattr(self, '_mus_tag', set())
        return new

    def tagstr(self):
        """Return a formatted string with all tags"""
        return "|" + "|".join(sorted(getattr(self, '_mus_tag', []))) + "|"


def _isstack(stack) -> bool:
    """Check if all items are Atom

    >>> _isstack([Atom('a'), Atom('b')])
    True
    >>> _isstack([Atom('a'), 'b'])
    False
    """

    for s in stack:
        if not isinstance(s, Atom):
            return False
    return True


def _stackify(*stack) -> List[Atom]:
    """Convert a list of strings to a list of atoms

    >>> r = _stackify('a', 'b')
    >>> isinstance(r[0], Atom)
    True
    >>> isinstance(r[1], Atom)
    True
    >>> r2 = _stackify(*r)
    >>> isinstance(r2[0], Atom)
    True
    >>> isinstance(r2[1], Atom)
    True
    """

    rv = list(map(Atom, stack))
    return rv


def _cmd2stack(cmd) -> List[Atom]:
    """Convert an ssp cmd string to a list of Atoms

    >>> stack = _cmd2stack("a|b|c")
    >>> stack
    ['a', 'b', 'c']
    >>> _isstack(stack)
    True

    """

    return _stackify(*re.split(r'[\|&]', cmd))


SSP_FUNCTIONS: Dict[str, Callable] = {}


def register_function(name):
    def register_decorator(func):
        @wraps(func)
        def wrapped_function(*args, **kwargs):
            return func(*args, **kwargs)
        if name in SSP_FUNCTIONS:
            raise Exception("can not register SSP function - exists")

        SSP_FUNCTIONS[name] = func
        return wrapped_function
    return register_decorator


@register_function('tag')
def tag_atom(stack: List[Atom]) -> None:
    """Tag the stacked atoms - for later processing

    >>> stack = _cmd2stack("a|b|c|testtag")
    >>> tag_atom(stack)
    >>> stack
    ['a', 'b', 'c']
    >>> stack[0].has_tag('testtag')
    True
    >>> stack[2].has_tag('testtag')
    True
    >>> stack[2].has_tag('not_testtag')
    False
    """
    assert len(stack) >= 2
    tag = stack.pop()
    [x.tag(tag) for x in stack]


@register_function('input')
def tag_atom_input(stack: List[Atom]) -> None:
    """Tag the stacked atoms as input - for later processing

    >>> stack = _cmd2stack("a|b|c")
    >>> tag_atom_input(stack)
    >>> stack
    ['a', 'b', 'c']
    >>> stack[0].has_tag('input')
    True
    >>> stack[2].has_tag('input')
    True
    >>> stack[2].has_tag('not_input')
    False
    """
    [x.tag('input') for x in stack]


@register_function('output')
def tag_atom_output(stack: List[Atom]) -> None:
    """Tag the stacked atoms as input - for later processing

    >>> stack = _cmd2stack("a|b|c")
    >>> tag_atom_output(stack)
    >>> stack
    ['a', 'b', 'c']
    >>> stack[0].has_tag('output')
    True
    >>> stack[2].has_tag('output')
    True
    >>> stack[2].has_tag('input')
    False
    """
    [x.tag('output') for x in stack]


@register_function('read')
def open_file(stack: list) -> None:
    """
    Open file & read space separated contents

    >>> stack = _stackify("./test/data/file_list")
    >>> open_file(stack)
    >>> stack
    ['./test/data/test01.txt', './test/data/other02.txt']
    >>> _isstack(stack)
    True

    Args:
        stack (list): Stack
    """
    assert len(stack) > 0
    to_open = stack.pop()
    to_inject = map(Atom, open(to_open).read().split())
    stack.extend(to_inject)


@register_function('glob')
def glob(stack: list) -> None:
    """
    Expand the last item of the stack as a path glob

    >>> stack = [Atom("./test/data/*.txt")]
    >>> glob(stack)
    >>> 'test/data/test01.txt' in stack
    True
    >>> 'test/data/test02.txt' in stack
    True
    >>> 'test/data/test03.txt' in stack
    False
    >>> _isstack(stack)
    True


    Args:
        stack (list): Stack
    """
    assert len(stack) > 0
    expand = map(Atom, Path('.').glob(str(stack.pop())))
    stack.extend(expand)


@register_function('filter')
def string_filter(
        stack: List[Atom],
        negative: bool = False) -> None:
    """
    Filter a list based on the presenence of a
    filter string

    >>> stack=_cmd2stack("a|b|c|ab|b")
    >>> len(stack) == 5
    True
    >>> string_filter(stack)
    >>> stack
    ['b', 'ab']
    >>> stack=_cmd2stack("a|b|c|ab|b")
    >>> string_filter(stack, negative=True)
    >>> stack
    ['a', 'c']
    >>> _isstack(stack)
    True

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
    """Opposite of filter - remove all matching strings

    >>> stack=_cmd2stack("a|b|c|ab|b")
    >>> string_filter_discard(stack)
    >>> stack
    ['a', 'c']
    >>> _isstack(stack)
    True

    """
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

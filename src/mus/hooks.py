"""
Super simple hooks system

register hooks anywhere - later call them.

Example:

    def my_hook(a, b):
        print(a, b)

    register_hook("my_hook", my_hook)

And from anywhere else in the code:
    call_hook("my_hook", a=1, b=2)

this package exposes a few hooks, defined in the modules.
"""

import logging
from collections import defaultdict

lg = logging.getLogger(__name__)

# store hooks for later execution
HOOKS: dict = defaultdict(list)


def register_hook(name: str,
                  func,
                  priority: int = 10):
    """
    Register a hook to be called later

    Args:
        name (str): Name of the hook to associate the function with
        func (callable): function to call
        priority (int, optional): Priority - higher goes first. Defaults to 10.
    """
    HOOKS[name].append(
        (priority, func))


def call_hook(name: str,
              **kwargs):
    """
    Call a pre-registred hook.

    Args:
        name (str): Name of the hook.
        kargs: remainer of arguments passed to the hook.
    """
    for priority, func in sorted(HOOKS.get(name, []), key=lambda x: -x[0]):

        lg.debug(
            f"Call hook {func.__name__} with priority {priority}")
        func(**kwargs)

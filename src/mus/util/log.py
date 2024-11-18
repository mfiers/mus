
import logging
import select
import sys
from collections.abc import Iterable
from typing import List

import click


class ColorFormatter(logging.Formatter):
    # Change this dictionary to suit your coloring needs!

    def format(self, record):

        # prevents import unless used
        from colorama import Back, Fore

        from mus.util import msec2nice

        colors = {
            "WARNING": Fore.RED,
            "ERROR": Fore.RED + Back.WHITE,
            "DEBUG": Fore.LIGHTBLACK_EX,
            "INFO": Fore.GREEN,
            "CRITICAL": Fore.RED + Back.WHITE
        }

        level_short = dict(
            WARNING='W',
            ERROR='E',
            INFO='I',
            DEBUG='D',
            CRITICAL='C'
        )

        rc = record.relativeCreated
        record.relativeCreatedStr = '(' + msec2nice(rc) + ')'

        color = colors.get(record.levelname, "")
        record.levelShort = color + level_short[record.levelname]

        if color:
            record.name = color + record.name
            record.msg = color + record.msg
            record.relativeCreatedStr = \
                Fore.LIGHTBLACK_EX + record.relativeCreatedStr
        return logging.Formatter.format(self, record)


class ColorLogger(logging.Logger):
    def __init__(self, name):
        logging.Logger.__init__(self, name)
        color_formatter = ColorFormatter(
            "%(levelShort)s %(message)s %(relativeCreatedStr)s",
        )
        console = logging.StreamHandler()
        console.setFormatter(color_formatter)
        self.addHandler(console)


def read_nonblocking_stdin() -> str:
    # check if there's something to read
    if select.select([sys.stdin], [], [], 0)[0]:
        # reads everything from stdin
        return sys.stdin.read().strip()
    else:
        # if nothing is there, return None
        return ""


def get_message(message: List[str] | str | None,
                editor: bool = False) -> str:

    if message is None:
        message_ = ""
    elif isinstance(message, str):
        message_ = message
    elif isinstance(message, Iterable):
        message_ = ' '.join(map(str, message))
    else:
        message_ = str(message)

    message_ = message_.strip()

    if message_ == "":
        # no message? something on stdin?
        message_ = read_nonblocking_stdin().strip()
    if message_ == "" or editor:
        # drop into editor
        _ = click.edit(message_)
        assert isinstance(_, str)
        message_ = _.strip()

    return message_
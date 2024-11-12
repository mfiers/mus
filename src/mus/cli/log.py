
import select
import sys
from typing import List

import click

from mus.db import Record


def read_nonblocking_stdin() -> str:
    # check if there's something to read
    if select.select([sys.stdin], [], [], 0)[0]:
        # reads everything from stdin
        return sys.stdin.read().strip()
    else:
        # if nothing is there, return None
        return ""


# Note - the **kwargs is required to ad arguments
# later on the fly (in a hook!)
@click.command("log")
@click.argument("message", nargs=-1)
def log(message: List[str], **kwargs):
    """Store a log message"""
    message_ = " ".join(message).strip()
    if message_ == "":
        message_ = read_nonblocking_stdin().strip()
    if message_ == "":
         message_ = click.edit().strip()
    if message == "":
        click.echo('No message specified')
        return

    rec = Record()
    rec.prepare()
    rec.message = message_
    rec.type = 'log'
    rec.save()


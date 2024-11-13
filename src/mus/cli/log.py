
from typing import List

import click

from mus.db import Record
from mus.util.log import get_message


# Note - the **kwargs is required to ad arguments
# later on the fly (in a hook!)
@click.command("log")
@click.argument("message", nargs=-1)
def log(message: List[str], **kwargs):
    """Store a log message"""

    message_ = get_message(message)
    if message_ == "":
        click.echo("Please provide a message")
        return

    rec = Record()
    rec.prepare()
    rec.message = message_
    rec.type = 'log'
    rec.save()


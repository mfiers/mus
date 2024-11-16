
from typing import List

import click

from mus.db import Record
from mus.util.log import get_message


# Note - the **kwargs is required to ad arguments
# later on the fly (in a hook!)
@click.command("log")
@click.option('-e', '--editor', is_flag=True,
              default=False,
              help='Always drop into editor')
@click.argument("message", nargs=-1)
def log(message: List[str], editor: bool, **kwargs):
    """Store a log message"""

    message_ = get_message(message, editor=editor)
    if message_ == "":
        click.echo("Please provide a message")
        return

    rec = Record()
    rec.prepare()
    rec.message = message_
    rec.type = 'log'
    rec.save()

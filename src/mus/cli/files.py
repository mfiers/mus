
import logging
import time
from pathlib import Path
from typing import List

import click
from click import echo, style

from mus.db import Record, get_db_connection
from mus.util.log import get_message

lg = logging.getLogger(__name__)


@click.command("tag")
@click.option('-e', '--editor', is_flag=True,
              default=False, help='Always drop into editor')
@click.argument("filename")
@click.argument("message", nargs=-1)
def filetag(filename: str, message: List[str],
            editor: bool, **kwargs):
    """Add a tag record to the specified file"""

    message_ = get_message(message, editor=editor)
    if message_ == "":
        click.echo("Please provide a message")
        return

    rec = Record()
    rec.prepare(
        filename=Path(filename),
        rectype='tag')
    rec.message = message_
    rec.save()


@click.command("file")
@click.argument("filename")
def findfile(filename):

    import mus.util
    from mus.util.files import get_checksum
    db = get_db_connection()

    filename = Path(filename).resolve()


    echo(f"Checking file {filename}")
    if filename.is_dir():
        # TODO: Do I need to more here?
        return

    checksum = get_checksum(filename)
    echo(f"Checksum: {checksum}")


    sql = """
        select *
        from muslog
        where filename = ?
          OR checksum = ?
        order by time desc
        limit 10 """

    result = db.execute(sql, [str(filename), checksum])
    for rec in result.fetchall():
        rtime = mus.util.msec2nice(1000 * (time.time() - rec.time))
        if Path(rec.filename) == Path(filename):
            same_path_marker = 'p'
            dpath = ''
        else:
            same_path_marker = '.'
            dpath = style(rec.filename,
                          fg='white', bold=False)
        if rec.checksum == checksum:
            same_checksum_marker = 'c'
        else:
            same_checksum_marker = '.'

        rtype = mus.util.format_type_short(rec.type)
        message = style(rec.message, fg='white', bold=True)

        echo(f"{rtype}{same_checksum_marker}{same_path_marker} "
             f"{rtime:>7} {message} {dpath}")

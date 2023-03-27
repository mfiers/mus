
import time
from pathlib import Path

import click
from click import echo, style

from mus.db import Record, get_db_connection


@click.command("tag")
@click.argument("filename")
@click.argument("message", nargs=-1)
def tag(filename, message):
    """Add a tag record to the specified file"""

    message = " ".join(message).strip()
    if not message:
        echo("Must provide a message")
        return

    rec = Record()
    rec.prepare(
        filename=Path(filename),
        rectype='tag')
    rec.message = message
    rec.save()


@click.command("file")
@click.argument("filename")
def file_(filename):

    import mus.util
    from mus.util.files import get_checksum
    db = get_db_connection()

    filename = Path(filename).resolve()
    checksum = get_checksum(filename)

    echo(f"Checking file {filename}")
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

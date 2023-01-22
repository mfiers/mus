
import click

from mus.db import get_db_path


# DB relat
@click.group()
def db():
    pass


@db.command("path")
def db_path():
    print(get_db_path())

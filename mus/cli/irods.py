import logging
import time
from pathlib import Path

import click
from click import echo, style

from mus.db import Record, get_db_connection


@click.group
def irods():
    pass



@click.command("ls")
def irods_ls():
    print('x')
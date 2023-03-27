
from dataclasses import fields

import click

from mus.db import find_by_uid, get_db_path


# DB relat
@click.group()
def db():
    pass


@db.command("path")
def db_path():
    print(get_db_path())


@db.command("uid")
@click.argument('uid')
def db_by_uid(uid):
    rec = find_by_uid(uid)
    if rec is None:
        exit(1)
    for k in fields(rec):
        print(k.name, getattr(rec, k.name), sep="\t")


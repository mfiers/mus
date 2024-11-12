
import os
import shutil
import time
from dataclasses import asdict, fields

import click

from mus.db import find_by_uid, get_db_connection, get_db_path
from mus.util import msec2nice


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


@db.command("head")
def db_head():
    nocols = 4
    conn = get_db_connection()
    sql = f'''
        SELECT * FROM muslog
        ORDER BY time DESC
        LIMIT {nocols}
    '''
    idx = []
    cols = []

    twidth = shutil.get_terminal_size((80, 24)).columns
    mcw = int(twidth / (nocols + 1)) - nocols

    for i, rec in enumerate(conn.execute(sql)):
        recd = asdict(rec)
        nidx, ncol = [], []

        for key in sorted(recd.keys()):

            nidx.append(key)
            if key in ['uid', 'child_of']:
                ncol.append(str(recd[key]).split('-')[0])
            elif key == 'cwd':
                ncol.append(str(recd[key]).replace(
                    os.path.expanduser("~"), "~"))
            elif key == 'time':
                dtime = time.time() - recd[key]
                ncol.append(msec2nice(dtime * 1000))
            else:
                ncol.append(str(recd[key]))

        if len(idx) == 0:
            idx = nidx

        cols.append(ncol)

    cols = [idx] + cols
    maxcolswidth = [max(map(len, col))
                    for col in cols]

    while sum(maxcolswidth) > len(cols) * mcw:
        _n = []
        if max(maxcolswidth) > mcw:
            for colwidth in maxcolswidth:
                if colwidth > mcw:
                    _n.append(colwidth - 1)
                else:
                    _n.append(colwidth)
            maxcolswidth = _n

    for i, row in enumerate(cols[0]):
        mrw = maxcolswidth[0]
        fstr = '{row:>' + str(mrw) + '}|'
        print(fstr.format(row=row[:mrw]), end='')
        for j, col in enumerate(cols[1:]):
            mrw = maxcolswidth[j + 1]
            fstr = '{row:>' + str(mrw) + '}|'
            print(fstr.format(row=col[i][:mrw]), end='')
        print()

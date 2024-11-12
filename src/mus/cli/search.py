
import os
import time
from collections import defaultdict
from pathlib import Path

import click

from mus.db import get_db_connection
from mus.util import get_host


@click.command("search")
@click.argument('filter_str', nargs=-1)
@click.option('--remove', is_flag=True, default=False)
@click.option('-t', '--tree', is_flag=True, default=False)
@click.option('-r', '--recursive', is_flag=True, default=False,
              help=('Use with path specifications to get recursive'
                    'search results'))
@click.option('-h', '--host')
@click.option('-u', '--user')
@click.option('-U', '--uid')
@click.option('-a', '--age')
@click.option('-p', '--project',
              help="Filter on project")
@click.option('-f', '--full', default=False, is_flag=True,
              help='Full output')
@click.option('-n', '--no', type=int, default=20,
              help='No results to show')
def cmd_search(filter_str, uid, tree, host, user, age, project, no, full,
               remove, recursive):
    "Search the mus database."

    from datetime import datetime

    db = get_db_connection()

    if uid is not None:
        if remove:
            sql = f"""DELETE FROM muslog WHERE uid LIKE "{uid}%" """
            query = db.execute(sql)
            db.commit()
            return

        sql = f"""
        SELECT uid, host, cwd, user, time, type,
            message, status, data,
            child_of, checksum, filename, cl
        FROM muslog
        WHERE uid LIKE "{uid}%"
        ORDER BY time desc
        LIMIT 1
        """
        query = db.execute(sql)

        rec = query.fetchone()

        mfl = max([
            len(x) for x in rec.__dataclass_fields__])
        fmt_string = '{:%ds} {}' % mfl
        for fld in rec.__dataclass_fields__:
            if hasattr(rec, fld):
                val = getattr(rec, fld)
                if fld == 'data' and not val:
                    continue
                if fld == 'time':
                    val = datetime.fromtimestamp(val)

                print(fmt_string.format(
                    fld, val))
        return

    if remove:
        sql = "DELETE FROM muslog"
    else:
        sql = """
            SELECT uid, host, cwd, user, time, type,
                message, status, data,
                cl, child_of
            FROM muslog
            """

    where_elements = []
    sqlargs = []

    if filter_str:
        # TODO: search for files
        # special case - if filter_str points to a path
        filter_str_path = Path(" ".join(filter_str))
        if filter_str_path.exists() \
                and filter_str_path.is_dir():
            where_elements.append("`host` = ?")
            sqlargs.append(get_host())
            if recursive:
                where_elements.append("`cwd` LIKE ?")
                sqlargs.append(os.getcwd() + '%')
            else:
                where_elements.append("`cwd` = ?")
                sqlargs.append(os.getcwd())

        else:
            # search for individual words independently
            for fs in [x.strip() for x in filter_str]:
                if fs:
                    where_elements.append("`message` LIKE ?")
                    sqlargs.append("%" + fs + "%")

    if host:
        where_elements.append("`host` LIKE ?")
        sqlargs.append("%" + host + "%")

    if user:
        where_elements.append("user LIKE ?")
        sqlargs.append("%" + user + "%")

    if project:
        where_elements.append("project LIKE ?")
        sqlargs.append("%|" + project + '|%')

    if age:
        age = age.strip()
        if age.endswith('d'):
            delta_age = float(age[:-1]) * 60 * 60 * 24
        elif age.endswith('h'):
            delta_age = float(age[:-1]) * 60 * 60
        elif age.endswith('m'):
            delta_age = float(age[:-1]) * 60
        else:
            delta_age = float(age)
        where_elements.append("time > ?")
        sqlargs.append(time.time() - delta_age)

    if where_elements:
        sql += " WHERE " + " AND ".join(where_elements)

    if remove:
        print(sql)
        db.execute(sql, sqlargs)
        db.commit()
        return

    sql += f"""
        ORDER BY time DESC
        LIMIT {no+100} """

    query = db.execute(sql, sqlargs)

    allrecs = list(query.fetchall())

    if tree:
        treedata = defaultdict(list)
        for a in allrecs:
            if a.child_of is not None:
                treedata[a.child_of].append(a.uid)

        def recprint(children_of=None, depth=0, i=0):
            for rec in allrecs:
                if rec.child_of == children_of:
                    i += 1
                    if i > no:
                        return i
                    print(rec.nice(full=full, depth=depth))
                    if rec.uid in treedata:
                        i = recprint(children_of=rec.uid, depth=depth+1, i=i)
            return i
        recprint()
    else:
        for i, rec in enumerate(allrecs):
            rec = allrecs[i]
            print(rec.nice(full=full))
            if i > no:
                return

    return

    # old code - for the time being not storing bash history...
    #
    i = 1


    no_print = 0
    while True:
        if no_print > no:
            break
        if i >= len(allrecs):
            break

        rec = allrecs[i - 1]
        j = i
        while allrecs[j].message == rec.message:
            if j == len(allrecs) - 1:
                break
            j += 1
        no_rep = j - i + 1
        print(rec.nice(no_rep=no_rep, full=full))
        no_print += 1
        i = j + 1

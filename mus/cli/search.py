
import time

import click

from mus.db import get_db_connection


@click.command("search")
@click.argument('filter_str', nargs=-1)
@click.option('-h', '--host')
@click.option('-u', '--user')
@click.option('-a', '--age')
@click.option('-p', '--project',
              help="Filter on project")
@click.option('-f', '--full', default=False, is_flag=True,
              help='Full output')
@click.option('-n', '--no', type=int, default=20,
              help='No results to show')
def cmd_search(filter_str, host, user, age, project, no, full):
    "Search the mus database."
    filter_str_2 = " ".join(filter_str).strip()

    db = get_db_connection()
    sql = """
        SELECT host, cwd, user, time, type,
            message, status, project, tag, data
        FROM muslog
        """

    where_elements = []
    sqlargs = []
    if filter_str:
        where_elements.append("`message` like ?")
        sqlargs.append("%" + filter_str_2 + "%")

    if host:
        where_elements.append("`host` like ?")
        sqlargs.append("%" + host + "%")

    if user:
        where_elements.append("user like ?")
        sqlargs.append("%" + user + "%")

    if project:
        where_elements.append("project like ?")
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
        sql += "WHERE " + " AND ".join(where_elements)

    sql += f"""
        ORDER BY time DESC
        LIMIT {no+100} """

    query = db.execute(sql, sqlargs)

    allrecs = list(query.fetchall())

    # Iterate through the results to reduce duplicates.
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

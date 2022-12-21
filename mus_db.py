
import os
import sqlite3


class RECORD():
    def __str__(self):
        return (
            f"{self.host} {self.user} {int(self.time)} "
            f"{self.message}"
        )


def record_factory(cursor, row):
    rv = RECORD()
    for idx, col in enumerate(cursor.description):
        setattr(rv, col[0], row[idx])
    return rv


def get_db_connection() -> sqlite3.Connection:
    """ Return a database connection.
    Create if it does not exist
    """

    cache_folder = os.path.expanduser('~/.cache')
    db = os.path.join(cache_folder, 'mus.db')
    if os.path.exists(db):
        conn = sqlite3.connect(db)

    else:
        if not os.path.exists(cache_folder):
            os.makedirs(cache_folder)

        conn = sqlite3.connect(db)
        conn.execute("""
        CREATE TABLE IF NOT EXISTS muslog (
            host TEXT,
            user TEXT,
            time INTEGER,
            type TEXT,
            message TEXT,
            status INTEGER,
            project TEXT,
            tag TEXT
            )""")
        conn.commit()

    conn.row_factory = record_factory
    return conn

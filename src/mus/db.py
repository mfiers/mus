
import getpass
import logging
import os
import sqlite3
import time
from dataclasses import dataclass
from pathlib import Path
from textwrap import wrap
from typing import Optional

from mus.hooks import call_hook
from mus.util import get_host, msec2nice

lg = logging.getLogger(__name__)


def record_factory(cursor, row):
    rv = Record()
    for idx, col in enumerate(cursor.description):
        if col[0] == 'data':
            val = row[idx]
            if val is not None:
                import json
                val = json.loads(row[idx])
                setattr(rv, col[0], val)
            else:
                setattr(rv, col[0], {})
        else:
            setattr(rv, col[0], row[idx])

    return rv


def get_db_path() -> str:
    mus_folder = Path('~').expanduser() / '.local' / 'mus'
    if not mus_folder.exists():
        mus_folder.mkdir(parents=True)

    return os.path.join(mus_folder, 'mus.db')


def init_muslog_table(conn: sqlite3.Connection):
    "Create the muslog table"
    conn.execute("""
        CREATE TABLE IF NOT EXISTS muslog (
            host TEXT,
            cwd TEXT,
            user TEXT,
            cl TEXT,
            time INTEGER,
            type TEXT,
            message TEXT,
            status INTEGER,
            uid TEXT,
            filename TEXT,
            checksum TEXT,
            child_of TEXT,
            data json
            )""")
    conn.commit()


def init_hashcache_table(conn: sqlite3.Connection):
    """Create the hash cache table.

    Not storing host - assuming the db is host specific
    """

    conn.execute("""
        CREATE TABLE IF NOT EXISTS hashcache (
            filename TEXT,
            mtime INTEGER,
            hash TEXT)""")
    conn.commit()
    conn.execute("""
                 CREATE UNIQUE INDEX IF NOT EXISTS hashcache_filename
                 ON hashcache ( filename ) """)
    conn.commit()


def get_db_connection() -> sqlite3.Connection:
    """ Return a database connection.
    Create if it does not exist
    """
    db = get_db_path()
    if os.path.exists(db):
        conn = sqlite3.connect(db)
    else:
        conn = sqlite3.connect(db)
        init_muslog_table(conn)
        init_hashcache_table(conn)

    conn.row_factory = record_factory
    return conn


def find_by_uid(uid):
    conn = get_db_connection()
    req = conn.execute(
        'SELECT * FROM muslog WHERE uid LIKE ? LIMIT 1', [uid + '%'])
    rec = req.fetchone()
    return rec


@dataclass(init=False)
class Record():

    type: Optional[str]
    message: Optional[str]
    host: str
    cwd: str
    user: str
    status: int
    time: float
    uid: str
    data: dict
    cl: Optional[str] = None
    filename: Optional[str] = None
    checksum: Optional[str] = None
    child_of: Optional[str] = None

    def __init__(self):
        self.data = {}
        self.status = 0

    def __str__(self):

        if hasattr(self, 'host'):
            message = getattr(self, "message", "-")
            assert self.type is not None
            rv = (
                f"{self.type[:3]:3} {self.host} {self.user} {int(self.time)} "
                f"{self.cwd} "
                f"{message}"
            )
        elif hasattr(self, 'hash'):
            rv = f"{self.filename} {getattr(self, 'hash', 'no hash')}"
        else:
            rv = "Unknown record type"

        return rv

    def nice(self,
             no_rep: Optional[int] = None,
             full: bool = False,
             depth: int = 0):
        """Return a colorama (through click) formatted string for this record

        Args:
            no_rep (int, optional): No of times this command was repeated.
                Defaults to None.
            full (float): Return a multi-line full output
            depth (int): add indentation space for tree view
        Returns:
            str: ANSI color formatted string
        """
        import shutil

        from click import style

        terminal_width = shutil.get_terminal_size((80, 24)).columns

        if depth >= 1:
            indent_str = '  ' * (depth-1) + '+->'
        else:
            indent_str = ''

        ntime = msec2nice(1000 * int(time.time() - self.time))
        ntime = style(f"{ntime:>7}", fg=55)
        if self.type == 'history':
            tmark = style('H', fg="green")
        elif self.type == 'log':
            tmark = style('L', bg="blue", fg='black')
        elif self.type == 'tag':
            tmark = style('T', bg="black", fg='cyan')
        elif self.type == 'macro':
            tmark = style('m', fg='red', bold=False)
        elif self.type == 'job':
            tmark = style('j', fg='yellow', bold=False)
        else:
            tmark = style('?', fg='white', bold=False)

        if self.status == 0:
            smark = " "
            smark_long = '       '
        else:
            smark = style("!", bg=196, fg="white")
            smark_long = style(f" {self.status:>5d}!",
                               fg="red", bold=False, bg="black")

        if no_rep is not None and no_rep > 1:
            srep = style(f" ({no_rep}x)", fg="white", bg='black', bold=True)
        else:
            srep = ""

        extra = ""
        if self.type == "job":
            if 'runtime' in self.data:
                rt = msec2nice(self.data["runtime"] * 1000)
                extra = style(f' ({rt})', fg=70)

        if self.uid:
            uid = style(self.uid[:6], fg=240, bold=False)
        else:
            uid = style('????', fg='red', bold=False)
        if full:
            subsequent_indent = ""
        else:
            subsequent_indent = "      "

        message = ''
        if self.cl is not None:
            message += self.cl
        if self.message is not None:
            if message:
                message += '; ' + self.message
            else:
                message = self.message
        if message:
            message = " \\\n".join(
                wrap(message, terminal_width - 20,
                     subsequent_indent=subsequent_indent))
            message = style(message, bold=True)

        if full:
            return (
                f"{tmark}{smark_long} {ntime:>8s} "
                f"| {self.user}@{self.host}:{self.cwd}\n"
                f"{message}{srep}{extra}")
        else:
            return (f"{tmark}{smark}{indent_str} {uid} "
                    + f"{ntime:>8s} {message}{srep}{extra}")

    def prepare(self,
                filename: Optional[Path] = None,
                rectype: Optional[str] = None,
                message: Optional[str] = None,
                cl: Optional[str] = None,
                child_of: Optional[str] = None,
                ):
        """Prepare record with default values

        Note - filename can be a directory as well.

        Returns: None
        """

        from uuid import uuid4

        from mus.util.files import get_checksum

        lg.debug(f'cwd: {os.getcwd()}')

        if filename is None:
            self.filename = None
            self.checksum = None
        elif filename.exists() and filename.is_file():
            self.filename = str(Path(filename).resolve())
            self.checksum = get_checksum(filename)
        elif filename.exists() and filename.is_dir():
            self.filename = str(Path(filename).resolve())
            self.checksum = None
        else:
            raise Exception("Unsure what happened")

        self.child_of = child_of
        self.cl = cl
        self.cwd = os.getcwd()
        self.time = time.time()
        self.type = rectype
        self.uid = str(uuid4())
        self.message = message

        # Gather information!
        self.host = get_host()

        self.user = getpass.getuser()

        call_hook('prepare_record', record=self)

    def add_message(self, message):
        if self.message is None:
            self.message = message
        else:
            self.message = self.message.rstrip("\n") + "\n" + message

    def save(self):

        # to prep save
        call_hook('save_record', record=self)

        import json
        db = get_db_connection()

        rdata = [self.host,
                 self.cwd,
                 self.user,
                 self.time,
                 self.type,
                 self.message,
                 self.status,
                 self.uid,
                 self.filename,
                 self.checksum,
                 self.child_of,
                 self.cl]

        if self.data:
            rdata.append(json.dumps(self.data))
            sql = """INSERT INTO muslog(
                    host, cwd, user, time, type, message, status,
                    uid, filename, checksum, child_of, cl, data)
                    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) """
        else:
            sql = """INSERT INTO muslog(
                    host, cwd, user, time, type, message, status,
                    uid, filename, checksum, child_of, cl)
                    VALUES (?,?,?,?,?,?,?,?,?,?,?,?) """

        db.execute(sql, tuple(rdata))
        db.commit()

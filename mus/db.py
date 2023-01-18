
import os
import sqlite3
import time
from dataclasses import dataclass
from pathlib import Path
from textwrap import wrap
from typing import Optional

from .util import msec2nice


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


def get_db_connection() -> sqlite3.Connection:
    """ Return a database connection.
    Create if it does not exist
    """
    db = get_db_path()
    if os.path.exists(db):
        conn = sqlite3.connect(db)
    else:
        conn = sqlite3.connect(db)
        conn.execute("""
        CREATE TABLE IF NOT EXISTS muslog (
            host TEXT,
            cwd TEXT,
            user TEXT,
            time INTEGER,
            type TEXT,
            message TEXT,
            status INTEGER,
            project TEXT,
            tag TEXT,
            uid TEXT,
            filename TEXT,
            checksum TEXT,
            child_of TEXT,
            data json
            )""")
        conn.commit()

    conn.row_factory = record_factory
    return conn


@dataclass(init=False)
class Record():

    type: Optional[str]
    message: str
    host: str
    cwd: str
    user: str
    project: Optional[str]
    tag: Optional[str]
    status: int
    data: dict
    time: float
    uid: str
    filename: Optional[str]
    checksum: Optional[str]
    child_of: Optional[str]

    def __init__(self):
        self.data = {}
        self.status = 0

    def __str__(self):
        proj = getattr(self, "projects", "?")
        message = getattr(self, "message", "-")
        return (
            f"{self.host} {self.user} {int(self.time)} "
            f"{self.cwd} "
            f"{proj} {self.tags} - "
            f"{message}"
        )

    def nice(self,
             no_rep: Optional[int] = None,
             full: bool = False):
        """Return a colorama formatted string for this record

        Args:
            no_rep (int, optional): No of times this command was repeated.
                Defaults to None.
            full (float): Return a multi-line full output
        Returns:
            str: ANSI color formatted string
        """
        import shutil

        from click import style

        twdith = shutil.get_terminal_size((80, 24)).columns

        ntime = msec2nice(1000 * int(time.time() - self.time))
        if self.type == 'history':
            tmark = style('H', fg="green")
        elif self.type == 'log':
            tmark = style('L', bg="blue", fg='black')
        elif self.type == 'tag':
            tmark = style('T', bg="cyan", fg='black')
        elif self.type == 'macro-exe':
            tmark = style('m', fg='white', bold=False)
        else:
            tmark = style('?', fg='white', bold=False)

        if self.status == 0:
            smark = " "
            smark_long = '       '
        else:
            smark = style("!", bg="red", fg="white")
            smark_long = style(f" {self.status:>5d}!",
                               fg="red", bold=False, bg="black")

        if no_rep is not None and no_rep > 1:
            srep = style(f" ({no_rep}x)", fg="white", bold=True)
        else:
            srep = ""

        extra = ""
        if self.type == "macro-exe":
            if 'runtime' in self.data:
                rt = msec2nice(self.data["runtime"] * 1000)
                extra = f' duration: {rt}'

        if self.uid:
            uid = style(self.uid[:4], fg='blue', bold=False,
                        bg='black')
        else:
            uid = style('????', fg='red', bold=False)
        if full:
            subsequent_indent = ""
        else:
            subsequent_indent = "      "
        if self.message is None:
            message = ""
        else:
            message = " \\\n".join(
                wrap(self.message, twdith - 20,
                     subsequent_indent=subsequent_indent))
            message = style(message, fg='white', bold=True)

        if full:
            return (
                f"{tmark}{smark_long} {ntime:>8s} "
                f"| {self.user}@{self.host}:{self.cwd}\n"
                f"{message}{srep}{extra}")
        else:
            return f"{tmark}{smark} {uid} {ntime:>8s} {message}{srep}{extra}"

    def prepare(self,
                filename: Optional[Path] = None,
                rectype: Optional[str] = None,
                message: Optional[str] = None,
                child_of: Optional[str] = None,
                ):
        """Prepare record with default values

        Returns: None
        """

        import os
        from uuid import uuid4

        from mus.config import get_config
        from mus.util.files import get_checksum

        if filename is not None:
            self.filename = str(Path(filename).resolve())
            self.checksum = get_checksum(filename)
        else:
            self.filename = None
            self.checksum = None
        self.child_of = child_of
        self.cwd = os.getcwd()
        self.time = time.time()
        self.type = rectype
        self.uid = str(uuid4())
        self.message = message

        # Gather information!
        if 'MUS_HOST' in os.environ:
            self.host = os.environ['MUS_HOST']
        else:
            import socket
            self.host = socket.gethostname()

        if 'MUS_USER' in os.environ:
            self.user = os.environ['MUS_USER']
        else:
            import getpass
            self.user = getpass.getuser()

        config = get_config()
        if 'tag' in config:
            self.tags = "|" + "|".join(config['tag']) + "|"
        else:
            self.tags = ""

        if 'project' in config:
            self.projects = "|" + "|".join(config['project']) + "|"
        else:
            self.projects = ""

    def save(self):
        import json
        db = get_db_connection()

        rdata = [self.host,
                 self.cwd,
                 self.user,
                 self.time,
                 self.type,
                 self.message,
                 self.status,
                 self.tags,
                 self.projects,
                 self.uid,
                 self.filename,
                 self.checksum,
                 self.child_of]

        if self.data:
            rdata.append(json.dumps(self.data))
            sql = """INSERT INTO muslog(
                    host, cwd, user, time, type, message, status,
                    tag, project, uid, filename, checksum, child_of, data)
                    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) """
        else:
            sql = """INSERT INTO muslog(
                    host, cwd, user, time, type, message, status,
                    tag, project, uid, filename, checksum, child_of)
                    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) """

        db.execute(sql, tuple(rdata))
        db.commit()

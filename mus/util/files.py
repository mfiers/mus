
import logging
from pathlib import Path

lg = logging.getLogger()


def get_checksum(filename: Path) -> str:
    import hashlib

    from mus.db import get_db_connection, init_hashcache_table

    if not filename.exists():
        raise FileExistsError()

    conn = get_db_connection()

    # TODO: remove this eventually
    init_hashcache_table(conn)

    sql = """SELECT * FROM hashcache
              WHERE filename=?"""
    result = conn.execute(sql, (str(filename.resolve()),))
    rec = result.fetchone()
    if rec is not None:
        return rec.hash

    mtime = int(filename.stat().st_mtime)

    sha256_hash = hashlib.sha256()
    with open(filename, "rb") as f:
        # Read and update hash string value in blocks of 4K
        for byte_block in iter(lambda: f.read(65536), b""):
            sha256_hash.update(byte_block)
    checksum = sha256_hash.hexdigest()

    sql = """INSERT INTO hashcache
                    (filename, mtime, hash)
             VALUES (?, ?, ?)
             ON CONFLICT (filename)
                DO UPDATE SET mtime=mtime,
                              hash=hash;"""
    conn.execute(sql, (str(filename.resolve()),
                       mtime, checksum))
    conn.commit()

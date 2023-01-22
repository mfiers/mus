
import logging
from pathlib import Path

lg = logging.getLogger()


def get_checksum(filename: Path) -> str:
    import hashlib
    import sys

    if not filename.exists():
        raise FileExistsError()

    sha256_hash = hashlib.sha256()
    with open(filename, "rb") as f:
        # Read and update hash string value in blocks of 4K
        for byte_block in iter(lambda: f.read(65536),b""):
            sha256_hash.update(byte_block)
    return sha256_hash.hexdigest()


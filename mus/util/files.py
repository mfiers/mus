
from pathlib import Path


def get_checksum(filename: Path) -> str:
    import subprocess as sp
    import sys

    if sys.platform == 'darwin':
        P = sp.check_output(
            ['shasum', '-U', '-a', '256', filename],
            text=True)
        checksum = P.split()[0]
    else:
        raise NotImplementedError()

    return checksum

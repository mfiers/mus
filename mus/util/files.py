
from pathlib import Path


def get_checksum(filename: Path) -> str:
    import subprocess as sp
    import sys

    if sys.platform == 'darwin':
        P = sp.check_output(
            ['shasum', '-U', '-a', '256', filename],
            text=True)
        checksum = P.split()[0]
    elif sys.platform == 'linux':
        P = sp.check_output(
            ['shasum', '-p', '-a', '256', filename],
            text=True)
        checksum = P.split()[0]
    else:
        raise NotImplementedError(f"for sys {sys.platform}")

    return checksum

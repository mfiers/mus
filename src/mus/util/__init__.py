
import logging
import os
from pathlib import Path


def get_template_path(template_path, template_name):
    """
    Get the full path to a template file embedded in the package.
    Works in wheels and source distributions.
    """
    from importlib.resources import files
    return str(files(template_path).joinpath(template_name))


def get_host():
    import socket
    return socket.gethostname()


def msec2nice(mtime):

    if mtime < 1000:
        return f"{mtime:.0f}ms"
    elif mtime < 60 * 1000:
        # seconds
        return f"{mtime / 1000:02.1f}s"
    elif mtime < 60 * 60 * 1000:
        # minutes when smaller than 60 minutes
        allseconds = int(mtime // 1000)
        minutes = allseconds // 60
        seconds = allseconds % 60
        return f"{minutes}m:{seconds:02d}s"
    elif mtime < 60 * 60 * 1000 * 24:
        allminutes = int(mtime // (1000 * 60))
        hours = allminutes // 60
        minutes = allminutes % 60
        return f"{hours}h:{minutes:02d}m"
    else:
        allhours = int(mtime // (1000 * 60 * 60))
        days = allhours // 24
        hours = allhours % 24
        return f"{days}d:{hours:02d}h"


def format_type_short(status):
    from click import style
    if status == 'tag':
        return style('T', bg='yellow', bold=False, fg='black')
    elif status == 'log':
        return style('L', bg='blue', bold=False, fg='black')
    elif status == 'history':
        return style('H', bg='green', bold=False, fg='black')
    elif status == 'inputfile':
        return style('i', bg='cyan', bold=False, fg='black')
    else:
        return style('?', bg='black', fg='red')


def pts(level, *message, **format):
    # if level.lower() == 'debug'
    print(logging.getLogger().getEffectiveLevel())

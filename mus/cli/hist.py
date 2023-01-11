#!/usr/bin/env python3
"""
Minimal script to store history in the mus sqlite database - this tool is
supposed to be executed as prompt_command. Maybe rewrite in nim or so?
"""

import re
import sys

from mus.config import get_config
from mus.db import Record

IGNORE_PATTERNS = [
    'ls',
    'ls -[ltrahS]+',
    'pwd',
    'mus .*']


def cli():
    stdin = sys.stdin.read().strip()
    rc_str, _, message = stdin.strip().split(None, 2)
    rc = int(rc_str)
    message = message.strip()

    ignore_regex = re.compile("|".join(IGNORE_PATTERNS))

    if ignore_regex.fullmatch(message):
        # If this is an ignoreable cl - the quit here.
        exit(0)

    if message == "":
        """Do not store empty commandlines"""
        exit()

    config = get_config()
    if config.get('store-history') == 'no':
        # do not store history
        exit(0)

    record = Record()
    record.prepare()
    record.message = message.strip()
    record.status = rc
    record.type = 'history'

    record.save()

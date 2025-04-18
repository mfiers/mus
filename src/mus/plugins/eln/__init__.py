

import logging
import os
import tempfile
import textwrap
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from textwrap import dedent
from typing import Any, Dict, List

import click

from mus.cli.files import tag_one_file
from mus.config import get_env, get_secret, save_kv_to_local_config
from mus.db import Record, get_db_connection
from mus.hooks import register_hook
from mus.plugins.eln.util import (
    ElnConflictingExperimentId,
    ElnNoExperimentId,
    add_eln_data_to_record,
    convert_ipynb_to_pdf,
    eln_comment,
    eln_create_filesection,
    eln_file_upload,
    expinfo,
    fix_eln_experiment_id,
    get_stamped_filename,
)

lg = logging.getLogger(__name__)

ALWAYS_UPLOAD_TO_ELN = ['ipynb', 'pdf', 'png', 'xlsx', 'xls', 'doc', 'docx']


@dataclass
class ELNDATA:
    experimentid: int = -1
    n: int = 0
    title: str = ''
    rest: str = ''
    filesets: List[List[str]] = field(default_factory=list)
    metadata: List[Dict[str, Any]] = field(default_factory=list)
    records: List[Any] = field(default_factory=list)


ElnData = ELNDATA()


# CLI commands
@click.group("eln")
def cmd_eln():
    """Elabjournal commands"""


@cmd_eln.command("upload")
@click.option('-e', '--editor', is_flag=True,
              default=False, help='Always drop into editor')
@click.option("-m", "--message", help="Mandatory message to attach to files")
@click.argument("filename", nargs=-1)
@click.pass_context
def eln_upload_shortcut(
            ctx,
            filename: List[str],
            message: str | None,
            editor: bool):
    "Upload a file to ELN"
    from mus.cli.files import filetag
    ctx.invoke(filetag, filename=filename, message=message,
               editor=editor, irods=False, eln=True)


@cmd_eln.command("tag-folder")
@click.option("-x", "--experimentID", type=int, required=True)
def eln_tag(experimentid):
    """Tag the current folder with data from ELN."""
    experimentid = fix_eln_experiment_id(experimentid)
    expdata = expinfo(experimentid)
    expdata = {
        'eln_' + k: v for k, v in expdata.items()
    }
    for k, v in expdata.items():
        print(f"{k:25} : {v}")
    save_kv_to_local_config(data = expdata)


@cmd_eln.command("update")
def eln_update():
    """Update based on local experiment id."""
    env = get_env()

    if 'eln_experiment_id' not in env:
        click.echo("No experiment id found?")
        return
    experimentid = env['eln_experiment_id']

    expdata = expinfo(experimentid)
    expdata_2 = {
        'eln_' + k: v for k, v in expdata.items()
    }

    save_kv_to_local_config(data=expdata_2)


# Hooks into the main mus infrastructure

def init_eln(cli):
    """Add ELN options to the log and tag commands."""
    from mus.cli import files
    from mus.cli import log as muslog

    # add the option to post to ELN to the log & tag command
    muslog.log.params.append(
        click.Option(['-E', '--eln'], is_flag=True,
                     default=False, help='Post to ELN'))
    files.filetag.params.append(
        click.Option(['-E', '--eln'], is_flag=True,
                     default=False, help='Save file to ELN'))
    files.filetag.params.append(
        click.Option(['-d', '--dry-run'], is_flag=True,
                     default=False, help='Dry run, do not upload anything'))

    # and an optional experiment id
    muslog.log.params.append(
        click.Option(['-x', '--eln-experimentid'], type=int,
                    help='ELN experiment ID'))
    files.filetag.params.append(
        click.Option(['-x', '--eln-experimentid'], type=int,
                    help='ELN experiment ID'))

    # add the command to tag a folder to the click
    # instance
    cli.add_command(cmd_eln)


def format_filedata(fset, mdata):
    fn = os.path.basename(mdata["filename"])

    obj_link = get_secret('irods_web').rstrip('/')
    #TODO: this is a hack - need a better solution
    col_link = obj_link.replace('data-object/view', 'collection/browse')

    rv = f"<li><b>{fn}</b>"

    if 'irods_url' in mdata:

        if mdata.get('is_dir') is True:
            url = col_link + mdata['irods_url']
        else:
            url = obj_link + mdata['irods_url']

        rv += f" <a href='{url}'>[IRODS]</a>"
    rv += f" <span style='font-size: small; color: #444;'>(shasum: {mdata['checksum']})</span></li>"
    return rv


def eln_save_record(record):
    """
    Post a log to the ELN.

    """

    ctx = click.get_current_context()
    if not ctx.params.get('eln'):
        return

    env = get_env()

    experimentid = ctx.params['eln_experimentid']
    if experimentid is not None:
        experimentid = fix_eln_experiment_id(experimentid)
        if 'eln_experiment_id' in env:
            raise ElnConflictingExperimentId()
    elif 'eln_experiment_id' in env:
        experimentid = int(env['eln_experiment_id'])
    else:
        raise ElnNoExperimentId("No experiment ID given")

    message = record.message.strip()

    # for debugging - do not store anything on ELN
    DEBUG_SKIP_ELN = message == 'NO_ELN'

    if message.count('\n') == 0:
        title = message
        rest = ''
    else:
        title, rest = message.split('\n', 1)

    if record.filename is None:
        # just a regular message, can be stored in an ELN comment
        metadata = [
            f"<li><b>{k.capitalize()}</b>: {getattr(record, k)}</li>"
            for k in ['host', 'cwd', 'user']
            ]

        lmessage = [
            '<ul>']
        lmessage += metadata
        lmessage += ['</ul>', '', rest]

        message = "\n".join(lmessage)
        if not DEBUG_SKIP_ELN:
            eln_comment(experimentid, title, message)
        return
    else:
        # remember everything, to ultimately store all files
        ElnData.n += 1
        if ElnData.n == 1:
            ElnData.experimentid = experimentid
            ElnData.title = title
            ElnData.rest = rest

        ElnData.records.append(record)
        # this (group) of files to append
        fileset = []
        # the original file
        fileset.append(record.filename)

        # pdf_convert = False
        pdf_filename = None
        if record.filename.endswith('.ipynb'):
            pdf_filename = convert_ipynb_to_pdf(record.filename)
            if pdf_filename is not None:

                # tag as well :)
                tag_one_file(pdf_filename, message=record.message)

        # store the message as well.
        metadata = {
            k: getattr(record, k)
            for k in ['host', 'cwd', 'user', 'filename',
                      'checksum']
        }

        if record.data.get('is_dir') is True:
            metadata['is_dir'] = True
        else:
            metadata['is_dir'] = False

        ntime = datetime\
            .fromtimestamp(record.time)\
            .strftime("%Y-%m-%d %H:%M:%S")
        metadata['upload_time'] = ntime

        # Just recording data here - will upload later.
        ElnData.metadata.append(metadata)
        ElnData.filesets.append(fileset)


def check_upload_anyway(fn, md):
    """should the file be uploaded anyhow?"""
    if not 'irods_url' in md:
        # was not uploaded to irods - then upload to ELN
        return True

    ext = fn.rsplit('.', 1)[-1]
    return ext in ALWAYS_UPLOAD_TO_ELN


def finish_file_upload(message):
    """
    message is the user provided message
    """
    ctx = click.get_current_context()

    if not ctx.params.get('eln'):
        return

    dry_run = ctx.params.get('dry_run')

    filelist = []
    mheader = ""
    files_to_eln = 0  # only if they are not in irods

    # for debugging - do not store anything on ELN
    DEBUG_SKIP_ELN = message == 'NO_ELN'

    for i, (fset, mdata) in enumerate(zip(ElnData.filesets, ElnData.metadata)):
        filelist.append(format_filedata(fset, mdata))
        for fn in fset:
            if check_upload_anyway(fn, mdata):
                files_to_eln += 1

        if i == 0:
            mheader = dedent(f"""
                Upload by {mdata['user']}
                from {mdata['host']}:{mdata['cwd']}
                at {mdata['upload_time']}.
            """).strip()

    mbody = mheader + "\n\n<ol>" + "\n".join(filelist) + "</ol>"

    if not DEBUG_SKIP_ELN:
        eln_comment(ElnData.experimentid,
                    ElnData.title,
                    mbody)

    if not dry_run and files_to_eln > 0 and (not DEBUG_SKIP_ELN):

        journal_id = eln_create_filesection(
            experimentid=ElnData.experimentid,
            title=ElnData.title + ' files',)

        i = 0
        for fset, mdata in zip(ElnData.filesets, ElnData.metadata):
            for fn in fset:
                if check_upload_anyway(fn, mdata):
                    print(fn)
                    eln_file_upload(journal_id, fn)
                    i += 1
        click.echo(f"Uploaded {i} files to ELN")


register_hook('plugin_init', init_eln)
register_hook('save_record', eln_save_record)
register_hook('prepare_record', add_eln_data_to_record)
register_hook('finish_filetag', finish_file_upload, priority=1)





import os
import tempfile
from datetime import datetime
from pathlib import Path

import click

from mus.config import get_env, save_env
from mus.hooks import register_hook
from mus.plugins.eln.util import (
    ElnConflictingExperimentId,
    ElnNoExperimentId,
    add_eln_data_to_record,
    convert_ipynb_to_pdf,
    eln_comment,
    eln_file_append,
    eln_file_upload,
    expinfo,
    fix_eln_experiment_id,
)


# CLI commands
@click.group("eln")
def cmd_eln():
    """Elabjournal commands"""


@cmd_eln.command("tag-folder")
@click.option("-x", "--experimentID", type=int, required=True)
def eln_tag(experimentid):
    """Tag the current folder with data from ELN."""
    experimentid = fix_eln_experiment_id(experimentid)
    expdata = expinfo(experimentid)
    expdata = {
        'eln_' + k: v for k, v in expdata.items()
    }
    save_env(expdata)

# Hooks

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

    # and an optional experiment id
    muslog.log.params.append(
        click.Option(['-X', '--eln-experimentid'], type=int,
                    help='ELN experiment ID'))
    files.filetag.params.append(
        click.Option(['-X', '--eln-experimentid'], type=int,
                    help='ELN experiment ID'))

    # add the command to tag a folder to the click
    # instance
    cli.add_command(cmd_eln)


def eln_save_log(record):
    """Post a log to the ELN."""
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

    message = record.message

    if message.count('\n') == 0:
        title = message
        rest = ''
    else:
        title, rest = message.split('\n', 1)

    if record.filename is None:

        # store message
        metadata = [
            f"<li><b>{k.capitalize()}</b>: {getattr(record, k)}</li>"
            for k in ['host', 'cwd', 'user']
            ]

        lmessage = [
            '<ul>']
        lmessage += metadata
        lmessage += ['</ul>', '', rest]

        message = "\n".join(lmessage)
        eln_comment(experimentid, title, message)
        return
    else:

        pdf = False
        pdf_filename = None
        if record.filename.endswith('.ipynb'):
            pdf_filename = convert_ipynb_to_pdf(record.filename)
            pdf = True

        # store message
        metadata = [
            f"* {k.capitalize()}: {getattr(record, k)}"
            for k in ['host', 'cwd', 'user', 'filename']
            ]
        ntime = datetime\
            .fromtimestamp(record.time)\
            .strftime("%Y-%m-%d %H:%M:%S")
        metadata.append(
            f"* Upload time: {ntime}"
        )

        lmessage = [
            title, ""]
        lmessage += metadata
        lmessage += ['', rest]

        message = "\n".join(lmessage)

        # store file
        tf = tempfile.NamedTemporaryFile(delete=False)
        tf.write(message.encode('utf-8'))
        tf.close()

        journal_id = eln_file_upload(experimentid, record.filename, title=title)
        if pdf:
            eln_file_append(
                journal_id, pdf_filename)
        eln_file_append(journal_id, tf.name,
                        uploadname=Path(record.filename).name + '.meta.txt')
        Path(tf.name).unlink()


register_hook('plugin_init', init_eln)
register_hook('save_record', eln_save_log)
register_hook('prepare_record', add_eln_data_to_record)
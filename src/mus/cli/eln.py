
import json
import os
import tempfile
from datetime import datetime
from pathlib import Path

import click

from mus.cli import files
from mus.cli import log as muslog
from mus.config import get_env, save_env
from mus.db import Record  # NOQA: E402
from mus.eln import (
    eln_comment,
    eln_file_append,
    eln_file_upload,
    expinfo,
    fix_eln_experiment_id,
)
from mus.exceptions import ElnConflictingExperimentId, ElnNoExperimentId
from mus.hooks import register_hook

# add the option to post to ELN to the log & tag command
muslog.log.params.append(
    click.Option(['-e', '--eln'], is_flag=True,
                 default=False, help='Post to ELN'))
files.filetag.params.append(
    click.Option(['-e', '--eln'], is_flag=True,
                 default=False, help='Save file to ELN'))

# and an experiment id
muslog.log.params.append(
    click.Option(['-x', '--eln-experimentid'], type=int,
                 help='ELN experiment ID'))
files.filetag.params.append(
    click.Option(['-x', '--eln-experimentid'], type=int,
                 help='ELN experiment ID'))


# CONFIGURATION
@click.group("eln")
def cmd_eln():
    """Elabjournal commands"""
    pass


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


def convert_ipynb_to_pdf(filename):
    # if the file is an ipython notebook - attempt to convert to PDF
    # datestamp - on the day level. I don't think we need second resolutiono
    # here - one file per day should be enough...
    stamp = datetime.now().strftime("%Y%m%d_%H%M")
    pdf_filename = filename.replace(".ipynb", f".{stamp}.pdf")
    click.echo(f"Converting ipython {os.path.basename(filename)} ")
    click.echo(f"Target: {pdf_filename} ")
    from nbconvert import PDFExporter

    pdf_data, resources = PDFExporter().from_filename(filename)
    with open(pdf_filename, "wb") as F:
        F.write(pdf_data)
    return pdf_filename

# def eln_save_file(record):
#     """Post a file to the ELN."""
#     ctx = click.get_current_context()
#     if not ctx.params.get('eln'):
#         return

#     env = get_env()

#     experimentid = ctx.params['eln_experimentid']
#     if experimentid is not None:
#         experimentid = fix_eln_experiment_id(experimentid)
#         if 'eln_experiment_id' in env:
#             raise ElnConflictingExperimentId()
#     elif 'eln_experiment_id' in env:
#         experimentid = int(env['eln_experiment_id'])
#     else:
#         raise ElnNoExperimentId("No experiment ID given")

#     journal_id = eln_file_upload(
#         experimentid, record.filename, title)

#     tf = tempfile.NamedTemporaryFile(delete=False)
#     tf.write(json.dumps({'journal_id': journal_id}).encode('utf-8'))
#     tf.close()
#     record.filename = tf.name
#     print(tf)
#     return
#     if record.filename is None:
#         return
#     if Path(record.filename).is_dir():
#         click.echo("Not saving directory to ELN")
#         return

#     print('upload', record.filename, journal_id)
#     print(eln_file_upload(journal_id, record.filename))


register_hook('save_record', eln_save_log)


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


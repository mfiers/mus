
import json

import click

from mus.cli import log as muslog
from mus.config import get_env, save_env
from mus.db import Record  # NOQA: E402
from mus.eln import eln_comment, expinfo, fix_eln_experiment_id
from mus.exceptions import ElnConflictingExperimentId, ElnNoExperimentId
from mus.hooks import register_hook

# add the option to post to ELN to the log command
muslog.log.params.append(
    click.Option(['-e', '--eln'], is_flag=True,
                 default=False, help='Post to ELN'))

# and an experiment id
muslog.log.params.append(
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
    if not ctx.params['eln']:
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
        title, rest = message.split('\n')

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


@cmd_eln.command("message")
@click.option("-x", "--experimentID", type=int)
@click.argument("message", nargs=-1)
def eln_message(experimentid, message):
    """Post a message to the ELN."""

    # store in json file
    with open('eln.json', 'a') as f:
        f.write(json.dumps({'message': message, 'title': title}) + '\n')

    # also store in mus db
    rec = Record()
    rec.prepare()
    rec.message = " ".join(message)
    rec.type = 'log'
    rec.save()

    #add metadata to message

    eln_comment(experimentid, title, message)
    print(message)
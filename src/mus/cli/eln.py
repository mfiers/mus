
import json

import click

from mus.config import save_env
from mus.eln import expinfo, fix_eln_experiment_id


# CONFIGURATION
@click.group("eln")
def cmd_eln():
    pass


@cmd_eln.command("tag")
@click.option("-x", "--experimentID", type=int, required=True)
def eln_tag(experimentid):
    experimentid = fix_eln_experiment_id(experimentid)
    expdata = expinfo(experimentid)
    expdata = {
        'eln_' + k: v for k, v in expdata.items()
    }

    save_env(expdata)
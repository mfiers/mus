import json
import logging
import os
import re
import socket
from collections import Counter
from datetime import datetime
from pathlib import Path

import click
import keyring

from mus.config import get_env, get_secret
from mus.hooks import register_hook
from mus.plugins.irods.util import get_irods_records, icmd
from mus.util.log import read_nonblocking_stdin

lg = logging.getLogger(__name__)


@click.group("irods")
def cmd_irods():
    "Irods Commands"
    pass


@cmd_irods.command("get")
@click.option("-f", "--force", is_flag=True, default=False,
              help="Force overwrite existing files",)
@click.argument("filename", nargs=-1)
def irods_get(filename, force):

    def get_mango_path(url):
        url = url.split("data-object/view")[1]
        return url

    cmd = []

    if force:
        cmd.append("-f")

    for _filename in filename:
        if not _filename.endswith(".mango"):
            lg.warning(f"Ignoring {_filename}")
            continue
        # attempt to get from irods
        target = Path(_filename).name[:-6]
        fulltarget = Path(str(_filename)[:-6])
        lg.warning(f"getting {target}")
        with open(_filename, 'rt') as F:
            try:
                # older style mango json
                md = json.load(F)
                url = get_mango_path(md["url"])
            except json.decoder.JSONDecodeError:
                # just the url
                F.seek(0)
                url = F.read().strip()

        expected = Path(url).name
        assert expected == target

        if (not force) and fulltarget.exists():
            lg.warning(f"Not overwriting {target}, use `-f`")
        else:
            icmd('iget', url, '.', '-K', *cmd )


MANDATORY_ELN_RECORDS = '''
    eln_experiment_name
    eln_experiment_id
    eln_project_name
    eln_project_id
    eln_study_name
    eln_study_id
'''.split()


def finish_file_upload():
    ctx = click.get_current_context()
    if not ctx.params.get('irods'):
        return
    if not ctx.params.get('eln'):
        raise click.UsageError("You MUST also upload to ELN (-E)")

    # Ensure all ELN data is present
    from mus.plugins.eln import ElnData
    env = get_env()

    records_missing = 0
    for mer in MANDATORY_ELN_RECORDS:
        if mer not in env:
            click.echo(f"Missing env data: {mer}")
            records_missing += 1
    if records_missing > 0:
        raise click.UsageError(
            "Missing ELN records, please run `mus eln tag-folder`")

    def sanitize(x):
        "Sanitize folder name"
        x = x.lower().replace(' ', '_')
        x = re.sub(r'[^0-9A-Za-z_]', '', x)
        x = re.sub(r'_+', '_', x)
        return x

    # determine irods target
    irods_home = get_secret('irods_home').rstrip('/')
    irods_folder = "/".join([
        irods_home,
        'mus',
        sanitize(env['eln_project_name']),
        sanitize(env['eln_study_name']),
        sanitize(env['eln_experiment_name']),
    ])

    # figure out what already is on irods
    irecs = {}
    for ir in get_irods_records(irods_folder):
        irecs[ir['name']] = ir

    # determine which records still need uploading
    to_upload = []
    irods_paths = []

    status = {}
    basepath = None
    fn2irods = {}

    for i, (rec, metadata) in enumerate(zip(ElnData.records, ElnData.metadata)):
        ip = os.path.basename(rec.filename)
        fp = Path(rec.filename).resolve()
        irods_paths.append(
            f"{irods_folder}/{ip}"
        )
        fn2irods[rec.filename] = f"{irods_folder}/{ip}"

        if i == 0:
            basepath = os.path.dirname(rec.filename)
        if ip in irecs:
            if rec.checksum == irecs[ip]['checksum']:
                # checksum matches - ignore
                status[ip] = 'ok'
            else:
                # checksum does not match - re-upload
                status[ip] = 'checksum mismatch'
                to_upload.append(fp)
        else:
            # does not seem to exists - upload
            status[ip] = "not found"
            to_upload.append(fp)

        metadata['irods_status'] = status[ip]
        metadata['irods_url'] = f"{irods_folder}/{ip}"


    fstat = Counter(status.values())
    click.echo(f"Irods files not uploaded yet             : {fstat['not found']}")
    click.echo(f"Irods uploaded, checksum ok, skip        : {fstat['ok']}")
    click.echo(f"Irods uploaded, checksum fail, overwrite : {fstat['checksum mismatch']}")

    if len(to_upload) > 0:
        # ensure target folder is there
        icmd('imkdir', '-p', irods_folder, )
        icmd('iput', '-K', '-f', *to_upload, irods_folder)

    # create mango files
    for filename, mangourl in fn2irods.items():
        mangofile = filename + '.mango'
        with open(mangofile, 'wt') as F:
            F.write(mangourl)

    env = get_env()
    irods_meta = {
        "mgs.project.path"              : basepath,
        "mgs.project.server"            : socket.gethostname(),
        "mgs.project.upload_date"       : datetime.now().strftime("%Y-%m-%d"),
        "mgs.project.__version__"       : "7.0.0",
        "mgs.project.experiment_id"     : env['eln_experiment_id'],
        "mgs.project.experiment_name"   : env['eln_experiment_name'],
        "mgs.project.project_id"        : env['eln_project_id'],
        "mgs.project.project_name"      : env['eln_project_name'],
        "mgs.project.study_id"          : env['eln_study_id'],
        "mgs.project.study_name"        : env['eln_study_name'],
        "mgs.project.description"       : 'test1222123131',
    }

    # assign metadata to the collection
    for key, value in irods_meta.items():
        icmd('imeta', 'set', '-C', irods_folder, key, value)

    return

    # this is so far the only thing I can get to work
    # cross platform :(
    for ip in irods_paths:
        for key, value in irods_meta.items():
            icmd('imeta', 'set', '-d', ip, key, value)


def init_irods(cli):
    from mus.cli import files
    cli.add_command(cmd_irods)
    files.filetag.params.append(
        click.Option(['-I', '--irods'], is_flag=True,
                     default=False, help='Save file to iRODS'))


register_hook('plugin_init', init_irods)
register_hook('finish_filetag', finish_file_upload)

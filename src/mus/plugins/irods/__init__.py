import base64
import json
import logging
import os
import platform
import re
import socket
import subprocess as sp
from collections import Counter
from datetime import datetime
from pathlib import Path
from typing import List, NamedTuple

import click
import keyring
from irods.exception import CollectionDoesNotExist, DataObjectDoesNotExist

import mus.exceptions
from mus.config import get_env, get_secret
from mus.db import Record
from mus.hooks import register_hook
from mus.plugins.irods.util import get_irods_records, get_irods_session, icmd
from mus.util.log import read_nonblocking_stdin

lg = logging.getLogger(__name__)

@click.group("irods")
def cmd_irods():
    "Irods Commands"
    pass


MANGO_PAIR = NamedTuple("MANGO_PAIR", [('local', Path), ('remote', str)])


def check_mango(*mpairs: MANGO_PAIR):

    if platform.system() == 'Darwin':
        click.echo('Check not implemented!')
        exit(-1)

    session = get_irods_session()
    irecs = {}

    def recursive_get_files(irods_folder, coll=None):
        if coll is None:
            coll = session.collections.get(irods_folder)
        assert coll is not None

        for rip in coll.subcollections:
            recursive_get_files(irods_folder, rip)
        for rip in coll.data_objects:
            remote_checksum = rip.chksum().split(":")[1]
            remote_checksum = base64.b64decode(remote_checksum).hex()
            remote_path = rip.path.replace(irods_folder.rstrip('/') + '/', '')
            irecs[remote_path] = dict(
                checksum=remote_checksum)

    checked = 0
    failed = 0

    for mp in mpairs:

        local = mp.local
        lg.debug(f"checking {local}")
        url = mp.remote

        if local.is_dir():
            recursive_get_files(url)
            for lp in local.glob('**/*'):
                if lp.is_dir():
                    continue
                rlp = str(lp.relative_to(local))
                rec = Record()
                rec.prepare(filename=lp, rectype='tag')
                local_checksum = rec.checksum
                checked += 1
                if rlp not in irecs:
                    lg.error(f"Can not find: [bold]{str(lp)}[/]",
                             extra={"markup": True})
                    failed += 1
                elif irecs[rlp]['checksum'] != local_checksum:
                    lg.error(f"Checksum mismatch: [bold]{rlp}[/]",
                             extra={"markup": True})
                    failed += 1
                else:
                    lg.info(f"Checksum ok: {rlp}")
        else:  # or a file
            try:
                obj = session.data_objects.get(url)
            except DataObjectDoesNotExist:
                lg.error(f"Mango object does not exist: [bold]{url}[/]",
                         extra={"markup": True})
                failed += 1
                continue

            remote_checksum = obj.chksum().split(":")[1]
            remote_checksum = base64.b64decode(remote_checksum).hex()
            rec = Record()
            rec.prepare(filename=local, rectype='tag')
            local_checksum = rec.checksum
            checked += 1
            if remote_checksum != local_checksum:
                lg.error(f"Checksum mismatch: [bold]{local}[/]",
                         extra={"markup": True})
                failed += 1
            else:
                lg.info(f"Checksum ok: {local}")

    if checked > 0 and failed == 0:
        click.echo(f"All checksums ({checked}) are ok.")
    elif checked > 0:
        lg.warning(f"Failed {failed} out of {checked} checksums!")
    else:
        lg.warning("Nothing checked!")


@cmd_irods.command("check")
@click.argument("filename", nargs=-1)
def irods_check(filename):

    to_check = []

    if len(filename) == 0:
        to_check = list(Path.cwd().glob('*.mango'))
    else:
        for _filename in filename:
            _filename = Path(_filename)

            # make sure it is a mango file - otherwise try to append
            if not _filename.name.endswith(".mango"):
                _filename = Path(str(_filename) + '.mango')

            # it should exist!
            if _filename.exists():
                to_check.append(_filename)
            else:
                lg.warning(f"Ignoring {_filename} - need .mango files")

    to_check = list(sorted(set(to_check)))
    if len(to_check) == 0:
        lg.warning("Nothing to check?")
        raise click.UsageError("No mango files to check?")

    to_check_2 = []

    # get local & remote paths
    for mango_file in to_check:
        local_path = Path(str(mango_file)[:-6])
        if not local_path.exists():
            lg.warning(f"Can not check {mango_file} - local missing")
            continue

        # open the ango file & get the URL
        remote_url = None
        with open(mango_file, 'rt') as F:
            try:
                # older style mango json
                md = json.load(F)
                remote_url = get_mango_path(md["url"])
            except json.decoder.JSONDecodeError:
                # just the url
                F.seek(0)
                remote_url = F.read().strip()

        to_check_2.append(MANGO_PAIR(local=local_path, remote=remote_url))

    check_mango(*to_check_2)


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


@cmd_irods.command("upload")
@click.option('-e', '--editor', is_flag=True,
              default=False, help='Always drop into editor')
@click.option("-m", "--message", help="Mandatory message to attach to files")
@click.option('-F', '--irods-force', is_flag=True,
              default=False, help='Force overwrite on iRODS')
@click.argument("filename", nargs=-1)
@click.pass_context
def irods_upload_shortcut(
            ctx,
            filename: List[str],
            message: str | None,
            irods_force: bool,
            editor: bool):
    from mus.cli.files import filetag
    ctx.invoke(filetag, filename=filename, message=message,
               editor=editor, irods=True, irods_force=irods_force,
               eln=True)


MANDATORY_ELN_RECORDS = '''
    eln_experiment_name
    eln_experiment_id
    eln_project_name
    eln_project_id
    eln_study_name
    eln_study_id
'''.split()


def icmd_recursive_stderr_handler(x):
    # helper to make some sense of stderr :(
    for line in x.strip().split("\n"):
        line = line.strip()
        if 'OVERWRITE_WITHOUT_FORCE_FLAG' in line:
            if ' put ' in line and ' failed.' in line:
                line = line.split(' put ')[1].split(' failed.')[0].strip()
                print("File exists:", line)
            elif ' put error for ' in line:
                line = line.split(' put error for ')[1].split(', status')[0].strip()
                print("Folder exists:", line)
            else:
                print('x', line)
        else:
            print(line)


def finish_file_upload(message):

    ctx = click.get_current_context()
    if not ctx.params.get('irods'):
        return
    if not ctx.params.get('eln'):
        raise click.UsageError("You MUST also upload to ELN (-E)")

    irods_force = ctx.params.get('irods_force', False)
    if irods_force:
        click.echo("FORCE UPLOAD! Overwriting files")

    try:
        irods_group = get_secret('irods_group').strip()
    except mus.exceptions.MusSecretNotDefined:
        raise click.UsageError("please specify 'irods_group' in secrets")

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
        sanitize(env['eln_project_name']),
        sanitize(env['eln_study_name']),
        sanitize(env['eln_experiment_name']),
    ])

    # figure out what already is on irods
    # For now - do NOT check what is already there - does not work
    irecs = {}
    if platform.system() == 'Darwin':
        pass
    else:
        session = get_irods_session()

        def recursive_get_files(coll):
            for rip in coll.subcollections:
                recursive_get_files(rip)
            for rip in coll.data_objects:
                remote_checksum = rip.chksum().split(":")[1]
                remote_checksum = base64.b64decode(remote_checksum).hex()
                remote_path = rip.path.replace(irods_folder.rstrip('/') + '/', '')
                irecs[remote_path] = dict(
                    checksum=remote_checksum
                )

        try:
            recursive_get_files(session.collections.get(irods_folder))
        except CollectionDoesNotExist:
            # ignore - nothing seems to be uploaded
            pass

    # determine which records still need uploading
    to_upload = []

    status = {}
    basepath = None
    fn2irods = {}
    sha256sums = {}

    for i, (rec, metadata) in enumerate(zip(ElnData.records, ElnData.metadata)):
        ip = os.path.basename(rec.filename)
        fp = Path(rec.filename).resolve()

        fn2irods[rec.filename] = f"{irods_folder}/{ip}"
        sha256sums[rec.filename] = rec.checksum

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
        elif Path(rec.filename).is_dir():
            to_upload.append(fp)
            status[ip] = 'folder'
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
    click.echo(f"Folders, upload without pre-check        : {fstat['folder']}")

    if len(to_upload) > 0:
        tu_dir, tu_file = [], []

        for tu in to_upload:
            if Path(tu).is_dir():
                tu_dir.append(tu)
            else:
                tu_file.append(tu)

        # ensure target folder is there
        iflag = 'f' if irods_force else ''

        lg.debug(f'imkdir -p {irods_folder}')
        icmd('imkdir', '-p', irods_folder)
        if tu_file:
            for _ in tu_file:
                click.echo(f"Uploading file: {_}")
            icmd('iput', f'-K{iflag}', *tu_file, irods_folder)
            lg.info(f"Uploaded files")
        if tu_dir:
            for _ in tu_dir:
                lg.debug(f"Uploading folder: {_}")
            icmd('iput', f'-Kr{iflag}', *tu_dir, irods_folder,
                 process_error=icmd_recursive_stderr_handler)
            lg.info(f"Uploaded folders")

        click.echo(f"set permissions to {irods_group}")
        icmd('ichmod', '-r', 'own', irods_group, irods_folder)

    to_check = []
    # create mango files & force doublechecking
    for filename, mangourl in fn2irods.items():
        click.echo(f"ichecksum {filename}")
        icmd('ichksum', '-K', '-r', mangourl)
        lg.info("ichksum ok")
        to_check.append(MANGO_PAIR(local=Path(filename), remote=mangourl))
        mangofile = filename + '.mango'

        with open(mangofile, 'wt') as F:
            F.write(mangourl)


    check_mango(*to_check)


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
        "mgs.project.collaborator"      : env['eln_collaborator'],
        "mgs.project.description"       : message,
    }

    if platform.system() == 'Darwin':
        # to be optimized - very slow - but works on a mac :(
        # assign metadata to the collection
        for fn in fn2irods:
            ip = fn2irods[fn]
            icmd('imeta', 'set', '-d', ip, "mgs.project.sha256", sha256sums[fn])

            for key, value in irods_meta.items():
                icmd('imeta', 'set', '-d', ip, key, value)
    else:

        import importlib.resources as resources

        from irods.session import iRODSSession
        from mango_mdschema import Schema

        # fix metadata keys - remove mgs.project. prefix to match schema
        im2 = {k.replace('mgs.project.', ''): v for k, v in irods_meta.items()}

        # load mango schema
        schema_trav = resources.files("mus")\
            .joinpath("plugins/irods/data/project-7.0.0-published.json")
        with resources.as_file(schema_trav) as F:
            schema = Schema(F)

        # connect to irods
        try:
            env_file = os.environ['IRODS_ENVIRONMENT_FILE']
        except KeyError:
            env_file = os.path.expanduser('~/.irods/irods_environment.json')

        ssl_settings = {}
        with iRODSSession(irods_env_file=env_file, **ssl_settings) as session:
            for fn in fn2irods:
                shasum = sha256sums[fn]
                im2['sha256'] = shasum
                ip = fn2irods[fn]

                if Path(fn).is_dir():
                    # recursive apply
                    def recursive_apply(coll):
                        for rip in coll.subcollections:
                            recursive_apply(rip)
                        for rip in coll.data_objects:
                            im2['sha256'] = 'n.d. (folder upload)'
                            schema.apply(rip, im2)
                    recursive_apply(session.collections.get(ip))
                else:
                    obj = session.data_objects.get(ip)
                    schema.apply(obj, im2)


def init_irods(cli):
    from mus.cli import files
    cli.add_command(cmd_irods)
    cli.add_command(irods_upload_shortcut)
    files.filetag.params.append(
        click.Option(['-I', '--irods'], is_flag=True,
                     default=False, help='Save file to iRODS'))
    files.filetag.params.append(
        click.Option(['-F', '--irods-force'], is_flag=True,
                     default=False, help='Force overwrite on iRODS'))


register_hook('plugin_init', init_irods)
register_hook('finish_filetag', finish_file_upload)

import base64
import fnmatch
import json
import logging
import os
import re
import socket
import subprocess as sp
from collections import Counter
from copy import copy
from dataclasses import dataclass, field
from datetime import datetime
from functools import lru_cache
from importlib import resources as impresources
from pathlib import Path
from textwrap import dedent
from typing import Dict, List

import click
import keyring

from mus.config import get_env
from mus.hooks import register_hook
from mus.plugins.irods.util import get_irods_records, icmd, rungo
from mus.util.log import read_nonblocking_stdin

lg = logging.getLogger(__name__)


@click.group("irods")
def cmd_irods():
    "Irods Commands"
    pass


@cmd_irods.command("init")
def iinit():
    password = read_nonblocking_stdin().strip()
    if not password:
        click.echo("Need to pipe in a password!")
        return
    keyring.set_password("mus", "irods_password", password)


def get_irods_session():
    import ssl

    from irods.session import iRODSSession

    env_file = Path("~/.irods/irods_environment.json").expanduser()

    ssl_context = ssl.create_default_context(
        purpose=ssl.Purpose.SERVER_AUTH, cafile=None, capath=None, cadata=None
    )
    ssl_settings = {"ssl_context": ssl_context}
    session = iRODSSession(
        host= "gbiomed.irods.icts.kuleuven.be",
        port= 1247,
        zone_name="gbiomed",
        user='u0089478',
        password=keyring.get_password("mus", "irods_password"),
        authentication_scheme="native",
        encryption_algorithm = "AES-256-CBC",
        encryption_salt_size = 8,
        encryption_key_size = 32,
        encryption_num_hash_rounds = 8,
        user_name = "u0089478",
        ssl_ca_certificate_file = "",
        ssl_verify_server = "cert",
        client_server_negotiation = "request_server_negotiation",
        client_server_policy = "CS_NEG_REQUIRE",
        default_resource = "default",
        cwd = "/gbiomed/home",
        **ssl_settings)

    return session

# @cmd_irods.command("test")
# def itest():
#     session = get_irods_session()
#     IRODS_HOME = "/gbiomed/home/BADS"
#     irods_collection = f"{IRODS_HOME}/mus/testm2"
#     subcoll = session.collections.create(irods_collection)
#     print(subcoll)

# @cmd_irods.command("show-password")
# def iinit():
#     password = read_nonblocking_stdin().strip()
#     if not password:
#         click.echo("Need to pipe in a password!")
#         return
#     keyring.set_password("mus", "irods_password", password)



# @lru_cache(1)
# def get_ignore_list():
#     raw = (
#         (impresources.files(darkmoon.data) / "ignore.txt")
#         .open("rb")
#         .read()
#         .decode("utf-8")
#     )
#     rv = [x.strip() for x in raw.strip().split("\n") if x.strip()]
#     return rv


# def check_in_ignore(filename):
#     FILE_IGNORE_LIST = get_ignore_list()
#     return any(fnmatch.fnmatch(filename, x) for x in FILE_IGNORE_LIST)


@cmd_irods.command("get")
@click.option("-f", "--force", is_flag=True,
              default=False,
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
        with open(_filename) as F:
            md = json.load(F)
            url = md["url"]

        expected = Path(url).name
        assert expected == target

        if (not force) and fulltarget.exists():
            lg.warning(f"Not overwriting {target}, use `-f`")
        else:
            rungo('get', get_mango_path(url), '.', '-K', )


# @cli.command("upload")
# @click.option(
#     "-x",
#     "--experimentID",
#     type=int,
#     required=False,
#     help="The ELN ID of the experiment to associate to the upload",
# )
# @click.option(
#     "-d", "--description", help="Short description of the uploaded file(s)"
# )
# @click.option(
#     "-f",
#     "--force",
#     is_flag=True,
#     default=False,
#     help="Force upload if file exists and is not the same",
# )
# @click.option(
#     "-N",
#     "--no-eln",
#     is_flag=True,
#     default=False,
#     help="Do not post a comment to ELN",
# )
# @click.option(
#     "-D",
#     "--doublecheck",
#     is_flag=True,
#     default=False,
#     help="Double-check by redownloading and checking sha256",
# )
# @click.option(
#     "-n",
#     "--dryrun",
#     is_flag=True,
#     default=False,
#     help="Dry-run - do not upload but check if files are correctly uploaded",
# )
# @click.argument("filename", nargs=-1)
# def irods_upload(
#     filename, experimentid, description, force, doublecheck, no_eln, dryrun
# ):
#     """
#     Upload object(s) to mango.

#     Returns:
#     None
#     """
#     if experimentid is None:
#         xpfile = Path("./eln.exp.json")
#         if not xpfile.exists():
#             raise click.UsageError("No experiment id defined")
#         with open(xpfile) as F:
#             expdata = json.load(F)
#             experimentid = int(expdata["experiment_id"])

#     experimentid = fix_eln_experiment_id(experimentid)

#     up = []
#     no_errors = 0
#     pdf_files = []

#     def convert_ipynb_to_pdf(filename):
#         # if the file is an ipython notebook - attempt to convert to PDF
#         # datestamp - on the day level. I don't think we need second resolutiono
#         # here - one file per day should be enough...
#         stamp = datetime.now().strftime("%Y%m%d")
#         pdf_filename = filename.replace(".ipynb", f".{stamp}.pdf")
#         lg.warning(
#             f"Converting ipython {os.path.basename(filename)} "
#             + f"notebook to PDF...  (This might take a while)  "
#         )
#         lg.warning(f"Target: {pdf_filename} ")
#         from nbconvert import PDFExporter

#         pdf_data, resources = PDFExporter().from_filename(filename)
#         with open(pdf_filename, "wb") as F:
#             F.write(pdf_data)
#         return pdf_filename

#     def create_pointer_file(upinfo):
#         local_filename = upinfo["path"] + ".mango"
#         with open(local_filename, "w") as F:
#             json.dump(upinfo, F, default=json_serial)

#     def upload_one_file(
#         _filename,
#         create_pointer: bool = True,
#     ):
#         nonlocal no_errors, up, pdf_files, dryrun
#         upinfo = None
#         try:
#             upinfo = irods.irods_upload(
#                 _filename,
#                 experimentid,
#                 description=description,
#                 doublecheck=doublecheck,
#                 force=force,
#                 dryrun=dryrun,
#             )

#             if upinfo is None:
#                 assert dryrun
#                 return

#             up.append(upinfo)
#             lg.info(f"{upinfo['nice_path']} | {upinfo['status']}")
#             if create_pointer:
#                 create_pointer_file(upinfo)

#         except FileExistsError:
#             no_errors += 1
#             lg.error(
#                 f"Error uploading {_filename}, "
#                 + "file exists, checksum differs"
#             )
#         except irods.IrodsUploadError as e:
#             no_errors += 1
#             lg.error(f"Upload error {_filename}: {e}")

#         # special operation for ipynb files, pdf convert and upload
#         # these as well.
#         if (not dryrun) and _filename.endswith(".ipynb"):
#             if upinfo is not None and upinfo["status"] != "exists, chksum ok":
#                 pdf_file = convert_ipynb_to_pdf(_filename)
#                 upload_one_file(pdf_file)
#                 pdf_files.append(pdf_file)

#     for _candidate in filename:
#         if os.path.isfile(_candidate):
#             upload_one_file(_candidate)
#         else:
#             lg.debug(f"Upload folder {_candidate}")
#             lg.warning("not creating pointers when uploading folders")
#             # Travers all the branch of a specified path
#             for root, dirs, files in os.walk(_candidate, topdown=True):
#                 dirs[:] = [d for d in dirs if not check_in_ignore(d)]
#                 files[:] = [f for f in files if not check_in_ignore(f)]
#                 for f in files:
#                     upload_one_file(
#                         os.path.join(root, f), create_pointer=False
#                     )

#     if no_errors > 0:
#         lg.error("Errors were encountered while uploading!")

#     if len(up) == 0:
#         if not no_eln:
#             lg.info("Nothing was uploaded, no message to ELN")
#         return

#     if description is not None:
#         eln_title = f"Mango upload: {description}".strip()
#     else:
#         eln_title = "Mango upload"

#     message = dedent(
#         f"""
#             <b>From:</b> {up[0]['server']}<br>
#             <b>Path:</b> {os.getcwd()}<br>
#             <b>Date:</b> {up[0]['upload_date']}<p>

#             <b>Files:</b>
#         """
#     )

#     umess = []
#     for u in up:
#         umess.append(
#             (
#                 f"""<a href="{u['url']}">{u['nice_path']}</a>"""
#                 + '<span style="color: darkgreen; font-size:70%;">'
#                 + f"""({u['status']}, sha256: {u['sha256'][:12]}..)</span>"""
#             )
#         )

#     eln_message = (
#         message
#         + "<ul>"
#         + "\n".join([f"<li>{x}</li>" for x in umess])
#         + "</ul>"
#     )

#     if no_eln:
#         print(eln_title)
#         print(eln_message)
#     else:
#         eln_comment(experimentid, eln_title, eln_message)

#         if pdf_files:
#             eln_file_title = f"Ipython notebook upload: {description}".strip()
#             eln_file_title = eln_file_title.strip().strip(":")
#             file_journal_id = eln_filesection(experimentid, eln_file_title)
#             for pdf in pdf_files:
#                 eln_file_upload(file_journal_id, pdf)


# from darkmoon.cli import dm_env, obsidian  # noqa E402

# cli.add_command(obsidian.obs)
# cli.add_command(dm_env.tag)
# cli.add_command(dm_env.dm_env)


# def main_cli():
#     cli()

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
        x = x.lower().replace(' ', '_')
        x = re.sub(r'[^0-9A-Za-z_]', '', x)
        x = re.sub(r'_+', '_', x)
        return x

    irods_folder = "/".join([
        env['irods_home'].rstrip('/'),
        'mus',
        sanitize(env['eln_project_name']),
        sanitize(env['eln_study_name']),
        sanitize(env['eln_experiment_name']),
    ])

    irecs = {}
    for ir in get_irods_records(irods_folder):
        irecs[ir['name']] = ir

    to_upload = []
    irods_paths = []

    status = {}
    basepath = None
    fn2irods = {}

    for i, rec in enumerate(ElnData.records):
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
                status[ip] = 'ok'
            else:
                status[ip] = 'fail'
                to_upload.append(fp)
        else:
            status[ip] = "?"
            to_upload.append(fp)

    fstat = Counter(status.values())
    click.echo(f"Irods files not uploaded yet             : {fstat['?']}")
    click.echo(f"Irods uploaded, checksum ok, skip        : {fstat['ok']}")
    click.echo(f"Irods uploaded, checksum fail, overwrite : {fstat['fail']}")

    if len(to_upload) > 0:
        # ensure target folder is there
        icmd('imkdir', '-p', irods_folder, )
        icmd('iput', '-K', '-f', *to_upload, irods_folder)

    #create mango files
    for filename, mangourl in fn2irods.items():
        mangofile = filename + '.mango'
        with open(mangofile, 'wt') as F:
            F.write(mangourl)

    #now do metadata
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
        "mgs.project.description": 'test1222123131',
    }
    allpathP = []
    for ip in irods_paths:
        allpathP.append('-d')
        allpathP.append(ip)

    for key, value in irods_meta.items():
        icmd('imeta', 'add', *allpathP, key, value) #f"{key}", f"'{value}'")


def init_irods(cli):
    from mus.cli import files
    cli.add_command(cmd_irods)
    files.filetag.params.append(
        click.Option(['-I', '--irods'], is_flag=True,
                     default=False, help='Save file to iRODS'))


register_hook('plugin_init', init_irods)
register_hook('finish_filetag', finish_file_upload)

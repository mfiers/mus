

import os
import tempfile
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, List

import click
from fpdf import FPDF

from mus.config import get_env, save_env
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


@dataclass
class ELNDATA:
    experimentid: int = -1
    n: int = 0
    title: str = ''
    rest: str = ''
    filesets: List[List[str]] = field(default_factory=list)
    metadata: List[Dict[str,Any]] = field(default_factory=list)

ElnData = ELNDATA()

# ElnData = dict(
#     n = 0,
#     message = '',
#     rest = '',
#     filesets = [],
#     metadata = [],
#     )

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


class PDF(FPDF):
    def print_title(self, title, rest, font='Arial'):
        self.set_font('Arial', 'B', 16)
        self.set_fill_color(232, 204, 139)
        self.cell(0, 6, f' {title}', 1, 1, 'L', 1)
        self.set_font(font, '', 10)
        self.multi_cell(0, 5, rest)
        self.ln()

    def print_fileset(self, fset, mdata, font='Arial'):
        self.set_fill_color(150, 193, 217)
        self.set_font('Arial', 'B', 12)
        fn = os.path.basename(mdata["filename"])
        self.cell(0, 6, fn, 1, 1, 'L', 1)
        self.ln()
        if len(fset) > 1:
            self.set_font(font, 'B', 10)
            fs = '\n'.join([
                f'{os.path.basename(k)}'
                for k in fset
                if k != mdata["filename"]])
            self.multi_cell(0, 5, fs)
            self.ln()
        self.set_font(font, '', 10)
        mf = '\n'.join(
            f'- {k.capitalize().replace("_", " ")}: {v}'
            for (k, v) in sorted(mdata.items())
            if k != 'filename'
        )
        self.multi_cell(0, 5, mf)
        self.ln()

    def print_chapter(self, title, rest, meta):
        self.add_page()
        self.chapter_title(title)
        self.chapter_body(rest, font='Arial')
        self.ln(4)
        self.chapter_body(meta, font='Arial')
        self.ln(4)


def eln_save_record(record):
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

    message = record.message.strip()
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
        eln_comment(experimentid, title, message)
        return
    else:
        # remember everything, to ultimately store all files
        ElnData.n += 1
        if ElnData.n == 1:
            ElnData.experimentid = experimentid
            ElnData.title = title
            ElnData.rest = rest

        # this (group) of files to append
        fileset = []
        # the original file
        fileset.append(record.filename)

        # pdf_convert = False
        pdf_filename = None
        if record.filename.endswith('.ipynb'):
            pdf_filename = convert_ipynb_to_pdf(record.filename)
            if pdf_filename is not None:
                # pdf_convert = True
                fileset.append(pdf_filename)

        # store message
        metadata = {
            k: getattr(record, k)
            for k in ['host', 'cwd', 'user', 'filename',
                      'checksum']
        }
        ntime = datetime\
            .fromtimestamp(record.time)\
            .strftime("%Y-%m-%d %H:%M:%S")
        metadata['upload_time'] = ntime

        ElnData.metadata.append(metadata)
        ElnData.filesets.append(fileset)

        # pdf
        # metapdf = PDF()
        # metapdf.print_chapter(1, title, rest, )
        # metapdf.output(metapdf_filename, 'F')
        #fileset.append(metapdf_filename)


        # journal_id = eln_create_filesection(experimentid, title)

        # eln_file_upload(journal_id, record.filename)
        # if pdf_convert:
        #    eln_file_upload(
        #        journal_id, pdf_filename)
        # eln_file_upload(journal_id, metapdf_filename)

def finish_file_upload():
    metapdf = PDF()
    metapdf.add_page()
    metapdf.print_title(ElnData.title, ElnData.rest)
    metapdf_filename = get_stamped_filename('eln_metadata', 'pdf')
    for fset, mdata in zip(ElnData.filesets, ElnData.metadata):
        metapdf.print_fileset(fset, mdata)
    metapdf.output(metapdf_filename, 'F')
    journal_id = eln_create_filesection(
        experimentid=ElnData.experimentid,
        title=ElnData.title)
    eln_file_upload(journal_id=journal_id,
                    filename=metapdf_filename)
    i = 1
    for fset in ElnData.filesets:
        for fn in fset:
            i += 1
            eln_file_upload(journal_id, fn)
    click.echo(f"Uploaded {i} files to ELN")

register_hook('plugin_init', init_eln)
register_hook('save_record', eln_save_record)
register_hook('prepare_record', add_eln_data_to_record)
register_hook('finish_filetag', finish_file_upload)




# @eln.command()
# def projects():
#     inf = elncall("projects")
#     assert isinstance(inf, dict)
#     print(f"# No projects: {inf['recordCount']}")
#     for rec in inf["data"]:
#         print(f"{rec['projectID']}\t{rec['name']}")


# @eln.command()
# @click.option("-p", "--projectID", type=int)
# @click.option("-n", "--name")
# def studies(projectid, name):
#     inf = elncall("studies", dict(searchName=name, projectID=projectid))
#     assert isinstance(inf, dict)
#     print(f"# No studies: {inf['recordCount']}")
#     for rec in inf["data"]:
#         print(f"{rec['studyID']}\t{rec['name']}")


# @eln.command()
# @click.option("-s", "--studyID", type=int)
# @click.option("-n", "--name")
# def create_experiment(studyid, name):
#     print(studyid, name)
#     inf = elncall(
#         "experiments",
#         dict(studyID=int(studyid), name=name),
#         method="post",
#     )
#     print(inf)


# @eln.command()
# @click.option("-s", "--studyID", type=int)
# @click.option("-n", "--name")
# @click.option("-r", "--raw", is_flag=True, default=False)
# def experiments(studyid, name, raw):
#     inf = elncall(
#         "experiments",
#         {
#             "searchName": name,
#             "studyID": studyid,
#         },
#     )
#     assert isinstance(inf, dict)
#     if raw:
#         print(json.dumps(inf, indent=4))

#     print(f"# No experiments: {inf['recordCount']}")
#     for k in inf["data"]:
#         print(f"{k['experimentID']}\t{k['name']}")


# @eln.command("sections")
# @click.option("-x", "--experimentID", type=int, required=True)
# @click.option("-r", "--raw", is_flag=True, default=False)
# def eln_sections(experimentid, raw):
#     inf = elncall(f"experiments/{experimentid}/sections")
#     if raw:
#         print(json.dumps(inf, indent=4))
#         return
#     print(f"# No sections: {inf['recordCount']}")
#     for rec in inf["data"]:
#         print(
#             f"{rec['order']}\t{rec['sectionType']}\t{rec['expJournalID']}\t{rec['sectionHeader']}"
#         )

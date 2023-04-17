
import logging
from pathlib import Path

from mus.db import Record
from mus.hooks import register_hook
from mus.util.files import get_checksum

lg = logging.getLogger(__name__)
FILE_DOES_NOT_EXIST = '_FILE_DOES_NOT_EXIST'


def track_iofiles_prerun(job):
    """
    Track input files of a job - register prior to running
∑
    """

    if 'input_files' not in job.sysdata:
        job.sysdata['input_files'] = {}

    if 'output_files' not in job.sysdata:
        job.sysdata['output_files'] = {}

    for k, v in job.data.items():
        if v.has_tag('input'):
            input_file = Path(job.rendered[k])
            if not input_file.exists():
                continue
            rec = Record()
            rec.prepare(
                filename=input_file,
                extra_tags=['input'],
                rectype='tag',
                child_of=job.record.uid)
            rec.message = 'iotracker'
            rec.save()
            job.sysdata['input_files'][input_file] = rec

        if v.has_tag('output'):
            output_file = Path(job.rendered[k])
            if not output_file.exists():
                ochk = FILE_DOES_NOT_EXIST
            else:
                ochk = get_checksum(output_file)
            job.sysdata['output_files'][output_file] = ochk


register_hook('start_job', track_iofiles_prerun, priority=20)


def track_iofiles_postrun(job):

    for k, v in job.data.items():
        if v.has_tag('input'):
            input_file = Path(job.rendered[k])
            if not input_file.exists():
                # strange - but lets not complain here...
                continue
            ichk = get_checksum(input_file)
            pre_input_rec = job.sysdata['input_files'][input_file]
            if ichk != pre_input_rec.checksum:
                lg.warning('input file has changes {input_file}')
                rec = Record()
                rec.prepare(
                    filename=input_file,
                    extra_tags=['input', 'changed'],
                    rectype='tag',
                    child_of=job.record.uid)
                rec.message = 'iotracker'
                rec.save()

        if v.has_tag('output'):
            output_file = Path(job.rendered[k])
            rec = Record()
            rec.prepare(
                filename=output_file,
                extra_tags=['output'],
                rectype='tag',
                child_of=job.record.uid)
            pre_output_rec = job.sysdata['output_files'][output_file]
            if pre_output_rec == FILE_DOES_NOT_EXIST:
                rec.add_tag('new')
            elif pre_output_rec == rec.checksum:
                rec.add_tag('unchanged')
            else:
                rec.add_tag('changed')
            rec.message = 'iotracker'
            rec.save()


register_hook('stop_job', track_iofiles_postrun, priority=20)


import os
from functools import lru_cache
from typing import Any, Dict

import requests

from mus.config import get_env
from mus.exceptions import ElnApiKeyNotDefined, ElnURLNotDefined
from mus.hooks import register_hook


def fix_eln_experiment_id(xid):
    """
    Get rid of the 1000000001292564 starting 1 for long form expids

    No clue if this means anything, but the api does not like it.
    """
    if xid > 1e15:
        return int(str(xid)[1:])
    else:
        return xid

def get_eln_apikey() -> str:
    """
    Retrieve the ELN API key from the environment variables.

    Raises:
        ElnApiKeyNotDefined: If the key is not defined

    Returns:
        _type_: str
    """
    env = get_env()
    if 'eln_apikey' in env:
        return env['eln_apikey']
    elif 'ELN_APIKEY' in os.environ:
        return os.environ['ELN_APIKEY']
    else:
        raise ElnApiKeyNotDefined()


def get_eln_url():
    env = get_env()
    if 'eln_url' in env:
        return env['eln_url']
    elif 'eln_url' in os.environ:
        return os.environ['ELN_URL']
    raise ElnURLNotDefined()


def eln_filesection(experimentid, title: str):
    req = elncall(f"experiments/{experimentid}/sections",
                  method='post',
                  data=dict(sectionType='FILE',
                            sectionHeader=title))
    return req.json()  # type: ignore


def eln_comment(experimentid,
                title: str,
                comment: str):

    # create section!
    req = elncall(f"experiments/{experimentid}/sections",
                  method='post',
                  data=dict(sectionType='COMMENT',
                            sectionHeader=title))
    journalId = req.json()  # type: ignore

    # upload comment
    req = elncall(f"experiments/sections/{journalId}/content",
                  method="put",
                  data=dict(contents=comment))

    return journalId


def eln_file_upload(journal_id, filename, description=None):
    with open(filename, 'rb') as F:
        req = elncall(f"experiments/sections/{journal_id}/files",
                      method="post", params={'fileName': filename},
                      data=F)


def elncall(path, params=None, method='get', data=None):
    APIKEY = get_eln_apikey()
    URL = get_eln_url()

    headers = {'Accept': 'application/json',
               'Authorization': APIKEY}

    to_remove = []
    if params is None:
        params = dict()

    for k, v in params.items():
        if v is None:
            to_remove.append(k)
    for k in to_remove:
        del params[k]

    if method == 'get':
        req = requests.get(
            f"{URL}/{path}", headers=headers, params=params)
        return req.json()
    elif method == 'post':
        req = requests.post(
            f"{URL}/{path}", headers=headers,
            data=data,
            params=params)
        return req
    elif method == 'put':
        req = requests.put(
            f"{URL}/{path}", headers=headers,
            data=data, params=params)
        return req


@lru_cache(8)
def expinfo(expid) -> Dict[str, Any]:
    exp = elncall(f'experiments/{expid}')
    assert isinstance(exp, dict)
    collabs = elncall(f'experiments/{expid}/collaborators')
    assert isinstance(collabs, dict)
    studyid = exp['studyID']
    studs = elncall('studies', dict(studyID=studyid))
    assert isinstance(studs, dict)
    assert studs['recordCount'] == 1
    stu = studs['data'][0]
    projs = elncall('projects', dict(projectID=stu['projectID']))
    assert isinstance(projs, dict)
    assert projs['recordCount'] == 1
    pro = projs['data'][0]

    return dict(
        experiment_id=expid,
        experiment_name=exp['name'],
        study_id=exp['studyID'],
        study_name=stu['name'],
        project_id=stu['projectID'],
        project_name=pro['name'],
        collaborator=[
            "{firstName} {lastName}".format(**x) for x in collabs['data']
        ]
    )

def add_eln_data_to_record(record):
    env = get_env()
    for k, v in env.items():
        if not k.startswith('eln_'):
            continue
        if k in ['eln_apikey', 'eln_url']:
            continue
        record.data[k] = v

register_hook('prepare_record', add_eln_data_to_record)
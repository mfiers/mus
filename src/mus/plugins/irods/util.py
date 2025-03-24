import base64
import copy
import hashlib
import json
import logging
import os
import socket
import subprocess as sp
import tempfile
from datetime import datetime
from functools import lru_cache
from importlib import resources as impresources
from pathlib import Path
from typing import Any, Dict, Union
from uuid import uuid4

from mus.config import get_keyring, get_secret

lg = logging.getLogger('plugin.irods.util')


class IrodsHomeNotDefined(Exception):
    pass


class MangoURLNotDefined(Exception):
    pass


class IrodsUploadError(Exception):
    pass


def icmd(*cl, allow_fail=False, process_error=None, **kwargs):

    prefix = get_keyring().get_password('mus', 'icmd_prefix')

    if prefix is None:
        prefix = []
    else:
        prefix = prefix.split()

    if process_error is not None:
        kwargs['stderr'] = sp.PIPE
        kwargs['text'] = True

    cl_ = prefix + list(map(str, cl))
    lg.debug("Executing: " + ' '.join(map(str, cl_)))

    P = sp.Popen(cl_, **kwargs)
    o, e = P.communicate()

    if process_error is not None:

        if len(e.strip) > 0:
            timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
            logfile = f"error_{cl[0]}_{timestamp}.txt"
            with open(logfile, "w") as f:
                f.write(e)

        process_error(e)

    if (not allow_fail) and P.returncode != 0:
        lg.critical("irods fails running")
        lg.critical(" ".join(map(str, cl)))
        exit(-1)

    return o


def get_irods_records(irods_folder):
    d = icmd('ils', '-L',  irods_folder, allow_fail=True, text=True,
             stdout=sp.PIPE, stderr=sp.PIPE).strip()

    # strip first line
    if '\n' not in d:
        return

    d = d.split("\n", 1)[1]
    ls = d.split("\n")

    # TODO: convert this to irodspython

    while True:
        # filter out folders - which I expect at the end
        # starting with 'C-'
        if ls[-1].strip().startswith('C-'):
            ls = ls[:-1]
        else:
            break

    i = 0
    while i < len(ls)-1:
        l1, l2 = ls[i].split(), ls[i+1].split()

        try:
            checksum = l2[0].split(":")[1]
            checksum = base64.b64decode(checksum).hex()
        except:
            #skip this record
            i += 2
            continue
        yield dict(
            name=l1[-1],
            path=irods_folder + '/' + l2[-1],
            checksum=checksum)

        i += 2


@lru_cache(1)
def get_irods_home():
    return get_secret('irods_home', 'Base folder for all stored data')
    if "IRODS_HOME" not in os.environ:
        raise IrodsHomeNotDefined()
    return os.environ["IRODS_HOME"]


@lru_cache(1)
def get_irods_owner():
    if "IRODS_OWNER" not in os.environ:
        return "BADS"  # TODO: change to exception
        # raise IrodsOwnerNotDefined()
    return os.environ["IRODS_OWNER"]


@lru_cache(1)
def get_mango_url():
    """
    Get the mango URL from the environment variables.


    >>> os.environ['MANGO_URL'] = 'https://mango.kuleuven.be/'
    >>> murl = get_mango_url()
    >>> type(murl)
    <class 'str'>
    >>> murl.startswith('https://')
    True

    Raises:
        MangoURLNotDefined: _description_

    Returns:
        _type_: _description_
    """
    if "MANGO_URL" not in os.environ:
        raise MangoURLNotDefined()
    return os.environ["MANGO_URL"].strip().rstrip("/")


@lru_cache(1)
def get_irods_session():
    import ssl

    from irods.session import iRODSSession  # to communicate with ManGO

    # connect to irods
    try:
        env_file = os.environ['IRODS_ENVIRONMENT_FILE']
    except KeyError:
        env_file = Path("~/.irods/irods_environment.json").expanduser()

    ssl_context = ssl.create_default_context(
        purpose=ssl.Purpose.SERVER_AUTH, cafile=None, capath=None, cadata=None
    )
    ssl_settings = {"ssl_context": ssl_context}
    session = iRODSSession(
        irods_env_file=env_file, **ssl_settings
    )  # type: ignore

    return session


@lru_cache(256)
def _get_local_file_checksum(file_path):
    # calculate a sha256 checksum of a given file
    hasher = hashlib.sha256()  # Use SHA-256
    chunk_size = 64 * 1024  # 64KB chunks
    with open(file_path, "rb") as afile:
        while True:
            buf = afile.read(chunk_size)
            if not buf:
                break
            hasher.update(buf)
    return hasher.hexdigest()


def irods_upload(
    filename: str,
    experimentid: int,
    path_prefix: Union[str, None] = None,
    force: bool = False,
    doublecheck: bool = False,
    dryrun: bool = False,
    use_iput_for_upload: bool = True,
    description: Union[str, None] = None,
) -> Dict[str, Any]:

    from mango_mdschema import Schema

    if dryrun:
        raise NotImplementedError("not implemented dryrun")

    IRODS_HOME = get_irods_home()
    lg.debug("Get iRODS Session")
    session = get_irods_session()

    assert os.path.exists(filename)

    path_head, filebasename = os.path.split(filename)

    if not os.path.isabs(filename):
        path_prefix = path_head

    filename = str(Path(filename).expanduser().resolve())

    # first get some information
    file_meta = expinfo(experimentid)
    file_meta["server"] = socket.getfqdn()
    file_meta["path"] = filename
    file_meta["upload_date"] = datetime.now()

    if description is not None:
        file_meta["description"] = description

    try:
        mdfile = (
            impresources.files(darkmoon.data) / "project-7.0.0-published.json"
        )
        metadata = mdfile.open("rt").read()  # type: ignore
    except AttributeError:
        # assuming older version - use legacy import
        from pkg_resources import resource_string as resource_bytes

        metadata = resource_bytes(
            "darkmoon.data", "project-7.0.0-published.json"
        ).decode("utf-8")

    mdtmp = None

    # need to write it to a tempfile - the Schema thing needs to
    # read from a file?
    with tempfile.NamedTemporaryFile("wt", delete=False) as F:
        F.write(metadata)
        mdtmp = F.name
    schema = Schema(mdtmp)
    os.unlink(mdtmp)

    irods_collection = (
        f"{IRODS_HOME}/"
        + f"{file_meta['project_name']}/"
        + f"{file_meta['study_name']}/"
        + f"{file_meta['experiment_name']}"
    )
    if path_prefix is not None:
        irods_collection = f"{irods_collection}/{path_prefix}"

    irods_collection = irods_collection.replace(" ", "_").replace("//", "/")

    subcoll = session.collections.create(irods_collection)
    irods_fullpath = f"{irods_collection}/{filebasename}"
    external_url = (
        f"{get_mango_url().rstrip('/')}"
        + f"/data-object/view/{irods_fullpath.strip('/')}"
    )

    local_checksum = _get_local_file_checksum(filename)

    # check if an object with this name already exists - if it does we do not
    # overwrite without using force
    thisobject = None
    for do in subcoll.data_objects:  # type: ignore
        if do.name == filebasename:  # type: ignore
            thisobject = do
            break

    if thisobject is not None:
        # check the remote checksum is ok
        remote_checksum = thisobject.chksum().split(":")[1]
        remote_checksum = base64.b64decode(remote_checksum).hex()

        if local_checksum == remote_checksum:
            lg.debug(
                f"File exists, same checksum, ignore: '{irods_fullpath}'!"
            )
            status = "exists"
        else:
            if not force:
                raise FileExistsError("File exists, Checksum differs")
            else:
                status = "overwrite"
                lg.debug(f"Overwriting '{irods_fullpath}'!")
    else:
        status = "new"

    # unless it already exists, upload the file
    if status != "exists":
        lg.debug("Start Upload to: " + irods_fullpath)
        if use_iput_for_upload:
            os.system(f"iput '{filename}' '{irods_collection}'")
        else:
            session.data_objects.put(filename, irods_collection)

    # check (again) if the object exists - it should - we just uploaded it!
    # this is to make sure we have the correct remote object
    thisobject = None
    for do in subcoll.data_objects:  # type: ignore
        if do.name == filebasename:  # type: ignore
            thisobject = do
            break

    # if we did not find the remote object - something is seriously wrong
    if thisobject is None:
        raise IrodsUploadError("Object upload error, object not found")

    # recursivly set the permissions on the top folder
    # bit brute force
    irods_collection_top = f"{IRODS_HOME}/{file_meta['project_name']}"
    subcoll_top = session.collections.create(irods_collection_top)
    acl_top = session.acls.get(subcoll_top)[0]
    acl_top.user_name = get_irods_owner()
    acl_top.access_name = "own"
    session.acls.set(acl_top, recursive=True)

    # get the remote checksum and compare with the local one
    remote_checksum = thisobject.chksum().split(":")[1]
    remote_checksum = base64.b64decode(remote_checksum).hex()

    if local_checksum != remote_checksum:
        raise IrodsUploadError("Object upload error, checksum mismatch")

    lg.debug("Remote checksum is ok: " + remote_checksum)
    status += ", chksum ok"
    if doublecheck:

        lg.info("re-downloading file to doublecheck checksum")
        tmpname = f"{uuid4().hex}.tmp"
        session.data_objects.get(thisobject.path, tmpname)
        new_sha256 = _get_local_file_checksum(tmpname)
        if new_sha256 != remote_checksum:
            raise IrodsUploadError(
                (
                    "Object upload error, doublecheck sha256 "
                    + f"fail (see {tmpname})"
                )
            )
        else:
            lg.debug("Doublecheck sha256 is ok!")
            status += ", dblchk ok"
            os.unlink(tmpname)

    file_meta["sha256"] = local_checksum

    lg.debug("Applying metadata")
    fmdump = copy.copy(file_meta)
    fmdump["upload_date"] = str(fmdump["upload_date"])

    lg.debug(json.dumps(fmdump, indent=4))
    schema.apply(thisobject, file_meta)
    lg.debug("Done uploading: " + filebasename)

    # not formal metadata
    file_meta["url"] = external_url
    file_meta["status"] = status
    if path_prefix is not None:
        file_meta["nice_path"] = f"{path_prefix}/{filebasename}"
    else:
        file_meta["nice_path"] = filename

    return dict(file_meta)

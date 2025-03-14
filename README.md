# mus

Mark's random set of utilities

## Install

Preferred install using `pipx`:

Add requirements for optional plugins using `[plugin]` syntax

```
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[all]"
```

If you want to add plugins later:

```
pipx upgrade "git+ssh://git@github.com/mfiers/mus.git#egg=mus[dev]"
```

Or simply update to the latest version:

```
pipx upgrade "git+ssh://git@github.com/mfiers/mus.git#egg=mus[dev]"
```

**Note: You need at least python 3.10+**


### Development install

git clone and then:

```
pipx install -e .[all]
```

### Keyring

`mus` uses [keyring](https://github.com/jaraco/keyring) to store secrets. On a headless(?) linux server you may need to set a password to your secrets database, add this to your .bashrc

```
export KEYRING_CRYPTFILE_PASSWORD="very secure password"
```

(Note: replace 'a very secure password' with something that is actually secure)

## ELN plugin

Note: for automatic conversion of ipynb to pdf, you need to install `pandoc` and `texlive-xetex`.

### Install

### Credentials

To use the ELN plugin, you need to get an API key from your ELN, see here: https://www.elabjournal.com/doc/GetanAPIToken.html

Store login credentials & base url:
```
cd $HOME
mus config secret-set eln_apikey <SECRET ELN API KEY>
mus config secret-set eln_url <ELN_URL>
```


### Usage

All ELN interaction happens on the level of ELN experiments. To link mus activity to an experiment, you need to create an experiment, and get the experiment ID from ELN:

<img width="346" alt="image" src="https://github.com/user-attachments/assets/c46887f0-3d8e-469e-bd12-af97c8b5c27b">

Subsequently you can store project, study and experiment IDs and titles from ELN, and store it in the local `.env` file for further use with:

```
mus eln tag-folder -x [eln-experiment-id]
```

If you've done this, the stored experiment-id will automatically be used when required.

You can also directly store a message on ELN using `mus log` with the `-E` flag:

```
mus log -E 'this is a test message'
```

If you've tagged the folder with eln data, then the experiment id will be picked up from that data, otherwise you need to specify the eln experiment id"

```
mus log -x [eln-experiment-id] -E 'this is a test message'
```

It is possible to upload a file to ELN using `mus tag`, again with the `-e` flag:

**Note: This upload to ELN, not to Mango/IRODS**

```
mus eln upload -m "short title or description" [filename] [filename] ...
```

Note, when uploading files, `*.ipynb` files will automatically be converted to timestamped PDF files, and these will be uploaded to ELN as well.

## iRODs plugin

This plugin relies on the ELN plugin being up & running. Without ELN metadata on project, study and experiment it will not upload anything.

Files that are uploaded to irods will not be uploaded to ELN (except pdf, ipynb and png files). Instead a small pdf with a link to irods will be uploaded to ELN.

### Prerequisites & configuration

- Install Irods i-commands & make sure you are authenticated.

```
# Base URL for mango links to irods objects
mus config secret-set irods_web '<IRODS_WEB_BASE>'

# Base URL to store data on irods
mus config secret-set irods_home '<IRODS_HOME_FOLDER>'

# Irods group name to own uploaded files
mus config secret-set irods_group '<IRODS_GROUP>'

```

### MacOS

you can run the icommands in docker, configure `mus` using:

```
mus secret-set icmd_prefix "docker run --platform linux/amd64  -i --rm -v $HOME:$HOME -v $HOME/.irods/:/root/.irods ghcr.io/utrechtuniversity/docker_icommands:0.2"
```

## Uploading files to IRODS

Uploading files to IRODS can be done using:

```
mus irods upload -m 'comment about file' [FILENAME]...
```

This uploads the file to iRODs and thenmakes a record on the elabjournal with the iRODS link. You must have a elabjournal experiment id for this to work (so, have executed `mus eln tag-folder -x [eln-experiment-id]`)


## History logging

(deprecated at the moment)


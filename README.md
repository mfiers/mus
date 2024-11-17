# mus
Mark's random set of utilities

## Install

Preferred install using `pipx`:

Add requirements for optional plugins using `[plugin]` syntax

```
pipx install "git+ssh://git@github.com/mfiers/mus.git#egg=mus[eln]"
```

If you want to add plugins later:

```
pipx upgrade "git+ssh://git@github.com/mfiers/mus.git#egg=mus[dev]"
```

## ELN plugin

Note: for automatic conversion of ipynb to pdf, you need to install `pandoc` and `texlive-xetex`.

### Install

### Get credentials

To use the ELN plugin, you need to get an API key from your ELN, see here: https://www.elabjournal.com/doc/GetanAPIToken.html

Set the values as follows (in a `$HOME/.env` file):

```
cd $HOME
mus config set eln_apikey [replace.with.your.eln.key]
mus config set eln_url https://vib.elabjournal.com/api/v1/
# ensure 0600 permissions
chmod go-rwX ~/.env
```

### Usage

All ELN interaction happens on the level of ELN experiments. To link mus activity to an experiment, you need to create an experiment, and get the experiment ID from ELN:

<img width="346" alt="image" src="https://github.com/user-attachments/assets/c46887f0-3d8e-469e-bd12-af97c8b5c27b">

Subsequently you can store project, study and experiment IDs and titles from ELN, and store it in the local `.env` file for further use with:

```
mus eln tag-folder -x [eln-experiment-id]
```

If you've done this, the stored experiment-id will automatically be used when required.

You can also directly store a message on ELN using `mus log` with the `-e` flag:

```
mus log -e 'this is a test message'
```

If you've tagged the folder with eln data, then the experiment id will be picked up from that data, otherwise you need to specify the eln experiment id"

```
mus log -e -x [eln-experiment-id] 'this is a test message'
```

It is possible to upload a file to ELN using `mus tag`, again with the `-e` flag:

```
mus tag -e [filename] with a short title or description
```

Or, if the folder was not tagged yet:


```
mus tag -e  -x [eln-experiment-id] [filename] with a short title or description
```

Note, when uploading files, `*.ipynb` files will automatically be converted to timestamped PDF files, and these will be uploaded to ELN as well.


## History logging

(deprecated at the moment)

### Bash:

```bash
export MUS_USER='mf'
export MUS_HOST='vsc'
export MUS_LAST_MACRO=""
export PATH="${HOME}/project/mus:${PATH}"
alias m="# "

function MUS_PROMPT_COMMAND {
    lasthist=$(history 1)
    # ( echo "$? ${lasthist}" | mus-hist & )
    _rex=" *[0-9]+ +m "
    if [[ "$lasthist" =~ $_rex  ]]
	then
        # this prevents rerunning the macro if
        # a command does not end up in the history (eg, empty command)
        if [[ "${lasthist}" != "${MUS_LAST_MACRO}" ]]
        then
            echo "$lasthist" | mus macro stdin-exe;
            export MUS_LAST_MACRO="${lasthist}"
        fi
    fi
}
export PROMPT_COMMAND=MUS_PROMPT_COMMAND
```

### zsh:

```zsh
function MUS_PROMPT_COMMAND {
    lasthist=`history -1`
    # ( echo "$? ${lasthist}" | mus-hist & )
    _rex=" *[0-9]+ +m "
    if [[ "$lasthist" =~ $_rex  ]]
	then
        # this prevents rerunning the macro if
        # a command does not end up in the history (eg, empty command)
        if [[ "${lasthist}" != "${MUS_LAST_MACRO}" ]]
        then
            echo "$lasthist" | mus macro stdin-exe;
            export MUS_LAST_MACRO="${lasthist}"
        fi
    fi
}

precmd_functions+=(MUS_PROMPT_COMMAND)
```

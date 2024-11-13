# mus
Mark's random set of utilities

## Install

Preferred install using `pipx`:

```
pipx install "git+ssh://git@github.com/mfiers/mus.git"
```


## ELN plugin

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

All ELN interaction happens on the level of ELN experiments. To link mus activity to an experiment, you need to get the experiment ID from ELN.


To store a message on ELN:

```
mus log -e 'this is a test message



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

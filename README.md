# mus
Mark's random set of utilities

## Install

Preferred install using `pipx`:

```
pipx install "git+ssh://git@github.com/mfiers/mus.git"
```


## ELN plugin

Set your ELN Key * base-url:

```
cd $HOME
mus config set eln_apikey [replace.with.your.eln.key]
mus config set eln_url https://vib.elabjournal.com/api/v1/
chmod go-rwX ~/.env
```



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

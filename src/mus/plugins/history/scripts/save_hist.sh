#!/usr/bin/env bash

export HISTTIMEFORMAT="";

log_to_sqlite() {
    # Capture command, hostname, cwd, and timestamp
    local command=$(history 1 | sed 's/^ *[0-9]* *//')
    command=${command//\'/\'\'}
    local hostname=$(hostname)
    local user=$(whoami)
    local uuid="$(uuidgen | tr '[:upper:]' '[:lower:]')"
    local cwd=$(pwd)
    local timestamp=$(date +%s)
    local rc=$last_return_code
    local bc=$BASH_COMMAND
    # echo $rc $command

    # Use SQLite parameterized query to safely insert data
    sqlite3 ~/.local/mus/mus.db  <<EOF
        INSERT INTO muslog
            (host, cwd, user, cl, time, type, status, uid)
        VALUES ("$hostname", '$cwd', '$user', '$command',
                $timestamp, "history", $rc, "$uuid" );
EOF
}

trap 'last_return_code=$?' DEBUG

# Use PROMPT_COMMAND to log details right before displaying the prompt again
PROMPT_COMMAND=log_to_sqlite

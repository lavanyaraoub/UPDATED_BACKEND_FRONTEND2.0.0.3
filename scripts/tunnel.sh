#!/bin/bash
set -e

LOG=/mnt/app/jamun/ngrok.log

while getopts a:t:p: flag
do
    case "${flag}" in
        a) action=${OPTARG} ;;
        t) token=${OPTARG} ;;
        p) pwd=${OPTARG} ;;
    esac
done

startHTTPTunnel() {
    # stop any existing ngrok agent (free plan allows only one)
    pkill ngrok || true

    sleep 1

    : > "$LOG"

    /usr/local/bin/ngrok authtoken "$token"

    /usr/local/bin/ngrok http 8000 \
        --log="$LOG" \
        --log-format=logfmt \
        </dev/null >/dev/null 2>&1 &
}
stopTunnel() {
    pkill ngrok || true
    : > "$LOG"
}

case "$action" in
    STARTHTTP)
        startHTTPTunnel
        ;;
    KILL)
        stopTunnel
        ;;
    *)
        echo "Unknown action: $action" >> "$LOG"
        exit 1
        ;;
esac

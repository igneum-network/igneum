#!/usr/bin/env sh
# Stops the dev node started by tools/devnode.sh: by its recorded pid only, after reading the command line.
set -eu
DATA=$1
PID=$(cat "$DATA/geth.pid")
CMD=$(ps -p "$PID" -o args= 2>/dev/null || true)
case "$CMD" in
  *geth*--dev*) kill "$PID"; echo "stopped pid $PID ($(echo "$CMD" | cut -c1-60))";;
  "") echo "pid $PID is not running";;
  *) echo "pid $PID is not the dev node ($CMD); not killed"; exit 1;;
esac

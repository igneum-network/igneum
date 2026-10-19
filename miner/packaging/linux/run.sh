#!/usr/bin/env bash
# The supervised runner for hosts without a systemd user session: start | stop | status | restart. The miner is
# restarted ten seconds after any exit; the pid files are the only handles (the stop reads the command line first).
set -u
opt=$(cd "$(dirname "$0")" && pwd); state="$HOME/.local/state/ember"; conf="$HOME/.config/ember/ember.conf"
mkdir -p "$state"
loop() {
  echo $$ > "$state/runner.pid"
  while true; do
    IGNEUM_WORKER_CACHE="$state/worker-cache" "$opt/ember" mine --config "$conf" --every 30 >> "$state/ember.log" 2>&1 &
    echo $! > "$state/ember.pid"; wait $!; echo "$(date -u +%FT%TZ) ember exited $?; restarting in 10 s" >> "$state/ember.log"; sleep 10
  done
}
case "${1:-status}" in
  start) if [ -f "$state/runner.pid" ] && kill -0 "$(cat "$state/runner.pid")" 2>/dev/null; then echo "already running"; exit 0; fi; nohup bash "$0" loop >> "$state/runner.log" 2>&1 & echo "runner started";;
  loop) loop;;
  stop) for f in ember runner; do p=$(cat "$state/$f.pid" 2>/dev/null || true); [ -n "$p" ] && kill -0 "$p" 2>/dev/null && { tr '\0' ' ' < /proc/$p/cmdline | grep -q "ember" && kill "$p" && echo "$f $p stopped"; }; done;;
  status) for f in runner ember; do p=$(cat "$state/$f.pid" 2>/dev/null || true); if [ -n "$p" ] && kill -0 "$p" 2>/dev/null; then echo "$f pid $p up"; else echo "$f down"; fi; done; tail -2 "$state/ember.log" 2>/dev/null;;
  restart) "$0" stop; sleep 1; "$0" start;;
esac

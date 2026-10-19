#!/usr/bin/env bash
# The Igneum Wallet devnet build's launcher (Linux and macOS): start | stop | status | url. Start runs the engine
# from this package's bin with the page from its ui, on a loopback port of the engine's choosing, and opens the page
# in the default browser (--open); the signer is the engine's own child. The pid file is the only handle: stop reads
# the pid's command line before signalling it and never kills by name. State: ~/.igneum-wallet (the vault, settings,
# history) and ~/.local/state/igneum-wallet (the pid file and the log).
set -u
here=$(cd "$(dirname "$0")" && pwd); state="$HOME/.local/state/igneum-wallet"
mkdir -p "$state"
case "${1:-start}" in
  start)
    if [ -f "$state/engine.pid" ] && kill -0 "$(cat "$state/engine.pid")" 2>/dev/null; then
      echo "already running (pid $(cat "$state/engine.pid")); the page url: $0 url"; exit 0
    fi
    nohup "$here/bin/igneum-wallet-engine" --ui "$here/ui" --signer "$here/bin/igneum-signer" --open >> "$state/engine.log" 2>&1 &
    echo $! > "$state/engine.pid"
    sleep 2
    grep '^ENGINE' "$state/engine.log" | tail -1 | sed 's/^ENGINE //'
    echo "started (pid $(cat "$state/engine.pid")); the browser opens the page; otherwise open the url above";;
  stop)
    p=$(cat "$state/engine.pid" 2>/dev/null || true)
    if [ -n "$p" ] && kill -0 "$p" 2>/dev/null && ps -p "$p" -o command= | grep -q igneum-wallet-engine; then
      kill "$p" && echo "stopped $p"
    else
      echo "not running (or the pid file is stale: removed)"
    fi
    rm -f "$state/engine.pid";;
  status)
    p=$(cat "$state/engine.pid" 2>/dev/null || true)
    if [ -n "$p" ] && kill -0 "$p" 2>/dev/null; then echo "engine pid $p up"; else echo "engine down"; fi;;
  url)
    grep '^ENGINE' "$state/engine.log" | tail -1 | sed 's/^ENGINE //';;
  *) echo "usage: $0 start|stop|status|url"; exit 2;;
esac

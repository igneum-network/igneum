#!/usr/bin/env sh
# The Tauri window host under a virtual display (xvfb-run) on the box: it must start, spawn the engine and the
# signer, keep the window open, and the engine must answer on its tokened URL. Run from wallet/ after
# `cargo build -p igneum-wallet-app` (and `cargo build` for the engine and the signer). Stops by recorded pids.
set -u
cd "$(dirname "$0")/.."
BIN=target/debug
RPC=${NODE_RPC:-http://127.0.0.1:8545}
WORK=$(mktemp -d)
echo "work dir $WORK"
export IGNEUM_WALLET_ENGINE="$PWD/$BIN/igneum-wallet-engine" IGNEUM_WALLET_UI="$PWD/app/ui" IGNEUM_WALLET_DATA="$WORK/data" IGNEUM_SIGNER_LIGHT_KDF=1
# no accessibility bus and no compositing under a virtual display (GTK and WebKitGTK otherwise wait or fail)
export NO_AT_BRIDGE=1 WEBKIT_DISABLE_COMPOSITING_MODE=1 WEBKIT_DISABLE_DMABUF_RENDERER=1
# a D-Bus session as well: without one GTK waits on the session bus and the host never reaches its setup
dbus-run-session -- xvfb-run -a -s "-screen 0 1280x800x24" $BIN/igneum-wallet-app > "$WORK/app.out" 2> "$WORK/app.err" &
echo $! > "$WORK/app.pid"
sleep 15
APP=$(pgrep -f "^$BIN/igneum-wallet-app" | tail -1)
if [ -n "$APP" ] && ps -p "$APP" > /dev/null; then echo "window host up after 15 s (pid $APP)"; else echo "window host exited:"; tail -20 "$WORK/app.err"; exit 1; fi
echo "host stderr:"; grep -E 'igneum-wallet-app:' "$WORK/app.err" | tail -5 | cut -c1-140
ENG=$(pgrep -f "igneum-wallet-engine.*--data-dir $WORK/data" | head -1)
SIG=$(pgrep -f "igneum-signer" -n)
echo "engine pid ${ENG:-none}, signer pid ${SIG:-none}"
[ -n "$ENG" ] || { echo "no engine"; tail -20 "$WORK/app.err"; kill "$APP"; exit 1; }
PORT=$(ss -lntp 2>/dev/null | grep "pid=$ENG," | sed -n 's/.*127\.0\.0\.1:\([0-9]*\).*/\1/p' | sort -u | grep -v '^26811$' | head -1)
echo "engine listening on 127.0.0.1:${PORT:-?}"
# the token is in the URL the host read; a wrong token must be refused, which proves the listener is the engine's
curl -s "http://127.0.0.1:$PORT/t/0000/api/state" | head -c 80; echo
[ -f "$WORK/data/settings.json" ] && echo "settings written: $(cat "$WORK/data/settings.json" | tr -d '\n' | cut -c1-80)"
ps -o pid,etimes,rss,args -p "$APP" | tail -1 | cut -c1-100
kill "$APP"; sleep 2; kill "$(cat "$WORK/app.pid")" 2>/dev/null; sleep 1
ps -p "$ENG" > /dev/null && { echo "engine still up after the host closed: stopping it by pid"; kill "$ENG"; } || echo "engine stopped with the host"
echo WINDOW-SMOKE-DONE

#!/usr/bin/env sh
# The bridge from a real site in a real browser: an engine with the bridge on a port, a test site served on its own
# origin (python's http.server), Playwright driving both tabs (tools/bridge-browser-drive.mjs). Stops both by pid.
set -u
cd "$(dirname "$0")/.."
BIN=target/debug
RPC=${NODE_RPC:-http://127.0.0.1:8545}
export PLAYWRIGHT_DIR=${PLAYWRIGHT_DIR:-${IGNEUM_BOX_ROOT:-/srv}/builds/product-b/playwright}
BPORT=26898; SPORT=8099
WORK=$(mktemp -d)
echo "work dir $WORK"
IGNEUM_SIGNER_LIGHT_KDF=1 $BIN/igneum-wallet-engine --data-dir "$WORK/engine" --rpc $RPC --signer $BIN/igneum-signer --ui app/ui --bridge-port $BPORT > "$WORK/engine.out" 2> "$WORK/engine.err" &
echo $! > "$WORK/engine.pid"
(cd tools/site && python3 -m http.server $SPORT --bind 127.0.0.1 > "$WORK/site.log" 2>&1 &
echo $! > "$WORK/site.pid")
trap 'kill "$(cat "$WORK/engine.pid")" 2>/dev/null; kill "$(cat "$WORK/site.pid")" 2>/dev/null; true' EXIT
sleep 3
URL=$(grep '^ENGINE' "$WORK/engine.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])')
curl -s -X POST "${URL}api/import" -H 'Content-Type: application/json' -d '{"mode":"seed","data":"test test test test test test test test test test test junk","password":"correct horse battery"}' > /dev/null
node tools/bridge-browser-drive.mjs "$URL" $BPORT "http://127.0.0.1:$SPORT/index.html"

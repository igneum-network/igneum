#!/usr/bin/env sh
# The page in a real browser: starts an engine against a node (NODE_RPC, default the dev node), then drives the page
# with Playwright's headless Chromium (tools/page-drive.mjs). Playwright is the build user's test tool under
# PLAYWRIGHT_DIR (default builds/product-b/playwright under the box root); the screenshot lands in SHOT. Stops the engine by pid.
set -u
cd "$(dirname "$0")/.."
BIN=target/debug
RPC=${NODE_RPC:-http://127.0.0.1:8545}
export PLAYWRIGHT_DIR=${PLAYWRIGHT_DIR:-${IGNEUM_BOX_ROOT:-/srv}/builds/product-b/playwright}
WORK=$(mktemp -d)
echo "work dir $WORK"
IGNEUM_SIGNER_LIGHT_KDF=1 $BIN/igneum-wallet-engine --data-dir "$WORK/engine" --rpc $RPC --signer $BIN/igneum-signer --ui app/ui --bridge-port 0 > "$WORK/engine.out" 2> "$WORK/engine.err" &
echo $! > "$WORK/engine.pid"
trap 'kill "$(cat "$WORK/engine.pid")" 2>/dev/null || true' EXIT
sleep 3
URL=$(grep '^ENGINE' "$WORK/engine.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])')
echo "engine at ${URL%%/t/*}/t/..."
SHOT=${SHOT:-$WORK/page.png} node tools/page-drive.mjs "$URL"
rc=$?
echo "screenshot: ${SHOT:-$WORK/page.png}"
exit $rc

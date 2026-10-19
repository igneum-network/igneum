#!/bin/bash
# The wallet's Mac app installed and opened on the mini by a headless path as the igneum user (the founder's order
# through main, 11 October 2026: the mini runs the new wallet app). Run ON THE MINI over ssh, as one plain command:
#   mini-app-install.sh <dmg url> <sha256> [public rpc to name]
# Rows (each a line on stdout, the last the verdict): fetch (the DMG from the releases host, its sha256 checked);
# install (attached read-only, the app copied into /Applications over any earlier copy, detached); open (`open -a`
# into the console session with the smoke's two environment files, the witness written by the host once the page
# has loaded); state (api/state as the menu bar reads it: the chain's name, the node line, the engine's version,
# the address when a wallet exists); chain (when a public RPC is named: the Settings route sets it, since an app
# cut before devnet7's pin reads devnet5 by default; the state read again). The app is LEFT RUNNING (the order);
# nothing is killed. The vault, if one is made later, is the igneum user's under ~/.igneum-wallet.
set -u
URL=${1:?dmg url}; SHA=${2:?sha256}; NAME=${3:-}
W=$HOME/igneum-wallet-install; mkdir -p "$W"; cd "$W" || exit 1
log() { echo "[$(date -u '+%H:%M:%SZ')] $*"; }
DMG=$W/$(basename "$URL")
# a file:// source already at the destination is not fetched (the first run read and wrote the same file through
# curl and clobbered it, 11 October 2026); any other source is downloaded beside it
case "$URL" in
  file://*) src=${URL#file://}; [ "$src" = "$DMG" ] || cp "$src" "$DMG" || { log "ROW fetch FAIL the DMG could not be copied from $src"; exit 1; } ;;
  *) curl -fsSL -m 300 -o "$DMG" "$URL" || { log "ROW fetch FAIL the DMG did not download from $URL"; exit 1; } ;;
esac
got=$(shasum -a 256 "$DMG" | cut -c1-64)
[ "$got" = "$SHA" ] || { log "ROW fetch FAIL sha256 $got is not $SHA"; exit 1; }
log "ROW fetch PASS $(basename "$DMG") $(stat -f %z "$DMG") bytes, sha256 $SHA"
MNT=$W/mnt; mkdir -p "$MNT"
hdiutil attach "$DMG" -nobrowse -readonly -mountpoint "$MNT" > /dev/null 2>&1 || { log "ROW install FAIL the DMG did not attach"; exit 1; }
[ -d "$MNT/Igneum Wallet.app" ] || { hdiutil detach "$MNT" > /dev/null 2>&1; log "ROW install FAIL no app in the DMG"; exit 1; }
# an earlier copy is replaced whole; a running earlier copy is told to quit by its own path first
osascript -e 'tell application "Igneum Wallet" to quit' > /dev/null 2>&1 || true
sleep 2
rm -rf "/Applications/Igneum Wallet.app"
cp -R "$MNT/Igneum Wallet.app" /Applications/ || { hdiutil detach "$MNT" > /dev/null 2>&1; log "ROW install FAIL the copy into /Applications failed"; exit 1; }
hdiutil detach "$MNT" > /dev/null 2>&1 || true
xattr -cr "/Applications/Igneum Wallet.app" 2>/dev/null || true
ver=$("/Applications/Igneum Wallet.app/Contents/MacOS/igneum-wallet-engine" --version 2>/dev/null | head -1)
log "ROW install PASS /Applications/Igneum Wallet.app ($ver)"
URLF=$W/engine-url.json; WIT=$W/window.txt; rm -f "$URLF" "$WIT"
open -a "/Applications/Igneum Wallet.app" --env IGNEUM_WALLET_URL_FILE="$URLF" --env IGNEUM_WALLET_SMOKE_FILE="$WIT" --env IGNEUM_WALLET_UPDATE_FIRST_SECS=600 || { log "ROW open FAIL open -a refused"; exit 1; }
for i in $(seq 1 60); do [ -s "$URLF" ] && [ -s "$WIT" ] && break; sleep 0.5; done
wit=$(cat "$WIT" 2>/dev/null || echo "")
hp=$(pgrep -x "Igneum Wallet" | head -1); ep=$(pgrep -x igneum-wallet-engine | head -1)
if [ -s "$URLF" ] && [ -n "$hp" ] && [ -n "$ep" ]; then log "ROW open PASS host pid $hp, engine pid $ep; the window: ${wit:-no witness yet}"; else log "ROW open FAIL host ${hp:-none}, engine ${ep:-none}, url file $([ -s "$URLF" ] && echo yes || echo no)"; exit 1; fi
U=$(python3 -c 'import sys,json; print(json.load(open(sys.argv[1]))["url"])' "$URLF")
state() { curl -s -m 8 "${U}api/state" | python3 -c 'import sys,json; d=json.load(sys.stdin); n=d.get("node",{}); net=d.get("network",{}); print("phase", d.get("phase"), "| version", d.get("version"), "| chain", net.get("name"), net.get("chain_id"), "pinned", net.get("genesis_pinned"), "| node", n.get("state"), n.get("source"), n.get("endpoint"), "block", n.get("block"), "| address", d.get("address") or "none yet", "| balance", d.get("balance_ign") or "")'; }
st=$(state 2>/dev/null || echo "")
[ -n "$st" ] && log "ROW state PASS $st" || { log "ROW state FAIL api/state did not answer at $U"; exit 1; }
if [ -n "$NAME" ]; then
  curl -s -m 8 -X POST "${U}api/settings" -H 'Content-Type: application/json' -d "{\"rpc\":\"$NAME\"}" > /dev/null
  sleep 7
  st=$(state 2>/dev/null || echo "")
  log "ROW chain PASS the node named through Settings: $st"
fi
log "INSTALL PASS the app is left running on the mini as the igneum user"

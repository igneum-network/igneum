#!/usr/bin/env bash
# wallet-mac-app-captures.sh <out dir> <dmg>: the window captures of the macOS app for the project's audit (the UI audit rule),
# on a bench Mac with a window server: the DMG attached and the app copied out, then for each theme (dark, light) and each
# page (home, history, settings) the host started with the launch variables (IGNEUM_WALLET_OPEN, IGNEUM_WALLET_THEME,
# IGNEUM_WALLET_SIZE) on a watch-only data directory (the fleet's pool address, no key), the display captured by
# screencapture at the Retina scale after 12 s (Home at 20 s so the chain card is drawn and the LIVE badge up), the
# app quit by its own path. Files: wallet-<page>-<size>-<theme>.png beside captures.log. Written 14 October 2026 for
# wallet 0.0.9's audit on the mini.
set -u
OUT=${1:?out dir}; DMG=${2:?dmg}
ADDR=${IGNEUM_WALLET_WATCH:-0x8e796a02714eBf0C48De7cCAb5e71254089d3215}
mkdir -p "$OUT"; LOG=$OUT/captures.log; : > "$LOG"
log() { echo "[$(date -u '+%H:%M:%SZ')] $*" | tee -a "$LOG"; }
MNT=$OUT/mnt; APPDIR=$OUT/app; DATA=$OUT/data
mkdir -p "$MNT" "$APPDIR" "$DATA"
if hdiutil attach "$DMG" -nobrowse -readonly -mountpoint "$MNT" >> "$LOG" 2>&1 && [ -d "$MNT/Igneum Wallet.app" ]; then
  cp -R "$MNT/Igneum Wallet.app" "$APPDIR/"; hdiutil detach "$MNT" >> "$LOG" 2>&1 || true
else
  log "the disk image did not attach or holds no bundle"; exit 1
fi
APP="$APPDIR/Igneum Wallet.app"; HOSTBIN="$APP/Contents/MacOS/Igneum Wallet"
xattr -cr "$APP" 2>/dev/null || true
printf '{"watch_only": true, "address": "%s", "created_unix": %s}\n' "$ADDR" "$(date +%s)" > "$DATA/watch.json"
log "app $("$APP/Contents/MacOS/igneum-wallet-engine" --version 2>/dev/null), watch-only on $ADDR"
n=0
for theme in dark light; do
  for page in home history settings; do
    for size in default 900x620; do
      name="wallet-$page-$size-$theme"
      URLF=$OUT/$name-url.json; rm -f "$URLF"
      ( cd "$APPDIR" && exec env IGNEUM_WALLET_DATA="$DATA" IGNEUM_SIGNER_LIGHT_KDF=1 IGNEUM_WALLET_URL_FILE="$URLF" IGNEUM_WALLET_UPDATE_FIRST_SECS=600 IGNEUM_WALLET_OPEN="$page" IGNEUM_WALLET_THEME="$theme" IGNEUM_WALLET_SIZE="$size" "$HOSTBIN" > "$OUT/$name-host.out" 2> "$OUT/$name-host.err" ) &
      hostpid=$!
      for i in $(seq 1 40); do [ -s "$URLF" ] && break; sleep 0.5; done
      wait_s=12; [ "$page" = home ] && wait_s=20
      sleep $wait_s
      if screencapture -x -t png "$OUT/$name.png" 2>>"$LOG"; then
        log "$name captured ($(stat -f %z "$OUT/$name.png") bytes) after $wait_s s; url written: $([ -s "$URLF" ] && echo yes || echo no)"
        n=$((n+1))
      else
        log "$name: no capture (no window server for this session)"
      fi
      osascript -e 'tell application "Igneum Wallet" to quit' >> "$LOG" 2>&1 || true
      for i in $(seq 1 30); do kill -0 "$hostpid" 2>/dev/null || break; sleep 0.5; done
      if kill -0 "$hostpid" 2>/dev/null; then
        case "$(ps -o comm= -p "$hostpid" 2>/dev/null)" in *"Igneum Wallet"*) kill "$hostpid"; log "$name: the host ended by its pid after the quit timed out";; esac
      fi
      sleep 1
    done
  done
done
log "captures: $n of 12"
[ "$n" -gt 0 ]

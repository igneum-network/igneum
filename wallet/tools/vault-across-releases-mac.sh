#!/usr/bin/env bash
# vault-across-releases-mac.sh <out dir> <new dmg> [<old version dir, e.g. 0.0.12-08a32c5c>]: the landing check's rule of 15 October
# 2026 (F-W1) on the Mac binaries: the OLD release's engine (its disk image fetched from the release host) creates a vault
# with a password on a fresh data directory, unlocks, locks and quits; the NEW release's engine (the stored disk image
# under test) opens the same data directory and unlocks with the same password. The rows and the verdict in
# vault-across.log; exit 0 only on PASS. No window server needed; no secret printed.
set -u
OUT=${1:?out dir}; NEW=${2:?new dmg}; OLDV=${3:-0.0.12-08a32c5c}
mkdir -p "$OUT"; LOG=$OUT/vault-across.log; : > "$LOG"
log() { echo "[$(date -u '+%H:%M:%SZ')] $*" | tee -a "$LOG"; }
W=$OUT/work; mkdir -p "$W/old" "$W/new" "$W/data"
OLDDMG=$W/old.dmg
curl -s -m 120 -o "$OLDDMG" "https://build.igneum.network/releases/wallet/$OLDV/Igneum-Wallet-$OLDV.dmg" || { log "the old disk image did not fetch"; exit 2; }
mount_app() { # <dmg> <dir>: the bundle copied out of the image
  local m="$2/mnt"; mkdir -p "$m"
  hdiutil attach "$1" -nobrowse -readonly -mountpoint "$m" >> "$LOG" 2>&1 || return 1
  cp -R "$m/Igneum Wallet.app" "$2/"; hdiutil detach "$m" >> "$LOG" 2>&1 || true
  xattr -cr "$2/Igneum Wallet.app" 2>/dev/null || true
  echo "$2/Igneum Wallet.app/Contents/MacOS"
}
OB=$(mount_app "$OLDDMG" "$W/old") || { log "the old image did not attach"; exit 2; }
NB=$(mount_app "$NEW" "$W/new") || { log "the new image did not attach"; exit 2; }
log "old: $("$OB/igneum-wallet-engine" --version)   new: $("$NB/igneum-wallet-engine" --version)"
PW="vault-across-$(date +%s)-$RANDOM-test-value"
start() { # <bin dir> <port>
  "$1/igneum-wallet-engine" --data-dir "$W/data" --rpc https://rpc.igneum.network --signer "$1/igneum-signer" --ui "$1/../Resources/ui" --bridge-port "$2" > "$W/e.out" 2> "$W/e.err" &
  echo $! > "$W/engine.pid"; for i in $(seq 1 240); do grep -q "^ENGINE" "$W/e.out" 2>/dev/null && break; sleep 0.5; done
  if ! grep -q "^ENGINE" "$W/e.out" 2>/dev/null; then log "the engine printed no ENGINE line in 120 s; its stderr: $(head -c 300 "$W/e.err" | tr '\n' ' ')"; echo ""; return 1; fi
  grep '^ENGINE' "$W/e.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"].rstrip("/"))'
}
stop() { EP=$(cat "$W/engine.pid"); curl -s -m 10 -X POST "$1/api/quit" -H 'Content-Type: application/json' -d '{}' > /dev/null; for i in $(seq 1 30); do kill -0 $EP 2>/dev/null || break; sleep 0.5; done; if kill -0 $EP 2>/dev/null; then case "$(ps -o comm= -p $EP 2>/dev/null)" in *igneum-wallet-engine*) kill $EP;; esac; fi; sleep 1; }
phase() { curl -s -m 20 "$1/api/state" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("phase"), d.get("address") or "")'; }
U=$(start "$OB" 26931)
log "old create: $(curl -s -m 180 -X POST "$U/api/create" -H 'Content-Type: application/json' -d "{\"password\":\"$PW\"}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("address") or d.get("error"))')"
log "old after create: $(phase "$U")"
curl -s -m 30 -X POST "$U/api/lock" -H 'Content-Type: application/json' -d '{}' > /dev/null; log "old after lock: $(phase "$U")"
log "old unlock: $(curl -s -m 180 -X POST "$U/api/unlock" -H 'Content-Type: application/json' -d "{\"password\":\"$PW\"}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print("ok" if d.get("address") else d)' | cut -c1-120)"
stop "$U"
log "vault sha after the old engine: $(shasum -a 256 "$W/data/vault.json" | cut -c1-16)"
U=$(start "$NB" 26932)
log "new at open: $(phase "$U")"
log "new unlock: $(curl -s -m 180 -X POST "$U/api/unlock" -H 'Content-Type: application/json' -d "{\"password\":\"$PW\"}" | cut -c1-160)"
P=$(phase "$U"); log "new after unlock: $P"
stop "$U"
log "vault sha after the new engine: $(shasum -a 256 "$W/data/vault.json" | cut -c1-16)"
cp "$W/e.err" "$OUT/new-engine.err" 2>/dev/null; rm -rf "$W"
case "$P" in home*) log "VAULT ACROSS RELEASES (mac): PASS"; exit 0;; *) log "VAULT ACROSS RELEASES (mac): FAIL"; exit 1;; esac

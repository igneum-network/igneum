#!/usr/bin/env bash
# vault-across-releases.sh <old linux archive> <new linux archive> [<rpc>]: the landing check's rule from 15 October 2026 (F-W1,
# an outside tester's vault refused after the updater's move): a vault made on release N unlocks on release N+1. The old
# release's engine creates a vault with a password on a fresh data directory, unlocks it, locks it and quits; the new
# release's engine opens the same data directory and unlocks with the same password. Exit 0 only when the new engine's
# api/state reads phase home after the unlock. Runs on a Linux builder; no secret printed (the password is a test value
# made here and never shown).
set -u
OLD=${1:?old archive}; NEW=${2:?new archive}; RPC=${3:-https://rpc.igneum.network}
W=$(mktemp -d /tmp/vault-across.XXXX); mkdir -p "$W/old" "$W/new" "$W/data"
tar xzf "$OLD" -C "$W/old" || exit 2; tar xzf "$NEW" -C "$W/new" || exit 2
OB=$(dirname "$(find "$W/old" -name igneum-wallet-engine | head -1)"); NB=$(dirname "$(find "$W/new" -name igneum-wallet-engine | head -1)")
echo "old: $("$OB/igneum-wallet-engine" --version)   new: $("$NB/igneum-wallet-engine" --version)"
PW="vault-across-$(date +%s)-$RANDOM-test-value"
start() { # <bin dir> <port>: the engine on the shared data directory; prints the URL
  "$1/igneum-wallet-engine" --data-dir "$W/data" --rpc "$RPC" --signer "$1/igneum-signer" --ui "$1/ui" --bridge-port "$2" > "$W/e.out" 2> "$W/e.err" &
  echo $! > "$W/engine.pid"; for i in $(seq 1 60); do grep -q "^ENGINE" "$W/e.out" 2>/dev/null && break; sleep 0.5; done
  grep '^ENGINE' "$W/e.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"].rstrip("/"))'
}
stop() { EP=$(cat "$W/engine.pid"); curl -s -m 10 -X POST "$1/api/quit" -H 'Content-Type: application/json' -d '{}' > /dev/null; for i in $(seq 1 30); do kill -0 $EP 2>/dev/null || break; sleep 0.5; done; if kill -0 $EP 2>/dev/null; then case "$(cat /proc/$EP/comm 2>/dev/null)" in igneum-wallet-e) kill $EP;; esac; fi; sleep 1; }
phase() { curl -s -m 20 "$1/api/state" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("phase"), d.get("address") or "")'; }
U=$(start "$OB" 26931)
curl -s -m 120 -X POST "$U/api/create" -H 'Content-Type: application/json' -d "{\"password\":\"$PW\"}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print("old create:", d.get("address") or d.get("error"))'
echo "old after create: $(phase "$U")"
curl -s -m 30 -X POST "$U/api/lock" -H 'Content-Type: application/json' -d '{}' > /dev/null; echo "old after lock: $(phase "$U")"
curl -s -m 120 -X POST "$U/api/unlock" -H 'Content-Type: application/json' -d "{\"password\":\"$PW\"}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print("old unlock:", "ok" if d.get("ok") or d.get("address") else d)' | cut -c1-120
stop "$U"
sha256sum "$W/data/vault.json" | cut -c1-16 | sed 's/^/vault sha after the old engine: /'
U=$(start "$NB" 26932)
echo "new at open: $(phase "$U")"
R=$(curl -s -m 120 -X POST "$U/api/unlock" -H 'Content-Type: application/json' -d "{\"password\":\"$PW\"}")
echo "new unlock: $(echo "$R" | cut -c1-160)"
P=$(phase "$U"); echo "new after unlock: $P"
stop "$U"
sha256sum "$W/data/vault.json" | cut -c1-16 | sed 's/^/vault sha after the new engine: /'
rm -rf "$W"
case "$P" in home*) echo "VAULT ACROSS RELEASES: PASS"; exit 0;; *) echo "VAULT ACROSS RELEASES: FAIL"; exit 1;; esac

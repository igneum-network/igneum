# shared by the TP-16 probes (tests/wallet/adapters.json). Sourced, not run.
# Needs: the wallet built (wallet/target/debug), anvil on PATH, python3, curl.
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
BIN="$ROOT/wallet/target/debug"
UI="$ROOT/wallet/app/ui"
WORK=$(mktemp -d)
FAILS=0
PIDS=""
probe() { # probe NAME PASS|FAIL why
  echo "PROBE $1 $2 $3"
  [ "$2" = PASS ] || FAILS=$((FAILS + 1))
}
cleanup() {
  for p in $PIDS; do kill "$p" 2>/dev/null || true; done
  # engines are started inside command substitutions, so their pids are recorded in files, not in $PIDS
  for f in "$WORK"/*.pid; do [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null || true; done
}
trap cleanup EXIT
# NODE_RPC=<url> runs the probes against a running node (tools/devnode.sh) instead of a fresh anvil per probe set.
start_anvil() { # port
  [ -z "${NODE_RPC:-}" ] || return 0
  anvil --silent --port "$1" --chain-id 7310 --block-time 1 > "$WORK/anvil-$1.log" 2>&1 &
  PIDS="$PIDS $!"
  sleep 2
}
start_engine() { # data-dir rpc -> prints url
  IGNEUM_SIGNER_LIGHT_KDF=1 "$BIN/igneum-wallet-engine" --data-dir "$1" --rpc "$2" --signer "$BIN/igneum-signer" --ui "$UI" --bridge-port 0 > "$1.out" 2> "$1.err" &
  echo $! > "$1.pid"
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if grep -q '^ENGINE' "$1.out" 2>/dev/null; then break; fi
    sleep 1
  done
  grep '^ENGINE' "$1.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])'
}
json() { # json EXPR  (stdin json, python expression over `j`)
  python3 -c "import sys,json; j=json.load(sys.stdin); print($1)"
}
post() { # post URL PATH JSON
  curl -s -X POST "$1$2" -H 'Content-Type: application/json' -d "$3"
}
finish() {
  if [ "$FAILS" -eq 0 ]; then echo "TP-16 probe set $1: PASS"; exit 0; else echo "TP-16 probe set $1: FAIL ($FAILS)"; exit 1; fi
}
TEST_PHRASE="test test test test test test test test test test test junk"
TEST_ADDR=0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266
OTHER_ADDR=0x70997970C51812dc3A010C7d01b50e0d17dc79C8

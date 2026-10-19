#!/usr/bin/env sh
# The two other send paths through a chain's public submit route (the project's word, 11 October 2026): an engine reading
# devnet7 through the PUBLIC endpoint (nothing named; the chain's default) with the dev faucet's vault:
#   BRIDGE-SEND   a website (curl with an Origin header) connects through the page bridge and asks eth_sendTransaction;
#                 the window answers yes (api/bridge/decide); the engine signs and posts to the chain's submit route;
#                 the hash goes back to the site and is read on the public read endpoint until a block carries it
#   ENVELOPE      a watch-only wallet of the faucet's address (a second engine, api/import mode watch) quotes a send,
#                 exports the unsigned envelope, the CLI signs it with the faucet's vault, the engine imports the
#                 signed envelope and posts it to the submit route; the hash read back the same way
# Run from wallet/ after `cargo build` on a box that reaches the public endpoints. The engines are stopped by their
# pids after their command lines are read. Prints ROW BRIDGE-SEND and ROW ENVELOPE PASS|FAIL.
set -u
cd "$(dirname "$0")/.."
BIN=target/debug
D=${AUTH_DIR:-${IGNEUM_BOX_ROOT:-/srv}/builds/product-b/work/authority5}
PUBLIC=${PUBLIC_RPC:-https://build.igneum.network/devnet7/rpc}
BPORT=${BRIDGE_PORT:-26895}
TO=0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266
WORK=$(mktemp -d); echo "work dir $WORK"
[ -f "$D/faucet.json" ] && [ -s "$D/pw" ] || { echo "ROW BRIDGE-SEND FAIL no faucet vault or password under $D"; echo "ROW ENVELOPE FAIL the same"; exit 1; }
PW=$(cat "$D/pw")
# the engines' pids go to a file: start_engine runs inside a command substitution, so a shell variable set there is
# lost (the first runs left four engines alive and the bridge port taken, 11 Oct 2026)
: > "$WORK/pids"
stop_all() { for p in $(cat "$WORK/pids"); do case "$(ps -p "$p" -o command= 2>/dev/null)" in *igneum-wallet-engine*) kill "$p" 2>/dev/null || true ;; esac; done; }
trap stop_all EXIT
start_engine() {
  # $1 data dir, $2 bridge port (0 for none)
  mkdir -p "$1"
  printf '{"rpc":"%s","rpc_named":false,"idle_lock_min":5,"ask_on_open":true,"auto_update":false,"theme":"system","bridge_on":true,"tokens":[]}\n' "$PUBLIC" > "$1/settings.json"
  IGNEUM_SIGNER_LIGHT_KDF=1 IGNEUM_WALLET_UPDATE_FIRST_SECS=600 $BIN/igneum-wallet-engine --data-dir "$1" --signer $BIN/igneum-signer --ui app/ui --bridge-port "$2" > "$1/engine.out" 2> "$1/engine.err" &
  echo $! >> "$WORK/pids"
  sleep 4
  grep '^ENGINE' "$1/engine.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])'
}
receipt_block() {
  i=0
  while [ $i -lt 40 ]; do
    b=$(curl -s -m 10 -X POST "$PUBLIC" -H 'Content-Type: application/json' -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_getTransactionReceipt\",\"params\":[\"$1\"]}" | python3 -c 'import sys,json; r=json.load(sys.stdin).get("result"); print(int(r["blockNumber"],16) if r else "")' 2>/dev/null || echo "")
    [ -n "$b" ] && { echo "$b"; return 0; }
    i=$((i+1)); sleep 5
  done
  return 1
}

# ---------- BRIDGE-SEND ----------
cp "$D/faucet.json" "$WORK/a/vault.json" 2>/dev/null || { mkdir -p "$WORK/a"; cp "$D/faucet.json" "$WORK/a/vault.json"; }
U1=$(start_engine "$WORK/a" "$BPORT")
curl -s -X POST "${U1}api/unlock" -H 'Content-Type: application/json' -d "{\"password\":\"$PW\"}" > /dev/null
src=$(curl -s "${U1}api/state" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["node"]["source"], d["node"]["state"], d["network"]["name"], d.get("address"))')
echo "engine a: $src"
# the site's token (G8): captured from the approved connect's reply header, sent on every later call
site() { curl -s -m 120 -D "$WORK/site.hdr" -X POST "http://127.0.0.1:$BPORT/rpc" -H 'Content-Type: application/json' -H "Origin: https://shop.example" -H "X-Igneum-Site: $(cat "$WORK/site.token" 2>/dev/null)" -d "$1"; t=$(grep -i '^x-igneum-site:' "$WORK/site.hdr" | tr -d '\r' | awk '{print $2}'); [ -n "$t" ] && printf '%s' "$t" > "$WORK/site.token"; }
answer() { for i in $(seq 1 60); do ID=$(curl -s "${U1}api/bridge/pending" | python3 -c 'import sys,json; p=json.load(sys.stdin); print(p[0]["id"] if p else "")'); if [ -n "$ID" ]; then curl -s -X POST "${U1}api/bridge/decide" -H 'Content-Type: application/json' -d "{\"id\":$ID,\"yes\":true}" > /dev/null; return 0; fi; sleep 0.5; done; return 1; }
FROM=$(printf '%s' "$src" | awk '{print $NF}')   # the last field: the chain's name carries a space ("Igneum devnet7")
(site '{"jsonrpc":"2.0","id":1,"method":"eth_requestAccounts","params":[]}' > "$WORK/connect.out") & SP=$!; answer; wait $SP
(site "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"eth_sendTransaction\",\"params\":[{\"from\":\"$FROM\",\"to\":\"$TO\",\"value\":\"0x38d7ea4c68000\"}]}" > "$WORK/send.out") & SP=$!; answer; wait $SP
H1=$(python3 -c 'import sys,json; d=json.load(open(sys.argv[1])); print(d.get("result",""))' "$WORK/send.out" 2>/dev/null || echo "")
if [ -n "$H1" ]; then
  B1=$(receipt_block "$H1") && echo "ROW BRIDGE-SEND PASS a website's eth_sendTransaction through the page bridge, answered yes in the window, signed and posted to the chain's submit route: $H1 in block $B1 read back on the public read endpoint" || echo "ROW BRIDGE-SEND FAIL $H1 posted but no receipt within 200 s"
else
  echo "ROW BRIDGE-SEND FAIL the site got no hash: $(cut -c1-300 "$WORK/send.out")"
fi

# ---------- ENVELOPE ----------
mkdir -p "$WORK/b"
U2=$(start_engine "$WORK/b" 0)
echo "engine b import: $(curl -s -X POST "${U2}api/import" -H 'Content-Type: application/json' -d "{\"mode\":\"watch\",\"data\":\"$FROM\"}" | cut -c1-200)"
w=$(curl -s "${U2}api/state" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["phase"], "watch_only", d["watch_only"], d["node"]["source"])')
echo "engine b: $w"
Q=$(curl -s -X POST "${U2}api/send/quote" -H 'Content-Type: application/json' -d "{\"to\":\"$TO\",\"amount\":\"0.001\"}")
QID=$(printf '%s' "$Q" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("quote_id",""))' 2>/dev/null || echo "")
[ -n "$QID" ] || { echo "ROW ENVELOPE FAIL no quote on the watch-only wallet: $(printf '%s' "$Q" | cut -c1-200)"; exit 1; }
curl -s -X POST "${U2}api/envelope/export" -H 'Content-Type: application/json' -d "{\"quote_id\":\"$QID\"}" > "$WORK/env-unsigned.json"
printf '%s' "$PW" > "$WORK/pw.txt"; chmod 600 "$WORK/pw.txt"
$BIN/igneum-wallet --rpc "$PUBLIC" --vault "$D/faucet.json" sign --password-file "$WORK/pw.txt" < "$WORK/env-unsigned.json" > "$WORK/env-signed.json" 2> "$WORK/sign.err" || { echo "ROW ENVELOPE FAIL the CLI did not sign: $(cut -c1-200 "$WORK/sign.err")"; exit 1; }
python3 -c 'import json,sys; s=json.load(open(sys.argv[1])); print(json.dumps({"signed": s}))' "$WORK/env-signed.json" > "$WORK/env-import.json"
R=$(curl -s -m 120 -X POST "${U2}api/envelope/import" -H 'Content-Type: application/json' -d @"$WORK/env-import.json")
H2=$(printf '%s' "$R" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("hash",""))' 2>/dev/null || echo "")
rm -f "$WORK/pw.txt"
if [ -n "$H2" ]; then
  B2=$(receipt_block "$H2") && echo "ROW ENVELOPE PASS a watch-only wallet's quote exported, signed by the CLI where the key is, imported and posted to the chain's submit route: $H2 in block $B2 read back on the public read endpoint" || echo "ROW ENVELOPE FAIL $H2 posted but no receipt within 200 s"
else
  echo "ROW ENVELOPE FAIL the import gave no hash: $(printf '%s' "$R" | cut -c1-300)"
fi

#!/usr/bin/env sh
# The public send row (the project's word, 11 October 2026: a public wallet that cannot send on a valueless dev chain cannot
# be tested): an engine reading devnet5 through its PUBLIC endpoint (the chain's default, no --rpc) sends 0.001 IGN
# from the dev faucet's vault (the authority smoke's, under AUTH_DIR on the box; a dev-only key) to the test account;
# the engine posts the signed transaction to the chain's public submit route (engine::submit_rpc) and the hash is
# read back on the public read endpoint until a block carries it. Rows: ROW PUBLIC-SEND PASS|FAIL with the hash,
# the route's answer and the block. While the route is absent the row reads FAIL with the route's own words (the
# engine shows them), which is the record's state until reliability's line. Run from wallet/ after `cargo build`.
# The engine is stopped by its pid after its command line is read.
set -u
cd "$(dirname "$0")/.."
BIN=target/debug
D=${AUTH_DIR:-${IGNEUM_BOX_ROOT:-/srv}/builds/product-b/work/authority5}
PUBLIC=${PUBLIC_RPC:-https://build.igneum.network/devnet5/rpc}
WORK=$(mktemp -d)
echo "work dir $WORK"
[ -f "$D/faucet.json" ] || { echo "ROW PUBLIC-SEND FAIL no faucet vault under $D (the authority smoke makes it)"; exit 1; }
mkdir -p "$WORK/engine"; cp "$D/faucet.json" "$WORK/engine/vault.json"
# the public endpoint is the default: no --rpc, and the settings file names it so the source reads "public"
printf '{"rpc":"%s","rpc_named":false,"idle_lock_min":5,"ask_on_open":true,"auto_update":false,"theme":"system","bridge_on":false,"tokens":[]}\n' "$PUBLIC" > "$WORK/engine/settings.json"
IGNEUM_SIGNER_LIGHT_KDF=1 IGNEUM_WALLET_UPDATE_FIRST_SECS=600 $BIN/igneum-wallet-engine --data-dir "$WORK/engine" --signer $BIN/igneum-signer --ui app/ui --bridge-port 0 > "$WORK/engine.out" 2> "$WORK/engine.err" &
echo $! > "$WORK/engine.pid"
stop_engine() { p=$(cat "$WORK/engine.pid" 2>/dev/null) || return 0; case "$(ps -p "$p" -o command= 2>/dev/null)" in *igneum-wallet-engine*) kill "$p" 2>/dev/null || true ;; esac; }
trap stop_engine EXIT
sleep 4
URL=$(grep '^ENGINE' "$WORK/engine.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])')
post() { curl -s -m 90 -X POST "$URL$1" -H 'Content-Type: application/json' -d "$2"; }
PW=$(cat "$D/pw" 2>/dev/null || echo "")
[ -n "$PW" ] || { echo "ROW PUBLIC-SEND FAIL no password file under $D"; exit 1; }
post api/unlock "{\"password\":\"$PW\"}" > "$WORK/unlock.json"
st=$(curl -s "${URL}api/state")
src=$(printf '%s' "$st" | python3 -c 'import sys,json; d=json.load(sys.stdin); n=d["node"]; print(n["source"], n["endpoint"], n["state"], d["network"]["name"], d.get("balance_ign"))')
echo "state: $src"
printf '%s' "$st" | python3 -c 'import sys,json; d=json.load(sys.stdin); sys.exit(0 if d["node"]["source"]=="public" and d["node"]["state"]=="ok" else 1)' || { echo "ROW PUBLIC-SEND FAIL the engine does not read the public endpoint as its source: $src"; exit 1; }
q=$(post api/send/quote '{"to":"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266","amount":"0.001"}')
qid=$(printf '%s' "$q" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("quote_id",""))' 2>/dev/null || echo "")
[ -n "$qid" ] || { echo "ROW PUBLIC-SEND FAIL no quote: $(printf '%s' "$q" | cut -c1-200)"; exit 1; }
r=$(post api/send "{\"quote_id\":\"$qid\"}")
hash=$(printf '%s' "$r" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("hash",""))' 2>/dev/null || echo "")
if [ -z "$hash" ]; then echo "ROW PUBLIC-SEND FAIL the send was refused: $(printf '%s' "$r" | cut -c1-300)"; exit 1; fi
echo "sent $hash through the submit route"
blk=""
i=0
while [ $i -lt 40 ]; do
  blk=$(curl -s -m 10 -X POST "$PUBLIC" -H 'Content-Type: application/json' -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_getTransactionReceipt\",\"params\":[\"$hash\"]}" | python3 -c 'import sys,json; r=json.load(sys.stdin).get("result"); print(int(r["blockNumber"],16) if r else "")' 2>/dev/null || echo "")
  [ -n "$blk" ] && break; i=$((i+1)); sleep 5
done
[ -n "$blk" ] || { echo "ROW PUBLIC-SEND FAIL $hash posted but no receipt on the public read endpoint within 200 s"; exit 1; }
echo "ROW PUBLIC-SEND PASS a wallet reading the public endpoint sent 0.001 IGN: the signed transaction posted to the chain's submit route, $hash in block $blk read back on the public read endpoint"

#!/usr/bin/env sh
# The wallet smoke on a box with anvil (Foundry): the CLI's full path (import, prepare, sign, broadcast, settlement)
# and the engine's watch-only path (quote, envelope out, sign with the CLI, envelope in, settlement). Run from wallet/
# after `cargo build`. anvil is started here and stopped by its recorded pid only.
set -eu
cd "$(dirname "$0")/.."
BIN=target/debug
WORK=$(mktemp -d)
echo "work dir $WORK"
anvil --silent --port 8599 --chain-id 7310 --block-time 1 > "$WORK/anvil.log" 2>&1 &
echo $! > "$WORK/anvil.pid"
trap 'kill "$(cat "$WORK/anvil.pid")" 2>/dev/null || true' EXIT
sleep 2
RPC=http://127.0.0.1:8599
echo "test test test test test test test test test test test junk" > "$WORK/phrase.txt"
echo "correct horse battery" > "$WORK/pw.txt"

echo "--- CLI: import, address"
$BIN/igneum-wallet --vault "$WORK/v.json" import --password-file "$WORK/pw.txt" --stdin < "$WORK/phrase.txt"
$BIN/igneum-wallet --vault "$WORK/v.json" address

echo "--- CLI: prepare, sign, broadcast, settlement"
FROM=0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266
TO=0x70997970C51812dc3A010C7d01b50e0d17dc79C8
$BIN/igneum-wallet --rpc $RPC prepare --from $FROM --to $TO --amount 1.5 > "$WORK/unsigned.json"
grep -c '"sentences"' "$WORK/unsigned.json"
$BIN/igneum-wallet --rpc $RPC --vault "$WORK/v.json" sign --password-file "$WORK/pw.txt" < "$WORK/unsigned.json" > "$WORK/signed.json"
HASH=$($BIN/igneum-wallet --rpc $RPC broadcast < "$WORK/signed.json")
echo "sent $HASH"
sleep 3
$BIN/igneum-wallet --rpc $RPC settlement "$HASH" | grep -E '"word"|"why"'
cast balance --rpc-url $RPC $TO

echo "--- engine: watch-only quote, envelope out, CLI signs, envelope in"
export IGNEUM_SIGNER_LIGHT_KDF=1
$BIN/igneum-wallet-engine --data-dir "$WORK/engine" --rpc $RPC --signer $BIN/igneum-signer --ui app/ui > "$WORK/engine.out" 2> "$WORK/engine.err" &
echo $! > "$WORK/engine.pid"
sleep 2
URL=$(grep '^ENGINE' "$WORK/engine.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])')
echo "engine at $URL"
curl -s "${URL}api/state" | python3 -c 'import sys,json; s=json.load(sys.stdin); print("phase", s["phase"], "node", s["node"]["state"], "chain", s["node"]["chain_id"])'
curl -s -X POST "${URL}api/import" -H 'Content-Type: application/json' -d "{\"mode\":\"watch\",\"data\":\"$FROM\"}" | head -c 200; echo
sleep 6
curl -s "${URL}api/state" | python3 -c 'import sys,json; s=json.load(sys.stdin); print("phase", s["phase"], "watch_only", s["watch_only"], "balance", s["balance_ign"])'
QUOTE=$(curl -s -X POST "${URL}api/send/quote" -H 'Content-Type: application/json' -d "{\"to\":\"$TO\",\"amount\":\"0.25\"}")
echo "$QUOTE" | python3 -c 'import sys,json; q=json.load(sys.stdin); print("\n".join(q["sentences"])); print("simulation", q["simulation"]); print("watch_only", q["watch_only"])'
QID=$(echo "$QUOTE" | python3 -c 'import sys,json; print(json.load(sys.stdin)["quote_id"])')
curl -s -X POST "${URL}api/envelope/export" -H 'Content-Type: application/json' -d "{\"quote_id\":\"$QID\"}" > "$WORK/env-unsigned.json"
$BIN/igneum-wallet --rpc $RPC --vault "$WORK/v.json" sign --password-file "$WORK/pw.txt" < "$WORK/env-unsigned.json" > "$WORK/env-signed.json"
python3 -c 'import json,sys; s=json.load(open(sys.argv[1])); print(json.dumps({"signed": s}))' "$WORK/env-signed.json" > "$WORK/env-import.json"
curl -s -X POST "${URL}api/envelope/import" -H 'Content-Type: application/json' -d @"$WORK/env-import.json"; echo
sleep 3
H2=$(curl -s "${URL}api/state" | python3 -c 'import sys,json; print(json.load(sys.stdin)["history"][0]["hash"])')
curl -s "${URL}api/tx/$H2" | python3 -c 'import sys,json; v=json.load(sys.stdin)["view"]; print(v["word"], "|", v["why"])'
curl -s -X POST "${URL}api/send" -H 'Content-Type: application/json' -d "{\"quote_id\":\"x\"}"; echo
cast balance --rpc-url $RPC $TO
kill "$(cat "$WORK/engine.pid")" 2>/dev/null || true
echo "--- engine: software wallet create, confirm, lock, unlock, send"
rm -rf "$WORK/engine"
$BIN/igneum-wallet-engine --data-dir "$WORK/engine" --rpc $RPC --signer $BIN/igneum-signer --ui app/ui > "$WORK/engine2.out" 2> "$WORK/engine2.err" &
echo $! > "$WORK/engine.pid"
sleep 2
URL=$(grep '^ENGINE' "$WORK/engine2.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])')
CREATE=$(curl -s -X POST "${URL}api/create" -H 'Content-Type: application/json' -d '{"password":"correct horse battery"}')
ADDR=$(echo "$CREATE" | python3 -c 'import sys,json; print(json.load(sys.stdin)["address"])')
echo "created $ADDR"
CHECKS=$(echo "$CREATE" | python3 -c 'import sys,json; w=json.load(sys.stdin)["words"]; print(json.dumps({"checks":[{"index":0,"word":w[0]},{"index":5,"word":w[5]},{"index":23,"word":w[23]}]}))')
curl -s -X POST "${URL}api/create/confirm" -H 'Content-Type: application/json' -d "$CHECKS" | python3 -c 'import sys,json; s=json.load(sys.stdin); print("backed_up", s["backed_up"], "unlocked", s["unlocked"])'
# the funder's key is Foundry's published anvil default (the dev account), read from the environment
cast send --rpc-url $RPC --private-key "${IGNEUM_SMOKE_KEY:?set the smoke key}" $ADDR --value 2ether > /dev/null
sleep 2
curl -s -X POST "${URL}api/lock" > /dev/null
curl -s "${URL}api/state" | python3 -c 'import sys,json; s=json.load(sys.stdin); print("phase after lock", s["phase"])'
curl -s -X POST "${URL}api/unlock" -H 'Content-Type: application/json' -d '{"password":"wrong password"}'; echo
curl -s -X POST "${URL}api/unlock" -H 'Content-Type: application/json' -d '{"password":"correct horse battery"}' | python3 -c 'import sys,json; print("unlocked", json.load(sys.stdin)["unlocked"])'
sleep 6
QUOTE=$(curl -s -X POST "${URL}api/send/quote" -H 'Content-Type: application/json' -d "{\"to\":\"$TO\",\"amount\":\"0.5\"}")
QID=$(echo "$QUOTE" | python3 -c 'import sys,json; print(json.load(sys.stdin)["quote_id"])')
curl -s -X POST "${URL}api/send" -H 'Content-Type: application/json' -d "{\"quote_id\":\"$QID\"}"; echo
sleep 3
H3=$(curl -s "${URL}api/state" | python3 -c 'import sys,json; print(json.load(sys.stdin)["history"][0]["hash"])')
curl -s "${URL}api/tx/$H3" | python3 -c 'import sys,json; v=json.load(sys.stdin)["view"]; print(v["word"], "|", v["why"])'
curl -s -X POST "${URL}api/reveal" -H 'Content-Type: application/json' -d '{"password":"correct horse battery"}' | python3 -c 'import sys,json; r=json.load(sys.stdin); print("reveal words", len(r["words"]), "key", r["key"][:6]+"...")'
kill "$(cat "$WORK/engine.pid")" 2>/dev/null || true
echo "SMOKE-DONE"

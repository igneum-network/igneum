#!/usr/bin/env sh
# The page bridge end to end against a running node (NODE_RPC, default the dev node on 8545): a "website"
# (curl with an Origin header) connects, reads, signs a message, signs typed data and sends a transaction, while the
# "window" (the engine's API) answers each ask. Run from wallet/ after `cargo build`.
set -u
cd "$(dirname "$0")/.."
BIN=target/debug
RPC=${NODE_RPC:-http://127.0.0.1:8545}
BPORT=26899
WORK=$(mktemp -d)
echo "work dir $WORK"
IGNEUM_SIGNER_LIGHT_KDF=1 $BIN/igneum-wallet-engine --data-dir "$WORK/engine" --rpc $RPC --signer $BIN/igneum-signer --ui app/ui --bridge-port $BPORT > "$WORK/engine.out" 2> "$WORK/engine.err" &
echo $! > "$WORK/engine.pid"
trap 'kill "$(cat "$WORK/engine.pid")" 2>/dev/null || true' EXIT
sleep 3
URL=$(grep '^ENGINE' "$WORK/engine.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])')
post() { curl -s -X POST "$URL$1" -H 'Content-Type: application/json' -d "$2"; }
site() { curl -s -X POST "http://127.0.0.1:$BPORT/rpc" -H 'Content-Type: application/json' -H "Origin: https://shop.example" -d "$1"; }
# the window answers the next ask with yes (or no) once it appears
answer() { for i in $(seq 1 40); do ID=$(curl -s "${URL}api/bridge/pending" | python3 -c 'import sys,json; p=json.load(sys.stdin); print(p[0]["id"] if p else "")'); if [ -n "$ID" ]; then curl -s "${URL}api/bridge/pending" | python3 -c 'import sys,json; p=json.load(sys.stdin)[0]; print("  ASK:", p["title"]); [print("   ", l) for l in p["lines"][:4]]'; post api/bridge/decide "{\"id\":$ID,\"yes\":$1}" > /dev/null; return; fi; sleep 0.5; done; echo "no ask appeared"; }

post api/import '{"mode":"seed","data":"test test test test test test test test test test test junk","password":"correct horse battery"}' > /dev/null
echo "--- no origin, no answer"; curl -s -X POST "http://127.0.0.1:$BPORT/rpc" -H 'Content-Type: application/json' -d '{}' ; echo
echo "--- preflight"; curl -s -i -X OPTIONS "http://127.0.0.1:$BPORT/rpc" -H "Origin: https://shop.example" | grep -i 'access-control-allow-origin'
echo "--- provider.js served"; curl -s "http://127.0.0.1:$BPORT/provider.js" | head -c 60; echo
echo "--- reads without a connection"; site '{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}'; echo; site '{"jsonrpc":"2.0","id":2,"method":"eth_accounts","params":[]}'; echo
echo "--- eth_sign refused"; site '{"jsonrpc":"2.0","id":3,"method":"eth_sign","params":["0x","0x00"]}'; echo
echo "--- sendTransaction before connect"; site '{"jsonrpc":"2.0","id":4,"method":"eth_sendTransaction","params":[{"to":"0x70997970C51812dc3A010C7d01b50e0d17dc79C8","value":"0x1"}]}'; echo
echo "--- connect, declined"; (site '{"jsonrpc":"2.0","id":5,"method":"eth_requestAccounts","params":[]}'; echo) & SP=$!; answer false; wait $SP
echo "--- connect, accepted"; (site '{"jsonrpc":"2.0","id":6,"method":"eth_requestAccounts","params":[]}'; echo) & SP=$!; answer true; wait $SP
echo "--- eth_accounts now"; site '{"jsonrpc":"2.0","id":7,"method":"eth_accounts","params":[]}'; echo
echo "--- a read proxied"; site '{"jsonrpc":"2.0","id":8,"method":"eth_getBalance","params":["0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266","latest"]}' | cut -c1-80; echo
echo "--- personal_sign"; (site '{"jsonrpc":"2.0","id":9,"method":"personal_sign","params":["0x68656c6c6f20776f726c64","0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"]}'; echo) & SP=$!; answer true; wait $SP
echo "--- personal_sign shaped like a transaction"; site '{"jsonrpc":"2.0","id":10,"method":"personal_sign","params":["0x02f87b01","0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"]}'; echo
echo "--- typed data on the wrong chain"; site '{"jsonrpc":"2.0","id":11,"method":"eth_signTypedData_v4","params":["0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266","{\"types\":{\"T\":[{\"name\":\"x\",\"type\":\"uint8\"}]},\"primaryType\":\"T\",\"domain\":{\"name\":\"d\",\"chainId\":1},\"message\":{\"x\":1}}"]}'; echo
echo "--- typed data (a permit with an unlimited value)"; (site '{"jsonrpc":"2.0","id":12,"method":"eth_signTypedData_v4","params":["0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266","{\"types\":{\"Permit\":[{\"name\":\"spender\",\"type\":\"address\"},{\"name\":\"value\",\"type\":\"uint256\"}]},\"primaryType\":\"Permit\",\"domain\":{\"name\":\"Token\",\"chainId\":7310},\"message\":{\"spender\":\"0x70997970C51812dc3A010C7d01b50e0d17dc79C8\",\"value\":\"115792089237316195423570985008687907853269984665640564039457584007913129639935\"}}"]}'; echo) & SP=$!; answer true; wait $SP
echo "--- sendTransaction, accepted"; (site '{"jsonrpc":"2.0","id":13,"method":"eth_sendTransaction","params":[{"from":"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266","to":"0x70997970C51812dc3A010C7d01b50e0d17dc79C8","value":"0xde0b6b3a7640000"}]}'; echo) & SP=$!; answer true; wait $SP
sleep 2
echo "--- the history carries it"; curl -s "${URL}api/state" | python3 -c 'import sys,json; s=json.load(sys.stdin); h=s["history"][0]; print(h["value_ign"], "IGN", h["sentences"][0]); print("sites", [x["origin"] for x in s["bridge"]["sites"]])'
echo "--- disconnect, then a read needing a connection"; post api/bridge/sites/remove '{"origin":"https://shop.example"}' > /dev/null; site '{"jsonrpc":"2.0","id":14,"method":"personal_sign","params":["0x68","0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"]}'; echo
echo "--- bridge off"; post api/bridge/on '{"on":false}' > /dev/null; site '{"jsonrpc":"2.0","id":15,"method":"eth_chainId","params":[]}'; echo
echo BRIDGE-SMOKE-DONE

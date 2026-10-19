#!/usr/bin/env sh
# The Pay set exercised on the devnet from two fresh keys (the team's own test keys, made here, never printed):
# the deployer funds the faucet and gives each key gas; both drip from the faucet; A creates an invoice; B pays it;
# the receipt is read back. A verification of the deployment on the real chain, labelled as the team's test use:
# it is not external adoption and the dashboard must not count it as such (the plan, page 29).
#   DEPLOYER_KEY=<hex> contracts/tools/devnet-pay-smoke.sh <rpc url> <keys dir>
set -eu
cd "$(dirname "$0")/../.."
RPC=${1:?rpc url}; KEYS=${2:?keys dir}
[ -n "${DEPLOYER_KEY:-}" ] || { echo "DEPLOYER_KEY is not set"; exit 2; }
J=dev/chain/igneum-chains.json
get() { python3 -c "import json,sys; n=[n for n in json.load(open('$J'))['networks'] if n['short']=='igneum-devnet'][0]; print(eval(sys.argv[1]))" "$1"; }
PAY=$(get "n['contracts']['IgneumPay']"); FAUCET=$(get "n['test_assets']['Faucet']"); WANT=$(get "n['genesis_hash']")
HAVE=$(curl -s -X POST "$RPC" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x0",false]}' | python3 -c 'import sys,json; print(json.load(sys.stdin)["result"]["hash"])')
[ "$HAVE" = "$WANT" ] || { echo "refused: genesis $HAVE is not the recorded $WANT"; exit 1; }
mkdir -p "$KEYS"; chmod 700 "$KEYS"
for k in a b; do
  if [ ! -f "$KEYS/smoke-$k.key" ]; then
    cast wallet new --json | python3 -c "
import sys,json; w=json.load(sys.stdin)['data'][0]
open('$KEYS/smoke-$k.key','w').write([w[x] for x in w if 'private' in x.lower()][0]); open('$KEYS/smoke-$k.addr','w').write([w[x] for x in w if 'address' in x.lower()][0])"
    chmod 600 "$KEYS/smoke-$k.key"
  fi
done
A=$(cat "$KEYS/smoke-a.addr"); B=$(cat "$KEYS/smoke-b.addr"); KA=$(cat "$KEYS/smoke-a.key"); KB=$(cat "$KEYS/smoke-b.key")
echo "pay $PAY faucet $FAUCET keys $A $B"
echo "--- the deployer funds the faucet and gives each key gas"
cast send --rpc-url "$RPC" --private-key "$DEPLOYER_KEY" "$FAUCET" --value 5ether > /dev/null
cast send --rpc-url "$RPC" --private-key "$DEPLOYER_KEY" "$A" --value 0.01ether > /dev/null
cast send --rpc-url "$RPC" --private-key "$DEPLOYER_KEY" "$B" --value 0.01ether > /dev/null
echo "faucet balance $(cast balance --rpc-url "$RPC" "$FAUCET" -e) IGN"
echo "--- both keys drip"
cast send --rpc-url "$RPC" --private-key "$KA" "$FAUCET" "drip()" > /dev/null && echo "A dripped: $(cast balance --rpc-url "$RPC" "$A" -e) IGN"
cast send --rpc-url "$RPC" --private-key "$KB" "$FAUCET" "drip()" > /dev/null && echo "B dripped: $(cast balance --rpc-url "$RPC" "$B" -e) IGN"
echo "--- A creates an invoice for 0.1 IGN; B pays it"
R=$(cast send --rpc-url "$RPC" --private-key "$KA" "$PAY" "createInvoice(address,uint256,uint64,bytes32,address)" 0x0000000000000000000000000000000000000000 100000000000000000 0 "$(cast format-bytes32-string devnet-smoke)" 0x0000000000000000000000000000000000000000 --json)
ID=$(echo "$R" | python3 -c 'import sys,json; r=json.load(sys.stdin); print([l["topics"][1] for l in r["logs"] if len(l["topics"])==4][0])')
echo "invoice $ID in block $(echo "$R" | python3 -c 'import sys,json; print(int(json.load(sys.stdin)["blockNumber"],16))')"
P=$(cast send --rpc-url "$RPC" --private-key "$KB" "$PAY" "pay(bytes32)" "$ID" --value 0.1ether --json)
echo "paid in block $(echo "$P" | python3 -c 'import sys,json; print(int(json.load(sys.stdin)["blockNumber"],16))') tx $(echo "$P" | python3 -c 'import sys,json; print(json.load(sys.stdin)["transactionHash"])')"
echo "--- the receipt"
cast call --rpc-url "$RPC" "$PAY" "receipt(bytes32)((address,address,uint256,uint64,uint64))" "$ID"
echo "status $(cast call --rpc-url "$RPC" "$PAY" "status(bytes32)(uint8)" "$ID") (2 = Paid)"
echo "A now holds $(cast balance --rpc-url "$RPC" "$A" -e) IGN"

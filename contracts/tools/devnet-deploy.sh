#!/usr/bin/env sh
# The devnet deployment in its fixed order (DESIGN.md 6b): the ERC-4337 pair at the deployer's nonces 0 and 1, the Pay
# set at 2 to 7, so the published addresses hold across a re-seal. Refuses unless the chain's genesis hash is the one
# recorded in dev/chain/igneum-chains.json and the deployer is at nonce 0 (the project's rule of 10 October 2026: read the
# latest line, and the chain, before any chain write). The key reaches forge through the environment, never argv.
#   DEPLOYER_KEY=<hex> [NETWORK=igneum-devnet9] contracts/tools/devnet-deploy.sh <rpc url>
set -eu
cd "$(dirname "$0")/../.."
RPC=${1:?rpc url}
[ -n "${DEPLOYER_KEY:-}" ] || { echo "DEPLOYER_KEY is not set"; exit 2; }
rpc() { curl -s -X POST "$RPC" -H 'Content-Type: application/json' -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}" | python3 -c 'import sys,json; j=json.load(sys.stdin); print(j.get("result", "ERROR " + json.dumps(j.get("error"))))'; }
NETWORK=${NETWORK:-igneum-devnet}
WANT=$(python3 -c 'import json,sys; print([n for n in json.load(open("dev/chain/igneum-chains.json"))["networks"] if n["short"]==sys.argv[1]][0]["genesis_hash"])' "$NETWORK")
[ -n "$WANT" ] || { echo "refused: $NETWORK has no recorded genesis in dev/chain/igneum-chains.json"; exit 1; }
HAVE=$(curl -s -X POST "$RPC" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x0",false]}' | python3 -c 'import sys,json; print(json.load(sys.stdin)["result"]["hash"])')
CHAIN=$(rpc eth_chainId '[]')
echo "chain $CHAIN genesis $HAVE"
[ "$HAVE" = "$WANT" ] || { echo "refused: the chain's genesis $HAVE is not the recorded $WANT (dev/chain/igneum-chains.json); re-record it on the project's word before deploying"; exit 1; }
DEPLOYER=$(cd contracts && cast wallet address --private-key "$DEPLOYER_KEY")
NONCE=$(rpc eth_getTransactionCount "[\"$DEPLOYER\", \"latest\"]")
echo "deployer $DEPLOYER nonce $NONCE"
[ "$NONCE" = "0x0" ] || { echo "refused: the deployer is at nonce $NONCE, not 0; the published addresses would not hold"; exit 1; }
echo "--- the ERC-4337 pair (nonces 0 and 1)"
(cd dev/erc4337 && forge script script/Deploy.s.sol --rpc-url "$RPC" --broadcast --private-key "$DEPLOYER_KEY" 2>&1 | grep -E 'EntryPoint v|SimpleAccountFactory|ONCHAIN|rror')
echo "--- the Pay set (nonces 2 to 7)"
(cd contracts && forge script script/DeployPay.s.sol --rpc-url "$RPC" --broadcast 2>&1 | grep -E '^  (chain|Igneum|Test|Faucet)|ONCHAIN|rror')
echo "compare the addresses above with DESIGN.md 6b; a difference means the order or the deployer changed"

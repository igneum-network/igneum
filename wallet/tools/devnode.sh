#!/usr/bin/env sh
# Starts the protocol team's dev node (--dev: chain id 7310, EVM-1 at block 0, CPU sealing) for the wallet team's runs on the box,
# with the public test key imported and unlocked so eth_signTransaction can be compared with our signer.
# The pid is recorded; stop it with tools/devnode-stop.sh DATADIR (by that pid only, after reading its command line).
# --rpc.txfeecap 0: the signing gate signs random fees above the 1 IGN default cap on purpose.
#   tools/devnode.sh GETH_BINARY DATADIR [HTTP_PORT]
set -eu
GETH=$1; DATA=$2; PORT=${3:-8545}
mkdir -p "$DATA"
echo "devnode" > "$DATA/password.txt"
if [ ! -d "$DATA/keystore" ] || [ -z "$(ls -A "$DATA/keystore" 2>/dev/null)" ]; then
  echo ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80 > "$DATA/testkey.txt"
  "$GETH" --datadir "$DATA" account import --password "$DATA/password.txt" "$DATA/testkey.txt" > /dev/null
  rm -f "$DATA/testkey.txt"
fi
nohup "$GETH" --dev --dev.period 2 --datadir "$DATA" --http --http.port "$PORT" --http.api eth,net,web3,debug \
  --unlock 0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266 --password "$DATA/password.txt" --allow-insecure-unlock \
  --rpc.txfeecap 0 --rpc.allow-unprotected-txs --miner.threads 1 --verbosity 3 > "$DATA/geth.log" 2>&1 &
echo $! > "$DATA/geth.pid"
for i in $(seq 1 30); do
  if curl -s -X POST "http://127.0.0.1:$PORT" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}' | grep -q result; then break; fi
  sleep 1
done
echo "node pid $(cat "$DATA/geth.pid") on http://127.0.0.1:$PORT, chain $(curl -s -X POST "http://127.0.0.1:$PORT" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}')"
# the frozen dev genesis (main 4d1d648) funds one fixed faucet, which --dev imports as the developer account; the
# public test key this harness signs with is funded from it here, so the signing-gate and the probes keep their
# accounts. The faucet account is unlocked by the node itself; cast's --unlocked asks the node to sign.
FAUCET=$(curl -s -X POST "http://127.0.0.1:$PORT" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"eth_coinbase","params":[]}' | python3 -c 'import sys,json; print(json.load(sys.stdin).get("result",""))')
if [ -n "$FAUCET" ] && [ "$FAUCET" != "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266" ]; then
  cast send --rpc-url "http://127.0.0.1:$PORT" --unlocked --from "$FAUCET" 0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266 --value 100000000ether > /dev/null 2>&1 && echo "test key funded from the faucet $FAUCET" || echo "funding the test key from $FAUCET failed"
fi
echo "genesis $(curl -s -X POST "http://127.0.0.1:$PORT" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x0",false]}' | python3 -c 'import sys,json; print(json.load(sys.stdin)["result"]["hash"])')"

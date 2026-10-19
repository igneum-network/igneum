#!/usr/bin/env sh
# The differential signing gate (DESIGN.md 1.3): for N random transactions, our raw signed bytes must equal Foundry's
# `cast mktx` (and, when a node URL is given, Core-Geth's eth_signTransaction with the dev account). One mismatch
# fails. Run from wallet/ after `cargo build`:  tools/diff-sign.sh [N] [NODE_RPC]
set -u
cd "$(dirname "$0")/.."
N=${1:-200}
NODE=${2:-}
BIN=target/debug/igneum-wallet
KEY=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80   # anvil's first account, public test key
CHAIN=7310
TOKEN=0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC
fail=0; ok=0
rnd() { python3 -c "import random; print(random.randint($1, $2))"; }
rndaddr() { python3 -c "import secrets; print('0x'+secrets.token_hex(20))"; }
i=0
while [ $i -lt "$N" ]; do
  i=$((i+1))
  nonce=$(rnd 0 100000); gas=$(rnd 21000 3000000); value=$(rnd 0 1000000000000000000000)
  tip=$(rnd 1 100000000000); base=$(rnd 1 1000000000000); maxfee=$((base + tip))
  to=$(rndaddr)
  if [ $((i % 3)) -eq 0 ]; then
    data=$(cast calldata "transfer(address,uint256)" "$to" "$value"); to=$TOKEN
  else
    data=0x
  fi
  if [ $((i % 2)) -eq 0 ]; then
    ours=$(printf '{"type":"eip1559","chain_id":%s,"nonce":%s,"max_priority_fee_per_gas":"%s","max_fee_per_gas":"%s","gas_limit":%s,"to":"%s","value":"%s","data":"%s"}' $CHAIN $nonce $tip $maxfee $gas $to $value $data | $BIN --key-hex $KEY sign-tx | python3 -c 'import sys,json; print(json.load(sys.stdin)["raw"])')
    if [ "$data" = 0x ]; then
      theirs=$(cast mktx --private-key $KEY --chain $CHAIN --nonce $nonce --gas-limit $gas --gas-price $maxfee --priority-gas-price $tip --value $value $to 2>&1)
    else
      theirs=$(cast mktx --private-key $KEY --chain $CHAIN --nonce $nonce --gas-limit $gas --gas-price $maxfee --priority-gas-price $tip --value $value $TOKEN "transfer(address,uint256)" "$(echo $data | cut -c35-74 | sed 's/^/0x/')" "$value" 2>&1)
    fi
    kind=eip1559
  else
    ours=$(printf '{"type":"legacy","chain_id":%s,"nonce":%s,"gas_price":"%s","gas_limit":%s,"to":"%s","value":"%s","data":"%s"}' $CHAIN $nonce $maxfee $gas $to $value $data | $BIN --key-hex $KEY sign-tx | python3 -c 'import sys,json; print(json.load(sys.stdin)["raw"])')
    if [ "$data" = 0x ]; then
      theirs=$(cast mktx --legacy --private-key $KEY --chain $CHAIN --nonce $nonce --gas-limit $gas --gas-price $maxfee --value $value $to 2>&1)
    else
      theirs=$(cast mktx --legacy --private-key $KEY --chain $CHAIN --nonce $nonce --gas-limit $gas --gas-price $maxfee --value $value $TOKEN "transfer(address,uint256)" "$(echo $data | cut -c35-74 | sed 's/^/0x/')" "$value" 2>&1)
    fi
    kind=legacy
  fi
  if [ "$ours" = "$theirs" ]; then ok=$((ok+1)); else fail=$((fail+1)); echo "MISMATCH $kind nonce=$nonce gas=$gas value=$value tip=$tip maxfee=$maxfee to=$to data=$data"; echo "  ours   $ours"; echo "  cast   $theirs"; fi
  if [ -n "$NODE" ]; then
    # the node signs the same payload with the same (unlocked test) key: eth_signTransaction returns the raw bytes
    hexq() { python3 -c "print(hex($1))"; }
    if [ "$kind" = eip1559 ]; then
      params=$(printf '{"from":"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266","to":"%s","value":"%s","nonce":"%s","gas":"%s","maxFeePerGas":"%s","maxPriorityFeePerGas":"%s","data":"%s","chainId":"%s"}' $to $(hexq $value) $(hexq $nonce) $(hexq $gas) $(hexq $maxfee) $(hexq $tip) $data $(hexq $CHAIN))
    else
      params=$(printf '{"from":"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266","to":"%s","value":"%s","nonce":"%s","gas":"%s","gasPrice":"%s","data":"%s","chainId":"%s"}' $to $(hexq $value) $(hexq $nonce) $(hexq $gas) $(hexq $maxfee) $data $(hexq $CHAIN))
    fi
    noderaw=$(curl -s -X POST "$NODE" -H 'Content-Type: application/json' -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_signTransaction\",\"params\":[$params]}" | python3 -c 'import sys,json; j=json.load(sys.stdin); print(j["result"]["raw"] if "result" in j else "ERROR " + json.dumps(j.get("error")))')
    if [ "$ours" = "$noderaw" ]; then nodeok=$((${nodeok:-0}+1)); else nodefail=$((${nodefail:-0}+1)); echo "NODE MISMATCH $kind nonce=$nonce gas=$gas value=$value tip=$tip maxfee=$maxfee to=$to data=$data"; echo "  ours   $ours"; echo "  node   $noderaw"; fi
  fi
done
echo "diff-sign against cast: $ok agree, $fail differ, of $N"
if [ -n "$NODE" ]; then
  echo "diff-sign against the node at $NODE: ${nodeok:-0} agree, ${nodefail:-0} differ, of $N"
  [ "${nodefail:-0}" -eq 0 ] || fail=$((fail+1))
fi
[ "$fail" -eq 0 ]

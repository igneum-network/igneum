#!/usr/bin/env sh
# The fund's contract on a devnet from the faucet's key (the project's order, 11 October 2026 23:03 UK: the seven signers
# as placeholder keys the founder replaces by his names in the morning; the fund spends nothing until the set is
# published). Run on the box from the repo root:
#   DEPLOYER_KEY=<hex> contracts/tools/devnet-fund-deploy.sh <rpc url> <out dir>
# Makes seven placeholder signers with `cast wallet new` into <out dir>/fund-signers.json (addresses and flags: the
# first three company-connected, four independent; mode 600, the keys in <out dir>/fund-signer-keys.json, mode 600,
# never printed), reads the chain id and the genesis, deploys by the script with the set in the environment, funds
# the contract with 1 IGN from the faucet so the credit path is on the log, reads the set back, and writes the
# record <out dir>/fund-<chain id>.json (the address, the chain, the genesis, the commit, the signers, the flags,
# the deployment transaction). The key reaches forge through the environment, never argv.
set -eu
cd "$(dirname "$0")/../.."
RPC=${1:?rpc url}
OUT=${2:?out dir}
[ -n "${DEPLOYER_KEY:-}" ] || { echo "DEPLOYER_KEY is not set"; exit 2; }
umask 077
mkdir -p "$OUT"
rpc() { curl -s -X POST "$RPC" -H 'Content-Type: application/json' -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}" | python3 -c 'import sys,json; r=json.load(sys.stdin); print(r["result"] if "result" in r else "error:"+json.dumps(r.get("error")))'; }
CHAIN=$(rpc eth_chainId '[]')
GENESIS=$(curl -s -X POST "$RPC" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x0",false]}' | python3 -c 'import sys,json; print(json.load(sys.stdin)["result"]["hash"])')
CHAIN_DEC=$(python3 -c "print(int('$CHAIN',16))")
echo "chain $CHAIN_DEC genesis $GENESIS"
cd contracts
if [ ! -s "$OUT/fund-signers.json" ]; then
  python3 - "$OUT" <<'PY'
import json, subprocess, sys
out = sys.argv[1]
addrs, keys = [], []
for i in range(7):
    r = subprocess.run(["cast", "wallet", "new", "--json"], capture_output=True, text=True, check=True)
    w = json.loads(r.stdout)
    w = w.get("data", w) if isinstance(w, dict) else w  # cast 1.8 wraps its JSON in {"data": ...}
    w = w[0] if isinstance(w, list) else w
    addrs.append(w["address"]); keys.append(w["private_key"])
flags = [1, 1, 1, 0, 0, 0, 0]
json.dump({"placeholder": True, "note": "placeholder signers the founder replaces by his named set; the fund spends nothing until the set is published",
           "signers": [{"address": a, "company_connected": bool(f)} for a, f in zip(addrs, flags)]}, open(out + "/fund-signers.json", "w"), indent=1)
json.dump({"keys": keys}, open(out + "/fund-signer-keys.json", "w"))
PY
  chmod 600 "$OUT/fund-signers.json" "$OUT/fund-signer-keys.json"
fi
SIGNERS=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(",".join(s["address"] for s in d["signers"]))' "$OUT/fund-signers.json")
FLAGS=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(",".join("1" if s["company_connected"] else "0" for s in d["signers"]))' "$OUT/fund-signers.json")
DEPLOYER=$(cast wallet address --private-key "$DEPLOYER_KEY")
echo "deployer $DEPLOYER nonce $(rpc eth_getTransactionCount "[\"$DEPLOYER\", \"latest\"]")"
if [ -s "$OUT/fund-$CHAIN_DEC.json" ]; then
  # a record exists: the contract stands; only the credit and the read-back run again (one deployment per record)
  ADDR=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["address"])' "$OUT/fund-$CHAIN_DEC.json")
  TX=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["deploy_tx"])' "$OUT/fund-$CHAIN_DEC.json")
  echo "IgneumFund $ADDR (from the record; not deployed again)"
else
  # a raw create with a gas limit (devnet9, 12 October 2026: forge's creation estimate meets "method handler
  # crashed" on the node; a raw send lands), the constructor's arguments encoded from the set
  BC=$(forge inspect src/fund/IgneumFund.sol:IgneumFund bytecode)
  FL=$(python3 -c 'import sys; print("[" + ",".join("true" if f == "1" else "false" for f in sys.argv[1].split(",")) + "]")' "$FLAGS")
  ARGS=$(cast abi-encode "constructor(address[],bool[])" "[$SIGNERS]" "$FL")
  cast send --rpc-url "$RPC" --private-key "$DEPLOYER_KEY" --gas-limit 4500000 --create "${BC}${ARGS#0x}" > "$OUT/fund-deploy.log" 2>&1 || { tail -5 "$OUT/fund-deploy.log"; exit 1; }
  ADDR=$(grep -E "^contractAddress" "$OUT/fund-deploy.log" | awk '{print $2}')
  [ -n "$ADDR" ] || { echo "no address in the log"; tail -5 "$OUT/fund-deploy.log"; exit 1; }
  grep -qE "^status +1" "$OUT/fund-deploy.log" || { echo "the deployment failed:"; grep -E "^(status|gasUsed)" "$OUT/fund-deploy.log"; exit 1; }
  echo "IgneumFund $ADDR"
  TX=$(grep -E "^transactionHash" "$OUT/fund-deploy.log" | awk '{print $2}')
fi
CODE=$(rpc eth_getCode "[\"$ADDR\", \"latest\"]" | cut -c1-10)
echo "code $CODE... tx $TX"
# the credit path: 1 IGN from the faucet lands on the log (Received). A plain value transfer to a contract needs
# more than the 21,000 a bare transfer gets (the first run failed at 21,000: status 0, the balance unmoved), so the
# gas limit is given.
cast send --rpc-url "$RPC" --private-key "$DEPLOYER_KEY" --value 1ether --gas-limit 60000 "$ADDR" > "$OUT/fund-credit.log" 2>&1 || { tail -5 "$OUT/fund-credit.log"; exit 1; }
grep -qE "^status +1" "$OUT/fund-credit.log" || { echo "the credit transaction failed:"; grep -E "^(status|gasUsed)" "$OUT/fund-credit.log"; exit 1; }
BAL=$(rpc eth_getBalance "[\"$ADDR\", \"latest\"]")
SET=$(cast call --rpc-url "$RPC" "$ADDR" "signers()(address[])")
echo "balance $BAL signers $SET"
python3 - "$OUT" "$ADDR" "$CHAIN_DEC" "$GENESIS" "$TX" "$BAL" <<'PY'
import json, subprocess, sys
out, addr, chain, genesis, tx, bal = sys.argv[1:7]
commit = subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()
signers = json.load(open(out + "/fund-signers.json"))
json.dump({"contract": "IgneumFund", "address": addr, "chain_id": int(chain), "genesis": genesis, "commit": commit, "deploy_tx": tx,
           "balance_wei_after_credit": int(bal, 16), "signers": signers["signers"], "placeholder_set": True,
           "mandate": "docs/igneumhash/FUND-MANDATE-2026-10-11.md section 5",
           "note": "the placeholder set the founder replaces by his named seven; the fund spends nothing until the set is published; the genesis config's fund address is the launch chain's deployment, not this one"},
          open(out + "/fund-%s.json" % chain, "w"), indent=1)
print("record", out + "/fund-%s.json" % chain)
PY

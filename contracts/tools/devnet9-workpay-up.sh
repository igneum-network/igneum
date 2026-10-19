#!/usr/bin/env sh
# WorkPay on devnet9 against the stand-in (docs/work-api/INTERFACE.md sections 2 and 10): one pass, run on builder seven
# from the repo root by path, once the writer address holds development-chain IGN from the DevFaucet:
#   WRITER_KEY_FILE=<the writer's key file> contracts/tools/devnet9-workpay-up.sh <rpc url> <work dir> <window> [budget IGN]
# 1. deploys IgneumWork(writer) (the STAND-IN form: label() names it) and WorkPay(work) from the writer's key;
# 2. runs tools/work-api/workroot.py over the window (refused unless finalized: windowEnd + 64 <= head) and writes
#    the record into the stand-in with setWindow (refused again by the contract unless finalized);
# 3. funds WorkPay's budget for the window (default 10 IGN, labelled "development-network tokens; no monetary value")
#    and pays every entry of the window in one claimMany from the tool's proofs;
# 4. stores <work dir>/devnet9-workpay-<window>.json: the addresses, the transaction hashes, the record, the payments
#    read back from the chain, and the tool's sha; nothing is printed that is a key.
set -eu
cd "$(dirname "$0")/../.."
RPC=${1:?rpc url}
OUT=${2:?work dir}
WINDOW=${3:?window}
BUDGET=${4:-10}
[ -s "${WRITER_KEY_FILE:-}" ] || { echo "WRITER_KEY_FILE is not a readable file"; exit 2; }
umask 077
mkdir -p "$OUT"
KEY=0x$(tr -d ' \n' < "$WRITER_KEY_FILE" | sed 's/^0x//')
TOOL=tools/work-api/workroot.py
rpc() { curl -s -X POST "$RPC" -H 'Content-Type: application/json' -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}" | python3 -c 'import sys,json; r=json.load(sys.stdin); print(r["result"] if "result" in r else "error:"+json.dumps(r.get("error")))'; }
CHAIN=$(rpc eth_chainId '[]'); HEAD=$(rpc eth_blockNumber '[]'); echo "chain $CHAIN head $HEAD"
cd contracts
WRITER=$(cast wallet address --private-key "$KEY")
echo "writer $WRITER balance $(cast balance --rpc-url "$RPC" "$WRITER")"
cd ..
# 1. the contracts (reused when the record already names them)
REC="$OUT/devnet9-workpay-contracts.json"
if [ -s "$REC" ]; then
  WORK=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["IgneumWork"])' "$REC")
  PAY=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["WorkPay"])' "$REC")
else
  cd contracts
  forge build > "$OUT/build.log" 2>&1 || { echo "the build failed"; tail -5 "$OUT/build.log"; exit 1; }
  # a creation goes as a raw send with a gas limit: the node's gas estimate on a creation crashes its handler
  # (the product team's finding of 02:1x UK on A9 and the public RPC; a plain eth_estimateGas answers)
  create() { # <path:Name> <abi-encoded constructor args without 0x> <log>
    INIT="$(forge inspect "$1" bytecode)$2"
    cast send --rpc-url "$RPC" --private-key "$KEY" --json --gas-limit 3000000 --create "$INIT" 2>&1 | tee "$3" | python3 -c 'import sys,json
try:
    r=json.load(sys.stdin); print(r.get("contractAddress") or "", r.get("status"))
except Exception as e:
    print("")'
  }
  R=$(create src/work/IgneumWork.sol:IgneumWork "$(cast abi-encode 'constructor(address)' "$WRITER" | sed 's/^0x//')" "$OUT/igneumwork-deploy.log")
  WORK=${R%% *}; [ -n "$WORK" ] && [ "${R#* }" = "0x1" ] || { echo "IgneumWork did not deploy"; tail -5 "$OUT/igneumwork-deploy.log"; exit 1; }
  R=$(create src/workpay/WorkPay.sol:WorkPay "$(cast abi-encode 'constructor(address)' "$WORK" | sed 's/^0x//')" "$OUT/workpay-deploy.log")
  PAY=${R%% *}; [ -n "$PAY" ] && [ "${R#* }" = "0x1" ] || { echo "WorkPay did not deploy"; tail -5 "$OUT/workpay-deploy.log"; exit 1; }
  cd ..
  python3 - "$REC" "$WORK" "$PAY" "$WRITER" "$CHAIN" <<'PY'
import json, subprocess, sys
rec, work, pay, writer, chain = sys.argv[1:6]
json.dump({"chain_id": int(chain, 16), "IgneumWork": work, "WorkPay": pay, "writer": writer, "form": "stand-in",
           "label": "development-network tokens; no monetary value",
           "commit": subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()}, open(rec, "w"), indent=1)
PY
fi
echo "IgneumWork $WORK (label: $(cast call --rpc-url "$RPC" "$WORK" 'label()(string)'))"
echo "WorkPay $PAY"
# 2. the window's record and the stand-in's write
python3 "$TOOL" proofs "$RPC" "$WINDOW" > "$OUT/window-$WINDOW.json"
ROOT=$(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); print(r["root"])' "$OUT/window-$WINDOW.json")
TOTAL=$(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); print(r["totalWork"])' "$OUT/window-$WINDOW.json")
ENDH=$(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); print(r["endHash"])' "$OUT/window-$WINDOW.json")
ENTRIES=$(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); print(r["entryCount"])' "$OUT/window-$WINDOW.json")
DEPTH=$(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); print(r["depth"])' "$OUT/window-$WINDOW.json")
echo "window $WINDOW root $ROOT total $TOTAL entries $ENTRIES depth $DEPTH end $ENDH"
if [ "$(cast call --rpc-url "$RPC" "$WORK" 'window(uint64)(bytes32,uint256,bytes32,uint32,uint8,uint8)' "$WINDOW" 2>/dev/null | head -1)" = "$ROOT" ]; then
  echo "the stand-in already carries window $WINDOW"
else
  SETTX=$(cast send --rpc-url "$RPC" --private-key "$KEY" --json --gas-limit 400000 "$WORK" 'setWindow(uint64,bytes32,uint256,bytes32,uint32,uint8)' "$WINDOW" "$ROOT" "$TOTAL" "$ENDH" "$ENTRIES" "$DEPTH" | python3 -c 'import sys,json; r=json.load(sys.stdin); print(r["transactionHash"], r["status"])')
  echo "setWindow $SETTX"
fi
# 3. the budget and the distribution
if [ "$(cast call --rpc-url "$RPC" "$PAY" 'budget(uint64)(uint256)' "$WINDOW")" = "0" ]; then
  FUNDTX=$(cast send --rpc-url "$RPC" --private-key "$KEY" --json --gas-limit 200000 --value "${BUDGET}ether" "$PAY" 'fund(uint64)' "$WINDOW" | python3 -c 'import sys,json; r=json.load(sys.stdin); print(r["transactionHash"], r["status"])')
  echo "fund $BUDGET IGN: $FUNDTX"
fi
ARGS=$(python3 - "$OUT/window-$WINDOW.json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
ps = r["proofs"]
accounts = "[" + ",".join(p["address"] for p in ps) + "]"
weights = "[" + ",".join(str(p["weight"]) for p in ps) + "]"
indices = "[" + ",".join(str(p["index"]) for p in ps) + "]"
sibs = "[" + ",".join("[" + ",".join(p["siblings"]) + "]" for p in ps) + "]"
print(accounts, weights, indices, sibs)
PY
)
set -- $ARGS
CLAIMTX=$(cast send --rpc-url "$RPC" --private-key "$KEY" --json --gas-limit $((200000 + 160000 * ENTRIES)) "$PAY" 'claimMany(uint64,address[],uint256[],uint256[],bytes32[][])' "$WINDOW" "$1" "$2" "$3" "$4" | python3 -c 'import sys,json; r=json.load(sys.stdin); print(r["transactionHash"], r["status"], r["gasUsed"])')
echo "claimMany $CLAIMTX"
# 4. the record: the payments read back from the chain's logs
python3 - "$OUT" "$WINDOW" "$WORK" "$PAY" "$RPC" "$CLAIMTX" "$TOOL" <<'PY'
import hashlib, json, subprocess, sys, urllib.request
out, window, work, pay, rpc, claimtx, tool = sys.argv[1:8]
def call(m, p):
    req = urllib.request.Request(rpc, data=json.dumps({"jsonrpc": "2.0", "id": 1, "method": m, "params": p}).encode(), headers={"Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req, timeout=30))["result"]
rec = json.load(open("%s/window-%s.json" % (out, window)))
txh = claimtx.split()[0]
rcpt = call("eth_getTransactionReceipt", [txh])
topic = subprocess.run(["cast", "keccak", "Paid(uint64,address,uint256,uint256)"], capture_output=True, text=True).stdout.strip()
payments = []
for lg in rcpt["logs"]:
    if lg["address"].lower() == pay.lower() and lg["topics"][0] == topic:
        w = int(lg["topics"][1], 16); acct = "0x" + lg["topics"][2][-40:]
        data = lg["data"][2:]; weight = int(data[:64], 16); amount = int(data[64:128], 16)
        payments.append({"window": w, "address": acct, "weight": weight, "amountWei": amount})
total_paid = sum(p["amountWei"] for p in payments)
budget = int(subprocess.run(["cast", "call", "--rpc-url", rpc, pay, "budget(uint64)(uint256)", window], capture_output=True, text=True).stdout.split()[0])
remainder = int(subprocess.run(["cast", "call", "--rpc-url", rpc, pay, "remainder(uint64)(uint256)", window], capture_output=True, text=True).stdout.split()[0])
record = {
    "label": "development-network tokens; no monetary value",
    "chainId": rec["chainId"], "window": int(window), "windowStart": rec["windowStart"], "windowEnd": rec["windowEnd"], "endHash": rec["endHash"],
    "IgneumWork": work, "WorkPay": pay, "form": "stand-in",
    "root": rec["root"], "totalWork": rec["totalWork"], "entryCount": rec["entryCount"], "depth": rec["depth"],
    "budgetWei": budget, "paidWei": total_paid, "remainderWei": remainder, "paymentsEqualBudgetLessRemainder": total_paid + remainder == budget,
    "claimTx": txh, "claimBlock": int(rcpt["blockNumber"], 16), "claimStatus": rcpt["status"],
    "payments": payments, "entries": rec["entries"],
    "tool": {"path": tool, "sha256": hashlib.sha256(open(tool, "rb").read()).hexdigest()},
    "commit": subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip(),
}
json.dump(record, open("%s/devnet9-workpay-%s.json" % (out, window), "w"), indent=1)
print(json.dumps({k: record[k] for k in ["window", "root", "totalWork", "entryCount", "budgetWei", "paidWei", "remainderWei", "paymentsEqualBudgetLessRemainder", "claimTx", "claimStatus"]}, indent=1))
PY

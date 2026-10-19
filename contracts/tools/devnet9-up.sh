#!/usr/bin/env sh
# devnet9's kit and test coins in one pass after the genesis (the founder's "public reachable dev net with test
# coins", 12 October 2026, through main). Run on builder seven from the repo root, by path, once the genesis is recorded
# in dev/chain/igneum-chains.json (the deploy tool refuses otherwise):
#   DEV_FAUCET_KEY_FILE=<the dev faucet account's key file> [FAUCET_RELAYER=<the relay's address>] contracts/tools/devnet9-up.sh <rpc url> <work dir>
# 1. a FRESH deployer key (made into <work dir>/deployer.key, mode 600, never printed) funded with 50 IGN from the
#    dev faucet account (the genesis's fixed dev-only account, 10^9 IGN), so the kit deploys from nonce 0 in the
#    published order (the ERC-4337 pair at 0 and 1, the Pay set at 2 to 7: DESIGN.md 6b);
# 2. the DevFaucet (25 IGN a drip, one a day per address, a 5,000 IGN daily cap) from the same deployer at nonce 8,
#    funded with 175,000 IGN from the dev faucet account (the project's figure: a week at 25 IGN a day for 1,000 addresses);
# 3. the records: <work dir>/devnet9-kit.json (every address with its nonce), the DevFaucet's address printed last.
set -eu
cd "$(dirname "$0")/../.."
RPC=${1:?rpc url}
OUT=${2:?work dir}
[ -s "${DEV_FAUCET_KEY_FILE:-}" ] || { echo "DEV_FAUCET_KEY_FILE is not a readable file"; exit 2; }
umask 077
mkdir -p "$OUT"
CLI=wallet/target/debug/igneum-wallet
rpc() { curl -s -X POST "$RPC" -H 'Content-Type: application/json' -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}" | python3 -c 'import sys,json; r=json.load(sys.stdin); print(r["result"] if "result" in r else "error:"+json.dumps(r.get("error")))'; }
CHAIN=$(rpc eth_chainId '[]'); echo "chain $CHAIN"
# the dev faucet account's vault for the CLI's sends (the key file is hex; the vault is made once beside it)
FV="$OUT/dev-faucet.vault.json"; FP="$OUT/dev-faucet.pw"
if [ ! -s "$FV" ]; then
  head -c 24 /dev/urandom | base64 > "$FP"; chmod 600 "$FP"
  K=$(cat "$DEV_FAUCET_KEY_FILE" | tr -d ' \n'); case "$K" in 0x*) ;; *) K="0x$K";; esac
  printf '%s' "$K" | $CLI --rpc "$RPC" --vault "$FV" import --stdin --password-file "$FP" > /dev/null
  chmod 600 "$FV"
fi
FA=$($CLI --rpc "$RPC" --vault "$FV" address | python3 -c 'import sys,json; print(json.load(sys.stdin)["address"])')
echo "dev faucet account $FA balance $(rpc eth_getBalance "[\"$FA\",\"latest\"]")"
# a fresh deployer
if [ ! -s "$OUT/deployer.key" ]; then head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$OUT/deployer.key"; chmod 600 "$OUT/deployer.key"; fi
DK=0x$(cat "$OUT/deployer.key")
DEPLOYER=$(cd contracts && cast wallet address --private-key "$DK")
echo "deployer $DEPLOYER nonce $(rpc eth_getTransactionCount "[\"$DEPLOYER\",\"latest\"]")"
if [ "$(rpc eth_getBalance "[\"$DEPLOYER\",\"latest\"]")" = "0x0" ]; then
  echo "funding the deployer with 50 IGN: $($CLI --rpc "$RPC" --vault "$FV" send --to "$DEPLOYER" --amount 50 --password-file "$FP" 2>/dev/null | tail -1 | cut -c1-18)"
  for i in $(seq 1 30); do [ "$(rpc eth_getBalance "[\"$DEPLOYER\",\"latest\"]")" != "0x0" ] && break; sleep 4; done
fi
NONCE=$(rpc eth_getTransactionCount "[\"$DEPLOYER\",\"latest\"]")
if [ "$NONCE" = "0x8" ] && [ -s "$OUT/kit-deploy.log" ]; then
  echo "--- the kit is deployed (the deployer at nonce 8; the log kept)"
else
  echo "--- the kit"
  DEPLOYER_KEY=$DK NETWORK=igneum-devnet9 contracts/tools/devnet-deploy.sh "$RPC" 2>&1 | tee "$OUT/kit-deploy.log" | grep -vE "private"
fi
echo "--- the DevFaucet (nonce 8)"
cd contracts
if [ -n "${DEVFAUCET_ADDR:-}" ]; then
  # the contract already deployed by hand (the first run: `forge create` met "method handler crashed" on both RPCs
  # while `cast send --create` with a gas limit went through); the address given, the funding and the record run
  FAUCET=$DEVFAUCET_ADDR
else
  BC=$(forge inspect src/faucet/DevFaucet.sol:DevFaucet bytecode)
  ARGS=$(cast abi-encode "constructor(uint256,uint256,uint256)" 25000000000000000000 86400 5000000000000000000000)
  FAUCET=$(cast send --rpc-url "$RPC" --private-key "$DK" --gas-limit 900000 --create "${BC}${ARGS#0x}" 2>&1 | tee "$OUT/devfaucet-deploy.log" | grep -E "^contractAddress" | awk '{print $2}')
fi
cd ..
[ -n "$FAUCET" ] || { echo "the DevFaucet did not deploy"; tail -5 "$OUT/devfaucet-deploy.log"; exit 1; }
echo "DevFaucet $FAUCET"
echo "funding the DevFaucet with 175000 IGN: $($CLI --rpc "$RPC" --vault "$FV" send --to "$FAUCET" --amount 175000 --password-file "$FP" 2>/dev/null | tail -1 | cut -c1-18)"
for i in $(seq 1 30); do [ "$(rpc eth_getBalance "[\"$FAUCET\",\"latest\"]")" != "0x0" ] && break; sleep 4; done
echo "DevFaucet holds $(rpc eth_getBalance "[\"$FAUCET\",\"latest\"]")"
# the relay's gas: the relayer (reliability's key on builder one; its address from the relay's GET /state, given as
# FAUCET_RELAYER) pays every drip's gas and must hold a budget before the route opens (the first public drip of 12
# October 2026 met "insufficient funds": the relayer held nothing); 500 IGN is weeks of drips at the chain's prices
if [ -n "${FAUCET_RELAYER:-}" ]; then
  if [ "$(rpc eth_getBalance "[\"$FAUCET_RELAYER\",\"latest\"]")" = "0x0" ]; then
    echo "funding the relayer $FAUCET_RELAYER with 500 IGN of gas: $($CLI --rpc "$RPC" --vault "$FV" send --to "$FAUCET_RELAYER" --amount 500 --password-file "$FP" 2>/dev/null | tail -1 | cut -c1-18)"
  else
    echo "the relayer $FAUCET_RELAYER holds $(rpc eth_getBalance "[\"$FAUCET_RELAYER\",\"latest\"]") already"
  fi
else
  echo "FAUCET_RELAYER not given: fund the relay's key with 500 IGN of gas before the route opens (its address is the relay's GET /state \"relayer\")"
fi
python3 - "$OUT" "$FAUCET" "$DEPLOYER" "$CHAIN" <<'PY'
import json, re, subprocess, sys
out, faucet, deployer, chain = sys.argv[1:5]
log = open(out + "/kit-deploy.log").read()
addrs = {}
for line in log.splitlines():
    m = re.match(r"\s*(EntryPoint v[\d.]+|SimpleAccountFactory|IgneumPay|IgneumRecurring|IgneumEscrow|TestToken|TestCollectible|Faucet)\s+(0x[0-9a-fA-F]{40})", line.strip())
    if m: addrs[m.group(1)] = m.group(2)
addrs["DevFaucet"] = faucet
rec = {"chain_id": int(chain, 16), "deployer": deployer, "order": "the ERC-4337 pair at nonces 0 and 1, the Pay set at 2 to 7, the DevFaucet at 8", "addresses": addrs,
       "dev_faucet": {"drip_ign": 25, "cooldown_s": 86400, "daily_cap_ign": 5000, "label": "development-network tokens; no monetary value"},
       "commit": subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()}
json.dump(rec, open(out + "/devnet9-kit.json", "w"), indent=1)
print(json.dumps(rec, indent=1))
PY

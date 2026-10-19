#!/usr/bin/env sh
# The bonded-identity actions against a dev chain with the authority rule (devnet4 by default; devnet5 when the protocol team
# lands it: NODE_RPC). Run on the box from wallet/ after `cargo build`, as one plain command. Every chain write here
# is a seat test transaction (never adoption); the bond is 1 IGN so the chain's draw is not moved (32 units of
# 65,536 at the next snapshot but one).
#
# Rows, in order (the log is the record; RUNS.md cites it):
#   1  the chain identified by its genesis hash (the name, the registry, the rule);
#   2  the dev faucet (public key material, dev-only) imported into a vault and a fresh holder account funded;
#   3  an identity key made (an Ed25519 seed in a 0600 file, its public key the identity);
#   4  bond 1 IGN to the identity from the holder: included, status 1, igneum_bondOf reads amount and account;
#   5  the draw for the identity (units 0 at the current snapshot: a bond enters two epochs on) and for the node's
#      own key, the wallet's eligible round against the node's (agrees_with_node);
#   6  the signer rotated to a second key: included, igneum_bondOf reads the signer;
#   7  another account adding to the identity: refused by the chain at the estimate (the registry's rule);
#   8  the version 3 forms (bond with exit, set-exit, claim-exit, hostile claim): refused at the estimate on a
#      version 2 registry, and claim-exit sent with --gas to record the refusal on the chain (status 0);
#   9  a read of the bond as a transaction: the sentence says it changes nothing (dry run only).
set -eu
cd "$(dirname "$0")/.."
BIN=target/debug
RPC=${NODE_RPC:-${IGNEUM_MESH_RPC:?set the mesh RPC}}
D=${AUTH_DIR:-${IGNEUM_BOX_ROOT:-/srv}/builds/product-b/work/authority}
mkdir -p "$D"; chmod 700 "$D"
LOG="$D/authority-smoke-$(date -u +%Y%m%dT%H%M%SZ).log"
exec > "$LOG" 2>&1
echo "authority smoke $(git rev-parse --short HEAD) rpc $RPC $(date -u +%Y-%m-%dT%H:%M:%SZ)"
W="$BIN/igneum-wallet --rpc $RPC"

wait_receipt() {
  # $1 hash: polls the node's receipt until a block carries it (up to 6 minutes under the round clock); prints the
  # wallet's settlement state line
  i=0
  while [ $i -lt 72 ]; do
    if curl -s -X POST -H content-type:application/json --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_getTransactionReceipt\",\"params\":[\"$1\"]}" "$RPC" | grep -q '"blockNumber"'; then
      $W settlement "$1" | grep -E '"state"|"block"' | head -2
      return 0
    fi
    i=$((i+1)); sleep 5
  done
  echo "no receipt for $1 after 6 minutes"; return 1
}
status_of() {
  # $1 hash: the receipt's status from the node
  curl -s -X POST -H content-type:application/json --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_getTransactionReceipt\",\"params\":[\"$1\"]}" "$RPC" | python3 -c 'import sys,json; r=json.load(sys.stdin)["result"]; print("status", r["status"], "block", int(r["blockNumber"],16))'
}

echo "--- 1 the chain"
$W chain

echo "--- 2 the faucet's vault and a fresh holder"
[ -f "$D/pw" ] || { head -c 24 /dev/urandom | base64 > "$D/pw"; chmod 600 "$D/pw"; }
if [ ! -f "$D/faucet.json" ]; then
  # the dev faucet's key is keccak256 of its published phrase (node/params/genesis_igneum.go): public, dev-only
  cast keccak "igneum development faucet: dev-only, public key material, never fund on any other chain" > "$D/faucet.key"
  chmod 600 "$D/faucet.key"
  $W --vault "$D/faucet.json" import --password-file "$D/pw" --stdin < "$D/faucet.key"
fi
FAUCET=$($W --vault "$D/faucet.json" address | python3 -c 'import sys,json; print(json.load(sys.stdin)["address"])')
echo "faucet $FAUCET"
if [ ! -f "$D/holder.json" ]; then
  $W --vault "$D/holder.json" create --password-file "$D/pw" > "$D/holder.create" 2>&1
  chmod 600 "$D/holder.create"
fi
HOLDER=$($W --vault "$D/holder.json" address | python3 -c 'import sys,json; print(json.load(sys.stdin)["address"])')
echo "holder $HOLDER"
H=$($W --vault "$D/faucet.json" send --to "$HOLDER" --amount 5 --password-file "$D/pw" 2>"$D/send.err") || { echo "the funding send failed:"; cat "$D/send.err"; exit 1; }
echo "funded: $H"; wait_receipt "$H"

echo "--- 3 the identity key"
[ -f "$D/bond.seed" ] || { head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$D/bond.seed"; chmod 600 "$D/bond.seed"; }
[ -f "$D/signer.seed" ] || { head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$D/signer.seed"; chmod 600 "$D/signer.seed"; }
KEY=$($W identity --seed-file "$D/bond.seed")
SIGNER=$($W signing-key --seed-file "$D/signer.seed")
echo "identity (pk = G(sk)) $KEY signing key (Ed25519) $SIGNER"
PROVER=${PI_CLAIM:-${IGNEUM_BOX_ROOT:-/srv}/builds/product-b/work/authority/pi-claim}
[ -x "$PROVER" ] && echo "prover $PROVER: pk check $($PROVER pk "$D/bond.seed")"

echo "--- 4 bond 1 IGN"
H=$($W --vault "$D/holder.json" bond --key "$KEY" --amount 1 --password-file "$D/pw")
echo "bond: $H"; wait_receipt "$H"; status_of "$H"
curl -s -X POST -H content-type:application/json --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"igneum_bondOf\",\"params\":[\"$KEY\",\"latest\"]}" "$RPC"; echo

echo "--- 5 the draw"
$W draw --key "$KEY"
echo "the node's own key:"
$W draw | grep -E '"key"|"eligible_round"|"node_reading"|"agrees_with_node"|"units"|"share_percent"'

echo "--- 6 the signer rotated"
H=$($W --vault "$D/holder.json" set-signer --key "$KEY" --signer "$SIGNER" --password-file "$D/pw")
echo "set-signer: $H"; wait_receipt "$H"; status_of "$H"
curl -s -X POST -H content-type:application/json --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"igneum_bondOf\",\"params\":[\"$KEY\",\"latest\"]}" "$RPC"; echo

echo "--- 7 another account adding to the identity (expected: refused at the estimate)"
$W --vault "$D/faucet.json" bond --key "$KEY" --amount 1 --password-file "$D/pw" || echo "refused as expected (exit $?)"

balance_of() {
  curl -s -X POST -H content-type:application/json --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_getBalance\",\"params\":[\"$1\",\"latest\"]}" "$RPC" | python3 -c 'import sys,json; print(int(json.load(sys.stdin)["result"],16))'
}
REGV=$($W chain | python3 -c 'import sys,json; a=json.load(sys.stdin).get("authority") or {}; print(a.get("registry_version",0))')
if [ "$REGV" = "3" ]; then
  echo "--- 8 the version 3 forms on the lab registry (devnet5; KEYED-FORM.md section 4): bond with exit, the exit changed with a top-up, the holder's exit by the reveal claim, a hostile reveal claim with phi burnt"
  [ -f "$D/bond2.seed" ] || { head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$D/bond2.seed"; chmod 600 "$D/bond2.seed"; }
  [ -f "$D/bond3.seed" ] || { head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$D/bond3.seed"; chmod 600 "$D/bond3.seed"; }
  KEY2=$($W identity --seed-file "$D/bond2.seed"); KEY3=$($W identity --seed-file "$D/bond3.seed")
  echo "second identity $KEY2 (exit set in the bond transaction to the faucet), third identity $KEY3 (for the hostile claim)"
  H=$($W --vault "$D/holder.json" bond --key "$KEY2" --amount 1 --exit "$FAUCET" --password-file "$D/pw")
  echo "bond-with-exit: $H"; wait_receipt "$H"; status_of "$H"
  $W draw --key "$KEY2" | grep -E '"exit"|"amount_ign"|"bonded"'
  H=$($W --vault "$D/holder.json" set-exit --key "$KEY2" --exit "$HOLDER" --amount 0.5 --password-file "$D/pw")
  echo "exit changed with a 0.5 IGN top-up: $H"; wait_receipt "$H"; status_of "$H"
  $W draw --key "$KEY2" | grep -E '"exit"|"amount_ign"'
  echo "the holder's own exit: the reveal claim to the registered exit address (expected: the whole 1.5 IGN back to the holder, the key unbonded)"
  B0=$(balance_of "$HOLDER")
  H=$($W --vault "$D/holder.json" claim --reveal --sk-file "$D/bond2.seed" --to "$HOLDER" --password-file "$D/pw" 2>"$D/claim-reveal.err") || { echo "the reveal claim failed:"; cat "$D/claim-reveal.err"; }
  if [ -n "${H:-}" ]; then echo "reveal claim (own exit): $H"; wait_receipt "$H"; status_of "$H"; B1=$(balance_of "$HOLDER"); echo "holder balance step $(python3 -c "print(($B1-$B0)/1e18)") IGN (the fee came off it)"; $W draw --key "$KEY2" | grep -E '"bonded"|"amount_ign"'; fi
  echo "the hostile claim: a third identity bonded 1 IGN by the holder, its secret presented by the faucet account to the faucet's own address (expected: three quarters to the faucet, one quarter burnt to the fund)"
  H=$($W --vault "$D/holder.json" bond --key "$KEY3" --amount 1 --password-file "$D/pw")
  echo "bond: $H"; wait_receipt "$H"; status_of "$H"
  FUND=$(curl -s -X POST -H content-type:application/json --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_getStorageAt\",\"params\":[\"0x0000000000000000000000000000000000000b0d\",\"0x2\",\"latest\"]}" "$RPC" | python3 -c 'import sys,json; w=json.load(sys.stdin)["result"]; print("0x"+w[-40:])')
  echo "fund address (registry slot 2) $FUND"
  F0=$(balance_of "$FAUCET"); D0=$(balance_of "$FUND")
  H=$($W --vault "$D/faucet.json" claim --reveal --sk-file "$D/bond3.seed" --to "$FAUCET" --password-file "$D/pw" 2>"$D/claim-hostile.err") || { echo "the hostile claim failed:"; cat "$D/claim-hostile.err"; }
  if [ -n "${H:-}" ]; then echo "hostile reveal claim: $H"; wait_receipt "$H"; status_of "$H"; F1=$(balance_of "$FAUCET"); D1=$(balance_of "$FUND"); echo "faucet step $(python3 -c "print(($F1-$F0)/1e18)") IGN (0.75 less the fee expected), fund step $(python3 -c "print(($D1-$D0)/1e18)") IGN (0.25 expected)"; $W draw --key "$KEY3" | grep -E '"bonded"|"amount_ign"'; fi
  echo "the proof form on the lab registry (expected: refused at the estimate; the verifier is not in the node yet)"
  if [ -x "$PROVER" ]; then
    $W --vault "$D/holder.json" claim --sk-file "$D/bond.seed" --to "$HOLDER" --prover "$PROVER" --out "$D/claim-d5.bin" --password-file "$D/pw" || echo "proof-form claim refused as expected (exit $?)"
  fi
else
echo "--- 8 the version 3 forms on a version 2 registry (expected: each refused at the estimate)"
$W --vault "$D/holder.json" bond --key "$KEY" --amount 1 --exit "$HOLDER" --password-file "$D/pw" || echo "bond-with-exit refused as expected (exit $?)"
$W --vault "$D/holder.json" set-exit --key "$KEY" --exit "$HOLDER" --password-file "$D/pw" || echo "set-exit refused as expected (exit $?)"
if [ -x "$PROVER" ]; then
  CHAIN_ID=$($W chain | python3 -c 'import sys,json; print(json.load(sys.stdin)["chain_id"])')
  echo "the claim's proof made by the prover from the secret's file (domain 1, K 1, the Groth16 wrap, C = u64be(chain id $CHAIN_ID)), then carried to the registry (expected: refused at the estimate, the version 2 registry has no claim):"
  $W --vault "$D/holder.json" claim --sk-file "$D/bond.seed" --to "$HOLDER" --prover "$PROVER" --out "$D/claim.bin" --password-file "$D/pw" || echo "claim (the holder's own release, to the holder) refused by the chain as expected (exit $?)"
  ls -l "$D/claim.bin" | awk '{print "proof file bytes", $5}'
  echo "the proof under the registry's own check (the prover's verify: the identifier, the bound, the wrap against PI_IMAGE_ID, the journal):"
  $PROVER verify "$KEY" "$CHAIN_ID" 0 "${HOLDER#0x}" "$D/claim.bin" | head -1 || true
  echo "the same proof against another claimant (expected: refused, the message binds the claimant):"
  $PROVER verify "$KEY" "$CHAIN_ID" 0 "${FAUCET#0x}" "$D/claim.bin" | head -1 || true
  echo "the same proof under another nonce (expected: refused):"
  $PROVER verify "$KEY" "$CHAIN_ID" 1 "${HOLDER#0x}" "$D/claim.bin" | head -1 || true
  echo "the same proof under another chain id (expected: refused):"
  $PROVER verify "$KEY" 7315 0 "${HOLDER#0x}" "$D/claim.bin" | head -1 || true
  echo "the claim sent with --gas to record the chain's refusal on the chain (the proof made again by the prover; the wrap fits the node's transaction bound):"
  H=$($W --vault "$D/holder.json" claim --sk-file "$D/bond.seed" --to "$HOLDER" --prover "$PROVER" --out "$D/claim-2.bin" --gas 3000000 --password-file "$D/pw" 2>"$D/claim.err") || { echo "the forced claim failed:"; cat "$D/claim.err"; }
  if [ -n "${H:-}" ]; then echo "claim forced: $H"; wait_receipt "$H"; status_of "$H"; fi
else
  echo "no prover at $PROVER: the claim rows are skipped (wallet/tools/box-build-pi-claim.sh builds it on the box)"
fi
fi
echo "--- 9 a read as a transaction (dry run)"
$W --vault "$D/holder.json" bond --key "$KEY" --amount 0 --dry-run --password-file "$D/pw" 2>&1 | grep -E 'read of the identity|"network"' | head -2

echo "done $(date -u +%H:%M:%SZ)"
echo "$LOG"

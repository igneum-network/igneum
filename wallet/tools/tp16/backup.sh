#!/usr/bin/env sh
# TP-16 backup_cmd: the backup round trip (create, reveal with the password, import the words elsewhere, same
# address), chain-id mismatch refusal on an envelope, watch-only storage free of key material.
set -u
. "$(dirname "$0")/lib.sh"
start_anvil 8603
RPC=${NODE_RPC:-http://127.0.0.1:8603}

# 1. create on machine A, reveal, import on machine B: the same address
URLA=$(start_engine "$WORK/a" $RPC)
C=$(post "$URLA" api/create '{"password":"correct horse battery"}')
ADDR=$(echo "$C" | json 'j["address"]')
WORDS=$(post "$URLA" api/reveal '{"password":"correct horse battery"}' | json '" ".join(j["words"])')
URLB=$(start_engine "$WORK/b" $RPC)
I=$(post "$URLB" api/import '{"mode":"seed","data":"'"$WORDS"'","password":"another password"}')
ADDRB=$(echo "$I" | json 'j["address"]')
[ "$ADDR" = "$ADDRB" ] && [ -n "$ADDR" ] && probe backup_round_trip PASS "$ADDR on both machines" || probe backup_round_trip FAIL "$ADDR vs $ADDRB"

# 2. the raw key export derives the same address through the CLI
KEY=$(post "$URLA" api/reveal '{"password":"correct horse battery"}' | json 'j["key"]')
echo "$KEY" > "$WORK/key.txt"; echo "a third password" > "$WORK/pw.txt"
"$BIN/igneum-wallet" --vault "$WORK/c.json" import --password-file "$WORK/pw.txt" --stdin < "$WORK/key.txt" > /dev/null
ADDRC=$("$BIN/igneum-wallet" --vault "$WORK/c.json" address | json 'j["address"]')
[ "$ADDR" = "$ADDRC" ] && probe key_export_round_trip PASS "the exported key gives $ADDRC" || probe key_export_round_trip FAIL "$ADDR vs $ADDRC"

# 3. chain-id mismatch: an envelope prepared for chain 7310, altered to chain 1, is refused by the CLI signer
"$BIN/igneum-wallet" --rpc $RPC prepare --from $TEST_ADDR --to $OTHER_ADDR --amount 0.1 > "$WORK/env.json"
sed 's/"chain_id": 7310,/"chain_id": 1,/' "$WORK/env.json" > "$WORK/env-bad.json"
echo "$TEST_PHRASE" | "$BIN/igneum-wallet" --vault "$WORK/t.json" import --password-file "$WORK/pw.txt" --stdin > /dev/null
if "$BIN/igneum-wallet" --rpc $RPC --vault "$WORK/t.json" sign --password-file "$WORK/pw.txt" < "$WORK/env-bad.json" > /dev/null 2> "$WORK/sign.err"; then probe chain_id_mismatch_refused FAIL "signed an envelope with a mismatched chain id"; else probe chain_id_mismatch_refused PASS "$(tail -1 "$WORK/sign.err")"; fi
# and the good one signs
"$BIN/igneum-wallet" --rpc $RPC --vault "$WORK/t.json" sign --password-file "$WORK/pw.txt" < "$WORK/env.json" > "$WORK/signed.json" 2>/dev/null && probe chain_id_match_signs PASS "the unaltered envelope signs" || probe chain_id_match_signs FAIL "could not sign the good envelope"

# 4. watch-only storage carries no key material
URLW=$(start_engine "$WORK/w" $RPC)
post "$URLW" api/import '{"mode":"watch","data":"'$TEST_ADDR'"}' > /dev/null
if [ -f "$WORK/w/vault.json" ]; then probe watch_no_vault FAIL "a vault file exists for a watch-only wallet"; else probe watch_no_vault PASS "no vault file"; fi
if grep -q -i -E 'phrase|"key"|sealed|junk' "$WORK/w/watch.json"; then probe watch_no_key_material FAIL "watch.json carries key material"; else probe watch_no_key_material PASS "watch.json holds the address only"; fi
R=$(post "$URLW" api/reveal '{"password":"x"}')
echo "$R" | grep -q 'watch-only' && probe watch_reveal_refused PASS "$(echo "$R" | json 'j["error"]')" || probe watch_reveal_refused FAIL "$R"

finish backup

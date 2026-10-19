#!/usr/bin/env sh
# TP-16 simulate_cmd: simulation versus execution. The quote's simulation and sentences are compared with what the
# chain did after the broadcast: the receipt's status, the balance deltas, the fee bound.
set -u
. "$(dirname "$0")/lib.sh"
start_anvil 8602
RPC=${NODE_RPC:-http://127.0.0.1:8602}
URL=$(start_engine "$WORK/engine" $RPC)
post "$URL" api/import '{"mode":"seed","data":"'"$TEST_PHRASE"'","password":"correct horse battery"}' > /dev/null
sleep 6

wei() { cast balance --rpc-url $RPC "$1"; }
B_TO=$(wei $OTHER_ADDR)

# 1. a payment expected to succeed
Q=$(post "$URL" api/send/quote '{"to":"'$OTHER_ADDR'","amount":"1.25"}')
echo "$Q" | json 'j["simulation"]["ok"]' | grep -q True && probe sim_success_predicted PASS "$(echo "$Q" | json 'j["simulation"]["text"]')" || probe sim_success_predicted FAIL "$Q"
QID=$(echo "$Q" | json 'j["quote_id"]')
FEEMAX=$(echo "$Q" | json 'j["fee_max_wei"]')
H=$(post "$URL" api/send '{"quote_id":"'$QID'"}' | json 'j["hash"]')
sleep 3
STATUS=$(cast receipt --rpc-url $RPC "$H" status 2>/dev/null)
[ "$STATUS" = 1 ] || [ "$STATUS" = true ] && probe sim_matches_execution PASS "receipt status 1 for $H" || probe sim_matches_execution FAIL "status $STATUS"
A_TO=$(wei $OTHER_ADDR)
DELTA=$(python3 -c "print(($A_TO) - ($B_TO))")
[ "$DELTA" = 1250000000000000000 ] && probe sim_value_delta PASS "the payee received exactly 1.25 IGN" || probe sim_value_delta FAIL "delta $DELTA"
# the fee paid comes from the receipt (gas used times the effective gas price), not from the sender's balance: on a
# dev node the test account is also the sealer and earns while the probe runs
GAS_USED=$(cast receipt --rpc-url $RPC "$H" gasUsed 2>/dev/null); EGP=$(cast receipt --rpc-url $RPC "$H" effectiveGasPrice 2>/dev/null)
SPENT=$(python3 -c "print(int('$GAS_USED' or 0) * int('$EGP' or 0))")
python3 -c "import sys; sys.exit(0 if 0 < $SPENT <= int('$FEEMAX') else 1)" && probe sim_fee_within_bound PASS "fee paid $SPENT wei ($GAS_USED gas at $EGP), bound $FEEMAX wei" || probe sim_fee_within_bound FAIL "fee paid $SPENT wei ($GAS_USED gas at $EGP), bound $FEEMAX wei"

# 2. a payment the simulation refuses: more than the balance
Q=$(post "$URL" api/send/quote '{"to":"'$OTHER_ADDR'","amount":"1000000000000000000000000000000000000000000000"}')
echo "$Q" | json 'j["simulation"]["ok"]' | grep -q False && probe sim_failure_predicted PASS "$(echo "$Q" | json 'j["simulation"]["text"]')" || probe sim_failure_predicted FAIL "$Q"
echo "$Q" | json 'j["enough"]' | grep -q False && probe sim_balance_check PASS "the quote says the balance does not cover it" || probe sim_balance_check FAIL "$Q"

# 3. the settlement words after inclusion are read from the chain, never 'final'
V=$(curl -s "${URL}api/tx/$H" | json 'j["view"]["word"] + " | " + j["view"]["why"]')
echo "$V" | grep -q -E '^(included|settled)' && ! echo "$V" | grep -q -i final && probe settlement_words PASS "$V" || probe settlement_words FAIL "$V"

finish simulate

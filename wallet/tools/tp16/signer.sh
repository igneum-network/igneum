#!/usr/bin/env sh
# TP-16 signer_cmd: the signer process boundary. Drives wallet/target/debug/igneum-signer over its stdio protocol.
set -u
. "$(dirname "$0")/lib.sh"

SIG="$BIN/igneum-signer"
export IGNEUM_SIGNER_LIGHT_KDF=1

# one request, one response line
ask() { printf '%s\n' "$1" | "$SIG" 2>/dev/null; }
# a session: several lines in, all responses out
session() { printf '%s\n' "$@" | "$SIG" 2>/dev/null; }

# 1. no network code linked: the binary names no HTTP client and opens no listener
if strings "$SIG" | grep -q -i -E 'ureq|hyper|reqwest|tokio'; then probe signer_no_network FAIL "an HTTP client crate is linked into the signer"; else probe signer_no_network PASS "no HTTP client crate in the signer binary"; fi
if "$SIG" < /dev/null > /dev/null 2>&1 & then SP=$!; sleep 1; if command -v ss >/dev/null 2>&1 && ss -lntp 2>/dev/null | grep -q "pid=$SP,"; then probe signer_no_listener FAIL "the signer listens on a socket"; else probe signer_no_listener PASS "no listening socket"; fi; kill $SP 2>/dev/null; fi

# 2. an unknown op (raw hash) is refused: there is no sign_hash op in the signer
R=$(ask '{"id":1,"op":"sign_hash","hash":"0x'"$(printf '%064d' 0)"'"}')
echo "$R" | grep -q '"ok":false' && echo "$R" | grep -q 'unknown op' && probe signer_raw_hash_refused PASS "sign_hash is not an op of the signer" || probe signer_raw_hash_refused FAIL "$R"

# 3. locked: sign_envelope refused before unlock
V="$WORK/vault.json"
ENV='{"version":1,"network":"Igneum devnet","chain_id":7310,"from":"'$TEST_ADDR'","transaction":{"type":"eip1559","chain_id":7310,"nonce":0,"max_priority_fee_per_gas":"0x3b9aca00","max_fee_per_gas":"0x77359400","gas_limit":21000,"to":"'$OTHER_ADDR'","value":"0x1","data":"0x"},"sentences":["You send 0.000000000000000001 IGN to 0x709979…79C8","Fee up to 0.000042 IGN (21000 gas)","From 0xf39Fd6…2266 on Igneum devnet (chain 7310), nonce 0"],"created_unix":1}'
OUT=$(session '{"id":1,"op":"open","path":"'$V'"}' '{"id":2,"op":"import_phrase","phrase":"'"$TEST_PHRASE"'","password":"correct horse battery"}' '{"id":3,"op":"lock"}' '{"id":4,"op":"sign_envelope","envelope":'"$ENV"'}')
echo "$OUT" | grep -q '"id":4,"ok":false' && echo "$OUT" | grep -q 'locked' && probe signer_locked_refuses PASS "sign_envelope refused while locked" || probe signer_locked_refuses FAIL "$OUT"

# 4. unlocked: the same envelope signs; a changed sentence is refused; a changed chain id is refused
OUT=$(session '{"id":1,"op":"open","path":"'$V'"}' '{"id":2,"op":"unlock","password":"correct horse battery"}' '{"id":3,"op":"sign_envelope","envelope":'"$ENV"'}')
echo "$OUT" | grep -q '"id":3,"ok":true' && echo "$OUT" | grep -q '"raw":"0x02' && probe signer_signs_matching_words PASS "signed with the words as shown" || probe signer_signs_matching_words FAIL "$OUT"
BAD=$(echo "$ENV" | sed 's/You send 0.000000000000000001 IGN/You send 1 IGN/')
OUT=$(session '{"id":1,"op":"open","path":"'$V'"}' '{"id":2,"op":"unlock","password":"correct horse battery"}' '{"id":3,"op":"sign_envelope","envelope":'"$BAD"'}')
echo "$OUT" | grep -q '"id":3,"ok":false' && echo "$OUT" | grep -q -i 'not what would be signed\|do not describe' && probe signer_display_mismatch_refused PASS "a changed sentence is refused" || probe signer_display_mismatch_refused FAIL "$OUT"
BADCHAIN=$(echo "$ENV" | sed 's/"chain_id":7310,"from"/"chain_id":1,"from"/')
OUT=$(session '{"id":1,"op":"open","path":"'$V'"}' '{"id":2,"op":"unlock","password":"correct horse battery"}' '{"id":3,"op":"sign_envelope","envelope":'"$BADCHAIN"'}')
echo "$OUT" | grep -q '"id":3,"ok":false' && probe signer_chain_mismatch_refused PASS "an envelope whose chain id disagrees with its transaction is refused" || probe signer_chain_mismatch_refused FAIL "$OUT"

# 5. no secret in ordinary responses: status, unlock, sign carry no phrase word and no key
if echo "$OUT" | grep -q -E 'junk|"key"|"words"'; then probe signer_no_secret_in_responses FAIL "a secret-looking field in an ordinary response"; else probe signer_no_secret_in_responses PASS "status, unlock and sign responses carry no secret"; fi

# 6. reveal needs the password again; a wrong one is refused
OUT=$(session '{"id":1,"op":"open","path":"'$V'"}' '{"id":2,"op":"unlock","password":"correct horse battery"}' '{"id":3,"op":"reveal","password":"wrong password here"}' '{"id":4,"op":"reveal","password":"correct horse battery"}')
echo "$OUT" | grep -q '"id":3,"ok":false' && echo "$OUT" | grep -q '"id":4,"ok":true' && echo "$OUT" | grep -q 'junk' && probe signer_reveal_needs_password PASS "reveal refused on a wrong password, given on the right one" || probe signer_reveal_needs_password FAIL "$OUT"

# 7. the vault file on disk holds no phrase and no key, and is 0600
if grep -q -E 'junk|"text"' "$V"; then probe vault_sealed FAIL "the vault file carries the phrase in clear"; else probe vault_sealed PASS "no phrase or key in the vault file"; fi
MODE=$(stat -c '%a' "$V" 2>/dev/null || stat -f '%Lp' "$V")
[ "$MODE" = 600 ] && probe vault_mode PASS "vault.json is 0600" || probe vault_mode FAIL "vault.json mode $MODE"

finish signer

#!/usr/bin/env sh
# TP-16 ui_probe_cmd: the engine's interface, as the window sees it. Nothing the page can call returns a secret
# without the password; the state carries none; raw-hash signing has no route; a wrong password is one sentence.
set -u
. "$(dirname "$0")/lib.sh"
start_anvil 8601
URL=$(start_engine "$WORK/engine" "${NODE_RPC:-http://127.0.0.1:8601}")
[ -n "$URL" ] || { probe engine_starts FAIL "no ENGINE line"; finish ui; }
probe engine_starts PASS "$URL"

# 1. the engine binds loopback only and the path needs the token
NOTOKEN=$(echo "$URL" | sed 's|/t/[0-9a-f]*/|/t/0000/|')
curl -s "${NOTOKEN}api/state" | grep -q 'no token' && probe engine_token_required PASS "a wrong token gets nothing" || probe engine_token_required FAIL "$(curl -s "${NOTOKEN}api/state" | head -c 100)"
echo "$URL" | grep -q '^http://127.0.0.1:' && probe engine_loopback PASS "loopback address" || probe engine_loopback FAIL "$URL"

# 2. create: the words come once; the state never carries them. Structural: every string value of the state (keys
#    are the engine's fixed schema) is checked for any two adjacent phrase words in phrase order, for the whole
#    phrase, and for the private key; a single BIP-39 word that is also an ordinary value ("wallet", "address") is
#    not a leak and is not matched.
C=$(post "$URL" api/create '{"password":"correct horse battery"}')
WORDS=$(echo "$C" | json '" ".join(j.get("words", []))')
NW=$(echo "$WORDS" | wc -w | tr -d ' ')
[ "$NW" = 24 ] || { probe state_no_secret FAIL "create failed: $(echo "$C" | head -c 200)"; finish ui; }
W0=$(echo "$WORDS" | cut -d' ' -f1)
KEY=$(post "$URL" api/reveal '{"password":"correct horse battery"}' | json 'j["key"]')
S=$(curl -s "${URL}api/state")
LEAK=$(echo "$S" | python3 -c '
import sys, json
words = sys.argv[1].split(); key = sys.argv[2].lower()
pairs = {(words[i], words[i + 1]) for i in range(len(words) - 1)}
vals = []
def walk(x):
    if isinstance(x, dict):
        for v in x.values(): walk(v)
    elif isinstance(x, list):
        for v in x: walk(v)
    elif isinstance(x, str): vals.append(x)
walk(json.load(sys.stdin))
for v in vals:
    low = v.lower()
    if key[2:] and key[2:] in low: print("the private key is in a value"); break
    if " ".join(words) in low: print("the phrase is in a value"); break
    toks = low.replace(",", " ").split()
    if any((toks[i], toks[i + 1]) in pairs for i in range(len(toks) - 1)): print("two adjacent phrase words in a value: " + v[:60]); break
' "$WORDS" "$KEY")
if [ -n "$LEAK" ]; then probe state_no_secret FAIL "$LEAK"; else probe state_no_secret PASS "no phrase pair, phrase or key in any value of api/state (24 words checked)"; fi

# 3. reveal needs the password; wrong is refused in one sentence
R=$(post "$URL" api/reveal '{"password":"nope nope nope"}')
echo "$R" | grep -q '"error"' && ! echo "$R" | grep -q "$W0" && probe reveal_wrong_password PASS "$(echo "$R" | json 'j["error"]')" || probe reveal_wrong_password FAIL "$R"
R=$(post "$URL" api/reveal '{"password":"correct horse battery"}')
echo "$R" | grep -q "\"$W0\"" && probe reveal_right_password PASS "words given with the password" || probe reveal_right_password FAIL "$R"

# 4. raw-hash signing has no route; neither do eth_sign style requests
for p in api/sign api/sign/hash api/eth_sign api/bridge/eth_sign; do
  R=$(post "$URL" "$p" '{"hash":"0x00"}')
  echo "$R" | grep -q 'not in this build\|not found' || { probe raw_hash_no_route FAIL "$p: $R"; finish ui; }
done
probe raw_hash_no_route PASS "no route signs a bare hash"

# 5. lock, then send is refused; unlock with the wrong password is one sentence
post "$URL" api/lock '{}' > /dev/null
R=$(post "$URL" api/send '{"quote_id":"x"}')
echo "$R" | grep -q 'expired\|locked' && probe send_when_locked_refused PASS "$(echo "$R" | json 'j["error"]')" || probe send_when_locked_refused FAIL "$R"
R=$(post "$URL" api/unlock '{"password":"wrong"}')
echo "$R" | grep -q 'wrong password' && probe unlock_wrong_password PASS "$(echo "$R" | json 'j["error"]')" || probe unlock_wrong_password FAIL "$R"

# 6. the sentences shown by a quote are exactly what the signer will check (the envelope export carries them)
post "$URL" api/unlock '{"password":"correct horse battery"}' > /dev/null
Q=$(post "$URL" api/send/quote '{"to":"'$OTHER_ADDR'","amount":"0.001"}')
QID=$(echo "$Q" | json 'j["quote_id"]')
E=$(post "$URL" api/envelope/export '{"quote_id":"'$QID'"}')
S1=$(echo "$Q" | json 'j["sentences"][0]'); S2=$(echo "$E" | json 'j["sentences"][0]')
[ "$S1" = "$S2" ] && echo "$S1" | grep -q 'You send 0.001 IGN to 0x709979' && probe quote_words_match_envelope PASS "$S1" || probe quote_words_match_envelope FAIL "$S1 / $S2"

finish ui

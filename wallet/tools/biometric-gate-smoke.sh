#!/usr/bin/env sh
# The biometric gate end to end without a device (OLD-BASE.md step 3, third piece): the engine started with a wallet
# against a node (NODE_RPC), its HOST line read as the window host would; then, as the "host" and the "page" in
# turn: the host-only routes refused without the token and answered with it; the host's status report; a send quote
# carrying its challenge; the send refused before the host confirms and after a wrong binding; the host reading the
# reason, confirming; the send taking the nonce once (a second use refused); a reveal challenge confirmed and the
# words returned from the unlocked key without a password; enrolment parking a password the signer checked (a wrong
# one refused), the host taking it once with the token, the sealed file written by this script as the host would,
# "enrolled" read back in the state; remove putting the file away. Run from wallet/ after `cargo build`. Prints
# ROW BIO-GATE PASS|FAIL. The engine is stopped by its pid after its command line is read.
set -u
cd "$(dirname "$0")/.."
BIN=target/debug
RPC=${NODE_RPC:-http://127.0.0.1:8545}
WORK=$(mktemp -d)
echo "work dir $WORK"
IGNEUM_SIGNER_LIGHT_KDF=1 $BIN/igneum-wallet-engine --data-dir "$WORK/engine" --rpc $RPC --signer $BIN/igneum-signer --ui app/ui --bridge-port 0 > "$WORK/engine.out" 2> "$WORK/engine.err" &
echo $! > "$WORK/engine.pid"
stop_engine() { p=$(cat "$WORK/engine.pid" 2>/dev/null) || return 0; case "$(ps -p "$p" -o command= 2>/dev/null)" in *igneum-wallet-engine*) kill "$p" 2>/dev/null || true ;; esac; }
trap stop_engine EXIT
sleep 3
URL=$(grep '^ENGINE' "$WORK/engine.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])')
HOST=$(grep '^HOST' "$WORK/engine.out" | head -1 | sed 's/^HOST //')
export URL HOST WORK
python3 - <<'PY'
import json, os, sys, urllib.request
URL, HOST, WORK = os.environ["URL"], json.loads(os.environ["HOST"]), os.environ["WORK"]
tok, biofile = HOST["token"], HOST["biometric_file"]
def call(path, body=None, host=False, method=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(URL + path, data=data, method=method or ("POST" if data is not None else "GET"))
    req.add_header("Content-Type", "application/json")
    if host: req.add_header("X-Igneum-Host", tok)
    try:
        with urllib.request.urlopen(req) as r: return r.status, json.loads(r.read())
    except urllib.error.HTTPError as e:
        try: return e.code, json.loads(e.read())
        except Exception: return e.code, {}
why = []
def need(cond, msg):
    if not cond: why.append(msg)
# a wallet from the test seed (the first anvil account, funded on the dev node)
s, r = call("api/import", {"mode": "seed", "data": "test test test test test test test test test test test junk", "password": "correct horse battery"})
need(s == 200, "import failed: %s" % r)
# host-only routes: refused without the token, answered with it
s, r = call("api/biometric/status", {"available": True, "kind": "touchid", "message": ""})
need(s != 200, "status without the host token was answered: %s %s" % (s, r))
s, r = call("api/biometric/status", {"available": True, "kind": "touchid", "message": ""}, host=True)
need(s == 200 and r.get("ok"), "status with the token failed: %s %s" % (s, r))
s, st = call("api/state")
need(st["biometric"]["available"] and st["biometric"]["kind"] == "touchid" and not st["biometric"]["enrolled"], "the state's biometric block is wrong: %s" % st.get("biometric"))
need(st["biometric_what"] == "Touch ID", "the what is %s" % st.get("biometric_what"))
# enrolment: a wrong password refused; the right one parked; the host takes it once with the token; the sealed file
s, r = call("api/biometric/enrol/begin", {"password": "wrong password here"})
need(s != 200, "enrolment began on a wrong password: %s" % r)
s, r = call("api/biometric/enrol/begin", {"password": "correct horse battery"})
need(s == 200 and r.get("token"), "enrolment did not begin: %s" % r)
etok = r.get("token", "")
s, r = call("api/biometric/enrol/take", {"token": etok})
need(s != 200, "the parked password was given without the host token")
s, r = call("api/biometric/enrol/take", {"token": "0000"}, host=True)
need(s != 200, "a wrong enrolment token was answered")
s, r = call("api/biometric/enrol/take", {"token": etok}, host=True)
need(s == 200 and r.get("secret") == "correct horse battery", "the host did not get the parked password: %s" % r)
s, r = call("api/biometric/enrol/take", {"token": etok}, host=True)
need(s != 200, "the parked password was given twice")
open(biofile, "w").write(json.dumps({"sealed_by": "the smoke as the host would", "kind": "touchid"}))
s, r = call("api/biometric/enrolled", {"kind": "touchid"}, host=True)
need(s == 200, "enrolled was refused: %s" % r)
s, st = call("api/state")
need(st["biometric"]["enrolled"], "the state does not read enrolled after the file was written")
# a send: the quote carries its challenge; nothing is sent before the host confirms, or on a wrong binding
s, q = call("api/send/quote", {"to": "0x70997970C51812dc3A010C7d01b50e0d17dc79C8", "amount": "0.001"})
need(s == 200 and q.get("confirm_needed") is True and len(q.get("confirm_nonce", "")) == 32 and q.get("confirm_reason", "").startswith("Send 0.001 IGN to 0x7099"), "the quote's challenge is wrong: %s" % {k: q.get(k) for k in ("confirm_needed", "confirm_nonce", "confirm_reason")})
nonce = q["confirm_nonce"]
s, r = call("api/send", {"quote_id": q["quote_id"]})
need(s != 200 and "confirm with Touch ID first" in json.dumps(r), "a send without a nonce was not refused with the words: %s" % r)
s, r = call("api/send", {"quote_id": q["quote_id"], "nonce": nonce})
need(s != 200 and "did not confirm" in json.dumps(r), "a send on an unconfirmed nonce was not refused: %s" % r)
s, r = call("api/biometric/challenge?nonce=" + nonce, host=True)
need(s == 200 and r.get("purpose") == "send" and r.get("reason", "").startswith("Send 0.001 IGN"), "the host could not read the reason: %s" % r)
s, r = call("api/biometric/confirm", {"nonce": nonce})
need(s != 200, "a confirm without the host token was answered")
s, r = call("api/biometric/confirm", {"nonce": nonce}, host=True)
need(s == 200, "the host's confirm failed: %s" % r)
# a second quote's send with the first quote's nonce: a mismatch (the binding is the quote's)
s, q2 = call("api/send/quote", {"to": "0x70997970C51812dc3A010C7d01b50e0d17dc79C8", "amount": "0.002"})
s, r = call("api/send", {"quote_id": q2["quote_id"], "nonce": nonce})
need(s != 200 and "something else" in json.dumps(r), "a nonce for another quote was taken: %s" % r)
s, r = call("api/send", {"quote_id": q["quote_id"], "nonce": nonce})
need(s == 200 and r.get("hash", "").startswith("0x"), "the confirmed send failed: %s" % r)
s, r = call("api/send", {"quote_id": q["quote_id"], "nonce": nonce})
need(s != 200, "the same quote was sent twice")
# the reveal through a confirmed challenge: the words without a password
s, r = call("api/biometric/challenge", {"purpose": "reveal"})
need(s == 200 and r.get("reason") == "Show your recovery words", "the reveal challenge is wrong: %s" % r)
rn = r.get("nonce", "")
s, r = call("api/reveal", {"nonce": rn})
need(s != 200, "a reveal on an unconfirmed challenge was answered")
s, r = call("api/biometric/confirm", {"nonce": rn}, host=True)
need(s == 200, "the reveal confirm failed: %s" % r)
s, r = call("api/reveal", {"nonce": rn})
need(s == 200 and r.get("words", [None])[0] == "test" and len(r.get("words", [])) == 12 and r.get("key", "").startswith("0x"), "the host-confirmed reveal gave no words: %s" % {k: (r.get(k) if k != "key" else "...") for k in ("words", "key")})
s, r = call("api/reveal", {"nonce": rn})
need(s != 200, "the reveal challenge was used twice")
s, r = call("api/reveal", {"nonce": "ffff"})
need(s != 200, "an unknown reveal nonce was answered")
# turn off: the sealed file goes
s, r = call("api/biometric/remove", {})
need(s == 200 and not os.path.exists(biofile), "remove left the sealed file")
s, st = call("api/state")
need(not st["biometric"]["enrolled"], "the state still reads enrolled after remove")
if why:
    print("ROW BIO-GATE FAIL " + "; ".join(why)); sys.exit(1)
print("ROW BIO-GATE PASS the HOST line read; the host-only routes refused without the token and answered with it; enrolment parked only a checked password and gave it once to the host; the sealed file's presence made the wallet enrolled; a quote carried its challenge; the send was refused without a nonce, on an unconfirmed nonce and on another quote's nonce, went once on the confirmed one and never twice; the reveal through a confirmed challenge gave the 12 words and the key with no password, once; remove put the sealed file away")
PY

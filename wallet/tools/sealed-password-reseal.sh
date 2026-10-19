#!/usr/bin/env bash
# sealed-password-reseal.sh <linux archive> [<rpc>]: the gate's second rule of 15 October 2026 (F-W1): a sealed password the
# host holds that no longer opens the vault (the host's Touch ID unlock refused by the engine) is re-sealed from the next
# password that does. At the engine's level, with the host's own calls (the host token from the engine's HOST line):
# a vault is made with password B; the host reports Touch ID available and a sealed file is present; the host's unlock
# with the stale password A is refused and reported; the password unlock with B succeeds and the reply carries
# reseal with a one-time token; the host takes the parked secret under that token and it equals B. Exit 0 only on PASS.
# No secret printed (the passwords are test values made here; the comparison is by equality alone).
set -u
NEW=${1:?linux archive}; RPC=${2:-https://rpc.igneum.network}
W=$(mktemp -d /tmp/reseal.XXXX); mkdir -p "$W/new" "$W/data"
tar xzf "$NEW" -C "$W/new" || exit 2
NB=$(dirname "$(find "$W/new" -name igneum-wallet-engine | head -1)")
echo "engine: $("$NB/igneum-wallet-engine" --version)"
A="stale-$(date +%s)-$RANDOM-test-value"; B="right-$(date +%s)-$RANDOM-test-value"
"$NB/igneum-wallet-engine" --data-dir "$W/data" --rpc "$RPC" --signer "$NB/igneum-signer" --ui "$NB/ui" --bridge-port 26941 > "$W/e.out" 2> "$W/e.err" &
EP=$!; for i in $(seq 1 60); do grep -q "^HOST" "$W/e.out" 2>/dev/null && break; sleep 0.5; done
U=$(grep '^ENGINE' "$W/e.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"].rstrip("/"))')
HT=$(grep '^HOST' "$W/e.out" | head -1 | sed 's/^HOST //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')
BF=$(grep '^HOST' "$W/e.out" | head -1 | sed 's/^HOST //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["biometric_file"])')
api() { curl -s -m 180 -X POST "$U/$1" -H 'Content-Type: application/json' -d "$2"; }
hapi() { curl -s -m 60 -X POST "$U/$1" -H 'Content-Type: application/json' -H "X-Igneum-Host: $HT" -d "$2"; }
echo "create: $(api api/create "{\"password\":\"$B\"}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print("ok" if d.get("address") else d)' | cut -c1-80)"
api api/lock '{}' > /dev/null
# the host's enrolment stands in by a sealed file at the path the engine named (the host seals it on a real machine)
printf '{"sealed_by":"the test, a stand-in for the host"}\n' > "$BF"
echo "host status: $(hapi api/biometric/status '{"available":true,"kind":"touchid","message":""}' | cut -c1-40)"
echo "enrolled read: $(curl -s -m 20 "$U/api/state" | python3 -c 'import sys,json; d=json.load(sys.stdin); print((d.get("biometric") or {}).get("enrolled"))')"
# the host's Touch ID unlock posts the stale password and is refused; the host reports it
R=$(api api/unlock "{\"password\":\"$A\"}"); echo "stale unlock: $(echo "$R" | cut -c1-90)"
echo "host report: $(hapi api/biometric/report '{"op":"unlock","ok":false,"code":"engine","message":"refused"}' | cut -c1-40)"
# the password unlock with the right one: the reply asks for the re-seal
R=$(api api/unlock "{\"password\":\"$B\"}")
RESEAL=$(echo "$R" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("reseal"), bool(d.get("token")))')
echo "right unlock: reseal and token: $RESEAL"
TOKEN=$(echo "$R" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("token") or "")')
# the host takes the parked secret under the token: it equals the right password
TAKEN=$(hapi api/biometric/enrol/take "{\"token\":\"$TOKEN\"}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("secret") or "")')
[ "$TAKEN" = "$B" ] && EQ=yes || EQ=no
echo "the parked secret equals the right password: $EQ"
api api/quit '{}' > /dev/null; for i in $(seq 1 20); do kill -0 $EP 2>/dev/null || break; sleep 0.5; done
rm -rf "$W"
if [ "$RESEAL" = "True True" ] && [ "$EQ" = yes ]; then echo "SEALED PASSWORD RESEAL: PASS"; exit 0; else echo "SEALED PASSWORD RESEAL: FAIL"; exit 1; fi

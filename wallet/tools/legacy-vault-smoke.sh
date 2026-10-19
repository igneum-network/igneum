#!/usr/bin/env sh
# The previous wallet's vault opened and migrated by the signer (OLD-BASE.md step 3): a file in the old format is
# written by a Python replay of the old seal (Argon2id over the password with the file's parameters, XChaCha20-Poly1305
# over the JSON secret, no associated data; the parameters light so the row is quick), then the signer is driven on
# its stdio: open reads it as legacy with the address in clear; unlock on a wrong password is refused and the file is
# untouched; unlock on the password migrates (the new file written and read back, the old file removed once the new seal opened: G7, the address the
# same); a fresh open reads the new file as this format and unlock opens it; sign_message works after. Run from
# wallet/ after `cargo build`; needs python3 with the argon2-cffi and pycryptodome packages on the box (the venv at
# builds/product-b/py under the box root, made once by the wallet team), or VENV naming another. Prints ROW LEGACY-VAULT PASS|FAIL.
set -u
cd "$(dirname "$0")/.."
BIN=target/debug
VENV=${VENV:-${IGNEUM_BOX_ROOT:-/srv}/builds/product-b/py}
PY="$VENV/bin/python3"; [ -x "$PY" ] || PY=python3
WORK=$(mktemp -d)
echo "work dir $WORK"
ADDR=$("$PY" tools/legacy-vault-make.py "$WORK/vault.json" "correct horse battery") || { echo "ROW LEGACY-VAULT FAIL could not write the old file"; exit 1; }
echo "old file written for $ADDR"
REQ="$WORK/req.jsonl"
printf '%s\n' \
  "{\"id\":1,\"op\":\"open\",\"path\":\"$WORK/vault.json\"}" \
  '{"id":2,"op":"unlock","password":"wrong password here"}' \
  '{"id":3,"op":"status"}' \
  '{"id":4,"op":"unlock","password":"correct horse battery"}' \
  '{"id":5,"op":"sign_message","message":"0x68656c6c6f"}' \
  '{"id":6,"op":"lock"}' \
  "{\"id\":7,\"op\":\"open\",\"path\":\"$WORK/vault.json\"}" \
  '{"id":8,"op":"unlock","password":"correct horse battery"}' \
  '{"id":9,"op":"status"}' > "$REQ"
IGNEUM_SIGNER_LIGHT_KDF=1 "$BIN/igneum-signer" < "$REQ" > "$WORK/resp.jsonl" 2> "$WORK/signer.err"
"$PY" - "$WORK" "$ADDR" <<'PY'
import json, sys, os
w, addr = sys.argv[1], sys.argv[2].lower()
r = {}
for l in open(os.path.join(w, "resp.jsonl")):
    if l.strip():
        o = json.loads(l); r[o["id"]] = o
why = []
st1 = r[1].get("result", {})
if not (r[1]["ok"] and st1.get("legacy") is True and str(st1.get("address", "")).lower() == addr and st1.get("has_vault") and not st1.get("unlocked")):
    why.append("open did not read the old file as legacy with its address: %s" % json.dumps(r[1])[:200])
if r[2]["ok"]:
    why.append("a wrong password unlocked")
# (the files are read after the whole session, when the migration has removed the old file: that the wrong password
# changed nothing is read from status 3 below, which still holds the file as legacy)
st3 = r[3].get("result", {})
if not (st3.get("legacy") is True and not st3.get("unlocked")):
    why.append("after the wrong password the file is no longer held as legacy: %s" % json.dumps(r[3])[:200])
st4 = r[4].get("result", {})
if not (r[4]["ok"] and st4.get("migrated_from_legacy") is True and st4.get("unlocked") and st4.get("legacy") is False and str(st4.get("address", "")).lower() == addr and st4.get("kind") == "phrase" and st4.get("backed_up") is True):
    why.append("the migration's status is wrong: %s" % json.dumps(r[4])[:300])
if os.path.exists(os.path.join(w, "vault.json.old")):
    why.append("the old file (the weaker seal) was kept as vault.json.old after the new seal opened (G7)")
new = json.load(open(os.path.join(w, "vault.json")))
if "sealed" not in new or "ciphertext" in new or new.get("public", {}).get("kind") != "phrase":
    why.append("the new file is not in this format: %s" % list(new)[:8])
sig = r[5].get("result", {}).get("signature", "")
if not (r[5]["ok"] and len(sig) == 132):
    why.append("sign_message after the migration failed: %s" % json.dumps(r[5])[:200])
st7 = r[7].get("result", {})
if not (r[7]["ok"] and st7.get("legacy") is False and st7.get("has_vault")):
    why.append("the fresh open did not read the new file as this format: %s" % json.dumps(r[7])[:200])
st9 = r[9].get("result", {})
if not (r[8]["ok"] and st9.get("unlocked") and str(st9.get("address", "")).lower() == addr):
    why.append("the new file did not unlock: %s" % json.dumps(r[8])[:200])
if why:
    print("ROW LEGACY-VAULT FAIL " + "; ".join(why)); sys.exit(1)
print("ROW LEGACY-VAULT PASS the old file read as legacy with its address in clear; a wrong password refused with the files untouched; the password migrated it (the phrase re-sealed in this format, backed_up carried, the old file removed once the new seal opened, the same address); sign_message after; the new file opens as this format and unlocks")
PY

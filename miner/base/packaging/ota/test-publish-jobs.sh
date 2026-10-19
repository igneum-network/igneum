#!/usr/bin/env bash
# Mac-side test of the jobs publisher's signed envelope (5 October 2026, the 13:19:41Z refusal on the second Windows machine):
#
#   packaging/ota/test-publish-jobs.sh
#
# What it proves, in a temporary folder with --dest (nothing deployed, the downloads folder untouched): one `add`
# writes igneum-jobs.json, its .sig and igneum-jobs.signed.json; the envelope's inner text IS the plain file byte for
# byte and its sig IS the plain signature; the signer reads the envelope back; a second `add` (a new publish) gives
# a new envelope; an envelope made by hand from the FIRST file and the SECOND signature (the stale pair an edge can
# serve across a deploy) is REFUSED by the signer with the words the app logs; a tampered inner byte is refused;
# `sign` rewrites all three consistently; a `run` script that fails a class check (a lost quote in an inline bash
# body, a body the check cannot read, a bash script that does not parse, a root prover run without the socket cleanup,
# a fetched kit used before a presence check)
# is REFUSED before anything is signed and the envelope is left as it was; and the real OTA key is the key the app
# embeds.
#
# A check is trusted only once it has fired on a known-good and a known-bad case (standing rule, 4 October 2026), so
# every negative case here must FAIL for the run to pass. Needs $IGNEUM_SECRETS_DIR/ota-signing-key (the publisher's own
# precondition) and the signer from this tree.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SIGNER="${IGNEUM_OTA_SIGN:-$ROOT/app/igneum-app/target/release/igneum-ota-sign}"
PUB="$HOME/.config/igneum/ota-signing-key.pub"
[ -x "$SIGNER" ] || { echo "no $SIGNER: build it first (cd app/igneum-app && nice -n 19 cargo build --release -j 4 --bin igneum-ota-sign)" >&2; exit 1; }
[ -f "$PUB" ] || { echo "no $PUB" >&2; exit 1; }

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
umask 077
pass=0; fail=0
ok() { pass=$((pass + 1)); echo "  ok   $1"; }
bad() { fail=$((fail + 1)); echo "  FAIL $1"; }
expect_ok() { local what="$1"; shift; if "$@" > "$T/out" 2>&1; then ok "$what"; else bad "$what: $(tail -1 "$T/out")"; fi; }
expect_fail() { local what="$1"; shift; if "$@" > "$T/out" 2>&1; then bad "$what: accepted, must refuse"; else ok "$what: refused ($(tail -1 "$T/out" | cut -c1-110))"; fi; }

D="$T/folder"
printf 'Write-Output "hello"\n' > "$T/s.ps1"
PUBLISH="$HERE/publish-jobs.sh"

echo "== one publish writes the pair and the envelope"
expect_ok "add --dest" "$PUBLISH" add --kind run --target 1ccfe586 --script "$T/s.ps1" --id test-one --dest "$D" --base-url https://example.invalid/dl/t
for f in igneum-jobs.json igneum-jobs.json.sig igneum-jobs.signed.json; do
  if [ -s "$D/$f" ]; then ok "$f written"; else bad "$f missing"; fi
done
expect_ok "the plain pair verifies" "$SIGNER" verify-jobs "$PUB" "$D/igneum-jobs.json" "$D/igneum-jobs.json.sig"
expect_ok "the envelope verifies" "$SIGNER" verify-signed-jobs "$PUB" "$D/igneum-jobs.signed.json"
python3 - "$D" > "$T/inner.txt" <<'PY'
import json, sys
d = sys.argv[1]
e = json.load(open(d + "/igneum-jobs.signed.json"))
plain = open(d + "/igneum-jobs.json", "rb").read()
sig = open(d + "/igneum-jobs.json.sig").read().strip()
print("format", e.get("format"))
print("inner-identical", e.get("file", "").encode() == plain)
print("sig-identical", e.get("sig") == sig)
print("one-line", b"\n" not in open(d + "/igneum-jobs.signed.json", "rb").read().rstrip(b"\n"))
PY
grep -q "^format igneum-jobs-signed-1$" "$T/inner.txt" && ok "envelope format igneum-jobs-signed-1" || bad "envelope format: $(grep ^format "$T/inner.txt")"
grep -q "^inner-identical True$" "$T/inner.txt" && ok "the envelope's file text is the plain file, byte for byte" || bad "the envelope's inner text differs from the plain file"
grep -q "^sig-identical True$" "$T/inner.txt" && ok "the envelope's sig is the plain signature" || bad "the envelope's sig differs from the plain signature"
grep -q "^one-line True$" "$T/inner.txt" && ok "the envelope is one line" || bad "the envelope spans lines"
cp "$D/igneum-jobs.json" "$T/first.json"; cp "$D/igneum-jobs.json.sig" "$T/first.sig"; cp "$D/igneum-jobs.signed.json" "$T/first.signed"

echo "== a second publish: new file, new signature, new envelope"
sleep 1
expect_ok "add a second job" "$PUBLISH" add --kind run --target 1ccfe586 --script "$T/s.ps1" --id test-two --dest "$D" --base-url https://example.invalid/dl/t
if ! cmp -s "$T/first.json" "$D/igneum-jobs.json" && ! cmp -s "$T/first.sig" "$D/igneum-jobs.json.sig" && ! cmp -s "$T/first.signed" "$D/igneum-jobs.signed.json"; then ok "all three files changed"; else bad "a file did not change on the second publish"; fi
expect_ok "the second envelope verifies" "$SIGNER" verify-signed-jobs "$PUB" "$D/igneum-jobs.signed.json"

echo "== the stale pair: the first file with the second signature (what two requests across a deploy can return)"
expect_fail "the plain pair, mixed" "$SIGNER" verify-jobs "$PUB" "$T/first.json" "$D/igneum-jobs.json.sig"
expect_fail "the signer refuses to wrap the mixed pair" "$SIGNER" envelope-jobs "$PUB" "$T/first.json" "$D/igneum-jobs.json.sig"
python3 - "$T/first.json" "$D/igneum-jobs.json.sig" "$T/mixed.signed" <<'PY'
import json, sys
f, s, out = sys.argv[1:4]
open(out, "w").write(json.dumps({"file": open(f).read(), "format": "igneum-jobs-signed-1", "sig": open(s).read().strip()}, sort_keys=True, separators=(",", ":")))
PY
expect_fail "a hand-made envelope of the mixed pair" "$SIGNER" verify-signed-jobs "$PUB" "$T/mixed.signed"
grep -q "jobs file signature does not verify" "$T/out" && ok "refused with the words the app logs" || bad "another reason: $(tail -1 "$T/out")"
python3 - "$D/igneum-jobs.signed.json" "$T/tampered.signed" <<'PY'
import json, sys
e = json.load(open(sys.argv[1])); e["file"] = e["file"].replace("test-two", "test-tw0")
open(sys.argv[2], "w").write(json.dumps(e, sort_keys=True, separators=(",", ":")))
PY
expect_fail "a tampered inner byte" "$SIGNER" verify-signed-jobs "$PUB" "$T/tampered.signed"
sed 's/igneum-jobs-signed-1/igneum-jobs-signed-9/' "$D/igneum-jobs.signed.json" > "$T/format.signed"
expect_fail "another format" "$SIGNER" verify-signed-jobs "$PUB" "$T/format.signed"

echo "== sign rewrites all three"
expect_ok "sign --dest" "$PUBLISH" sign --dest "$D" --base-url https://example.invalid/dl/t
expect_ok "the re-signed envelope verifies" "$SIGNER" verify-signed-jobs "$PUB" "$D/igneum-jobs.signed.json"
python3 - "$D" > "$T/inner2.txt" <<'PY'
import json, sys
d = sys.argv[1]
e = json.load(open(d + "/igneum-jobs.signed.json"))
print("inner-identical", e.get("file", "").encode() == open(d + "/igneum-jobs.json", "rb").read())
print("sig-identical", e.get("sig") == open(d + "/igneum-jobs.json.sig").read().strip())
PY
grep -q "^inner-identical True$" "$T/inner2.txt" && grep -q "^sig-identical True$" "$T/inner2.txt" && ok "after sign: the envelope still holds the plain file and its signature" || bad "after sign: the envelope and the pair differ"
expect_ok "list reads the folder" "$PUBLISH" list --dest "$D"

echo "== the class checks refuse a bad script before anything is signed (5 October 2026: a job never passes CI first)"
cp "$D/igneum-jobs.signed.json" "$T/before.signed"
printf '& wsl.exe -d Ubuntu-24.04 -- bash -c '"'"'echo "started; ls /opt/igneum'"'"' 2>&1\n' > "$T/lost-quote.ps1"
expect_fail "a PowerShell script with a lost quote in its bash body" "$PUBLISH" add --kind run --target 1ccfe586 --script "$T/lost-quote.ps1" --id test-lost-quote --dest "$D" --base-url https://example.invalid/dl/t
grep -q "unexpected EOF while looking for matching" "$T/out" && ok "refused with the bash -n error" || bad "another reason: $(tail -1 "$T/out")"
printf '$cmd = (Get-Content body.txt) -join "; "\n& wsl.exe -- bash -c $cmd\n' > "$T/unreadable.ps1"
expect_fail "a PowerShell script whose bash body the check cannot read" "$PUBLISH" add --kind run --target 1ccfe586 --script "$T/unreadable.ps1" --id test-unreadable --dest "$D" --base-url https://example.invalid/dl/t
grep -q "unextractable body" "$T/out" && ok "refused as unextractable, not skipped" || bad "another reason: $(tail -1 "$T/out")"
printf 'echo "started; ls /opt/igneum\n' > "$T/lost-quote.sh"
expect_fail "a bash script with a lost quote" "$PUBLISH" add --kind run --target 1ccfe586 --script "$T/lost-quote.sh" --id test-lost-quote-sh --dest "$D" --base-url https://example.invalid/dl/t
printf '& wsl.exe -d Ubuntu-24.04 -u root -- bash /mnt/c/prove.sh\n# igneum-prove-host --mode shard --shard 0\n' > "$T/root-prover.ps1"
expect_fail "a root prover script without the socket cleanup" "$PUBLISH" add --kind run --target 1ccfe586 --script "$T/root-prover.ps1" --id test-root-socket --dest "$D" --base-url https://example.invalid/dl/t
grep -q "prover-socket" "$T/out" && ok "refused by the socket check" || bad "another reason: $(tail -1 "$T/out")"
printf '$jobs = Split-Path $env:IGNEUM_JOB_DIR\n$exe = Join-Path (Join-Path $jobs "fetch-kit-1") "worker.exe"\n& $exe --list\n' > "$T/kit-unchecked.ps1"
expect_fail "a run script that uses a fetched kit before a presence check" "$PUBLISH" add --kind run --target 1ccfe586 --script "$T/kit-unchecked.ps1" --id test-kit-unchecked --dest "$D" --base-url https://example.invalid/dl/t
grep -q "kit path used before a presence check" "$T/out" && ok "refused by the kit-path check" || bad "another reason: $(tail -1 "$T/out")"
cmp -s "$T/before.signed" "$D/igneum-jobs.signed.json" && ok "a refused publish leaves the envelope untouched" || bad "a refused publish changed the envelope"

echo "== the key"
EMB="$("$SIGNER" embedded | sed -n 1p)"
[ "$EMB" = "$(tr -d '[:space:]' < "$PUB")" ] && ok "the embedded key is the Mac's OTA public key" || bad "the embedded key is not $PUB"

echo
echo "$pass passed, $fail failed"
[ "$fail" = 0 ]

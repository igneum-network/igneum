#!/usr/bin/env bash
# Mac-side test of the signed payload-inputs chain (review round 4, R4.5.2, ledger G13), run before any push-inputs:
#
#   packaging/windows/test-inputs-signing.sh
#
# What it proves, with a throwaway key made for the run: the manifest writer (inputs-manifest.sh) produces what the
# signer signs and the verifier accepts; the verifier then REFUSES a changed zip byte, a changed manifest byte, a
# changed unpacked file, an unlisted file in the folder, a missing file, a wrong pinned commit, a signature by another
# key, and the app's embedded key refuses the throwaway key. If $IGNEUM_SECRETS_DIR/ota-signing-key exists it also signs
# the test manifest with the real key and verifies it with `embedded`, which proves the key the Mac signs with is the
# key the runner trusts. Nothing is uploaded, deployed or written outside a temporary folder.
#
# A check is trusted only once it has been seen to fire on a known-good and a known-bad case (standing rule, 4 October
# 2026), so every negative case here must FAIL for the run to pass.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SIGNER="${IGNEUM_OTA_SIGN:-$ROOT/app/igneum-app/target/release/igneum-ota-sign}"
[ -x "$SIGNER" ] || { echo "no $SIGNER: build it first (cd app/igneum-app && nice -n 19 cargo build --release -j 4 --bin igneum-ota-sign)" >&2; exit 1; }
# shellcheck source=packaging/windows/inputs-manifest.sh
. "$HERE/inputs-manifest.sh"

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
umask 077
pass=0; fail=0
ok() { pass=$((pass + 1)); echo "  ok   $1"; }
bad() { fail=$((fail + 1)); echo "  FAIL $1"; }
expect_ok() { local what="$1"; shift; if "$@" > "$T/out" 2>&1; then ok "$what"; else bad "$what: $(tail -1 "$T/out")"; fi; }
expect_fail() { local what="$1"; shift; if "$@" > "$T/out" 2>&1; then bad "$what: accepted, must refuse"; else ok "$what: refused ($(tail -1 "$T/out" | cut -c1-110))"; fi; }

# a stage folder shaped like push-inputs.sh's, a zip of it, the manifest, a throwaway key
mkdir -p "$T/payload-inputs"
printf 'not a real node\n' > "$T/payload-inputs/igneumd.exe"
printf 'not a real miner\n' > "$T/payload-inputs/igneum-miner.exe"
printf 'not a real worker\n' > "$T/payload-inputs/igneum-worker-cuda.exe"
printf 'dll\n' > "$T/payload-inputs/nvrtc64_120_0.dll"
(cd "$T" && zip -qr payload-inputs.zip payload-inputs)
NODE="6aa69a45364b9b30a32695e33eb66f100c9be85f"
REPO_C="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo 0000000000000000000000000000000000000000)"
write_inputs_manifest "$T/payload-inputs" "$T/payload-inputs.zip" "$NODE" "finality-fixes" "$REPO_C" "$T/payload-inputs.json"
"$SIGNER" keygen "$T/key" "$T/key.pub" > /dev/null
"$SIGNER" keygen "$T/other" "$T/other.pub" > /dev/null
printf '%s\n' "$NODE" > "$T/node-source.pin"

echo "sign and verify (throwaway key)"
expect_ok "sign-inputs writes a 128-hex signature" bash -c "\"$SIGNER\" sign-inputs \"$T/key\" \"$T/payload-inputs.json\" > \"$T/payload-inputs.json.sig\" && [ \"\$(tr -d '[:space:]' < \"$T/payload-inputs.json.sig\" | wc -c | tr -d ' ')\" = 128 ]"
expect_ok "verify-inputs: signature, zip and pin" "$SIGNER" verify-inputs "$T/key.pub" "$T/payload-inputs.json" "$T/payload-inputs.json.sig" --zip "$T/payload-inputs.zip" --node-commit "$T/node-source.pin"
expect_ok "verify-inputs: the unpacked folder" "$SIGNER" verify-inputs "$T/key.pub" "$T/payload-inputs.json" "$T/payload-inputs.json.sig" --dir "$T/payload-inputs"
expect_ok "verify-inputs: the pin as a literal" "$SIGNER" verify-inputs "$T/key.pub" "$T/payload-inputs.json" "$T/payload-inputs.json.sig" --node-commit "$NODE"

echo "what must be refused"
cp "$T/payload-inputs.zip" "$T/zip.bak"; printf 'x' >> "$T/payload-inputs.zip"
expect_fail "one byte appended to the zip" "$SIGNER" verify-inputs "$T/key.pub" "$T/payload-inputs.json" "$T/payload-inputs.json.sig" --zip "$T/payload-inputs.zip"
cp "$T/zip.bak" "$T/payload-inputs.zip"
sed 's/finality-fixes/finality-fixed/' "$T/payload-inputs.json" > "$T/tampered.json"
expect_fail "one byte changed in the manifest" "$SIGNER" verify-inputs "$T/key.pub" "$T/tampered.json" "$T/payload-inputs.json.sig"
printf 'tampered\n' > "$T/payload-inputs/igneumd.exe"
expect_fail "a changed unpacked file" "$SIGNER" verify-inputs "$T/key.pub" "$T/payload-inputs.json" "$T/payload-inputs.json.sig" --dir "$T/payload-inputs"
printf 'not a real node\n' > "$T/payload-inputs/igneumd.exe"
printf 'extra\n' > "$T/payload-inputs/extra.dll"
expect_fail "an unlisted file in the folder" "$SIGNER" verify-inputs "$T/key.pub" "$T/payload-inputs.json" "$T/payload-inputs.json.sig" --dir "$T/payload-inputs"
rm "$T/payload-inputs/extra.dll"
mv "$T/payload-inputs/nvrtc64_120_0.dll" "$T/dll.bak"
expect_fail "a missing file" "$SIGNER" verify-inputs "$T/key.pub" "$T/payload-inputs.json" "$T/payload-inputs.json.sig" --dir "$T/payload-inputs"
mv "$T/dll.bak" "$T/payload-inputs/nvrtc64_120_0.dll"
expect_fail "a wrong pinned commit" "$SIGNER" verify-inputs "$T/key.pub" "$T/payload-inputs.json" "$T/payload-inputs.json.sig" --node-commit "${NODE/6aa6/7aa6}"
expect_fail "a short pin" "$SIGNER" verify-inputs "$T/key.pub" "$T/payload-inputs.json" "$T/payload-inputs.json.sig" --node-commit "6aa69a45"
expect_fail "a signature by another key" "$SIGNER" verify-inputs "$T/other.pub" "$T/payload-inputs.json" "$T/payload-inputs.json.sig"
expect_fail "the app's embedded key against the throwaway signature" "$SIGNER" verify-inputs embedded "$T/payload-inputs.json" "$T/payload-inputs.json.sig"
sed 's/"igneum-miner.exe"/"igneum-miner.exe.bak"/' "$T/payload-inputs.json" > "$T/nominer.json"
expect_fail "sign-inputs refuses a manifest without igneum-miner.exe" "$SIGNER" sign-inputs "$T/key" "$T/nominer.json"
printf '{"format":"igneum-payload-inputs/1"}\n' > "$T/short.json"
expect_fail "sign-inputs refuses a truncated manifest" "$SIGNER" sign-inputs "$T/key" "$T/short.json"

KEY="$HOME/.config/igneum/ota-signing-key"
if [ -f "$KEY" ]; then
  echo "the real key (nothing leaves this folder)"
  expect_ok "sign with the Mac's OTA key, verify with the key compiled into the app" bash -c "\"$SIGNER\" sign-inputs \"$KEY\" \"$T/payload-inputs.json\" > \"$T/real.sig\" && \"$SIGNER\" verify-inputs embedded \"$T/payload-inputs.json\" \"$T/real.sig\" --zip \"$T/payload-inputs.zip\" --dir \"$T/payload-inputs\" --node-commit \"$T/node-source.pin\""
else
  echo "  skip the real-key case: no $KEY on this machine"
fi

echo "$pass passed, $fail failed"
[ "$fail" = 0 ]

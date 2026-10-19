#!/usr/bin/env bash
# Publishes payload-inputs.zip: the pieces of the Windows app that the GitHub build (.github/workflows/windows.yml)
# cannot make on a hosted runner, because they come from the node fork (vendor/, not in git) or from NVIDIA's
# redistributables (large, not in git). Run it on the Mac after every node or worker cross-build:
#
#   packaging/windows/push-inputs.sh [--no-deploy]
#
# What goes in (flat): igneumd.exe, igneum-miner.exe (vendor/igneum-node/target-integration/x86_64-pc-windows-gnu/
# release, IGNEUM_WIN_RELEASE overrides), the three mingw runtime DLLs, igneum-worker-cuda.exe with nvrtc64_*_0.dll,
# nvrtc-builtins64_*.dll and the licence texts (proto-cuda/nvrtc, fetch-redist.sh + build-windows.sh),
# igneum-worker-opencl.exe (proto-opencl). Beside the zip: payload-inputs.json (the signed manifest, format
# app/igneum-app/src/inputs.rs: the zip's sha256 and size, every file's sha256 and size, the node fork commit and
# branch, the repository commit, the time) and payload-inputs.json.sig, its detached Ed25519 signature made on this
# Mac with the OTA key ($IGNEUM_SECRETS_DIR/ota-signing-key, the key the apps already trust). The workflow verifies the
# signature with the public key compiled into the app BEFORE it builds anything, checks the zip and every unpacked
# file against the manifest, and checks the node commit against packaging/windows/node-source.pin in the commit it
# builds (review round 4, R4.5.2, ledger G13). This script writes the pin; commit it with the push.
# The zip, the manifest, the signature and the pin's sibling payload-inputs.sha256 (kept for older checkouts of
# the workflow) land in the downloads folder (dl/<token>/) and the folder is deployed with the Vercel CLI.
#
# Where things are read from (never in the repo): the token in $IGNEUM_SECRETS_DIR/dl-token, the downloads folder in
# $IGNEUM_SECRETS_DIR/dlsite-dir (one line, the path of the dlsite directory; IGNEUM_DLSITE overrides), the Vercel login
# in $IGNEUM_SECRETS_DIR/vercel, the signing key in $IGNEUM_SECRETS_DIR/ota-signing-key (0600) and its public half .pub.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
REL="${IGNEUM_WIN_RELEASE:-$ROOT/vendor/igneum-node/target-integration/x86_64-pc-windows-gnu/release}"
MINGW=/opt/homebrew/opt/mingw-w64/toolchain-x86_64/x86_64-w64-mingw32
NVRTC_DIR="$ROOT/proto-cuda/nvrtc"
CL_WORKER="$ROOT/proto-opencl/igneum-worker-opencl.exe"
TELEMETRY="$ROOT/proto-opencl/igneum-gpu-telemetry.exe"   # AMD power, heat, fans, clocks (5 October 2026)
TOKEN_FILE="$HOME/.config/igneum/dl-token"
DLSITE="${IGNEUM_DLSITE:-}"
[ -n "$DLSITE" ] || { [ -f "$HOME/.config/igneum/dlsite-dir" ] && DLSITE="$(tr -d '[:space:]' < "$HOME/.config/igneum/dlsite-dir")"; } || true
DEPLOY=1
[ "${1:-}" = "--no-deploy" ] && DEPLOY=0

[ -f "$TOKEN_FILE" ] || { echo "no $TOKEN_FILE (the downloads token)" >&2; exit 1; }
TOKEN="$(tr -d '[:space:]' < "$TOKEN_FILE")"
[ -n "$DLSITE" ] && [ -d "$DLSITE/dl/$TOKEN" ] || { echo "no downloads folder: set IGNEUM_DLSITE or write the dlsite path to $IGNEUM_SECRETS_DIR/dlsite-dir (it must hold dl/<token>/)" >&2; exit 1; }
[ -f "$REL/igneumd.exe" ] || { echo "no $REL/igneumd.exe; cross-compile the node first (proto-cuda/windows-node/cross-build.sh)" >&2; exit 1; }
[ -f "$REL/igneum-miner.exe" ] || { echo "no $REL/igneum-miner.exe; cross-compile the miner first" >&2; exit 1; }

STAGE="$(mktemp -d)/payload-inputs"
mkdir -p "$STAGE"
cp "$REL/igneumd.exe" "$REL/igneum-miner.exe" "$STAGE/"
# the runtime DLLs: the ones next to the exes first (a PC build job ships its own toolchain's, an earlier release), else this Mac's
# toolchain (which linked a Mac cross-build); then the gate: every symbol the exes import must be exported by the DLL
# (5 October 2026, an earlier release: GCC 13 exes with the GCC 16 DLL, "Entry Point Not Found" on both PCs)
for dll in lib/libstdc++-6.dll lib/libgcc_s_seh-1.dll bin/libwinpthread-1.dll; do
  name="$(basename "$dll")"
  if [ -f "$REL/$name" ]; then cp "$REL/$name" "$STAGE/"; echo "$name: from next to the exes ($REL)"
  elif [ -f "$MINGW/$dll" ]; then cp "$MINGW/$dll" "$STAGE/"; x86_64-w64-mingw32-strip "$STAGE/$name" 2>/dev/null || true; echo "$name: from the Mac toolchain ($MINGW)"
  else echo "note: $name not in the mingw toolchain (the node exe is linked -static, so it may not need it)"; fi
done
"$(dirname "$0")/check-runtime-dlls.sh" "$STAGE"
if [ -f "$NVRTC_DIR/igneum-worker-cuda.exe" ]; then
  cp "$NVRTC_DIR/igneum-worker-cuda.exe" "$STAGE/"
  for f in "$NVRTC_DIR"/redist/bin/nvrtc*.dll "$NVRTC_DIR"/redist/LICENSE-*.txt "$NVRTC_DIR/THIRD-PARTY.md"; do [ -f "$f" ] && cp "$f" "$STAGE/"; done
  ls "$STAGE"/nvrtc64_*_0.dll >/dev/null 2>&1 || echo "warning: igneum-worker-cuda.exe without nvrtc64_*_0.dll (run $NVRTC_DIR/fetch-redist.sh)"
else echo "warning: no $NVRTC_DIR/igneum-worker-cuda.exe (run $NVRTC_DIR/build-windows.sh); the app will build the CUDA worker on the PC"; fi
[ -f "$CL_WORKER" ] && cp "$CL_WORKER" "$STAGE/" || echo "warning: no $CL_WORKER"
[ -f "$TELEMETRY" ] && cp "$TELEMETRY" "$STAGE/" || echo "warning: no $TELEMETRY (AMD cards show no draw or temperature)"

# the Linux x86_64 prover pair for the PCs' WSL2 (6 October 2026: the CI payload had never carried it, so every PC ran a stale
# host and exporter built by setup-wsl.sh from an old package, and no PC verified peer proofs or exported segments after
# an earlier release). The pair travels flat in the zip under distinct names (inputs.rs allows plain file names only); make-payload.sh
# puts them at wsl2\bin\igneum-prove-host and igneum-prove-export, candidate 1 of the WSL lookup (src/wslhost.rs). Refused
# without them: IGNEUM_PROVE_LINUX names the folder (the box's target-remote/release or the Mac's zigbuild target-linux).
PROVE_LINUX="${IGNEUM_PROVE_LINUX:-$ROOT/proving/igneum-prove/target-linux/x86_64-unknown-linux-gnu/release}"
for b in igneum-prove-host igneum-prove-export; do
  [ -f "$PROVE_LINUX/$b" ] || { echo "no Linux $b in $PROVE_LINUX (build the pair on the store host with tools/build-remote.sh from proving/igneum-prove, or cargo zigbuild --target x86_64-unknown-linux-gnu.2.36 --features igneum-prove-host/cuda); the PCs' provers would stay stale" >&2; exit 1; }
  file "$PROVE_LINUX/$b" | grep -q "ELF 64-bit.*x86-64" || { echo "$PROVE_LINUX/$b is not a Linux x86_64 ELF" >&2; exit 1; }
  cp "$PROVE_LINUX/$b" "$STAGE/$b.linux-x86_64"
  echo "$b.linux-x86_64: $(stat -f %z "$PROVE_LINUX/$b") bytes from $PROVE_LINUX"
done

# the signer, built from the app crate (it includes src/manifest.rs and src/inputs.rs, so it signs what the runner verifies)
KEY="$HOME/.config/igneum/ota-signing-key"
PUB="$HOME/.config/igneum/ota-signing-key.pub"
SIGNER="$ROOT/app/igneum-app/target/release/igneum-ota-sign"
[ -f "$KEY" ] && [ -f "$PUB" ] || { echo "no $KEY or $PUB (the OTA signing key; packaging/ota/publish-release-manifest.sh explains keygen)" >&2; exit 1; }
if [ ! -x "$SIGNER" ]; then
  echo "building igneum-ota-sign"
  (cd "$ROOT/app/igneum-app" && nice -n 19 cargo build --release -j 4 --bin igneum-ota-sign --quiet)
fi
EMBEDDED="$("$SIGNER" embedded | sed -n 1p)"
[ "$EMBEDDED" = "$(tr -d '[:space:]' < "$PUB")" ] || { echo "the public key in app/igneum-app/src/manifest.rs ($EMBEDDED) is not $PUB; the runner would refuse this signature" >&2; exit 1; }

# the manifest: what is in the zip, from where, when. IGNEUM_NODE_SRC names the worktree the exes were built from
# (default devnet-v4); its full commit is pinned in the manifest and in packaging/windows/node-source.pin.
NODE_SRC="${IGNEUM_NODE_SRC:-$ROOT/vendor/igneum-node-v4}"
NODE_COMMIT="$(git -C "$NODE_SRC" rev-parse HEAD 2>/dev/null || true)"
[ ${#NODE_COMMIT} = 40 ] || { echo "cannot read the node source commit from $NODE_SRC (set IGNEUM_NODE_SRC to the worktree the exes were built from)" >&2; exit 1; }
NODE_BRANCH="$(git -C "$NODE_SRC" rev-parse --abbrev-ref HEAD 2>/dev/null || echo detached)"
if [ -n "$(git -C "$NODE_SRC" status --porcelain --untracked-files=no 2>/dev/null)" ]; then
  echo "warning: $NODE_SRC has uncommitted changes; the pinned commit $NODE_COMMIT does not describe these exes exactly" >&2
fi
REPO_COMMIT="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || true)"
[ ${#REPO_COMMIT} = 40 ] || { echo "cannot read the repository commit" >&2; exit 1; }

DEST="$DLSITE/dl/$TOKEN"
OUT="$DEST/payload-inputs.zip"
rm -f "$OUT"
(cd "$(dirname "$STAGE")" && zip -qr "$OUT" "payload-inputs" -x '*.DS_Store')
# shellcheck source=packaging/windows/inputs-manifest.sh
. "$HERE/inputs-manifest.sh"
write_inputs_manifest "$STAGE" "$OUT" "$NODE_COMMIT" "$NODE_BRANCH" "$REPO_COMMIT" "$DEST/payload-inputs.json"
"$SIGNER" sign-inputs "$KEY" "$DEST/payload-inputs.json" > "$DEST/payload-inputs.json.sig"
# what the runner will do, done here first: the signature, the zip and the folder against the manifest
"$SIGNER" verify-inputs "$PUB" "$DEST/payload-inputs.json" "$DEST/payload-inputs.json.sig" --zip "$OUT" --dir "$STAGE" --node-commit "$NODE_COMMIT"
shasum -a 256 "$OUT" | awk '{print $1}' > "$DEST/payload-inputs.sha256"
printf '%s\n' "$NODE_COMMIT" > "$HERE/node-source.pin"
echo "payload-inputs.zip: $(stat -f %z "$OUT") bytes, sha256 $(cat "$DEST/payload-inputs.sha256")"
cat "$DEST/payload-inputs.json"
echo "signature: $(cut -c1-16 "$DEST/payload-inputs.json.sig")... (payload-inputs.json.sig)"
echo "pinned node commit $NODE_COMMIT ($NODE_BRANCH) in packaging/windows/node-source.pin: commit it with this push, or the workflow refuses the manifest"
rm -rf "$(dirname "$STAGE")"

if [ "$DEPLOY" = 1 ]; then
  echo "deploying $DLSITE"
  (cd "$DLSITE" && npx vercel@latest --global-config "$HOME/.config/igneum/vercel" deploy --prod --yes 2>&1 | grep -v "$TOKEN" || true)
  code="$(curl -s -o /dev/null -w '%{http_code}' "https://dl.igneum.network/dl/$TOKEN/payload-inputs.json")"
  echo "https://dl.igneum.network/dl/<token>/payload-inputs.json -> HTTP $code"
  [ "$code" = 200 ] || { echo "the manifest is not reachable yet; check the deploy output" >&2; exit 1; }
else
  echo "not deployed (--no-deploy): cd $DLSITE && npx vercel@latest --global-config $IGNEUM_SECRETS_DIR/vercel deploy --prod --yes"
fi
echo "Next: the GitHub build picks it up on the next push to master (or: gh workflow run windows.yml --repo igneum-network/igneum)."

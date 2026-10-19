#!/usr/bin/env bash
# Builds the node's verifier library (verifier-ffi, a C-ABI staticlib over the pinned Miden verifier at the
# patched security setting) and links it where the node's cgo package expects it, then builds geth with the
# verifier. Run on a lab box or a builder: nothing of this runs on the Mac.
#   tools/money/stage1-prover/build-node-verifier.sh [geth output path]
set -euo pipefail
cd "$(dirname "$0")"
./prepare-patched.sh
cargo build --release -p money-verifier-ffi
LIB="${CARGO_TARGET_DIR:-target}/release/libmoney_verifier.a"
DEST=../../../node/consensus/igneum/pool/miden/lib
mkdir -p "$DEST" && cp -f "$LIB" "$DEST/libmoney_verifier.a"
echo "library $(sha256sum "$DEST/libmoney_verifier.a" | cut -c1-16) at $DEST"
if [ $# -ge 1 ]; then (cd ../../../node && go build -tags money_miden -o "$1" ./cmd/geth) && echo "geth with the verifier at $1 ($(sha256sum "$1" | cut -c1-16))"; fi

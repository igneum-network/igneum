#!/usr/bin/env bash
# The security setting of record (docs/money/STAGE1-128BIT-2026-10-18.md): Miden VM 0.36.1 proves over
# the quadratic extension of Goldilocks with ONE proof configuration (miden-air 0.36.1 src/config.rs:
# LOG_BLOWUP 3, NUM_QUERIES 27, QUERY_POW_BITS 17), 96 conjectured bits, and its 128-bit challenge
# field caps the lookup round at 108 bits for this statement whatever the query count (the join page,
# STAGE1-JOIN-2026-10-18.md section 5). This script copies five crates of the pinned release from the
# cargo registry into ./patched (never committed) and applies patches/<crate>.patch to each (the diffs
# of record, their bytes hashed on the page): the cubic extension F_p[X]/(X^3 - X - 1) as the Miden VM
# proof's challenge field (miden-field, miden-crypto, miden-air, miden-prover, miden-verifier), 38 FRI
# queries, and the lookup-boundary correction applied before the transcript cap. The workspace's
# [patch.crates-io] then builds the prover, the verifier and the node's verifier library against the
# copies. A proof made under the release's or the join's setting does not verify under this one (the
# transcript differs) and the verifier refuses it.
set -euo pipefail
cd "$(dirname "$0")"
REGBASE="$(ls -d "${CARGO_HOME:-$HOME/.cargo}"/registry/src/index.crates.io-* /opt/rust/cargo/registry/src/index.crates.io-* 2>/dev/null | head -1)"
CRATES="miden-field miden-crypto miden-air miden-prover miden-verifier"
mkdir -p patched
for c in $CRATES; do
  src="$REGBASE/$c-0.36.1"
  [ -d "$src" ] || { echo "$c 0.36.1 is not in the cargo registry (cargo fetch first)"; exit 1; }
  rm -rf "patched/$c" && cp -r "$src" "patched/$c"
  rm -f "patched/$c/.cargo-ok" "patched/$c/Cargo.toml.orig"
  patch -p1 -s -d "patched/$c" < "patches/$c.patch"
  echo "$c 0.36.1 + patches/$c.patch (sha256 $(sha256sum "patches/$c.patch" | cut -c1-16)) at patched/$c"
done
grep -n "const NUM_QUERIES: usize =\|const QUERY_POW_BITS: usize =\|const LOG_BLOWUP: u8 =\|pub type CubeFelt" patched/miden-air/src/config.rs

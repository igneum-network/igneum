#!/usr/bin/env sh
# On builder seven, as one plain command: builds pow/pi's pi-claim (the claim's prover and verifier, risc0 3.0.6, the
# riscv toolchain rzup installed for the build user) in the wallet team's tree and copies the binary to the wallet team's work
# dir. The first build pulls the risc0 crates and the guest (minutes); later builds are seconds. Prints the image id
# the built guest carries, which must equal PI_IMAGE_ID of PI-NOTES.md, else the build is not the chain's guest.
set -eu
cd "$(dirname "$0")/../../pow/pi"
D=${AUTH_DIR:-${IGNEUM_BOX_ROOT:-/srv}/builds/product-b/work/authority}
mkdir -p "$D"
export PATH="$HOME/.cargo/bin:/opt/rust/cargo/bin:$PATH"
cargo build --release --bin pi-claim --bin pi-vectors 2>&1 | grep -E '^(error|warning: unused|Finished)|error\[' | head -20
cp target/release/pi-claim "$D/pi-claim"
echo "image id $(target/release/pi-vectors image-id)"
"$D/pi-claim" 2>&1 | head -1
date -u +%H:%M:%SZ

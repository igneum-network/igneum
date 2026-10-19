#!/usr/bin/env sh
# On the box, as one plain command: build the wallet workspace (debug) and run the core and authority tests.
# Prints the last lines of each step and the time; a failing step stops the script.
set -eu
cd "$(dirname "$0")/.."
echo "== build $(git rev-parse --short HEAD)"
cargo build 2>&1 | grep -E '^(error|warning: unused|Finished)|error\[' | head -20
echo "== core tests"
cargo test -p igneum-wallet-core 2>&1 | grep -E 'test result|FAILED|panicked|^error' | head -8
echo "== authority tests"
cargo test -p igneum-wallet-authority 2>&1 | grep -E 'test result|FAILED|panicked|^error' | head -8
echo "== clippy warnings"
cargo clippy --all-targets 2>&1 | grep -A 6 '^warning' | head -40 || true
date -u +%H:%M:%SZ

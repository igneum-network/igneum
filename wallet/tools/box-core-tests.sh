#!/usr/bin/env sh
# On the box, as one plain command: the core tests and clippy for the wallet workspace.
set -eu
cd "$(dirname "$0")/.."
cargo test -p igneum-wallet-core 2>&1 | grep -E 'test result|FAILED|panicked|^error' | head -8
cargo clippy --all-targets 2>&1 | grep -c '^warning' || true
date -u +%H:%M:%SZ

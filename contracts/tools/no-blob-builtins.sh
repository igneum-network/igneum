#!/usr/bin/env sh
# The gate for decision D-B01: contracts compile for cancun (OpenZeppelin 5.7 needs MCOPY), but Igneum EVM-1 has no
# blob opcodes, so the two blob builtins never appear in anything deployed. Run from contracts/: tools/no-blob-builtins.sh
set -eu
cd "$(dirname "$0")/.."
if grep -rnE 'blobhash|blobbasefee|BLOBHASH|BLOBBASEFEE' src/; then
  echo "blob builtins found under src/: Igneum EVM-1 has no blob opcodes (D-B01)" >&2
  exit 1
fi
echo "no blob builtins under src/"

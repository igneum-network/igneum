#!/usr/bin/env bash
# Money stage 1, the 128-bit cut: the measurement of record (docs/money/STAGE1-128BIT-2026-10-18.md).
# Run from this directory on the lab box after ./prepare-patched.sh && cargo build --release, with the
# join's bench witnesses (the two-in two-out spend at depth 32, seed 1; the shield) as the arguments:
#   ./measure-128bit.sh <spend-witness.json> <shield-witness.json> [out-dir]
# Every figure on the page is the median of five runs from these commands; the proof bytes are the
# median over the five proofs each run writes.
set -euo pipefail
cd "$(dirname "$0")"
B=target/release
OUT=${3:-out-128bit}; mkdir -p $OUT
for st in spend shield; do
  if [ $st = spend ]; then WIT=$1; else WIT=$2; fi
  cp "$WIT" $OUT/w-$st.json
  # proving on all cores, eight cores, one core (each: five runs, the median printed)
  RAYON_NUM_THREADS=96 $B/money-prove $OUT/w-$st.json $OUT/p-$st.bin $OUT/c-$st.json 5 | tee $OUT/prove-$st-96t.log
  RAYON_NUM_THREADS=8 taskset -c 0-7 $B/money-prove $OUT/w-$st.json /dev/null /dev/null 5 | tee $OUT/prove-$st-8t.log
  RAYON_NUM_THREADS=1 taskset -c 0 $B/money-prove $OUT/w-$st.json /dev/null /dev/null 5 | tee $OUT/prove-$st-1t.log
  # peak memory of one proving run on eight cores
  RAYON_NUM_THREADS=8 taskset -c 0-7 /usr/bin/time -v $B/money-prove $OUT/w-$st.json /dev/null /dev/null 1 2>&1 | grep -E "Maximum resident|median" | tee $OUT/mem-$st-8t.log
  # verification on one core, 101 runs in one process, five processes; the security report once
  for i in 1 2 3 4 5; do taskset -c 0 $B/money-verify $OUT/p-$st.bin $OUT/c-$st.json 101 | head -1; done | tee $OUT/verify-$st.log
  taskset -c 0 $B/money-verify $OUT/p-$st.bin $OUT/c-$st.json 1 | tee $OUT/security-$st.log
done
ls -l $B/money-verify $B/money-prove $OUT/*.bin
sha256sum $B/money-verify $B/money-prove $OUT/w-spend.json $OUT/w-shield.json patches/*.patch

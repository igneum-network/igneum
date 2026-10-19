#!/usr/bin/env bash
# Money stage 1, cut 1: the measurement of record. Run from this directory on the lab box
# after `cargo build --release`. Writes run logs under ./out. Every figure in the record page
# is the median of five runs from these commands.
set -euo pipefail
cd "$(dirname "$0")"
B=target/release
OUT=out; mkdir -p $OUT
$B/money-witness 32 1 $OUT/w32.json
$B/money-witness 20 1 $OUT/w20.json
for depth in 32 20; do
  for hasher in blake3 poseidon2; do
    # proving on all cores, eight cores, one core (each: five runs, the median printed)
    RAYON_NUM_THREADS=96 $B/money-prove $OUT/w$depth.json $hasher $OUT/p-$depth-$hasher.bin $OUT/c-$depth-$hasher.json 5 | tee $OUT/prove-$depth-$hasher-96t.log
    RAYON_NUM_THREADS=8 taskset -c 0-7 $B/money-prove $OUT/w$depth.json $hasher /dev/null /dev/null 5 | tee $OUT/prove-$depth-$hasher-8t.log
    RAYON_NUM_THREADS=1 taskset -c 0 $B/money-prove $OUT/w$depth.json $hasher /dev/null /dev/null 5 | tee $OUT/prove-$depth-$hasher-1t.log
    # peak memory of one proving run on eight cores
    RAYON_NUM_THREADS=8 taskset -c 0-7 /usr/bin/time -v $B/money-prove $OUT/w$depth.json $hasher /dev/null /dev/null 1 2>&1 | grep -E "Maximum resident|median" | tee $OUT/mem-$depth-$hasher-8t.log
    # verification on one core, 101 runs in one process, five processes
    for i in 1 2 3 4 5; do taskset -c 0 $B/money-verify $OUT/p-$depth-$hasher.bin $OUT/c-$depth-$hasher.json 101; done | tee $OUT/verify-$depth-$hasher.log
  done
done
ls -l $B/money-verify $B/money-prove $OUT/*.bin
sha256sum $B/money-verify $B/money-prove $B/money-witness

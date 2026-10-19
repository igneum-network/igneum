#!/usr/bin/env bash
# The operand collector end to end on a box (never on the Mac): the two vector sets from ref.py, exactness with
# iverilog on both, the VCDs at each platform's clock, synthesis on both libraries (flow/synth.sh), the per-bit
# activities (the annotation kit), placement with OpenROAD (flow/place.tcl, buffered) at the utilisation that fits
# the pins, the propagated and the ANNOTATED power reads (flow/power.tcl), the timed ASAP7 form, the figures into
# out/SUMMARY.txt. Requirements: iverilog, python3, ORFS's yosys and openroad on the path (YOSYS= and OPENROAD=
# name them), an ORFS checkout for the platforms; about 40 minutes on 32 cores.
#   flow/run.sh <part dir> <orfs dir> [asap7_period_ps=1000] [nangate45_period_ps=2000]
set -euo pipefail
D="${1:?part dir}"; ORFS="${2:?orfs dir}"; PA7="${3:-1000}"; PNG="${4:-2000}"
OPENROAD="${OPENROAD:-openroad}"
OUT="$D/out"; mkdir -p "$OUT"
cd "$D"
echo "run at UTC $(date -u '+%F %T'); asap7 period $PA7 ps; nangate45 period $PNG ps" | tee "$OUT/SUMMARY.txt"
# 1. the vectors and exactness
python3 ref.py gen random 1 vectors/random.hex 1024 | tee -a "$OUT/SUMMARY.txt"
python3 ref.py gen grammar 2 vectors/grammar.hex 1024 | tee -a "$OUT/SUMMARY.txt"
( cd vectors && sha256sum random.hex grammar.hex > SHA256SUMS.new && diff SHA256SUMS SHA256SUMS.new && echo "vectors match SHA256SUMS" | tee -a "$OUT/SUMMARY.txt" && rm SHA256SUMS.new )
flow/vcds.sh "$D" "$PA7" "$PNG"
# 2. synthesis on both libraries
flow/synth.sh "$D" "$ORFS" | tee -a "$OUT/SUMMARY.txt"
# 3. the activities per bit from the VCDs at the simulation's own clock (the annotation kit), the four in parallel
for v in grammar_asap7 random_asap7 grammar_nangate45 random_nangate45; do
  python3 flow/kit/vcd_activity.py "$OUT/$v.vcd" "$OUT/activity_$v.tsv" --clk clk > "$OUT/activity_$v.log" 2>&1 &
done
# 4. placement and the power reads on both platforms in parallel, the utilisation lowered until the pins fit
flow/place.sh "$D" "$ORFS" asap7 "$PA7" &
flow/place.sh "$D" "$ORFS" nangate45 "$PNG" &
wait
# 5. the timed ASAP7 form (repair_timing on the buffered placement) and its grammar read
PLAT=asap7 ORFS="$ORFS" OUT="$OUT/asap7" LIBDIR="$OUT/lib" "$OPENROAD" -exit flow/timed.tcl > "$OUT/asap7/timed.log" 2>&1 || tail -3 "$OUT/asap7/timed.log"
grep -E "^wns|^tns|Design area" "$OUT/asap7/timed.log" | sed 's/^/asap7 timed: /' | tee -a "$OUT/SUMMARY.txt"
flow/timed_read.sh "$D" "$ORFS" grammar "$PA7"
# 6. the figures per word delivered or routed
python3 flow/figures.py "$OUT" "$PA7" "$PNG" | tee -a "$OUT/SUMMARY.txt"
echo "finished at UTC $(date -u '+%F %T')" | tee -a "$OUT/SUMMARY.txt"

#!/usr/bin/env bash
# The annotated power read of the timed ASAP7 form (out/asap7/oc.timed.odb from flow/timed.tcl: repair_timing on the
# buffered placement) on one set: the propagated read with the net dump, the mapping, the annotated read.
#   flow/timed_read.sh <part dir> <orfs dir> <set: grammar|random> [period_ps=1000]
set -uo pipefail
D="${1:?part dir}"; ORFS="${2:?orfs dir}"; s="${3:?set}"; period="${4:-1000}"
OPENROAD="${OPENROAD:-openroad}"
OUT="$D/out"; O="$OUT/asap7"; KIT="$D/flow/kit"
export PLAT=asap7 ORFS PERIOD=$period LIBDIR="$OUT/lib" KIT ODB="$O/oc.timed.odb"
echo "--- asap7 timed $s power reads ($(date -u +%T))" | tee -a "$OUT/SUMMARY.txt"
OUT="$O" VCD="$OUT/${s}_asap7.vcd" NETS="$O/nets_timed.tsv" ANNOTATE="" REPORT="$O/power_${s}_timed_propagated.txt" \
  "$OPENROAD" -exit "$D/flow/power.tcl" > "$O/power_${s}_timed_propagated.log" 2>&1 || tail -3 "$O/power_${s}_timed_propagated.log"
python3 "$KIT/map_activity.py" "$OUT/activity_${s}_asap7.tsv" "$O/nets_timed.tsv" "$O/annotate_timed_$s.tcl" --clock clk --strip "oc_tb.dut" \
  --report "$O/map_report_timed_$s.txt" --annotated "$O/annotated_timed_$s.tsv" | tee -a "$OUT/SUMMARY.txt"
OUT="$O" VCD="$OUT/${s}_asap7.vcd" NETS="$O/nets_timed2.tsv" ANNOTATE="$O/annotate_timed_$s.tcl" REPORT="$O/power_${s}_timed_annotated.txt" \
  "$OPENROAD" -exit "$D/flow/power.tcl" > "$O/power_${s}_timed_annotated.log" 2>&1 || tail -3 "$O/power_${s}_timed_annotated.log"
for kind in propagated annotated; do grep "^Total" "$O/power_${s}_timed_$kind.txt" | sed "s/^/asap7 timed $s $kind /" | tee -a "$OUT/SUMMARY.txt"; done
echo "--- asap7 timed $s power reads ended ($(date -u +%T))" | tee -a "$OUT/SUMMARY.txt"

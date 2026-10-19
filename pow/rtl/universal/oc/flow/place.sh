#!/usr/bin/env bash
# Placement and the two power reads on one platform: waits for the fixed netlist and the VCD activities, places at
# the utilisation given (flow/place.tcl; stepped down 30, 15, 8, 4 until the pins fit), then for each set the
# propagated read (the ports from read_vcd, the interior propagated: NOT a figure of record) and the ANNOTATED read
# (the kit: vcd_activity.py's per-bit toggles per simulation cycle mapped onto the placed netlist's nets by
# map_activity.py), appending to out/SUMMARY.txt.
#   flow/place.sh <part dir> <orfs dir> <asap7|nangate45> <period_ps>
set -uo pipefail
D="${1:?part dir}"; ORFS="${2:?orfs dir}"; plat="${3:?platform}"; period="${4:?period ps}"
OPENROAD="${OPENROAD:-openroad}"
OUT="$D/out"; O="$OUT/$plat"; KIT="$D/flow/kit"
export PLAT=$plat ORFS PERIOD=$period LIBDIR="$OUT/lib" KIT
# OUT inside openroad is the platform directory; the shell's OUT stays the part's out dir
until [ -s "$O/oc.fixed.v" ]; do sleep 15; done
echo "--- $plat placement started $(date -u +%T), netlist $(wc -l < "$O/oc.fixed.v") lines" | tee -a "$OUT/SUMMARY.txt"
placed=""
if [ "${REUSE_PLACED:-0}" = 1 ] && [ -s "$O/oc.placed.odb" ]; then placed=kept; echo "--- $plat placement reused ($(date -u +%T))" | tee -a "$OUT/SUMMARY.txt"; fi
[ -n "$placed" ] || for util in ${UTILS:-30 15 8 4}; do
  echo "--- $plat placement at utilisation $util ($(date -u +%T))" | tee -a "$OUT/SUMMARY.txt"
  if OUT="$O" UTIL=$util "$OPENROAD" -exit "$D/flow/place.tcl" > "$O/place_u$util.log" 2>&1 && [ -s "$O/oc.placed.odb" ]; then
    cp "$O/place_u$util.log" "$O/place.log"; placed=$util
    grep -E "^placed:|Design area|^wns|^tns" "$O/place.log" | tee -a "$OUT/SUMMARY.txt"
    echo "placed at utilisation $util ($(date -u +%T))" | tee -a "$OUT/SUMMARY.txt"; break
  else
    grep -i "error" "$O/place_u$util.log" | tail -3 | tee -a "$OUT/SUMMARY.txt"
  fi
done
[ -n "$placed" ] || { echo "--- $plat: no placement fitted" | tee -a "$OUT/SUMMARY.txt"; exit 1; }
for s in grammar random; do
  until [ -s "$OUT/activity_${s}_${plat}.tsv" ] && grep -q "^vcd_activity:" "$OUT/activity_${s}_${plat}.log"; do sleep 15; done
  echo "--- $plat $s power reads ($(date -u +%T)): $(grep '^vcd_activity:' "$OUT/activity_${s}_${plat}.log")" | tee -a "$OUT/SUMMARY.txt"
  OUT="$O" VCD="$OUT/${s}_${plat}.vcd" NETS="$O/nets.tsv" ANNOTATE="" REPORT="$O/power_${s}_propagated.txt" \
    "$OPENROAD" -exit "$D/flow/power.tcl" > "$O/power_${s}_propagated.log" 2>&1 || tail -3 "$O/power_${s}_propagated.log"
  python3 "$KIT/map_activity.py" "$OUT/activity_${s}_${plat}.tsv" "$O/nets.tsv" "$O/annotate_$s.tcl" --clock clk --strip "oc_tb.dut" \
    --report "$O/map_report_$s.txt" --annotated "$O/annotated_$s.tsv" | tee -a "$OUT/SUMMARY.txt"
  OUT="$O" VCD="$OUT/${s}_${plat}.vcd" NETS="$O/nets2.tsv" ANNOTATE="$O/annotate_$s.tcl" REPORT="$O/power_${s}_annotated.txt" \
    "$OPENROAD" -exit "$D/flow/power.tcl" > "$O/power_${s}_annotated.log" 2>&1 || tail -3 "$O/power_${s}_annotated.log"
  for kind in propagated annotated; do
    echo "$plat $s $kind: $(grep -A1 'annotat' "$O/power_${s}_$kind.log" | grep -v '^--' | tr '\n' ' ' | cut -c1-300)" | tee -a "$OUT/SUMMARY.txt"
    grep "^Total" "$O/power_${s}_$kind.txt" | sed "s/^/$plat $s $kind /" | tee -a "$OUT/SUMMARY.txt"
  done
done
echo "--- $plat placement and power ended $(date -u +%T)" | tee -a "$OUT/SUMMARY.txt"

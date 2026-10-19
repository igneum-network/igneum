#!/usr/bin/env bash
# The annotated power read of the placed gather_table (the annotation kit of pow/rtl/universal/power-annotate, the
# brief's addendum 2): the VCD's per-bit activity per simulation cycle, the propagated read (ports from read_vcd,
# the interior propagated: reported beside, never the figure of record), the name mapping, the annotated read, and
# the pJ per 32-lane gather and per octet gather from each. Runs after flow/run.sh on a box.
#   flow/annotate.sh <part-dir> <orfs-dir> <asap7|nangate45> <kit-dir>
set -euo pipefail
D="${1:?part dir}"; ORFS="${2:?orfs}"; PLAT="${3:?platform}"; KIT="${4:?annotation kit dir}"
OUT="${OUT:-$D/out/$PLAT}"
ODB="$OUT/orfs/results/$PLAT/gather_table/base/3_place.odb"
[ -f "$ODB" ] || { echo "no placed odb at $ODB"; exit 1; }
log() { echo "$*" | tee -a "$OUT/SUMMARY.txt"; }
log "annotated read at UTC $(date -u '+%F %T'); platform $PLAT"
# 1. the activity per simulation cycle from the testbench's streamed run (the dump covers that run only)
[ -f "$OUT/activity.tsv" ] || python3 "$KIT/vcd_activity.py" "$OUT/gather_table_tb.vcd" "$OUT/activity.tsv" --clk clk | tee -a "$OUT/SUMMARY.txt"
head -5 "$OUT/activity.tsv" | tee -a "$OUT/SUMMARY.txt"
fill() { sed -e "s|@ORFS@|$ORFS|g" -e "s|@PLAT@|$PLAT|g" -e "s|@ODB@|$ODB|" -e "s|@SDC@|$OUT/constraint.sdc|" -e "s|@VCD@|$OUT/gather_table_tb.vcd|" \
             -e "s|@SCOPE@|gather_table_tb/dut|" -e "s|@NETS@|$OUT/nets.tsv|" -e "s|@NETSTCL@|$D/flow/nets.tcl|" -e "s|@ANNOTATE@|$1|" -e "s|@REPORT@|$2|" "$D/flow/power.tcl.in"; }
# 2. the propagated run (writes the nets dump)
fill "" "$OUT/power_propagated.txt" > "$OUT/power_propagated.tcl"
( cd "$OUT" && "$ORFS/tools/install/OpenROAD/bin/openroad" -no_init -exit power_propagated.tcl 2>&1 | grep -v "^OpenROAD\|^Features\|^This program\|^Components" | tee propagated.log | tail -14 )
# 3. the mapping and the annotated run
python3 "$KIT/map_activity.py" "$OUT/activity.tsv" "$OUT/nets.tsv" "$OUT/annotate.tcl" --clock clk --strip gather_table_tb.dut --report "$OUT/map_report.txt" --annotated "$OUT/annotated.tsv" | tee -a "$OUT/SUMMARY.txt"
fill "$OUT/annotate.tcl" "$OUT/power_annotated.txt" > "$OUT/power_annotated.tcl"
( cd "$OUT" && "$ORFS/tools/install/OpenROAD/bin/openroad" -no_init -exit power_annotated.tcl 2>&1 | grep -v "^OpenROAD\|^Features\|^This program\|^Components" | tee annotated.log | tail -14 )
# 4. the figure: the non-macro power at the SDC clock over the streamed run's cycles, per request
CYC=$(grep "^streamed:" "$OUT/SUMMARY.txt" | head -1 | awk '{print $6}')
REQ=$(grep "^streamed:" "$OUT/SUMMARY.txt" | head -1 | awk '{print $2}')
PERIOD=$(grep -o 'period [0-9.]*' "$OUT/constraint.sdc" | awk '{print $2}')
for kind in propagated annotated; do
  python3 - "$kind" "$OUT/power_$kind.txt" "$PERIOD" "$PLAT" "$CYC" "$REQ" <<'EOP' | tee -a "$OUT/SUMMARY.txt"
import sys, re
kind, path, period, plat, cyc, req = sys.argv[1], sys.argv[2], float(sys.argv[3]), sys.argv[4], int(sys.argv[5]), int(sys.argv[6])
tsec = period * (1e-12 if plat == "asap7" else 1e-9)
rows = {}
for line in open(path):
    m = re.match(r"^(Sequential|Combinational|Clock|Macro|Pad|Total)\s+([0-9.e+-]+)\s+([0-9.e+-]+)\s+([0-9.e+-]+)\s+([0-9.e+-]+)", line)
    if m: rows[m.group(1)] = [float(m.group(i)) for i in range(2, 6)]
tot = rows.get("Total", [0, 0, 0, 0])[3]; mac = rows.get("Macro", [0, 0, 0, 0])[3]
logic = tot - mac
e_req = logic * tsec * cyc / req
print("%s: total %.4g W, macro %.4g W (the kit's placeholder, set aside), logic %.4g W at %.4g GHz over %d cycles for %d requests: %.2f pJ per 32-lane gather, %.2f pJ per octet gather (the logic alone)" % (kind, tot, mac, logic, 1.0 / tsec / 1e9, cyc, req, e_req * 1e12, e_req * 1e12 / 4))
EOP
done
{ echo "--- propagated ---"; grep -A8 "^Group" "$OUT/power_propagated.txt" | head -10; echo "--- annotated ---"; grep -A8 "^Group" "$OUT/power_annotated.txt" | head -10
  echo "--- annotation ---"; grep -i "annotated\|unannotated" "$OUT/annotated.log" | head -8; cat "$OUT/map_report.txt" 2>/dev/null | head -12; } | tee -a "$OUT/SUMMARY.txt"
# the summary carries no box path: the part's directory and the ORFS tree are written relative
sed -i -e "s|$D/||g" -e "s|$ORFS|ORFS|g" -e "s|$HOME/[^ ]*|(box path)|g" -e "s|/${BOX_ROOT_USER:-root}/[^ ]*|(box path)|g" "$OUT/SUMMARY.txt"
log "annotated read finished UTC $(date -u '+%F %T')"

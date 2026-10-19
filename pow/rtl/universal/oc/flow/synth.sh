#!/usr/bin/env bash
# Synthesis of the operand collector on ASAP7 (RVT, the typical corner, NLDM: the AO, INVBUF, OA, SEQ and SIMPLE
# libraries) and nangate45 (typical) with the distribution's yosys; the two libraries in parallel. Writes
# out/<platform>/oc.stat.txt, oc.netlist.v and oc.fixed.v (the OpenSTA-readable netlist).
#   flow/synth.sh <part dir> <orfs dir>
set -euo pipefail
D="${1:?part dir}"; ORFS="${2:?orfs dir}"
YOSYS="${YOSYS:-yosys}"
P="$ORFS/flow/platforms"
OUT="$D/out"; mkdir -p "$OUT/asap7" "$OUT/nangate45" "$OUT/lib"
for k in AO INVBUF OA SEQ SIMPLE; do
  for g in "$P"/asap7/lib/NLDM/asap7sc7p5t_${k}_RVT_TT_nldm_*.lib*; do
    [ -s "$g" ] || continue
    b=$(basename "$g" .gz)
    if [ -s "$OUT/lib/$b" ]; then continue; fi
    case "$g" in *.gz) gunzip -c "$g" > "$OUT/lib/$b" || rm -f "$OUT/lib/$b" ;; *) cp "$g" "$OUT/lib/$b" ;; esac
  done
done
find "$OUT/lib" -name "*.lib" -size 0 -delete
A7=$(ls "$OUT"/lib/asap7sc7p5t_*_RVT_TT_nldm_*.lib | tr '\n' ' ')
NG="$P/nangate45/lib/NangateOpenCellLibrary_typical.lib"
mk() {
  local plat=$1; shift; local libs="$*"; local rl="" la=""
  for l in $libs; do rl="${rl}read_liberty -lib $l\\n"; la="$la -liberty $l"; done
  sed -e "s|@READ_LIBERTY@|$rl|" -e "s|@LIBERTY_ARGS@|$la|g" -e "s|@OUT@|$OUT/$plat/oc|" "$D/flow/synth.ys.in" > "$OUT/$plat/synth.ys"
  ( cd "$D" && echo "yosys $plat started $(date -u +%T)" > "$OUT/$plat/synth.out"; /usr/bin/time -v "$YOSYS" -q -l "$OUT/$plat/yosys.log" "$OUT/$plat/synth.ys" >> "$OUT/$plat/synth.out" 2>&1; echo "yosys $plat ended $(date -u +%T) rc $?" >> "$OUT/$plat/synth.out"
    python3 "$D/flow/fixnet.py" "$plat" "$OUT/$plat/oc.netlist.v" "$OUT/$plat/oc.fixed.v" >> "$OUT/$plat/synth.out" 2>&1 ) &
}
echo "synthesis started $(date -u +%FT%TZ) with $("$YOSYS" -V 2>/dev/null | head -1)"
mk asap7 $A7
[ -s "$OUT/nangate45/oc.fixed.v" ] || mk nangate45 $NG
wait
for plat in asap7 nangate45; do echo "--- $plat"; grep -E "Chip area|Number of cells|fixnet|ended|Maximum resident|Elapsed" "$OUT/$plat/oc.stat.txt" "$OUT/$plat/synth.out" | head -8; done
echo "synthesis finished $(date -u +%FT%TZ)"

#!/usr/bin/env bash
# First synthesis numbers of part 6 with the distribution's yosys and the kits' liberties (unplaced; the placed
# flow is flow/run.sh). Three readings per run: the read path with the banks as the kit's SRAM macro (black-boxed
# from its liberty) on ASAP7 and on nangate45, the fill adapter alone, and one bank as a flop array on ASAP7 (the
# "upper bound" form's cost, times the bank count). Runs on a box, never on the Mac.
#   flow/synth.sh <orfs-platforms-dir> [out-dir]
# The ASAP7 pitfalls (the security review's recipe): the sequential liberty read beside AO, INVBUF, OA and SIMPLE; no
# enable flop (dfflegalize to $_DFF_P_ before mapping); logic mapped before the flops.
set -euo pipefail
P="${1:?platforms dir}"; OUT="${2:-out/synth}"; mkdir -p "$OUT/asap7lib"
D="$(cd "$(dirname "$0")/.." && pwd)"
for z in "$P"/asap7/lib/NLDM/asap7sc7p5t_{AO,INVBUF,OA,SIMPLE}_RVT_TT_nldm_*.lib.gz; do b="$(basename "$z" .gz)"; [ -f "$OUT/asap7lib/$b" ] || zcat "$z" > "$OUT/asap7lib/$b"; done
cp -n "$P"/asap7/lib/NLDM/asap7sc7p5t_SEQ_RVT_TT_nldm_*.lib "$OUT/asap7lib/" 2>/dev/null || true
A7=$(ls "$OUT"/asap7lib/*.lib | tr '\n' ' ')
A7SEQ=$(ls "$OUT"/asap7lib/*SEQ*.lib)
NG="$P/nangate45/lib/NangateOpenCellLibrary_typical.lib"
libs_args() { for l in "$@"; do printf -- " -liberty %s" "$l"; done; }
# $1 tag, $2 top, $3 verilog files, $4 macro liberty or "", $5 cell libs, $6 seq lib, $7 extra yosys lines
mk() {
  local tag="$1" top="$2" files="$3" mlib="$4" libs="$5" seq="$6" extra="$7"
  {
    echo "read_verilog $files"
    [ -n "$mlib" ] && echo "read_liberty -lib $mlib"
    echo "$extra"
    echo "hierarchy -check -top $top"
    echo "synth -top $top -flatten"
    echo "dfflegalize -cell \$_DFF_P_ 01"
    for l in $libs; do echo "read_liberty -lib $l"; done
    echo "abc$(libs_args $libs)"
    echo "dfflibmap -liberty $seq"
    echo "opt_clean"
    echo "tee -o $OUT/$tag.stat.txt stat -top $top$(libs_args $libs)"
    echo "write_verilog -noattr $OUT/$tag.netlist.v"
  } > "$OUT/$tag.ys"
  ( echo "start $(date -u +%FT%TZ)"; /usr/bin/time -v yosys -q -l "$OUT/$tag.yosys.log" "$OUT/$tag.ys" 2>&1 | tail -25; echo "end $(date -u +%FT%TZ)" ) > "$OUT/$tag.log" 2>&1 &
}
R="$D/rtl"
mk table_asap7 gather_table "$R/gather_table.v $R/macro/asap7/gather_bank.v" "$P/asap7/lib/NLDM/fakeram7_2048x39.lib" "$A7" "$A7SEQ" ""
mk table_ng45 gather_table "$R/gather_table.v $R/macro/nangate45/gather_bank.v" "$P/nangate45/lib/fakeram45_2048x39.lib" "$NG" "$NG" ""
mk fill_asap7 gather_fill "$R/gather_fill.v" "" "$A7" "$A7SEQ" ""
mk bank_flops_asap7 gather_bank "$R/gather_bank.v" "" "$A7" "$A7SEQ" "chparam -set AW 11 -set DW 32 gather_bank"
echo "four syntheses started $(date -u +%FT%TZ) under $OUT"

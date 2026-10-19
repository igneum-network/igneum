#!/usr/bin/env bash
# The VCDs for the power reads: both sets at each platform's clock period (the testbench's +half in ps).
#   flow/vcds.sh <part dir> [asap7_period_ps=1000] [nangate45_period_ps=2000]
set -euo pipefail
D="${1:?part dir}"; PA7="${2:-1000}"; PNG="${3:-2000}"
OUT="$D/out"; mkdir -p "$OUT"
iverilog -g2005 -o "$OUT/oc_tb" "$D/tb/oc_tb.v" "$D/rtl/oc.v"
for plat in asap7 nangate45; do
  if [ $plat = asap7 ]; then half=$((PA7 / 2)); else half=$((PNG / 2)); fi
  for s in random grammar; do
    ( cd "$D/vectors" && vvp "$OUT/oc_tb" +hex=$s.hex +vcd +half=$half | grep oc_tb: | tee -a "$OUT/SUMMARY.txt" && mv oc_tb.vcd "$OUT/${s}_${plat}.vcd" )
  done
done
ls -la "$OUT"/*.vcd

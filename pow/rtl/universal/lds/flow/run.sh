#!/usr/bin/env bash
# The placed flow of part 6 on a box (never on the Mac): the testbench's VCD at the platform's clock, ORFS
# synthesis, floorplan and placement with the kit's fakeram 2048x39 macros as the banks, then OpenSTA's power on
# the placed netlist with the VCD's port activity (the interior propagated) split by group (macro, sequential,
# combinational, clock). Every step writes under $OUT; the summary is $OUT/SUMMARY.txt.
#   flow/run.sh <part-dir> <orfs-dir> <asap7|nangate45> [period]
# period: ASAP7 in ps (1000 = the 1 GHz testbench clock, HALF 0.5), nangate45 in ns (2.0 = HALF 1.0); the SDC
# period must equal the VCD's clock or the energy per cycle is off (the security review's pitfall 5).
set -euo pipefail
D="${1:?part dir}"; ORFS="${2:?orfs}"; PLAT="${3:?platform}"
case "$PLAT" in
  asap7) PERIOD="${4:-1000}"; HALF=$(python3 -c "print($PERIOD/2000)"); LEF="$ORFS/flow/platforms/asap7/lef/fakeram7_2048x39.lef"; LIB="$ORFS/flow/platforms/asap7/lib/NLDM/fakeram7_2048x39.lib"; BANK="$D/rtl/macro/asap7/gather_bank.v"; HALO="2 2"; CHAN="5 5" ;;
  nangate45) PERIOD="${4:-2.0}"; HALF=$(python3 -c "print($PERIOD/2)"); LEF="$ORFS/flow/platforms/nangate45/lef/fakeram45_2048x39.lef"; LIB="$ORFS/flow/platforms/nangate45/lib/fakeram45_2048x39.lib"; BANK="$D/rtl/macro/nangate45/gather_bank.v"; HALO="5 5"; CHAN="20 20" ;;
  *) echo "platform asap7 or nangate45"; exit 2 ;;
esac
OUT="${OUT:-$D/out/$PLAT}"; mkdir -p "$OUT"
echo "run at UTC $(date -u '+%F %T'); platform $PLAT; period $PERIOD; testbench half period $HALF ns" | tee "$OUT/SUMMARY.txt"

# 1. the VCD at the platform's clock (the streamed run of the testbench on the behavioural banks)
iverilog -g2005 -P gather_table_tb.HALF=$HALF -o "$OUT/tb" "$D/tb/gather_table_tb.v" "$D/rtl/gather_table.v" "$D/rtl/gather_bank.v" "$D/rtl/gather_fill.v"
( cd "$OUT" && vvp tb +fill="$D/vectors/fill.hex" +ops="$D/vectors/ops.hex" +expect="$D/vectors/expect.hex" +vcd | tee -a SUMMARY.txt | tail -3 )

# 2. ORFS: synthesis, floorplan, placement (the banks as macros from the kit's LEF and liberty)
CFG="$OUT/config.mk"
cat > "$CFG" <<EOC
export DESIGN_NICKNAME = gather_table
export DESIGN_NAME = gather_table
export PLATFORM = $PLAT
export VERILOG_FILES = $D/rtl/gather_table.v $BANK
export SDC_FILE = $OUT/constraint.sdc
export ADDITIONAL_LEFS = $LEF
export ADDITIONAL_LIBS = $LIB
export CORE_UTILIZATION = 30
export PLACE_DENSITY = 0.60
export MACRO_PLACE_HALO = $HALO
export MACRO_PLACE_CHANNEL = $CHAN
export TNS_END_PERCENT = 100
EOC
sed "s/@PERIOD@/$PERIOD/" "$D/flow/constraint.sdc.in" > "$OUT/constraint.sdc"
( cd "$ORFS/flow" && make DESIGN_CONFIG="$CFG" WORK_HOME="$OUT/orfs" synth 2>&1 | tail -15 | tee -a "$OUT/SUMMARY.txt" )
( cd "$ORFS/flow" && make DESIGN_CONFIG="$CFG" WORK_HOME="$OUT/orfs" floorplan place 2>&1 | tail -15 | tee -a "$OUT/SUMMARY.txt" )
grep -h -i -E 'design area|instances|wns|tns' "$OUT"/orfs/reports/$PLAT/gather_table/base/synth_stat.txt "$OUT"/orfs/logs/$PLAT/gather_table/base/3_*.log 2>/dev/null | tail -12 | tee -a "$OUT/SUMMARY.txt" || true

# 3. the power on the placed netlist from the VCD (scope: the testbench's dut), by group
cat > "$OUT/power.tcl" <<EOT
source $ORFS/flow/platforms/$PLAT/setRC.tcl
foreach lib [glob $ORFS/flow/platforms/$PLAT/lib/*.lib* $ORFS/flow/platforms/$PLAT/lib/NLDM/*.lib*] { catch { read_liberty \$lib } }
set netlist [lindex [lsort [glob -nocomplain $OUT/orfs/results/$PLAT/gather_table/base/3_place.v $OUT/orfs/results/$PLAT/gather_table/base/1_synth.v]] 0]
puts "netlist \$netlist"
read_verilog \$netlist
link_design gather_table
read_sdc $OUT/constraint.sdc
if {[catch { read_vcd -scope gather_table_tb/dut $OUT/gather_table_tb.vcd } err]} { puts "read_vcd: \$err; falling back to a 0.5 activity"; set_power_activity -input -activity 0.5 }
report_power > $OUT/power_report.txt
puts [exec cat $OUT/power_report.txt]
EOT
( cd "$OUT" && "$ORFS/tools/install/OpenROAD/bin/openroad" -exit power.tcl 2>&1 | tail -25 | tee -a "$OUT/SUMMARY.txt" ) || true
# the summary carries no box path: the part's directory and the ORFS tree are written relative
sed -i -e "s|$D/||g" -e "s|$ORFS|ORFS|g" -e "s|$HOME/[^ ]*|(box path)|g" -e "s|/${BOX_ROOT_USER:-root}/[^ ]*|(box path)|g" "$OUT/SUMMARY.txt"
echo "finished at UTC $(date -u '+%F %T')" | tee -a "$OUT/SUMMARY.txt"

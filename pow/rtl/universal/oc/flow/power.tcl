# The power read on the placed operand collector (OpenSTA inside openroad): the placed design, its parasitics
# estimated from the placement, the clock at the VCD's own period, the ports' activity from the VCD (read_vcd), the
# net dump for the mapper, then, when ANNOTATE names an annotate.tcl, every matched interior net set from the same
# VCD's per-bit activity (the annotation kit), report_activity_annotation and the power reports.
# Environment: PLAT, ORFS, OUT, PERIOD (ps), LIBDIR, VCD, NETS (the nets.tsv to write), ANNOTATE ("" for the
# propagated read), REPORT (the power report path), KIT (the annotation kit's dir).
set plat $::env(PLAT); set orfs $::env(ORFS); set out $::env(OUT); set period $::env(PERIOD)
set P $orfs/flow/platforms/$plat
if {$plat == "asap7"} {
  foreach l [lsort [glob $::env(LIBDIR)/asap7sc7p5t_*_RVT_TT_nldm_*.lib]] { read_liberty $l }
} else {
  read_liberty $P/lib/NangateOpenCellLibrary_typical.lib
}
# ODB names another saved design (the timed form oc.timed.odb); the default is the buffered placement
if {[info exists ::env(ODB)] && $::env(ODB) ne ""} { read_db $::env(ODB) } else { read_db $out/oc.placed.odb }
read_sdc $out/oc.sdc
source $P/setRC.tcl
estimate_parasitics -placement
if {[catch { read_vcd -scope oc_tb/dut $::env(VCD) } err]} { puts "read_vcd: $err" }
set ::nets_out $::env(NETS)
source $::env(KIT)/sta/nets.tcl
set annotate $::env(ANNOTATE)
if {$annotate ne ""} { source $annotate }
report_activity_annotation
report_power > $::env(REPORT)
report_power -instances [get_cells *] > $::env(REPORT).inst
puts [exec cat $::env(REPORT)]

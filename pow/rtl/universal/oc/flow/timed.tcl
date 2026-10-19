# The timed form on ASAP7: gate sizing and buffering for the clock on the buffered placement (repair_timing), the
# achieved clock reported, the design saved as oc.timed.odb for a power read beside the buffered form.
# Environment: OUT (the asap7 out dir), LIBDIR, ORFS.
set out $::env(OUT)
foreach l [lsort [glob $::env(LIBDIR)/asap7sc7p5t_*_RVT_TT_nldm_*.lib]] { read_liberty $l }
read_db $out/oc.placed.odb
read_sdc $out/oc.sdc
source $::env(ORFS)/flow/platforms/asap7/setRC.tcl
estimate_parasitics -placement
repair_timing -setup -repair_tns 100 -setup_margin 0
detailed_placement
estimate_parasitics -placement
puts "timed:"
report_design_area
report_wns
report_tns
report_checks -path_delay max -format summary
write_db $out/oc.timed.odb
write_verilog $out/oc.timed.v

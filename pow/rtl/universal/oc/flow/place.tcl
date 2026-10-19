# Placement of the operand collector with OpenROAD (one script, no ORFS make): the platform's LEFs and libraries,
# the fixed netlist, the clock at the period given, the floorplan at the utilisation given, the platform's track
# script, the pins on two horizontal and two vertical layers at one track's spacing (the block has about 19,000
# pins: the three read buses, the gather return, the four item registers, the result and the three operand buses),
# global and detailed placement, the area and timing reports, the placed design saved for flow/power.tcl.
# Environment: PLAT (asap7|nangate45), ORFS, OUT (the platform's out dir), PERIOD (ps), UTIL (percent), LIBDIR.
set plat $::env(PLAT); set orfs $::env(ORFS); set out $::env(OUT); set period $::env(PERIOD); set util $::env(UTIL)
set P $orfs/flow/platforms/$plat
if {$plat == "asap7"} {
  read_lef $P/lef/asap7_tech_1x_201209.lef
  read_lef $P/lef/asap7sc7p5t_28_R_1x_220121a.lef
  foreach l [lsort [glob $::env(LIBDIR)/asap7sc7p5t_*_RVT_TT_nldm_*.lib]] { read_liberty $l }
  set site asap7sc7p5t; set hl {M4 M6}; set vl {M5 M7}; set tracks $P/openRoad/make_tracks.tcl; set margin 2; set tu 1.0
} else {
  read_lef $P/lef/NangateOpenCellLibrary.tech.lef
  read_lef $P/lef/NangateOpenCellLibrary.macro.mod.lef
  read_liberty $P/lib/NangateOpenCellLibrary_typical.lib
  set site FreePDK45_38x28_10R_NP_162NW_34O; set hl {metal3 metal5}; set vl {metal2 metal4}; set tracks $P/make_tracks.tcl; set margin 5; set tu 0.001
}
read_verilog $out/oc.fixed.v
link_design oc
# the period in the library's time unit (ASAP7 ps, nangate45 ns)
set T [expr $period * $tu]
create_clock -name clk -period $T [get_ports clk]
set_input_delay -clock clk [expr $T * 0.2] [delete_from_list [all_inputs] [get_ports clk]]
set_output_delay -clock clk [expr $T * 0.2] [all_outputs]
write_sdc $out/oc.sdc
initialize_floorplan -utilization $util -aspect_ratio 1 -core_space $margin -site $site
source $tracks
place_pins -hor_layers $hl -ver_layers $vl -min_distance 1 -min_distance_in_tracks
global_placement -density [expr min(0.95, $util / 100.0 + 0.25)]
source $P/setRC.tcl
estimate_parasitics -placement
# the buffered form: the select and immediate broadcasts fan out to thousands of loads; repair_design buffers them
# (the recipe's "buffered"), then the placement is legalised again and the parasitics re-estimated
repair_design
detailed_placement
estimate_parasitics -placement
puts "placed: platform $plat period_ps $period utilisation $util"
report_design_area
report_wns
report_tns
report_checks -path_delay max -format summary
write_db $out/oc.placed.odb
write_def $out/oc.placed.def
write_verilog $out/oc.placed.v

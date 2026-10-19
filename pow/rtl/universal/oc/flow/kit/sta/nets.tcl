# Dump every net of the linked (flat) netlist with its driver and load pins, and every instance's library cell, for
# map_activity.py. Run inside openroad after link_design; set ::nets_out to the output path first:
#   set nets_out out/nets.tsv ; source nets.tcl
# nets.tsv, one line per net: <net> TAB <driver kind> TAB <driver pins> TAB <load pins> (pins as inst/pin, space
# separated); driver kind "inst" (an instance output pin), "port" (a top-level input port: read_vcd annotates those),
# "none" (undriven). cells.tsv beside it: <instance> TAB <liberty cell>.
proc dump_nets {path} {
  set f [open $path w]
  set n 0
  foreach net [get_nets -hierarchical *] {
    set name [get_full_name $net]
    set drv {}
    set lds {}
    set kind none
    foreach pin [get_pins -of_objects $net] {
      if {[get_property $pin direction] eq "output"} { lappend drv [get_full_name $pin]; set kind inst } else { lappend lds [get_full_name $pin] }
    }
    if {$kind eq "none"} {
      foreach port [get_ports -of_objects $net] {
        if {[get_property $port direction] eq "input"} { lappend drv [get_full_name $port]; set kind port }
      }
    }
    puts $f "$name\t$kind\t[join $drv { }]\t[join $lds { }]"
    incr n
  }
  close $f
  set cpath [file join [file dirname $path] cells.tsv]
  set g [open $cpath w]
  set m 0
  foreach inst [get_cells -hierarchical *] {
    set cell "?"
    if {[catch { set cell [get_property $inst ref_name] }]} {
      catch { set cell [get_name [get_lib_cells -of_objects $inst]] }
    }
    puts $g "[get_full_name $inst]\t$cell"
    incr m
  }
  close $g
  puts "nets.tcl: $n nets written to $path, $m instances to $cpath"
}
if {[info exists ::nets_out]} { dump_nets $::nets_out }

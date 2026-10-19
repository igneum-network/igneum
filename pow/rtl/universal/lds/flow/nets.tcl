# The annotation kit's nets dump (pow/rtl/universal/power-annotate/sta/nets.tcl) for a design read from ORFS's ODB:
# the same two files (nets.tsv: net, driver kind, driver pins, load pins as inst/pin; cells.tsv: instance, cell)
# written from the ODB block itself (OpenSTA's get_property on an ODB-linked design cannot infer the object's
# type and sta::port_direction crashed on it), so the mapper sees the same names OpenSTA uses for
# set_power_activity -pins. Run after read_db with ::nets_out set.
proc dump_nets {path} {
  set block [ord::get_db_block]
  set f [open $path w]
  set n 0
  foreach net [$block getNets] {
    set name [$net getName]
    set drv {}
    set lds {}
    set kind none
    foreach it [$net getITerms] {
      set pn "[[$it getInst] getName]/[[$it getMTerm] getName]"
      if {[$it getIoType] eq "OUTPUT"} { lappend drv $pn; set kind inst } else { lappend lds $pn }
    }
    if {$kind eq "none"} {
      foreach bt [$net getBTerms] {
        if {[$bt getIoType] eq "INPUT"} { lappend drv [$bt getName]; set kind port }
      }
    }
    puts $f "$name\t$kind\t[join $drv { }]\t[join $lds { }]"
    incr n
  }
  close $f
  set cpath [file join [file dirname $path] cells.tsv]
  set g [open $cpath w]
  set m 0
  foreach inst [$block getInsts] {
    puts $g "[$inst getName]\t[[$inst getMaster] getName]"
    incr m
  }
  close $g
  puts "nets.tcl: $n nets written to $path, $m instances to $cpath"
}
if {[info exists ::nets_out]} { dump_nets $::nets_out }

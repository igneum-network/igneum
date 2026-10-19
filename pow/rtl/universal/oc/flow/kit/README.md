# The annotation kit, as used here

Four files of the annotation seat's kit (pow/rtl/universal/power-annotate on lead/ue-power-annotate at 31da9461e:
vcd_activity.py, map_activity.py, sta/nets.tcl, sta/power.tcl.in), copied so flow/place.sh runs from this
directory alone. One change in map_activity.py: the inverter rule's scan for an inverter's input net was a pass
over every net per matched inverter-driven net (quadratic: on this block's ASAP7 netlist, a flop-output inverter per
flop, the mapping ran past ten minutes); the nets loading each instance are indexed once, the result is the same.

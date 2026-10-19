#!/usr/bin/env python3
"""The placed figures from the power reports: for each platform and set, the annotated read (the figure of record:
every named interior net set from the VCD's own toggles per simulation cycle) and the propagated read beside it
(the ports from read_vcd, the interior propagated by OpenSTA: not a figure of record), the energy per cycle at the
VCD's clock, and the energy per word delivered or routed: the forward path's operands (3 x W per issued
instruction) plus the write path's result words (W per routed result), over the cycles the VCD covers.
Usage: figures.py <out dir> <asap7 period ps> <nangate45 period ps>"""
import re
import sys

out, pa7, png = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
summary = open(out + "/SUMMARY.txt").read()
W = 32
for plat, period in (("asap7", pa7), ("nangate45", png)):
    for s in ("grammar", "random"):
        tb = re.search(r"oc_tb: +%s.hex cases (\d+) checks \d+ issued (\d+) operands (\d+) results (\d+) mismatches (\d+) \(clock %d ps\)" % (s, period), summary)
        if not tb:
            print("%s %s: no testbench line at %d ps" % (plat, s, period)); continue
        cases, issued, operands, results, mism = (int(x) for x in tb.groups())
        act = re.search(r"vcd_activity: (\d+) bits, clock \S+, (\d+) cycles", open("%s/activity_%s_%s.log" % (out, s, plat)).read())
        cycles = int(act.group(2)) if act else cases + 4
        words = operands + results * W
        for kind in ("annotated", "propagated", "timed_annotated", "timed_propagated"):
            try:
                rep = open("%s/%s/power_%s_%s.txt" % (out, plat, s, kind)).read()
            except OSError:
                if not kind.startswith("timed"):
                    print("%s %s %s: no power report" % (plat, s, kind))
                continue
            m = re.search(r"Total\s+([\d.e+-]+)\s+([\d.e+-]+)\s+([\d.e+-]+)\s+([\d.e+-]+)", rep)
            if not m:
                print("%s %s %s: no total in the report" % (plat, s, kind)); continue
            internal, switching, leakage, total = (float(x) for x in m.groups())
            e_cycle = total * period  # W x ps = pJ
            e_total = e_cycle * cycles
            note = ""
            if kind.endswith("annotated"):
                try:
                    note = "; " + open("%s/%s/map_report_%s%s.txt" % (out, plat, "timed_" if kind.startswith("timed") else "", s)).readline().strip()
                except OSError:
                    pass
            print("%s %s %s (clock %d ps, %d cycles, issued %d of %d cases, mismatches %d): total %.4f W = %.1f pJ per cycle "
                  "(internal %.4f, switching %.4f, leakage %.5f W); %.3f pJ per word delivered or routed (%d operands + %d result words); "
                  "%.3f pJ per operand delivered with the whole block charged to the operands (the upper bound)%s"
                  % (plat, s, kind.upper(), period, cycles, issued, cases, mism, total, e_cycle, internal, switching, leakage,
                     e_total / words, operands, results * W, e_total / operands, note))

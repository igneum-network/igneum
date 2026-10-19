#!/usr/bin/env python3
"""Make a yosys netlist readable by OpenSTA: the flop-output inverters that dfflibmap leaves as `assign x = ~y;`
become inverter instances of the library (ASAP7 INVx1_ASAP7_75t_R with A and Y; nangate45 INV_X1 with A and ZN), and
the word `signed` is removed. Usage: fixnet.py <asap7|nangate45> in.v out.v"""
import re
import sys

plat, src, dst = sys.argv[1:4]
cell, pin_in, pin_out = ("INVx1_ASAP7_75t_R", "A", "Y") if plat == "asap7" else ("INV_X1", "A", "ZN")
name = r"(?:\\\S+ |[A-Za-z_][\w$]*)(?:\s*\[\d+\])?"
pat = re.compile(r"^\s*assign\s+(" + name + r")\s*=\s*~\s*(" + name + r")\s*;\s*$")
n = 0
out = []
with open(src) as f:
    for line in f:
        line = re.sub(r"\bsigned\b", "", line)
        m = pat.match(line)
        if m:
            n += 1
            out.append("  %s fixnet_inv_%d (.%s(%s), .%s(%s));\n" % (cell, n, pin_in, m.group(2), pin_out, m.group(1)))
        else:
            out.append(line)
with open(dst, "w") as f:
    f.writelines(out)
print("fixnet: %d inverters instanced, written %s" % (n, dst))

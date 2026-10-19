#!/usr/bin/env python3
"""Map the RTL VCD's per-bit activity (vcd_activity.py) onto the synthesised netlist's nets (nets.tcl) and emit the
OpenSTA script that annotates every matched net's driver pins:
  set_power_activity -pins {<driver pins>} -activity <toggles per cycle> -duty <duty> -clock <clk>
so the interior is ANNOTATED where the RTL named it (every register word, every named wire that survived synthesis)
instead of propagated from the ports.
  map_activity.py activity.tsv nets.tsv annotate.tcl [--clock clk] [--strip PREFIX] [--alias FROM=TO ...]
                  [--rewrite REGEX=REPL ...] [--inv REGEX] [--report R] [--annotated A.tsv]
Name matching, both sides normalised: backslashes and spaces removed; the VCD path (dots between scopes) against
Yosys's flattened names (the instance path with dots, e.g. oct[0].u.lane[3].u.R[5]); a vector bit k of VCD name N is
the netlist net N[k]; a scalar is N itself. --strip removes a leading scope from the VCD names (the testbench's
"tb.dut."); --alias rewrites a VCD name prefix; --rewrite applies a Python regular expression substitution to the VCD
names (mirror wires of a memory: --rewrite 'mo\\[0\\]\\.ml_\\[(\\d+)\\]\\.mr\\[(\\d+)\\]\\.r=lane[\\1].u.R[\\2]').
Two rules extend a matched net to the register it came from, since a flop's complementary output carries the same
toggles at the complementary duty: (Q/QN) an unnamed net driven by another output pin of the same flop; (inverter)
a matched net driven by an inverter (cells.tsv, --inv regex, default ^INV) whose unnamed input net a flop drives:
that net gets (activity, 1 - duty). The report counts, over the netlist: nets annotated (by name, by the two rules),
nets driven by ports (read_vcd's), named nets with no VCD activity (unmatched), unnamed interior nets (Yosys's and
abc's: PROPAGATED by OpenSTA from the annotated boundary), and over the VCD: bits that matched nothing. A matched
net whose bit never toggles is annotated with activity 0 and its duty: a held register is a fact of the activity.
--annotated writes the annotated set (net, activity, duty, driver pins) for the hand sum.
"""
import re, sys


def norm(s):
    # backslashes and spaces of escaped names dropped; OpenSTA's hierarchical divider "/" read as the VCD's "."
    return s.replace("\\", "").replace(" ", "").replace("/", ".")


def main():
    args = sys.argv[1:]
    if len(args) < 3:
        sys.exit(__doc__)
    act_path, nets_path, out_path = args[:3]
    clock = "clk"; strip = ""; aliases = []; rewrites = []; inv_re = "^INV"; report = None; annotated_path = None
    i = 3
    while i < len(args):
        if args[i] == "--clock": clock = args[i + 1]; i += 2
        elif args[i] == "--strip": strip = args[i + 1]; i += 2
        elif args[i] == "--alias": a, b = args[i + 1].split("=", 1); aliases.append((a, b)); i += 2
        elif args[i] == "--rewrite": a, b = args[i + 1].split("=", 1); rewrites.append((re.compile(a), b)); i += 2
        elif args[i] == "--inv": inv_re = args[i + 1]; i += 2
        elif args[i] == "--report": report = args[i + 1]; i += 2
        elif args[i] == "--annotated": annotated_path = args[i + 1]; i += 2
        else: sys.exit("unknown option " + args[i])
    if strip and not strip.endswith("."):
        strip += "."
    inv_pat = re.compile(inv_re)
    # the VCD side: name[bit] -> (activity, duty, toggles)
    act = {}
    header = []
    for line in open(act_path):
        if line.startswith("#"):
            header.append(line.rstrip()); continue
        name, bit, tg, a, d = line.rstrip("\n").split("\t")
        if strip and name.startswith(strip):
            name = name[len(strip):]
        for fr, to in aliases:
            if name.startswith(fr):
                name = to + name[len(fr):]
        for pat, repl in rewrites:
            name = pat.sub(repl, name)
        bit = int(bit)
        key = norm(name) + ("[%d]" % bit if bit >= 0 else "")
        act[key] = (float(a), float(d), int(tg))
    # the netlist side
    nets = {}      # net -> (kind, drivers, loads)
    order = []
    for line in open(nets_path):
        if not line.strip(): continue
        parts = line.rstrip("\n").split("\t")
        while len(parts) < 4: parts.append("")
        name, kind = parts[0], parts[1]
        drv = parts[2].split(); lds = parts[3].split()
        nets[name] = (kind, drv, lds); order.append(name)
    cells = {}
    try:
        for line in open(nets_path.rsplit("/", 1)[0] + "/cells.tsv" if "/" in nets_path else "cells.tsv"):
            t = line.rstrip("\n").split("\t")
            if len(t) == 2: cells[t[0]] = t[1]
    except OSError:
        pass
    # instance -> [(pin, net)] over output pins, and net by driver pin
    inst_outs = {}
    for name, (kind, drv, lds) in nets.items():
        if kind == "inst":
            for p in drv:
                inst, pin = p.rsplit("/", 1)
                inst_outs.setdefault(inst, []).append((pin, name))
    def unnamed(key):
        return key.startswith("$") or key.startswith("_") or "$" in key
    annot = {}     # net -> (activity, duty, drivers, how)
    n_port = n_unmatched = n_auto = n_undriven = 0
    unmatched = []
    for name in order:
        kind, drv, lds = nets[name]
        key = norm(name)
        if kind == "port":
            n_port += 1; continue
        if kind == "none" or not drv:
            n_undriven += 1; continue
        if unnamed(key):
            n_auto += 1; continue
        hit = act.get(key)
        if hit is None and key.endswith("[0]"):
            hit = act.get(key[:-3])  # a one-bit bus for a VCD scalar
        if hit is None:
            n_unmatched += 1; unmatched.append(name); continue
        a, d, tg = hit
        annot[name] = (a, d, drv, "name")
    used = set(norm(n) for n in annot)
    # the two register rules; the nets loading each instance indexed once (the inverter rule's scan was a pass over
    # every net per inverter: quadratic on a netlist with a flop-output inverter per flop)
    loads_by_inst = {}
    for name2, (kind2, drv2, lds2) in nets.items():
        for l in lds2:
            loads_by_inst.setdefault(l.rsplit("/", 1)[0], []).append(name2)
    n_qn = n_inv = 0
    for name in list(annot):
        a, d, drv, how = annot[name]
        for p in drv:
            inst, pin = p.rsplit("/", 1)
            cell = cells.get(inst, "")
            # (Q/QN): the same flop's other output pins driving unnamed nets
            for pin2, net2 in inst_outs.get(inst, []):
                if net2 == name or net2 in annot or not unnamed(norm(net2)): continue
                comp = (pin2.startswith("QN") and not pin.startswith("QN")) or (pin.startswith("QN") and not pin2.startswith("QN"))
                annot[net2] = (a, (1 - d) if comp else d, [inst + "/" + pin2], "qn"); n_qn += 1
            # (inverter): a matched net driven by an inverter whose unnamed input net is driven by a flop output
            if inv_pat.search(cell):
                # the inverter's input net: a net whose loads include inst/<input pin>
                for net2 in loads_by_inst.get(inst, []):
                    if net2 in annot or not unnamed(norm(net2)) or nets[net2][0] != "inst": continue
                    dcell = cells.get(nets[net2][1][0].rsplit("/", 1)[0], "") if nets[net2][1] else ""
                    if re.search(r"DFF|SDFF|FLOP|DLH|LATCH", dcell, re.I):
                        annot[net2] = (a, 1 - d, nets[net2][1], "inv"); n_inv += 1
    n_annot = sum(1 for v in annot.values() if v[3] == "name")
    vcd_unused = [k for k in act if k not in used]
    groups = {}
    for name, (a, d, drv, how) in annot.items():
        groups.setdefault((round(a, 5), round(d, 4)), []).extend(drv)
    total = len(annot)
    with open(out_path, "w") as out:
        out.write("# annotate.tcl from map_activity.py: %d nets annotated on their driver pins (%d by name, %d flop complements, %d through inverters); clock %s\n" % (total, n_annot, n_qn, n_inv, clock))
        for h in header:
            out.write(h + "\n")
        out.write("# activity = toggles per simulation clock cycle; duty = fraction high\n")
        for (a, d), pins in sorted(groups.items()):
            for j in range(0, len(pins), 200):
                out.write("set_power_activity -pins {%s} -activity %g -duty %g -clock %s\n" % (" ".join("{%s}" % p for p in pins[j:j + 200]), a, d, clock))
        out.write("puts \"annotate.tcl: %d nets annotated (%d by name, %d flop complements, %d through inverters), %d port-driven (read_vcd), %d named unmatched, %d unnamed interior propagated, %d undriven\"\n" % (total, n_annot, n_qn, n_inv, n_port, n_unmatched, n_auto - n_qn - n_inv, n_undriven))
    line = "map_activity: netlist nets %d: annotated %d (%d by name, %d flop complements, %d through inverters), port-driven %d, named unmatched %d, unnamed interior (propagated) %d, undriven %d; VCD bits %d, unused %d" % (len(nets), total, n_annot, n_qn, n_inv, n_port, n_unmatched, n_auto - n_qn - n_inv, n_undriven, len(act), len(vcd_unused))
    print(line)
    if annotated_path:
        with open(annotated_path, "w") as f:
            f.write("# net\tactivity\tduty\thow\tdrivers\n")
            for name, (a, d, drv, how) in sorted(annot.items()):
                f.write("%s\t%g\t%g\t%s\t%s\n" % (name, a, d, how, " ".join(drv)))
    if report:
        with open(report, "w") as r:
            r.write(line + "\n")
            r.write("\n# named netlist nets with no VCD activity (%d)\n" % len(unmatched))
            for u in unmatched: r.write(u + "\n")
            r.write("\n# VCD bits matching no netlist net (%d)\n" % len(vcd_unused))
            for u in vcd_unused: r.write(u + "\n")


if __name__ == "__main__":
    main()

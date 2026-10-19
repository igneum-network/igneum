#!/usr/bin/env python3
"""Per-signal switching activity from an RTL simulation's VCD, for the register-annotated power read.

For every variable in the VCD (every bit of every vector), over the window [t_from, t_to]:
  toggles  the number of 0 -> 1 and 1 -> 0 transitions (x or z never count: a bit must be known before and after)
  activity toggles per SIMULATION clock cycle, the clock measured from the VCD's own clock signal (its edges in the
           window), so the SDC period of the later power read cannot mis-scale it (OpenSTA's read_vcd divides by the
           SDC clock instead: the VCD-clock pitfall)
  duty     the fraction of the window the bit is high (x and z count as low)
Output: a TSV with one line per bit: name, bit, toggles, activity, duty; the first lines (#) carry the window, the
clock period in VCD units, the cycle count and the timescale. Names are the VCD's scope path joined by '.', e.g.
tb.dut.oct[0].u.lane[3].u.R[5]; the bit is the index inside the declared range (-1 for a scalar).
  vcd_activity.py sim.vcd out.tsv [--clk NAME] [--from T] [--to T] [--scope PREFIX]
--clk: the clock's VCD name (default: the first variable called clk at any scope, the shallowest). --from/--to in the
VCD's own time units (default: the first clock edge after dumping starts, to the last change). --scope: keep only
variables under this prefix (dots), the prefix stripped from the names written.
"""
import sys


def parse_args(argv):
    o = {"clk": None, "from": None, "to": None, "scope": None}
    pos = []
    i = 0
    while i < len(argv):
        a = argv[i]
        if a.startswith("--"):
            o[a[2:]] = argv[i + 1]
            i += 2
        else:
            pos.append(a)
            i += 1
    if len(pos) != 2:
        sys.exit(__doc__)
    return pos[0], pos[1], o


def main():
    src, dst, o = parse_args(sys.argv[1:])
    f = open(src, "r", buffering=1 << 20)
    scope = []
    vars_by_id = {}   # id -> list of (name, size, msb, lsb)
    timescale = ""
    # ---- header ----
    while True:
        line = f.readline()
        if not line:
            sys.exit("no $enddefinitions in the VCD")
        t = line.split()
        if not t:
            continue
        if t[0] == "$scope":
            scope.append(t[2])
        elif t[0] == "$upscope":
            scope.pop()
        elif t[0] == "$var":
            # $var type size id ref [range] $end ; the ref may carry a bit select like R[3] and a range like [31:0]
            size = int(t[2]); vid = t[3]; ref = t[4]
            msb = lsb = 0
            if len(t) > 6 and t[5].startswith("["):
                r = t[5].strip("[]").split(":")
                msb, lsb = int(r[0]), int(r[-1])
            elif size > 1:
                msb, lsb = size - 1, 0
            name = ".".join(scope + [ref])
            vars_by_id.setdefault(vid, []).append((name, size, msb, lsb))
        elif t[0] == "$timescale":
            timescale = " ".join(t[1:]).replace("$end", "").strip()
            if not timescale:
                timescale = f.readline().strip()
                f.readline()
        elif t[0] == "$enddefinitions":
            break
    # one record per id: [value int, known mask int, last time, toggles list, high time list, size]
    rec = {}
    for vid, lst in vars_by_id.items():
        size = lst[0][1]
        rec[vid] = [0, 0, 0, [0] * size, [0] * size, size]
    # the clock
    clk_id = o["clk"]
    if clk_id is None or clk_id not in vars_by_id:
        want = o["clk"]
        best = None
        for vid, lst in vars_by_id.items():
            for name, size, _, _ in lst:
                leaf = name.rsplit(".", 1)[-1]
                if size == 1 and ((want is None and leaf == "clk") or (want is not None and (name == want or name.endswith("." + want)))):
                    depth = name.count(".")
                    if best is None or depth < best[0]:
                        best = (depth, vid, name)
        if best is None:
            sys.exit("clock not found (use --clk NAME)")
        clk_id = best[1]
        clk_name = best[2]
    else:
        clk_name = vars_by_id[clk_id][0][0]
    t_from = int(o["from"]) if o["from"] is not None else None
    t_to = int(o["to"]) if o["to"] is not None else None
    # ---- value changes ----
    now = 0
    started = t_from is not None and t_from <= 0
    clk_edges = 0
    first_edge = None
    last_t = 0
    clk_val = 0
    in_window = False

    def apply(vid, val, known, t):
        r = rec[vid]
        if in_window:
            dt = t - r[2]
            if dt:
                old = r[0]
                hi = r[4]
                while old:
                    lsb = old & -old
                    hi[lsb.bit_length() - 1] += dt
                    old ^= lsb
            x = (r[0] ^ val) & r[1] & known
            tg = r[3]
            while x:
                lsb = x & -x
                tg[lsb.bit_length() - 1] += 1
                x ^= lsb
        r[0] = val; r[1] = known; r[2] = t

    for line in f:
        c = line[0]
        if c == "#":
            now = int(line[1:])
            if not in_window and (t_from is None or now >= t_from):
                pass
            if t_to is not None and now > t_to:
                break
            last_t = now
            continue
        if c == "0" or c == "1":
            vid = line[1:].rstrip()
            v = 1 if c == "1" else 0
            if vid == clk_id:
                if v == 1 and clk_val == 0:
                    if not in_window and (t_from is None or now >= t_from):
                        # the window opens at the first rising edge at or after t_from
                        in_window = True
                        first_edge = now
                        for r in rec.values():
                            r[2] = now
                    if in_window:
                        clk_edges += 1
                clk_val = v
            apply(vid, v, 1, now)
        elif c == "b" or c == "B":
            sp = line.find(" ")
            bits = line[1:sp]
            vid = line[sp + 1:].rstrip()
            r = rec.get(vid)
            if r is None:
                continue
            size = r[5]
            if len(bits) < size:
                bits = bits[0] * (size - len(bits)) + bits if bits[0] in "xzXZ" else bits.rjust(size, "0")
            if "x" in bits or "z" in bits or "X" in bits or "Z" in bits:
                known = int("".join("1" if ch in "01" else "0" for ch in bits), 2)
                val = int("".join(ch if ch in "01" else "0" for ch in bits), 2)
            else:
                known = (1 << size) - 1
                val = int(bits, 2)
            apply(vid, val, known, now)
        elif c == "x" or c == "z" or c == "X" or c == "Z":
            vid = line[1:].rstrip()
            if vid in rec:
                apply(vid, 0, 0, now)
        elif c == "$":
            pass  # $dumpvars, $dumpon, $dumpoff, $dumpall, $end
        # reals (r...) are ignored
    t_end = last_t if t_to is None else min(t_to, last_t)
    if not in_window:
        sys.exit("the clock never rose inside the window")
    # close the window: the high time up to t_end
    for r in rec.values():
        dt = t_end - r[2]
        if dt > 0:
            old = r[0]; hi = r[4]
            while old:
                lsb = old & -old
                hi[lsb.bit_length() - 1] += dt
                old ^= lsb
    span = t_end - first_edge
    cycles = clk_edges - 1 if clk_edges > 1 else 1  # whole cycles between the first and the last rising edge
    # the period from the edges actually seen
    period = span / cycles if cycles else 0
    prefix = (o["scope"] + ".") if o["scope"] else None
    with open(dst, "w") as out:
        out.write("# vcd_activity: source %s timescale %s clock %s period_vcd_units %.6g cycles %d window %d %d\n" % (src, timescale, clk_name, period, cycles, first_edge, t_end))
        out.write("# name\tbit\ttoggles\tactivity\tduty\n")
        n = 0
        for vid, lst in vars_by_id.items():
            r = rec[vid]
            size = r[5]
            for name, _, msb, lsb in lst:
                if prefix:
                    if not name.startswith(prefix):
                        continue
                    name = name[len(prefix):]
                for b in range(size):
                    # the VCD's bit string is msb first: bit string index 0 is msb; our ints have bit 0 = the last char
                    idx = (lsb + b) if lsb <= msb else (lsb - b)
                    tg = r[3][b]
                    hi = r[4][b]
                    out.write("%s\t%d\t%d\t%.6g\t%.6g\n" % (name, idx if size > 1 else -1, tg, tg / cycles if cycles else 0.0, hi / span if span else 0.0))
                    n += 1
    print("vcd_activity: %d bits, clock %s, %d cycles, period %.6g %s, window %d..%d" % (n, clk_name, cycles, period, timescale, first_edge, t_end))


if __name__ == "__main__":
    main()

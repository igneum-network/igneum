#!/usr/bin/env python3
"""The vectors of part 6 (the GATHER table), cut from ref.py before the RTL.

Writes, beside this file:
  fill.hex     the resident tables, one 32-bit word per line: octet o, table t, word a at line (o x NTO + t) x N + a
  ops.hex      one request per line, 448 bits as the testbench's op word:
               [443:436] tag, [435:424] imm, [423:392] lane mask, [391:384] the table per octet (octet o at
               [384 + 2 o +: 2]), [383:0] the lanes' R[src2] low 12 bits (lane l at [12 l +: 12])
  expect.hex   one line per request, 1,032 bits: [1031:1024] the pass count of the banked form, [1023:0] the
               words (lane l at [32 l +: 32]); the testbench compares every word and, in the isolated run, the
               cycle count against passes + 2
  CORNERS.txt  the corner cases by index (the first lines after the random block)
The random block: 4,096 requests, every (octet, table) pair read exactly 1,024 times, six percent with a random
lane mask; then the corner cases. Seeded, so the files regenerate byte for byte.
"""
import os
import random
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
import ref  # noqa: E402

W, OCT, AW, NB, TB = ref.W, ref.OCT, ref.AW, ref.NB, ref.TB
NOCT = W // OCT
NTO = 1 << TB
N = 1 << AW
ALL = (1 << W) - 1
SEED = 20261012


def op_word(tag, imm, mask, tids, addrs):
    v = 0
    for l in range(W):
        v |= (addrs[l] & 0xFFF) << (12 * l)
    for o in range(NOCT):
        v |= (tids[o] & 3) << (384 + 2 * o)
    v |= (mask & ALL) << 392
    v |= (imm & 0xFFF) << 424
    v |= (tag & 0xFF) << 436
    return v


def main():
    rng = random.Random(SEED)
    here = os.path.dirname(os.path.abspath(__file__))
    tables = [[[rng.getrandbits(32) for _ in range(N)] for _ in range(NTO)] for _ in range(NOCT)]
    reqs = []   # (tag, imm, mask, tids, addrs32)
    # the random block: a per-octet schedule of 1,024 reads of each table, shuffled
    sched = []
    for o in range(NOCT):
        s = [t for t in range(NTO) for _ in range(1024)]
        rng.shuffle(s)
        sched.append(s)
    for k in range(NTO * 1024):
        tids = [sched[o][k] for o in range(NOCT)]
        addrs = [rng.getrandbits(32) for _ in range(W)]
        imm = rng.getrandbits(32)
        mask = ALL
        if rng.random() < 0.06:
            mask = rng.getrandbits(W)
            if rng.random() < 0.25:
                mask &= ~(0xFF << (8 * rng.randrange(NOCT)))   # one octet fully masked
        reqs.append((k & 0xFF, imm, mask, tids, addrs))
    corners = []

    def corner(name, imm, mask, tids, addrs):
        corners.append((len(reqs), name))
        reqs.append((len(reqs) & 0xFF, imm, mask, tids, addrs))

    corner("broadcast: every lane the same address, one pass", 0, ALL, [0, 1, 2, 3], [0x5A5] * W)
    corner("one bank per octet, eight distinct rows: eight passes", 0, ALL, [3, 2, 1, 0], [NB * (l % OCT) for l in range(W)])
    corner("distinct banks: one pass", 0, ALL, [1, 1, 1, 1], [l % NB for l in range(W)])
    corner("address 0 everywhere, imm 0", 0, ALL, [0, 0, 0, 0], [0] * W)
    corner("address all ones, imm 0", 0, ALL, [3, 3, 3, 3], [0xFFFFFFFF] * W)
    corner("address 0, imm all ones (the xor reaches N - 1)", 0xFFFFFFFF, ALL, [2, 1, 0, 3], [0] * W)
    corner("imm all ones against address all ones (the xor reaches 0)", 0xFFFFFFFF, ALL, [0, 3, 1, 2], [0xFFFFFFFF] * W)
    corner("lane mask zero: nothing served, one pass, all words zero", 0x123, 0, [0, 1, 2, 3], [rng.getrandbits(32) for _ in range(W)])
    corner("one octet only (octet 2)", 0x77, 0xFF << 16, [0, 1, 2, 3], [rng.getrandbits(32) for _ in range(W)])
    corner("seven passes: two lanes share a row, six other rows, one bank", 0, ALL, [1, 2, 3, 0],
           [NB * (0 if l % OCT < 2 else l % OCT) for l in range(W)])
    corner("the same low bits across octets: no conflict across octets", 0, ALL, [0, 1, 2, 3],
           [(l // OCT) * NB * 16 + (l % OCT) for l in range(W)])
    corner("two rows per bank in every bank: two passes", 0, ALL, [2, 2, 2, 2],
           [(l % OCT) % 4 + NB * (l % OCT // 4) for l in range(W)])
    corner("the last word of every table", 0, ALL, [0, 1, 2, 3], [N - 1] * W)
    corner("masked lanes inside a conflicting set: the conflict counts only live lanes", 0, 0x0F0F0F0F, [3, 3, 3, 3],
           [NB * (l % OCT) for l in range(W)])
    for t in range(NTO):
        corner("every octet on table %d, random addresses" % t, rng.getrandbits(32), ALL, [t] * NOCT, [rng.getrandbits(32) for _ in range(W)])

    with open(os.path.join(here, "fill.hex"), "w") as f:
        for o in range(NOCT):
            for t in range(NTO):
                for a in range(N):
                    f.write("%08x\n" % tables[o][t][a])
    with open(os.path.join(here, "ops.hex"), "w") as fo, open(os.path.join(here, "expect.hex"), "w") as fe:
        for (tag, imm, mask, tids, addrs) in reqs:
            fo.write("%0112x\n" % op_word(tag, imm, mask, tids, addrs))
            words = ref.gather(tables, tids, addrs, imm, mask)
            p = ref.passes(addrs, imm, mask)
            v = p << 1024
            for l in range(W):
                v |= words[l] << (32 * l)
            fe.write("%0258x\n" % v)
    with open(os.path.join(here, "CORNERS.txt"), "w") as f:
        f.write("# request index: the corner case (the random block is requests 0 to %d)\n" % (NTO * 1024 - 1))
        for i, name in corners:
            f.write("%d: %s\n" % (i, name))
    print("requests %d (random %d, corners %d); tables %d x %d x %d words" % (len(reqs), NTO * 1024, len(corners), NOCT, NTO, N))


if __name__ == "__main__":
    main()

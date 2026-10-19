#!/usr/bin/env python3
"""The exact model of part 6 of the universal engine: the per-nonce GATHER table (seat LDS).

The reference (pow/ref/src/igneumhash.cpp, r0e_run_octet, version 10) reads, for every lane l of an octet,
    R[l][dst] = R[l][src1] ^ T[(R[l][src2] ^ imm) % TABLE_WORDS]
where T is the nonce's table of TABLE_WORDS 32-bit words (4,096 words = 16 KB at version 10, filled once per
nonce from one XOF of the nonce's seed). This part is the table and its read: it returns T[(R[src2] ^ imm) mod
TABLE_WORDS] for every lane of a W-lane group (W / 8 octets, each octet naming its own resident table); the XOR
into R[src1] is the integer pipe's (part 3) and the fill words come from the fill source (the XOF engine, or
version 11's slice-and-mix) through the write port. The table size is a parameter (512, 1,024 or 4,096 words).

Two models live here:
  gather()   the value model: the words every lane receives (exact, what the acceptance compares);
  passes()   the cycle model of the banked form: how many bank passes a request takes under the RTL's
             serialisation rule (lowest lane wins a bank, every other pending lane of the same bank and the same
             row is served in the same pass, a broadcast; distinct rows in one bank serialise); the request's
             latency is passes + 2 cycles from its accepted cycle to rd_ready when no fill blocks it.
The truncation identity the RTL relies on, (R ^ imm) mod 2^AW == (R mod 2^AW) ^ (imm mod 2^AW), is checked by
self_test().
"""
import random
import sys

W = 32          # lanes per group
OCT = 8         # lanes per octet (one nonce)
AW = 12         # table address bits: 2^AW words (12: 16 KB, 10: 4 KB, 9: 2 KB)
NB = 8          # banks per octet set, interleaved on the low address bits
TB = 2          # table index bits per octet: 2^TB resident tables per octet (C = (W / OCT) x 2^TB contexts)


def gather_addr(r_src2, imm, aw=AW):
    """The word index of the reference: (R[src2] xor imm) mod 2^aw, from the full 32-bit values."""
    return (r_src2 ^ imm) & ((1 << aw) - 1)


def gather(tables, tids, addrs, imm, mask, w=W, oct_=OCT, aw=AW):
    """The words of one W-lane gather request.

    tables[o][t]  the resident table t of octet o (a list of 2^aw words)
    tids[o]       the table each octet reads this request
    addrs[l]      lane l's R[src2] (any width; only its low aw bits reach the table)
    imm           the instruction's immediate (any width; only its low aw bits matter)
    mask          the lane mask (bit l set: lane l is served; a masked lane returns 0)
    """
    out = []
    for l in range(w):
        o = l // oct_
        if (mask >> l) & 1:
            out.append(tables[o][tids[o]][gather_addr(addrs[l], imm, aw)])
        else:
            out.append(0)
    return out


def passes(addrs, imm, mask, aw=AW, nb=NB, w=W, oct_=OCT):
    """The bank passes one request takes in the banked RTL (the maximum over the octets, at least 1).

    The RTL's rule per octet and per bank each pass: among the pending lanes that name the bank, the lowest lane
    is served and so is every pending lane of that bank with the same row (a broadcast); the rest wait for the
    next pass. Lanes of different octets never conflict (each octet has its own bank set).
    """
    bw = nb.bit_length() - 1
    worst = 1
    for o in range(w // oct_):
        pend = [l for l in range(o * oct_, (o + 1) * oct_) if (mask >> l) & 1]
        n = 0
        while pend:
            n += 1
            served = set()
            for b in range(nb):
                inb = [l for l in pend if gather_addr(addrs[l], imm, aw) & (nb - 1) == b]
                if not inb:
                    continue
                row = gather_addr(addrs[inb[0]], imm, aw) >> bw
                for l in inb:
                    if gather_addr(addrs[l], imm, aw) >> bw == row:
                        served.add(l)
            pend = [l for l in pend if l not in served]
        worst = max(worst, n)
    return worst


def latency(addrs, imm, mask, **kw):
    """Cycles from the accepted cycle to rd_ready, no fill in the way: passes + 2."""
    return passes(addrs, imm, mask, **kw) + 2


def self_test(n=10000, seed=1):
    rng = random.Random(seed)
    for _ in range(n):
        r = rng.getrandbits(32)
        imm = rng.getrandbits(32)
        assert gather_addr(r, imm) == ((r & 0xFFF) ^ (imm & 0xFFF)), "truncation identity"
    # the conflict rule on the named cases
    same = [0x123] * W
    assert passes(same, 0, (1 << W) - 1) == 1, "broadcast is one pass"
    one_bank = [l * NB for l in range(W)]                      # every lane of an octet in bank 0, distinct rows
    assert passes(one_bank, 0, (1 << W) - 1) == OCT, "one bank, eight rows: eight passes"
    distinct = [l % NB for l in range(W)]                      # every lane its own bank
    assert passes(distinct, 0, (1 << W) - 1) == 1, "distinct banks: one pass"
    assert passes(distinct, 0, 0) == 1, "no lanes: one pass"
    return True


if __name__ == "__main__":
    self_test()
    print("ref.py self-test ok")

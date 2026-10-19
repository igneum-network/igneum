#!/usr/bin/env python3
"""The exact model of the operand collector and routing crossbar (part 2 of the universal specialist engine,
docs/igneumhash/adversary/UNIVERSAL-ENGINE-PARTS.md with the addendum of 12 October 2026; the version 10 reference's
r0e_run_octet for the item-indexed second source and the gather address).

The forward path, one instruction for one context across W lanes, latency 1 cycle: each of the three sources a, b, c
is selected per instruction by a 3-bit code: 0 register (that source's register-file read bus), 1 immediate (broadcast
to every lane), 2 reserved (the parts document's shared-memory source; the addendum removed it; reads zero), 3 item
(the lane's OCTET's 256-byte item register, 64 words, at index (register-bus value xor imm) mod 64; present on the
sources named by ITEM_SRC, a bit per source a, b, c; a source without the item path reads zero under code 3),
4 gather (the gather unit's per-lane return), 5 to 7 zero. Beside the operands the gather address per lane,
(register bus b xor imm) mod 4096, the grammar's table index. The tags (ctx, op, dst, imm, lane mask) ride along.

The write path, latency 1 cycle: the result bus is routed by res_dst_sel: 0 the register file's write port (with
ctx, the destination register and the lane mask), 1 reserved (the shared-memory write port, removed; dropped),
2 the address path (the walk engine's next address), 3 none. Data outputs hold their last value when not valid.

Hex vector layout (32-bit words, one per line): header [ncases, W, IW, ITEM_SRC]; then per case:
  w0 ctrl: issue_v[0] | sel_a[3:1] | sel_b[6:4] | sel_c[9:7] | ctx[13:10] | dst[18:14] | op[23:19] | res_v[24]
           | res_dst_sel[26:25] | res_ctx[30:27]
  w1 lane_mask, w2 imm, w3 res_lane_mask, w4 res_dst
  rf_a[W], rf_b[W], rf_c[W], gather[W], item[(W/8)*IW], res[W]
  expected: opa[W], opb[W], opc[W], gaddr[W], wb (rf_wr_v[0] | addr_v[1] | rf_wr_reg[6:2] | rf_wr_ctx[10:7])
Usage: ref.py gen <random|grammar> <seed> <out.hex> [ncases]"""
import random
import sys

W = 32
OCT = 8
IW = 64
C = 16
RB = 5
ITEM_SRC = 0b010  # the item path on source b only: the grammar's second source
M32 = 0xFFFFFFFF
SRC_REG, SRC_IMM, SRC_RSV, SRC_ITEM, SRC_GATHER, SRC_ZERO = 0, 1, 2, 3, 4, 5
DST_REG, DST_RSV, DST_ADDR, DST_NONE = 0, 1, 2, 3
WORDS_PER_CASE = 5 + 4 * W + (W // OCT) * IW + W + 4 * W + 1


def select(sel, bus, imm, item, gather, item_ok):
    """One source across W lanes: bus, gather are W words; item is (W/OCT) x IW words."""
    out = []
    for l in range(W):
        if sel == SRC_REG:
            v = bus[l]
        elif sel == SRC_IMM:
            v = imm
        elif sel == SRC_ITEM and item_ok:
            v = item[l // OCT][(bus[l] ^ imm) % IW]
        elif sel == SRC_GATHER:
            v = gather[l]
        else:
            v = 0
        out.append(v & M32)
    return out


def forward(c):
    opa = select(c["sel_a"], c["rf_a"], c["imm"], c["item"], c["gather"], ITEM_SRC & 1)
    opb = select(c["sel_b"], c["rf_b"], c["imm"], c["item"], c["gather"], ITEM_SRC & 2)
    opc = select(c["sel_c"], c["rf_c"], c["imm"], c["item"], c["gather"], ITEM_SRC & 4)
    gaddr = [(c["rf_b"][l] ^ c["imm"]) & 0xFFF for l in range(W)]
    return opa, opb, opc, gaddr


def writeback(c):
    rf_wr_v = 1 if (c["res_v"] and c["res_dst_sel"] == DST_REG) else 0
    addr_v = 1 if (c["res_v"] and c["res_dst_sel"] == DST_ADDR) else 0
    return rf_wr_v | (addr_v << 1) | (c["res_dst"] << 2) | (c["res_ctx"] << 7)


def rand_case(r, mode):
    """mode random: every code uniformly (the reserved and the upper zero codes included); mode grammar: the version
    10 window's counts (FMA 12, FADD 4, IXOR 3, IROTL 3, IADD 1, IMULHI 1, SHFL 4, GATHER 2, TILE 2; the item flag on
    8 of the 24 arithmetic operations), so the selection mix is the real programme's and the data random."""
    c = {}
    if mode == "random":
        c["issue_v"] = 0 if r.random() < 1 / 16 else 1
        c["sel_a"], c["sel_b"], c["sel_c"] = r.randrange(8), r.randrange(8), r.randrange(8)
        c["op"] = r.randrange(32)
        c["res_v"] = 0 if r.random() < 1 / 16 else 1
        c["res_dst_sel"] = r.randrange(4)
    else:
        op = r.choice([0] * 12 + [1] * 4 + [2] * 3 + [3] * 3 + [4] + [5] + [8] * 4 + [9] * 2 + [10] * 2)
        c["op"] = op
        c["issue_v"] = 1
        if op <= 7:
            flag = r.random() < 8 / 24
            c["sel_a"] = SRC_REG
            c["sel_b"] = SRC_ITEM if flag else SRC_REG
            c["sel_c"] = SRC_REG if op == 0 else SRC_ZERO
        elif op == 8:
            c["sel_a"], c["sel_b"], c["sel_c"] = SRC_REG, SRC_REG, SRC_ZERO
        elif op == 9:
            c["sel_a"], c["sel_b"], c["sel_c"] = SRC_REG, SRC_GATHER, SRC_ZERO
        else:
            c["issue_v"] = 0
            c["sel_a"], c["sel_b"], c["sel_c"] = SRC_ZERO, SRC_ZERO, SRC_ZERO
        c["res_v"] = 1 if r.random() < 30 / 32 else 0
        c["res_dst_sel"] = DST_ADDR if r.random() < 1 / 64 else DST_REG
    c["ctx"] = r.randrange(C)
    c["dst"] = r.randrange(1 << RB)
    c["imm"] = r.getrandbits(32)
    c["lane_mask"] = r.getrandbits(W)
    c["res_ctx"] = r.randrange(C)
    c["res_dst"] = r.randrange(1 << RB)
    c["res_lane_mask"] = r.getrandbits(W)
    c["rf_a"] = [r.getrandbits(32) for _ in range(W)]
    c["rf_b"] = [r.getrandbits(32) for _ in range(W)]
    c["rf_c"] = [r.getrandbits(32) for _ in range(W)]
    c["gather"] = [r.getrandbits(32) for _ in range(W)]
    c["item"] = [[r.getrandbits(32) for _ in range(IW)] for _ in range(W // OCT)]
    c["res"] = [r.getrandbits(32) for _ in range(W)]
    return c


def corner_cases(r):
    """Every source selection triple over the six named codes (216) on random data; then the named edges."""
    out = []
    for sa in range(6):
        for sb in range(6):
            for sc in range(6):
                c = rand_case(r, "random")
                c["issue_v"] = 1
                c["sel_a"], c["sel_b"], c["sel_c"] = sa, sb, sc
                out.append(c)
    fills = [0, M32, 0x80000000, 0x7FFFFFFF, 0xAAAAAAAA, 0x55555555]
    for fill in fills:
        for sel in (SRC_REG, SRC_ITEM, SRC_GATHER, SRC_IMM):
            c = rand_case(r, "random")
            c["issue_v"] = 1
            c["sel_a"] = c["sel_b"] = c["sel_c"] = sel
            c["imm"] = fill
            for k in ("rf_a", "rf_b", "rf_c", "gather", "res"):
                c[k] = [fill] * W
            c["item"] = [[fill ^ (j * 0x01010101 & M32) for j in range(IW)] for _ in range(W // OCT)]
            c["lane_mask"] = fill & ((1 << W) - 1)
            c["res_lane_mask"] = (~fill) & ((1 << W) - 1)
            c["res_v"] = 1
            c["res_dst_sel"] = DST_REG if fill & 1 else DST_ADDR
            out.append(c)
    # the item index at 0 and 63 in every lane, with and without the immediate's bits
    for imm in (0, 63, 0xFFFFFFC0, M32):
        for base in (0, 63):
            c = rand_case(r, "random")
            c["issue_v"] = 1
            c["sel_a"] = c["sel_b"] = c["sel_c"] = SRC_ITEM
            c["imm"] = imm
            c["rf_a"] = [base | (r.getrandbits(26) << 6) for _ in range(W)]
            c["rf_b"] = [base | (r.getrandbits(26) << 6) for _ in range(W)]
            c["rf_c"] = [base | (r.getrandbits(26) << 6) for _ in range(W)]
            out.append(c)
    # no issue, no result: the outputs hold; every destination code with res_v low and high
    for sel in range(4):
        for v in (0, 1):
            c = rand_case(r, "random")
            c["issue_v"] = 0
            c["res_v"] = v
            c["res_dst_sel"] = sel
            out.append(c)
    return out


def pack_ctrl(c):
    return (c["issue_v"] | (c["sel_a"] << 1) | (c["sel_b"] << 4) | (c["sel_c"] << 7) | (c["ctx"] << 10)
            | (c["dst"] << 14) | (c["op"] << 19) | (c["res_v"] << 24) | (c["res_dst_sel"] << 25) | (c["res_ctx"] << 27))


def emit(cases, path):
    words = [len(cases), W, IW, ITEM_SRC]
    for c in cases:
        opa, opb, opc, gaddr = forward(c)
        words += [pack_ctrl(c), c["lane_mask"], c["imm"], c["res_lane_mask"], c["res_dst"]]
        words += c["rf_a"] + c["rf_b"] + c["rf_c"] + c["gather"]
        for o in c["item"]:
            words += o
        words += c["res"]
        words += opa + opb + opc + gaddr + [writeback(c)]
    assert len(words) == 4 + len(cases) * WORDS_PER_CASE
    with open(path, "w") as f:
        f.write("".join("%08x\n" % (w & M32) for w in words))
    n_issue = sum(c["issue_v"] for c in cases)
    n_res = sum(c["res_v"] for c in cases)
    n_rf = sum(1 for c in cases if c["res_v"] and c["res_dst_sel"] == DST_REG)
    n_addr = sum(1 for c in cases if c["res_v"] and c["res_dst_sel"] == DST_ADDR)
    print("wrote %s: %d cases, %d words per case, issued %d (operands delivered %d), results %d (register %d, address %d)"
          % (path, len(cases), WORDS_PER_CASE, n_issue, 3 * W * n_issue, n_res, n_rf, n_addr))


def main():
    if len(sys.argv) < 5 or sys.argv[1] != "gen":
        print(__doc__)
        sys.exit(2)
    mode, seed, path = sys.argv[2], int(sys.argv[3]), sys.argv[4]
    n = int(sys.argv[5]) if len(sys.argv) > 5 else 1024
    r = random.Random(seed)
    cases = [rand_case(r, mode) for _ in range(n)]
    if mode == "random":
        cases += corner_cases(r)
    emit(cases, path)


if __name__ == "__main__":
    main()

# The vectors

Two sets, cut by `ref.py` (deterministic, the Python standard library alone) and pinned by `SHA256SUMS`; the hex
files are not committed (11 MB of random words) and regenerate in a second:

    python3 ref.py gen random 1 vectors/random.hex 1024     # 1,280 cases: 1,024 random plus the corners
    python3 ref.py gen grammar 2 vectors/grammar.hex 1024   # 1,024 cases at the version 10 window's counts
    (cd vectors && sha256sum -c SHA256SUMS)

The random set: every source code 0 to 7 on each of a, b, c uniformly (the reserved and the upper zero codes
included), every destination code 0 to 3, issue and result valid low one cycle in sixteen, random data on every bus
and in every octet's item register; then the corners: every (a, b, c) triple over the six named codes (216 cases),
the all-zero, all-one, sign-bit, 0x7FFFFFFF and alternating fills on every bus with the item register a fill
pattern per word and the lane masks the fill and its complement, the item index at 0 and 63 in every lane with
the immediate's low bits clear, set and all-one, and the hold cases (no issue; each destination code with the
result valid low and high). The testbench resets the collector in the middle of the set and checks the valids drop.

The grammar set: the selection mix of the real programme (SPEC-R0E-v0.md section 2, version 10's fixed counts:
FMA 12, FADD 4, IXOR 3, IROTL 3, IADD 1, IMULHI 1, SHFL 4, GATHER 2, TILE 2 of 32; the item flag on 8 of the 24
arithmetic operations; FMA's third source the destination register, the others' zero; GATHER's second source the
gather return; TILE no issue through the collector; the result to the register file with the address path one in
64) on random data. Its VCD is the activity the placed power is read on, labelled "random data at the grammar's
selection mix"; the reference's own trace set is the integrator's.

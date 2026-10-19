# Part 6 of the universal engine: the per-nonce GATHER table (seat LDS)

The part of the parts document (docs/igneumhash/adversary/UNIVERSAL-ENGINE-PARTS.md) as the addendum of 12 October
2026 corrected it from the version 10 reference (pow/ref/src/igneumhash.cpp, r0e_run_octet): there is no shared-memory
transpose (TILE is register wiring, part 5's); this part is the per-nonce 16 KB GATHER table, 4,096 words filled once
per nonce, read by all eight lanes of an octet at `(R[src2] xor imm) mod 4,096`, two reads per window of 32
operations, four nonces per 32-lane group, C contexts in flight; the placed energy per read is the term. Every figure
below carries its grade (EXACT, SYNTHESISED, PLACED, ESTIMATE) and the run it came from; the placed figures are read by
the integrator, not graded here.

## 1. What it does

For every lane `l` of a W-lane group (W = 32: four octets of eight lanes, one nonce each) the part returns the word
`T_o[(R[l][src2] xor imm) mod 2^AW]` from the table `T_o` the octet names; the reference's `R[l][dst] = R[l][src1] xor
T[...]` finishes in the integer pipe (part 3's IXOR path takes the returned word as its second source), so the part's
value model is the table read alone. Masked lanes return zero. The table size is a parameter: AW 12 (4,096 words,
16 KB, version 10), 10 (4 KB) or 9 (2 KB), for the version 11 line that may shrink it.

The structure (rtl/gather_table.v): every octet owns a BANK SET of NB = 8 single-port banks, word-interleaved on the
low three address bits, each bank holding the rows of the 2^TB = 4 tables resident for that octet, so C = 4 x 4 = 16
contexts (nonces) are in flight per group with 256 KB of table (16 tables of 16 KB); a bank is 2,048 x 32 (8 KB), the
kit's fakeram 2048x39 macro in the placed flow (rtl/macro/asap7, rtl/macro/nangate45) and a behavioural array for
exactness (rtl/gather_bank.v). A request is one instruction for the whole group. Each cycle after acceptance is one
PASS: in every octet every bank serves the lowest pending lane that names it plus every pending lane of the same bank
and the same row (a broadcast); lanes of the same bank at other rows wait for the next pass under `lds_stall`. The
octets' bank sets are independent, so lanes of different octets never conflict.

The fill (the table's 4,096 words per nonce) is a separate path, as the coordinator asked so that either fill source
lands without a rewrite: a wide write port of 8 words per cycle (one per bank, 512 cycles per 16 KB table, never
stalled) that any source drives, and rtl/gather_fill.v, the adapter from a one-word-per-cycle stream whose source may
stall (`in_v` low: the XOF engine of version 10, or the outside review's chained scattered dataset reads of version 11, each
word's address depending on the last) to that port. The read path never changes with the source.

## 2. The interface

```
gather_table #(W 32, OCT 8, AW 12, NB 8, TB 2, OW 2, TAGW 8)
  clk, rst (synchronous, active high)
  read request:  rd_v, rd_tag[7:0], rd_tid[7:0] (the table per octet, 2 bits each, octet o at [2o +: 2]),
                 rd_addr[383:0] (lane l's R[src2] low 12 bits at [12l +: 12]), rd_imm[11:0], lane_mask[31:0]
  read return:   lds_stall, rd_ready (one cycle), rd_tag_out[7:0], rd_data[1023:0] (lane l's word at [32l +: 32])
  fill port:     wr_v, wr_oct[1:0], wr_tid[1:0], wr_addr[11:0] (8-aligned), wr_data[255:0] (word i to bank i)
gather_fill #(AW 12, NB 8, TB 2, OW 2)
  start, start_oct, start_tid; in_v, in_word[31:0], in_ready; done; the write port's signals as outputs
```

Timing (all fixed, no combinational path from an input to an output; `lds_stall` is a function of the state):

- A request presented with `rd_v` while `lds_stall` is low is accepted at that clock edge. `lds_stall` high means
  the current request is still serialising: hold the next one.
- Latency: passes + 2 cycles from the accepted edge to the cycle `rd_ready` is high, where passes is the number of
  bank passes (1 when no two live lanes of an octet name the same bank at different rows): 3 cycles conflict-free.
  Throughput one request per cycle when conflict-free (the next request is accepted while the previous one's data
  is in flight).
- The pass count on random addresses, from ref.py over 2,000 random 32-lane requests (every lane live): NB 8 gives a
  mean of 3.30 passes per request, NB 16 2.60, NB 32 2.16 (NB is a parameter; the vectors and the figures below are
  at NB 8). On the vectors' random block the testbench measured 3.22. A request of one octet alone (eight lanes)
  has the same distribution, since the octets' sets are independent.
- A fill write to an octet's bank set takes that set's single port for one cycle: a read pass of that octet in that
  cycle is deferred by one cycle; the fill never stalls. A word written by a fill presented in cycle t is seen by
  every read accepted in cycle t or later (checked by the testbench's visibility run).
- The address: `(R xor imm) mod 2^AW` equals `(R mod 2^AW) xor (imm mod 2^AW)`, so only the low AW bits of R[src2]
  and of imm cross the interface and the xor is done inside (ref.py's self_test checks the identity).

What the spec left silent and this part chose: the request is per 32-lane group with a table index per octet (an
issue per octet is the same request with the other octets masked); the tag carries the issuer's context and
destination; the xor into R[src1] is part 3's; one bank set per octet rather than 32 shared banks (no cross-octet
conflicts and an 8:1 return multiplexer per lane instead of 32:1); the bank arbitration takes the lowest lane first
(the order never changes the words, only which pass serves a lane); the tables of the C contexts share the banks by
the table index above the row (contexts never read in the same cycle: one request per cycle).

## 3. Exactness (EXACT, on the box, 12 October 2026)

ref.py is the model (the value model `gather` and the cycle model `passes`), written before the RTL; vectors/gen.py
cut the vectors from it (seeded, byte-for-byte reproducible): 4,096 random requests in which every one of the 16
contexts is read exactly 1,024 times (six percent with random lane masks, some with a whole octet masked) and 18
corner cases (vectors/CORNERS.txt: broadcast, one bank with eight rows, distinct banks, address 0 and all ones
against imm 0 and all ones, the lane mask zero, one octet alone, seven passes, two rows per bank, the last word of
every table, masked lanes inside a conflicting set, every octet on each table). The testbench (tb/gather_table_tb.v,
iverilog 12) fills the tables of octets 0 and 1 through the wide port and those of octets 2 and 3 through the
gather_fill adapter, then runs every request twice: isolated, every word, the tag and the latency against
passes + 2; streamed back to back under `lds_stall` with an idempotent fill injected every fifth cycle (the deferral
path), every return checked in order; then the visibility rule (a fill and a read of its words presented in the same
cycle: the new words, latency 4; the words restored, latency 3).

Result: 4,114 requests x 2 runs, 263,296 words, 0 mismatches, 11 s. Streamed: 4,114 requests in 14,520 cycles with
the fills in the way (3.53 cycles per request).

## 4. Synthesis (SYNTHESISED: yosys 0.68 of the ORFS tree, abc on the kits' liberties, unplaced)

| block | ASAP7 (RVT, TT) um2 | nangate45 um2 | note |
|---|---|---|---|
| the read path's logic (gather_table with the 32 banks as the kit's macro, black-boxed): the request register, the arbitration, the bank address and enable logic, the return multiplexers, the fill stage | 2,259 (1,938 flops; the 1,024 of them the return register) | 34,069 (1,938 flops) | 20,433 cells on nangate45; the largest term the 8:1 x 32-bit return multiplexers (7,877 MUX2) |
| the 32 bank macros (fakeram 2048x39, one port) | 32 x 3,354 = 107,323 | 32 x 45,479 = 1,455,322 | the kit's generated macro: its area is the generator's model, its energy a placeholder (section 6) |
| one bank as a flop array (2,048 x 32, the "upper bound" form) | 33,588 (65,568 flops) | | x 32 = 1,074,816 um2: ten times the macro form; the figure the flop-array placement would carry |
| the fill adapter (gather_fill) | 229 (508 flops) | | one per group |

The read path's logic is 2 percent of the macro form's area at ASAP7: the table is the part, the logic around it is
small.

## 5. Placement and the placed energy (PLACED, the activity's provenance stated)

The flow (flow/run.sh): the testbench's streamed run dumps the VCD of the design under test at the platform's clock
(1 GHz on ASAP7, 500 MHz on nangate45, the SDC period equal to the VCD's clock), ORFS synthesises, floorplans and
places the read path with the 32 macros from the kit's LEF and liberty (CORE_UTILIZATION 30, PLACE_DENSITY 0.60),
and OpenSTA reports the power of the placed netlist with the VCD's activity on the ports and the interior
propagated, by group (macro, sequential, combinational, clock). The pJ per 32-lane gather = the non-macro power at
the VCD's clock x the cycles per request of the streamed run (3.53) / the clock; per octet gather a quarter of it.

The runs (12 October 2026, on the box; OpenROAD 26Q4-110, ORFS yosys 0.68; placement only, no clock tree, so
the flops' clock pins carry the clock's share under "sequential" and the "clock" group is empty; parasitics
estimated from placement). The activity: the testbench's streamed run of the vectors (4,114 random and corner
requests back to back, 14,520 cycles, 3.53 cycles per request, an idempotent fill write injected every fifth
cycle), the VCD at the platform's clock, measured per simulation cycle by the annotation kit of
pow/rtl/universal/power-annotate (the brief's addendum 2): ANNOTATED = every register and every named RTL net that
survived synthesis annotated from the VCD, the unnamed interior cones propagated from them; "ports, propagated"
= OpenSTA's read_vcd on the ports alone with the interior propagated (reported beside, never the figure). The
banks are the kit's macros in the placed netlist, so no mirror wires are needed (no memory array is in the logic);
the macros' own power is the generator's placeholder and is SET ASIDE (section 6), the figure is the logic's.

| platform, clock | placed area (logic + 32 macros) | timing at placement | read | logic power | pJ per 32-lane gather | pJ per octet gather | annotation |
|---|---|---|---|---|---|---|---|
| ASAP7 RVT TT, 1 GHz (the VCD's clock 1.000 ns, SDC 1000 ps) | 114,174 um2 at 31 percent utilisation (the macros 107,323 of it) | wns 0.00, worst slack 18.2 ps: met | ANNOTATED | 21.5 mW (sequential 5.2, combinational 16.3) | 75.9 | 19.0 | 2,079 nets from the VCD (1,768 by name, 311 through inverters) + 720 ports; 29,528 unnamed interior nets propagated; 7,681 abc-named nets unmatched (propagated); of 40,010 |
| the same | | | ports, propagated | 38.2 mW | 134.8 | 33.7 | the method of record before the kit: 1.78x the annotated read |
| Nangate45 typical, 500 MHz (the VCD's clock 2.000 ns, SDC 2.0 ns) | 1,509,516 um2 at 30 percent (the macros 1,455,322) | wns 0.00, worst slack 0.17 ns: met | ANNOTATED | 88.0 mW (sequential 20.7, combinational 67.4) | 621 | 155 | 3,260 nets (2,355 by name, 905 flop complements) + 720 ports; 22,848 unnamed propagated; of 36,224 |
| the same | | | ports, propagated | 58.0 mW | 409 | 102 | the 45 nm figure is the second library's reading only |

The four statements the review lead asks of a PLACED row:

- The activity's choice: the part's own streamed vectors (4,096 random requests with every context read 1,024
  times plus the 18 corners, back to back, fills injected), NOT the programme's trace. The table is read at
  register-drawn addresses that the reference's programme makes uniform over the 4,096 words (the xor of a
  register with the immediate), so random addresses with every lane live are the table's own activity; the
  programme's trace would only change the lane masks (all live under version 10) and the request spacing (two
  per window of 32, so the part idles between them: the per-gather energy stands, the idle clock does not enter
  it since no clock tree is placed).
- The propagated share of the POWER (not of the nets): at ASAP7 the instances whose output net is annotated from
  the VCD carry 9.2 percent of the logic power (2,079 instances: the registers and the named RTL wires) and the
  instances whose output net is propagated carry 90.8 percent (35,961 instances: the arbitration and the return
  multiplexers, abc's unnamed cones between the annotated registers and the macro pins); at Nangate45 22.6 and
  77.4 percent. So the figure is the registers measured and the multiplexer cones propagated from measured
  boundaries (the request register's and the macro outputs' activities): PLACED-PROVISIONAL by the
  adjudicator's grade, of record for the logic until a gate-level simulation of the placed netlist replaces the
  cones' activity.
- The voltage: ASAP7 RVT at the typical corner's VDD 0.7 V (the liberty's voltage_map), Nangate45 typical at
  1.1 V (nom_voltage). Placed, NOT routed: global and detailed placement, parasitics estimated from placement
  (estimate_parasitics -placement), no clock tree, no routing.
- The branch lands on main through the landing check (lead/ue-lds).

The figure of record for the logic: 19.0 pJ per octet gather (75.9 per 32-lane request) at ASAP7, PLACED-PROVISIONAL
(annotated registers, propagated cones, as stated above), the
sequential share a quarter of it (the request register, the pass bookkeeping and the 1,024-bit return register) and
the combinational three quarters (the per-bank arbitration over eight lanes, the bank address multiplexers and the
8:1 x 32-bit return multiplexers). It carries the fills' share (one wide write every fifth cycle in the activity)
and the serialisation (3.22 passes per request on random addresses): a conflict-free octet gather would be about
a third of it. The reports are under flow/results/<platform>/ (SUMMARY.txt, power_annotated.txt,
power_propagated.txt).

What this part's placed number does NOT contain: the SRAM read itself (section 6, the estimate), the clock tree
(no CTS), routing parasitics (placement estimates), the fill source (the XOF or the dataset reads), and the
integer pipe's xor of the returned word (part 3).

## 6. The SRAM term (ESTIMATE, labelled; the review lead's rule of 12 October 2026)

No SRAM term is PLACED from either kit: the fakeram liberty's energies are the generator's placeholders (its
configuration file says "VALUES NOT REALISTIC"; the clock pin's internal energy is 1.345 fJ per edge for the 256x32
and the 2048x39 macro alike, the data pins 0.013 fJ), so the macro's power in the OpenSTA report is reported and set
aside, never charged. The figure of record for this part's bank, a 2,048 x 32 (8 KB) single-port macro at a 7 nm
class node, is the review lead's 4 to 16 KB row: 1.6 pJ per 32-bit word read (1.2 to 2.4), a write at 1.2x
(1.9 pJ). Per request the bank reads are the distinct (bank, row) pairs the live lanes name, 32 on random addresses
(one per lane; a broadcast saves one), so:

- the SRAM term per 32-lane gather: 32 x 1.6 = 51 pJ (38 to 77); per octet gather (eight lanes, the grammar's
  unit) 12.8 pJ (9.6 to 19.2);
- the fill: 4,096 writes x 1.9 pJ = 7.9 nJ per nonce; at version 10's counts (two gathers per window of 32, 2,048
  operations per lane-round, 64 rounds: 8,192 octet gathers per nonce) that is 0.96 pJ per octet gather amortised;
- the flop-array form's placed figure, when read, is the "upper bound" (section 4 gives its area; its energy is
  the clock on 2,048 x 32 flops per bank plus the read tree, not a figure to charge the specialist with).

The term the energy claim reads for this part, per octet gather: the SRAM estimate 12.8 pJ (9.6 to 19.2) plus the
placed logic of section 5, 19.0 pJ ANNOTATED at ASAP7, plus the fill's amortised write 0.96 pJ: about 33 pJ per
octet gather (29 to 39 from the SRAM row's range), DERIVED, two of them per window of 32 operations. Nothing of
the macro's own power report enters it. the review lead's reading (12 October 2026): the bank lever (16 banks per
octet, or one bank set shared across the octets) is immaterial at 3 to 9 percent of a window's energy, so the
design stays as placed; and the part's 4.1 pJ per word read (33 pJ over eight words) puts the GPU's shared-memory
class nearer 1x than the 2.5x prior, which the ablation cell measures.

## 7. Files

| path | what |
|---|---|
| ref.py | the exact model: gather (the words), passes (the bank passes), latency; self_test |
| vectors/gen.py, vectors/fill.hex, ops.hex, expect.hex, CORNERS.txt | the vectors cut from ref.py (regenerate: `python3 vectors/gen.py`) |
| rtl/gather_table.v | the read path with the banks as gather_bank instances |
| rtl/gather_bank.v | the behavioural single-port bank (exactness; the flop-array form in synthesis) |
| rtl/macro/asap7/gather_bank.v, rtl/macro/nangate45/gather_bank.v | the bank as the kit's fakeram 2048x39 macro (the placed flow reads one of these in place of rtl/gather_bank.v) |
| rtl/gather_fill.v | the fill adapter from a one-word stream to the wide write port |
| tb/gather_table_tb.v | the testbench: the fill, the isolated and streamed runs, the visibility rule; +vcd |
| flow/synth.sh | the first synthesis numbers (yosys, the kits' liberties; the pitfalls of the security review's recipe) |
| flow/run.sh, flow/constraint.sdc.in | the placed flow: the VCD at the platform's clock, ORFS synth, floorplan, place, OpenSTA power by group |

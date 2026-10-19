# Part 2: the operand collector and routing crossbar (seat OC)

The universal specialist engine's operand collector (docs/igneumhash/adversary/UNIVERSAL-ENGINE-PARTS.md, part 2,
with the addendum of 12 October 2026 and the version 10 reference's `r0e_run_octet`): for one instruction of one
context across W = 32 lanes (four octets of eight, one nonce each) it gathers the three source operands and presents
them aligned to the execution units, and routes the result back to the register file or the address path.

| file | what |
|---|---|
| `ref.py` | the exact Python model and the vector generator (the hex layout in its docstring) |
| `vectors/` | the two sets' checksums and their recipe (vectors/README.md) |
| `rtl/oc.v` | the RTL, Verilog-2005, one module `oc` |
| `tb/oc_tb.v` | the iverilog testbench: one case per cycle, every output checked a cycle later, a reset mid-stream, the VCD at the clock given |
| `flow/` | `run.sh` (everything on a box), `synth.sh` and `synth.ys.in` (yosys on both libraries), `fixnet.py` (the netlist for OpenSTA), `place.tcl` (OpenROAD placement and power), `figures.py` (the per-operand figures) |

## The interface

Parameters: `W` 32 lanes, `OCT` 8 lanes per octet, `IW` 64 item words per octet (256 bytes), `RB` 5 register index
bits (16 registers is the grammar's size, 32 the margin; the field is RB bits wide either way), `ITEM_SRC` 3'b010
(a bit per source a, b, c naming which carry the item path; the default is the grammar's second source alone).

The issue side, from the sequencer and the register file, sampled on `clk` when `issue_v`. All of it in ONE cycle:
`issue_v`, the sels and the tags are presented in the same cycle the register file's read buses are valid (the
register file's read latency is 1, so the top delays the sequencer's outputs by one cycle before this port); the
collector never samples the buses a cycle after `issue_v`.

| signal | width | meaning |
|---|---|---|
| `ctx`, `op`, `dst`, `imm`, `lane_mask` | 4, 5, RB, 32, W | the instruction's tags, carried to the units aligned with the operands |
| `sel_a`, `sel_b`, `sel_c` | 3 each | the source code per operand: 0 register, 1 immediate, 2 reserved (reads zero), 3 item, 4 gather, 5 to 7 zero |
| `rf_a`, `rf_b`, `rf_c` | W x 32 | the register file's three read buses |
| `gather_rd` | W x 32 | the gather unit's per-lane return |
| `item` | (W/OCT) x IW x 32 | the four octets' item registers (the walk engine's, one per nonce) |

The operand side, one cycle later: `opv`; `op_ctx`, `op_op`, `op_dst`, `op_imm`, `op_lane_mask`; `opa`, `opb`,
`opc` (W x 32 each); `gaddr` (W x 12), the gather address per lane.

The result side, from the execution units, sampled when `res_v`: `res` (W x 32), `res_dst_sel` (0 register file,
1 reserved, 2 the address path, 3 none), `res_ctx`, `res_dst`, `res_lane_mask`. One cycle later: `rf_wr_v`,
`rf_wr_ctx`, `rf_wr_reg`, `rf_wr_lane_mask`, `rf_wr_data`; `addr_v`, `addr_ctx`, `addr_out`.

`rst` is synchronous, active high, and clears the valids only.

## What the collector does

- Register, immediate (broadcast to every lane), gather return, zero: a five-way select per lane per source.
- The item-indexed source (code 3): lane l reads word `(bus xor imm) mod 64` of its octet's item register, where
  `bus` is that source's register read bus, so `sel_b = 3` with `rf_b = R[src2]` is the reference's
  `W[(R[l][src2] ^ imm) % n_words]`. A 64:1 select of 32 bits per lane, on source b by default.
- The gather address: `gaddr[l] = (rf_b[l] xor imm) mod 4096`, the reference's `T[(R[l][src2] ^ imm) % 4096]`
  index, presented beside the operands for the gather unit; the item index is the same expression's low six bits.
- The per-lane select the grammar's MASK uses: `lane_mask` rides with the instruction to the units and
  `res_lane_mask` returns with the result to become `rf_wr_lane_mask`, the register file's per-lane write enable.
- The write path: the result bus to the register file's write port with its tags, or to the address path (the walk
  engine's next address), or dropped.
- Latency 1 cycle each way; no combinational path from any input to any output. Every data register loads only when
  its path is valid (an idle cycle or an unselected path toggles nothing); data outputs hold their last value when
  their valid is low, which the testbench checks only under the valid.

## What the spec left silent, chosen here

- The parts document's shared-memory source (code 2) and write port (destination code 1) are reserved after the
  addendum removed shared memory: code 2 reads zero, destination 1 drops the result. Codes 6 and 7 read zero.
- The item register is an input bus of the four octets' current items (the walk engine holds the per-context items;
  the collector selects from the issuing context's), not a copy inside the collector.
- The item path exists on source b alone by default (the grammar never draws it on a or c); `ITEM_SRC = 3'b111`
  gives the general crossbar at three times the item-select area.
- The execution units return `res_ctx`, `res_dst` and `res_lane_mask` with `res` (the parts document names only
  `res`, `res_v`, `res_dst_sel`); the tags cross the units with the operation, as the sequencer's scoreboard needs.
- The register index field is `RB` = 5 bits so the 32-register margin fits; the grammar's 16 use the low four.

## Exactness (MEASURED on the box)

| set | cases | issued (operands delivered) | results routed | mismatches |
|---|---|---|---|---|
| random (1,024 random plus 256 corners) | 1,280 | 1,218 (116,928) | 651 (328 register, 323 address) | 0 |
| grammar (version 10's window counts) | 1,024 | 964 (92,544) | 965 (956 register, 9 address) | 0 |

iverilog 12, 4.5 s and 4.0 s; the VCDs at a 1,000 ps clock (ASAP7's constraint) and 2,000 ps (nangate45's).

## Synthesis (SYNTHESISED: yosys 0.68 and abc on the library, the five pitfalls applied, unplaced)

| library | cells | of which flops | area um2 | note |
|---|---|---|---|---|
| ASAP7 RVT, typical, NLDM | 118,292 (plus 5,630 flop-output inverters instanced for OpenSTA) | 5,630 DFFHQNx1 | 9,972 (10,218 with the inverters) | 19,076 port bits |
| nangate45 typical | 111,543 | 5,630 DFF_X1 | 147,961 | |

The flops: 3 x 32 x 32 operand bits, 32 x 12 gather-address bits, 2 x 32 x 32 write-path data bits, the tags and
the valids. The logic: per lane the 64:1 item select (the largest term), the three five-way selects, the index
xor. No latches, no inferred memories.

## Placement (PLACED: OpenROAD 26Q4-110, global and detailed placement, parasitics estimated from the placement)

The block has 19,076 pins (the three read buses, the gather return, the four octets' item registers, the result and
the operand buses), so the pins sit on two horizontal and two vertical layers at one track's spacing (M4, M6 / M5,
M7 on ASAP7; metal3, metal5 / metal2, metal4 on nangate45); at the 30 percent utilisation the floorplan gives the
die is pin-bound neither way. The select and immediate broadcasts fan out to thousands of loads, so the placement is
the buffered form: `repair_design` after global placement (8,222 buffers on 1,627 nets at ASAP7; 7,652 on 2,703 at
nangate45), then detailed placement.

| library | die | design area um2 (utilisation) | clock | timing |
|---|---|---|---|---|
| ASAP7 | 188.6 x 188.6 um | 10,938 (32 percent) | 1,000 ps | NOT MET on the buffered form: wns -778 ps (the item index's 64:1 select fanout, buffered but not sized); the timed form (`repair_timing`, 11,036 um2) wns -95 ps: the achieved clock 1,095 ps, 913 MHz |
| nangate45 | 1,424.6 x 1,424.6 um | 159,973 (32 percent) | 2,000 ps | wns -0.12 ns: the achieved clock 2.12 ns, 471 MHz |

## The placed power (the figure of record: ASAP7, the grammar set, ANNOTATED, 33.1 pJ per instruction cycle, 0.28 pJ per word delivered or routed, 0.37 pJ per operand as the upper bound)

The activity: the grammar set's VCD (random data at version 10's selection mix: 964 instructions issued and 965
results routed in 1,028 cycles) and the random set's (every code uniformly; 1,218 issued, 651 routed in 1,284
cycles), each cut at the platform's own clock period (1,000 ps on ASAP7, 2,000 ps on nangate45) and read at the
same period, so the energy per cycle is the VCD's. The two reads per set: PROPAGATED (the ports from `read_vcd`,
the interior propagated by OpenSTA: not a figure of record, the kit's proof read it 1.98x high on a counter) and
ANNOTATED (the annotation kit: every bit's toggles per simulation cycle from the VCD mapped onto the placed
netlist's nets, the flops by name and their complements, `report_activity_annotation` stating the count). On this
block the interior is the select logic between annotated ports and annotated flops, so the two reads agree within
a few percent; the annotated one is stated first.

The per-word figure: the block's energy per cycle over the words it moves, 96 operands per issued instruction
(three sources across 32 lanes) plus 32 result words per routed result. The last column charges the whole block to
the operands alone, the upper bound of "pJ per operand delivered".

| library, set | read | annotated nets | total mW | pJ per cycle | pJ per word delivered or routed | pJ per operand (upper bound) |
|---|---|---|---|---|---|---|
| ASAP7, grammar | ANNOTATED | 11,164 nets by the kit (5,630 flops by name, 5,534 through their output inverters) + 13,446 port nets by read_vcd; 107,128 unnamed interior nets propagated from them; 8,222 buffer nets unmatched | 33.1 | 33.1 | 0.276 | 0.368 |
| ASAP7, grammar | propagated | | 30.4 | 30.4 | 0.253 | 0.338 |
| ASAP7, random | ANNOTATED | the same counts | 30.4 | 30.4 | 0.283 | 0.334 |
| ASAP7, random | propagated | | 30.4 | 30.4 | 0.283 | 0.334 |
| ASAP7 timed form, grammar | ANNOTATED | 11,164 as above; 8,476 buffer nets unmatched | 33.3 | 33.3 | 0.278 | 0.370 |
| nangate45, grammar | ANNOTATED | 10,339 nets by the kit (5,630 flops by name, 4,709 complements) + 13,446 port nets by read_vcd; 106,834 unnamed interior nets propagated from them; 7,652 buffer nets unmatched | 124.0 | 248.0 | 2.07 | 2.76 |
| nangate45, grammar | propagated | | 116.0 | 232.0 | 1.93 | 2.58 |
| nangate45, random | ANNOTATED | the same counts | 117.0 | 234.0 | 2.18 | 2.57 |
| nangate45, random | propagated | | 116.0 | 232.0 | 2.16 | 2.55 |

The conditions of every placed figure above: ASAP7 the RVT cells at the typical corner of the library (nom_process
1, 0.7 V, 25 C, the NLDM tables); nangate45 the typical library (1.1 V, 25 C). The routing state is PLACED, NOT
ROUTED: global and detailed placement with the buffers of `repair_design`, the wire parasitics estimated from the
placement by the platform's `setRC.tcl` layer resistances and capacitances (`estimate_parasitics -placement`); no
clock tree (the clock is ideal in the reads, so the clock network's own power is absent from the totals and the
flops' clock-pin power is in the sequential internal term); no global or detailed routing. The timing miss at
1 GHz is the integrator's to take as a pipeline stage on the 64:1 item select.

Grades: EXACT (the two sets on the box, 0 mismatches); SYNTHESISED (both libraries); PLACED (both libraries,
buffered, not routed; ASAP7 timing at 1 GHz not met, the achieved clock stated; the activity random data at the
grammar's selection mix, not the reference's trace set, which is the integrator's); the ANNOTATED reads with their
net counts above. The nangate45 figures are the second library's reading, not the chip's. The register file's own
read and write energy is part 1's; the item register's and the gather table's storage are parts 7 and 6; this is
the routing between them.

The run on the box (`flow/run.sh <part dir> <orfs dir>`): the vectors and exactness 20 s, the four VCDs 20 s, the
syntheses 90 s each, the activities 3 min, the placements 4 to 5 min each, each power read 2 to 3 min, the mapping 2
min per set; about 40 minutes end to end on 32 cores.

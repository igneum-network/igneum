// Part 6 of the universal engine: the per-nonce GATHER table (seat LDS). SPEC-R0E-v0.md section 2 and the
// reference's r0e_run_octet: every lane l of an octet reads T[(R[l][src2] xor imm) mod TABLE_WORDS] from its
// nonce's table, two reads per window of 32 operations; the word is xored into R[l][src1] by the integer pipe.
//
// The form: a W-lane group is W / OCT octets (4 at W 32), one nonce each; every octet owns a BANK SET of NB banks,
// word-interleaved on the low BW address bits, each bank holding the rows of the 2^TB tables resident for that
// octet (C = (W / OCT) x 2^TB contexts in flight; at the defaults 16 tables of 16 KB, 256 KB per group). A request
// is one instruction for the whole group: per lane the low AW bits of R[src2], one immediate (xored here on the
// low bits: (R xor imm) mod 2^AW equals (R mod 2^AW) xor (imm mod 2^AW)), per octet the table it reads, a lane
// mask and a tag. The read returns, per lane, the table word; masked lanes return zero.
//
// Timing: a request presented with rd_v while lds_stall is low is accepted at that clock edge. Each following
// cycle is one PASS over the request's pending lanes: in every octet, every bank serves the lowest pending lane
// that names it, plus every other pending lane of the same bank and the same row (a broadcast); pending lanes of
// the same bank at a different row wait for the next pass and lds_stall stays high, so the issuer holds the next
// request. The data of a pass leaves the banks one cycle later and lands in rd_data the cycle after; rd_ready
// is high for one cycle with the last pass's data (rd_tag_out names the request). Latency: passes + 2 cycles
// from the accepted edge to the cycle rd_ready is high (3 conflict-free); throughput one request per cycle when
// conflict-free. A fill write to an octet's bank set takes that set's single port for one cycle: a read pass of
// that octet in that cycle is deferred by one cycle (the fill never stalls; a word written by a fill presented
// in cycle t is seen by every read accepted in cycle t or later).
//
// The fill port: wr_v writes NB consecutive words (one per bank) of table wr_tid of octet wr_oct at the
// NB-aligned address wr_addr (its low BW bits ignored); 2^AW / NB cycles fill one table (512 at the defaults).
// Any fill source drives it (the XOF engine, version 11's slice-and-mix); gather_fill.v adapts a word stream.
//
// Parameters: W lanes, OCT lanes per octet, AW table address bits (12: 16 KB, 10: 4 KB, 9: 2 KB per table),
// NB banks per octet (a power of two, at most OCT is useful), TB table-index bits per octet (2^TB resident
// tables per octet), OW the width of wr_oct (at least log2(W / OCT)), TAGW the tag width.
// Verilog-2005; no combinational path from an input to an output (lds_stall is a function of the state).
module gather_table #(
  parameter W    = 32,
  parameter OCT  = 8,
  parameter AW   = 12,
  parameter NB   = 8,
  parameter TB   = 2,
  parameter OW   = 2,
  parameter TAGW = 8
) (
  input                           clk,
  input                           rst,
  // the read request
  input                           rd_v,
  input  [TAGW - 1:0]             rd_tag,
  input  [(W / OCT) * TB - 1:0]   rd_tid,
  input  [W * AW - 1:0]           rd_addr,
  input  [AW - 1:0]               rd_imm,
  input  [W - 1:0]                lane_mask,
  output                          lds_stall,
  output reg                      rd_ready,
  output reg [TAGW - 1:0]         rd_tag_out,
  output reg [W * 32 - 1:0]       rd_data,
  // the fill port
  input                           wr_v,
  input  [OW - 1:0]               wr_oct,
  input  [TB - 1:0]               wr_tid,
  input  [AW - 1:0]               wr_addr,
  input  [NB * 32 - 1:0]          wr_data
);
  localparam NOCT = W / OCT;
  localparam BW   = $clog2(NB);
  localparam RW   = AW - BW;          // row bits inside one table
  localparam ROWW = TB + RW;          // a bank's address: the table index above the row
  localparam NBK  = NOCT * NB;

  // ---- the request register (one request in flight; its pending lanes drain pass by pass)
  reg                     q_busy;
  reg  [W - 1:0]          q_pend;
  reg  [W - 1:0]          q_mask;
  reg  [W * AW - 1:0]     q_addr;
  reg  [NOCT * TB - 1:0]  q_tid;
  reg  [TAGW - 1:0]       q_tag;

  // ---- the fill stage (a write presented in cycle t takes the octet's banks in cycle t + 1)
  reg                     wq_v;
  reg  [OW - 1:0]         wq_oct;
  reg  [TB - 1:0]         wq_tid;
  reg  [RW - 1:0]         wq_row;
  reg  [NB * 32 - 1:0]    wq_data;

  // ---- per lane: the bank it names and the row inside the bank (the table index above the row)
  wire [W * BW - 1:0]     l_bank;
  wire [W * ROWW - 1:0]   l_row;
  genvar gl;
  generate
    for (gl = 0; gl < W; gl = gl + 1) begin : lane_fields
      assign l_bank[gl * BW +: BW] = q_addr[gl * AW +: BW];
      assign l_row[gl * ROWW +: ROWW] = {q_tid[(gl / OCT) * TB +: TB], q_addr[gl * AW + BW +: RW]};
    end
  endgenerate

  // ---- the pass: per octet and bank, the lowest pending lane wins, same-row lanes ride along
  reg  [W - 1:0]          served;
  reg  [NBK - 1:0]        b_en;
  reg  [NBK * ROWW - 1:0] b_addr;
  reg  [ROWW - 1:0]       wrow;
  reg                     found;
  integer o, b, l;
  always @(*) begin
    served = {W{1'b0}};
    b_en = {NBK{1'b0}};
    b_addr = {(NBK * ROWW){1'b0}};
    for (o = 0; o < NOCT; o = o + 1) begin
      for (b = 0; b < NB; b = b + 1) begin
        found = 1'b0;
        wrow = {ROWW{1'b0}};
        for (l = o * OCT; l < (o + 1) * OCT; l = l + 1) begin
          if (q_pend[l] && (l_bank[l * BW +: BW] == b[BW - 1:0]) && !found) begin
            found = 1'b1;
            wrow = l_row[l * ROWW +: ROWW];
          end
        end
        // a fill cycle on this octet's bank set holds the pass (the banks are single-port)
        if (found && !(wq_v && (wq_oct == o[OW - 1:0]))) begin
          b_en[o * NB + b] = 1'b1;
          b_addr[(o * NB + b) * ROWW +: ROWW] = wrow;
          for (l = o * OCT; l < (o + 1) * OCT; l = l + 1) begin
            if (q_pend[l] && (l_bank[l * BW +: BW] == b[BW - 1:0]) && (l_row[l * ROWW +: ROWW] == wrow)) served[l] = 1'b1;
          end
        end
      end
    end
  end
  wire [W - 1:0] pend_next = q_pend & ~served;
  wire           more      = |pend_next;
  assign lds_stall = q_busy & more;

  always @(posedge clk) begin
    if (rst) begin
      q_busy <= 1'b0;
      q_pend <= {W{1'b0}};
    end else if (rd_v && !lds_stall) begin
      q_busy <= 1'b1;
      q_pend <= lane_mask;
      q_mask <= lane_mask;
      q_addr <= rd_addr ^ {W{rd_imm}};
      q_tid  <= rd_tid;
      q_tag  <= rd_tag;
    end else if (q_busy) begin
      q_pend <= pend_next;
      if (!more) q_busy <= 1'b0;
    end
  end

  always @(posedge clk) begin
    if (rst) wq_v <= 1'b0;
    else wq_v <= wr_v;
    if (wr_v) begin
      wq_oct  <= wr_oct;
      wq_tid  <= wr_tid;
      wq_row  <= wr_addr[AW - 1:BW];
      wq_data <= wr_data;
    end
  end

  // ---- the banks: NOCT sets of NB, each 2^ROWW words
  wire [NBK * 32 - 1:0] bq;
  genvar go, gb;
  generate
    for (go = 0; go < NOCT; go = go + 1) begin : oset
      for (gb = 0; gb < NB; gb = gb + 1) begin : bank
        wire fill = wq_v && (wq_oct == go);
        wire [ROWW - 1:0] a = fill ? {wq_tid, wq_row} : b_addr[(go * NB + gb) * ROWW +: ROWW];
        gather_bank #(.AW(ROWW), .DW(32)) u (
          .clk(clk), .ce(fill | b_en[go * NB + gb]), .we(fill), .addr(a),
          .wd(wq_data[gb * 32 +: 32]), .q(bq[(go * NB + gb) * 32 +: 32]));
      end
    end
  endgenerate

  // ---- the pass's bookkeeping travels one cycle with the banks' read
  reg                 s1_v, s1_last;
  reg  [W - 1:0]      s1_served, s1_mask;
  reg  [W * BW - 1:0] s1_bank;
  reg  [TAGW - 1:0]   s1_tag;
  always @(posedge clk) begin
    if (rst) s1_v <= 1'b0;
    else s1_v <= q_busy;
    s1_last   <= !more;
    s1_served <= served;
    s1_mask   <= q_mask;
    s1_bank   <= l_bank;
    s1_tag    <= q_tag;
  end

  // ---- the return: every served lane takes its bank's word; the last pass zeroes the masked lanes
  integer k;
  always @(posedge clk) begin
    if (rst) rd_ready <= 1'b0;
    else rd_ready <= s1_v & s1_last;
    if (s1_v) begin
      rd_tag_out <= s1_tag;
      for (k = 0; k < W; k = k + 1) begin
        if (s1_served[k]) rd_data[k * 32 +: 32] <= bq[((k / OCT) * NB + s1_bank[k * BW +: BW]) * 32 +: 32];
        else if (s1_last && !s1_mask[k]) rd_data[k * 32 +: 32] <= 32'd0;
      end
    end
  end
endmodule

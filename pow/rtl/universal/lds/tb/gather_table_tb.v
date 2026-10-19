// The testbench of part 6 (the GATHER table): iverilog, the vectors of vectors/gen.py (cut from ref.py).
//   iverilog -g2005 -o gather_table_tb tb/gather_table_tb.v rtl/gather_table.v rtl/gather_bank.v rtl/gather_fill.v
//   vvp gather_table_tb +fill=vectors/fill.hex +ops=vectors/ops.hex +expect=vectors/expect.hex [+vcd]
// Four runs in one: (1) the fill: the tables of octets 0 and 1 through the wide write port, those of octets 2 and
// 3 through the gather_fill adapter from a word stream; (2) every request isolated: the words of all 32 lanes
// and the tag against the expectation, and the cycle count from the accepted edge to rd_ready against
// passes + 2; (3) every request streamed back to back under lds_stall with an idempotent fill write injected
// every fifth cycle (the deferral path exercised, the words unchanged), every return checked in order (this run
// is the VCD for the power read: +vcd); (4) the fill visibility rule: a fill presented in the same cycle as a
// read of its words is seen by that read (one deferral cycle), then the words restored and re-read.
// The clock is 1 GHz at timescale 1ns/1ps (HALF 0.5) unless -P gather_table_tb.HALF=1.0 (500 MHz): the VCD clock of the power read.
`timescale 1ns/1ps
module gather_table_tb;
  parameter W = 32, OCT = 8, AW = 12, NB = 8, TB = 2, OW = 2, TAGW = 8;
  parameter real HALF = 0.5;   // the half period in ns: 0.5 is the 1 GHz clock of the ASAP7 read, 1.0 the 500 MHz of nangate45
  localparam NOCT = W / OCT, NTO = 1 << TB, N = 1 << AW, MAXREQ = 8192, BW = $clog2(NB);

  reg clk = 1'b0;
  always #HALF clk = ~clk;
  reg rst = 1'b1;

  reg                        rd_v = 1'b0;
  reg  [TAGW - 1:0]          rd_tag = 0;
  reg  [NOCT * TB - 1:0]     rd_tid = 0;
  reg  [W * AW - 1:0]        rd_addr = 0;
  reg  [AW - 1:0]            rd_imm = 0;
  reg  [W - 1:0]             lane_mask = 0;
  wire                       lds_stall, rd_ready;
  wire [TAGW - 1:0]          rd_tag_out;
  wire [W * 32 - 1:0]        rd_data;
  reg                        wr_v = 1'b0;
  reg  [OW - 1:0]            wr_oct = 0;
  reg  [TB - 1:0]            wr_tid = 0;
  reg  [AW - 1:0]            wr_addr = 0;
  reg  [NB * 32 - 1:0]       wr_data = 0;
  // the adapter
  reg                        f_start = 1'b0, f_in_v = 1'b0, use_fill = 1'b0;
  reg  [OW - 1:0]            f_oct = 0;
  reg  [TB - 1:0]            f_tid = 0;
  reg  [31:0]                f_word = 0;
  wire                       f_ready, f_done, f_wr_v;
  wire [OW - 1:0]            f_wr_oct;
  wire [TB - 1:0]            f_wr_tid;
  wire [AW - 1:0]            f_wr_addr;
  wire [NB * 32 - 1:0]       f_wr_data;
  gather_fill #(.AW(AW), .NB(NB), .TB(TB), .OW(OW)) u_fill (
    .clk(clk), .rst(rst), .start(f_start), .start_oct(f_oct), .start_tid(f_tid), .in_v(f_in_v), .in_word(f_word),
    .in_ready(f_ready), .done(f_done), .wr_v(f_wr_v), .wr_oct(f_wr_oct), .wr_tid(f_wr_tid), .wr_addr(f_wr_addr), .wr_data(f_wr_data));
  wire                 d_wr_v    = use_fill ? f_wr_v : wr_v;
  wire [OW - 1:0]      d_wr_oct  = use_fill ? f_wr_oct : wr_oct;
  wire [TB - 1:0]      d_wr_tid  = use_fill ? f_wr_tid : wr_tid;
  wire [AW - 1:0]      d_wr_addr = use_fill ? f_wr_addr : wr_addr;
  wire [NB * 32 - 1:0] d_wr_data = use_fill ? f_wr_data : wr_data;

  gather_table #(.W(W), .OCT(OCT), .AW(AW), .NB(NB), .TB(TB), .OW(OW), .TAGW(TAGW)) dut (
    .clk(clk), .rst(rst),
    .rd_v(rd_v), .rd_tag(rd_tag), .rd_tid(rd_tid), .rd_addr(rd_addr), .rd_imm(rd_imm), .lane_mask(lane_mask),
    .lds_stall(lds_stall), .rd_ready(rd_ready), .rd_tag_out(rd_tag_out), .rd_data(rd_data),
    .wr_v(d_wr_v), .wr_oct(d_wr_oct), .wr_tid(d_wr_tid), .wr_addr(d_wr_addr), .wr_data(d_wr_data));

  reg [447:0]  ops [0:MAXREQ - 1];
  reg [1031:0] exp [0:MAXREQ - 1];
  reg [31:0]   fill [0:NOCT * NTO * N - 1];
  reg [1023:0] fn;
  integer nreq, i, j, l, o, t, a, cyc, mism, words, lanes_served, passes_sum, seed;
  integer s_sent, s_got, s_mism;
  reg [1023:0] want;
  reg vcd;

  task present(input integer idx);
    begin
      rd_tag = ops[idx][443:436];
      rd_imm = ops[idx][424 +: AW];
      lane_mask = ops[idx][423:392];
      rd_tid = ops[idx][384 +: NOCT * TB];
      for (l = 0; l < W; l = l + 1) rd_addr[l * AW +: AW] = ops[idx][12 * l +: AW];
      rd_v = 1'b1;
    end
  endtask

  task write8(input integer oo, input integer tt, input integer aa);
    integer q;
    begin
      wr_v = 1'b1; wr_oct = oo; wr_tid = tt; wr_addr = aa;
      for (q = 0; q < NB; q = q + 1) wr_data[q * 32 +: 32] = fill[(oo * NTO + tt) * N + aa + q];
    end
  endtask

  task check(input integer idx, input integer cycles, input integer cyc_check);
    integer q;
    begin
      want = exp[idx][1023:0];
      for (q = 0; q < W; q = q + 1) begin
        words = words + 1;
        if (rd_data[q * 32 +: 32] !== want[q * 32 +: 32]) begin
          mism = mism + 1;
          if (mism <= 10) $display("MISMATCH request %0d lane %0d: got %08x want %08x", idx, q, rd_data[q * 32 +: 32], want[q * 32 +: 32]);
        end
      end
      if (rd_tag_out !== ops[idx][443:436]) begin mism = mism + 1; if (mism <= 10) $display("MISMATCH request %0d tag %0h want %0h", idx, rd_tag_out, ops[idx][443:436]); end
      if (cyc_check && cycles != exp[idx][1031:1024] + 2) begin
        mism = mism + 1;
        if (mism <= 10) $display("MISMATCH request %0d latency %0d cycles, want passes %0d + 2", idx, cycles, exp[idx][1031:1024]);
      end
    end
  endtask

  // the streamed checker: every rd_ready is the next request's return
  always @(negedge clk) begin
    if (s_sent >= 0 && rd_ready) begin
      if (s_got < nreq) begin
        want = exp[s_got][1023:0];
        for (j = 0; j < W; j = j + 1) if (rd_data[j * 32 +: 32] !== want[j * 32 +: 32]) begin
          s_mism = s_mism + 1;
          if (s_mism <= 10) $display("STREAM MISMATCH request %0d lane %0d: got %08x want %08x", s_got, j, rd_data[j * 32 +: 32], want[j * 32 +: 32]);
        end
        if (rd_tag_out !== ops[s_got][443:436]) s_mism = s_mism + 1;
      end else s_mism = s_mism + 1;
      s_got = s_got + 1;
    end
  end

  reg acc;
  initial begin
    s_sent = -1; s_got = 0; s_mism = 0; mism = 0; words = 0; passes_sum = 0; seed = 7;
    vcd = $test$plusargs("vcd");
    if (!$value$plusargs("fill=%s", fn)) fn = "vectors/fill.hex";
    $readmemh(fn, fill);
    if (!$value$plusargs("ops=%s", fn)) fn = "vectors/ops.hex";
    $readmemh(fn, ops);
    if (!$value$plusargs("expect=%s", fn)) fn = "vectors/expect.hex";
    $readmemh(fn, exp);
    nreq = 0;
    while (nreq < MAXREQ && ^ops[nreq] !== 1'bx) nreq = nreq + 1;
    $display("gather_table_tb: W %0d AW %0d NB %0d TB %0d; %0d requests", W, AW, NB, TB, nreq);
    repeat (3) @(negedge clk);
    rst = 1'b0;
    // (1) the fill: octets 0 and 1 by the wide port, 2 and 3 (the upper half) by the adapter's word stream
    for (o = 0; o < NOCT; o = o + 1) begin
      for (t = 0; t < NTO; t = t + 1) begin
        if (o < (NOCT + 1) / 2) begin
          for (a = 0; a < N; a = a + NB) begin @(negedge clk); write8(o, t, a); end
          @(negedge clk); wr_v = 1'b0;
        end else begin
          @(negedge clk); use_fill = 1'b1; f_start = 1'b1; f_oct = o; f_tid = t;
          @(negedge clk); f_start = 1'b0;
          for (a = 0; a < N; a = a + 1) begin
            f_in_v = 1'b1; f_word = fill[(o * NTO + t) * N + a];
            @(negedge clk);
          end
          f_in_v = 1'b0;
          while (!f_done) @(negedge clk);
          @(negedge clk); use_fill = 1'b0;
        end
      end
    end
    $display("fill done at %0t", $time);
    // (2) isolated
    for (i = 0; i < nreq; i = i + 1) begin
      @(negedge clk); present(i);
      while (lds_stall) @(negedge clk);
      @(negedge clk); rd_v = 1'b0; cyc = 1;
      while (!rd_ready) begin @(negedge clk); cyc = cyc + 1; end
      check(i, cyc, 1);
      passes_sum = passes_sum + exp[i][1031:1024];
    end
    $display("isolated: %0d requests, %0d words, %0d mismatches, mean passes %0d.%02d", nreq, words, mism, passes_sum / nreq, (100 * passes_sum / nreq) % 100);
    // (3) streamed, with the VCD
    if (vcd) begin $dumpfile("gather_table_tb.vcd"); $dumpvars(0, dut); end
    @(negedge clk); s_sent = 0; present(0); acc = !lds_stall; i = 0; cyc = 0;
    while (s_got < nreq) begin
      @(negedge clk);
      cyc = cyc + 1;
      if (acc) begin
        i = i + 1;
        if (i < nreq) present(i); else rd_v = 1'b0;
      end
      acc = rd_v && !lds_stall;
      // an idempotent fill every fifth cycle: the deferral path, the words unchanged
      if (cyc % 5 == 0) begin
        o = $unsigned($random(seed)) % NOCT; t = $unsigned($random(seed)) % NTO; a = ($unsigned($random(seed)) % (N / NB)) * NB;
        write8(o, t, a);
      end else wr_v = 1'b0;
    end
    wr_v = 1'b0;
    if (vcd) $dumpoff;
    $display("streamed: %0d requests returned in %0d cycles, %0d mismatches", s_got, cyc, s_mism);
    mism = mism + s_mism; s_sent = -1;
    // (4) the fill visibility rule on octet 1, table 2, words 256 to 263
    @(negedge clk);
    wr_v = 1'b1; wr_oct = 1; wr_tid = 2; wr_addr = 256;
    for (j = 0; j < NB; j = j + 1) wr_data[j * 32 +: 32] = 32'hC0DE0000 + j;
    rd_tag = 8'hA5; rd_imm = 0; lane_mask = 32'h0000FF00; rd_tid = 0; rd_tid[1 * TB +: TB] = 2;
    for (l = 0; l < W; l = l + 1) rd_addr[l * AW +: AW] = 256 + (l % OCT);
    rd_v = 1'b1;
    @(negedge clk); rd_v = 1'b0; wr_v = 1'b0; cyc = 1;
    while (!rd_ready) begin @(negedge clk); cyc = cyc + 1; end
    for (l = 0; l < W; l = l + 1) begin
      want[31:0] = (l >= 8 && l < 16) ? (32'hC0DE0000 + (l - 8)) : 32'd0;
      if (rd_data[l * 32 +: 32] !== want[31:0]) begin mism = mism + 1; $display("VISIBILITY MISMATCH lane %0d got %08x want %08x", l, rd_data[l * 32 +: 32], want[31:0]); end
    end
    if (cyc != 4) begin mism = mism + 1; $display("VISIBILITY MISMATCH latency %0d, want 4 (one deferral)", cyc); end
    // restore and re-read
    @(negedge clk); write8(1, 2, 256);
    @(negedge clk); wr_v = 1'b0; rd_v = 1'b1;
    @(negedge clk); rd_v = 1'b0; cyc = 1;
    while (!rd_ready) begin @(negedge clk); cyc = cyc + 1; end
    for (l = 8; l < 16; l = l + 1)
      if (rd_data[l * 32 +: 32] !== fill[(1 * NTO + 2) * N + 256 + (l - 8)]) begin mism = mism + 1; $display("RESTORE MISMATCH lane %0d", l); end
    if (cyc != 3) begin mism = mism + 1; $display("RESTORE MISMATCH latency %0d, want 3", cyc); end
    $display("RESULT gather_table: %0d requests x 2 runs, %0d words, %0d mismatches", nreq, words * 2, mism);
    $finish;
  end
endmodule

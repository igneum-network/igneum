// The operand collector against ref.py's vectors: one case per cycle back to back, every output checked one cycle
// later (the forward and the write path both at latency 1); a reset in the middle of the set checks the valids
// drop and the pipeline restarts. +hex=<file> the vector set (ref.py's layout), +vcd writes oc_tb.vcd over the
// dut, +half=<ps> the clock half period in picoseconds (default 500: a 1 ns clock, the 1 GHz constraint's).
//   iverilog -g2005 -o oc_tb tb/oc_tb.v rtl/oc.v && vvp oc_tb +hex=vectors/random.hex
`timescale 1ps/1ps
module oc_tb;
  localparam W = 32, OCT = 8, IW = 64, RB = 5;
  localparam NI = (W/OCT)*IW;
  localparam WPC = 5 + 4*W + NI + W + 4*W + 1;
  integer half = 500;
  reg clk = 0;
  always #(half) clk = ~clk;
  reg rst = 1;
  reg issue_v = 0; reg [3:0] ctx = 0; reg [4:0] op = 0; reg [RB-1:0] dst = 0; reg [31:0] imm = 0; reg [W-1:0] lane_mask = 0;
  reg [2:0] sel_a = 0, sel_b = 0, sel_c = 0;
  reg [W*32-1:0] rf_a = 0, rf_b = 0, rf_c = 0, gather_rd = 0, res = 0;
  reg [NI*32-1:0] item = 0;
  reg res_v = 0; reg [1:0] res_dst_sel = 0; reg [3:0] res_ctx = 0; reg [RB-1:0] res_dst = 0; reg [W-1:0] res_lane_mask = 0;
  wire opv; wire [3:0] op_ctx; wire [4:0] op_op; wire [RB-1:0] op_dst; wire [31:0] op_imm; wire [W-1:0] op_lane_mask;
  wire [W*32-1:0] opa, opb, opc; wire [W*12-1:0] gaddr;
  wire rf_wr_v; wire [3:0] rf_wr_ctx; wire [RB-1:0] rf_wr_reg; wire [W-1:0] rf_wr_lane_mask; wire [W*32-1:0] rf_wr_data;
  wire addr_v; wire [3:0] addr_ctx; wire [W*32-1:0] addr_out;
  oc #(.W(W), .OCT(OCT), .IW(IW), .RB(RB)) dut(
    .clk(clk), .rst(rst), .issue_v(issue_v), .ctx(ctx), .op(op), .dst(dst), .imm(imm), .lane_mask(lane_mask),
    .sel_a(sel_a), .sel_b(sel_b), .sel_c(sel_c), .rf_a(rf_a), .rf_b(rf_b), .rf_c(rf_c), .gather_rd(gather_rd), .item(item),
    .opv(opv), .op_ctx(op_ctx), .op_op(op_op), .op_dst(op_dst), .op_imm(op_imm), .op_lane_mask(op_lane_mask),
    .opa(opa), .opb(opb), .opc(opc), .gaddr(gaddr),
    .res_v(res_v), .res_dst_sel(res_dst_sel), .res_ctx(res_ctx), .res_dst(res_dst), .res_lane_mask(res_lane_mask), .res(res),
    .rf_wr_v(rf_wr_v), .rf_wr_ctx(rf_wr_ctx), .rf_wr_reg(rf_wr_reg), .rf_wr_lane_mask(rf_wr_lane_mask), .rf_wr_data(rf_wr_data),
    .addr_v(addr_v), .addr_ctx(addr_ctx), .addr_out(addr_out));

  reg [31:0] mem [0:4 + 1400 * WPC - 1];
  reg [1023:0] hexfile;
  integer n, i, k, base, errs, checks, issued, results, operands;
  // the pending case (applied last edge) for the check this edge
  reg p_valid; reg [31:0] p_ctrl, p_lane_mask, p_imm, p_res_lane_mask, p_res_dst; integer p_base;
  reg [31:0] x;

  task apply(input integer b);
    begin
      x = mem[b];
      issue_v = x[0]; sel_a = x[3:1]; sel_b = x[6:4]; sel_c = x[9:7]; ctx = x[13:10]; dst = x[18:14]; op = x[23:19];
      res_v = x[24]; res_dst_sel = x[26:25]; res_ctx = x[30:27];
      lane_mask = mem[b + 1]; imm = mem[b + 2]; res_lane_mask = mem[b + 3]; res_dst = mem[b + 4];
      for (k = 0; k < W; k = k + 1) begin
        rf_a[32*k +: 32] = mem[b + 5 + k]; rf_b[32*k +: 32] = mem[b + 5 + W + k]; rf_c[32*k +: 32] = mem[b + 5 + 2*W + k];
        gather_rd[32*k +: 32] = mem[b + 5 + 3*W + k];
        res[32*k +: 32] = mem[b + 5 + 4*W + NI + k];
      end
      for (k = 0; k < NI; k = k + 1) item[32*k +: 32] = mem[b + 5 + 4*W + k];
    end
  endtask

  task check(input integer b);
    integer e; reg [31:0] c, wb;
    begin
      e = b + 5 + 4*W + NI + W; c = mem[b]; wb = mem[e + 4*W];
      checks = checks + 1;
      if (opv !== c[0]) begin errs = errs + 1; if (errs < 10) $display("case %0d: opv %b want %b", i, opv, c[0]); end
      if (c[0]) begin
        issued = issued + 1; operands = operands + 3 * W;
        if (op_ctx !== c[13:10] || op_dst !== c[18:14] || op_op !== c[23:19] || op_imm !== mem[b + 2] || op_lane_mask !== mem[b + 1]) begin
          errs = errs + 1; if (errs < 10) $display("case %0d: tags ctx %0d op %0d dst %0d imm %08x mask %08x", i, op_ctx, op_op, op_dst, op_imm, op_lane_mask); end
        for (k = 0; k < W; k = k + 1) begin
          if (opa[32*k +: 32] !== mem[e + k]) begin errs = errs + 1; if (errs < 10) $display("case %0d lane %0d opa %08x want %08x (sel %0d)", i, k, opa[32*k +: 32], mem[e + k], c[3:1]); end
          if (opb[32*k +: 32] !== mem[e + W + k]) begin errs = errs + 1; if (errs < 10) $display("case %0d lane %0d opb %08x want %08x (sel %0d)", i, k, opb[32*k +: 32], mem[e + W + k], c[6:4]); end
          if (opc[32*k +: 32] !== mem[e + 2*W + k]) begin errs = errs + 1; if (errs < 10) $display("case %0d lane %0d opc %08x want %08x (sel %0d)", i, k, opc[32*k +: 32], mem[e + 2*W + k], c[9:7]); end
          if ({20'd0, gaddr[12*k +: 12]} !== mem[e + 3*W + k]) begin errs = errs + 1; if (errs < 10) $display("case %0d lane %0d gaddr %03x want %08x", i, k, gaddr[12*k +: 12], mem[e + 3*W + k]); end
        end
      end
      if (rf_wr_v !== wb[0] || addr_v !== wb[1]) begin errs = errs + 1; if (errs < 10) $display("case %0d: rf_wr_v %b addr_v %b want %b %b", i, rf_wr_v, addr_v, wb[0], wb[1]); end
      if (wb[0]) begin
        results = results + 1;
        if (rf_wr_reg !== wb[6:2] || rf_wr_ctx !== wb[10:7] || rf_wr_lane_mask !== mem[b + 3]) begin errs = errs + 1; if (errs < 10) $display("case %0d: rf write tags", i); end
        for (k = 0; k < W; k = k + 1) if (rf_wr_data[32*k +: 32] !== mem[b + 5 + 4*W + NI + k]) begin errs = errs + 1; if (errs < 10) $display("case %0d lane %0d rf_wr_data %08x want %08x", i, k, rf_wr_data[32*k +: 32], mem[b + 5 + 4*W + NI + k]); end
      end
      if (wb[1]) begin
        results = results + 1;
        if (addr_ctx !== wb[10:7]) begin errs = errs + 1; if (errs < 10) $display("case %0d: addr ctx", i); end
        for (k = 0; k < W; k = k + 1) if (addr_out[32*k +: 32] !== mem[b + 5 + 4*W + NI + k]) begin errs = errs + 1; if (errs < 10) $display("case %0d lane %0d addr_out %08x want %08x", i, k, addr_out[32*k +: 32], mem[b + 5 + 4*W + NI + k]); end
      end
    end
  endtask

  initial begin
    if (!$value$plusargs("hex=%s", hexfile)) hexfile = "random.hex";
    if (!$value$plusargs("half=%d", half)) half = 500;
    $readmemh(hexfile, mem);
    n = mem[0];
    if (mem[1] !== W || mem[2] !== IW) begin $display("vector set W %0d IW %0d, dut W %0d IW %0d", mem[1], mem[2], W, IW); $fatal(1, "SHAPE"); end
    errs = 0; checks = 0; issued = 0; results = 0; operands = 0; p_valid = 0;
    if ($test$plusargs("vcd")) begin $dumpfile("oc_tb.vcd"); $dumpvars(0, dut); end
    repeat (2) @(negedge clk);
    rst = 0;
    for (i = 0; i <= n; i = i + 1) begin
      @(negedge clk);
      if (p_valid) check(p_base);
      if (i == n / 2) begin
        // a reset mid-stream: the valids fall on the next edge, the data holds
        issue_v = 0; res_v = 0; rst = 1; p_valid = 0; @(negedge clk);
        if (opv !== 1'b0 || rf_wr_v !== 1'b0 || addr_v !== 1'b0) begin errs = errs + 1; $display("reset: valids high"); end
        rst = 0;
      end
      if (i < n) begin base = 4 + i * WPC; apply(base); p_base = base; p_valid = 1; end
      else begin issue_v = 0; res_v = 0; p_valid = 0; end
    end
    @(negedge clk);
    $display("oc_tb: %s cases %0d checks %0d issued %0d operands %0d results %0d mismatches %0d (clock %0d ps)", hexfile, n, checks, issued, operands, results, errs, 2 * half);
    if (errs != 0) $fatal(1, "MISMATCH");
    $finish;
  end
endmodule

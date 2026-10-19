// The operand collector and routing crossbar (part 2 of the universal specialist engine; the parts document with the
// addendum of 12 October 2026 and the version 10 reference's r0e_run_octet). Verilog-2005, synthesisable.
//
// The forward path (latency 1 cycle): for one instruction of one context across W lanes, each source a, b, c is
// chosen by a 3-bit code: 0 the register file's read bus for that source, 1 the immediate broadcast, 2 reserved
// (the shared-memory source the addendum removed; reads zero), 3 the item (the lane's OCTET's 256-byte item register
// of IW = 64 words at index (bus xor imm) mod 64; on the sources ITEM_SRC names, a bit each for a, b, c; elsewhere
// zero), 4 the gather unit's per-lane return, 5 to 7 zero. Beside the operands: gaddr, the per-lane gather address
// (bus b xor imm) mod 4096, and the tags (ctx, op, dst, imm, lane mask) aligned with the operands.
// The write path (latency 1 cycle): the result bus routed by res_dst_sel: 0 the register file's write port (with ctx,
// the destination register and the lane mask), 1 reserved (the shared-memory port, removed; dropped), 2 the address
// path (the walk engine's next address), 3 none. Every data register loads only when its path is valid, so an idle
// cycle or an unselected path costs no toggles; data outputs hold their last value when not valid.
module oc #(
  parameter W = 32,          // lanes
  parameter OCT = 8,         // lanes per octet (one nonce)
  parameter IW = 64,         // item words per octet (256 bytes)
  parameter RB = 5,          // register index bits (16 registers is the grammar's size, 32 the margin)
  parameter ITEM_SRC = 3'b010 // the item path on source b only (the grammar's second source)
) (
  input                       clk,
  input                       rst,
  // the issue side
  input                       issue_v,
  input  [3:0]                ctx,
  input  [4:0]                op,
  input  [RB-1:0]             dst,
  input  [31:0]               imm,
  input  [W-1:0]              lane_mask,
  input  [2:0]                sel_a,
  input  [2:0]                sel_b,
  input  [2:0]                sel_c,
  input  [W*32-1:0]           rf_a,
  input  [W*32-1:0]           rf_b,
  input  [W*32-1:0]           rf_c,
  input  [W*32-1:0]           gather_rd,
  input  [(W/OCT)*IW*32-1:0]  item,
  output reg                  opv,
  output reg [3:0]            op_ctx,
  output reg [4:0]            op_op,
  output reg [RB-1:0]         op_dst,
  output reg [31:0]           op_imm,
  output reg [W-1:0]          op_lane_mask,
  output [W*32-1:0]           opa,
  output [W*32-1:0]           opb,
  output [W*32-1:0]           opc,
  output [W*12-1:0]           gaddr,
  // the result side
  input                       res_v,
  input  [1:0]                res_dst_sel,
  input  [3:0]                res_ctx,
  input  [RB-1:0]             res_dst,
  input  [W-1:0]              res_lane_mask,
  input  [W*32-1:0]           res,
  output reg                  rf_wr_v,
  output reg [3:0]            rf_wr_ctx,
  output reg [RB-1:0]         rf_wr_reg,
  output reg [W-1:0]          rf_wr_lane_mask,
  output reg [W*32-1:0]       rf_wr_data,
  output reg                  addr_v,
  output reg [3:0]            addr_ctx,
  output reg [W*32-1:0]       addr_out
);
  localparam SRC_REG = 3'd0, SRC_IMM = 3'd1, SRC_ITEM = 3'd3, SRC_GATHER = 3'd4;
  localparam DST_REG = 2'd0, DST_ADDR = 2'd2;

  // the tags
  always @(posedge clk) begin
    if (rst) opv <= 1'b0; else opv <= issue_v;
    if (issue_v) begin
      op_ctx <= ctx; op_op <= op; op_dst <= dst; op_imm <= imm; op_lane_mask <= lane_mask;
    end
  end

  // one source for one lane: the five-way select with the octet's 64:1 item select by the register-drawn index
  genvar l;
  generate
    for (l = 0; l < W; l = l + 1) begin : lane
      wire [31:0] ra = rf_a[32*l +: 32];
      wire [31:0] rb = rf_b[32*l +: 32];
      wire [31:0] rc = rf_c[32*l +: 32];
      wire [31:0] g  = gather_rd[32*l +: 32];
      wire [IW*32-1:0] it = item[(l/OCT)*IW*32 +: IW*32];
      wire [5:0] ia = ra[5:0] ^ imm[5:0];
      wire [5:0] ib = rb[5:0] ^ imm[5:0];
      wire [5:0] ic = rc[5:0] ^ imm[5:0];
      wire [31:0] item_a = ITEM_SRC[0] ? it[32*ia +: 32] : 32'd0;
      wire [31:0] item_b = ITEM_SRC[1] ? it[32*ib +: 32] : 32'd0;
      wire [31:0] item_c = ITEM_SRC[2] ? it[32*ic +: 32] : 32'd0;
      reg [31:0] va, vb, vc;
      always @(*) begin
        case (sel_a)
          SRC_REG:    va = ra;
          SRC_IMM:    va = imm;
          SRC_ITEM:   va = item_a;
          SRC_GATHER: va = g;
          default:    va = 32'd0;
        endcase
        case (sel_b)
          SRC_REG:    vb = rb;
          SRC_IMM:    vb = imm;
          SRC_ITEM:   vb = item_b;
          SRC_GATHER: vb = g;
          default:    vb = 32'd0;
        endcase
        case (sel_c)
          SRC_REG:    vc = rc;
          SRC_IMM:    vc = imm;
          SRC_ITEM:   vc = item_c;
          SRC_GATHER: vc = g;
          default:    vc = 32'd0;
        endcase
      end
      reg [31:0] qa, qb, qc;
      reg [11:0] qg;
      always @(posedge clk) begin
        if (issue_v) begin
          qa <= va; qb <= vb; qc <= vc;
          qg <= rb[11:0] ^ imm[11:0];
        end
      end
      assign opa[32*l +: 32] = qa;
      assign opb[32*l +: 32] = qb;
      assign opc[32*l +: 32] = qc;
      assign gaddr[12*l +: 12] = qg;
    end
  endgenerate

  // the write path
  wire to_rf   = res_v && (res_dst_sel == DST_REG);
  wire to_addr = res_v && (res_dst_sel == DST_ADDR);
  always @(posedge clk) begin
    if (rst) begin rf_wr_v <= 1'b0; addr_v <= 1'b0; end
    else begin rf_wr_v <= to_rf; addr_v <= to_addr; end
    if (to_rf) begin
      rf_wr_data <= res; rf_wr_ctx <= res_ctx; rf_wr_reg <= res_dst; rf_wr_lane_mask <= res_lane_mask;
    end
    if (to_addr) begin
      addr_out <= res; addr_ctx <= res_ctx;
    end
  end
endmodule

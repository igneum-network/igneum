// The gather bank as the nangate45 kit's SRAM macro (fakeram45_2048x39: 2,048 words, 39 bits, one port, a write mask held all ones): the bank of
// the default configuration (TB 2 tables of 2^(12 - 3) rows: 2,048 rows; 32 of the 39 bits used). The placed flow
// reads this file in place of rtl/gather_bank.v; a different AW, NB or TB needs the kit macro of its row count.
module gather_bank #(parameter AW = 11, parameter DW = 32) (
  input               clk,
  input               ce,
  input               we,
  input  [AW - 1:0]   addr,
  input  [DW - 1:0]   wd,
  output [DW - 1:0]   q
);
  wire [38:0] rd_out;
  fakeram45_2048x39 u_m (.rd_out(rd_out), .addr_in(addr), .we_in(we), .wd_in({7'd0, wd}), .w_mask_in({39{1'b1}}), .clk(clk), .ce_in(ce));
  assign q = rd_out[31:0];
endmodule

// One bank of the gather table: a single-port synchronous memory of 2^AW words of DW bits (the kit's SRAM macro
// in the placed flow; this behavioural form for exactness and for the flop-array cost reading). One port: a write
// or a read per cycle under ce; a read presents q after the clock edge and holds it until the next read. The same
// semantics as the kit's fakeram model (a read in a write cycle returns the old word; the table never does both).
module gather_bank #(parameter AW = 11, parameter DW = 32) (
  input               clk,
  input               ce,
  input               we,
  input  [AW - 1:0]   addr,
  input  [DW - 1:0]   wd,
  output reg [DW - 1:0] q
);
  reg [DW - 1:0] mem [0:(1 << AW) - 1];
  always @(posedge clk) begin
    if (ce) begin
      if (we) mem[addr] <= wd;
      q <= mem[addr];
    end
  end
endmodule

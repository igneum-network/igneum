// The fill adapter of the gather table: a one-word-per-cycle stream from any fill source (the XOF engine of
// version 10, or version 11's dataset-slice-and-mix) into the table's wide write port (NB words per cycle, one
// per bank). `start` names the octet and the table and resets the address; the source then presents words with
// in_v while in_ready is high (one per cycle); every NB words one write leaves on wr_v; `done` pulses after the
// last write of the 2^AW words. The read path never changes with the source: only this stream does.
module gather_fill #(
  parameter AW = 12,
  parameter NB = 8,
  parameter TB = 2,
  parameter OW = 2
) (
  input                     clk,
  input                     rst,
  input                     start,
  input  [OW - 1:0]         start_oct,
  input  [TB - 1:0]         start_tid,
  input                     in_v,
  input  [31:0]             in_word,
  output                    in_ready,
  output reg                done,
  output reg                wr_v,
  output reg [OW - 1:0]     wr_oct,
  output reg [TB - 1:0]     wr_tid,
  output reg [AW - 1:0]     wr_addr,
  output reg [NB * 32 - 1:0] wr_data
);
  localparam BW = $clog2(NB);
  reg                 busy;
  reg  [AW - 1:0]     addr;
  reg  [BW - 1:0]     cnt;
  reg  [NB * 32 - 1:0] buf_w;
  assign in_ready = busy;
  integer i;
  always @(posedge clk) begin
    if (rst) begin
      busy <= 1'b0; wr_v <= 1'b0; done <= 1'b0; cnt <= {BW{1'b0}}; addr <= {AW{1'b0}};
    end else begin
      wr_v <= 1'b0;
      done <= 1'b0;
      if (start) begin
        busy <= 1'b1; wr_oct <= start_oct; wr_tid <= start_tid; addr <= {AW{1'b0}}; cnt <= {BW{1'b0}};
      end else if (busy && in_v) begin
        for (i = 0; i < NB; i = i + 1) if (cnt == i[BW - 1:0]) buf_w[i * 32 +: 32] <= in_word;
        if (cnt == NB - 1) begin
          wr_v <= 1'b1;
          wr_addr <= addr;
          for (i = 0; i < NB; i = i + 1) wr_data[i * 32 +: 32] <= (i == NB - 1) ? in_word : buf_w[i * 32 +: 32];
          addr <= addr + NB;
          cnt <= {BW{1'b0}};
          if (addr == ((1 << AW) - NB)) begin busy <= 1'b0; done <= 1'b1; end
        end else cnt <= cnt + 1'b1;
      end
    end
  end
endmodule

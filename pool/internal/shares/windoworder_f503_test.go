package shares

import "testing"

// F-503 (0.1.90): the pay window is keyed by the carry's ORDER in the journal: a block credited after a close, whatever
// its height, lands in the next window; no share falls in no window; the sum over windows equals the sum over the
// journal; a move-back takes a line out of the window its carry fell in, a paid window's named and never paid twice.
func TestWindowWorkByCarryOrder(t *testing.T) {
	j := &Journal{outstanding: map[uint64]Line{}}
	acc, m1, m2 := "0xpool", "0xaaaa", "0xbbbb"
	line := func(seq uint64, member string, coord uint8, weight uint64, bps uint32) Line {
		return Line{Seq: seq, Kind: "accepted", Account: acc, Member: member, Coord: coord, HasCoord: true, Weight: weight, FeeBps: bps, Height: 100 + seq}
	}
	for _, l := range []Line{line(1, m1, 5, 1000, 100), line(2, m1, 6, 50000, 100), line(3, m2, 6, 70000, 0), line(4, m1, 5, 3000, 100), line(5, m2, 5, 500, 0)} {
		j.apply(l)
	}
	// three carries before the first close (blocks 110, 120, 120)
	j.apply(Line{Seq: 1, Kind: "carried", Account: acc, BlockAt: 110, Block: "0x1"})
	j.apply(Line{Seq: 2, Kind: "carried", Account: acc, BlockAt: 120, Block: "0x2"})
	j.apply(Line{Seq: 3, Kind: "carried", Account: acc, BlockAt: 120, Block: "0x2"})
	edge1 := j.CarryEdge()
	if edge1 != 3 {
		t.Fatalf("the edge after three carries is 3, got %d", edge1)
	}
	w1 := j.WindowWork(acc, 0, edge1)
	if w1.Work[m1][5].Work != 1000 || w1.Work[m1][6].Work != 50000 || w1.Work[m2][6].Work != 70000 || w1.Work[""][6].Work != 120000 {
		t.Fatalf("the first window: %+v", w1.Work)
	}
	if w1.FirstHeight != 110 || w1.LastHeight != 120 {
		t.Fatalf("the first window's blocks 110 to 120, got %d to %d", w1.FirstHeight, w1.LastHeight)
	}
	j.PruneCarriedThrough(edge1)
	// F-503's case: after the close a block at height 115 (inside the closed window's heights) is credited, and one
	// at 200; both land in the NEXT window by their carry order
	j.apply(Line{Seq: 5, Kind: "carried", Account: acc, BlockAt: 115, Block: "0x15"})
	j.apply(Line{Seq: 4, Kind: "carried", Account: acc, BlockAt: 200, Block: "0x3"})
	edge2 := j.CarryEdge()
	w2 := j.WindowWork(acc, edge1, edge2)
	if w2.Work[m2][5].Work != 500 || w2.Work[m1][5].Work != 3000 || w2.FirstHeight != 115 || w2.LastHeight != 200 {
		t.Fatalf("the late carry lands in the next window: %+v (%d to %d)", w2.Work, w2.FirstHeight, w2.LastHeight)
	}
	// the sum over the windows equals the sum over the journal's carried lines
	if w1.Work[""][5].Work+w1.Work[""][6].Work+w2.Work[""][5].Work != 1000+50000+70000+3000+500 {
		t.Fatalf("a share fell in no window")
	}
	// a move-back of a line carried in the PAID window: named on the next window, never paid twice; a move-back of a
	// line in the open window takes it out
	j.apply(Line{Seq: 2, Kind: "uncarried", Account: acc, Height: 102})
	j.apply(Line{Seq: 4, Kind: "uncarried", Account: acc, Height: 104})
	w2 = j.WindowWork(acc, edge1, edge2)
	if w2.Work[m1][5].Work != 0 || w2.Work[m2][5].Work != 500 {
		t.Fatalf("the open window's move-back: %+v", w2.Work)
	}
	if w2.MovedBackPaidWork != 50000 {
		t.Fatalf("the paid window's move-back named: %d", w2.MovedBackPaidWork)
	}
	if again := j.WindowWork(acc, edge1, edge2); again.MovedBackPaidWork != 50000 {
		t.Fatalf("the read is pure: the figure stands until the prune, got %d", again.MovedBackPaidWork)
	}
	// the edge of a record closed by height before 0.1.90: the carries in blocks at or under that height
	if e := j.CarryEdgeAtHeight(120); e != 4 {
		// carries 1, 2, 3 were pruned at the first close; 4 is seq 5's carry at 115 (at or under 120); 5 is at 200
		t.Fatalf("the edge at height 120 is the carry number 4, got %d", e)
	}
	// the open window's moved-back line (seq 4, carried at 200) is never named as paid at the next close
	j.PruneCarriedThrough(edge2)
	if next := j.WindowWork(acc, edge2, j.CarryEdge()); next.MovedBackPaidWork != 0 {
		t.Fatalf("a line moved back inside the open window was never paid: %d", next.MovedBackPaidWork)
	}
	// a carried line whose accepted line is gone still takes a number (the numbering stable across replays)
	before := j.CarryEdge()
	j.apply(Line{Seq: 99, Kind: "carried", Account: acc, BlockAt: 300})
	if j.CarryEdge() != before {
		// the carried case guards on j.lines[seq]: an unknown seq applies nothing at all, by the journal's own rule
		t.Fatalf("an unknown seq must apply nothing")
	}
}

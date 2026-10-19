package shares

import "testing"

// F-530 (0.1.94): a seq takes a carry number once. Carried, uncarried, carried again inside the open window: the row
// is restored and paid once; across a paid close: the re-carry is named as restored paid work and never numbered or
// paid again; a second uncarried line for a row already moved back names nothing. The same in every replay.
func TestASeqTakesACarryNumberOnce(t *testing.T) {
	j := &Journal{outstanding: map[uint64]Line{}}
	acc := "0xacc"
	line := func(seq uint64, w uint64) Line {
		return Line{Seq: seq, Account: acc, Member: "0xm", Weight: w}
	}
	// the open window: seq 1 carried, moved back, carried again: one number, paid once
	j.carriedAccount(line(1, 100), 10, 0)
	j.movedBack(line(1, 100), 1)
	j.carriedAccount(line(1, 100), 10, 0)
	if j.CarryEdge() != 1 {
		t.Fatalf("one number for one seq, got %d", j.CarryEdge())
	}
	span := j.WindowWork(acc, 0, j.CarryEdge())
	if got := span.Work["0xm"][0].Work; got != 100 {
		t.Fatalf("the restored row is paid once: %d", got)
	}
	if span.MovedBackPaidWork != 0 || span.RestoredPaidWork != 0 {
		t.Fatalf("nothing named inside the open window: %+v", span)
	}
	// the window closes and is paid; the rows through the edge are pruned
	edge := j.CarryEdge()
	j.PruneCarriedThrough(edge)
	// the churn after the paid close: moved back (named once), moved back again (nothing), carried again (restored,
	// named, never numbered or paid again)
	j.movedBack(line(1, 100), 1)
	j.movedBack(line(1, 100), 1)
	j.carriedAccount(line(1, 100), 10, 0)
	j.carriedAccount(line(1, 100), 10, 0)
	// a new seq in the next window
	j.carriedAccount(line(2, 7), 11, 0)
	if j.CarryEdge() != 2 {
		t.Fatalf("the re-carry took no number: edge %d", j.CarryEdge())
	}
	span = j.WindowWork(acc, edge, j.CarryEdge())
	if got := span.Work["0xm"][0].Work; got != 7 {
		t.Fatalf("the next window pays the new seq alone: %d", got)
	}
	if span.MovedBackPaidWork != 100 {
		t.Fatalf("the move-back from the paid window named once: %d", span.MovedBackPaidWork)
	}
	if span.RestoredPaidWork != 100 {
		t.Fatalf("the re-carries after the paid close named as restored once a seq, got %d", span.RestoredPaidWork)
	}
	j.PruneCarriedThrough(j.CarryEdge())
	if again := j.WindowWork(acc, j.CarryEdge(), j.CarryEdge()); again.MovedBackPaidWork != 0 || again.RestoredPaidWork != 0 {
		t.Fatalf("forgotten at the prune: %+v", again)
	}
	// a replay of the same lines numbers the same
	k := &Journal{outstanding: map[uint64]Line{}}
	for _, step := range []struct {
		carry bool
		seq   uint64
	}{{true, 1}, {false, 1}, {true, 1}, {false, 1}, {false, 1}, {true, 1}, {true, 1}, {true, 2}} {
		if step.carry {
			k.carriedAccount(line(step.seq, 100), 10, 0)
		} else {
			k.movedBack(line(step.seq, 100), step.seq)
		}
	}
	if k.CarryEdge() != 2 {
		t.Fatalf("the replay numbers the same: %d", k.CarryEdge())
	}
}

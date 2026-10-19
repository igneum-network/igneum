package issuance

import (
	"math/big"
	"testing"
)

// The height-gated re-cut (the founder's "re cut" for the 12-second target,
// 16 October 2026): a schedule's next form from a gate height, the gross
// issued at the landing check carried whole, every block issued once.

func shiftForm(epoch uint64, k uint, tail int64) Params {
	return Params{Epoch: epoch, M: ign(1000), K: k, Tail: ign(tail), FundBps: 100, FundAddress: testFund}
}

func recutForm(epoch, div uint64, tailPerEpoch int64) Params {
	return Params{Epoch: epoch, M: ign(1000), Tail: new(big.Int), FundBps: 100, EpochDiv: div, TailPerEpoch: ign(tailPerEpoch), FundAddress: testFund}
}

// sumCheck reads every block from 1 to through: the gross issued is the sum
// of the subsidies and the fund issued the sum of the splits, block by
// block, across the gate.
func sumCheck(t *testing.T, s *Table, through uint64) {
	t.Helper()
	issued, fund := new(big.Int), new(big.Int)
	for h := uint64(1); h <= through; h++ {
		sub := s.Subsidy(h)
		if sub.Sign() <= 0 {
			t.Fatalf("height %d: subsidy %v", h, sub)
		}
		issued.Add(issued, sub)
		_, f := s.ParamsAt(h).Split(sub)
		fund.Add(fund, f)
		if got := s.Issued(h); got.Cmp(issued) != 0 {
			t.Fatalf("height %d: Issued %v, the subsidies' sum %v", h, got, issued)
		}
		if got := s.FundIssued(h); got.Cmp(fund) != 0 {
			t.Fatalf("height %d: FundIssued %v, the splits' sum %v", h, got, fund)
		}
	}
}

func TestNextFormCarriesTheGrossIssued(t *testing.T) {
	old := shiftForm(8, 4, 1)
	gate := uint64(13) // inside the old form's second epoch: any height is a whole gate under the shift form
	next := recutForm(10, 2, 3)
	p := old
	p.NextFrom, p.Next = gate, &next
	if err := Check(p, false); err != nil {
		t.Fatal(err)
	}
	s := New(p)
	alone := New(old)
	// Under the landing check the two agree block by block.
	for h := uint64(0); h <= gate; h++ {
		if s.Subsidy(h).Cmp(alone.Subsidy(h)) != 0 || s.Issued(h).Cmp(alone.Issued(h)) != 0 || s.FundIssued(h).Cmp(alone.FundIssued(h)) != 0 {
			t.Fatalf("height %d under the landing check differs from the first form alone", h)
		}
	}
	// The first block above the gate: the next form's first epoch starts
	// at the gross issued at the landing check.
	carried := alone.Issued(gate)
	wantAmount := next.epochAmount(carried)
	if wantAmount.Cmp(next.TailPerEpoch) < 0 {
		wantAmount = next.TailPerEpoch
	}
	wantPer := new(big.Int).Div(wantAmount, big.NewInt(10))
	if got := s.Subsidy(gate + 1); got.Cmp(wantPer) != 0 {
		t.Fatalf("the first block above the gate: subsidy %v, want %v (the amount %v over 10 from the carried %v)", got, wantPer, wantAmount, carried)
	}
	if got := s.Issued(gate + 1); got.Cmp(new(big.Int).Add(carried, wantPer)) != 0 {
		t.Fatalf("the first block above the gate: issued %v, want the carried %v plus %v", got, carried, wantPer)
	}
	// The next form's epoch ends ten blocks above the gate, the remainder in
	// its last block.
	last := gate + 10
	_, rem := next.epochBlocks(carried)
	if got := s.Subsidy(last); got.Cmp(new(big.Int).Add(wantPer, rem)) != 0 {
		t.Fatalf("the next form's first last block %d: subsidy %v, want %v plus the remainder %v", last, got, wantPer, rem)
	}
	if got := s.Issued(last); got.Cmp(new(big.Int).Add(carried, wantAmount)) != 0 {
		t.Fatalf("the next form's first epoch issued %v, want the carried %v plus the amount %v", got, carried, wantAmount)
	}
	sumCheck(t, s, 120)
	if s.ParamsAt(gate).Epoch != 8 || s.ParamsAt(gate+1).Epoch != 10 {
		t.Fatal("ParamsAt does not switch at the landing check")
	}
}

func TestNextFormFromARecutFormAtItsEpochEnd(t *testing.T) {
	// The public test network's case to come: the 20-second re-cut form to
	// the 12-second one at an epoch's last height, the old epoch's
	// remainder paid whole in its last block.
	old := recutForm(6, 2, 3)
	next := recutForm(10, 2, 3)
	p := old
	p.NextFrom, p.Next = 12, &next
	if err := Check(p, false); err != nil {
		t.Fatal(err)
	}
	s := New(p)
	alone := New(old)
	if s.Subsidy(12).Cmp(alone.Subsidy(12)) != 0 {
		t.Fatal("the old epoch's last block differs at the landing check")
	}
	sumCheck(t, s, 80)
	// A gate inside an old epoch is refused.
	p.NextFrom = 13
	if err := Check(p, false); err == nil {
		t.Fatal("a gate inside an epoch of the re-cut form admitted")
	}
	p.NextFrom = 0
	if err := Check(p, false); err == nil {
		t.Fatal("a next form with no gate admitted")
	}
}

func TestNextFormTransitionAndChain(t *testing.T) {
	// Two gates in a row (a chain re-cut twice), the tail reached in the
	// last form: Transition answers from the form that holds it.
	// a issues 15.625 IGN a block (M over 64) in epochs of four, so by its
	// gate at 9 about 135 IGN are out; b takes an eighth of the rest a
	// block in epochs of six (783 by 15, 945 by 21, 986 by 27, still
	// above its tail of 1 at the landing check 30); c takes half of the rest a
	// block in epochs of eight and reaches its tail of 2 in its second
	// epoch, at 39.
	a := shiftForm(4, 6, 1)
	b := shiftForm(6, 3, 1)
	c := shiftForm(8, 1, 2)
	p := a
	pb := b
	pb.NextFrom, pb.Next = 30, &c
	p.NextFrom, p.Next = 9, &pb
	if err := Check(p, false); err != nil {
		t.Fatal(err)
	}
	s := New(p)
	sumCheck(t, s, 400)
	e, first, ok := s.Transition()
	if !ok {
		t.Fatal("no transition on a tailed schedule")
	}
	if first <= 30 {
		t.Fatalf("the tail at %d, under the last gate", first)
	}
	if got := s.Subsidy(first); got.Cmp(ign(2)) != 0 {
		t.Fatalf("the first tail height %d (epoch %d of its form) pays %v, want the last form's tail %v", first, e, got, ign(2))
	}
	if got := s.Subsidy(first - 1); got.Cmp(ign(2)) <= 0 {
		t.Fatalf("the height before the tail pays %v, not above the tail", got)
	}
}

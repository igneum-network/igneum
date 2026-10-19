// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package issuance

import (
	"math/big"
	"testing"
)

// Recut is the launch schedule's form (the project's ruling of 14:0x UK and its
// tail correction of 14:2x, 11 October 2026): M 100,000,000 IGN, epochs of
// 6,144 blocks, (M minus the supply) / 2,048 an epoch floor-divided over
// the blocks with the remainder in the last, the tail 2,048 IGN an epoch
// (525,600 IGN a year at 20 s; one third of an IGN a block on average),
// the fund one percent.
var Recut = Params{
	Epoch:        6144,
	EpochDiv:     2048,
	M:            ign(100_000_000),
	K:            0,
	Tail:         new(big.Int),
	TailPerEpoch: ign(2048),
	FundBps:      100,
}

func TestRecutOpeningAndRemainder(t *testing.T) {
	s := New(Recut)
	// The opening: (10^8 / 2048) IGN = 48,828.125 IGN an epoch; per block
	// floor(48,828.125 / 6144) IGN = 7.947286... IGN; the remainder in
	// block 6144.
	amount := new(big.Int).Div(ign(100_000_000), big.NewInt(2048))
	per := new(big.Int).Div(amount, big.NewInt(6144))
	rem := new(big.Int).Sub(amount, new(big.Int).Mul(per, big.NewInt(6144)))
	if rem.Sign() <= 0 {
		t.Fatal("the opening epoch has no remainder; the test proves nothing")
	}
	if s.Subsidy(1).Cmp(per) != 0 || s.Subsidy(6143).Cmp(per) != 0 {
		t.Fatalf("opening subsidy %v, want %v", s.Subsidy(1), per)
	}
	want := new(big.Int).Add(per, rem)
	if s.Subsidy(6144).Cmp(want) != 0 {
		t.Fatalf("the epoch's last block %v, want %v", s.Subsidy(6144), want)
	}
	// 7.947286 IGN to six places (the exact figure 7.9472856... IGN =
	// 10^26 / 12,582,912 wei, rounded).
	if per.Cmp(new(big.Int).Div(ign(100_000_000), big.NewInt(2048*6144))) != 0 {
		t.Fatalf("opening per block %v wei", per)
	}
	rounded := new(big.Int).Add(per, big.NewInt(500_000_000_000))
	if ign6 := rounded.Div(rounded, big.NewInt(1e12)).Int64(); ign6 != 7_947_286 {
		t.Fatalf("opening per block %d micro-IGN rounded, want 7947286", ign6)
	}
	// Issued is the exact sum: at 6144 the epoch's amount whole; the next
	// epoch starts from it.
	if s.Issued(6144).Cmp(amount) != 0 {
		t.Fatalf("issued at 6144 %v, want %v", s.Issued(6144), amount)
	}
	sum := new(big.Int)
	for h := uint64(1); h <= 2*6144+5; h++ {
		sum.Add(sum, s.Subsidy(h))
		if h%1000 == 0 || h == 6144 || h == 6145 || h == 12288 || h == 12289 {
			if s.Issued(h).Cmp(sum) != 0 {
				t.Fatalf("issued at %d %v, sum %v", h, s.Issued(h), sum)
			}
		}
	}
	start2, _ := s.EpochState(1)
	if start2.Cmp(amount) != 0 {
		t.Fatalf("epoch 1 starts at %v, want %v", start2, amount)
	}
	// The second epoch's amount is (M minus the first's) / 2048: the one
	// in 2,048 decline.
	amount2 := new(big.Int).Div(new(big.Int).Sub(ign(100_000_000), amount), big.NewInt(2048))
	if s.Issued(2*6144).Cmp(new(big.Int).Add(amount, amount2)) != 0 {
		t.Fatal("the second epoch's amount")
	}
	// The fund: one percent of every block, the remainder's block split
	// on its own; FundIssued the sum of the splits.
	fsum := new(big.Int)
	for h := uint64(1); h <= 6144+3; h++ {
		_, f := Recut.Split(s.Subsidy(h))
		fsum.Add(fsum, f)
		if h == 6144 || h == 6147 {
			if s.FundIssued(h).Cmp(fsum) != 0 {
				t.Fatalf("fund issued at %d %v, sum %v", h, s.FundIssued(h), fsum)
			}
		}
	}
	// The tail: an epoch starting at the asymptote pays 2,048 IGN whole:
	// 333,333,333,333,333,333 wei a block and the remainder 2,048 wei in
	// its last block (6,144 x that per block is 2,048 wei short).
	tailPer, tailRem := Recut.epochBlocks(ign(100_000_000))
	if tailPer.Cmp(big.NewInt(333_333_333_333_333_333)) != 0 || tailRem.Int64() != 2048 {
		t.Fatalf("the tail's epoch: %v %v", tailPer, tailRem)
	}
	// An epoch whose amount is under the tail pays the tail: at a supply
	// 1 IGN under M the amount is under 1 IGN; the subsidy is the tail's.
	nearPer, nearRem := Recut.epochBlocks(new(big.Int).Sub(ign(100_000_000), unit))
	if nearPer.Cmp(tailPer) != 0 || nearRem.Cmp(tailRem) != 0 {
		t.Fatalf("the near-asymptote epoch: %v %v", nearPer, nearRem)
	}
	// The per-block Tail is not read under the form.
	withTail := Recut
	withTail.Tail = ign(5)
	if p, _ := withTail.epochBlocks(ign(100_000_000)); p.Cmp(tailPer) != 0 {
		t.Fatal("the per-block tail was read under the re-cut form")
	}
	// The shift form is untouched: Candidate's figures as before.
	c := New(Candidate)
	if c.Subsidy(8192).Cmp(c.Subsidy(1)) != 0 {
		t.Fatal("the shift form grew a remainder")
	}
}

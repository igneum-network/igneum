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

// The one-billion supply (the founder's order of 18 October 2026,
// docs/tokenomics/SUPPLY-2026-10-18.md): the 12-second form's shape with
// every amount ten times. recut12 is the 12-second form at the old figures
// (params.IgneumIssuance12s), supply1B the new (params.IgneumIssuance1B),
// recut1B20 the 20-second launch form at the new figures (the scaled
// 12,288-gate check only; no chain carries it). The figures below are the
// model's integer arithmetic (the same rule as the chains' reads of record
// in docs/economics/ISSUANCE-12S-2026-10-16.md, every amount ten times).
var (
	recut12   = Params{Epoch: 10240, EpochDiv: 2048, M: ign(100_000_000), Tail: new(big.Int), TailPerEpoch: ign(2048), FundBps: 100}
	supply1B  = Params{Epoch: 10240, EpochDiv: 2048, M: ign(1_000_000_000), Tail: new(big.Int), TailPerEpoch: ign(20480), FundBps: 100}
	recut1B20 = Params{Epoch: 6144, EpochDiv: 2048, M: ign(1_000_000_000), Tail: new(big.Int), TailPerEpoch: ign(20480), FundBps: 100}
)

// TestSupply1BFromGenesis: the launch genesis's form. The opening epoch
// issues 488,281.25 IGN (ten times 48,828.125), 47.683716 IGN a block with
// no remainder (10^27 / 20,971,520 wei), the fund 0.476837 IGN of it; the
// second epoch's figures and the gross after two epochs ten times the
// 12-second form's.
func TestSupply1BFromGenesis(t *testing.T) {
	s := New(supply1B)
	eq(t, "block 1", s.Subsidy(1), "47683715820312500000")
	eq(t, "block 10,240 (the epoch's last: no remainder)", s.Subsidy(10240), "47683715820312500000")
	eq(t, "issued at 10,240", s.Issued(10240), "488281250000000000000000")
	miner, fund := s.Reward(1)
	eq(t, "the miner's part of block 1", miner, "47206878662109375000")
	eq(t, "the fund's part of block 1", fund, "476837158203125000")
	eq(t, "block 10,241", s.Subsidy(10241), "47660432755947113037")
	eq(t, "block 20,480 (the second epoch's last, its 1,120-wei remainder)", s.Subsidy(20480), "47660432755947114157")
	eq(t, "issued at 20,480", s.Issued(20480), "976324081420898437500000")
	// Ten times the 12-second form at the old figures, block by block: to
	// the wei where the division is exact (the opening epoch), within ten
	// wei elsewhere (the floors; an epoch's last block carries its own
	// remainder and is not compared).
	o := New(recut12)
	ten := big.NewInt(10)
	for _, h := range []uint64{1, 2, 10240, 10241, 30000, 40961} {
		want := new(big.Int).Mul(o.Subsidy(h), ten)
		got := s.Subsidy(h)
		d := new(big.Int).Sub(got, want)
		if d.Sign() < 0 {
			d.Neg(d)
		}
		if d.Cmp(ten) > 0 {
			t.Fatalf("block %d: %v is not ten times the old %v (off by %v)", h, got, o.Subsidy(h), d)
		}
	}
	if s.Subsidy(0).Sign() != 0 || s.Issued(0).Sign() != 0 {
		t.Fatal("genesis issued")
	}
}

// TestSupply1BTail: the tail 20,480 IGN an epoch pays two IGN a block with
// no remainder (ten times 2,048; the 12-second form's fifth of an IGN
// times ten); an epoch whose amount is under the tail pays the tail.
func TestSupply1BTail(t *testing.T) {
	per, rem := supply1B.epochBlocks(ign(1_000_000_000))
	if per.Cmp(ign(2)) != 0 || rem.Sign() != 0 {
		t.Fatalf("the tail's epoch: %v a block, remainder %v; want 2 IGN and none", per, rem)
	}
	near, nearRem := supply1B.epochBlocks(new(big.Int).Sub(ign(1_000_000_000), unit))
	if near.Cmp(per) != 0 || nearRem.Sign() != 0 {
		t.Fatalf("the near-asymptote epoch: %v %v", near, nearRem)
	}
	// The 20-second form's tail at the new figures: 3.333... IGN a block
	// with the 2,048-wei remainder in the epoch's last block, as the old.
	per20, rem20 := recut1B20.epochBlocks(ign(1_000_000_000))
	if per20.Cmp(mustBig(t, "3333333333333333333")) != 0 || rem20.Int64() != 2048 {
		t.Fatalf("the 20-second tail's epoch at the new figures: %v %v", per20, rem20)
	}
}

// TestSupply1BGate12288Scaled: the 12,288-gate figures of record (two
// 20-second epochs carried whole, then the 12-second form) with every
// amount ten times: the gross carried 976,324,081,420,898,437,500,000 wei,
// block 12,289 paying 47,637,161,060,265,498,235 wei (the fund
// 476,371,610,602,654,982), the first 12-second epoch's remainder 8,414 wei
// in block 22,528.
func TestSupply1BGate12288Scaled(t *testing.T) {
	p := recut1B20
	p.FundAddress = testFund
	p.NextFrom, p.Next = 12288, &supply1B
	if err := Check(p, false); err != nil {
		t.Fatal(err)
	}
	s := New(p)
	eq(t, "issued at the landing check 12,288", s.Issued(12288), "976324081420898437500000")
	eq(t, "block 12,288 (the second 20-second epoch's last)", s.Subsidy(12288), "79434054593245189515")
	eq(t, "block 12,289", s.Subsidy(12289), "47637161060265498235")
	miner, fund := s.Reward(12289)
	eq(t, "the miner's part of 12,289", miner, "47160789449662843253")
	eq(t, "the fund's part of 12,289", fund, "476371610602654982")
	eq(t, "block 22,528 (the remainder 8,414 wei)", s.Subsidy(22528), "47637161060265506649")
	eq(t, "issued at 22,528", s.Issued(22528), "1464128610678017139434814")
}

// TestSupply1BAfterThePublicTestNetworksGate: the public test network's
// schedule (the 20-second launch form to 12,288, the 12-second form above
// it, as the chain paid: 4,763,716,106,026,549,823 wei at 12,289) with the
// one-billion form appended at 22,528, the first epoch boundary of the
// 12-second form above its gate: the gross carried
// 146,412,861,067,801,713,943,481 wei, block 22,529 paying
// 47,676,734,311,052,904,047 wei. A gate inside the 12-second form's epoch
// is refused.
func TestSupply1BAfterThePublicTestNetworksGate(t *testing.T) {
	second := recut12
	second.NextFrom, second.Next = 22528, &supply1B
	p := Recut
	p.FundAddress = testFund
	p.NextFrom, p.Next = 12288, &second
	if err := Check(p, false); err != nil {
		t.Fatal(err)
	}
	s := New(p)
	eq(t, "block 12,289 (the 12-second form, the chain's read of record)", s.Subsidy(12289), "4763716106026549823")
	eq(t, "block 22,528 (the 12-second form's last block under the old figures)", s.Subsidy(22528), "4763716106026555784")
	eq(t, "issued at the landing check 22,528", s.Issued(22528), "146412861067801713943481")
	eq(t, "block 22,529 (the first at the one-billion figures)", s.Subsidy(22529), "47676734311052904047")
	eq(t, "block 32,768 (the one-billion form's first epoch's last)", s.Subsidy(32768), "47676734311052907130")
	eq(t, "issued at 32,768", s.Issued(32768), "634622620412983451387844")
	sumCheck(t, s, 32768+3)
	inside := second
	inside.NextFrom = 22000
	q := Recut
	q.FundAddress = testFund
	q.NextFrom, q.Next = 12288, &inside
	if Check(q, false) == nil {
		t.Fatal("a one-billion gate inside a 12-second epoch was accepted")
	}
}

// TestSupply1BAfterTheResearchChainsGate: the research chain's schedule
// (the shift form to 16,384, the 12-second form above it, as the chain paid:
// 4,763,716,106,026,549,823 wei at 16,385) with the one-billion form
// appended at 26,624, its first epoch boundary above the gate: block 26,625
// pays 47,676,734,311,052,904,047 wei.
func TestSupply1BAfterTheResearchChainsGate(t *testing.T) {
	second := recut12
	second.NextFrom, second.Next = 26624, &supply1B
	p := Candidate
	p.FundAddress = testFund
	p.NextFrom, p.Next = 16384, &second
	if err := Check(p, false); err != nil {
		t.Fatal(err)
	}
	s := New(p)
	eq(t, "block 16,385 (the 12-second form, the chain's read of record)", s.Subsidy(16385), "4763716106026549823")
	eq(t, "issued at the landing check 26,624", s.Issued(26624), "146412861067801713938252")
	eq(t, "block 26,625 (the first at the one-billion figures)", s.Subsidy(26625), "47676734311052904047")
	eq(t, "block 36,865", s.Subsidy(36865), "47653454655627585246")
	sumCheck(t, s, 36865)
}

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

package params

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestIssuanceForChainID: the seventh development chain carries the re-cut
// schedule as its config default (a copy, its own fund stand-in); an
// unlisted id carries none; the development default is untouched.
func TestIssuanceForChainID(t *testing.T) {
	i := IgneumIssuanceForChainID(7317)
	if i == nil {
		t.Fatal("no issuance for 7317")
	}
	if i.Epoch != 6144 || i.EpochDiv != 2048 || i.FundBps != 100 || i.M.Cmp(new(big.Int).Mul(big.NewInt(100_000_000), big.NewInt(1e18))) != 0 {
		t.Fatalf("7317's issuance %+v", *i)
	}
	if i.Tail.Sign() != 0 || i.TailPerEpoch.Cmp(new(big.Int).Mul(big.NewInt(2048), big.NewInt(1e18))) != 0 {
		t.Fatalf("tail %v per block, %v per epoch; want 0 and 2,048 IGN", i.Tail, i.TailPerEpoch)
	}
	if i.FundAddress != common.HexToAddress("0x0000000000000000000000000000000000007317") {
		t.Fatalf("fund %s", i.FundAddress.Hex())
	}
	// A copy: a caller's change does not reach the table.
	i.Epoch = 1
	i.M.SetInt64(1)
	if j := IgneumIssuanceForChainID(7317); j.Epoch != 6144 || j.M.Cmp(i.M) == 0 {
		t.Fatal("the table was changed through a copy")
	}
	// The eighth and ninth development chains carry the same schedule with
	// their own fund stand-ins and the launch limits. The eighth carries no
	// rule fork (the latest form of every rule from genesis); the ninth
	// carries the two its activation files record (docs/chain/devnet9
	// ACTIVATION-1.md, the length order at 700, and ACTIVATION-2.md, the
	// policy parts at 900, under node 0.9.3), which this case had not read
	// (F-195, 16 October 2026).
	for _, id := range []uint64{7318, 7319} {
		k := IgneumIssuanceForChainID(id)
		if k == nil || k.Epoch != 6144 || k.FundAddress != common.HexToAddress("0x000000000000000000000000000000000000"+fmt.Sprint(id)) {
			t.Fatalf("%d's issuance %+v", id, k)
		}
		if l := IgneumRPCLimitsForChainID(id); l == nil || l.ShareCapPerAddress != 256 || l.RPCRate != 20 {
			t.Fatalf("%d's limits %+v", id, l)
		}
	}
	if IgneumRuleForksForChainID(7318) != nil {
		t.Fatalf("7318 carries a rule fork")
	}
	if f := IgneumRuleForksForChainID(7319); f == nil || f.ForkLengthV1 != 700 || f.PolicyV1 != 900 {
		t.Fatalf("7319's rule forks %+v, want the length order at 700 and the policy parts at 900", f)
	}
	// The tenth (7320) carries the same launch issuance and limits with its
	// own stand-in, and is the first chain whose rule forks are listed: every
	// by-height rule on from genesis, the second pay form among them.
	if k := IgneumIssuanceForChainID(7320); k == nil || k.Epoch != 6144 || k.EpochDiv != 2048 || k.FundBps != 100 || k.FundAddress != common.HexToAddress("0x0000000000000000000000000000000000007320") {
		t.Fatalf("7320's issuance %+v", k)
	}
	if l := IgneumRPCLimitsForChainID(7320); l == nil || l.ShareCapPerAddress != 256 || l.RPCRate != 20 {
		t.Fatalf("7320's limits %+v", l)
	}
	if f := IgneumRuleForksForChainID(7320); f == nil || f.PayV2 != 1 || f.ClockV1 != 1 || f.PolicyV1 != 1 || f.ForkLengthV1 != 1 {
		t.Fatalf("7320's rule forks %+v", f)
	}
	if IgneumIssuanceForChainID(7310) != nil || IgneumIssuanceForChainID(7315) != nil || IgneumIssuanceForChainID(7316) != nil {
		t.Fatal("an unlisted chain id carries an issuance")
	}
	if d := IgneumDevChainConfig.Igneum.Issuance; d.EpochDiv != 0 || d.Epoch != 8192 || d.K != 24 {
		t.Fatalf("the development default changed: %+v", *d)
	}
}

// TestRPCLimitsForChainID: the seventh development chain carries the
// launch limits (20 a second, burst 40, 64 shares an address a block) as
// its config default, a copy; an unlisted id carries none (the limits
// off).
func TestRPCLimitsForChainID(t *testing.T) {
	l := IgneumRPCLimitsForChainID(7317)
	if l == nil || l.RPCRate != 20 || l.RPCBurst != 40 || l.ShareCapPerAddress != 256 {
		t.Fatalf("7317's limits %+v", l)
	}
	l.RPCRate = 1
	if IgneumRPCLimitsForChainID(7317).RPCRate != 20 {
		t.Fatal("the table was changed through a copy")
	}
	if IgneumRPCLimitsForChainID(7310) != nil || IgneumRPCLimitsForChainID(7316) != nil {
		t.Fatal("an unlisted chain id carries limits")
	}
	if IgneumLaunchRPCLimits.ShareCapPerAddress != 256 {
		t.Fatal("the per-address cap is not the lane's ceiling (the project's rule of 17:0x UK, 12 October 2026)")
	}
}

// TestSupply1B: the one-billion form (the founder's order of 18 October
// 2026) is the 12-second form with every amount ten times and the shape
// unchanged; the launch placeholder carries it from genesis; the public
// test network's and the research chain's gates default to zero, UNSET,
// so their schedules end at the 12-second form at the old figures until
// the review lead names the heights; withSupply appends the form last, from the
// gate, as a copy, and returns the issuance unchanged for a zero gate.
func TestSupply1B(t *testing.T) {
	ten := big.NewInt(10)
	b, s := IgneumIssuance1B, IgneumIssuance12s
	if b.Epoch != s.Epoch || b.EpochDiv != s.EpochDiv || b.FundBps != s.FundBps || b.K != s.K || b.Tail.Sign() != 0 || b.Next != nil || b.NextFrom != 0 {
		t.Fatalf("the one-billion form's shape differs from the 12-second form's: %+v", *b)
	}
	if b.M.Cmp(new(big.Int).Mul(s.M, ten)) != 0 || b.M.Cmp(new(big.Int).Mul(big.NewInt(1_000_000_000), big.NewInt(1e18))) != 0 {
		t.Fatalf("M %v, want 1,000,000,000 IGN, ten times the 12-second form's", b.M)
	}
	if b.TailPerEpoch.Cmp(new(big.Int).Mul(s.TailPerEpoch, ten)) != 0 || b.TailPerEpoch.Cmp(new(big.Int).Mul(big.NewInt(20480), big.NewInt(1e18))) != 0 {
		t.Fatalf("tail %v an epoch, want 20,480 IGN, ten times the 12-second form's", b.TailPerEpoch)
	}
	// The launch placeholder carries the form from genesis; the development
	// default is untouched.
	if l := IgneumChainConfig.Igneum.Issuance; l == nil || l.Epoch != 10240 || l.EpochDiv != 2048 || l.M.Cmp(b.M) != 0 || l.TailPerEpoch.Cmp(b.TailPerEpoch) != 0 || l.Next != nil {
		t.Fatalf("the launch placeholder's issuance %+v, want the one-billion form", l)
	}
	if d := IgneumDevChainConfig.Igneum.Issuance; d.M.Cmp(s.M) != 0 || d.Epoch != 8192 {
		t.Fatalf("the development default changed: %+v", *d)
	}
	// The two chains: with the gate unset the schedule ends at the
	// 12-second form at the old figures; with it set, at the one-billion
	// form from that gate.
	for _, c := range []struct {
		id   uint64
		gate uint64
	}{{7322, IgneumPublic1BFrom}, {7358, IgneumResearch1BFrom}} {
		i := IgneumIssuanceForChainID(c.id)
		if i == nil || i.Next == nil || i.Next.M.Cmp(s.M) != 0 || i.Next.Epoch != 10240 {
			t.Fatalf("%d's second form is not the 12-second form: %+v", c.id, i)
		}
		if c.gate == 0 {
			if i.Next.Next != nil || i.Next.NextFrom != 0 {
				t.Fatalf("%d's gate is unset but a third form is listed: %+v", c.id, i.Next)
			}
			continue
		}
		if i.Next.NextFrom != c.gate || i.Next.Next == nil || i.Next.Next.M.Cmp(b.M) != 0 || i.Next.Next.TailPerEpoch.Cmp(b.TailPerEpoch) != 0 || i.Next.Next.Next != nil {
			t.Fatalf("%d's third form is not the one-billion form from %d: %+v", c.id, c.gate, i.Next)
		}
	}
	// the review lead's heights (18 October 2026): the first epoch boundary of
	// the 12-second form above each chain's own gate.
	if IgneumPublic1BFrom != 22528 || IgneumResearch1BFrom != 26624 {
		t.Fatalf("the gates %d and %d, want 22,528 and 26,624", IgneumPublic1BFrom, IgneumResearch1BFrom)
	}
	if (IgneumPublic1BFrom-IgneumPublic12sFrom)%10240 != 0 || (IgneumResearch1BFrom-IgneumResearch12sFrom)%10240 != 0 {
		t.Fatal("a gate is inside an epoch of the 12-second form")
	}
	// The one-billion lab chain (7397): the 12-second form from genesis and
	// the one-billion form from 64, both at 32 blocks an epoch, the fund's
	// stand-in on the first form.
	if l := IgneumIssuanceForChainID(7397); l == nil || l.Epoch != 32 || l.M.Cmp(s.M) != 0 || l.NextFrom != 64 || l.Next == nil || l.Next.Epoch != 32 || l.Next.M.Cmp(b.M) != 0 || l.Next.TailPerEpoch.Cmp(b.TailPerEpoch) != 0 || l.Next.Next != nil || l.FundAddress != common.HexToAddress("0x0000000000000000000000000000000000007310") {
		t.Fatalf("the lab chain's issuance: %+v", l)
	}
	if IgneumIssuance12s.Epoch != 10240 || IgneumIssuance1B.Epoch != 10240 {
		t.Fatal("withEpoch wrote through a form of record")
	}
	// withSupply with a zero gate returns the issuance as it is.
	if withSupply(IgneumChainIssuance[7358], 0, b) != IgneumChainIssuance[7358] {
		t.Fatal("a zero gate changed the issuance")
	}
	// withSupply appends the form last, from the landing check, copying every form on
	// the way: the table's entry is not written through.
	// The base: the public test network's schedule before the one-billion
	// form (the gate is set, so the table's entry already carries it).
	base := withNext(withFund(IgneumLaunchIssuance, common.HexToAddress("0x0000000000000000000000000000000000007322")), IgneumPublic12sFrom, IgneumIssuance12s)
	before := base.Next.Next
	c := withSupply(base, 22528, b)
	if c == base || c.Next == base.Next {
		t.Fatal("withSupply did not copy")
	}
	if c.NextFrom != IgneumPublic12sFrom || c.Next == nil || c.Next.NextFrom != 22528 || c.Next.Next == nil || c.Next.Next.Next != nil {
		t.Fatalf("the appended chain: %+v", c)
	}
	if last := c.Next.Next; last.M.Cmp(b.M) != 0 || last.TailPerEpoch.Cmp(b.TailPerEpoch) != 0 || last.Epoch != 10240 || last.FundBps != 100 || last.FundAddress != (common.Address{}) {
		t.Fatalf("the appended form: %+v", *last)
	}
	if c.FundAddress != common.HexToAddress("0x0000000000000000000000000000000000007322") || c.Next.M.Cmp(s.M) != 0 {
		t.Fatalf("the first forms changed: %+v", c)
	}
	if base.Next.Next != before || before != nil {
		t.Fatal("the base was written through")
	}
	// The table's entry: the one-billion form from the review lead's height.
	if e := IgneumChainIssuance[7322]; e.Next.NextFrom != IgneumPublic1BFrom || e.Next.Next == nil || e.Next.Next.M.Cmp(b.M) != 0 || e.Next.Next.Next != nil {
		t.Fatalf("the public test network's schedule: %+v", e.Next)
	}
}

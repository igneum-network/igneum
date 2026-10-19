// Copyright 2026 The Igneum Authors
// This file is part of the Igneum node.
//
// The Igneum node is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The Igneum node is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the Igneum node. If not, see <http://www.gnu.org/licenses/>.

package igneum

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestAdmissionBoundRefusesBeforeAcceptance (PA-53's admission bound): an
// address holding a whole list's worth of unpaid shares in the pool
// (MaxShares) has its next share refused at submit by name, never accepted
// to expire; another address is admitted; after a carriage pays the first
// address's shares it is admitted again.
func TestAdmissionBoundRefusesBeforeAcceptance(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	e.config.Parts.Sortition.MaxShares = 3
	e.config.Parts.Sortition.SharesPerBlock = 3
	e.SetShareCap(0)
	head := chain.head
	h := head.Number.Uint64() + 1
	d := e.shareChainDifficulty(chain, head, nil)
	a, b := common.Address{0x31}, common.Address{0x32}
	submit := func(addr common.Address, extra uint64) (bool, string) {
		hs := ShareHeaderHash(head.Hash(), h, addr, extra)
		nonce, mix := solveShare(t, e, hs, h, d)
		return e.SubmitShare(chain, addr, extra, nonce, mix, h)
	}
	for i := uint64(1); i <= 3; i++ {
		if ok, reason := submit(a, i); !ok {
			t.Fatalf("share %d of a under the bound refused: %s", i, reason)
		}
	}
	if ok, reason := submit(a, 4); ok || reason != errShareAdmission.Error() {
		t.Fatalf("a's fourth unpaid share: ok %v, %q (want the admission refusal by name)", ok, reason)
	}
	if ok, reason := submit(b, 1); !ok {
		t.Fatalf("b refused under a's bound: %s", reason)
	}
	acc := e.ShareAccountOf(a)
	if acc.Accepted != 3 || acc.Refused != 1 {
		t.Fatalf("a's account: accepted %d refused %d, want 3 and 1", acc.Accepted, acc.Refused)
	}
	if st := e.ShareStatsNow(); st.RefusedBy[errShareAdmission.Error()] != 1 {
		t.Fatalf("the refusal is not counted by its name: %+v", st.RefusedBy)
	}
	// A block carries a's three (the list's whole): a is admitted again.
	entries, _, err := e.TemplateShares(chain, head, chain.st, common.Address{0xaa})
	if err != nil {
		t.Fatal(err)
	}
	nh := childOf(head)
	chain.push(nh)
	e.MarkCarried(chain, types.NewBlockWithHeader(nh).WithMinedShares(entries))
	if ok, reason := submit(a, 5); !ok {
		t.Fatalf("a after its carriage refused: %s", reason)
	}
}

// TestLaneAtCarriageRefusesTheAggregate (F-50's second half): shares from
// many addresses, each under its own bound, are refused by name once the
// outstanding count across them reaches the chain's carriage within the
// recency (sharesPerBlock x recency), before any receipt; a carriage that
// drains the pool admits again.
func TestLaneAtCarriageRefusesTheAggregate(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	e.config.Parts.Sortition.MaxShares = 8
	e.config.Parts.Sortition.SharesPerBlock = 2
	e.SetShareCap(0)
	bound := e.laneCarriageBound()
	if bound != 2*e.ShareRecencyBlocks() {
		t.Fatalf("the lane's bound is %d, want sharesPerBlock x recency", bound)
	}
	head := chain.head
	h := head.Number.Uint64() + 1
	d := e.shareChainDifficulty(chain, head, nil)
	submit := func(addr common.Address, extra uint64) (bool, string) {
		hs := ShareHeaderHash(head.Hash(), h, addr, extra)
		nonce, mix := solveShare(t, e, hs, h, d)
		return e.SubmitShare(chain, addr, extra, nonce, mix, h)
	}
	for i := uint64(0); i < bound; i++ {
		if ok, reason := submit(common.Address{0x60 + byte(i%8), byte(i / 8)}, i); !ok {
			t.Fatalf("share %d under the lane's bound refused: %s", i, reason)
		}
	}
	if ok, reason := submit(common.Address{0x70}, 1); ok || reason != errShareLaneAtCarriage.Error() {
		t.Fatalf("the share past the lane's carriage: ok %v, %q (want refused by name)", ok, reason)
	}
	if st := e.ShareStatsNow(); st.RefusedBy[errShareLaneAtCarriage.Error()] != 1 || st.Pooled != bound {
		t.Fatalf("stats: refusedBy %+v pooled %d", st.RefusedBy, st.Pooled)
	}
	// A block carries MaxShares of them: the lane admits that many again.
	entries, _, err := e.TemplateShares(chain, head, chain.st, common.Address{0xaa})
	if err != nil {
		t.Fatal(err)
	}
	nh := childOf(head)
	chain.push(nh)
	e.MarkCarried(chain, types.NewBlockWithHeader(nh).WithMinedShares(entries))
	if ok, reason := submit(common.Address{0x70}, 2); !ok {
		t.Fatalf("after a carriage drained the pool the lane refused: %s", reason)
	}
}

// TestSelectionOldestFirst (PA-53's selection rule as F-174 re-cut it, 15
// October 2026: the carried-weight term retired): with more pooled shares
// than a list holds, the template takes the heavier step first and, within
// a step, the OLDEST shares first (the share's height, then arrival),
// whatever the addresses' carried weight; an address the chain has paid
// before holds no precedence over a fresh one at the same step, so a
// never-carried key is never starved (F-122's second face), and the flood
// case rests on the step order and the per-address admission bound. The
// shares the flood pushed past the cap ride the next template.
func TestSelectionOldestFirst(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	e.config.Parts.Sortition.MaxShares = 2
	e.config.Parts.Sortition.SharesPerBlock = 2
	e.SetShareCap(0)
	head := chain.head
	h := head.Number.Uint64() + 1
	d := e.shareChainDifficulty(chain, head, nil)
	honest := common.Address{0x41}
	submit := func(addr common.Address, extra uint64, parent *types.Header, height uint64) {
		hs := ShareHeaderHash(parent.Hash(), height, addr, extra)
		nonce, mix := solveShare(t, e, hs, height, d)
		if ok, reason := e.SubmitShareOn(chain, addr, extra, nonce, mix, height, parent.Hash()); !ok {
			t.Fatalf("share %d of %x refused: %s", extra, addr[:1], reason)
		}
	}
	// The honest address earns weight: one share carried by a block.
	submit(honest, 1, head, h)
	entries, _, err := e.TemplateShares(chain, head, chain.st, common.Address{0xaa})
	if err != nil || types.MinedShareCount(entries) != 1 {
		t.Fatalf("the first template: %v, %d", err, types.MinedShareCount(entries))
	}
	nh := childOf(head)
	chain.push(nh)
	e.MarkCarried(chain, types.NewBlockWithHeader(nh).WithMinedShares(entries))
	// Then a flood of three fresh addresses, submitted BEFORE the honest
	// address's next share, at the next height.
	h2 := nh.Number.Uint64() + 1
	d = e.shareChainDifficulty(chain, nh, nil)
	for i := 0; i < 3; i++ {
		submit(common.Address{0x50 + byte(i)}, 1, nh, h2)
	}
	submit(honest, 2, nh, h2)
	picked, _ := e.templateShares(chain, nh)
	if len(picked) != 2 {
		t.Fatalf("the list took %d shares, want 2", len(picked))
	}
	// The same step throughout: the two oldest by arrival, the flood's
	// first two; the honest address's newer share and the flood's third
	// wait for the next template whatever the honest address's carriage.
	if picked[0].Address != (common.Address{0x50}) || picked[1].Address != (common.Address{0x51}) {
		t.Fatalf("the picks are %x and %x, want the two oldest shares at the step (0x50, 0x51)", picked[0].Address[:1], picked[1].Address[:1])
	}
	// Carried, they leave the pool; the next template at the same height
	// takes the next two by age: the flood's third, then the honest one.
	nh2 := childOf(nh)
	entries2, _, err := e.TemplateShares(chain, nh, chain.st, common.Address{0xaa})
	if err != nil {
		t.Fatal(err)
	}
	chain.push(nh2)
	e.MarkCarried(chain, types.NewBlockWithHeader(nh2).WithMinedShares(entries2))
	// A sibling template on the same parent nh (the carriage marked, the
	// pool drained of the two): the remaining two in age order.
	left, _ := e.templateShares(chain, nh)
	if len(left) != 2 || left[0].Address != (common.Address{0x52}) || left[1].Address != honest {
		t.Fatalf("after the carriage the pool's next picks are %v, want the flood's third then the honest share", left)
	}
}

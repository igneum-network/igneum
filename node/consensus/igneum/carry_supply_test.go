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

package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestCarrySupplyQuorum (the security review's form of 20:46 UK, 16 October 2026,
// the review lead's ruling): the carry's quorum is scaled to the canonical
// chain's own supply of carried endorsements over the last carryWindow
// blocks (the headers' Endorsed), never to the local pool: (1) no supply
// (the public test network: no node endorses), no wait; (2) one endorser
// a window, still no wait (no cliff at the first endorser); (3) a full
// supply, the table's quorum stands; (4) a withholder halving the supply
// halves the quorum; (5) no chain reader, the quorum as before.
func TestCarrySupplyQuorum(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.EndorseExpected = 32
	st := nativeState(t, big.NewInt(1_000_000_000))
	st.SetState(testRegistry, tableSlot(3, 0), common.Hash{0x55})
	st.SetState(testRegistry, tableSlot(3, 1), common.BigToHash(big.NewInt(1)))
	st.SetState(testRegistry, tableSlot(3, 2), common.BigToHash(big.NewInt(49152)))
	st.SetState(testRegistry, tableSlot(3, 3), common.BigToHash(big.NewInt(1)))
	if q := e.carryQuorumAt(st, 350); q != 16 {
		t.Fatalf("the table's quorum %d, want 16", q)
	}
	// A canonical chain of carryWindow + 8 blocks whose headers carry the
	// given endorsement counts (every block the same count).
	build := func(endorsed uint64) (*forkChain, *types.Header) {
		c := newForkChain()
		var parent *types.Header
		for n := uint64(0); n < carryWindow+8; n++ {
			h := &types.Header{Number: new(big.Int).SetUint64(n), Difficulty: big.NewInt(1), Endorsed: endorsed}
			if parent != nil {
				h.ParentHash = parent.Hash()
			}
			c.add(h)
			c.canonical[n] = h
			parent = h
		}
		return c, parent
	}
	// (1) no supply: no wait.
	c, parent := build(0)
	if q := e.carrySupplyQuorum(c, st, parent, 350); q != 0 {
		t.Fatalf("no supply: quorum %d, want 0", q)
	}
	// (2) one endorser a window (one block of the window carries one):
	// the mean is under 1.5 a block, no wait, no cliff.
	c, parent = build(0)
	c.canonical[parent.Number.Uint64()-10].Endorsed = 1
	if q := e.carrySupplyQuorum(c, st, parent, 350); q != 0 {
		t.Fatalf("one endorser a window: quorum %d, want 0", q)
	}
	// (3) the full supply (every block carrying 32): the table's 16 stands.
	c, parent = build(32)
	if q := e.carrySupplyQuorum(c, st, parent, 350); q != 16 {
		t.Fatalf("full supply: quorum %d, want 16", q)
	}
	// (4) a withholder halving the supply (12 carried a block): floor(2/3 x 12) = 8.
	c, parent = build(12)
	if q := e.carrySupplyQuorum(c, st, parent, 350); q != 8 {
		t.Fatalf("half supply: quorum %d, want 8", q)
	}
	// The window is the last carryWindow blocks: the older blocks' counts do
	// not reach the quorum (a chain whose first 8 blocks carried 32 and the
	// rest nothing reads no supply).
	c, parent = build(0)
	for n := uint64(0); n < 8; n++ {
		c.canonical[n].Endorsed = 32
	}
	if q := e.carrySupplyQuorum(c, st, parent, 350); q != 0 {
		t.Fatalf("supply outside the window: quorum %d, want 0", q)
	}
	// (5) no chain reader: the table's quorum as before.
	if q := e.carrySupplyQuorum(nil, st, parent, 350); q != 16 {
		t.Fatalf("no reader: quorum %d, want 16", q)
	}
}

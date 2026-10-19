// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library.
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
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestExpiryClassReadsTheWindowNotTheNumberIndex (the node team's read of 12
// October 2026, pod d at 1,721): the sweep excludes a parent by the hash
// walk of the window's ancestors; its class must come from the same walk.
// Here the chain's number index still names the LOSING parent at its
// height (the reorg's index write lagging the head, as it did for thirteen
// seconds on pod d) while the head's ancestry no longer holds it: the share
// expires as expiredReorg, not as the window's end; and a share whose
// parent lies below the window's oldest ancestor still expires as the
// window's end.
func TestExpiryClassReadsTheWindowNotTheNumberIndex(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	head := chain.head
	a := common.Address{0x45}
	solve := func(extra uint64, parent *types.Header) (uint64, common.Hash) {
		sh := parent.Number.Uint64() + 1
		d := e.shareChainDifficulty(chain, parent, chain.byNumber[sh])
		return solveShare(t, e, ShareHeaderHash(parent.Hash(), sh, a, extra), sh, d)
	}
	h := head.Number.Uint64() + 1
	n1, m1 := solve(1, head)
	if ok, reason := e.SubmitShare(chain, a, 1, n1, m1, h); !ok {
		t.Fatal(reason)
	}
	old := chain.byNumber[h-7]
	n2, m2 := solve(2, old)
	if ok, reason := e.SubmitShare(chain, a, 2, n2, m2, h-6); !ok {
		t.Fatal(reason)
	}
	// The reorg: a sibling of the head's parent takes the line, two blocks
	// follow it. The hash chain (add) moves; the number index is left
	// naming the old head at its height, as a lagging index would.
	sibling := childOf(chain.byNumber[h-2])
	sibling.Time++
	chain.add(sibling)
	c1 := childOf(sibling)
	chain.add(c1)
	c2 := childOf(c1)
	chain.add(c2)
	chain.head = c2
	chain.byNumber[c1.Number.Uint64()] = c1
	chain.byNumber[c2.Number.Uint64()] = c2
	// byNumber[h-1] still names `head`, the loser.
	if chain.GetHeaderByNumber(h-1).Hash() != head.Hash() {
		t.Fatal("the test's premise: the number index names the losing parent")
	}
	if n := e.ExpireShares(chain, chain.head); n != 2 {
		t.Fatalf("expired at the new head: %d (want 2)", n)
	}
	st := e.ShareStatsNow()
	if st.ExpiredReorg[h] != 1 {
		t.Fatalf("the share on the losing parent inside the window: reorg %v window %v (want reorg at %d)", st.ExpiredReorg, st.ExpiredNoBlock, h)
	}
	if st.ExpiredNoBlock[h-6] != 1 {
		t.Fatalf("the share below the window: reorg %v window %v (want the window's end at %d)", st.ExpiredReorg, st.ExpiredNoBlock, h-6)
	}
	acc := e.ShareAccountOf(a)
	if !acc.Closes || acc.ExpiredReorg != 1 || acc.ExpiredWindow != 1 || acc.ExpiredReorgBy[h] != 1 {
		t.Fatalf("the ledger: %+v", acc)
	}
}

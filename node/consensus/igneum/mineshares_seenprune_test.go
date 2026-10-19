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
)

// TestSeenIndexPrunedBelowTheWindow (A9 on the ninth development chain, 12
// October 2026: the duplicate index grew by every accepted share for the
// node's life): a share's key is in the index while its height is inside
// the window and refuses the share a second time; once the window has
// passed it the sweep drops the key; and the same share offered again is
// refused as stale before the index is asked, so the pruned key never
// admits it.
func TestSeenIndexPrunedBelowTheWindow(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x12}
	d := e.calcDifficulty(head, chain.byNumber[head.Number.Uint64()-1])
	hs := ShareHeaderHash(head.Hash(), h, a, 9)
	nonce, mix := solveShare(t, e, hs, h, d)
	if ok, reason := e.SubmitShare(chain, a, 9, nonce, mix, h); !ok {
		t.Fatalf("a valid share refused: %s", reason)
	}
	if ok, reason := e.SubmitShare(chain, a, 9, nonce, mix, h); ok || reason != errShareDuplicate.Error() {
		t.Fatalf("the same share twice: ok %v reason %q", ok, reason)
	}
	p := &e.sharePool
	p.mu.Lock()
	n := len(p.seen)
	p.mu.Unlock()
	if n != 1 {
		t.Fatalf("the index holds %d keys after one accepted share", n)
	}
	// The window passes the share: its parent leaves the ancestors and the
	// sweep expires it and drops its key.
	parent := head
	for i := 0; i < int(ShareRecency); i++ {
		nh := childOf(parent)
		chain.push(nh)
		parent = nh
		e.ExpireShares(chain, nh)
	}
	p.mu.Lock()
	n = len(p.seen)
	below := p.seenPrunedBelow
	p.mu.Unlock()
	if n != 0 {
		t.Fatalf("the index holds %d keys past the window (pruned below %d)", n, below)
	}
	// The pruned share offered again: stale, never a duplicate and never
	// accepted.
	if ok, reason := e.SubmitShare(chain, a, 9, nonce, mix, h); ok || reason != errShareStale.Error() {
		t.Fatalf("the pruned share offered again: ok %v reason %q", ok, reason)
	}
	if st := e.ShareStatsNow(); st.Accepted != 1 || st.Expired != 1 || st.Pooled != 0 {
		t.Fatalf("the pool's stats: %+v", st)
	}
}

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
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
)

// PA-46 (the share gossip; the node team's half): the window-wide duplicate
// set at import, the no-double-credit across siblings, the pool's expiry
// on a non-sealing node.

// carriageChain is the share harness's chain with bodies: GetBlock as
// core.BlockChain serves it, so the carried set of an ancestor the cache
// does not hold is read from its body.
type carriageChain struct {
	*shareChain
	blocks map[common.Hash]*types.Block
}

func (c *carriageChain) GetBlock(hash common.Hash, number uint64) *types.Block {
	b := c.blocks[hash]
	if b != nil && b.NumberU64() != number {
		return nil
	}
	return b
}

// carryBlock builds a valid carrying block on parent with the given
// shares and the derived list.
func carryBlock(t *testing.T, e *Engine, chain *shareChain, parent *types.Header, list []types.MinedShare, sealer common.Address) *types.Block {
	t.Helper()
	var entries []byte
	for _, s := range list {
		entries = append(entries, s.Bytes()...)
	}
	accepted := acceptedEntries(list)
	derived, _, err := e.ShareList(chain.st, parent.Number.Uint64()+1, accepted, sealer)
	if err != nil {
		t.Fatal(err)
	}
	withdrawals := shares.Withdrawals(derived)
	root := types.MinedSharesRoot(entries)
	wroot := types.DeriveSha(types.Withdrawals(withdrawals), trie.NewStackTrie(nil))
	header := childOf(parent)
	header.SharesRoot, header.WithdrawalsHash = &root, &wroot
	header.Coinbase = common.BytesToAddress(wroot[:20])
	return types.NewBlock(header, nil, nil, nil, trie.NewStackTrie(nil)).WithWithdrawals(withdrawals).WithMinedShares(entries)
}

// mineFor solves a share for the block at height sh whose parent is anc,
// under the chain difficulty of that block (the harness keeps one
// difficulty).
func mineFor(t *testing.T, e *Engine, anc *types.Header, addr common.Address, extra uint64, k uint8) types.MinedShare {
	t.Helper()
	sh := anc.Number.Uint64() + 1
	hs := ShareHeaderHash(anc.Hash(), sh, addr, extra)
	nonce, mix := solveShare(t, e, hs, sh, anc.Difficulty)
	return types.MinedShare{Address: addr, ExtraNonce: extra, Nonce: nonce, MixDigest: mix, ParentIdx: k}
}

// TestCarriedShareRefusedInALaterBlock: a share carried by block h is
// refused in block h + 1 (k = 1, the same share) by the window-wide set,
// from the import cache and, with the cache cleared, from the ancestor's
// body through the chain reader; a fresh share at h + 1 is accepted; a
// header-only reader without the cache cannot see it (stated).
func TestCarriedShareRefusedInALaterBlock(t *testing.T) {
	e, sc := mineShareEngine(t)
	defer e.Close()
	chain := &carriageChain{shareChain: sc, blocks: make(map[common.Hash]*types.Block)}
	sealer := common.Address{0xaa}
	a := common.Address{0x11}
	parent := sc.head
	x := mineFor(t, e, parent, a, 1, 0)
	b1 := carryBlock(t, e, sc, parent, []types.MinedShare{x}, sealer)
	if err := e.VerifyMinedShares(b1, sc.st, chain); err != nil {
		t.Fatalf("block h refused: %v", err)
	}
	chain.blocks[b1.Hash()] = b1
	sc.push(b1.Header())
	// Block h + 1 carrying x again (its parent is now k = 1).
	again := x
	again.ParentIdx = 1
	b2 := carryBlock(t, e, sc, b1.Header(), []types.MinedShare{again}, sealer)
	if err := e.VerifyMinedShares(b2, sc.st, chain); !errors.Is(err, errShareCarriedBefore) {
		t.Fatalf("the second canonical carriage: %v", err)
	}
	// From the body: the cache cleared, the reader's GetBlock serves h.
	e.sharePool.mu.Lock()
	e.sharePool.carriedBy = nil
	e.sharePool.mu.Unlock()
	if err := e.VerifyMinedShares(b2, sc.st, chain); !errors.Is(err, errShareCarriedBefore) {
		t.Fatalf("the second carriage with the body read: %v", err)
	}
	// A header-only reader with no cache cannot see the earlier carriage
	// (stated; a node always reads bodies).
	e.sharePool.mu.Lock()
	e.sharePool.carriedBy = nil
	e.sharePool.mu.Unlock()
	if err := e.VerifyMinedShares(b2, sc.st, sc); err != nil {
		t.Fatalf("header-only reader: %v", err)
	}
	// A fresh share at h + 1 (on block h as parent) is accepted beside a
	// second fresh one on the grandparent.
	y := mineFor(t, e, b1.Header(), common.Address{0x22}, 2, 0)
	z := mineFor(t, e, parent, common.Address{0x33}, 3, 1)
	b3 := carryBlock(t, e, sc, b1.Header(), []types.MinedShare{y, z}, sealer)
	if err := e.VerifyMinedShares(b3, sc.st, chain); err != nil {
		t.Fatalf("fresh shares at h + 1: %v", err)
	}
	// The window's edge: the same share at k = ShareRecency - 1 is still
	// within the set's reach; the chain of ShareRecency blocks on h.
	chain.blocks[b3.Hash()] = b3
	sc.push(b3.Header())
	cur := b3.Header()
	for i := 0; i < int(ShareRecency)-3; i++ {
		nh := childOf(cur)
		sc.push(nh)
		cur = nh
	}
	edge := x
	edge.ParentIdx = uint8(cur.Number.Uint64() - parent.Number.Uint64())
	if int(edge.ParentIdx) < int(ShareRecency) {
		bEdge := carryBlock(t, e, sc, cur, []types.MinedShare{edge}, sealer)
		if err := e.VerifyMinedShares(bEdge, sc.st, chain); !errors.Is(err, errShareCarriedBefore) {
			t.Fatalf("the carriage at the window's edge (k %d): %v", edge.ParentIdx, err)
		}
	}
}

// TestNoDoubleCreditAcrossSiblings: two sealers hold one share and both
// carry it in sibling blocks; the canonical state pays it once (the
// sibling's Finalize wrote into its own state, never the canonical one),
// and the credits in state are the canonical block's alone.
func TestNoDoubleCreditAcrossSiblings(t *testing.T) {
	e := shareEngine(t)
	defer e.Close()
	st := nativeState(t, big.NewInt(1_000_000))
	x := common.Address{0x11}
	sealerA, sealerB := common.Address{0xaa}, common.Address{0xbb}
	accepted := []shares.Entry{{Address: x, Weight: 1}}
	listA, carryA, err := e.ShareList(st, 5, accepted, sealerA)
	if err != nil {
		t.Fatal(err)
	}
	// The sibling's state is a copy of the parent's, as a side import's is.
	sibling := st.Copy()
	listB, carryB, err := e.ShareList(sibling, 5, accepted, sealerB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.SettleShares(st, big.NewInt(2_000), listA, carryA); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SettleShares(sibling, big.NewInt(2_000), listB, carryB); err != nil {
		t.Fatal(err)
	}
	// Canonical: x paid once (1,000 of 2,000 at weights 1 and 1), sealer A
	// paid, sealer B nothing; the sibling's state holds its own payment
	// and never reaches the canonical state.
	if got := st.GetBalance(x).Uint64(); got != 1_000 {
		t.Fatalf("x paid %d in the canonical state, want 1000", got)
	}
	if st.GetBalance(sealerA).Uint64() != 1_000 || st.GetBalance(sealerB).Uint64() != 0 {
		t.Fatalf("sealers in the canonical state: A %d, B %d", st.GetBalance(sealerA).Uint64(), st.GetBalance(sealerB).Uint64())
	}
	if sibling.GetBalance(x).Uint64() != 1_000 || sibling.GetBalance(sealerB).Uint64() != 1_000 {
		t.Fatal("the sibling's own state")
	}
	// A credit (an address past the list's limit) is the same: in one
	// state only.
	var many []shares.Entry
	for i := 0; i < shares.Ceiling+5; i++ {
		many = append(many, shares.Entry{Address: common.BigToAddress(big.NewInt(int64(1000 + i))), Weight: 1})
	}
	listC, carryC, err := e.ShareList(st, 6, many, sealerA)
	if err != nil || len(carryC) != 6 {
		t.Fatalf("the over-limit list: %d credits, %v", len(carryC), err)
	}
	sib2 := st.Copy()
	if _, err := e.SettleShares(st, big.NewInt(1), listC, carryC); err != nil {
		t.Fatal(err)
	}
	held, _ := e.ShareCarry(st)
	if len(held) == 0 {
		t.Fatal("no credits written in the canonical state")
	}
	if sibHeld, _ := e.ShareCarry(sib2); len(sibHeld) != 0 {
		t.Fatal("the sibling's copy holds the canonical credits")
	}
}

// TestExpireSharesOnANonSealingNode: a node that never builds a template
// still drops the shares whose parent left the window when the gossip
// handler calls ExpireShares at each new head; the count is by name.
func TestExpireSharesOnANonSealingNode(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x11}
	d := e.calcDifficulty(head, chain.byNumber[head.Number.Uint64()-1])
	hs := ShareHeaderHash(head.Hash(), h, a, 9)
	nonce, mix := solveShare(t, e, hs, h, d)
	if ok, reason := e.SubmitShare(chain, a, 9, nonce, mix, h); !ok {
		t.Fatalf("a valid share refused: %s", reason)
	}
	// Seven heads on: still within the window, nothing expires.
	parent := head
	for i := 0; i < int(ShareRecency)-1; i++ {
		nh := childOf(parent)
		chain.push(nh)
		parent = nh
		if n := e.ExpireShares(chain, nh); n != 0 {
			t.Fatalf("expired %d at head %v inside the window", n, nh.Number)
		}
	}
	if st := e.ShareStatsNow(); st.Pooled != 1 || st.Expired != 0 {
		t.Fatalf("inside the window: %+v", st)
	}
	// The eighth head: the share's parent leaves the window; expired by
	// name at the height it was accepted at.
	nh := childOf(parent)
	chain.push(nh)
	if n := e.ExpireShares(chain, nh); n != 1 {
		t.Fatalf("expired %d past the window, want 1", n)
	}
	st := e.ShareStatsNow()
	if st.Pooled != 0 || st.Expired != 1 || st.ExpiredNoBlock[h] != 1 {
		t.Fatalf("past the window: %+v", st)
	}
	// Off the lane or no head: nothing.
	if e.ExpireShares(chain, nil) != 0 {
		t.Fatal("expiry with no head")
	}
}

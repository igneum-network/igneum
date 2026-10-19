// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package igneum

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestEmptyListHoldAfterReorg reproduces the old public test network's (chain 7320) 10,325 (15 October
// 2026): a block carrying the pool's shares is dropped by a one-block
// reorganisation for a twin at its height, its shares put back naming the
// dropped parent, and the sealer's next template on the twin has nothing to
// carry while shares flow. The template HOLDS (a wait, not a seal with an
// empty list) for one block interval after the put-back, and the moment a
// share on the twin is pooled the template carries it.
func TestEmptyListHoldAfterReorg(t *testing.T) {
	e, sc := mineShareEngine(t)
	defer e.Close()
	chain := &carriageChain{shareChain: sc, blocks: make(map[common.Hash]*types.Block)}
	e.SetGossipChain(chain)
	head := sc.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x55}
	sealer := common.Address{0xaa}
	// A share on the head, carried by a block at h.
	d := e.calcDifficulty(head, sc.byNumber[head.Number.Uint64()-1])
	hs := ShareHeaderHash(head.Hash(), h, a, 1)
	nonce, mix := solveShare(t, e, hs, h, d)
	if ok, reason := e.SubmitShareOn(chain, a, 1, nonce, mix, h, head.Hash()); !ok {
		t.Fatalf("the share on the head refused: %s", reason)
	}
	entries, _, err := e.TemplateShares(chain, head, sc.st, sealer)
	if err != nil || types.MinedShareCount(entries) != 1 {
		t.Fatalf("the template on the head: %v, %d entries", err, types.MinedShareCount(entries))
	}
	picked, _ := types.ParseMinedShares(entries)
	b1 := carryBlock(t, e, sc, head, picked, sealer)
	chain.blocks[b1.Hash()] = b1
	e.MarkCarried(chain, b1)
	if st := e.ShareStatsNow(); st.Pooled != 0 || st.Carried != 1 {
		t.Fatalf("after the carriage: %+v", st)
	}
	// The reorganisation: a twin of the HEAD (the block below b1) becomes
	// canonical, so b1 is dropped and its share put back, naming the old
	// head as its parent.
	twin := types.CopyHeader(head)
	twin.Extra = []byte{0xee}
	sc.push(twin)
	e.UnmarkCarried(b1)
	if st := e.ShareStatsNow(); st.Pooled != 1 {
		t.Fatalf("after the put-back: %+v", st)
	}
	// The next template on the twin at the same height: nothing carriable
	// (the put-back share names the dropped parent) and shares were
	// flowing, so it holds.
	_, _, err = e.TemplateShares(chain, twin, sc.st, sealer)
	if err == nil || !IsTemplateWait(err) || !errors.Is(err, errSortitionWait) {
		t.Fatalf("the template on the twin after the reorganisation: want the hold, got %v", err)
	}
	// A share on the twin arrives: the template carries it at once.
	dt := e.calcDifficulty(twin, sc.byNumber[twin.Number.Uint64()-1])
	hs2 := ShareHeaderHash(twin.Hash(), h, a, 2)
	n2, m2 := solveShare(t, e, hs2, h, dt)
	if ok, reason := e.SubmitShareOn(chain, a, 2, n2, m2, h, twin.Hash()); !ok {
		t.Fatalf("the share on the twin refused: %s", reason)
	}
	entries2, _, err := e.TemplateShares(chain, twin, sc.st, sealer)
	if err != nil || types.MinedShareCount(entries2) != 1 {
		t.Fatalf("the template on the twin with the new share: %v, %d entries", err, types.MinedShareCount(entries2))
	}
	// Past the interval the hold is released even with nothing to carry.
	e.sharePool.mu.Lock()
	e.sharePool.putBackAt -= int64(e.holdSeconds()) + 1
	e.sharePool.mu.Unlock()
	if hold, _ := e.emptyListHold(h); hold {
		t.Fatal("the hold outlives the block interval")
	}
}

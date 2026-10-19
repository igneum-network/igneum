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
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/ishr"
)

// TestGossipFedPoolsCarryOnce is devnet7's stall at 350 (11 October 2026,
// 15:48 UTC; the project's order of 17:0x UK): two sealers' pools fed the same
// share by the gossip; one carries it; the other's next template must
// exclude it, and a block of the other's carrying it must be refused on
// import by the window-wide set. Before the protocol team's fix the second
// sealer's template carried the share, its own validation refused the
// block, and the pool kept the share, so the sealer was stuck for good:
// this test is on two engines, not one, which is where the fault lived.
func TestGossipFedPoolsCarryOnce(t *testing.T) {
	// Two engines over one chain and state (the archive each node holds);
	// sealer 2 journals its pool, for the restart case (F-42's first).
	e1, sc := mineShareEngine(t)
	defer e1.Close()
	e2, _ := mineShareEngine(t)
	defer e2.Close()
	chain := &carriageChain{shareChain: sc, blocks: make(map[common.Hash]*types.Block)}
	e1.SetGossipChain(chain)
	e2.SetGossipChain(chain)
	journal := filepath.Join(t.TempDir(), "sealer2-shares.journal")
	if err := e2.SetShareJournal(journal, chain); err != nil {
		t.Fatal(err)
	}
	head := sc.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x11}
	d := e1.calcDifficulty(head, sc.byNumber[head.Number.Uint64()-1])
	hs := ShareHeaderHash(head.Hash(), h, a, 9)
	nonce, mix := solveShare(t, e1, hs, h, d)
	// The share reaches sealer 1 by submission and sealer 2 by the gossip
	// (ReceiveShare verifies it exactly as a submission).
	if ok, reason := e1.SubmitShareOn(chain, a, 9, nonce, mix, h, head.Hash()); !ok {
		t.Fatalf("sealer 1 refused the share: %s", reason)
	}
	wire := ishr.Share{Address: a, ExtraNonce: 9, Nonce: nonce, MixDigest: mix, ParentHash: head.Hash(), Height: h}
	if ok, reason, bad := e2.ReceiveShare(wire, "peer-1"); !ok || bad {
		t.Fatalf("sealer 2 refused the gossiped share: %s (bad %v)", reason, bad)
	}
	if s1, s2 := e1.ShareStatsNow(), e2.ShareStatsNow(); s1.Pooled != 1 || s2.Pooled != 1 {
		t.Fatalf("the pools: sealer 1 %d, sealer 2 %d", s1.Pooled, s2.Pooled)
	}
	// Sealer 1 carries it: its template on the head, the block built from
	// the template's entries and list, imported on both sealers (the
	// import check on each; sealer 1 marks the carriage on its own seal
	// path; sealer 2 is given NO MarkCarried here, since the fault was a
	// pool that learned of carriages only on its own seal path: the
	// import check alone must keep the share out of its next template).
	sealer1 := common.Address{0xaa}
	entries, list, err := e1.TemplateShares(chain, head, sc.st, sealer1)
	if err != nil || types.MinedShareCount(entries) != 1 {
		t.Fatalf("sealer 1's template: %v, %d entries", err, types.MinedShareCount(entries))
	}
	picked, _ := types.ParseMinedShares(entries)
	b1 := carryBlock(t, e1, sc, head, picked, sealer1)
	if err := e1.VerifyMinedShares(b1, sc.st, chain); err != nil {
		t.Fatalf("sealer 1's own block refused: %v", err)
	}
	if err := e2.VerifyMinedShares(b1, sc.st, chain); err != nil {
		t.Fatalf("sealer 1's block refused on sealer 2: %v", err)
	}
	_ = list
	chain.blocks[b1.Hash()] = b1
	sc.push(b1.Header())
	e1.MarkCarried(chain, b1)
	// Sealer 2's next template, on the new head, must not carry the
	// share (it was paid by block h), and its pool must not keep it.
	sealer2 := common.Address{0xbb}
	entries2, _, err := e2.TemplateShares(chain, b1.Header(), sc.st, sealer2)
	if err != nil {
		t.Fatal(err)
	}
	if n := types.MinedShareCount(entries2); n != 0 {
		t.Fatalf("sealer 2's template carries %d share(s) an earlier block carried", n)
	}
	if s2 := e2.ShareStatsNow(); s2.Pooled != 0 {
		t.Fatalf("sealer 2's pool still holds the carried share: %+v", s2)
	}
	// And a block of sealer 2's carrying it anyway (k = 1 on the new
	// head) is refused on import by the window-wide set, on both.
	again := picked[0]
	again.ParentIdx = 1
	b2 := carryBlock(t, e2, sc, b1.Header(), []types.MinedShare{again}, sealer2)
	for i, e := range []*Engine{e1, e2} {
		if err := e.VerifyMinedShares(b2, sc.st, chain); !errors.Is(err, errShareCarriedBefore) {
			t.Fatalf("engine %d: the second carriage: %v", i+1, err)
		}
	}
	// F-42's second case: after that SELF-refusal (sealer 2's own block
	// refused by its own validation, the stall's shape), sealer 2's next
	// template must drop the refused share at once, never stand at the
	// height with the same template.
	entriesAfter, _, err := e2.TemplateShares(chain, b1.Header(), sc.st, sealer2)
	if err != nil {
		t.Fatal(err)
	}
	if n := types.MinedShareCount(entriesAfter); n != 0 {
		t.Fatalf("after the self-refusal sealer 2's next template still carries %d refused share(s)", n)
	}
	// F-42's first case: sealer 2 RESTARTS (a new engine replaying its
	// journal, which holds X as accepted and no carried record of its own,
	// since another sealer carried X); the replay must not re-admit a
	// share an ancestor within the recency carried (security's pool read
	// "accepted 192, replayed 192, carried 0" is this case).
	e2.Close()
	e3, _ := mineShareEngine(t)
	defer e3.Close()
	e3.anchor(chain)
	e3.SetGossipChain(chain)
	if err := e3.SetShareJournal(journal, chain); err != nil {
		t.Fatal(err)
	}
	if s3 := e3.ShareStatsNow(); s3.Pooled != 0 || s3.Replayed != 0 {
		t.Fatalf("the restart re-admitted a carried share: %+v", s3)
	}
	entries4, _, err := e3.TemplateShares(chain, b1.Header(), sc.st, sealer2)
	if err != nil || types.MinedShareCount(entries4) != 0 {
		t.Fatalf("the restarted sealer's template carries %d share(s): %v", types.MinedShareCount(entries4), err)
	}
	e2 = e3
	// A fresh share gossiped to both after the carriage is pooled by both
	// and carried by sealer 2's template once.
	h2 := b1.NumberU64() + 1
	hs2 := ShareHeaderHash(b1.Hash(), h2, a, 10)
	d2 := e1.calcDifficulty(b1.Header(), head)
	nonce2, mix2 := solveShare(t, e1, hs2, h2, d2)
	wire2 := ishr.Share{Address: a, ExtraNonce: 10, Nonce: nonce2, MixDigest: mix2, ParentHash: b1.Hash(), Height: h2}
	for i, e := range []*Engine{e1, e2} {
		if ok, reason, _ := e.ReceiveShare(wire2, "peer"); !ok {
			t.Fatalf("engine %d refused the fresh share: %s", i+1, reason)
		}
	}
	entries3, _, err := e2.TemplateShares(chain, b1.Header(), sc.st, sealer2)
	if err != nil || types.MinedShareCount(entries3) != 1 {
		t.Fatalf("sealer 2's template after the fresh share: %v, %d entries", err, types.MinedShareCount(entries3))
	}
}

// TestGossipedShareCreditsAccount (F-147, 15 October 2026): a share that
// reaches a non-sealing engine by the gossip alone is credited to its
// address's account (igneum_shareAccount's accepted) as a submitted one
// is, with no refusal recorded; a replay credits nothing (the ledger's
// accounts are rebuilt first).
func TestGossipedShareCreditsAccount(t *testing.T) {
	e1, sc := mineShareEngine(t)
	defer e1.Close()
	e2, _ := mineShareEngine(t)
	defer e2.Close()
	chain := &carriageChain{shareChain: sc, blocks: make(map[common.Hash]*types.Block)}
	e1.SetGossipChain(chain)
	e2.SetGossipChain(chain)
	head := sc.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x47}
	d := e1.calcDifficulty(head, sc.byNumber[head.Number.Uint64()-1])
	hs := ShareHeaderHash(head.Hash(), h, a, 5)
	nonce, mix := solveShare(t, e1, hs, h, d)
	if ok, reason := e1.SubmitShareOn(chain, a, 5, nonce, mix, h, head.Hash()); !ok {
		t.Fatalf("sealer 1 refused the share: %s", reason)
	}
	wire := ishr.Share{Address: a, ExtraNonce: 5, Nonce: nonce, MixDigest: mix, ParentHash: head.Hash(), Height: h}
	if ok, reason, bad := e2.ReceiveShare(wire, "peer-1"); !ok || bad {
		t.Fatalf("the reader refused the gossiped share: %s (bad %v)", reason, bad)
	}
	a1, a2 := e1.ShareAccountOf(a), e2.ShareAccountOf(a)
	if a1.Accepted != 1 || a2.Accepted != 1 {
		t.Fatalf("the address's accepted credit: submitted %d, gossiped %d, want 1 and 1", a1.Accepted, a2.Accepted)
	}
	if a2.Refused != 0 {
		t.Fatalf("the reader recorded %d refusals on a gossiped acceptance", a2.Refused)
	}
	if s2 := e2.ShareStatsNow(); s2.Accepted != 1 || s2.Pooled != 1 {
		t.Fatalf("the reader's aggregate: %+v", s2)
	}
}

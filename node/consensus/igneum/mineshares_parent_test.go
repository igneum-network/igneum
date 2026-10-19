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
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestCarriedMarkKeyedByParent (the settlement team's ledger-20 of 04:2x UK,
// 12 October 2026, the lost-after-carriage class): two shares of one
// address, extra nonce, nonce and height mined for two SIBLING parents are
// two shares; a block carrying the one for the first sibling marks only
// that one carried, the other stays pooled, the journal's record carries
// the parent, and a restart replays the pooled one and reads the carried
// one by its parent.
func TestCarriedMarkKeyedByParent(t *testing.T) {
	e1, chain := mineShareEngine(t)
	path := filepath.Join(t.TempDir(), "igneum-shares.journal")
	if err := e1.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x77}
	// The head's share first (the chain's canonical block at the height is
	// the head the share names).
	d := e1.shareChainDifficulty(chain, head, nil)
	hs1 := ShareHeaderHash(head.Hash(), h, a, 3)
	n1, m1 := solveShare(t, e1, hs1, h, d)
	if ok, r := e1.SubmitShare(chain, a, 3, n1, m1, h); !ok {
		t.Fatalf("the share on the head refused: %s", r)
	}
	// Then a twin of the head at the same height, held by the chain (push
	// sets it canonical; the head is put back as the canonical block and
	// the head of the chain, the twin a sibling it holds by hash).
	// Then a twin of the head at the same height, canonical while its
	// share is accepted (the pool takes shares on the canonical parent
	// only; the live case is a share accepted under its parent and the
	// reorg making the other twin canonical after), then the head put back
	// as the canonical block and the head, the twin a sibling the chain
	// holds by hash.
	twin := types.CopyHeader(head)
	twin.Extra = []byte{0xee}
	chain.push(twin)
	// The twin's share with the SAME extra nonce and nonce (a worker whose
	// nonces restart at zero): a different share (H_s binds the parent),
	// solved under the twin's hash; when its nonce differs the test still
	// drives the keys by the two parents.
	dt := e1.shareChainDifficulty(chain, twin, nil)
	hs2 := ShareHeaderHash(twin.Hash(), h, a, 3)
	n2, m2 := solveShare(t, e1, hs2, h, dt)
	if ok, r := e1.SubmitShareOn(chain, a, 3, n2, m2, h, twin.Hash()); !ok {
		t.Fatalf("the share on the twin refused: %s", r)
	}
	chain.byNumber[head.Number.Uint64()] = head
	chain.head = head
	if st := e1.ShareStatsNow(); st.Pooled != 2 {
		t.Fatalf("pooled %d, want 2", st.Pooled)
	}
	// A block on the head carries the head's share: the twin's stays.
	sh := types.MinedShare{Address: a, ExtraNonce: 3, Nonce: n1, MixDigest: m1, ParentIdx: 0}
	e1.MarkCarried(chain, types.NewBlockWithHeader(childOf(head)).WithMinedShares(sh.Bytes()))
	st := e1.ShareStatsNow()
	if st.Carried != 1 || st.Pooled != 1 {
		t.Fatalf("after the carriage: carried %d pooled %d, want 1 and 1 (the twin's share keyed apart by its parent)", st.Carried, st.Pooled)
	}
	e1.sharePool.mu.Lock()
	var left []common.Hash
	for ph := range e1.sharePool.byParent {
		left = append(left, ph)
	}
	e1.sharePool.mu.Unlock()
	if len(left) != 1 || left[0] != twin.Hash() {
		t.Fatalf("the pooled share's parent is %v, want the twin %v", left, twin.Hash())
	}
	// The journal: a type-7 record with the head as the parent.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	recs := journalRecords(data)
	var carried []ShareJournalRecord
	for _, r := range recs {
		if r.Kind == "carried" {
			carried = append(carried, r)
		}
	}
	if len(carried) != 1 || carried[0].Parent == nil || *carried[0].Parent != head.Hash() || carried[0].Nonce != n1 {
		t.Fatalf("the journal's carriage records: %+v", carried)
	}
	// A restart with the head canonical: the head's share reads carried by
	// its parent key; the twin's share, whose parent is no longer the
	// canonical block at its height, is named EXPIRED BY REORG in the
	// ledger (the class the fix closes was a share carried by nothing and
	// named by nothing: the twin's share marked carried by the head's
	// carriage); accepted = carried + expiredReorg closes.
	e1.Close()
	e2 := genesisEngine(t)
	e2.config.Parts.Sortition.SharesPerBlock = 4
	e2.config.Parts.Sortition.MaxShares = 8
	e2.anchor(chain)
	defer e2.Close()
	if err := e2.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	st2 := e2.ShareStatsNow()
	if st2.Replayed != 0 || st2.Pooled != 0 || st2.Dropped != 2 {
		t.Fatalf("the replay: replayed %d pooled %d dropped %d, want both read from the ledger (carried, expired by reorg)", st2.Replayed, st2.Pooled, st2.Dropped)
	}
	acc := e2.ShareAccountOf(a)
	if acc.Accepted != 2 || acc.Carried != 1 || acc.ExpiredReorg != 1 || acc.Pooled != 0 {
		t.Fatalf("the ledger after the replay: accepted %d carried %d expiredReorg %d pooled %d, want 2, 1, 1, 0", acc.Accepted, acc.Carried, acc.ExpiredReorg, acc.Pooled)
	}
}

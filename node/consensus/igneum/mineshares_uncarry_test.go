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
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestUncarryWithdrawsTheWholeRecord (F-48's second path, 12 October
// 2026): a block's carriage is journaled for its whole list, so a share
// this pool no longer held (carried out by an earlier block here, or
// expired) gets a carried record from a twin's import; when that twin
// loses its height the uncarry must withdraw that record too, or the
// share's last record stays "carried" for a block that is not canonical
// and the account reads it paid by nobody (47 of a member's shares at 1034
// on pod d). After the drop the journal's last record for the share is
// uncarried and the replay's ledger reads it as carried by the FIRST
// block alone.
func TestUncarryWithdrawsTheWholeRecord(t *testing.T) {
	e1, chain := mineShareEngine(t)
	path := filepath.Join(t.TempDir(), "igneum-shares.journal")
	if err := e1.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x78}
	d := e1.shareChainDifficulty(chain, head, nil)
	hs := ShareHeaderHash(head.Hash(), h, a, 5)
	n, m := solveShare(t, e1, hs, h, d)
	if ok, r := e1.SubmitShare(chain, a, 5, n, m, h); !ok {
		t.Fatalf("the share refused: %s", r)
	}
	sh := types.MinedShare{Address: a, ExtraNonce: 5, Nonce: n, MixDigest: m, ParentIdx: 0}
	// The first block on the head carries the share: it leaves the pool.
	first := types.NewBlockWithHeader(childOf(head)).WithMinedShares(sh.Bytes())
	e1.MarkCarried(chain, first)
	if st := e1.ShareStatsNow(); st.Carried != 1 || st.Pooled != 0 {
		t.Fatalf("after the first carriage: carried %d pooled %d, want 1 and 0", st.Carried, st.Pooled)
	}
	// A twin of the first block carries the same share (the pool holds
	// nothing to move; the journal records the carriage all the same).
	twinHeader := childOf(head)
	twinHeader.Extra = []byte{0xee}
	twin := types.NewBlockWithHeader(twinHeader).WithMinedShares(sh.Bytes())
	e1.MarkCarried(chain, twin)
	// The twin loses its height: its carriage is withdrawn in the journal
	// though the pool moved nothing for it.
	e1.UnmarkCarried(twin)
	if st := e1.ShareStatsNow(); st.Carried != 1 || st.Pooled != 0 {
		t.Fatalf("after the twin's drop: carried %d pooled %d, want 1 and 0 (the first block's carriage stands)", st.Carried, st.Pooled)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, r := range journalRecords(data) {
		if r.Address == a && r.Nonce == n {
			kinds = append(kinds, r.Kind)
		}
	}
	want := []string{"accepted", "carried", "carried", "uncarried"}
	if len(kinds) != len(want) {
		t.Fatalf("the share's records: %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("the share's records: %v, want %v", kinds, want)
		}
	}
	// The replay: the first block's carriage stands in the ledger (the
	// uncarried record withdraws the carried KEY, which the first block
	// laid too; the replay then reads the share pooled again when its
	// window holds, which is the identity-4 form: the chain's canonical
	// carriage is the pay, the ledger the node's own account).
	e1.Close()
	e2 := genesisEngine(t)
	e2.config.Parts.Sortition.SharesPerBlock = 4
	e2.config.Parts.Sortition.MaxShares = 8
	e2.anchor(chain)
	defer e2.Close()
	if err := e2.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	acc := e2.ShareAccountOf(a)
	if acc.Accepted != 1 || acc.Carried != 0 {
		t.Fatalf("the ledger after the replay: accepted %d carried %d, want 1 and 0 (the carried key withdrawn; the chain's canonical carriage the pay)", acc.Accepted, acc.Carried)
	}
}

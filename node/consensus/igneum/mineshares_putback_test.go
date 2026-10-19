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

// TestUnmarkCarriedPutsTheSharesBack (identity 4, 12 October 2026): a share
// carried by a block that then loses its height comes back to the pool,
// the counters and the address's ledger move back, the journal writes the
// uncarried record, a second call is a no-op, and a restart replays the
// share (the carried key withdrawn by the record). The share is carriable
// again: a later carriage takes it out once more.
func TestUnmarkCarriedPutsTheSharesBack(t *testing.T) {
	e1, chain := mineShareEngine(t)
	path := filepath.Join(t.TempDir(), "igneum-shares.journal")
	if err := e1.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x44}
	d := e1.shareChainDifficulty(chain, head, nil)
	hs := ShareHeaderHash(head.Hash(), h, a, 7)
	n, m := solveShare(t, e1, hs, h, d)
	if ok, r := e1.SubmitShare(chain, a, 7, n, m, h); !ok {
		t.Fatalf("share refused: %s", r)
	}
	sh := types.MinedShare{Address: a, ExtraNonce: 7, Nonce: n, MixDigest: m, ParentIdx: 0}
	block := types.NewBlockWithHeader(childOf(head)).WithMinedShares(sh.Bytes())
	e1.MarkCarried(chain, block)
	if st := e1.ShareStatsNow(); st.Pooled != 0 || st.Carried != 1 || st.Uncarried != 0 {
		t.Fatalf("after the carriage: pooled %d carried %d uncarried %d", st.Pooled, st.Carried, st.Uncarried)
	}
	if acc := e1.ShareAccountOf(a); acc.Pooled != 0 || acc.Carried != 1 {
		t.Fatalf("the ledger after the carriage: pooled %d carried %d", acc.Pooled, acc.Carried)
	}
	// The block loses its height.
	e1.UnmarkCarried(block)
	st := e1.ShareStatsNow()
	if st.Pooled != 1 || st.Carried != 0 || st.Uncarried != 1 {
		t.Fatalf("after the uncarry: pooled %d carried %d uncarried %d", st.Pooled, st.Carried, st.Uncarried)
	}
	if acc := e1.ShareAccountOf(a); acc.Pooled != 1 || acc.Carried != 0 || acc.Accepted != 1 {
		t.Fatalf("the ledger after the uncarry: pooled %d carried %d accepted %d", acc.Pooled, acc.Carried, acc.Accepted)
	}
	// A second call for the same block changes nothing.
	e1.UnmarkCarried(block)
	if st := e1.ShareStatsNow(); st.Pooled != 1 || st.Uncarried != 1 {
		t.Fatalf("a second uncarry moved the counters: pooled %d uncarried %d", st.Pooled, st.Uncarried)
	}
	// The journal: the carried key withdrawn, the uncarried record read.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if parseShareJournal(data).carried[carriedAtKey(a, 7, n, h)] {
		t.Fatal("the carried key stands after the uncarried record")
	}
	var kinds []string
	for _, r := range journalRecords(data) {
		kinds = append(kinds, r.Kind)
	}
	if len(kinds) != 3 || kinds[0] != "accepted" || kinds[1] != "carried" || kinds[2] != "uncarried" {
		t.Fatalf("the journal's records read %v, want [accepted carried uncarried]", kinds)
	}
	// The share is carriable again.
	e1.MarkCarried(chain, types.NewBlockWithHeader(childOf(head)).WithMinedShares(sh.Bytes()))
	if st := e1.ShareStatsNow(); st.Pooled != 0 || st.Carried != 1 {
		t.Fatalf("after the second carriage: pooled %d carried %d", st.Pooled, st.Carried)
	}
	e1.Close()
	// A restart from a journal ending in the uncarried record replays
	// the share.
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cut := journalRecords(data)
	if len(cut) != 4 || cut[3].Kind != "carried" {
		t.Fatalf("the journal's records read %d, the last %q", len(cut), cut[len(cut)-1].Kind)
	}
	if err := os.WriteFile(path, data[:cut[3].Offset], 0o600); err != nil {
		t.Fatal(err)
	}
	e2 := genesisEngine(t)
	e2.config.Parts.Sortition.SharesPerBlock = 4
	e2.config.Parts.Sortition.MaxShares = 8
	e2.anchor(chain)
	defer e2.Close()
	if err := e2.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	if st := e2.ShareStatsNow(); st.Replayed != 1 || st.Dropped != 0 || st.Pooled != 1 {
		t.Fatalf("the replay after an uncarried record: replayed %d dropped %d pooled %d", st.Replayed, st.Dropped, st.Pooled)
	}
}

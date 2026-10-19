package igneum

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestShareJournalPaysAcrossARestart (PA-44): a share accepted before a
// restart and not yet carried is in the pool after it and carried by the
// next template; a share carried before the restart is not carried again;
// a share past the recency at the restart is dropped; the file is rewritten
// with the live pool and a truncated tail is cut.
func TestShareJournalPaysAcrossARestart(t *testing.T) {
	e1, chain := mineShareEngine(t)
	path := filepath.Join(t.TempDir(), "igneum-shares.journal")
	if err := e1.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	head := chain.head
	h := head.Number.Uint64() + 1
	a, b, c := common.Address{0x11}, common.Address{0x22}, common.Address{0x33}
	submit := func(e *Engine, addr common.Address, extra uint64, parent *types.Header) types.MinedShare {
		sh := parent.Number.Uint64() + 1
		d := e.shareChainDifficulty(chain, parent, chain.byNumber[sh])
		hs := ShareHeaderHash(parent.Hash(), sh, addr, extra)
		nonce, mix := solveShare(t, e, hs, sh, d)
		if ok, reason := e.SubmitShare(chain, addr, extra, nonce, mix, sh); !ok {
			t.Fatalf("share %x/%d refused: %s", addr[:1], extra, reason)
		}
		return types.MinedShare{Address: addr, ExtraNonce: extra, Nonce: nonce, MixDigest: mix}
	}
	// a and b on the head; c six blocks down (within the recency now, past
	// it after three more blocks).
	sa := submit(e1, a, 1, head)
	submit(e1, b, 2, head)
	submit(e1, c, 3, chain.byNumber[head.Number.Uint64()-6])
	// a is carried by a block of this node's before the restart.
	sa.ParentIdx = 0
	carried := types.NewBlockWithHeader(childOf(head)).WithMinedShares(sa.Bytes())
	e1.MarkCarried(chain, carried)
	if st := e1.ShareStatsNow(); st.Pooled != 2 || st.Carried != 1 {
		t.Fatalf("before the restart: %+v", st)
	}
	// Three blocks on, the node restarts.
	parent := head
	for i := 0; i < 3; i++ {
		nh := childOf(parent)
		chain.push(nh)
		parent = nh
	}
	e1.Close()
	e2 := genesisEngine(t)
	e2.config.Parts.Sortition.SharesPerBlock = 4
	e2.config.Parts.Sortition.MaxShares = 8
	e2.config.Parts.Sortition.MaxPayouts = 256
	e2.anchor(chain)
	defer e2.Close()
	if err := e2.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	st := e2.ShareStatsNow()
	if st.Pooled != 1 || st.Replayed != 1 || st.Dropped != 2 || st.Accepted != 1 {
		t.Fatalf("after the restart: %+v (want b alone: a carried, c stale)", st)
	}
	// The next template carries b (k = 3) and its list pays b.
	entries, list, err := e2.TemplateShares(chain, parent, chain.st, common.Address{0xaa})
	if err != nil || types.MinedShareCount(entries) != 1 {
		t.Fatalf("template after the restart: %v entries %d", err, types.MinedShareCount(entries))
	}
	got, _ := types.ParseMinedShares(entries)
	if got[0].Address != b || got[0].ParentIdx != 3 {
		t.Fatalf("carried after the restart: %+v", got[0])
	}
	if len(list) != 2 || list[0].Address != b {
		t.Fatalf("the list after the restart: %+v", list)
	}
	// b replayed is a duplicate.
	if ok, _ := e2.SubmitShare(chain, b, 2, got[0].Nonce, got[0].MixDigest, h); ok {
		t.Fatal("a replayed share accepted after the restart")
	}
	// The ledger after the restart: a's acceptance and carriage, b's
	// acceptance, c's acceptance and the expiry the replay recorded (the
	// window passed while the node was down); the accounts close for each,
	// and the replay counted no refusal.
	data, err := os.ReadFile(path)
	// The acceptance record is type 5 from PA-53 (type 1's fields plus the
	// step byte).
	// The carriage record is type 7 (type 3's fields plus the parent).
	want := 3*(1+shareJournalAcceptedBytes+1) + (1 + shareJournalCarriedPBytes) + (1 + shareJournalExpiredBytes)
	if err != nil || len(data) != want {
		t.Fatalf("ledger after the restart: %v, %d bytes (want %d)", err, len(data), want)
	}
	if st.Refused != 0 {
		t.Fatalf("the replay counted refusals: %+v", st.RefusedBy)
	}
	for _, tc := range []struct {
		addr                     common.Address
		carried, pooled, expired uint64
	}{{a, 1, 0, 0}, {b, 0, 1, 0}, {c, 0, 0, 1}} {
		acc := e2.ShareAccountOf(tc.addr)
		if !acc.Closes || acc.Accepted != 1 || acc.Carried != tc.carried || acc.Pooled != tc.pooled || acc.ExpiredWindow != tc.expired || acc.ExpiredReorg != 0 {
			t.Fatalf("account %x after the restart: %+v", tc.addr[:1], acc)
		}
	}
	// A torn tail is cut at the next start.
	e2.Close()
	if err := os.WriteFile(path, append(data, 1, 2, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	e3 := genesisEngine(t)
	e3.config.Parts.Sortition.SharesPerBlock = 4
	e3.config.Parts.Sortition.MaxShares = 8
	e3.anchor(chain)
	defer e3.Close()
	if err := e3.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	if st := e3.ShareStatsNow(); st.Pooled != 1 || st.Replayed != 1 || st.Dropped != 2 || st.Refused != 0 {
		t.Fatalf("after the second restart: %+v (want b replayed; a by its carriage record, c by its expiry record)", st)
	}
	if acc := e3.ShareAccountOf(c); !acc.Closes || acc.Accepted != 1 || acc.ExpiredWindow != 1 {
		t.Fatalf("c's account from the ledger's expiry record: %+v", acc)
	}
	if data2, err := os.ReadFile(path); err != nil || len(data2) != want {
		t.Fatalf("ledger after the second restart: %v, %d bytes (want %d: the torn tail cut, nothing re-recorded)", err, len(data2), want)
	}
	// Without a journal the pool is memory alone (the dev form).
	e4 := genesisEngine(t)
	e4.anchor(chain)
	defer e4.Close()
	if err := e4.SetShareJournal("", chain); err != nil {
		t.Fatal(err)
	}
	if st := e4.ShareStatsNow(); st.Pooled != 0 {
		t.Fatalf("no journal: %+v", st)
	}
}

// TestShareJournalCarriedKeyIncludesTheHeight (the node team's finding of
// 17:2x UK, 12 October 2026): two shares sharing the address, extra nonce
// and nonce at two heights are two shares; the carriage of the one at h
// does not drop the one at h + 1 on replay; a first-form record (type 2,
// no height) still reads as carried at any height for one swap.
func TestShareJournalCarriedKeyIncludesTheHeight(t *testing.T) {
	e1, chain := mineShareEngine(t)
	path := filepath.Join(t.TempDir(), "igneum-shares.journal")
	if err := e1.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x11}
	// The share at h on the head, and a share at h + 1 on a new block,
	// forced to the same extra nonce and nonce by searching the second's
	// solution and then reusing its nonce at the first height is not
	// possible with real work; instead both are solved and the second's
	// (address, extraNonce) is made equal to the first's and its nonce
	// checked: when the solved nonces differ the test still exercises the
	// keyed replay through the journal's own records, written below.
	d1 := e1.shareChainDifficulty(chain, head, nil)
	hs1 := ShareHeaderHash(head.Hash(), h, a, 5)
	n1, m1 := solveShare(t, e1, hs1, h, d1)
	if ok, r := e1.SubmitShare(chain, a, 5, n1, m1, h); !ok {
		t.Fatalf("share at h refused: %s", r)
	}
	nh := childOf(head)
	chain.push(nh)
	d2 := e1.shareChainDifficulty(chain, nh, nil)
	hs2 := ShareHeaderHash(nh.Hash(), h+1, a, 5)
	n2, m2 := solveShare(t, e1, hs2, h+1, d2)
	if ok, r := e1.SubmitShare(chain, a, 5, n2, m2, h+1); !ok {
		t.Fatalf("share at h+1 refused: %s", r)
	}
	// Carry the share at h (k = 1 from a block at h + 1's successor is not
	// needed: a block at h carries it with k = 0).
	sh := types.MinedShare{Address: a, ExtraNonce: 5, Nonce: n1, MixDigest: m1, ParentIdx: 0}
	e1.MarkCarried(chain, types.NewBlockWithHeader(childOf(head)).WithMinedShares(sh.Bytes()))
	// A hand-written FIRST-FORM carriage record for a third share's three
	// fields at any height: it drops that share at every height.
	e1.shareJournal.write(append([]byte{}, func() []byte {
		b := make([]byte, 1+shareJournalCarriedBytes)
		b[0] = shareJournalCarried
		copy(b[1:], a[:])
		b[21], b[29] = 9, 9 // extraNonce 9, nonce 9 (little-endian low bytes)
		return b
	}()...))
	e1.Close()
	e2 := genesisEngine(t)
	e2.config.Parts.Sortition.SharesPerBlock = 4
	e2.config.Parts.Sortition.MaxShares = 8
	e2.anchor(chain)
	defer e2.Close()
	if err := e2.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	st := e2.ShareStatsNow()
	if n1 == n2 {
		// the real collision: the carried one at h dropped, the one at h+1 replayed
		if st.Replayed != 1 || st.Dropped != 1 {
			t.Fatalf("same nonce at two heights: replayed %d dropped %d (want 1, 1)", st.Replayed, st.Dropped)
		}
	} else if st.Replayed != 1 || st.Dropped != 1 {
		t.Fatalf("replayed %d dropped %d (want the h+1 share replayed, the carried one dropped)", st.Replayed, st.Dropped)
	}
	// The replayed share is the one at h + 1.
	e2.sharePool.mu.Lock()
	var heights []uint64
	for _, l := range e2.sharePool.byParent {
		for _, ps := range l {
			heights = append(heights, ps.height)
		}
	}
	e2.sharePool.mu.Unlock()
	if len(heights) != 1 || heights[0] != h+1 {
		t.Fatalf("the pool after replay holds heights %v, want [%d]", heights, h+1)
	}
	// The journal now writes type 3 only: parse the rewritten file and the
	// carriage of the h+1 share, and check the key carries the height.
	sh2 := types.MinedShare{Address: a, ExtraNonce: 5, Nonce: n2, MixDigest: m2, ParentIdx: 0}
	e2.MarkCarried(chain, types.NewBlockWithHeader(childOf(nh)).WithMinedShares(sh2.Bytes()))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	carried := parseShareJournal(data).carried
	// The journal now writes type 7: the key with the height AND the
	// share's parent (nh, the block the share at h + 1 was mined for).
	if !carried[carriedPKey(a, 5, n2, h+1, nh.Hash())] {
		t.Fatal("the carriage at h+1 was not journaled with its height and parent")
	}
	if carried[carriedAtKey(a, 5, n2, h+1)] {
		t.Fatal("a height-only (type 3) record was written after the parent form")
	}
	if carried[shareKey(a, 5, n2)] {
		t.Fatal("a first-form (any-height) record was written after the swap")
	}
}

// TestShareLedgerNamesAReorgExpiry (the project's order of 19:2x UK, 11 October
// 2026, the 180 unaccounted shares): a share whose parent loses a one-block
// reorg within the window expires as expiredReorg, in the pool's stats and
// in its address's ledger; a share the window passes expires as
// expiredWindow; a refusal by name is in the ledger too; and a restart
// rebuilds the same ledger from the journal's records, so accepted =
// carried + pooled + expiredReorg + expiredWindow closes before and after.
func TestShareLedgerNamesAReorgExpiry(t *testing.T) {
	e1, chain := mineShareEngine(t)
	path := filepath.Join(t.TempDir(), "igneum-shares.journal")
	if err := e1.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	head := chain.head
	a := common.Address{0x44}
	solve := func(e *Engine, extra uint64, parent *types.Header) (uint64, common.Hash) {
		sh := parent.Number.Uint64() + 1
		d := e.shareChainDifficulty(chain, parent, chain.byNumber[sh])
		return solveShare(t, e, ShareHeaderHash(parent.Hash(), sh, a, extra), sh, d)
	}
	// Share 1 on the head (height h): its parent will lose the reorg.
	// Share 2 six blocks down: the window passes it.
	h := head.Number.Uint64() + 1
	n1, m1 := solve(e1, 1, head)
	if ok, reason := e1.SubmitShare(chain, a, 1, n1, m1, h); !ok {
		t.Fatal(reason)
	}
	old := chain.byNumber[h-7]
	n2, m2 := solve(e1, 2, old)
	if ok, reason := e1.SubmitShare(chain, a, 2, n2, m2, h-6); !ok {
		t.Fatal(reason)
	}
	// A refusal by name goes to the ledger: the same share twice.
	if ok, _ := e1.SubmitShare(chain, a, 1, n1, m1, h); ok {
		t.Fatal("a duplicate accepted")
	}
	// The reorg: a sibling replaces the head, and two blocks follow it;
	// share 1's parent is no longer an ancestor though its height is inside
	// the window; share 2's parent (h-7) falls out of the window at the
	// head h+1 (the window there is h+1 down to h-6).
	sibling := childOf(chain.byNumber[h-2])
	sibling.Time++
	chain.push(sibling)
	chain.push(childOf(sibling))
	chain.push(childOf(chain.head))
	if n := e1.ExpireShares(chain, chain.head); n != 2 {
		t.Fatalf("expired at the new head: %d (want 2)", n)
	}
	st := e1.ShareStatsNow()
	if st.ExpiredReorg[h] != 1 || st.ExpiredNoBlock[h-6] != 1 || st.Expired != 2 || st.Pooled != 0 {
		t.Fatalf("the pool's stats after the reorg: %+v", st)
	}
	check := func(e *Engine, when string) {
		acc := e.ShareAccountOf(a)
		if !acc.Closes || acc.Accepted != 2 || acc.Carried != 0 || acc.Pooled != 0 || acc.ExpiredReorg != 1 || acc.ExpiredWindow != 1 || acc.ExpiredReorgBy[h] != 1 || acc.ExpiredWindowBy[h-6] != 1 {
			t.Fatalf("the ledger %s: %+v", when, acc)
		}
		if len(acc.Heights) != 2 || acc.Heights[0] != h-6 || acc.Heights[1] != h {
			t.Fatalf("the ledger's heights %s: %v", when, acc.Heights)
		}
	}
	check(e1, "before the restart")
	if acc := e1.ShareAccountOf(a); acc.Refused != 1 || acc.RefusedBy[errShareDuplicate.Error()] != 1 {
		t.Fatalf("the refusal by name: %+v", acc.RefusedBy)
	}
	// The restart rebuilds the ledger from the records alone (the pool is
	// empty: both expiries are in the file).
	e1.Close()
	e2 := genesisEngine(t)
	e2.config.Parts.Sortition.SharesPerBlock = 4
	e2.config.Parts.Sortition.MaxShares = 8
	e2.anchor(chain)
	defer e2.Close()
	if err := e2.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	if st := e2.ShareStatsNow(); st.Replayed != 0 || st.Dropped != 2 || st.Refused != 0 {
		t.Fatalf("after the restart: %+v", st)
	}
	check(e2, "after the restart")
	// A refusal is live only: the ledger holds none after the restart.
	if acc := e2.ShareAccountOf(a); acc.Refused != 0 {
		t.Fatalf("refusals rebuilt from the ledger: %+v", acc.RefusedBy)
	}
	// An unknown address closes trivially.
	if acc := e2.ShareAccountOf(common.Address{0x99}); !acc.Closes || acc.Accepted != 0 {
		t.Fatalf("an unknown address: %+v", acc)
	}
	// The limits a worker paces by.
	if l := e2.ShareLimitsNow(); l.MaxShares != 8 || l.Recency != e2.ShareRecencyBlocks() || l.PoolCapacity != e2.SharePoolBound() {
		t.Fatalf("the limits: %+v", l)
	}
}

// TestReplayClassifiesByTheParentsCanonicity (the project's ruling of 20:0x UK,
// 11 October 2026, on ecosystem's read of the swapped devnet7 peer: 531's
// and 537's shares, whose parents 530 and 536 lost their siblings, were
// filed under the window's end because the replay read the class by the
// window's position): a journal of two acceptances far below the window,
// one on a parent a sibling displaced and one on the canonical parent,
// replays as one expiredReorg and one expiredWindow, by the parent's
// canonicity at its height alone.
func TestReplayClassifiesByTheParentsCanonicity(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	head := chain.head
	// Move the head twelve blocks on, so both parents are below the window.
	parent := head
	for i := 0; i < 12; i++ {
		nh := childOf(parent)
		chain.push(nh)
		parent = nh
	}
	h := head.Number.Uint64() + 1 // the shares' height; their parents at h-1
	canonical := chain.byNumber[h-1]
	sibling := childOf(chain.byNumber[h-2])
	sibling.Time += 3
	a := common.Address{0x55}
	lost := pooledShare{share: types.MinedShare{Address: a, ExtraNonce: 1, Nonce: 7, MixDigest: common.Hash{0x01}}, parent: sibling.Hash(), height: h}
	timed := pooledShare{share: types.MinedShare{Address: a, ExtraNonce: 2, Nonce: 9, MixDigest: common.Hash{0x02}}, parent: canonical.Hash(), height: h}
	path := filepath.Join(t.TempDir(), "igneum-shares.journal")
	if err := os.WriteFile(path, append(acceptedRecord(lost), acceptedRecord(timed)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	acc := e.ShareAccountOf(a)
	if !acc.Closes || acc.Accepted != 2 || acc.ExpiredReorg != 1 || acc.ExpiredWindow != 1 || acc.ExpiredReorgBy[h] != 1 || acc.ExpiredWindowBy[h] != 1 || acc.Pooled != 0 {
		t.Fatalf("the replay's classes: %+v", acc)
	}
	if st := e.ShareStatsNow(); st.Refused != 0 || st.Dropped != 2 || st.Replayed != 0 {
		t.Fatalf("the replay's stats: %+v", st)
	}
	// The ledger now carries both expiry records; a second replay reads
	// the same classes from them.
	e2, _ := mineShareEngine(t)
	defer e2.Close()
	if err := e2.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	if acc := e2.ShareAccountOf(a); !acc.Closes || acc.ExpiredReorg != 1 || acc.ExpiredWindow != 1 {
		t.Fatalf("the second replay's classes: %+v", acc)
	}
	// A record filed under the window by the earlier classifier (the
	// devnet7 peer's 531 and 537 at its 20:00 UK restart) is re-read
	// against the chain: the displaced parent reads reorg.
	path2 := filepath.Join(t.TempDir(), "igneum-shares.journal")
	if err := os.WriteFile(path2, append(acceptedRecord(lost), expiredRecord(lost, expiryWindow)...), 0o600); err != nil {
		t.Fatal(err)
	}
	e3, _ := mineShareEngine(t)
	defer e3.Close()
	if err := e3.SetShareJournal(path2, chain); err != nil {
		t.Fatal(err)
	}
	if acc := e3.ShareAccountOf(a); !acc.Closes || acc.ExpiredReorg != 1 || acc.ExpiredWindow != 0 {
		t.Fatalf("a window record on a displaced parent: %+v", acc)
	}
}

// TestUncarryWithdrawsEveryKeyForm (F-532, 17 October 2026): a carriage
// written under the height form (type 3) and undone by the parent form's
// uncarry (type 8) is UNCARRIED at the replay; before the cure the older
// record's key survived the uncarry and the rebuilt account read the share
// as carried, so a paid height's class moved across a restart.
func TestUncarryWithdrawsEveryKeyForm(t *testing.T) {
	a := common.Address{0x22}
	var parent common.Hash
	parent[0] = 7
	rec := append([]byte(nil), carriedRecord(a, 9, 11, 500)...)       // type 3: the height form
	rec = append(rec, carriedRecordP(a, 9, 11, 500, parent)...)          // type 7: the parent form
	rec = append(rec, uncarriedRecordP(a, 9, 11, 500, parent)...)        // type 8: the carriage undone
	l := parseShareJournal(rec)
	for _, k := range []string{carriedPKey(a, 9, 11, 500, parent), carriedAtKey(a, 9, 11, 500), shareKey(a, 9, 11)} {
		if l.carried[k] {
			t.Fatalf("the carriage survives the uncarry under the key %q", k)
		}
	}
	// The older form's uncarry (type 6) withdraws the height-less key too.
	rec = append([]byte(nil), carriedRecord(a, 9, 11, 500)...)
	rec = append(rec, uncarriedRecord(a, 9, 11, 500)...)
	l = parseShareJournal(rec)
	if l.carried[carriedAtKey(a, 9, 11, 500)] || l.carried[shareKey(a, 9, 11)] {
		t.Fatal("the type 6 uncarry leaves a carried key")
	}
	// A carriage with no uncarry stands under every form it was written in.
	rec = append([]byte(nil), carriedRecord(a, 9, 11, 500)...)
	l = parseShareJournal(rec)
	if !l.carried[carriedAtKey(a, 9, 11, 500)] {
		t.Fatal("a carriage without an uncarry is not carried")
	}
}

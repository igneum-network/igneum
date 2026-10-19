package igneum

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
)

// shareChain is a fakeChain with a canonical header line and the parent's
// state, as the state processor and the worker see the chain.
type shareChain struct {
	*fakeChain
	byNumber map[uint64]*types.Header
	head     *types.Header
	st       *state.StateDB
}

func (c *shareChain) StateAt(root common.Hash) (*state.StateDB, error) { return c.st, nil }
func (c *shareChain) CurrentHeader() *types.Header                     { return c.head }
func (c *shareChain) GetHeaderByNumber(n uint64) *types.Header         { return c.byNumber[n] }
func (c *shareChain) push(h *types.Header) {
	c.add(h)
	c.byNumber[h.Number.Uint64()] = h
	c.head = h
}

// shareEngine is the genesis form with the share lane on (sharesPerBlock 4
// so the null algorithm's work is cheap to find and the quarter's rounding
// shows), the sealer's own entry at the test's coinbase.
func mineShareEngine(t *testing.T) (*Engine, *shareChain) {
	t.Helper()
	e := genesisEngine(t)
	e.config.Parts.Sortition.SharesPerBlock = 4
	e.config.Parts.Sortition.MaxShares = 8
	e.config.Parts.Sortition.MaxPayouts = 256
	st := nativeState(t, big.NewInt(1_000_000_000))
	chain := &shareChain{fakeChain: newFakeChain(), byNumber: make(map[uint64]*types.Header), st: st}
	g := genesisHeader()
	chain.push(g)
	parent := g
	for i := 0; i < 10; i++ {
		h := childOf(parent)
		chain.push(h)
		parent = h
	}
	e.anchor(chain)
	return e, chain
}

// solveShare finds a nonce whose null-algorithm work on H_s meets the share
// target of difficulty d.
func solveShare(t *testing.T, e *Engine, hs common.Hash, height uint64, d *big.Int) (uint64, common.Hash) {
	t.Helper()
	nonce, mix, ok := pow.Solve(e.algo, hs, height, e.shareDifficulty(d), 0, nil)
	if !ok {
		t.Fatal("no share found")
	}
	return nonce, mix
}

func TestShareHeaderAndWork(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	parent := chain.head
	h := parent.Number.Uint64() + 1
	a, b := common.Address{0x11}, common.Address{0x22}
	if ShareHeaderHash(parent.Hash(), h, a, 7) == ShareHeaderHash(parent.Hash(), h, b, 7) || ShareHeaderHash(parent.Hash(), h, a, 7) == ShareHeaderHash(parent.Hash(), h, a, 8) {
		t.Fatal("the share header is not bound to the address and the extra nonce")
	}
	d := parent.Difficulty
	hs := ShareHeaderHash(parent.Hash(), h, a, 7)
	nonce, mix := solveShare(t, e, hs, h, d)
	if err := e.verifyShareWork(hs, h, nonce, mix, d); err != nil {
		t.Fatalf("a found share refused: %v", err)
	}
	if err := e.verifyShareWork(hs, h, nonce+1, mix, d); err == nil {
		t.Fatal("a wrong nonce accepted")
	}
	// The share target from the work package: the block target times the
	// count (within the integer floor).
	wp := pow.WorkPackage{Target: pow.Target(d)}
	hdr := &types.Header{Number: new(big.Int).SetUint64(h), ParentHash: parent.Hash(), Difficulty: d}
	e.shareWorkFields(&wp, hdr)
	if wp.ShareTarget == nil || wp.SharesPerBlock != 4 || wp.ShareHeight != h || wp.ShareParent != parent.Hash() {
		t.Fatalf("share fields: %+v", wp)
	}
	if wp.ShareTarget.Cmp(wp.Target) <= 0 {
		t.Fatal("the share target is not easier than the block target")
	}
}

func TestMinedSharesValidationAndList(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	parent := chain.head
	h := parent.Number.Uint64() + 1
	sealer := common.Address{0xaa}
	// Three shares for two addresses on the parent (k = 0) and one on the
	// grandparent (k = 1).
	var list []types.MinedShare
	mk := func(addr common.Address, extra uint64, k uint8) types.MinedShare {
		anc := chain.byNumber[parent.Number.Uint64()-uint64(k)]
		sh := anc.Number.Uint64() + 1
		var d *big.Int
		if k == 0 {
			d = parent.Difficulty // the carrying block's difficulty (childOf keeps it)
		} else {
			d = chain.byNumber[sh].Difficulty
		}
		hs := ShareHeaderHash(anc.Hash(), sh, addr, extra)
		nonce, mix := solveShare(t, e, hs, sh, d)
		return types.MinedShare{Address: addr, ExtraNonce: extra, Nonce: nonce, MixDigest: mix, ParentIdx: k}
	}
	a, b := common.Address{0x11}, common.Address{0x22}
	list = append(list, mk(a, 1, 0), mk(a, 2, 0), mk(b, 3, 1))
	var entries []byte
	for _, s := range list {
		entries = append(entries, s.Bytes()...)
	}
	accepted := acceptedEntries(list)
	if len(accepted) != 2 || accepted[0].Address != a || accepted[0].Weight != 2 || accepted[1].Weight != 1 {
		t.Fatalf("accepted entries: %+v", accepted)
	}
	derived, _, err := e.ShareList(chain.st, h, accepted, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if last := derived[len(derived)-1]; last.Address != sealer || last.Weight != 1 {
		t.Fatalf("the sealer's entry: %+v (want weight max(1, ceil(3/3)) = 1)", last)
	}
	withdrawals := shares.Withdrawals(derived)
	root := types.MinedSharesRoot(entries)
	wroot := types.DeriveSha(types.Withdrawals(withdrawals), trie.NewStackTrie(nil))
	header := childOf(parent)
	header.SharesRoot, header.WithdrawalsHash = &root, &wroot
	header.Coinbase = common.BytesToAddress(wroot[:20])
	block := types.NewBlock(header, nil, nil, nil, trie.NewStackTrie(nil)).WithWithdrawals(withdrawals).WithMinedShares(entries)
	if err := e.VerifyMinedShares(block, chain.st, chain); err != nil {
		t.Fatalf("a valid carried set refused: %v", err)
	}
	if _, ok := e.mined.get(root); !ok {
		t.Fatal("the stash is not set for Finalize")
	}
	// A duplicate entry.
	dup := append(append([]byte{}, entries...), list[0].Bytes()...)
	dr := types.MinedSharesRoot(dup)
	bad := types.CopyHeader(header)
	bad.SharesRoot = &dr
	if err := e.VerifyMinedShares(types.NewBlockWithHeader(bad).WithWithdrawals(withdrawals).WithMinedShares(dup), chain.st, chain); !errors.Is(err, errShareDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	// A forged mix.
	forged := append([]byte{}, entries...)
	forged[40] ^= 1
	fr := types.MinedSharesRoot(forged)
	bad = types.CopyHeader(header)
	bad.SharesRoot = &fr
	if err := e.VerifyMinedShares(types.NewBlockWithHeader(bad).WithWithdrawals(withdrawals).WithMinedShares(forged), chain.st, chain); !errors.Is(err, errShareWork) {
		t.Fatalf("forged mix: %v", err)
	}
	// A parent index beyond the recency.
	far := list[0]
	far.ParentIdx = ShareRecency
	fe := far.Bytes()
	fer := types.MinedSharesRoot(fe)
	bad = types.CopyHeader(header)
	bad.SharesRoot = &fer
	if err := e.VerifyMinedShares(types.NewBlockWithHeader(bad).WithWithdrawals(withdrawals).WithMinedShares(fe), chain.st, chain); !errors.Is(err, errShareAncestor) {
		t.Fatalf("far parent: %v", err)
	}
	// The list not the derivation (a reweighted entry).
	rew := shares.Withdrawals(derived)
	rew[0].Amount++
	rr := types.DeriveSha(types.Withdrawals(rew), trie.NewStackTrie(nil))
	bad = types.CopyHeader(header)
	bad.WithdrawalsHash = &rr
	if err := e.VerifyMinedShares(types.NewBlockWithHeader(bad).WithWithdrawals(rew).WithMinedShares(entries), chain.st, chain); !errors.Is(err, errMinedShareList) {
		t.Fatalf("reweighted list: %v", err)
	}
	// Shares without a list.
	bad = types.CopyHeader(header)
	bad.WithdrawalsHash = nil
	if err := e.VerifyMinedShares(types.NewBlockWithHeader(bad).WithMinedShares(entries), chain.st, chain); !errors.Is(err, errShareNoList) {
		t.Fatalf("no list: %v", err)
	}
}

func TestSharePoolSubmitAndCarry(t *testing.T) {
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
	if ok, _ := e.SubmitShare(chain, a, 9, nonce, mix, h); ok {
		t.Fatal("a replayed share accepted")
	}
	if ok, _ := e.SubmitShare(chain, a, 9, nonce+1, mix, h); ok {
		t.Fatal("a wrong share accepted")
	}
	if ok, _ := e.SubmitShare(chain, a, 9, nonce, mix, h-ShareRecency); ok {
		t.Fatal("a stale share accepted")
	}
	st := e.ShareStatsNow()
	if st.Accepted != 1 || st.Refused != 3 || st.Pooled != 1 {
		t.Fatalf("stats: %+v", st)
	}
	// The template on the head carries it with k = 0 and derives the list.
	entries, list, err := e.TemplateShares(chain, head, chain.st, common.Address{0xaa})
	if err != nil || types.MinedShareCount(entries) != 1 || len(list) != 2 || list[1].Address != (common.Address{0xaa}) {
		t.Fatalf("template: %v entries %d list %+v", err, types.MinedShareCount(entries), list)
	}
	got, _ := types.ParseMinedShares(entries)
	if got[0].ParentIdx != 0 || got[0].Address != a {
		t.Fatalf("carried share: %+v", got[0])
	}
	// Seven blocks on: the share is carried with k = 7, eight on it expires.
	parent := head
	for i := 0; i < 7; i++ {
		nh := childOf(parent)
		chain.push(nh)
		parent = nh
	}
	entries, _, err = e.TemplateShares(chain, parent, chain.st, common.Address{0xaa})
	if err != nil || types.MinedShareCount(entries) != 1 {
		t.Fatalf("at the recency's edge: %v %d", err, types.MinedShareCount(entries))
	}
	got, _ = types.ParseMinedShares(entries)
	if got[0].ParentIdx != 7 {
		t.Fatalf("k at the edge: %d", got[0].ParentIdx)
	}
	nh := childOf(parent)
	chain.push(nh)
	entries, list, err = e.TemplateShares(chain, nh, chain.st, common.Address{0xaa})
	if err != nil || len(entries) != 0 || len(list) != 0 {
		t.Fatalf("past the recency: %v entries %d list %d", err, len(entries), len(list))
	}
	if st := e.ShareStatsNow(); st.Expired != 1 || st.Pooled != 0 {
		t.Fatalf("expiry counted: %+v", st)
	}
}

// TestSharesTargetedAtTheChainDifficultyUnderTheClock: under the round
// clock amendment a bonded block's header carries D_seal, not D; the share
// target reads the chain's D in the submission, the template's work package
// and the import, so a share accepted at D for an unsealed height is valid
// in the bonded block that carries it (devnet7's carry gap of 11 October
// 2026: the import read the header's 54,000 against a share accepted at
// about 8,250 and the sealer's own validation refused every carrying block).
func TestSharesTargetedAtTheChainDifficultyUnderTheClock(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.SealWindowDiv, s.TPiMs, s.DMin, s.TMin = 4, 250, big.NewInt(54000), 10
	// ChainDifficulty walks the anchored chain (the node anchors at the
	// chain's creation; mineShareEngine's anchor is the algorithm's).
	e.sortitionAnchor(chain)
	head := chain.head
	h := head.Number.Uint64() + 1
	if !e.ClockOn(h) {
		t.Fatal("the clock is off")
	}
	a := common.Address{0x11}
	d := e.shareChainDifficulty(chain, head, nil)
	hs := ShareHeaderHash(head.Hash(), h, a, 9)
	nonce, mix := solveShare(t, e, hs, h, d)
	if ok, reason := e.SubmitShare(chain, a, 9, nonce, mix, h); !ok {
		t.Fatalf("a share at the chain's D refused: %s", reason)
	}
	entries, list, err := e.TemplateShares(chain, head, chain.st, common.Address{0xaa})
	if err != nil || types.MinedShareCount(entries) != 1 || len(list) != 2 {
		t.Fatalf("template: %v entries %d list %d", err, types.MinedShareCount(entries), len(list))
	}
	// The carrying block is bonded: its header carries D_seal, far above D.
	root := types.MinedSharesRoot(entries)
	wroot := types.DeriveSha(types.Withdrawals(list), trie.NewStackTrie(nil))
	header := childOf(head)
	header.Difficulty = new(big.Int).Mul(d, big.NewInt(64))
	header.SharesRoot, header.WithdrawalsHash = &root, &wroot
	header.Coinbase = common.BytesToAddress(wroot[:20])
	if got := e.ChainDifficulty(header); got.Cmp(d) != 0 {
		t.Fatalf("the chain's D under the bonded header %v, the submission's %v", got, d)
	}
	block := types.NewBlock(header, nil, nil, nil, trie.NewStackTrie(nil)).WithWithdrawals(list).WithMinedShares(entries)
	if err := e.VerifyMinedShares(block, chain.st, chain); err != nil {
		t.Fatalf("a share at the chain's D refused under a bonded header: %v", err)
	}
	// The work package's share target is D's, not D_seal's.
	wp := &pow.WorkPackage{}
	e.shareWorkFields(wp, header)
	if want := pow.Target(e.shareDifficulty(d)); wp.ShareTarget == nil || wp.ShareTarget.Cmp(want) != 0 {
		t.Fatalf("the template's share target %v, want D's %v", wp.ShareTarget, want)
	}
	// Once the bonded block is canonical, a submission at its height reads
	// the same D as its import did.
	chain.push(header)
	hs2 := ShareHeaderHash(head.Hash(), h, a, 10)
	nonce2, mix2 := solveShare(t, e, hs2, h, d)
	if ok, reason := e.SubmitShare(chain, a, 10, nonce2, mix2, h); !ok {
		t.Fatalf("a share at the chain's D refused at a sealed bonded height: %s", reason)
	}
}

// TestSharePoolPerAddressCap (SECURITY-GAPS.md; the project's order of 21:4x UK, 11
// October 2026): one address is refused past the cap of accepted shares at
// a height, another address at the same height has its own count, the next
// height starts afresh, carriage does not reset a cap within the recency,
// and the count goes when the height leaves the recency; cap zero is off.
func TestSharePoolPerAddressCap(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	e.SetShareCap(3)
	head := chain.head
	h := head.Number.Uint64() + 1
	d := e.shareChainDifficulty(chain, head, nil)
	a, b := common.Address{0x11}, common.Address{0x22}
	submit := func(addr common.Address, extra uint64, height uint64, parent *types.Header) (bool, string) {
		hs := ShareHeaderHash(parent.Hash(), height, addr, extra)
		nonce, mix := solveShare(t, e, hs, height, d)
		return e.SubmitShare(chain, addr, extra, nonce, mix, height)
	}
	for i := uint64(1); i <= 3; i++ {
		if ok, reason := submit(a, i, h, head); !ok {
			t.Fatalf("share %d of a under the cap refused: %s", i, reason)
		}
	}
	if ok, reason := submit(a, 4, h, head); ok || reason != errShareCap.Error() {
		t.Fatalf("the fourth share of a at the cap: ok %v, %q", ok, reason)
	}
	if ok, reason := submit(b, 1, h, head); !ok {
		t.Fatalf("b at the same height refused: %s", reason)
	}
	if st := e.ShareStatsNow(); st.Accepted != 4 || st.Refused != 1 {
		t.Fatalf("stats: %+v", st)
	}
	// Carriage of a's three does not reset a's cap at h.
	entries, _, err := e.TemplateShares(chain, head, chain.st, common.Address{0xaa})
	if err != nil || types.MinedShareCount(entries) != 4 {
		t.Fatalf("template: %v, %d", err, types.MinedShareCount(entries))
	}
	nh := childOf(head)
	chain.push(nh)
	e.MarkCarried(chain, types.NewBlockWithHeader(nh).WithMinedShares(entries))
	if ok, reason := submit(a, 5, h, head); ok || reason != errShareCap.Error() {
		t.Fatalf("after carriage a's cap at h reset: ok %v, %q", ok, reason)
	}
	// The next height starts afresh for a.
	if ok, reason := submit(a, 6, h+1, nh); !ok {
		t.Fatalf("a at the next height refused: %s", reason)
	}
	// Eight blocks on, h's counts are gone (the height left the recency).
	parent := nh
	for i := 0; i < ShareRecency; i++ {
		x := childOf(parent)
		chain.push(x)
		parent = x
	}
	e.TemplateShares(chain, parent, chain.st, common.Address{0xaa})
	e.sharePool.mu.Lock()
	_, still := e.sharePool.perAddr[h]
	e.sharePool.mu.Unlock()
	if still {
		t.Fatal("the per-address counts of a height past the recency were kept")
	}
	// Cap zero: off.
	e2, chain2 := mineShareEngine(t)
	defer e2.Close()
	e2.SetShareCap(0)
	head2 := chain2.head
	h2 := head2.Number.Uint64() + 1
	d2 := e2.shareChainDifficulty(chain2, head2, nil)
	for i := uint64(1); i <= 5; i++ {
		hs := ShareHeaderHash(head2.Hash(), h2, a, i)
		nonce, mix := solveShare(t, e2, hs, h2, d2)
		if ok, reason := e2.SubmitShare(chain2, a, i, nonce, mix, h2); !ok {
			t.Fatalf("cap off, share %d refused: %s", i, reason)
		}
	}
}

// TestSharePoolStaleParentBeforeWork (the project's rule of 23:5x UK, 11 October
// 2026, from the paced 4070 row): a share mined honestly on a SIBLING of
// the node's canonical block names its parent and is refused as "stale
// parent" before any work check, never as invalid work; the same share
// named without its parent reads the work error as before (an older
// client); the refusals are counted by reason.
func TestSharePoolStaleParentBeforeWork(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	head := chain.head
	gp := chain.byNumber[head.Number.Uint64()-1]
	// A sibling of the head: the same parent, a different time.
	sibling := childOf(gp)
	sibling.Time = head.Time + 1
	if sibling.Hash() == head.Hash() {
		t.Fatal("the sibling fixture is the head")
	}
	h := head.Number.Uint64() + 1
	a := common.Address{0x11}
	d := e.shareChainDifficulty(chain, head, nil)
	// Honest work on the sibling.
	hs := ShareHeaderHash(sibling.Hash(), h, a, 7)
	nonce, mix := solveShare(t, e, hs, h, d)
	ok, reason := e.SubmitShareOn(chain, a, 7, nonce, mix, h, sibling.Hash())
	if ok || reason != errShareStaleParent.Error() {
		t.Fatalf("a sibling's share: ok %v, %q (want stale parent)", ok, reason)
	}
	// The same share without its parent named reads the work error.
	if ok, reason := e.SubmitShare(chain, a, 7, nonce, mix, h); ok || reason == errShareStaleParent.Error() || reason == "" {
		t.Fatalf("without the parent: ok %v, %q (want the work error)", ok, reason)
	}
	// Honest work on the head with its parent named is accepted.
	hs = ShareHeaderHash(head.Hash(), h, a, 8)
	nonce, mix = solveShare(t, e, hs, h, d)
	if ok, reason := e.SubmitShareOn(chain, a, 8, nonce, mix, h, head.Hash()); !ok {
		t.Fatalf("the head's share with its parent named refused: %s", reason)
	}
	st := e.ShareStatsNow()
	if st.RefusedBy[errShareStaleParent.Error()] != 1 || st.RefusedBy[errShareWork.Error()] != 1 || st.Refused != 2 || st.Accepted != 1 {
		t.Fatalf("refusals by reason: %+v", st)
	}
}

// TestSharePoolAcceptedMeansPaid (G-23, the project's ruling of 22:5x UK, 11
// October 2026): a flood of ten times the ceiling over ten blocks, from
// many addresses, pays or carries every accepted share and refuses the rest
// by name ("pool full" at the window's capacity, "address at cap" at the
// per-address cap); with the sealer carrying every block nothing expires,
// and the one remaining expiry (no block of this sealer's within the
// recency) is named by height in the stats.
func TestSharePoolAcceptedMeansPaid(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.MaxShares = 8 // the test's ceiling; the capacity is 8 x the recency 8 = 64
	e.SetShareCap(2)
	capacity := s.MaxShares * ShareRecency
	accepted, poolFull, atCap, admission := 0, 0, 0, 0
	carriedTotal := 0
	parent := chain.head
	// Ten blocks; before each, a flood of ten times the ceiling at the next
	// height from ten addresses (eight shares each a height: two under the
	// cap, six "address at cap"), then the sealer carries the ceiling; the
	// pool's twenty a height against eight carried fills the window's
	// capacity by the sixth block and "pool full" follows.
	for blk := 0; blk < 10; blk++ {
		h := parent.Number.Uint64() + 1
		d := e.shareChainDifficulty(chain, parent, nil)
		for i := uint64(0); i < 10*s.MaxShares; i++ {
			addr := common.Address{byte(i % 10), 0x5a}
			hs := ShareHeaderHash(parent.Hash(), h, addr, i)
			nonce, mix := solveShare(t, e, hs, h, d)
			ok, reason := e.SubmitShare(chain, addr, i, nonce, mix, h)
			switch {
			case ok:
				accepted++
			case reason == errSharePoolFull.Error():
				poolFull++
			case reason == errShareCap.Error():
				atCap++
			case reason == errShareAdmission.Error(), reason == errShareLaneAtCarriage.Error():
				// PA-53's admission bound (an address holding a whole
				// list's worth of unpaid shares, MaxShares, 8 here) and
				// the lane's carriage bound (the aggregate against
				// sharesPerBlock x recency): refused by name before the
				// pool fills.
				admission++
			default:
				t.Fatalf("block %d share %d refused for another reason: %s", blk, i, reason)
			}
		}
		if st := e.ShareStatsNow(); st.Pooled > capacity {
			t.Fatalf("the pool over its capacity: %d > %d", st.Pooled, capacity)
		}
		entries, list, err := e.TemplateShares(chain, parent, chain.st, common.Address{0xaa})
		if err != nil {
			t.Fatal(err)
		}
		n := types.MinedShareCount(entries)
		if n > int(s.MaxShares) {
			t.Fatalf("carried over the ceiling: %d", n)
		}
		if n > 0 && len(list) == 0 {
			t.Fatal("shares carried with no payout list")
		}
		nh := childOf(parent)
		chain.push(nh)
		e.MarkCarried(chain, types.NewBlockWithHeader(nh).WithMinedShares(entries))
		carriedTotal += n
		parent = nh
	}
	st := e.ShareStatsNow()
	// The flood is refused by name: at the per-height cap, and beyond it
	// by the admission bound (each address's unpaid shares reach a whole
	// list's worth before the pool's capacity does) or by the pool's
	// capacity, whichever a flood meets first.
	if atCap == 0 || poolFull+admission == 0 {
		t.Fatalf("the flood was not refused by name: pool full %d, admission %d, at cap %d", poolFull, admission, atCap)
	}
	if int(st.Accepted) != accepted || int(st.Refused) != poolFull+atCap+admission {
		t.Fatalf("stats against the count: %+v (accepted %d, refused %d)", st, accepted, poolFull+atCap+admission)
	}
	// Under a flood beyond the chain's capacity the selection rule (PA-53's
	// (4)) pays the addresses of carried weight first, so what expires is
	// the shares of addresses the chain never paid; no address with a
	// carriage behind it expires a share (the ruling's bar: "every carried
	// share paid once", an honest address at or above the chain's minimum
	// weight paid within the recency).
	for i := 0; i < 10; i++ {
		acc := e.ShareAccountOf(common.Address{byte(i), 0x5a})
		if acc.Carried > 0 && acc.ExpiredWindow > 0 {
			t.Fatalf("address %d, paid %d, expired %d by window: a paid address starved", i, acc.Carried, acc.ExpiredWindow)
		}
	}
	if int(st.Carried)+int(st.Pooled)+int(st.Expired) != accepted || carriedTotal != int(st.Carried) {
		t.Fatalf("every accepted share is carried, pooled or expired by name: carried %d pooled %d expired %d accepted %d", st.Carried, st.Pooled, st.Expired, accepted)
	}
	if st.Capacity != capacity {
		t.Fatalf("capacity %d, want %d", st.Capacity, capacity)
	}
	// The one remaining expiry: the sealer seals nothing for a recency
	// window, and what was pooled leaves the window named by height.
	pooledBefore := st.Pooled
	expiredBefore, namedBefore := st.Expired, uint64(0)
	for _, c := range st.ExpiredNoBlock {
		namedBefore += c
	}
	for i := 0; i < ShareRecency+1; i++ {
		nh := childOf(parent)
		chain.push(nh)
		parent = nh
	}
	e.TemplateShares(chain, parent, chain.st, common.Address{0xaa})
	st = e.ShareStatsNow()
	var named uint64
	for _, c := range st.ExpiredNoBlock {
		named += c
	}
	if st.Expired-expiredBefore != pooledBefore || named-namedBefore != pooledBefore || st.Pooled != 0 {
		t.Fatalf("the no-block expiry: expired %d named %d pooled %d (want %d more, %d more, 0)", st.Expired-expiredBefore, named-namedBefore, st.Pooled, pooledBefore, pooledBefore)
	}
}

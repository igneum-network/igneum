package igneum

import (
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/ishr"
	"github.com/ethereum/go-ethereum/trie"
)

// TestFirstFinderClaimsTheTemplate (devnet7's stall at 350 and security's
// 1145, 11 October 2026: two threads found seals on one template within a
// second, their proofs' files collided and the block carried a receipt over
// the other nonce): the first finder claims a template, every later finder
// on it is refused, and the set is bounded.
func TestFirstFinderClaimsTheTemplate(t *testing.T) {
	var c sealClaims
	a, b := common.Hash{1}, common.Hash{2}
	if !c.claim(a) {
		t.Fatal("the first finder was refused")
	}
	if c.claim(a) {
		t.Fatal("a second finder on the same template was admitted")
	}
	if !c.claim(b) {
		t.Fatal("another template's first finder was refused")
	}
	for i := 0; i < 300; i++ {
		var h common.Hash
		h[0], h[1] = byte(i), byte(i>>8)
		h[31] = 0xee
		c.claim(h)
	}
	if len(c.set) > 256 || len(c.order) > 256 {
		t.Fatalf("the claim set is not bounded: %d", len(c.set))
	}
	if !c.claim(a) {
		t.Fatal("an evicted template is not claimable again")
	}
}

// TestTemplateDropsSharesAnotherSealerCarried (devnet7's stall at 350: the
// gossip put one share in every pool, the other sealer's block carried it,
// this pool kept it, every template re-carried it and the import rule
// refused the block): a pooled share an imported block within the window
// carried is dropped at the next template as carried elsewhere, never
// carried again and never counted as expired.
func TestTemplateDropsSharesAnotherSealerCarried(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x11}
	d := e.shareChainDifficulty(chain, head, nil)
	hs := ShareHeaderHash(head.Hash(), h, a, 1)
	nonce, mix := solveShare(t, e, hs, h, d)
	if ok, reason := e.SubmitShare(chain, a, 1, nonce, mix, h); !ok {
		t.Fatalf("refused: %s", reason)
	}
	// Another sealer's block at h carries the share; this node imports it
	// (VerifyMinedShares caches the carried keys by the block's hash) and
	// it becomes the head.
	sh := types.MinedShare{Address: a, ExtraNonce: 1, Nonce: nonce, MixDigest: mix, ParentIdx: 0}
	entries := sh.Bytes()
	accepted := acceptedEntries([]types.MinedShare{sh})
	derived, _, err := e.ShareList(chain.st, h, accepted, common.Address{0xbb})
	if err != nil {
		t.Fatal(err)
	}
	withdrawals := shares.Withdrawals(derived)
	root := types.MinedSharesRoot(entries)
	wroot := types.DeriveSha(types.Withdrawals(withdrawals), trie.NewStackTrie(nil))
	hdr := childOf(head)
	hdr.SharesRoot, hdr.WithdrawalsHash = &root, &wroot
	hdr.Coinbase = common.BytesToAddress(wroot[:20])
	block := types.NewBlock(hdr, nil, nil, nil, trie.NewStackTrie(nil)).WithWithdrawals(withdrawals).WithMinedShares(entries)
	if err := e.VerifyMinedShares(block, chain.st, chain); err != nil {
		t.Fatalf("the other sealer's block refused: %v", err)
	}
	chain.push(block.Header())
	// The next template on it carries nothing: the share is dropped as
	// carried elsewhere, the pool empty, nothing expired.
	ents, list, err := e.TemplateShares(chain, block.Header(), chain.st, common.Address{0xaa})
	if err != nil {
		t.Fatal(err)
	}
	if n := types.MinedShareCount(ents); n != 0 {
		t.Fatalf("the template re-carried %d shares another sealer's block carried", n)
	}
	_ = list
	st := e.ShareStatsNow()
	if st.CarriedElsewhere != 1 || st.Pooled != 0 || st.Expired != 0 {
		t.Fatalf("after the other sealer's carriage: %+v", st)
	}
	// The gossip's receipt of the same share: refused as carried elsewhere,
	// not the peer's fault, not pooled.
	e.SetGossipChain(chain)
	ok, reason, bad := e.ReceiveShare(ishr.Share{Address: a, ExtraNonce: 1, Nonce: nonce, MixDigest: mix, ParentHash: head.Hash(), Height: h}, "peer")
	if ok || bad || reason != errShareCarriedElsewhere.Error() {
		t.Fatalf("a carried share from a peer: ok %v bad %v %q", ok, bad, reason)
	}
	if st := e.ShareStatsNow(); st.Pooled != 0 || st.RefusedBy[errShareCarriedElsewhere.Error()] != 1 {
		t.Fatalf("after the peer's copy: %+v", st)
	}
}

// TestReplayDropsSharesTheChainCarried (F-42's replay term): a journaled
// share a canonical block within the window carried, by any sealer, is
// dropped at the restart's replay and never re-admitted to the pool.
func TestReplayDropsSharesTheChainCarried(t *testing.T) {
	e1, chain := mineShareEngine(t)
	path := filepath.Join(t.TempDir(), "igneum-shares.journal")
	if err := e1.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x11}
	d := e1.shareChainDifficulty(chain, head, nil)
	hs := ShareHeaderHash(head.Hash(), h, a, 3)
	nonce, mix := solveShare(t, e1, hs, h, d)
	if ok, reason := e1.SubmitShare(chain, a, 3, nonce, mix, h); !ok {
		t.Fatalf("refused: %s", reason)
	}
	// Another sealer's block carries it; this node imports it (no
	// MarkCarried of its own: the journal keeps the acceptance alone).
	sh := types.MinedShare{Address: a, ExtraNonce: 3, Nonce: nonce, MixDigest: mix, ParentIdx: 0}
	entries := sh.Bytes()
	derived, _, err := e1.ShareList(chain.st, h, acceptedEntries([]types.MinedShare{sh}), common.Address{0xbb})
	if err != nil {
		t.Fatal(err)
	}
	withdrawals := shares.Withdrawals(derived)
	root := types.MinedSharesRoot(entries)
	wroot := types.DeriveSha(types.Withdrawals(withdrawals), trie.NewStackTrie(nil))
	hdr := childOf(head)
	hdr.SharesRoot, hdr.WithdrawalsHash = &root, &wroot
	hdr.Coinbase = common.BytesToAddress(wroot[:20])
	block := types.NewBlock(hdr, nil, nil, nil, trie.NewStackTrie(nil)).WithWithdrawals(withdrawals).WithMinedShares(entries)
	e1.Close()
	// The restart: the chain holds the carrying block as the head.
	e2 := genesisEngine(t)
	e2.config.Parts.Sortition.SharesPerBlock = 4
	e2.config.Parts.Sortition.MaxShares = 8
	e2.anchor(chain)
	defer e2.Close()
	if err := e2.VerifyMinedShares(block, chain.st, chain); err != nil {
		t.Fatalf("the carrying block refused on the restarted node: %v", err)
	}
	chain.push(block.Header())
	if err := e2.SetShareJournal(path, chain); err != nil {
		t.Fatal(err)
	}
	st := e2.ShareStatsNow()
	if st.Replayed != 0 || st.Dropped != 1 || st.Pooled != 0 {
		t.Fatalf("the replay re-admitted a share the chain carried: %+v", st)
	}
}

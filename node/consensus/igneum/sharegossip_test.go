package igneum

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/ishr"
)

// TestShareGossipEngineSide (PA-46): a share accepted by direct submission
// is remembered in the index, announced through the announcer with its
// parent and height, and served to a peer's fetch; a share received from a
// peer is verified as a submission and pooled; ReceiveShare flags the peer
// as bad only when the WORK fails, not on a stale parent or a duplicate.
func TestShareGossipEngineSide(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	e.SetGossipChain(chain)
	var announced []ishr.Share
	e.SetShareAnnouncer(func(s ishr.Share) { announced = append(announced, s) })
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x11}
	d := e.shareChainDifficulty(chain, head, nil)
	// A direct submission with the parent named.
	hs := ShareHeaderHash(head.Hash(), h, a, 1)
	nonce, mix := solveShare(t, e, hs, h, d)
	if ok, reason := e.SubmitShareOn(chain, a, 1, nonce, mix, h, head.Hash()); !ok {
		t.Fatalf("submission refused: %s", reason)
	}
	if len(announced) != 1 || announced[0].ParentHash != head.Hash() || announced[0].Height != h || announced[0].Nonce != nonce {
		t.Fatalf("announced: %+v", announced)
	}
	hash := announced[0].Hash()
	if !e.HasShare(hash) {
		t.Fatal("the submitted share is not in the index")
	}
	if s, ok := e.ShareByHash(hash); !ok || s.Address != a {
		t.Fatalf("fetch from the index: %v %+v", ok, s)
	}
	if e.GossipIndexSize() != 1 {
		t.Fatalf("index size %d", e.GossipIndexSize())
	}
	// A peer's share, valid: pooled, not bad.
	hs2 := ShareHeaderHash(head.Hash(), h, a, 2)
	nonce2, mix2 := solveShare(t, e, hs2, h, d)
	peerShare := ishr.Share{Address: a, ExtraNonce: 2, Nonce: nonce2, MixDigest: mix2, ParentHash: head.Hash(), Height: h}
	if ok, reason, bad := e.ReceiveShare(peerShare, "peer-1"); !ok || bad {
		t.Fatalf("a peer's valid share: ok %v bad %v %s", ok, bad, reason)
	}
	if st := e.ShareStatsNow(); st.Accepted != 2 || st.Pooled != 2 {
		t.Fatalf("after the peer's share: %+v", st)
	}
	// The same share again: a duplicate, refused, NOT bad.
	if ok, _, bad := e.ReceiveShare(peerShare, "peer-1"); ok || bad {
		t.Fatalf("a duplicate from a peer: ok %v bad %v", ok, bad)
	}
	// A stale parent (a sibling): refused, NOT bad.
	sib := peerShare
	sib.ExtraNonce = 3
	sib.ParentHash = common.Hash{0xab}
	if ok, reason, bad := e.ReceiveShare(sib, "peer-1"); ok || bad || reason != errShareStaleParent.Error() {
		t.Fatalf("a sibling's share from a peer: ok %v bad %v %q", ok, bad, reason)
	}
	// Bad work: refused AND bad (the peer's fault).
	wrong := peerShare
	wrong.ExtraNonce = 4
	wrong.Nonce = nonce2 + 1
	if ok, _, bad := e.ReceiveShare(wrong, "peer-1"); ok || !bad {
		t.Fatalf("a share whose work fails: ok %v bad %v (want bad)", ok, bad)
	}
	// The window is the recency.
	if e.ShareWindow() != e.ShareRecencyBlocks() {
		t.Fatalf("window %d, recency %d", e.ShareWindow(), e.ShareRecencyBlocks())
	}
}

// TestGetWorkShareFieldsFollowTheHead (ecosystem's run 8, 11 October 2026:
// a sealer waiting on its round floor commits no new template, so its
// getWork named a height the chain had passed and a worker's shares at it
// hit the cap, 62 percent refused): when the sealer's template is at or
// behind the head, igneum_getWork's share fields are the head's successor's
// (the share height, the parent, the target at the chain's D), and the
// BLOCK JOB is withheld (F-420, 17 October 2026: the pool's solution found
// against a template two blocks behind the head was admitted and dropped
// at the parent check; HeaderHash and Target zero, StaleTemplate naming
// why); a template ahead of the head is served as it is.
func TestGetWorkShareFieldsFollowTheHead(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	e.SetThreads(-1) // remote only: the template stays pending
	e.SetGossipChain(chain)
	head := chain.head
	h := head.Number.Uint64() + 1
	tmpl := childOf(head)
	block := types.NewBlockWithHeader(tmpl)
	results := make(chan *types.Block, 1)
	if err := e.Seal(chain, block, results, nil); err != nil {
		t.Fatal(err)
	}
	api := &IgneumAPI{engine: e, chain: chain}
	w, err := api.GetWork(nil)
	if err != nil {
		t.Fatal(err)
	}
	if w.Height != h || w.ShareHeight != h || w.ShareParent != head.Hash() {
		t.Fatalf("a template ahead of the head: height %d share height %d parent %s (want %d, %d, the head)", w.Height, w.ShareHeight, w.ShareParent.Hex()[:10], h, h)
	}
	// Another sealer's block at h becomes the head: the template is behind.
	other := childOf(head)
	other.Time = head.Time + 7
	chain.push(other)
	w2, err := api.GetWork(nil)
	if err != nil {
		t.Fatal(err)
	}
	if w2.HeaderHash != (common.Hash{}) || w2.Target != nil || w2.StaleTemplate == "" {
		t.Fatalf("the block job of a template behind the head was served: hash %s target %v stale %q", w2.HeaderHash.Hex()[:10], w2.Target, w2.StaleTemplate)
	}
	if w.HeaderHash == (common.Hash{}) || w.StaleTemplate != "" || w.ParentHash != head.Hash() {
		t.Fatalf("the template ahead of the head lost its block job or its parent: %+v", w)
	}
	if w2.ShareHeight != h+1 || w2.ShareParent != other.Hash() {
		t.Fatalf("the share fields did not follow the head: share height %d parent %s (want %d, the new head)", w2.ShareHeight, w2.ShareParent.Hex()[:10], h+1)
	}
	d := e.shareChainDifficulty(chain, other, nil)
	if want := pow.Target(e.shareDifficulty(d)); w2.ShareTarget == nil || w2.ShareTarget.Cmp(want) != 0 {
		t.Fatalf("the share target %v, want the head's successor's %v", w2.ShareTarget, want)
	}
}

// TestShareWorkOnANonSealingNode (the share gossip's launch form; product
// A's finding of 16:4x UK, 12 October 2026): a node that seals nothing
// serves the share work for its head's successor (ShareOnly, a zero header
// hash, no seal target, the share target at the chain's D, the parent the
// head, the twin document), and a share mined on it is accepted there.
func TestShareWorkOnANonSealingNode(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	if _, err := e.ShareWork(); err == nil {
		t.Fatal("share work served without a chain")
	}
	e.SetGossipChain(chain)
	head := chain.head
	h := head.Number.Uint64() + 1
	wp, err := e.ShareWork()
	if err != nil {
		t.Fatal(err)
	}
	if !wp.ShareOnly || wp.HeaderHash != (common.Hash{}) || wp.Target != nil {
		t.Fatalf("not share-only: %+v", wp)
	}
	if wp.Height != h || wp.ShareHeight != h || wp.ShareParent != head.Hash() || wp.SharesPerBlock != e.config.Parts.Sortition.SharesPerBlock {
		t.Fatalf("the share fields: height %d/%d parent %s per block %d", wp.Height, wp.ShareHeight, wp.ShareParent.Hex()[:10], wp.SharesPerBlock)
	}
	d := e.shareChainDifficulty(chain, head, nil)
	if want := pow.Target(e.shareDifficulty(d)); wp.ShareTarget == nil || wp.ShareTarget.Cmp(want) != 0 {
		t.Fatalf("the share target %v, want the chain's %v", wp.ShareTarget, want)
	}
	// A share mined on the served work is accepted by this node.
	a := common.Address{0x11}
	hs := ShareHeaderHash(wp.ShareParent, wp.ShareHeight, a, 1)
	nonce, mix := solveShare(t, e, hs, wp.ShareHeight, d)
	if ok, reason := e.SubmitShareOn(chain, a, 1, nonce, mix, wp.ShareHeight, wp.ShareParent); !ok {
		t.Fatalf("a share on the served work refused: %s", reason)
	}
}

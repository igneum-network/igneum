package igneum

import (
	"math/big"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// The share answer's cuts of 16 October 2026 (0.10.44, the one named
// exception to the one-fix rule): the period's own anchor from the first
// ask at a boundary, and F-224's second verification before a refusal.

// TestShareWorkNamesThePeriodsOwnAnchorAtTheBoundary (the pool team's
// journal read): at the first height of a period the share work names
// THAT period's anchor (the block at the period's start minus the reveal,
// two blocks before the boundary) from the first ask, never the previous
// period's; below the first anchor height the genesis hash as before.
func TestShareWorkNamesThePeriodsOwnAnchorAtTheBoundary(t *testing.T) {
	stub := newProgrammedStub(t, 10)
	parts := testParts()
	parts.Program = &ProgramParams{RevealBlocks: 2}
	parts.Sortition = &SortitionParams{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 6, MaxRound: 16, SharesPerBlock: 4, MaxShares: 8, MaxPayouts: 256}
	e, err := New(Config{Algorithm: stub, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	c := newForkChain()
	g := genesisHeader()
	c.setCanonical(g)
	canon := chainOf(c, g, 9, 'a') // heights 1 to 9
	for _, h := range canon[:8] {
		c.setCanonical(h) // the head at 8: the share height 9 is period 0's
	}
	e.setChain(c)
	e.SetGossipChain(c)
	wp, err := e.ShareWork()
	if err != nil {
		t.Fatal(err)
	}
	if wp.ShareHeight != 9 || wp.ProgramAnchor == nil || *wp.ProgramAnchor != g.Hash() || wp.AnchorHeight != 0 {
		t.Fatalf("below the first anchor height: share height %d anchor %v height %d (want 9, the genesis, 0)", wp.ShareHeight, wp.ProgramAnchor, wp.AnchorHeight)
	}
	// The head at 9, the last height of period 0: the share height 10 is
	// the FIRST height of period 1, whose anchor is the block at 8.
	c.setCanonical(canon[8])
	wp, err = e.ShareWork()
	if err != nil {
		t.Fatal(err)
	}
	if wp.ShareHeight != 10 || wp.ProgramAnchor == nil || *wp.ProgramAnchor != canon[7].Hash() || wp.AnchorHeight != 8 {
		t.Fatalf("at the boundary: share height %d anchor %v height %d (want 10, the block at 8 %x, 8)", wp.ShareHeight, wp.ProgramAnchor, wp.AnchorHeight, canon[7].Hash().Bytes()[:4])
	}
	if wp.ShareParent != canon[8].Hash() {
		t.Fatalf("the share parent %x, want the head %x", wp.ShareParent[:4], canon[8].Hash().Bytes()[:4])
	}
}

// flakyAlgo is the null algorithm with a wrong NON-ZERO mix for the next
// n hashes (F-224's symptom: the verifier's own answer wrong on valid
// work for a moment, the same inputs right before and after).
type flakyAlgo struct {
	pow.Algorithm
	fails atomic.Int32
}

func (f *flakyAlgo) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	m, r := f.Algorithm.Compute(headerHash, height, nonce)
	if f.fails.Load() > 0 {
		f.fails.Add(-1)
		m[0] ^= 0xff
	}
	return m, r
}

func (f *flakyAlgo) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	return pow.Verify(f, headerHash, height, nonce, mixDigest, difficulty)
}

// TestShareRefusedOnceIsVerifiedAgain (F-224): a share whose work fails
// the first verification is verified once more before the answer; a
// transient becomes an accepted share counted as a flap, a refusal that
// holds on both passes is refused for its work as before.
func TestShareRefusedOnceIsVerifiedAgain(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	flaky := &flakyAlgo{Algorithm: e.algo}
	e.algo = flaky
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x44}
	d := e.shareChainDifficulty(chain, head, nil)
	hs := ShareHeaderHash(head.Hash(), h, a, 1)
	nonce, mix := solveShare(t, e, hs, h, d)
	flaky.fails.Store(1)
	ok, reason := e.SubmitShare(chain, a, 1, nonce, mix, h)
	if !ok {
		t.Fatalf("a share refused once must verify on the second pass: %s", reason)
	}
	if st := e.ShareStatsNow(); st.Flapped != 1 || st.Refused != 0 || st.Accepted != 1 {
		t.Fatalf("after the flap: flapped %d refused %d accepted %d (want 1, 0, 1)", st.Flapped, st.Refused, st.Accepted)
	}
	// Both passes fail: the refusal holds, by name.
	hs2 := ShareHeaderHash(head.Hash(), h, a, 2)
	nonce2, mix2 := solveShare(t, e, hs2, h, d)
	flaky.fails.Store(2)
	ok, reason = e.SubmitShare(chain, a, 2, nonce2, mix2, h)
	if ok || !strings.Contains(reason, errShareWork.Error()) {
		t.Fatalf("a refusal that holds: ok %v reason %q", ok, reason)
	}
	if st := e.ShareStatsNow(); st.Flapped != 1 || st.Refused != 1 || st.RefusedBy[errShareWork.Error()] != 1 {
		t.Fatalf("after the held refusal: flapped %d refused %d by %v", st.Flapped, st.Refused, st.RefusedBy)
	}
	// A share that is simply wrong (the mix altered) is refused on both
	// passes with no flap.
	bad := mix
	bad[1] ^= 0x01
	ok, _ = e.SubmitShare(chain, a, 3, nonce, bad, h)
	if ok {
		t.Fatal("a wrong mix accepted")
	}
	if st := e.ShareStatsNow(); st.Flapped != 1 || st.Refused != 2 {
		t.Fatalf("after the wrong mix: flapped %d refused %d (want 1, 2)", st.Flapped, st.Refused)
	}
}

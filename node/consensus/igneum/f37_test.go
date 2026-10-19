package igneum

import (
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
)

// stateChain is a fakeChain that also gives the parent's state, as
// core.BlockChain does: the shape under which verifyHeader runs the
// authority rule before the walk (F-37).
type stateChain struct {
	*fakeChain
	st *state.StateDB
}

func (c *stateChain) StateAt(root common.Hash) (*state.StateDB, error) { return c.st, nil }

// TestForgedSignatureRefusedBeforeTheWalk is F-37's regression (security's
// C10 order, 11 October 2026): under the sortition rule a block's signature
// and eligibility are checked at the parent's state BEFORE its proof of
// work, so a forged-signature flood costs an Ed25519 verify per block and
// never the walk. The walk here is a fake sealer with a 20 ms delay: a
// forged signature is refused at its own code in a fraction of that, 1,000
// times over, and the honest block is the one that pays the delay.
func TestForgedSignatureRefusedBeforeTheWalk(t *testing.T) {
	e := sortitionEngine(t)
	e.config.FakePoW = true
	e.config.FakeDelay = 20 * time.Millisecond
	seed := make([]byte, 32)
	seed[0] = 7
	if err := e.SetSigner(seed); err != nil {
		t.Fatal(err)
	}
	pub := *e.SignerKey()

	// A chain past the rule's height (10): genesis and eleven unsealed
	// children at the target interval, the state at the parent any state
	// (the unbonded round reads no bond).
	chain := &stateChain{fakeChain: newFakeChain(), st: registryStateV2(t, testCap)}
	parent := genesisHeader()
	chain.add(parent)
	for i := 0; i < 11; i++ {
		h := childOf(parent)
		chain.add(h)
		parent = h
	}
	s := e.config.Parts.Sortition
	honest := childOf(parent)
	honest.BondKey = &pub
	honest.Round = s.UnbondedRound
	honest.Time = parent.Time + s.UnbondedRound*s.Delta + 1
	if err := e.signHeader(honest); err != nil {
		t.Fatal(err)
	}
	// The honest block reaches the walk: the fake sealer's delay is paid.
	t0 := time.Now()
	if err := e.VerifyHeader(chain, honest, true); err != nil {
		t.Fatalf("honest block refused: %v", err)
	}
	if d := time.Since(t0); d < e.config.FakeDelay {
		t.Fatalf("honest block did not reach the walk: %v", d)
	}
	// 1,000 forged signatures, each refused at the signature's own code and
	// without the walk's delay.
	times := make([]time.Duration, 0, 1000)
	for i := 0; i < 1000; i++ {
		forged := types.CopyHeader(honest)
		forged.Sig = append([]byte{}, honest.Sig...)
		forged.Sig[i%64] ^= byte(1 + i/64)
		t1 := time.Now()
		err := e.VerifyHeader(chain, forged, true)
		d := time.Since(t1)
		if !errors.Is(err, errSortitionBadSig) {
			t.Fatalf("forged signature %d: got %v, want %v", i, err, errSortitionBadSig)
		}
		if d >= e.config.FakeDelay {
			t.Fatalf("forged signature %d reached the walk: %v", i, d)
		}
		times = append(times, d)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	p50, p99 := times[len(times)/2], times[len(times)*99/100]
	t.Logf("forged-signature refusals: n %d, p50 %v, p99 %v (the finding's line 0.1 ms; the walk's 5.0 ms)", len(times), p50, p99)
	if p50 > time.Millisecond {
		t.Fatalf("forged signature refused too slowly: p50 %v", p50)
	}
	// A chain without state access orders nothing and refuses nothing here:
	// the walk runs and the state processor refuses the block later.
	plain := newFakeChain()
	for _, h := range chain.fakeChain.headers {
		plain.add(h)
	}
	forged := types.CopyHeader(honest)
	forged.Sig = append([]byte{}, honest.Sig...)
	forged.Sig[0] ^= 1
	if err := e.VerifyHeader(plain, forged, true); err != nil {
		t.Fatalf("without state access the header stage refused: %v", err)
	}
	if err := e.VerifyAuthority(forged, chain.st); !errors.Is(err, errSortitionBadSig) {
		t.Fatalf("the state processor's rule: got %v, want %v", err, errSortitionBadSig)
	}
}

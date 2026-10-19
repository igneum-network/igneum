//go:build devhold

package igneum

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/consensus"
)

// TestDevHoldRefusesAndReleases (the devhold build): a held coordinate's
// header is refused as a future block and accepted after the release;
// the other coordinates pass throughout.
func TestDevHoldRefusesAndReleases(t *testing.T) {
	e := vectorEngine(t, "")
	c := newForkChain()
	g := genesisHeader()
	g.Time = 1_000_000
	c.setCanonical(g)
	e.anchor(c)
	h1 := taggedChild(t, e, c, g, 1, 20)
	h2 := taggedChild(t, e, c, g, 2, 20)
	e.DevHoldCoordinate(1, true)
	if !e.devHoldRefuses(h1) || e.devHoldRefuses(h2) {
		t.Fatal("the hold does not key on the coordinate")
	}
	if err := e.verifyHeader(c, h1, g, nil, false, int64(g.Time)+100); !errors.Is(err, consensus.ErrFutureBlock) {
		t.Fatalf("a held block is not refused as future: %v", err)
	}
	e.DevHoldCoordinate(1, false)
	if e.devHoldRefuses(h1) {
		t.Fatal("the release did not release")
	}
}

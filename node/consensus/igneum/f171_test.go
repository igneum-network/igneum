package igneum

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
)

// TestTemplateRoundOver (F-171): a template's round window ends at the
// parent's time plus the start of the next round; at or past it the miner
// re-issues the template so the block's stamp advances with the search and
// an honest slow block is never read as past-stamped by policy part 4.
func TestTemplateRoundOver(t *testing.T) {
	parts := TestParts()
	parts.Sortition = &SortitionParams{Height: 1, Delta: 20, TailDelta: 10}
	e := NewFullFaker(parts)
	defer e.Close()
	c := newForkChain()
	g := genesisHeader()
	now := uint64(time.Now().Unix())
	g.Time = now - 1000
	c.setCanonical(g)
	parent := &types.Header{ParentHash: g.Hash(), Number: big.NewInt(1), Time: now - 25, Difficulty: big.NewInt(1)}
	c.setCanonical(parent)
	child := func(r uint64) *types.Header {
		return &types.Header{ParentHash: parent.Hash(), Number: big.NewInt(2), Time: now - 25, Round: r}
	}
	// Round 0's window ends at parent + 20 (passed at parent + 25); round
	// 1's at parent + 30 under the tail (open); round 2's at parent + 40.
	if !e.TemplateRoundOver(c, child(0)) {
		t.Fatal("round 0's window is over at parent + 25 and reads open")
	}
	if e.TemplateRoundOver(c, child(1)) || e.TemplateRoundOver(c, child(2)) {
		t.Fatal("a round whose window is open reads over")
	}
	// The window's end exactly is over (the deadline's own bound).
	parent2 := &types.Header{ParentHash: g.Hash(), Number: big.NewInt(1), Time: now - 20, Difficulty: big.NewInt(2)}
	c.setCanonical(parent2)
	if !e.TemplateRoundOver(c, &types.Header{ParentHash: parent2.Hash(), Number: big.NewInt(2), Time: now - 20, Round: 0}) {
		t.Fatal("the window's end reads open")
	}
	// A template re-issued at parent + 25 (its stamp past round 0's end)
	// is over at the next boundary, parent + 30, not at every tick.
	reissued := &types.Header{ParentHash: parent.Hash(), Number: big.NewInt(2), Time: now - 25 + 25, Round: 0}
	if e.TemplateRoundOver(c, reissued) {
		t.Fatal("a re-issued template reads over before the next round boundary")
	}
	reissued.Time = now - 25 + 22
	// Stamped at parent + 22: its boundary is parent + 30 too (open).
	if e.TemplateRoundOver(c, reissued) {
		t.Fatal("a re-issued template inside the next round reads over")
	}
	parent3 := &types.Header{ParentHash: g.Hash(), Number: big.NewInt(1), Time: now - 31, Difficulty: big.NewInt(3)}
	c.setCanonical(parent3)
	// Issued at parent + 22 (round 0): the boundary after it is parent +
	// 30, passed at parent + 31: over once; re-issued at parent + 31 its
	// next boundary is parent + 40: open.
	if !e.TemplateRoundOver(c, &types.Header{ParentHash: parent3.Hash(), Number: big.NewInt(2), Time: now - 31 + 22, Round: 0}) {
		t.Fatal("a template past the boundary after its stamp reads open")
	}
	if e.TemplateRoundOver(c, &types.Header{ParentHash: parent3.Hash(), Number: big.NewInt(2), Time: now - 31 + 31, Round: 0}) {
		t.Fatal("the re-issued template reads over at once")
	}
	// Without the rule, at the genesis, or without the parent: never.
	if e.TemplateRoundOver(c, &types.Header{ParentHash: g.Hash(), Number: big.NewInt(0)}) {
		t.Fatal("the genesis reads over")
	}
	if e.TemplateRoundOver(c, &types.Header{ParentHash: types.EmptyRootHash, Number: big.NewInt(2), Round: 0}) {
		t.Fatal("an unknown parent reads over")
	}
	off := NewFullFaker(TestParts())
	defer off.Close()
	if off.TemplateRoundOver(c, child(0)) {
		t.Fatal("a chain without the sortition rule reads over")
	}
}

package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// The chain difficulty's floor re-pinned by a height gate (the security review's
// F-86): below DiffMinV1 the genesis set's minimum holds; from the landing check the
// controller clamps at DiffMinV1Min, so the first block at the landing check reads
// the new floor at once whatever the parent's interval, and a fast parent
// above the floor still raises the difficulty the controller's way.
func TestDiffMinGate(t *testing.T) {
	e := newTestEngine(t)
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)
	parent := childOf(g)
	parent.Number = big.NewInt(1198)
	parent.Time = g.Time + 20*1198
	gp := childOf(g)
	gp.Number = big.NewInt(1197)
	gp.Time = parent.Time - 20 // the parent arrived at the target: the floor holds
	floor := new(big.Int).Set(testDifficulty)
	below := e.calcDifficulty(parent, gp) // height 1199: below the gate
	if below.Cmp(floor) != 0 {
		t.Fatalf("below the gate: %v, want the genesis floor %v", below, floor)
	}
	e.SetRuleForks(&ctypes.IgneumRuleForks{DiffMinV1: 1200, DiffMinV1Min: 6_000_000})
	if d := e.calcDifficulty(parent, gp); d.Cmp(floor) != 0 {
		t.Fatalf("height 1199 with the gate at 1200: %v, want the genesis floor", d)
	}
	parent.Number = big.NewInt(1199)
	gp.Number = big.NewInt(1198)
	at := e.calcDifficulty(parent, gp) // height 1200: the gate
	if at.Cmp(big.NewInt(6_000_000)) != 0 {
		t.Fatalf("at the landing check: %v, want the floor 6,000,000", at)
	}
	// A fast parent above the new floor raises the difficulty from it.
	parent.Difficulty = big.NewInt(6_000_000)
	gp.Time = parent.Time - 1
	fast := e.calcDifficulty(parent, gp)
	if fast.Cmp(big.NewInt(6_000_000)) <= 0 {
		t.Fatalf("a 1 s interval at the landing check must raise the difficulty above the floor: %v", fast)
	}
	// Off (zero): the genesis floor.
	e.SetRuleForks(&ctypes.IgneumRuleForks{})
	if d := e.calcDifficulty(parent, gp); d.Cmp(floor) < 0 {
		t.Fatalf("with the gate off the floor is the genesis set's: %v", d)
	}
}

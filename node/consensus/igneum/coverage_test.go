package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

func coverageEngine(t *testing.T, window uint64) *Engine {
	t.Helper()
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
	v.Rule = "coverage"
	v.Window = window
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// branch lays blocks above parent with the given coordinates and
// difficulties, returning the head.
func branch(chain *forkChain, parent *types.Header, coords []uint8, diffs []uint64, t0 uint64) *types.Header {
	p := parent
	for i, c := range coords {
		h := &types.Header{ParentHash: p.Hash(), UncleHash: types.EmptyUncleHash, Number: new(big.Int).Add(p.Number, big.NewInt(1)), Time: t0 + uint64(i+1)*20, GasLimit: p.GasLimit, Difficulty: new(big.Int).SetUint64(diffs[i])}
		h.SetCoordTag(c)
		chain.add(h)
		p = h
	}
	return p
}

// TestCoverageRuleAbandonedCoordinateLoses is revision 13's test of record
// (F-96, C-28 row 45): a branch frozen on a coordinate loses to the honest
// branch covering all four, whatever its work on the three it holds.
func TestCoverageRuleAbandonedCoordinateLoses(t *testing.T) {
	e := coverageEngine(t, 64)
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	// A shared prefix with all four coordinates, heavy on graph.
	prefix := branch(chain, g, []uint8{0, 1, 2, 3, 0, 1, 2, 3}, []uint64{100, 100, 100, 100, 100, 100, 100, 100}, g.Time)
	chain.setCanonical(prefix)
	// The attacker: three coordinates, three times the work, no graph.
	att := branch(chain, prefix, []uint8{0, 1, 2, 0, 1, 2, 0, 1, 2}, []uint64{300, 300, 300, 300, 300, 300, 300, 300, 300}, prefix.Time)
	// The honest branch: lighter, all four covered (graph at the floor).
	hon := branch(chain, prefix, []uint8{0, 1, 2, 3, 0, 1, 2}, []uint64{100, 100, 100, 8, 100, 100, 100}, prefix.Time)
	reorg, decided, err := e.PreferByWeight(chain, hon, att)
	if err != nil || !decided || reorg {
		t.Fatalf("the honest branch (coverage 4) must hold against the abandoner (coverage 3): reorg %v decided %v err %v", reorg, decided, err)
	}
	reorg, decided, err = e.PreferByWeight(chain, att, hon)
	if err != nil || !decided || !reorg {
		t.Fatalf("from the abandoner's head the honest branch must win: reorg %v decided %v err %v", reorg, decided, err)
	}
}

// TestCoverageRuleTrickleComparedOnOwnWork: an attacker trickling the
// abandoned coordinate at the floor reaches coverage 4 and is then compared
// at the second key on ITS OWN window's work, never the prefix's; with the
// honest side's graph work above the trickle's and the attacker's three
// held coordinates not enough to cover the gap, the honest branch holds.
func TestCoverageRuleTrickleComparedOnOwnWork(t *testing.T) {
	e := coverageEngine(t, 64)
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	prefix := branch(chain, g, []uint8{0, 1, 2, 3}, []uint64{100, 100, 100, 100000}, g.Time)
	chain.setCanonical(prefix)
	// Attacker: 1.2x on three coordinates, one floor block of graph (8).
	att := branch(chain, prefix, []uint8{0, 1, 2, 3, 0, 1, 2}, []uint64{120, 120, 120, 8, 120, 120, 120}, prefix.Time)
	// Honest: the three at 100, graph at 100 (above the trickle).
	hon := branch(chain, prefix, []uint8{0, 1, 2, 3, 0, 1, 2}, []uint64{100, 100, 100, 100, 100, 100, 100}, prefix.Time)
	// Both windows cover the same set, so the reference terms are equal
	// on both sides and the keys are the plain products: attacker
	// 240^3 x 8 = 110,592,000 against honest 200^3 x 100 = 800,000,000.
	reorg, decided, err := e.PreferByWeight(chain, hon, att)
	if err != nil || !decided || reorg {
		t.Fatalf("the trickle must lose on its own graph work: reorg %v decided %v err %v", reorg, decided, err)
	}
	// With the attacker's graph work at the honest level, 1.2x on the
	// other three wins at the second key (the stated residual).
	att2 := branch(chain, prefix, []uint8{0, 1, 2, 3, 0, 1, 2}, []uint64{120, 120, 120, 100, 120, 120, 120}, prefix.Time+1)
	reorg, decided, err = e.PreferByWeight(chain, hon, att2)
	if err != nil || !decided || !reorg {
		t.Fatalf("equal coverage and more normalised work must win: reorg %v decided %v err %v", reorg, decided, err)
	}
}

// TestCoverageRuleWindowDropsOldBlocks: a coordinate's block older than the
// window is not coverage.
func TestCoverageRuleWindowDropsOldBlocks(t *testing.T) {
	e := coverageEngine(t, 4)
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	prefix := branch(chain, g, []uint8{0, 1, 2, 3}, []uint64{100, 100, 100, 100}, g.Time)
	chain.setCanonical(prefix)
	// A: graph once at the start, then six of the others: the window of 4 holds no graph.
	a := branch(chain, prefix, []uint8{3, 0, 1, 2, 0, 1, 2}, []uint64{100, 100, 100, 100, 100, 100, 100}, prefix.Time)
	// B: graph inside the window.
	b := branch(chain, prefix, []uint8{0, 1, 2, 3}, []uint64{50, 50, 50, 50}, prefix.Time)
	reorg, decided, err := e.PreferByWeight(chain, a, b)
	if err != nil || !decided || !reorg {
		t.Fatalf("coverage inside the window must win: reorg %v decided %v err %v", reorg, decided, err)
	}
}

// TestCoordinatePayAccruedShare: under payByCoordinate a block of
// coordinate c is paid c's share of every height since c's last block,
// this one included; the shares of a height sum to its miner subsidy.
func TestCoordinatePayAccruedShare(t *testing.T) {
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
	v.Rule, v.PayByCoordinate = "coverage", true
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	// Heights 1..6: coordinates 0,1,2,3,0,1; then height 7 of coordinate 3.
	head := branch(chain, g, []uint8{0, 1, 2, 3, 0, 1}, []uint64{100, 100, 100, 100, 100, 100}, g.Time)
	chain.setCanonical(head)
	h7 := branch(chain, head, []uint8{3}, []uint64{100}, head.Time)
	pot, ok := e.coordinatePay(chain, h7)
	if !ok {
		t.Fatal("the coordinate pay rule is registered")
	}
	want := new(big.Int)
	for h := uint64(5); h <= 7; h++ {
		miner, _ := parts.Issuance.Reward(h)
		want.Add(want, coordinateShare(miner, 3, 4))
	}
	if pot.Cmp(want) != 0 {
		t.Fatalf("pot %s want %s (coordinate 3's shares of heights 5 to 7)", pot, want)
	}
	// Height 7's four shares sum to its miner subsidy exactly.
	miner, _ := parts.Issuance.Reward(7)
	sum := new(big.Int)
	for c := 0; c < 4; c++ {
		sum.Add(sum, coordinateShare(miner, c, 4))
	}
	if sum.Cmp(miner) != 0 {
		t.Fatalf("shares sum %s, subsidy %s", sum, miner)
	}
	// Coordinate 0's next block at 8 takes heights 6 to 8.
	h8 := branch(chain, h7, []uint8{0}, []uint64{100}, h7.Time)
	pot, _ = e.coordinatePay(chain, h8)
	want = new(big.Int)
	for h := uint64(6); h <= 8; h++ {
		miner, _ := parts.Issuance.Reward(h)
		want.Add(want, coordinateShare(miner, 0, 4))
	}
	if pot.Cmp(want) != 0 {
		t.Fatalf("pot %s want %s (coordinate 0's shares of heights 6 to 8)", pot, want)
	}
}

// TestWeightedShareSumsExactly: the registration's weights split every
// subsidy exactly (the remainder to the lowest indices) and the governing
// coordinate takes its weight's part.
func TestWeightedShareSumsExactly(t *testing.T) {
	w := []uint64{1, 3, 1, 1}
	for _, m := range []int64{1, 5, 6, 7, 1000, 7867813110351562500} {
		M := big.NewInt(m)
		sum := new(big.Int)
		for c := 0; c < 4; c++ {
			sum.Add(sum, weightedShare(M, c, w))
		}
		if sum.Cmp(M) != 0 {
			t.Fatalf("M %d: shares sum %s", m, sum)
		}
	}
	M := big.NewInt(600)
	if s := weightedShare(M, 1, w); s.Int64() != 300 {
		t.Fatalf("the governing coordinate's half: %s", s)
	}
	if s := weightedShare(M, 0, w); s.Int64() != 100 {
		t.Fatalf("a sixth: %s", s)
	}
}

// TestCoverageRuleFiveCoordinates (revision 16): the coverage count and
// the geometric key read the registered count of five; a branch covering
// five beats one covering four, and the shares over five sum exactly.
func TestCoverageRuleFiveCoordinates(t *testing.T) {
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates,
		ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000},
		ctypes.IgneumCoordinate{Name: "fabric-static", Algorithm: null.Name, Target: 80, DRef: 16000})
	v.Rule, v.Window, v.PayByCoordinate, v.PayShares = "coverage", 64, true, []uint64{1, 3, 1, 1, 2}
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	prefix := branch(chain, g, []uint8{0, 1, 2, 3, 4}, []uint64{100, 100, 100, 100, 100}, g.Time)
	chain.setCanonical(prefix)
	four := branch(chain, prefix, []uint8{0, 1, 2, 3, 0, 1, 2, 3}, []uint64{300, 300, 300, 300, 300, 300, 300, 300}, prefix.Time)
	five := branch(chain, prefix, []uint8{0, 1, 2, 3, 4}, []uint64{100, 100, 100, 100, 100}, prefix.Time)
	reorg, decided, err := e.PreferByWeight(chain, four, five)
	if err != nil || !decided || !reorg {
		t.Fatalf("the branch covering five must win: reorg %v decided %v err %v", reorg, decided, err)
	}
	for _, mm := range []int64{7, 8, 9, 1000} {
		M := big.NewInt(mm)
		sum := new(big.Int)
		for c := 0; c < 5; c++ {
			sum.Add(sum, weightedShare(M, c, v.PayShares))
		}
		if sum.Cmp(M) != 0 {
			t.Fatalf("M %d: five shares sum %s", mm, sum)
		}
	}
	if s := weightedShare(big.NewInt(800), 4, v.PayShares); s.Int64() != 200 {
		t.Fatalf("fabric-static's quarter: %s", s)
	}
}

// TestCoordinateActivation (revision 16): a coordinate registered with an
// activation height is refused below it, the shares below it are over the
// four active coordinates and sum exactly, and above it the five.
func TestCoordinateActivation(t *testing.T) {
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates,
		ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000},
		ctypes.IgneumCoordinate{Name: "fabric-static", Algorithm: null.Name, Target: 80, DRef: 16000, Activation: 10})
	v.Rule, v.Window, v.PayByCoordinate, v.PayShares = "coverage", 64, true, []uint64{1, 3, 1, 1, 4}
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	h9 := branch(chain, g, []uint8{0, 1, 2, 3, 0, 1, 2, 3, 0}, []uint64{100, 100, 100, 100, 100, 100, 100, 100, 100}, g.Time)
	chain.setCanonical(h9)
	early := &types.Header{ParentHash: h9.Hash(), Number: big.NewInt(10), Time: h9.Time + 20, Difficulty: big.NewInt(100)}
	early.SetCoordTag(4)
	if _, ok := e.coordOf(&types.Header{ParentHash: g.Hash(), Number: big.NewInt(5), Coord: early.Coord}); ok {
		t.Fatal("coordinate 4 below its activation must carry no coordinate")
	}
	if _, ok := e.coordOf(early); !ok {
		t.Fatal("coordinate 4 at its activation height is a coordinate")
	}
	// The shares at height 9 are over four weights (1,3,1,1 of 6), at 10
	// over five (of 10); both sum to M exactly and coordinate 4 takes
	// nothing below 10.
	w := v.PayShares
	for _, h := range []uint64{9, 10} {
		wh := make([]uint64, 5)
		for i := range w {
			if m.ActiveAt(uint8(i), h) {
				wh[i] = w[i]
			}
		}
		M := big.NewInt(1001)
		sum := new(big.Int)
		for c := 0; c < 5; c++ {
			sum.Add(sum, weightedShare(M, c, wh))
		}
		if sum.Cmp(M) != 0 {
			t.Fatalf("height %d: shares sum %s", h, sum)
		}
		if h == 9 && weightedShare(M, 4, wh).Sign() != 0 {
			t.Fatal("coordinate 4 takes a share below its activation")
		}
	}
}

// TestPaySharesAtChange: the weights in force by height under payChanges,
// and the accrued pot split across a change height.
func TestPaySharesAtChange(t *testing.T) {
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
	v.Rule = "coverage"
	v.PayByCoordinate = true
	v.PayShares = []uint64{1, 3, 1, 1}
	v.PayChanges = []ctypes.IgneumPayChange{{Height: 100, PayShares: []uint64{1, 1, 1, 1}}}
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if w := m.PaySharesAt(99); w[1] != 3 {
		t.Fatalf("below the change: %v", w)
	}
	if w := m.PaySharesAt(100); w[1] != 1 {
		t.Fatalf("at the change: %v", w)
	}
	if w := m.PaySharesAt(1000); w[1] != 1 {
		t.Fatalf("above the change: %v", w)
	}
}

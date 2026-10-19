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

// The fork choice's same-height rule and the served measure (the review lead's
// ruling ed600ddc8, b5460078c as main re-pointed it, the served band at the
// fork b9ff75f28, 16 October 2026). The fixture: the coverage rule over
// four coordinates (tensor 1000, bandwidth 2000, fabric 4000 as the
// references; graph 8000 with its own floor 8, so a graph block at 8 is a
// FLOOR block), a half-life of ten seconds, a prefix of eight blocks over
// the first three lanes at twenty-second intervals and NONE on graph, so at
// the fork (the prefix's head) the controller reads the three lanes far
// above twice their floor (served) and graph at its floor 8, under the band
// 16 (unserved whatever its blocks above the fork carry); then the races
// above it.

func firstSeenEngine(t *testing.T) *Engine {
	return firstSeenEngineRule(t, "coverage")
}

func firstSeenEngineRule(t *testing.T, rule string) *Engine {
	t.Helper()
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000, DMin: 8})
	v.Rule = rule
	v.Window = 64
	v.HalfLife = 10
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

func firstSeenChain(t *testing.T) (*Engine, *forkChain, *types.Header) {
	return firstSeenChainRule(t, "coverage")
}

func firstSeenChainRule(t *testing.T, rule string) (*Engine, *forkChain, *types.Header) {
	t.Helper()
	e := firstSeenEngineRule(t, rule)
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	prefix := branch(chain, g, []uint8{0, 1, 2, 0, 1, 2, 0, 1}, []uint64{100, 100, 100, 100, 100, 100, 100, 100}, g.Time)
	for h := prefix; h.Number.Sign() > 0; h = chain.GetHeader(h.ParentHash, h.Number.Uint64()-1) {
		chain.setCanonical(h)
	}
	band, err := e.servedAt(chain, e.vectorMenu(), prefix)
	if err != nil || len(band) != 4 || !band[0] || !band[1] || !band[2] || band[3] {
		t.Fatalf("the served band at the fork must read the three lanes in and graph at its floor out: %v err %v", band, err)
	}
	return e, chain, prefix
}

func prefer(t *testing.T, e *Engine, chain *forkChain, current, extern *types.Header, want bool, why string) {
	t.Helper()
	reorg, decided, err := e.PreferByWeight(chain, current, extern)
	if err != nil || !decided || reorg != want {
		t.Fatalf("%s: reorg %v (want %v) decided %v err %v", why, reorg, want, decided, err)
	}
}

// TestFirstSeenRuleAtOneHeight: below the gate a same-height race is decided
// by the normalised weight (the heavy lane's block replaces the light lane's
// whatever arrived first); from the landing check the block imported first stands
// between single blocks, both ways, and the lever is gone.
func TestFirstSeenRuleAtOneHeight(t *testing.T) {
	e, chain, prefix := firstSeenChain(t)
	h := prefix.Number.Uint64() + 1
	light := branch(chain, prefix, []uint8{0}, []uint64{8}, prefix.Time)
	heavy := branch(chain, prefix, []uint8{1}, []uint64{1000}, prefix.Time+1)
	prefer(t, e, chain, light, heavy, true, "below the gate the heavy block replaces the light one at the same height")
	e.SetFirstSeenFrom(h)
	prefer(t, e, chain, light, heavy, false, "from the landing check the light block imported first stands against the heavy one")
	prefer(t, e, chain, heavy, light, false, "from the landing check the heavy block imported first stands against the light one")
	// The lever: a heavy lane's competing block produced AFTER the light
	// lane's at the same height replaces it on no node.
	late := branch(chain, prefix, []uint8{1}, []uint64{100000}, prefix.Time+5)
	prefer(t, e, chain, light, late, false, "the lever: a later heavy block at the same height replaced the first")
	// Deeper branches at equal height are the measure's, not first-seen's:
	// two blocks each side, the heavier pair replaces.
	light2 := branch(chain, light, []uint8{0}, []uint64{8}, light.Time)
	heavy2 := branch(chain, heavy, []uint8{2}, []uint64{1000}, heavy.Time)
	prefer(t, e, chain, light2, heavy2, true, "at equal height past single blocks the measure decides")
	// The gate is a height: a race whose first contested height is under
	// it stays on the measure.
	e.SetFirstSeenFrom(h + 1)
	prefer(t, e, chain, light, heavy, true, "a race under the landing check stays on the measure")
	e.SetFirstSeenFrom(0)
	if e.firstSeenOn(h) {
		t.Fatal("the rule reads on with no gate")
	}
}

// TestFirstSeenUnequalHeights (the measure primary and total): at unequal
// heights the served measure decides, a shorter heavier branch replacing
// (the two-for-three margin, the floor lane's own weakness) and a longer
// lighter one not; the longer wins at equal measure; no standing split.
func TestFirstSeenUnequalHeights(t *testing.T) {
	e, chain, prefix := firstSeenChain(t)
	e.SetFirstSeenFrom(prefix.Number.Uint64() + 1)
	light := branch(chain, prefix, []uint8{0}, []uint64{8}, prefix.Time)
	heavier := branch(chain, prefix, []uint8{1, 2}, []uint64{1000, 1000}, prefix.Time+1)
	prefer(t, e, chain, light, heavier, true, "a longer heavier branch replaces the single light block")
	twoLight := branch(chain, light, []uint8{0}, []uint64{8}, light.Time)
	oneHeavy := branch(chain, prefix, []uint8{0}, []uint64{100000}, prefix.Time+2)
	prefer(t, e, chain, twoLight, oneHeavy, true, "a shorter heavier branch replaces by the measure (the margin, named)")
	prefer(t, e, chain, oneHeavy, twoLight, false, "and the longer lighter branch does not replace it: no standing split, one order on every node")
	heavyHead := branch(chain, prefix, []uint8{1}, []uint64{2000}, prefix.Time+3)
	floorRun := branch(chain, prefix, []uint8{0, 0}, []uint64{8, 8}, prefix.Time+4)
	prefer(t, e, chain, heavyHead, floorRun, false, "a longer but lighter branch does not replace")
	eight := branch(chain, prefix, []uint8{0}, []uint64{8}, prefix.Time+6)
	fourFour := branch(chain, prefix, []uint8{0, 0}, []uint64{4, 4}, prefix.Time+7)
	prefer(t, e, chain, eight, fourFour, true, "a longer branch of equal measure replaces (the length order)")
	prefer(t, e, chain, fourFour, eight, false, "a shorter branch of equal measure does not")
}

// TestFirstSeenLeverAtUnequalHeights (the security review's first cell): the
// shorter heavier head against the longer lighter one resolves by the
// measure on every node, never a standing split: a heavy block against a
// run of quarter blocks of the same lane holds while the run's measure is
// under its own, and the run takes over once equal and longer; both nodes
// read the same order at every step.
func TestFirstSeenLeverAtUnequalHeights(t *testing.T) {
	e, chain, prefix := firstSeenChain(t)
	e.SetFirstSeenFrom(1)
	heavy := branch(chain, prefix, []uint8{0}, []uint64{100}, prefix.Time)
	quarter := branch(chain, prefix, []uint8{0}, []uint64{25}, prefix.Time+1)
	prefer(t, e, chain, heavy, quarter, false, "single blocks at one height: the heavy node holds its first block")
	prefer(t, e, chain, quarter, heavy, false, "single blocks at one height: the light node holds its first block (the transient split)")
	q := quarter
	for i := 2; i <= 3; i++ {
		q = branch(chain, q, []uint8{0}, []uint64{25}, q.Time)
		prefer(t, e, chain, heavy, q, false, "the run under the heavy block's measure does not replace it")
		prefer(t, e, chain, q, heavy, true, "the heavy block replaces the lighter run on the other node: one order, no standing split")
	}
	q = branch(chain, q, []uint8{0}, []uint64{25}, q.Time)
	prefer(t, e, chain, heavy, q, true, "four quarters: equal measure and longer, the run replaces the heavy block")
	prefer(t, e, chain, q, heavy, false, "and the heavy block never replaces the run from here")
}

// TestFirstSeenConvergenceUnderLeastLaneMajority (the security review's second
// cell): a floor lane's majority holder stacks that lane's floor blocks on
// one branch; under the served form the stacked blocks are excluded from
// the first key (graph at its floor at the fork, under the band) and count
// only for their small scaled sum, so a split between branches of equal
// honest work is decided by the honest lanes' minimum and the length, never
// by the floor holder's blocks.
func TestFirstSeenConvergenceUnderLeastLaneMajority(t *testing.T) {
	e, chain, prefix := firstSeenChain(t)
	e.SetFirstSeenFrom(1)
	x := branch(chain, prefix, []uint8{0, 1}, []uint64{100, 100}, prefix.Time)
	y := branch(chain, prefix, []uint8{0, 1, 3}, []uint64{100, 100, 8}, prefix.Time+1)
	// The served minimum is equal (lanes 0 and 1 at 100 each; graph at its
	// floor at the fork excluded); Y is longer and carries the floor
	// block's hair of sum, so Y replaces X by the sum and the length alike.
	prefer(t, e, chain, x, y, true, "equal served minimum: the longer branch replaces (the floor block's hair of sum beside)")
	// The honest branch with one more lane served beats the stacked one:
	// X2 (lanes 0, 1, 2 at 100) against Y2 (lanes 0, 1 at 100 and two
	// graph floor blocks): Y2's served minimum is zero (fabric absent),
	// X2's is 100 over 4000, so X2 stands and replaces Y2 on the other
	// node; the floor holder's blocks swing nothing.
	x2 := branch(chain, x, []uint8{2}, []uint64{100}, x.Time)
	y2 := branch(chain, y, []uint8{3}, []uint64{8}, y.Time)
	prefer(t, e, chain, x2, y2, false, "the floor holder's stacked blocks do not move the node on the honest branch")
	prefer(t, e, chain, y2, x2, true, "the honest branch wins by the served lanes' minimum on the other node")
	// A floor lane withheld from one branch and stacked on the other is
	// still excluded (the mark is read on either branch): Y3 with three
	// floor blocks against X2 reads the same.
	y3 := branch(chain, y2, []uint8{3}, []uint64{8}, y2.Time)
	prefer(t, e, chain, x2, y3, false, "withholding and stacking the floor lane casts no vote")
	// Below the gate the floor blocks counted for coverage and the product;
	// the point of the served form is above it.
	e.SetFirstSeenFrom(0)
	if _, decided, err := e.PreferByWeight(chain, x2, y2); err != nil || !decided {
		t.Fatalf("below the gate the measure must decide: decided %v err %v", decided, err)
	}
}

// TestFirstSeenServedBandAtTheFork (the security review's read of 1acc2ea4d, the
// chair's ruling b9ff75f28): the served test is read at the two branches'
// common ancestor by a band (the lane's difficulty at the fork at or above
// twice its floor), never on the branches' own blocks, so a floor lane's
// holder lifting the lane one unit above its floor on its own branch (or
// any height above it) changes nothing in the comparison, and a served
// lane stays served where a branch's blocks of it sit at the floor: the
// branch lacking it scores zero (presence as the union, the burst
// defence).
func TestFirstSeenServedBandAtTheFork(t *testing.T) {
	e, chain, prefix := firstSeenChain(t)
	e.SetFirstSeenFrom(1)
	x := branch(chain, prefix, []uint8{0, 1}, []uint64{100, 100}, prefix.Time)
	x2 := branch(chain, x, []uint8{2}, []uint64{100}, x.Time)
	// The floor lane lifted one unit on its own branch: Y9 (lanes 0, 1 at
	// 100 and two graph blocks at 9, one above the floor 8) reads exactly as
	// the floor-block branch of the convergence cell: graph stays out of
	// the first key, the honest branch stands and replaces.
	y9 := branch(chain, prefix, []uint8{0, 1, 3, 3}, []uint64{100, 100, 9, 9}, prefix.Time+1)
	prefer(t, e, chain, x2, y9, false, "a floor lane lifted one unit on its own branch moves nothing")
	prefer(t, e, chain, y9, x2, true, "and the honest branch replaces it by the served lanes' minimum")
	// Lifted far above its floor on its own branch (the chip's lever on the
	// point mark: the lane made served on both branches by one branch's
	// mining): the band at the fork reads graph at its floor still, from
	// the engine's own reader, whatever the branch's blocks carry.
	yBig := branch(chain, prefix, []uint8{0, 1, 3, 3}, []uint64{100, 100, 1000, 1000}, prefix.Time+2)
	band, err := e.servedAt(chain, e.vectorMenu(), prefix)
	if err != nil || band[3] {
		t.Fatalf("the band at the fork after a lifted branch: %v err %v (graph must read out)", band, err)
	}
	_ = yBig
}

// The withholding lever (the security review's read of 2b1fdeddd; the review lead's
// ruling bf15bed11): L = 4k blocks of a branch's own length above the
// ancestor, k the served-lane count there (three in this fixture: L = 12;
// four in the K4 fixture: L = 16). Under L the scaled sum decides, then
// the length; past L on both branches a served lane absent on a branch
// that carries every other served lane is excluded from the first key for
// both, the key the minimum over the rest, a branch lacking two or more
// served lanes at zero.

// lanes lays n blocks above parent cycling over the coordinates given, all
// at one difficulty.
func lanes(chain *forkChain, parent *types.Header, coords []uint8, n int, d uint64, t0 uint64) *types.Header {
	cs := make([]uint8, n)
	ds := make([]uint64, n)
	for i := range cs {
		cs[i] = coords[i%len(coords)]
		ds[i] = d
	}
	return branch(chain, parent, cs, ds, t0)
}

// TestFirstSeenWithholder (cell 1): a served lane held by one sealer is
// absent on the honest branch for L and more blocks (full work on the
// other lanes); against the withholder's private branch of L blocks of
// every lane the honest branch is canonical (the lane excluded, the
// minimum over the others), and against a private branch of four blocks
// the honest branch is canonical by the sum under L.
func TestFirstSeenWithholder(t *testing.T) {
	e, chain, prefix := firstSeenChain(t)
	e.SetFirstSeenFrom(1)
	if L := withholdingL(mustBand(t, e, chain, prefix)); L != 12 {
		t.Fatalf("L = %d, want 12 at three served lanes", L)
	}
	honest := lanes(chain, prefix, []uint8{0, 1}, 12, 100, prefix.Time)
	private := lanes(chain, prefix, []uint8{0, 1, 2}, 12, 50, prefix.Time+1)
	prefer(t, e, chain, honest, private, false, "past L the withheld lane is excluded and the honest branch holds by the minimum over the others")
	prefer(t, e, chain, private, honest, true, "and from the private branch the honest one replaces")
	short := lanes(chain, prefix, []uint8{0, 1, 2, 2}, 4, 100, prefix.Time+2)
	prefer(t, e, chain, honest, short, false, "under L the sum decides: the honest branch holds against four blocks of every lane")
	prefer(t, e, chain, short, honest, true, "and replaces from the short branch")
}

// TestFirstSeenLuckCell (cell 2): an honest branch of L blocks lacking one
// lane by chance against a shorter branch of every lane is canonical by
// the sum; both past L, the lane is excluded and the minimum over the
// others decides.
func TestFirstSeenLuckCell(t *testing.T) {
	e, chain, prefix := firstSeenChain(t)
	e.SetFirstSeenFrom(1)
	honest := lanes(chain, prefix, []uint8{0, 1}, 12, 100, prefix.Time)
	shorter := lanes(chain, prefix, []uint8{0, 1, 2}, 6, 100, prefix.Time+1)
	prefer(t, e, chain, honest, shorter, false, "under L the sum: the longer honest branch holds")
	prefer(t, e, chain, shorter, honest, true, "and replaces from the shorter")
	// Both past L: fabric excluded (absent on the honest branch alone);
	// the minimum over tensor and bandwidth: the honest 600 on each
	// (bandwidth 600 / 2000 the minimum), the full branch 400 on each at
	// 100 (bandwidth 400 / 2000): the honest holds; at 200 a block the
	// full branch's 800 / 2000 outweighs it and replaces.
	full100 := lanes(chain, prefix, []uint8{0, 1, 2}, 12, 100, prefix.Time+2)
	prefer(t, e, chain, honest, full100, false, "past L the lacking lane is excluded; the honest branch holds by the minimum over the rest")
	full200 := lanes(chain, prefix, []uint8{0, 1, 2}, 12, 200, prefix.Time+3)
	prefer(t, e, chain, honest, full200, true, "past L the heavier minimum over the rest replaces")
	prefer(t, e, chain, full200, honest, false, "and is not replaced")
}

// TestFirstSeenSingleLaneMajorityPastL (cell 3): past L a branch of one
// lane scores zero on the first key against a branch of every lane
// whatever its work; two lanes of four the same (the K4 fixture).
func TestFirstSeenSingleLaneMajorityPastL(t *testing.T) {
	e, chain, prefix := firstSeenChain(t)
	e.SetFirstSeenFrom(1)
	one := lanes(chain, prefix, []uint8{0}, 12, 100000, prefix.Time)
	all := lanes(chain, prefix, []uint8{0, 1, 2}, 12, 100, prefix.Time+1)
	prefer(t, e, chain, all, one, false, "a one-lane branch past L scores zero: the full branch holds")
	prefer(t, e, chain, one, all, true, "and replaces the one-lane branch")
	e4, chain4, prefix4 := firstSeenChainK4(t)
	e4.SetFirstSeenFrom(1)
	if L := withholdingL(mustBand(t, e4, chain4, prefix4)); L != 16 {
		t.Fatalf("L = %d, want 16 at four served lanes", L)
	}
	two := lanes(chain4, prefix4, []uint8{0, 1}, 16, 100000, prefix4.Time)
	four := lanes(chain4, prefix4, []uint8{0, 1, 2, 3}, 16, 100, prefix4.Time+1)
	prefer(t, e4, chain4, four, two, false, "two lanes of four past L score zero: the full branch holds")
	prefer(t, e4, chain4, two, four, true, "and replaces the two-lane branch")
}

// TestFirstSeenCostCell (cell 4, the rule's cost, neither pass nor fail):
// past L a branch of all served lanes but one, with more work than the
// honest branch on each of the others, wins; the rule's reach is one lane.
func TestFirstSeenCostCell(t *testing.T) {
	e, chain, prefix := firstSeenChain(t)
	e.SetFirstSeenFrom(1)
	honest := lanes(chain, prefix, []uint8{0, 1, 2}, 12, 100, prefix.Time)
	attacker := lanes(chain, prefix, []uint8{0, 1}, 12, 300, prefix.Time+1)
	prefer(t, e, chain, honest, attacker, true, "the cost: all served lanes but one with more work on each of the others replaces past L")
	prefer(t, e, chain, attacker, honest, false, "and is not replaced")
	t.Log("the rule's cost recorded: a party alone on two served lanes reorganises at will past L; the reach is one lane (VECTORWORK.md)")
}

func mustBand(t *testing.T, e *Engine, chain *forkChain, anc *types.Header) []bool {
	t.Helper()
	band, err := e.servedAt(chain, e.vectorMenu(), anc)
	if err != nil {
		t.Fatal(err)
	}
	return band
}

// firstSeenChainK4 is the fixture with graph served too (two graph blocks
// in the prefix, so its controller figure at the fork is far above twice
// its floor): four served lanes, L = 16.
func firstSeenChainK4(t *testing.T) (*Engine, *forkChain, *types.Header) {
	t.Helper()
	e := firstSeenEngineRule(t, "coverage")
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	prefix := branch(chain, g, []uint8{0, 1, 2, 3, 0, 1, 2, 3}, []uint64{100, 100, 100, 100, 100, 100, 100, 100}, g.Time)
	for h := prefix; h.Number.Sign() > 0; h = chain.GetHeader(h.ParentHash, h.Number.Uint64()-1) {
		chain.setCanonical(h)
	}
	band, err := e.servedAt(chain, e.vectorMenu(), prefix)
	if err != nil || len(band) != 4 || !band[0] || !band[1] || !band[2] || !band[3] {
		t.Fatalf("the K4 band at the fork must read all four in: %v err %v", band, err)
	}
	return e, chain, prefix
}

// TestFirstSeenServedReadFromTheBranch (the security review's read of 1acc2ea4d):
// under the minimum rule the served set and the order are read from the
// branches' own headers and the shared ancestor, never from the canonical
// chain by number, so two nodes holding different heads read one order;
// the test re-reads the race with each branch canonical in turn.
func TestFirstSeenServedReadFromTheBranch(t *testing.T) {
	e, chain, prefix := firstSeenChainRule(t, "minimum")
	e.SetFirstSeenFrom(1)
	x := branch(chain, prefix, []uint8{0, 1}, []uint64{100, 100}, prefix.Time)
	y := branch(chain, prefix, []uint8{0, 1, 2}, []uint64{100, 100, 1}, prefix.Time+1)
	// The cumulative minimum: fabric 200 on X against 201 on Y (its floor
	// block above the fork, the lane served by the band at the fork), so Y
	// outweighs X on the first key from either side.
	check := func(which string) {
		t.Helper()
		prefer(t, e, chain, x, y, true, which+": the heavier fabric replaces")
		prefer(t, e, chain, y, x, false, which+": and is not replaced")
	}
	check("the prefix canonical")
	for h := x; h.Number.Cmp(prefix.Number) > 0; h = chain.GetHeader(h.ParentHash, h.Number.Uint64()-1) {
		chain.setCanonical(h)
	}
	check("X canonical")
	for h := y; h.Number.Cmp(prefix.Number) > 0; h = chain.GetHeader(h.ParentHash, h.Number.Uint64()-1) {
		chain.setCanonical(h)
	}
	check("Y canonical")
}

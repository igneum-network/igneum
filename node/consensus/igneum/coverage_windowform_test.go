package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// vw7Engine is vectorwork7's five-coordinate registration (its references)
// under the coverage rule with the given window form.
func vw7Engine(t *testing.T, form string) *Engine {
	t.Helper()
	v := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Window: 64, WindowForm: form}
	for i, d := range []uint64{79360, 1700000, 272560, 101711, 45248730} {
		v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: []string{"tensor", "bandwidth", "fabric", "graph", "fabric-static"}[i], Algorithm: null.Name, Target: 80, DRef: d})
	}
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

// TestCoverageWindowFormSlideRaces is revision 16's test vector (VECTORWORK.md
// at b22d69be2; the security review's vectorwork7 slide cell, docs/security/records/
// vectorwork-live-2026-10-13/vw7-cell3-slide): the three depth-one races at
// heights 26, 96 and 100 in which the honest fabric-static sealer's block
// (its difficulty near the floor) lost to the honest fleet's own near-target
// block under the suffix form; the window before each lacked coordinate 4,
// so under the branch form the fabric-static sibling wins at key 1
// (coverage 5 against 4). The chain is the record's honest branch (its
// coordinates and difficulties by height).
func TestCoverageWindowFormSlideRaces(t *testing.T) {
	races := []struct {
		n          int
		coords     []uint8
		diffs      []uint64
		canonCoord uint8
		canonDiff  uint64
		orphanDiff uint64
	}{
		{n: 26, coords: []uint8{0,2,1,3,2,1,1,1,1,0,2,0,0,1,3,1,0,1,2,2,3,1,2,1,1}, diffs: []uint64{79360,272560,1615889,95716,262888,1554878,1637147,1717146,1718469,65061,230627,63036,65212,1498420,67983,1505372,61358,1552479,192564,202764,66023,1579003,206871,1614644,1701296}, canonCoord: 0, canonDiff: 57778, orphanDiff: 1791567},
		{n: 96, coords: []uint8{0,2,1,3,2,1,1,1,1,0,2,0,0,1,3,1,0,1,2,2,3,1,2,1,1,0,3,2,2,1,1,1,2,0,0,1,3,3,3,3,1,2,1,1,1,2,0,1,1,1,2,1,0,1,1,0,2,1,3,2,2,0,2,1,1,1,3,1,1,1,1,2,0,1,0,1,0,0,2,2,2,3,2,0,1,0,2,2,1,0,2,0,0,0,2}, diffs: []uint64{79360,272560,1615889,95716,262888,1554878,1637147,1717146,1718469,65061,230627,63036,65212,1498420,67983,1505372,61358,1552479,192564,202764,66023,1579003,206871,1614644,1701296,57778,62129,192117,199974,1580222,1634644,1721141,199974,50455,52193,1654877,52938,55742,57662,60479,1619625,163949,1477798,1535839,1601039,155236,39557,1652322,1731802,1804045,154758,1883551,39074,1927545,2015481,37427,135755,1853330,38450,138829,145290,34813,151574,1843370,1942434,2043574,36179,1939425,2029489,2084870,2196853,130135,29135,2210420,29793,2304270,30940,32355,124550,131241,136808,29008,139688,31108,2126971,30023,130236,137230,1961941,29931,138613,31180,32858,34599,141640}, canonCoord: 0, canonDiff: 36069, orphanDiff: 5302283},
		{n: 100, coords: []uint8{0,2,1,3,2,1,1,1,1,0,2,0,0,1,3,1,0,1,2,2,3,1,2,1,1,0,3,2,2,1,1,1,2,0,0,1,3,3,3,3,1,2,1,1,1,2,0,1,1,1,2,1,0,1,1,0,2,1,3,2,2,0,2,1,1,1,3,1,1,1,1,2,0,1,0,1,0,0,2,2,2,3,2,0,1,0,2,2,1,0,2,0,0,0,2,0,0,3,2}, diffs: []uint64{79360,272560,1615889,95716,262888,1554878,1637147,1717146,1718469,65061,230627,63036,65212,1498420,67983,1505372,61358,1552479,192564,202764,66023,1579003,206871,1614644,1701296,57778,62129,192117,199974,1580222,1634644,1721141,199974,50455,52193,1654877,52938,55742,57662,60479,1619625,163949,1477798,1535839,1601039,155236,39557,1652322,1731802,1804045,154758,1883551,39074,1927545,2015481,37427,135755,1853330,38450,138829,145290,34813,151574,1843370,1942434,2043574,36179,1939425,2029489,2084870,2196853,130135,29135,2210420,29793,2304270,30940,32355,124550,131241,136808,29008,139688,31108,2126971,30023,130236,137230,1961941,29931,138613,31180,32858,34599,141640,36069,37949,23007,143510}, canonCoord: 2, canonDiff: 148916, orphanDiff: 4981616},
	}
	for _, form := range []string{"suffix", "branch"} {
		e := vw7Engine(t, form)
		for _, r := range races {
			chain := newForkChain()
			g := genesisHeader()
			chain.setCanonical(g)
			prefix := branch(chain, g, r.coords, r.diffs, g.Time)
			chain.setCanonical(prefix)
			canon := branch(chain, prefix, []uint8{r.canonCoord}, []uint64{r.canonDiff}, prefix.Time)
			orphan := branch(chain, prefix, []uint8{4}, []uint64{r.orphanDiff}, prefix.Time)
			reorg, decided, err := e.PreferByWeight(chain, canon, orphan)
			if err != nil || !decided {
				t.Fatalf("%s race %d: decided %v err %v", form, r.n, decided, err)
			}
			if form == "suffix" && reorg {
				t.Fatalf("suffix form race %d: the fabric-static sibling must lose on its own block's work", r.n)
			}
			if form == "branch" && !reorg {
				t.Fatalf("branch form race %d: the fabric-static sibling must win at key 1 (coverage 5 against 4)", r.n)
			}
		}
	}
}

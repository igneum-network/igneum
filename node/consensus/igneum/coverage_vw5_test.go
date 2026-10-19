package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestCoverageRaceVectorwork5At52 replays vectorwork5's race at height 52
// (13 October 2026, 10:46:50Z, the first race contrary to the geometric
// key on a coverage-rule chain): the bandwidth twin 0x48f01b (D 510,516,
// normalised 0.536, sealed first) against the fabric twin 0x9fda3c
// (D 239,132, normalised 0.877), both one block above the common ancestor
// 51; coverage 1 each, so revision 13's second key, the normalised
// single-block work, keeps the fabric twin, as the chain did, where the
// cumulative geometric key (8.475 against 8.450) would have kept the
// bandwidth twin.
func TestCoverageRaceVectorwork5At52(t *testing.T) {
	v := &ctypes.IgneumVector{
		Coordinates: []ctypes.IgneumCoordinate{
			{Name: "tensor", Algorithm: null.Name, Target: 80, DRef: 79360, LagCap: 2948},
			{Name: "bandwidth", Algorithm: null.Name, Target: 80, DRef: 952560, LagCap: 6175},
			{Name: "fabric", Algorithm: null.Name, Target: 80, DRef: 272560, LagCap: 4550},
			{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 101711, LagCap: 3270},
		},
		HalfLife: 900, DtMax: 900, Rule: "coverage", Window: 64, PayByCoordinate: true,
	}
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(8192)
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	coords := []uint8{}
	diffs := []uint64{}
	for i := 0; i < 51; i++ {
		coords = append(coords, uint8(i%4))
		diffs = append(diffs, 100000)
	}
	anc := branch(chain, g, coords, diffs, g.Time)
	chain.setCanonical(anc)
	bandwidth := branch(chain, anc, []uint8{1}, []uint64{510516}, anc.Time)
	fabric := branch(chain, anc, []uint8{2}, []uint64{239132}, anc.Time+1)
	reorg, decided, err := e.PreferByWeight(chain, bandwidth, fabric)
	if err != nil || !decided {
		t.Fatalf("decided %v err %v", decided, err)
	}
	if !reorg {
		t.Fatalf("the fabric twin (0.877) must replace the bandwidth twin (0.536) under the coverage key")
	}
	reorg, _, _ = e.PreferByWeight(chain, fabric, bandwidth)
	if reorg {
		t.Fatalf("from the fabric head the bandwidth twin must not win")
	}
}

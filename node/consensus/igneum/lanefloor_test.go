package igneum

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestLaneFloorReadsTheFormInForce (F-245, the security review's residual on the
// lane entry as landed): the served band at a race's common ancestor is
// twice the lane's floor, and the floor is the form in force at the
// ancestor's child height, not the base document's. The first-seen
// fixture (three lanes far above their floors; graph at its floor 8 with
// no block) gains an in-place change on graph at height 9 lifting its
// floor to 16: laneFloor reads 8 under the change's height and 16 from
// it; at the fork (the prefix's head 8, the child 9) graph's controller
// figure sits at or above its new floor 16 and under the new band 32, so
// graph reads OUT. Under the base floor's band (16) the same figure would
// have read graph as served by the lift alone, the review lead's fault.
func TestLaneFloorReadsTheFormInForce(t *testing.T) {
	v := vectorConfig()
	lifted := ctypes.IgneumDocumentChange{Height: 9, AlgorithmParams: json.RawMessage(`{"form":"lifted"}`), DRef: 8000, DMin: 16, LagCap: vector.DerivedLagCap(10, 8000, 16)}
	v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, AlgorithmParams: json.RawMessage(`{"form":"base"}`), Target: 80, DRef: 8000, DMin: 8, DocumentChanges: []ctypes.IgneumDocumentChange{lifted}})
	v.Rule = "coverage"
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
	if f := e.laneFloor(m, 3, 8); f.Cmp(big.NewInt(8)) != 0 {
		t.Fatalf("graph's floor under the change's height: %v, want the base 8", f)
	}
	if f := e.laneFloor(m, 3, 9); f.Cmp(big.NewInt(16)) != 0 {
		t.Fatalf("graph's floor from the change's height: %v, want the change's 16", f)
	}
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	prefix := branch(chain, g, []uint8{0, 1, 2, 0, 1, 2, 0, 1}, []uint64{100, 100, 100, 100, 100, 100, 100, 100}, g.Time)
	for h := prefix; h.Number.Sign() > 0; h = chain.GetHeader(h.ParentHash, h.Number.Uint64()-1) {
		chain.setCanonical(h)
	}
	// The cell's condition: graph's figure at the fork is at or above the
	// base floor's band (16, the fault's "served") and under the lifted
	// floor's band (32).
	d, err := e.vectorDifficulty(chain, prefix, 3)
	if err != nil || d.Cmp(big.NewInt(16)) < 0 || d.Cmp(big.NewInt(32)) >= 0 {
		t.Fatalf("graph's controller figure at the fork under the lifted form: %v err %v, want at or above 16 and under 32", d, err)
	}
	band, err := e.servedAt(chain, m, prefix)
	if err != nil || len(band) != 4 || !band[0] || !band[1] || !band[2] || band[3] {
		t.Fatalf("the served band at the fork under the lifted floor: %v err %v (graph at %v is under the band 32: out)", band, err, d)
	}
	// The other direction (the review lead's ruling: both on the record): the
	// same change lowering graph's floor from 8 to 4. Graph's figure at the
	// fork is the reference decayed by the idle prefix's lag (160 s, no
	// graph block) and clamped at the form's floor: at the half-life of
	// ten it sits on the clamp (16 lifted, 4 lowered), so the lowered
	// engine takes a half-life of seventeen, where the lag places the
	// figure at 8000 x 2^(-160/17) = 11: IN under the lowered band 8 and
	// OUT under the base floor's band 16, the fault's lowered side.
	lowered := lifted
	lowered.DMin, lowered.LagCap = 4, vector.DerivedLagCap(17, 8000, 4)
	v.HalfLife = 17
	v.Coordinates[3].DocumentChanges = []ctypes.IgneumDocumentChange{lowered}
	m2, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := New(Config{Algorithm: m2, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e2.Close() })
	if f := e2.laneFloor(m2, 3, 9); f.Cmp(big.NewInt(4)) != 0 {
		t.Fatalf("graph's floor from the lowering change's height: %v, want 4", f)
	}
	d2, err := e2.vectorDifficulty(chain, prefix, 3)
	if err != nil || d2.Cmp(big.NewInt(8)) < 0 || d2.Cmp(big.NewInt(16)) >= 0 {
		t.Fatalf("graph's controller figure at the fork under the lowered form: %v err %v, want at or above 8 and under 16", d2, err)
	}
	if band, err := e2.servedAt(chain, m2, prefix); err != nil || len(band) != 4 || !band[3] {
		t.Fatalf("the served band at the fork under the lowered floor: %v err %v (graph at %v is at or above the band 8: in; the base floor's band 16 would read it out)", band, err, d2)
	}
}

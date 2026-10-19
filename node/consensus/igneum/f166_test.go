package igneum

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestSealingBreadth (F-166): from sealingBreadthFrom a block whose
// coordinate has pay weight 0 at its own height is refused on import and
// never assembled; below the height, and once a payChanges entry restores
// the weight, the coordinate seals; the fork vector under the minimum rule
// reads the sealable coordinates alone; the admission sets the height once
// above the head.
func TestSealingBreadth(t *testing.T) {
	const A = 10
	v := vectorConfig()
	v.Rule = "minimum"
	v.PayByCoordinate = true
	v.PayShares = []uint64{1, 1, 1}
	v.PayChanges = []ctypes.IgneumPayChange{{Height: A, PayShares: []uint64{1, 0, 1}}, {Height: 20, PayShares: []uint64{1, 1, 1}}}
	v.SealingBreadthFrom = A
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
	c := newForkChain()
	g := genesisHeader()
	g.Time = 1_000_000
	c.setCanonical(g)
	e.anchor(c)
	// The menu's read: coordinate 1 sealable below A, not from A, again
	// from 20; coordinates 0 and 2 throughout.
	if !m.SealableAt(1, A-1) || m.SealableAt(1, A) || m.SealableAt(1, 19) || !m.SealableAt(1, 20) || !m.SealableAt(0, A) || !m.SealableAt(2, A) {
		t.Fatalf("sealable: %v %v %v %v %v %v", m.SealableAt(1, A-1), m.SealableAt(1, A), m.SealableAt(1, 19), m.SealableAt(1, 20), m.SealableAt(0, A), m.SealableAt(2, A))
	}
	// Coordinate 0 seals to A - 2, then a coordinate-1 block at A - 1
	// (valid), then coordinate 0 to A - 1's child.
	parent := g
	for n := 1; n < A-1; n++ {
		// Coordinate 2's one block at height 2, coordinate 0's the rest.
		coord := uint8(0)
		if n == 2 {
			coord = 2
		}
		h := taggedChild(t, e, c, parent, coord, 80)
		c.setCanonical(h)
		parent = h
	}
	h1 := taggedChild(t, e, c, parent, 1, 80)
	if err := e.verifyVectorHeader(c, h1, parent); err != nil {
		t.Fatalf("a coordinate-1 block at %d (below the height) refused: %v", A-1, err)
	}
	c.setCanonical(h1)
	parent = h1
	// At A: coordinate 1's block is invalid, coordinate 0's valid.
	bad := taggedChild(t, e, c, parent, 1, 80)
	if err := e.verifyVectorHeader(c, bad, parent); !errors.Is(err, errVectorCoordUnpaid) {
		t.Fatalf("a coordinate-1 block at %d admitted: %v", A, err)
	}
	good := taggedChild(t, e, c, parent, 0, 80)
	if err := e.verifyVectorHeader(c, good, parent); err != nil {
		t.Fatalf("a coordinate-0 block at %d refused: %v", A, err)
	}
	// Assembly: a node on coordinate 1 makes no template at A, one at 20.
	if err := e.SetCoordinate(1); err != nil {
		t.Fatal(err)
	}
	tpl := &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).Add(parent.Number, big.NewInt(1)), Time: parent.Time + 80}
	if err := e.prepareVector(c, tpl, parent); !errors.Is(err, errVectorCoordUnpaid) {
		t.Fatalf("a template on the unpaid coordinate at %d: %v", A, err)
	}
	c.setCanonical(good)
	parent = good
	for n := A + 1; n < 20; n++ {
		h := taggedChild(t, e, c, parent, 0, 80)
		c.setCanonical(h)
		parent = h
	}
	tpl = &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).Add(parent.Number, big.NewInt(1)), Time: parent.Time + 80}
	if err := e.prepareVector(c, tpl, parent); err != nil {
		t.Fatalf("a template on the restored coordinate at 20: %v", err)
	}
	if cc, ok := tpl.CoordTag(); !ok || cc != 1 {
		t.Fatalf("the template's coordinate %d %v", cc, ok)
	}
	// The fork vector under the minimum rule: coordinate 1 holds one
	// block (at A - 1, the least scaled work), coordinate 2 one (at 2),
	// coordinate 0 the rest. At 19 coordinate 1 is outside the breadth,
	// so the minimum is coordinate 2's; at 20 (restored) it is coordinate
	// 1's.
	coord0, _ := m.Coordinate(0)
	coord1, _ := m.Coordinate(1)
	coord2, _ := m.Coordinate(2)
	den := new(big.Int).Mul(coord0.DRef, new(big.Int).Mul(coord1.DRef, coord2.DRef))
	s19, _ := e.vectorStateOf(c, parent)
	v1 := new(big.Int).Mul(s19.work[1], new(big.Int).Div(den, coord1.DRef))
	v2 := new(big.Int).Mul(s19.work[2], new(big.Int).Div(den, coord2.DRef))
	if v1.Cmp(v2) >= 0 {
		t.Fatalf("the cell's shape: coordinate 1's %v is not under coordinate 2's %v", v1, v2)
	}
	w19, _, err := e.vectorWeight(c, parent)
	if err != nil {
		t.Fatal(err)
	}
	if w19.Cmp(v2) != 0 {
		t.Fatalf("the weight at 19 %v, want coordinate 2's %v (coordinate 1 outside the breadth)", w19, v2)
	}
	h20 := taggedChild(t, e, c, parent, 0, 80)
	c.setCanonical(h20)
	w20, _, err := e.vectorWeight(c, h20)
	if err != nil {
		t.Fatal(err)
	}
	if w20.Cmp(v1) != 0 {
		t.Fatalf("the weight at 20 %v, want coordinate 1's %v (the breadth whole again)", w20, v1)
	}
	// The admission: set once, above the head.
	stored := vectorConfig()
	given := *stored
	given.SealingBreadthFrom = 151
	if _, err := Reregistration(stored, &given, 150); err != nil {
		t.Fatalf("sealingBreadthFrom above the head refused: %v", err)
	}
	if _, err := Reregistration(stored, &given, 151); err == nil {
		t.Fatal("sealingBreadthFrom at the head admitted")
	}
	stored.SealingBreadthFrom = 100
	given.SealingBreadthFrom = 200
	if _, err := Reregistration(stored, &given, 150); err == nil {
		t.Fatal("a changed sealingBreadthFrom admitted")
	}
}

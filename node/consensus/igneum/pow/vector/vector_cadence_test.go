package vector

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestCadenceActive pins REVISION-CADENCE.md cut A: a retired form is no
// coordinate from its height; a form past maxLiveBlocks with no successor
// named is invalid, with one named it stays.
func TestCadenceActive(t *testing.T) {
	one := uint64(1)
	v := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", MaxLiveBlocks: 1000, Coordinates: []ctypes.IgneumCoordinate{
		{Name: "a", Algorithm: null.Name, Target: 80, DRef: 100, Activation: 0, RetiredFrom: 500},
		{Name: "b", Algorithm: null.Name, Target: 80, DRef: 100, Activation: 100, Successor: &one},
		{Name: "c", Algorithm: null.Name, Target: 80, DRef: 100, Activation: 100},
	}}
	if err := Validate(v); err == nil {
		t.Fatal("a self successor admitted")
	}
	zero := uint64(0)
	v.Coordinates[1].Successor = &zero
	if err := Validate(v); err != nil {
		t.Fatal(err)
	}
	m, err := New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if !m.ActiveAt(0, 499) || m.ActiveAt(0, 500) {
		t.Fatal("retiredFrom")
	}
	if !m.ActiveAt(1, 5000) {
		t.Fatal("a successor named: live past maxLive")
	}
	if !m.ActiveAt(2, 1099) || m.ActiveAt(2, 1100) {
		t.Fatal("no successor: invalid from activation + maxLive")
	}
}

// TestNinthCoordinate: the bound (thirty-two since cut B) admits vectorwork8's
// ninth and seventeenth coordinates and refuses a thirty-third.
func TestNinthCoordinate(t *testing.T) {
	mk := func(n int) *ctypes.IgneumVector {
		v := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage"}
		for i := 0; i < n; i++ {
			v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: string(rune('a' + i)), Algorithm: null.Name, Target: 80, DRef: 100})
		}
		return v
	}
	if err := Validate(mk(9)); err != nil {
		t.Fatalf("nine coordinates: %v", err)
	}
	if err := Validate(mk(32)); err != nil {
		t.Fatalf("thirty-two coordinates: %v", err)
	}
	if err := Validate(mk(33)); err == nil {
		t.Fatal("thirty-three coordinates admitted")
	}
}

// TestLanesChanges: the breadth's lanes change by height, the last entry
// at or under the height in force, the shape checked.
func TestLanesChanges(t *testing.T) {
	v := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", BalancedLanes: []uint8{0, 1}, BalancedLanesFrom: 10,
		BalancedLanesChanges: []ctypes.IgneumLanesChange{{Height: 100, Lanes: []uint8{0, 1, 2}}, {Height: 200, Lanes: []uint8{0, 2}}},
		Coordinates: []ctypes.IgneumCoordinate{
			{Name: "a", Algorithm: null.Name, Target: 80, DRef: 100},
			{Name: "b", Algorithm: null.Name, Target: 80, DRef: 100},
			{Name: "c", Algorithm: null.Name, Target: 80, DRef: 100},
		}}
	if err := Validate(v); err != nil {
		t.Fatal(err)
	}
	m, err := New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if l := m.BalancedLanesAt(99); len(l) != 2 || l[1] != 1 {
		t.Fatalf("before the first change: %v", l)
	}
	if l := m.BalancedLanesAt(150); len(l) != 3 {
		t.Fatalf("the first change: %v", l)
	}
	if l := m.BalancedLanesAt(200); len(l) != 2 || l[1] != 2 {
		t.Fatalf("the second change: %v", l)
	}
	v.BalancedLanesChanges = append(v.BalancedLanesChanges, ctypes.IgneumLanesChange{Height: 150, Lanes: []uint8{0}})
	if err := Validate(v); err == nil {
		t.Fatal("an out-of-order entry admitted")
	}
	v.BalancedLanesChanges = []ctypes.IgneumLanesChange{{Height: 100, Lanes: []uint8{0, 3}}}
	if err := Validate(v); err == nil {
		t.Fatal("a lane out of range admitted")
	}
}

// TestPayChangesShorterThanCoordinates: an entry written before a
// coordinate was added reads with the missing weights 0.
func TestPayChangesShorterThanCoordinates(t *testing.T) {
	v := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", PayShares: []uint64{1, 2, 3},
		PayChanges: []ctypes.IgneumPayChange{{Height: 100, PayShares: []uint64{2, 2}}, {Height: 200, PayShares: []uint64{1, 1, 5}}},
		Coordinates: []ctypes.IgneumCoordinate{
			{Name: "a", Algorithm: null.Name, Target: 80, DRef: 100},
			{Name: "b", Algorithm: null.Name, Target: 80, DRef: 100},
			{Name: "c", Algorithm: null.Name, Target: 80, DRef: 100, Activation: 150},
		}}
	if err := Validate(v); err != nil {
		t.Fatal(err)
	}
	m, err := New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if w := m.PaySharesAt(150); len(w) != 3 || w[2] != 0 || w[0] != 2 {
		t.Fatalf("the short entry padded: %v", w)
	}
	if w := m.PaySharesAt(200); len(w) != 3 || w[2] != 5 {
		t.Fatalf("the full entry: %v", w)
	}
	v.PayChanges[0].PayShares = []uint64{1, 1, 1, 1}
	if err := Validate(v); err == nil {
		t.Fatal("an entry longer than the coordinates admitted")
	}
}

// TestRevealOf: a coordinate's own programme reveal depth by its algorithm,
// 0 for one that names none.
func TestRevealOf(t *testing.T) {
	v := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Coordinates: []ctypes.IgneumCoordinate{
		{Name: "a", Algorithm: null.Name, Target: 80, DRef: 100},
		{Name: "b", Algorithm: null.Name, Target: 80, DRef: 100, RevealBlocks: 64},
	}}
	m, err := New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if r := m.RevealOf(m.ByCoordinate(1)); r != 64 {
		t.Fatalf("coordinate 1's reveal %d", r)
	}
	// The null relation is one object for every coordinate, so the deepest
	// reveal among the sharers is the answer for coordinate 0 too; a
	// document of its own (igneumhash) answers its own coordinate's.
	if r := m.RevealOf(m.ByCoordinate(0)); r != 64 {
		t.Fatalf("coordinate 0's reveal %d (the shared null object)", r)
	}
}

// TestDocumentRevealOf (F-730): a coordinate's registered reveal depth
// answers for EVERY instance of its document, where RevealOf (the form
// before the landing check, kept for the blocks sealed under it) answers for the
// base instance alone: an in-place document change's form resolves 0
// through RevealOf (the engine then fell back to the relation's default,
// the public test network from 13,800) and the registered depth through
// DocumentRevealOf; a keyed document's twin and a change's twin the same;
// a coordinate that names no reveal answers 0 both ways; an object of no
// coordinate answers 0.
func TestDocumentRevealOf(t *testing.T) {
	var made []*docStub
	v := changedVector(change(100, "one", 200))
	v.Coordinates[1].RevealBlocks = 64
	m, err := New(v, docBuild(&made))
	if err != nil {
		t.Fatal(err)
	}
	base, changed := m.ByCoordinateAt(1, 50), m.ByCoordinateAt(1, 100)
	if base == changed {
		t.Fatal("the change built no instance of its own")
	}
	if r := m.RevealOf(base); r != 64 {
		t.Fatalf("the base instance through RevealOf: %d", r)
	}
	if r := m.RevealOf(changed); r != 0 {
		t.Fatalf("the change's instance through RevealOf: %d (the form before the landing check answers the base alone)", r)
	}
	if r := m.DocumentRevealOf(base); r != 64 {
		t.Fatalf("the base instance through DocumentRevealOf: %d", r)
	}
	if r := m.DocumentRevealOf(changed); r != 64 {
		t.Fatalf("the change's instance through DocumentRevealOf: %d", r)
	}
	// A coordinate that names no reveal answers 0 both ways (the chain's
	// applies), and an object of no coordinate answers 0.
	if r := m.DocumentRevealOf(m.ByCoordinateAt(0, 100)); r != 0 {
		t.Fatalf("a coordinate without a reveal: %d", r)
	}
	if r := m.DocumentRevealOf(&docStub{}); r != 0 {
		t.Fatalf("a stranger: %d", r)
	}
	if r := m.DocumentRevealOf(nil); r != 0 {
		t.Fatalf("nil: %d", r)
	}

	// A keyed document's twin, and a change's twin, are instances too.
	build := func(name string, params []byte) (pow.Algorithm, error) {
		b, err := pow.NewDev(null.Name, nil)
		if err != nil {
			return nil, err
		}
		return &twinStub{Algorithm: b, doc: append([]byte(nil), params...)}, nil
	}
	keyedDoc := json.RawMessage(`{"version":8,"dataset_mib":16,"features":5152,"keyed_rounds":2}`)
	keyedDoc2 := json.RawMessage(`{"version":8,"dataset_mib":32,"features":5152,"keyed_rounds":2}`)
	kv := &ctypes.IgneumVector{
		Coordinates: []ctypes.IgneumCoordinate{
			{Name: "plain", Algorithm: "stub", AlgorithmParams: json.RawMessage(`{"version":8,"dataset_mib":16,"features":4128}`), Target: 80, DRef: 1000, DMin: 10, LagCap: DerivedLagCap(60, 1000, 10)},
			{Name: "keyed", Algorithm: "stub", AlgorithmParams: keyedDoc, Target: 80, DRef: 2000, DMin: 10, LagCap: DerivedLagCap(60, 2000, 10), RevealBlocks: 64,
				DocumentChanges: []ctypes.IgneumDocumentChange{{Height: 50, AlgorithmParams: keyedDoc2, DRef: 2000, DMin: 10, LagCap: DerivedLagCap(60, 2000, 10)}}},
		},
		HalfLife: 60, DtMax: 60,
	}
	km, err := New(kv, build)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		a    pow.Algorithm
	}{
		{"the base", km.ByCoordinateAt(1, 10)},
		{"the base's twin", km.UnkeyedAlgorithmAt(1, 10)},
		{"the change", km.ByCoordinateAt(1, 50)},
		{"the change's twin", km.UnkeyedAlgorithmAt(1, 50)},
	} {
		if c.a == nil {
			t.Fatalf("%s: no instance", c.name)
		}
		if r := km.DocumentRevealOf(c.a); r != 64 {
			t.Fatalf("%s through DocumentRevealOf: %d", c.name, r)
		}
	}
	if r := km.RevealOf(km.UnkeyedAlgorithmAt(1, 10)); r != 0 {
		t.Fatalf("the twin through RevealOf: %d (the form before answers the base alone)", r)
	}
	if r := km.DocumentRevealOf(km.ByCoordinateAt(0, 10)); r != 0 {
		t.Fatalf("the plain coordinate names no reveal: %d", r)
	}
}

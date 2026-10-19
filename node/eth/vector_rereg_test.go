package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestVectorReregistration pins F-113's admission: a persisted chain takes
// a document that keeps its stored coordinates and adds one activating
// above the head, and refuses a changed entry, a dropped coordinate or an
// activation at or below the head.
func TestVectorReregistration(t *testing.T) {
	c := func(name string, d uint64, act uint64) ctypes.IgneumCoordinate {
		return ctypes.IgneumCoordinate{Name: name, Algorithm: "igneumhash", Target: 80, DRef: d, Activation: act}
	}
	stored := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("b", 200, 0)}}
	if _, err := vectorReregistration(nil, stored, 10); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if _, err := vectorReregistration(stored, stored, 10); err != nil {
		t.Fatalf("the same document: %v", err)
	}
	add := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("b", 200, 0), c("c", 300, 382)}}
	if rw, err := vectorReregistration(stored, add, 232); err != nil || rw != 0 {
		t.Fatalf("an activation above the head: rewind %d, %v", rw, err)
	}
	// A node restarted at or past the activation rewinds to the block
	// before it and takes the document (the node team's second hand).
	if rw, err := vectorReregistration(stored, add, 382); err != nil || rw != 381 {
		t.Fatalf("an activation at the head: rewind %d, %v", rw, err)
	}
	if rw, err := vectorReregistration(stored, add, 405); err != nil || rw != 381 {
		t.Fatalf("an activation below the head: rewind %d, %v", rw, err)
	}
	noact := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("b", 200, 0), c("c", 300, 0)}}
	if _, err := vectorReregistration(stored, noact, 10); err == nil {
		t.Fatal("an added coordinate without an activation admitted")
	}
	changed := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Coordinates: []ctypes.IgneumCoordinate{c("a", 101, 0), c("b", 200, 0)}}
	if _, err := vectorReregistration(stored, changed, 10); err == nil {
		t.Fatal("a changed entry admitted")
	}
	dropped := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0)}}
	if _, err := vectorReregistration(stored, dropped, 10); err == nil {
		t.Fatal("a dropped coordinate admitted")
	}
	rule := &ctypes.IgneumVector{HalfLife: 900, Rule: "geometric", Coordinates: stored.Coordinates}
	if _, err := vectorReregistration(stored, rule, 10); err == nil {
		t.Fatal("a changed rule admitted")
	}
}

// TestVectorReregistrationPayChanges: a pay weight change goes only by an
// appended payChanges entry above the head; payShares itself is fixed.
func TestVectorReregistrationPayChanges(t *testing.T) {
	c := func(name string, d uint64) ctypes.IgneumCoordinate {
		return ctypes.IgneumCoordinate{Name: name, Algorithm: "igneumhash", Target: 80, DRef: d}
	}
	stored := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", PayShares: []uint64{1, 3}, Coordinates: []ctypes.IgneumCoordinate{c("a", 100), c("b", 200)}}
	changed := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", PayShares: []uint64{1, 2}, Coordinates: stored.Coordinates}
	if _, err := vectorReregistration(stored, changed, 10); err == nil {
		t.Fatal("a changed payShares admitted")
	}
	gated := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", PayShares: []uint64{1, 3}, Coordinates: stored.Coordinates, PayChanges: []ctypes.IgneumPayChange{{Height: 500, PayShares: []uint64{1, 2}}}}
	if rw, err := vectorReregistration(stored, gated, 10); err != nil || rw != 0 {
		t.Fatalf("a payChanges entry above the head: rewind %d, %v", rw, err)
	}
	if _, err := vectorReregistration(stored, gated, 500); err == nil {
		t.Fatal("a payChanges entry at the head admitted")
	}
}

// TestVectorReregistrationAddedCoordinateShare: an added coordinate appends
// its weight to payShares; the stored weights stay.
func TestVectorReregistrationAddedCoordinateShare(t *testing.T) {
	c := func(name string, d uint64, act uint64) ctypes.IgneumCoordinate {
		return ctypes.IgneumCoordinate{Name: name, Algorithm: "igneumhash", Target: 80, DRef: d, Activation: act}
	}
	stored := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", PayShares: []uint64{1, 3}, Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("b", 200, 0)}}
	added := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", PayShares: []uint64{1, 3, 1}, Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("b", 200, 0), c("c", 300, 50)}}
	if rw, err := vectorReregistration(stored, added, 10); err != nil || rw != 0 {
		t.Fatalf("an added coordinate's weight: rewind %d, %v", rw, err)
	}
	bad := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", PayShares: []uint64{1, 2, 1}, Coordinates: added.Coordinates}
	if _, err := vectorReregistration(stored, bad, 10); err == nil {
		t.Fatal("a changed stored weight admitted")
	}
}

// TestVectorReregistrationFutureActivationMoves: a stored coordinate whose
// activation is above the head may move its activation (still above the
// head); one already active may not.
func TestVectorReregistrationFutureActivationMoves(t *testing.T) {
	c := func(name string, d uint64, act uint64) ctypes.IgneumCoordinate {
		return ctypes.IgneumCoordinate{Name: name, Algorithm: "igneumhash", Target: 80, DRef: d, Activation: act}
	}
	stored := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("t", 300, 953)}}
	moved := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("t", 300, 920)}}
	if _, err := vectorReregistration(stored, moved, 909); err != nil {
		t.Fatalf("a future activation moved above the head: %v", err)
	}
	if _, err := vectorReregistration(stored, moved, 930); err == nil {
		t.Fatal("an activation moved below the head admitted")
	}
	active := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Coordinates: []ctypes.IgneumCoordinate{c("a", 101, 0), c("t", 300, 953)}}
	if _, err := vectorReregistration(stored, active, 909); err == nil {
		t.Fatal("an active coordinate's change admitted")
	}
}

// TestVectorReregistrationCadence: a retirement and a successor set once
// whatever the local head (a late restart rewinds to retiredFrom - 1), the
// constants immutable; the notice rule is cut B's, against the registry
// transaction's inclusion height.
func TestVectorReregistrationCadence(t *testing.T) {
	c := func(name string, d uint64, act uint64) ctypes.IgneumCoordinate {
		return ctypes.IgneumCoordinate{Name: name, Algorithm: "igneumhash", Target: 80, DRef: d, Activation: act}
	}
	stored := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", NoticeBlocks: 100, Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("b", 200, 0)}}
	ret := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", NoticeBlocks: 100, Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("b", 200, 0)}}
	ret.Coordinates[0].RetiredFrom = 150
	if rw, err := vectorReregistration(stored, ret, 10); err != nil || rw != 0 {
		t.Fatalf("a retirement above the head: rewind %d, %v", rw, err)
	}
	if rw, err := vectorReregistration(stored, ret, 160); err != nil || rw != 149 {
		t.Fatalf("a late restart past the retirement: rewind %d, %v", rw, err)
	}
	ret2 := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", NoticeBlocks: 100, Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("b", 200, 0)}}
	ret2.Coordinates[0].RetiredFrom = 170
	if _, err := vectorReregistration(ret, ret2, 10); err == nil {
		t.Fatal("a second retirement height admitted")
	}
	one := uint64(1)
	ret3 := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", NoticeBlocks: 100, Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("b", 200, 0)}}
	ret3.Coordinates[0].RetiredFrom = 150
	ret3.Coordinates[0].Successor = &one
	if _, err := vectorReregistration(ret, ret3, 200); err != nil {
		t.Fatalf("a successor named late: %v", err)
	}
	add := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", NoticeBlocks: 100, Coordinates: []ctypes.IgneumCoordinate{c("a", 100, 0), c("b", 200, 0), c("c", 300, 150)}}
	if rw, err := vectorReregistration(stored, add, 160); err != nil || rw != 149 {
		t.Fatalf("a late restart past an added activation: rewind %d, %v", rw, err)
	}
	cst := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", NoticeBlocks: 200, Coordinates: stored.Coordinates}
	if _, err := vectorReregistration(stored, cst, 10); err == nil {
		t.Fatal("a changed noticeBlocks admitted")
	}
}

// TestVectorReregistrationLanesChanges: the stored entries immutable, an
// appended entry above the head.
func TestVectorReregistrationLanesChanges(t *testing.T) {
	c := func(name string, d uint64) ctypes.IgneumCoordinate {
		return ctypes.IgneumCoordinate{Name: name, Algorithm: "igneumhash", Target: 80, DRef: d}
	}
	stored := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Coordinates: []ctypes.IgneumCoordinate{c("a", 100), c("b", 200)},
		BalancedLanesChanges: []ctypes.IgneumLanesChange{{Height: 50, Lanes: []uint8{0}}}}
	given := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", Coordinates: []ctypes.IgneumCoordinate{c("a", 100), c("b", 200)},
		BalancedLanesChanges: []ctypes.IgneumLanesChange{{Height: 50, Lanes: []uint8{0}}, {Height: 120, Lanes: []uint8{0, 1}}}}
	if _, err := vectorReregistration(stored, given, 100); err != nil {
		t.Fatalf("an appended entry above the head: %v", err)
	}
	if _, err := vectorReregistration(stored, given, 120); err == nil {
		t.Fatal("an appended entry at the head admitted")
	}
	given.BalancedLanesChanges[0].Lanes = []uint8{1}
	if _, err := vectorReregistration(stored, given, 100); err == nil {
		t.Fatal("a changed stored entry admitted")
	}
}

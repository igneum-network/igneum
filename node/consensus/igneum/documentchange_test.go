package igneum

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

func documentChangeVector(changes ...ctypes.IgneumDocumentChange) *ctypes.IgneumVector {
	return &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", NoticeBlocks: 64, MaxLiveBlocks: 100000, Coordinates: []ctypes.IgneumCoordinate{
		{Name: "a", Algorithm: null.Name, Target: 80, DRef: 100},
		{Name: "b", Algorithm: null.Name, AlgorithmParams: json.RawMessage(`{"form":"base"}`), Target: 80, DRef: 100, DocumentChanges: changes},
	}}
}

// TestReregistrationDocumentChangesAppendOnly (LANE-MODIFICATION.md section
// 1): a stored coordinate may take in-place document changes appended
// above the head; the stored entries are immutable, and an entry at or
// under the head is refused.
func TestReregistrationDocumentChangesAppendOnly(t *testing.T) {
	one := ctypes.IgneumDocumentChange{Height: 1000, AlgorithmParams: json.RawMessage(`{"form":"one"}`), DRef: 200}
	two := ctypes.IgneumDocumentChange{Height: 2000, AlgorithmParams: json.RawMessage(`{"form":"two"}`), DRef: 400}
	stored := documentChangeVector()
	if _, err := Reregistration(stored, documentChangeVector(one), 500); err != nil {
		t.Fatalf("an appended change above the head refused: %v", err)
	}
	if _, err := Reregistration(stored, documentChangeVector(one), 1000); err == nil || !strings.Contains(err.Error(), "at or under the head") {
		t.Fatalf("a change at the head admitted (%v)", err)
	}
	stored1 := documentChangeVector(one)
	if _, err := Reregistration(stored1, documentChangeVector(one, two), 1500); err != nil {
		t.Fatalf("a second change appended above the head refused: %v", err)
	}
	altered := one
	altered.DRef = 201
	if _, err := Reregistration(stored1, documentChangeVector(altered), 1500); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("a stored change altered was admitted (%v)", err)
	}
	if _, err := Reregistration(stored1, documentChangeVector(), 1500); err == nil || !strings.Contains(err.Error(), "drops") {
		t.Fatalf("a stored change dropped was admitted (%v)", err)
	}
	// A change changes nothing else of the coordinate: a weight or a
	// name beside it is refused as before.
	given := documentChangeVector(one, two)
	given.Coordinates[1].DRef = 101
	if _, err := Reregistration(stored1, given, 1500); err == nil {
		t.Fatal("the base reference changed beside a document change was admitted")
	}
}

// TestLandingNoticeDocumentChange: a landing's appended change takes effect
// at least the notice above the landing and never under
// DocumentChangeNotice blocks.
func TestLandingNoticeDocumentChange(t *testing.T) {
	current := documentChangeVector()
	at := func(h uint64) *ctypes.IgneumVector {
		return documentChangeVector(ctypes.IgneumDocumentChange{Height: h, AlgorithmParams: json.RawMessage(`{"form":"one"}`), DRef: 200})
	}
	landing := uint64(5000)
	if err := landingNotice(current, at(landing+DocumentChangeNotice+1), landing); err != nil {
		t.Fatalf("a change at the landing plus the notice refused: %v", err)
	}
	if err := landingNotice(current, at(landing+DocumentChangeNotice), landing); err == nil {
		t.Fatal("a change one under the landing plus the notice admitted")
	}
	if err := landingNotice(current, at(landing+65), landing); err == nil {
		t.Fatal("a change at the registration's 64-block notice admitted under the document change's floor of 100")
	}
	// A stored change under the landing is not re-judged.
	stored := at(100)
	if err := landingNotice(stored, at(100), landing); err != nil {
		t.Fatalf("a stored change re-judged at a later landing: %v", err)
	}
}

// TestVoidEntryAndTheRetirementLock (LANE-MODIFICATION.md section 1b): a
// pending change locks a retirement at or under its height; a void entry
// (append-only, naming a pending stored change, landing before it) frees
// the retirement; a void of nothing, of a change already in force or
// landing at or past the change is refused.
func TestVoidEntryAndTheRetirementLock(t *testing.T) {
	one := ctypes.IgneumDocumentChange{Height: 1000, AlgorithmParams: json.RawMessage(`{"form":"one"}`), DRef: 200}
	void := ctypes.IgneumDocumentChange{Height: 1000, Void: true}
	stored := documentChangeVector(one)
	// The lock: a retirement at or under the pending change.
	retire := func(changes []ctypes.IgneumDocumentChange, from uint64) *ctypes.IgneumVector {
		v := documentChangeVector(changes...)
		v.Coordinates[1].RetiredFrom = from
		return v
	}
	if _, err := Reregistration(stored, retire([]ctypes.IgneumDocumentChange{one}, 900), 500); err == nil || !strings.Contains(err.Error(), "locks a retirement") {
		t.Fatalf("a retirement under a pending change admitted (%v)", err)
	}
	if _, err := Reregistration(stored, retire([]ctypes.IgneumDocumentChange{one}, 1000), 500); err == nil {
		t.Fatal("a retirement at the pending change's height admitted")
	}
	// A retirement above the change admits; a retirement under a voided
	// change admits.
	v := retire([]ctypes.IgneumDocumentChange{one}, 1200)
	if _, err := Reregistration(stored, v, 500); err != nil {
		t.Fatalf("a retirement above the pending change refused: %v", err)
	}
	if _, err := Reregistration(stored, retire([]ctypes.IgneumDocumentChange{one, void}, 900), 500); err != nil {
		t.Fatalf("a retirement under a voided change refused: %v", err)
	}
	// The void itself: admitted while the change is pending, refused once
	// the change is in force (the head at or past it) or when it names
	// nothing.
	if _, err := Reregistration(stored, documentChangeVector(one, void), 500); err != nil {
		t.Fatalf("a void of a pending change refused: %v", err)
	}
	if _, err := Reregistration(stored, documentChangeVector(one, void), 1000); err == nil {
		t.Fatal("a void of a change in force admitted")
	}
	if _, err := Reregistration(documentChangeVector(), documentChangeVector(void), 500); err == nil {
		t.Fatal("a void of nothing admitted")
	}
	storedVoid := documentChangeVector(one, void)
	if _, err := Reregistration(storedVoid, documentChangeVector(one, void, void), 500); err == nil {
		t.Fatal("a second void of the same change admitted")
	}
	if _, err := Reregistration(storedVoid, documentChangeVector(one), 500); err == nil {
		t.Fatal("a stored void dropped was admitted")
	}
	// The landing: a void lands before the change it names.
	if err := landingNotice(stored, documentChangeVector(one, void), 999); err != nil {
		t.Fatalf("a void landing before the change refused: %v", err)
	}
	if err := landingNotice(stored, documentChangeVector(one, void), 1000); err == nil {
		t.Fatal("a void landing at the change's height admitted")
	}
}

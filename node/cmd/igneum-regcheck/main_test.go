package main

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestPreflightRefusesAnUnsealedPaidSet (F-221, 16 October 2026): the
// execute-36 succession paid 25, 26 and 27 from A=8640 while every running
// sealer was on 22, 23 and 24 and none was armed to move at A-1, so no node
// could build 8640 for 8 minutes 45 seconds; the pre-flight reads that
// document as refused, and admits it once the successors are named armed
// or one of them is already sealing.
func TestPreflightRefusesAnUnsealedPaidSet(t *testing.T) {
	coords := make([]ctypes.IgneumCoordinate, 28)
	given := &ctypes.IgneumVector{Coordinates: coords, PayShares: make([]uint64, 28), PayChanges: []ctypes.IgneumPayChange{
		{Height: 3622, PayShares: pay(28, 22, 23, 24)},
		{Height: 8640, PayShares: pay(28, 25, 26, 27)},
	}}
	current := &ctypes.IgneumVector{Coordinates: coords[:25], PayChanges: given.PayChanges[:1]}
	heights := newPayHeights(current, given, 8489)
	if len(heights) != 1 || heights[0] != 8640 {
		t.Fatalf("the new pay heights above the landing 8489: %v, want [8640]", heights)
	}
	if set := paidAt(given, 8640); strings.Join(strs(set), ",") != "25,26,27" {
		t.Fatalf("the paid set from 8640: %v, want 25,26,27", set)
	}
	if set := paidAt(given, 8639); strings.Join(strs(set), ",") != "22,23,24" {
		t.Fatalf("the paid set at 8639: %v, want 22,23,24", set)
	}
	sealed := map[uint8]int{22: 20, 23: 24, 24: 20} // the last 64 blocks on the running lanes
	if ok, line := judge(8640, paidAt(given, 8640), sealed, nil, 64); ok {
		t.Fatalf("the stall's document admitted: %s", line)
	}
	if ok, line := judge(8640, paidAt(given, 8640), sealed, map[uint8]bool{25: true, 26: true, 27: true}, 64); !ok || !strings.Contains(line, "armed for A-1: 25, 26, 27") {
		t.Fatalf("the armed successors refused: %s", line)
	}
	sealed[27] = 1
	if ok, line := judge(8640, paidAt(given, 8640), sealed, nil, 64); !ok || !strings.Contains(line, "27 (1 of the last 64 blocks)") {
		t.Fatalf("a successor already sealing refused: %s", line)
	}
	// A document that adds no pay change (an in-place document change)
	// reads no new height and the pre-flight has nothing to judge.
	if h := newPayHeights(given, given, 9000); len(h) != 0 {
		t.Fatalf("an unchanged pay list read new heights: %v", h)
	}
}

// TestAddsDocumentChanges (LANE-MODIFICATION.md section 1a's roll gate): a
// document carrying a coordinate's in-place document change the
// registration in force does not is read as such, so the pre-flight asks
// every node of record for the document-change entry before the send.
func TestAddsDocumentChanges(t *testing.T) {
	base := &ctypes.IgneumVector{Coordinates: []ctypes.IgneumCoordinate{{Name: "a"}, {Name: "b"}}}
	if addsDocumentChanges(base, base) {
		t.Fatal("a document without changes read as adding one")
	}
	one := &ctypes.IgneumVector{Coordinates: []ctypes.IgneumCoordinate{{Name: "a"}, {Name: "b", DocumentChanges: []ctypes.IgneumDocumentChange{{Height: 100}}}}}
	if !addsDocumentChanges(base, one) {
		t.Fatal("a document adding a change read as adding none")
	}
	if addsDocumentChanges(one, one) {
		t.Fatal("a stored change re-read as added")
	}
	two := &ctypes.IgneumVector{Coordinates: []ctypes.IgneumCoordinate{{Name: "a"}, {Name: "b", DocumentChanges: []ctypes.IgneumDocumentChange{{Height: 100}, {Height: 200}}}}}
	if !addsDocumentChanges(one, two) {
		t.Fatal("a second change appended read as adding none")
	}
	added := &ctypes.IgneumVector{Coordinates: []ctypes.IgneumCoordinate{{Name: "a"}, {Name: "b"}, {Name: "c", DocumentChanges: []ctypes.IgneumDocumentChange{{Height: 300}}}}}
	if !addsDocumentChanges(base, added) {
		t.Fatal("a change on an added coordinate read as adding none")
	}
}

// TestReferenceBoundAndReleasedForms (the security review's cuts 4 and the
// kernel-readiness rule): an appended change's reference is admitted within
// a factor of ReferenceBoundFactor of the lane's running difficulty both
// ways, and a document outside the released workers' list is refused.
func TestReferenceBoundAndReleasedForms(t *testing.T) {
	running := big.NewInt(1_000_000)
	for _, c := range []struct {
		dRef uint64
		ok   bool
	}{{1_000_000, true}, {4_000_000, true}, {250_000, true}, {4_000_001, false}, {249_999, false}} {
		if ok, _ := referenceBound(c.dRef, running); ok != c.ok {
			t.Fatalf("dRef %d against %s: %v, want %v", c.dRef, running, ok, c.ok)
		}
	}
	if ok, _ := referenceBound(1, nil); ok {
		t.Fatal("a reference judged with no running difficulty")
	}
	if !releasedForm("igneumhash", []byte(`{"version": 18, "rounds": 1024}`)) {
		t.Fatal("the raster form (18) read as unreleased")
	}
	if releasedForm("igneumhash", []byte(`{"version": 17, "rounds": 512}`)) {
		t.Fatal("the texture form (17, no released kernel) read as released")
	}
	if releasedForm("igneumhash", []byte(`{"rounds": 512}`)) {
		t.Fatal("a document without a version read as released")
	}
	if !releasedForm("igneum-graph", []byte(`{"chain_id": 7358}`)) {
		t.Fatal("the graph coordinate read as unreleased")
	}
	if releasedForm("igneum-play", []byte(`{}`)) {
		t.Fatal("an algorithm outside the list read as released")
	}
}

func pay(n int, paid ...int) []uint64 {
	w := make([]uint64, n)
	for _, c := range paid {
		w[c] = 1
	}
	return w
}

func strs(set []uint8) []string {
	var out []string
	for _, c := range set {
		out = append(out, strings.TrimSpace(strings.Repeat(" ", 0)+itoa(int(c))))
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestTwinBuildsReadsTheDerivedTwin (F-580): an unkeyed document has no twin
// and passes; a keyed document's derived twin is built through the engine's
// own constructor, so a twin the engine would refuse is refused here, before
// the signer signs, never at the landing.
func TestTwinBuildsReadsTheDerivedTwin(t *testing.T) {
	if msg := twinBuilds("igneumhash", []byte(`{"version":18,"dataset_mib":1,"rounds":1}`)); msg != "" {
		t.Fatalf("an unkeyed document has no twin to build: %q", msg)
	}
	if msg := twinBuilds("igneumhash", []byte(`{"features": 1024, "keyed_rounds": 4`)); msg == "" {
		t.Fatalf("a twin that cannot be derived (the document unreadable) must be refused")
	}
	// The keyed bit alone names a twin (features without bit 10, no
	// keyed_rounds); the algorithm's own constructor decides the rest.
	msg := twinBuilds("no-such-algorithm", []byte(`{"version":18,"features":1024,"keyed_rounds":4}`))
	if msg == "" {
		t.Fatalf("a twin the engine cannot build must be refused")
	}
	if !strings.Contains(msg, "unkeyed twin") {
		t.Fatalf("the refusal names the twin: %q", msg)
	}
}

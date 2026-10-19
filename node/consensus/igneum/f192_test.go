package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestF192MinPayBound (16 October 2026): the minimum pay rule's y bound is
// A256_c unscaled and A256_c x window / 256 scaled (node 0.10.18's form),
// and the engine derives unscaled at and under the chain's gate, scaled
// above it, scaled everywhere without a gate.
func TestF192MinPayBound(t *testing.T) {
	a := big.NewInt(256_000)
	if got := minPayBound(a, 64, false); got.Cmp(a) != 0 {
		t.Fatalf("unscaled bound %v, want %v", got, a)
	}
	if got := minPayBound(a, 64, true); got.Cmp(big.NewInt(64_000)) != 0 {
		t.Fatalf("scaled bound at window 64 %v, want 64000", got)
	}
	if got := minPayBound(a, 256, true); got.Cmp(a) != 0 {
		t.Fatalf("scaled bound at window 256 %v, want %v (the unscaled form)", got, a)
	}
	if minPayBound(a, 64, false) == a {
		t.Fatalf("the unscaled bound aliases A256_c")
	}

	e := NewFaker(TestParts())
	for _, h := range []uint64{1, 4211, 5939, 5940, 1 << 40} {
		if e.payBoundUnscaledAt(h) {
			t.Fatalf("no gate: height %d reads unscaled", h)
		}
	}
	e.SetPayBoundUnscaledUntil(5939)
	if e.PayBoundUnscaledUntil() != 5939 {
		t.Fatalf("gate %d, want 5939", e.PayBoundUnscaledUntil())
	}
	for h, want := range map[uint64]bool{1: true, 65: true, 4211: true, 5939: true, 5940: false, 6917: false} {
		if got := e.payBoundUnscaledAt(h); got != want {
			t.Fatalf("gate 5939: height %d unscaled %v, want %v", h, got, want)
		}
	}
}

// TestF192RegistryNoticeGate (16 October 2026): the notice floor (a
// chain-held registration without the research mark needs noticeBlocks of
// at least 64) binds at every landing without a gate, and only at landings
// at or above the chain's gate with one, so a fresh walk admits the
// research chain's landings 4115 to 5379 as the chain admitted them.
func TestF192RegistryNoticeGate(t *testing.T) {
	key := common.HexToAddress("0x8efb30577370a071d77c63f3bab7da0a7af96be0")
	coord := func(name string) ctypes.IgneumCoordinate {
		return ctypes.IgneumCoordinate{Name: name, Algorithm: "igneumhash", Target: 80, DRef: 100000}
	}
	stored := &ctypes.IgneumVector{RegistryKey: key, Coordinates: []ctypes.IgneumCoordinate{coord("a")}}
	// The entry above every head used here, so the floor is the only rule
	// that can refuse the document.
	given := &ctypes.IgneumVector{RegistryKey: key, Coordinates: []ctypes.IgneumCoordinate{coord("a")}, PayChanges: []ctypes.IgneumPayChange{{Height: 7000, PayShares: []uint64{5}}}}
	if _, err := Reregistration(stored, given, 4206); err == nil {
		t.Fatalf("no gate: a document without the research mark and a notice under 64 admitted at 4206")
	}
	if _, err := ReregistrationFrom(stored, given, 4206, 5949); err != nil {
		t.Fatalf("gate 5949: the 4206 landing refused: %v", err)
	}
	if _, err := ReregistrationFrom(stored, given, 5379, 5949); err != nil {
		t.Fatalf("gate 5949: the 5379 landing refused: %v", err)
	}
	if _, err := ReregistrationFrom(stored, given, 5949, 5949); err == nil {
		t.Fatalf("gate 5949: a document without the mark admitted at the landing check's own landing")
	}
	marked := *given
	marked.Research = true
	if _, err := ReregistrationFrom(stored, &marked, 5949, 5949); err != nil {
		t.Fatalf("gate 5949: the marked document refused at 5949: %v", err)
	}
	e := NewFaker(TestParts())
	if e.RegistryNoticeFrom() != 0 {
		t.Fatalf("a fresh engine carries a gate: %d", e.RegistryNoticeFrom())
	}
	e.SetRegistryNoticeFrom(5949)
	if e.RegistryNoticeFrom() != 5949 {
		t.Fatalf("gate %d, want 5949", e.RegistryNoticeFrom())
	}
}

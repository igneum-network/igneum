package igneum

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/difficulty"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// F-151 (the 12-second form, 15 October 2026): a height-gated change of
// the clock constants (halfLife, dtMax, tMin, tailDelta) admitted by a
// registry landing. The boundary: the block at the entry's height A is the
// first read under the new constants; the block at A - 1 reads the old.

// TestConstantsChangeBoundary: the controllers. Coordinate 0 seals every
// block on target; coordinates 1 (no cap, the anchored form before the
// change) and 2 (the registered cap 7,200) idle, their lag the elapsed
// time. The entry at A cuts the half-life from 3,600 to 900: at the fold's
// crossing into A the carried lag is rescaled by the half-lives' ratio
// (the design review's residual at 2f3e5369a), so the block at A reads the
// difficulty A - 1 would have (continuity to the integer), the blocks
// above A fold under the new half-life, and coordinate 2's derived cap
// follows by the same ratio (1,800) and binds where the lag reaches it.
func TestConstantsChangeBoundary(t *testing.T) {
	const A = 25
	v := vectorConfig()
	v.Coordinates[2].DMin = 1000
	v.Coordinates[2].LagCap = vector.DerivedLagCap(3600, 4000, 1000) // 7200
	v.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: A, HalfLife: 900, DtMax: 900}}
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
	old := difficulty.Params{Target: 80, HalfLife: 3600, DtMax: 3600, Min: big.NewInt(1)}
	nu := difficulty.Params{Target: 80, HalfLife: 900, DtMax: 900, Min: big.NewInt(1)}
	old2 := difficulty.Params{Target: 80, HalfLife: 3600, DtMax: 3600, Min: big.NewInt(1000)}
	nu2 := difficulty.Params{Target: 80, HalfLife: 900, DtMax: 900, Min: big.NewInt(1000)}
	hlAt := func(n uint64) uint64 {
		if n >= A {
			return 900
		}
		return 3600
	}
	capAt := func(n uint64) int64 {
		if n >= A {
			return 1800
		}
		return 7200
	}
	// The model of the folded lag after each block, per coordinate.
	var lag1, lag2 int64
	parent := g
	for n := uint64(1); n <= 46; n++ {
		p1, p2 := old, old2
		if hlAt(n) == 900 {
			p1, p2 = nu, nu2
		}
		d1, err := e.vectorDifficulty(c, parent, 1)
		if err != nil {
			t.Fatal(err)
		}
		if want := p1.NextFromLag(big.NewInt(2000), lag1); d1.Cmp(want) != 0 {
			t.Fatalf("height %d: coordinate 1's difficulty %v, want %v (lag %d)", n, d1, want, lag1)
		}
		d2, err := e.vectorDifficulty(c, parent, 2)
		if err != nil {
			t.Fatal(err)
		}
		if want := p2.NextFromLag(big.NewInt(4000), lag2); d2.Cmp(want) != 0 {
			t.Fatalf("height %d: coordinate 2's difficulty %v, want %v (lag %d)", n, d2, want, lag2)
		}
		switch n {
		case A:
			// THE CONTINUITY CELL: the block at A reads what A - 1's
			// controller read at its lag L, to the integer: the new
			// half-life over the rescaled lag L x 900 / 3600 equals the
			// old over L.
			L := int64(A-1) * 80
			if want := old.NextFromLag(big.NewInt(2000), L); d1.Cmp(want) != 0 || nu.NextFromLag(big.NewInt(2000), L*900/3600).Cmp(want) != 0 {
				t.Fatalf("the boundary is not continuous: %v at A, %v at A - 1's lag %d", d1, want, L)
			}
			if want := old2.NextFromLag(big.NewInt(4000), L); d2.Cmp(want) != 0 {
				t.Fatalf("coordinate 2 at the boundary: %v, want %v", d2, want)
			}
		case A + 1:
			// Above A the fold runs under the new half-life: the old
			// constants over the unscaled lag read another figure.
			if other := old.NextFromLag(big.NewInt(2000), int64(A)*80); other.Cmp(d1) == 0 {
				t.Fatalf("height %d reads the old controller's figure %v", n, d1)
			}
		}
		h := taggedChild(t, e, c, parent, 0, 80)
		c.setCanonical(h)
		parent = h
		// The model's fold of block n: the interval, the cap at n, then
		// the rescale where the child's half-life differs.
		lag1 += 80
		lag2 += 80
		if lag2 > capAt(n) {
			lag2 = capAt(n)
		}
		if hlAt(n+1) != hlAt(n) {
			lag1 = lag1 * int64(hlAt(n+1)) / int64(hlAt(n))
			lag2 = lag2 * int64(hlAt(n+1)) / int64(hlAt(n))
		}
		info, err := e.vectorInfo(c, h)
		if err != nil {
			t.Fatal(err)
		}
		if info.Lags[1] != lag1 || info.Lags[2] != lag2 {
			t.Fatalf("height %d: the folded lags %d %d, want %d %d (the cap in force %d)", n, info.Lags[1], info.Lags[2], lag1, lag2, m.LagCapAt(2, n))
		}
		if info.HalfLife != hlAt(n+1) {
			t.Fatalf("height %d: the reader's half-life for the child %d, want %d", n, info.HalfLife, hlAt(n+1))
		}
	}
	// The rescaled lag at A - 1: 24 x 80 = 1,920 to 480; by block 42 the
	// lag reaches the new cap 1,800 and the controller reads the floor.
	if lag2 != 1800 {
		t.Fatalf("coordinate 2's lag after 46 blocks %d, want the new cap 1800", lag2)
	}
	d2, err := e.vectorDifficulty(c, parent, 2)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("coordinate 2 at its cap under the new half-life: %v, want the floor 1000", d2)
	}
	if m.LagCapAt(2, A-1) != 7200 || m.LagCapAt(2, A) != 1800 || m.LagCapAt(1, A) != 0 {
		t.Fatalf("the caps at the boundary: %d %d %d", m.LagCapAt(2, A-1), m.LagCapAt(2, A), m.LagCapAt(1, A))
	}
	// The menu's resolution: a later entry naming one constant keeps the
	// others as the entry before it set them.
	v2 := vectorConfig()
	v2.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 10, HalfLife: 900}, {Height: 20, TMin: 6}}
	m2, err := vector.New(v2, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if c9, c10, c20 := m2.ConstantsAt(9), m2.ConstantsAt(10), m2.ConstantsAt(20); c9.HalfLife != 3600 || c9.DtMax != 3600 || c9.TMin != 0 || c10.HalfLife != 900 || c10.DtMax != 3600 || c10.TMin != 0 || c20.HalfLife != 900 || c20.TMin != 6 {
		t.Fatalf("the cumulative resolution: %+v %+v %+v", c9, c10, c20)
	}
}

// TestConstantsChangeClock: the seal clock's floors read the registration's
// tMin and tailDelta from the entry's height through the engine's hook,
// the chain's own (and the rule fork's) below it.
func TestConstantsChangeClock(t *testing.T) {
	const A = 100
	v := vectorConfig()
	v.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: A, TMin: 6, TailDelta: 6}}
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	parts.Sortition = &SortitionParams{Delta: 20, TMin: 12}
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s := e.config.Parts.Sortition
	if s.ConstantsAt == nil {
		t.Fatal("the engine did not wire the registration's constants into the sortition rule")
	}
	if s.TMinAt(A-1) != 12 || s.RoundStart(A-1, 0) != 0 || s.RoundStart(A-1, 1) != 20 || s.RoundStart(A-1, 2) != 40 {
		t.Fatalf("below the height: tMin %d, rounds %d %d %d", s.TMinAt(A-1), s.RoundStart(A-1, 0), s.RoundStart(A-1, 1), s.RoundStart(A-1, 2))
	}
	if s.TMinAt(A) != 6 || s.RoundStart(A, 0) != 0 || s.RoundStart(A, 1) != 20 || s.RoundStart(A, 2) != 26 || s.RoundStart(A, 4) != 38 {
		t.Fatalf("at the height: tMin %d, rounds %d %d %d %d", s.TMinAt(A), s.RoundStart(A, 0), s.RoundStart(A, 1), s.RoundStart(A, 2), s.RoundStart(A, 4))
	}
	// The registration's entry wins over a rule fork's tail from its
	// height; the fork's figures hold below it.
	e.SetRuleForks(&ctypes.IgneumRuleForks{TailV1: 50, TailDelta: 10, TailTMin: 8})
	if s.TMinAt(49) != 12 || s.TMinAt(50) != 8 || s.RoundStart(50, 2) != 30 || s.TMinAt(A) != 6 || s.RoundStart(A, 2) != 26 {
		t.Fatalf("with a rule fork: %d %d %d %d %d", s.TMinAt(49), s.TMinAt(50), s.RoundStart(50, 2), s.TMinAt(A), s.RoundStart(A, 2))
	}
	// A landed re-registration binds without a restart: the handle is
	// read at every call.
	v2 := *v
	v2.ConstantsChanges = append(append([]ctypes.IgneumConstantsChange(nil), v.ConstantsChanges...), ctypes.IgneumConstantsChange{Height: 200, TMin: 4})
	if err := m.Replace(&v2); err != nil {
		t.Fatal(err)
	}
	if s.TMinAt(199) != 6 || s.TMinAt(200) != 4 || s.RoundStart(200, 2) != 26 {
		t.Fatalf("after the replace: %d %d %d", s.TMinAt(199), s.TMinAt(200), s.RoundStart(200, 2))
	}
}

// TestConstantsChangeAdmission: the shape, the re-registration's rule (the
// stored entries immutable, an appended entry above the head) and the
// landing's (above the landing block).
func TestConstantsChangeAdmission(t *testing.T) {
	bad := vectorConfig()
	bad.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 20, HalfLife: 900}, {Height: 10, TMin: 6}}
	if err := vector.Validate(bad); !errors.Is(err, vector.ErrShape) {
		t.Fatalf("entries out of order admitted: %v", err)
	}
	bad = vectorConfig()
	bad.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 10}}
	if err := vector.Validate(bad); !errors.Is(err, vector.ErrShape) {
		t.Fatalf("an entry changing nothing admitted: %v", err)
	}
	bad = vectorConfig()
	bad.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 0, HalfLife: 900}}
	if err := vector.Validate(bad); !errors.Is(err, vector.ErrShape) {
		t.Fatalf("an entry at height 0 admitted: %v", err)
	}
	stored := vectorConfig()
	stored.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 100, HalfLife: 900}}
	given := *stored
	given.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 100, HalfLife: 900}, {Height: 151, TMin: 6, TailDelta: 6}}
	if _, err := Reregistration(stored, &given, 150); err != nil {
		t.Fatalf("an entry above the head refused: %v", err)
	}
	if _, err := Reregistration(stored, &given, 151); err == nil {
		t.Fatal("an entry at the head admitted")
	}
	given.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 100, HalfLife: 1800}, {Height: 151, TMin: 6}}
	if _, err := Reregistration(stored, &given, 150); err == nil {
		t.Fatal("a changed stored entry admitted")
	}
	given.ConstantsChanges = nil
	if _, err := Reregistration(stored, &given, 150); err == nil {
		t.Fatal("a dropped stored entry admitted")
	}
	given.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 100, HalfLife: 900}, {Height: 151, TMin: 6}}
	if err := landingNotice(stored, &given, 151); !errors.Is(err, errRegistryNotice) {
		t.Fatalf("an entry at the landing admitted: %v", err)
	}
	if err := landingNotice(stored, &given, 150); err != nil {
		t.Fatalf("an entry above the landing refused: %v", err)
	}
	// The stored entry at 100 stands under a landing above it.
	if err := landingNotice(stored, &given, 149); err != nil {
		t.Fatalf("the stored entry refused under a later landing: %v", err)
	}
	// The parser's bounds on the values (SortitionFor's three): under a
	// clock of Delta 20, tPiMs 250, a tMin of 20 (the proof over Delta),
	// a tailDelta of 21 (over Delta) and one of 0.25 s or under tPiMs
	// are refused at the config registration and at the landing; 4 and 6
	// admitted.
	e := vectorEngine(t, "")
	e.config.Parts.Sortition = &SortitionParams{Delta: 20, TPiMs: 250, SealWindowDiv: 4, TMin: 12}
	for _, bad := range []ctypes.IgneumConstantsChange{{Height: 10, TMin: 20}, {Height: 10, TMin: 21}, {Height: 10, TailDelta: 21}} {
		doc := vectorConfig()
		doc.ConstantsChanges = []ctypes.IgneumConstantsChange{bad}
		if err := e.constantsWithinClock(doc); err == nil {
			t.Fatalf("an entry outside the clock's bounds admitted: %+v", bad)
		}
	}
	doc := vectorConfig()
	doc.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 10, TMin: 4, TailDelta: 6}}
	if err := e.constantsWithinClock(doc); err != nil {
		t.Fatalf("the research chain's values refused: %v", err)
	}
	docBad := vectorConfig()
	docBad.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 10, TailDelta: 21}}
	mBad, err := vector.New(docBad, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	parts.Sortition = &SortitionParams{Delta: 20, TPiMs: 250, SealWindowDiv: 4, TMin: 12}
	if _, err := New(Config{Algorithm: mBad, Parts: parts}); err == nil {
		t.Fatal("a config registration outside the clock's bounds built an engine")
	}
}

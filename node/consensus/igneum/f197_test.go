package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestVectorStateCacheByMenuGeneration (F-197, 16 October 2026): a vector
// state cached under one registration never serves a verification under
// the next. Two engines walk the same forty blocks: the first with the
// constants change (the half-life 3600 to 900 at A = 25) registered from
// the start, the second with a registration without it, replaced by one
// with it only once the walk has cached every state to A - 1 (the way a
// landing's new menu meets a reader's cache). Before the fix the second
// engine's cached state at A - 1 carried the lag unrescaled and its
// difficulty at A was the first engine's times two to the power of the
// lag's ratio (the research chain's 6437: have 319601351, want 721178065);
// now the replaced menu's generation retires the cache and both engines
// read one difficulty at A and after it.
func TestVectorStateCacheByMenuGeneration(t *testing.T) {
	const A = 25
	withChange := vectorConfig()
	withChange.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: A, HalfLife: 900, DtMax: 900}}
	without := vectorConfig()

	newEngine := func(v *ctypes.IgneumVector) (*Engine, *vector.Menu) {
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
		return e, m
	}
	eRef, _ := newEngine(withChange)
	defer eRef.Close()
	eLate, mLate := newEngine(without)
	defer eLate.Close()
	if mLate.Generation() != 0 {
		t.Fatalf("a fresh menu's generation %d, want 0", mLate.Generation())
	}

	cRef, cLate := newForkChain(), newForkChain()
	g := genesisHeader()
	g.Time = 1_000_000
	cRef.setCanonical(g)
	cLate.setCanonical(g)
	eRef.anchor(cRef)
	eLate.anchor(cLate)

	pRef, pLate := g, g
	for n := uint64(1); n <= 40; n++ {
		if n == A {
			// The landing's menu arrives with every state to A - 1 cached
			// under the old one.
			if err := mLate.Replace(withChange); err != nil {
				t.Fatal(err)
			}
			if mLate.Generation() != 1 {
				t.Fatalf("the replaced menu's generation %d, want 1", mLate.Generation())
			}
		}
		dRef, err := eRef.vectorDifficulty(cRef, pRef, 1)
		if err != nil {
			t.Fatal(err)
		}
		dLate, err := eLate.vectorDifficulty(cLate, pLate, 1)
		if err != nil {
			t.Fatal(err)
		}
		if dRef.Cmp(dLate) != 0 {
			t.Fatalf("height %d: the late menu's difficulty %v, the reference's %v", n, dLate, dRef)
		}
		hRef := taggedChild(t, eRef, cRef, pRef, 0, 80)
		cRef.setCanonical(hRef)
		pRef = hRef
		hLate := taggedChild(t, eLate, cLate, pLate, 0, 80)
		cLate.setCanonical(hLate)
		pLate = hLate
		if hRef.Difficulty.Cmp(hLate.Difficulty) != 0 {
			t.Fatalf("height %d: the chains' blocks differ in difficulty: %v and %v", n, hRef.Difficulty, hLate.Difficulty)
		}
	}
	// The retired states stay retired: a lookup under the new generation
	// at a height walked under the old one recomputes and agrees.
	sLate, err := eLate.vectorStateOf(cLate, cLate.canonical[A-1])
	if err != nil {
		t.Fatal(err)
	}
	sRef, err := eRef.vectorStateOf(cRef, cRef.canonical[A-1])
	if err != nil {
		t.Fatal(err)
	}
	if sLate.gen != 1 || sLate.lag[1] != sRef.lag[1] {
		t.Fatalf("the state at A - 1 under the new menu: generation %d lag %d, the reference's lag %d", sLate.gen, sLate.lag[1], sRef.lag[1])
	}
}

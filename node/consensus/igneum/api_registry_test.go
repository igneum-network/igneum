package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestVectorRegistryServesRules (the review lead's cell on F-151's landing read):
// igneum_vectorRegistry's answer carries the registration's height-gated
// rules whole (the constants' changes and the constants in force at the
// height, the pay and lanes changes, the balanced, fold and breadth
// heights with the two rules' states), so a reader never needs a node's
// journal.
func TestVectorRegistryServesRules(t *testing.T) {
	v := vectorConfig()
	v.Rule = "coverage"
	v.PayByCoordinate = true
	v.PayShares = []uint64{1, 1, 1}
	v.PayChanges = []ctypes.IgneumPayChange{{Height: 50, PayShares: []uint64{1, 0, 1}}}
	v.BalancedLanesChanges = []ctypes.IgneumLanesChange{{Height: 50, Lanes: []uint8{0, 2}}}
	v.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 100, HalfLife: 540, DtMax: 540, TMin: 8, TailDelta: 12}}
	v.BalancedFrom, v.BalancedV2From, v.BalancedV3From, v.BalancedMinFrom = 1, 1, 1, 20
	v.SealerFoldFrom, v.SealingBreadthFrom = 60, 70
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
	api := &IgneumAPI{engine: e}
	info, err := api.VectorRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	// With no chain the height is 0: the rules' heights served, their
	// states off, the constants the registration's.
	if len(info.ConstantsChanges) != 1 || info.ConstantsChanges[0].Height != 100 || info.ConstantsChanges[0].HalfLife != 540 {
		t.Fatalf("the constants' changes: %+v", info.ConstantsChanges)
	}
	if info.Constants.HalfLife != 3600 || info.Constants.DtMax != 3600 || info.Constants.TMin != 0 {
		t.Fatalf("the constants at 0: %+v", info.Constants)
	}
	if len(info.PayChanges) != 1 || info.PayChanges[0].Height != 50 || len(info.LanesChanges) != 1 || len(info.LanesChanges[0].Lanes) != 2 || info.LanesChanges[0].Lanes[1] != 2 {
		t.Fatalf("the pay and lanes changes: %+v %+v", info.PayChanges, info.LanesChanges)
	}
	if info.BalancedMinFrom != 20 || info.SealerFoldFrom != 60 || info.SealingBreadthFrom != 70 || info.SealerFold || info.SealingBreadth {
		t.Fatalf("the rules' heights and states at 0: %+v", info)
	}
	// At a height past every rule the states read on and the constants
	// the entry's.
	c := newForkChain()
	g := genesisHeader()
	c.setCanonical(g)
	parent := g
	for n := 1; n <= 100; n++ {
		h := childOf(parent)
		c.setCanonical(h)
		parent = h
	}
	api.chain = c
	info, err = api.VectorRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.Height != 100 || info.Constants.HalfLife != 540 || info.Constants.TMin != 8 || info.Constants.TailDelta != 12 || !info.SealerFold || !info.SealingBreadth {
		t.Fatalf("at 100: %+v", info)
	}
}

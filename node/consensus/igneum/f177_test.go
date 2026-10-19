package igneum

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestSortitionRPCOnVectorForm (F-177): on a vector-form chain the engine
// wires the registration's clock constants into the sortition rule as a
// function (F-151); igneum_sortition's answer must still marshal (the
// public test network's answered "json: unsupported type" from 0.10.20,
// the root feed's bond total null) and carry the constants in force at
// the next height as values.
func TestSortitionRPCOnVectorForm(t *testing.T) {
	v := vectorConfig()
	v.ConstantsChanges = []ctypes.IgneumConstantsChange{{Height: 5, TMin: 8, TailDelta: 12}}
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	parts.Sortition = &SortitionParams{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 20, MaxRound: 16, UnbondedRound: 17, MaxPayouts: 256, TMin: 12}
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.config.Parts.Sortition.ConstantsAt == nil {
		t.Fatal("the hook is not wired")
	}
	c := newForkChain()
	g := genesisHeader()
	c.setCanonical(g)
	parent := g
	for n := 1; n <= 6; n++ {
		h := childOf(parent)
		c.setCanonical(h)
		parent = h
	}
	api := &IgneumAPI{engine: e, chain: c}
	info, err := api.Sortition()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("igneum_sortition does not marshal on the vector form: %v", err)
	}
	var back struct {
		Params struct {
			Delta       uint64 `json:"Delta"`
			TMin        uint64 `json:"TMin"`
			Height      uint64 `json:"height"`
			TMinAt      uint64 `json:"tMinAt"`
			TailDeltaAt uint64 `json:"tailDeltaAt"`
		} `json:"params"`
		Head uint64 `json:"head"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	// The head 6, the next 7: the entry at 5 in force (tMin 8, the tail 12).
	if back.Head != 6 || back.Params.Height != 7 || back.Params.Delta != 20 || back.Params.TMin != 12 || back.Params.TMinAt != 8 || back.Params.TailDeltaAt != 12 {
		t.Fatalf("the params view: %+v", back.Params)
	}
	// Without the vector form the view reads the chain's own (its own
	// params: New wires the hook into the pointer it is given).
	pparts := TestParts()
	pparts.Sortition = &SortitionParams{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 20, MaxRound: 16, UnbondedRound: 17, MaxPayouts: 256, TMin: 12}
	plain := NewFullFaker(pparts)
	defer plain.Close()
	pinfo, err := (&IgneumAPI{engine: plain, chain: c}).Sortition()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(pinfo); err != nil {
		t.Fatal(err)
	}
	pv := pinfo.Params.(SortitionParamsView)
	if pv.TMinAt != 12 || pv.TailDeltaAt != 20 {
		t.Fatalf("the plain chain's view: %+v", pv)
	}
}

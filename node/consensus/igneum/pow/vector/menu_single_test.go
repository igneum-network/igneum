package vector

import (
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// singleStub records the one-resident-context form an adapter is given.
type singleStub struct {
	pow.Algorithm
	single bool
	calls  int
}

func (s *singleStub) SetSingleDataset(on bool) { s.single = on; s.calls++ }

// TestReplaceCarriesTheSingleForm (F-215, 16 October 2026): a registration
// that replaces the menu builds new adapters for the coordinates it
// changes; they take the single form the node chose, as they take the full
// choice and the seal coordinate. Before the fix every coordinate a landing
// changed came back in the lookahead form (two epochs' full datasets built
// beside a third held) and the research chain's nodes grew to 18 to 26 GiB.
func TestReplaceCarriesTheSingleForm(t *testing.T) {
	var made []*singleStub
	build := func(name string, params []byte) (pow.Algorithm, error) {
		a, err := pow.NewDev(name, params)
		if err != nil {
			return nil, err
		}
		s := &singleStub{Algorithm: a}
		made = append(made, s)
		return s, nil
	}
	v := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", MaxLiveBlocks: 1000, Coordinates: []ctypes.IgneumCoordinate{
		{Name: "a", Algorithm: null.Name, Target: 80, DRef: 100},
		{Name: "b", Algorithm: null.Name, Target: 80, DRef: 100},
	}}
	m, err := New(v, build)
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 2 {
		t.Fatalf("adapters built: %d, want 2", len(made))
	}
	m.SetSingleDataset(true)
	for i, s := range made {
		if !s.single {
			t.Fatalf("coordinate %d not in the single form after SetSingleDataset", i)
		}
	}
	// A landing that changes coordinate b (its name here; a document in the
	// chain's case): b is built anew, a is reused.
	v2 := &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", MaxLiveBlocks: 1000, Coordinates: []ctypes.IgneumCoordinate{
		{Name: "a", Algorithm: null.Name, Target: 80, DRef: 100},
		{Name: "b-landed", Algorithm: null.Name, Target: 80, DRef: 100},
	}}
	if err := m.Replace(v2); err != nil {
		t.Fatal(err)
	}
	if len(made) != 3 {
		t.Fatalf("adapters built after the replace: %d, want 3 (one rebuilt)", len(made))
	}
	if !made[2].single {
		t.Fatal("the adapter built by the replace is not in the single form (the lookahead form: two epochs' datasets resident)")
	}
	if !made[0].single {
		t.Fatal("the reused adapter lost the single form")
	}
	// A menu never given the single form carries none through a replace.
	made = nil
	m2, err := New(v, build)
	if err != nil {
		t.Fatal(err)
	}
	if err := m2.Replace(v2); err != nil {
		t.Fatal(err)
	}
	for i, s := range made {
		if s.single {
			t.Fatalf("adapter %d given the single form by a replace of a menu never in it", i)
		}
	}
}

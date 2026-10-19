package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestKeyedRoundsPerCoordinate (F-433, 17 October 2026): under the vector
// form the keyed state is the header's coordinate's document's, read
// through one accessor: a two-coordinate menu with coordinate 1 keyed (S
// = 2 by a keyed stub over the null algorithm) and coordinate 0 unkeyed.
// A header tagged 1 reads S = 2 at the template's S, the genesis form's
// keyed question and the verifier's refusal of shares; a header tagged 0
// reads 0 and its shares are refused as on an unkeyed document; a header
// without a tag reads 0; the node's own view (igneum_sortition) follows
// its sealing coordinate.
func TestKeyedRoundsPerCoordinate(t *testing.T) {
	v := &ctypes.IgneumVector{
		Coordinates: []ctypes.IgneumCoordinate{
			{Name: "plain", Algorithm: null.Name, Target: 80, DRef: 1000},
			{Name: "keyed", Algorithm: "keyedstub", Target: 80, DRef: 2000},
		},
		HalfLife: 3600, DtMax: 3600,
	}
	build := func(name string, params []byte) (pow.Algorithm, error) {
		if name == "keyedstub" {
			base, err := pow.NewDev(null.Name, nil)
			if err != nil {
				return nil, err
			}
			return &keyedStub{Algorithm: base, s: 2}, nil
		}
		return pow.NewDev(name, params)
	}
	m, err := vector.New(v, build)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	tagged := func(c uint8) *types.Header {
		h := &types.Header{Number: big.NewInt(7), Difficulty: big.NewInt(1)}
		h.SetCoordTag(c)
		return h
	}
	if s := e.docKeyedRounds(tagged(1)); s != 2 {
		t.Fatalf("the keyed coordinate reads S = %d, want 2", s)
	}
	if s := e.docKeyedRounds(tagged(0)); s != 0 {
		t.Fatalf("the unkeyed coordinate reads S = %d, want 0", s)
	}
	if s := e.docKeyedRounds(&types.Header{Number: big.NewInt(7)}); s != 0 {
		t.Fatalf("a header without a coordinate reads S = %d, want 0", s)
	}
	// The template's S (no genesis form in TestParts: the document's S for every block).
	if s := e.keyedRoundsFor(tagged(1)); s != 2 {
		t.Fatalf("the template's S on the keyed coordinate: %d, want 2", s)
	}
	if s := e.keyedRoundsFor(tagged(0)); s != 0 {
		t.Fatalf("the template's S on the unkeyed coordinate: %d, want 0", s)
	}
	// The node's own view follows its sealing coordinate.
	if err := e.SetCoordinate(1); err != nil {
		t.Fatal(err)
	}
	if s := e.ownDocKeyedRounds(7); s != 2 {
		t.Fatalf("the own view on coordinate 1: %d, want 2", s)
	}
	if err := e.SetCoordinate(0); err != nil {
		t.Fatal(err)
	}
	if s := e.ownDocKeyedRounds(7); s != 0 {
		t.Fatalf("the own view on coordinate 0: %d, want 0", s)
	}
}

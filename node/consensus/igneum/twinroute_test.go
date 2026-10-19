package igneum

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestVectorUnkeyedRoute (0.10.58, F-494): under the vector form a header
// without shares on a keyed coordinate is hashed with the form's twin; a
// header with shares with the keyed form; this node's own template marked
// keyed at assembly with the keyed form though it carries no shares yet;
// an unkeyed coordinate's header with its own form.
func TestVectorUnkeyedRoute(t *testing.T) {
	build := func(name string, params []byte) (pow.Algorithm, error) {
		base, err := pow.NewDev(null.Name, nil)
		if err != nil {
			return nil, err
		}
		if name == "keyedstub" {
			return &keyedStub{Algorithm: base, s: 2}, nil
		}
		return base, nil
	}
	v := &ctypes.IgneumVector{
		Coordinates: []ctypes.IgneumCoordinate{
			{Name: "plain", Algorithm: null.Name, Target: 80, DRef: 1000},
			{Name: "keyed", Algorithm: "keyedstub", AlgorithmParams: json.RawMessage(`{"features":5152,"keyed_rounds":2}`), Target: 80, DRef: 2000},
		},
		HalfLife: 3600, DtMax: 3600,
	}
	m, err := vector.New(v, build)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Algorithm: m, Parts: TestParts()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	tagged := func(c uint8, shares int) *types.Header {
		h := &types.Header{Number: big.NewInt(7), Difficulty: big.NewInt(1)}
		h.SetCoordTag(c)
		if shares > 0 {
			h.Shares = make([]byte, shares)
		}
		return h
	}
	twin := m.UnkeyedAlgorithmAt(1, 7)
	if twin == nil {
		t.Fatal("the keyed coordinate has no twin")
	}
	if got := e.vectorUnkeyedFor(tagged(1, 0)); got != twin {
		t.Fatalf("a shareless header on the keyed coordinate routes to %T, want the twin", got)
	}
	if got := e.vectorUnkeyedFor(tagged(1, 64)); got != nil {
		t.Fatal("a header with shares routes to the twin")
	}
	if got := e.vectorUnkeyedFor(tagged(0, 0)); got != nil {
		t.Fatal("an unkeyed coordinate's header routes to a twin")
	}
	own := tagged(1, 0)
	e.keyed.set(e.SealHash(own), true)
	if got := e.vectorUnkeyedFor(own); got != nil {
		t.Fatal("this node's own keyed template routes to the twin")
	}
	e.keyed.set(e.SealHash(own), false)
	if got := e.vectorUnkeyedFor(own); got != twin {
		t.Fatal("this node's own unkeyed template does not route to the twin")
	}
	// The document's S still reads from the keyed form, never the twin.
	if s := e.docKeyedRounds(tagged(1, 0)); s != 2 {
		t.Fatalf("the document's S on a shareless header: %d, want 2", s)
	}
}

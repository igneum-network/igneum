package vector

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// baseStub is an IgneumHash-like adapter whose WorkParams is the document's
// map (sameBase reads it), recording its donor and full flag.
type baseStub struct {
	pow.Algorithm
	fields map[string]any
	donor  any
	full   bool
}

func (s *baseStub) WorkParams() any           { return s.fields }
func (s *baseStub) ParamsDigest() common.Hash { b, _ := json.Marshal(s.fields); return common.BytesToHash(b) }
func (s *baseStub) SetDonor(d any)            { s.donor = d }
func (s *baseStub) SetFullDataset(full bool)  { s.full = full }

// TestReaderHoldsOneFullContextPerLiveBase (F-559 / F-560, 17 October
// 2026): on a node without a sealing coordinate every live base has one
// full holder and the rest of that base ride it; a retired coordinate of a
// held base rides the holder; a sealing node keeps the one-holder form
// (its own coordinate). The public test network's shape: coordinate 0 a
// retired 4 GiB base, coordinates 5 and 6 live on that base, 7 live on a
// 1 GiB base of its own (the anvil lane that verified light under the
// batch, oregon's stall).
func TestReaderHoldsOneFullContextPerLiveBase(t *testing.T) {
	made := map[string]*baseStub{}
	build := func(name string, params []byte) (pow.Algorithm, error) {
		base, err := pow.NewDev(null.Name, nil)
		if err != nil {
			return nil, err
		}
		var f map[string]any
		if err := json.Unmarshal(params, &f); err != nil {
			return nil, err
		}
		s := &baseStub{Algorithm: base, fields: f}
		made[string(params)] = s
		return s, nil
	}
	big := json.RawMessage(`{"version":15,"dataset_mib":4096,"cache_mib":64,"parents":256,"item_bytes":256}`)
	big2 := json.RawMessage(`{"version":15,"dataset_mib":4096,"cache_mib":64,"parents":256,"item_bytes":256,"program_ops":64}`)
	small := json.RawMessage(`{"version":21,"dataset_mib":1024,"cache_mib":64,"parents":256,"item_bytes":256}`)
	v := &ctypes.IgneumVector{
		Coordinates: []ctypes.IgneumCoordinate{
			{Name: "old", Algorithm: "stub", AlgorithmParams: big, Target: 36, DRef: 1000, DMin: 10, LagCap: DerivedLagCap(60, 1000, 10), RetiredFrom: 100},
			{Name: "live-a", Algorithm: "stub", AlgorithmParams: big2, Target: 36, DRef: 1000, DMin: 10, LagCap: DerivedLagCap(60, 1000, 10)},
			{Name: "live-b", Algorithm: "stub", AlgorithmParams: small, Target: 36, DRef: 1000, DMin: 10, LagCap: DerivedLagCap(60, 1000, 10)},
		},
		HalfLife: 60, DtMax: 60,
	}
	m, err := New(v, build)
	if err != nil {
		t.Fatal(err)
	}
	old, a, b := made[string(big)], made[string(big2)], made[string(small)]
	// The reader at a head past the retirement: live-a holds the 4 GiB
	// base, old rides it, live-b holds its own 1 GiB base.
	m.SetFullDataset(true)
	m.Anchor(200)
	if !a.full || a.donor != nil {
		t.Fatalf("live-a is not the 4 GiB base's holder: full %v donor %v", a.full, a.donor)
	}
	if !b.full || b.donor != nil {
		t.Fatalf("live-b is not the 1 GiB base's holder: full %v donor %v (the anvil lane verified light before F-559)", b.full, b.donor)
	}
	if !old.full || old.donor != a {
		t.Fatalf("the retired coordinate does not ride its base's live holder: full %v donor %v", old.full, old.donor)
	}
	// A sealing node on live-a: the one-holder form of before (live-b of
	// another base holds its own context light).
	m.SetSealCoordinate(1)
	if !a.full || a.donor != nil {
		t.Fatal("the sealing coordinate is not the holder")
	}
	if b.full || b.donor != nil {
		t.Fatalf("the sealing node built a second full dataset for another base: full %v", b.full)
	}
	if !old.full || old.donor != a {
		t.Fatal("the same-base rider does not ride the sealer's context")
	}
}

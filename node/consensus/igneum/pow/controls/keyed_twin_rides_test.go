//go:build controls && cgo

package controls

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestKeyedCoordinateTwinHashesOnTheFormsDataset (F-495, the review lead's row on
// the security review's strike against 0.10.58): a keyed coordinate's unkeyed twin
// HASHES on its form's full dataset, not on a light context of its own.
// The menu's donor pass gives the twin its form as the donor and the
// form's full flag; the adapter rides a donor only under the flag
// (withContext: "d != nil && full"), so with the flag false the twin built
// its own light context and every share and open-seat hash through it was
// a light hash (about 100 ms against 2 ms). The row hashes through the
// twin the menu hands out and reads the context's kind by the adapters'
// build counts: the form built its one full context, the twin built none,
// and the answer equals a standalone full adapter's over the same unkeyed
// document. The last leg pins the gate itself: an adapter with a donor
// and the flag false builds a context of its own.
func TestKeyedCoordinateTwinHashesOnTheFormsDataset(t *testing.T) {
	keyedDoc := json.RawMessage(`{"version": 8, "dataset_mib": 16, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 5152, "chain_id": 7358, "keyed_rounds": 2}`)
	plainDoc := json.RawMessage(`{"version": 8, "dataset_mib": 16, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 4128, "chain_id": 7358}`)
	build := func(name string, params []byte) (pow.Algorithm, error) { return newIgneumhashRef(7358, params) }
	v := &ctypes.IgneumVector{
		Coordinates: []ctypes.IgneumCoordinate{
			{Name: "plain", Algorithm: "igneumhash", AlgorithmParams: plainDoc, Target: 80, DRef: 1000, DMin: 10, LagCap: vector.DerivedLagCap(60, 1000, 10)},
			{Name: "keyed", Algorithm: "igneumhash", AlgorithmParams: keyedDoc, Target: 80, DRef: 2000, DMin: 10, LagCap: vector.DerivedLagCap(60, 2000, 10)},
		},
		HalfLife: 60, DtMax: 60,
	}
	m, err := vector.New(v, build)
	if err != nil {
		t.Fatal(err)
	}
	const height = 10
	m.SetFullDataset(true)
	m.SetSealCoordinate(1)
	m.Anchor(height)
	form, ok := m.ByCoordinateAt(1, height).(*ihRef)
	if !ok {
		t.Fatalf("the keyed form is %T, not the reference adapter", m.ByCoordinateAt(1, height))
	}
	twin, ok := m.UnkeyedAlgorithmAt(1, height).(*ihRef)
	if !ok {
		t.Fatalf("the twin is %T, not the reference adapter", m.UnkeyedAlgorithmAt(1, height))
	}
	defer form.Close()
	defer twin.Close()
	if !form.full || !twin.full {
		t.Fatalf("full flags: form %v twin %v, want both set", form.full, twin.full)
	}
	twinDoc, err := pow.UnkeyedTwin(keyedDoc)
	if err != nil {
		t.Fatal(err)
	}
	aloneAlg, err := newIgneumhashRef(7358, twinDoc)
	if err != nil {
		t.Fatal(err)
	}
	alone := aloneAlg.(*ihRef)
	alone.SetFullDataset(true)
	defer alone.Close()

	anchor := common.HexToHash("0x5e8")
	header := common.HexToHash("0x11")
	// The hash through the twin the menu hands out, then the standalone's.
	for nonce := uint64(1); nonce <= 8; nonce++ {
		mix, res := twin.Under(anchor).Compute(header, height, nonce)
		wantMix, wantRes := alone.Under(anchor).Compute(header, height, nonce)
		if mix == (common.Hash{}) && res == (common.Hash{}) {
			t.Fatalf("nonce %d: the twin's hash refused", nonce)
		}
		if mix != wantMix || res != wantRes {
			t.Fatalf("nonce %d: the twin's answer %x / %x differs from the standalone full adapter's %x / %x", nonce, mix[:4], res[:4], wantMix[:4], wantRes[:4])
		}
	}
	// The context's kind: the form's one full dataset, nothing of the twin's.
	if b := form.builds.Load(); b != 1 {
		t.Fatalf("the form built %d contexts, want its one full dataset", b)
	}
	if b := twin.builds.Load(); b != 0 {
		t.Fatalf("the twin built %d contexts of its own; it rides its form's dataset (F-495)", b)
	}

	// The gate pinned: a donor with the flag false is not ridden (the
	// strike's form), the adapter builds a context of its own.
	lightAlg, err := newIgneumhashRef(7358, twinDoc)
	if err != nil {
		t.Fatal(err)
	}
	light := lightAlg.(*ihRef)
	defer light.Close()
	light.SetDonor(form)
	light.SetFullDataset(false)
	light.Under(anchor).Compute(header, height, 1)
	if b := light.builds.Load(); b != 1 {
		t.Fatalf("the adapter with a donor and the flag false built %d contexts, want its own one (the gate withContext rides a donor only under the flag)", b)
	}
	if b := form.builds.Load(); b != 1 {
		t.Fatalf("the form built %d contexts after the light leg, want still one", b)
	}
}

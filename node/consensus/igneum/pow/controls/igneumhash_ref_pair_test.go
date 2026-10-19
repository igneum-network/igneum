//go:build controls && cgo

package controls

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// TestKeyedRowRidesTwinDataset (the hash team's shared context, SPEC-R0E-v0
// section 5a; the claims gate's C-12 form): a keyed document's adapter
// rides its unkeyed twin's dataset. One full build in the twin, none in
// the row; the row's keyed hash equals a standalone keyed adapter's, the
// twin's unkeyed hash equals a standalone unkeyed adapter's; the pair
// holds one dataset.
func TestKeyedRowRidesTwinDataset(t *testing.T) {
	keyed := []byte(`{"version": 8, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 5152, "chain_id": 7315, "keyed_rounds": 2}`)
	twinDoc := []byte(`{"version": 8, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 4128, "chain_id": 7315}`)
	build := func(doc []byte) *ihRef {
		a, err := newIgneumhashRef(7315, doc)
		if err != nil {
			t.Fatal(err)
		}
		return a.(*ihRef)
	}
	row, twin := build(keyed), build(twinDoc)
	rowAlone, twinAlone := build(keyed), build(twinDoc)
	for _, a := range []*ihRef{row, twin, rowAlone, twinAlone} {
		a.SetFullDataset(true)
		a.SetSingleDataset(true)
	}
	row.SetDonor(twin)
	header := common.HexToHash("0x11")
	var sk [32]byte
	sk[0], sk[31] = 7, 9
	k, ok := pow.Algorithm(row).(pow.Keyed)
	if !ok {
		t.Fatal("the row is not keyed")
	}
	kAlone := pow.Algorithm(rowAlone).(pow.Keyed)
	mix, result, shares := k.ComputeKeyed(header, 1, 42, &sk, nil)
	mixA, resultA, sharesA := kAlone.ComputeKeyed(header, 1, 42, &sk, nil)
	if mix != mixA || result != resultA || fmt.Sprintf("%x", shares) != fmt.Sprintf("%x", sharesA) {
		t.Fatalf("the keyed hash over the twin's dataset differs from the standalone: %x/%x against %x/%x", mix[:4], result[:4], mixA[:4], resultA[:4])
	}
	m2, r2 := twin.Compute(header, 1, 42)
	m2A, r2A := twinAlone.Compute(header, 1, 42)
	if m2 != m2A || r2 != r2A {
		t.Fatal("the twin's unkeyed hash differs from the standalone")
	}
	if b := row.builds.Load(); b != 0 {
		t.Fatalf("the row built %d contexts of its own; it rides the twin's", b)
	}
	if b := twin.builds.Load(); b != 1 {
		t.Fatalf("the twin built %d contexts, want one (the pair's one dataset)", b)
	}
	row.mu.RLock()
	for i := range row.slot {
		if row.slot[i].set {
			row.mu.RUnlock()
			t.Fatal("the row holds a context of its own")
		}
	}
	row.mu.RUnlock()
	for _, a := range []*ihRef{row, twin, rowAlone, twinAlone} {
		a.Close()
	}
}

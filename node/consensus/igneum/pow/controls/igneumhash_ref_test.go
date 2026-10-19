//go:build controls && cgo

package controls

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// TestIgneumhashReferenceV8 builds the IgneumHash document (version 8, form (b)
// with bit 5 at (2, 16), 2 GiB) through the registry, prints its pid, and
// checks one hash verifies under its own computation and fails under a
// changed nonce.
func TestIgneumhashReferenceV8(t *testing.T) {
	doc := []byte(`{"version": 8, "dataset_mib": 2048, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 4128, "chain_id": 7310}`)
	a, err := pow.NewDev("igneumhash", doc)
	if err != nil {
		t.Fatalf("the reference refused the IgneumHash document: %v", err)
	}
	type digester interface{ ParamsDigest() common.Hash }
	t.Logf("pid %x", a.(digester).ParamsDigest())
	h := common.HexToHash("0x1111")
	mix, out := a.Compute(h, 5300, 7)
	if mix == (common.Hash{}) || out == (common.Hash{}) {
		t.Fatal("zero hash from the reference")
	}
	easy := big.NewInt(1)
	if err := a.Verify(h, 5300, 7, mix, easy, false); err != nil {
		t.Fatalf("own computation refused: %v", err)
	}
	if err := a.Verify(h, 5300, 8, mix, easy, false); err == nil {
		t.Fatal("another nonce accepted under the same mix")
	}
	if a.Epoch(5300) != 5300/4096 {
		t.Fatalf("epoch %d", a.Epoch(5300))
	}
}

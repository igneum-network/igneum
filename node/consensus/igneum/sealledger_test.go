package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestSealLedgerOneBlockPerKeyPerHeight: the node-local half of one block per
// key per height on the relay and import path (the fetcher's hook): the first
// seal a node sees under a key at a height stands, a second different seal
// under the same key at the same height is refused (kept as evidence), the
// same block seen twice is not a second, and a different key at the same
// height is not a second. Under the vector form an open-seat block is
// counted under its coinbase key (the key its draw value is computed with),
// so the fork rule's draw-value tie-break is total (the formal seat's
// revision-10 campaign, main 04e66e34; VECTORWORK.md revision 11, section
// 3 (i)).
func TestSealLedgerOneBlockPerKeyPerHeight(t *testing.T) {
	e := vectorEngine(t, "geometric")
	mk := func(coinbase byte, nonce uint64, bond *common.Hash) *types.Header {
		h := &types.Header{Number: big.NewInt(7), Coinbase: common.Address{coinbase}, Difficulty: big.NewInt(1), BondKey: bond}
		h.Nonce = types.EncodeNonce(nonce)
		h.SetCoordTag(1)
		return h
	}
	// Open-seat blocks under the vector form: counted by the coinbase key.
	if e.RecordSeal(mk(1, 1, nil)) {
		t.Fatal("the first seal under a key is never a second")
	}
	if e.RecordSeal(mk(1, 1, nil)) {
		t.Fatal("the same block seen again is not a second")
	}
	if e.RecordSeal(mk(2, 1, nil)) {
		t.Fatal("a different key at the height is not a second")
	}
	if !e.RecordSeal(mk(1, 2, nil)) {
		t.Fatal("a second, different seal under one key at one height must be refused")
	}
	ev := e.SealEquivocations()
	if len(ev) != 1 || ev[0].Height != 7 || ev[0].First == ev[0].Second {
		t.Fatalf("evidence: %+v", ev)
	}
	// Bonded blocks: counted by the bond key.
	k := common.HexToHash("0xaa")
	if e.RecordSeal(mk(3, 1, &k)) || !e.RecordSeal(mk(4, 2, &k)) {
		t.Fatal("bonded: the first stands, a second by the key is refused")
	}
}

// TestSealLedgerOpenSeatOutsideVectorForm: outside the vector form an
// open-seat block is not counted (the ledger's original scope).
func TestSealLedgerOpenSeatOutsideVectorForm(t *testing.T) {
	e := NewFaker(TestParts())
	defer e.Close()
	h := &types.Header{Number: big.NewInt(7), Coinbase: common.Address{1}, Difficulty: big.NewInt(1)}
	h.Nonce = types.EncodeNonce(1)
	h2 := types.CopyHeader(h)
	h2.Nonce = types.EncodeNonce(2)
	if e.RecordSeal(h) || e.RecordSeal(h2) {
		t.Fatal("no bond key outside the vector form: never a second")
	}
}

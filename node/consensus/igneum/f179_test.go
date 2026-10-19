package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
)

// TestBondedSignerDraw (F-179, the public test network's stall at 508, 15
// October 2026): when the node's bond key draws the open seat but its
// signer key holds units in the table (the bonds posted on the sealers'
// signer keys), the template is sealed as the signer key at the key's own
// round, never as the open seat under a bonded key (which the verifier
// refuses below the key's round); an unbonded signer key keeps the open
// seat.
func TestBondedSignerDraw(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	st := nativeState(t, big.NewInt(1_000_000_000))
	chain := newSupplyChain()
	for i := 0; i < 99; i++ {
		chain.extend(0, 1)
	}
	st.SetState(testRegistry, igneumreg.SlotFloor, common.BigToHash(big.NewInt(160_000)))
	seed := make([]byte, 32)
	seed[0] = 7
	if err := e.SetSigner(seed); err != nil {
		t.Fatal(err)
	}
	signer := *e.SignerKey()
	bondKey := common.HexToHash("0x0abc") // the node's bond key, unbonded
	// Before the signer key is bonded: the open seat stands.
	if _, _, ok := e.bondedSignerDraw(st, bondKey, 250); ok {
		t.Fatal("an unbonded signer key took the draw")
	}
	// The bonds on the signer key (the operator's form at 310), the epoch
	// table written at 99.
	bondNative(t, st, testHolder, signer, 40_000)
	if err := e.writeTable(chain, chain.canonical[99], st); err != nil {
		t.Fatal(err)
	}
	d, err := e.drawAt(st, bondKey, 250)
	if err != nil || !d.open {
		t.Fatalf("the bond key's draw is not the open seat: %+v %v", d, err)
	}
	ds, err := e.drawAt(st, signer, 250)
	if err != nil || ds.open || ds.units != MaxUnitsPerKey {
		t.Fatalf("the signer key's draw: %+v %v", ds, err)
	}
	key, r, ok := e.bondedSignerDraw(st, bondKey, 250)
	if !ok || key == nil || *key != signer || r != ds.round {
		t.Fatalf("the sealing draw: key %v round %d ok %v, want the signer key at its round %d", key, r, ok, ds.round)
	}
	// The same key as the bond key: nothing to take over.
	if _, _, ok := e.bondedSignerDraw(st, signer, 250); ok {
		t.Fatal("the bonded bond key itself took the signer's draw")
	}
	// The verifier's read of a template sealed under the signer key at
	// its round: the draw agrees, so no refusal below the round.
	if ds.round > r {
		t.Fatalf("the verify's round %d above the template's %d", ds.round, r)
	}
}

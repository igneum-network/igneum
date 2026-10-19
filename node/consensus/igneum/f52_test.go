package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
)

// stateSupplyChain is the supply chain (headers by number, the epoch's seed) with one state at every parent.
type stateSupplyChain struct {
	*supplyChain
	st *state.StateDB
}

func (c *stateSupplyChain) StateAt(root common.Hash) (*state.StateDB, error) { return c.st, nil }

// F-52 (the security review, 12:5x UK, 12 October 2026): a bonded key whose registered signer is not this node's signing
// key drew a keyed round and the sealer assembled a template it could not sign, sealing nothing (a deadlock on a
// one-sealer chain). The fix: until the registry's signer at the parent's state is this node's, the node seals
// the open seat under its own signer key, as an unbonded node does (the ordinary unbonded template: no units, no
// shares, no proof); once the rotation is in the parent's state, the keyed draw is kept.
func TestHeldKeyedDrawSealsTheOpenSeat(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	seed := make([]byte, 32)
	seed[0] = 7
	if err := e.SetSigner(seed); err != nil {
		t.Fatal(err)
	}
	pub := *e.SignerKey()
	st := nativeState(t, big.NewInt(1_000_000_000))
	chain := newSupplyChain()
	for i := 0; i < 99; i++ {
		chain.extend(0, 1)
	}
	st.SetState(testRegistry, igneumreg.SlotFloor, common.BigToHash(big.NewInt(160_000)))
	// testKey bonded by its holder; its registered signer the DEFAULT (the key itself), not this node's
	bondNative(t, st, testHolder, testKey, 40_000)
	if err := e.writeTable(chain, chain.canonical[99], st); err != nil {
		t.Fatalf("writeTable: %v", err)
	}
	for i := 99; i < 249; i++ {
		chain.extend(0, 1)
	}
	sc := &stateSupplyChain{supplyChain: chain, st: st}
	e.sortitionAnchor(sc)
	parent := chain.canonical[249]
	height := uint64(250)
	s := e.config.Parts.Sortition
	db, err := e.drawAt(st, testKey, height)
	if err != nil || db.open {
		t.Fatalf("the bonded draw: %+v %v", db, err)
	}
	r, open, _, err := e.roundFor(sc, parent, height, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if open || r >= s.UnbondedRound {
		t.Skipf("the bonded key drew round %d (open %v) on this seed; the fix is not exercised", r, open)
	}
	mk := func() *types.Header {
		key := testKey
		h := childOf(parent)
		h.BondKey = &key
		h.Time = parent.Time + s.UnbondedRound*s.Delta + 1
		return h
	}
	// the rotation pending: the template is the open seat's under this node's signer
	h1 := mk()
	if err := e.prepareSortition(sc, h1, parent); err != nil {
		t.Fatalf("prepareSortition with the rotation pending: %v", err)
	}
	if h1.BondKey == nil || *h1.BondKey != pub {
		t.Fatalf("the template names %v, want the node's signer key %v (the open seat)", h1.BondKey, pub)
	}
	if d, err := e.drawAt(st, *h1.BondKey, height); err != nil || !d.open || d.units != 0 {
		t.Fatalf("the held draw's template is not the ordinary unbonded one: %+v %v", d, err)
	}
	if e.sealIsKeyed(h1, st) {
		t.Fatal("the held draw's template reads keyed")
	}
	// the rotation in the parent's STATE (never the pool's sight of it): the keyed draw is kept
	st.SetState(testRegistry, signerSlot(testKey), pub)
	if got := e.config.Parts.Authority.SignerOf(st, testKey); got != pub {
		t.Fatalf("signer after the rotation %x", got)
	}
	h2 := mk()
	if err := e.prepareSortition(sc, h2, parent); err != nil {
		t.Fatalf("prepareSortition after the rotation: %v", err)
	}
	if h2.BondKey == nil || *h2.BondKey != testKey {
		t.Fatalf("after the rotation the template names %v, want the bond key %v (the keyed draw kept)", h2.BondKey, testKey)
	}
	if h2.Round != r {
		t.Fatalf("after the rotation the round is %d, want the bonded draw's %d", h2.Round, r)
	}
}

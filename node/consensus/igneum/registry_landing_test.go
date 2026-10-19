// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package igneum

import (
	"encoding/json"
	"errors"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/trie"
)

// TestRegistryLandingByTransaction: cut B. A registration naming a registry
// key is chain-held: a transaction from the key to the registry address
// carrying a document that adds a coordinate and retires one at the notice
// lands (the menu takes it, the landing is recorded), one from another key
// is refused, one under the notice is refused, and the block dropped by a
// reorganisation takes its landing with it.
func TestRegistryLandingByTransaction(t *testing.T) {
	key, _ := crypto.GenerateKey()
	other, _ := crypto.GenerateKey()
	v := vectorConfig()
	v.Rule = "coverage"
	v.NoticeBlocks = 10
	v.Research = true
	v.RegistryKey = crypto.PubkeyToAddress(key.PublicKey)
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.registry.path = filepath.Join(t.TempDir(), "igneum-registry.json")
	chain := newFakeChain()
	chainID := big.NewInt(7310) // the fake chain's
	signer := types.LatestSignerForChainID(chainID)
	mk := func(height uint64, doc *ctypes.IgneumVector, k interface{}) *types.Block {
		raw, _ := json.Marshal(doc)
		inner := &types.LegacyTx{Nonce: 0, To: &VectorRegistryAddress, Gas: 200000, GasPrice: big.NewInt(1), Data: RegistryTxData(raw)}
		var tx *types.Transaction
		if k == key {
			tx = types.MustSignNewTx(key, signer, inner)
		} else {
			tx = types.MustSignNewTx(other, signer, inner)
		}
		h := &types.Header{Number: new(big.Int).SetUint64(height), Time: height * 10, Difficulty: big.NewInt(1), Extra: []byte{byte(height)}}
		return types.NewBlock(h, []*types.Transaction{tx}, nil, nil, trie.NewStackTrie(nil))
	}
	// The succession: coordinate 3 "graph" activates at 121, coordinate 1
	// retires there with 3 its successor; the landing at 100 with the
	// notice 10 admits it (121 > 100 + 10).
	three := uint64(3)
	doc := vectorConfig()
	doc.Rule = "coverage"
	doc.NoticeBlocks = 10
	doc.Research = true
	doc.RegistryKey = v.RegistryKey
	doc.Coordinates = append(doc.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000, Activation: 121})
	doc.Coordinates[1].RetiredFrom = 121
	doc.Coordinates[1].Successor = &three
	// An unprotected (pre-EIP-155) transaction from the key: refused
	// (replayable onto every chain naming the key); one protected for
	// another chain id likewise.
	{
		raw, _ := json.Marshal(doc)
		inner := &types.LegacyTx{Nonce: 0, To: &VectorRegistryAddress, Gas: 200000, GasPrice: big.NewInt(1), Data: RegistryTxData(raw)}
		unprotected := types.MustSignNewTx(key, types.HomesteadSigner{}, inner)
		otherChain := types.MustSignNewTx(key, types.LatestSignerForChainID(big.NewInt(7358)), inner)
		for _, tx := range []*types.Transaction{unprotected, otherChain} {
			h := &types.Header{Number: big.NewInt(100), Time: 1000, Difficulty: big.NewInt(1), Extra: []byte{0x77}}
			e.LandRegistry(chain, types.NewBlock(h, []*types.Transaction{tx}, nil, nil, trie.NewStackTrie(nil)))
			if m.Count() != 3 || len(e.VectorLandings()) != 0 {
				t.Fatalf("a replayable registry transaction landed (protected %v, chain id %v)", tx.Protected(), tx.ChainId())
			}
		}
	}
	// From another key: refused.
	e.LandRegistry(chain, mk(100, doc, other))
	if m.Count() != 3 || len(e.VectorLandings()) != 0 {
		t.Fatalf("a document from another key landed: %d coordinates, %d landings", m.Count(), len(e.VectorLandings()))
	}
	// Under the notice (activation 105 at a landing of 100): refused.
	under := *doc
	under.Coordinates = append([]ctypes.IgneumCoordinate(nil), doc.Coordinates...)
	under.Coordinates[3].Activation = 105
	under.Coordinates[1].RetiredFrom = 105
	e.LandRegistry(chain, mk(100, &under, key))
	if m.Count() != 3 || len(e.VectorLandings()) != 0 {
		t.Fatalf("a document under the notice landed: %d coordinates", m.Count())
	}
	// The right one lands.
	b := mk(100, doc, key)
	e.LandRegistry(chain, b)
	if m.Count() != 4 || len(e.VectorLandings()) != 1 {
		t.Fatalf("the document did not land: %d coordinates, %d landings", m.Count(), len(e.VectorLandings()))
	}
	if !m.ActiveAt(3, 121) || m.ActiveAt(3, 120) || m.ActiveAt(1, 121) || !m.ActiveAt(1, 120) {
		t.Fatal("the landed succession's activation and retirement")
	}
	r := m.Registration()
	if r.RegistryKey != v.RegistryKey || r.Coordinates[1].Successor == nil || *r.Coordinates[1].Successor != 3 {
		t.Fatal("the landed registration's fields")
	}
	// Landing the same block again changes nothing.
	e.LandRegistry(chain, b)
	if len(e.VectorLandings()) != 1 {
		t.Fatal("a block landed twice")
	}
	// A configuration re-registration against a chain-held registration is
	// not this test's (eth/backend.go refuses it); the admission's own
	// immutability: the key may not change.
	changed := *doc
	changed.RegistryKey = crypto.PubkeyToAddress(other.PublicKey)
	if _, err := Reregistration(doc, &changed, 150); err == nil {
		t.Fatal("a changed registry key admitted")
	}
	// The reorganisation drops the block: the landing goes with it.
	e.UnlandRegistry(b)
	if m.Count() != 3 || len(e.VectorLandings()) != 0 {
		t.Fatalf("the dropped block's landing stayed: %d coordinates, %d landings", m.Count(), len(e.VectorLandings()))
	}
}

// TestUnpaidLaneTakesNoShare: a share on a coordinate whose pay weight is
// 0 at its height is refused before acceptance (the project's ruling, 15 October
// 2026), accepted again where the weight is above 0.
func TestUnpaidLaneTakesNoShare(t *testing.T) {
	v := vectorConfig()
	v.Rule = "coverage"
	v.PayShares = []uint64{1, 1, 1}
	v.PayChanges = []ctypes.IgneumPayChange{{Height: 5, PayShares: []uint64{0, 1, 1}}, {Height: 50, PayShares: []uint64{1, 1, 1}}}
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	m.SetSealCoordinate(0)
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	parts.Authority = &AuthorityParams{Height: 10, Registry: testRegistry, Cap: testCap, RegistryVersion: igneumreg.Version, BRefBps: 1600, SRef: big.NewInt(1_000_000)}
	parts.Sortition = &SortitionParams{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 6, MaxRound: 16, UnbondedRound: 17, MaxPayouts: 256, OpenSeat: true, SharesPerBlock: 4, MaxShares: 8}
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.SetCoordinate(0)
	chain := newFakeChain()
	parent := &types.Header{Number: big.NewInt(9), Difficulty: big.NewInt(1), Time: 90}
	chain.add(parent)
	a := common.Address{0x42}
	if ok, reason := e.SubmitShareOn(chain, a, 1, 1, common.Hash{}, 10, parent.Hash()); ok || reason != errShareLaneUnpaid.Error() {
		t.Fatalf("a share on the unpaid lane at 10: ok %v, %q", ok, reason)
	}
}

// TestRegistryKeyRotation: cut B's public-chain residual. The current key
// names the next key and its height under the notice; before the height
// the next key is refused and the current admitted; from the height the
// next key signs and the current is refused; the notice floor of 64 on a
// chain-held registration not marked research.
func TestRegistryKeyRotation(t *testing.T) {
	key, _ := crypto.GenerateKey()
	next, _ := crypto.GenerateKey()
	v := vectorConfig()
	v.Rule = "coverage"
	v.NoticeBlocks = 10
	v.Research = true
	v.RegistryKey = crypto.PubkeyToAddress(key.PublicKey)
	public := *v
	public.Research = false
	if _, err := Reregistration(vectorConfig(), &public, 1); err == nil {
		t.Fatal("a chain-held registration with notice 10 and not research admitted")
	}
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.registry.path = filepath.Join(t.TempDir(), "igneum-registry.json")
	chain := newFakeChain()
	signer := types.LatestSignerForChainID(big.NewInt(7310))
	mk := func(height uint64, doc *ctypes.IgneumVector, k interface{}) *types.Block {
		raw, _ := json.Marshal(doc)
		inner := &types.LegacyTx{Nonce: 0, To: &VectorRegistryAddress, Gas: 200000, GasPrice: big.NewInt(1), Data: RegistryTxData(raw)}
		var tx *types.Transaction
		if k == key {
			tx = types.MustSignNewTx(key, signer, inner)
		} else {
			tx = types.MustSignNewTx(next, signer, inner)
		}
		h := &types.Header{Number: new(big.Int).SetUint64(height), Time: height * 10, Difficulty: big.NewInt(1), Extra: []byte{byte(height), byte(height >> 8)}}
		return types.NewBlock(h, []*types.Transaction{tx}, nil, nil, trie.NewStackTrie(nil))
	}
	// The rotation named under the notice (from 105 at a landing of 100): refused.
	rot := *v
	rot.RegistryKeyNext = crypto.PubkeyToAddress(next.PublicKey)
	rot.RegistryKeyFrom = 105
	e.LandRegistry(chain, mk(100, &rot, key))
	if len(e.VectorLandings()) != 0 {
		t.Fatal("a rotation under the notice landed")
	}
	rot.RegistryKeyFrom = 120
	e.LandRegistry(chain, mk(100, &rot, key))
	if len(e.VectorLandings()) != 1 {
		t.Fatal("the rotation did not land")
	}
	// Before 120 the next key is refused and the current admitted.
	e.LandRegistry(chain, mk(110, &rot, next))
	if len(e.VectorLandings()) != 1 {
		t.Fatal("the next key landed before its height")
	}
	e.LandRegistry(chain, mk(111, &rot, key))
	if len(e.VectorLandings()) != 2 {
		t.Fatal("the current key refused before the rotation's height")
	}
	// From 120 the next key signs with the registration naming it as the
	// key; the current key is refused.
	after := rot
	after.RegistryKey = rot.RegistryKeyNext
	e.LandRegistry(chain, mk(121, &after, key))
	if len(e.VectorLandings()) != 2 {
		t.Fatal("the old key landed past the rotation")
	}
	e.LandRegistry(chain, mk(122, &after, next))
	if len(e.VectorLandings()) != 3 {
		t.Fatal("the next key refused at its height")
	}
	if r := m.Registration(); r.RegistryKey != crypto.PubkeyToAddress(next.PublicKey) {
		t.Fatalf("the registration in force names %s, want the next key", r.RegistryKey.Hex())
	}
}

// TestSealCoordinateAddedByRegistry: F-146 (node7 of the research chain,
// 15 October 2026). A sealer named for a coordinate a registry transaction
// added starts against the stored registration, which lacks it: under a
// chain-held registration the seal waits for the landing that holds the
// coordinate and is set by it, a dropped landing defers it again (the
// menu's context assignment tolerates the seal beyond its count), and a
// registration naming no key refuses the coordinate at once as before.
func TestSealCoordinateAddedByRegistry(t *testing.T) {
	key, _ := crypto.GenerateKey()
	v := vectorConfig()
	v.Rule = "coverage"
	v.NoticeBlocks = 10
	v.Research = true
	v.RegistryKey = crypto.PubkeyToAddress(key.PublicKey)
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.registry.path = filepath.Join(t.TempDir(), "igneum-registry.json")
	if err := e.SetCoordinate(3); err != nil {
		t.Fatalf("the coordinate beyond the stored registration refused under a chain-held registration: %v", err)
	}
	if e.Coordinate() != 3 || !e.sealPending {
		t.Fatal("the deferred seal coordinate not recorded")
	}
	chain := newFakeChain()
	signer := types.LatestSignerForChainID(big.NewInt(7310))
	doc := vectorConfig()
	doc.Rule = "coverage"
	doc.NoticeBlocks = 10
	doc.Research = true
	doc.RegistryKey = v.RegistryKey
	doc.Coordinates = append(doc.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000, Activation: 121})
	raw, _ := json.Marshal(doc)
	tx := types.MustSignNewTx(key, signer, &types.LegacyTx{Nonce: 0, To: &VectorRegistryAddress, Gas: 200000, GasPrice: big.NewInt(1), Data: RegistryTxData(raw)})
	h := &types.Header{Number: big.NewInt(100), Time: 1000, Difficulty: big.NewInt(1), Extra: []byte{0x64}}
	b := types.NewBlock(h, []*types.Transaction{tx}, nil, nil, trie.NewStackTrie(nil))
	e.LandRegistry(chain, b)
	if m.Count() != 4 || len(e.VectorLandings()) != 1 {
		t.Fatalf("the document did not land: %d coordinates, %d landings", m.Count(), len(e.VectorLandings()))
	}
	if e.sealPending || m.ByCoordinate(3) == nil {
		t.Fatal("the landing that holds coordinate 3 did not set the seal")
	}
	// The reorganisation drops the landing: the seal is deferred again and
	// the menu returns to three coordinates without a fault.
	e.UnlandRegistry(b)
	if m.Count() != 3 || !e.sealPending {
		t.Fatalf("the dropped landing left the seal set beyond the registration in force: %d coordinates, pending %v", m.Count(), e.sealPending)
	}
	e.LandRegistry(chain, b)
	if m.Count() != 4 || e.sealPending {
		t.Fatal("the re-landed registration did not set the seal again")
	}
	// A registration naming no registry key has no landings: refused now.
	plain, err := vector.New(vectorConfig(), pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	pe, err := New(Config{Algorithm: plain, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer pe.Close()
	if err := pe.SetCoordinate(3); !errors.Is(err, errVectorCoord) {
		t.Fatalf("a coordinate beyond a registration naming no key: %v, want errVectorCoord", err)
	}
}

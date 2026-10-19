package igneum

import (
	"crypto/ed25519"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
)

// endorseEngine is the genesis form with the settlement rule at E_att 32,
// a signer, a bonded key with the quarter cap in units in the live table.
func endorseEngine(t *testing.T) (*Engine, *stateChainE, common.Hash) {
	t.Helper()
	e := genesisEngine(t)
	e.config.Parts.Sortition.EndorseExpected = 32
	e.config.Parts.Sortition.MaxEndorsements = 128
	seed := make([]byte, 32)
	seed[0] = 3
	if err := e.SetSigner(seed); err != nil {
		t.Fatal(err)
	}
	key := *e.KeyedBondKey()
	st := nativeState(t, big.NewInt(1_000_000_000))
	st.SetState(testRegistry, igneumreg.SlotFloor, common.BigToHash(big.NewInt(160_000)))
	bondNative(t, st, testHolder, key, 40_000)
	// The signer registered on the bond (the set-signer form).
	signer := *e.SignerKey()
	if _, err := igneumreg.Run(st, testRegistry, testHolder, testHolder, uint256.NewInt(0), append(key[:], signer[:]...)); err != nil {
		t.Fatalf("set signer: %v", err)
	}
	chain := &stateChainE{fakeChain: newFakeChain(), st: st}
	e.anchor(chain)
	return e, chain, key
}

type stateChainE struct {
	*fakeChain
	st *state.StateDB
}

func (c *stateChainE) StateAt(root common.Hash) (*state.StateDB, error) { return c.st, nil }

func TestEndorsementsLotEntriesAndValidation(t *testing.T) {
	e, chain, key := endorseEngine(t)
	defer e.Close()
	st := chain.st
	s := e.config.Parts.Sortition
	// Epoch 0's live table: the key holds min(40,000 / u, 16,384) units.
	table, err := e.tableAt(st, s.EpochAt(50))
	if err != nil {
		t.Fatal(err)
	}
	units := e.unitsAt(st, table, key)
	if units != MaxUnitsPerKey {
		t.Fatalf("units %d, want %d", units, MaxUnitsPerKey)
	}
	// The lot: at E_att 32 over 16,384 of 65,536 units the expected hits per
	// height are 8; over 200 heights the mean reads 8 within a wide band.
	hits := 0
	for h := uint64(1); h <= 200; h++ {
		for j := uint64(0); j < units; j++ {
			if EndorseHits(table.seed, key, j, h, s.EndorseExpected) {
				hits++
			}
		}
	}
	if mean := float64(hits) / 200; mean < 6 || mean > 10 {
		t.Fatalf("endorsers per height %.2f, want about 8", mean)
	}
	// An endorsement of block 50 (parent hash of the carrying block 51).
	parentHash := common.HexToHash("0x5050")
	msg := EndorseMessage(e.endorse.chainIDValue(), 50, parentHash)
	// The registry's twin of the message and of the hashes (the penalty
	// entries read them below the engine; the node team's TestRegistryHashTwins
	// is the other half).
	if string(igneumreg.EndorseMessage(e.endorse.chainIDValue(), 50, parentHash)) != string(msg) {
		t.Fatal("the registry's EndorseMessage differs from the engine's")
	}
	twin := &types.Header{Number: big.NewInt(51), ParentHash: parentHash, Difficulty: big.NewInt(1000), BondKey: &key, Round: 2, Sig: make([]byte, 64)}
	eh := types.EndorsementsHash([]byte{1})
	twin.EndorsementsHash, twin.Endorsed = &eh, 3
	if igneumreg.SealHash(twin) != e.SealHash(twin) || igneumreg.SigningHash(twin) != e.SigningHash(twin) {
		t.Fatal("the registry's seal or signing hash differs from the engine's with the endorsement fields set")
	}
	e.lock.Lock()
	signer := e.signer
	e.lock.Unlock()
	var entries []byte
	var first *Endorsement
	for j := uint64(0); j < units; j++ {
		if EndorseHits(table.seed, key, j, 50, s.EndorseExpected) {
			en := Endorsement{Key: key, Unit: uint32(j), Sig: ed25519.Sign(signer, msg)}
			if first == nil {
				first = &en
			}
			entries = append(entries, en.Bytes()...)
		}
	}
	if first == nil {
		t.Skip("no hit at height 50 for this seed (the lot is random; the mean is tested above)")
	}
	n := types.EndorsementCount(entries)
	hh := types.EndorsementsHash(entries)
	header := &types.Header{Number: big.NewInt(51), ParentHash: parentHash, EndorsementsHash: &hh, Endorsed: uint64(n), Difficulty: big.NewInt(1000)}
	// Valid: every entry passes, the count is the header's.
	if got, err := e.verifyEndorsementEntries(header, entries, st); err != nil || got != uint64(n) {
		t.Fatalf("valid list: %d %v", got, err)
	}
	// The weight: (D >> r) + Endorsed x D.
	header.Round = 1
	want := new(big.Int).Add(big.NewInt(500), new(big.Int).Mul(big.NewInt(1000), big.NewInt(int64(n))))
	if w := e.BlockWeight(header); w.Cmp(want) != 0 {
		t.Fatalf("weight %v, want %v", w, want)
	}
	// A wrong count.
	bad := types.CopyHeader(header)
	bad.Endorsed++
	if _, err := e.verifyEndorsementEntries(bad, entries, st); !errors.Is(err, errEndorseCount) {
		t.Fatalf("wrong count: %v", err)
	}
	// A duplicate entry.
	dup := append(append([]byte{}, entries...), first.Bytes()...)
	dh := types.EndorsementsHash(dup)
	bad = types.CopyHeader(header)
	bad.EndorsementsHash, bad.Endorsed = &dh, uint64(n+1)
	if _, err := e.verifyEndorsementEntries(bad, dup, st); !errors.Is(err, errEndorseDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	// A forged signature.
	forged := append([]byte{}, entries...)
	forged[len(forged)-1] ^= 1
	fh := types.EndorsementsHash(forged)
	bad = types.CopyHeader(header)
	bad.EndorsementsHash = &fh
	if _, err := e.verifyEndorsementEntries(bad, forged, st); !errors.Is(err, errEndorseBadSig) {
		t.Fatalf("forged: %v", err)
	}
	// A unit that does not hit.
	miss := uint32(0)
	for EndorseHits(table.seed, key, uint64(miss), 50, s.EndorseExpected) {
		miss++
	}
	en := Endorsement{Key: key, Unit: miss, Sig: ed25519.Sign(signer, msg)}
	one := en.Bytes()
	oh := types.EndorsementsHash(one)
	bad = &types.Header{Number: big.NewInt(51), ParentHash: parentHash, EndorsementsHash: &oh, Endorsed: 1, Difficulty: big.NewInt(1000)}
	if _, err := e.verifyEndorsementEntries(bad, one, st); !errors.Is(err, errEndorseNotDrawn) {
		t.Fatalf("not drawn: %v", err)
	}
	// The pool: the entries round-trip in deterministic order and attach to
	// a template whose parent is the endorsed block.
	if taken, err := e.SubmitEndorsements(50, parentHash, entries); err != nil || taken != n {
		t.Fatalf("submit: %d %v", taken, err)
	}
	if got := e.EndorsementsFor(50, parentHash); types.EndorsementCount(got) != n {
		t.Fatalf("pool: %d entries", types.EndorsementCount(got))
	}
	// The template is built as the worker builds it: types.NewBlock derives
	// the transaction and receipt roots onto the BLOCK's header, the caller's
	// header keeps zero roots; the attach must copy the block's (the sixth
	// development chain's blocks from 20 carried zero roots and every peer
	// refused them, 11 October 2026).
	tmpl := &types.Header{Number: big.NewInt(51), ParentHash: parentHash, Difficulty: big.NewInt(1000), GasLimit: 8_000_000}
	tx := types.NewTransaction(0, common.Address{0x77}, big.NewInt(1), 21000, big.NewInt(1), nil)
	receipt := &types.Receipt{Status: 1, CumulativeGasUsed: 21000}
	block := types.NewBlock(tmpl, []*types.Transaction{tx}, nil, []*types.Receipt{receipt}, trie.NewStackTrie(nil))
	txRoot, rcRoot := block.Header().TxHash, block.Header().ReceiptHash
	if txRoot == (common.Hash{}) || txRoot == types.EmptyTxsHash {
		t.Fatalf("the test block carries no transaction root: %x", txRoot)
	}
	block, err = e.attachEndorsements(tmpl, block, st)
	if err != nil || tmpl.EndorsementsHash == nil || tmpl.Endorsed != uint64(n) || types.EndorsementCount(block.Endorsements()) != n {
		t.Fatalf("attach: %v (header %v %d, body %d)", err, tmpl.EndorsementsHash, tmpl.Endorsed, types.EndorsementCount(block.Endorsements()))
	}
	if block.Header().TxHash != txRoot || block.Header().ReceiptHash != rcRoot {
		t.Fatalf("the attach lost the block's roots: tx %x (want %x), receipts %x (want %x)", block.Header().TxHash, txRoot, block.Header().ReceiptHash, rcRoot)
	}
	if block.Header().EndorsementsHash == nil || *block.Header().EndorsementsHash != *tmpl.EndorsementsHash || block.Header().Endorsed != uint64(n) {
		t.Fatal("the block's header does not carry the endorsement fields")
	}
	// A chain without the rule refuses any list.
	e.config.Parts.Sortition.EndorseExpected = 0
	if _, err := e.verifyEndorsementEntries(header, entries, st); !errors.Is(err, errEndorseOff) {
		t.Fatalf("rule off: %v", err)
	}
}

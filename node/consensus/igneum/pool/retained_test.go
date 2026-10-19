// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// A pool with a few admitted records under the digest double: two shields
// in "block 1", a spend in "block 2", a block that appended nothing; the
// retained sets as a pruning node would extract them.
func retainedFixture(t *testing.T) (*State, []*RetainedBlock) {
	t.Helper()
	st := testState(t)
	p := Open(st, Address(7403), 7403, Poseidon2{})
	v := UnsafeDigestDouble{}
	st.AddBalance(p.addr, uint256.NewInt(2000))
	var blocks []*RetainedBlock
	mk := func(kind byte, data []byte, pos uint64) Retained {
		retained, proof, k, ok := SplitProof(data)
		if !ok || k != kind {
			t.Fatalf("the record does not split")
		}
		return Retained{TxHash: crypto.Keccak256Hash(data), Kind: kind, Position: pos, Record: append([]byte(nil), retained...), ProofHash: crypto.Keccak256Hash(proof), ProofSize: uint32(len(proof))}
	}
	// block 1: two shields
	rb := &RetainedBlock{Number: 1, Position: 0}
	for i := 0; i < 2; i++ {
		sh := Shield{Commitment: leaf(0x10 + byte(i)), Ciphertext: bytes.Repeat([]byte{byte(0x30 + i)}, 40)}
		sh.Proof = DoubleProof(sh.Statement(7403, big.NewInt(1000)), []byte("lab"))
		idx, why := p.AdmitShield(common.Address{1}, big.NewInt(1000), sh, v)
		if why != "" {
			t.Fatalf("shield %d refused: %s", i, why)
		}
		rb.Records = append(rb.Records, mk(ShieldTag, sh.Encode(), idx))
	}
	p.RecordRoot()
	p.RecordCountAt(1)
	rb.End = p.Count()
	blocks = append(blocks, rb)
	// block 2: a spend of the two
	sp := Spend{Anchor: p.Root(), Nullifiers: [][32]byte{leaf(0xd0), leaf(0xd1)}, Outputs: []Output{{Commitment: leaf(0xe0), Ciphertext: bytes.Repeat([]byte{0x40}, 40)}, {Commitment: leaf(0xe1), Ciphertext: bytes.Repeat([]byte{0x41}, 40)}}, PublicValue: new(big.Int), Fee: big.NewInt(10)}
	sp.Proof = DoubleProof(sp.Statement(7403), []byte("lab"))
	if why := p.AdmitSpend(common.Address{1}, new(big.Int), sp, v); why != "" {
		t.Fatalf("spend refused: %s", why)
	}
	rb = &RetainedBlock{Number: 2, Position: 2, Records: []Retained{mk(SpendTag, sp.Encode(), 2)}}
	p.RecordRoot()
	p.RecordCountAt(2)
	rb.End = p.Count()
	blocks = append(blocks, rb)
	// block 3: nothing
	blocks = append(blocks, &RetainedBlock{Number: 3, Position: 4, End: 4, Records: []Retained{}})
	return p, blocks
}

func TestSplitAndPrune(t *testing.T) {
	sh := Shield{Commitment: leaf(1), Ciphertext: []byte{1, 2, 3}, Proof: bytes.Repeat([]byte{9}, 100)}
	data := sh.Encode()
	retained, proof, kind, ok := SplitProof(data)
	if !ok || kind != ShieldTag || len(proof) != 100 || len(retained) != 1+32+2+3 {
		t.Fatalf("shield split: ok %v kind %d retained %d proof %d", ok, kind, len(retained), len(proof))
	}
	pruned, ok := Prune(data)
	if !ok || len(pruned) != len(retained)+32 || !bytes.Equal(pruned[len(retained):], crypto.Keccak256(proof)) {
		t.Fatalf("shield prune")
	}
	// the pruned form still parses as a shield, its proof the 32-byte hash
	if ps, ok := ParseShield(pruned); !ok || len(ps.Proof) != 32 || ps.Commitment != sh.Commitment {
		t.Fatalf("the pruned shield does not parse")
	}
	// pruning twice is the identity
	if again, ok := Prune(pruned); !ok || len(again) != len(pruned) {
		t.Fatalf("a second prune changes the length")
	}
	sp := Spend{Anchor: leaf(2), Nullifiers: [][32]byte{leaf(3), leaf(4)}, Outputs: []Output{{Commitment: leaf(5), Ciphertext: []byte{7}}, {Commitment: leaf(6)}}, PublicValue: big.NewInt(5), Fee: big.NewInt(1), Proof: bytes.Repeat([]byte{8}, 77)}
	data = sp.Encode()
	retained, proof, kind, ok = SplitProof(data)
	if !ok || kind != SpendTag || len(proof) != 77 || len(retained) != len(data)-77 {
		t.Fatalf("spend split")
	}
	if _, _, _, ok := SplitProof([]byte{0x77, 1, 2}); ok {
		t.Fatalf("an unreadable record split")
	}
}

func TestRetainedStoreRoundTrip(t *testing.T) {
	_, blocks := retainedFixture(t)
	db := rawdb.NewMemoryDatabase()
	for _, rb := range blocks {
		rb.Hash = crypto.Keccak256Hash([]byte{byte(rb.Number)})
		if err := WriteRetainedBlock(db, rb); err != nil {
			t.Fatal(err)
		}
	}
	for _, rb := range blocks {
		got := ReadRetainedBlock(db, rb.Number, rb.Hash)
		if got == nil || !got.Equal(rb) {
			t.Fatalf("block %d does not round-trip", rb.Number)
		}
		if !HasRetainedBlock(db, rb.Number, rb.Hash) || HasRetainedBlock(db, rb.Number, common.Hash{}) {
			t.Fatalf("block %d presence", rb.Number)
		}
	}
	if ReadRetainedBlock(db, 9, common.Hash{}) != nil {
		t.Fatalf("a missing block reads")
	}
}

func TestCheckAcceptsTheChainsSets(t *testing.T) {
	p, blocks := retainedFixture(t)
	expect := uint64(0)
	for _, rb := range blocks {
		if err := Check(p, rb, expect); err != nil {
			t.Fatalf("block %d refused: %v", rb.Number, err)
		}
		expect = rb.End
	}
}

func TestCheckRefusesCorruptedSets(t *testing.T) {
	p, blocks := retainedFixture(t)
	clone := func(rb *RetainedBlock) *RetainedBlock {
		c := *rb
		c.Records = make([]Retained, len(rb.Records))
		for i, r := range rb.Records {
			c.Records[i] = r
			c.Records[i].Record = append([]byte(nil), r.Record...)
		}
		return &c
	}
	rows := []struct {
		name    string
		block   int
		expect  uint64
		corrupt func(rb *RetainedBlock)
	}{
		{"a ciphertext byte flipped", 0, 0, func(rb *RetainedBlock) { rb.Records[0].Record[40] ^= 1 }},
		{"a commitment byte flipped", 0, 0, func(rb *RetainedBlock) { rb.Records[1].Record[5] ^= 1 }},
		{"a record dropped", 0, 0, func(rb *RetainedBlock) { rb.Records = rb.Records[:1] }},
		{"a record's position moved", 0, 0, func(rb *RetainedBlock) { rb.Records[1].Position = 5 }},
		{"the block's start moved", 0, 1, func(rb *RetainedBlock) {}},
		{"the end misreported", 0, 0, func(rb *RetainedBlock) { rb.End = 3 }},
		{"a nullifier replaced", 1, 2, func(rb *RetainedBlock) { rb.Records[0].Record[34] ^= 1 }},
		{"a spend's second output ciphertext flipped", 1, 2, func(rb *RetainedBlock) { r := rb.Records[0].Record; r[len(r)-1-84-1] ^= 1 }},
		{"a record added to an empty block", 2, 4, func(rb *RetainedBlock) {
			rb.Records = append(rb.Records, blocks[1].Records[0])
			rb.Records[0].Position = 4
			rb.End = 6
		}},
		{"a record moved to the next block", 2, 4, func(rb *RetainedBlock) {
			rb.Position, rb.End = 2, 4
			rb.Records = append(rb.Records, blocks[1].Records[0])
		}},
		{"an unreadable record", 0, 0, func(rb *RetainedBlock) { rb.Records[0].Record = []byte{0x77} }},
		{"a kind its bytes do not carry", 0, 0, func(rb *RetainedBlock) { rb.Records[0].Kind = SpendTag }},
	}
	for _, row := range rows {
		rb := clone(blocks[row.block])
		row.corrupt(rb)
		if err := Check(p, rb, row.expect); err == nil {
			t.Errorf("%s: accepted", row.name)
		} else {
			t.Logf("%s: refused: %v", row.name, err)
		}
	}
	if err := Check(p, nil, 0); err == nil {
		t.Errorf("a nil block accepted")
	}
}

func TestCiphertextHashAndCountAtInState(t *testing.T) {
	p, blocks := retainedFixture(t)
	for i := uint64(0); i < p.Count(); i++ {
		if p.CiphertextHash(i) == ([32]byte{}) {
			t.Fatalf("leaf %d has no ciphertext hash", i)
		}
	}
	if p.CiphertextHash(p.Count()) != ([32]byte{}) {
		t.Fatalf("a hash past the count")
	}
	if p.CountAt(1) != 2 || p.CountAt(2) != 4 || p.CountAt(3) != 0 {
		t.Fatalf("count-at: %d %d %d", p.CountAt(1), p.CountAt(2), p.CountAt(3))
	}
	// the ciphertext hash is the keccak of the admitted ciphertext
	outs, _, _ := blocks[0].Records[0].Outputs()
	if p.CiphertextHash(0) != [32]byte(crypto.Keccak256Hash(outs[0].Ciphertext)) {
		t.Fatalf("the ciphertext hash is not the keccak of the ciphertext")
	}
}

// TestRetainedSetsWithACoinbaseNote (F-791): a block whose sealer share
// was minted as a coinbase note (coinbase.go) carries the note in the
// state and not in its transactions; Extract counts it in the block's end,
// Check accepts the chain's own set for that block and the block after it
// (which starts after the note), and refuses a set whose end leaves the
// note out, a set that claims the note's position for a record, and a
// record moved onto a block that minted nothing.
func TestRetainedSetsWithACoinbaseNote(t *testing.T) {
	p, blocks := retainedFixture(t)
	sealer := common.Address{0xcc}
	reg := testRegistration(t)
	if why := p.AdmitRegistration(sealer, nil, reg); why != "" {
		t.Fatal(why)
	}
	pk, _ := p.PayoutKey(sealer)
	// block 4: one shield, then the coinbase note after it
	sh := Shield{Commitment: leaf(0x20), Ciphertext: bytes.Repeat([]byte{0x50}, 40)}
	sh.Proof = DoubleProof(sh.Statement(7403, big.NewInt(100)), []byte("lab"))
	idx, why := p.AdmitShield(common.Address{1}, big.NewInt(100), sh, UnsafeDigestDouble{})
	if why != "" {
		t.Fatal(why)
	}
	if _, _, ok := p.Mint(sealer, pk, big.NewInt(7), 4, common.Hash{4}); !ok {
		t.Fatal("the mint")
	}
	p.RecordRoot()
	p.RecordCountAt(4)
	retained, proof, _, _ := SplitProof(sh.Encode())
	rec := Retained{TxHash: crypto.Keccak256Hash(sh.Encode()), Kind: ShieldTag, Position: idx, Record: append([]byte(nil), retained...), ProofHash: crypto.Keccak256Hash(proof), ProofSize: uint32(len(proof))}
	b4 := &RetainedBlock{Number: 4, Position: 4, End: 6, Records: []Retained{rec}}
	// block 5: the note alone, no record
	if _, _, ok := p.Mint(sealer, pk, big.NewInt(7), 5, common.Hash{5}); !ok {
		t.Fatal("the second mint")
	}
	p.RecordRoot()
	p.RecordCountAt(5)
	b5 := &RetainedBlock{Number: 5, Position: 6, End: 7, Records: []Retained{}}
	// block 6: a spend after the notes
	sp := Spend{Anchor: p.Root(), Nullifiers: [][32]byte{leaf(0xd4), leaf(0xd5)}, Outputs: []Output{{Commitment: leaf(0xe4), Ciphertext: bytes.Repeat([]byte{0x44}, 40)}, {Commitment: leaf(0xe5), Ciphertext: bytes.Repeat([]byte{0x45}, 40)}}, PublicValue: new(big.Int), Fee: big.NewInt(1)}
	sp.Proof = DoubleProof(sp.Statement(7403), []byte("lab"))
	if why := p.AdmitSpend(common.Address{1}, new(big.Int), sp, UnsafeDigestDouble{}); why != "" {
		t.Fatal(why)
	}
	p.RecordRoot()
	p.RecordCountAt(6)
	retained, proof, _, _ = SplitProof(sp.Encode())
	b6 := &RetainedBlock{Number: 6, Position: 7, End: 9, Records: []Retained{{TxHash: crypto.Keccak256Hash(sp.Encode()), Kind: SpendTag, Position: 7, Record: append([]byte(nil), retained...), ProofHash: crypto.Keccak256Hash(proof), ProofSize: uint32(len(proof))}}}

	// Extract counts the note: the chain's own sets accepted in sequence.
	expect := uint64(0)
	for _, rb := range append(blocks, b4, b5, b6) {
		if err := Check(p, rb, expect); err != nil {
			t.Fatalf("block %d refused: %v", rb.Number, err)
		}
		expect = rb.End
	}
	if p.CountAt(4) != 6 || p.CountAt(5) != 7 || p.CountAt(6) != 9 {
		t.Fatalf("count-at: %d %d %d", p.CountAt(4), p.CountAt(5), p.CountAt(6))
	}
	// Extract on a fresh pool: a shield carried by a transaction, then the
	// block's coinbase note; the end is past the note and Check takes it.
	q := Open(testState(t), Address(7405), 7405, Poseidon2{})
	q.st.AddBalance(q.addr, uint256.NewInt(100))
	if why := q.AdmitRegistration(sealer, nil, reg); why != "" {
		t.Fatal(why)
	}
	sh2 := Shield{Commitment: leaf(0x21), Ciphertext: bytes.Repeat([]byte{0x51}, 40)}
	sh2.Proof = DoubleProof(sh2.Statement(7405, big.NewInt(100)), []byte("lab"))
	if _, why := q.AdmitShield(common.Address{1}, big.NewInt(100), sh2, UnsafeDigestDouble{}); why != "" {
		t.Fatal(why)
	}
	if _, _, ok := q.Mint(sealer, pk, big.NewInt(7), 1, common.Hash{1}); !ok {
		t.Fatal("the mint on the fresh pool")
	}
	q.RecordRoot()
	q.RecordCountAt(1)
	to := Address(7405)
	tx := types.NewTx(&types.LegacyTx{To: &to, Data: sh2.Encode()})
	got := Extract(q, nil, 1, common.Hash{1}, []*types.Transaction{tx})
	if len(got.Records) != 1 || got.Position != 0 || got.End != 2 {
		t.Fatalf("Extract with the note: records %d position %d end %d", len(got.Records), got.Position, got.End)
	}
	if err := Check(q, got, 0); err != nil {
		t.Fatalf("the extracted set with the note refused: %v", err)
	}

	// The lying forms.
	short := *b4
	short.End = 5 // the note left out
	if err := Check(p, &short, 4); err == nil {
		t.Error("a block whose end leaves the coinbase note out: accepted")
	}
	taken := *b5
	taken.Records = []Retained{rec}
	taken.Records[0].Position = 6 // a record claiming the note's position
	if err := Check(p, &taken, 6); err == nil {
		t.Error("a record at the coinbase note's position: accepted")
	}
	moved := *b6
	moved.Position, moved.End = 6, 8 // the spend moved one back, onto the note
	if err := Check(p, &moved, 6); err == nil {
		t.Error("a record moved onto the coinbase note's position: accepted")
	}
	none := *b5
	none.End = 6 // a block that minted, served as empty with no end
	if err := Check(p, &none, 6); err == nil {
		t.Error("a block that minted, served as nothing: accepted")
	}
}

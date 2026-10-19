// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

func testState(t testing.TB) *state.StateDB {
	st, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func leaf(i byte) [32]byte { var l [32]byte; l[31] = i; l[0] = 0xaa; return l }

// rootOf computes a tree's root the slow way: every leaf laid out, every
// level hashed in full.
func rootOf(h Hasher, leaves [][32]byte) [32]byte {
	z := Zeros(h)
	level := append([][32]byte(nil), leaves...)
	for d := 0; d < Depth; d++ {
		if len(level)%2 == 1 {
			level = append(level, z[d])
		}
		next := make([][32]byte, 0, len(level)/2)
		for i := 0; i < len(level); i += 2 {
			next = append(next, h.Node(level[i], level[i+1]))
		}
		if len(next) == 0 {
			next = append(next, z[d+1])
		}
		level = next
	}
	return level[0]
}

// TestAppendAndRoot: the incremental tree's root equals the full tree's
// after every append (the frontier's skipped writes supersede nothing),
// the empty root is Zeros[Depth], the leaves read back by index, the
// count and the root sequence advance by the rule.
func TestAppendAndRoot(t *testing.T) {
	st := testState(t)
	p := Open(st, Address(7402), 7402, Poseidon2{})
	if p.Root() != p.EmptyRoot() || p.Root() != rootOf(Poseidon2{}, nil) {
		t.Fatal("the empty tree's root is Zeros[Depth]")
	}
	var leaves [][32]byte
	for i := byte(1); i <= 37; i++ {
		if idx, why := p.AdmitShield(common.Address{1}, big.NewInt(int64(i)), shieldOf(p, leaf(i), big.NewInt(int64(i))), UnsafeDigestDouble{}); why != "" || idx != uint64(i-1) {
			t.Fatalf("shield %d: index %d why %q", i, idx, why)
		}
		leaves = append(leaves, leaf(i))
		if p.Root() != rootOf(Poseidon2{}, leaves) {
			t.Fatalf("root after %d appends differs from the full tree's", i)
		}
		if p.Leaf(uint64(i-1)) != leaf(i) {
			t.Fatalf("leaf %d does not read back", i-1)
		}
	}
	if p.Count() != 37 || p.Seq() != 0 {
		t.Fatalf("count %d seq %d", p.Count(), p.Seq())
	}
	if !p.RecordRoot() || p.Seq() != 1 || p.AnchorSeq(p.Root()) != 1 || !p.AnchorValid(p.Root()) {
		t.Fatal("the root is recorded at sequence 1")
	}
	if p.RecordRoot() {
		t.Fatal("a block that appended nothing records no root")
	}
	// Reopened from the state, the same root and count.
	q := Open(st, Address(7402), 7402, Poseidon2{})
	if q.Root() != p.Root() || q.Count() != 37 || q.Turnstile().Int64() != 37*38/2 {
		t.Fatalf("reopened: root %x count %d turnstile %v", q.Root(), q.Count(), q.Turnstile())
	}
}

// shieldOf is a shield record under the double (the tests' proofs).
func shieldOf(p *State, cm [32]byte, v *big.Int) Shield {
	s := Shield{Commitment: cm, Ciphertext: []byte("ct")}
	s.Proof = DoubleProof(s.Statement(p.ChainID(), v), []byte("lab"))
	return s
}

func shieldNote(t *testing.T, p *State, sender common.Address, v int64, pk, r [32]byte) (cm [32]byte, index uint64) {
	cm = p.Hasher().Commitment(big.NewInt(v), pk, r)
	p.st.AddBalance(p.addr, uint256.NewInt(uint64(v)))
	index, why := p.AdmitShield(sender, big.NewInt(v), shieldOf(p, cm, big.NewInt(v)), UnsafeDigestDouble{})
	if why != "" {
		t.Fatal(why)
	}
	return cm, index
}

// spendOf is a spend record under the double; a spend names two
// nullifiers and two outputs, so one of each is padded with a fixed
// dummy when the caller gives one.
func spendOf(p *State, anchor [32]byte, nfs [][32]byte, outs []Output, vpub, fee int64, to common.Address) Spend {
	for len(nfs) < 2 {
		nfs = append(nfs, leaf(0xd0+byte(len(nfs))))
	}
	for len(outs) < 2 {
		outs = append(outs, Output{Commitment: leaf(0xe0 + byte(len(outs)))})
	}
	s := Spend{Anchor: anchor, Nullifiers: nfs, Outputs: outs, PublicValue: big.NewInt(vpub), Fee: big.NewInt(fee), PublicOutput: to}
	s.Proof = DoubleProof(s.Statement(p.ChainID()), []byte("lab"))
	return s
}

// TestNullifierReplayRefused: a nullifier spends once; the same nullifier
// in a later spend, in the same record twice, is refused, and a refusal
// writes nothing.
func TestNullifierReplayRefused(t *testing.T) {
	st := testState(t)
	p := Open(st, Address(7402), 7402, Poseidon2{})
	nk, pk, r := leaf(9), leaf(8), leaf(7)
	cm, idx := shieldNote(t, p, common.Address{1}, 1000, pk, r)
	p.RecordRoot()
	nf := p.Hasher().Nullifier(nk, cm, idx)
	out := Output{Commitment: leaf(2)}
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, p.Root(), [][32]byte{nf, leaf(0xc1)}, []Output{out}, 0, 0, common.Address{}), UnsafeDigestDouble{}); why != "" {
		t.Fatal(why)
	}
	p.RecordRoot()
	before := st.IntermediateRoot(true)
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, p.Root(), [][32]byte{nf, leaf(0xc2)}, nil, 0, 0, common.Address{}), UnsafeDigestDouble{}); why != "nullifier 0 is spent" {
		t.Fatalf("the replay is refused: %q", why)
	}
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, p.Root(), [][32]byte{leaf(3), leaf(3)}, nil, 0, 0, common.Address{}), UnsafeDigestDouble{}); why != "a nullifier is named twice" {
		t.Fatalf("the double naming is refused: %q", why)
	}
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, p.Root(), [][32]byte{leaf(3), leaf(4), leaf(5)}, nil, 0, 0, common.Address{}), UnsafeDigestDouble{}); why != "the statement takes two inputs and two outputs" {
		t.Fatalf("three nullifiers are refused: %q", why)
	}
	var notWord [32]byte
	for i := range notWord {
		notWord[i] = 0xff
	}
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, p.Root(), [][32]byte{leaf(3), notWord}, nil, 0, 0, common.Address{}), UnsafeDigestDouble{}); why != "nullifier 1 is not a word" {
		t.Fatalf("a non-word nullifier is refused: %q", why)
	}
	if _, why := p.AdmitShield(common.Address{1}, big.NewInt(1), shieldOf(p, notWord, big.NewInt(1)), UnsafeDigestDouble{}); why != "the commitment is not a word" {
		t.Fatalf("a non-word commitment is refused: %q", why)
	}
	if _, why := p.AdmitShield(common.Address{1}, big.NewInt(1), Shield{Commitment: leaf(5)}, UnsafeDigestDouble{}); why != "the proof is refused: the proof is shorter than its digest" {
		t.Fatalf("a shield without a proof is refused: %q", why)
	}
	if st.IntermediateRoot(true) != before {
		t.Fatal("a refusal wrote state")
	}
}

// TestStaleRootRefused: a spend against a root older than the last
// RecentRoots recorded is refused, one inside the window admitted, one
// never recorded refused.
func TestStaleRootRefused(t *testing.T) {
	st := testState(t)
	p := Open(st, Address(7402), 7402, Poseidon2{})
	nk, pk, r := leaf(9), leaf(8), leaf(7)
	cm, idx := shieldNote(t, p, common.Address{1}, 10, pk, r)
	p.RecordRoot()
	old := p.Root()
	for i := 0; i < RecentRoots-1; i++ {
		shieldNote(t, p, common.Address{1}, 1, pk, leaf(byte(i+20)))
		p.RecordRoot()
	}
	// old is the oldest of the last RecentRoots: still valid.
	nf := p.Hasher().Nullifier(nk, cm, idx)
	if !p.AnchorValid(old) {
		t.Fatal("the root RecentRoots-1 blocks back is inside the window")
	}
	shieldNote(t, p, common.Address{1}, 1, pk, leaf(99))
	p.RecordRoot()
	if p.AnchorValid(old) {
		t.Fatal("the root RecentRoots blocks back is outside the window")
	}
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, old, [][32]byte{nf}, nil, 0, 0, common.Address{}), UnsafeDigestDouble{}); why != "the anchor is stale (not among the last recorded roots)" {
		t.Fatalf("stale: %q", why)
	}
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, leaf(77), [][32]byte{nf}, nil, 0, 0, common.Address{}), UnsafeDigestDouble{}); why != "the anchor is no recorded root" {
		t.Fatalf("unknown: %q", why)
	}
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, p.Root(), [][32]byte{nf}, nil, 0, 0, common.Address{}), UnsafeDigestDouble{}); why != "" {
		t.Fatalf("fresh: %q", why)
	}
}

// TestValueConservation: the turnstile and the pool's balance follow every
// shield, unshield and fee to the wei; a spend that would take the
// turnstile negative is refused; the public output is paid and the fee is
// burned (credited to no one).
func TestValueConservation(t *testing.T) {
	st := testState(t)
	p := Open(st, Address(7402), 7402, Poseidon2{})
	nk, pk := leaf(9), leaf(8)
	cm1, i1 := shieldNote(t, p, common.Address{1}, 700, pk, leaf(1))
	cm2, i2 := shieldNote(t, p, common.Address{1}, 300, pk, leaf(2))
	p.RecordRoot()
	if p.Turnstile().Int64() != 1000 || st.GetBalance(p.addr).Uint64() != 1000 {
		t.Fatal("the turnstile is the shielded value")
	}
	to := common.Address{5}
	nf1, nf2 := p.Hasher().Nullifier(nk, cm1, i1), p.Hasher().Nullifier(nk, cm2, i2)
	// 1000 in: 250 to the public output, 50 fee, the rest (700) in two new notes (the proof's claim).
	outs := []Output{{Commitment: leaf(3)}, {Commitment: leaf(4)}}
	if why := p.AdmitSpend(common.Address{2}, big.NewInt(1), spendOf(p, p.Root(), [][32]byte{nf1, nf2}, outs, 250, 50, to), UnsafeDigestDouble{}); why != "a spend carries no value" {
		t.Fatalf("value on a spend: %q", why)
	}
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, p.Root(), [][32]byte{nf1, nf2}, outs, 1000, 1, to), UnsafeDigestDouble{}); why != "the turnstile would go negative" {
		t.Fatalf("over the turnstile: %q", why)
	}
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, p.Root(), [][32]byte{nf1, nf2}, outs, 250, 50, to), UnsafeDigestDouble{}); why != "" {
		t.Fatal(why)
	}
	if p.Turnstile().Int64() != 700 || st.GetBalance(p.addr).Uint64() != 700 || st.GetBalance(to).Uint64() != 250 {
		t.Fatalf("turnstile %v balance %v output %v", p.Turnstile(), st.GetBalance(p.addr), st.GetBalance(to))
	}
	if p.Count() != 4 || !p.NullifierSeen(nf1) || !p.NullifierSeen(nf2) {
		t.Fatal("the outputs appended and the nullifiers marked")
	}
	// The fee left the supply: 1000 in, 250 out to the output, 700 in the pool, 50 nowhere.
	total := new(big.Int).Add(st.GetBalance(p.addr).ToBig(), st.GetBalance(to).ToBig())
	if total.Int64() != 950 {
		t.Fatalf("the fee is burned: %v", total)
	}
}

// TestVerifierDouble: the double accepts its own proof and refuses a wrong
// digest, a proof moved to another statement and a short proof; the
// NoVerifier refuses everything, so the pool with no verifier admits no
// spend.
func TestVerifierDouble(t *testing.T) {
	s := Statement{Kind: SpendTag, ChainID: 7402, Anchor: leaf(1), Nullifiers: [][32]byte{leaf(2)}, PublicValue: big.NewInt(1), Fee: big.NewInt(2)}
	proof := DoubleProof(s, []byte("payload"))
	if err := (UnsafeDigestDouble{}).Verify(s, proof); err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), proof...)
	bad[0] ^= 1
	if err := (UnsafeDigestDouble{}).Verify(s, bad); err == nil {
		t.Fatal("a wrong digest is refused")
	}
	other := s
	other.Fee = big.NewInt(3)
	if err := (UnsafeDigestDouble{}).Verify(other, proof); err == nil {
		t.Fatal("a proof moved to another statement is refused")
	}
	other = s
	other.ChainID = 7403
	if err := (UnsafeDigestDouble{}).Verify(other, proof); err == nil {
		t.Fatal("a proof moved to another chain is refused")
	}
	other = s
	other.Kind = ShieldTag
	if err := (UnsafeDigestDouble{}).Verify(other, proof); err == nil {
		t.Fatal("a proof moved to another kind is refused")
	}
	if err := (UnsafeDigestDouble{}).Verify(s, proof[:31]); err == nil {
		t.Fatal("a short proof is refused")
	}
	if err := (NoVerifier{}).Verify(s, proof); err == nil {
		t.Fatal("no verifier refuses every proof")
	}
	st := testState(t)
	p := Open(st, Address(7402), 7402, Poseidon2{})
	cm, idx := shieldNote(t, p, common.Address{1}, 10, leaf(8), leaf(7))
	p.RecordRoot()
	nf := p.Hasher().Nullifier(leaf(9), cm, idx)
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, p.Root(), [][32]byte{nf}, nil, 0, 0, common.Address{}), nil); why != "the proof is refused: no proof verifier on this node" {
		t.Fatalf("no verifier: %q", why)
	}
}

// TestRecordRoundTrip: the two records encode and parse to themselves; a
// record past the bounds does not parse.
func TestRecordRoundTrip(t *testing.T) {
	sh := Shield{Commitment: leaf(1), Ciphertext: []byte("hello"), Proof: []byte("shieldproof")}
	got, ok := ParseShield(sh.Encode())
	if !ok || got.Commitment != sh.Commitment || string(got.Ciphertext) != "hello" || string(got.Proof) != "shieldproof" {
		t.Fatal("shield round trip")
	}
	if got.Statement(7402, big.NewInt(9)).Hash() != sh.Statement(7402, big.NewInt(9)).Hash() || got.Statement(7402, big.NewInt(9)).Hash() == sh.Statement(7402, big.NewInt(8)).Hash() {
		t.Fatal("the shield statement binds the value")
	}
	sp := Spend{Anchor: leaf(1), Nullifiers: [][32]byte{leaf(2), leaf(3)}, Outputs: []Output{{leaf(4), []byte("a")}, {leaf(5), nil}}, PublicValue: big.NewInt(77), Fee: big.NewInt(5), PublicOutput: common.Address{9}, Proof: []byte("proofbytes")}
	g, ok := ParseSpend(sp.Encode())
	if !ok || g.Anchor != sp.Anchor || len(g.Nullifiers) != 2 || g.Nullifiers[1] != leaf(3) || len(g.Outputs) != 2 || string(g.Outputs[0].Ciphertext) != "a" || g.Outputs[1].Commitment != leaf(5) || g.PublicValue.Int64() != 77 || g.Fee.Int64() != 5 || g.PublicOutput != sp.PublicOutput || string(g.Proof) != "proofbytes" {
		t.Fatalf("spend round trip: %+v", g)
	}
	if g.Statement(7402).Hash() != sp.Statement(7402).Hash() {
		t.Fatal("the statement's hash survives the round trip")
	}
	three := Spend{Anchor: leaf(1), Nullifiers: [][32]byte{leaf(1), leaf(2), leaf(3)}, PublicValue: big.NewInt(0), Fee: big.NewInt(0)}
	if _, ok := ParseSpend(three.Encode()); ok {
		t.Fatal("three nullifiers do not parse")
	}
	if _, ok := ParseShield([]byte{ShieldTag}); ok {
		t.Fatal("a shield without a commitment does not parse")
	}
}

// BenchmarkAdmitSpend is the block validation cost of one two-in two-out
// spend under the double (the proof's cost is the double's hash; the real
// verifier's is measured by the lab), on a memory state.
func BenchmarkAdmitSpend(b *testing.B) {
	st := testState(b)
	p := Open(st, Address(7402), 7402, Poseidon2{})
	nk, pk := leaf(9), leaf(8)
	var nfs [][32]byte
	for i := 0; i < 2*b.N+2; i++ {
		var r [32]byte
		r[0], r[1], r[2], r[3] = byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
		cm := p.Hasher().Commitment(big.NewInt(10), pk, r)
		st.AddBalance(p.addr, uint256.NewInt(10))
		idx, _ := p.AdmitShield(common.Address{1}, big.NewInt(10), shieldOf(p, cm, big.NewInt(10)), UnsafeDigestDouble{})
		nfs = append(nfs, p.Hasher().Nullifier(nk, cm, idx))
	}
	p.RecordRoot()
	anchor := p.Root()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var o1, o2 [32]byte
		o1[0], o1[1], o1[2], o1[3], o1[4] = 1, byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
		o2 = o1
		o2[0] = 2
		s := spendOf(p, anchor, [][32]byte{nfs[2*i], nfs[2*i+1]}, []Output{{Commitment: o1}, {Commitment: o2}}, 1, 1, common.Address{5})
		if why := p.AdmitSpend(common.Address{2}, nil, s, UnsafeDigestDouble{}); why != "" {
			b.Fatal(why)
		}
	}
}

// BenchmarkAdmitShield is one shield's cost (the append: 32 node hashes,
// the leaf and amortised two frontier slots).
func BenchmarkAdmitShield(b *testing.B) {
	st := testState(b)
	p := Open(st, Address(7402), 7402, Poseidon2{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var cm [32]byte
		cm[0], cm[1], cm[2], cm[3], cm[4] = 7, byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
		if _, why := p.AdmitShield(common.Address{1}, big.NewInt(10), shieldOf(p, cm, big.NewInt(10)), UnsafeDigestDouble{}); why != "" {
			b.Fatal(why)
		}
	}
}

// TestCiphertextBoundIsTheClueAndTheOutput (F-774): the bound is the clue
// plus the output ciphertext (768 + 1,293 = 2,061 bytes); a ciphertext of
// exactly that parses and is admitted in a shield and in a spend output,
// one byte more does not parse and is refused by the rules.
func TestCiphertextBoundIsTheClueAndTheOutput(t *testing.T) {
	if MaxCiphertext != 2061 {
		t.Fatalf("MaxCiphertext = %d, the clue and the output ciphertext are 2,061", MaxCiphertext)
	}
	at := make([]byte, MaxCiphertext)
	over := make([]byte, MaxCiphertext+1)
	sh := Shield{Commitment: leaf(1), Ciphertext: at, Proof: []byte("p")}
	if g, ok := ParseShield(sh.Encode()); !ok || len(g.Ciphertext) != MaxCiphertext {
		t.Fatal("a shield ciphertext at the bound parses")
	}
	sh.Ciphertext = over
	if _, ok := ParseShield(sh.Encode()); ok {
		t.Fatal("a shield ciphertext one byte past the bound does not parse")
	}
	sp := Spend{Anchor: leaf(1), Nullifiers: [][32]byte{leaf(2), leaf(3)}, Outputs: []Output{{leaf(4), at}, {leaf(5), at}}, PublicValue: big.NewInt(0), Fee: big.NewInt(0)}
	if g, ok := ParseSpend(sp.Encode()); !ok || len(g.Outputs[0].Ciphertext) != MaxCiphertext || len(g.Outputs[1].Ciphertext) != MaxCiphertext {
		t.Fatal("spend output ciphertexts at the bound parse")
	}
	sp.Outputs[1].Ciphertext = over
	if _, ok := ParseSpend(sp.Encode()); ok {
		t.Fatal("a spend output ciphertext one byte past the bound does not parse")
	}

	p := Open(testState(t), Address(7402), 7402, Poseidon2{})
	cm := p.Hasher().Commitment(big.NewInt(10), leaf(8), leaf(7))
	p.st.AddBalance(p.addr, uint256.NewInt(10))
	s := shieldOf(p, cm, big.NewInt(10))
	s.Ciphertext = over
	if _, why := p.AdmitShield(common.Address{1}, big.NewInt(10), s, UnsafeDigestDouble{}); why == "" {
		t.Fatal("the rules admit a shield ciphertext past the bound")
	}
	s.Ciphertext = at
	if _, why := p.AdmitShield(common.Address{1}, big.NewInt(10), s, UnsafeDigestDouble{}); why != "" {
		t.Fatalf("the rules refuse a shield ciphertext at the bound: %s", why)
	}
}

// TestSpendRefusesThePoolAsPublicOutput (F-775): a spend whose public
// output is the pool address would credit the value back above the
// turnstile; it is refused, whether or not it carries a public value, and a
// refusal writes nothing.
func TestSpendRefusesThePoolAsPublicOutput(t *testing.T) {
	st := testState(t)
	p := Open(st, Address(7402), 7402, Poseidon2{})
	cm, idx := shieldNote(t, p, common.Address{1}, 100, leaf(8), leaf(7))
	p.RecordRoot()
	nf := p.Hasher().Nullifier(leaf(9), cm, idx)
	for _, vpub := range []int64{40, 0} {
		before := st.IntermediateRoot(true)
		sp := spendOf(p, p.Root(), [][32]byte{nf}, nil, vpub, 0, p.addr)
		if why := p.AdmitSpend(common.Address{2}, nil, sp, UnsafeDigestDouble{}); why != "the pool address is no public output" {
			t.Fatalf("public value %d to the pool address: %q", vpub, why)
		}
		if st.IntermediateRoot(true) != before || p.NullifierSeen(nf) {
			t.Fatalf("public value %d: the refusal wrote state", vpub)
		}
	}
	to := common.Address{0x77}
	if why := p.AdmitSpend(common.Address{2}, nil, spendOf(p, p.Root(), [][32]byte{nf}, nil, 40, 0, to), UnsafeDigestDouble{}); why != "" {
		t.Fatalf("an ordinary public output is admitted: %q", why)
	}
	if st.GetBalance(p.addr).Uint64() != 60 || p.Turnstile().Uint64() != 60 {
		t.Fatal("the balance still equals the turnstile after the spend")
	}
}

// TestBurnStray (F-775): the balance above the turnstile is the stray
// value; burning it leaves the balance at the turnstile; nothing is burned
// at or under it.
func TestBurnStray(t *testing.T) {
	st := testState(t)
	p := Open(st, Address(7402), 7402, Poseidon2{})
	shieldNote(t, p, common.Address{1}, 10, leaf(8), leaf(7))
	if v := BurnStray(st, p.addr); v.Sign() != 0 || st.GetBalance(p.addr).Uint64() != 10 {
		t.Fatalf("nothing stray: burned %v balance %v", v, st.GetBalance(p.addr))
	}
	st.AddBalance(p.addr, uint256.NewInt(7)) // a contract's internal call
	if StrayValue(st, p.addr).Uint64() != 7 {
		t.Fatalf("stray %v", StrayValue(st, p.addr))
	}
	if v := BurnStray(st, p.addr); v.Uint64() != 7 || st.GetBalance(p.addr).Uint64() != 10 || p.Turnstile().Uint64() != 10 {
		t.Fatalf("burned %v balance %v turnstile %v", v, st.GetBalance(p.addr), p.Turnstile())
	}
}

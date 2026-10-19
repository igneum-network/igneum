// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"crypto/mlkem"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

func testRegistration(t *testing.T) Registration {
	t.Helper()
	dk, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	r := Registration{EK: dk.EncapsulationKey().Bytes()}
	r.PK = WordBytes(Word{1, 2, 3, 4})
	r.D[0] = 7
	return r
}

// TestRegistrationRecord: the record round-trips at 1 + 1,227 bytes; the
// tag alone is the clearing form; a wrong length is not a record.
func TestRegistrationRecord(t *testing.T) {
	r := testRegistration(t)
	enc := r.Encode()
	if len(enc) != 1+AddressBytes || enc[0] != RegisterTag {
		t.Fatalf("encoded %d bytes, tag %x", len(enc), enc[0])
	}
	got, ok := ParseRegistration(enc)
	if !ok || got.PK != r.PK || string(got.EK) != string(r.EK) || got.D != r.D || got.Empty() {
		t.Fatal("the record does not round-trip")
	}
	if e, ok := ParseRegistration([]byte{RegisterTag}); !ok || !e.Empty() || len(e.Encode()) != 1 {
		t.Fatal("the tag alone is the clearing form")
	}
	if _, ok := ParseRegistration(enc[:len(enc)-1]); ok {
		t.Fatal("a short record parses")
	}
	if _, ok := ParseRegistration(append([]byte{ShieldTag}, enc[1:]...)); ok {
		t.Fatal("a shield tag parses as a registration")
	}
}

// TestRegistrationRules: a registration carrying value, a zero key, a key
// that is not a word, or an encapsulation key that fails FIPS 203's
// check is refused and writes nothing; a good one is stored whole and
// read back; an empty one clears it.
func TestRegistrationRules(t *testing.T) {
	st := testState(t)
	p := Open(st, Address(7405), 7405, Poseidon2{})
	acct := common.Address{0xaa}
	r := testRegistration(t)
	before := st.IntermediateRoot(true)
	if why := p.AdmitRegistration(acct, big.NewInt(1), r); why == "" {
		t.Fatal("value admitted")
	}
	bad := r
	bad.PK = [32]byte{}
	if why := p.AdmitRegistration(acct, nil, bad); why == "" {
		t.Fatal("a zero key admitted")
	}
	bad = r
	for i := range bad.PK {
		bad.PK[i] = 0xff
	}
	if why := p.AdmitRegistration(acct, nil, bad); why == "" {
		t.Fatal("a non-word key admitted")
	}
	bad = r
	bad.EK = append([]byte(nil), r.EK...)
	// the last 32 bytes of an ML-KEM-768 encapsulation key are rho; the
	// polynomial part before it must be canonical, so a coefficient past
	// q fails the modulus check
	bad.EK[0], bad.EK[1] = 0xff, 0xff
	if why := p.AdmitRegistration(acct, nil, bad); why == "" {
		t.Fatal("a malformed encapsulation key admitted")
	}
	bad.EK = r.EK[:KEMPublicKey-1]
	if why := p.AdmitRegistration(acct, nil, bad); why == "" {
		t.Fatal("a short encapsulation key admitted")
	}
	if st.IntermediateRoot(true) != before {
		t.Fatal("a refusal wrote state")
	}
	if _, ok := p.PayoutKey(acct); ok {
		t.Fatal("a key before any registration")
	}
	if why := p.AdmitRegistration(acct, new(big.Int), r); why != "" {
		t.Fatalf("refused: %s", why)
	}
	// the pool account holds no balance yet: the block's end must not
	// clear it as empty (EIP-161) with the registry in its storage
	st.IntermediateRoot(true)
	if st.GetNonce(Address(7405)) != 1 {
		t.Fatal("the pool account is not kept from the empty-account clearing")
	}
	pk, ok := p.PayoutKey(acct)
	if !ok || pk != r.PK {
		t.Fatal("the key is not stored")
	}
	got, ok := p.PayoutAddress(acct)
	if !ok || got.PK != r.PK || string(got.EK) != string(r.EK) || got.D != r.D {
		t.Fatal("the address is not stored whole")
	}
	if _, ok := p.PayoutKey(common.Address{0xbb}); ok {
		t.Fatal("another account has a key")
	}
	if why := p.AdmitRegistration(acct, nil, Registration{}); why != "" {
		t.Fatalf("the clearing form refused: %s", why)
	}
	if _, ok := p.PayoutKey(acct); ok {
		t.Fatal("the key is not cleared")
	}
}

// TestMintRandomnessIsAWordAndPublic: r is a word, deterministic in its
// public inputs, and different for every input that differs.
func TestMintRandomnessIsAWordAndPublic(t *testing.T) {
	a := MintRandomness(7405, 10, common.Hash{1}, common.Address{2})
	if !IsWord(a) {
		t.Fatal("not a word")
	}
	if a != MintRandomness(7405, 10, common.Hash{1}, common.Address{2}) {
		t.Fatal("not deterministic")
	}
	for _, b := range [][32]byte{
		MintRandomness(7406, 10, common.Hash{1}, common.Address{2}),
		MintRandomness(7405, 11, common.Hash{1}, common.Address{2}),
		MintRandomness(7405, 10, common.Hash{3}, common.Address{2}),
		MintRandomness(7405, 10, common.Hash{1}, common.Address{4}),
	} {
		if b == a {
			t.Fatal("two inputs share r")
		}
	}
}

// TestMint: a sealer with no key mints nothing; with a key the note is
// appended at the next index with the commitment H(H(v, pk), r), the
// turnstile and the minted sum rise by v, the pool address is credited
// v, and the record is read back by index and by height; the next mint
// after a shield sits after it.
func TestMint(t *testing.T) {
	st := testState(t)
	p := Open(st, Address(7405), 7405, Poseidon2{})
	sealer := common.Address{0xcc}
	v := big.NewInt(4763716106026549823)
	if _, _, ok := p.Mint(sealer, [32]byte{}, v, 5, common.Hash{9}); ok {
		t.Fatal("minted with no key")
	}
	r := testRegistration(t)
	if why := p.AdmitRegistration(sealer, nil, r); why != "" {
		t.Fatal(why)
	}
	pk, _ := p.PayoutKey(sealer)
	for _, bad := range []*big.Int{nil, new(big.Int), big.NewInt(-1), new(big.Int).Lsh(big.NewInt(1), 128)} {
		if _, _, ok := p.Mint(sealer, pk, bad, 5, common.Hash{9}); ok {
			t.Fatalf("minted %v", bad)
		}
	}
	index, cm, ok := p.Mint(sealer, pk, v, 5, common.Hash{9})
	if !ok || index != 0 {
		t.Fatalf("mint: ok %v index %d", ok, index)
	}
	want := Poseidon2{}.Commitment(v, r.PK, MintRandomness(7405, 5, common.Hash{9}, sealer))
	if cm != want || p.Leaf(0) != want || p.Count() != 1 {
		t.Fatal("the commitment is not the note's")
	}
	if p.Turnstile().Cmp(v) != 0 || p.Minted().Cmp(v) != 0 || st.GetBalance(Address(7405)).Cmp(uint256.MustFromBig(v)) != 0 {
		t.Fatalf("turnstile %v minted %v balance %v", p.Turnstile(), p.Minted(), st.GetBalance(Address(7405)))
	}
	if i, ok := p.MintAt(5); !ok || i != 0 {
		t.Fatal("the height record")
	}
	if _, ok := p.MintAt(6); ok {
		t.Fatal("a height that minted nothing has a record")
	}
	if s, got, ok := p.MintRecord(0); !ok || s != sealer || got.Cmp(v) != 0 {
		t.Fatal("the index record")
	}
	if !p.RecordRoot() || p.Seq() != 1 {
		t.Fatal("the root is not recorded")
	}
	// a shield then a second mint: the coinbase note after the record
	st.AddBalance(Address(7405), uint256.NewInt(5))
	sh := Shield{Commitment: WordBytes(Word{5, 5, 5, 5})}
	sh.Proof = DoubleProof(sh.Statement(7405, big.NewInt(5)), []byte("lab"))
	if _, why := p.AdmitShield(common.Address{1}, big.NewInt(5), sh, UnsafeDigestDouble{}); why != "" {
		t.Fatal(why)
	}
	index, _, ok = p.Mint(sealer, pk, v, 6, common.Hash{10})
	if !ok || index != 2 || p.Count() != 3 {
		t.Fatalf("the second mint: ok %v index %d count %d", ok, index, p.Count())
	}
	if _, _, ok := p.MintRecord(1); ok {
		t.Fatal("the shield's leaf has a mint record")
	}
	sum := new(big.Int).Add(v, v)
	if p.Minted().Cmp(sum) != 0 || p.Turnstile().Cmp(new(big.Int).Add(sum, big.NewInt(5))) != 0 {
		t.Fatalf("minted %v turnstile %v", p.Minted(), p.Turnstile())
	}
	if p.Turnstile().Cmp(st.GetBalance(Address(7405)).ToBig()) != 0 {
		t.Fatal("the turnstile is not the pool's balance")
	}
}

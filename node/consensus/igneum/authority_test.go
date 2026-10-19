package igneum

import (
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/asm"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

var (
	testRegistry = common.HexToAddress("0x0000000000000000000000000000000000000b0d")
	testCap      = big.NewInt(1_000_000)
	testKey      = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	testHolder   = common.HexToAddress("0x00000000000000000000000000000000000000aa")
	testOther    = common.HexToAddress("0x00000000000000000000000000000000000000bb")
)

// TestRegistryCodePinned holds the registry's source and its pinned
// compilation together.
func TestRegistryCodePinned(t *testing.T) {
	c := asm.NewCompiler(false)
	c.Feed(asm.Lex([]byte(RegistryAsm), false))
	hex, errs := c.Compile()
	if len(errs) > 0 {
		t.Fatalf("registry asm: %v", errs)
	}
	if hex != common.Bytes2Hex(RegistryCode) {
		t.Fatalf("registry code differs from the pinned bytes: compiled %s", hex)
	}
}

func registryState(t *testing.T, cap *big.Int) *state.StateDB {
	t.Helper()
	st, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatal(err)
	}
	acc := RegistryAlloc(cap, false)
	st.SetBalance(testHolder, uint256.NewInt(10_000_000))
	st.SetBalance(testOther, uint256.NewInt(10_000_000))
	st.SetCode(testRegistry, acc.Code)
	for k, v := range acc.Storage {
		st.SetState(testRegistry, k, v)
	}
	return st
}

func callRegistry(st *state.StateDB, from common.Address, key common.Hash, value *big.Int) ([]byte, error) {
	cfg := &runtime.Config{State: st, Origin: from, Value: value, GasLimit: 1_000_000}
	out, _, err := runtime.Call(testRegistry, key[:], cfg)
	return out, err
}

// TestRegistryContract exercises the bond registry: a bond records the
// caller as the account, more from the same account adds, another account
// is refused, the cap is enforced, a read returns amount and account.
func TestRegistryContract(t *testing.T) {
	st := registryState(t, testCap)
	a := &AuthorityParams{Height: 1, Registry: testRegistry, Cap: testCap}
	if _, err := callRegistry(st, testHolder, testKey, big.NewInt(600_000)); err != nil {
		t.Fatalf("first bond: %v", err)
	}
	if b := a.BondOf(st, testKey); b.Amount.Cmp(big.NewInt(600_000)) != 0 || b.Account != testHolder {
		t.Fatalf("after the first bond: %+v", b)
	}
	if _, err := callRegistry(st, testOther, testKey, big.NewInt(1)); err == nil {
		t.Fatal("another account bonded a held key")
	}
	if _, err := callRegistry(st, testHolder, testKey, big.NewInt(400_001)); err == nil {
		t.Fatal("a bond over the cap was accepted")
	}
	if _, err := callRegistry(st, testHolder, testKey, big.NewInt(400_000)); err != nil {
		t.Fatalf("bond to the cap: %v", err)
	}
	if b := a.BondOf(st, testKey); b.Amount.Cmp(testCap) != 0 {
		t.Fatalf("at the cap: %+v", b)
	}
	out, err := callRegistry(st, testOther, testKey, new(big.Int))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(out) != 64 || new(big.Int).SetBytes(out[:32]).Cmp(testCap) != 0 || common.BytesToAddress(out[32:]) != testHolder {
		t.Fatalf("read returned %x", out)
	}
	if _, err := callRegistry(st, testHolder, common.Hash{}, new(big.Int)); err != nil {
		t.Fatalf("read of an unbonded key: %v", err)
	}
	// Bad calldata reverts.
	cfg := &runtime.Config{State: st, Origin: testHolder, Value: big.NewInt(1), GasLimit: 1_000_000}
	if _, _, err := runtime.Call(testRegistry, []byte{1, 2, 3}, cfg); err == nil {
		t.Fatal("a 3-byte call was accepted")
	}
}

func authorityEngine(t *testing.T, lax bool) *Engine {
	t.Helper()
	e := newTestEngine(t)
	e.config.Parts.Authority = &AuthorityParams{Height: 10, Registry: testRegistry, Cap: testCap}
	e.SetAuthorityLax(lax)
	return e
}

func keyedHeader(n int64, key *common.Hash, coinbase common.Address) *types.Header {
	return &types.Header{Number: big.NewInt(n), Coinbase: coinbase, BondKey: key, Difficulty: big.NewInt(1)}
}

// TestVerifyAuthority walks the rule: a key before the height, no key at
// it, an unbonded key, a redirected coinbase, the valid block, the lax
// harness, and the registry's own storage as the source.
func TestVerifyAuthority(t *testing.T) {
	e := authorityEngine(t, false)
	st := registryState(t, testCap)
	if _, err := callRegistry(st, testHolder, testKey, big.NewInt(5)); err != nil {
		t.Fatal(err)
	}
	k := testKey
	cases := []struct {
		name string
		h    *types.Header
		want error
	}{
		{"early", keyedHeader(9, &k, testHolder), errBondKeyEarly},
		{"missing", keyedHeader(10, nil, testHolder), errBondKeyMissing},
		{"zero", keyedHeader(10, &common.Hash{}, testHolder), errBondKeyZero},
		{"unbonded", keyedHeader(10, func() *common.Hash { x := common.HexToHash("0x22"); return &x }(), testHolder), errBondKeyUnbonded},
		{"redirected", keyedHeader(10, &k, testOther), errBondAccount},
		{"valid", keyedHeader(10, &k, testHolder), nil},
		{"before", keyedHeader(9, nil, testOther), nil},
	}
	for _, c := range cases {
		err := e.VerifyAuthority(c.h, st)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	lax := authorityEngine(t, true)
	if err := lax.VerifyAuthority(keyedHeader(10, &k, testOther), st); err != nil {
		t.Errorf("lax node refused a redirected block: %v", err)
	}
	if err := lax.VerifyAuthority(keyedHeader(10, nil, testOther), st); !errors.Is(err, errBondKeyMissing) {
		t.Errorf("lax node took a keyless block: %v", err)
	}
	// A node without the rule refuses any keyed header.
	plain := newTestEngine(t)
	if err := plain.VerifyAuthority(keyedHeader(10, &k, testHolder), st); !errors.Is(err, errBondKeyEarly) {
		t.Errorf("a node without the rule took a keyed header: %v", err)
	}
}

// TestBondKeyHeader: the field round-trips in RLP and JSON, is absent when
// nil, and moves the seal hash (so work under one key verifies under no
// other).
func TestBondKeyHeader(t *testing.T) {
	e := newTestEngine(t)
	k := testKey
	h := keyedHeader(10, &k, testHolder)
	h.Extra = []byte{}
	enc, err := rlp.EncodeToBytes(h)
	if err != nil {
		t.Fatal(err)
	}
	var dec types.Header
	if err := rlp.DecodeBytes(enc, &dec); err != nil {
		t.Fatal(err)
	}
	if dec.BondKey == nil || *dec.BondKey != k {
		t.Fatalf("bond key lost in RLP: %v", dec.BondKey)
	}
	if dec.Hash() != h.Hash() {
		t.Fatal("hash differs after the round trip")
	}
	plain := keyedHeader(10, nil, testHolder)
	plain.Extra = []byte{}
	enc2, _ := rlp.EncodeToBytes(plain)
	var dec2 types.Header
	if err := rlp.DecodeBytes(enc2, &dec2); err != nil {
		t.Fatal(err)
	}
	if dec2.BondKey != nil {
		t.Fatal("a nil key decoded as set")
	}
	if len(enc2) >= len(enc) {
		t.Fatal("the keyless header is not shorter")
	}
	other := common.HexToHash("0x22")
	h2 := types.CopyHeader(h)
	h2.BondKey = &other
	if e.SealHash(h) == e.SealHash(h2) || e.SealHash(h) == e.SealHash(plain) {
		t.Fatal("the seal hash does not carry the key")
	}
	if cp := types.CopyHeader(h); cp.BondKey == h.BondKey || *cp.BondKey != k {
		t.Fatal("CopyHeader shares or loses the key")
	}
	js, _ := h.MarshalJSON()
	if !bytes.Contains(js, []byte(`"bondKey":"0x1111`)) {
		t.Fatalf("JSON lacks the key: %s", js)
	}
	_ = crypto.Keccak256
}

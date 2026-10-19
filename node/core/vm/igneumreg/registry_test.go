// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package igneumreg

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// memState is a StateDB over maps.
type memState struct {
	storage map[common.Address]map[common.Hash]common.Hash
	balance map[common.Address]*uint256.Int
}

func newMemState() *memState {
	return &memState{storage: map[common.Address]map[common.Hash]common.Hash{}, balance: map[common.Address]*uint256.Int{}}
}

func (m *memState) GetState(a common.Address, k common.Hash) common.Hash { return m.storage[a][k] }
func (m *memState) SetState(a common.Address, k, v common.Hash) {
	if m.storage[a] == nil {
		m.storage[a] = map[common.Hash]common.Hash{}
	}
	m.storage[a][k] = v
}
func (m *memState) GetBalance(a common.Address) *uint256.Int {
	if b := m.balance[a]; b != nil {
		return new(uint256.Int).Set(b)
	}
	return new(uint256.Int)
}
func (m *memState) SubBalance(a common.Address, v *uint256.Int) {
	m.balance[a] = new(uint256.Int).Sub(m.GetBalance(a), v)
}
func (m *memState) AddBalance(a common.Address, v *uint256.Int) {
	m.balance[a] = new(uint256.Int).Add(m.GetBalance(a), v)
}

var (
	reg    = common.HexToAddress("0x0b0d")
	fund   = common.HexToAddress("0xf0f0")
	holder = common.HexToAddress("0x1111")
	other  = common.HexToAddress("0x2222")
	thief  = common.HexToAddress("0x3333")
)

// call runs one registry call the way the EVM does: the value moves first,
// an error reverts the state (here: a copy is taken and restored).
func call(t *testing.T, st *memState, from common.Address, value int64, input []byte) ([]byte, error) {
	t.Helper()
	v := uint256.NewInt(uint64(value))
	snapStorage := map[common.Hash]common.Hash{}
	for k, val := range st.storage[reg] {
		snapStorage[k] = val
	}
	snapBal := map[common.Address]*uint256.Int{}
	for a, b := range st.balance {
		snapBal[a] = new(uint256.Int).Set(b)
	}
	st.SubBalance(from, v)
	st.AddBalance(reg, v)
	out, err := Run(st, reg, from, from, v, input)
	if err != nil {
		st.storage[reg] = snapStorage
		st.balance = snapBal
	}
	return out, err
}

func setup(cap, floor int64) *memState {
	st := newMemState()
	st.SetState(reg, SlotCap, common.BigToHash(big.NewInt(cap)))
	st.SetState(reg, SlotFloor, common.BigToHash(big.NewInt(floor)))
	st.SetState(reg, SlotFund, common.BytesToHash(fund.Bytes()))
	for _, a := range []common.Address{holder, other, thief} {
		st.balance[a] = uint256.NewInt(1_000_000)
	}
	return st
}

func TestRegistryBondCapAndList(t *testing.T) {
	st := setup(1_000_000, 0)
	var sk [32]byte
	sk[0] = 7
	key := BondKeyOf(sk)
	// Floor zero and balance zero: the cap is zero and no bond is admitted
	// (the genesis form: nothing bonds before issuance accrues).
	if _, err := call(t, st, holder, 100, key[:]); !errors.Is(err, ErrCap) {
		t.Fatalf("bond at floor zero: %v", err)
	}
	// Floor 1000: the cap is 26 percent of max(balance, floor) = 260.
	st.SetState(reg, SlotFloor, common.BigToHash(big.NewInt(1000)))
	if _, err := call(t, st, holder, 261, key[:]); !errors.Is(err, ErrCap) {
		t.Fatalf("bond over the cap: %v", err)
	}
	if _, err := call(t, st, holder, 200, key[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
	b := BondOf(st, reg, key)
	if b.Amount.Int64() != 200 || b.Account != holder || b.Exit != holder || b.Signer != (common.Hash{}) {
		t.Fatalf("bond record %+v", b)
	}
	if ks := Keys(st, reg); len(ks) != 1 || ks[0] != key {
		t.Fatalf("key list %v", ks)
	}
	// Another account cannot add to it; the account can, within the cap
	// (the balance is now 200, the floor 1000: cap 260).
	if _, err := call(t, st, other, 10, key[:]); !errors.Is(err, ErrAccount) {
		t.Fatalf("foreign top-up: %v", err)
	}
	if _, err := call(t, st, holder, 61, key[:]); !errors.Is(err, ErrCap) {
		t.Fatalf("top-up over the cap: %v", err)
	}
	if _, err := call(t, st, holder, 60, key[:]); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	// A small key is refused (slot aliasing).
	small := common.BigToHash(big.NewInt(3))
	if _, err := call(t, st, holder, 1, small[:]); !errors.Is(err, ErrKey) {
		t.Fatalf("small key: %v", err)
	}
	// The read.
	out, err := call(t, st, other, 0, key[:])
	if err != nil || len(out) != 128 || new(big.Int).SetBytes(out[:32]).Int64() != 260 {
		t.Fatalf("read: %v %x", err, out)
	}
	// Rotate the signer: the account only, bonded keys only.
	signer := common.HexToHash("0xabc")
	if _, err := call(t, st, other, 0, append(key[:], signer[:]...)); !errors.Is(err, ErrAccount) {
		t.Fatalf("foreign rotate: %v", err)
	}
	if _, err := call(t, st, holder, 0, append(key[:], signer[:]...)); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if BondOf(st, reg, key).Signer != signer {
		t.Fatal("signer not set")
	}
	// A fresh bond with an exit address.
	var sk2 [32]byte
	sk2[0] = 9
	key2 := BondKeyOf(sk2)
	exit := common.BytesToHash(other.Bytes())
	if _, err := call(t, st, holder, 100, append(key2[:], exit[:]...)); err != nil {
		t.Fatalf("bond with exit: %v", err)
	}
	if b := BondOf(st, reg, key2); b.Exit != other || b.Account != holder {
		t.Fatalf("bond with exit record %+v", b)
	}
	if n := len(Keys(st, reg)); n != 2 {
		t.Fatalf("key list %d", n)
	}
}

func TestRegistryHostileClaim(t *testing.T) {
	st := setup(1_000_000, 10_000)
	var sk [32]byte
	sk[0] = 7
	key := BondKeyOf(sk)
	if _, err := call(t, st, holder, 2000, key[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
	claim := func(secret [32]byte, to common.Address) []byte {
		in := append([]byte{}, secret[:]...)
		in = append(in, common.BytesToHash(to.Bytes()).Bytes()...)
		return append(in, make([]byte, 32)...)
	}
	// A wrong secret claims nothing.
	var wrong [32]byte
	wrong[0] = 8
	if _, err := call(t, st, thief, 0, claim(wrong, thief)); !errors.Is(err, ErrUnbonded) {
		t.Fatalf("wrong secret: %v", err)
	}
	// A claim with value, or a non-zero third word, is not an operation.
	if _, err := call(t, st, thief, 1, claim(sk, thief)); !errors.Is(err, ErrCalldata) {
		t.Fatalf("claim with value: %v", err)
	}
	bad := claim(sk, thief)
	bad[95] = 1
	if _, err := call(t, st, thief, 0, bad); !errors.Is(err, ErrClaimShape) {
		t.Fatalf("claim shape: %v", err)
	}
	// The hostile claim: anyone with the secret, to any address but the
	// exit: a quarter burnt to the fund, the rest paid, the key unbonded.
	before := st.GetBalance(thief).Uint64()
	if _, err := call(t, st, thief, 0, claim(sk, thief)); err != nil {
		t.Fatalf("hostile claim: %v", err)
	}
	if got := st.GetBalance(thief).Uint64() - before; got != 1500 {
		t.Fatalf("thief paid %d, want 1500", got)
	}
	// The quarter is BURNT: the fund receives nothing, the registry's
	// balance falls to zero, and the 500 reach no account.
	if got := st.GetBalance(fund).Uint64(); got != 0 {
		t.Fatalf("fund received %d on a burn", got)
	}
	if got := st.GetBalance(reg).Uint64(); got != 0 {
		t.Fatalf("registry keeps %d", got)
	}
	if b := BondOf(st, reg, key); b.Amount.Sign() != 0 {
		t.Fatalf("key still bonded: %+v", b)
	}
	// A second claim finds nothing.
	if _, err := call(t, st, thief, 0, claim(sk, thief)); !errors.Is(err, ErrUnbonded) {
		t.Fatalf("second claim: %v", err)
	}
	// The orderly exit: a claim to the exit address pays the whole amount.
	var sk2 [32]byte
	sk2[0] = 9
	key2 := BondKeyOf(sk2)
	if _, err := call(t, st, holder, 2000, key2[:]); err != nil {
		t.Fatalf("bond 2: %v", err)
	}
	before = st.GetBalance(holder).Uint64()
	if _, err := call(t, st, other, 0, claim(sk2, holder)); err != nil {
		t.Fatalf("orderly exit by anyone to the exit address: %v", err)
	}
	if got := st.GetBalance(holder).Uint64() - before; got != 2000 {
		t.Fatalf("exit paid %d, want 2000", got)
	}
	if got := st.GetBalance(fund).Uint64(); got != 0 {
		t.Fatalf("fund changed on the orderly exit: %d", got)
	}
}

func TestBondKeyOfIsDeterministic(t *testing.T) {
	var a, b [32]byte
	a[31], b[31] = 1, 2
	if BondKeyOf(a) == BondKeyOf(b) || BondKeyOf(a) != BondKeyOf(a) {
		t.Fatal("G is not a function of the secret")
	}
	if BondKeyOf(a).Big().Cmp(big.NewInt(minKey)) < 0 {
		t.Fatal("a derived key fell in the reserved range")
	}
}

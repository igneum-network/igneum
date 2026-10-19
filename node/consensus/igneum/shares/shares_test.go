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

package shares

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

type memState map[common.Hash]common.Hash

func (m memState) GetState(_ common.Address, k common.Hash) common.Hash    { return m[k] }
func (m memState) SetState(_ common.Address, k common.Hash, v common.Hash) { m[k] = v }

var account = common.HexToAddress("0x0b0d")

func addr(i int) common.Address { return common.BigToAddress(big.NewInt(int64(i + 1))) }

// TestTargetFromChainWork: the share target is the block target times n
// (one network-wide figure from D; no per-address difficulty), off at n
// 0, floored at difficulty 1.
func TestTargetFromChainWork(t *testing.T) {
	d := big.NewInt(1 << 40)
	if Target(d, 0) != nil || Target(nil, 128) != nil || Target(big.NewInt(0), 128) != nil {
		t.Fatal("a target where there is none")
	}
	// The rule is pow.Target(D / n) exactly: the share difficulty D / n
	// (an integer), the target 2^256 - 1 over it; n times the block
	// target differs from it only by the integer rounding (under n) and
	// is not the form.
	want := pow.Target(big.NewInt(1 << 33))
	if got := Target(d, 128); got.Cmp(want) != 0 {
		t.Fatalf("target at n 128: %v, want %v", got, want)
	}
	approx := new(big.Int).Mul(pow.Target(d), big.NewInt(128))
	if diff := new(big.Int).Sub(want, approx); diff.Sign() < 0 || diff.Cmp(big.NewInt(128)) >= 0 {
		t.Fatalf("n x the block target is off by %v", diff)
	}
	// D under n: the share difficulty floors at 1 (every hash meets it).
	if got := Target(big.NewInt(5), 128); got.Cmp(pow.MaxTarget()) != 0 {
		t.Fatalf("floor: %v", got)
	}
	var h common.Hash
	h[0] = 0x80
	if Meets(h, Target(d, 128)) || !Meets(h, pow.MaxTarget()) || Meets(h, nil) {
		t.Fatal("Meets")
	}
	// About n accepted shares per block: the share target is n times the
	// block's, so a hash that meets the block target meets the share's.
	if !Meets(common.BigToHash(pow.Target(d)), Target(d, 128)) {
		t.Fatal("a block solution is not a share")
	}
}

// TestBuildCeilingAndCarry: 300 distinct addresses in one block pay 256
// and carry 44 in order; the next block drains the carry first; nothing
// is dropped at any step (gate row 1).
func TestBuildCeilingAndCarry(t *testing.T) {
	var accepted []Entry
	for i := 0; i < 300; i++ {
		accepted = append(accepted, Entry{Address: addr(i), Weight: uint64(i + 1)})
	}
	list, carry, err := Build(nil, accepted, Ceiling)
	if err != nil || len(list) != Ceiling || len(carry) != 44 {
		t.Fatalf("build: %d paid, %d carried, %v", len(list), len(carry), err)
	}
	if list[0] != accepted[0] || list[255] != accepted[255] || carry[0] != accepted[256] || carry[43] != accepted[299] {
		t.Fatal("the order is not address order")
	}
	// Any input order gives the same list (the canonical order is by
	// address); a limit of Ceiling minus one leaves the sealer's place.
	reversed := make([]Entry, len(accepted))
	for i, e := range accepted {
		reversed[len(accepted)-1-i] = e
	}
	if l2, c2, _ := Build(nil, reversed, Ceiling); !Equal(l2, list) || !Equal(c2, carry) {
		t.Fatal("the list depends on the input order")
	}
	if l3, c3, _ := Build(nil, accepted, Ceiling-1); len(l3) != Ceiling-1 || len(c3) != 45 || c3[0] != accepted[255] {
		t.Fatal("the sealer's place")
	}
	// The carry through state and back, byte for byte.
	st := memState{}
	WriteCarry(st, account, carry)
	back, err := ReadCarry(st, account)
	if err != nil || !Equal(back, carry) {
		t.Fatalf("carry round trip: %v", err)
	}
	// The next block: ten new shares; the carry's 44 are paid first, then
	// the ten; the carry empties and its slots are cleared.
	var next []Entry
	for i := 0; i < 10; i++ {
		next = append(next, Entry{Address: addr(1000 + i), Weight: 1})
	}
	list2, carry2, err := Build(back, next, Ceiling)
	if err != nil || len(list2) != 54 || len(carry2) != 0 || list2[0] != carry[0] || list2[53] != next[9] {
		t.Fatalf("drain: %d paid, %d carried, %v", len(list2), len(carry2), err)
	}
	WriteCarry(st, account, carry2)
	if back2, err := ReadCarry(st, account); err != nil || len(back2) != 0 {
		t.Fatalf("cleared carry: %v %v", back2, err)
	}
	if st[carrySlot(1)] != (common.Hash{}) || st[carrySlot(88)] != (common.Hash{}) {
		t.Fatal("old slots not cleared")
	}
	// A share already carried adds to its carried entry (no second
	// place): the address keeps its position.
	list3, carry3, err := Build(carry, []Entry{{Address: carry[5].Address, Weight: 100}}, Ceiling)
	if err != nil || len(carry3) != 0 || list3[5].Weight != carry[5].Weight+100 || len(list3) != 44 {
		t.Fatalf("merge into the carry: %v", err)
	}
	// Every accepted weight is in the list or the carry: the sums agree.
	var in, out uint64
	for _, e := range accepted {
		in += e.Weight
	}
	for _, e := range list {
		out += e.Weight
	}
	for _, e := range carry {
		out += e.Weight
	}
	if in != out {
		t.Fatalf("weight lost: %d in, %d out", in, out)
	}
}

// TestBuildAddressSplittingAndReplay: one card's shares over two
// addresses earn the same weight as over one (and take two entries, not
// more); the same address twice in a block is one entry; a replayed share
// is the wire form's refusal (the protocol team's distinct-by-(pk, nonce, parent)
// check), so Build never sees it, and a zero weight is refused here.
func TestBuildAddressSplittingAndReplay(t *testing.T) {
	one, _, err := Build(nil, []Entry{{addr(0), 3}, {addr(0), 3}}, Ceiling)
	if err != nil || len(one) != 1 || one[0].Weight != 6 {
		t.Fatalf("one address: %v %v", one, err)
	}
	two, _, err := Build(nil, []Entry{{addr(0), 3}, {addr(1), 3}}, Ceiling)
	if err != nil || len(two) != 2 || two[0].Weight+two[1].Weight != 6 {
		t.Fatalf("two addresses: %v %v", two, err)
	}
	// The pay is the same whole: 600 over one entry of 6 or two of 3.
	p1, _ := Pay(big.NewInt(600), one)
	p2, _ := Pay(big.NewInt(600), two)
	if p1[0].Amount.Int64() != 600 || p2[0].Amount.Int64()+p2[1].Amount.Int64() != 600 {
		t.Fatal("splitting changes the pay")
	}
	if _, _, err := Build(nil, []Entry{{addr(0), 0}}, Ceiling); !errors.Is(err, ErrWeight) {
		t.Fatalf("zero weight: %v", err)
	}
	// A full carry: the next NEW address is refused; a known one is not.
	var carry []Entry
	for i := 0; i < MaxCarry; i++ {
		carry = append(carry, Entry{Address: addr(i), Weight: 1})
	}
	var block []Entry
	for i := 0; i < Ceiling; i++ {
		block = append(block, Entry{Address: addr(MaxCarry + i), Weight: 1})
	}
	if _, _, err := Build(carry, block, Ceiling); err != nil {
		t.Fatalf("carry full to the line: %v", err)
	}
	if _, _, err := Build(carry, append(block, Entry{Address: addr(MaxCarry + Ceiling), Weight: 1}), Ceiling); !errors.Is(err, ErrCarryFull) {
		t.Fatalf("carry over the line: %v", err)
	}
	if _, _, err := Build(carry, append(block, Entry{Address: addr(7), Weight: 1}), Ceiling); err != nil {
		t.Fatalf("a carried address at the line: %v", err)
	}
}

// TestPayProRataAndDust: the amount is paid whole over the weights; an
// entry whose share rounds to zero carries its weight forward.
func TestPayProRataAndDust(t *testing.T) {
	list := []Entry{{addr(0), 1}, {addr(1), 2}, {addr(2), 7}}
	paid, dust := Pay(big.NewInt(1000), list)
	if len(dust) != 0 || len(paid) != 3 || paid[0].Amount.Int64() != 100 || paid[1].Amount.Int64() != 200 || paid[2].Amount.Int64() != 700 {
		t.Fatalf("pro rata: %v %v", paid, dust)
	}
	// 1001 over 10: the remainder's 1 to the last paid entry.
	paid, _ = Pay(big.NewInt(1001), list)
	if paid[2].Amount.Int64() != 701 {
		t.Fatalf("remainder: %v", paid)
	}
	// Dust: 5 over weights 1, 2, 7 pays 0, 1, 3 plus the remainder 1 to
	// the last; the zero entry is carried.
	paid, dust = Pay(big.NewInt(5), list)
	if len(dust) != 1 || dust[0] != list[0] || len(paid) != 2 || paid[0].Amount.Int64() != 1 || paid[1].Amount.Int64() != 4 {
		t.Fatalf("dust: %v %v", paid, dust)
	}
	sum := new(big.Int)
	for _, p := range paid {
		sum.Add(sum, p.Amount)
	}
	if sum.Int64() != 5 {
		t.Fatalf("not paid whole: %v", sum)
	}
	// Nothing payable: everything carried, nothing paid.
	paid, dust = Pay(big.NewInt(1), []Entry{{addr(0), 5}, {addr(1), 5}})
	if len(paid) != 0 || len(dust) != 2 {
		t.Fatalf("unpayable: %v %v", paid, dust)
	}
	if paid, dust := Pay(nil, list); len(paid) != 0 || len(dust) != 3 {
		t.Fatal("nil amount")
	}
}

// TestWithdrawalsRoundTrip: the list in the block's form and back; Equal.
func TestWithdrawalsRoundTrip(t *testing.T) {
	list := []Entry{{addr(0), 1}, {addr(1), 2}}
	ws := Withdrawals(list)
	if ws[1].Index != 1 || ws[1].Amount != 2 || ws[1].Address != addr(1) {
		t.Fatal("withdrawal form")
	}
	if !Equal(FromWithdrawals(ws), list) || Equal(list, list[:1]) || Equal(list, []Entry{{addr(1), 2}, {addr(0), 1}}) {
		t.Fatal("Equal")
	}
}

// TestCarryStateMalformed: a count over the bound or a zero weight in
// state reads as a fault, never as a list.
func TestCarryStateMalformed(t *testing.T) {
	st := memState{}
	st[carrySlot(0)] = common.BigToHash(big.NewInt(MaxCarry + 1))
	if _, err := ReadCarry(st, account); !errors.Is(err, ErrCarryState) {
		t.Fatalf("count over the bound: %v", err)
	}
	st[carrySlot(0)] = common.BigToHash(big.NewInt(1))
	st[carrySlot(1)] = common.BytesToHash(addr(0).Bytes())
	if _, err := ReadCarry(st, account); !errors.Is(err, ErrCarryState) {
		t.Fatalf("zero weight: %v", err)
	}
}

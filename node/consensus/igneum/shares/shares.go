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

// Package shares is the share lane's consensus rule (LAUNCH-GUARDS.md 2.9;
// the project's order of 13:4x UK, 11 October 2026, for the seventh development
// chain): one network-wide share target from chain work, the payout list's
// ceiling with a deterministic carry-forward balance, and the pro rata
// payment over the list with dust carried. Pure over its arguments and a
// StateDB; the engine (consensus/igneum/shares_engine.go) binds it to the
// chain's parameters and the protocol team's wire form (the share's header, its
// submission and the body's list) verifies each share before it reaches
// Build. Nothing here is served or adopted; PROTOTYPE.
//
// The target. A share is unkeyed IgneumHash work on the twin document over the
// block template (design G's chain-native share as LAUNCH-GUARDS 2.1a
// reads it: a share never becomes a block), accepted iff its hash is at or
// under Target(D, n) = pow.Target(D / n), n the operating shares per block
// (160 at launch), so the network's work yields about n accepted shares a
// block whatever the card count; no per-address difficulty exists (one
// that can be multiplied through Sybil identities, 2.9's refusal).
//
// The list. A block pays at most Ceiling = 256 payout entries (the
// withdrawals form: address and weight). The canonical list of a block is
// built by Build from the share-credit balance the parent's state holds
// (drained first, in the order the credits were first written, so no
// address waits behind a lower one for ever) and the block's accepted
// shares sorted by address ascending (the protocol team's derived list), one
// entry per address with the weights summed (so splitting one card's
// shares over addresses earns exactly what one address earns, and never
// a second entry's place); entries past the limit enter the credit
// balance in order, never dropped: an accepted share either appears in
// the payout calculation of the block it is in or in a later block's
// through the credits (2.9's gate row 1). The limit is the ceiling less
// the sealer's own entry, which the protocol team's derived list appends last.
//
// The payment. Pay splits an amount pro rata over the list by weight; an
// entry whose pro rata amount rounds to zero (dust) is not paid and its
// weight re-enters the credits, so dust is deferred, never burnt; the
// rounding remainder goes to the last paid entry (the F-35 form). The
// subsidy's miner share and the block's tips are the amount, the fund's
// percent and the open seat are untouched (F-35 unchanged).
package shares

import (
	"fmt"
	"bytes"
	"errors"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	// OperatingSharesPerBlock is the launch operating target n (2.9; the project's
	// ruling of 12:5x UK on the outside review's figure: 160, the 8.3 MH/s floor
	// card's 26.8 shares a week at 100,000 cards, 19 percent weekly
	// variance; 128 was the first reading).
	OperatingSharesPerBlock = 160
	// Ceiling is the consensus ceiling on a block's payout entries (the
	// withdrawals list's 256, LAUNCH-GUARDS 2.2's pooling-form row).
	Ceiling = 256
	// MaxCarry bounds the carry balance's entries so a flood of distinct
	// addresses cannot grow the state without bound: past it a new address
	// is refused at Build (the block is invalid) and the sealer must wait
	// for the carry to drain; at 256 entries a block it drains in
	// MaxCarry / Ceiling blocks.
	MaxCarry = 16 * Ceiling
)

var (
	ErrNoShares   = errors.New("igneum shares: the rule is off (sharesPerBlock 0)")
	ErrWeight     = errors.New("igneum shares: a share with zero weight")
	ErrCarryFull  = errors.New("igneum shares: the carry balance is full")
	ErrListOrder  = errors.New("igneum shares: the block's list is not the canonical list")
	ErrCarryState = errors.New("igneum shares: the carry balance in state is malformed")
)

// Target is the share target at block difficulty D under n shares per
// block: pow.Target(max(1, D / n)); nil when the rule is off (n 0) or D is
// not a difficulty.
func Target(difficulty *big.Int, sharesPerBlock uint64) *big.Int {
	if sharesPerBlock == 0 || difficulty == nil || difficulty.Sign() <= 0 {
		return nil
	}
	d := new(big.Int).Div(difficulty, new(big.Int).SetUint64(sharesPerBlock))
	if d.Sign() == 0 {
		d.SetUint64(1)
	}
	return pow.Target(d)
}

// Meets reports whether a share's hash is at or under the target.
func Meets(hash common.Hash, target *big.Int) bool {
	return target != nil && new(big.Int).SetBytes(hash[:]).Cmp(target) <= 0
}

// Entry is one payout entry: an address and its weight in shares.
type Entry struct {
	Address common.Address
	Weight  uint64
}

// StateDB is what the carry needs of the state.
type StateDB interface {
	GetState(common.Address, common.Hash) common.Hash
	SetState(common.Address, common.Hash, common.Hash)
}

var carryDomain = []byte("IgneumHash/R0A/share-credit")

// carrySlot is the i-th word of the share-credit list under the consensus
// account: word 0 the count, then for entry j the address at 1 + 2j and
// the weight at 2 + 2j (the protocol team's slot base "IgneumHash/R0A/share-credit").
func carrySlot(i uint64) common.Hash {
	b := crypto.Keccak256Hash(carryDomain).Big()
	return common.BigToHash(b.Add(b, new(big.Int).SetUint64(i)))
}

// CreditOf is an address's share credit at a state (igneum_shareCredit).
func CreditOf(st StateDB, account common.Address, addr common.Address) (uint64, error) {
	held, err := ReadCarry(st, account)
	if err != nil {
		return 0, err
	}
	for _, e := range held {
		if e.Address == addr {
			return e.Weight, nil
		}
	}
	return 0, nil
}

// ReadCarry reads the share-credit balance in its stored order (the order
// the credits were first written).
func ReadCarry(st StateDB, account common.Address) ([]Entry, error) {
	n := st.GetState(account, carrySlot(0)).Big()
	if !n.IsUint64() || n.Uint64() > MaxCarry {
		return nil, ErrCarryState
	}
	out := make([]Entry, 0, n.Uint64())
	for j := uint64(0); j < n.Uint64(); j++ {
		addr := common.BytesToAddress(st.GetState(account, carrySlot(1+2*j)).Bytes())
		w := st.GetState(account, carrySlot(2+2*j)).Big()
		if !w.IsUint64() || w.Sign() == 0 {
			return nil, ErrCarryState
		}
		out = append(out, Entry{Address: addr, Weight: w.Uint64()})
	}
	return out, nil
}

// WriteCarry writes the carry balance, clearing the old entries past the
// new count.
func WriteCarry(st StateDB, account common.Address, carry []Entry) {
	old := st.GetState(account, carrySlot(0)).Big()
	st.SetState(account, carrySlot(0), common.BigToHash(new(big.Int).SetUint64(uint64(len(carry)))))
	for j, e := range carry {
		st.SetState(account, carrySlot(1+2*uint64(j)), common.BytesToHash(e.Address.Bytes()))
		st.SetState(account, carrySlot(2+2*uint64(j)), common.BigToHash(new(big.Int).SetUint64(e.Weight)))
	}
	if old.IsUint64() {
		for j := uint64(len(carry)); j < old.Uint64() && j < MaxCarry; j++ {
			st.SetState(account, carrySlot(1+2*j), common.Hash{})
			st.SetState(account, carrySlot(2+2*j), common.Hash{})
		}
	}
}

// Build is the canonical list of a block: the credits first in their
// stored order, then the block's accepted shares sorted by address
// ascending (any input order gives one list), one entry per address with
// the weights summed (an address already present adds to its entry
// wherever it sits), the first limit entries paid by this block and the
// rest the new credits. The limit is at most Ceiling (a larger one reads
// as Ceiling) and leaves the sealer's own entry its place when the caller
// passes Ceiling minus one. A zero weight is refused; a new address past
// MaxCarry credits is refused (the block is invalid; the sealer drops
// that share from its template and the share is resubmitted when the
// credits have drained).
func Build(carry []Entry, accepted []Entry, limit int) (list []Entry, newCarry []Entry, err error) {
	if limit <= 0 || limit > Ceiling {
		limit = Ceiling
	}
	merged := make([]Entry, 0, len(carry)+len(accepted))
	index := make(map[common.Address]int, len(carry)+len(accepted))
	for _, e := range carry {
		if e.Weight == 0 {
			return nil, nil, ErrCarryState
		}
		if i, ok := index[e.Address]; ok {
			merged[i].Weight = saturate(merged[i].Weight, e.Weight)
			continue
		}
		index[e.Address] = len(merged)
		merged = append(merged, e)
	}
	sorted := append([]Entry(nil), accepted...)
	sort.SliceStable(sorted, func(i, j int) bool { return bytes.Compare(sorted[i].Address[:], sorted[j].Address[:]) < 0 })
	for _, e := range sorted {
		if e.Weight == 0 {
			return nil, nil, ErrWeight
		}
		if i, ok := index[e.Address]; ok {
			merged[i].Weight = saturate(merged[i].Weight, e.Weight)
			continue
		}
		if len(merged) >= limit+MaxCarry {
			return nil, nil, ErrCarryFull
		}
		index[e.Address] = len(merged)
		merged = append(merged, e)
	}
	if len(merged) <= limit {
		return merged, nil, nil
	}
	return merged[:limit], merged[limit:], nil
}

func saturate(a, b uint64) uint64 {
	if a+b < a {
		return ^uint64(0)
	}
	return a + b
}

// Withdrawals is the list in the block's withdrawals form (index the
// position, validator 0, amount the weight).
func Withdrawals(list []Entry) []*types.Withdrawal {
	out := make([]*types.Withdrawal, len(list))
	for i, e := range list {
		out[i] = &types.Withdrawal{Index: uint64(i), Address: e.Address, Amount: e.Weight}
	}
	return out
}

// FromWithdrawals reads a block's list back as entries.
func FromWithdrawals(ws []*types.Withdrawal) []Entry {
	out := make([]Entry, len(ws))
	for i, w := range ws {
		out[i] = Entry{Address: w.Address, Weight: w.Amount}
	}
	return out
}

// Equal reports whether two lists are the same entries in the same order.
func Equal(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Payment is one credit of Pay.
type Payment struct {
	Address common.Address
	Amount  *big.Int
}

// Pay splits amount pro rata over the list by weight. An entry whose pro
// rata amount is zero (dust) is not paid and is returned as carried
// weight; the rounding remainder goes to the last paid entry so the
// amount is paid whole. A list without a payable entry (amount under the
// total weight) pays nothing and carries every entry.
func Pay(amount *big.Int, list []Entry) (paid []Payment, dust []Entry) {
	if amount == nil || amount.Sign() <= 0 || len(list) == 0 {
		for _, e := range list {
			if e.Weight > 0 {
				dust = append(dust, e)
			}
		}
		return nil, dust
	}
	total := new(big.Int)
	for _, e := range list {
		total.Add(total, new(big.Int).SetUint64(e.Weight))
	}
	sum := new(big.Int)
	for _, e := range list {
		if e.Weight == 0 {
			// A name without weight (the sealer's entry under the fold,
			// F-165): paid nothing, never carried.
			continue
		}
		share := new(big.Int).Mul(amount, new(big.Int).SetUint64(e.Weight))
		share.Div(share, total)
		if share.Sign() == 0 {
			dust = append(dust, e)
			continue
		}
		sum.Add(sum, share)
		paid = append(paid, Payment{Address: e.Address, Amount: share})
	}
	if len(paid) > 0 {
		last := paid[len(paid)-1]
		last.Amount = new(big.Int).Add(last.Amount, new(big.Int).Sub(amount, sum))
		paid[len(paid)-1] = last
	}
	return paid, dust
}

// Describe is a list's entries as "addr:weight" pairs, for a fault's line.
func Describe(list []Entry) string {
	out := make([]byte, 0, 32*len(list))
	for i, e := range list {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, e.Address.Hex()[:10]...)
		out = append(out, ':')
		out = append(out, fmt.Sprint(e.Weight)...)
	}
	return string(out)
}

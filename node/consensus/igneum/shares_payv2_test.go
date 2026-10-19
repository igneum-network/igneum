// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library.
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

package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestPayRuleSecondForm (F-63's fix (b), the design seat's ruling of 12
// October 2026): under PayV2 the sealer's term is a fixed quarter whatever
// the list holds; the members' three quarters are paid by weight scaled by
// min(W, Q) / Q with Q the lane's sharesPerBlock; the rest is unissued. A
// full list (W = Q) pays the whole amount; a list lighter than Q pays the
// sealer's quarter whole and the members W / Q of their three quarters; an
// empty list (the plain form) pays the quarter alone; off (PayV2 zero) the
// first form pays as before.
func TestPayRuleSecondForm(t *testing.T) {
	e := shareEngine(t)
	defer e.Close()
	// The test's lists are sized against a quorum of 4 (the share engine's
	// first form; the harness's figure has moved since), so it pins the
	// quorum it reads.
	e.config.Parts.Sortition.SharesPerBlock = 4
	q := e.config.Parts.Sortition.SharesPerBlock
	amount := big.NewInt(1_000_000)
	sealer := shareAddr(9000)
	bal := func(st *state.StateDB, a common.Address) *big.Int { return st.GetBalance(a).ToBig() }
	chain := newFakeChain()
	// Full: four members at weight 1 (W = Q = 4) and the sealer's entry
	// last; the sealer takes 250,000 and the members 750,000 between them.
	st := nativeState(t, big.NewInt(1_000_000))
	e.SetRuleForks(&ctypes.IgneumRuleForks{PayV2: 1})
	var full []shares.Entry
	for i := 0; i < 4; i++ {
		full = append(full, shares.Entry{Address: shareAddr(i), Weight: 1})
	}
	full = append(full, shares.Entry{Address: sealer, Weight: 2})
	_, unissued, err := e.SettleSharesV2(st, 0, amount, full, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := bal(st, sealer); got.Cmp(big.NewInt(250_000)) != 0 {
		t.Fatalf("the sealer's quarter on a full list: %v", got)
	}
	members := new(big.Int)
	for i := 0; i < 4; i++ {
		members.Add(members, bal(st, shareAddr(i)))
	}
	if members.Cmp(big.NewInt(750_000)) != 0 || unissued.Sign() != 0 {
		t.Fatalf("a full list: members %v unissued %v", members, unissued)
	}
	// Lighter than Q: two members (W = 2): the members' pot is 750,000 x
	// 2 / 4 = 375,000 and 375,000 is unissued; the sealer's quarter whole.
	st = nativeState(t, big.NewInt(1_000_000))
	light := []shares.Entry{{Address: shareAddr(10), Weight: 1}, {Address: shareAddr(11), Weight: 1}, {Address: sealer, Weight: 1}}
	_, unissued, err = e.SettleSharesV2(st, 0, amount, light, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := bal(st, sealer); got.Cmp(big.NewInt(250_000)) != 0 {
		t.Fatalf("the sealer's quarter on a light list: %v", got)
	}
	m := new(big.Int).Add(bal(st, shareAddr(10)), bal(st, shareAddr(11)))
	if m.Cmp(big.NewInt(375_000)) != 0 || unissued.Cmp(big.NewInt(375_000)) != 0 {
		t.Fatalf("a light list: members %v unissued %v", m, unissued)
	}
	// Heavier than Q: eight members (W = 8 > Q): the members' pot is the
	// whole three quarters, no more (the dilution the admission bound owns).
	st = nativeState(t, big.NewInt(1_000_000))
	var heavy []shares.Entry
	for i := 20; i < 28; i++ {
		heavy = append(heavy, shares.Entry{Address: shareAddr(i), Weight: 1})
	}
	heavy = append(heavy, shares.Entry{Address: sealer, Weight: 3})
	if _, unissued, err = e.SettleSharesV2(st, 0, amount, heavy, nil); err != nil || unissued.Sign() != 0 {
		t.Fatalf("a heavy list: unissued %v err %v", unissued, err)
	}
	// The record's arithmetic is the settlement's: payV2Amounts over the
	// light list names the same quarter, pot, credits and unissued figure
	// that SettleSharesV2 credited (igneum_blockIssuance reports from it).
	a := payV2Amounts(amount, light, q, false)
	if a.Sealer != sealer || a.Quarter.Cmp(big.NewInt(250_000)) != 0 || a.Weight != 2 || a.Quorum != q || a.Pot.Cmp(big.NewInt(375_000)) != 0 || a.Credited.Cmp(big.NewInt(625_000)) != 0 || a.Unissued.Cmp(big.NewInt(375_000)) != 0 || len(a.Paid) != 2 {
		t.Fatalf("the record's arithmetic on the light list: %+v", a)
	}
	// The plain form through Finalize: no list, the subsidy's quarter to
	// the coinbase, three quarters unissued; off, the whole subsidy.
	st = nativeState(t, big.NewInt(1_000_000))
	subsidy, _ := e.config.Parts.Issuance.Reward(5)
	cb := shareAddr(9001)
	h := &types.Header{Number: big.NewInt(5), Difficulty: big.NewInt(1), Coinbase: cb}
	e.Finalize(chain, h, st, nil, nil, nil)
	if got := bal(st, cb); got.Cmp(new(big.Int).Div(subsidy, big.NewInt(4))) != 0 {
		t.Fatalf("the plain form under the second form: %v, want a quarter of %v", got, subsidy)
	}
	st = nativeState(t, big.NewInt(1_000_000))
	e.SetRuleForks(&ctypes.IgneumRuleForks{})
	e.Finalize(chain, h, st, nil, nil, nil)
	if got := bal(st, cb); got.Cmp(subsidy) != 0 {
		t.Fatalf("the plain form under the first form: %v, want %v", got, subsidy)
	}
}

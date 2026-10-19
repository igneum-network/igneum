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

package igneum

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

func shareEngine(t *testing.T) *Engine {
	t.Helper()
	e := genesisEngine(t)
	e.config.Parts.Sortition.SharesPerBlock = shares.OperatingSharesPerBlock
	return e
}

func shareAddr(i int) common.Address { return common.BigToAddress(big.NewInt(int64(100 + i))) }

// TestShareLaneParameters: the lane needs the ceiling as the list's
// bound; off at 0.
func TestShareLaneParameters(t *testing.T) {
	cfg := &ctypes.IgneumConfig{
		Authority: &ctypes.IgneumAuthority{Height: 1, Registry: testRegistry, Cap: big.NewInt(1), RegistryVersion: 4},
		Sortition: &ctypes.IgneumSortition{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 6, MaxRound: 16, UnbondedRound: 17, MaxPayouts: 128, OpenSeat: true, SharesPerBlock: 160},
	}
	if _, err := SortitionFor(cfg); err == nil {
		t.Fatal("the lane with a 128-entry bound accepted")
	}
	cfg.Sortition.MaxPayouts = shares.Ceiling
	p, err := SortitionFor(cfg)
	if err != nil || p.SharesPerBlock != 160 || p.MaxShares != shares.Ceiling {
		t.Fatalf("the lane at the ceiling: %v %+v", err, p)
	}
	cfg.Sortition.MaxShares = 64
	if _, err := SortitionFor(cfg); err == nil {
		t.Fatal("sharesPerBlock over maxShares accepted")
	}
	cfg.Sortition.MaxShares = 300
	if _, err := SortitionFor(cfg); err == nil {
		t.Fatal("maxShares over the ceiling accepted")
	}
	cfg.Sortition.MaxShares = 200
	if p, err := SortitionFor(cfg); err != nil || p.MaxShares != 200 {
		t.Fatalf("maxShares 200: %v", err)
	}
	// The carriage window (G-23): unset reads the entry's bound (8 while
	// the entry carries a one-byte parent index), a value over it refused,
	// a value under it taken; the pool bound is maxShares x the recency.
	if p, err := SortitionFor(cfg); err != nil || p.ShareRecency != ShareRecency {
		t.Fatalf("shareRecency unset: %v %+v", err, p)
	}
	// The cap is the lane's MaxShareRecency (64), not the entry's width:
	// 64 taken, 65 refused.
	cfg.Sortition.ShareRecency = MaxShareRecency
	if p, err := SortitionFor(cfg); err != nil || p.ShareRecency != MaxShareRecency {
		t.Fatalf("shareRecency at the lane's bound: %v", err)
	}
	cfg.Sortition.ShareRecency = MaxShareRecency + 1
	if _, err := SortitionFor(cfg); err == nil {
		t.Fatal("shareRecency over the lane's bound accepted")
	}
	cfg.Sortition.ShareRecency = 4
	if p, err := SortitionFor(cfg); err != nil || p.ShareRecency != 4 {
		t.Fatalf("shareRecency 4: %v", err)
	}
	e := shareEngine(t)
	defer e.Close()
	e.config.Parts.Sortition.MaxShares = shares.Ceiling
	if e.ShareRecencyBlocks() != ShareRecency || e.SharePoolBound() != shares.Ceiling*ShareRecency {
		t.Fatalf("recency %d, pool bound %d", e.ShareRecencyBlocks(), e.SharePoolBound())
	}
	e.config.Parts.Sortition.ShareRecency = 4
	if e.ShareRecencyBlocks() != 4 || e.SharePoolBound() != shares.Ceiling*4 {
		t.Fatalf("recency %d, pool bound %d at 4", e.ShareRecencyBlocks(), e.SharePoolBound())
	}
	e.config.Parts.Sortition.SharesPerBlock = 0
	if e.SharePoolBound() != 0 {
		t.Fatal("a pool bound with the lane off")
	}
	cfg.Sortition.ShareRecency = 0
	cfg.Sortition.MaxShares = 0
	cfg.Sortition.SharesPerBlock = 0
	cfg.Sortition.MaxPayouts = 128
	if _, err := SortitionFor(cfg); err != nil {
		t.Fatalf("the lane off: %v", err)
	}
}

// TestShareTargetServed: the template's share target is the block target
// times n under the lane and nil without it.
func TestShareTargetServed(t *testing.T) {
	e := shareEngine(t)
	defer e.Close()
	h := &types.Header{Number: big.NewInt(5), Difficulty: big.NewInt(1 << 30)}
	want := pow.Target(new(big.Int).Div(h.Difficulty, big.NewInt(shares.OperatingSharesPerBlock)))
	if got := e.ShareTarget(h); got == nil || got.Cmp(want) != 0 {
		t.Fatalf("share target %v, want %v", got, want)
	}
	e.config.Parts.Sortition.SharesPerBlock = 0
	if e.ShareTarget(h) != nil {
		t.Fatal("a share target with the lane off")
	}
	if e.ShareTarget(&types.Header{Number: big.NewInt(0), Difficulty: big.NewInt(1)}) != nil {
		t.Fatal("a share target before the rule's height")
	}
}

// TestShareListAndSettlement: two blocks through the engine's three calls:
// 300 accepted shares at the first pay 255 and carry 45 (written under
// the registry account), the import check refuses a list that omits,
// reorders or reweights an entry, the second block drains the carry first.
func TestShareListAndSettlement(t *testing.T) {
	e := shareEngine(t)
	defer e.Close()
	st := nativeState(t, big.NewInt(1_000_000))
	var accepted []shares.Entry
	for i := 0; i < 300; i++ {
		accepted = append(accepted, shares.Entry{Address: shareAddr(i), Weight: 1})
	}
	// The whole derived list: 255 shares paid, the sealer's entry last at
	// ceil(300 / 3) = 100, 45 carried.
	sealer := shareAddr(9000)
	list, carry, err := e.ShareList(st, 5, accepted, sealer)
	if err != nil || len(list) != shares.Ceiling || len(carry) != 45 || list[255] != (shares.Entry{Address: sealer, Weight: 100}) {
		t.Fatalf("list: %d, carry %d, last %+v, %v", len(list), len(carry), list[len(list)-1], err)
	}
	for n, w := range map[int]uint64{0: 1, 1: 1, 2: 1, 3: 1, 4: 2, 6: 2, 7: 3, 300: 100} {
		if SealerWeight(n) != w {
			t.Fatalf("sealer weight at %d shares: %d, want %d", n, SealerWeight(n), w)
		}
	}
	h := &types.Header{Number: big.NewInt(5), Difficulty: big.NewInt(1)}
	ws := shares.Withdrawals(list)
	if err := e.VerifyShareList(st, h, accepted, ws); err != nil {
		t.Fatalf("the derived list refused: %v", err)
	}
	// Omitted, reordered, reweighted, the sealer's entry missing or at the
	// wrong weight, no list: each refused.
	if err := e.VerifyShareList(st, h, accepted, ws[1:]); !errors.Is(err, errShareList) {
		t.Fatalf("omitted: %v", err)
	}
	if err := e.VerifyShareList(st, h, accepted, ws[:255]); !errors.Is(err, errShareList) {
		t.Fatalf("the sealer's entry missing: %v", err)
	}
	light := shares.Withdrawals(list)
	light[255] = &types.Withdrawal{Index: 255, Address: sealer, Amount: 101}
	if err := e.VerifyShareList(st, h, accepted, light); !errors.Is(err, errShareList) {
		t.Fatalf("the sealer's weight: %v", err)
	}
	if err := e.VerifyShareList(st, h, accepted, nil); !errors.Is(err, errShareList) {
		t.Fatalf("no list: %v", err)
	}
	// Any sealer address passes at the rule's weight (the import takes it
	// from the list's last entry).
	other := shares.Withdrawals(list)
	other[255] = &types.Withdrawal{Index: 255, Address: shareAddr(9001), Amount: 100}
	if err := e.VerifyShareList(st, h, accepted, other); err != nil {
		t.Fatalf("another sealer address: %v", err)
	}
	// A block with no shares: the sealer's entry alone at weight 1.
	alone, _, err := e.ShareList(st, 5, nil, sealer)
	if err != nil || len(alone) != 1 || alone[0].Weight != 1 {
		t.Fatalf("no shares: %v %v", alone, err)
	}
	swapped := shares.Withdrawals(list)
	swapped[0], swapped[1] = swapped[1], swapped[0]
	if err := e.VerifyShareList(st, h, accepted, swapped); !errors.Is(err, errShareList) {
		t.Fatalf("reordered: %v", err)
	}
	heavy := shares.Withdrawals(list)
	heavy[3] = &types.Withdrawal{Index: 3, Address: heavy[3].Address, Amount: 2}
	if err := e.VerifyShareList(st, h, accepted, heavy); !errors.Is(err, errShareList) {
		t.Fatalf("reweighted: %v", err)
	}
	// Settlement: 355,000 over 355 weight units (255 shares of one, the
	// sealer's 100) pays 1,000 a unit: the sealer reads 100,000, a quarter
	// of the shares' 255,000 and a bit (ceil); the carry of 45 written.
	dust, err := e.SettleShares(st, big.NewInt(355_000), list, carry)
	if err != nil || len(dust) != 0 {
		t.Fatalf("settle: %v %v", dust, err)
	}
	if got := st.GetBalance(shareAddr(0)).Uint64(); got != 1_000 {
		t.Fatalf("entry 0 paid %d", got)
	}
	if got := st.GetBalance(sealer).Uint64(); got != 100_000 {
		t.Fatalf("the sealer paid %d", got)
	}
	if got := st.GetBalance(shareAddr(299)).Uint64(); got != 0 {
		t.Fatalf("a carried share paid %d", got)
	}
	held, err := e.ShareCarry(st)
	if err != nil || len(held) != 45 || held[0].Address != shareAddr(255) {
		t.Fatalf("carry in state: %d %v", len(held), err)
	}
	if c, err := e.ShareCredit(st, shareAddr(255)); err != nil || c != 1 {
		t.Fatalf("credit of a carried address: %d %v", c, err)
	}
	if c, _ := e.ShareCredit(st, shareAddr(0)); c != 0 {
		t.Fatalf("credit of a paid address: %d", c)
	}
	// The second block: one new share; the list is the 45 carried then
	// it; after settlement the carry is empty and every share of the first
	// block has been paid once.
	// The sealer's entry is last at weight 1 (one share, ceil(1 / 3) = 1).
	list2, carry2, err := e.ShareList(st, 6, []shares.Entry{{Address: shareAddr(500), Weight: 1}}, sealer)
	if err != nil || len(list2) != 47 || len(carry2) != 0 || list2[45].Address != shareAddr(500) || list2[46] != (shares.Entry{Address: sealer, Weight: 1}) {
		t.Fatalf("second list: %d %d %v", len(list2), len(carry2), err)
	}
	if _, err := e.SettleShares(st, big.NewInt(47_000), list2, carry2); err != nil {
		t.Fatal(err)
	}
	if got := st.GetBalance(shareAddr(299)).Uint64(); got != 1_000 {
		t.Fatalf("the carried share paid %d at the second block", got)
	}
	if got := st.GetBalance(sealer).Uint64(); got != 101_000 {
		t.Fatalf("the sealer holds %d after the second block", got)
	}
	if held, err := e.ShareCarry(st); err != nil || len(held) != 0 {
		t.Fatalf("carry after the drain: %d %v", len(held), err)
	}
	// Dust carries: 10 over two weights of 1 and 100 pays 0 and 10; the
	// dust entry is in the carry after.
	list3 := []shares.Entry{{Address: shareAddr(700), Weight: 1}, {Address: shareAddr(701), Weight: 100}}
	dust, err = e.SettleShares(st, big.NewInt(10), list3, nil)
	if err != nil || len(dust) != 1 || dust[0].Address != shareAddr(700) {
		t.Fatalf("dust: %v %v", dust, err)
	}
	if held, _ := e.ShareCarry(st); len(held) != 1 || held[0].Address != shareAddr(700) {
		t.Fatalf("dust not carried: %v", held)
	}
	// The lane off: the calls refuse.
	e.config.Parts.Sortition.SharesPerBlock = 0
	if _, _, err := e.ShareList(st, 7, nil, sealer); !errors.Is(err, errSharesOff) {
		t.Fatalf("lane off: %v", err)
	}
}

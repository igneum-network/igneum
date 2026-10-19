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
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

// G-31 (the outside review's money reading; DESIGN PA-49): the share lane's
// adversarial and state-bound cases at the engine level, the NEW ones
// beside the tests that already hold each class.

// TestG31SplitterAtTheCapFillsPlacesNeverPay: the per-address cap is read
// from the chain id's config (256 on the launch-equivalent chain, never a
// constant); a splitter of k addresses whose shares exceed the 256 ceiling
// fills the paid places exactly and the rest go to the credits, paid at
// the next block: the ceiling bounds places, never pay; and the split
// earns the same whole as one address would.
func TestG31SplitterAtTheCapFillsPlacesNeverPay(t *testing.T) {
	limits := params.IgneumRPCLimitsForChainID(7317)
	if limits == nil || limits.ShareCapPerAddress == 0 {
		t.Fatal("the launch-equivalent chain carries no per-address cap")
	}
	capPerAddr := limits.ShareCapPerAddress
	t.Logf("the running per-address cap on chain 7317: %d a height (the ceiling %d)", capPerAddr, shares.Ceiling)
	e := shareEngine(t)
	defer e.Close()
	e.config.Parts.Sortition.MaxShares = shares.Ceiling
	st := nativeState(t, big.NewInt(1_000_000))
	sealer := common.Address{0xaa}
	// k addresses at the cap: k x cap shares; with the cap at the ceiling
	// one address alone may fill the block's places, so the splitter's
	// edge is nil: k = 2 at the cap over the ceiling by 256 shares.
	var accepted []shares.Entry
	k := uint64(2)
	for i := uint64(0); i < k; i++ {
		accepted = append(accepted, shares.Entry{Address: common.BigToAddress(big.NewInt(int64(100 + i))), Weight: capPerAddr})
	}
	// Three hundred further single-share addresses behind the splitter.
	for i := 0; i < 300; i++ {
		accepted = append(accepted, shares.Entry{Address: common.BigToAddress(big.NewInt(int64(1000 + i))), Weight: 1})
	}
	list, credits, err := e.ShareList(st, 5, accepted, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != shares.Ceiling {
		t.Fatalf("the list has %d places, want the ceiling %d", len(list), shares.Ceiling)
	}
	// Every accepted weight is in the list or the credits.
	var in, out uint64
	for _, a := range accepted {
		in += a.Weight
	}
	for _, l := range list[:len(list)-1] {
		out += l.Weight
	}
	for _, c := range credits {
		out += c.Weight
	}
	if in != out {
		t.Fatalf("weight lost at the ceiling: %d in, %d paid or credited", in, out)
	}
	// THE FINDING THIS CASE READS (PA-49 (1)): at a FULL list a k-address
	// splitter takes k places and displaces k - 1 units of other weight
	// out of this block, so its share of THIS block's amount is larger by
	// about (k - 1) / W than one address's at the summed weight: a
	// per-block RATE shift (here 682 of 590,542 wei at k = 2, 0.12
	// percent), never a weight gain, since the displaced weight is paid
	// at the next block's rate and nothing is lost or made. The test
	// prints the shift and holds the invariants: the splitter's paid
	// weight equals its accepted weight, each block pays pro rata by
	// weight among its entries, and every accepted weight is paid exactly
	// once over the drain.
	amount := big.NewInt(1_000_000)
	paid, _ := shares.Pay(amount, list)
	splitPay := new(big.Int)
	for _, p := range paid {
		if p.Address == common.BigToAddress(big.NewInt(100)) || p.Address == common.BigToAddress(big.NewInt(101)) {
			splitPay.Add(splitPay, p.Amount)
		}
	}
	one := append([]shares.Entry{{Address: common.BigToAddress(big.NewInt(100)), Weight: k * capPerAddr}}, accepted[k:]...)
	oneList, oneCredits, _ := e.ShareList(st, 5, one, sealer)
	paidOne, _ := shares.Pay(amount, oneList)
	onePay := new(big.Int)
	for _, p := range paidOne {
		if p.Address == common.BigToAddress(big.NewInt(100)) {
			onePay.Set(p.Amount)
		}
	}
	if len(oneCredits) != len(credits)-int(k)+1 {
		t.Fatalf("the splitter's extra places: %d credits against %d with one address", len(credits), len(oneCredits))
	}
	shift := new(big.Int).Sub(splitPay, onePay)
	t.Logf("the splitter's per-block rate shift at k %d over a full list: %v wei of %v (%.3f percent; the bound (k - 1) / W = %.3f percent)", k, shift, onePay, 100*float64(shift.Int64())/float64(onePay.Int64()), 100*float64(k-1)/float64(2*capPerAddr+254))
	if shift.Sign() < 0 || float64(shift.Int64())/float64(onePay.Int64()) > float64(k-1)/float64(2*capPerAddr+254)*1.01 {
		t.Fatalf("the rate shift %v is outside its bound", shift)
	}
	// Pro rata by weight within the block: each paid entry's amount is
	// its weight's share (the last entry takes the remainder).
	var total uint64
	for _, l := range list {
		total += l.Weight
	}
	for i, p := range paid[:len(paid)-1] {
		want := new(big.Int).Div(new(big.Int).Mul(amount, new(big.Int).SetUint64(list[i].Weight)), new(big.Int).SetUint64(total))
		if p.Amount.Cmp(want) != 0 {
			t.Fatalf("entry %d paid %v, pro rata %v", i, p.Amount, want)
		}
	}
	// Over the drain every accepted weight is paid exactly once (the
	// splitter's k x cap included) and the credits go in their order.
	if _, err := e.SettleShares(st, amount, list, credits); err != nil {
		t.Fatal(err)
	}
	paidWeight := map[common.Address]uint64{}
	for _, l := range list[:len(list)-1] {
		paidWeight[l.Address] += l.Weight
	}
	next, rest, err := e.ShareList(st, 6, nil, sealer)
	if err != nil || len(rest) != 0 || len(next) != len(credits)+1 || next[0] != credits[0] {
		t.Fatalf("the credits at the next block: %d paid, %d left, %v", len(next), len(rest), err)
	}
	for _, l := range next[:len(next)-1] {
		paidWeight[l.Address] += l.Weight
	}
	for _, a := range accepted {
		if paidWeight[a.Address] != a.Weight {
			t.Fatalf("%s paid weight %d, accepted %d", a.Address.Hex(), paidWeight[a.Address], a.Weight)
		}
	}
}

// TestG31TwoEnginesOneList: two engines over the same credits and shares
// build byte-equal withdrawals from any input order (the list's order is
// the chain's, never the node's).
func TestG31TwoEnginesOneList(t *testing.T) {
	e1 := shareEngine(t)
	defer e1.Close()
	e2 := shareEngine(t)
	defer e2.Close()
	st1 := nativeState(t, big.NewInt(1_000_000))
	st2 := nativeState(t, big.NewInt(1_000_000))
	sealer := common.Address{0xaa}
	var accepted []shares.Entry
	for i := 0; i < 300; i++ {
		accepted = append(accepted, shares.Entry{Address: common.BigToAddress(big.NewInt(int64(7000 - i))), Weight: uint64(1 + i%3)})
	}
	reversed := make([]shares.Entry, len(accepted))
	for i, a := range accepted {
		reversed[len(accepted)-1-i] = a
	}
	l1, c1, _ := e1.ShareList(st1, 5, accepted, sealer)
	l2, c2, _ := e2.ShareList(st2, 5, reversed, sealer)
	w1, _ := rlp.EncodeToBytes(types.Withdrawals(shares.Withdrawals(l1)))
	w2, _ := rlp.EncodeToBytes(types.Withdrawals(shares.Withdrawals(l2)))
	if !bytes.Equal(w1, w2) || !shares.Equal(c1, c2) {
		t.Fatal("two engines built different lists from one set")
	}
	// And after a settlement on each, the credits in the two states
	// agree, so the next lists agree too.
	e1.SettleShares(st1, big.NewInt(1000), l1, c1)
	e2.SettleShares(st2, big.NewInt(1000), l2, c2)
	n1, _, _ := e1.ShareList(st1, 6, nil, sealer)
	n2, _, _ := e2.ShareList(st2, 6, nil, sealer)
	if !shares.Equal(n1, n2) {
		t.Fatal("the next lists differ after one settlement")
	}
}

// TestG31DustBoundAtLaunchAmounts: at the launch subsidy (chain 7317's
// opening 7.947 IGN a block in wei) over the ceiling's weights an entry's
// pro rata pay never rounds to zero, so dust cannot arise on the launch
// chain; the bound is stated with the number (the smallest pay at 256
// unit weights plus the sealer's).
func TestG31DustBoundAtLaunchAmounts(t *testing.T) {
	iss := params.IgneumIssuanceForChainID(7317)
	if iss == nil {
		t.Fatal("no issuance for 7317")
	}
	opening := new(big.Int).Div(iss.M, new(big.Int).SetUint64(iss.EpochDiv))
	opening.Div(opening, new(big.Int).SetUint64(iss.Epoch))
	miner := new(big.Int).Sub(opening, new(big.Int).Div(new(big.Int).Mul(opening, new(big.Int).SetUint64(iss.FundBps)), big.NewInt(10000)))
	var list []shares.Entry
	for i := 0; i < shares.Ceiling-1; i++ {
		list = append(list, shares.Entry{Address: common.BigToAddress(big.NewInt(int64(100 + i))), Weight: 1})
	}
	list = append(list, shares.Entry{Address: common.Address{0xaa}, Weight: SealerWeight(shares.Ceiling - 1)})
	paid, dust := shares.Pay(miner, list)
	if len(dust) != 0 || len(paid) != len(list) {
		t.Fatalf("dust at the launch amounts: %d dust entries", len(dust))
	}
	smallest := paid[0].Amount
	for _, p := range paid {
		if p.Amount.Cmp(smallest) < 0 {
			smallest = p.Amount
		}
	}
	t.Logf("the smallest pay at the opening subsidy over %d weights: %s wei (%.6f IGN); dust needs a weight share under 1 / %s", len(list), smallest, float64(smallest.Int64())/1e18, miner)
	// The tail's per-block amount (one third of an IGN) likewise.
	tail := new(big.Int).Div(iss.TailPerEpoch, new(big.Int).SetUint64(iss.Epoch))
	if _, dust := shares.Pay(tail, list); len(dust) != 0 {
		t.Fatal("dust at the tail's amount")
	}
}

// TestG31CarryForwardLatency: a credited address behind 255 fresh
// addresses every block is paid at the block the credits' first-written
// order reaches it: at most ceil(credits / 255) blocks for a backlog of
// 1,000 credits with no fresh shares, and every credit paid exactly once.
func TestG31CarryForwardLatency(t *testing.T) {
	e := shareEngine(t)
	defer e.Close()
	e.config.Parts.Sortition.MaxShares = shares.Ceiling
	st := nativeState(t, big.NewInt(1_000_000))
	sealer := common.Address{0xaa}
	var backlog []shares.Entry
	for i := 0; i < 1000; i++ {
		backlog = append(backlog, shares.Entry{Address: common.BigToAddress(big.NewInt(int64(50_000 + i))), Weight: 1})
	}
	// Block 5 accepts the 1,000: 255 paid, 745 credited.
	list, credits, err := e.ShareList(st, 5, backlog, sealer)
	if err != nil || len(credits) != 745 {
		t.Fatalf("the backlog: %d credits, %v", len(credits), err)
	}
	if _, err := e.SettleShares(st, big.NewInt(255_000+1), list, credits); err != nil {
		t.Fatal(err)
	}
	paidOnce := map[common.Address]int{}
	for _, l := range list[:len(list)-1] {
		paidOnce[l.Address]++
	}
	blocks := 1
	for {
		held, _ := e.ShareCarry(st)
		if len(held) == 0 {
			break
		}
		blocks++
		if blocks > 5 {
			t.Fatalf("the backlog not drained in ceil(1000 / 255) = 4 blocks: %d left after %d", len(held), blocks)
		}
		l, c, err := e.ShareList(st, uint64(4+blocks), nil, sealer)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range l[:len(l)-1] {
			paidOnce[x.Address]++
		}
		if _, err := e.SettleShares(st, big.NewInt(int64(len(l))*1000), l, c); err != nil {
			t.Fatal(err)
		}
	}
	if blocks != 4 {
		t.Fatalf("drained in %d blocks, want 4", blocks)
	}
	if len(paidOnce) != 1000 {
		t.Fatalf("%d addresses paid, want 1000", len(paidOnce))
	}
	for a, n := range paidOnce {
		if n != 1 {
			t.Fatalf("%s paid %d times", a.Hex(), n)
		}
	}
}

// TestShareRecency64AtImport (devnet8's window, the project's order of 19:4x UK,
// 11 October 2026): the import's ancestor walk reads the chain's window,
// not the lane's default of 8: a share on the ancestor at k 20 verifies
// under shareRecency 64 and is refused as past the window under 8; a
// share at k 64 is past a window of 64. The cap is the lane's 64 whatever
// the carried entry's width.
func TestShareRecency64AtImport(t *testing.T) {
	e, sc := mineShareEngine(t)
	defer e.Close()
	chain := &carriageChain{shareChain: sc, blocks: make(map[common.Hash]*types.Block)}
	// Sixty more blocks so an ancestor at k 64 exists.
	parent := sc.head
	for i := 0; i < 60; i++ {
		nh := childOf(parent)
		sc.push(nh)
		parent = nh
	}
	head := sc.head
	sealer := common.Address{0xaa}
	e.config.Parts.Sortition.ShareRecency = 64
	anc20 := sc.byNumber[head.Number.Uint64()-20]
	x := mineFor(t, e, anc20, common.Address{0x11}, 1, 20)
	b := carryBlock(t, e, sc, head, []types.MinedShare{x}, sealer)
	if err := e.VerifyMinedShares(b, sc.st, chain); err != nil {
		t.Fatalf("a share at k 20 under a window of 64: %v", err)
	}
	anc64 := sc.byNumber[head.Number.Uint64()-64]
	far := mineFor(t, e, anc64, common.Address{0x22}, 2, 64)
	bFar := carryBlock(t, e, sc, head, []types.MinedShare{far}, sealer)
	if err := e.VerifyMinedShares(bFar, sc.st, chain); !errors.Is(err, errShareAncestor) {
		t.Fatalf("a share at k 64 under a window of 64: %v", err)
	}
	e.config.Parts.Sortition.ShareRecency = ShareRecency
	if err := e.VerifyMinedShares(b, sc.st, chain); !errors.Is(err, errShareAncestor) {
		t.Fatalf("a share at k 20 under the default window of 8: %v", err)
	}
	if e.ShareRecencyBlocks() != ShareRecency || MaxShareRecency != 64 {
		t.Fatal("the window's default and the lane's bound")
	}
}

// TestG31StateGrowthBounds: the carried-key cache never exceeds its bound
// (it resets at carriedCacheMax entries), the credits in state refuse a
// new address past 16 x 256 (the block invalid rather than the state
// growing), and the per-address counts below the window are dropped by
// the expiry sweep.
func TestG31StateGrowthBounds(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	// The cache: carriedCacheMax + 10 blocks remembered; the map never
	// holds more than carriedCacheMax.
	for i := 0; i < carriedCacheMax+10; i++ {
		e.rememberCarried(common.BigToHash(big.NewInt(int64(i+1))), []string{"k"})
		e.sharePool.mu.Lock()
		n := len(e.sharePool.carriedBy)
		e.sharePool.mu.Unlock()
		if n > carriedCacheMax {
			t.Fatalf("the carried cache holds %d, over its bound %d", n, carriedCacheMax)
		}
	}
	// The credits' bound: a full carry plus one new address at Build.
	var carry []shares.Entry
	for i := 0; i < shares.MaxCarry; i++ {
		carry = append(carry, shares.Entry{Address: common.BigToAddress(big.NewInt(int64(90_000 + i))), Weight: 1})
	}
	var block []shares.Entry
	for i := 0; i < shares.Ceiling; i++ {
		block = append(block, shares.Entry{Address: common.BigToAddress(big.NewInt(int64(200_000 + i))), Weight: 1})
	}
	if _, _, err := shares.Build(carry, append(block, shares.Entry{Address: common.BigToAddress(big.NewInt(300_000)), Weight: 1}), shares.Ceiling); err != shares.ErrCarryFull {
		t.Fatalf("the credits' bound: %v", err)
	}
	// The per-address counts: a count at the head's height, then
	// ShareRecency + 1 heads on; the sweep drops it.
	e.SetShareCap(2)
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x11}
	d := e.calcDifficulty(head, chain.byNumber[head.Number.Uint64()-1])
	hs := ShareHeaderHash(head.Hash(), h, a, 9)
	nonce, mix := solveShare(t, e, hs, h, d)
	if ok, reason := e.SubmitShare(chain, a, 9, nonce, mix, h); !ok {
		t.Fatalf("share refused: %s", reason)
	}
	e.sharePool.mu.Lock()
	counted := e.sharePool.perAddr[h][a]
	e.sharePool.mu.Unlock()
	if counted != 1 {
		t.Fatalf("the per-address count at %d: %d", h, counted)
	}
	parent := head
	for i := 0; i < int(ShareRecency)+1; i++ {
		nh := childOf(parent)
		chain.push(nh)
		parent = nh
	}
	e.ExpireShares(chain, parent)
	e.sharePool.mu.Lock()
	_, kept := e.sharePool.perAddr[h]
	e.sharePool.mu.Unlock()
	if kept {
		t.Fatalf("the per-address counts at %d kept past the window", h)
	}
}

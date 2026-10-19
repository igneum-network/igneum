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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/issuance"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/igneumhash"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/schedule"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/coregeth"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
)

// testDifficulty is the floor of the test rule; at the target interval the
// difficulty stays on it, which keeps the null-algorithm searches fast.
var testDifficulty = big.NewInt(1 << 10)

var testFund = TestFund

func testParts() Parts { return TestParts() }

// devEngineBlock is an engine block as a development genesis carries it.
func devEngineBlock(algorithm string) *ctypes.IgneumConfig {
	return &ctypes.IgneumConfig{
		Algorithm:  algorithm,
		Difficulty: &ctypes.IgneumDifficulty{Target: 15, HalfLife: 3600, DtMax: 3600, Min: new(big.Int).Set(testDifficulty)},
		Issuance:   &ctypes.IgneumIssuance{Epoch: 8192, M: new(big.Int).Set(issuance.Candidate.M), K: 24, Tail: new(big.Int).Set(issuance.Candidate.Tail), FundBps: 100, FundAddress: testFund},
	}
}

// fakeChain is a ChainHeaderReader over a map of headers.
type fakeChain struct {
	config  ctypes.ChainConfigurator
	headers map[common.Hash]*types.Header
	head    *types.Header // CurrentHeader's answer when set (nil otherwise, as every older cell reads it)
}

func newFakeChain() *fakeChain {
	return &fakeChain{
		config: &coregeth.CoreGethChainConfig{
			ChainID: big.NewInt(7310),
			Igneum:  devEngineBlock(null.Name),
		},
		headers: make(map[common.Hash]*types.Header),
	}
}

func (c *fakeChain) Config() ctypes.ChainConfigurator { return c.config }
func (c *fakeChain) CurrentHeader() *types.Header     { return c.head }
func (c *fakeChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	h := c.headers[hash]
	if h != nil && h.Number.Uint64() != number {
		return nil
	}
	return h
}
func (c *fakeChain) GetHeaderByNumber(number uint64) *types.Header  { return nil }
func (c *fakeChain) GetHeaderByHash(hash common.Hash) *types.Header { return c.headers[hash] }
func (c *fakeChain) GetTd(hash common.Hash, number uint64) *big.Int { return nil }
func (c *fakeChain) add(h *types.Header)                            { c.headers[h.Hash()] = h }

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	algo, err := pow.NewDev(null.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Algorithm: algo, Parts: testParts()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func genesisHeader() *types.Header {
	return &types.Header{
		Number:     big.NewInt(0),
		Time:       uint64(time.Now().Unix()) - 1000,
		Difficulty: new(big.Int).Set(testDifficulty),
		GasLimit:   16_000_000,
		UncleHash:  types.EmptyUncleHash,
	}
}

// childOf returns an unsealed valid child of parent at parent.Time+15 (the
// target interval, so the difficulty stays on the floor).
func childOf(parent *types.Header) *types.Header {
	return &types.Header{
		ParentHash: parent.Hash(),
		UncleHash:  types.EmptyUncleHash,
		Number:     new(big.Int).Add(parent.Number, big.NewInt(1)),
		Time:       parent.Time + 15,
		Difficulty: new(big.Int).Set(testDifficulty),
		GasLimit:   parent.GasLimit,
	}
}

// seal solves the header with the reference solver.
func seal(t *testing.T, e *Engine, h *types.Header) {
	t.Helper()
	nonce, mix, ok := pow.Solve(e.algo, e.SealHash(h), h.Number.Uint64(), h.Difficulty, 0, nil)
	if !ok {
		t.Fatal("solver aborted")
	}
	h.Nonce = types.EncodeNonce(nonce)
	h.MixDigest = mix
}

func TestNewRequiresParts(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New with nothing configured must fail")
	}
	p := testParts()
	p.Issuance = nil
	if _, err := New(Config{FakePoW: true, Parts: p}); err == nil {
		t.Fatal("New without issuance must fail")
	}
	if _, err := NewFromChainConfig(devEngineBlock("no-such"), testParts(), nil, false, true); !errors.Is(err, pow.ErrUnknownAlgorithm) {
		t.Fatalf("unknown algorithm: have %v want ErrUnknownAlgorithm", err)
	}
}

// TestPartsForRefusesIncomplete: what a node runs is what its genesis says;
// a missing or inconsistent block is refused, not defaulted.
func TestPartsForRefusesIncomplete(t *testing.T) {
	if _, err := PartsFor(nil); err == nil {
		t.Fatal("nil engine block accepted")
	}
	if _, err := PartsFor(&ctypes.IgneumConfig{Algorithm: null.Name}); err == nil {
		t.Fatal("engine block without difficulty accepted")
	}
	c := devEngineBlock(null.Name)
	c.Issuance = nil
	if _, err := PartsFor(c); err == nil {
		t.Fatal("engine block without issuance accepted")
	}
	c = devEngineBlock(null.Name)
	c.Issuance.FundAddress = common.Address{}
	if _, err := PartsFor(c); err == nil {
		t.Fatal("fund share without a fund address accepted")
	}
	c = devEngineBlock(null.Name)
	p, err := PartsFor(c)
	if err != nil {
		t.Fatal(err)
	}
	if p.FundAddress != testFund || p.Difficulty.Min.Cmp(testDifficulty) != 0 || p.Issuance == nil {
		t.Fatal("parts do not reflect the engine block")
	}
}

// TestNullRefusedOutsideDev is F-02: a genesis naming the null algorithm
// stops the node unless --dev is set; the dev path builds it.
func TestNullRefusedOutsideDev(t *testing.T) {
	cfg := devEngineBlock(null.Name)
	if _, err := NewFromChainConfig(cfg, testParts(), nil, false, false); !errors.Is(err, pow.ErrDevOnlyAlgorithm) {
		t.Fatalf("null outside dev: have %v want ErrDevOnlyAlgorithm", err)
	}
	if _, err := NewForChain(cfg, nil, false, false); !errors.Is(err, pow.ErrDevOnlyAlgorithm) {
		t.Fatalf("NewForChain null outside dev: have %v want ErrDevOnlyAlgorithm", err)
	}
	e, err := NewForChain(cfg, nil, false, true)
	if err != nil {
		t.Fatalf("null in dev: %v", err)
	}
	e.Close()
}

func TestSealHashExcludesNonceAndMix(t *testing.T) {
	e := newTestEngine(t)
	h := childOf(genesisHeader())
	before := e.SealHash(h)
	h.Nonce = types.EncodeNonce(12345)
	h.MixDigest = common.HexToHash("0x01")
	if e.SealHash(h) != before {
		t.Fatal("SealHash depends on Nonce or MixDigest")
	}
	h.Extra = []byte{1}
	if e.SealHash(h) == before {
		t.Fatal("SealHash does not depend on Extra")
	}
}

func TestVerifyHeaderValidSeal(t *testing.T) {
	e := newTestEngine(t)
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)
	h := childOf(g)
	seal(t, e, h)
	if err := e.VerifyHeader(chain, h, true); err != nil {
		t.Fatalf("valid sealed header rejected: %v", err)
	}
	// Tamper with the nonce: the seal must fail.
	h.Nonce = types.EncodeNonce(h.Nonce.Uint64() + 1)
	if err := e.VerifyHeader(chain, h, true); !errors.Is(err, errInvalidPoW) {
		t.Fatalf("tampered nonce: have %v want errInvalidPoW", err)
	}
}

// TestSealHeightFromChainContext is the GPU team's requirement (and the
// security seat's TP item): the height fed to the proof of work comes from
// the validated chain context, never from the block's own claim.
func TestSealHeightFromChainContext(t *testing.T) {
	e := newTestEngine(t)
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)
	h := childOf(g)
	// A seal computed for height 2, placed on a header whose chain height is 1.
	nonce, mix, ok := pow.Solve(e.algo, e.SealHash(h), 2, h.Difficulty, 0, nil)
	if !ok {
		t.Fatal("solver aborted")
	}
	h.Nonce, h.MixDigest = types.EncodeNonce(nonce), mix
	if err := e.VerifyHeader(chain, h, true); !errors.Is(err, errInvalidPoW) {
		t.Fatalf("seal for another height accepted: %v", err)
	}
	// The same header with the height it declares changed to 2: the number
	// rule rejects it before the seal is looked at.
	h2 := types.CopyHeader(h)
	h2.Number = big.NewInt(2)
	if err := e.verifyHeader(chain, h2, g, nil, true, time.Now().Unix()); !errors.Is(err, consensus.ErrInvalidNumber) {
		t.Fatalf("wrong declared height: have %v want ErrInvalidNumber", err)
	}
	// verifySeal itself refuses a header whose Number disagrees with the context.
	seal(t, e, h)
	if err := e.verifySeal(nil, h, 2, false); !errors.Is(err, errInvalidPoW) {
		t.Fatalf("verifySeal with a disagreeing context height: have %v want errInvalidPoW", err)
	}
	if err := e.verifySeal(nil, h, 1, false); err != nil {
		t.Fatalf("verifySeal with the right context height: %v", err)
	}
}

func TestVerifyHeaderRules(t *testing.T) {
	e := newTestEngine(t)
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)

	cases := []struct {
		name   string
		mutate func(h *types.Header)
		want   error // nil means "any error"
	}{
		{"extra too long", func(h *types.Header) { h.Extra = make([]byte, 33) }, nil},
		{"future block", func(h *types.Header) { h.Time = uint64(time.Now().Unix()) + 100 }, consensus.ErrFutureBlock},
		{"older than parent", func(h *types.Header) { h.Time = g.Time }, errOlderBlockTime},
		{"uncle hash present", func(h *types.Header) { h.UncleHash = common.HexToHash("0x01") }, errUnclesNotAllowed},
		{"wrong difficulty", func(h *types.Header) { h.Difficulty = big.NewInt(1) }, nil},
		{"zero difficulty", func(h *types.Header) { h.Difficulty = big.NewInt(0) }, errInvalidDifficulty},
		{"gas used above limit", func(h *types.Header) { h.GasUsed = h.GasLimit + 1 }, nil},
		{"gas limit jump", func(h *types.Header) { h.GasLimit = g.GasLimit * 2 }, nil},
		{"base fee before 1559", func(h *types.Header) { h.BaseFee = big.NewInt(1) }, nil},
		{"withdrawals hash", func(h *types.Header) { x := types.EmptyWithdrawalsHash; h.WithdrawalsHash = &x }, errUnsupportedWithdraw},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := childOf(g)
			tc.mutate(h)
			err := e.VerifyHeader(chain, h, false)
			if err == nil {
				t.Fatalf("%s: accepted", tc.name)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("%s: have %v want %v", tc.name, err, tc.want)
			}
		})
	}
	// The number rule, with the parent supplied directly (through the chain
	// the parent lookup fails first).
	bad := childOf(g)
	bad.Number = big.NewInt(5)
	if err := e.verifyHeader(chain, bad, g, nil, false, time.Now().Unix()); !errors.Is(err, consensus.ErrInvalidNumber) {
		t.Fatalf("wrong number: have %v want ErrInvalidNumber", err)
	}
	// The unmutated child passes without a seal check.
	if err := e.VerifyHeader(chain, childOf(g), false); err != nil {
		t.Fatalf("plain child rejected: %v", err)
	}
	// Unknown parent.
	orphan := childOf(g)
	orphan.ParentHash = common.HexToHash("0xdead")
	if err := e.VerifyHeader(chain, orphan, false); !errors.Is(err, consensus.ErrUnknownAncestor) {
		t.Fatalf("orphan: have %v want ErrUnknownAncestor", err)
	}
}

// TestDifficultyFollowsParentInterval: the rule takes the parent's interval
// (which needs the grandparent), never the new header's time.
func TestDifficultyFollowsParentInterval(t *testing.T) {
	e := newTestEngine(t)
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)
	h1 := childOf(g)
	chain.add(h1)
	// h1 arrived at the target interval, so h2's difficulty is the floor
	// whatever h2's own time is.
	h2 := childOf(h1)
	h2.Time = h1.Time + 1
	if err := e.VerifyHeader(chain, h2, false); err != nil {
		t.Fatalf("fast child rejected (new time must not count): %v", err)
	}
	// A parent that arrived late (long interval) lowers the next difficulty
	// to the floor; one that arrived fast raises it above the floor.
	fast := childOf(g)
	fast.Time = g.Time + 1
	chain.add(fast)
	want := e.config.Parts.Difficulty.Next(1, fast.Time, g.Time, fast.Difficulty)
	if want.Cmp(testDifficulty) <= 0 {
		t.Fatalf("a 1 s interval must raise the difficulty: %v", want)
	}
	h3 := childOf(fast)
	if err := e.VerifyHeader(chain, h3, false); err == nil {
		t.Fatal("floor difficulty accepted after a fast parent")
	}
	h3.Difficulty = want
	if err := e.VerifyHeader(chain, h3, false); err != nil {
		t.Fatalf("rule difficulty rejected: %v", err)
	}
	if d := e.CalcDifficulty(chain, 0, fast); d.Cmp(want) != 0 {
		t.Fatalf("CalcDifficulty: have %v want %v", d, want)
	}
	// Prepare fills the same value.
	h4 := childOf(fast)
	h4.Difficulty = nil
	if err := e.Prepare(chain, h4); err != nil || h4.Difficulty.Cmp(want) != 0 {
		t.Fatalf("Prepare: %v %v", err, h4.Difficulty)
	}
	// An unknown grandparent is an error, not a guess.
	lonely := childOf(fast)
	if err := e.verifyHeader(chain, lonely, fast, nil, false, time.Now().Unix()); err == nil {
		// verifyHeader with a nil grandparent treats the parent as genesis-like;
		// the engine paths never do that for a non-genesis parent:
		parentOnly := types.CopyHeader(fast)
		parentOnly.ParentHash = common.HexToHash("0xbeef")
		chain.add(parentOnly)
		if err := e.VerifyHeader(chain, childOf(parentOnly), false); !errors.Is(err, errUnknownGrandparent) {
			t.Fatalf("unknown grandparent: have %v want errUnknownGrandparent", err)
		}
	}
}

func TestVerifyHeadersBatchOrder(t *testing.T) {
	e := newTestEngine(t)
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)
	var headers []*types.Header
	parent := g
	for i := 0; i < 5; i++ {
		h := childOf(parent)
		headers = append(headers, h)
		parent = h
	}
	headers[3].GasUsed = headers[3].GasLimit + 1 // one bad header in the middle
	abort, results := e.VerifyHeaders(chain, headers, make([]bool, len(headers)))
	defer close(abort)
	for i := range headers {
		err := <-results
		if i == 3 && err == nil {
			t.Fatal("bad header 3 accepted")
		}
		if i < 3 && err != nil {
			t.Fatalf("header %d rejected: %v", i, err)
		}
	}
}

func TestUnclesRefused(t *testing.T) {
	e := newTestEngine(t)
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)
	h := childOf(g)
	block := types.NewBlock(h, nil, []*types.Header{childOf(g)}, nil, nil)
	if err := e.VerifyUncles(nil, block); !errors.Is(err, errUnclesNotAllowed) {
		t.Fatalf("block with an uncle: have %v want errUnclesNotAllowed", err)
	}
	if err := e.VerifyUncles(nil, types.NewBlockWithHeader(h)); err != nil {
		t.Fatalf("block without uncles: %v", err)
	}
}

func TestFakerAcceptsAnySeal(t *testing.T) {
	e := NewFaker(testParts())
	defer e.Close()
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)
	h := childOf(g)
	if err := e.VerifyHeader(chain, h, true); err != nil {
		t.Fatalf("faker rejected an unsealed header: %v", err)
	}
}

// TestFinalizeCredits: the schedule's two amounts go to the coinbase and the
// fund, and nothing else is credited.
func TestFinalizeCredits(t *testing.T) {
	e := newTestEngine(t)
	chain := newFakeChain()
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := childOf(genesisHeader())
	h.Coinbase = common.HexToAddress("0xc0")
	e.Finalize(chain, h, statedb, nil, nil, nil)
	miner, fund := e.config.Parts.Issuance.Reward(1)
	if got := statedb.GetBalance(h.Coinbase).ToBig(); got.Cmp(miner) != 0 {
		t.Fatalf("coinbase credited %v want %v", got, miner)
	}
	if got := statedb.GetBalance(testFund).ToBig(); got.Cmp(fund) != 0 {
		t.Fatalf("fund credited %v want %v", got, fund)
	}
	if fund.Sign() <= 0 || miner.Sign() <= 0 {
		t.Fatalf("candidate schedule pays nothing at height 1: %v %v", miner, fund)
	}
	// Genesis pays nothing.
	g := genesisHeader()
	g.Coinbase = common.HexToAddress("0xc1")
	e.Finalize(chain, g, statedb, nil, nil, nil)
	if got := statedb.GetBalance(g.Coinbase); !got.IsZero() {
		t.Fatalf("genesis credited %v", got)
	}
}

// TestFinalizeCreditSaturates is F-11: a reward that would carry a balance
// past 2^256 - 1 saturates instead of wrapping, on the coinbase and on the
// fund address alike, and a balance below the edge is credited exactly.
func TestFinalizeCreditSaturates(t *testing.T) {
	e := newTestEngine(t)
	chain := newFakeChain()
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := childOf(genesisHeader())
	h.Coinbase = common.HexToAddress("0xc0")
	miner, fund := e.config.Parts.Issuance.Reward(1)
	if miner.Sign() <= 0 || fund.Sign() <= 0 {
		t.Fatalf("candidate schedule pays nothing at height 1: %v %v", miner, fund)
	}
	max := new(uint256.Int).SetAllOne()

	// One wei short of the edge on the coinbase; the fund address at the edge.
	statedb.SetBalance(h.Coinbase, new(uint256.Int).Sub(max, uint256.NewInt(1)))
	statedb.SetBalance(testFund, max)
	e.Finalize(chain, h, statedb, nil, nil, nil)
	if got := statedb.GetBalance(h.Coinbase); !got.Eq(max) {
		t.Fatalf("coinbase at the edge credited to %v, want saturation at %v", got, max)
	}
	if got := statedb.GetBalance(testFund); !got.Eq(max) {
		t.Fatalf("fund at the edge credited to %v, want saturation at %v", got, max)
	}

	// Below the edge by exactly the reward: credited to the edge, no wrap.
	h2 := childOf(h)
	h2.Coinbase = common.HexToAddress("0xc2")
	minerU := uint256.MustFromBig(miner)
	statedb.SetBalance(h2.Coinbase, new(uint256.Int).Sub(max, minerU))
	e.Finalize(chain, h2, statedb, nil, nil, nil)
	if got := statedb.GetBalance(h2.Coinbase); !got.Eq(max) {
		t.Fatalf("coinbase one reward below the edge credited to %v, want %v", got, max)
	}
}

func TestMiningRPCRoundTrip(t *testing.T) {
	e := newTestEngine(t)
	e.SetThreads(-1) // remote only
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)
	h := childOf(g)
	// The pending block carries a transaction: the sealed block must keep
	// it (a header-only solution dropped every body of a remote-sealed
	// block, so the state carried transfers no block or receipt showed; the
	// pool's development chain, 10 October 2026).
	tx := types.NewTransaction(0, common.HexToAddress("0x01"), big.NewInt(1), 21000, big.NewInt(1), nil)
	block := types.NewBlock(h, []*types.Transaction{tx}, nil, nil, trie.NewStackTrie(nil))
	h = block.Header()

	results := make(chan *types.Block, 1)
	if err := e.Seal(chain, block, results, nil); err != nil {
		t.Fatal(err)
	}
	api := &IgneumAPI{engine: e}
	legacy := &API{e}

	w, err := api.GetWork(nil)
	if err != nil {
		t.Fatal(err)
	}
	if w.Algorithm != null.Name {
		t.Fatalf("work algorithm: have %q want %q", w.Algorithm, null.Name)
	}
	if w.HeaderHash != e.SealHash(h) || w.Height != 1 {
		t.Fatal("work package does not describe the pending block")
	}
	if lw, err := legacy.GetWork(); err != nil || lw[0] != w.HeaderHash.Hex() || lw[3] != "0x1" {
		t.Fatalf("legacy work: %v %v", lw, err)
	}
	// A wrong submission is refused with a reason.
	if r := api.SubmitWork(types.EncodeNonce(1), common.HexToHash("0x01"), common.Hash{}, nil); r.Accepted || r.Reason == "" {
		t.Fatalf("unknown work accepted or no reason: %+v", r)
	}
	if r := api.SubmitWork(types.EncodeNonce(1), w.HeaderHash, common.Hash{}, nil); r.Accepted || r.Reason == "" {
		t.Fatalf("invalid solution accepted or no reason: %+v", r)
	}
	// The real solution is accepted and the sealed block comes back.
	nonce, mix, ok := pow.Solve(e.algo, w.HeaderHash, w.Height, h.Difficulty, 0, nil)
	if !ok {
		t.Fatal("solver aborted")
	}
	if !legacy.SubmitWork(types.EncodeNonce(nonce), w.HeaderHash, mix, nil) {
		t.Fatal("valid solution rejected")
	}
	select {
	case sealed := <-results:
		if sealed.Nonce() != nonce || sealed.MixDigest() != mix {
			t.Fatal("sealed block carries another solution")
		}
		if len(sealed.Transactions()) != 1 || sealed.Transactions()[0].Hash() != tx.Hash() {
			t.Fatalf("the sealed block lost its body: %d transactions", len(sealed.Transactions()))
		}
		if err := e.verifySeal(nil, sealed.Header(), 1, false); err != nil {
			t.Fatalf("sealed block does not verify: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no sealed block delivered")
	}
	// Hashrate reporting.
	if !legacy.SubmitHashrate(1000, common.HexToHash("0xaa")) {
		t.Fatal("hashrate not accepted")
	}
	if legacy.GetHashrate() < 1000 {
		t.Fatalf("hashrate: have %d want >= 1000", legacy.GetHashrate())
	}
}

func TestCPUSealFindsBlock(t *testing.T) {
	e := newTestEngine(t)
	e.SetThreads(1)
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)
	block := types.NewBlockWithHeader(childOf(g))
	results := make(chan *types.Block, 1)
	stop := make(chan struct{})
	defer close(stop)
	if err := e.Seal(chain, block, results, stop); err != nil {
		t.Fatal(err)
	}
	select {
	case sealed := <-results:
		if err := e.verifySeal(nil, sealed.Header(), 1, false); err != nil {
			t.Fatalf("CPU-sealed block does not verify: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CPU sealer found nothing")
	}
}

// TestSubmitAdmission is the mining RPC's admission order (the specification's
// section 11, IH 09): unknown, stale and duplicate submissions are refused in
// the sealer's loop before any proof is checked; a flood past the queue is
// refused as busy at once; and igneum_getWork answers while verifications are
// in flight, because the loop never verifies.
func TestSubmitAdmission(t *testing.T) {
	e := newTestEngine(t)
	e.SetThreads(-1) // remote only
	e.remote.verifyDelay = 50 * time.Millisecond
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)
	h := childOf(g)
	block := types.NewBlockWithHeader(h)
	results := make(chan *types.Block, 64)
	if err := e.Seal(chain, block, results, nil); err != nil {
		t.Fatal(err)
	}
	api := &IgneumAPI{engine: e}
	w, err := api.GetWork(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Unknown work is refused before anything (no delay).
	start := time.Now()
	if r := api.SubmitWork(types.EncodeNonce(1), common.HexToHash("0x01"), common.Hash{}, nil); r.Accepted || !strings.Contains(r.Reason, "no live job") {
		t.Fatalf("unknown work: %+v", r)
	}
	if time.Since(start) > 20*time.Millisecond {
		t.Fatal("an unknown submission reached the verifier")
	}
	// A first submission is admitted and reaches the verifier (which refuses
	// its mix digest after the delay); the same (nonce, mix) again is a
	// duplicate, refused before the verifier.
	start = time.Now()
	if r := api.SubmitWork(types.EncodeNonce(7), w.HeaderHash, common.Hash{7}, nil); r.Accepted || !strings.Contains(r.Reason, "mix digest") {
		t.Fatalf("first submission: %+v", r)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("the first submission did not reach the verifier")
	}
	start = time.Now()
	if r := api.SubmitWork(types.EncodeNonce(7), w.HeaderHash, common.Hash{7}, nil); r.Accepted || !strings.Contains(r.Reason, "duplicate") {
		t.Fatalf("duplicate: %+v", r)
	}
	if time.Since(start) > 20*time.Millisecond {
		t.Fatal("a duplicate reached the verifier")
	}
	// A flood past the queue: every admitted submission takes the fake delay,
	// so more than the queue plus the workers in flight are refused as busy,
	// and getWork answers meanwhile.
	flood := SubmitQueueDepth() + VerifyWorkers() + 16
	var busy, admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < flood; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := api.SubmitWork(types.EncodeNonce(uint64(100+i)), w.HeaderHash, common.Hash{byte(i), 1}, nil)
			if strings.Contains(r.Reason, "busy") {
				busy.Add(1)
			} else {
				admitted.Add(1)
			}
		}(i)
	}
	time.Sleep(10 * time.Millisecond)
	start = time.Now()
	if _, err := api.GetWork(nil); err != nil {
		t.Fatalf("getWork during the flood: %v", err)
	}
	if time.Since(start) > 40*time.Millisecond {
		t.Fatalf("getWork waited %v behind the flood", time.Since(start))
	}
	wg.Wait()
	if busy.Load() == 0 {
		t.Fatalf("no submission refused as busy in a flood of %d over a queue of %d and %d workers", flood, SubmitQueueDepth(), VerifyWorkers())
	}
	if admitted.Load() > int32(SubmitQueueDepth()+VerifyWorkers()+1) {
		t.Fatalf("%d admitted, more than the queue and the workers", admitted.Load())
	}
	t.Logf("flood %d: %d admitted, %d busy; workers %d, queue %d", flood, admitted.Load(), busy.Load(), VerifyWorkers(), SubmitQueueDepth())
	// F-418: once the head has moved off the template's parent, a solution
	// against that template is refused at admission as stale (before the
	// verifier, no delay), never answered accepted and dropped later.
	e.setChain(chain)
	twin := childOf(g)
	twin.Time++ // another block at the template's height: the head moves off the template's parent
	chain.add(twin)
	chain.head = twin
	start = time.Now()
	if r := api.SubmitWork(types.EncodeNonce(9), w.HeaderHash, common.Hash{9}, nil); r.Accepted || !strings.Contains(r.Reason, "parent is not the chain's head") {
		t.Fatalf("a template off the head: %+v", r)
	}
	if time.Since(start) > 20*time.Millisecond {
		t.Fatal("a stale-parent submission reached the verifier")
	}
}

// TestSeenRing is security's F-19: the duplicate ring forgets its oldest
// entry past the bound, so a flood of distinct submissions never makes the
// bound refuse a later honest one, while a duplicate within the ring is
// still refused.
func TestSeenRing(t *testing.T) {
	r := newSeenRing()
	key := func(i int) seenKey {
		return seenKey{nonce: types.EncodeNonce(uint64(i)), mix: common.Hash{byte(i >> 8), byte(i)}}
	}
	for i := 0; i < MaxSeenPerWork; i++ {
		r.add(key(i))
	}
	if !r.has(key(0)) || !r.has(key(MaxSeenPerWork-1)) || len(r.set) != MaxSeenPerWork {
		t.Fatal("ring does not hold its bound")
	}
	r.add(key(MaxSeenPerWork)) // the oldest leaves
	if r.has(key(0)) || !r.has(key(1)) || !r.has(key(MaxSeenPerWork)) || len(r.set) != MaxSeenPerWork {
		t.Fatal("ring did not forget its oldest entry")
	}
	for i := 0; i < 3*MaxSeenPerWork; i++ {
		r.add(key(100000 + i))
	}
	if len(r.set) != MaxSeenPerWork {
		t.Fatalf("ring holds %d after a flood, bound %d", len(r.set), MaxSeenPerWork)
	}
}

// TestVerifyWorkersReserve: the verifier count leaves the reserved share free.
func TestVerifyWorkersReserve(t *testing.T) {
	n := runtime.GOMAXPROCS(0)
	w := VerifyWorkers()
	if w < 1 || w >= n && n > 1 {
		t.Fatalf("workers %d of %d CPUs", w, n)
	}
	if SubmitQueueDepth() != SubmitQueuePerWorker*w {
		t.Fatal("queue depth is not the declared multiple")
	}
}

// TestScheduleTransitionReorg: the bounded menu through the engine. A chain
// configuration with one transition at height 4 verifies headers on each
// side with the document in force at their height (the digests differ), and
// a reorganisation across the transition height re-verifies each side with
// its own entry; the work package carries the document in force at the
// template's height.
func TestScheduleTransitionReorg(t *testing.T) {
	cfg := devEngineBlock(igneumhash.Name)
	cfg.AlgorithmParams = []byte(`{"version": 1, "cache_mib": 1}`)
	cfg.Schedule = &ctypes.IgneumSchedule{
		Entries:  []ctypes.IgneumScheduleEntry{{Name: "more-rounds", Height: 4, AlgorithmParams: []byte(`{"version": 1, "rounds": 128, "cache_mib": 1}`)}},
		Terminal: ctypes.IgneumTerminal{Hold: true},
	}
	e, err := NewFromChainConfig(cfg, testParts(), nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.SetThreads(-1)
	chain := newFakeChain()
	g := genesisHeader()
	chain.add(g)
	seal := func(parent *types.Header, extra []byte) *types.Header {
		h := childOf(parent)
		h.Extra = extra
		var gp *types.Header
		if parent.Number.Sign() > 0 {
			gp = chain.GetHeader(parent.ParentHash, parent.Number.Uint64()-1)
		}
		h.Difficulty = e.calcDifficulty(parent, gp)
		nonce, mix, ok := pow.Solve(e.algo, e.SealHash(h), h.Number.Uint64(), h.Difficulty, 0, nil)
		if !ok {
			t.Fatal("no solution")
		}
		h.Nonce, h.MixDigest = types.EncodeNonce(nonce), mix
		return h
	}
	// The main branch: heights 1 to 6 across the transition at 4.
	var branch []*types.Header
	parent := g
	for i := 0; i < 6; i++ {
		h := seal(parent, nil)
		if err := e.VerifyHeader(chain, h, true); err != nil {
			t.Fatalf("height %d: %v", h.Number, err)
		}
		chain.add(h)
		branch = append(branch, h)
		parent = h
	}
	m := e.algo.(*schedule.Algorithm)
	if m.At(3).Name != "base" || m.At(4).Name != "more-rounds" {
		t.Fatal("schedule not in force")
	}
	// A header sealed at height 5 under the base document (the wrong entry)
	// is refused: the document is chain-bound by height.
	wrong := childOf(branch[3])
	wrong.Difficulty = e.calcDifficulty(branch[3], branch[2])
	nonce, mix, _ := pow.Solve(m.At(0).Algorithm, e.SealHash(wrong), 5, wrong.Difficulty, 0, nil)
	wrong.Nonce, wrong.MixDigest = types.EncodeNonce(nonce), mix
	if err := e.VerifyHeader(chain, wrong, true); err == nil {
		t.Fatal("a proof under the base document verified above the transition")
	}
	// A reorganisation across the transition: a side branch from height 2
	// through 7 (heavier by length), each header verified with its own
	// entry: heights 3 under the base, 4 to 7 under more-rounds.
	parent = branch[1]
	for i := 3; i <= 7; i++ {
		h := seal(parent, []byte("side"))
		if err := e.VerifyHeader(chain, h, true); err != nil {
			t.Fatalf("side height %d: %v", h.Number, err)
		}
		chain.add(h)
		parent = h
	}
	// The work package for a template at height 8 carries the more-rounds
	// document and its digest; at height 1 it would carry the base's.
	wp := e.workPackage(types.NewBlockWithHeader(childOf(parent)))
	if doc := wp.Params.(igneumhash.Document); doc.Rounds != 128 || wp.ParamsDigest != m.ParamsDigestAt(8) || wp.ParamsDigest == m.ParamsDigestAt(0) {
		t.Fatalf("work package at 8: %+v digest %x", wp.Params, wp.ParamsDigest)
	}
}

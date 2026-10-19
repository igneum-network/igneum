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

package core

import (
	"bytes"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
	"github.com/ethereum/go-ethereum/trie"
)

// TestDAGConflictAcrossParallelBlocks is the design's double-spend rule
// (docs/money/ORDERING-DESIGN-2026-10-18.md section 4): two parallel blocks
// A and B, children of the genesis, each carrying one of two transactions
// of the SAME nonce from one sender; a block C that selects the one of
// greater blue work (equal here: the greater hash) and merges the other.
// The ordering decides: C's state is the selected parent's, the merged
// block's transaction is NOT accepted at its ordered position (the nonce is
// spent), C is valid, and the loser's recipient holds nothing.
func TestDAGConflictAcrossParallelBlocks(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	gspec := &genesisT.Genesis{
		Config: params.IgneumTestChainConfig,
		Alloc:  genesisT.GenesisAlloc{sender: {Balance: new(big.Int).Mul(big.NewInt(10), big.NewInt(1e18))}},
	}
	engine := igneum.NewFaker(igneum.TestParts())
	engine.SetDAGFrom(1, 0)
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, gspec, nil, engine, vm.Config{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	signer := types.LatestSigner(gspec.Config)
	p, q := common.HexToAddress("0x0000000000000000000000000000000000000ab1"), common.HexToAddress("0x0000000000000000000000000000000000000ab2")
	mk := func(to common.Address) *types.Transaction {
		return types.MustSignNewTx(key, signer, &types.DynamicFeeTx{ChainID: gspec.Config.GetChainID(), Nonce: 0, GasTipCap: big.NewInt(1e9), GasFeeCap: big.NewInt(4e9), Gas: 21000, To: &to, Value: big.NewInt(1)})
	}
	txP, txQ := mk(p), mk(q)
	// The two blocks are made under the same gated engine the chain runs,
	// so their state roots carry the DAG form's pay (the genesis, their one
	// mergeset blue, issues nothing; their own subsidy is their merging
	// block's to pay).
	_, blocksA, _ := GenerateChainWithGenesis(gspec, engine, 1, func(i int, g *BlockGen) {
		g.SetCoinbase(common.HexToAddress("0xa1"))
		g.AddTx(txP)
	})
	_, blocksB, _ := GenerateChainWithGenesis(gspec, engine, 1, func(i int, g *BlockGen) {
		g.SetCoinbase(common.HexToAddress("0xb1"))
		g.AddTx(txQ)
	})
	a, b := blocksA[0], blocksB[0]
	if a.Hash() == b.Hash() {
		t.Fatal("the two parallel blocks are one")
	}
	if _, err := chain.InsertChain(types.Blocks{a}); err != nil {
		t.Fatalf("A: %v", err)
	}
	if _, err := chain.InsertChain(types.Blocks{b}); err != nil {
		t.Fatalf("B: %v", err)
	}
	// Equal blue work: the selected parent is the greater hash.
	sp, merged := a, b
	ah, bh := a.Hash(), b.Hash()
	if bytes.Compare(bh[:], ah[:]) > 0 {
		sp, merged = b, a
	}
	if chain.CurrentBlock().Hash() != sp.Hash() {
		t.Fatalf("the head is %x, want the greater hash %x", chain.CurrentBlock().Hash().Bytes()[:4], sp.Hash().Bytes()[:4])
	}
	header := &types.Header{
		ParentHash: sp.Hash(),
		Number:     new(big.Int).Add(sp.Number(), common.Big1),
		GasLimit:   sp.GasLimit(),
		Time:       sp.Time() + 1,
		Coinbase:   common.HexToAddress("0xc1"),
		BaseFee:    eip1559.CalcBaseFee(gspec.Config, sp.Header()),
	}
	if err := engine.Prepare(chain, header); err != nil {
		t.Fatal(err)
	}
	header.SetMergedParents([]common.Hash{merged.Hash()})
	statedb, err := chain.StateAt(sp.Root())
	if err != nil {
		t.Fatal(err)
	}
	recs, err := ApplyMergeset(gspec.Config, chain, engine, header, statedb, vm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Accepted || !strings.Contains(recs[0].Reason, "nonce too low") || recs[0].Block != merged.Hash() {
		t.Fatalf("the merged transaction's record: %+v", recs)
	}
	engine.Finalize(chain, header, statedb, nil, nil, nil)
	header.Root = statedb.IntermediateRoot(gspec.Config.IsEnabled(gspec.Config.GetEIP161dTransition, header.Number))
	header.UncleHash, header.TxHash, header.ReceiptHash = types.EmptyUncleHash, types.EmptyTxsHash, types.EmptyReceiptsHash
	c := types.NewBlock(header, nil, nil, nil, trie.NewStackTrie(nil))
	if _, err := chain.InsertChain(types.Blocks{c}); err != nil {
		t.Fatalf("C refused: %v", err)
	}
	if chain.CurrentBlock().Hash() != c.Hash() {
		t.Fatal("C is not the head")
	}
	st, err := chain.State()
	if err != nil {
		t.Fatal(err)
	}
	winner, loser := p, q
	if sp == b {
		winner, loser = q, p
	}
	if st.GetBalance(winner).Uint64() != 1 || st.GetBalance(loser).Uint64() != 0 {
		t.Fatalf("balances: winner %v loser %v", st.GetBalance(winner), st.GetBalance(loser))
	}
	if st.GetNonce(sender) != 1 {
		t.Fatalf("the sender's nonce %d, want 1 (the note spent once)", st.GetNonce(sender))
	}
	// The subsidy of C's mergeset blues at their orders: the selected
	// parent (order 1) and the merged block (order 2), each to its own
	// coinbase; C's own coinbase waits for its merging block.
	m1, _ := igneum.TestParts().Issuance.Reward(1)
	m2, _ := igneum.TestParts().Issuance.Reward(2)
	if got := st.GetBalance(sp.Coinbase()).ToBig(); got.Cmp(new(big.Int).Add(m1, new(big.Int).SetUint64(21000*1e9))) != 0 {
		t.Fatalf("the selected parent's coinbase holds %v, want Reward(1) plus its block's tip", got)
	}
	// The merged block's transaction never executed in this lineage, so
	// its tip is not paid (the design's section 4: "no fee is paid for it
	// because it never executed"): the merged coinbase holds Reward(2) alone.
	if got := st.GetBalance(merged.Coinbase()).ToBig(); got.Cmp(m2) != 0 {
		t.Fatalf("the merged block's coinbase holds %v, want Reward(2) alone (the refused transaction's tip unpaid)", got)
	}
	if got := st.GetBalance(header.Coinbase); !got.IsZero() {
		t.Fatalf("C's own coinbase holds %v before its merging block", got)
	}
}

// TestDAGParentsRefusedOffTheGate: on a chain without the ordering (the
// gate zero, every chain of record) a header carrying merged parents is
// refused, and one without is what it was.
func TestDAGParentsRefusedOffTheGate(t *testing.T) {
	gspec := &genesisT.Genesis{Config: params.IgneumTestChainConfig}
	engine := igneum.NewFaker(igneum.TestParts())
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, gspec, nil, engine, vm.Config{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	_, blocks, _ := GenerateChainWithGenesis(gspec, igneum.NewFaker(igneum.TestParts()), 2, nil)
	if _, err := chain.InsertChain(blocks[:1]); err != nil {
		t.Fatal(err)
	}
	h := types.CopyHeader(blocks[1].Header())
	if err := engine.VerifyHeader(chain, h, false); err != nil {
		t.Fatalf("a plain header refused: %v", err)
	}
	h.SetMergedParents([]common.Hash{blocks[0].Hash()})
	if err := engine.VerifyHeader(chain, h, false); err == nil || !strings.Contains(err.Error(), "the gate is zero") {
		t.Fatalf("a header with merged parents off the gate: %v", err)
	}
	if DAGOn(engine, 1) {
		t.Fatal("the ordering on without a gate")
	}
	// The header's bytes without merged parents are unchanged: the field
	// encodes as absent.
	plain := types.CopyHeader(blocks[1].Header())
	withNil := types.CopyHeader(blocks[1].Header())
	withNil.SetMergedParents(nil)
	if plain.Hash() != withNil.Hash() || len(withNil.Parents) != 0 {
		t.Fatal("an empty merged-parents field changes the header")
	}
}

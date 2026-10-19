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

package miner

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// TestTemplateParentUnderPolicy45: policy part 5, among two twins at the head's
// height with equal cumulative weight the template's parent is the twin the
// engine's first-seen record names (the one it endorsed), and the canonical
// head when the record names the head itself or no twin is held; policy part
// 4, a head whose stamp is older than its live arrival by more than one round
// is not built on, the template's parent is its parent, one height back only.
func TestTemplateParentUnderPolicy45(t *testing.T) {
	t.Parallel()
	var (
		db          = rawdb.NewMemoryDatabase()
		chainConfig = params.IgneumTestChainConfig
		parts       = igneum.TestParts()
	)
	// A round length for the past-drift read (PastDrifted needs Delta), the
	// sortition rule itself off at every height the test reaches (Height
	// far ahead), so the faker's templates are the plain ones.
	parts.Sortition = &igneum.SortitionParams{Height: 1 << 40, Delta: 20}
	engine := igneum.NewFaker(parts)
	w, b := newTestWorker(t, chainConfig, engine, db, 2)
	defer w.close()
	head := b.chain.CurrentBlock()
	if head.Number.Uint64() != 2 {
		t.Fatalf("head %d, want 2", head.Number.Uint64())
	}
	// A twin of the head: a second block 2 on block 1 with another
	// coinbase, imported as a side block (equal weight under the faker).
	parent1 := b.chain.GetBlockByNumber(1)
	twins, _ := core.GenerateChain(chainConfig, parent1, engine, db, 1, func(i int, gen *core.BlockGen) {
		gen.SetCoinbase(common.Address{0x55})
	})
	if _, err := b.chain.InsertChain(twins); err != nil {
		t.Fatalf("the twin's import: %v", err)
	}
	// At equal weight the engine's tie rule (PA-47: the lower seal hash
	// under the faker) names the canonical one; the test takes whichever
	// is canonical after the import as the head and the other as the twin.
	headHash := b.chain.CurrentBlock().Hash()
	var twin *types.Block
	if headHash == head.Hash() {
		twin = twins[0]
	} else if headHash == twins[0].Hash() {
		twin = b.chain.GetBlock(head.Hash(), 2)
		head = b.chain.CurrentBlock()
	} else {
		t.Fatalf("the head after the import is neither twin: %v", headHash)
	}
	if td1, td2 := b.chain.GetTd(head.Hash(), 2), b.chain.GetTd(twin.Hash(), 2); td1 == nil || td2 == nil || td1.Cmp(td2) != 0 {
		t.Fatalf("the twins' weights differ: %v %v", td1, td2)
	}
	parentOf := func() common.Hash {
		env, err := w.prepareWork(&generateParams{timestamp: uint64(time.Now().Unix()), coinbase: testBankAddress})
		if err != nil {
			t.Fatalf("prepareWork: %v", err)
		}
		defer env.discard()
		return env.header.ParentHash
	}
	// No first-seen record at 2: the canonical head.
	if p := parentOf(); p != head.Hash() {
		t.Fatalf("without a record the parent is %v, want the head %v", p, head.Hash())
	}
	// The first-seen record names the twin (the endorser endorsed it): the
	// template builds on the twin.
	engine.NoteSeen(twin.Header())
	if p := parentOf(); p != twin.Hash() {
		t.Fatalf("with the twin first seen the parent is %v, want the twin %v (policy part 5)", p, twin.Hash())
	}
	// Part 4: the generated blocks are stamped seconds after the epoch
	// (GenerateChain's parent time plus ten), so the twin, recorded as
	// arriving NOW on the live path, is older than its arrival by far more
	// than a round: the template no longer builds on it, nor on the head
	// (the twin is the first seen and the head is the lot's; the walk-back
	// is from the chosen parent one height), it builds on block 1.
	if head.Time > 1_000_000 {
		t.Fatalf("the generated head's stamp %d is not epoch-era; the test's premise fails", head.Time)
	}
	// The arrival record warms up over eight consecutive live heads (F-51);
	// a twin at a held height then counts.
	for h := uint64(1); h <= 8; h++ {
		engine.NoteArrival(&types.Header{Number: new(big.Int).SetUint64(h), Time: uint64(time.Now().Unix()), Extra: []byte{0xf5, byte(h)}})
	}
	engine.NoteArrival(twin.Header())
	if !engine.PastDrifted(twin.Header()) {
		t.Fatal("the twin stamped at the epoch does not read past-drifted on arrival")
	}
	// The twin drifted and the head not: part 5 does not switch to a
	// drifted twin, the head stays the parent.
	if p := parentOf(); p != head.Hash() {
		t.Fatalf("with the twin past-drifted the parent is %v, want the head %v", p, head.Hash())
	}
	engine.NoteArrival(head)
	if p := parentOf(); p != parent1.Hash() {
		t.Fatalf("with the head past-drifted the parent is %v, want block 1 %v (policy part 4, one height back)", p, parent1.Hash())
	}
	_ = types.CopyHeader
}

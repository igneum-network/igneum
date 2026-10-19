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
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestArrivalRecordAndPastDrift (policy part 4, the past-drift refusal):
// a block stamped now that arrives now is not past-drifted; one stamped
// more than Delta before its arrival is; a block with no arrival record
// (a sync's segment, a healed partition, a node started at it) never is;
// the first-seen read is served under the lock; the record is bounded.
func TestArrivalRecordAndPastDrift(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.Delta = 20
	now := uint64(time.Now().Unix())
	// The record warms up over arrivalWarmup consecutive heights (F-51).
	warmUp(e, 500-arrivalWarmup, 499, now)
	fresh := &types.Header{Number: big.NewInt(500), Time: now, Extra: []byte{1}}
	stale := &types.Header{Number: big.NewInt(500), Time: now - 2*s.Delta - 5, Extra: []byte{2}}
	edge := &types.Header{Number: big.NewInt(500), Time: now - 2*s.Delta, Extra: []byte{3}}
	noRecord := &types.Header{Number: big.NewInt(500), Time: now - 3600, Extra: []byte{4}}
	for _, h := range []*types.Header{fresh, stale, edge} {
		e.NoteArrival(h)
		e.NoteSeen(h)
	}
	if e.PastDrifted(fresh) {
		t.Fatal("a block stamped at its arrival reads past-drifted")
	}
	if !e.PastDrifted(stale) {
		t.Fatal("a block stamped two rounds + 5 s before its arrival does not read past-drifted")
	}
	if e.PastDrifted(edge) {
		t.Fatal("a block stamped exactly two rounds before its arrival reads past-drifted (the bound is strict; an honest search runs the round out)")
	}
	if e.PastDrifted(noRecord) {
		t.Fatal("a block with no arrival record reads past-drifted")
	}
	if e.PastDrifted(nil) {
		t.Fatal("nil reads past-drifted")
	}
	// The first-seen read: the first noted at the height.
	if first, ok := e.FirstSeenAt(500); !ok || first != fresh.Hash() {
		t.Fatalf("first seen at 500: %v %v, want %v", first, ok, fresh.Hash())
	}
	// A second arrival of the same block keeps the first time.
	e.NoteArrival(stale)
	if !e.PastDrifted(stale) {
		t.Fatal("a second arrival changed the record")
	}
	// Bounded: a block far ahead drops the old records (the record warmed
	// up again past the gap).
	far := &types.Header{Number: big.NewInt(500 + firstSeenKeep + 10), Time: now}
	warmUp(e, 500+firstSeenKeep+10-arrivalWarmup, 500+firstSeenKeep+9, now)
	e.NoteArrival(far)
	e.endorse.mu.Lock()
	_, kept := e.endorse.arrivedAt[stale.Hash()]
	e.endorse.mu.Unlock()
	if kept {
		t.Fatal("the arrival record is not bounded")
	}
	// Delta zero (no clock): nothing is past-drifted.
	s.Delta = 0
	if e.PastDrifted(far) {
		t.Fatal("past-drifted under Delta 0")
	}
}

// warmUp notes one fresh arrival at each height from lo to hi, so the
// record counts from the next height.
func warmUp(e *Engine, lo, hi uint64, now uint64) {
	for h := lo; h <= hi; h++ {
		e.NoteArrival(&types.Header{Number: new(big.Int).SetUint64(h), Time: now, Extra: []byte{0xee, byte(h), byte(h >> 8)}})
	}
}

// TestArrivalRecordWarmsUp (F-51): a node's first heads after a start or a
// sync's gap are not recorded (its own lateness read as the sender's past
// stamp on the ninth development chain: pod d at 1021 after its restart,
// pod e2 at 1024 inside its dataset build), the record counting from the
// arrivalWarmup-th consecutive height; a gap in the heights starts the
// count again; a twin at the held height keeps it.
func TestArrivalRecordWarmsUp(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.Delta = 20
	now := uint64(time.Now().Unix())
	old := now - 10*s.Delta
	// Seven consecutive stale heads: none recorded.
	for h := uint64(100); h < 100+arrivalWarmup-1; h++ {
		e.NoteArrival(&types.Header{Number: new(big.Int).SetUint64(h), Time: old, Extra: []byte{1}})
	}
	last := &types.Header{Number: big.NewInt(100 + arrivalWarmup - 2), Time: old, Extra: []byte{1}}
	if e.PastDrifted(last) {
		t.Fatal("a stale head inside the warm-up reads past-drifted")
	}
	// The eighth consecutive: recorded, and stale.
	eighth := &types.Header{Number: big.NewInt(100 + arrivalWarmup - 1), Time: old, Extra: []byte{2}}
	e.NoteArrival(eighth)
	if !e.PastDrifted(eighth) {
		t.Fatal("the first head past the warm-up is not recorded")
	}
	// A twin at the held height keeps the count.
	twin := &types.Header{Number: big.NewInt(100 + arrivalWarmup - 1), Time: old, Extra: []byte{3}}
	e.NoteArrival(twin)
	if !e.PastDrifted(twin) {
		t.Fatal("a twin at the held height lost the count")
	}
	// A gap (a segment's tail): the count starts again.
	gapped := &types.Header{Number: big.NewInt(100 + arrivalWarmup + 40), Time: old, Extra: []byte{4}}
	e.NoteArrival(gapped)
	if e.PastDrifted(gapped) {
		t.Fatal("the first head after a gap is recorded")
	}
}

// TestOwnBlockNeverPastDrifted (F-51): a block this node sealed is never
// past-drifted at its sealer, however long its own search ran (the scratch
// pair's two sealers refused their own heads and stood for good).
func TestOwnBlockNeverPastDrifted(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.Delta = 20
	now := uint64(time.Now().Unix())
	warmUp(e, 700-arrivalWarmup, 699, now)
	own := &types.Header{Number: big.NewInt(700), Time: now - 10*s.Delta, Extra: []byte{5}}
	other := &types.Header{Number: big.NewInt(700), Time: now - 10*s.Delta, Extra: []byte{6}}
	e.NoteOwnBlock(own.Hash(), 700)
	e.NoteArrival(own)
	e.NoteArrival(other)
	if e.PastDrifted(own) {
		t.Fatal("the node's own slow-searched block reads past-drifted at its sealer")
	}
	if !e.PastDrifted(other) {
		t.Fatal("another sealer's block with the same stamp does not read past-drifted")
	}
	// Bounded: an own-block record far behind the head is dropped.
	e.NoteOwnBlock(common.Hash{0xaa}, 700+firstSeenKeep+10)
	e.endorse.mu.Lock()
	_, kept := e.endorse.ownBlocks[own.Hash()]
	e.endorse.mu.Unlock()
	if kept {
		t.Fatal("the own-block record is not bounded")
	}
}

package igneum

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestAuthorityMemoServesTheChildrenWithoutTheState (F-39; the project's order of
// 16:5x UK, 12 October 2026): the facts recorded at a parent's Finalize
// serve a child's rule reads without the state and agree with the state's
// own answers; a fact the memo does not hold reads errMemoMiss without a
// state and the state's answer with one; the memo is bounded.
func TestAuthorityMemoServesTheChildrenWithoutTheState(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	head := chain.head
	st := chain.st
	height := head.Number.Uint64()
	// No memo yet: the state serves, and no state at all misses.
	r0 := e.rulesFor(nil, head.Hash())
	if _, err := r0.tableAt(s.EpochAt(height + 1)); !errors.Is(err, errMemoMiss) {
		t.Fatalf("without a memo or a state: %v (want errMemoMiss)", err)
	}
	// Record the parent's facts as Finalize does.
	e.recordFacts(head.Hash(), height, st)
	if e.MemoSize() != 1 {
		t.Fatalf("memo size %d", e.MemoSize())
	}
	rm := e.rulesFor(nil, head.Hash())  // the memo alone
	rs := e.rulesFor(st, common.Hash{}) // the state alone
	epoch := s.EpochAt(height + 1)
	tm, errm := rm.tableAt(epoch)
	ts, errs := rs.tableAt(epoch)
	if (errm == nil) != (errs == nil) {
		t.Fatalf("table: memo %v, state %v", errm, errs)
	}
	if errm == nil && (tm.seed != ts.seed || tm.nTotal != ts.nTotal || tm.u.Cmp(ts.u) != 0) {
		t.Fatalf("the memo's table differs from the state's")
	}
	// The draw for a key agrees by both paths (the open seat here).
	key := common.Hash{0x42}
	dm, err := e.drawWith(rm, key, height+1)
	if err != nil {
		t.Fatalf("draw from the memo: %v", err)
	}
	ds, err := e.drawWith(rs, key, height+1)
	if err != nil {
		t.Fatalf("draw from the state: %v", err)
	}
	if dm.open != ds.open || dm.round != ds.round || dm.units != ds.units {
		t.Fatalf("the draw differs: memo %+v state %+v", dm, ds)
	}
	// An unregistered key reads no bond and no mark from the memo alone,
	// as the state answers.
	bm, _ := rm.bondOf(key)
	bs, _ := rs.bondOf(key)
	if (bm.Amount == nil || bm.Amount.Sign() == 0) != (bs.Amount == nil || bs.Amount.Sign() == 0) {
		t.Fatalf("the bond differs: memo %v state %v", bm.Amount, bs.Amount)
	}
	if mm, _ := rm.marked(key); mm {
		t.Fatal("an unregistered key reads marked from the memo")
	}
	// The clock: both paths agree (nil before the rule here).
	cm, errcm := rm.clock()
	cs, errcs := rs.clock()
	if (errcm == nil) != (errcs == nil) || (cm == nil) != (cs == nil) {
		t.Fatalf("the clock differs: memo %v/%v state %v/%v", cm, errcm, cs, errcs)
	}
	// A table of another epoch is not the memo's: a miss without a state.
	if _, err := rm.tableAt(epoch + 5); !errors.Is(err, errMemoMiss) {
		t.Fatalf("another epoch from the memo alone: %v (want errMemoMiss)", err)
	}
	// The memo is bounded.
	for i := 0; i < authorityMemoSize+10; i++ {
		h := types.CopyHeader(head)
		h.Number = new(big.Int).SetUint64(height + 1 + uint64(i))
		e.recordFacts(h.Hash(), h.Number.Uint64(), st)
	}
	if e.MemoSize() != authorityMemoSize {
		t.Fatalf("memo size %d, want the bound %d", e.MemoSize(), authorityMemoSize)
	}
	if e.factsFor(head.Hash()) != nil {
		t.Fatal("the oldest parent was not evicted")
	}
}

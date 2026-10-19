//go:build controls

package controls

import (
	"testing"
	"time"
)

// epochBoundary finds the first height whose epoch differs from height
// 1's under the document's schedule (the reference's EPOCH_LENGTH).
func epochBoundary(t *testing.T, a *ihRef) uint64 {
	t.Helper()
	first := a.Epoch(1)
	for h := uint64(2); h < 1<<20; h++ {
		if a.Epoch(h) != first {
			return h
		}
	}
	t.Fatal("no epoch boundary under 2^20")
	return 0
}

func (a *ihRef) held(epoch uint64) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lookup(epoch) != nil
}

// TestLookaheadBuildsTheNextEpochAhead: Anchor within LookaheadBlocks of
// the next epoch's first height builds that epoch's context in the
// background, without a hash asking for it; Anchor far from the boundary
// builds only the head's own epoch; a second Anchor while the build runs
// starts no second build (the ninth development chain's 1024 boundary, 12
// October 2026: six minutes on the slowest core with every hash and import
// waiting, since the build ran on first use under the lock).
func TestLookaheadBuildsTheNextEpochAhead(t *testing.T) {
	doc := []byte(`{"version": 8, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 5152, "chain_id": 7315, "keyed_rounds": 2}`)
	alg, err := newIgneumhashRef(7315, doc)
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	a := alg.(*ihRef)
	boundary := epochBoundary(t, a)
	cur, next := a.Epoch(boundary-1), a.Epoch(boundary)
	if cur == next {
		t.Fatalf("no boundary at %d", boundary)
	}
	wait := func(epoch uint64, want bool, what string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if a.held(epoch) == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("not reached within 60 s: %s", what)
	}
	// Far from the boundary: the head's epoch is built, the next is not.
	if boundary > LookaheadBlocks+2 {
		a.Anchor(boundary - LookaheadBlocks - 2)
		wait(cur, true, "the head's own epoch built at the far anchor")
		time.Sleep(200 * time.Millisecond)
		if a.held(next) {
			t.Fatal("the next epoch was built from an anchor outside the lookahead")
		}
	}
	// Within the lookahead: the next epoch is built ahead, twice asked, once made.
	a.Anchor(boundary - 1)
	a.Anchor(boundary - 1)
	wait(next, true, "the next epoch built ahead of the boundary")
	// The boundary's own hash finds the context held: no build on its path.
	a.mu.RLock()
	ctx := a.lookup(next)
	a.mu.RUnlock()
	if ctx == nil {
		t.Fatal("the context built ahead is not the one held")
	}
	if a.Epoch(boundary) != next {
		t.Fatal("the boundary's epoch moved")
	}
}

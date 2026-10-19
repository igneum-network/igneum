//go:build controls

package controls

import (
	"testing"
	"time"
)

// F-215 (16 October 2026): the adapter holds at most FullContextsHeld full
// contexts (the current and the next epoch's); a third's install frees the
// one farthest from it, and Anchor frees the previous epoch's full context
// once the head is StaleBand blocks past the boundary. The research
// chain's nodes held three epochs' 4 GiB datasets per coordinate (the
// lookahead's next beside the current and the previous never freed) and
// the kernel killed three of them at 19 to 21 GB.
func (a *ihRef) fullHeld() (n int, epochs []uint64) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for i := range a.slot {
		if a.slot[i].set && a.slot[i].full {
			n++
			epochs = append(epochs, a.slot[i].epoch)
		}
	}
	return n, epochs
}

func TestFullContextsBoundedAtInstall(t *testing.T) {
	doc := []byte(`{"version": 8, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 5152, "chain_id": 7315, "keyed_rounds": 2}`)
	alg, err := newIgneumhashRef(7315, doc)
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	a := alg.(*ihRef)
	a.SetFullDataset(true)
	boundary := epochBoundary(t, a)
	e0, e1 := a.Epoch(boundary-1), a.Epoch(boundary)
	third := boundary * 2
	e2 := a.Epoch(third)
	if e2 == e1 || e2 == e0 {
		t.Fatalf("no third epoch at %d", third)
	}
	// Three full contexts installed in turn: the third's install frees the
	// farthest (e0), two stay.
	for _, h := range []uint64{boundary - 1, boundary, third} {
		ctx, err := a.buildAs(h, true)
		if err != nil {
			t.Fatalf("build at %d: %v", h, err)
		}
		a.mu.Lock()
		a.installAs(a.Epoch(h), ctx, true, 64<<20)
		a.mu.Unlock()
	}
	n, epochs := a.fullHeld()
	if n != FullContextsHeld {
		t.Fatalf("full contexts held after three installs: %d %v, want %d", n, epochs, FullContextsHeld)
	}
	if a.held(e0) {
		t.Fatalf("the farthest epoch %d still held after the bound: %v", e0, epochs)
	}
	if !a.held(e1) || !a.held(e2) {
		t.Fatalf("the two nearest epochs not held: %v", epochs)
	}
	a.Close()
}

func TestAnchorFreesThePreviousEpochPastTheBand(t *testing.T) {
	doc := []byte(`{"version": 8, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 5152, "chain_id": 7315, "keyed_rounds": 2}`)
	alg, err := newIgneumhashRef(7315, doc)
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	a := alg.(*ihRef)
	a.SetFullDataset(true)
	boundary := epochBoundary(t, a)
	if boundary <= StaleBand+2 {
		t.Skipf("the document's epoch (%d blocks) is shorter than the band", boundary)
	}
	cur, next := a.Epoch(boundary-1), a.Epoch(boundary)
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
	// Within the lookahead both epochs are built and held.
	a.Anchor(boundary - 1)
	wait(cur, true, "the head's epoch built")
	wait(next, true, "the next epoch built ahead")
	// The boundary crossed and the head inside the band: the previous epoch
	// stays (its late blocks and the reorg window verify on it).
	a.Anchor(boundary)
	a.Anchor(boundary + StaleBand - 1)
	time.Sleep(100 * time.Millisecond)
	if !a.held(cur) {
		t.Fatalf("the previous epoch %d freed inside the band", cur)
	}
	// At the band's end the previous epoch's full context is freed; the
	// head's stays.
	a.Anchor(boundary + StaleBand)
	wait(cur, false, "the previous epoch's full context freed past the band")
	if !a.held(next) {
		t.Fatalf("the head's epoch %d freed with the stale one", next)
	}
	if n, epochs := a.fullHeld(); n > FullContextsHeld {
		t.Fatalf("full contexts held: %d %v, the bound %d", n, epochs, FullContextsHeld)
	}
	a.Close()
}

//go:build controls

package controls

import (
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// F-56 (12 October 2026): at a sealer's first keyed search every search
// thread's first hash found no context (the light ones freed by
// SetFullDataset) and built its own 2 GiB one at once, the lookahead's
// builds beside them, and the cgroup killed the node. The single flight:
// N concurrent first hashes on a fresh full-dataset adapter cost ONE
// build; the lookahead joining the on-demand flight adds none; the next
// epoch's lookahead adds exactly one more.
func TestSingleFlightBuildsOneContextUnderConcurrentFirstHashes(t *testing.T) {
	doc := []byte(`{"version": 8, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 5152, "chain_id": 7315, "keyed_rounds": 2}`)
	alg, err := newIgneumhashRef(7315, doc)
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	a := alg.(*ihRef)
	a.SetFullDataset(true) // the sealer's form: the light contexts dropped, the full ones built
	const threads = 32
	var sk [32]byte
	sk[0], sk[31] = 7, 9 // a test secret: the keyed document's search is the sealer's path (the keyless entry refuses it)
	var wg sync.WaitGroup
	var mixes [threads]common.Hash
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// the lookahead's word of the head, as the engine gives it on
			// every template, interleaved with the first hashes
			if i%4 == 0 {
				a.Anchor(1)
			}
			mixes[i], _, _ = a.ComputeKeyed(common.Hash{1}, 1, uint64(i), &sk, nil)
		}(i)
	}
	wg.Wait()
	for i := 0; i < threads; i++ {
		if mixes[i] == (common.Hash{}) {
			t.Fatalf("thread %d: the hash refused", i)
		}
	}
	if n := a.builds.Load(); n != 1 {
		t.Fatalf("%d context builds under %d concurrent first hashes, want 1", n, threads)
	}
	if !a.held(a.Epoch(1)) {
		t.Fatal("the epoch's context is not held after the flight")
	}
	// the next epoch's lookahead: exactly one more build, once
	boundary := epochBoundary(t, a)
	for i := 0; i < 8; i++ {
		a.Anchor(boundary - 1)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && !a.held(a.Epoch(boundary)) {
		time.Sleep(20 * time.Millisecond)
	}
	if !a.held(a.Epoch(boundary)) {
		t.Fatal("the next epoch's context was not built by the lookahead")
	}
	if n := a.builds.Load(); n != 2 {
		t.Fatalf("%d context builds after the lookahead, want 2 (the current and the next)", n)
	}
}

// The case that killed pod d: the keyed document AND its unkeyed twin (the
// schedule's pair under one sealer, both switched to full-dataset
// contexts), 32 search threads' first hashes on each at once: one build
// per adapter, two in all; the next epoch's lookahead on both: four, the
// bound a sealer pays at any thread count.
func TestSingleFlightTwinAdaptersFourBuildsAtMost(t *testing.T) {
	keyed := []byte(`{"version": 8, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 5152, "chain_id": 7315, "keyed_rounds": 2}`)
	twin := []byte(`{"version": 8, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 4128, "chain_id": 7315}`)
	mk := func(doc []byte) *ihRef {
		alg, err := newIgneumhashRef(7315, doc)
		if err != nil {
			t.Fatalf("document: %v", err)
		}
		a := alg.(*ihRef)
		a.SetFullDataset(true)
		return a
	}
	k, u := mk(keyed), mk(twin)
	var sk [32]byte
	sk[0] = 3
	const threads = 32
	var wg sync.WaitGroup
	for i := 0; i < threads; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if i%4 == 0 {
				k.Anchor(1)
			}
			if mix, _, _ := k.ComputeKeyed(common.Hash{2}, 1, uint64(i), &sk, nil); mix == (common.Hash{}) {
				t.Errorf("keyed thread %d refused", i)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			if i%4 == 0 {
				u.Anchor(1)
			}
			if mix, _ := u.Compute(common.Hash{2}, 1, uint64(i)); mix == (common.Hash{}) {
				t.Errorf("twin thread %d refused", i)
			}
		}(i)
	}
	wg.Wait()
	if n := k.builds.Load() + u.builds.Load(); n != 2 {
		t.Fatalf("%d builds across the keyed adapter and its twin at the first search, want 2", n)
	}
	boundary := epochBoundary(t, k)
	for i := 0; i < 8; i++ {
		k.Anchor(boundary - 1)
		u.Anchor(boundary - 1)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && !(k.held(k.Epoch(boundary)) && u.held(u.Epoch(boundary))) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := k.builds.Load() + u.builds.Load(); n != 4 {
		t.Fatalf("%d builds after both lookaheads, want 4 (one current and one next per adapter)", n)
	}
}

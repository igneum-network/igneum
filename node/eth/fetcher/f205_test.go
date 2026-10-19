package fetcher

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestProvokedParentCostsThePusherNeverAThirdPeer (F-205, 16 October 2026,
// the security review's read of F-203): the cost of a provoked parent that never
// arrives falls on the PUSHER alone. A peer that pushes a bogus head + 1
// block without ever announcing has no requester of its own, so the parent
// is asked of an honest peer that announced; that honest peer is neither
// charged nor dropped when the parent never arrives (under F-203's first
// form it was named in the pusher's place and dropped: an eviction lever,
// one honest peer every fetch timeout for an unsealed push). A pusher
// whose request functions ride its push is its own requester and IS
// dropped when its parent never arrives, the honest peer untouched.
func TestProvokedParentCostsThePusherNeverAThirdPeer(t *testing.T) {
	hashes, blocks := makeChain(2, 0, genesis)
	_, blocksBogus := makeChain(2, 9, genesis)
	var bogusTop *types.Block
	for _, b := range blocksBogus {
		if b.NumberU64() == 2 {
			bogusTop = b
		}
	}
	noBlocks := map[common.Hash]*types.Block{}

	// Case 1: the unannouncing pusher.
	tester := newTester(false)
	goodHeaders := tester.makeHeaderFetcher("good", blocks, 0)
	goodBodies := tester.makeBodyFetcher("good", blocks, 0)
	imported := make(chan interface{}, 4)
	tester.fetcher.importedHook = func(header *types.Header, block *types.Block) { imported <- block }
	tester.fetcher.Notify("good", hashes[1], 1, time.Now(), goodHeaders, goodBodies)
	verifyImportEvent(t, imported, true)
	verifyChainHeight(t, tester, 1)
	// "mute" never announced: its push has no request functions.
	tester.fetcher.Enqueue("mute", bogusTop)
	time.Sleep(50 * time.Millisecond)
	// The honest peer's genuine next block imports at once.
	tester.fetcher.Enqueue("good", blocks[hashes[0]])
	verifyImportEvent(t, imported, true)
	verifyChainHeight(t, tester, 2)
	time.Sleep(fetchTimeout + 500*time.Millisecond)
	tester.fetcher.Notify("good", hashes[0], 2, time.Now(), goodHeaders, goodBodies) // the sweep's event
	time.Sleep(200 * time.Millisecond)
	tester.lock.RLock()
	goodDropped, muteDropped := tester.drops["good"], tester.drops["mute"]
	tester.lock.RUnlock()
	if goodDropped {
		t.Fatalf("the honest peer asked for the parent in the pusher's place was dropped")
	}
	if muteDropped {
		t.Fatalf("the unannouncing pusher was dropped although its push went through a third peer's request")
	}
	tester.fetcher.Stop()

	// Case 2: the pusher with its request functions on the push.
	tester = newTester(false)
	goodHeaders = tester.makeHeaderFetcher("good", blocks, 0)
	goodBodies = tester.makeBodyFetcher("good", blocks, 0)
	badHeaders := tester.makeHeaderFetcher("bad", noBlocks, 0)
	badBodies := tester.makeBodyFetcher("bad", noBlocks, 0)
	imported = make(chan interface{}, 4)
	tester.fetcher.importedHook = func(header *types.Header, block *types.Block) { imported <- block }
	tester.fetcher.Notify("good", hashes[1], 1, time.Now(), goodHeaders, goodBodies)
	verifyImportEvent(t, imported, true)
	verifyChainHeight(t, tester, 1)
	tester.fetcher.EnqueueWith("bad", bogusTop, badHeaders, badBodies)
	time.Sleep(50 * time.Millisecond)
	tester.fetcher.Enqueue("good", blocks[hashes[0]])
	verifyImportEvent(t, imported, true)
	verifyChainHeight(t, tester, 2)
	time.Sleep(fetchTimeout + 500*time.Millisecond)
	tester.fetcher.Notify("good", hashes[0], 2, time.Now(), goodHeaders, goodBodies)
	time.Sleep(200 * time.Millisecond)
	tester.lock.RLock()
	goodDropped, badDropped := tester.drops["good"], tester.drops["bad"]
	tester.lock.RUnlock()
	if goodDropped {
		t.Fatalf("the honest peer was dropped beside a pusher that was its own requester")
	}
	if !badDropped {
		t.Fatalf("the pusher whose own request for the parent timed out was not dropped")
	}
	tester.fetcher.Stop()
}

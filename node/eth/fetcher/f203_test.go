package fetcher

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestProvokedParentNeverServedDropsThePeer (F-203, 16 October 2026,
// the security review's read of F-199): a peer pushing a block at the head's height
// plus one whose parent this node lacks provokes a request for that parent,
// counted against the peer's announcement allowance; the pushed block waits
// ASIDE, so another peer's genuine next block imports at once (under F-199's
// first form the held block stalled every import behind it for the fetch
// timeout); and when the provoked fetch times out because the peer never
// serves the parent, the waiting block is dropped and the peer with it.
func TestProvokedParentNeverServedDropsThePeer(t *testing.T) {
	hashes, blocks := makeChain(2, 0, genesis)
	// A branch the bad peer claims but never serves: B1 and B2 off genesis
	// under another seed; the bad peer's fetchers know nothing of them.
	hashesB, blocksB := makeChain(2, 9, genesis)
	tester := newTester(false)
	defer tester.fetcher.Stop()
	goodHeaders := tester.makeHeaderFetcher("good", blocks, 0)
	goodBodies := tester.makeBodyFetcher("good", blocks, 0)
	badHeaders := tester.makeHeaderFetcher("bad", map[common.Hash]*types.Block{}, 0)
	badBodies := tester.makeBodyFetcher("bad", map[common.Hash]*types.Block{}, 0)
	imported := make(chan interface{}, 4)
	tester.fetcher.importedHook = func(header *types.Header, block *types.Block) { imported <- block }

	// Both peers announce so their requesters are known.
	tester.fetcher.Notify("good", hashes[1], 1, time.Now(), goodHeaders, goodBodies)
	verifyImportEvent(t, imported, true)
	verifyChainHeight(t, tester, 1)
	tester.fetcher.Notify("bad", hashesB[0], 2, time.Now(), badHeaders, badBodies)
	time.Sleep(50 * time.Millisecond)

	// The bad peer pushes B2 (head + 1) whose parent B1 it never serves.
	tester.fetcher.Enqueue("bad", blocksB[hashesB[0]])
	time.Sleep(50 * time.Millisecond)
	// The good peer's genuine block 2 imports at once: the drain is not held.
	start := time.Now()
	tester.fetcher.Enqueue("good", blocks[hashes[0]])
	verifyImportEvent(t, imported, true)
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("the genuine next block imported after %v behind the waiting one; want at once", d)
	}
	verifyChainHeight(t, tester, 2)
	// The provoked fetch times out; the fetcher sweeps its expired fetches
	// at its next event (a live node has one every second; here a known
	// block's announcement serves), and the bad peer is dropped with its
	// block forgotten.
	time.Sleep(fetchTimeout + 500*time.Millisecond)
	tester.fetcher.Notify("good", hashes[0], 2, time.Now(), goodHeaders, goodBodies)
	time.Sleep(200 * time.Millisecond)
	tester.lock.RLock()
	dropped := tester.drops["bad"]
	tester.lock.RUnlock()
	if !dropped {
		t.Fatalf("the peer whose pushed block's parent never arrived was not dropped")
	}
	// No block of the bad branch landed.
	if tester.getBlock(hashesB[0]) != nil || tester.getBlock(hashesB[1]) != nil {
		t.Fatalf("a block of the unserved branch was imported")
	}
}

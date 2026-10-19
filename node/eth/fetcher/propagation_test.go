package fetcher

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
)

// TestOneBlockGapParentRequested (F-182): a block delivered
// whole whose parent this node does not hold, one above a one-block gap,
// has its parent requested at once from the delivering peer through the
// fetcher (the parent announced as if the peer had announced it), so the
// gap closes without the downloader; both blocks import in order.
func TestOneBlockGapParentRequested(t *testing.T) {
	hashes, blocks := makeChain(3, 0, genesis)
	tester := newTester(false)
	defer tester.fetcher.Stop()
	headerFetcher := tester.makeHeaderFetcher("valid", blocks, -gatherSlack)
	bodyFetcher := tester.makeBodyFetcher("valid", blocks, 0)
	imported := make(chan interface{}, 4)
	tester.fetcher.importedHook = func(header *types.Header, block *types.Block) { imported <- block }
	// The peer's request functions are known from an announcement of
	// block 1 (hashes are newest first: hashes[2] is block 1).
	tester.fetcher.Notify("valid", hashes[2], 1, time.Now(), headerFetcher, bodyFetcher)
	verifyImportEvent(t, imported, true)
	verifyChainHeight(t, tester, 1)
	// Block 3 delivered whole with block 2 unknown: a one-block gap.
	tester.fetcher.Enqueue("valid", blocks[hashes[0]])
	// Block 2 is requested and imported, then block 3 from the queue.
	verifyImportEvent(t, imported, true)
	verifyImportEvent(t, imported, true)
	verifyImportDone(t, imported)
	verifyChainHeight(t, tester, 3)
}

// TestAnnounceFetchedAtOnce (F-182): an announced block is
// requested with no arrival allowance (arriveTimeout 0), so its import
// follows the announcement within the request's own round trip.
func TestAnnounceFetchedAtOnce(t *testing.T) {
	if arriveTimeout != 0 || gatherSlack != 0 {
		t.Fatalf("the announce allowance is %v and the slack %v, want none", arriveTimeout, gatherSlack)
	}
	hashes, blocks := makeChain(1, 0, genesis)
	tester := newTester(false)
	defer tester.fetcher.Stop()
	headerFetcher := tester.makeHeaderFetcher("valid", blocks, 0)
	bodyFetcher := tester.makeBodyFetcher("valid", blocks, 0)
	imported := make(chan interface{}, 2)
	tester.fetcher.importedHook = func(header *types.Header, block *types.Block) { imported <- block }
	start := time.Now()
	tester.fetcher.Notify("valid", hashes[0], 1, time.Now(), headerFetcher, bodyFetcher)
	verifyImportEvent(t, imported, true)
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("the announced block imported after %v, want under the old allowance of 500 ms", d)
	}
	verifyChainHeight(t, tester, 1)
}

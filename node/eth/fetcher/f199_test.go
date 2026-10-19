package fetcher

import (
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
)

// TestSiblingHeadParentRequested (F-199, 16 October 2026): a node whose
// head is a SIBLING of the chain's block at that height takes a pushed
// child of the chain's block as the request for that block, imports it as
// a side block, and the child then lands: two of the research chain's eight
// restarted onto a sibling 7274 and dropped every pushed 7275 as "Unknown
// parent" for two and a half minutes, the parent request of F-182 knowing
// the one-block gap by height only. The tester here keeps side blocks as
// the chain does (a block under the head's height is stored, the head
// unmoved); the chain's child at head + 1 then moves the head.
func TestSiblingHeadParentRequested(t *testing.T) {
	// Our branch: genesis -> A1 (the head). The chain's: genesis -> B1 -> B2.
	hashesA, blocksA := makeChain(1, 1, genesis)
	hashesB, blocksB := makeChain(2, 2, genesis)
	tester := newTester(false)
	tester.fetcher.Stop()
	sideInsert := func(blocks types.Blocks) (int, error) {
		tester.lock.Lock()
		defer tester.lock.Unlock()
		for i, block := range blocks {
			if _, ok := tester.blocks[block.ParentHash()]; !ok {
				return i, errors.New("unknown parent")
			}
			tester.blocks[block.Hash()] = block
			tester.headers[block.Hash()] = block.Header()
			if head := tester.blocks[tester.hashes[len(tester.hashes)-1]]; block.NumberU64() > head.NumberU64() {
				tester.hashes = append(tester.hashes, block.Hash())
			}
		}
		return 0, nil
	}
	tester.fetcher = NewBlockFetcher(false, tester.getHeader, tester.getBlock, tester.verifyHeader, tester.broadcastBlock, tester.chainHeight, tester.insertHeaders, sideInsert, tester.dropPeer)
	tester.fetcher.Start()
	defer tester.fetcher.Stop()

	// Our head A1, held directly.
	tester.lock.Lock()
	a1 := blocksA[hashesA[0]]
	tester.blocks[a1.Hash()] = a1
	tester.headers[a1.Hash()] = a1.Header()
	tester.hashes = append(tester.hashes, a1.Hash())
	tester.lock.Unlock()
	verifyChainHeight(t, tester, 1)

	headerFetcher := tester.makeHeaderFetcher("valid", blocksB, 0)
	bodyFetcher := tester.makeBodyFetcher("valid", blocksB, 0)
	imported := make(chan interface{}, 4)
	tester.fetcher.importedHook = func(header *types.Header, block *types.Block) { imported <- block }
	// makeChain lists its hashes from the top: hashesB[0] is B2, hashesB[1]
	// B1. The peer announces B2 so its requester is known, then pushes B2.
	tester.fetcher.Notify("valid", hashesB[0], 2, time.Now(), headerFetcher, bodyFetcher)
	time.Sleep(50 * time.Millisecond)
	tester.fetcher.Enqueue("valid", blocksB[hashesB[0]])
	// B1 (the sibling of A1) is requested and imported as a side block,
	// then B2 lands on it and the head moves to 2.
	verifyImportEvent(t, imported, true)
	verifyImportEvent(t, imported, true)
	verifyImportDone(t, imported)
	verifyChainHeight(t, tester, 2)
	if tester.getBlock(hashesB[0]) == nil || tester.getBlock(hashesB[1]) == nil {
		t.Fatalf("the sibling B1 or its child B2 not imported")
	}
}

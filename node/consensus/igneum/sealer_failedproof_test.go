package igneum

import (
	"errors"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
)

// TestFailedProofResumesTheSearch (the security review's F-76 on the drain): after
// the first finder's drain stopped the template's other threads, a proof
// FAILURE must not abandon the template until the next recommit: the
// claim is released, Seal reopens the search on the same template, and
// the second find is proven and delivered. The prover's failure is the
// attach hook refusing the first find and passing the second.
func TestFailedProofResumesTheSearch(t *testing.T) {
	e := newTestEngine(t)
	e.SetThreads(4)
	e.sealStaggerMs = 0 // no stagger on the resumed search: the test reads the resumption, not the stagger
	var attempts atomic.Int32
	attachHookForTest = func(h *types.Header) error {
		if attempts.Add(1) == 1 {
			return errors.New("test: the prover refused the first find")
		}
		return nil
	}
	t.Cleanup(func() { attachHookForTest = nil })
	header := &types.Header{Number: big.NewInt(1), Difficulty: new(big.Int).Set(testDifficulty), Time: 1000, GasLimit: 1}
	block := types.NewBlockWithHeader(header)
	results := make(chan *types.Block, 4)
	stop := make(chan struct{})
	if err := e.Seal(nil, block, results, stop); err != nil {
		t.Fatal(err)
	}
	select {
	case sealed := <-results:
		if sealed == nil || sealed.Hash() == block.Hash() {
			t.Fatal("the delivered block carries no seal")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the template was abandoned after the failed proof: no second find was proven")
	}
	if n := attempts.Load(); n < 2 {
		t.Fatalf("the prover was asked %d time(s); the second find never reached it", n)
	}
	close(stop)
}

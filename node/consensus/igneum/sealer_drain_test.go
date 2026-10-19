package igneum

import (
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/core/types"
)

// countingAlgo counts every attempt the search makes over the null
// algorithm (which finds at once).
type countingAlgo struct {
	pow.Algorithm
	attempts atomic.Int64
}

func (c *countingAlgo) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	c.attempts.Add(1)
	return c.Algorithm.Compute(headerHash, height, nonce)
}

// TestSealDrainsSearchBeforeProof (the prover-stall finding of 21:3x UK,
// 12 October 2026, on the condition-4 box chain T: the first finder's
// proof ran beside 31 threads still hashing the same template and took
// 3.5 and 5.0 s instead of 0.3): the first finder stops the template's
// other search threads before its proof. With eight threads over an
// algorithm whose every nonce is a seal, the attempts made while the
// proof hook runs are at most the thread count (each thread finishes the
// attempt it is in), where an undrained search would make thousands.
func TestSealDrainsSearchBeforeProof(t *testing.T) {
	base, err := pow.NewDev(null.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	algo := &countingAlgo{Algorithm: base}
	e, err := New(Config{Algorithm: algo, Parts: testParts()})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	const threads = 8
	e.SetThreads(threads)
	var during atomic.Int64
	proving := make(chan struct{}, 1)
	e.proofHook = func() {
		// The drain is closed before the hook; the threads in flight
		// finish their attempt and stop. Measure the attempts made from
		// here to the end of the "proof".
		time.Sleep(50 * time.Millisecond)
		before := algo.attempts.Load()
		time.Sleep(300 * time.Millisecond)
		during.Store(algo.attempts.Load() - before)
		proving <- struct{}{}
	}
	header := &types.Header{Number: big.NewInt(1), Difficulty: new(big.Int).Set(testDifficulty), Time: 1000, GasLimit: 1}
	block := types.NewBlockWithHeader(header)
	results := make(chan *types.Block, threads)
	stop := make(chan struct{})
	if err := e.Seal(nil, block, results, stop); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proving:
	case <-time.After(3 * time.Second):
		t.Fatal("no finder reached the proof")
	}
	if d := during.Load(); d > threads {
		t.Fatalf("%d attempts ran during the proof; the search was not drained (at most %d expected)", d, threads)
	}
	select {
	case sealed := <-results:
		if sealed == nil || sealed.Hash() == block.Hash() {
			t.Fatal("the delivered block carries no seal")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the drained search delivered no seal")
	}
	close(stop)
}

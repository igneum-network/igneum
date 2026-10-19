package igneum

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
)

// TestProvenSealSurvivesTheStop (the node team's read of 21:3x UK, 11 October
// 2026, on the pool cell's members): the worker re-cuts the template at
// every recommit with new transactions and stops the running Seal; a
// finder that is inside its CPU proof at the stop finishes the proof and
// its PROVEN seal reaches the results channel, never discarded on the
// closed abort. The proof's 4 to 10 s is a 200 ms hook; the stop fires 50
// ms after the search starts (the null algorithm finds at once).
func TestProvenSealSurvivesTheStop(t *testing.T) {
	e := newTestEngine(t)
	e.SetThreads(1)
	proving := make(chan struct{}, 1)
	e.proofHook = func() {
		proving <- struct{}{}
		time.Sleep(200 * time.Millisecond)
	}
	header := &types.Header{Number: big.NewInt(1), Difficulty: new(big.Int).Set(testDifficulty), Time: 1000, GasLimit: 1}
	block := types.NewBlockWithHeader(header)
	results := make(chan *types.Block, 1)
	stop := make(chan struct{})
	if err := e.Seal(nil, block, results, stop); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proving:
	case <-time.After(2 * time.Second):
		t.Fatal("the search found no nonce")
	}
	// The recommit's stop lands while the proof runs.
	time.Sleep(50 * time.Millisecond)
	close(stop)
	select {
	case sealed := <-results:
		if sealed == nil || sealed.Hash() == block.Hash() {
			t.Fatal("the delivered block carries no seal")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the proven seal was discarded at the stop")
	}
}

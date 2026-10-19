//go:build controls

package controls

import (
	"os"
	"runtime"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestParallelHashLoopResidentSize (F-219, 16 October 2026, the research
// chain's second growth under 0.10.37): a node verifies a block's share list
// in parallel, so the reference hashes from many OS threads at once, and the
// raster form's buffers were per OS thread (a thread's first raster hash
// allocated about 300 MB, never freed while the thread lived): 32 goroutines
// hashing 150 raster hashes each read +10,297 / +11,622 / +12,365 MB over
// three rounds on the 0.10.37 tree, not released at Close. The buffers now
// come from a bounded pool per context (the width here 8), so the loop's
// resident size must be bounded by the width's slots and FLAT past the first
// round: the figure of record on the row, and the verdict.
func TestParallelHashLoopResidentSize(t *testing.T) {
	if _, err := os.ReadFile("/proc/self/status"); err != nil {
		t.Skip("no /proc/self/status (the read is Linux's)")
	}
	doc := `{"version":18,"rounds":1024,"epoch_blocks":4096,"period_blocks":300,"chain_id":7358,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000"}`
	alg, err := newIgneumhashRef(7358, []byte(doc))
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	a := alg.(*ihRef)
	a.SetFullDataset(true)
	a.SetSingleDataset(true)
	const width = 8
	SetParallelWidth(width)
	defer SetParallelWidth(0)
	header := common.HexToHash("0x11")
	if _, out := a.Compute(header, 8400, 1); out == (common.Hash{}) {
		t.Fatal("the first hash computed nothing")
	}
	built := rssMB(t)
	t.Logf("raster context built, rss %d MiB, GOMAXPROCS %d, the pool width %d", built, runtime.GOMAXPROCS(0), width)
	const workers, each = 32, 150
	var after [3]int
	for round := 1; round <= 3; round++ {
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < each; i++ {
					h := uint64(8400 + ((w*each + i) % 400))
					if _, out := a.Compute(header, h, uint64(round*100000+w*each+i)); out == (common.Hash{}) {
						t.Errorf("hash computed nothing")
						return
					}
				}
			}(w)
		}
		wg.Wait()
		after[round-1] = rssMB(t)
		t.Logf("after round %d (%d hashes on %d goroutines): rss %d MiB (+%d over the built figure)", round, workers*each, workers, after[round-1], after[round-1]-built)
	}
	a.Close()
	t.Logf("closed, rss %d MiB", rssMB(t))
	// the bound: 8 slots of about 8 MB (the tiles 4 MiB at 1,024 triangles, the digest buffer, the keys) plus the Go side's
	// own; the old form read +10 GB here
	if grew := after[0] - built; grew > 512 {
		t.Errorf("the first round grew the resident size %d MiB: the pool is not bounding the raster's buffers (the width %d)", grew, width)
	}
	if grew := after[2] - after[0]; grew > 128 {
		t.Errorf("rounds two and three grew the resident size %d MiB past the first: a per-thread or per-call holder remains", grew)
	}
}

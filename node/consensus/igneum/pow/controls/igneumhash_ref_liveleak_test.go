//go:build controls

package controls

import (
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestLiveLaneHashLoopDoesNotLeak (16 October 2026, the research chain's
// second growth under 0.10.37, 5 to 15 GB an hour on every node with no
// context build logged): the chain's three live lane documents as registered
// (coordinates 22, 23 and 24: the version 15 bandwidth form, the version 21
// Anvil-R form, the version 18 raster form; none keyed), each built once on
// its own full context and hashed thousands of times as a block's share list
// is verified; the process's resident size must not climb with the count.
// The 4 GiB datasets make this a build box's test: LIVE_LEAK=1 runs it.
func TestLiveLaneHashLoopDoesNotLeak(t *testing.T) {
	if os.Getenv("LIVE_LEAK") != "1" {
		t.Skip("LIVE_LEAK=1 runs the 4 GiB forms")
	}
	docs := map[string]string{
		"22 bandwidth v15": `{"version":15,"dataset_mib":4096,"cache_mib":64,"parents":256,"item_bytes":256,"epoch_blocks":4096,"period_blocks":300,"rounds":256,"program_ops":64,"chain_id":7358,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000"}`,
		"23 anvilr v21":    `{"version":21,"rounds":128,"program_ops":16,"sequence":8,"period_blocks":64,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000","lanes":16,"registers":32,"scratch_kib":1,"dataset_mib":1024,"cache_mib":64,"parents":256,"item_bytes":256,"fold_every":2,"epoch_blocks":4096,"chain_id":7358,"fp_mode":6,"pair_log2":1,"lean":1,"prefetch":16}`,
		"24 raster v18":    `{"version":18,"rounds":1024,"epoch_blocks":4096,"period_blocks":300,"chain_id":7358,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000"}`,
	}
	rounds := 3000
	for _, name := range []string{"24 raster v18", "23 anvilr v21", "22 bandwidth v15"} {
		doc := docs[name]
		t.Run(name, func(t *testing.T) {
			alg, err := newIgneumhashRef(7358, []byte(doc))
			if err != nil {
				t.Fatalf("document: %v", err)
			}
			a := alg.(*ihRef)
			a.SetFullDataset(true)
			a.SetSingleDataset(true)
			header := common.HexToHash("0x11")
			if _, out := a.Compute(header, 8400, 1); out == (common.Hash{}) {
				t.Fatal("the first hash computed nothing")
			}
			built := rssMB(t)
			t.Logf("%s: context built, rss %d MiB", name, built)
			for i := 0; i < rounds; i++ {
				// Heights across two periods so a period's draw happens inside the loop.
				h := uint64(8400 + (i % 400))
				if _, out := a.Compute(header, h, uint64(i)+2); out == (common.Hash{}) {
					t.Fatalf("hash %d computed nothing", i)
				}
				if i == rounds/3 || i == 2*rounds/3 {
					t.Logf("%s: after %d hashes rss %d MiB (+%d)", name, i, rssMB(t), rssMB(t)-built)
				}
			}
			after := rssMB(t)
			t.Logf("%s: after %d hashes rss %d MiB (+%d over the built figure)", name, rounds, after, after-built)
			if after-built > 512 {
				t.Errorf("%s: the resident size climbed %d MiB over %d hashes: a per-call leak", name, after-built, rounds)
			}
			a.Close()
			t.Logf("%s: closed, rss %d MiB", name, rssMB(t))
		})
	}
}

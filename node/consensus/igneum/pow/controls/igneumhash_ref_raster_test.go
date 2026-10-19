//go:build controls

package controls

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestRasterPoolAlternatingDocuments (the security review's read of F-219's cure, 16
// October 2026): a slot's buffers are sized by the attempt that holds it and
// never assumed from an earlier one, so documents of different rounds and
// target sides served in turn by bounded pools must read the oracle's bytes
// on every attempt. A pool is the context's (a shared context leases from
// its donor's, and a shared context is refused over a donor of another
// dataset id, which carries the rounds), so two documents of different
// rounds never meet in one pool by construction; the proof here is the
// closest live form: 32 goroutines alternating the live raster document
// (1,024 triangles, the 8,192 side) and the 18b form (256 triangles, the
// 16,384 side, the 1 GiB target) attempt by attempt under the width 4, every
// attempt checked against the oracle's figure for its (document, height,
// nonce), and a slot of each pool re-sized across the alternation.
func TestRasterPoolAlternatingDocuments(t *testing.T) {
	docs := []string{
		`{"version":18,"rounds":1024,"epoch_blocks":4096,"period_blocks":300,"chain_id":7358,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000"}`,
		`{"version":18,"rounds":256,"target_log2":14,"epoch_blocks":4096,"period_blocks":300,"chain_id":7358,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000"}`,
	}
	refs := make([]*ihRef, len(docs))
	for i, doc := range docs {
		alg, err := newIgneumhashRef(7358, []byte(doc))
		if err != nil {
			t.Fatalf("document %d: %v", i, err)
		}
		refs[i] = alg.(*ihRef)
		refs[i].SetFullDataset(true)
		refs[i].SetSingleDataset(true)
		defer refs[i].Close()
	}
	header := common.HexToHash("0x11")
	const pairs = 96
	type out struct{ mix, result common.Hash }
	heightOf := func(i int) uint64 { return 8400 + uint64(i*7)%900 }
	nonceOf := func(i int) uint64 { return uint64(i)*0x9E3779B97F4A7C15 + 3 }
	// the oracle's figures first, one goroutine, the oracle paths on
	oracle := make([][]out, len(docs))
	setRasterOracle(true)
	for d := range docs {
		oracle[d] = make([]out, pairs)
		for i := 0; i < pairs; i++ {
			mix, result := refs[d].Compute(header, heightOf(i), nonceOf(i))
			if result == (common.Hash{}) {
				setRasterOracle(false)
				t.Fatalf("oracle: document %d pair %d computed nothing", d, i)
			}
			oracle[d][i] = out{mix, result}
		}
	}
	setRasterOracle(false)
	SetParallelWidth(4)
	defer SetParallelWidth(0)
	const workers, each = 32, 48
	var bad, done atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for k := 0; k < each; k++ {
				d := (w + k) % len(docs) // the document alternates attempt by attempt on every goroutine
				i := (w*each + k) % pairs
				mix, result := refs[d].Compute(header, heightOf(i), nonceOf(i))
				done.Add(1)
				if mix != oracle[d][i].mix || result != oracle[d][i].result {
					if bad.Add(1) <= 3 {
						t.Errorf("document %d height %d nonce %d: the pool's %x / %x, the oracle's %x / %x", d, heightOf(i), nonceOf(i), mix[:4], result[:4], oracle[d][i].mix[:4], oracle[d][i].result[:4])
					}
				}
			}
		}(w)
	}
	wg.Wait()
	if bad.Load() > 0 {
		t.Fatalf("%d of %d alternating attempts differ from the oracle", bad.Load(), done.Load())
	}
	t.Logf("%d attempts on %d goroutines alternating two documents (1,024 triangles at the 8,192 side; 256 at the 16,384 side) under the width 4: every attempt the oracle's bytes", done.Load(), workers)
}

// TestRasterTilesMatchTheOracle (F-219, 16 October 2026): the raster form's
// unkeyed attempt draws on 18d's tiles over a zero base (the slot of the
// bounded pool) in place of the normative calloc'd target, and its keyed
// attempt on the tiles over the dataset in place of the sparse overlay; both
// oracle paths are kept behind ignpow_set_raster_oracle and must give the
// same mix and result on every (height, nonce). The research chain's live
// raster document (coordinate 24, now 27) and the 18c keyed form (the 256
// MiB target of target_log2 13) over heights spanning three key periods.
func TestRasterTilesMatchTheOracle(t *testing.T) {
	docs := []struct{ name, doc string }{
		{"24 raster v18 (unkeyed, the live document)", `{"version":18,"rounds":1024,"epoch_blocks":4096,"period_blocks":300,"chain_id":7358,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000"}`},
		{"v18c keyed target t256", `{"version":18,"rounds":256,"epoch_blocks":4096,"period_blocks":300,"chain_id":7358,"dataset_mib":256,"cache_mib":16,"parents":256,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000"}`},
		{"v18c keyed target t256 draw 1", `{"version":18,"rounds":256,"epoch_blocks":4096,"period_blocks":300,"chain_id":7358,"dataset_mib":256,"cache_mib":16,"parents":256,"draw":1,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000"}`},
	}
	for _, d := range docs {
		d := d
		t.Run(d.name, func(t *testing.T) {
			alg, err := newIgneumhashRef(7358, []byte(d.doc))
			if err != nil {
				t.Fatalf("document: %v", err)
			}
			a := alg.(*ihRef)
			a.SetFullDataset(true)
			a.SetSingleDataset(true)
			defer a.Close()
			type key struct {
				height, nonce uint64
			}
			type out struct{ mix, result common.Hash }
			const pairs = 400
			keys := make([]key, 0, pairs)
			for i := 0; i < pairs; i++ {
				// three key periods (300 blocks) within the epoch, the
				// nonce a spread of bits
				keys = append(keys, key{height: 8400 + uint64(i*2)%900, nonce: uint64(i)*0x9E3779B97F4A7C15 + 7})
			}
			header := common.HexToHash("0x11")
			run := func(oracle bool) []out {
				setRasterOracle(oracle)
				defer setRasterOracle(false)
				res := make([]out, 0, len(keys))
				for _, k := range keys {
					mix, result := a.Compute(header, k.height, k.nonce)
					if result == (common.Hash{}) {
						t.Fatalf("oracle=%v: height %d nonce %d computed nothing", oracle, k.height, k.nonce)
					}
					res = append(res, out{mix, result})
				}
				return res
			}
			tiles := run(false)
			oracle := run(true)
			bad := 0
			for i := range keys {
				if tiles[i] != oracle[i] {
					bad++
					if bad <= 3 {
						t.Errorf("height %d nonce %d: the tiles %x / %x, the oracle %x / %x", keys[i].height, keys[i].nonce, tiles[i].mix[:4], tiles[i].result[:4], oracle[i].mix[:4], oracle[i].result[:4])
					}
				}
			}
			if bad > 0 {
				t.Fatalf("%d of %d (height, nonce) pairs differ between the tiles and the oracle", bad, len(keys))
			}
			t.Logf("%s: %d pairs over three periods, the tiles and the oracle the same bytes", d.name, len(keys))
		})
	}
}

// TestRasterPoolSharedContextLeasesFromTheDonor (F-219): a keyed raster row
// over its unkeyed twin's dataset hashes through a per-call shared context;
// the shared context must lease from the donor's pool, not build one of its
// own (which would be allocated and freed on every call). The row's hash is
// read twice with the pool width at one so the second call must reuse the
// one slot (a second slot would wait for ever on a leak of the lease).
func TestRasterPoolSharedContextLeasesFromTheDonor(t *testing.T) {
	keyed := []byte(`{"version":18,"rounds":256,"epoch_blocks":4096,"period_blocks":300,"chain_id":7358,"dataset_mib":256,"cache_mib":16,"parents":256,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000"}`)
	alg, err := newIgneumhashRef(7358, keyed)
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	a := alg.(*ihRef)
	a.SetFullDataset(true)
	a.SetSingleDataset(true)
	defer a.Close()
	SetParallelWidth(1)
	defer SetParallelWidth(0)
	header := common.HexToHash("0x11")
	var first common.Hash
	for i := 0; i < 8; i++ {
		_, result := a.Compute(header, 8400, 1)
		if result == (common.Hash{}) {
			t.Fatalf("call %d computed nothing", i)
		}
		if i == 0 {
			first = result
		} else if result != first {
			t.Fatalf("call %d: %x, the first %x", i, result[:4], first[:4])
		}
	}
}

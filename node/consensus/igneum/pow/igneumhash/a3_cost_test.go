// Copyright 2026 The Igneum Authors
// This file is part of the Igneum node.
//
// The Igneum node is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The Igneum node is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the Igneum node. If not, see <http://www.gnu.org/licenses/>.

package igneumhash

import (
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// TestA3ReconstructionCost is the measurement the research brief's known
// risk to light verification asks for (BRIEF-R1 section 7; the A3 text,
// SPEC-TEXT-v0.2-A2A3.md section 2): the light verifier reconstructs R x C
// items per proof, one per chunk whatever the chunk's size, so a request
// shape costs its item count. It measures, at the proposed version 1 point
// (dataset 2048 MiB, cache 32 MiB, parents 256), the cost of one item
// reconstruction, the cost of the whole proof at the v0.1 shape (64 items),
// and the reconstruction load of each shape at R = 64: 1x128 (64 items, the
// v0.1 point), 1x256 and 2x128 (128), 4x64 (256). It runs with
// IGNEUM_A3_COST=1 and prints Markdown rows.
func TestA3ReconstructionCost(t *testing.T) {
	if os.Getenv("IGNEUM_A3_COST") == "" {
		t.Skip("set IGNEUM_A3_COST=1 to measure the A3 reconstruction cost")
	}
	p := DefaultParams()
	p.DatasetBytes = 2048 << 20
	env := NewEnvelope(7310, p)
	es := env.EpochSeed(1)
	cache := BuildCache(es, p.CacheBytes)
	items := NewItemsV1(es, cache, p.Parents)
	n := p.Items()
	rng := rand.New(rand.NewSource(20261010))
	quant := func(d []time.Duration) (p50, p99, max time.Duration) {
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		return d[len(d)/2], d[len(d)*99/100], d[len(d)-1]
	}
	// One item reconstruction (the parent walk over the cache), random items.
	var out [ItemBytes]byte
	for i := 0; i < 2000; i++ {
		items.Item(rng.Uint64()%n, &out) // warm
	}
	one := make([]time.Duration, 20000)
	for i := range one {
		idx := rng.Uint64() % n
		s := time.Now()
		items.Item(idx, &out)
		one[i] = time.Since(s)
	}
	i50, i99, imax := quant(one)
	// A load of k reconstructions per nonce for each shape (the addresses are
	// state-dependent in the text; here random, the same cost).
	type shape struct {
		name  string
		items int
	}
	shapes := []shape{{"1x128 (the v0.1 point)", 64}, {"1x256", 128}, {"2x128", 128}, {"4x64", 256}}
	fmt.Println("| shape at R = 64, PAYLOAD 256 (128 for the v0.1 point) | item reconstructions per proof | reconstruction load per nonce p50 | p99 | max |")
	fmt.Println("|---|---|---|---|---|")
	for _, sh := range shapes {
		lat := make([]time.Duration, 500)
		for i := range lat {
			s := time.Now()
			for k := 0; k < sh.items; k++ {
				items.Item(rng.Uint64()%n, &out)
			}
			lat[i] = time.Since(s)
		}
		p50, p99, max := quant(lat)
		fmt.Printf("| %s | %d | %.3f ms | %.3f ms | %.3f ms |\n", sh.name, sh.items, float64(p50)/1e6, float64(p99)/1e6, float64(max)/1e6)
	}
	// The whole proof at the v0.1 shape through the adapter (64 items plus
	// the loop and the final hash), invalid mix at difficulty 1.
	a := New(7310, p, defaultWork())
	h := common.HexToHash("0xa3")
	for i := 0; i < 200; i++ {
		a.Verify(h, 1, uint64(i), common.Hash{byte(i)}, big.NewInt(1), false)
	}
	full := make([]time.Duration, 2000)
	for i := range full {
		s := time.Now()
		a.Verify(h, 1, uint64(1000+i), common.Hash{byte(i), 1}, big.NewInt(1), false)
		full[i] = time.Since(s)
	}
	f50, f99, fmax := quant(full)
	fmt.Printf("\none item reconstruction: p50 %.2f us, p99 %.2f us, max %.2f us; the whole v0.1-shape proof (64 items, loop, final): p50 %.3f ms, p99 %.3f ms, max %.3f ms; loop and final outside the items: %.3f ms\n",
		float64(i50)/1e3, float64(i99)/1e3, float64(imax)/1e3, float64(f50)/1e6, float64(f99)/1e6, float64(fmax)/1e6, float64(f50-64*i50)/1e6)
	fmt.Printf("measured %s on one thread\n", time.Now().UTC().Format(time.RFC3339))
}

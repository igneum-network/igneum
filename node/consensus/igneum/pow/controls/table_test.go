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

//go:build controls && cgo

package controls

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/igneumhash"
)

// TestVerifierTable is the four-candidate measurement of VERIFIER-R0A.md
// section 6: every candidate through the same pow.Algorithm path on the same
// node and CPU, at its comparison points, under the same hostile campaign
// (random invalid proofs: a mix digest that never recomputes). It runs only
// with IGNEUM_VERIFIER_TABLE=1 (minutes) and prints Markdown rows.
func TestVerifierTable(t *testing.T) {
	if os.Getenv("IGNEUM_VERIFIER_TABLE") == "" {
		t.Skip("set IGNEUM_VERIFIER_TABLE=1 to measure the four-candidate table")
	}
	type point struct {
		candidate string
		label     string
		params    []byte
		height    uint64
	}
	points := []point{
		{"igneumhash", "v1 baseline (1 GiB, cache 32 MiB, parents 256)", []byte(`{"version": 1}`), 1},
		{"igneumhash", "v1 proposed point (2 GiB, cache 32 MiB, parents 256)", []byte(`{"version": 1, "dataset_mib": 2048}`), 1},
		{KawpowName, "genesis (epoch 0, cache 16 MiB)", nil, 1},
		{KawpowName, "matched 4.5 GiB point (epoch 448, cache 72 MiB)", nil, 3360000},
		{KawpowName, "live horizon (epoch 598, cache 91 MiB)", nil, 4487776},
		{FishhashName, "fixed (4.5 GiB, cache 72 MiB)", nil, 1},
		{AutolykosName, "genesis (N at height 1)", nil, 1},
		{AutolykosName, "live horizon (height 1,500,000)", nil, 1500000},
	}
	const n, campaign = 2000, 20000
	fmt.Println("| candidate | point | first proof after start | p50 | p95 | p99 | max | allocs/proof | retained after 20,000 invalid | resident caches |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|")
	for _, p := range points {
		a, err := pow.NewDev(p.candidate, p.params)
		if err != nil {
			t.Logf("%s %s: %v (skipped)", p.candidate, p.label, err)
			continue
		}
		header := common.HexToHash("0x6468beee66bb5264f1191b196797b748220bf33cf2072630a7f1c030c520c307")
		difficulty := big.NewInt(1) // the target every result passes: the mix comparison is the whole cost
		randomMix := func() (uint64, common.Hash) {
			var b [40]byte
			rand.Read(b[:])
			return binary.LittleEndian.Uint64(b[:8]), common.BytesToHash(b[8:])
		}
		// first proof after start: context creation (the cache build) plus one proof
		runtime.GC()
		nonce, mix := randomMix()
		start := time.Now()
		_ = a.Verify(header, p.height, nonce, mix, difficulty, false)
		first := time.Since(start)
		// steady state
		lat := make([]time.Duration, n)
		for i := range lat {
			nonce, mix := randomMix()
			s := time.Now()
			if err := a.Verify(header, p.height, nonce, mix, difficulty, false); err == nil {
				t.Fatalf("%s: a random mix digest verified", p.candidate)
			}
			lat[i] = time.Since(s)
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		q := func(f float64) time.Duration { return lat[int(f*float64(n-1))] }
		allocs := testing.AllocsPerRun(50, func() {
			nonce, mix := randomMix()
			_ = a.Verify(header, p.height, nonce, mix, difficulty, false)
		})
		// retained heap after a campaign of invalid proofs
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := 0; i < campaign; i++ {
			nonce, mix := randomMix()
			_ = a.Verify(header, p.height, nonce, mix, difficulty, false)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		retained := int64(after.HeapInuse) - int64(before.HeapInuse)
		resident := "none"
		switch c := a.(type) {
		case *Algorithm:
			resident = fmt.Sprintf("%d x %.1f MiB (C heap)", 1, float64(c.CacheBytes(p.height))/(1<<20))
		case *igneumhash.Algorithm:
			resident = fmt.Sprintf("%.1f MiB (Go heap)", float64(c.CacheResident())/(1<<20))
		}
		fmt.Printf("| %s | %s | %.1f ms | %.3f ms | %.3f ms | %.3f ms | %.3f ms | %.0f | %+d KB | %s |\n",
			p.candidate, p.label, float64(first)/1e6, float64(q(0.5))/1e6, float64(q(0.95))/1e6, float64(q(0.99))/1e6, float64(lat[n-1])/1e6, allocs, retained/1024, resident)
		if c, ok := a.(*Algorithm); ok {
			c.Close()
		}
	}
	fmt.Printf("\nmeasured %s, GOMAXPROCS %d, %s\n", time.Now().UTC().Format(time.RFC3339), runtime.GOMAXPROCS(0), Build())
}

// Copyright 2026 The Igneum Authors
// This file is part of go-ethereum (Igneum fork).
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"flag"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/consensus/igneum/igneumhash"
)

// bench measures the verifier's cost for a cell (the master plan chapter 27,
// the programme's gate G1): light verification per proof (p50, p95, p99,
// worst) over random and hostile inputs, bytes allocated per proof, and the
// materialised dataset's build time and size for the miner-side comparison.
//
//	igneumhash-diff bench CELL [-n 2000] [-dataset]
func bench(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	n := fs.Int("n", 2000, "proofs to verify")
	withDataset := fs.Bool("dataset", false, "also build the materialised dataset and time it")
	pos, flags := splitArgs(args)
	fs.Parse(flags)
	if len(pos) < 1 {
		usage()
	}
	p, ok := igneumhash.Ablation[pos[0]]
	if !ok {
		fail(fmt.Errorf("unknown cell %q", pos[0]))
	}
	light := p.NewLight(0)
	var hh [32]byte
	// a valid proof's mix for the accept path, then the hostile classes
	st, err := p.Compute(light, hh, 1, 1, false)
	if err != nil {
		fail(err)
	}
	type class struct {
		name string
		run  func(i int) error
	}
	one := big.NewInt(1)
	classes := []class{
		{"accept (difficulty 1)", func(i int) error {
			_, err := p.Verify(light, hh, 1, 1, st.Mix, one)
			return err
		}},
		{"reject: wrong mix", func(i int) error {
			bad := st.Mix
			bad[i%32] ^= byte(1 + i%255)
			_, err := p.Verify(light, hh, 1, 1, bad, one)
			return err
		}},
		{"reject: target 1 (difficulty 2^256 - 1)", func(i int) error {
			max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
			_, err := p.Verify(light, hh, 1, 1, st.Mix, max)
			return err
		}},
		{"reject: zero difficulty (no hash)", func(i int) error {
			_, err := p.Verify(light, hh, 1, 1, st.Mix, big.NewInt(0))
			return err
		}},
		{"random nonce, random header", func(i int) error {
			var h [32]byte
			for k := range h {
				h[k] = byte(i*131 + k*17)
			}
			_, err := p.Verify(light, h, uint64(i)%p.EpochLength, uint64(i)*0x9E3779B97F4A7C15, st.Mix, one)
			return err
		}},
	}
	fmt.Printf("cell %s: dataset %d MiB, scratch %d KiB, rounds %d, lanes %d; %d proofs per class; %d cores\n",
		pos[0], p.DatasetBytes>>20, p.ScratchBytes>>10, p.Rounds, p.Lanes, *n, runtime.NumCPU())
	fmt.Println("| class | p50 | p95 | p99 | worst | bytes allocated per proof |")
	fmt.Println("|---|---:|---:|---:|---:|---:|")
	for _, c := range classes {
		durs := make([]time.Duration, *n)
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		for i := 0; i < *n; i++ {
			t0 := time.Now()
			c.run(i)
			durs[i] = time.Since(t0)
		}
		runtime.ReadMemStats(&m1)
		sort.Slice(durs, func(a, b int) bool { return durs[a] < durs[b] })
		q := func(f float64) time.Duration { return durs[int(f*float64(*n-1))] }
		fmt.Printf("| %s | %s | %s | %s | %s | %d |\n", c.name, q(0.5).Round(time.Microsecond), q(0.95).Round(time.Microsecond),
			q(0.99).Round(time.Microsecond), durs[*n-1].Round(time.Microsecond), (m1.TotalAlloc-m0.TotalAlloc)/uint64(*n))
	}
	if *withDataset {
		runtime.GC()
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		t0 := time.Now()
		ds, err := p.Generate(0)
		if err != nil {
			fail(err)
		}
		el := time.Since(t0)
		runtime.ReadMemStats(&m1)
		fmt.Printf("\nmaterialised dataset: %d MiB in %s (%d cores), heap grew %d MiB; id %x\n",
			ds.Bytes()>>20, el.Round(time.Millisecond), runtime.NumCPU(), (m1.HeapInuse-m0.HeapInuse)>>20, ds.ID[:8])
	}
	_ = os.Stdout
}

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

// rxe1-diff is the differential tool of the RXE1 second implementation (IH 05).
//
//	rxe1-diff gen  VARIANT OUT.json [-n 8] [-test]          inputs for a record (the edge nonces, empty and maximum headers)
//	rxe1-diff fill IN.json OUT.json [-full]                  this implementation's results (and the full trace with -full)
//	rxe1-diff check REF.json [-report out.md]                the first diverging intermediate per vector
//	rxe1-diff campaign VARIANT OUT.json|OUT.jsonl [-n 10000] [-seed S]  the trace-digest campaign: n nonces, one digest each (.jsonl: the reference's form)
//	rxe1-diff campaign-check FILE.jsonl VARIANT [-report out.md]      recompute the reference's campaign file line by line
//
// VARIANT is one of the records this tool knows (rxe1-diff list), or a JSON record file.
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/consensus/igneum/rxe1"
)

// The seven screening records of the specification's stage B (2 GiB, R 32,
// K 8, parents 256, the baseline cache 32 MiB) plus the 4 GiB rows and tiny
// test-only records. The dataset-spec digest is zero until IH 01 publishes
// the binding; every file records it.
var known = func() map[string]rxe1.Params {
	m := map[string]rxe1.Params{}
	add := func(p rxe1.Params) { m[p.Variant()] = p }
	for _, schema := range []uint32{rxe1.Schema, rxe1.Schema2} {
		for _, d := range []uint64{2 << 30, 4 << 30} {
			for _, r := range []uint32{32, 64} {
				base := rxe1.Params{DatasetSpecDigest: rxe1.DatasetSpecDigest, DatasetBytes: d, Parents: 256, CacheBytes: 32 << 20, R: r, K: 8, Schema: schema}
				base.Mode, base.B = rxe1.ModeM, 0
				add(base)
				for _, b := range []uint32{1, 2, 4} {
					f := base
					f.Mode, f.B = rxe1.ModeF, b
					add(f)
					e := base
					e.Mode, e.B = rxe1.ModeE, b
					add(e)
				}
			}
		}
	}
	// schema 3: the three first-screen cells (SPEC-TEXT-v0.2-A2A3.md section 4)
	for _, c := range []struct{ route, chunks, payload uint32 }{{rxe1.RouteState, 1, 256}, {rxe1.RouteFixed, 4, 256}, {rxe1.RouteFixed, 1, 256}} {
		add(rxe1.Params{DatasetSpecDigest: rxe1.DatasetSpecDigest, DatasetBytes: 2 << 30, Parents: 256, CacheBytes: 32 << 20, R: 64, K: 8, B: 1, Mode: rxe1.ModeF, Schema: rxe1.Schema3, Group: 16, Route: c.route, Chunks: c.chunks, Payload: c.payload})
		add(rxe1.Params{DatasetSpecDigest: rxe1.DatasetSpecDigest, DatasetBytes: 1 << 20, Parents: 256, CacheBytes: 16 << 10, R: 64, K: 8, B: 1, Mode: rxe1.ModeF, Schema: rxe1.TestSchema3, Group: 16, Route: c.route, Chunks: c.chunks, Payload: c.payload})
	}
	for _, mode := range []struct {
		m rxe1.Mode
		b uint32
	}{{rxe1.ModeM, 0}, {rxe1.ModeF, 1}, {rxe1.ModeF, 4}, {rxe1.ModeE, 2}} {
		for _, schema := range []uint32{rxe1.TestSchema, rxe1.TestSchema2} {
			t := rxe1.Params{DatasetSpecDigest: rxe1.DatasetSpecDigest, DatasetBytes: 1 << 20, Parents: 256, CacheBytes: 16 << 10, R: 32, K: 8, B: mode.b, Mode: mode.m, Schema: schema}
			add(t)
			t.DatasetBytes, t.CacheBytes = 16<<20, 1<<20
			add(t)
		}
	}
	return m
}()

// lookup resolves a variant: the known table first, then the tag itself
// (s2-, s3-, s4- prefixes; m f e p s; b r k g u x y l n d p c), production
// records only (a test record's cache size is below the tag's resolution).
func lookup(name string) (rxe1.Params, bool) {
	if p, ok := known[name]; ok {
		return p, true
	}
	parts := strings.Split(name, "-")
	p := rxe1.Params{DatasetSpecDigest: rxe1.DatasetSpecDigest, Schema: rxe1.Schema}
	switch parts[0] {
	case "s2":
		p.Schema, parts = rxe1.Schema2, parts[1:]
	case "s3":
		p.Schema, parts = rxe1.Schema3, parts[1:]
	case "s4":
		p.Schema, parts = rxe1.Schema4, parts[1:]
	case "s5":
		p.Schema, p.DatasetSpecDigest, p.Parents, p.CacheBytes, parts = rxe1.Schema5, [32]byte{}, 0, 0, parts[1:]
	}
	if len(parts) == 0 || parts[0] == "test" {
		return p, false
	}
	modes := map[string]rxe1.Mode{"m": rxe1.ModeM, "f": rxe1.ModeF, "e": rxe1.ModeE, "p": rxe1.ModeP, "s": rxe1.ModeS}
	mode, ok := modes[parts[0]]
	if !ok {
		return p, false
	}
	p.Mode = mode
	for _, f := range parts[1:] {
		if len(f) < 2 {
			return p, false
		}
		var n uint64
		if _, err := fmt.Sscanf(f[1:], "%d", &n); err != nil {
			return p, false
		}
		switch f[0] {
		case 'b':
			p.B = uint32(n)
		case 'r':
			p.R = uint32(n)
		case 'k':
			p.K = uint32(n)
		case 'g':
			p.Group = uint32(n)
		case 'u':
			p.Route = uint32(n)
		case 'x':
			p.Chunks = uint32(n)
		case 'y':
			p.Payload = uint32(n)
		case 'l':
			p.BlockLen = uint32(n)
		case 'n':
			p.Family = uint32(n)
		case 'd':
			p.DatasetBytes = n << 20
		case 'p':
			p.Parents = uint32(n)
		case 'c':
			p.CacheBytes = n << 20
		default:
			return p, false
		}
	}
	return p, p.Validate() == nil && p.Variant() == name
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "list":
		names := make([]string, 0, len(known))
		for n := range known {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Println(n)
		}
	case "gen":
		gen(os.Args[2:])
	case "fill":
		fill(os.Args[2:])
	case "check":
		check(os.Args[2:])
	case "campaign":
		campaign(os.Args[2:])
	case "campaign-check":
		campaignCheck(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rxe1-diff list | gen VARIANT OUT.json [-n 8] | fill IN.json OUT.json [-full] | check REF.json [-report out.md] | campaign VARIANT OUT.json [-n 10000] [-seed S]")
	os.Exit(2)
}

func split(args []string) (pos, flags []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && a != "-full" && a != "-test" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return
}

func params(name string) rxe1.Params {
	if p, ok := lookup(strings.TrimPrefix(name, "rxe1-")); ok {
		return p
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		fail(fmt.Errorf("unknown variant %q and no such file", name))
	}
	var p rxe1.Params
	if err := json.Unmarshal(raw, &p); err != nil {
		fail(err)
	}
	return p
}

func gen(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	n := fs.Int("n", 8, "random vectors after the edge classes")
	pos, flags := split(args)
	fs.Parse(flags)
	if len(pos) < 2 {
		usage()
	}
	p := params(pos[0])
	vf := &rxe1.VectorFile{Algorithm: rxe1.ID, Spec: "SPEC-TEXT-v0.1 sections 6 to 8", Params: p}
	var es [32]byte
	for i := range es {
		es[i] = byte(0xA5 ^ i)
	}
	chain := hex.EncodeToString([]byte("igneum-devnet-7310"))
	header := hex.EncodeToString([]byte("rxe1 header preimage without the solution"))
	maxHeader := hex.EncodeToString(make([]byte, 4096))
	edges := []struct {
		name   string
		nonce  uint64
		header string
		epoch  uint64
	}{
		{"nonce-0", 0, header, 0}, {"nonce-1", 1, header, 0}, {"nonce-2^32-1", 1<<32 - 1, header, 0}, {"nonce-2^32", 1 << 32, header, 0},
		{"nonce-2^64-1", 1<<64 - 1, header, 0}, {"header-empty", 5, "", 0}, {"header-max", 6, maxHeader, 0},
		{"epoch-1", 7, header, 1}, {"epoch-2^32", 8, header, 1 << 32}, {"epoch-2^64-1", 9, header, 1<<64 - 1},
	}
	target := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"
	for _, e := range edges {
		vf.Vectors = append(vf.Vectors, rxe1.Vector{Name: e.name, ChainID: chain, HeaderBlob: e.header, EpochSeed: hex.EncodeToString(es[:]), EpochNumber: e.epoch, Nonce: e.nonce, Target: target})
	}
	rng := rand.New(rand.NewSource(20261010))
	for i := 0; i < *n; i++ {
		vf.Vectors = append(vf.Vectors, rxe1.Vector{Name: fmt.Sprintf("random-%02d", i), ChainID: chain, HeaderBlob: header, EpochSeed: hex.EncodeToString(es[:]), EpochNumber: uint64(i % 3), Nonce: rng.Uint64(), Target: target})
	}
	write(pos[1], vf)
}

func fill(args []string) {
	fs := flag.NewFlagSet("fill", flag.ExitOnError)
	full := fs.Bool("full", false, "write the full trace of every intermediate")
	pos, flags := split(args)
	fs.Parse(flags)
	if len(pos) < 2 {
		usage()
	}
	vf, err := rxe1.LoadVectors(pos[0])
	if err != nil {
		fail(err)
	}
	for i := range vf.Vectors {
		vf.Vectors[i].Expect.TraceDigest = "pending" // asks Check for the trace
	}
	vf.KeepTraces = *full
	results, _, err := vf.Check(vf.Params.Validate() != nil)
	if err != nil {
		fail(err)
	}
	vf.Fill(results, *full)
	vf.Generator = "rxe1-diff (protocol B, the second implementation)"
	write(pos[1], vf)
}

func check(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	report := fs.String("report", "", "write a markdown report")
	pos, flags := split(args)
	fs.Parse(flags)
	if len(pos) < 1 {
		usage()
	}
	if rxe1.IsReferenceFile(pos[0]) {
		t0 := time.Now()
		divs, err := rxe1.CheckReferenceFile(pos[0], os.Getenv("IGNEUM_FULL_DATASETS") != "")
		if err != nil {
			fail(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# RXE1 differential: %s\n\n- reference file; diverging %d; wall %s\n\n", pos[0], len(divs), time.Since(t0).Round(time.Millisecond))
		if len(divs) == 0 {
			b.WriteString("Every vector agrees on every published intermediate.\n")
		} else {
			b.WriteString("| vector | first diverging intermediate | have | want |\n|---|---|---|---|\n")
			for _, d := range divs {
				fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", d.Vector, d.Stage, d.Have, d.Want)
			}
		}
		fmt.Print(b.String())
		if *report != "" {
			os.WriteFile(*report, []byte(b.String()), 0o644)
		}
		if len(divs) > 0 {
			os.Exit(1)
		}
		return
	}
	vf, err := rxe1.LoadVectors(pos[0])
	if err != nil {
		fail(err)
	}
	t0 := time.Now()
	results, divs, err := vf.Check(vf.Params.Validate() != nil)
	if err != nil {
		fail(err)
	}
	var b strings.Builder
	pid := vf.Params.PID()
	fmt.Fprintf(&b, "# RXE1 differential: %s\n\n- variant %s; pid %x; generator: %s\n- vectors %d; agreeing %d; diverging %d; wall %s\n\n",
		pos[0], vf.Params.Variant(), pid[:8], vf.Generator, len(vf.Vectors), len(vf.Vectors)-len(divs), len(divs), time.Since(t0).Round(time.Millisecond))
	if len(divs) == 0 {
		b.WriteString("Every vector agrees on every published intermediate.\n")
	} else {
		b.WriteString("| vector | first diverging intermediate | have | want |\n|---|---|---|---|\n")
		for _, d := range divs {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", d.Vector, d.Stage, d.Have, d.Want)
		}
	}
	b.WriteString("\nThis side's outputs (first eight bytes of seed and out):\n")
	for i, v := range vf.Vectors {
		fmt.Fprintf(&b, "- %s: seed %x out %x\n", v.Name, results[i].Seed[:8], results[i].Out[:8])
	}
	fmt.Print(b.String())
	if *report != "" {
		os.WriteFile(*report, []byte(b.String()), 0o644)
	}
	if len(divs) > 0 {
		os.Exit(1)
	}
}

// campaign writes n nonces' trace digests for a record (section 12: at
// least 10,000 full-size nonce comparisons per decisive backend). The file
// is this side's; the reference computes the same digests from the same
// inputs and the two files compare line by line.
func campaign(args []string) {
	fs := flag.NewFlagSet("campaign", flag.ExitOnError)
	n := fs.Int("n", 10000, "nonces")
	seed := fs.Int64("seed", 20261010, "input generator seed")
	pos, flags := split(args)
	fs.Parse(flags)
	if len(pos) < 2 {
		usage()
	}
	p := params(pos[0])
	vf := &rxe1.VectorFile{Algorithm: rxe1.ID, Spec: "campaign", Params: p}
	rng := rand.New(rand.NewSource(*seed))
	var es [32]byte
	rng.Read(es[:])
	chain := hex.EncodeToString([]byte("igneum-devnet-7310"))
	for i := 0; i < *n; i++ {
		hb := make([]byte, rng.Intn(200))
		rng.Read(hb)
		vf.Vectors = append(vf.Vectors, rxe1.Vector{Name: fmt.Sprintf("c%06d", i), ChainID: chain, HeaderBlob: hex.EncodeToString(hb), EpochSeed: hex.EncodeToString(es[:]), EpochNumber: uint64(rng.Intn(4)), Nonce: rng.Uint64()})
		vf.Vectors[i].Expect.TraceDigest = "pending"
	}
	t0 := time.Now()
	results, _, err := vf.Check(p.Validate() != nil)
	if err != nil {
		fail(err)
	}
	vf.Fill(results, false)
	vf.Generator = fmt.Sprintf("rxe1-diff campaign seed %d, %d nonces, %s", *seed, *n, time.Since(t0).Round(time.Millisecond))
	if strings.HasSuffix(pos[1], ".jsonl") {
		if err := rxe1.WriteCampaign(pos[1], vf, results); err != nil {
			fail(err)
		}
		fmt.Println("wrote", pos[1])
		return
	}
	write(pos[1], vf)
}

// campaignCheck recomputes the reference's JSONL campaign file for a record:
//
//	rxe1-diff campaign-check FILE.jsonl VARIANT [-report out.md]
func campaignCheck(args []string) {
	fs := flag.NewFlagSet("campaign-check", flag.ExitOnError)
	report := fs.String("report", "", "write a markdown report")
	pos, flags := split(args)
	fs.Parse(flags)
	if len(pos) < 2 {
		usage()
	}
	p := params(pos[1])
	t0 := time.Now()
	lines, divs, err := rxe1.CheckCampaign(pos[0], p, p.Validate() != nil)
	if err != nil {
		fail(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# RXE1 campaign check: %s\n\n- variant %s; lines %d; agreeing %d; differing %d; wall %s\n\n", pos[0], p.Variant(), lines, lines-len(divs), len(divs), time.Since(t0).Round(time.Millisecond))
	if len(divs) == 0 {
		b.WriteString("Every line agrees on out and on the trace digest.\n")
	} else {
		b.WriteString("| line | field | have | want |\n|---|---|---|---|\n")
		for i, d := range divs {
			if i >= 50 {
				fmt.Fprintf(&b, "| ... | %d more | | |\n", len(divs)-50)
				break
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", d.Vector, d.Stage, d.Have, d.Want)
		}
	}
	fmt.Print(b.String())
	if *report != "" {
		os.WriteFile(*report, []byte(b.String()), 0o644)
	}
	if len(divs) > 0 {
		os.Exit(1)
	}
}

func write(path string, v any) {
	raw, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		fail(err)
	}
	fmt.Println("wrote", path)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "rxe1-diff:", err)
	os.Exit(1)
}

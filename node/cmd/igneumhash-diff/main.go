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

// igneumhash-diff is the differential tool between the reference's vectors
// and the node's independent implementation (hard gate G0).
//
//	igneumhash-diff check  REF.json [-report out.md]   run every vector; report the first diverging stage and byte
//	igneumhash-diff fill   IN.json OUT.json [-rounds]   write this implementation's stages for the inputs in IN.json
//	igneumhash-diff gen    CELL OUT.json [-n 16]        write an input set for an ablation cell (no expectations)
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/consensus/igneum/igneumhash"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "check":
		check(os.Args[2:])
	case "fill":
		fill(os.Args[2:])
	case "reffill":
		reffill(os.Args[2:])
	case "gen":
		gen(os.Args[2:])
	case "bench":
		bench(os.Args[2:])
	default:
		usage()
	}
}

// splitArgs lets flags follow the positional arguments (check FILE -report x).
func splitArgs(args []string) (pos, flags []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") || (len(flags) > 0 && !strings.HasPrefix(flags[len(flags)-1], "-=") && strings.HasPrefix(flags[len(flags)-1], "-") && !strings.Contains(flags[len(flags)-1], "=") && !isBoolFlag(flags[len(flags)-1])) {
			flags = append(flags, a)
		} else {
			pos = append(pos, a)
		}
	}
	return pos, flags
}

func isBoolFlag(f string) bool {
	return f == "-rounds" || f == "-dataset" || f == "--rounds" || f == "--dataset"
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: igneumhash-diff check REF.json [-report out.md] | fill IN.json OUT.json [-rounds] | reffill REF.json OUT.json | gen CELL OUT.json [-n 16]")
	os.Exit(2)
}

func check(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	report := fs.String("report", "", "write a markdown report here")
	pos, flags := splitArgs(args)
	fs.Parse(flags)
	if len(pos) < 1 {
		usage()
	}
	path := pos[0]
	vf, err := loadAny(path)
	if err != nil {
		fail(err)
	}
	start := time.Now()
	results, divs, err := vf.Check()
	if err != nil {
		fail(err)
	}
	elapsed := time.Since(start)
	var b strings.Builder
	fmt.Fprintf(&b, "# IgneumHash-R0A v0 differential: %s\n\n", path)
	fmt.Fprintf(&b, "- algorithm: %s; variant: %s; class: %s; spec: %s; generator: %s\n", vf.Algorithm, vf.Params.Variant(), vf.Class, vf.Spec, vf.Generator)
	ph := vf.Params.ParamsHash()
	fmt.Fprintf(&b, "- params: chainId %d, version %d, epochLength %d, dataset %d bytes, scratch %d, rounds %d, lanes %d; P = %s\n",
		vf.Params.ChainID, vf.Params.Version, vf.Params.EpochLength, vf.Params.DatasetBytes, vf.Params.ScratchBytes, vf.Params.Rounds, vf.Params.Lanes, hex.EncodeToString(ph[:]))
	fmt.Fprintf(&b, "- vectors: %d; agreeing: %d; diverging: %d; wall time %s (includes dataset generation)\n\n", len(vf.Vectors), len(vf.Vectors)-len(divs), len(divs), elapsed.Round(time.Millisecond))
	if len(divs) == 0 {
		b.WriteString("Every vector agrees on every published stage.\n")
	} else {
		b.WriteString("| vector | first diverging stage | byte | have | want |\n|---|---|---:|---|---|\n")
		for _, d := range divs {
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %s |\n", d.Vector, d.Stage, d.Byte, d.Have, d.Want)
		}
	}
	b.WriteString("\nStages this implementation computed (first eight bytes; for the reference to diff the other way):\n\n")
	for i, v := range vf.Vectors {
		st := results[i]
		if v.Skip != "" || v.DatasetOnly {
			continue
		}
		fmt.Fprintf(&b, "- %s: P %s S %s E %s D %s M %s F %s\n", v.Name,
			hex.EncodeToString(st.ParamsHash[:8]), hex.EncodeToString(st.Seed[:8]), hex.EncodeToString(st.EpochSeed[:8]), hex.EncodeToString(st.DatasetID[:8]),
			hex.EncodeToString(st.Mix[:8]), hex.EncodeToString(st.Digest[:8]))
	}
	fmt.Print(b.String())
	if *report != "" {
		if err := os.WriteFile(*report, []byte(b.String()), 0o644); err != nil {
			fail(err)
		}
	}
	if len(divs) > 0 {
		os.Exit(1)
	}
}

func fill(args []string) {
	fs := flag.NewFlagSet("fill", flag.ExitOnError)
	rounds := fs.Bool("rounds", false, "include the section 4 trace")
	pos, flags := splitArgs(args)
	fs.Parse(flags)
	if len(pos) < 2 {
		usage()
	}
	vf, err := loadAny(pos[0])
	if err != nil {
		fail(err)
	}
	if *rounds {
		for i := range vf.Vectors {
			if vf.Vectors[i].Expect.Trace == nil {
				vf.Vectors[i].Expect.Trace = &igneumhash.TraceExpect{}
			}
		}
	}
	results, _, err := vf.Check()
	if err != nil {
		fail(err)
	}
	vf.Fill(results, *rounds)
	vf.Algorithm = igneumhash.Name
	writeJSON(pos[1], vf)
}

func gen(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	n := fs.Int("n", 16, "vectors to write")
	pos, flags := splitArgs(args)
	fs.Parse(flags)
	if len(pos) < 2 {
		usage()
	}
	p, ok := igneumhash.Ablation[pos[0]]
	if !ok {
		fail(fmt.Errorf("unknown cell %q", pos[0]))
	}
	vf := &igneumhash.VectorFile{Algorithm: igneumhash.Name, Params: p}
	for i := 0; i < *n; i++ {
		var hh [32]byte
		for k := range hh {
			hh[k] = byte((i*131 + k*17 + 7) & 0xff)
		}
		height := uint64(i) * (p.EpochLength/4 + 1)
		nonce := uint64(i)*0x9E3779B97F4A7C15 + 1
		difficulty := "1"
		switch i { // the vector classes the envelope owes (section 10) and chapter 27's edges
		case 0:
			height = 0 // genesis height, epoch 0
		case 1:
			nonce = 0
		case 2:
			nonce = ^uint64(0)
		case 3:
			height = p.EpochLength - 1
		case 4:
			height = p.EpochLength
		case 5:
			hh = [32]byte{}
		case 6:
			for k := range hh {
				hh[k] = 0xff
			}
		case 7:
			nonce = 0x00000001_FFFFFFFF // lo32 at its last value
		case 8:
			nonce = 0x00000002_00000000 // the next job's hi32, lo32 restarted: no carry
		case 9:
			difficulty = "115792089237316195423570985008687907853269984665640564039457584007913129639935" // 2^256 - 1: target 0
		case 10:
			difficulty = "1000000000000" // an ordinary target
		}
		vf.Vectors = append(vf.Vectors, igneumhash.Vector{Name: fmt.Sprintf("%s-%02d", pos[0], i), HeaderHash: hex.EncodeToString(hh[:]), Height: height, Nonce: nonce, Difficulty: difficulty})
	}
	writeJSON(fs.Arg(1), vf)
}

// loadAny reads a reference vector file (a "candidate" header) or this
// implementation's own format.
func loadAny(path string) (*igneumhash.VectorFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(raw[:min(len(raw), 200)]), "\"candidate\"") {
		return igneumhash.LoadReference(path)
	}
	return igneumhash.LoadVectors(path)
}

func writeJSON(path string, v any) {
	raw, err := jsonMarshal(v)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		fail(err)
	}
	fmt.Println("wrote", path)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "igneumhash-diff:", err)
	os.Exit(1)
}

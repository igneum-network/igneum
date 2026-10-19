// b1-diff: the differential tool of the Forge-B1 second implementation (F1
// on B1): own vectors, the reference's known.json, the first differing value
// per vector.
//
//	b1-diff gen  '<params document>' OUT.json [-seeds N] [-labels K] [-seed S]
//	b1-diff fill IN.json OUT.json [-labels K]            this side's values for an input's seeds
//	b1-diff check FILE.json [-report OUT.md] [-tag TAG]  the reference's or this side's file
//	b1-diff info '<params document>'                pid, T, proof bytes
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/consensus/igneum/forgeb1"
)

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "b1-diff: "+f+"\n", a...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		die("usage: b1-diff gen|fill|check|info ...")
	}
	switch os.Args[1] {
	case "info":
		p, testOnly, err := forgeb1.ParamsFromDocument([]byte(os.Args[2]))
		if err != nil {
			die("%v", err)
		}
		pid := p.PID()
		prog := forgeb1.ProgramID(pid)
		fmt.Printf("variant %s test_only %v pid %s program_id %s N %d D %d proof_bytes %d\n", p.Variant(), testOnly, hex.EncodeToString(pid[:]), hex.EncodeToString(prog[:]), p.N(), p.D(), p.ProofBytes())
	case "gen":
		fs := flag.NewFlagSet("gen", flag.ExitOnError)
		triples := fs.Int("triples", 4, "triples: all-zero, all-0xff, then random")
		labels := fs.Int("labels", 16, "labels recorded per vector")
		seed := fs.Int64("seed", 20261010, "random seed")
		if len(os.Args) < 4 {
			die("gen '<document>' OUT.json")
		}
		fs.Parse(os.Args[4:])
		doc := os.Args[2]
		if _, _, err := forgeb1.ParamsFromDocument([]byte(doc)); err != nil {
			die("%v", err)
		}
		rng := rand.New(rand.NewSource(*seed))
		z, f := hex.EncodeToString(make([]byte, 32)), strings.Repeat("ff", 32)
		vf := &forgeb1.VectorFile{Params: json.RawMessage(doc)}
		vf.Vectors = append(vf.Vectors, forgeb1.Vector{EpochSeed: z, FillSeed: z, Transcript: z}, forgeb1.Vector{EpochSeed: f, FillSeed: f, Transcript: f})
		for len(vf.Vectors) < *triples {
			b := make([]byte, 96)
			rng.Read(b)
			vf.Vectors = append(vf.Vectors, forgeb1.Vector{EpochSeed: hex.EncodeToString(b[0:32]), FillSeed: hex.EncodeToString(b[32:64]), Transcript: hex.EncodeToString(b[64:96])})
		}
		start := time.Now()
		if err := vf.Fill(*labels); err != nil {
			die("%v", err)
		}
		write(os.Args[3], vf)
		fmt.Printf("wrote %s: %d vectors in %s\n", os.Args[3], len(vf.Vectors), time.Since(start).Round(time.Millisecond))
	case "fill":
		fs := flag.NewFlagSet("fill", flag.ExitOnError)
		labels := fs.Int("labels", 16, "labels recorded per vector")
		if len(os.Args) < 4 {
			die("fill IN.json OUT.json")
		}
		fs.Parse(os.Args[4:])
		vf, err := forgeb1.ReadFile(os.Args[2])
		if err != nil {
			die("%v", err)
		}
		if err := vf.Fill(*labels); err != nil {
			die("%v", err)
		}
		write(os.Args[3], vf)
	case "check":
		fs := flag.NewFlagSet("check", flag.ExitOnError)
		report := fs.String("report", "", "write a markdown report")
		tagFlag := fs.String("tag", "", "the variant tag when the file carries no params document (b1-g0-m64-l128-q32-b64)")
		if len(os.Args) < 3 {
			die("check FILE.json")
		}
		fs.Parse(os.Args[3:])
		vf, err := forgeb1.ReadFile(os.Args[2])
		if err != nil {
			die("%v", err)
		}
		var params *forgeb1.Params
		if *tagFlag != "" {
			p, err := forgeb1.ParamsFromTag(*tagFlag)
			if err != nil {
				die("%v", err)
			}
			params = &p
		} else if len(vf.Params) == 0 {
			// the tag from the file's directory name
			p, err := forgeb1.ParamsFromTag(filepath.Base(filepath.Dir(os.Args[2])))
			if err != nil {
				die("no params document in the file and no -tag: %v", err)
			}
			params = &p
		}
		start := time.Now()
		divs, err := vf.Check(params)
		if err != nil {
			die("%s: %v", os.Args[2], err)
		}
		line := fmt.Sprintf("- %s; vectors %d; diverging %d; wall %s", os.Args[2], len(vf.Vectors), len(divs), time.Since(start).Round(time.Millisecond))
		var b strings.Builder
		fmt.Fprintf(&b, "# b1-diff check\n\n%s\n\n", line)
		if len(divs) > 0 {
			b.WriteString("| vector | first divergence | this side | the file |\n|---|---|---|---|\n")
			for _, d := range divs {
				fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", d.Vector, d.Stage, d.Have, d.Want)
			}
		}
		if *report != "" {
			os.WriteFile(*report, []byte(b.String()), 0o644)
		}
		fmt.Print(b.String())
		if len(divs) > 0 {
			os.Exit(1)
		}
	default:
		die("unknown subcommand %q", os.Args[1])
	}
}

func write(path string, vf *forgeb1.VectorFile) {
	b, err := json.MarshalIndent(vf, "", " ")
	if err != nil {
		die("%v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		die("%v", err)
	}
}

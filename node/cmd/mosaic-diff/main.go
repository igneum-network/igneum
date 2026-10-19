// mosaic-diff: the Mosaic-VF Phase A differential (protocol B's independent
// implementation of the work formula, normalisation, score and order).
//
//	mosaic-diff check FILE.json [-report OUT.md]   the protocol team's or this side's vectors
//	mosaic-diff own OUT.json                        this side's vectors
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/consensus/igneum/mosaicscore"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: mosaic-diff check FILE.json [-report OUT.md] | own OUT.json")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "own":
		b, _ := json.MarshalIndent(mosaicscore.Own(), "", " ")
		if err := os.WriteFile(os.Args[2], b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "mosaic-diff:", err)
			os.Exit(1)
		}
	case "check":
		fs := flag.NewFlagSet("check", flag.ExitOnError)
		report := fs.String("report", "", "write a markdown report")
		fs.Parse(os.Args[3:])
		vf, err := mosaicscore.ReadFile(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "mosaic-diff:", err)
			os.Exit(1)
		}
		start := time.Now()
		divs, err := vf.Check()
		if err != nil {
			fmt.Fprintln(os.Stderr, "mosaic-diff:", err)
			os.Exit(1)
		}
		n := len(vf.Work) + len(vf.Normalise) + len(vf.Score) + len(vf.ChainScore) + len(vf.Compare)
		var b strings.Builder
		fmt.Fprintf(&b, "# mosaic-diff check\n\n- %s; vectors %d (work %d, normalise %d, score %d, chain score %d, compare %d); diverging %d; wall %s\n\n", os.Args[2], n, len(vf.Work), len(vf.Normalise), len(vf.Score), len(vf.ChainScore), len(vf.Compare), len(divs), time.Since(start).Round(time.Microsecond))
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
		fmt.Fprintln(os.Stderr, "mosaic-diff: unknown subcommand", os.Args[1])
		os.Exit(2)
	}
}

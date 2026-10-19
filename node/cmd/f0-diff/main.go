// f0-diff: the F0 encoding check (Forge, protocol B's independent implementation).
//
//	f0-diff check FILE.json [-report OUT.md]   the protocol team's or this side's vectors: the first differing byte per vector
//	f0-diff fill IN.json OUT.json              this side's x, seed, ticket, qualifies and public for the input tuples
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/consensus/igneum/forgecheck"
)

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "f0-diff: "+f+"\n", a...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 3 {
		die("usage: f0-diff check FILE.json | fill IN.json OUT.json")
	}
	switch os.Args[1] {
	case "fill":
		if len(os.Args) < 4 {
			die("fill IN.json OUT.json")
		}
		vf, err := forgecheck.ReadFile(os.Args[2])
		if err != nil {
			die("%v", err)
		}
		if err := vf.Fill(); err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(vf, "", " ")
		if err := os.WriteFile(os.Args[3], b, 0o644); err != nil {
			die("%v", err)
		}
	case "check":
		fs := flag.NewFlagSet("check", flag.ExitOnError)
		report := fs.String("report", "", "write a markdown report")
		fs.Parse(os.Args[3:])
		vf, err := forgecheck.ReadFile(os.Args[2])
		if err != nil {
			die("%v", err)
		}
		start := time.Now()
		divs, err := vf.Check()
		if err != nil {
			die("%v", err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# f0-diff check\n\n- %s; vectors %d; diverging %d; rejected encodings listed %d; wall %s\n\n", os.Args[2], len(vf.Vectors), len(divs), len(vf.Rejected), time.Since(start).Round(time.Microsecond))
		if len(divs) > 0 {
			b.WriteString("| vector | first divergence | this side | the file |\n|---|---|---|---|\n")
			for _, d := range divs {
				fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", d.Vector, d.Stage, d.Have, d.Want)
			}
		}
		for _, r := range vf.Rejected {
			fmt.Fprintf(&b, "- rejected encoding (the file's list): %s\n", r)
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

// f0r2-diff: the F0 revision 2 encoding check (Bounded Memory Tickets objects; protocol B's independent implementation).
//
//	f0r2-diff check FILE.json [-report OUT.md]   the protocol team's vectors: the first differing object per vector
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/consensus/igneum/forgecheckr2"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, "usage: f0r2-diff check FILE.json [-report OUT.md]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	report := fs.String("report", "", "write a markdown report")
	fs.Parse(os.Args[3:])
	vf, err := forgecheckr2.ReadFile(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "f0r2-diff:", err)
		os.Exit(1)
	}
	start := time.Now()
	divs, err := vf.Check()
	if err != nil {
		fmt.Fprintln(os.Stderr, "f0r2-diff:", err)
		os.Exit(1)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# f0r2-diff check\n\n- %s; vectors %d; diverging %d; rejected encodings listed %d; wall %s\n\n", os.Args[2], len(vf.Vectors), len(divs), len(vf.Rejected), time.Since(start).Round(time.Microsecond))
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
}

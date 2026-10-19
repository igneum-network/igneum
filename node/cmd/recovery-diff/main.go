// recovery-diff: the work-priced recovery differential (protocol B's independent implementation of RECOVERY-PRICING.md section 2).
//
//	recovery-diff check FILE.json
package main

import (
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/consensus/igneum/recoverycheck"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, "usage: recovery-diff check FILE.json")
		os.Exit(2)
	}
	vf, err := recoverycheck.ReadFile(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	divs, err := vf.Check()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("- %s: vectors %d; diverging %d\n", os.Args[2], len(vf.Vectors), len(divs))
	for _, d := range divs {
		fmt.Println("  ", d)
	}
	if len(divs) > 0 {
		os.Exit(1)
	}
}

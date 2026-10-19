// selection-diff: the succession opening condition's differential (protocol B's second reading of ENVELOPE 9.3 item 1).
//
//	selection-diff check FILE.json
package main

import (
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/consensus/igneum/selectioncheck"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, "usage: selection-diff check FILE.json")
		os.Exit(2)
	}
	vf, err := selectioncheck.ReadFile(os.Args[2])
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

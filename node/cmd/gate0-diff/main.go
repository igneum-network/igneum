// gate0-diff: the Mosaic-VF v0.2 gate 0 differential (protocol B's independent implementation).
//
//	gate0-diff check FILE.json...   the hash team's score, transition, reservoir and overflow files
package main

import (
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/consensus/igneum/mosaicv2"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, "usage: gate0-diff check FILE.json...")
		os.Exit(2)
	}
	rc := 0
	for _, f := range os.Args[2:] {
		class, n, divs, err := mosaicv2.CheckFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gate0-diff:", err)
			rc = 1
			continue
		}
		fmt.Printf("- %s (%s): vectors %d; diverging %d\n", f, class, n, len(divs))
		for _, d := range divs {
			fmt.Println("  ", d)
			rc = 1
		}
	}
	os.Exit(rc)
}

// lifecycle-diff: the lifecycle menu differential (protocol B's independent implementation of ENVELOPE-R0A-v0.md section 9.2).
//
//	lifecycle-diff own OUT.json      this side's vectors (schedules, validity, At, Next, upgrade verdicts)
//	lifecycle-diff check FILE.json          another implementation's file in this side's form
//	lifecycle-diff check-theirs FILE.json   the protocol team's pow/vectors/lifecycle/vectors.json
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/consensus/igneum/lifecyclecheck"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lifecycle-diff own OUT.json | check FILE.json")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "own":
		b, _ := json.MarshalIndent(lifecyclecheck.Own(), "", " ")
		if err := os.WriteFile(os.Args[2], b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "check-theirs":
		n, divs, err := lifecyclecheck.CheckTheirs(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("- %s: vectors %d; diverging %d\n", os.Args[2], n, len(divs))
		for _, d := range divs {
			fmt.Println("  ", d)
		}
		if len(divs) > 0 {
			os.Exit(1)
		}
	case "check":
		vf, err := lifecyclecheck.ReadFile(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		divs := vf.Check()
		fmt.Printf("- %s: cases %d; diverging %d\n", os.Args[2], len(vf.Cases), len(divs))
		for _, d := range divs {
			fmt.Println("  ", d)
		}
		if len(divs) > 0 {
			os.Exit(1)
		}
	}
}

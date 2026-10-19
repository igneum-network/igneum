// tp115-paths: protocol B's recovery paths for TP-115 and the differential against the protocol team's.
//
//	tp115-paths gen OUT.json [-dpre N]
//	tp115-paths check THEIRS.json [-dpre N]   the first differing index per key
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"sort"

	"github.com/ethereum/go-ethereum/consensus/igneum/tp115"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: tp115-paths gen OUT.json [-dpre N] | check THEIRS.json [-dpre N]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("tp115", flag.ExitOnError)
	dpre := fs.String("dpre", "1048576", "the pre-silence difficulty D_pre")
	fs.Parse(os.Args[3:])
	d, ok := new(big.Int).SetString(*dpre, 10)
	if !ok {
		fmt.Fprintln(os.Stderr, "dpre")
		os.Exit(1)
	}
	paths := tp115.Research.Paths(d, append(append([]uint64(nil), tp115.Silences...), 300, 600, 1200))
	switch os.Args[1] {
	case "gen":
		b, _ := json.MarshalIndent(map[string]any{"generator": "node/consensus/igneum/tp115 (protocol B, from PHASE-A-CONSENSUS.md section 6.3)", "d_pre": d.String(), "paths": paths}, "", " ")
		os.WriteFile(os.Args[2], b, 0o644)
	case "check":
		raw, err := os.ReadFile(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var theirs struct {
			Paths map[string][]string `json:"paths"`
		}
		if err := json.Unmarshal(raw, &theirs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		keys := make([]string, 0, len(theirs.Paths))
		for k := range theirs.Paths {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		diverging := 0
		for _, k := range keys {
			mine, ok := tp115.Research.PathFor(d, k)
			if !ok {
				fmt.Printf("%s: no path on this side\n", k)
				diverging++
				continue
			}
			t := theirs.Paths[k]
			first := -1
			for i := 0; i < len(t) || i < len(mine); i++ {
				if i >= len(t) || i >= len(mine) || t[i] != mine[i] {
					first = i
					break
				}
			}
			if first < 0 {
				fmt.Printf("%s: agree (%d blocks)\n", k, len(t))
				continue
			}
			diverging++
			at := func(p []string, i int) string {
				if i < len(p) {
					return p[i]
				}
				return "(end)"
			}
			fmt.Printf("%s: first difference at block %d: mine %s theirs %s (lengths %d, %d)\n", k, first, at(mine, first), at(t, first), len(mine), len(t))
		}
		fmt.Printf("- D_pre %s: keys %d; diverging %d\n", d, len(keys), diverging)
		if diverging > 0 {
			os.Exit(1)
		}
	}
}

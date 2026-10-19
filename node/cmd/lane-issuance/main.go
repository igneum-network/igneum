// lane-issuance: replays the Mosaic-VF lane-reservoir research policy over a
// lane sequence for ecosystem's simulator (research only; nothing in the
// engine calls it).
//
//	lane-issuance '<policy json>' SEQUENCE [-schedule candidate|hardcap] [-fees WEI]
//
// SEQUENCE is a file of one block per line: "lane" or "lane fees" (decimal
// wei); "-" reads standard input. The output is one JSON line per block
// (height, lane, subsidy, fees, reservoir) and a final state line.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/consensus/igneum/issuance"
	"github.com/ethereum/go-ethereum/consensus/igneum/issuance/lanes"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lane-issuance '<policy json>' SEQUENCE [-schedule candidate|hardcap] [-fees WEI]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("lane-issuance", flag.ExitOnError)
	sched := fs.String("schedule", "candidate", "candidate (tail) or hardcap")
	defFees := fs.String("fees", "0", "fees per block when the sequence names none")
	fs.Parse(os.Args[3:])
	var p lanes.Policy
	if err := json.Unmarshal([]byte(os.Args[1]), &p); err != nil {
		fmt.Fprintln(os.Stderr, "lane-issuance: policy:", err)
		os.Exit(1)
	}
	params := issuance.Candidate
	if *sched == "hardcap" {
		params = issuance.HardCap
	}
	r, err := lanes.New(p, issuance.New(params))
	if err != nil {
		fmt.Fprintln(os.Stderr, "lane-issuance:", err)
		os.Exit(1)
	}
	in := os.Stdin
	if os.Args[2] != "-" {
		f, err := os.Open(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "lane-issuance:", err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	}
	fees0, ok := new(big.Int).SetString(*defFees, 10)
	if !ok {
		fmt.Fprintln(os.Stderr, "lane-issuance: -fees is not a decimal integer")
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		var lane int
		if _, err := fmt.Sscanf(parts[0], "%d", &lane); err != nil {
			fmt.Fprintln(os.Stderr, "lane-issuance: lane:", line)
			os.Exit(1)
		}
		fees := fees0
		if len(parts) > 1 {
			if fees, ok = new(big.Int).SetString(parts[1], 10); !ok {
				fmt.Fprintln(os.Stderr, "lane-issuance: fees:", line)
				os.Exit(1)
			}
		}
		out, err := r.Next(lane, fees)
		if err != nil {
			fmt.Fprintln(os.Stderr, "lane-issuance:", err)
			os.Exit(1)
		}
		enc.Encode(map[string]any{"height": out.Height, "lane": out.Lane, "subsidy": out.Subsidy.String(), "fees": out.Fees.String(), "reservoir": out.Reservoir.String()})
	}
	st := r.State
	res := make([]string, len(st.Reservoir))
	for i, v := range st.Reservoir {
		res[i] = v.String()
	}
	enc.Encode(map[string]any{"final": true, "height": st.Height, "issued": st.Issued.String(), "never_issued": st.NeverIssued.String(), "diverted": st.Diverted.String(), "fees_paid": st.FeesPaid.String(), "reservoirs": res, "miner_total": lanes.MinerTotal(r.S, st.Height).String()})
}

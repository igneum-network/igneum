// Copyright 2026 The Igneum Authors
// This file is part of go-ethereum (Igneum fork).
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

// igneum-supply writes the issuance schedule from the node's own code
// (consensus/igneum/issuance) as the TP-04 fixture FX-SUPPLY-TABLE
// (docs/security/TEST-PROGRAMME.md): one row per epoch with the subsidy,
// the miner and fund shares and the cumulative supply at the epoch's last
// height, from genesis through the tail transition plus the requested years
// of tail, and one row per requested height with -at.
//
//	igneum-supply -branch tail|cap [-tail-years 10] [-out table.csv]
//	igneum-supply -branch tail -at 53198849
package main

import (
	"flag"
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/consensus/igneum/issuance"
)

const blocksPerYear = 365 * 86400 / 15

func main() {
	branch := flag.String("branch", "tail", "tail or cap")
	tailYears := flag.Uint64("tail-years", 10, "years of tail after the transition (tail branch); years after the subsidy falls below 0.01 IGN (cap branch)")
	out := flag.String("out", "", "output file (default stdout)")
	at := flag.Uint64("at", 0, "print the row for one height and exit")
	flag.Parse()

	var p issuance.Params
	switch *branch {
	case "tail":
		p = issuance.Candidate
	case "cap":
		p = issuance.HardCap
	default:
		fmt.Fprintln(os.Stderr, "branch must be tail or cap")
		os.Exit(2)
	}
	s := issuance.New(p)
	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	if *at > 0 {
		miner, fund := s.Reward(*at)
		fmt.Fprintln(w, "height,subsidy,miner,fund,cumulative_supply,cumulative_fund")
		fmt.Fprintf(w, "%d,%s,%s,%s,%s,%s\n", *at, s.Subsidy(*at), miner, fund, s.Issued(*at), s.FundIssued(*at))
		return
	}
	var lastEpoch uint64
	if e, _, ok := s.Transition(); ok {
		lastEpoch = e + (*tailYears*blocksPerYear)/p.Epoch + 1
	} else {
		hundredth := new(big.Int).Div(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil), big.NewInt(100))
		for e := uint64(0); ; e++ {
			if _, r := s.EpochState(e); r.Cmp(hundredth) < 0 {
				lastEpoch = e + (*tailYears*blocksPerYear)/p.Epoch + 1
				break
			}
		}
	}
	fmt.Fprintln(w, "epoch,first_height,last_height,subsidy,miner,fund,cumulative_supply_at_last,cumulative_fund_at_last")
	fmt.Fprintf(w, "0,0,0,0,0,0,0,0\n")
	for e := uint64(0); e <= lastEpoch; e++ {
		_, r := s.EpochState(e)
		miner, fund := p.Split(r)
		first := e*p.Epoch + 1
		last := (e + 1) * p.Epoch
		fmt.Fprintf(w, "%d,%d,%d,%s,%s,%s,%s,%s\n", e, first, last, r, miner, fund, s.Issued(last), s.FundIssued(last))
	}
}

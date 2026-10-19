// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// igneum-emission prints the launch emission schedule as the node computes
// it (G-01's second, independent replay against the economics team's
// table; the project's order of 17:4x UK, 11 October 2026): from chain 7317's
// config default (params.IgneumChainIssuance; the re-cut form of PA-43,
// epochDiv 2048, the remainder in the epoch's last block, the per-epoch
// tail 2,048 IGN) it prints issued(h), subsidy(h), miner(h) and fund(h) in
// integer wei at the first and last block of every epoch through epoch
// 6,494 and the first tail epoch, one line at the tail's start (the first
// epoch whose amount is under the tail) and one at the year-60 height,
// and one fixture line for a one-block reorganisation at an epoch
// boundary: the remainder rides the epoch's LAST block, so a sibling at
// the boundary height carries the same subsidy (the schedule is a
// function of the height alone) and the reorganisation moves the
// remainder's block, never the amount. Pure over the schedule; no node.
//
//	go run ./cmd/igneum-emission > emission.csv
package main

import (
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/consensus/igneum/issuance"
	"github.com/ethereum/go-ethereum/params"
)

const (
	chainID   = 7317
	lastEpoch = 6494
	year60    = uint64(94_672_800) // 60 x 365.25 days at 20 s a block (the launch clock): 60 x 31,557,600 / 20
)

func main() {
	cfg := params.IgneumIssuanceForChainID(chainID)
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "no issuance default for chain", chainID)
		os.Exit(1)
	}
	p := issuance.Params{Epoch: cfg.Epoch, M: new(big.Int).Set(cfg.M), K: cfg.K, Tail: new(big.Int).Set(cfg.Tail), FundBps: cfg.FundBps, EpochDiv: cfg.EpochDiv}
	if cfg.TailPerEpoch != nil {
		p.TailPerEpoch = new(big.Int).Set(cfg.TailPerEpoch)
	}
	t := issuance.New(p)
	fmt.Printf("# igneum-emission: chain %d, epoch %d blocks, epochDiv %d, M %s wei, tailPerEpoch %s wei, fundBps %d; integer wei; issued(h) is the gross supply after block h\n", chainID, p.Epoch, p.EpochDiv, p.M, p.TailPerEpoch, p.FundBps)
	fmt.Println("row,epoch,height,issued_wei,subsidy_wei,miner_wei,fund_wei,fund_issued_wei")
	line := func(row string, h uint64) {
		e := (h - 1) / p.Epoch
		sub := t.Subsidy(h)
		miner, fund := t.Reward(h)
		fmt.Printf("%s,%d,%d,%s,%s,%s,%s,%s\n", row, e, h, t.Issued(h), sub, miner, fund, t.FundIssued(h))
	}
	tailStart := uint64(0)
	for e := uint64(0); e <= lastEpoch; e++ {
		first := e*p.Epoch + 1
		last := (e + 1) * p.Epoch
		line("first", first)
		line("last", last)
		if tailStart == 0 {
			start, _ := t.EpochState(e)
			amount := new(big.Int).Sub(p.M, start)
			amount.Div(amount, new(big.Int).SetUint64(p.EpochDiv))
			if amount.Cmp(p.TailPerEpoch) < 0 {
				tailStart = e
			}
		}
	}
	if tailStart == 0 {
		// Past the printed range: find it.
		for e := uint64(lastEpoch + 1); ; e++ {
			start, _ := t.EpochState(e)
			amount := new(big.Int).Sub(p.M, start)
			amount.Div(amount, new(big.Int).SetUint64(p.EpochDiv))
			if amount.Cmp(p.TailPerEpoch) < 0 {
				tailStart = e
				break
			}
		}
	}
	// The tail's start and the first whole tail epoch.
	line("tail_start", tailStart*p.Epoch+1)
	line("tail_first_epoch_first", tailStart*p.Epoch+1)
	line("tail_first_epoch_last", (tailStart+1)*p.Epoch)
	line("year60", year60)
	// The fixture: a one-block reorganisation at an epoch boundary. The
	// boundary height H = (e + 1) x Epoch carries the epoch's remainder;
	// two siblings at H (the canonical block and the one it displaces)
	// carry the same subsidy, since the schedule reads the height alone,
	// so the reorganisation moves the block that pays the remainder and
	// never the amount: issued(H) is one number on both branches.
	H := uint64(1) * p.Epoch
	start, per := t.EpochState(0)
	rem := new(big.Int).Sub(t.Issued(H), new(big.Int).Add(start, new(big.Int).Mul(per, new(big.Int).SetUint64(p.Epoch))))
	fmt.Printf("reorg_fixture,0,%d,%s,%s,,,remainder_in_last_block=%s;sibling_subsidy=%s;issued_equal_on_both_branches=true\n", H, t.Issued(H), t.Subsidy(H), rem, t.Subsidy(H))
}

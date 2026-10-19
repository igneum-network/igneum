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

package igneum

import (
	"math/big"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
)

// The attribution metric: each miner identity's share of the accepted work
// over a trailing window of the canonical chain, READ-ONLY DIAGNOSTIC for
// the pool team's feed. Nothing in consensus reads it and no rule
// depends on it (the outside review's amendment ruling of 11 October 2026 rejected
// any share-based trigger). Research prototype; every figure is labelled.
//
// Identity: the header's bond key where the block carries one (the
// registered identity the authority prototype's share and pool paths carry
// in the header), otherwise the coinbase address (solo work). Accepted work
// is the sum of the difficulty of the canonical blocks whose time lies
// inside the window ending at the head's time (block time, not the wall
// clock, so two nodes at the same head read the same figures).

const (
	// AttributionWindowDefault is the trailing window, thirty days in
	// seconds.
	AttributionWindowDefault uint64 = 30 * 24 * 3600
	// attributionMaxBlocks bounds the walk (a development chain with
	// second-long blocks).
	attributionMaxBlocks uint64 = 4_000_000
)

// AttributionLimits are the metric's stated limits: what it can show and
// what it cannot, in the words the feed carries beside the figures.
var AttributionLimits = []string{
	"withholding: a miner that finds blocks and withholds them is not counted; a withholding attack is detectable as a share below the identity's hash rate, not prevented",
	"sybil splitting: one operator under several bond keys or coinbase addresses reads as several identities; detectable by off-chain correlation, not prevented",
	"pools: work mined through a pool reads under the pool's identity (its bond key or coinbase); the pool's own share ledger is not visible to the node",
	"payout lists: a block with a payout list is attributed to its bond key, never to the list's entries",
	"window: by block time ending at the head's time, not by the wall clock; a chain that stalls keeps its last window",
	"diagnostic: read-only; no consensus rule, activation or trigger reads this figure",
}

// AttributionEntry is one identity's row.
type AttributionEntry struct {
	Identity string       `json:"identity"`
	Kind     string       `json:"kind"` // "bond" or "coinbase"
	Blocks   uint64       `json:"blocks"`
	Work     *hexutil.Big `json:"work"`
	ShareBps uint64       `json:"shareBps"` // share of the window's work in basis points, floor
}

// AttributionReport is igneum_attribution.
type AttributionReport struct {
	Label         string             `json:"label"`
	Head          uint64             `json:"head"`
	HeadHash      common.Hash        `json:"headHash"`
	HeadTime      uint64             `json:"headTime"`
	WindowSeconds uint64             `json:"windowSeconds"`
	WindowStart   uint64             `json:"windowStart"` // the first block time inside the window
	FromHeight    uint64             `json:"fromHeight"`  // the lowest height counted
	Blocks        uint64             `json:"blocks"`
	TotalWork     *hexutil.Big       `json:"totalWork"`
	Identities    []AttributionEntry `json:"identities"`
	Limits        []string           `json:"limits"`
}

// attributionCache keeps the last report per (head, window): the walk is
// one header read per block in the window.
type attributionCache struct {
	mu     sync.Mutex
	head   common.Hash
	window uint64
	report *AttributionReport
}

type attributionAcc struct {
	kind   string
	blocks uint64
	work   *big.Int
}

// Attribution computes the report at the chain's head for a window in
// seconds (zero or absent: thirty days).
func Attribution(chain consensus.ChainHeaderReader, windowSeconds uint64) *AttributionReport {
	if windowSeconds == 0 {
		windowSeconds = AttributionWindowDefault
	}
	rep := &AttributionReport{
		Label:         "prototype; read-only diagnostic; accepted work by identity over a trailing window of block time; nothing depends on it",
		WindowSeconds: windowSeconds,
		TotalWork:     (*hexutil.Big)(new(big.Int)),
		Identities:    []AttributionEntry{},
		Limits:        AttributionLimits,
	}
	head := chain.CurrentHeader()
	if head == nil {
		return rep
	}
	rep.Head, rep.HeadHash, rep.HeadTime = head.Number.Uint64(), head.Hash(), head.Time
	var start uint64
	if head.Time > windowSeconds {
		start = head.Time - windowSeconds
	}
	rep.WindowStart = start
	acc := map[string]*attributionAcc{}
	total := new(big.Int)
	h := head
	for n := uint64(0); h != nil && h.Number.Sign() > 0 && h.Time >= start && n < attributionMaxBlocks; n++ {
		id, kind := attributionIdentity(h)
		a := acc[id]
		if a == nil {
			a = &attributionAcc{kind: kind, work: new(big.Int)}
			acc[id] = a
		}
		a.blocks++
		a.work.Add(a.work, h.Difficulty)
		total.Add(total, h.Difficulty)
		rep.Blocks++
		rep.FromHeight = h.Number.Uint64()
		h = chain.GetHeader(h.ParentHash, h.Number.Uint64()-1)
	}
	rep.TotalWork = (*hexutil.Big)(total)
	for id, a := range acc {
		e := AttributionEntry{Identity: id, Kind: a.kind, Blocks: a.blocks, Work: (*hexutil.Big)(a.work)}
		if total.Sign() > 0 {
			bps := new(big.Int).Mul(a.work, big.NewInt(10000))
			e.ShareBps = bps.Div(bps, total).Uint64()
		}
		rep.Identities = append(rep.Identities, e)
	}
	sort.Slice(rep.Identities, func(i, j int) bool {
		a, b := rep.Identities[i], rep.Identities[j]
		if c := (*big.Int)(a.Work).Cmp((*big.Int)(b.Work)); c != 0 {
			return c > 0
		}
		return a.Identity < b.Identity
	})
	return rep
}

// attributionIdentity names a block's miner identity: its bond key when
// the header carries one, else its coinbase.
func attributionIdentity(h *types.Header) (id, kind string) {
	if h.BondKey != nil && *h.BondKey != (common.Hash{}) {
		return "bond:" + h.BondKey.Hex(), "bond"
	}
	return "coinbase:" + h.Coinbase.Hex(), "coinbase"
}

// Attribution is igneum_attribution(windowSeconds): the report at the head,
// cached per head and window.
func (api *IgneumAPI) Attribution(windowSeconds *hexutil.Uint64) *AttributionReport {
	var window uint64
	if windowSeconds != nil {
		window = uint64(*windowSeconds)
	}
	if window == 0 {
		window = AttributionWindowDefault
	}
	head := api.chain.CurrentHeader()
	c := &api.attribution
	c.mu.Lock()
	defer c.mu.Unlock()
	if head != nil && c.report != nil && c.head == head.Hash() && c.window == window {
		return c.report
	}
	rep := Attribution(api.chain, window)
	if head != nil {
		c.head, c.window, c.report = head.Hash(), window, rep
	}
	return rep
}

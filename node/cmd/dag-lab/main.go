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

// dag-lab is the ordering ladder's step 0 driver
// (docs/money/ORDERING-STEP1-2026-10-19.md): against a throwaway chain under
// the block DAG ordering it funds a set of senders from the development
// faucet, broadcasts plain transfers at a steady rate through the sealing
// nodes (every k-th a conflicting pair: two transactions of one nonce sent
// to two sealers at once, the ordering to decide), and reads each
// transaction's fate on a reader node through igneum_dagTx: the time from
// broadcast to its first carrying block, to its acceptance at an ordered
// position, to a blue-score depth of 1, 2, 3, 6 and the merge-depth bound
// above its accepting block with that block unchanged (the design's
// CONFIRMED and SETTLED), and whether its accepting block ever changed
// after a depth was reached (a reversal). It samples every node's head
// every 30 seconds and, at the end, compares every node's ordering hash
// at every order, counts blues and reds and lanes, reads the arrival
// clock of every block on every node (the propagation delay D per lane)
// and the reorganisation records, and writes it all to -out.
package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

type node struct {
	name string
	url  string
	rpc  *rpc.Client
	eth  *ethclient.Client
}

type dagTx struct {
	Tx             common.Hash    `json:"tx"`
	Carried        []common.Hash  `json:"carried"`
	Accepting      common.Hash    `json:"accepting"`
	AcceptingOrder hexutil.Uint64 `json:"acceptingOrder"`
	HeadOrder      hexutil.Uint64 `json:"headOrder"`
	HeadHash       common.Hash    `json:"headHash"`
	Accepted       bool           `json:"accepted"`
	Refused        []common.Hash  `json:"refused"`
}

type dagHead struct {
	Gate       hexutil.Uint64 `json:"gate"`
	K          uint8          `json:"k"`
	MergeDepth uint64         `json:"mergeDepth"`
	Head       dagBlock       `json:"head"`
	Tips       []common.Hash  `json:"tips"`
}

type dagBlock struct {
	Hash           common.Hash    `json:"hash"`
	Number         hexutil.Uint64 `json:"number"`
	Lane           *uint8         `json:"lane"`
	Parents        []common.Hash  `json:"parents"`
	SelectedParent common.Hash    `json:"selectedParent"`
	MergesetBlues  []common.Hash  `json:"mergesetBlues"`
	MergesetReds   []common.Hash  `json:"mergesetReds"`
	BlueScore      hexutil.Uint64 `json:"blueScore"`
	BlueWork       *hexutil.Big   `json:"blueWork"`
	OnChain        bool           `json:"onChain"`
	Blue           bool           `json:"blue"`
	ArrivalMs      *int64         `json:"arrivalMs"`
	Time           hexutil.Uint64 `json:"time"`
	Txs            int            `json:"txs"`
}

type dagChainEntry struct {
	Hash         common.Hash    `json:"hash"`
	Number       hexutil.Uint64 `json:"number"`
	Order        hexutil.Uint64 `json:"order"`
	Lane         *uint8         `json:"lane"`
	Mergeset     []common.Hash  `json:"mergeset"`
	Blues        int            `json:"blues"`
	Reds         int            `json:"reds"`
	OrderingHash common.Hash    `json:"orderingHash"`
	Time         hexutil.Uint64 `json:"time"`
	Credits      []uint64       `json:"credits"`
	Credited     []bool         `json:"credited"`
	BlueWork     *hexutil.Big   `json:"blueWork"`
}

type dagReorg struct {
	At      int64       `json:"at"`
	OldHead common.Hash `json:"oldHead"`
	NewHead common.Hash `json:"newHead"`
	Depth   uint64      `json:"depth"`
	Added   uint64      `json:"added"`
}

// txRecord is one transaction's clock.
type txRecord struct {
	Hash        common.Hash      `json:"hash"`
	Sender      int              `json:"sender"`
	Node        string           `json:"node"`
	Conflict    string           `json:"conflict,omitempty"` // the pair's id when part of a conflicting pair
	BroadcastMs int64            `json:"broadcastMs"`
	CarriedMs   int64            `json:"carriedMs,omitempty"`
	AcceptedMs  int64            `json:"acceptedMs,omitempty"`
	Accepting   common.Hash      `json:"accepting,omitempty"`
	Order       uint64           `json:"order,omitempty"`
	Confirmed   map[string]int64 `json:"confirmedMs"` // depth -> ms
	Reversals   map[string]int   `json:"reversals"`   // depth -> count of accepting-block changes after the depth was reached
	Refused     bool             `json:"refused"`
	Final       string           `json:"final"`
	lastAccept  common.Hash
	done        bool
}

type headSample struct {
	AtMs   int64             `json:"atMs"`
	Heads  map[string]uint64 `json:"heads"`  // node -> head number
	Orders map[string]uint64 `json:"orders"` // node -> head blue score
	Tips   map[string]int    `json:"tips"`
	Hashes map[string]string `json:"hashes"`
}

type result struct {
	Started  string                      `json:"started"`
	Ended    string                      `json:"ended"`
	Params   map[string]any              `json:"params"`
	Nodes    []string                    `json:"nodes"`
	Txs      []*txRecord                 `json:"txs"`
	Heads    []headSample                `json:"heads"`
	Summary  map[string]any              `json:"summary"`
	Chain    []*dagChainEntry            `json:"chain"`
	Reorgs   map[string][]dagReorg       `json:"reorgs"`
	Arrivals map[string]map[string]int64 `json:"arrivals"` // block -> node -> ms
	Lanes    map[string]uint8            `json:"lanes"`
	Metrics  map[string]map[string]any   `json:"metrics"`
}

func main() {
	var (
		rpcs      = flag.String("rpcs", "", "every node's RPC, name=url, comma separated (the first is the reader the clocks are read on)")
		sendTo    = flag.String("send", "", "the names of the nodes transactions are broadcast through, comma separated")
		n         = flag.Int("n", 500, "transactions to send")
		interval  = flag.Duration("interval", 2*time.Second, "the gap between broadcasts")
		every     = flag.Int("conflict-every", 25, "every k-th broadcast is a conflicting pair (0 off)")
		senders   = flag.Int("senders", 8, "funded sender accounts")
		depths    = flag.String("depths", "1,2,3,6,150", "the blue-score depths the confirmation clock reads")
		poll      = flag.Duration("poll", 250*time.Millisecond, "the reader's poll interval")
		wait      = flag.Duration("wait", 15*time.Minute, "how long after the last broadcast to keep reading the clocks")
		minBlocks = flag.Uint64("min-blocks", 0, "keep reading until the reader's chain holds this many selected-chain blocks (0: no wait)")
		out       = flag.String("out", "result.json", "the result file")
	)
	flag.Parse()
	var nodes []*node
	for _, kv := range strings.Split(*rpcs, ",") {
		name, url, ok := strings.Cut(kv, "=")
		if !ok {
			fatal("rpcs: %q is not name=url", kv)
		}
		c, err := rpc.Dial(url)
		if err != nil {
			fatal("dial %s: %v", url, err)
		}
		nodes = append(nodes, &node{name: name, url: url, rpc: c, eth: ethclient.NewClient(c)})
	}
	if len(nodes) == 0 {
		fatal("no nodes")
	}
	reader := nodes[0]
	var senderNodes []*node
	for _, name := range strings.Split(*sendTo, ",") {
		for _, nd := range nodes {
			if nd.name == name {
				senderNodes = append(senderNodes, nd)
			}
		}
	}
	if len(senderNodes) == 0 {
		fatal("no sending nodes")
	}
	var depthList []uint64
	for _, d := range strings.Split(*depths, ",") {
		var v uint64
		fmt.Sscanf(d, "%d", &v)
		depthList = append(depthList, v)
	}
	ctx := context.Background()
	chainID, err := reader.eth.ChainID(ctx)
	if err != nil {
		fatal("chain id: %v", err)
	}
	var head dagHead
	if err := reader.rpc.CallContext(ctx, &head, "igneum_dagHead"); err != nil {
		fatal("igneum_dagHead: %v", err)
	}
	fmt.Printf("chain %d gate %d k %d mergeDepth %d head %d order %d tips %d\n", chainID, head.Gate, head.K, head.MergeDepth, head.Head.Number, head.Head.BlueScore, len(head.Tips))
	res := &result{Started: time.Now().UTC().Format(time.RFC3339), Params: map[string]any{"n": *n, "interval": interval.String(), "conflictEvery": *every, "senders": *senders, "depths": depthList, "k": head.K, "mergeDepth": head.MergeDepth, "chainId": chainID.String()}, Reorgs: map[string][]dagReorg{}, Arrivals: map[string]map[string]int64{}, Lanes: map[string]uint8{}, Metrics: map[string]map[string]any{}}
	for _, nd := range nodes {
		res.Nodes = append(res.Nodes, nd.name)
		res.Metrics[nd.name] = map[string]any{}
		var m map[string]any
		if err := nd.rpc.CallContext(ctx, &m, "debug_metrics", true); err == nil {
			res.Metrics[nd.name]["start"] = pick(m)
		}
	}
	signer := types.LatestSignerForChainID(chainID)

	// The senders, funded from the development faucet (public material,
	// params.IgneumDevFaucetKey) through the first sending node.
	faucet := params.IgneumDevFaucetKey()
	faucetAddr := crypto.PubkeyToAddress(faucet.PublicKey)
	keys := make([]*ecdsa.PrivateKey, *senders)
	addrs := make([]common.Address, *senders)
	for i := range keys {
		k, _ := crypto.GenerateKey()
		keys[i], addrs[i] = k, crypto.PubkeyToAddress(k.PublicKey)
	}
	nonce, err := senderNodes[0].eth.PendingNonceAt(ctx, faucetAddr)
	if err != nil {
		fatal("faucet nonce: %v", err)
	}
	gasPrice := big.NewInt(2_000_000_000)
	var fundTxs []common.Hash
	for i, a := range addrs {
		tx := types.MustSignNewTx(faucet, signer, &types.DynamicFeeTx{ChainID: chainID, Nonce: nonce + uint64(i), GasTipCap: gasPrice, GasFeeCap: new(big.Int).Mul(gasPrice, big.NewInt(4)), Gas: 21000, To: &a, Value: new(big.Int).Mul(big.NewInt(1e18), big.NewInt(10))})
		if err := senderNodes[0].eth.SendTransaction(ctx, tx); err != nil {
			fatal("fund %d: %v", i, err)
		}
		fundTxs = append(fundTxs, tx.Hash())
	}
	fmt.Printf("funding %d senders from %s, %d transactions, waiting for their acceptance on %s\n", *senders, faucetAddr.Hex(), len(fundTxs), reader.name)
	for {
		all := true
		for _, h := range fundTxs {
			var t dagTx
			if err := reader.rpc.CallContext(ctx, &t, "igneum_dagTx", h); err != nil || !t.Accepted {
				all = false
				break
			}
		}
		if all {
			break
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Printf("senders funded %s\n", time.Now().UTC().Format("15:04:05Z"))

	// The clocks.
	var (
		mu      sync.Mutex
		records []*txRecord
		byHash  = map[common.Hash]*txRecord{}
		stop    = make(chan struct{})
		wg      sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(*poll)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			mu.Lock()
			pending := make([]*txRecord, 0, len(records))
			for _, r := range records {
				if !r.done {
					pending = append(pending, r)
				}
			}
			mu.Unlock()
			if len(pending) == 0 {
				continue
			}
			batch := make([]rpc.BatchElem, len(pending))
			results := make([]dagTx, len(pending))
			for i, r := range pending {
				batch[i] = rpc.BatchElem{Method: "igneum_dagTx", Args: []any{r.Hash}, Result: &results[i]}
			}
			if err := reader.rpc.BatchCallContext(ctx, batch); err != nil {
				continue
			}
			now := time.Now().UnixMilli()
			mu.Lock()
			for i, r := range pending {
				if batch[i].Error != nil {
					continue
				}
				t := results[i]
				if r.CarriedMs == 0 && len(t.Carried) > 0 {
					r.CarriedMs = now
				}
				if t.Accepted {
					if r.AcceptedMs == 0 || t.Accepting != r.lastAccept {
						if r.AcceptedMs != 0 && t.Accepting != r.lastAccept {
							// The accepting block changed: a reversal for
							// every depth already reached.
							for d := range r.Confirmed {
								r.Reversals[d]++
							}
						}
						if r.AcceptedMs == 0 {
							r.AcceptedMs = now
						}
						r.lastAccept, r.Accepting, r.Order = t.Accepting, t.Accepting, uint64(t.AcceptingOrder)
					}
					depth := uint64(t.HeadOrder) - uint64(t.AcceptingOrder)
					all := true
					for _, d := range depthList {
						key := fmt.Sprint(d)
						if _, ok := r.Confirmed[key]; !ok {
							if depth >= d {
								r.Confirmed[key] = now
							} else {
								all = false
							}
						}
					}
					if all {
						r.done, r.Final = true, "settled"
					}
				} else if len(t.Refused) > 0 && r.AcceptedMs == 0 {
					r.Refused = true
				}
			}
			mu.Unlock()
		}
	}()
	// The head samples every 30 seconds.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			s := headSample{AtMs: time.Now().UnixMilli(), Heads: map[string]uint64{}, Orders: map[string]uint64{}, Tips: map[string]int{}, Hashes: map[string]string{}}
			for _, nd := range nodes {
				var h dagHead
				if err := nd.rpc.CallContext(ctx, &h, "igneum_dagHead"); err == nil {
					s.Heads[nd.name], s.Orders[nd.name], s.Tips[nd.name], s.Hashes[nd.name] = uint64(h.Head.Number), uint64(h.Head.BlueScore), len(h.Tips), h.Head.Hash.Hex()[:10]
				}
			}
			mu.Lock()
			res.Heads = append(res.Heads, s)
			mu.Unlock()
			fmt.Printf("%s heads %v orders %v tips %v\n", time.Now().UTC().Format("15:04:05Z"), s.Heads, s.Orders, s.Tips)
		}
	}()

	// The broadcasts.
	nonces := make([]uint64, *senders)
	for i, a := range addrs {
		nonces[i], _ = reader.eth.NonceAt(ctx, a, nil)
	}
	send := func(nd *node, tx *types.Transaction, r *txRecord) {
		r.BroadcastMs = time.Now().UnixMilli()
		if err := nd.eth.SendTransaction(ctx, tx); err != nil {
			r.Final = "send failed: " + err.Error()
			r.done = true
		}
	}
	fresh := func() common.Address {
		k, _ := crypto.GenerateKey()
		return crypto.PubkeyToAddress(k.PublicKey)
	}
	mk := func(i int, to common.Address, nonce uint64) *types.Transaction {
		return types.MustSignNewTx(keys[i], signer, &types.DynamicFeeTx{ChainID: chainID, Nonce: nonce, GasTipCap: gasPrice, GasFeeCap: new(big.Int).Mul(gasPrice, big.NewInt(4)), Gas: 21000, To: &to, Value: big.NewInt(1)})
	}
	conflicts := 0
	for i := 0; i < *n; i++ {
		s := i % *senders
		nd := senderNodes[i%len(senderNodes)]
		if *every > 0 && i > 0 && i%*every == 0 && len(senderNodes) > 1 {
			// A conflicting pair: one nonce, two recipients, two sealers at once.
			conflicts++
			id := fmt.Sprintf("pair-%d", conflicts)
			ta, tb := mk(s, fresh(), nonces[s]), mk(s, fresh(), nonces[s])
			nonces[s]++
			ra := &txRecord{Hash: ta.Hash(), Sender: s, Node: nd.name, Conflict: id, Confirmed: map[string]int64{}, Reversals: map[string]int{}}
			other := senderNodes[(i+1)%len(senderNodes)]
			rb := &txRecord{Hash: tb.Hash(), Sender: s, Node: other.name, Conflict: id, Confirmed: map[string]int64{}, Reversals: map[string]int{}}
			mu.Lock()
			records = append(records, ra, rb)
			byHash[ra.Hash], byHash[rb.Hash] = ra, rb
			mu.Unlock()
			var pw sync.WaitGroup
			pw.Add(2)
			go func() { defer pw.Done(); send(nd, ta, ra) }()
			go func() { defer pw.Done(); send(other, tb, rb) }()
			pw.Wait()
		} else {
			tx := mk(s, fresh(), nonces[s])
			nonces[s]++
			r := &txRecord{Hash: tx.Hash(), Sender: s, Node: nd.name, Confirmed: map[string]int64{}, Reversals: map[string]int{}}
			mu.Lock()
			records = append(records, r)
			byHash[r.Hash] = r
			mu.Unlock()
			send(nd, tx, r)
		}
		if (i+1)%50 == 0 {
			mu.Lock()
			fmt.Printf("%s sent %d (%d conflicting pairs)\n", time.Now().UTC().Format("15:04:05Z"), len(records), conflicts)
			mu.Unlock()
		}
		time.Sleep(*interval)
	}
	fmt.Printf("%s all %d broadcast; reading the clocks for %s (and until %d chain blocks)\n", time.Now().UTC().Format("15:04:05Z"), *n, wait, *minBlocks)
	deadline := time.Now().Add(*wait)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Second)
		if *minBlocks > 0 {
			if h, err := reader.eth.BlockNumber(ctx); err == nil && h < *minBlocks {
				deadline = time.Now().Add(time.Minute)
			}
		}
	}
	close(stop)
	wg.Wait()
	res.Ended = time.Now().UTC().Format(time.RFC3339)

	// The chain and the ordering on every node.
	for _, nd := range nodes {
		var m map[string]any
		if err := nd.rpc.CallContext(ctx, &m, "debug_metrics", true); err == nil {
			res.Metrics[nd.name]["end"] = pick(m)
		}
		var rs []dagReorg
		if err := nd.rpc.CallContext(ctx, &rs, "igneum_dagReorgs"); err == nil {
			res.Reorgs[nd.name] = rs
		}
	}
	chains := map[string][]*dagChainEntry{}
	minHead := ^uint64(0)
	for _, nd := range nodes {
		h, err := nd.eth.BlockNumber(ctx)
		if err != nil {
			continue
		}
		if h < minHead {
			minHead = h
		}
		var all []*dagChainEntry
		for from := uint64(0); from <= h; from += 2048 {
			var part []*dagChainEntry
			if err := nd.rpc.CallContext(ctx, &part, "igneum_dagChain", hexutil.Uint64(from), hexutil.Uint64(2048)); err != nil {
				fmt.Printf("%s igneum_dagChain: %v\n", nd.name, err)
				break
			}
			all = append(all, part...)
			if len(part) < 2048 {
				break
			}
		}
		chains[nd.name] = all
	}
	res.Chain = chains[reader.name]
	// The ordering equal at every order on every node, up to the lowest
	// head less the merge depth's margin (the heads differ by the blocks
	// in flight).
	cmp := uint64(0)
	if minHead > 16 {
		cmp = minHead - 16
	}
	equal, unequal := 0, 0
	var firstUnequal uint64
	for n := uint64(0); n <= cmp; n++ {
		var ref common.Hash
		ok := true
		for _, nd := range nodes {
			c := chains[nd.name]
			if uint64(len(c)) <= n {
				ok = false
				break
			}
			if nd == reader {
				ref = c[n].OrderingHash
			} else if c[n].OrderingHash != ref {
				ok = false
			}
		}
		if ok {
			equal++
		} else {
			if unequal == 0 {
				firstUnequal = n
			}
			unequal++
		}
	}
	// Blues, reds, lanes, the block rate.
	blues, reds := 0, 0
	laneCount := map[string]int{}
	blueByLane := map[string]int{}
	var blocks []common.Hash
	for _, e := range res.Chain {
		if e.Number == 0 {
			continue
		}
		blues += e.Blues
		reds += e.Reds
		for _, m := range e.Mergeset {
			if m != e.Mergeset[0] {
				blocks = append(blocks, m)
			}
		}
		blocks = append(blocks, e.Hash)
	}
	// Every chain block is the selected parent (a mergeset blue) of its
	// child, so the sum of blues over the chain counts each blue once.
	total := blues + reds
	var firstTime, lastTime uint64
	if len(res.Chain) > 1 {
		firstTime, lastTime = uint64(res.Chain[1].Time), uint64(res.Chain[len(res.Chain)-1].Time)
	}
	// The arrival clock of every block on every node (the propagation
	// delay per lane), in batches.
	uniq := map[common.Hash]bool{}
	var list []common.Hash
	for _, b := range blocks {
		if !uniq[b] {
			uniq[b] = true
			list = append(list, b)
		}
	}
	for _, nd := range nodes {
		for i := 0; i < len(list); i += 256 {
			j := i + 256
			if j > len(list) {
				j = len(list)
			}
			batch := make([]rpc.BatchElem, j-i)
			outs := make([]dagBlock, j-i)
			for k := range batch {
				batch[k] = rpc.BatchElem{Method: "igneum_dagBlock", Args: []any{list[i+k]}, Result: &outs[k]}
			}
			if err := nd.rpc.BatchCallContext(ctx, batch); err != nil {
				continue
			}
			for k := range batch {
				if batch[k].Error != nil || outs[k].ArrivalMs == nil {
					continue
				}
				key := list[i+k].Hex()
				if res.Arrivals[key] == nil {
					res.Arrivals[key] = map[string]int64{}
				}
				res.Arrivals[key][nd.name] = *outs[k].ArrivalMs
				if outs[k].Lane != nil {
					res.Lanes[key] = *outs[k].Lane
					laneCount[fmt.Sprint(*outs[k].Lane)]++
					if nd == reader && outs[k].Blue {
						blueByLane[fmt.Sprint(*outs[k].Lane)]++
					}
				}
			}
		}
	}
	laneD := map[string][]float64{}
	for b, arr := range res.Arrivals {
		if len(arr) < 2 {
			continue
		}
		lo, hi := int64(1<<62), int64(0)
		for _, t := range arr {
			if t < lo {
				lo = t
			}
			if t > hi {
				hi = t
			}
		}
		lane := fmt.Sprint(res.Lanes[b])
		laneD[lane] = append(laneD[lane], float64(hi-lo)/1000)
	}
	// Per-lane counts from the arrivals are per node; divide by the
	// node count of each block's record.
	for l := range laneCount {
		laneCount[l] = 0
	}
	for b, l := range res.Lanes {
		_ = b
		laneCount[fmt.Sprint(l)]++
	}
	// The clocks' statistics.
	stats := map[string]any{}
	var carried, accepted []float64
	conf := map[string][]float64{}
	reversals := map[string]int{}
	refused, unaccepted, pairsDecided, pairsBoth := 0, 0, 0, 0
	pairSeen := map[string]int{}
	mu.Lock()
	for _, r := range records {
		if r.Conflict != "" {
			if r.AcceptedMs != 0 {
				pairSeen[r.Conflict]++
			}
		}
		if r.CarriedMs != 0 {
			carried = append(carried, float64(r.CarriedMs-r.BroadcastMs)/1000)
		}
		if r.AcceptedMs != 0 {
			accepted = append(accepted, float64(r.AcceptedMs-r.BroadcastMs)/1000)
		} else if r.Refused {
			refused++
		} else {
			unaccepted++
		}
		for d, t := range r.Confirmed {
			conf[d] = append(conf[d], float64(t-r.BroadcastMs)/1000)
		}
		for d, c := range r.Reversals {
			reversals[d] += c
		}
	}
	for _, c := range pairSeen {
		if c == 1 {
			pairsDecided++
		} else if c == 2 {
			pairsBoth++
		}
	}
	res.Txs = records
	mu.Unlock()
	stats["sent"] = len(records)
	stats["conflictPairs"] = conflicts
	stats["pairsDecidedOneAccepted"] = pairsDecided
	stats["pairsBothAccepted"] = pairsBoth
	stats["refusedAtOrdering"] = refused
	stats["neverAccepted"] = unaccepted
	stats["carriedS"] = quantiles(carried)
	stats["acceptedS"] = quantiles(accepted)
	for d, v := range conf {
		stats["confirmed"+d+"S"] = quantiles(v)
	}
	stats["reversalsAfterDepth"] = reversals
	stats["orderingEqualOrders"] = equal
	stats["orderingUnequalOrders"] = unequal
	stats["orderingFirstUnequal"] = firstUnequal
	stats["orderingComparedUpTo"] = cmp
	stats["chainBlocks"] = len(res.Chain) - 1
	stats["blues"] = blues
	stats["reds"] = reds
	stats["blocks"] = total
	if total > 0 {
		stats["redFraction"] = float64(reds) / float64(total)
	}
	if lastTime > firstTime && total > 1 {
		stats["blockIntervalS"] = float64(lastTime-firstTime) / float64(total-1)
		stats["spanS"] = lastTime - firstTime
	}
	stats["lanes"] = laneCount
	stats["bluesByLane"] = blueByLane
	for l, v := range laneD {
		stats["delayLane"+l+"S"] = quantiles(v)
	}
	// Form B's credits per lane over the run (the reader's chain: the last
	// entry's Credits are the credited counts in the head's blue past), the
	// share of the credited work per lane, and the blue blocks of each lane
	// the credit passed over; per 150-order window the greatest share any
	// lane held (the record page's windowed bound).
	if n := len(res.Chain); n > 0 && len(res.Chain[n-1].Credits) > 0 {
		cr := res.Chain[n-1].Credits
		var total uint64
		for _, c := range cr {
			total += c
		}
		share := map[string]float64{}
		uncredited := map[string]int{}
		for l, c := range cr {
			if total > 0 {
				share[fmt.Sprint(l)] = float64(c) / float64(total)
			}
			uncredited[fmt.Sprint(l)] = blueByLane[fmt.Sprint(l)] - int(c)
		}
		stats["credits"] = cr
		stats["creditsTotal"] = total
		stats["creditShare"] = share
		stats["uncreditedByLane"] = uncredited
		const W = 150
		maxShare, maxLane, maxAt := 0.0, "", 0
		for i := 1; i+W < n; i++ {
			a, b := res.Chain[i].Credits, res.Chain[i+W].Credits
			if len(a) != len(b) {
				continue
			}
			var tw uint64
			for l := range a {
				tw += b[l] - a[l]
			}
			for l := range a {
				if tw > 0 {
					if s := float64(b[l]-a[l]) / float64(tw); s > maxShare {
						maxShare, maxLane, maxAt = s, fmt.Sprint(l), i
					}
				}
			}
		}
		stats["windowMaxShare"] = map[string]any{"window": W, "share": maxShare, "lane": maxLane, "fromChainBlock": maxAt}
	}
	reorgDepths := map[string]int{}
	maxDepth := uint64(0)
	for _, rs := range res.Reorgs {
		for _, r := range rs {
			reorgDepths[fmt.Sprint(r.Depth)]++
			if r.Depth > maxDepth {
				maxDepth = r.Depth
			}
		}
	}
	stats["reorgDepths"] = reorgDepths
	stats["reorgMaxDepth"] = maxDepth
	res.Summary = stats
	f, err := os.Create(*out)
	if err != nil {
		fatal("out: %v", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", " ")
	if err := enc.Encode(res); err != nil {
		fatal("write: %v", err)
	}
	f.Close()
	b, _ := json.MarshalIndent(stats, "", " ")
	fmt.Println(string(b))
}

func pick(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"p2p", "chain", "system"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

func quantiles(v []float64) map[string]any {
	if len(v) == 0 {
		return map[string]any{"n": 0}
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	q := func(p float64) float64 {
		i := int(p * float64(len(s)-1))
		return s[i]
	}
	sum := 0.0
	for _, x := range s {
		sum += x
	}
	return map[string]any{"n": len(s), "min": s[0], "p50": q(0.5), "p90": q(0.9), "p95": q(0.95), "p99": q(0.99), "max": s[len(s)-1], "mean": sum / float64(len(s))}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

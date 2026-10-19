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

// The ordering's readers (igneum_dag*): a block's colouring and position,
// the head and the tips, a transaction's fate in the ordering (the lab
// driver's confirmation clock reads it), the selected chain with each
// block's ordered mergeset and an ordering hash per order (equal on every
// node is the first evidence row of the ladder's step 0), the reorganisation
// record. Off the gate every call answers "the ordering is off".

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/igneum/dag"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

var errDAGRPCOff = errors.New("igneum dag: the ordering is off on this chain (the gate is zero)")

// DAGBlockInfo is igneum_dagBlock's answer.
type DAGBlockInfo struct {
	Hash           common.Hash    `json:"hash"`
	Number         hexutil.Uint64 `json:"number"`
	Lane           *uint8         `json:"lane,omitempty"`
	Parents        []common.Hash  `json:"parents"`
	SelectedParent common.Hash    `json:"selectedParent"`
	MergesetBlues  []common.Hash  `json:"mergesetBlues"`
	MergesetReds   []common.Hash  `json:"mergesetReds"`
	BlueScore      hexutil.Uint64 `json:"blueScore"`
	BlueWork       *hexutil.Big   `json:"blueWork"`
	// Credits is form B's credited count per lane in the block's blue past;
	// Credited marks each mergeset blue credited at this block.
	Credits  []uint64       `json:"credits,omitempty"`
	Credited []bool         `json:"credited,omitempty"`
	Depth    hexutil.Uint64 `json:"depth"`
	// OnChain: the block is on this node's selected chain now; Blue: in the
	// blue set of the head (the virtual block's view); Order: its blue
	// score when on the chain.
	OnChain   bool           `json:"onChain"`
	Blue      bool           `json:"blue"`
	ArrivalMs *int64         `json:"arrivalMs,omitempty"`
	Time      hexutil.Uint64 `json:"time"`
	Txs       int            `json:"txs"`
	Accepted  []dag.Accepted `json:"accepted,omitempty"`
}

func (api *IgneumAPI) dagOn() bool { return api.engine.DAGFrom() != 0 }

func (api *IgneumAPI) dagInfo(h common.Hash) (*DAGBlockInfo, error) {
	header := api.chain.GetHeaderByHash(h)
	if header == nil {
		return nil, errors.New("unknown block")
	}
	d, err := api.engine.dagDataOf(api.chain, h)
	if err != nil {
		return nil, err
	}
	head := api.chain.CurrentHeader()
	hd, err := api.engine.dagDataOf(api.chain, head.Hash())
	if err != nil {
		return nil, err
	}
	info := &DAGBlockInfo{
		Hash:           h,
		Number:         hexutil.Uint64(header.Number.Uint64()),
		Parents:        d.Parents,
		SelectedParent: d.SelectedParent,
		MergesetBlues:  d.MergesetBlues,
		MergesetReds:   d.MergesetReds,
		BlueScore:      hexutil.Uint64(d.BlueScore),
		BlueWork:       (*hexutil.Big)(new(big.Int).Set(d.BlueWork)),
		Credits:        d.Credits,
		Credited:       d.Credited,
		Depth:          hexutil.Uint64(d.Depth),
		Time:           hexutil.Uint64(header.Time),
		Blue:           dag.IsBlue(dagLookup{api.engine, api.chain}, hd, h),
		Accepted:       api.engine.dagAccepted(h),
	}
	if c, ok := header.CoordTag(); ok {
		info.Lane = &c
	}
	if canon := api.chain.GetHeaderByNumber(header.Number.Uint64()); canon != nil && canon.Hash() == h {
		info.OnChain = true
	}
	if t, ok := api.engine.dagArrival(h); ok {
		info.ArrivalMs = &t
	}
	if cr, ok := api.chain.(interface {
		GetBlockByHash(common.Hash) *types.Block
	}); ok {
		if b := cr.GetBlockByHash(h); b != nil {
			info.Txs = len(b.Transactions())
		}
	}
	return info, nil
}

// DagBlock is a block's colouring and position.
func (api *IgneumAPI) DagBlock(hash common.Hash) (*DAGBlockInfo, error) {
	if !api.dagOn() {
		return nil, errDAGRPCOff
	}
	return api.dagInfo(hash)
}

// DAGHeadInfo is igneum_dagHead's answer.
type DAGHeadInfo struct {
	Gate hexutil.Uint64 `json:"gate"`
	K    uint8          `json:"k"`
	// LaneSeconds is the gate entry's lane cadence (zero: the registration's).
	LaneSeconds uint64        `json:"laneSeconds"`
	MergeDepth  uint64        `json:"mergeDepth"`
	Head        *DAGBlockInfo `json:"head"`
	Tips        []common.Hash `json:"tips"`
}

// DagHead is the gate, the k, the head's data and the tips.
func (api *IgneumAPI) DagHead() (*DAGHeadInfo, error) {
	if !api.dagOn() {
		return nil, errDAGRPCOff
	}
	head, err := api.dagInfo(api.chain.CurrentHeader().Hash())
	if err != nil {
		return nil, err
	}
	return &DAGHeadInfo{Gate: hexutil.Uint64(api.engine.DAGFrom()), K: api.engine.DAGK(), LaneSeconds: api.engine.DAGLaneSeconds(), MergeDepth: DAGMergeDepth, Head: head, Tips: api.engine.dagTips()}, nil
}

// DAGTxInfo is igneum_dagTx's answer: where the transaction is carried and
// whether, and by which selected-chain block, it is accepted now.
type DAGTxInfo struct {
	Tx      common.Hash   `json:"tx"`
	Carried []common.Hash `json:"carried"`
	// Accepting is the selected-chain block at whose ordered position the
	// transaction executed (its own block when on the chain, else the chain
	// block that merged its block); zero while none. AcceptingOrder is that
	// block's blue score and HeadOrder the head's: the depth in blue score
	// above the acceptance is the difference.
	Accepting      common.Hash    `json:"accepting"`
	AcceptingOrder hexutil.Uint64 `json:"acceptingOrder"`
	HeadOrder      hexutil.Uint64 `json:"headOrder"`
	HeadHash       common.Hash    `json:"headHash"`
	Accepted       bool           `json:"accepted"`
	// Refused names the blocks whose ordered position refused it (a
	// conflict already applied before it), when any.
	Refused []common.Hash `json:"refused,omitempty"`
}

// DagTx is a transaction's fate in the ordering as this node holds it now.
func (api *IgneumAPI) DagTx(tx common.Hash) (*DAGTxInfo, error) {
	if !api.dagOn() {
		return nil, errDAGRPCOff
	}
	head := api.chain.CurrentHeader()
	hd, err := api.engine.dagDataOf(api.chain, head.Hash())
	if err != nil {
		return nil, err
	}
	info := &DAGTxInfo{Tx: tx, Carried: api.engine.dagCarried(tx), HeadOrder: hexutil.Uint64(hd.BlueScore), HeadHash: head.Hash()}
	// Walk the selected chain from the head down to the first chain block
	// whose record names the transaction (the records are per block; a
	// carried transaction's chain block is one of its carriers' merging
	// blocks, within the merge depth of the carrier).
	if len(info.Carried) == 0 {
		return info, nil
	}
	lowest := ^uint64(0)
	for _, c := range info.Carried {
		if h := api.chain.GetHeaderByHash(c); h != nil && h.Number.Uint64() < lowest {
			lowest = h.Number.Uint64()
		}
	}
	cur := head
	for cur != nil && cur.Number.Uint64()+DAGMergeDepth+16 >= lowest {
		for _, r := range api.engine.dagAccepted(cur.Hash()) {
			if r.Tx != tx {
				continue
			}
			if r.Accepted {
				d, err := api.engine.dagDataOf(api.chain, cur.Hash())
				if err != nil {
					return nil, err
				}
				info.Accepting, info.AcceptingOrder, info.Accepted = cur.Hash(), hexutil.Uint64(d.BlueScore), true
				return info, nil
			}
			info.Refused = append(info.Refused, cur.Hash())
		}
		if cur.Number.Sign() == 0 {
			break
		}
		cur = api.chain.GetHeader(cur.ParentHash, cur.Number.Uint64()-1)
	}
	return info, nil
}

// DAGChainEntry is one selected-chain block of igneum_dagChain: its order
// (blue score), its ordered mergeset and the ordering hash at that order
// (keccak over the ordered mergeset's hashes after the block's own), the
// figure every node must agree on.
type DAGChainEntry struct {
	Hash         common.Hash    `json:"hash"`
	Number       hexutil.Uint64 `json:"number"`
	Order        hexutil.Uint64 `json:"order"`
	Lane         *uint8         `json:"lane,omitempty"`
	Mergeset     []common.Hash  `json:"mergeset"`
	Blues        int            `json:"blues"`
	Reds         int            `json:"reds"`
	OrderingHash common.Hash    `json:"orderingHash"`
	Time         hexutil.Uint64 `json:"time"`
	// Credits is form B's credited count per lane after this block's
	// mergeset; Credited marks the mergeset blues (in colouring order, the
	// selected parent first) credited here.
	Credits  []uint64     `json:"credits,omitempty"`
	Credited []bool       `json:"credited,omitempty"`
	BlueWork *hexutil.Big `json:"blueWork"`
}

// DagChain is the selected chain from the given height (the chain's
// number) for count blocks, each with its ordered mergeset.
func (api *IgneumAPI) DagChain(from hexutil.Uint64, count hexutil.Uint64) ([]*DAGChainEntry, error) {
	if !api.dagOn() {
		return nil, errDAGRPCOff
	}
	if count > 4096 {
		count = 4096
	}
	l := dagLookup{api.engine, api.chain}
	var out []*DAGChainEntry
	for n := uint64(from); n < uint64(from)+uint64(count); n++ {
		h := api.chain.GetHeaderByNumber(n)
		if h == nil {
			break
		}
		d, err := api.engine.dagDataOf(api.chain, h.Hash())
		if err != nil {
			return nil, err
		}
		entry := &DAGChainEntry{Hash: h.Hash(), Number: hexutil.Uint64(n), Order: hexutil.Uint64(d.BlueScore), Time: hexutil.Uint64(h.Time), Credits: d.Credits, Credited: d.Credited, BlueWork: (*hexutil.Big)(new(big.Int).Set(d.BlueWork))}
		if c, ok := h.CoordTag(); ok {
			entry.Lane = &c
		}
		if d.Depth > 0 {
			entry.Mergeset = dag.OrderedMergeset(l, d)
			entry.Blues, entry.Reds = len(d.MergesetBlues), len(d.MergesetReds)
		}
		var buf []byte
		buf = append(buf, h.Hash().Bytes()...)
		for _, m := range entry.Mergeset {
			buf = append(buf, m.Bytes()...)
		}
		entry.OrderingHash = crypto.Keccak256Hash(buf)
		out = append(out, entry)
	}
	return out, nil
}

// DagReorgs is the record of the selected chain's moves to a block that was
// not the old head's child.
func (api *IgneumAPI) DagReorgs() ([]DAGReorg, error) {
	if !api.dagOn() {
		return nil, errDAGRPCOff
	}
	return api.engine.dagReorgs(), nil
}

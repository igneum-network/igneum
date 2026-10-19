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

package core

// The state under parallel blocks (docs/money/ORDERING-DESIGN-2026-10-18.md
// section 3, the EVM row, Kaspa's form): a block commits to the state after
// executing, in the ordering, its whole mergeset and then its own
// transactions, so the state root is a function of the block's past alone.
// ApplyMergeset is that first part, the same function in the state
// processor (import) and the miner (assembly): the mergeset's blocks in the
// ordering's order after the selected parent, each block's transactions in
// their order, each applied against the carrying block's base fee (its own
// market; the burn is that block's) with the tip to the MERGING block's
// coinbase; a transaction that fails its pre-check at its ordered position
// (a nonce already used, a balance gone, a fee cap under the base fee) is
// NOT accepted, its state untouched, and the block stays valid. Gas is the
// carrying block's limit, not the merging block's: the merging block's
// GasUsed counts its own transactions alone, as its header says.

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/dag"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// dagEngine is the engine under the ordering.
type dagEngine interface {
	DAGOn(number uint64) bool
	DAGMergeset(chain consensus.ChainHeaderReader, header *types.Header) ([]common.Hash, error)
	NoteDAGAccepted(block common.Hash, recs []dag.Accepted)
}

// dagChain is what the mergeset's application reads: headers and bodies by hash.
type dagChain interface {
	ChainContext
	consensus.ChainHeaderReader
	GetBlockByHash(common.Hash) *types.Block
}

// DAGOn reports whether the engine runs the ordering at the height.
func DAGOn(engine consensus.Engine, number uint64) bool {
	de, ok := engine.(dagEngine)
	return ok && de.DAGOn(number)
}

// ApplyMergeset applies the header's mergeset to the state (the selected
// parent's state as given) and returns the acceptance record of the
// mergeset's transactions. An unknown mergeset block is an error (the
// block cannot be processed); a transaction's refusal is not.
func ApplyMergeset(config ctypes.ChainConfigurator, chain dagChain, engine consensus.Engine, header *types.Header, statedb *state.StateDB, cfg vm.Config) ([]dag.Accepted, error) {
	de, ok := engine.(dagEngine)
	if !ok || !de.DAGOn(header.Number.Uint64()) {
		return nil, nil
	}
	mergeset, err := de.DAGMergeset(chain, header)
	if err != nil {
		return nil, err
	}
	var (
		recs   []dag.Accepted
		signer = types.MakeSigner(config, header.Number, header.Time)
		index  = 0
	)
	for _, mh := range mergeset {
		mb := chain.GetBlockByHash(mh)
		if mb == nil {
			return nil, fmt.Errorf("igneum dag: mergeset block %x unknown", mh[:4])
		}
		if len(mb.Transactions()) == 0 {
			continue
		}
		context := NewEVMBlockContext(header, chain, &header.Coinbase)
		if mb.BaseFee() != nil {
			context.BaseFee = mb.BaseFee()
		}
		vmenv := vm.NewEVM(context, vm.TxContext{}, statedb, config, cfg)
		gp := new(GasPool).AddGas(mb.GasLimit())
		for _, tx := range mb.Transactions() {
			rec := dag.Accepted{Tx: tx.Hash(), Block: mh}
			msg, err := TransactionToMessage(tx, signer, context.BaseFee)
			if err != nil {
				rec.Reason = err.Error()
				recs = append(recs, rec)
				continue
			}
			snap := statedb.Snapshot()
			gas := gp.Gas()
			statedb.SetTxContext(tx.Hash(), index)
			var used uint64
			if _, err := applyTransaction(msg, config, gp, statedb, header.Number, common.Hash{}, tx, &used, vmenv); err != nil {
				statedb.RevertToSnapshot(snap)
				gp.SetGas(gas)
				rec.Reason = err.Error()
			} else {
				rec.Accepted = true
				index++
			}
			recs = append(recs, rec)
		}
	}
	return recs, nil
}

// OwnAccepted appends a block's own transactions to its record: every own
// transaction executed (the block would be invalid otherwise).
func OwnAccepted(recs []dag.Accepted, block common.Hash, txs types.Transactions) []dag.Accepted {
	for _, tx := range txs {
		recs = append(recs, dag.Accepted{Tx: tx.Hash(), Block: block, Accepted: true})
	}
	return recs
}

// NoteDAGAccepted stores a block's record with the engine.
func NoteDAGAccepted(engine consensus.Engine, block common.Hash, recs []dag.Accepted) {
	if de, ok := engine.(dagEngine); ok {
		de.NoteDAGAccepted(block, recs)
	}
}

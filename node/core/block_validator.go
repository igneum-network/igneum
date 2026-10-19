// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
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

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/ethereum/go-ethereum/trie"
)

// BlockValidator is responsible for validating block headers, uncles and
// processed state.
//
// BlockValidator implements Validator.
type BlockValidator struct {
	config ctypes.ChainConfigurator // Chain configuration options
	bc     *BlockChain              // Canonical block chain
	engine consensus.Engine         // Consensus engine used for validating
}

// NewBlockValidator returns a new block validator which is safe for re-use
func NewBlockValidator(config ctypes.ChainConfigurator, blockchain *BlockChain, engine consensus.Engine) *BlockValidator {
	validator := &BlockValidator{
		config: config,
		engine: engine,
		bc:     blockchain,
	}
	return validator
}

// ValidateBody validates the given block's uncles and verifies the block
// header's transaction and uncle roots. The headers are assumed to be already
// validated at this point.
func (v *BlockValidator) ValidateBody(block *types.Block) error {
	// Check whether the block is already imported.
	if v.bc.HasBlockAndState(block.Hash(), block.NumberU64()) {
		return ErrKnownBlock
	}

	// Header validity is known at this point. Here we verify that uncles, transactions
	// and withdrawals given in the block body match the header.
	header := block.Header()
	if err := v.engine.VerifyUncles(v.bc, block); err != nil {
		return err
	}
	if hash := types.CalcUncleHash(block.Uncles()); hash != header.UncleHash {
		return fmt.Errorf("uncle root hash mismatch (header value %x, calculated %x)", header.UncleHash, hash)
	}
	if hash := types.DeriveSha(block.Transactions(), trie.NewStackTrie(nil)); hash != header.TxHash {
		return fmt.Errorf("transaction root hash mismatch (header value %x, calculated %x)", header.TxHash, hash)
	}

	// Withdrawals are present after the Shanghai fork.
	if header.WithdrawalsHash != nil {
		// The list must be present in the body under a root; an absent list
		// is the empty list (core/types Block.DecodeRLP's note), so only a
		// root that is not the empty list's reads "missing".
		if len(block.Withdrawals()) == 0 {
			if *header.WithdrawalsHash != types.EmptyWithdrawalsHash {
				return errors.New("missing withdrawals in block body")
			}
		} else if hash := types.DeriveSha(block.Withdrawals(), trie.NewStackTrie(nil)); hash != *header.WithdrawalsHash {
			return fmt.Errorf("withdrawals root hash mismatch (header value %x, calculated %x)", *header.WithdrawalsHash, hash)
		}
	} else if len(block.Withdrawals()) > 0 {
		// A list without a root (Igneum's payout list is never present
		// without one).
		return errors.New("withdrawals present in block body")
	}
	// The keyed seal's proof (SPEC-R0A-v0.md 0.9.1): the body carries exactly
	// the bytes the header's PiHash commits to, and none without one.
	if header.PiHash != nil {
		if len(block.Pi()) == 0 {
			return errors.New("missing proof in block body")
		}
		if hash := types.PiHash(block.Pi()); hash != *header.PiHash {
			return fmt.Errorf("proof commitment mismatch (header value %x, calculated %x)", *header.PiHash, hash)
		}
	} else if len(block.Pi()) > 0 {
		return errors.New("proof present in block body without a commitment")
	}
	// The parent's endorsements (the settlement rule): the body carries
	// exactly the entries the header commits to, counted as the header says.
	if header.EndorsementsHash != nil {
		n := types.EndorsementCount(block.Endorsements())
		if n <= 0 {
			return errors.New("missing or malformed endorsements in block body")
		}
		if hash := types.EndorsementsHash(block.Endorsements()); hash != *header.EndorsementsHash {
			return fmt.Errorf("endorsements commitment mismatch (header value %x, calculated %x)", *header.EndorsementsHash, hash)
		}
		if uint64(n) != header.Endorsed {
			return fmt.Errorf("endorsement count mismatch (header says %d, body carries %d)", header.Endorsed, n)
		}
	} else if len(block.Endorsements()) > 0 || header.Endorsed != 0 {
		return errors.New("endorsements present without a commitment")
	}
	// The share lane's carried shares (LAUNCH-GUARDS.md 2.9): the body
	// carries exactly the entries the header commits to, none without one.
	if err := validateSharesBody(v.engine, header, block.MinedShares()); err != nil {
		return err
	}
	// The proof's verification under --igneum.pi-verify (consensus/igneum
	// pi.go): a keyed seal's proof against the key, the walk's messages and
	// the shares; a refusal is a bad block at the proof's own code.
	if pv, ok := v.engine.(interface{ VerifyPi(*types.Block) error }); ok {
		if err := pv.VerifyPi(block); err != nil {
			return err
		}
	}

	// Ancestor block must be known.
	if !v.bc.HasBlockAndState(block.ParentHash(), block.NumberU64()-1) {
		if !v.bc.HasBlock(block.ParentHash(), block.NumberU64()-1) {
			return consensus.ErrUnknownAncestor
		}
		return consensus.ErrPrunedAncestor
	}
	return nil
}

// ValidateState validates the various changes that happen after a state transition,
// such as amount of used gas, the receipt roots and the state root itself.
func (v *BlockValidator) ValidateState(block *types.Block, statedb *state.StateDB, receipts types.Receipts, usedGas uint64) error {
	header := block.Header()
	if block.GasUsed() != usedGas {
		return fmt.Errorf("invalid gas used (remote: %d local: %d)", block.GasUsed(), usedGas)
	}
	// Validate the received block's bloom with the one derived from the generated receipts.
	// For valid blocks this should always validate to true.
	rbloom := types.CreateBloom(receipts)
	if rbloom != header.Bloom {
		return fmt.Errorf("invalid bloom (remote: %x  local: %x)", header.Bloom, rbloom)
	}
	// Tre receipt Trie's root (R = (Tr [[H1, R1], ... [Hn, Rn]]))
	receiptSha := types.DeriveSha(receipts, trie.NewStackTrie(nil))
	if receiptSha != header.ReceiptHash {
		return fmt.Errorf("invalid receipt root hash (remote: %x local: %x)", header.ReceiptHash, receiptSha)
	}
	// Validate the state root against the received state root and throw
	if root := statedb.IntermediateRoot(v.config.IsEnabled(v.config.GetEIP161dTransition, header.Number)); header.Root != root {
		return fmt.Errorf("invalid merkle root (remote: %x local: %x) dberr: %w", header.Root, root, statedb.Error())
	}
	return nil
}

// CalcGasLimit computes the gas limit of the next block after parent. It aims
// to keep the baseline gas close to the provided target, and increase it towards
// the target if the baseline gas is lower.
func CalcGasLimit(parentGasLimit, desiredLimit uint64) uint64 {
	delta := parentGasLimit/vars.GasLimitBoundDivisor - 1
	limit := parentGasLimit
	if desiredLimit < vars.MinGasLimit {
		desiredLimit = vars.MinGasLimit
	}
	// If we're outside our allowed gas range, we try to hone towards them
	if limit < desiredLimit {
		limit = parentGasLimit + delta
		if limit > desiredLimit {
			limit = desiredLimit
		}
		return limit
	}
	if limit > desiredLimit {
		limit = parentGasLimit - delta
		if limit < desiredLimit {
			limit = desiredLimit
		}
	}
	return limit
}

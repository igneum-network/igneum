// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// FirstPass says whether a record is applied in a block's first pass
// (F-801, the head race: every head-consuming statement names the head's
// nullifier, so an announcement proven against head_k was refused whenever
// the thief's one-wei request against head_k stood earlier in the block,
// every block while its allowance lasted). The records that protect a
// vault (an announcement, a recovery cancellation, a withdrawal's
// cancellation) are applied before the records that move value out of it
// (a withdrawal, a completion) and before everything else, in transaction
// order within each pass, so the announcement takes the head whatever the
// transaction order and a cancellation beats a completion of one pending
// note in one block. The order is public and the same on every node;
// Extract (the retained set, igneum_poolBlock) walks the same order, so the
// tree positions agree with Finalize's appends.
func FirstPass(data []byte) bool {
	if len(data) == 0 || !IsVaultTag(data[0]) {
		return false
	}
	return data[0] == VaultAnnounceTag || data[0] == VaultRecCancelTag || data[0] == VaultCancelTag
}

// Ordered is a block's transactions to the pool address in the order the
// pool applies them: the first-pass records, then the rest, each in
// transaction order. Transactions to any other address are left out.
func Ordered(txs []*types.Transaction, addr common.Address) []*types.Transaction {
	var first, rest []*types.Transaction
	for _, tx := range txs {
		if tx.To() == nil || *tx.To() != addr {
			continue
		}
		if FirstPass(tx.Data()) {
			first = append(first, tx)
		} else {
			rest = append(rest, tx)
		}
	}
	return append(first, rest...)
}

package igneum

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// The global withdrawal index (ctypes.IgneumRuleForks.WithdrawalIndexV1;
// ecosystem's F-87, 13 October 2026). EIP-4895's index is one counter
// across the chain; the node's payout lists restarted it at zero every
// block, so an explorer keying withdrawals by index refused every chunk of
// more than one block. From the gate height, entry i of a block's list is
// indexed C(parent) + i, where C(h) is the number of withdrawals in blocks
// 1 to h, carried in the registry's state at withdrawalCountSlot and
// advanced by Finalize; the verifier checks the indices against the
// parent's count. Below the gate the per-block index stands.

var withdrawalCountSlot = crypto.Keccak256Hash([]byte("IgneumHash/R0A/withdrawal-count"))

// withdrawalIndexOn reports whether the global index is in force at a height.
func (e *Engine) withdrawalIndexOn(height uint64) bool {
	e.lock.Lock()
	f := e.forks
	e.lock.Unlock()
	return f != nil && f.WithdrawalIndexV1 != 0 && height >= f.WithdrawalIndexV1
}

// WithdrawalCount is C at a state (the registry's slot).
func (e *Engine) WithdrawalCount(st *state.StateDB) uint64 {
	return st.GetState(e.config.Parts.Authority.Registry, withdrawalCountSlot).Big().Uint64()
}

// setWithdrawalCount writes C.
func (e *Engine) setWithdrawalCount(st *state.StateDB, c uint64) {
	st.SetState(e.config.Parts.Authority.Registry, withdrawalCountSlot, common.BigToHash(new(big.Int).SetUint64(c)))
}

// indexWithdrawals writes the global indices onto a template's list from
// the parent's count; below the gate the list is left as built.
func (e *Engine) indexWithdrawals(st *state.StateDB, height uint64, list []*types.Withdrawal) {
	if !e.withdrawalIndexOn(height) {
		return
	}
	base := e.WithdrawalCount(st)
	for i, w := range list {
		w.Index = base + uint64(i)
	}
}

// verifyWithdrawalIndices checks a block's indices under the landing check.
func (e *Engine) verifyWithdrawalIndices(st *state.StateDB, height uint64, list []*types.Withdrawal) error {
	if !e.withdrawalIndexOn(height) {
		return nil
	}
	base := e.WithdrawalCount(st)
	for i, w := range list {
		if w.Index != base+uint64(i) {
			return fmt.Errorf("withdrawal %d indexed %d, want %d (the chain's count %d at the parent)", i, w.Index, base+uint64(i), base)
		}
	}
	return nil
}

// advanceWithdrawalCount adds a block's withdrawals to C (Finalize, under
// the gate).
func (e *Engine) advanceWithdrawalCount(st *state.StateDB, height uint64, n int) {
	if !e.withdrawalIndexOn(height) || n == 0 {
		return
	}
	e.setWithdrawalCount(st, e.WithdrawalCount(st)+uint64(n))
}

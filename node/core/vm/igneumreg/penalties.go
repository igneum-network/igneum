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

package igneumreg

// The four penalty and exit rules of LAUNCH-GUARDS.md section 2.7 (the
// frozen forms; the project's operation forms of 12:2x UK, 11 October 2026), at
// registry version 4, each a deterministic operation every node executes
// the same way as a transaction's call:
//
//   - 0x10 EQUIVOCATION: payload RLP([headerA, headerB]); valid iff one
//     height, one bond key, two seal hashes, each signature valid over its
//     signing hash under the key's registered signer (the key's own bytes
//     when none is registered: the open-seat and liveness forms) at the
//     CURRENT state, the key bonded. Penalty: the WHOLE bond forfeited
//     (F-40, the project's ruling of 18:4x UK: full bond exposure, the drafts'
//     "one half" superseded); one block's reward (Context.Reward: the
//     miner's share of the subsidy at this height) of it to the
//     transaction's origin, the reporter, and the rest BURNT (it leaves
//     the registry and reaches no account; SlotBurnt counts it);
//     the key marked (it seals nothing from the next block, the validity
//     rule's refusal; it draws nothing; it bonds nothing again) and out of
//     the key list, so the next table omits it. A second report of the
//     pair finds the key unbonded and reverts.
//   - 0x11 ENDORSEMENT EQUIVOCATION: payload LE64(height) || hashA ||
//     hashB || entryA || entryB, the entries key (32) || LE32(unit) ||
//     signature (64) of the settlement rule; valid iff two hashes, one
//     (key, unit), each signature valid over the endorsement message of
//     its hash under the key's signer, the key bonded. The same penalty.
//   - 0x12 RELEASE: payload key; by the bond's account (the origin), at or
//     after the lock height (the post height plus Context.LockBlocks);
//     pays the whole balance to the exit address and unbonds the key (out
//     of the list). Before the lock it reverts.
//   - 0x13 CLAIM: payload pk (32) || to (32, the address right-aligned,
//     the twelve leading bytes zero) || s_0 (32) || pi (1 to 2,048 bytes,
//     the Groth16 wrap under the pinned image id); pow/pi's verifier
//     checks pi under domain 1 against pk, m_0 = ClaimMessage(chain id,
//     pk, nonce_b, to) and s_0, nonce_b the key's claim counter, which the
//     accepted claim then increments (a reverted call writes nothing, so
//     the counter counts accepted claims; a replay of an accepted proof
//     finds the key unbonded or the counter moved). On OK the whole bond
//     leaves at once: to equal to the registered exit address is the
//     holder's release by proof and needs the lock expired (the lock is
//     not bypassed by the secret); any other to pays three quarters to it
//     and BURNS the quarter phi; the key leaves the list.
//
// Nothing here pays the maintenance fund (LAUNCH-GUARDS 2.7: it takes
// nothing from any penalty). The node pays block rewards at each block, so
// a penalised key has no pending reward to burn; the rule's "pending
// rewards burnt" is satisfied by construction and stated.

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/pi"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

// The operation selectors.
const (
	OpEquivocation        byte = 0x10
	OpEndorseEquivocation byte = 0x11
	OpRelease             byte = 0x12
	OpClaim               byte = 0x13
)

// The lab's gas figures per operation: two Ed25519 checks and an RLP
// decode for an equivocation; the Groth16 verification (about 4 ms on the
// reference CPU, PI-NOTES.md) for a claim, priced so a block of refused
// claims costs its sender more than the verifier's time costs the node.
const (
	GasEquivocation uint64 = 100_000
	GasClaim        uint64 = 500_000
)

// EndorsementEntryBytes is one endorsement entry: key (32) || LE32(unit)
// || Ed25519 signature (64) (core/types/endorse.go's form on the protocol team's
// branch; the constant is repeated here so the registry does not depend on
// that file).
const EndorsementEntryBytes = 32 + 4 + 64

// GasOf is the flat gas of a call with this calldata.
func GasOf(input []byte) uint64 {
	switch op, ok := selectorOf(input); {
	case !ok:
		return Gas
	case op == OpClaim:
		return GasClaim
	case op == OpEquivocation || op == OpEndorseEquivocation:
		return GasEquivocation
	}
	return Gas
}

func runPenalty(ctx Context, st StateDB, self, caller, origin common.Address, op byte, payload []byte) error {
	switch op {
	case OpEquivocation:
		return equivocation(ctx, st, self, origin, payload)
	case OpEndorseEquivocation:
		return endorseEquivocation(ctx, st, self, origin, payload)
	case OpRelease:
		if len(payload) != 32 {
			return ErrCalldata
		}
		return release(ctx, st, self, caller, origin, common.BytesToHash(payload))
	case OpClaim:
		return claimWithProof(ctx, st, self, payload)
	}
	return ErrCalldata
}

// signerOf is the key the registry checks a block's or an endorsement's
// signature under: the registered signer, else the key itself.
func signerOf(st StateDB, self common.Address, key common.Hash) ed25519.PublicKey {
	if s := st.GetState(self, SignerSlot(key)); s != (common.Hash{}) {
		return ed25519.PublicKey(s[:])
	}
	return ed25519.PublicKey(key[:])
}

func equivocation(ctx Context, st StateDB, self, origin common.Address, payload []byte) error {
	var pair []*types.Header
	if err := rlp.DecodeBytes(payload, &pair); err != nil || len(pair) != 2 || pair[0] == nil || pair[1] == nil {
		return ErrEvidence
	}
	a, b := pair[0], pair[1]
	if a.Number == nil || b.Number == nil || a.Number.Sign() <= 0 || a.Number.Cmp(b.Number) != 0 {
		return ErrSameHeight
	}
	if a.BondKey == nil || b.BondKey == nil || *a.BondKey != *b.BondKey {
		return ErrSameKey
	}
	if len(a.Sig) != ed25519.SignatureSize || len(b.Sig) != ed25519.SignatureSize {
		return ErrEvidence
	}
	if SealHash(a) == SealHash(b) {
		return ErrSameSeal
	}
	key := *a.BondKey
	if st.GetState(self, AmountSlot(key)).Big().Sign() == 0 {
		return ErrUnbonded
	}
	signer := signerOf(st, self, key)
	ha, hb := SigningHash(a), SigningHash(b)
	if !ed25519.Verify(signer, ha[:], a.Sig) || !ed25519.Verify(signer, hb[:], b.Sig) {
		return ErrBadSig
	}
	penalise(ctx, st, self, origin, key)
	return nil
}

func endorseEquivocation(ctx Context, st StateDB, self, origin common.Address, payload []byte) error {
	if len(payload) != 8+32+32+2*EndorsementEntryBytes {
		return ErrCalldata
	}
	height := binary.LittleEndian.Uint64(payload[:8])
	hashA := common.BytesToHash(payload[8:40])
	hashB := common.BytesToHash(payload[40:72])
	entryA := payload[72 : 72+EndorsementEntryBytes]
	entryB := payload[72+EndorsementEntryBytes:]
	if hashA == hashB {
		return ErrSameEntry
	}
	key := common.BytesToHash(entryA[:32])
	if common.BytesToHash(entryB[:32]) != key || binary.LittleEndian.Uint32(entryA[32:36]) != binary.LittleEndian.Uint32(entryB[32:36]) {
		return ErrSameEntry
	}
	if st.GetState(self, AmountSlot(key)).Big().Sign() == 0 {
		return ErrUnbonded
	}
	signer := signerOf(st, self, key)
	if !ed25519.Verify(signer, EndorseMessage(ctx.ChainID, height, hashA), entryA[36:]) ||
		!ed25519.Verify(signer, EndorseMessage(ctx.ChainID, height, hashB), entryB[36:]) {
		return ErrBadSig
	}
	penalise(ctx, st, self, origin, key)
	return nil
}

// penalise applies the equivocation penalty to a bonded key: the WHOLE
// bond forfeited (the outside review's bar is full bond exposure; security's
// F-40 and the project's ruling of 18:4x UK, 11 October 2026: the code's form is
// the rule, the drafts' "one half" superseded), one block's reward of it
// to the reporter (the whole bond when the bond is under one reward), the
// rest burnt.
func penalise(ctx Context, st StateDB, self, reporter common.Address, key common.Hash) {
	amount := st.GetState(self, AmountSlot(key)).Big()
	reward := new(big.Int)
	if ctx.Reward != nil && ctx.Reward.Sign() > 0 {
		reward.Set(ctx.Reward)
	}
	if reward.Cmp(amount) > 0 {
		reward.Set(amount)
	}
	burnt := new(big.Int).Sub(amount, reward)
	st.SetState(self, AmountSlot(key), common.Hash{})
	st.SetState(self, MarkedSlot(key), common.BigToHash(big.NewInt(1)))
	removeKey(st, self, key)
	burn(st, self, burnt)
	transfer(st, self, reporter, reward)
}

// burn removes an amount from the registry's balance and credits it to no
// address; SlotBurnt counts the supply removed so far.
func burn(st StateDB, self common.Address, amount *big.Int) {
	v, overflow := uint256.FromBig(amount)
	if overflow || v.IsZero() {
		return
	}
	st.SubBalance(self, v)
	total := new(big.Int).Add(st.GetState(self, SlotBurnt).Big(), amount)
	st.SetState(self, SlotBurnt, common.BigToHash(total))
}

func release(ctx Context, st StateDB, self, caller, origin common.Address, key common.Hash) error {
	if caller != origin {
		return ErrOrigin
	}
	amount := st.GetState(self, AmountSlot(key)).Big()
	if amount.Sign() == 0 {
		return ErrUnbonded
	}
	if common.BytesToAddress(st.GetState(self, AccountSlot(key)).Bytes()) != caller {
		return ErrAccount
	}
	if ctx.Number < LockHeight(ctx, st, self, key) {
		return ErrLocked
	}
	exit := common.BytesToAddress(st.GetState(self, ExitSlot(key)).Bytes())
	st.SetState(self, AmountSlot(key), common.Hash{})
	removeKey(st, self, key)
	transfer(st, self, exit, amount)
	return nil
}

func claimWithProof(ctx Context, st StateDB, self common.Address, payload []byte) error {
	if len(payload) < 96+1 || len(payload) > 96+pi.MaxBytesClaim {
		return ErrCalldata
	}
	pk := common.BytesToHash(payload[:32])
	for _, b := range payload[32:44] {
		if b != 0 {
			return ErrClaimTo
		}
	}
	to := common.BytesToAddress(payload[44:64])
	var s0 [32]byte
	copy(s0[:], payload[64:96])
	proof := payload[96:]
	amount := st.GetState(self, AmountSlot(pk)).Big()
	if amount.Sign() == 0 {
		return ErrUnbonded
	}
	exit := common.BytesToAddress(st.GetState(self, ExitSlot(pk)).Bytes())
	if to == exit && ctx.Number < LockHeight(ctx, st, self, pk) {
		// The cheap refusal before the verifier runs.
		return ErrLocked
	}
	nonce := st.GetState(self, ClaimNonceSlot(pk)).Big().Uint64()
	m0 := ClaimMessage(ctx.ChainID, pk, nonce, to)
	if code := pi.Verify(pi.DomainClaim, pk, [][32]byte{m0}, [][32]byte{s0}, proof); code != pi.OK {
		return fmt.Errorf("%w: %s", ErrClaimProof, pi.CodeName(code))
	}
	st.SetState(self, ClaimNonceSlot(pk), common.BigToHash(new(big.Int).SetUint64(nonce+1)))
	pay := new(big.Int).Set(amount)
	if to != exit {
		quarter := new(big.Int).Mul(amount, big.NewInt(PhiBps))
		quarter.Div(quarter, big.NewInt(10000))
		pay.Sub(pay, quarter)
		burn(st, self, quarter)
	}
	st.SetState(self, AmountSlot(pk), common.Hash{})
	removeKey(st, self, pk)
	transfer(st, self, to, pay)
	return nil
}

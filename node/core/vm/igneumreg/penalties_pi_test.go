//go:build ignpi

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

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/pi"
)

// The hostile claim on the real verifier, with the hash team's claim rows
// as the vectors (pow/vectors/pi/<set>/pi.json rows claim, claim_wrong_to,
// claim_composite; the landed K 2 set): chain 7315, nonce 0, to = 0x11 x
// 20, pk 9e5f..., s_0 c63e..., the Groth16 wrap receipts/claim-0.bin (700
// bytes) under the pinned image id.

const claimSet = "../../../../pow/vectors/pi/v8-d2048-s0-r64-k32-p256-w128-b16-x16-f1056-k2"

func claimVector(t *testing.T) (pk common.Hash, to common.Address, s0 [32]byte, proof, composite []byte) {
	t.Helper()
	pk = common.HexToHash("9e5f6270f0506ea38c26cf765b3775cf1a6c989eb3b1ac0583a09f6dfacd5e6e")
	for i := range to {
		to[i] = 0x11
	}
	copy(s0[:], common.FromHex("c63ed6cb0b60124d44bd2248b329f2b07a1fa44010404ec7dfff70902534b4af"))
	var err error
	if proof, err = os.ReadFile(filepath.Join(claimSet, "receipts", "claim-0.bin")); err != nil {
		t.Skipf("claim vector not present: %v", err)
	}
	if composite, err = os.ReadFile(filepath.Join(claimSet, "receipts", "claim_composite-0.bin")); err != nil {
		t.Skipf("composite vector not present: %v", err)
	}
	return
}

func TestClaimWithProofVector(t *testing.T) {
	if pi.LibraryImageIDHex() != pi.ImageIDHex {
		t.Fatalf("the linked verifier's image id %s is not the pinned %s", pi.LibraryImageIDHex(), pi.ImageIDHex)
	}
	pk, to, s0, proof, composite := claimVector(t)
	st := setup(1_000_000, 4_000_000)
	c := ctx4(20, 0, 0)
	if _, err := exec(t, c, st, holder, 1_000, pk[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
	// The wrong claimant: the journal's m_0 differs (the row
	// claim_wrong_to); the composite receipt: over the claim's bound (the
	// row claim_composite, refused at the length before any parsing); a
	// truncated wrap: refused by the verifier; the wrong share: the
	// journal.
	if _, err := exec(t, c, st, thief, 0, claimBytes(pk, thief, s0, proof)); !errors.Is(err, ErrClaimProof) {
		t.Fatalf("wrong to: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, claimBytes(pk, to, s0, composite)); !errors.Is(err, ErrCalldata) {
		t.Fatalf("composite: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, claimBytes(pk, to, s0, proof[:len(proof)-1])); !errors.Is(err, ErrClaimProof) {
		t.Fatalf("truncated: %v", err)
	}
	var s1 [32]byte
	copy(s1[:], s0[:])
	s1[0] ^= 1
	if _, err := exec(t, c, st, thief, 0, claimBytes(pk, to, s1, proof)); !errors.Is(err, ErrClaimProof) {
		t.Fatalf("wrong share: %v", err)
	}
	wrongChain := ctx4(20, 0, 0)
	wrongChain.ChainID = 7316
	if _, err := exec(t, wrongChain, st, thief, 0, claimBytes(pk, to, s0, proof)); !errors.Is(err, ErrClaimProof) {
		t.Fatalf("another chain: %v", err)
	}
	if st.GetState(reg, AmountSlot(pk)).Big().Int64() != 1_000 || st.GetState(reg, ClaimNonceSlot(pk)).Big().Sign() != 0 {
		t.Fatal("a refused claim changed the state")
	}
	// The hostile claim by anyone holding the proof: three quarters to
	// `to`, the quarter burnt, the counter moved, the key out of the list.
	// The proof's holder is the secret's holder; the sender is anyone.
	if _, err := exec(t, c, st, thief, 0, claimBytes(pk, to, s0, proof)); err != nil {
		t.Fatalf("the claim: %v", err)
	}
	if got := balance(st, to); got != 750 {
		t.Fatalf("to paid %d, want 750", got)
	}
	if burnt := st.GetState(reg, SlotBurnt).Big().Int64(); burnt != 250 {
		t.Fatalf("burnt %d, want 250", burnt)
	}
	if st.GetState(reg, ClaimNonceSlot(pk)).Big().Uint64() != 1 || st.GetState(reg, AmountSlot(pk)).Big().Sign() != 0 || len(Keys(st, reg)) != 0 {
		t.Fatal("the claimed key is not unbonded with its counter at 1 and out of the list")
	}
	// A replay of the accepted proof: the key is unbonded; after a re-bond
	// the counter is 1 and the nonce-0 proof's journal no longer matches.
	if _, err := exec(t, c, st, thief, 0, claimBytes(pk, to, s0, proof)); !errors.Is(err, ErrUnbonded) {
		t.Fatalf("replay on an unbonded key: %v", err)
	}
	if _, err := exec(t, c, st, holder, 1_000, pk[:]); err != nil {
		t.Fatalf("re-bond: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, claimBytes(pk, to, s0, proof)); !errors.Is(err, ErrClaimProof) {
		t.Fatalf("replay after a re-bond: %v", err)
	}
}

func TestReleaseByProofNeedsTheLock(t *testing.T) {
	pk, to, s0, proof, _ := claimVector(t)
	st := setup(1_000_000, 4_000_000)
	// The exit address IS the vector's claimant, so the claim is the
	// holder's release by proof: refused before the lock (the lock is not
	// bypassed by the secret), the whole amount after it, nothing burnt.
	bondInput := append(pk.Bytes(), common.BytesToHash(to.Bytes()).Bytes()...)
	if _, err := exec(t, ctx4(10, 100, 0), st, holder, 1_000, bondInput); err != nil {
		t.Fatalf("bond with exit: %v", err)
	}
	if _, err := exec(t, ctx4(50, 100, 0), st, thief, 0, claimBytes(pk, to, s0, proof)); !errors.Is(err, ErrLocked) {
		t.Fatalf("release by proof before the lock: %v", err)
	}
	if _, err := exec(t, ctx4(110, 100, 0), st, thief, 0, claimBytes(pk, to, s0, proof)); err != nil {
		t.Fatalf("release by proof at the lock: %v", err)
	}
	if got := balance(st, to); got != 1_000 {
		t.Fatalf("exit paid %d, want 1000", got)
	}
	if st.GetState(reg, SlotBurnt) != (common.Hash{}) {
		t.Fatal("a release burnt something")
	}
}

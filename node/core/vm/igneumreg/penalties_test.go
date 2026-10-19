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
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/pi"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

// The version-4 regression tests (LAUNCH-GUARDS.md 2.7; DESIGN.md PA-37 to
// PA-40): each operation's effect and each refusal by name, with the
// version-3 forms unchanged beside them.

const testChain = 7315

// exec runs one call under ctx the way the EVM does (the value first; an
// error restores the state).
func exec(t *testing.T, ctx Context, st *memState, from common.Address, value int64, input []byte) ([]byte, error) {
	t.Helper()
	v := uint256.NewInt(uint64(value))
	snapStorage := map[common.Hash]common.Hash{}
	for k, val := range st.storage[reg] {
		snapStorage[k] = val
	}
	snapBal := map[common.Address]*uint256.Int{}
	for a, b := range st.balance {
		snapBal[a] = new(uint256.Int).Set(b)
	}
	st.SubBalance(from, v)
	st.AddBalance(reg, v)
	out, err := Exec(ctx, st, reg, from, from, v, input)
	if err != nil {
		st.storage[reg] = snapStorage
		st.balance = snapBal
	}
	return out, err
}

func ctx4(number uint64, lock uint64, reward int64) Context {
	return Context{Version: VersionPenalties, Number: number, ChainID: testChain, LockBlocks: lock, Reward: big.NewInt(reward)}
}

// signerKey is a deterministic Ed25519 key whose public key is the bond
// key (the open-seat form: no registered signer, the key's own bytes).
func signerKey(seed byte) (ed25519.PrivateKey, common.Hash) {
	var s [ed25519.SeedSize]byte
	s[0] = seed
	s[31] = 1
	priv := ed25519.NewKeyFromSeed(s[:])
	return priv, common.BytesToHash(priv.Public().(ed25519.PublicKey))
}

// signedHeader is a signed header as a chain carries it: with a base fee
// (every header under the fee rule has one; a nil base fee would decode
// from RLP as zero, not nil, and change the hash on the round trip).
func signedHeader(priv ed25519.PrivateKey, key common.Hash, height int64, extra byte) *types.Header {
	h := &types.Header{Number: big.NewInt(height), Difficulty: big.NewInt(1), BaseFee: big.NewInt(1), Extra: []byte{extra}, BondKey: &key, Round: 1, Coinbase: holder}
	sh := SigningHash(h)
	h.Sig = ed25519.Sign(priv, sh[:])
	return h
}

func pairBytes(t *testing.T, a, b *types.Header) []byte {
	t.Helper()
	enc, err := rlp.EncodeToBytes([]*types.Header{a, b})
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte{OpEquivocation}, enc...)
}

func balance(st *memState, a common.Address) int64 { return int64(st.GetBalance(a).Uint64()) }

func TestEquivocationPenalty(t *testing.T) {
	st := setup(1_000_000, 4_000_000)
	priv, key := signerKey(1)
	c := ctx4(20, 0, 7_000)
	if _, err := exec(t, c, st, holder, 100_000, key[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
	a := signedHeader(priv, key, 5, 1)
	b := signedHeader(priv, key, 5, 2)
	// Refusals by name, nothing changed by any of them.
	if _, err := exec(t, Context{Version: Version}, st, thief, 0, pairBytes(t, a, b)); !errors.Is(err, ErrNotPenalties) {
		t.Fatalf("version 3: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, []byte{OpEquivocation, 1, 2, 3}); !errors.Is(err, ErrEvidence) {
		t.Fatalf("garbage: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, pairBytes(t, a, a)); !errors.Is(err, ErrSameSeal) {
		t.Fatalf("one seal twice: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, pairBytes(t, a, signedHeader(priv, key, 6, 2))); !errors.Is(err, ErrSameHeight) {
		t.Fatalf("two heights: %v", err)
	}
	priv2, key2 := signerKey(2)
	if _, err := exec(t, c, st, thief, 0, pairBytes(t, a, signedHeader(priv2, key2, 5, 2))); !errors.Is(err, ErrSameKey) {
		t.Fatalf("two keys: %v", err)
	}
	forged := signedHeader(priv2, key, 5, 2)
	if _, err := exec(t, c, st, thief, 0, pairBytes(t, a, forged)); !errors.Is(err, ErrBadSig) {
		t.Fatalf("a forged signature: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, pairBytes(t, signedHeader(priv2, key2, 5, 1), signedHeader(priv2, key2, 5, 2))); !errors.Is(err, ErrUnbonded) {
		t.Fatalf("an unbonded key: %v", err)
	}
	// Two different signatures over one content are two seals (SealHash
	// covers Sig), still an equivocation: the honest signer is
	// deterministic and never makes them.
	twin := *a
	twin.Sig = ed25519.Sign(priv, append([]byte{}, SigningHash(a).Bytes()...))
	if SealHash(a) != SealHash(&twin) {
		t.Fatal("a deterministic signer made two signatures")
	}
	if st.GetState(reg, AmountSlot(key)).Big().Int64() != 100_000 || len(Keys(st, reg)) != 1 {
		t.Fatal("a refusal changed the state")
	}
	// The penalty: the whole bond forfeited, the reporter paid one block's
	// reward of it, the rest burnt (F-40); the key marked, unbonded, out of
	// the list.
	before := balance(st, thief)
	if _, err := exec(t, c, st, thief, 0, pairBytes(t, a, b)); err != nil {
		t.Fatalf("equivocation: %v", err)
	}
	if got := balance(st, thief) - before; got != 7_000 {
		t.Fatalf("reporter paid %d, want 7000", got)
	}
	if got := balance(st, reg); got != 0 {
		t.Fatalf("registry holds %d after the penalty, want 0", got)
	}
	if burnt := st.GetState(reg, SlotBurnt).Big().Int64(); burnt != 93_000 {
		t.Fatalf("burnt %d, want 93000", burnt)
	}
	if st.GetState(reg, AmountSlot(key)).Big().Sign() != 0 || !Marked(st, reg, key) || len(Keys(st, reg)) != 0 {
		t.Fatal("the key is not unbonded, marked and out of the list")
	}
	// A second report of the pair reverts; the key bonds nothing again;
	// the account's other key is untouched.
	if _, err := exec(t, c, st, thief, 0, pairBytes(t, a, b)); !errors.Is(err, ErrUnbonded) {
		t.Fatalf("second report: %v", err)
	}
	if _, err := exec(t, c, st, holder, 100, key[:]); !errors.Is(err, ErrMarked) {
		t.Fatalf("re-bond of a marked key: %v", err)
	}
	// The reward is capped at the WHOLE bond (a tiny bond: F-40, the whole
	// bond forfeited; a bond under one reward goes to the reporter whole
	// and nothing is burnt).
	priv3, key3 := signerKey(3)
	if _, err := exec(t, c, st, holder, 10, key3[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
	before = balance(st, thief)
	if _, err := exec(t, c, st, thief, 0, pairBytes(t, signedHeader(priv3, key3, 9, 1), signedHeader(priv3, key3, 9, 2))); err != nil {
		t.Fatalf("equivocation: %v", err)
	}
	if got := balance(st, thief) - before; got != 10 {
		t.Fatalf("reporter paid %d of a 10 bond, want 10", got)
	}
	if burnt := st.GetState(reg, SlotBurnt).Big().Int64(); burnt != 93_000 {
		t.Fatalf("burnt %d, want 93000", burnt)
	}
	if got := balance(st, reg); got != 0 {
		t.Fatalf("registry holds %d after the tiny bond's penalty, want 0", got)
	}
}

func TestEquivocationUnderARegisteredSigner(t *testing.T) {
	st := setup(1_000_000, 4_000_000)
	_, key := signerKey(4)
	signerPriv, signer := signerKey(5)
	c := ctx4(20, 0, 1)
	if _, err := exec(t, c, st, holder, 1_000, key[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
	if _, err := exec(t, c, st, holder, 0, append(key.Bytes(), signer.Bytes()...)); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	// Signed under the key's own bytes: refused, the registered signer
	// rules; signed under the signer: the penalty.
	own, _ := signerKey(4)
	if _, err := exec(t, c, st, thief, 0, pairBytes(t, signedHeader(own, key, 5, 1), signedHeader(own, key, 5, 2))); !errors.Is(err, ErrBadSig) {
		t.Fatalf("under the key's bytes with a signer registered: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, pairBytes(t, signedHeader(signerPriv, key, 5, 1), signedHeader(signerPriv, key, 5, 2))); err != nil {
		t.Fatalf("under the signer: %v", err)
	}
	if !Marked(st, reg, key) {
		t.Fatal("not marked")
	}
}

func endorsement(priv ed25519.PrivateKey, key common.Hash, unit uint32, height uint64, hash common.Hash) []byte {
	e := make([]byte, 0, EndorsementEntryBytes)
	e = append(e, key[:]...)
	var le [4]byte
	binary.LittleEndian.PutUint32(le[:], unit)
	e = append(e, le[:]...)
	return append(e, ed25519.Sign(priv, EndorseMessage(testChain, height, hash))...)
}

func endorsePair(height uint64, hashA, hashB common.Hash, a, b []byte) []byte {
	p := []byte{OpEndorseEquivocation}
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], height)
	p = append(p, le[:]...)
	p = append(p, hashA[:]...)
	p = append(p, hashB[:]...)
	p = append(p, a...)
	return append(p, b...)
}

func TestEndorsementEquivocationPenalty(t *testing.T) {
	st := setup(1_000_000, 4_000_000)
	priv, key := signerKey(6)
	c := ctx4(20, 0, 300)
	if _, err := exec(t, c, st, holder, 1_000, key[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
	hA, hB := common.HexToHash("0xa1"), common.HexToHash("0xb2")
	eA := endorsement(priv, key, 3, 50, hA)
	eB := endorsement(priv, key, 3, 50, hB)
	if _, err := exec(t, c, st, thief, 0, endorsePair(50, hA, hA, eA, eA)); !errors.Is(err, ErrSameEntry) {
		t.Fatalf("one hash: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, endorsePair(50, hA, hB, eA, endorsement(priv, key, 4, 50, hB))); !errors.Is(err, ErrSameEntry) {
		t.Fatalf("two units: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, endorsePair(51, hA, hB, eA, eB)); !errors.Is(err, ErrBadSig) {
		t.Fatalf("another height: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, endorsePair(50, hA, hB, eA, eB)[:100]); !errors.Is(err, ErrCalldata) {
		t.Fatalf("short: %v", err)
	}
	other := Context{Version: VersionPenalties, Number: 20, ChainID: testChain + 1}
	if _, err := exec(t, other, st, thief, 0, endorsePair(50, hA, hB, eA, eB)); !errors.Is(err, ErrBadSig) {
		t.Fatalf("another chain id: %v", err)
	}
	before := balance(st, thief)
	if _, err := exec(t, c, st, thief, 0, endorsePair(50, hA, hB, eA, eB)); err != nil {
		t.Fatalf("endorsement equivocation: %v", err)
	}
	if got := balance(st, thief) - before; got != 300 {
		t.Fatalf("reporter paid %d, want 300", got)
	}
	if burnt := st.GetState(reg, SlotBurnt).Big().Int64(); burnt != 700 {
		t.Fatalf("burnt %d, want 700", burnt)
	}
	if !Marked(st, reg, key) || len(Keys(st, reg)) != 0 {
		t.Fatal("the key is not marked and out of the list")
	}
}

func TestReleaseAndTheLock(t *testing.T) {
	st := setup(1_000_000, 4_000_000)
	_, key := signerKey(7)
	exit := common.HexToAddress("0x4444")
	// Bonded at height 10 with the exit address, lock 100 blocks.
	if _, err := exec(t, ctx4(10, 100, 0), st, holder, 5_000, append(key.Bytes(), common.BytesToHash(exit.Bytes()).Bytes()...)); err != nil {
		t.Fatalf("bond with exit: %v", err)
	}
	if got := st.GetState(reg, BondHeightSlot(key)).Big().Uint64(); got != 10 {
		t.Fatalf("post height %d, want 10", got)
	}
	rel := append([]byte{OpRelease}, key.Bytes()...)
	// Before the lock: the release and the exit change are refused; a
	// top-up without an exit word is not, and does not move the lock.
	if _, err := exec(t, ctx4(50, 100, 0), st, holder, 0, rel); !errors.Is(err, ErrLocked) {
		t.Fatalf("release before the lock: %v", err)
	}
	if _, err := exec(t, ctx4(50, 100, 0), st, holder, 1, append(key.Bytes(), common.BytesToHash(holder.Bytes()).Bytes()...)); !errors.Is(err, ErrLocked) {
		t.Fatalf("exit change before the lock: %v", err)
	}
	if _, err := exec(t, ctx4(109, 100, 0), st, holder, 1_000, key[:]); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	if got := st.GetState(reg, BondHeightSlot(key)).Big().Uint64(); got != 10 {
		t.Fatalf("a top-up moved the post height to %d", got)
	}
	if _, err := exec(t, ctx4(109, 100, 0), st, holder, 0, rel); !errors.Is(err, ErrLocked) {
		t.Fatalf("release one block early: %v", err)
	}
	// Not the account, not bonded.
	if _, err := exec(t, ctx4(110, 100, 0), st, other, 0, rel); !errors.Is(err, ErrAccount) {
		t.Fatalf("release by another: %v", err)
	}
	_, key2 := signerKey(8)
	if _, err := exec(t, ctx4(110, 100, 0), st, holder, 0, append([]byte{OpRelease}, key2.Bytes()...)); !errors.Is(err, ErrUnbonded) {
		t.Fatalf("release of nothing: %v", err)
	}
	// At the lock: the exit address may change; the release pays the
	// whole balance to it and unbonds the key.
	exit2 := common.HexToAddress("0x5555")
	if _, err := exec(t, ctx4(110, 100, 0), st, holder, 1, append(key.Bytes(), common.BytesToHash(exit2.Bytes()).Bytes()...)); err != nil {
		t.Fatalf("exit change at the lock: %v", err)
	}
	if _, err := exec(t, ctx4(110, 100, 0), st, holder, 0, rel); err != nil {
		t.Fatalf("release at the lock: %v", err)
	}
	if got := balance(st, exit2); got != 6_001 {
		t.Fatalf("exit paid %d, want 6001", got)
	}
	if st.GetState(reg, AmountSlot(key)).Big().Sign() != 0 || len(Keys(st, reg)) != 0 || Marked(st, reg, key) {
		t.Fatal("the released key is not unbonded, out of the list and unmarked")
	}
	// A released key may bond again as a fresh key: a new post height.
	if _, err := exec(t, ctx4(200, 100, 0), st, other, 10, key[:]); err != nil {
		t.Fatalf("re-bond: %v", err)
	}
	if got := st.GetState(reg, BondHeightSlot(key)).Big().Uint64(); got != 200 {
		t.Fatalf("re-bond post height %d, want 200", got)
	}
	// Version 3 keeps its form: the exit changes at once and no post
	// height is written.
	st3 := setup(1_000_000, 4_000_000)
	if _, err := call(t, st3, holder, 100, key[:]); err != nil {
		t.Fatalf("v3 bond: %v", err)
	}
	if _, err := call(t, st3, holder, 1, append(key.Bytes(), common.BytesToHash(exit.Bytes()).Bytes()...)); err != nil {
		t.Fatalf("v3 exit change: %v", err)
	}
	if st3.GetState(reg, BondHeightSlot(key)) != (common.Hash{}) {
		t.Fatal("version 3 wrote a post height")
	}
	if _, err := call(t, st3, holder, 0, rel); !errors.Is(err, ErrNotPenalties) {
		t.Fatalf("v3 release: %v", err)
	}
}

// TestClaimMessageVector pins m_0 to the hash team's claim row (the landed
// pi sets, pi.json row "claim": chain 7315, nonce 0, to = 0x11 x 20).
func TestClaimMessageVector(t *testing.T) {
	pk := common.HexToHash("9e5f6270f0506ea38c26cf765b3775cf1a6c989eb3b1ac0583a09f6dfacd5e6e")
	var to common.Address
	for i := range to {
		to[i] = 0x11
	}
	got := ClaimMessage(7315, pk, 0, to)
	if hex.EncodeToString(got[:]) != "104df86e993da0d68e11b06dd230eaaa63bb19760aff408058357cb194a34613" {
		t.Fatalf("m_0 %x", got)
	}
	if other := ClaimMessage(7315, pk, 1, to); other == got {
		t.Fatal("the nonce is not in m_0")
	}
	if other := ClaimMessage(7316, pk, 0, to); other == got {
		t.Fatal("the chain id is not in m_0")
	}
}

func claimBytes(pk common.Hash, to common.Address, s0 [32]byte, proof []byte) []byte {
	p := []byte{OpClaim}
	p = append(p, pk[:]...)
	p = append(p, common.BytesToHash(to.Bytes()).Bytes()...)
	p = append(p, s0[:]...)
	return append(p, proof...)
}

func TestClaimFormsByVersion(t *testing.T) {
	st := setup(1_000_000, 4_000_000)
	var sk [32]byte
	sk[0] = 9
	pk := BondKeyOf(sk)
	c := ctx4(20, 0, 0)
	if _, err := exec(t, c, st, holder, 1_000, pk[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
	// Version 4 refuses the secret-reveal form.
	reveal := append(append(append([]byte{}, sk[:]...), common.BytesToHash(thief.Bytes()).Bytes()...), make([]byte, 32)...)
	if _, err := exec(t, c, st, thief, 0, reveal); !errors.Is(err, ErrRetired) {
		t.Fatalf("secret reveal at version 4: %v", err)
	}
	// The proof form's shape refusals: no proof, over the bound, a word
	// that is not an address, an unbonded key; and, on a node without
	// the verifier linked, the proof is refused as not built (never
	// accepted).
	var s0 [32]byte
	if _, err := exec(t, c, st, thief, 0, claimBytes(pk, thief, s0, nil)); !errors.Is(err, ErrCalldata) {
		t.Fatalf("no proof: %v", err)
	}
	if _, err := exec(t, c, st, thief, 0, claimBytes(pk, thief, s0, make([]byte, pi.MaxBytesClaim+1))); !errors.Is(err, ErrCalldata) {
		t.Fatalf("over the bound: %v", err)
	}
	bad := claimBytes(pk, thief, s0, []byte{2})
	bad[1+32] = 1
	if _, err := exec(t, c, st, thief, 0, bad); !errors.Is(err, ErrClaimTo) {
		t.Fatalf("not an address: %v", err)
	}
	_, key2 := signerKey(10)
	if _, err := exec(t, c, st, thief, 0, claimBytes(key2, thief, s0, []byte{2})); !errors.Is(err, ErrUnbonded) {
		t.Fatalf("unbonded: %v", err)
	}
	if !pi.Built {
		if _, err := exec(t, c, st, thief, 0, claimBytes(pk, thief, s0, []byte{2})); !errors.Is(err, ErrClaimProof) {
			t.Fatalf("no verifier: %v", err)
		}
	}
	if st.GetState(reg, AmountSlot(pk)).Big().Int64() != 1_000 || st.GetState(reg, ClaimNonceSlot(pk)).Big().Sign() != 0 {
		t.Fatal("a refused claim changed the state")
	}
	// Version 3 keeps the secret-reveal form (the fifth development chain).
	st3 := setup(1_000_000, 4_000_000)
	if _, err := call(t, st3, holder, 1_000, pk[:]); err != nil {
		t.Fatalf("v3 bond: %v", err)
	}
	if _, err := call(t, st3, thief, 0, reveal); err != nil {
		t.Fatalf("v3 secret reveal: %v", err)
	}
	if got := balance(st3, thief); got != 1_000_000+750 {
		t.Fatalf("v3 thief paid to %d", got)
	}
	if _, err := call(t, st3, thief, 0, claimBytes(pk, thief, s0, []byte{2})); !errors.Is(err, ErrNotPenalties) {
		t.Fatalf("v3 proof form: %v", err)
	}
}

func TestGasOf(t *testing.T) {
	if GasOf(make([]byte, 32)) != Gas || GasOf(make([]byte, 96)) != Gas || GasOf(append([]byte{OpRelease}, make([]byte, 32)...)) != Gas {
		t.Fatal("version-3 forms and the release at the flat gas")
	}
	if GasOf([]byte{OpClaim, 1, 2}) != GasClaim || GasOf([]byte{OpEquivocation, 1}) != GasEquivocation || GasOf([]byte{OpEndorseEquivocation, 1}) != GasEquivocation {
		t.Fatal("the penalty operations' gas")
	}
	// A 32-byte key whose first byte is a selector is a key.
	k := make([]byte, 32)
	k[0] = OpEquivocation
	if _, ok := selectorOf(k); ok {
		t.Fatal("a key read as a selector")
	}
}

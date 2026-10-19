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

// Package igneumreg is version 3 of the bond registry: the work-authority
// rule's genesis system contract in its NATIVE form (run by the EVM at the
// registry address in place of bytecode; core/vm calls Run), carrying the
// hostile-key rule and the exit rule of DRAFT-HOSTILE-KEY.md sections 1 to
// 3 and 7 as frozen in LAUNCH-GUARDS.md section 2, and the genesis form's
// early-concentration guard (section 2.4: the cap a quarter of max(B_total,
// B_floor) plus one percent, B_floor written by consensus each block).
// Research PROTOTYPE on the lab build, nothing adopted. Versions 1 and 2
// (bytecode, consensus/igneum/authority.go) stay as they are for the chains
// that carry them.
//
// Operations, by calldata length and value (anything else reverts):
//
//   - 32 bytes, no value: read. Returns 128 bytes: amount, account, signer,
//     exit address.
//   - 32 bytes, value > 0: bond. The caller must be the transaction's origin.
//     A fresh key records the caller as the bond's account and as its exit
//     address, and appends the key to the key list; a bonded key accepts
//     more only from its account. The amount after the addition must not
//     exceed the cap (below).
//   - 64 bytes (key, exit), value > 0: bond with an exit address (a fresh
//     key) or, by the account, a change of the exit address with the bond
//     (the lock of DRAFT-P1 section 3 is NOT modelled on the lab build,
//     stated: the exit address changes at once).
//   - 64 bytes (key, signer), no value: rotate the signer, by the account.
//   - 96 bytes (sk, to, zero), no value: the hostile-key claim. pk = G(sk)
//     = BLAKE3(tag("IgneumHash/R0A/bond-pk") || sk) must be a bonded key.
//     The whole balance leaves the bond at once and the key is unbonded
//     (amount zero; it leaves the next units table). to equal to the
//     bond's exit address is the holder's orderly exit and pays the whole
//     amount to it; any other to is a hostile claim and pays amount less
//     phi = one quarter, which is BURNT (it reaches no account; the fund
//     address in slot 2 is kept for the record and receives nothing;
//     LAUNCH-GUARDS 19b29108). Anyone may send it:
//     presenting the secret is the claim. The claim reveals sk in the
//     transaction, so a sealer that sees it first can claim first: the
//     frozen draft's signature of knowledge has no such exposure; the lab
//     form states it.
//
// The cap: after <= min(static cap, 26 percent of max(registry balance,
// B_floor)), B_floor the floor slot consensus writes in Finalize (16
// percent of the issuance to date, LAUNCH-GUARDS.md 2.4). At genesis the
// floor is zero and no bond is admitted until issuance accrues.
//
// Storage: slot 0 the static cap; slot 1 B_floor; slot 2 the fund address;
// slot 3 the key count; slot 4 reserved (the fallback activation form);
// slot 5 the supply burnt by penalties to date (version 4);
// slot keccak("IgneumHash/R0A/keylist") + i the i-th bonded key; slot key
// the amount; slot keccak(key) the account; keccak(key || 0x01) the
// signer; keccak(key || 0x02) the exit address. Keys below 2^8 are
// refused so the small slots never alias a key.
//
// VERSION 4 (penalties.go; LAUNCH-GUARDS.md section 2.7, the four penalty
// and exit rules, the project's forms of 12:2x UK on 11 October 2026) adds the
// operations selected by a LEADING BYTE, calldata = u8(op) || payload:
// 0x10 the equivocation, 0x11 the endorsement equivocation, 0x12 the
// release after the lock, 0x13 the hostile claim on the real verifier
// (pow/pi), and the slots keccak("IgneumHash/R0A/marked" || key) the
// mark, keccak("IgneumHash/R0A/bond-height" || key) the post height the
// lock runs from, keccak("IgneumHash/R0A/claim-nonce" || key) the claim
// counter. Version 4 refuses the 96-byte secret-reveal claim; version 3
// keeps every form above exactly as it was (the fifth development chain
// runs it from genesis and a state write it never made would fork it), so
// the version-4 operations and slots are refused on a version-3 chain.
package igneumreg

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"lukechampine.com/blake3"
)

// Version is the registry version the bond, signer, read and secret-reveal
// forms were written for (the fifth development chain). VersionPenalties
// is version 4: the same forms, the secret-reveal claim refused, the four
// penalty and exit operations and their slots (penalties.go). Native
// reports whether a version runs this package.
const (
	Version          = 3
	VersionPenalties = 4
)

func Native(version uint64) bool { return version == Version || version == VersionPenalties }

// Gas is the flat gas of one registry call (the lab's figure) for the
// version-3 forms; GasOf gives the per-operation figure (penalties.go).
const Gas uint64 = 50_000

// Slots of the registry's own fields.
var (
	SlotCap        = common.BigToHash(big.NewInt(0))
	SlotFloor      = common.BigToHash(big.NewInt(1))
	SlotFund       = common.BigToHash(big.NewInt(2))
	SlotCount      = common.BigToHash(big.NewInt(3))
	SlotActivation = common.BigToHash(big.NewInt(4))
	SlotBurnt      = common.BigToHash(big.NewInt(5))
	keyListBase    = crypto.Keccak256Hash([]byte("IgneumHash/R0A/keylist"))
)

// Context is what an operation reads of the chain beyond the state: the
// registry version the chain's engine block names, the block's height,
// the chain id (the claim message binds it), the lock in blocks and one
// block's reward (the miner's share of the subsidy at this height; the
// reporter's pay). core/vm fills it at the call site; a zero Context is
// version 3 with no lock and no reward (the package tests' default).
type Context struct {
	Version    uint64
	Number     uint64
	ChainID    uint64
	LockBlocks uint64
	Reward     *big.Int
}

func (c Context) penalties() bool { return c.Version == VersionPenalties }

// The hostile-key rule's constants.
const (
	// PhiBps is the fraction of a hostile claim burnt to the fund, in
	// basis points: one quarter.
	PhiBps = 2500
	// CapBps is the per-key cap as a fraction of max(B_total, B_floor), in
	// basis points: a quarter plus one percent slack.
	CapBps = 2600
	// minKey: keys below it are refused (slot aliasing).
	minKey = 256
)

// Errors (each reverts the call).
var (
	ErrCalldata   = errors.New("igneum registry: calldata is not an operation")
	ErrOrigin     = errors.New("igneum registry: a bond is posted by the transaction's origin only")
	ErrKey        = errors.New("igneum registry: key below the reserved range")
	ErrAccount    = errors.New("igneum registry: not the bond's account")
	ErrCap        = errors.New("igneum registry: bond over the cap")
	ErrOverflow   = errors.New("igneum registry: amount overflow")
	ErrUnbonded   = errors.New("igneum registry: key is not bonded")
	ErrClaimShape = errors.New("igneum registry: claim's third word must be zero")
	// Version 4 (penalties.go).
	ErrRetired      = errors.New("igneum registry: the secret-reveal claim is retired at registry version 4 (the claim carries a proof)")
	ErrNotPenalties = errors.New("igneum registry: operation needs registry version 4")
	ErrMarked       = errors.New("igneum registry: key is marked by an equivocation")
	ErrLocked       = errors.New("igneum registry: the bond is locked until its lock height")
	ErrEvidence     = errors.New("igneum registry: the evidence is not two sealed headers")
	ErrSameHeight   = errors.New("igneum registry: the two headers are not at one height")
	ErrSameKey      = errors.New("igneum registry: the two headers are not under one bond key")
	ErrSameSeal     = errors.New("igneum registry: the two headers have one seal hash")
	ErrBadSig       = errors.New("igneum registry: a signature does not verify under the key's signer")
	ErrSameEntry    = errors.New("igneum registry: the two endorsements are not by one unit over different hashes")
	ErrClaimProof   = errors.New("igneum registry: the claim's proof is refused")
	ErrClaimTo      = errors.New("igneum registry: the claim's address word is not an address")
)

// StateDB is what the registry needs of the EVM's state.
type StateDB interface {
	GetState(common.Address, common.Hash) common.Hash
	SetState(common.Address, common.Hash, common.Hash)
	GetBalance(common.Address) *uint256.Int
	SubBalance(common.Address, *uint256.Int)
	AddBalance(common.Address, *uint256.Int)
}

// AmountSlot, AccountSlot, SignerSlot and ExitSlot are a key's slots.
func AmountSlot(key common.Hash) common.Hash  { return key }
func AccountSlot(key common.Hash) common.Hash { return crypto.Keccak256Hash(key[:]) }
func SignerSlot(key common.Hash) common.Hash  { return crypto.Keccak256Hash(key[:], []byte{1}) }
func ExitSlot(key common.Hash) common.Hash    { return crypto.Keccak256Hash(key[:], []byte{2}) }

// MarkedSlot, BondHeightSlot and ClaimNonceSlot are a key's version-4
// slots: the equivocation mark, the post height the lock runs from, the
// claim counter nonce_b (the project's base strings, 12:2x UK).
func MarkedSlot(key common.Hash) common.Hash {
	return crypto.Keccak256Hash([]byte("IgneumHash/R0A/marked"), key[:])
}
func BondHeightSlot(key common.Hash) common.Hash {
	return crypto.Keccak256Hash([]byte("IgneumHash/R0A/bond-height"), key[:])
}
func ClaimNonceSlot(key common.Hash) common.Hash {
	return crypto.Keccak256Hash([]byte("IgneumHash/R0A/claim-nonce"), key[:])
}

// KeySlot is the i-th entry of the key list.
func KeySlot(i uint64) common.Hash {
	b := new(big.Int).SetBytes(keyListBase[:])
	b.Add(b, new(big.Int).SetUint64(i))
	return common.BigToHash(b)
}

// bondPKTag is tag("IgneumHash/R0A/bond-pk"): the one-byte length prefix
// and the ASCII (the specification's section 0.9).
var bondPKTag = append([]byte{22}, []byte("IgneumHash/R0A/bond-pk")...)

// BondKeyOf is G: the bond key of a secret, pk = BLAKE3(tag || sk).
func BondKeyOf(sk [32]byte) common.Hash {
	h := blake3.New(32, nil)
	h.Write(bondPKTag)
	h.Write(sk[:])
	var out common.Hash
	h.Sum(out[:0])
	return out
}

// Cap is the cap in force at a state: min(static cap, 26 percent of
// max(balance, floor)).
func Cap(st StateDB, self common.Address) *big.Int {
	static := st.GetState(self, SlotCap).Big()
	floor := st.GetState(self, SlotFloor).Big()
	m := st.GetBalance(self).ToBig()
	if floor.Cmp(m) > 0 {
		m = floor
	}
	dyn := new(big.Int).Mul(m, big.NewInt(CapBps))
	dyn.Div(dyn, big.NewInt(10000))
	if dyn.Cmp(static) < 0 {
		return dyn
	}
	return static
}

// Run executes one call to the registry at self under the version-3 forms
// with no chain context (the fifth development chain's form; the tests'
// shorthand): the EVM has transferred value already and reverts the state
// on error. Exec is the call with a Context (core/vm's call site).
func Run(st StateDB, self, caller, origin common.Address, value *uint256.Int, input []byte) ([]byte, error) {
	return Exec(Context{Version: Version}, st, self, caller, origin, value, input)
}

// Exec executes one call to the registry at self under ctx.
func Exec(ctx Context, st StateDB, self, caller, origin common.Address, value *uint256.Int, input []byte) ([]byte, error) {
	hasValue := value != nil && !value.IsZero()
	if op, ok := selectorOf(input); ok {
		if hasValue {
			return nil, ErrCalldata
		}
		if !ctx.penalties() {
			return nil, ErrNotPenalties
		}
		return nil, runPenalty(ctx, st, self, caller, origin, op, input[1:])
	}
	switch len(input) {
	case 32:
		key := common.BytesToHash(input)
		if !hasValue {
			return read(st, self, key), nil
		}
		return nil, bond(ctx, st, self, caller, origin, value, key, nil)
	case 64:
		key := common.BytesToHash(input[:32])
		second := common.BytesToHash(input[32:])
		if hasValue {
			return nil, bond(ctx, st, self, caller, origin, value, key, &second)
		}
		return nil, rotate(st, self, caller, origin, key, second)
	case 96:
		if hasValue {
			return nil, ErrCalldata
		}
		if ctx.penalties() {
			return nil, ErrRetired
		}
		var sk [32]byte
		copy(sk[:], input[:32])
		to := common.BytesToAddress(input[32:64])
		if common.BytesToHash(input[64:]) != (common.Hash{}) {
			return nil, ErrClaimShape
		}
		return nil, claim(st, self, sk, to)
	}
	return nil, ErrCalldata
}

// selectorOf reads the version-4 selector: the version-3 forms are selected
// by LENGTH first (32, 64, 96 bytes keep their meaning whatever their first
// byte, since a key is any 32-byte value), and every other calldata is
// u8(op) || payload with op in OpEquivocation to OpClaim. A release is 33
// bytes, an endorsement equivocation 273, a claim 98 to 2,144, an
// equivocation the RLP of two headers (hundreds of bytes); none is 32, 64
// or 96.
func selectorOf(input []byte) (byte, bool) {
	switch len(input) {
	case 0, 32, 64, 96:
		return 0, false
	}
	op := input[0]
	if op < OpEquivocation || op > OpClaim {
		return 0, false
	}
	return op, true
}

// removeKey takes a key out of the key list (the last entry moves into its
// place) so the next table never reads it. A key not in the list is left
// alone.
func removeKey(st StateDB, self common.Address, key common.Hash) {
	n := st.GetState(self, SlotCount).Big().Uint64()
	for i := uint64(0); i < n; i++ {
		if st.GetState(self, KeySlot(i)) != key {
			continue
		}
		last := st.GetState(self, KeySlot(n-1))
		st.SetState(self, KeySlot(i), last)
		st.SetState(self, KeySlot(n-1), common.Hash{})
		st.SetState(self, SlotCount, common.BigToHash(new(big.Int).SetUint64(n-1)))
		return
	}
}

// Marked reports a key's equivocation mark (version 4; a marked key seals
// nothing, draws nothing and bonds nothing again).
func Marked(st StateDB, self common.Address, key common.Hash) bool {
	return st.GetState(self, MarkedSlot(key)) != (common.Hash{})
}

// LockHeight is the first height a key's bond may be released at or its
// exit address changed: the post height plus the lock (version 4; a key
// with no recorded post height reads zero, so the lock is open).
func LockHeight(ctx Context, st StateDB, self common.Address, key common.Hash) uint64 {
	posted := st.GetState(self, BondHeightSlot(key)).Big()
	if !posted.IsUint64() || posted.Sign() == 0 {
		return 0
	}
	return posted.Uint64() + ctx.LockBlocks
}

func read(st StateDB, self common.Address, key common.Hash) []byte {
	out := make([]byte, 0, 128)
	out = append(out, st.GetState(self, AmountSlot(key)).Bytes()...)
	out = append(out, st.GetState(self, AccountSlot(key)).Bytes()...)
	out = append(out, st.GetState(self, SignerSlot(key)).Bytes()...)
	out = append(out, st.GetState(self, ExitSlot(key)).Bytes()...)
	return out
}

func bond(ctx Context, st StateDB, self, caller, origin common.Address, value *uint256.Int, key common.Hash, exit *common.Hash) error {
	if caller != origin {
		return ErrOrigin
	}
	if key.Big().Cmp(big.NewInt(minKey)) < 0 {
		return ErrKey
	}
	if ctx.penalties() && Marked(st, self, key) {
		return ErrMarked
	}
	amount := st.GetState(self, AmountSlot(key)).Big()
	if amount.Sign() == 0 {
		// A fresh key: the caller is the account and, without an exit
		// word, the exit address; the key joins the list. Under version 4
		// the post height is recorded: the lock runs from it (the exit
		// rule of LAUNCH-GUARDS.md 2.2; a top-up never moves it).
		st.SetState(self, AccountSlot(key), common.BytesToHash(caller.Bytes()))
		if exit == nil {
			st.SetState(self, ExitSlot(key), common.BytesToHash(caller.Bytes()))
		} else {
			st.SetState(self, ExitSlot(key), *exit)
		}
		if ctx.penalties() {
			st.SetState(self, BondHeightSlot(key), common.BigToHash(new(big.Int).SetUint64(ctx.Number)))
		}
		n := st.GetState(self, SlotCount).Big().Uint64()
		st.SetState(self, KeySlot(n), key)
		st.SetState(self, SlotCount, common.BigToHash(new(big.Int).SetUint64(n+1)))
	} else {
		account := common.BytesToAddress(st.GetState(self, AccountSlot(key)).Bytes())
		if account != caller {
			return ErrAccount
		}
		if exit != nil {
			// The exit address changes only after the lock (version 4;
			// version 3 changes it at once, stated in KEYED-FORM.md).
			if ctx.penalties() && ctx.Number < LockHeight(ctx, st, self, key) {
				return ErrLocked
			}
			st.SetState(self, ExitSlot(key), *exit)
		}
	}
	after := new(big.Int).Add(amount, value.ToBig())
	if after.BitLen() > 256 {
		return ErrOverflow
	}
	if after.Cmp(Cap(st, self)) > 0 {
		return ErrCap
	}
	st.SetState(self, AmountSlot(key), common.BigToHash(after))
	return nil
}

func rotate(st StateDB, self, caller, origin common.Address, key, signer common.Hash) error {
	if caller != origin {
		return ErrOrigin
	}
	if st.GetState(self, AmountSlot(key)).Big().Sign() == 0 {
		return ErrUnbonded
	}
	if common.BytesToAddress(st.GetState(self, AccountSlot(key)).Bytes()) != caller {
		return ErrAccount
	}
	st.SetState(self, SignerSlot(key), signer)
	return nil
}

func claim(st StateDB, self common.Address, sk [32]byte, to common.Address) error {
	key := BondKeyOf(sk)
	amount := st.GetState(self, AmountSlot(key)).Big()
	if amount.Sign() == 0 {
		return ErrUnbonded
	}
	exit := common.BytesToAddress(st.GetState(self, ExitSlot(key)).Bytes())
	// The key leaves the table first: amount zero, nothing else kept.
	st.SetState(self, AmountSlot(key), common.Hash{})
	pay := new(big.Int).Set(amount)
	if to != exit {
		// The hostile claim's forfeited quarter is a BURN (LAUNCH-GUARDS
		// 19b29108, the project's ruling of 10:3x UK): it leaves the registry and
		// reaches no account; the supply falls by it.
		burn := new(big.Int).Mul(amount, big.NewInt(PhiBps))
		burn.Div(burn, big.NewInt(10000))
		pay.Sub(pay, burn)
		if v, overflow := uint256.FromBig(burn); !overflow && !v.IsZero() {
			st.SubBalance(self, v)
		}
	}
	transfer(st, self, to, pay)
	return nil
}

func transfer(st StateDB, from, to common.Address, amount *big.Int) {
	v, overflow := uint256.FromBig(amount)
	if overflow || v.IsZero() {
		return
	}
	st.SubBalance(from, v)
	st.AddBalance(to, v)
}

// Bond is a key's record at a state (the engine's read).
type Bond struct {
	Amount  *big.Int
	Account common.Address
	Signer  common.Hash
	Exit    common.Address
}

// BondOf reads a key.
func BondOf(st StateDB, self common.Address, key common.Hash) Bond {
	return Bond{
		Amount:  st.GetState(self, AmountSlot(key)).Big(),
		Account: common.BytesToAddress(st.GetState(self, AccountSlot(key)).Bytes()),
		Signer:  st.GetState(self, SignerSlot(key)),
		Exit:    common.BytesToAddress(st.GetState(self, ExitSlot(key)).Bytes()),
	}
}

// Keys lists the bonded keys in posting order. Under version 3 every key
// ever posted stays listed (a claimed key with amount zero); under version
// 4 a penalised, released or claimed key leaves the list (the last entry
// takes its place, so the order is posting order with those moves).
func Keys(st StateDB, self common.Address) []common.Hash {
	n := st.GetState(self, SlotCount).Big().Uint64()
	out := make([]common.Hash, 0, n)
	for i := uint64(0); i < n; i++ {
		out = append(out, st.GetState(self, KeySlot(i)))
	}
	return out
}

// Floor reads B_floor, Total the registry's balance (B_total).
func Floor(st StateDB, self common.Address) *big.Int { return st.GetState(self, SlotFloor).Big() }
func Total(st StateDB, self common.Address) *big.Int { return st.GetBalance(self).ToBig() }

// Placeholder is the code a version 3 registry account carries in genesis:
// INVALID, so a node without the native form reverts every call instead of
// accepting value into an account no rule reads.
var Placeholder = []byte{0xfe}

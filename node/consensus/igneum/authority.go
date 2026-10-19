// Copyright 2026 Igneum. Research prototype of the miner-bound work authority
// rule (the outside review's widened-scope ruling of 10 October 2026, section 3, the
// dev-chain test; the project's dispatch of 23:2x UK). A PROPOSAL and a PROTOTYPE:
// nothing here is adopted, served or part of any launch genesis.

package igneum

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// AuthorityParams are the rule's constants from the engine block
// (ctypes.IgneumAuthority): from Height every block carries a bond key in
// its header (types.Header.BondKey, inside the seal hash and so inside the
// proof-of-work pre-image: the key is hashed into the seed through the
// header's field set, the WEAK FORM of DRAFT-P1-KEY-BOUND-PROOF.md section
// 2, carried on the version in force until a version with the per-attempt
// signature exists), the key is bonded in the registry contract at the
// parent's state, and the coinbase is the bond's account (the reward is
// payable only to the account that posted the bond). Before Height no block
// carries a key (the unbonded allowance is the whole chain before the rule
// and nothing after it: U = 0).
type AuthorityParams struct {
	Height   uint64
	Registry common.Address
	Cap      *big.Int
	// RegistryVersion, BRefBps and SRef: the native registry and the
	// genesis form's floor (ctypes.IgneumAuthority; sortition_genesis.go).
	RegistryVersion uint64
	BRefBps         uint64
	SRef            *big.Int
	// LockBlocks is the exit rule's lock (registry version 4; penalties.go).
	LockBlocks uint64
}

// Active reports whether the rule binds a block at this height.
func (a *AuthorityParams) Active(height uint64) bool {
	return a != nil && height >= a.Height
}

// AuthorityFor reads the rule's constants from the engine block; nil when
// the chain carries no rule.
func AuthorityFor(cfg *ctypes.IgneumConfig) (*AuthorityParams, error) {
	if cfg == nil || cfg.Authority == nil {
		return nil, nil
	}
	a := cfg.Authority
	if a.Registry == (common.Address{}) {
		return nil, errors.New("igneum: authority rule names no registry address")
	}
	if a.Cap == nil || a.Cap.Sign() <= 0 {
		return nil, errors.New("igneum: authority rule has no positive per-key cap")
	}
	if a.Height == 0 {
		return nil, errors.New("igneum: authority rule at height 0 leaves no block to post a bond in")
	}
	p := &AuthorityParams{Height: a.Height, Registry: a.Registry, Cap: new(big.Int).Set(a.Cap), RegistryVersion: a.RegistryVersion, BRefBps: a.BRefBps}
	if a.SRef != nil {
		p.SRef = new(big.Int).Set(a.SRef)
	}
	if a.BRefBps > 10000 {
		return nil, errors.New("igneum: authority bRefBps above 10000")
	}
	if a.BRefBps > 0 && !igneumreg.Native(a.RegistryVersion) {
		return nil, errors.New("igneum: the floor (bRefBps) needs the native registry (registryVersion 3 or 4)")
	}
	p.LockBlocks = a.LockBlocks
	return p, nil
}

// The bond registry: a genesis system contract with three operations, all
// on a 32-byte calldata (the bond key) and nothing else.
//
//   - value > 0: bond. The caller must be the transaction's origin (a top-level
//     signed transaction; no contract can hold or move a bond). A fresh key
//     records the caller as the bond's account; a bonded key accepts more only
//     from its own account. The amount after the addition must not exceed the
//     cap in slot 0 (and must not wrap). Nothing is ever withdrawn (the
//     prototype has no release).
//   - value == 0: read. Returns 64 bytes: the amount, then the account.
//   - anything else reverts.
//
// Storage: slot 0 the cap; slot key the amount; slot KECCAK256(key) the
// account. RegistryAsm is the source, RegistryCode its compilation by
// core/asm (TestRegistryCodePinned holds them together).
const RegistryAsm = `
	CALLDATASIZE
	PUSH 0x20
	EQ
	JUMPI @ok
revert:
	PUSH 0x00
	PUSH 0x00
	REVERT
ok:
	PUSH 0x00
	CALLDATALOAD
	CALLVALUE
	PUSH 0x00
	EQ
	JUMPI @view
	CALLER
	ORIGIN
	EQ
	ISZERO
	JUMPI @revert
	DUP1
	PUSH 0x00
	MSTORE
	PUSH 0x20
	PUSH 0x00
	KECCAK256
	DUP1
	SLOAD
	DUP1
	ISZERO
	JUMPI @fresh
	CALLER
	EQ
	ISZERO
	JUMPI @revert
	JUMP @add
fresh:
	POP
	CALLER
	DUP2
	SSTORE
add:
	POP
	DUP1
	SLOAD
	CALLVALUE
	ADD
	DUP1
	CALLVALUE
	GT
	JUMPI @revert
	DUP1
	PUSH 0x00
	SLOAD
	LT
	JUMPI @revert
	SWAP1
	SSTORE
	STOP
view:
	DUP1
	SLOAD
	PUSH 0x00
	MSTORE
	PUSH 0x40
	MSTORE
	PUSH 0x20
	PUSH 0x40
	KECCAK256
	SLOAD
	PUSH 0x20
	MSTORE
	PUSH 0x40
	PUSH 0x00
	RETURN
`

// RegistryCode is the registry's runtime bytecode (the compilation of
// RegistryAsm, pinned; the genesis of a chain with the rule carries it at
// the registry address with the cap in slot 0).
var RegistryCode = common.FromHex(registryCodeHex)

// RegistryAsmV2 adds the sortition stage's signer: a 64-byte calldata (key,
// signer) with no value from the bond's account sets the bond's signer
// (slot KECCAK256(key || 0x01)); a read returns 96 bytes (amount, account,
// signer). The bond operation is version 1's.
const RegistryAsmV2 = `
	CALLDATASIZE
	PUSH 0x20
	EQ
	JUMPI @ok
	CALLDATASIZE
	PUSH 0x40
	EQ
	JUMPI @rotate
revert:
	PUSH 0x00
	PUSH 0x00
	REVERT
ok:
	PUSH 0x00
	CALLDATALOAD
	CALLVALUE
	PUSH 0x00
	EQ
	JUMPI @view
	CALLER
	ORIGIN
	EQ
	ISZERO
	JUMPI @revert
	DUP1
	PUSH 0x00
	MSTORE
	PUSH 0x20
	PUSH 0x00
	KECCAK256
	DUP1
	SLOAD
	DUP1
	ISZERO
	JUMPI @fresh
	CALLER
	EQ
	ISZERO
	JUMPI @revert
	JUMP @add
fresh:
	POP
	CALLER
	DUP2
	SSTORE
add:
	POP
	DUP1
	SLOAD
	CALLVALUE
	ADD
	DUP1
	CALLVALUE
	GT
	JUMPI @revert
	DUP1
	PUSH 0x00
	SLOAD
	LT
	JUMPI @revert
	SWAP1
	SSTORE
	STOP
view:
	DUP1
	SLOAD
	PUSH 0x00
	MSTORE
	DUP1
	PUSH 0x40
	MSTORE
	PUSH 0x20
	PUSH 0x40
	KECCAK256
	SLOAD
	PUSH 0x20
	MSTORE
	PUSH 0x40
	MSTORE
	PUSH 0x01
	PUSH 0x60
	MSTORE8
	PUSH 0x21
	PUSH 0x40
	KECCAK256
	SLOAD
	PUSH 0x40
	MSTORE
	PUSH 0x60
	PUSH 0x00
	RETURN
rotate:
	CALLVALUE
	JUMPI @revert
	CALLER
	ORIGIN
	EQ
	ISZERO
	JUMPI @revert
	PUSH 0x00
	CALLDATALOAD
	DUP1
	SLOAD
	ISZERO
	JUMPI @revert
	DUP1
	PUSH 0x00
	MSTORE
	PUSH 0x20
	PUSH 0x00
	KECCAK256
	SLOAD
	CALLER
	EQ
	ISZERO
	JUMPI @revert
	PUSH 0x00
	MSTORE
	PUSH 0x01
	PUSH 0x20
	MSTORE8
	PUSH 0x21
	PUSH 0x00
	KECCAK256
	PUSH 0x20
	CALLDATALOAD
	SWAP1
	SSTORE
	STOP
`

// RegistryCodeV2 is RegistryAsmV2's compilation, pinned
// (TestRegistryCodeV2Pinned); a chain with the sortition rule carries it.
var RegistryCodeV2 = common.FromHex(registryCodeV2Hex)

const registryCodeV2Hex = "36602014630000001a5736604014630000009f575b60006000fd5b6000353460001463000000765733321415630000001457806000526020600020805480156300000054573314156300000014576300000059565b503381555b508054340180341163000000145780600054106300000014579055005b805460005280604052602060402054602052604052600160605360216040205460405260606000f35b34630000001457333214156300000014576000358054156300000014578060005260206000205433141563000000145760005260016020536021600020602035905500"

// registryCodeHex is pinned by TestRegistryCodePinned from RegistryAsm.
const registryCodeHex = "366020146300000010575b60006000fd5b60003534600014630000006c5733321415630000000a5780600052602060002080548015630000004a57331415630000000a57630000004f565b503381555b5080543401803411630000000a578060005410630000000a579055005b805460005260405260206040205460205260406000f3"

// RegistryAlloc is the registry's genesis account: the code (version 2
// with the signer operation when the chain carries the sortition rule) and
// the cap.
func RegistryAlloc(cap *big.Int, v2 bool) types.Account {
	code := RegistryCode
	if v2 {
		code = RegistryCodeV2
	}
	return types.Account{
		Code:    common.CopyBytes(code),
		Storage: map[common.Hash]common.Hash{{}: common.BigToHash(cap)},
		Balance: new(big.Int),
	}
}

// Errors of the rule, each a reason a block is invalid.
var (
	errBondKeyEarly    = errors.New("igneum authority: bond key before the rule's height")
	errBondKeyMissing  = errors.New("igneum authority: bond key required from the rule's height")
	errBondKeyZero     = errors.New("igneum authority: zero bond key")
	errBondKeyUnbonded = errors.New("igneum authority: bond key is not bonded in the registry")
	errBondAccount     = errors.New("igneum authority: coinbase is not the bond's account")
	errBondKeyUnset    = errors.New("igneum authority: this node has no bond key to seal under")
)

// Bond is a registry record.
type Bond struct {
	Amount  *big.Int
	Account common.Address
}

// BondOf reads a key's bond from the registry at a state.
func (a *AuthorityParams) BondOf(st *state.StateDB, key common.Hash) Bond {
	amount := st.GetState(a.Registry, key).Big()
	account := common.BytesToAddress(st.GetState(a.Registry, crypto.Keccak256Hash(key[:])).Bytes())
	return Bond{Amount: amount, Account: account}
}

// verifyAuthorityHeader is the stateless part: the key's presence by height.
func (a *AuthorityParams) verifyAuthorityHeader(header *types.Header) error {
	height := header.Number.Uint64()
	if !a.Active(height) {
		if header.BondKey != nil {
			return errBondKeyEarly
		}
		return nil
	}
	if header.BondKey == nil {
		return errBondKeyMissing
	}
	if *header.BondKey == (common.Hash{}) {
		return errBondKeyZero
	}
	return nil
}

// VerifyAuthority is the stateful rule: at the parent's state, the header's
// key is bonded and the coinbase is the bond's account. The engine runs it on
// import (core.StateProcessor, before the block's transactions) and on
// assembly (its own block). A node with no rule accepts nothing here.
func (e *Engine) VerifyAuthority(header *types.Header, st *state.StateDB) error {
	return e.verifyAuthorityAt(header, st, true)
}

// verifyAuthorityAt is VerifyAuthority with the header's state named:
// sealed (an imported block, the seal's fields present) or an unsealed
// template (this node's own assembly before the search, where the keyed
// seal's shares cannot exist yet; the fifth development chain halted at
// its second bond when the template check demanded them, 11 Oct 2026).
func (e *Engine) verifyAuthorityAt(header *types.Header, st *state.StateDB, sealed bool) error {
	a := e.config.Parts.Authority
	if a == nil {
		if header.BondKey != nil {
			return errBondKeyEarly
		}
		return nil
	}
	if err := a.verifyAuthorityHeader(header); err != nil {
		return err
	}
	if !a.Active(header.Number.Uint64()) {
		return nil
	}
	if e.authorityLax {
		return nil
	}
	s := e.config.Parts.Sortition
	if s.Active(header.Number.Uint64()) && s.OpenSeat {
		// The genesis form: the bond is read from the epoch's table and
		// the open seat admits a key without units (sortition_genesis.go).
		return e.verifySortitionGenesis(header, st, sealed)
	}
	if s.Active(header.Number.Uint64()) && header.Round >= s.UnbondedRound {
		// The unbonded round (liveness): any signer, no bond, coinbase free.
		return e.verifySortition(header, st)
	}
	b := a.BondOf(st, *header.BondKey)
	if b.Amount.Sign() <= 0 {
		return errBondKeyUnbonded
	}
	// The coinbase is the bond's account, or, with a payout list under the
	// sortition rule, the list root's prefix (the list the signer signed is
	// where the reward goes).
	if header.WithdrawalsHash == nil && header.Coinbase != b.Account {
		return fmt.Errorf("%w: coinbase %s, bond account %s", errBondAccount, header.Coinbase.Hex(), b.Account.Hex())
	}
	return e.verifySortition(header, st)
}

// SetBondKey names the key this node seals under (nil: none; a node under
// the rule then refuses to assemble a block).
func (e *Engine) SetBondKey(key *common.Hash) {
	e.lock.Lock()
	defer e.lock.Unlock()
	if key == nil {
		e.bondKey = nil
		return
	}
	k := *key
	e.bondKey = &k
}

// BondKey returns the key this node seals under.
func (e *Engine) BondKey() *common.Hash {
	e.lock.Lock()
	defer e.lock.Unlock()
	if e.bondKey == nil {
		return nil
	}
	k := *e.bondKey
	return &k
}

// SetAuthorityLax makes this node skip the stateful rule on import and
// assembly (the header field stays): the TEST HARNESS's non-compliant node,
// which seals rule-breaking blocks for compliant nodes to refuse. Never on
// a node that is not a test fixture.
func (e *Engine) SetAuthorityLax(lax bool) {
	e.lock.Lock()
	defer e.lock.Unlock()
	e.authorityLax = lax
}

// Authority returns the rule's constants (nil without the rule).
func (e *Engine) Authority() *AuthorityParams { return e.config.Parts.Authority }

// prepareAuthority sets the header's key for a block this node assembles.
func (e *Engine) prepareAuthority(header *types.Header) error {
	a := e.config.Parts.Authority
	if !a.Active(header.Number.Uint64()) {
		header.BondKey = nil
		return nil
	}
	key := e.BondKey()
	if key == nil {
		return errBondKeyUnset
	}
	header.BondKey = key
	return nil
}

// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// RecentRoots is K: a spend's anchor must be one of the last K roots the
// pool recorded (one root a block that appended), so a proof made against
// a root K appending blocks old is refused as stale and a wallet proves
// against a fresh anchor.
const RecentRoots = 64

const addressTag = "igneum money shielded pool"

// Address is the pool's address on a chain: derived from the chain id
// alone, no code, no key; its balance is the pool's value, credited by the
// EVM on a shield and debited by Finalize on an unshield and a fee.
func Address(chainID uint64) common.Address {
	var buf [len(addressTag) + 8]byte
	copy(buf[:], addressTag)
	binary.BigEndian.PutUint64(buf[len(addressTag):], chainID)
	return common.BytesToAddress(crypto.Keccak256(buf[:])[12:])
}

// The pool's storage under its address (consensus state, in the state
// root): the leaf count, the frontier (one filled subtree a level), the
// leaves by index, the keccak of each leaf's ciphertext by index (the
// pruned-sync cut: a node that takes the retained records from a peer
// checks every ciphertext against the state), the current root, the root
// sequence and each recorded root's sequence number (the anchor check),
// the count after each block that appended (by height: the per-block
// commitment a syncing node checks a block's retained set against), the
// nullifier set and the turnstile.
func slot(name string, parts ...[]byte) common.Hash {
	b := []byte(name)
	for _, p := range parts {
		b = append(b, p...)
	}
	return crypto.Keccak256Hash(b)
}

func u64(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

// State is the pool as one block's state holds it: opened on a StateDB,
// read and written by the admissions, the root recorded once at the
// block's end.
type State struct {
	st       *state.StateDB
	addr     common.Address
	chainID  uint64
	h        Hasher
	zeros    [Depth + 1][32]byte
	appended bool
	root     [32]byte
	// parentRevoked is the revoked set's root as the state held it when the
	// pool was opened (the parent block's, for a block being built): the one
	// root a vault completion may be proven against (vault.go).
	parentRevoked [32]byte
}

// Open reads the pool at addr on chainID from st under the hash h.
func Open(st *state.StateDB, addr common.Address, chainID uint64, h Hasher) *State {
	p := &State{st: st, addr: addr, chainID: chainID, h: h, zeros: Zeros(h)}
	p.root = p.storedRoot()
	p.parentRevoked = p.RevokedRoot()
	return p
}

// Address is the pool's address.
func (p *State) Address() common.Address { return p.addr }

// Hasher is the pool's hash.
func (p *State) Hasher() Hasher { return p.h }

// ChainID is the chain the pool is on (the statements' domain).
func (p *State) ChainID() uint64 { return p.chainID }

// Count is the number of notes in the tree.
func (p *State) Count() uint64 {
	return binary.BigEndian.Uint64(p.st.GetState(p.addr, slot("count")).Bytes()[24:])
}

func (p *State) storedRoot() [32]byte {
	if p.Count() == 0 {
		return p.zeros[Depth]
	}
	return p.st.GetState(p.addr, slot("root"))
}

// Root is the tree's root after every append so far (the empty tree's
// root at count zero).
func (p *State) Root() [32]byte { return p.root }

// EmptyRoot is the empty tree's root.
func (p *State) EmptyRoot() [32]byte { return p.zeros[Depth] }

// Seq is the number of roots recorded (one a block that appended).
func (p *State) Seq() uint64 {
	return binary.BigEndian.Uint64(p.st.GetState(p.addr, slot("seq")).Bytes()[24:])
}

// Leaf is the commitment at index i (zero past the count).
func (p *State) Leaf(i uint64) [32]byte {
	return p.st.GetState(p.addr, slot("leaf", u64(i)))
}

// CiphertextHash is the keccak of the ciphertext the note at index i was
// admitted with (zero past the count).
func (p *State) CiphertextHash(i uint64) [32]byte {
	return p.st.GetState(p.addr, slot("ct", u64(i)))
}

// CountAt is the note count after the block at height appended (zero for
// a block that appended nothing, or before the landing check).
func (p *State) CountAt(height uint64) uint64 {
	return binary.BigEndian.Uint64(p.st.GetState(p.addr, slot("count-at", u64(height))).Bytes()[24:])
}

// Turnstile is the pool's value figure: shielded in plus minted in (the
// coinbase notes, coinbase.go) minus unshielded out minus fees paid out
// (the ADR's item 7). It equals the pool address's balance less any value
// a refused shield has not yet returned.
func (p *State) Turnstile() *big.Int {
	return p.st.GetState(p.addr, slot("turnstile")).Big()
}

// NullifierSeen says whether nf is in the set.
func (p *State) NullifierSeen(nf [32]byte) bool {
	return p.st.GetState(p.addr, slot("nf", nf[:])) != (common.Hash{})
}

// AnchorSeq is the sequence number a root was recorded at (0: never).
func (p *State) AnchorSeq(root [32]byte) uint64 {
	return binary.BigEndian.Uint64(p.st.GetState(p.addr, slot("anchor", root[:])).Bytes()[24:])
}

// AnchorValid says whether root is one of the last RecentRoots recorded.
func (p *State) AnchorValid(root [32]byte) bool {
	s := p.AnchorSeq(root)
	return s > 0 && s+RecentRoots > p.Seq()
}

// append puts cm at the next index with its ciphertext's keccak and
// returns the index. The frontier write at a level is made only when the
// index's lower bits are all ones there (the write any later append at
// that level would supersede is skipped), so an append writes the leaf,
// the ciphertext hash and, amortised, two frontier slots.
func (p *State) append(cm [32]byte, ciphertext []byte) uint64 {
	index := p.Count()
	cur := cm
	i := index
	for level := 0; level < Depth; level++ {
		if i&1 == 0 {
			if index&((uint64(1)<<uint(level))-1) == (uint64(1)<<uint(level))-1 {
				p.st.SetState(p.addr, slot("frontier", u64(uint64(level))), cur)
			}
			cur = p.h.Node(cur, p.zeros[level])
		} else {
			left := [32]byte(p.st.GetState(p.addr, slot("frontier", u64(uint64(level)))))
			cur = p.h.Node(left, cur)
		}
		i >>= 1
	}
	p.st.SetState(p.addr, slot("leaf", u64(index)), cm)
	p.st.SetState(p.addr, slot("ct", u64(index)), crypto.Keccak256Hash(ciphertext))
	p.st.SetState(p.addr, slot("count"), common.BytesToHash(u64(index+1)))
	p.root = cur
	p.appended = true
	return index
}

// StrayValue is the pool address's balance above its turnstile on st: value
// that arrived by a path other than a shield (F-775: a contract's internal
// call or a selfdestruct to the address, the coinbase named as the address,
// a transaction whose sender could not be read), which no spend can ever
// take out because a spend is bounded by the turnstile. Zero when the
// balance is at or under the turnstile. Pool rule: stray value is burned.
func StrayValue(st *state.StateDB, addr common.Address) *big.Int {
	balance := st.GetBalance(addr).ToBig()
	turnstile := st.GetState(addr, slot("turnstile")).Big()
	if balance.Cmp(turnstile) <= 0 {
		return new(big.Int)
	}
	return balance.Sub(balance, turnstile)
}

// BurnStray removes the pool address's stray value from its balance (credited
// to no one, as the fee is) and returns the amount; the balance then equals
// the turnstile whenever no refused shield's value is waiting to be returned.
func BurnStray(st *state.StateDB, addr common.Address) *big.Int {
	v := StrayValue(st, addr)
	if v.Sign() > 0 {
		st.SubBalance(addr, uint256.MustFromBig(v))
	}
	return v
}

// touch keeps the pool account out of EIP-161's empty-account clearing
// (an account with no nonce, no balance and no code is deleted, with its
// storage, at the block's end): the first write sets the nonce to one, so
// the tree, the nullifier set and the payout registry survive a zero
// balance (a registration before any shield, which the coinbase lab's
// first run lost at block 15; a pool whose every note is unshielded).
// Called by every admission that writes.
func (p *State) touch() {
	if p.st.GetNonce(p.addr) == 0 {
		p.st.SetNonce(p.addr, 1)
	}
}

func (p *State) setTurnstile(v *big.Int) {
	p.st.SetState(p.addr, slot("turnstile"), common.BigToHash(v))
}

// RecordRoot records the root after this block's appends (nothing when
// the block appended nothing): the root slot, the sequence and the
// anchor's sequence number. Called once by Finalize after the block's
// records.
func (p *State) RecordRoot() bool {
	if !p.appended {
		return false
	}
	seq := p.Seq() + 1
	p.st.SetState(p.addr, slot("root"), p.root)
	p.st.SetState(p.addr, slot("seq"), common.BytesToHash(u64(seq)))
	p.st.SetState(p.addr, slot("anchor", p.root[:]), common.BytesToHash(u64(seq)))
	p.appended = false
	return true
}

// RecordCountAt writes the count after the block at height (called by
// Finalize after RecordRoot returned true): the per-block commitment of
// the pruned-sync design (docs/money/PRUNED-SYNC-DESIGN-2026-10-18.md).
func (p *State) RecordCountAt(height uint64) {
	p.st.SetState(p.addr, slot("count-at", u64(height)), common.BytesToHash(u64(p.Count())))
}

// AdmitShield applies a shield carried by a transaction of value from
// sender to the pool, its proof checked by v: "" admits (the commitment
// appended at the returned index, the turnstile raised by the value), else
// the refusal's words (the value is the caller's to return); nothing is
// written on a refusal.
func (p *State) AdmitShield(sender common.Address, value *big.Int, s Shield, v Verifier) (index uint64, why string) {
	if value == nil || value.Sign() <= 0 {
		return 0, "a shield carries value"
	}
	if value.BitLen() > maxValueBits {
		return 0, "the value is past the form's bound"
	}
	if s.Commitment == ([32]byte{}) {
		return 0, "the commitment is zero"
	}
	if !IsWord(s.Commitment) {
		return 0, "the commitment is not a word"
	}
	if len(s.Ciphertext) > MaxCiphertext {
		return 0, "the ciphertext is past the form's bound"
	}
	if p.Count() >= uint64(1)<<Depth {
		return 0, "the tree is full"
	}
	if v == nil {
		v = NoVerifier{}
	}
	if err := v.Verify(s.Statement(p.chainID, value), s.Proof); err != nil {
		return 0, "the proof is refused: " + err.Error()
	}
	p.touch()
	p.setTurnstile(new(big.Int).Add(p.Turnstile(), value))
	return p.append(s.Commitment, s.Ciphertext), ""
}

// AdmitSpend applies a spend carried by a transaction of no value from
// sender to the pool, its proof checked by v: "" admits (the nullifiers
// marked, the outputs appended, the public value paid from the pool to
// the public output, the fee burned, the turnstile lowered by both), else
// the refusal's words; nothing is written on a refusal.
func (p *State) AdmitSpend(sender common.Address, value *big.Int, s Spend, v Verifier) string {
	if value != nil && value.Sign() != 0 {
		return "a spend carries no value"
	}
	if len(s.Nullifiers) != MaxInputs || len(s.Outputs) != MaxOutputs {
		return "the statement takes two inputs and two outputs"
	}
	if !IsWord(s.Anchor) {
		return "the anchor is not a word"
	}
	if s.PublicValue == nil || s.Fee == nil || s.PublicValue.Sign() < 0 || s.Fee.Sign() < 0 {
		return "the public value and the fee are non-negative"
	}
	if s.PublicValue.BitLen() > maxValueBits || s.Fee.BitLen() > maxValueBits {
		return "the public value or the fee is past the form's bound"
	}
	if s.PublicValue.Sign() > 0 && s.PublicOutput == (common.Address{}) {
		return "a public value names its output"
	}
	if s.PublicOutput == p.addr {
		// F-775: a public output at the pool would land the value back in
		// the pool's balance above the turnstile, where nothing can spend it.
		return "the pool address is no public output"
	}
	for i, o := range s.Outputs {
		if o.Commitment == ([32]byte{}) {
			return fmt.Sprintf("output %d's commitment is zero", i)
		}
		if !IsWord(o.Commitment) {
			return fmt.Sprintf("output %d's commitment is not a word", i)
		}
		if len(o.Ciphertext) > MaxCiphertext {
			return fmt.Sprintf("output %d's ciphertext is past the form's bound", i)
		}
	}
	for i, nf := range s.Nullifiers {
		if !IsWord(nf) {
			return fmt.Sprintf("nullifier %d is not a word", i)
		}
		for j := 0; j < i; j++ {
			if s.Nullifiers[j] == nf {
				return "a nullifier is named twice"
			}
		}
		if p.NullifierSeen(nf) {
			return fmt.Sprintf("nullifier %d is spent", i)
		}
	}
	if !p.AnchorValid(s.Anchor) {
		if p.AnchorSeq(s.Anchor) > 0 {
			return "the anchor is stale (not among the last recorded roots)"
		}
		return "the anchor is no recorded root"
	}
	if v == nil {
		v = NoVerifier{}
	}
	if err := v.Verify(s.Statement(p.chainID), s.Proof); err != nil {
		return "the proof is refused: " + err.Error()
	}
	out := new(big.Int).Add(s.PublicValue, s.Fee)
	turnstile := p.Turnstile()
	if turnstile.Cmp(out) < 0 {
		return "the turnstile would go negative"
	}
	if p.Count()+uint64(len(s.Outputs)) > uint64(1)<<Depth {
		return "the tree is full"
	}
	// Admitted: the writes.
	p.touch()
	for _, nf := range s.Nullifiers {
		p.st.SetState(p.addr, slot("nf", nf[:]), common.BytesToHash([]byte{1}))
	}
	for _, o := range s.Outputs {
		p.append(o.Commitment, o.Ciphertext)
	}
	p.setTurnstile(new(big.Int).Sub(turnstile, out))
	if out.Sign() > 0 {
		p.st.SubBalance(p.addr, uint256.MustFromBig(out))
	}
	if s.PublicValue.Sign() > 0 {
		// The public output paid; the fee is burned (credited to no one).
		p.st.AddBalance(s.PublicOutput, uint256.MustFromBig(s.PublicValue))
	}
	return ""
}

// Info is the pool's view (igneum_pool).
type Info struct {
	Address   common.Address `json:"address"`
	Hash      string         `json:"hash"`
	Verifier  string         `json:"verifier"`
	Gate      uint64         `json:"gate"`
	Count     uint64         `json:"count"`
	Root      common.Hash    `json:"root"`
	Seq       uint64         `json:"seq"`
	Turnstile *big.Int       `json:"turnstile"`
	Minted    *big.Int       `json:"minted"` // the coinbase notes' sum since genesis (coinbase.go)
	Balance   *big.Int       `json:"balance"`
	// The revoked set (the vault's policy hashes revealed by a policy
	// completion): its root and its count.
	RevokedRoot  common.Hash `json:"revokedRoot"`
	RevokedCount uint64      `json:"revokedCount"`
}

// View reads the pool's figures.
func (p *State) View() Info {
	return Info{
		Address:      p.addr,
		Hash:         p.h.Name(),
		Count:        p.Count(),
		Root:         common.Hash(p.root),
		Seq:          p.Seq(),
		Turnstile:    p.Turnstile(),
		Minted:       p.Minted(),
		Balance:      p.st.GetBalance(p.addr).ToBig(),
		RevokedRoot:  common.Hash(p.RevokedRoot()),
		RevokedCount: p.RevokedCount(),
	}
}

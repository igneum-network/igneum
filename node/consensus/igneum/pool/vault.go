// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// The vault's seven statements (Project Money stage two, cut one; docs/money/
// VAULT-SPEC-2026-10-18.md, docs/money/STAGE2-CUT1-VAULT-2026-10-18.md) ride
// the stage-one record form: a transaction of no value to the pool address,
// one VAULT RECORD in its data, admitted or refused by Finalize under the
// same gate as the shield and the spend (params.IgneumChainPoolFrom; zero
// is off). The node holds no vault secret and reads no policy: it checks the
// record's public words against the proof of the statement's program
// (tools/money/stage1-prover/prover/masm/vault-*.masm), the anchor, the
// nullifier and position-tag sets, the height and time rules below, and for
// a completion the revoked set's root as it stood at the parent block; then
// it marks the nullifiers and the tag, appends the new commitments and keeps
// the revoked set: an announcement reveals the old policy hash and the node
// revokes it in the announcing block (F-799: a pending withdrawal under the
// old policy is revoked at the announcement, not at the recovery's
// completion, so the revocation binds inside D under the floors); a recovery
// cancellation reveals the same hash and removes it (the kept policy's
// pending notes complete again); a policy completion reveals it and adds
// nothing (the key is there since the announcement). Value never leaves the
// pool through a vault statement: the turnstile is untouched.
const (
	VaultCreateTag         = 0x03 // two ordinary notes become a head and a change note
	VaultWithdrawTag       = 0x04 // the head is replaced; a pending note is born
	VaultCancelTag         = 0x05 // the cancellation key turns a pending note into an inbound note
	VaultCompleteTag       = 0x06 // the destination turns a matured pending note into two ordinary notes
	VaultAnnounceTag       = 0x07 // the recovery authority announces a new policy on the head
	VaultPolicyCompleteTag = 0x08 // the announced policy replaces the old one; the old hash is revoked
	VaultRecCancelTag      = 0x09 // the cancellation key clears a pending recovery

	// DeclareMargin bounds how far ahead of its including block a withdrawal
	// or an announcement may declare its height, and DeclareMarginSeconds its
	// time (the spec's section 4.2: 64 blocks, 768 seconds).
	DeclareMargin        = 64
	DeclareMarginSeconds = 768

	// RevokedDepth is the revoked set's tree depth: an indexed Merkle tree
	// over H whose leaves are [key, next_key, 0, 0], key a revoked policy
	// hash's first element, sorted by the next pointers; leaf 0 the sentinel
	// (key 0, no upper neighbour), which the empty leaf already is.
	RevokedDepth = 16
)

// IsVaultTag says whether a record tag is one of the vault's.
func IsVaultTag(tag byte) bool {
	return tag >= VaultCreateTag && tag <= VaultRecCancelTag
}

// VaultName is a vault statement's name by its tag.
func VaultName(tag byte) string {
	switch tag {
	case VaultCreateTag:
		return "money-vault-create-v1"
	case VaultWithdrawTag:
		return "money-vault-withdraw-v1"
	case VaultCancelTag:
		return "money-vault-cancel-v1"
	case VaultCompleteTag:
		return "money-vault-complete-v1"
	case VaultAnnounceTag:
		return "money-vault-announce-v1"
	case VaultPolicyCompleteTag:
		return "money-vault-policy-complete-v1"
	case VaultRecCancelTag:
		return "money-vault-rec-cancel-v1"
	}
	return ""
}

// vaultShape is the count of nullifiers and outputs each statement carries.
func vaultShape(tag byte) (nullifiers, outputs int) {
	switch tag {
	case VaultCreateTag:
		return 2, 2
	case VaultWithdrawTag, VaultCompleteTag:
		return 1, 2
	case VaultCancelTag, VaultAnnounceTag, VaultPolicyCompleteTag, VaultRecCancelTag:
		return 1, 1
	}
	return 0, 0
}

// Vault is a vault record: one layout for the seven statements, each reading
// the fields its statement names (the rest zero).
type Vault struct {
	Tag         byte
	Anchor      [32]byte   // the root the proof was made against
	Nullifiers  [][32]byte // create: the two inputs'; withdraw, announce, policy-complete, rec-cancel: the head's; cancel, complete: the pending note's
	Ptag        [32]byte   // cancel, complete: the pending note's position tag
	RevokedRoot [32]byte   // complete: the revoked set's root at the parent block
	Revealed    [32]byte   // announce, policy-complete, rec-cancel: the old policy hash (revoked at the announcement, kept at the completion, restored by the cancellation)
	Height      uint64     // withdraw, announce: h_decl; complete, policy-complete: h_now
	Time        uint64     // the time beside it
	Outputs     []Output   // create: the head and the change; withdraw: the head and the pending note; cancel: the inbound note; complete: two ordinary notes; the others: the head
	Proof       []byte
}

// Encode is the record's bytes: the tag, the anchor, the nullifier count
// and the nullifiers, the position tag, the revoked root, the revealed
// hash, the height and the time as eight bytes each, the output count and
// each output as commitment, a two-byte ciphertext length and the
// ciphertext, then the proof to the end.
func (r Vault) Encode() []byte {
	out := []byte{r.Tag}
	out = append(out, r.Anchor[:]...)
	out = append(out, byte(len(r.Nullifiers)))
	for _, nf := range r.Nullifiers {
		out = append(out, nf[:]...)
	}
	out = append(out, r.Ptag[:]...)
	out = append(out, r.RevokedRoot[:]...)
	out = append(out, r.Revealed[:]...)
	out = append(out, u64(r.Height)...)
	out = append(out, u64(r.Time)...)
	out = append(out, byte(len(r.Outputs)))
	for _, o := range r.Outputs {
		out = append(out, o.Commitment[:]...)
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(o.Ciphertext)))
		out = append(out, l[:]...)
		out = append(out, o.Ciphertext...)
	}
	return append(out, r.Proof...)
}

// ParseVault reads a vault record by its layout alone (false: not one, or a
// count or length past the form's bounds); the rules are AdmitVault's.
func ParseVault(data []byte) (Vault, bool) {
	var r Vault
	if len(data) < 1+32+1 || !IsVaultTag(data[0]) {
		return r, false
	}
	r.Tag = data[0]
	copy(r.Anchor[:], data[1:33])
	p := 33
	n := int(data[p])
	p++
	if n > MaxInputs || len(data) < p+32*n+32+32+32+8+8+1 {
		return r, false
	}
	for i := 0; i < n; i++ {
		var nf [32]byte
		copy(nf[:], data[p:p+32])
		r.Nullifiers = append(r.Nullifiers, nf)
		p += 32
	}
	copy(r.Ptag[:], data[p:p+32])
	copy(r.RevokedRoot[:], data[p+32:p+64])
	copy(r.Revealed[:], data[p+64:p+96])
	r.Height = binary.BigEndian.Uint64(data[p+96 : p+104])
	r.Time = binary.BigEndian.Uint64(data[p+104 : p+112])
	p += 112
	m := int(data[p])
	p++
	if m > MaxOutputs {
		return r, false
	}
	for i := 0; i < m; i++ {
		if len(data) < p+34 {
			return r, false
		}
		var o Output
		copy(o.Commitment[:], data[p:p+32])
		l := int(binary.BigEndian.Uint16(data[p+32 : p+34]))
		p += 34
		if l > MaxCiphertext || len(data) < p+l {
			return r, false
		}
		o.Ciphertext = append([]byte(nil), data[p:p+l]...)
		p += l
		r.Outputs = append(r.Outputs, o)
	}
	if len(data)-p > MaxProof {
		return r, false
	}
	r.Proof = append([]byte(nil), data[p:]...)
	return r, true
}

// Words are the record's public words after the domain, in the order the
// statement folds them (the prover's `public` list, prover/src/vault.rs):
//
//	create:          anchor, nf1, nf2, cm_head, cm_change
//	withdraw:        anchor, nf, cm_head_out, cm_pending, [h_decl, t_decl, 0, 0]
//	cancel:          anchor, nf, ptag, cm_inbound
//	complete:        anchor, revoked_root, nf, ptag, cm1, cm2, [h_now, t_now, 0, 0]
//	announce:        anchor, nf, cm_head_out, old_policy_hash, [h_decl, t_decl, 0, 0]
//	policy-complete: anchor, nf, cm_head_out, old_policy_hash, [h_now, t_now, 0, 0]
//	rec-cancel:      anchor, nf, cm_head_out, old_policy_hash
func (r Vault) Words() []Word {
	ht := Word{r.Height, r.Time, 0, 0}
	nf := func(i int) Word { return wordAt(r.Nullifiers, i) }
	cm := func(i int) Word {
		if i < len(r.Outputs) {
			return wordOf(r.Outputs[i].Commitment)
		}
		return Word{}
	}
	anchor := wordOf(r.Anchor)
	switch r.Tag {
	case VaultCreateTag:
		return []Word{anchor, nf(0), nf(1), cm(0), cm(1)}
	case VaultWithdrawTag:
		return []Word{anchor, nf(0), cm(0), cm(1), ht}
	case VaultCancelTag:
		return []Word{anchor, nf(0), wordOf(r.Ptag), cm(0)}
	case VaultCompleteTag:
		return []Word{anchor, wordOf(r.RevokedRoot), nf(0), wordOf(r.Ptag), cm(0), cm(1), ht}
	case VaultAnnounceTag:
		return []Word{anchor, nf(0), cm(0), wordOf(r.Revealed), ht}
	case VaultPolicyCompleteTag:
		return []Word{anchor, nf(0), cm(0), wordOf(r.Revealed), ht}
	case VaultRecCancelTag:
		return []Word{anchor, nf(0), cm(0), wordOf(r.Revealed)}
	}
	return nil
}

// Statement is the record's public inputs on a chain.
func (r Vault) Statement(chainID uint64) Statement {
	return Statement{Kind: r.Tag, ChainID: chainID, Words: r.Words()}
}

// The revoked set's storage under the pool address: the leaf count (the
// sentinel at index 0 counted once a key is inserted), the leaves by index
// and the root. The set is not append-only since F-799: an announcement
// inserts the old policy hash's key and a recovery cancellation removes it;
// the tree is rebuilt from the leaves in full either way (the set is small:
// recoveries are rare).
func (p *State) revokedCount() uint64 {
	return binary.BigEndian.Uint64(p.st.GetState(p.addr, slot("rev-count")).Bytes()[24:])
}

// RevokedLeaf is the leaf at index i of the revoked set (the zero word past
// the count; leaf 0 the sentinel).
func (p *State) RevokedLeaf(i uint64) [32]byte {
	return p.st.GetState(p.addr, slot("rev-leaf", u64(i)))
}

// RevokedCount is the number of revoked policy hashes.
func (p *State) RevokedCount() uint64 {
	if c := p.revokedCount(); c > 0 {
		return c - 1
	}
	return 0
}

// RevokedRoot is the revoked set's root as the state holds it now (the
// empty tree's root of depth RevokedDepth before the first revocation).
func (p *State) RevokedRoot() [32]byte {
	if p.revokedCount() == 0 {
		return p.revokedZeros()[RevokedDepth]
	}
	return p.st.GetState(p.addr, slot("rev-root"))
}

// ParentRevokedRoot is the revoked set's root as it stood when the pool was
// opened for this block (the parent block's state): the one root a
// completion may be proven against (the spec's section 3.5, F-778).
func (p *State) ParentRevokedRoot() [32]byte { return p.parentRevoked }

func (p *State) revokedZeros() [RevokedDepth + 1][32]byte {
	var z [RevokedDepth + 1][32]byte
	for i := 0; i < RevokedDepth; i++ {
		z[i+1] = p.h.Node(z[i], z[i])
	}
	return z
}

// RevokedLeafWord is the leaf of a key and its upper neighbour.
func RevokedLeafWord(key, next uint64) [32]byte {
	return WordBytes(Word{key, next, 0, 0})
}

// RevokedKey is the revoked set's key of a policy hash: its first element.
func RevokedKey(policyHash [32]byte) uint64 {
	return wordOf(policyHash)[0]
}

type revokedLeaf struct {
	key, next uint64
}

func (p *State) revokedLeaves() []revokedLeaf {
	n := p.revokedCount()
	if n == 0 {
		n = 1
	}
	leaves := make([]revokedLeaf, n)
	for i := uint64(0); i < n; i++ {
		w := wordOf(p.RevokedLeaf(i))
		leaves[i] = revokedLeaf{key: w[0], next: w[1]}
	}
	return leaves
}

// revokedRootOf computes the tree's root and the path of one leaf from the
// leaves in full (the set is small: recoveries are rare).
func (p *State) revokedRootOf(leaves []revokedLeaf, index uint64) ([32]byte, [][32]byte) {
	z := p.revokedZeros()
	level := make([][32]byte, len(leaves))
	for i, l := range leaves {
		level[i] = RevokedLeafWord(l.key, l.next)
	}
	var path [][32]byte
	i := index
	for d := 0; d < RevokedDepth; d++ {
		sib := z[d]
		if j := i ^ 1; j < uint64(len(level)) {
			sib = level[j]
		}
		path = append(path, sib)
		next := make([][32]byte, (len(level)+1)/2)
		for k := range next {
			r := z[d]
			if 2*k+1 < len(level) {
				r = level[2*k+1]
			}
			next[k] = p.h.Node(level[2*k], r)
		}
		level = next
		i >>= 1
	}
	if len(level) == 0 {
		return z[RevokedDepth], path
	}
	return level[0], path
}

// revokedInsert appends key to the set (the low leaf's next pointer moved to
// it, the new leaf taking the old next) and rewrites the root; a key already
// in the set changes nothing.
func (p *State) revokedInsert(key uint64) bool {
	if key == 0 {
		return false
	}
	leaves := p.revokedLeaves()
	low := 0
	for i, l := range leaves {
		if l.key == key {
			return false
		}
		if l.key < key && l.key >= leaves[low].key {
			low = i
		}
	}
	leaves = append(leaves, revokedLeaf{key: key, next: leaves[low].next})
	leaves[low].next = key
	p.st.SetState(p.addr, slot("rev-leaf", u64(uint64(low))), RevokedLeafWord(leaves[low].key, leaves[low].next))
	p.st.SetState(p.addr, slot("rev-leaf", u64(uint64(len(leaves)-1))), RevokedLeafWord(key, leaves[len(leaves)-1].next))
	p.st.SetState(p.addr, slot("rev-count"), common.BytesToHash(u64(uint64(len(leaves)))))
	root, _ := p.revokedRootOf(leaves, 0)
	p.st.SetState(p.addr, slot("rev-root"), root)
	return true
}

// revokedRemove takes key out of the set (the leaf dropped, the next
// pointers relinked over the sorted keys, the last slot cleared) and
// rewrites the root; false when the key is not in the set.
func (p *State) revokedRemove(key uint64) bool {
	if key == 0 {
		return false
	}
	leaves := p.revokedLeaves()
	kept := make([]revokedLeaf, 0, len(leaves))
	found := false
	for _, l := range leaves {
		if l.key == key {
			found = true
			continue
		}
		kept = append(kept, l)
	}
	if !found {
		return false
	}
	for i := range kept {
		next := uint64(0)
		for _, m := range kept {
			if m.key > kept[i].key && (next == 0 || m.key < next) {
				next = m.key
			}
		}
		kept[i].next = next
	}
	for i, l := range kept {
		p.st.SetState(p.addr, slot("rev-leaf", u64(uint64(i))), RevokedLeafWord(l.key, l.next))
	}
	p.st.SetState(p.addr, slot("rev-leaf", u64(uint64(len(kept)))), common.Hash{})
	p.st.SetState(p.addr, slot("rev-count"), common.BytesToHash(u64(uint64(len(kept)))))
	root, _ := p.revokedRootOf(kept, 0)
	p.st.SetState(p.addr, slot("rev-root"), root)
	return true
}

// RevokedProof is a non-membership witness for a key (igneum_poolRevokedPath):
// the root, the low leaf (the greatest key below it) with its index and
// upper neighbour, and the siblings from the leaf up; or, when the key is in
// the set, Member true and the low leaf is the key's own.
type RevokedProof struct {
	Root     common.Hash   `json:"root"`
	Count    uint64        `json:"count"`
	Member   bool          `json:"member"`
	LowIndex uint64        `json:"lowIndex"`
	LowKey   uint64        `json:"lowKey"`
	LowNext  uint64        `json:"lowNext"`
	Siblings []common.Hash `json:"siblings"`
}

// RevokedPath is the non-membership (or membership) witness of a key.
func (p *State) RevokedPath(key uint64) RevokedProof {
	leaves := p.revokedLeaves()
	low := 0
	member := false
	for i, l := range leaves {
		if l.key == key && key != 0 {
			low, member = i, true
			break
		}
		if l.key < key && l.key >= leaves[low].key {
			low = i
		}
	}
	root, path := p.revokedRootOf(leaves, uint64(low))
	out := RevokedProof{Root: common.Hash(root), Count: p.RevokedCount(), Member: member, LowIndex: uint64(low), LowKey: leaves[low].key, LowNext: leaves[low].next}
	for _, s := range path {
		out.Siblings = append(out.Siblings, common.Hash(s))
	}
	return out
}

// AdmitVault applies a vault record carried by a transaction of no value to
// the pool, its proof checked by v, under the including block's height and
// header time: "" admits (the nullifiers and the position tag marked, the
// outputs appended, the revealed hash revoked by an announcement or restored
// by a recovery cancellation), else the refusal's words; nothing is written
// on a refusal.
func (p *State) AdmitVault(sender common.Address, value *big.Int, r Vault, v Verifier, height, blockTime uint64) string {
	if value != nil && value.Sign() != 0 {
		return "a vault statement carries no value"
	}
	wantNf, wantOut := vaultShape(r.Tag)
	if wantNf == 0 || len(r.Nullifiers) != wantNf || len(r.Outputs) != wantOut {
		return fmt.Sprintf("%s takes %d nullifiers and %d outputs", VaultName(r.Tag), wantNf, wantOut)
	}
	if !IsWord(r.Anchor) {
		return "the anchor is not a word"
	}
	for i, o := range r.Outputs {
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
	for i, nf := range r.Nullifiers {
		if !IsWord(nf) {
			return fmt.Sprintf("nullifier %d is not a word", i)
		}
		for j := 0; j < i; j++ {
			if r.Nullifiers[j] == nf {
				return "a nullifier is named twice"
			}
		}
		if p.NullifierSeen(nf) {
			return fmt.Sprintf("nullifier %d is spent", i)
		}
	}
	tagged := r.Tag == VaultCancelTag || r.Tag == VaultCompleteTag
	if tagged {
		if !IsWord(r.Ptag) || r.Ptag == ([32]byte{}) {
			return "the position tag is not a word"
		}
		if p.NullifierSeen(r.Ptag) {
			return "the pending note is already cancelled or completed (its position tag is in the set)"
		}
	}
	switch r.Tag {
	case VaultWithdrawTag, VaultAnnounceTag:
		if r.Height < height || r.Height > height+DeclareMargin {
			return fmt.Sprintf("the declared height %d is not within %d of the including block %d", r.Height, DeclareMargin, height)
		}
		if r.Time < blockTime || r.Time > blockTime+DeclareMarginSeconds {
			return fmt.Sprintf("the declared time %d is not within %d of the block's %d", r.Time, DeclareMarginSeconds, blockTime)
		}
	case VaultCompleteTag, VaultPolicyCompleteTag:
		if r.Height > height || r.Time > blockTime {
			return fmt.Sprintf("the proven height %d or time %d is past the including block's (%d, %d)", r.Height, r.Time, height, blockTime)
		}
	}
	if r.Tag == VaultCompleteTag {
		if !IsWord(r.RevokedRoot) {
			return "the revoked root is not a word"
		}
		if r.RevokedRoot != p.parentRevoked {
			return "the revoked root is not the parent block's"
		}
	}
	switch r.Tag {
	case VaultAnnounceTag, VaultPolicyCompleteTag, VaultRecCancelTag:
		if !IsWord(r.Revealed) || r.Revealed == ([32]byte{}) {
			return "the revealed policy hash is not a word"
		}
		// F-799: the announcement revokes; the completion and the
		// cancellation name a policy the announcement revoked.
		if r.Tag != VaultAnnounceTag && !p.RevokedPath(RevokedKey(r.Revealed)).Member {
			return "the revealed policy hash was not revoked at the announcement"
		}
	}
	if !p.AnchorValid(r.Anchor) {
		if p.AnchorSeq(r.Anchor) > 0 {
			return "the anchor is stale (not among the last recorded roots)"
		}
		return "the anchor is no recorded root"
	}
	if v == nil {
		v = NoVerifier{}
	}
	if err := v.Verify(r.Statement(p.chainID), r.Proof); err != nil {
		return "the proof is refused: " + err.Error()
	}
	if p.Count()+uint64(len(r.Outputs)) > uint64(1)<<Depth {
		return "the tree is full"
	}
	// Admitted: the writes.
	for _, nf := range r.Nullifiers {
		p.st.SetState(p.addr, slot("nf", nf[:]), common.BytesToHash([]byte{1}))
	}
	if tagged {
		p.st.SetState(p.addr, slot("nf", r.Ptag[:]), common.BytesToHash([]byte{1}))
	}
	for _, o := range r.Outputs {
		p.append(o.Commitment, o.Ciphertext)
	}
	switch r.Tag {
	case VaultAnnounceTag:
		p.revokedInsert(RevokedKey(r.Revealed))
	case VaultRecCancelTag:
		p.revokedRemove(RevokedKey(r.Revealed))
	}
	return ""
}

// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func vaultRecord(tag byte, anchor [32]byte, nfs [][32]byte, outs ...[32]byte) Vault {
	r := Vault{Tag: tag, Anchor: anchor, Nullifiers: nfs}
	for _, o := range outs {
		r.Outputs = append(r.Outputs, Output{Commitment: o, Ciphertext: []byte{7, 7}})
	}
	return r
}

func proven(p *State, r Vault) Vault {
	r.Proof = DoubleProof(r.Statement(p.chainID), []byte("vault"))
	return r
}

// TestVaultRecordRoundTrip: every tag's record encodes and parses back
// field for field; a shield or a spend is not a vault record.
func TestVaultRecordRoundTrip(t *testing.T) {
	for tag := byte(VaultCreateTag); tag <= VaultRecCancelTag; tag++ {
		n, m := vaultShape(tag)
		r := Vault{Tag: tag, Anchor: leaf(1), Ptag: leaf(2), RevokedRoot: leaf(3), Revealed: leaf(4), Height: 1234, Time: 99999, Proof: []byte{9, 8, 7}}
		for i := 0; i < n; i++ {
			r.Nullifiers = append(r.Nullifiers, leaf(byte(10+i)))
		}
		for i := 0; i < m; i++ {
			r.Outputs = append(r.Outputs, Output{Commitment: leaf(byte(20 + i)), Ciphertext: []byte{1, 2, 3, byte(i)}})
		}
		got, ok := ParseVault(r.Encode())
		if !ok {
			t.Fatalf("tag %d: the record does not parse", tag)
		}
		if got.Tag != r.Tag || got.Anchor != r.Anchor || got.Ptag != r.Ptag || got.RevokedRoot != r.RevokedRoot || got.Revealed != r.Revealed || got.Height != r.Height || got.Time != r.Time || len(got.Nullifiers) != n || len(got.Outputs) != m || string(got.Proof) != string(r.Proof) {
			t.Fatalf("tag %d: the parsed record differs: %+v", tag, got)
		}
		for i := range r.Outputs {
			if got.Outputs[i].Commitment != r.Outputs[i].Commitment || string(got.Outputs[i].Ciphertext) != string(r.Outputs[i].Ciphertext) {
				t.Fatalf("tag %d: output %d differs", tag, i)
			}
		}
		if VaultName(tag) == "" || !IsVaultTag(tag) {
			t.Fatalf("tag %d has no name", tag)
		}
	}
	if _, ok := ParseVault([]byte{ShieldTag, 1, 2}); ok {
		t.Fatal("a shield is not a vault record")
	}
	if _, ok := ParseShield(vaultRecord(VaultCreateTag, leaf(1), nil).Encode()); ok {
		t.Fatal("a vault record is not a shield")
	}
	if _, ok := ParseSpend(vaultRecord(VaultCreateTag, leaf(1), nil).Encode()); ok {
		t.Fatal("a vault record is not a spend")
	}
}

// TestVaultAdmissions: the node's rules for the seven statements under the
// lab double (the proof's binding to the public words is the double's; the
// circuits' rules are the prover's tests'): the head's serialisation by its
// nullifier, the declared-height and time windows, the position tag's one
// consumer, the revoked root read at the parent, the revoked set's growth
// on a policy completion, and the turnstile untouched throughout.
func TestVaultAdmissions(t *testing.T) {
	st := testState(t)
	p := Open(st, Address(7404), 7404, Poseidon2{})
	// a note in the tree, the root recorded: the anchor
	if _, why := p.AdmitShield(common.Address{1}, big.NewInt(5), shieldOf(p, leaf(1), big.NewInt(5)), UnsafeDigestDouble{}); why != "" {
		t.Fatal(why)
	}
	p.RecordRoot()
	anchor := p.Root()
	turnstile := p.Turnstile()
	refused := func(name string, r Vault, height, ts uint64, want string) {
		t.Helper()
		before := p.Count()
		if why := p.AdmitVault(common.Address{1}, new(big.Int), r, UnsafeDigestDouble{}, height, ts); !strings.Contains(why, want) {
			t.Fatalf("%s: refused with %q, want %q", name, why, want)
		}
		if p.Count() != before {
			t.Fatalf("%s: a refusal wrote the tree", name)
		}
	}
	admitted := func(name string, r Vault, height, ts uint64) {
		t.Helper()
		before := p.Count()
		if why := p.AdmitVault(common.Address{1}, new(big.Int), r, UnsafeDigestDouble{}, height, ts); why != "" {
			t.Fatalf("%s: refused: %s", name, why)
		}
		if p.Count() != before+uint64(len(r.Outputs)) {
			t.Fatalf("%s: the outputs were not appended", name)
		}
		if p.Turnstile().Cmp(turnstile) != 0 {
			t.Fatalf("%s: the turnstile moved", name)
		}
	}
	// create: two nullifiers, the head and the change
	create := proven(p, vaultRecord(VaultCreateTag, anchor, [][32]byte{leaf(11), leaf(12)}, leaf(21), leaf(22)))
	withValue := create
	if why := p.AdmitVault(common.Address{1}, big.NewInt(1), withValue, UnsafeDigestDouble{}, 10, 1000); !strings.Contains(why, "no value") {
		t.Fatalf("a vault record with value: %q", why)
	}
	refused("create with one output", proven(p, vaultRecord(VaultCreateTag, anchor, [][32]byte{leaf(11), leaf(12)}, leaf(21))), 10, 1000, "takes 2 nullifiers and 2 outputs")
	refused("create against no root", proven(p, vaultRecord(VaultCreateTag, leaf(99), [][32]byte{leaf(11), leaf(12)}, leaf(21), leaf(22))), 10, 1000, "no recorded root")
	refused("create under a forged proof", vaultRecord(VaultCreateTag, anchor, [][32]byte{leaf(11), leaf(12)}, leaf(21), leaf(22)), 10, 1000, "the proof is refused")
	admitted("create", create, 10, 1000)
	refused("create replayed", create, 11, 1003, "is spent")
	// withdraw: the head's nullifier; the declared height and time windows
	w := vaultRecord(VaultWithdrawTag, anchor, [][32]byte{leaf(31)}, leaf(41), leaf(42))
	w.Height, w.Time = 9, 1003
	refused("withdraw declared below the block", proven(p, w), 10, 1003, "declared height")
	w.Height = 10 + DeclareMargin + 1
	refused("withdraw declared past the margin", proven(p, w), 10, 1003, "declared height")
	w.Height, w.Time = 10+DeclareMargin, 1003+DeclareMarginSeconds+1
	refused("withdraw's time past the margin", proven(p, w), 10, 1003, "declared time")
	w.Time = 1003 + DeclareMarginSeconds
	admitted("withdraw at the margin", proven(p, w), 10, 1003)
	// the same head consumed again (a second request, a concurrent request): refused as a duplicate
	w2 := vaultRecord(VaultWithdrawTag, anchor, [][32]byte{leaf(31)}, leaf(43), leaf(44))
	w2.Height, w2.Time = 10, 1003
	refused("second request against one head", proven(p, w2), 10, 1003, "nullifier 0 is spent")
	// cancel and complete of one pending note: whichever lands first wins by the position tag
	c := vaultRecord(VaultCancelTag, anchor, [][32]byte{leaf(51)}, leaf(61))
	c.Ptag = leaf(70)
	refused("cancel with a zero tag", proven(p, vaultRecord(VaultCancelTag, anchor, [][32]byte{leaf(51)}, leaf(61))), 12, 1010, "position tag")
	admitted("cancel", proven(p, c), 12, 1010)
	k := vaultRecord(VaultCompleteTag, anchor, [][32]byte{leaf(52)}, leaf(62), leaf(63))
	k.Ptag, k.RevokedRoot, k.Height, k.Time = leaf(70), p.ParentRevokedRoot(), 12, 1010
	refused("complete after the cancel", proven(p, k), 12, 1010, "already cancelled or completed")
	k.Ptag = leaf(71)
	k.Height = 13
	refused("complete proven past the block", proven(p, k), 12, 1010, "past the including block")
	k.Height, k.Time = 12, 1011
	refused("complete proven past the block's time", proven(p, k), 12, 1010, "past the including block")
	k.Time = 1010
	k.RevokedRoot = leaf(5)
	refused("complete against another revoked root", proven(p, k), 12, 1010, "not the parent block's")
	k.RevokedRoot = p.ParentRevokedRoot()
	if k.RevokedRoot != p.revokedZeros()[RevokedDepth] || p.RevokedCount() != 0 {
		t.Fatal("the empty revoked set's root is the empty tree's")
	}
	admitted("complete", proven(p, k), 12, 1010)
	if !p.NullifierSeen(leaf(71)) || !p.NullifierSeen(leaf(70)) {
		t.Fatal("the position tags are in the set")
	}
	// announce: the old hash enters the revoked set in the announcing block (F-799); the policy
	// completes later naming the same hash and adds nothing
	a := vaultRecord(VaultAnnounceTag, anchor, [][32]byte{leaf(81)}, leaf(91))
	a.Height, a.Time = 20, 1100
	refused("announcement revealing nothing", proven(p, a), 20, 1100, "revealed policy hash")
	old := WordBytes(Word{123456789, 5, 6, 7})
	a.Revealed = old
	parentRoot := p.ParentRevokedRoot()
	admitted("announce", proven(p, a), 20, 1100)
	if p.RevokedCount() != 1 || p.RevokedRoot() == parentRoot || p.ParentRevokedRoot() != parentRoot {
		t.Fatalf("the revoked set after the announcement: count %d, root moved %v, parent kept %v", p.RevokedCount(), p.RevokedRoot() != parentRoot, p.ParentRevokedRoot() == parentRoot)
	}
	pc := vaultRecord(VaultPolicyCompleteTag, anchor, [][32]byte{leaf(82)}, leaf(92))
	pc.Height, pc.Time = 25, 1200
	refused("policy completion revealing nothing", proven(p, pc), 25, 1200, "revealed policy hash")
	pc.Revealed = WordBytes(Word{5, 5, 5, 5})
	refused("policy completion naming an unannounced policy", proven(p, pc), 25, 1200, "not revoked at the announcement")
	pc.Revealed = old
	admitted("policy-complete", proven(p, pc), 25, 1200)
	if p.RevokedCount() != 1 {
		t.Fatalf("the policy completion adds nothing: count %d", p.RevokedCount())
	}
	// in the same block, a completion against the new root is refused (the parent's binds); against the parent's it is admitted
	k2 := vaultRecord(VaultCompleteTag, anchor, [][32]byte{leaf(53)}, leaf(64), leaf(65))
	k2.Ptag, k2.RevokedRoot, k2.Height, k2.Time = leaf(72), p.RevokedRoot(), 25, 1200
	refused("complete against the block's own new revoked root", proven(p, k2), 25, 1200, "not the parent block's")
	k2.RevokedRoot = parentRoot
	admitted("complete against the parent's revoked root", proven(p, k2), 25, 1200)
	// the next block: the new root is the parent's
	p.RecordRoot()
	q := Open(st, Address(7404), 7404, Poseidon2{})
	anchor2 := q.Root()
	if q.ParentRevokedRoot() != p.RevokedRoot() || q.ParentRevokedRoot() == parentRoot {
		t.Fatal("the next block reads the revoked root as its parent left it")
	}
	k3 := vaultRecord(VaultCompleteTag, anchor2, [][32]byte{leaf(54)}, leaf(66), leaf(67))
	k3.Ptag, k3.RevokedRoot, k3.Height, k3.Time = leaf(73), parentRoot, 26, 1210
	if why := q.AdmitVault(common.Address{1}, new(big.Int), proven(q, k3), UnsafeDigestDouble{}, 26, 1210); !strings.Contains(why, "not the parent block's") {
		t.Fatalf("a stale revoked root in the next block: %q", why)
	}
	k3.RevokedRoot = q.ParentRevokedRoot()
	if why := q.AdmitVault(common.Address{1}, new(big.Int), proven(q, k3), UnsafeDigestDouble{}, 26, 1210); why != "" {
		t.Fatal(why)
	}
	// rec-cancel: reveals the announced policy's hash and removes it (F-799)
	rc := vaultRecord(VaultRecCancelTag, anchor2, [][32]byte{leaf(83)}, leaf(93))
	if why := q.AdmitVault(common.Address{1}, new(big.Int), proven(q, rc), UnsafeDigestDouble{}, 26, 1210); !strings.Contains(why, "revealed policy hash") {
		t.Fatalf("a recovery cancellation revealing nothing: %q", why)
	}
	rc.Revealed = old
	if why := q.AdmitVault(common.Address{1}, new(big.Int), proven(q, rc), UnsafeDigestDouble{}, 26, 1210); why != "" {
		t.Fatal(why)
	}
	if q.RevokedCount() != 0 || q.RevokedRoot() != parentRoot {
		t.Fatalf("the cancellation restores the policy: count %d, root back %v", q.RevokedCount(), q.RevokedRoot() == parentRoot)
	}
	// the proof of one statement does not admit another record (the statement hash binds the words)
	mv := vaultRecord(VaultAnnounceTag, anchor2, [][32]byte{leaf(84)}, leaf(94))
	mv.Revealed, mv.Height, mv.Time = WordBytes(Word{3, 3, 3, 3}), 26, 1210
	moved := proven(q, mv)
	moved.Outputs[0].Commitment = leaf(95)
	if why := q.AdmitVault(common.Address{1}, new(big.Int), moved, UnsafeDigestDouble{}, 26, 1210); !strings.Contains(why, "the proof is refused") {
		t.Fatalf("a moved proof: %q", why)
	}
	if q.Turnstile().Cmp(turnstile) != 0 {
		t.Fatal("the turnstile moved under the vault statements")
	}
}

// TestVaultRevocationAtTheAnnouncement (F-799): rule 7's revocation never
// bound under the floors (a pending note requested before an announcement
// matures at D while the old policy was revoked only at R >= 2 D). Now the
// announcement reveals the old policy hash and the node revokes it in the
// announcing block, so from the next block (the parent-root rule, F-778) a
// completion under that policy has no non-membership proof; the policy
// completion names the same hash and adds nothing; a recovery cancellation
// names it and removes it, so the kept policy's pending notes complete
// again. The set's root returns to its earlier value on the removal.
func TestVaultRevocationAtTheAnnouncement(t *testing.T) {
	st := testState(t)
	p := Open(st, Address(7421), 7421, Poseidon2{})
	if _, why := p.AdmitShield(common.Address{1}, big.NewInt(5), shieldOf(p, leaf(1), big.NewInt(5)), UnsafeDigestDouble{}); why != "" {
		t.Fatal(why)
	}
	p.RecordRoot()
	anchor := p.Root()
	empty := p.revokedZeros()[RevokedDepth]
	old := WordBytes(Word{424242, 1, 2, 3})
	old2 := WordBytes(Word{777777, 4, 5, 6})
	admit := func(q *State, name string, r Vault, height, ts uint64) {
		t.Helper()
		if why := q.AdmitVault(common.Address{1}, new(big.Int), proven(q, r), UnsafeDigestDouble{}, height, ts); why != "" {
			t.Fatalf("%s: refused: %s", name, why)
		}
	}
	refuse := func(q *State, name string, r Vault, height, ts uint64, want string) {
		t.Helper()
		if why := q.AdmitVault(common.Address{1}, new(big.Int), proven(q, r), UnsafeDigestDouble{}, height, ts); !strings.Contains(why, want) {
			t.Fatalf("%s: refused with %q, want %q", name, why, want)
		}
	}
	// the announcement: the old policy hash revealed and revoked in its own block
	a := vaultRecord(VaultAnnounceTag, anchor, [][32]byte{leaf(81)}, leaf(91))
	a.Height, a.Time, a.Revealed = 20, 1100, old
	admit(p, "announce", a, 20, 1100)
	if p.RevokedCount() != 1 || !p.RevokedPath(RevokedKey(old)).Member {
		t.Fatalf("the announcement revokes the old policy: count %d, member %v", p.RevokedCount(), p.RevokedPath(RevokedKey(old)).Member)
	}
	if p.ParentRevokedRoot() != empty {
		t.Fatal("in the announcing block the parent's root (the empty set's) still binds")
	}
	rootOne := p.RevokedRoot()
	// the next block: the parent's root holds the key, so a completion under the old policy cannot be
	// proven against it (the circuit's non-membership path has no low leaf below the key: the prover's
	// test); the node refuses any completion against the older root
	p.RecordRoot()
	q := Open(st, Address(7421), 7421, Poseidon2{})
	anchor2 := q.Root()
	if q.ParentRevokedRoot() != rootOne {
		t.Fatal("the next block reads the revoked root the announcement left")
	}
	k := vaultRecord(VaultCompleteTag, anchor2, [][32]byte{leaf(52)}, leaf(62), leaf(63))
	k.Ptag, k.RevokedRoot, k.Height, k.Time = leaf(70), empty, 21, 1103
	refuse(q, "a completion against the pre-announcement root", k, 21, 1103, "not the parent block's")
	// the policy completion names the announced hash and adds nothing; another hash is refused
	pc := vaultRecord(VaultPolicyCompleteTag, anchor2, [][32]byte{leaf(82)}, leaf(92))
	pc.Height, pc.Time, pc.Revealed = 25, 1200, WordBytes(Word{9, 9, 9, 9})
	refuse(q, "a policy completion naming an unannounced policy", pc, 25, 1200, "not revoked at the announcement")
	pc.Revealed = old
	admit(q, "policy-complete", pc, 25, 1200)
	if q.RevokedCount() != 1 || q.RevokedRoot() != rootOne {
		t.Fatalf("the completion keeps the set as the announcement left it: count %d", q.RevokedCount())
	}
	// a second vault's announcement, then its cancellation: the key removed, the root back to rootOne
	a2 := vaultRecord(VaultAnnounceTag, anchor2, [][32]byte{leaf(85)}, leaf(96))
	a2.Height, a2.Time, a2.Revealed = 25, 1200, old2
	admit(q, "announce 2", a2, 25, 1200)
	if q.RevokedCount() != 2 || !q.RevokedPath(RevokedKey(old2)).Member {
		t.Fatalf("two revoked: count %d", q.RevokedCount())
	}
	rc := vaultRecord(VaultRecCancelTag, anchor2, [][32]byte{leaf(86)}, leaf(97))
	rc.Revealed = WordBytes(Word{9, 9, 9, 9})
	refuse(q, "a cancellation naming an unannounced policy", rc, 26, 1210, "not revoked at the announcement")
	rc.Revealed = old2
	admit(q, "rec-cancel", rc, 26, 1210)
	if q.RevokedCount() != 1 || q.RevokedPath(RevokedKey(old2)).Member || !q.RevokedPath(RevokedKey(old)).Member || q.RevokedRoot() != rootOne {
		t.Fatalf("the cancellation restores old2 and keeps old: count %d, old2 member %v, root back %v", q.RevokedCount(), q.RevokedPath(RevokedKey(old2)).Member, q.RevokedRoot() == rootOne)
	}
	// the leaves after the removal: the sentinel pointing at old's key, old's leaf with no upper neighbour
	leaves := q.revokedLeaves()
	if len(leaves) != 2 || leaves[0] != (revokedLeaf{0, RevokedKey(old)}) || leaves[1] != (revokedLeaf{RevokedKey(old), 0}) || q.RevokedLeaf(2) != ([32]byte{}) {
		t.Fatalf("the leaves after the removal: %+v", leaves)
	}
	// removing the last key leaves the empty tree's root
	rc2 := vaultRecord(VaultRecCancelTag, anchor2, [][32]byte{leaf(87)}, leaf(98))
	rc2.Revealed = old
	admit(q, "rec-cancel of the last", rc2, 26, 1210)
	if q.RevokedCount() != 0 || q.RevokedRoot() != empty {
		t.Fatal("the empty set's root after the last removal")
	}
	p.RecordRoot()
	if Open(st, Address(7421), 7421, Poseidon2{}).ParentRevokedRoot() != empty {
		t.Fatal("the next block reads the empty root")
	}
}

// TestRevokedSet: the indexed tree's insertions keep the sorted chain of
// next pointers, the root equals a full recompute, the non-membership
// witness names the right low leaf, and a member is reported as one.
func TestRevokedSet(t *testing.T) {
	st := testState(t)
	p := Open(st, Address(7404), 7404, Poseidon2{})
	for _, k := range []uint64{500, 300, 900, 300} {
		p.revokedInsert(k)
	}
	if p.RevokedCount() != 3 {
		t.Fatalf("count %d", p.RevokedCount())
	}
	leaves := p.revokedLeaves()
	want := []revokedLeaf{{0, 300}, {500, 900}, {300, 500}, {900, 0}}
	for i, l := range want {
		if leaves[i] != l {
			t.Fatalf("leaf %d is %+v, want %+v", i, leaves[i], l)
		}
	}
	var full [][32]byte
	for _, l := range leaves {
		full = append(full, RevokedLeafWord(l.key, l.next))
	}
	z := p.revokedZeros()
	level := full
	for d := 0; d < RevokedDepth; d++ {
		if len(level)%2 == 1 {
			level = append(level, z[d])
		}
		next := make([][32]byte, 0, len(level)/2)
		for i := 0; i < len(level); i += 2 {
			next = append(next, p.h.Node(level[i], level[i+1]))
		}
		level = next
	}
	if level[0] != p.RevokedRoot() {
		t.Fatal("the root differs from the full recompute")
	}
	for _, c := range []struct {
		key                      uint64
		member                   bool
		lowIndex, lowKey, lowNxt uint64
	}{{400, false, 2, 300, 500}, {100, false, 0, 0, 300}, {1000, false, 3, 900, 0}, {500, true, 1, 500, 900}, {300, true, 2, 300, 500}} {
		pr := p.RevokedPath(c.key)
		if pr.Member != c.member || pr.LowIndex != c.lowIndex || pr.LowKey != c.lowKey || pr.LowNext != c.lowNxt || len(pr.Siblings) != RevokedDepth || pr.Root != common.Hash(p.RevokedRoot()) {
			t.Fatalf("path for %d: %+v", c.key, pr)
		}
		// the siblings reach the root
		cur := RevokedLeafWord(pr.LowKey, pr.LowNext)
		i := pr.LowIndex
		for d := 0; d < RevokedDepth; d++ {
			if i&1 == 0 {
				cur = p.h.Node(cur, [32]byte(pr.Siblings[d]))
			} else {
				cur = p.h.Node([32]byte(pr.Siblings[d]), cur)
			}
			i >>= 1
		}
		if cur != p.RevokedRoot() {
			t.Fatalf("path for %d does not reach the root", c.key)
		}
	}
	if p.revokedInsert(0) {
		t.Fatal("the zero key is the sentinel, never inserted")
	}
}

// TestVaultVectors: the vault's shared vectors (tools/money/vectors/
// money-vault-vectors.json, written by money-vault-vectors with miden-crypto)
// hold for the node's statement hashes, the revoked leaf and the pinned
// program hashes' file.
func TestVaultVectors(t *testing.T) {
	text, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "tools", "money", "vectors", "money-vault-vectors.json"))
	if err != nil {
		t.Skip("the vault vectors file is not beside this checkout")
	}
	var v struct {
		Statements map[string]struct {
			Kind             byte     `json:"kind"`
			ChainID          uint64   `json:"chain_id"`
			WordsAfterDomain []string `json:"words_after_domain"`
			Hash             string   `json:"hash"`
		} `json:"statements"`
		Revoked struct {
			Leaf79 string `json:"leaf_7_9"`
			Depth  int    `json:"depth"`
		} `json:"revoked"`
		Policy struct {
			Hash string `json:"hash"`
		} `json:"policy"`
		RevokedKey struct {
			Key uint64 `json:"key_of_policy"`
		}
	}
	if err := json.Unmarshal(text, &v); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	json.Unmarshal(text, &raw)
	var rev struct {
		Key uint64 `json:"key_of_policy"`
	}
	json.Unmarshal(raw["revoked"], &rev)
	if v.Revoked.Depth != RevokedDepth {
		t.Fatalf("the revoked depth is %d here, %d in the vectors", RevokedDepth, v.Revoked.Depth)
	}
	if got := common.Bytes2Hex(func() []byte { b := RevokedLeafWord(7, 9); return b[:] }()); got != v.Revoked.Leaf79 {
		t.Fatalf("the revoked leaf [7, 9]: %s, want %s", got, v.Revoked.Leaf79)
	}
	if ph := common.HexToHash(v.Policy.Hash); RevokedKey(ph) != rev.Key {
		t.Fatalf("the revoked key of the policy hash: %d, want %d", RevokedKey(ph), rev.Key)
	}
	for name, s := range v.Statements {
		if VaultName(s.Kind) != name {
			t.Fatalf("kind %d is %q here, %q in the vectors", s.Kind, VaultName(s.Kind), name)
		}
		var words []Word
		for _, w := range s.WordsAfterDomain {
			words = append(words, wordOf(common.HexToHash(w)))
		}
		got := Statement{Kind: s.Kind, ChainID: s.ChainID, Words: words}.Hash()
		if common.Bytes2Hex(got[:]) != s.Hash {
			t.Fatalf("%s: the statement hash %x, want %s", name, got, s.Hash)
		}
	}
}

// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// The fixed vectors the node, the wallet and the prover share
// (tools/money/vectors/money-stage1-vectors.json, written by the prover's
// money-vectors with miden-crypto 0.36.1): this implementation of H and of
// the encodings must reproduce every one.
func TestSharedVectors(t *testing.T) {
	text, err := os.ReadFile("../../../../tools/money/vectors/money-stage1-vectors.json")
	if err != nil {
		t.Skip("the vectors file is not in this tree: " + err.Error())
	}
	var v struct {
		Permutation struct {
			Input  [12]uint64 `json:"input"`
			Output [12]uint64 `json:"output"`
		} `json:"permutation"`
		Merge struct {
			A, B   [4]uint64
			Out    [4]uint64 `json:"out"`
			OutHex string    `json:"out_hex"`
		} `json:"merge"`
		NodeHex    struct{ Left, Right, Out string } `json:"node_hex"`
		ValueWords []struct {
			Value string    `json:"value"`
			Limbs [4]uint64 `json:"limbs"`
		} `json:"value_words"`
		AddressWord struct {
			Address string    `json:"address"`
			Limbs   [4]uint64 `json:"limbs"`
		} `json:"address_word"`
		Keys struct {
			SkSpend string `json:"sk_spend"`
			Nk      string `json:"nk"`
			Pk      string `json:"pk"`
		} `json:"keys"`
		Note struct {
			Value, Pk, R, Cm, Nullifier string
			Index                       uint64
		} `json:"note"`
		EmptyRoots      map[string]string `json:"empty_roots"`
		ShieldStatement struct {
			ChainID uint64 `json:"chain_id"`
			Cm      string
			Value   string
			Hash    string
		} `json:"shield_statement"`
		SpendStatement struct {
			ChainID      uint64    `json:"chain_id"`
			Anchor       string    `json:"anchor"`
			Nullifiers   [2]string `json:"nullifiers"`
			Commitments  [2]string `json:"commitments"`
			PublicValue  string    `json:"public_value"`
			Fee          string    `json:"fee"`
			PublicOutput string    `json:"public_output"`
			Hash         string    `json:"hash"`
		} `json:"spend_statement"`
	}
	if err := json.Unmarshal(text, &v); err != nil {
		t.Fatal(err)
	}
	h32 := func(s string) (out [32]byte) {
		b, err := hex.DecodeString(s)
		if err != nil || len(b) != 32 {
			t.Fatalf("vector hex %q", s)
		}
		copy(out[:], b)
		return out
	}
	s := v.Permutation.Input
	permute(&s)
	if s != v.Permutation.Output {
		t.Fatalf("the permutation differs from the vectors")
	}
	m := Merge(Word(v.Merge.A), Word(v.Merge.B))
	mb := WordBytes(m)
	if m != Word(v.Merge.Out) || hex.EncodeToString(mb[:]) != v.Merge.OutHex {
		t.Fatalf("merge: got %v want %v", m, v.Merge.Out)
	}
	h := Poseidon2{}
	if h.Node(h32(v.NodeHex.Left), h32(v.NodeHex.Right)) != h32(v.NodeHex.Out) {
		t.Fatal("node")
	}
	for _, vw := range v.ValueWords {
		n, _ := new(big.Int).SetString(vw.Value, 10)
		w, ok := ValueWord(n)
		if !ok || w != Word(vw.Limbs) {
			t.Fatalf("value word %s: %v", vw.Value, w)
		}
	}
	if AddressWord(common.HexToAddress(v.AddressWord.Address)) != Word(v.AddressWord.Limbs) {
		t.Fatal("address word")
	}
	sk, nk := h32(v.Keys.SkSpend), h32(v.Keys.Nk)
	pk := WordBytes(Merge(wordOf(sk), wordOf(nk)))
	if pk != h32(v.Keys.Pk) {
		t.Fatal("pk")
	}
	val, _ := new(big.Int).SetString(v.Note.Value, 10)
	cm := h.Commitment(val, h32(v.Note.Pk), h32(v.Note.R))
	if cm != h32(v.Note.Cm) {
		t.Fatal("commitment")
	}
	if h.Nullifier(nk, cm, v.Note.Index) != h32(v.Note.Nullifier) {
		t.Fatal("nullifier")
	}
	z := Zeros(h)
	for _, lv := range []struct {
		k string
		i int
	}{{"1", 1}, {"2", 2}, {"32", 32}} {
		if z[lv.i] != h32(v.EmptyRoots[lv.k]) {
			t.Fatalf("empty root at level %s", lv.k)
		}
	}
	sv, _ := new(big.Int).SetString(v.ShieldStatement.Value, 10)
	sh := Shield{Commitment: h32(v.ShieldStatement.Cm)}
	if sh.Statement(v.ShieldStatement.ChainID, sv).Hash() != h32(v.ShieldStatement.Hash) {
		t.Fatal("shield statement hash")
	}
	sp := v.SpendStatement
	pv, _ := new(big.Int).SetString(sp.PublicValue, 10)
	fee, _ := new(big.Int).SetString(sp.Fee, 10)
	spend := Spend{Anchor: h32(sp.Anchor), Nullifiers: [][32]byte{h32(sp.Nullifiers[0]), h32(sp.Nullifiers[1])},
		Outputs:     []Output{{Commitment: h32(sp.Commitments[0])}, {Commitment: h32(sp.Commitments[1])}},
		PublicValue: pv, Fee: fee, PublicOutput: common.HexToAddress(sp.PublicOutput)}
	if spend.Statement(sp.ChainID).Hash() != h32(sp.Hash) {
		t.Fatal("spend statement hash")
	}
}

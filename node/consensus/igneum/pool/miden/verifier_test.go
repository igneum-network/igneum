// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

//go:build money_miden

package miden

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
)

// A proof made by money-prove over a synthetic witness (MONEY_PROOF and
// MONEY_WITNESS name the files; the lab writes them) verifies against the
// statement the node rebuilds from the witness's public fields, and is
// refused against a statement with one field changed, with one byte
// changed, and with the wrong kind.
func TestVerifyAgainstProverOutput(t *testing.T) {
	pf, wf := os.Getenv("MONEY_PROOF"), os.Getenv("MONEY_WITNESS")
	if pf == "" || wf == "" {
		t.Skip("MONEY_PROOF and MONEY_WITNESS name a proof and its witness")
	}
	proof, err := os.ReadFile(pf)
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		ChainID      uint64    `json:"chain_id"`
		Anchor       string    `json:"anchor"`
		Nullifiers   [2]string `json:"nullifiers"`
		Commitments  [2]string `json:"commitments"`
		PublicValue  string    `json:"public_value"`
		Fee          string    `json:"fee"`
		PublicOutput string    `json:"public_output"`
	}
	text, err := os.ReadFile(wf)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(text, &w); err != nil {
		t.Fatal(err)
	}
	h32 := func(s string) (out [32]byte) {
		b, _ := hex.DecodeString(s)
		copy(out[:], b)
		return out
	}
	pv, _ := new(big.Int).SetString(w.PublicValue, 10)
	fee, _ := new(big.Int).SetString(w.Fee, 10)
	sp := pool.Spend{Anchor: h32(w.Anchor), Nullifiers: [][32]byte{h32(w.Nullifiers[0]), h32(w.Nullifiers[1])},
		Outputs:     []pool.Output{{Commitment: h32(w.Commitments[0])}, {Commitment: h32(w.Commitments[1])}},
		PublicValue: pv, Fee: fee, PublicOutput: common.HexToAddress(w.PublicOutput)}
	v, err := New(pool.MinSecurityBits)
	if err != nil {
		t.Fatal(err)
	}
	s := sp.Statement(w.ChainID)
	started := time.Now()
	if err := v.Verify(s, proof); err != nil {
		t.Fatal(err)
	}
	t.Logf("verified in %v: %d bytes, %d conjectured bits, verifier %s", time.Since(started), len(proof), v.Bits(), v.Name())
	other := s
	other.Fee = new(big.Int).Add(fee, big.NewInt(1))
	if err := v.Verify(other, proof); err == nil {
		t.Fatal("a changed fee is refused")
	}
	other = s
	other.ChainID++
	if err := v.Verify(other, proof); err == nil {
		t.Fatal("another chain is refused")
	}
	other = s
	other.Kind = pool.ShieldTag
	if err := v.Verify(other, proof); err == nil {
		t.Fatal("the wrong kind is refused")
	}
	bad := append([]byte(nil), proof...)
	bad[len(bad)/2] ^= 1
	if err := v.Verify(s, bad); err == nil {
		t.Fatal("a flipped byte is refused")
	}
	if err := v.Verify(s, bad[:100]); err == nil {
		t.Fatal("a truncated proof is refused")
	}
	// The cost, in-process, as Finalize pays it.
	n := 20
	started = time.Now()
	for i := 0; i < n; i++ {
		if err := v.Verify(s, proof); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%d verifications: %v each", n, time.Since(started)/time.Duration(n))
}

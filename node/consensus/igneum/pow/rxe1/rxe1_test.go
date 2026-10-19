// Copyright 2026 The Igneum Authors
// This file is part of the Igneum node.
//
// The Igneum node is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The Igneum node is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the Igneum node. If not, see <http://www.gnu.org/licenses/>.

//go:build controls && cgo

package rxe1

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

const vectorsDir = "../../../../../pow/vectors/rxe1"

// A test-only document (dataset 16 MiB, cache 1 MiB): the light context is
// quick to build; the point's shape is the v0.1 loop in mode F.
const testDoc = `{"schema": 1, "mode": 1, "blocks": 1, "rounds": 32, "registers": 8, "dataset_bytes": 16777216, "cache_bytes": 1048576, "test_only": 1}`

func hash32(t *testing.T, s string) common.Hash {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		t.Fatalf("bad 32-byte hex %q", s)
	}
	return common.BytesToHash(b)
}

// TestDevOnlyAndDocument: refused outside --dev, a document is required and
// validated by the reference, the pid is the commitment.
func TestDevOnlyAndDocument(t *testing.T) {
	if _, err := pow.New(Name, []byte(testDoc)); err == nil {
		t.Fatal("built outside --dev")
	}
	if _, err := pow.NewDev(Name, nil); err == nil {
		t.Fatal("built without a document")
	}
	if _, err := pow.NewDev(Name, []byte(`{"schema": 1, "mode": 1, "blocks": 1, "rounds": 7, "registers": 8, "dataset_bytes": 16777216, "test_only": 1}`)); err == nil {
		t.Fatal("accepted rounds 7")
	}
	a, err := pow.NewDev(Name, []byte(testDoc))
	if err != nil {
		t.Fatal(err)
	}
	r := a.(*Algorithm)
	defer r.Close()
	if r.ParamsDigest() == (common.Hash{}) || r.Epoch(4095) != 0 || r.Epoch(4096) != 1 {
		t.Fatal("pid or epoch")
	}
	if r.SeedHash(0) == common.Hash(harnessEpochSeed(7310, 0)) {
		t.Fatal("the chain's epoch seed equals the harness's: the namespace rule is not in force")
	}
}

// TestCorpus runs the reference's ABI-class vectors (which carry the harness
// epoch seed) through the adapter's evaluation with that seed given
// explicitly: the plumbing (chain id bytes, header blob, nonce, mix = seed,
// hash = out) against the reference's own truth. The variant's dataset is 2
// GiB; the light context builds its 32 MiB cache once per epoch touched.
func TestCorpus(t *testing.T) {
	if os.Getenv("IGNEUM_RXE1_CORPUS") == "" {
		t.Skip("set IGNEUM_RXE1_CORPUS=1 (a 2 GiB variant's light contexts, about a minute)")
	}
	variant := "e-b1-r32-k8-d2048-p256-c32"
	var f struct {
		Params  map[string]any `json:"params"`
		Vectors []struct {
			Height uint64 `json:"height"`
			Header string `json:"header"`
			Nonce  string `json:"nonce"`
			Mix    string `json:"mix"`
			Hash   string `json:"hash"`
		} `json:"vectors"`
	}
	b, err := os.ReadFile(filepath.Join(vectorsDir, variant, "unseen.json"))
	if err != nil {
		t.Skip(err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	doc, _ := json.Marshal(f.Params)
	a, err := New(doc)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for i, v := range f.Vectors {
		if i >= 8 {
			break
		}
		nonce, _ := strconv.ParseUint(v.Nonce, 10, 64)
		epoch := a.Epoch(v.Height)
		mix, out, err := a.evalWithSeed(harnessEpochSeed(a.chainID, epoch), hash32(t, v.Header), epoch, nonce)
		if err != nil {
			t.Fatal(err)
		}
		if mix != hash32(t, v.Mix) || out != hash32(t, v.Hash) {
			t.Fatalf("vector %d: mix %x hash %x, corpus %s %s", i, mix, out, v.Mix, v.Hash)
		}
	}
}

// TestChainRule: through the chain's own rule the adapter is deterministic,
// a proof verifies under the chain's target and not above it, the epoch
// window applies, and the same inputs under the harness seed differ.
func TestChainRule(t *testing.T) {
	a, err := New([]byte(testDoc))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := common.HexToHash("0x7310")
	mix, out := a.Compute(h, 10, 5)
	mix2, out2 := a.Compute(h, 10, 5)
	if mix != mix2 || out != out2 || out == (common.Hash{}) {
		t.Fatal("not deterministic")
	}
	if err := a.Verify(h, 10, 5, mix, big.NewInt(1), false); err != nil {
		t.Fatalf("own proof at difficulty 1: %v", err)
	}
	// The chain's rule: out read big-endian must not exceed the target; at the
	// maximum difficulty only a zero result passes.
	maxD := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	if err := a.Verify(h, 10, 5, mix, maxD, false); err != pow.ErrResultAboveTarget {
		t.Fatalf("max difficulty: %v", err)
	}
	if err := a.Verify(h, 10, 6, mix, big.NewInt(1), false); err != pow.ErrInvalidMixDigest {
		t.Fatalf("wrong nonce: %v", err)
	}
	hm, _, _ := a.evalWithSeed(harnessEpochSeed(a.chainID, 0), h, 0, 5)
	if hm == mix {
		t.Fatal("the harness seed gave the chain's mix: the namespace rule is not in force")
	}
	a.Anchor(4096 * 5)
	if err := a.Verify(h, 10, 5, mix, big.NewInt(1), false); err != pow.ErrEpochNotAdmitted {
		t.Fatalf("epoch 0 at head epoch 5: %v", err)
	}
}

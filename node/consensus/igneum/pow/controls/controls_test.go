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

package controls

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

// vectorsDir is the reference's corpus, relative to this package.
const vectorsDir = "../../../../../pow/vectors"

type vectorFile struct {
	Candidate string `json:"candidate"`
	Class     string `json:"class"`
	Vectors   []struct {
		Height    uint64 `json:"height"`
		Header    string `json:"header"`
		Nonce     string `json:"nonce"`
		CtxHeight uint64 `json:"ctx_height"`
		Mix       string `json:"mix"`
		Hash      string `json:"hash"`
		Boundary  string `json:"boundary"`
		Expect    string `json:"expect"`
	} `json:"vectors"`
}

func loadVectors(t *testing.T, id, class string) *vectorFile {
	t.Helper()
	p := filepath.Join(vectorsDir, id, class+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var f vectorFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return &f
}

func hash32(t *testing.T, s string) common.Hash {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		t.Fatalf("bad 32-byte hex %q", s)
	}
	return common.BytesToHash(b)
}

func newControl(t *testing.T, id string) *Algorithm {
	t.Helper()
	a, err := pow.NewDev(id, nil)
	if err != nil {
		t.Skipf("%s: %v", id, err)
	}
	return a.(*Algorithm)
}

// TestSelfTest runs the reference's embedded vectors (every worker and the
// node run this at start).
func TestSelfTest(t *testing.T) {
	n, err := SelfTest("")
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no embedded vectors")
	}
	t.Logf("%d embedded vectors ok; %s", n, Build())
}

// TestDevOnly: the controls are refused outside --dev and take no document.
func TestDevOnly(t *testing.T) {
	for _, id := range Names {
		if _, err := pow.New(id, nil); err == nil {
			t.Errorf("%s: built outside --dev", id)
		}
		if _, err := pow.NewDev(id, []byte(`{"rounds": 1}`)); err == nil {
			t.Errorf("%s: accepted a parameter document", id)
		}
	}
}

// TestCorpus verifies the reference's vector corpus through the adapter: every
// "ok" vector recomputes its mix and passes its boundary, every other vector
// fails as the corpus says (the classes with a hash also match it).
func TestCorpus(t *testing.T) {
	for _, id := range Names {
		a := newControl(t, id)
		defer a.Close()
		checked := 0
		for _, class := range []string{"unseen", "nonce-boundary", "transitions", "malformed"} {
			f := loadVectors(t, id, class)
			if f == nil {
				continue
			}
			for i, v := range f.Vectors {
				nonce, err := strconv.ParseUint(v.Nonce, 10, 64)
				if err != nil {
					t.Fatalf("%s/%s[%d]: nonce %q", id, class, i, v.Nonce)
				}
				header := hash32(t, v.Header)
				mix := hash32(t, v.Mix)
				target := new(big.Int).Set(pow.MaxTarget())
				if v.Boundary != "" {
					target.SetBytes(common.FromHex(v.Boundary))
				}
				err = pow.VerifyTarget(a, header, v.Height, nonce, mix, target)
				switch {
				case v.Expect == "" || v.Expect == "ok":
					if err != nil {
						t.Errorf("%s/%s[%d] height %d: %v", id, class, i, v.Height, err)
					}
					if v.Hash != "" {
						if _, out := a.Compute(header, v.Height, nonce); out != hash32(t, v.Hash) {
							t.Errorf("%s/%s[%d]: final hash %x, corpus %s", id, class, i, out, v.Hash)
						}
					}
				default:
					if err == nil {
						t.Errorf("%s/%s[%d] height %d: expected %q, verified", id, class, i, v.Height, v.Expect)
					}
				}
				checked++
			}
		}
		if checked == 0 {
			t.Errorf("%s: no corpus vectors found under %s", id, vectorsDir)
		}
		t.Logf("%s: %d corpus vectors through the adapter", id, checked)
	}
}

// TestEpochs: the adapter keeps at most two contexts and recovers an evicted
// epoch by rebuilding it (a reorganisation across a boundary).
func TestEpochs(t *testing.T) {
	a := newControl(t, KawpowName)
	defer a.Close()
	h := common.HexToHash("0x01")
	for _, height := range []uint64{1, 7500, 15000, 22500, 1} {
		mix, out := a.Compute(h, height, 7)
		if mix == (common.Hash{}) || out == (common.Hash{}) {
			t.Fatalf("height %d: zero hashes", height)
		}
	}
	set := 0
	for i := range a.slot {
		if a.slot[i].set {
			set++
		}
	}
	if set != EpochCacheEntries {
		t.Fatalf("%d contexts held, want %d", set, EpochCacheEntries)
	}
	// Anchored at epoch 1: epochs 0 to 2 are admitted, 3 is refused before
	// any build, and a thrash across the window builds nothing more.
	a.Anchor(7500)
	before := a.Builds()
	if err := a.Verify(h, 22500, 7, common.Hash{1}, big.NewInt(1), false); err != pow.ErrEpochNotAdmitted {
		t.Fatalf("epoch 3 at anchor 1: %v, want not admitted", err)
	}
	if got := a.Builds(); got != before {
		t.Fatalf("a refused epoch built a context")
	}
	// The window's three epochs are built at most once each (one of them left
	// the sweep above), then a thrash across the window builds nothing.
	thrash := func() {
		for i := 0; i < 300; i++ {
			height := uint64(i%3) * 7500
			if err := a.Verify(h, height, uint64(i), common.Hash{byte(i)}, big.NewInt(1), false); err != pow.ErrInvalidMixDigest {
				t.Fatalf("height %d: %v, want invalid mix digest", height, err)
			}
		}
	}
	thrash()
	if got := a.Builds(); got > before+1 {
		t.Fatalf("the first pass over the window built %d contexts, want at most 1", got-before)
	}
	settled := a.Builds()
	thrash()
	if got := a.Builds(); got != settled {
		t.Fatalf("thrash across the window built %d contexts", got-settled)
	}
}

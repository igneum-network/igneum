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

package null

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

func TestRegistered(t *testing.T) {
	// Outside --dev the null algorithm is refused (F-02).
	if _, err := pow.New(Name, nil); !errors.Is(err, pow.ErrDevOnlyAlgorithm) {
		t.Fatalf("New(%q) outside dev: have %v want ErrDevOnlyAlgorithm", Name, err)
	}
	if !pow.IsDevOnly(Name) {
		t.Fatal("null is not registered dev-only")
	}
	a, err := pow.NewDev(Name, nil)
	if err != nil {
		t.Fatalf("NewDev(%q): %v", Name, err)
	}
	if a.Name() != Name {
		t.Fatalf("name: have %q want %q", a.Name(), Name)
	}
	if _, err := pow.NewDev("no-such-algorithm", nil); !errors.Is(err, pow.ErrUnknownAlgorithm) {
		t.Fatalf("unknown name: have %v want ErrUnknownAlgorithm", err)
	}
}

func TestComputeIsDeterministicAndBound(t *testing.T) {
	a := Algorithm{}
	h := common.HexToHash("0x0102030405060708091011121314151617181920212223242526272829303132")
	m1, r1 := a.Compute(h, 7, 42)
	m2, r2 := a.Compute(h, 7, 42)
	if m1 != m2 || r1 != r2 {
		t.Fatal("Compute is not deterministic")
	}
	// Every input byte must change the output: header hash, height, nonce.
	if m, _ := a.Compute(common.HexToHash("0x01"), 7, 42); m == m1 {
		t.Fatal("mix does not depend on the header hash")
	}
	if m, _ := a.Compute(h, 8, 42); m == m1 {
		t.Fatal("mix does not depend on the height")
	}
	if m, _ := a.Compute(h, 7, 43); m == m1 {
		t.Fatal("mix does not depend on the nonce")
	}
}

func TestSolveVerifyRoundTrip(t *testing.T) {
	a := Algorithm{}
	h := common.HexToHash("0xdeadbeef")
	difficulty := big.NewInt(1 << 12) // one in 4096 nonces; fast on any CPU
	nonce, mix, ok := pow.Solve(a, h, 1, difficulty, 0, nil)
	if !ok {
		t.Fatal("Solve did not find a nonce")
	}
	if err := a.Verify(h, 1, nonce, mix, difficulty, false); err != nil {
		t.Fatalf("Verify of a solved nonce: %v", err)
	}
	// Wrong mix digest.
	if err := a.Verify(h, 1, nonce, common.Hash{}, difficulty, false); !errors.Is(err, pow.ErrInvalidMixDigest) {
		t.Fatalf("wrong mix: have %v want ErrInvalidMixDigest", err)
	}
	// Wrong height binds: the same nonce at another height has another mix.
	if err := a.Verify(h, 2, nonce, mix, difficulty, false); err == nil {
		t.Fatal("a solution for height 1 verified at height 2")
	}
	// A much harder target rejects the same solution (with overwhelming probability).
	hard := new(big.Int).Lsh(big.NewInt(1), 200)
	if err := a.Verify(h, 1, nonce, mix, hard, false); !errors.Is(err, pow.ErrResultAboveTarget) {
		t.Fatalf("hard target: have %v want ErrResultAboveTarget", err)
	}
	// Non-positive difficulty.
	if err := a.Verify(h, 1, nonce, mix, big.NewInt(0), false); !errors.Is(err, pow.ErrInvalidDifficulty) {
		t.Fatalf("zero difficulty: have %v want ErrInvalidDifficulty", err)
	}
}

func TestSolveAbort(t *testing.T) {
	abort := make(chan struct{})
	close(abort)
	// An impossible target: only abort can end the search.
	if _, _, ok := pow.Solve(Algorithm{}, common.Hash{}, 1, new(big.Int).Lsh(big.NewInt(1), 255), 0, abort); ok {
		t.Fatal("Solve returned ok after abort")
	}
}

// TestVectors checks every frozen vector. With no frozen vectors it fails
// under IGNEUM_REQUIRE_FIXTURES=1 (a mandatory fixture that is missing must
// fail, the plan page 24) and skips otherwise.
func TestVectors(t *testing.T) {
	a := Algorithm{}
	vs := a.Vectors()
	if len(vs) == 0 {
		if os.Getenv("IGNEUM_REQUIRE_FIXTURES") != "" {
			t.Fatal("no frozen vectors for the null algorithm (run TestGenerateVectors with IGNEUM_WRITE_VECTORS=1 and freeze the output)")
		}
		t.Skip("no frozen vectors yet")
	}
	for _, v := range vs {
		mix, result := a.Compute(v.HeaderHash, v.Height, v.Nonce)
		if mix != v.MixDigest || result != v.Result {
			t.Errorf("vector %s: have (%x, %x) want (%x, %x)", v.Name, mix, result, v.MixDigest, v.Result)
		}
	}
}

// TestGenerateVectors prints the vectors to freeze into frozenVectors when
// IGNEUM_WRITE_VECTORS=1. It is a generator, not a check.
func TestGenerateVectors(t *testing.T) {
	if os.Getenv("IGNEUM_WRITE_VECTORS") == "" {
		t.Skip("set IGNEUM_WRITE_VECTORS=1 to print the vectors")
	}
	a := Algorithm{}
	inputs := []pow.Vector{
		{Name: "zero", HeaderHash: common.Hash{}, Height: 0, Nonce: 0},
		{Name: "one", HeaderHash: common.HexToHash("0x01"), Height: 1, Nonce: 1},
		{Name: "max-nonce", HeaderHash: common.HexToHash("0xff"), Height: 1 << 40, Nonce: ^uint64(0)},
		{Name: "pattern", HeaderHash: common.HexToHash("0x0102030405060708091011121314151617181920212223242526272829303132"), Height: 123456789, Nonce: 0x0123456789abcdef},
	}
	for _, v := range inputs {
		v.MixDigest, v.Result = a.Compute(v.HeaderHash, v.Height, v.Nonce)
		fmt.Printf("{Name: %q, HeaderHash: common.HexToHash(%q), Height: %d, Nonce: %#x, MixDigest: common.HexToHash(%q), Result: common.HexToHash(%q)},\n",
			v.Name, v.HeaderHash.Hex(), v.Height, v.Nonce, v.MixDigest.Hex(), v.Result.Hex())
	}
}

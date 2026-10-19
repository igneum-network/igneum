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

// Package null is the stand-in proof of work for unit tests and devnets
// (docs/protocol-a/DESIGN.md 4.1, D-A10). It is registered development-only:
// a node refuses to start on a genesis naming it unless --dev is set (F-02). It is not a mining algorithm and no
// launch genesis names it: it has no dataset, no memory hardness and a trivially
// cheap verification. It exists so that everything around the pow.Algorithm
// boundary (header rules, sealing, the mining RPC, block import, reorgs, the
// devnet) is built and tested before Phase Zero selects the real algorithm.
//
// Definition, for every backend that wants to reproduce it:
//
//	mix    = keccak256(headerHash || be64(height) || le64(nonce))
//	result = keccak256(mix || headerHash)
//
// where be64 is the big-endian and le64 the little-endian 8-byte encoding. The
// nonce is little-endian to match the ethash convention workers already use.
package null

import (
	"encoding/binary"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/crypto"
)

// Name is the registered name of this algorithm.
const Name = "null"

func init() {
	pow.RegisterDevOnly(Name, func(params []byte) (pow.Algorithm, error) {
		return Algorithm{}, nil
	})
}

// Algorithm implements pow.Algorithm. It is stateless.
type Algorithm struct{}

// Name implements pow.Algorithm.
func (Algorithm) Name() string { return Name }

// Epoch implements pow.Algorithm: no dataset, so epoch 0 everywhere.
func (Algorithm) Epoch(height uint64) uint64 { return 0 }

// SeedHash implements pow.Algorithm: no dataset, so the zero hash.
func (Algorithm) SeedHash(height uint64) common.Hash { return common.Hash{} }

// Compute implements pow.Algorithm.
func (Algorithm) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	var buf [common.HashLength + 16]byte
	copy(buf[:common.HashLength], headerHash[:])
	binary.BigEndian.PutUint64(buf[common.HashLength:common.HashLength+8], height)
	binary.LittleEndian.PutUint64(buf[common.HashLength+8:], nonce)
	mix := crypto.Keccak256Hash(buf[:])
	result := crypto.Keccak256Hash(mix[:], headerHash[:])
	return mix, result
}

// Verify implements pow.Algorithm. fullDataset is ignored: there is no dataset.
func (a Algorithm) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	return pow.Verify(a, headerHash, height, nonce, mixDigest, difficulty)
}

// Vectors implements pow.Algorithm: the frozen HW-01 vectors (regenerate with
// TestGenerateVectors under IGNEUM_WRITE_VECTORS=1 if the definition changes,
// which is a consensus change).
func (Algorithm) Vectors() []pow.Vector { return frozenVectors }

// frozenVectors are the HW-01 vectors, generated on the build box on 9 October 2026
// (Go 1.27.2) and frozen here; every backend reproducing the null algorithm
// must match them byte for byte.
var frozenVectors = []pow.Vector{
	{Name: "zero", HeaderHash: common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"), Height: 0, Nonce: 0x0, MixDigest: common.HexToHash("0xc980e59163ce244bb4bb6211f48c7b46f88a4f40943e84eb99bdc41e129bd293"), Result: common.HexToHash("0xc4b92ebaf240cc21f71044ad7a470dd24fc3aef5d2ef4a05bd2435a5ba970ab6")},
	{Name: "one", HeaderHash: common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"), Height: 1, Nonce: 0x1, MixDigest: common.HexToHash("0xa49ad3687c629a6cf4240a3a9b993f7de5248149cafd7411d8951cf9867acf10"), Result: common.HexToHash("0xe6f420148dcdc9f1198b7c3cc64b18a15cb60603dc6848a84e55a8bd68d9e9ba")},
	{Name: "max-nonce", HeaderHash: common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000ff"), Height: 1099511627776, Nonce: 0xffffffffffffffff, MixDigest: common.HexToHash("0x3e718513c97e1b2990e7ab21eb9079bda264571b65e2c3efd5d5554fd7aedd93"), Result: common.HexToHash("0xb9227d4986232fb90b84e93c6b459a27a8a75cf7ce11d8c53b7ab8d7a4630982")},
	{Name: "pattern", HeaderHash: common.HexToHash("0x0102030405060708091011121314151617181920212223242526272829303132"), Height: 123456789, Nonce: 0x123456789abcdef, MixDigest: common.HexToHash("0x8f27b594e46c3706375a3e59e9577a3a9143735e5597c218dbe9f74722439e9f"), Result: common.HexToHash("0x0988ee95baa64958ba329ff2df12e9b77b20067b35365f78cbd27e18ccfbd7f1")},
}

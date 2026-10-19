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

package ethapi

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/blocktest"
)

// TestRPCMarshalBlockBodyLists: the block's igneum lists (the carried
// shares here) reach the JSON with and without the transactions (the work
// interface seat's finding of 12 October 2026: RPCMarshalBlock set them and
// the intermediate form dropped them).
func TestRPCMarshalBlockBodyLists(t *testing.T) {
	t.Parallel()
	sh := types.MinedShare{Address: common.Address{0x11}, ExtraNonce: 5, Nonce: 9, MixDigest: common.Hash{0x22}}
	block := types.NewBlock(&types.Header{Number: big.NewInt(100)}, nil, nil, nil, blocktest.NewHasher()).WithMinedShares(sh.Bytes())
	for _, inclTx := range []bool{false, true} {
		out, err := json.Marshal(RPCMarshalBlock(block, inclTx, false, nil))
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if _, ok := got["minedShares"]; !ok {
			t.Fatalf("inclTx %v: the JSON has no minedShares field: %s", inclTx, out)
		}
		if _, ok := got["pi"]; ok {
			t.Fatalf("inclTx %v: an empty proof was written", inclTx)
		}
	}
}

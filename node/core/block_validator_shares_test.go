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

package core

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
)

// stepEngine is an engine that names the chain's share form.
type stepEngine struct {
	consensus.Engine
	max uint64
}

func (e stepEngine) ShareStepMax() uint64 { return e.max }

// TestValidateSharesBodyForms (the ninth development chain's stall at 110,
// 12 October 2026): a body in the step form passes under a step-form
// chain and is malformed under a count-form chain, and the count form the
// other way round; an empty list under a commitment and a list without one
// are refused.
func TestValidateSharesBodyForms(t *testing.T) {
	sh := types.MinedShare{Address: common.Address{0x11}, ExtraNonce: 5, Nonce: 9, MixDigest: common.Hash{0x22}, Step: 3}
	var countBody, stepBody []byte
	for i := 0; i < 3; i++ {
		sh.Nonce = uint64(9 + i)
		countBody = append(countBody, sh.BytesOf(false)...)
		stepBody = append(stepBody, sh.BytesOf(true)...)
	}
	stepChain := stepEngine{max: 32}
	countChain := stepEngine{max: 0}
	headerFor := func(body []byte) *types.Header {
		root := types.MinedSharesRoot(body)
		return &types.Header{SharesRoot: &root}
	}
	if err := validateSharesBody(stepChain, headerFor(stepBody), stepBody); err != nil {
		t.Fatalf("a step-form body under the step-form chain: %v", err)
	}
	if err := validateSharesBody(countChain, headerFor(countBody), countBody); err != nil {
		t.Fatalf("a count-form body under the count-form chain: %v", err)
	}
	if err := validateSharesBody(countChain, headerFor(stepBody), stepBody); err == nil {
		t.Fatal("a step-form body passed under the count-form chain")
	}
	if err := validateSharesBody(stepChain, headerFor(countBody), countBody); err == nil {
		t.Fatal("a count-form body passed under the step-form chain")
	}
	if err := validateSharesBody(stepChain, headerFor(nil), nil); err == nil {
		t.Fatal("an empty list under a commitment passed")
	}
	if err := validateSharesBody(stepChain, &types.Header{}, stepBody); err == nil {
		t.Fatal("a list without a commitment passed")
	}
	if err := validateSharesBody(stepChain, &types.Header{}, nil); err != nil {
		t.Fatalf("no list, no commitment: %v", err)
	}
}

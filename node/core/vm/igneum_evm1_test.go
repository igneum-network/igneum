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

package vm

import (
	"math/big"
	"reflect"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// TestIgneumEVM1 pins the exact EVM rule set the Igneum configuration yields
// at block 0 (docs/protocol-a/DESIGN.md 6.3, PA-14): the opcodes present and
// absent, the semantics of the shared opcode numbers, and the precompile set.
// Any later drift in the config or the activators fails here first.
func TestIgneumEVM1(t *testing.T) {
	var (
		block0 = big.NewInt(0)
		time0  = uint64(0)
		cfg    = params.IgneumDevChainConfig
	)
	jt, err := LookupInstructionSet(cfg, block0, &time0)
	if err != nil {
		t.Fatal(err)
	}
	present := map[OpCode]string{
		PUSH0:          "EIP-3855",
		TLOAD:          "EIP-1153",
		TSTORE:         "EIP-1153",
		MCOPY:          "EIP-5656",
		BASEFEE:        "EIP-3198",
		CHAINID:        "EIP-1344",
		SELFBALANCE:    "EIP-1884",
		CREATE2:        "EIP-1014",
		EXTCODEHASH:    "EIP-1052",
		SHL:            "EIP-145",
		STATICCALL:     "EIP-214",
		REVERT:         "EIP-140",
		RETURNDATASIZE: "EIP-211",
		DELEGATECALL:   "EIP-7",
		DIFFICULTY:     "Byzantium opcode 0x44, difficulty semantics",
	}
	// Every slot of the table is filled (validate panics otherwise); an absent
	// opcode executes opUndefined, so presence is "not opUndefined".
	fp := func(f interface{}) uintptr { return reflect.ValueOf(f).Pointer() }
	undefined := fp(opUndefined)
	for op, eip := range present {
		if jt[op] == nil || fp(jt[op].execute) == undefined {
			t.Errorf("%s (%s) is not in the EVM-1 instruction set", op, eip)
		}
	}
	absent := map[OpCode]string{
		BLOBHASH:    "EIP-4844",
		BLOBBASEFEE: "EIP-7516",
	}
	for op, eip := range absent {
		if jt[op] != nil && fp(jt[op].execute) != undefined {
			t.Errorf("%s (%s) is in the EVM-1 instruction set; it must not be", op, eip)
		}
	}
	// Semantics of the shared numbers: 0x44 is DIFFICULTY (opDifficulty), not
	// PREVRANDAO (opRandom); SELFDESTRUCT is the EIP-6780 form; CREATE and
	// CREATE2 carry the EIP-3860 initcode limit.
	if fp(jt[DIFFICULTY].execute) != fp(opDifficulty) {
		t.Error("0x44 does not execute opDifficulty (PREVRANDAO is not in EVM-1)")
	}
	if fp(jt[SELFDESTRUCT].execute) != fp(opSelfdestruct6780) {
		t.Error("SELFDESTRUCT is not the EIP-6780 form")
	}
	if fp(jt[CREATE].dynamicGas) != fp(gasCreateEip3860) || fp(jt[CREATE2].dynamicGas) != fp(gasCreate2Eip3860) {
		t.Error("CREATE or CREATE2 does not meter initcode per EIP-3860")
	}
	// Precompiles: exactly 0x01 to 0x09; no 0x0a (point evaluation), no BLS.
	pre := PrecompiledContractsForConfig(cfg, block0, &time0)
	var have []int
	for addr := range pre {
		have = append(have, int(addr[len(addr)-1]))
	}
	sort.Ints(have)
	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("precompile set: have %v want %v", have, want)
	}
	if _, ok := pre[common.BytesToAddress([]byte{0x0a})]; ok {
		t.Error("0x0a (KZG point evaluation) is present")
	}
}

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

package params

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params/types/coregeth"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestGenesisHashIgneumDev freezes the development genesis hash (L2 golden
// test). While IgneumDevGenesisHash is zero it reports the computed hash and
// fails only under IGNEUM_REQUIRE_FIXTURES (PA-19).
func TestGenesisHashIgneumDev(t *testing.T) {
	block := genesisToBlock(DefaultIgneumDevGenesisBlock(), nil)
	if IgneumDevGenesisHash == (common.Hash{}) {
		t.Logf("IgneumDevGenesisHash is not frozen; computed: %s", block.Hash().Hex())
		if os.Getenv("IGNEUM_REQUIRE_FIXTURES") != "" {
			t.Fatal("the development genesis hash must be frozen")
		}
		return
	}
	if block.Hash() != IgneumDevGenesisHash {
		t.Errorf("want: %s, got: %s", IgneumDevGenesisHash.Hex(), block.Hash().Hex())
	}
}

// TestIsIgneumDevGenesis is F-02's second refusal: --dev accepts only an
// Igneum development genesis.
func TestIsIgneumDevGenesis(t *testing.T) {
	if !IsIgneumDevGenesis(DefaultIgneumDevGenesisBlock()) {
		t.Fatal("the default dev genesis is not recognised")
	}
	faucet := common.HexToAddress("0x01")
	if g := IgneumDevGenesisBlock(0); !IsIgneumDevGenesis(g) || g.Alloc[IgneumDevFaucet].Balance.Cmp(IgneumDevFaucetBalance) != 0 {
		t.Fatal("the dev genesis is not recognised or the faucet is not funded")
	}
	if faucet == IgneumDevFaucet {
		t.Fatal("test address collides with the faucet")
	}
	if IsIgneumDevGenesis(DefaultIgneumGenesisBlock()) || IsIgneumDevGenesis(nil) {
		t.Fatal("a non-Igneum genesis is accepted as the dev genesis")
	}
	other := DefaultIgneumDevGenesisBlock()
	cfg := *IgneumDevChainConfig
	cfg.Igneum = &ctypes.IgneumConfig{Algorithm: "not-null"}
	other.Config = &cfg
	if IsIgneumDevGenesis(other) {
		t.Fatal("a dev-shaped genesis naming another algorithm is accepted")
	}
	// A development chain sealed by the IgneumHash research candidate
	// (--dev.algorithm igneumhash) restarts under --dev.
	cfg.Igneum = &ctypes.IgneumConfig{Algorithm: "igneumhash"}
	if !IsIgneumDevGenesis(other) {
		t.Fatal("a dev genesis on the IgneumHash candidate is refused")
	}
}

// TestIgneumDevGenesisShape checks the identity fields of DESIGN.md 6.1.
func TestIgneumDevGenesisShape(t *testing.T) {
	g := DefaultIgneumDevGenesisBlock()
	// The development chain funds exactly its fixed, dev-only faucet; the launch
	// placeholder funds nobody (no premine, page 19).
	if len(g.Alloc) != 1 || g.Alloc[IgneumDevFaucet].Balance == nil {
		t.Fatalf("dev allocation: %d accounts, want the faucet alone", len(g.Alloc))
	}
	if launch := DefaultIgneumGenesisBlock(); len(launch.Alloc) != 0 {
		t.Fatalf("premine: %d accounts in the launch allocation, want 0", len(launch.Alloc))
	}
	if IgneumDevFaucet != crypto.PubkeyToAddress(IgneumDevFaucetKey().PublicKey) {
		t.Fatal("the faucet address does not match its key")
	}
	if len(g.ExtraData) > 32 {
		t.Fatalf("extra data %d bytes, want at most 32", len(g.ExtraData))
	}
	if g.GasLimit != 16_000_000 {
		t.Fatalf("gas limit %d, want 16000000", g.GasLimit)
	}
	if g.BaseFee == nil || g.BaseFee.Sign() <= 0 {
		t.Fatal("EIP-1559 is active from block 0: the genesis needs a base fee")
	}
	if nid := g.Config.GetNetworkID(); g.Config.GetChainID().Cmp(big.NewInt(7310)) != 0 || nid == nil || *nid != 7310 {
		t.Fatalf("chain/network id: %v/%v, want 7310/7310", g.Config.GetChainID(), nid)
	}
	if g.Config.GetConsensusEngineType() != ctypes.ConsensusEngineT_Igneum {
		t.Fatalf("engine type %v, want igneum", g.Config.GetConsensusEngineType())
	}
	if g.Config.GetIgneumConfig().Algorithm != "null" {
		t.Fatalf("dev algorithm %q, want null", g.Config.GetIgneumConfig().Algorithm)
	}
}

// TestIgneumEVM1RuleSet checks that every supported rule is active at block 0
// and every unsupported one is absent (DESIGN.md 6.3).
func TestIgneumEVM1RuleSet(t *testing.T) {
	c := IgneumDevChainConfig
	zero := big.NewInt(0)
	active := map[string]func() *uint64{
		"EIP2":    c.GetEIP2Transition,
		"EIP7":    c.GetEIP7Transition,
		"EIP150":  c.GetEIP150Transition,
		"EIP155":  c.GetEIP155Transition,
		"EIP160":  c.GetEIP160Transition,
		"EIP161d": c.GetEIP161dTransition,
		"EIP170":  c.GetEIP170Transition,
		"EIP140":  c.GetEIP140Transition,
		"EIP198":  c.GetEIP198Transition,
		"EIP211":  c.GetEIP211Transition,
		"EIP212":  c.GetEIP212Transition,
		"EIP213":  c.GetEIP213Transition,
		"EIP214":  c.GetEIP214Transition,
		"EIP658":  c.GetEIP658Transition,
		"EIP145":  c.GetEIP145Transition,
		"EIP1014": c.GetEIP1014Transition,
		"EIP1052": c.GetEIP1052Transition,
		"EIP152":  c.GetEIP152Transition,
		"EIP1108": c.GetEIP1108Transition,
		"EIP1344": c.GetEIP1344Transition,
		"EIP1884": c.GetEIP1884Transition,
		"EIP2028": c.GetEIP2028Transition,
		"EIP2200": c.GetEIP2200Transition,
		"EIP2565": c.GetEIP2565Transition,
		"EIP2718": c.GetEIP2718Transition,
		"EIP2929": c.GetEIP2929Transition,
		"EIP2930": c.GetEIP2930Transition,
		"EIP1559": c.GetEIP1559Transition,
		"EIP3198": c.GetEIP3198Transition,
		"EIP3529": c.GetEIP3529Transition,
		"EIP3541": c.GetEIP3541Transition,
		"EIP3651": c.GetEIP3651Transition,
		"EIP3855": c.GetEIP3855Transition,
		"EIP3860": c.GetEIP3860Transition,
		"EIP1153": c.GetEIP1153Transition,
		"EIP5656": c.GetEIP5656Transition,
		"EIP6780": c.GetEIP6780Transition,
	}
	for name, get := range active {
		if n := get(); n == nil || *n != 0 {
			t.Errorf("%s: want active at block 0, have %v", name, n)
		}
		if !c.IsEnabled(get, zero) {
			t.Errorf("%s: IsEnabled at block 0 is false", name)
		}
	}
	absent := map[string]func() *uint64{
		"EIP4399 PREVRANDAO":  c.GetEIP4399Transition,
		"EIP4895 withdrawals": c.GetEIP4895Transition,
		"EIP2537 BLS":         c.GetEIP2537Transition,
		"EIP2315 subroutines": c.GetEIP2315Transition,
		"EIP779 DAO fork":     c.GetEthashEIP779Transition,
		"EIP649 bomb delay":   c.GetEthashEIP649Transition,
		"EIP1234 bomb delay":  c.GetEthashEIP1234Transition,
		"EIP2384 bomb delay":  c.GetEthashEIP2384Transition,
		"EIP3554 bomb delay":  c.GetEthashEIP3554Transition,
		"EIP4345 bomb delay":  c.GetEthashEIP4345Transition,
		"EIP5133 bomb delay":  c.GetEthashEIP5133Transition,
	}
	for name, get := range absent {
		if n := get(); n != nil {
			t.Errorf("%s: want absent, have %d", name, *n)
		}
	}
	for name, get := range map[string]func() *uint64{
		"EIP4895 time": c.GetEIP4895TransitionTime,
	} {
		if n := get(); n != nil {
			t.Errorf("%s: want absent, have %d", name, *n)
		}
	}
	if ttd := c.GetEthashTerminalTotalDifficulty(); ttd != nil {
		t.Errorf("terminal total difficulty: want absent, have %v", ttd)
	}
}

// TestIgneumDevConfigJSONRoundTrip checks the configuration survives the JSON
// form a genesis file uses, engine block included.
func TestIgneumDevConfigJSONRoundTrip(t *testing.T) {
	data, err := json.Marshal(IgneumDevChainConfig)
	if err != nil {
		t.Fatal(err)
	}
	var back coregeth.CoreGethChainConfig
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Igneum == nil || back.Igneum.Algorithm != "null" {
		t.Fatalf("engine block lost in JSON: %s", data)
	}
	if back.Clique != nil || back.Lyra2 != nil {
		t.Fatalf("another engine block appeared in JSON: %s", data)
	}
	if back.GetConsensusEngineType() != ctypes.ConsensusEngineT_Igneum {
		t.Fatalf("engine type after round trip: %v", back.GetConsensusEngineType())
	}
	again, err := json.Marshal(&back)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Fatalf("JSON not stable:\n%s\n%s", data, again)
	}
}

// TestIgneumDevFaucetBounds is F-11: the faucet's allocation is 10^27 wei,
// far below the point where block rewards could wrap a balance, and the
// default development etherbase is not the faucet.
func TestIgneumDevFaucetBounds(t *testing.T) {
	want := new(big.Int).Exp(big.NewInt(10), big.NewInt(27), nil)
	if IgneumDevFaucetBalance.Cmp(want) != 0 {
		t.Fatalf("dev faucet allocation %v, want 10^27 wei", IgneumDevFaucetBalance)
	}
	if IgneumDevEtherbase == IgneumDevFaucet || IgneumDevEtherbase == (common.Address{}) {
		t.Fatalf("dev etherbase %s must be neither the faucet %s nor empty", IgneumDevEtherbase.Hex(), IgneumDevFaucet.Hex())
	}
	if _, funded := DefaultIgneumDevGenesisBlock().Alloc[IgneumDevEtherbase]; funded {
		t.Fatal("the dev etherbase must not be funded at genesis")
	}
}

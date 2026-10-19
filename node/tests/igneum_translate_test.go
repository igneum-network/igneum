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

package tests

import (
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestFixtureTranslationActivations asserts, for every configuration in
// Forks, that every fork activation (each Get*Transition and
// Get*TransitionTime getter of the configurator) of the translated
// configuration equals the fixture's, and that the engine block is Igneum's.
func TestFixtureTranslationActivations(t *testing.T) {
	iface := reflect.TypeOf((*ctypes.ChainConfigurator)(nil)).Elem()
	var getters []string
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		if !strings.HasPrefix(m.Name, "Get") || !strings.Contains(m.Name, "Transition") {
			continue
		}
		if m.Type.NumIn() != 0 || m.Type.NumOut() != 1 || m.Type.Out(0) != reflect.TypeOf((*uint64)(nil)) {
			continue
		}
		getters = append(getters, m.Name)
	}
	if len(getters) < 40 {
		t.Fatalf("only %d transition getters found on the configurator", len(getters))
	}
	for name, cfg := range Forks {
		fx, err := translateFixtureConfig(name, cfg)
		if err != nil {
			if _, ok := err.(UnsupportedForkError); ok {
				continue
			}
			t.Fatalf("%s: %v", name, err)
		}
		if fx.Config.GetConsensusEngineType() != ctypes.ConsensusEngineT_Igneum {
			t.Errorf("%s: engine type %v, want igneum", name, fx.Config.GetConsensusEngineType())
		}
		if fx.Config.GetChainID().Cmp(cfg.GetChainID()) != 0 {
			t.Errorf("%s: chain id %v, want %v", name, fx.Config.GetChainID(), cfg.GetChainID())
		}
		a, b := reflect.ValueOf(cfg), reflect.ValueOf(fx.Config)
		for _, g := range getters {
			want := a.MethodByName(g).Call(nil)[0].Interface().(*uint64)
			have := b.MethodByName(g).Call(nil)[0].Interface().(*uint64)
			if (want == nil) != (have == nil) || (want != nil && *want != *have) {
				t.Errorf("%s: %s: translated %v, fixture %v", name, g, deref(have), deref(want))
			}
		}
	}
}

func deref(p *uint64) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

// TestFixtureTranslationReward checks the reward schedule the translation
// derives against ethash's, at the gates of three fixture configurations.
func TestFixtureTranslationReward(t *testing.T) {
	ether := big.NewInt(1e18)
	cases := []struct {
		fork   string
		number uint64
		diff   int64
		want   int64
	}{
		{"Frontier", 1, 1, 5},
		{"Byzantium", 1, 1, 3},
		{"ConstantinopleFix", 1, 1, 2},
		{"ByzantiumToConstantinopleFixAt5", 4, 1, 3},
		{"ByzantiumToConstantinopleFixAt5", 5, 1, 2},
		{"London", 1, 0, 0},
	}
	for _, c := range cases {
		fx, err := translateFixtureConfig(c.fork, Forks[c.fork])
		if err != nil {
			t.Fatalf("%s: %v", c.fork, err)
		}
		h := &types.Header{Number: new(big.Int).SetUint64(c.number), Difficulty: big.NewInt(c.diff)}
		want := new(big.Int).Mul(big.NewInt(c.want), ether)
		if got := fx.Reward(h); got.Cmp(want) != 0 {
			t.Errorf("%s block %d: reward %v, want %v", c.fork, c.number, got, want)
		}
	}
}

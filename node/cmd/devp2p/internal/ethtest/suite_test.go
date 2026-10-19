// Copyright 2021 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package ethtest

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/internal/utesting"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/vars"
)

func TestEthSuite(t *testing.T) {
	// The fixture set is the Igneum proof-of-work chain written by
	// TestWriteIgneumFixtures (PA-28); its marker file is mandatory: missing,
	// the suite fails under IGNEUM_REQUIRE_FIXTURES and is skipped otherwise,
	// never silently passed.
	if _, err := os.Stat("./testdata/IGNEUM-FIXTURE"); err != nil {
		if os.Getenv("IGNEUM_REQUIRE_FIXTURES") != "" {
			t.Fatal("devp2p eth suite: the Igneum proof-of-work fixture set is missing (PA-28)")
		}
		t.Skip("devp2p eth suite: the Igneum fixture set is missing; regenerate it with TestWriteIgneumFixtures (PA-28)")
	}
	geth, err := runGeth("./testdata")
	if err != nil {
		t.Fatalf("could not run geth: %v", err)
	}
	defer geth.Close()

	suite, err := NewSuite(geth.Server().Self(), "./testdata")
	if err != nil {
		t.Fatalf("could not create new test suite: %v", err)
	}
	for _, test := range suite.EthTests() {
		t.Run(test.Name, func(t *testing.T) {
			if test.Slow && testing.Short() {
				t.Skipf("%s: skipping in -short mode", test.Name)
			}
			result := utesting.RunTests([]utesting.Test{{Name: test.Name, Fn: test.Fn}}, os.Stdout)
			if result[0].Failed {
				t.Fatal()
			}
		})
	}
}

func TestSnapSuite(t *testing.T) {
	// The same fixture set as TestEthSuite (PA-28), plus the snap suite's
	// expectations: upstream's were hashes hard-coded from the hivechain
	// state (account-range cut-offs, trie-node and storage-slot hashes) and
	// PA-28b derives them from the fixture's own state dump. Until it lands
	// the expectations are a missing mandatory fixture: fail under
	// IGNEUM_REQUIRE_FIXTURES, skip otherwise, never a silent pass.
	if _, err := os.Stat("./testdata/IGNEUM-FIXTURE"); err != nil {
		if os.Getenv("IGNEUM_REQUIRE_FIXTURES") != "" {
			t.Fatal("devp2p snap suite: the Igneum proof-of-work fixture set is missing (PA-28)")
		}
		t.Skip("devp2p snap suite: the Igneum fixture set is missing; regenerate it with TestWriteIgneumFixtures (PA-28)")
	}
	if _, err := os.Stat("./testdata/IGNEUM-SNAP-EXPECTATIONS"); err != nil {
		if os.Getenv("IGNEUM_REQUIRE_FIXTURES") != "" {
			t.Fatal("devp2p snap suite: the expectations for the Igneum fixture set are missing (PA-28b)")
		}
		t.Skip("devp2p snap suite: the expectations for the Igneum fixture set are missing (PA-28b)")
	}
	geth, err := runGeth("./testdata")
	if err != nil {
		t.Fatalf("could not run geth: %v", err)
	}
	defer geth.Close()

	suite, err := NewSuite(geth.Server().Self(), "./testdata")
	if err != nil {
		t.Fatalf("could not create new test suite: %v", err)
	}
	for _, test := range suite.SnapTests() {
		t.Run(test.Name, func(t *testing.T) {
			result := utesting.RunTests([]utesting.Test{{Name: test.Name, Fn: test.Fn}}, os.Stdout)
			if result[0].Failed {
				t.Fatal()
			}
		})
	}
}

// runGeth creates and starts a geth node
func runGeth(dir string) (*node.Node, error) {
	stack, err := node.New(&node.Config{
		P2P: p2p.Config{
			ListenAddr:  "127.0.0.1:0",
			NoDiscovery: true,
			MaxPeers:    10, // in case a test requires multiple connections, can be changed in the future
			NoDial:      true,
		},
	})
	if err != nil {
		return nil, err
	}

	err = setupGeth(stack, dir)
	if err != nil {
		stack.Close()
		return nil, err
	}
	if err = stack.Start(); err != nil {
		stack.Close()
		return nil, err
	}

	return stack, nil
}

func setupGeth(stack *node.Node, dir string) error {
	chain, err := NewChain(dir)
	if err != nil {
		return err
	}

	ethConfig := &ethconfig.Config{
		Genesis:          &chain.genesis,
		NetworkId:        chain.genesis.Config.GetChainID().Uint64(), // 19763
		ProtocolVersions: vars.DefaultProtocolVersions,
		DatabaseCache:    10,
		TrieCleanCache:   10,
		TrieDirtyCache:   16,
		TrieTimeout:      60 * time.Minute,
		SnapshotCache:    10,
	}

	// The fixture chain is the development chain, sealed for real with the
	// null algorithm (TestWriteIgneumFixtures). The null algorithm builds only
	// in development mode, which is what this in-process test node is.
	if chain.config.GetConsensusEngineType() != ctypes.ConsensusEngineT_Igneum {
		return fmt.Errorf("fixture genesis selects engine %v, want igneum", chain.config.GetConsensusEngineType())
	}
	ethConfig.DevMode = true

	backend, err := eth.New(stack, ethConfig)
	if err != nil {
		return err
	}
	if _, err := backend.BlockChain().InsertChain(chain.blocks[1:]); err != nil {
		return err
	}
	// The suite's transaction tests need the node to accept transactions, which
	// a proof-of-work node does once it is synced; upstream pushed the head
	// through the engine API's forkchoice to the same end.
	backend.SetSynced()
	return nil
}

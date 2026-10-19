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
	"crypto/ecdsa"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
	"github.com/ethereum/go-ethereum/params/vars"
)

// IgneumDevGenesisHash is the hash of the development genesis block (one
// block, the fixed faucet funded), frozen on the build box; the golden test
// TestGenesisHashIgneumDev compares against it.
var IgneumDevGenesisHash = common.HexToHash("0xdd91c21e498df949b07ca4a8818fae9ff1fa1ef83962625ab33ca76aefca50ec") // re-frozen 10 October 2026 (F-11: the 10^27 wei faucet), the build box, core.GenesisToBlock

// The development chain's faucet is one fixed, dev-only account, so that
// every development node computes the same genesis (one genesis, in code;
// the project's decision of 10 October 2026). Its private key is derived from a
// published phrase and is PUBLIC: anyone can spend from it, which is the
// point on a development chain and a catastrophe anywhere else. --dev is the
// only way to run on this genesis (F-02: flags.go refuses any other datadir
// genesis under --dev), and the public test network runs on it under --dev ON
// PURPOSE (F-599, 17 October 2026): a test network whose coins have no value
// and which may reset, its public-phrase faucet drawing from this account.
// The rule in writing: no chain that carries value ever runs on a genesis
// that funds a public-key account; a LAUNCH genesis never allocates to this
// key and its form refuses under --dev; IsIgneumDevGenesis stays the only
// place that recognises this genesis.
var (
	igneumDevFaucetPhrase = "igneum development faucet: dev-only, public key material, never fund on any other chain"
	// IgneumDevFaucetBalance is the faucet's allocation, 10^27 wei (10^9 IGN;
	// security's F-11, 10 October 2026). Upstream's dev-mode convention of
	// 2^256 - 9 wei wrapped the balance once block rewards were credited to the
	// faucet, which the first devnet did after 160 blocks.
	IgneumDevFaucetBalance = new(big.Int).Exp(big.NewInt(10), big.NewInt(27), nil)
	// IgneumDevFaucet is the faucet's address.
	IgneumDevFaucet = crypto.PubkeyToAddress(IgneumDevFaucetKey().PublicKey)
	// IgneumDevEtherbase is where a --dev node's rewards go unless
	// --miner.etherbase says otherwise: a dev-only sink derived from a public
	// phrase, never the faucet (F-11). Nobody holds its key; rewards on a
	// development chain are not for spending, the faucet is.
	IgneumDevEtherbase = common.BytesToAddress(crypto.Keccak256([]byte("igneum development etherbase: dev-only reward sink, no key"))[12:])
)

// IgneumDevFaucetKey returns the dev-only faucet's private key (public
// material, see above): keccak256 of the published phrase as the scalar.
func IgneumDevFaucetKey() *ecdsa.PrivateKey {
	key, err := crypto.ToECDSA(crypto.Keccak256([]byte(igneumDevFaucetPhrase)))
	if err != nil {
		panic("igneum: the dev faucet phrase does not derive a valid key: " + err.Error())
	}
	return key
}

// IgneumDevGenesisDifficulty is the genesis difficulty of the development
// chain. It equals the fixed difficulty of the devnet stand-in calculator
// (consensus/igneum.DifficultyFor) so that block 1 validates.
var IgneumDevGenesisDifficulty = big.NewInt(1 << 16)

// DefaultIgneumDevGenesisBlock returns the Igneum development genesis: no
// premine (an empty allocation), the EIP-1559 initial base fee, a 16,000,000
// gas limit (D-A5), zero nonce and mix hash. The launch genesis is a separate
// function added with the founder's decisions.
// IgneumDevGenesisBlock is the --dev genesis: the one development genesis
// (the fixed faucet funded) with the requested gas limit (zero keeps the
// default and the frozen hash).
func IgneumDevGenesisBlock(gasLimit uint64) *genesisT.Genesis {
	g := DefaultIgneumDevGenesisBlock()
	if gasLimit != 0 {
		g.GasLimit = gasLimit // a dev-only knob; it changes the genesis hash, so a devnet leaves it at the default
	}
	return g
}

// IsIgneumDevGenesis reports whether a genesis is an Igneum development
// genesis: the igneum engine block naming the null algorithm and the
// development chain id. --dev refuses anything else (F-02).
func IsIgneumDevGenesis(g *genesisT.Genesis) bool {
	if g == nil || g.Config == nil {
		return false
	}
	if g.Config.GetConsensusEngineType() != ctypes.ConsensusEngineT_Igneum {
		return false
	}
	// The development-only algorithms: the null stand-in and the IgneumHash
	// research candidate (--dev.algorithm). A datadir sealed by either is a
	// development chain and restarts under --dev; any other name is refused.
	cfg := g.Config.GetIgneumConfig()
	if cfg == nil || (cfg.Algorithm != "null" && cfg.Algorithm != "igneumhash") {
		return false
	}
	id := g.Config.GetChainID()
	return id != nil && (id.Cmp(IgneumDevChainID) == 0 || (id.IsUint64() && IsIgneumResearchChainID(id.Uint64())))
}

// The research chain ids: a development chain under --dev.chainid takes
// one so that a transaction signed for it replays on no other development
// chain (every earlier dev chain shared 7310 with a different genesis hash,
// the product team's finding of 11 October 2026; those chains keep 7310 and the
// replay class between them is on the record). The range's end moved from
// 7399 to 7499 on 18 October 2026 for the Money lab chains (7401 the
// shielded pool's), every id to 7399 being taken.
const (
	IgneumResearchChainIDFirst uint64 = 7311
	IgneumResearchChainIDLast  uint64 = 7499
)

// IsIgneumResearchChainID reports whether an id lies in the research range.
func IsIgneumResearchChainID(id uint64) bool {
	return id >= IgneumResearchChainIDFirst && id <= IgneumResearchChainIDLast
}

// DefaultIgneumGenesisBlock is the launch genesis placeholder (PA-05): the
// Igneum rules with no proof-of-work algorithm selected, so a node started on
// it refuses to run until the founder's decisions and the Phase Zero selection
// fill it in. It is what an empty datadir gets with no genesis given, and it
// is deliberately unusable rather than silently another chain's.
func DefaultIgneumGenesisBlock() *genesisT.Genesis {
	return &genesisT.Genesis{
		Config:     IgneumChainConfig,
		Nonce:      0,
		Timestamp:  0,
		ExtraData:  []byte("igneum/unselected"),
		GasLimit:   16_000_000,
		Difficulty: new(big.Int).Set(IgneumDevGenesisDifficulty),
		BaseFee:    big.NewInt(vars.InitialBaseFee),
		Alloc:      genesisT.GenesisAlloc{},
	}
}

func DefaultIgneumDevGenesisBlock() *genesisT.Genesis {
	return &genesisT.Genesis{
		Config:     IgneumDevChainConfig,
		Nonce:      0,
		Timestamp:  0,
		ExtraData:  []byte("igneum-dev/null"), // at most 32 bytes
		GasLimit:   16_000_000,
		Difficulty: new(big.Int).Set(IgneumDevGenesisDifficulty),
		BaseFee:    big.NewInt(vars.InitialBaseFee),
		Alloc:      genesisT.GenesisAlloc{IgneumDevFaucet: {Balance: new(big.Int).Set(IgneumDevFaucetBalance)}},
	}
}

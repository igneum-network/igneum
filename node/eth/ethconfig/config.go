// Copyright 2021 The go-ethereum Authors
// This file is part of the go-ethereum library.
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

// Package ethconfig contains the configuration of the ETH and LES protocols.
package ethconfig

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	// The dev-only null algorithm registers itself in init; without this
	// import nothing links it and --dev stops with "unknown algorithm: null"
	// (found 9 October 2026 bringing the explorer up). pow.New
	// still refuses it outside --dev (F-02).
	_ "github.com/ethereum/go-ethereum/consensus/igneum/pow/igneumhash"
	_ "github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/gasprice"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/miner"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
	"github.com/ethereum/go-ethereum/params/vars"
)

// FullNodeGPO contains default gasprice oracle settings for full node.
var FullNodeGPO = gasprice.Config{
	Blocks:           20,
	Percentile:       60,
	MaxHeaderHistory: 1024,
	MaxBlockHistory:  1024,
	MaxPrice:         gasprice.DefaultMaxPrice,
	IgnorePrice:      gasprice.DefaultIgnorePrice,
}

// Defaults contains default settings for use on the Ethereum main net.
var Defaults = Config{
	PiResident:         true,
	SyncMode:           downloader.SnapSync,
	NetworkId:          0, // enable auto configuration of networkID == chainID
	ProtocolVersions:   vars.DefaultProtocolVersions,
	TxLookupLimit:      2350000,
	TransactionHistory: 2350000,
	StateHistory:       vars.FullImmutabilityThreshold,
	LightPeers:         100,
	UltraLightFraction: 75,
	DatabaseCache:      512,
	TrieCleanCache:     154,
	TrieDirtyCache:     256,
	TrieTimeout:        60 * time.Minute,
	SnapshotCache:      102,
	FilterLogCacheSize: 32,
	Miner:              miner.DefaultConfig,
	TxPool:             legacypool.DefaultConfig,
	RPCGasCap:          50000000,
	RPCEVMTimeout:      5 * time.Second,
	GPO:                FullNodeGPO,
	RPCTxFeeCap:        1, // 1 ether
}

//go:generate go run github.com/fjl/gencodec -type Config -formats toml -out gen_config.go

// Config contains configuration options for ETH and LES protocols.
type Config struct {
	// The genesis block, which is inserted if the database is empty.
	// If nil, the Ethereum main net block is used.
	Genesis *genesisT.Genesis `toml:",omitempty"`

	// DevMode is set by the --dev flag only. It is the one thing that lets a
	// development-only proof of work (the null algorithm) run (F-02).
	DevMode bool `toml:"-"`

	// FakePoW accepts every seal (tests and --fakepow only).
	FakePoW bool `toml:"-"`

	// PiVerify turns the keyed seal's proof pi verification on
	// (--igneum.pi-verify; consensus/igneum/pi.go) and PiProver names the
	// prover CLI this node's own seals use (--igneum.pi-prover).
	PiVerify bool `toml:",omitempty"`
	// PoolUnsafeVerifier installs the shielded pool's lab test double as
	// the spend verifier (--dev.pool.unsafe-verifier; never on a chain of
	// record: the backend refuses it outside --dev on a research-range id).
	PoolUnsafeVerifier bool `toml:"-"`
	// PoolPrune drops the shielded pool's proof bodies PoolPruneKeep blocks
	// after import and serves the retained records (--pool.prune; eth/
	// pool_prune.go); a fresh node under it joins by the pruned sync (snap
	// sync with no bodies before the pivot). Off by default: a node keeps
	// every body whole.
	PoolPrune     bool   `toml:",omitempty"`
	PoolPruneKeep uint64 `toml:",omitempty"`
	PiProver      string `toml:",omitempty"`
	// PiProverFallback is the second prover CLI (the CPU build) a seal
	// falls back to when PiProver cannot run on the host (F-89).
	PiProverFallback string `toml:",omitempty"`
	// VerifierCore is the reserved verifier thread's CPU: 0 (the default)
	// the last allowed CPU on two or more, N the N-th allowed, -1 off.
	VerifierCore int `toml:",omitempty"`
	// PiResident runs the prover as one resident process for the sealer's
	// life (--igneum.pi-resident; consensus/igneum/piserve.go). On by
	// default from the clock re-pin (the frozen tPiMs is the resident
	// path's); the flag turns it off.
	PiResident bool `toml:",omitempty"`
	// VerifyBudgetMs is the verifier's global CPU budget in milliseconds a
	// second across every peer (--igneum.verify-budget-ms;
	// consensus/igneum/verifybudget.go); zero is off.
	VerifyBudgetMs uint64 `toml:",omitempty"`
	// Coordinate is VectorWork's work coordinate this node seals under
	// (--igneum.coord; consensus/igneum/vector.go); nil seals coordinate 0
	// on a chain with the vector form.
	Coordinate *uint8 `toml:",omitempty"`
	// ResidentDataset makes a verifying node hold the full-dataset contexts
	// (--igneum.resident-dataset; consensus/igneum/resident.go); on by
	// default, the light contexts when off.
	ResidentDataset bool `toml:",omitempty"`
	// SealWorker is the GPU worker executable this node's keyed search runs
	// on (--igneum.seal-worker; consensus/igneum/sealworker.go) and
	// SealWorkerDevice the card it opens (--igneum.seal-worker-device).
	SealWorker       string `toml:",omitempty"`
	SealWorkerDevice string `toml:",omitempty"`

	// Endorse turns this node's endorser on (the settlement rule's lab
	// transport, consensus/igneum/endorse.go; --igneum.endorse) and
	// EndorseTo names the sealers' RPC URLs its endorsements are pushed to
	// (--igneum.endorse-to).
	Endorse   bool     `toml:",omitempty"`
	EndorseTo []string `toml:",omitempty"`
	// PropagateAll pushes a new block whole to every peer instead of the
	// square-root subset (--igneum.propagate-all; a sealer with few direct
	// peers, the body lever of 12 October 2026).
	PropagateAll bool `toml:",omitempty"`

	// Protocol options
	NetworkId        uint64 // Network ID to use for selecting peers to connect to. When 0, chainID is used.
	ProtocolVersions []uint // Protocol versions are the supported versions of the eth protocol (first is primary).
	SyncMode         downloader.SyncMode

	// This can be set to list of enrtree:// URLs which will be queried for
	// for nodes to connect to.
	EthDiscoveryURLs  []string
	SnapDiscoveryURLs []string

	NoPruning  bool // Whether to disable pruning and flush everything to disk
	NoPrefetch bool // Whether to disable prefetching and only load state on demand

	// Deprecated, use 'TransactionHistory' instead.
	TxLookupLimit      uint64 `toml:",omitempty"` // The maximum number of blocks from head whose tx indices are reserved.
	TransactionHistory uint64 `toml:",omitempty"` // The maximum number of blocks from head whose tx indices are reserved.
	StateHistory       uint64 `toml:",omitempty"` // The maximum number of blocks from head whose state histories are reserved.

	// State scheme represents the scheme used to store ethereum states and trie
	// nodes on top. It can be 'hash', 'path', or none which means use the scheme
	// consistent with persistent state.
	StateScheme string `toml:",omitempty"`

	// RequiredBlocks is a set of block number -> hash mappings which must be in the
	// canonical chain of all remote peers. Setting the option makes geth verify the
	// presence of these blocks for every new peer connection.
	RequiredBlocks map[uint64]common.Hash `toml:"-"`

	// Light client options
	LightServ        int  `toml:",omitempty"` // Maximum percentage of time allowed for serving LES requests
	LightIngress     int  `toml:",omitempty"` // Incoming bandwidth limit for light servers
	LightEgress      int  `toml:",omitempty"` // Outgoing bandwidth limit for light servers
	LightPeers       int  `toml:",omitempty"` // Maximum number of LES client peers
	LightNoPrune     bool `toml:",omitempty"` // Whether to disable light chain pruning
	LightNoSyncServe bool `toml:",omitempty"` // Whether to serve light clients before syncing

	// Ultra Light client options
	UltraLightServers      []string `toml:",omitempty"` // List of trusted ultra light servers
	UltraLightFraction     int      `toml:",omitempty"` // Percentage of trusted servers to accept an announcement
	UltraLightOnlyAnnounce bool     `toml:",omitempty"` // Whether to only announce headers, or also serve them

	// Database options
	SkipBcVersionCheck    bool `toml:"-"`
	DatabaseHandles       int  `toml:"-"`
	DatabaseCache         int
	DatabaseFreezer       string
	DatabaseFreezerRemote string

	TrieCleanCache int
	TrieDirtyCache int
	TrieTimeout    time.Duration
	SnapshotCache  int
	Preimages      bool

	// This is the number of blocks for which logs will be cached in the filter system.
	FilterLogCacheSize int

	// Mining options
	Miner miner.Config

	// Transaction pool options
	TxPool legacypool.Config

	// Gas Price Oracle options
	GPO gasprice.Config

	// Enables tracking of SHA3 preimages in the VM
	EnablePreimageRecording bool

	// Miscellaneous options
	DocRoot string `toml:"-"`

	// Type of the EWASM interpreter ("" for default)
	EWASMInterpreter string

	// Type of the EVM interpreter ("" for default)
	EVMInterpreter string

	// RPCGasCap is the global gas cap for eth-call variants.
	RPCGasCap uint64

	// RPCEVMTimeout is the global timeout for eth-call.
	RPCEVMTimeout time.Duration

	// RPCTxFeeCap is the global transaction fee(price * gaslimit) cap for
	// send-transaction variants. The unit is ether.
	RPCTxFeeCap float64

	// OverrideShanghai (TODO: remove after the fork)
	OverrideShanghai *uint64 `toml:",omitempty"`

	// OverrideCancun (TODO: remove after the fork)
	OverrideCancun *uint64 `toml:",omitempty"`

	// OverrideVerkle (TODO: remove after the fork)
	OverrideVerkle *uint64 `toml:",omitempty"`

	// OverrideIgneumSchedule sets the bounded menu's schedule on a
	// development chain (--dev.algorithm.schedule).
	OverrideIgneumSchedule *ctypes.IgneumSchedule `toml:"-"`

	// OverrideIgneumAuthority sets the miner-bound work authority
	// prototype's rule on a development chain (--dev.authority); IgneumBondKey
	// is the key the node seals under; IgneumAuthorityLax is the test
	// harness's non-compliant switch.
	OverrideIgneumAuthority *ctypes.IgneumAuthority `toml:"-"`
	IgneumBondKey           *common.Hash            `toml:"-"`
	IgneumAuthorityLax      bool                    `toml:"-"`
	// The lab build's research instruments (consensus/igneum/labmode.go):
	// the withholding sealer's N and the lax-endorsement vector file;
	// refused off the lab build.
	IgneumWithhold   uint64 `toml:"-"`
	IgneumLaxEndorse string `toml:"-"`
	// IgneumSealStaggerMs is PA-52's stagger between eligible keys' search
	// starts by lot rank (consensus/igneum/stagger.go), milliseconds, 0 off.
	IgneumSealStaggerMs uint64 `toml:"-"`
	// IgneumOpenSpreadMs is F-114's open-seat spread slot width
	// (consensus/igneum/stagger.go), milliseconds, 0 off.
	IgneumOpenSpreadMs      uint64                  `toml:"-"`
	OverrideIgneumSortition *ctypes.IgneumSortition `toml:"-"`
	IgneumBondSeed          []byte                  `toml:"-"`

	// OverrideIgneumAlgorithm restores a stored development engine block's
	// algorithm when the stored name is empty (core.ChainOverrides).
	OverrideIgneumAlgorithm       string          `toml:"-"`
	OverrideIgneumAlgorithmParams json.RawMessage `toml:"-"`

	// OverrideIgneumVector carries the development flags' vector document
	// onto a persisted data directory (F-113, 14 October 2026: --dev drops
	// the flags' genesis when a chain exists, so a re-registration by
	// activation, the eleventh chain's (chain 7321) coordinate 4 at 382, reached no node's engine
	// and no node's stored configuration; a node pinned to the new
	// coordinate was refused at start). It reaches the engine in
	// eth/backend.go and the stored chain configuration through
	// core.ChainOverrides, under the re-registration check there.
	OverrideIgneumVector *ctypes.IgneumVector `toml:"-"`
	// OverrideIgneumIssuance carries the chain id's issuance default to
	// the stored chain configuration and the engine on every start (the
	// 12-second gate of 16 October 2026 never reached a node whose datadir
	// stored its config before the default gained the next form: the
	// stored config is the engine's, and no override refreshed the
	// issuance as the schedule's and the vector's do).
	OverrideIgneumIssuance *ctypes.IgneumIssuance `toml:"-"`
}

// CreateConsensusEngine creates the consensus engine a chain configuration
// names: the Igneum engine from its engine block (PA-07), returned unwrapped
// (no merge engine, DESIGN.md 4.6). A configuration without an engine block
// has no engine (ethash left with PA-09b). fakePoW builds the engine in fake
// mode on the block's parts: every seal accepted, for tests and --fakepow.
// chainID is the chain's id, the key of the gates every engine carries
// whatever path built it (the node's start and the command line's chain
// maker alike, so an offline import settles a race as the running node
// does: the security review's read of 1acc2ea4d, 16 October 2026): the fork
// choice's same-height rule from params.IgneumChainFirstSeenFrom.
func CreateConsensusEngine(chainID uint64, igneumConfig *ctypes.IgneumConfig, devMode bool, fakePoW bool, notify []string, noverify bool) (consensus.Engine, error) {
	if igneumConfig == nil {
		return nil, errors.New("no consensus engine: the chain configuration has no igneum engine block")
	}
	var engine *igneum.Engine
	// The chain id's lab word (F-734): a lab chain may pay its fund share to
	// no address; a chain of record is refused at start (issuance.Check).
	lab := params.IsIgneumResearchChainID(chainID)
	if fakePoW {
		parts, err := igneum.PartsForChain(igneumConfig, lab)
		if err != nil {
			return nil, err
		}
		log.Warn("Proof of work in fake mode: every seal is accepted")
		engine, err = igneum.New(igneum.Config{FakePoW: true, Parts: parts, Notify: notify, NoVerify: noverify})
		if err != nil {
			return nil, err
		}
	} else {
		parts, err := igneum.PartsForChain(igneumConfig, lab)
		if err != nil {
			return nil, err
		}
		engine, err = igneum.NewFromChainConfig(igneumConfig, parts, notify, noverify, devMode)
		if err != nil {
			return nil, err
		}
		if devMode {
			engine.SetThreads(0) // the development chain seals on the CPU (null algorithm)
		} else {
			engine.SetThreads(-1) // no CPU sealing on a real chain: workers mine through the RPC
		}
	}
	// The fork choice's same-height rule from the chain id's gate (the
	// chair's ruling of 16 October 2026, consensus/igneum/firstseen.go),
	// set where the engine is built so every start path carries it.
	if from := params.IgneumFirstSeenFromForChainID(chainID); from != 0 {
		engine.SetFirstSeenFrom(from)
		log.Info("Fork choice's same-height rule gated by chain id", "chainId", chainID, "from", from)
	}
	// The fork choice's lane weights by the running difficulty at the
	// common ancestor from the chain id's gate (F-569, the review lead's ruling
	// 7f95a4dc1 of 18 October 2026; consensus/igneum/vector.go laneWeightsBy).
	if from := params.IgneumForkWeightFromForChainID(chainID); from != 0 {
		engine.SetForkWeightFrom(from)
		log.Info("Fork choice's lane weights by the running difficulty gated by chain id", "chainId", chainID, "from", from)
	}
	// The share target's difficulty the lane's from the chain id's gate
	// (F-690, the review lead's ruling bd0784c6d of 18 October 2026).
	if from := params.IgneumShareLaneFromForChainID(chainID); from != 0 {
		engine.SetShareLaneFrom(from)
		log.Info("Share target by the lane's difficulty gated by chain id", "chainId", chainID, "from", from)
	}
	// The sponsored lane's gate by chain id (F-703: its own table, not a
	// field of the rule forks).
	if from := params.IgneumSponsoredFromForChainID(chainID); from != 0 {
		engine.SetSponsoredFrom(from)
		log.Info("Sponsored lane gated by chain id", "chainId", chainID, "from", from)
	}
	// The shielded pool's gate by chain id (Project Money stage one; its
	// own table, zero on every chain of record).
	if from := params.IgneumPoolFromForChainID(chainID); from != 0 {
		engine.SetPoolFrom(from)
		log.Info("Shielded pool gated by chain id", "chainId", chainID, "from", from)
	}
	// The programme reveal from the document for every instance of a lane
	// (F-730), by chain id; 0 keeps the form before.
	if from := params.IgneumDocumentRevealFromForChainID(chainID); from != 0 {
		engine.SetDocumentRevealFrom(from)
		log.Info("Programme reveal from the document gated by chain id", "chainId", chainID, "from", from)
	}
	return engine, nil
}

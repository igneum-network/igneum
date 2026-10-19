// Copyright 2014 The go-ethereum Authors
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

// Package eth implements the Ethereum protocol.
package eth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"sync"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool/miden"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/schedule"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/bloombits"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state/pruner"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/gasprice"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/eth/protocols/ishr"
	"github.com/ethereum/go-ethereum/eth/protocols/snap"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/internal/shutdowncheck"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/miner"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/dnsdisc"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

// Config contains the configuration options of the ETH protocol.
// Deprecated: use ethconfig.Config instead.
type Config = ethconfig.Config

// Ethereum implements the Ethereum full node service.
type Ethereum struct {
	config *ethconfig.Config

	// Handlers
	txPool *txpool.TxPool

	blockchain         *core.BlockChain
	poolPruner         *poolPruner // the shielded pool's proof pruner (--pool.prune; nil off)
	handler            *handler
	ethDialCandidates  enode.Iterator
	snapDialCandidates enode.Iterator
	// shares is the share gossip (eth/protocols/ishr, PA-46): the sub-protocol
	// beside eth on the same peer links; nil on a chain without the lane.
	shares *ishr.Handler

	// DB interfaces
	chainDb ethdb.Database // Block chain database

	eventMux       *event.TypeMux
	engine         consensus.Engine
	accountManager *accounts.Manager

	bloomRequests     chan chan *bloombits.Retrieval // Channel receiving bloom data retrieval requests
	bloomIndexer      *core.ChainIndexer             // Bloom indexer operating during block imports
	closeBloomHandler chan struct{}

	APIBackend *EthAPIBackend

	miner     *miner.Miner
	gasPrice  *big.Int
	etherbase common.Address

	networkID     uint64
	netRPCService *ethapi.NetAPI

	p2pServer *p2p.Server

	lock sync.RWMutex // Protects the variadic fields (e.g. gas price and etherbase)

	shutdownTracker *shutdowncheck.ShutdownTracker // Tracks if and when the node has shutdown ungracefully
}

// New creates a new Ethereum object (including the
// initialisation of the common Ethereum object)
func New(stack *node.Node, config *ethconfig.Config) (*Ethereum, error) {
	// Ensure configuration values are compatible and sane
	if config.SyncMode == downloader.LightSync {
		return nil, errors.New("can't run eth.Ethereum in light sync mode, light mode has been deprecated")
	}
	if !config.SyncMode.IsValid() {
		return nil, fmt.Errorf("invalid sync mode %d", config.SyncMode)
	}
	if config.Miner.GasPrice == nil || config.Miner.GasPrice.Cmp(common.Big0) <= 0 {
		log.Warn("Sanitizing invalid miner gas price", "provided", config.Miner.GasPrice, "updated", ethconfig.Defaults.Miner.GasPrice)
		config.Miner.GasPrice = new(big.Int).Set(ethconfig.Defaults.Miner.GasPrice)
	}
	if config.NoPruning && config.TrieDirtyCache > 0 {
		if config.SnapshotCache > 0 {
			config.TrieCleanCache += config.TrieDirtyCache * 3 / 5
			config.SnapshotCache += config.TrieDirtyCache * 2 / 5
		} else {
			config.TrieCleanCache += config.TrieDirtyCache
		}
		config.TrieDirtyCache = 0
	}
	log.Info("Allocated trie memory caches", "clean", common.StorageSize(config.TrieCleanCache)*1024*1024, "dirty", common.StorageSize(config.TrieDirtyCache)*1024*1024)

	// Assemble the Ethereum object
	chainDb, err := stack.OpenDatabaseWithFreezer("chaindata", config.DatabaseCache, config.DatabaseHandles, config.DatabaseFreezer, "eth/db/chaindata/", false)
	if err != nil {
		return nil, err
	}
	scheme, err := rawdb.ParseStateScheme(config.StateScheme, chainDb)
	if err != nil {
		return nil, err
	}
	// Try to recover offline state pruning only in hash-based.
	if scheme == rawdb.HashScheme {
		if err := pruner.RecoverPruning(stack.ResolvePath(""), chainDb); err != nil {
			log.Error("Failed to recover state", "error", err)
		}
	}
	// The stored chain configuration (or the genesis) names the engine; the
	// Igneum block, when present, selects the Igneum engine (PA-07).
	chainConfig, err := core.LoadChainConfig(chainDb, config.Genesis)
	if err != nil {
		return nil, err
	}
	var igneumConfig *ctypes.IgneumConfig
	var vectorRewind uint64
	if chainConfig != nil && chainConfig.GetConsensusEngineType() == ctypes.ConsensusEngineT_Igneum {
		igneumConfig = chainConfig.GetIgneumConfig()
		// The lifecycle menu's ratification rule (PLAN-TO-WIN.md point 8):
		// a schedule given to a node is admitted against the one it holds
		// at its head height; a schedule that fails the rule is refused and
		// the held one stays in force (the fallback is the previous valid
		// schedule, never discretion). A development schedule (no
		// activation window) is never checked.
		if config.OverrideIgneumSchedule != nil && igneumConfig != nil {
			var head uint64
			if hash := rawdb.ReadHeadHeaderHash(chainDb); hash != (common.Hash{}) {
				if n := rawdb.ReadHeaderNumber(chainDb, hash); n != nil {
					head = *n
				}
			}
			if err := schedule.ValidateUpgrade(igneumConfig.Schedule, config.OverrideIgneumSchedule, head); err != nil {
				log.Error("Schedule refused; the held schedule stays in force", "head", head, "err", err)
				// The held schedule replaces the refused one as the override:
				// the engine takes it below, and SetupGenesisBlockWithOverride
				// writes it to the stored chain config (which it rewrites from
				// the chain's default plus the overrides on every start, so a
				// nil override would store no schedule at all and a refused
				// one would be stored if it stayed; the second chain stored a
				// refused schedule and transitioned on it, 10 October 2026).
				config.OverrideIgneumSchedule = igneumConfig.Schedule
				if config.Genesis != nil && config.Genesis.Config != nil {
					if gic := config.Genesis.Config.GetIgneumConfig(); gic != nil {
						gic.Schedule = igneumConfig.Schedule
						if err := config.Genesis.Config.SetIgneumConfig(gic); err != nil {
							log.Error("Failed to restore the held schedule on the genesis config", "err", err)
						}
					}
				}
			}
		}
		// The stored configuration of a persisted datadir predates any
		// schedule given on this start: the override reaches the engine
		// here, as it reaches the chain in SetupGenesisBlockWithOverride
		// (without this the second chain sealed past its transition height
		// on the base document, 10 October 2026).
		if config.OverrideIgneumSchedule != nil {
			igneumConfig.Schedule = config.OverrideIgneumSchedule
		}
		// The vector document of this start on a persisted chain (F-113):
		// the re-registration form of VECTORWORK.md section 1 is the only
		// change admitted, every coordinate the stored registration holds
		// kept as it is (its entry byte for byte) and each added one
		// activating above this node's head, so no block already imported
		// changes its meaning; anything else is refused and the stored
		// document stays in force. A persisted chain without a stored
		// document takes the given one (its first registration).
		if config.OverrideIgneumIssuance != nil {
			// The chain id's issuance default is the engine's on every start
			// (its gate heights are the node's table, not the datadir's).
			igneumConfig.Issuance = config.OverrideIgneumIssuance
			log.Info("Issuance from the chain id's config default (the stored configuration refreshed)", "chainId", chainConfig.GetChainID(), "epoch", igneumConfig.Issuance.Epoch, "nextFrom", igneumConfig.Issuance.NextFrom)
		}
		if config.OverrideIgneumVector != nil {
			var head uint64
			if hash := rawdb.ReadHeadHeaderHash(chainDb); hash != (common.Hash{}) {
				if n := rawdb.ReadHeaderNumber(chainDb, hash); n != nil {
					head = *n
				}
			}
			rewind, err := vectorReregistrationFrom(igneumConfig.Vector, config.OverrideIgneumVector, head, params.IgneumRegistryNoticeFromForChainID(chainConfig.GetChainID().Uint64()))
			if err == nil && igneumConfig.Vector != nil && igneumConfig.Vector.RegistryKey != (common.Address{}) && !bytes.Equal(jsonOf(igneumConfig.Vector), jsonOf(config.OverrideIgneumVector)) {
				// Cut B: once a registry key is stored the registration is
				// chain-held; a differing document by configuration is
				// refused (re-register by the registry transaction).
				err = errors.New("the registration is chain-held (a registry key is stored): re-register by the registry transaction, not by configuration")
			}
			if err != nil {
				log.Error("Vector document refused; the stored registration stays in force", "head", head, "err", err)
				config.OverrideIgneumVector = igneumConfig.Vector
			} else if rewind > 0 {
				// A node restarted PAST an added coordinate's activation
				// (the node team's second hand, 03:14 UK, 14 October 2026: a
				// sealer down across the window, a reader rolled late)
				// takes the document and rewinds to the block before the
				// activation, so the blocks above it re-import under the
				// new registration instead of a silent fork under the old.
				vectorRewind = rewind
				log.Warn("Vector document re-registers past an activation this node imported under the stored registration: rewinding to the block before it", "head", head, "rewindTo", rewind)
			}
			igneumConfig.Vector = config.OverrideIgneumVector
		}
		if config.OverrideIgneumAuthority != nil {
			igneumConfig.Authority = config.OverrideIgneumAuthority
			igneumConfig.Sortition = config.OverrideIgneumSortition
		}
		if igneumConfig.Algorithm == "" && config.OverrideIgneumAlgorithm != "" {
			log.Warn("Stored engine block names no algorithm; taking the development flags' (the stored configuration is repaired on this start)", "algorithm", config.OverrideIgneumAlgorithm)
			igneumConfig.Algorithm = config.OverrideIgneumAlgorithm
			igneumConfig.AlgorithmParams = config.OverrideIgneumAlgorithmParams
		}
	}

	engine, err := ethconfig.CreateConsensusEngine(chainConfig.GetChainID().Uint64(), igneumConfig, config.DevMode, config.FakePoW, config.Miner.Notify, config.Miner.Noverify)
	if err != nil {
		return nil, err
	}
	// The keyed seal's proof pi (consensus/igneum/pi.go): verification on
	// or off by the flag, the prover and its work directory under the datadir.
	if ps, ok := engine.(interface {
		SetPi(verify bool, prover, workDir string) error
	}); ok {
		if fb, ok := engine.(interface{ SetPiProverFallback(string) }); ok && config.PiProverFallback != "" {
			fb.SetPiProverFallback(config.PiProverFallback)
		}
		if vc, ok := engine.(interface{ SetVerifierCore(int) }); ok {
			vc.SetVerifierCore(config.VerifierCore)
		}
		if err := ps.SetPi(config.PiVerify, config.PiProver, stack.ResolvePath("igneum-pi")); err != nil {
			return nil, err
		}
	}
	// The shielded pool's lab test double (--dev.pool.unsafe-verifier):
	// under --dev on a research-range chain id that is no chain of record
	// only (F-771: poolUnsafeVerifierRefusal); a chain of record refuses
	// the flag at start. The guard of substance is the pool gate at zero.
	if config.PoolUnsafeVerifier {
		id := chainConfig.GetChainID().Uint64()
		if err := poolUnsafeVerifierRefusal(config.DevMode, id); err != nil {
			return nil, err
		}
		if pv, ok := engine.(interface{ SetPoolVerifier(pool.Verifier) }); ok {
			pv.SetPoolVerifier(pool.UnsafeDigestDouble{})
			log.Warn("UNSAFE shielded pool verifier: the digest test double accepts any well-formed spend (lab only)", "chainId", id)
		}
	}
	// The shielded pool's real verifier (consensus/igneum/pool/miden: the
	// pinned Miden VM verifier linked by cgo under the money_miden tag)
	// wherever the pool's gate is on and the lab double was not asked for;
	// a build without the library keeps pool.NoVerifier and says so.
	if from := params.IgneumPoolFromForChainID(chainConfig.GetChainID().Uint64()); from != 0 && !config.PoolUnsafeVerifier {
		if pv, ok := engine.(interface{ SetPoolVerifier(pool.Verifier) }); ok {
			if v, err := miden.New(pool.MinSecurityBits); err == nil {
				pv.SetPoolVerifier(v)
				log.Info("Shielded pool proof verifier", "verifier", v.Name(), "gate", from)
			} else {
				log.Warn("Shielded pool: no proof verifier in this build, every shield and spend refused", "gate", from, "err", err)
			}
		}
	}
	if pr, ok := engine.(interface{ SetPiResident(bool) }); ok && config.PiResident {
		pr.SetPiResident(true)
	}
	// The retained records' store (igneum_poolBlock, igneum_poolRetained,
	// igneum_poolBackfill read it; the pruner writes it): the chain
	// database, under its own prefix.
	if ps, ok := engine.(interface{ SetPoolStore(ethdb.KeyValueStore) }); ok {
		ps.SetPoolStore(chainDb)
	}
	// The verifier's global CPU budget (consensus/igneum/verifybudget.go)
	// under --igneum.verify-budget-ms; off at zero.
	if vb, ok := engine.(interface{ SetVerifyBudget(uint64) }); ok && config.VerifyBudgetMs > 0 {
		vb.SetVerifyBudget(config.VerifyBudgetMs)
	}
	// VectorWork (consensus/igneum/vector.go): the coordinate this node
	// seals under, on a chain with the vector form.
	if sc, ok := engine.(interface{ SetCoordinate(uint8) error }); ok && config.Coordinate != nil {
		if err := sc.SetCoordinate(*config.Coordinate); err != nil {
			return nil, err
		}
	}
	// The block DAG ordering (consensus/igneum/dagform.go) under the chain
	// id's gate params.IgneumChainDAGFrom: zero everywhere of record.
	if sd, ok := engine.(interface {
		SetDAGFrom(uint64, uint64)
		DAGK() uint8
	}); ok {
		if from := params.IgneumDAGFromForChainID(chainConfig.GetChainID().Uint64()); from != 0 {
			lane := params.IgneumDAGLaneSecondsForChainID(chainConfig.GetChainID().Uint64())
			sd.SetDAGFrom(from, lane)
			log.Warn("Block DAG ordering in force (the ordering ladder; a lab chain)", "chainId", chainConfig.GetChainID(), "gate", from, "laneSeconds", lane, "k", sd.DAGK(), "mergeDepth", igneum.DAGMergeDepth)
		}
	}
	// The resident dataset (consensus/igneum/resident.go): the verifier
	// holds the full-dataset contexts under --igneum.resident-dataset.
	if rd, ok := engine.(interface{ SetResidentDataset(bool) }); ok {
		rd.SetResidentDataset(config.ResidentDataset)
	}
	// The keyed seal worker (consensus/igneum/sealworker.go): the keyed
	// search on a GPU worker child under --igneum.seal-worker.
	if sw, ok := engine.(interface {
		SetSealWorker(exe, device string) error
	}); ok && config.SealWorker != "" {
		if err := sw.SetSealWorker(config.SealWorker, config.SealWorkerDevice); err != nil {
			return nil, err
		}
	}
	if ie, ok := engine.(*igneum.Engine); ok {
		if config.IgneumBondSeed != nil {
			if err := ie.SetSigner(config.IgneumBondSeed); err != nil {
				return nil, err
			}
			if config.IgneumBondKey == nil {
				config.IgneumBondKey = ie.SignerKey()
			}
		}
		ie.SetBondKey(config.IgneumBondKey)
		// PA-52's stagger (consensus/igneum/stagger.go): a node's value,
		// the flag's default one second.
		ie.SetSealStagger(config.IgneumSealStaggerMs)
		ie.SetOpenSpread(config.IgneumOpenSpreadMs)
		if config.IgneumAuthorityLax {
			log.Warn("TEST HARNESS: this node skips the authority rule's state check (a non-compliant node)")
			ie.SetAuthorityLax(true)
		}
		// The lab build's research instruments (consensus/igneum/labmode.go):
		// refused off the lab build, so a public build with either flag set
		// fails to start rather than seals quietly.
		if config.IgneumWithhold > 0 {
			if _, err := ie.SetWithhold(config.IgneumWithhold); err != nil {
				return nil, err
			}
		}
		if config.IgneumLaxEndorse != "" {
			if err := ie.SetLaxEndorse(config.IgneumLaxEndorse); err != nil {
				return nil, err
			}
		}
	}
	networkID := config.NetworkId
	if networkID == 0 {
		networkID = chainConfig.GetChainID().Uint64()
	}
	// The RPC limits are the chain config's, read by the CHAIN's id (node
	// seat B's params.IgneumChainRPCLimits; SECURITY-GAPS.md): the public
	// listener's rate and burst go to the node before its listeners open
	// (the --rpc.public flag marks them), the per-address share cap to the
	// engine's pool. An unlisted chain id runs with the limits off.
	// The chain's rule forks (params.IgneumChainRuleForks; the project's standing
	// rule of 20:1x UK, 11 October 2026: a consensus change swapped onto a
	// running devnet carries a height gate from the swap height).
	if forks := params.IgneumRuleForksForChainID(chainConfig.GetChainID().Uint64()); forks != nil {
		if rf, ok := engine.(interface{ SetRuleForks(*ctypes.IgneumRuleForks) }); ok {
			rf.SetRuleForks(forks)
			log.Info("Rule forks by chain id", "chainId", chainConfig.GetChainID(), "sealMeasureV1", forks.SealMeasureV1)
		}
	}
	// F-192 (16 October 2026): the minimum pay rule's y bound, unscaled up to
	// the chain id's gate and scaled above it (consensus/igneum/paybound.go).
	if until := params.IgneumPayBoundUnscaledUntilForChainID(chainConfig.GetChainID().Uint64()); until != 0 {
		if pb, ok := engine.(interface{ SetPayBoundUnscaledUntil(uint64) }); ok {
			pb.SetPayBoundUnscaledUntil(until)
			log.Info("Minimum pay rule's y bound unscaled by chain id", "chainId", chainConfig.GetChainID(), "until", until)
		}
	}
	// F-192's second gate: the registry's notice floor binds only for
	// landings at or above the chain id's gate (consensus/igneum
	// ReregistrationFrom), so a fresh walk admits the landings the chain
	// admitted before the rule.
	if from := params.IgneumRegistryNoticeFromForChainID(chainConfig.GetChainID().Uint64()); from != 0 {
		if rn, ok := engine.(interface{ SetRegistryNoticeFrom(uint64) }); ok {
			rn.SetRegistryNoticeFrom(from)
			log.Info("Registry notice floor gated by chain id", "chainId", chainConfig.GetChainID(), "from", from)
		}
	}
	// The fork choice's same-height rule's gate is set where the engine is
	// built (ethconfig.CreateConsensusEngine, by the chain id), so the
	// command line's chain maker carries it as this start does.
	if lim := params.IgneumRPCLimitsForChainID(chainConfig.GetChainID().Uint64()); lim != nil {
		stack.SetRPCLimits(float64(lim.RPCRate), float64(lim.RPCBurst))
		if sc, ok := engine.(interface{ SetShareCap(uint64) }); ok {
			sc.SetShareCap(lim.ShareCapPerAddress)
		}
		log.Info("RPC limits from the chain config", "chainId", chainConfig.GetChainID(), "rate", lim.RPCRate, "burst", lim.RPCBurst, "shareCapPerAddress", lim.ShareCapPerAddress, "public", stack.Config().RPCPublic)
	}
	eth := &Ethereum{
		config:            config,
		chainDb:           chainDb,
		eventMux:          stack.EventMux(),
		accountManager:    stack.AccountManager(),
		engine:            engine,
		closeBloomHandler: make(chan struct{}),
		networkID:         networkID,
		gasPrice:          config.Miner.GasPrice,
		etherbase:         config.Miner.Etherbase,
		bloomRequests:     make(chan chan *bloombits.Retrieval),
		bloomIndexer:      core.NewBloomIndexer(chainDb, vars.BloomBitsBlocks, vars.BloomConfirms),
		p2pServer:         stack.Server(),
		shutdownTracker:   shutdowncheck.NewShutdownTracker(chainDb),
	}
	bcVersion := rawdb.ReadDatabaseVersion(chainDb)
	var dbVer = "<nil>"
	if bcVersion != nil {
		dbVer = fmt.Sprintf("%d", *bcVersion)
	}
	log.Info("Initialising Ethereum protocol", "network", networkID, "dbversion", dbVer)

	if !config.SkipBcVersionCheck {
		if bcVersion != nil && *bcVersion > core.BlockChainVersion {
			return nil, fmt.Errorf("database version is v%d, Geth %s only supports v%d", *bcVersion, params.VersionWithMeta, core.BlockChainVersion)
		} else if bcVersion == nil || *bcVersion < core.BlockChainVersion {
			if bcVersion != nil { // only print warning on upgrade, not on init
				log.Warn("Upgrade blockchain database version", "from", dbVer, "to", core.BlockChainVersion)
			}
			rawdb.WriteDatabaseVersion(chainDb, core.BlockChainVersion)
		}
	}
	var (
		vmConfig = vm.Config{
			EnablePreimageRecording: config.EnablePreimageRecording,
			EWASMInterpreter:        config.EWASMInterpreter,
			EVMInterpreter:          config.EVMInterpreter,
		}
		cacheConfig = &core.CacheConfig{
			TrieCleanLimit:      config.TrieCleanCache,
			TrieCleanNoPrefetch: config.NoPrefetch,
			TrieDirtyLimit:      config.TrieDirtyCache,
			TrieDirtyDisabled:   config.NoPruning,
			TrieTimeLimit:       config.TrieTimeout,
			SnapshotLimit:       config.SnapshotCache,
			Preimages:           config.Preimages,
			StateHistory:        config.StateHistory,
			StateScheme:         scheme,
		}
	)
	// Override the chain config with provided settings.
	var overrides core.ChainOverrides
	if config.OverrideIgneumSchedule != nil {
		overrides.OverrideIgneumSchedule = config.OverrideIgneumSchedule
	}
	if config.OverrideIgneumVector != nil {
		overrides.OverrideIgneumVector = config.OverrideIgneumVector
	}
	if config.OverrideIgneumIssuance != nil {
		overrides.OverrideIgneumIssuance = config.OverrideIgneumIssuance
	}
	if config.OverrideIgneumAuthority != nil {
		overrides.OverrideIgneumAuthority = config.OverrideIgneumAuthority
		overrides.OverrideIgneumSortition = config.OverrideIgneumSortition
	}
	if config.OverrideIgneumAlgorithm != "" {
		overrides.OverrideIgneumAlgorithm = config.OverrideIgneumAlgorithm
		overrides.OverrideIgneumAlgorithmParams = config.OverrideIgneumAlgorithmParams
	}
	if config.OverrideCancun != nil {
		overrides.OverrideCancun = config.OverrideCancun
	}
	if config.OverrideVerkle != nil {
		overrides.OverrideVerkle = config.OverrideVerkle
	}
	eth.blockchain, err = core.NewBlockChain(chainDb, cacheConfig, config.Genesis, &overrides, eth.engine, vmConfig, eth.shouldPreserve, &config.TransactionHistory)
	if err != nil {
		return nil, err
	}
	if vectorRewind > 0 {
		if err := eth.blockchain.SetHead(vectorRewind); err != nil {
			return nil, fmt.Errorf("vector re-registration: rewind to %d: %w", vectorRewind, err)
		}
		log.Warn("Chain rewound for the vector re-registration; the blocks above re-import from the peers", "head", eth.blockchain.CurrentBlock().Number)
	}
	eth.bloomIndexer.Start(eth.blockchain)

	// The settlement rule's endorser (consensus/igneum/endorse.go): this
	// node endorses the heads it holds and pushes to the named sealers.
	if config.Endorse {
		if es, ok := eth.engine.(interface {
			StartEndorser(chain consensus.ChainHeaderReader, pushTo []string)
		}); ok {
			es.StartEndorser(eth.blockchain, config.EndorseTo)
		}
	}

	// The withholding instrument's RPC disarm reveals held blocks through the
	// mined-block event, the same path the worker uses (consensus/igneum/labmode.go).
	if lb, ok := eth.engine.(interface{ SetLabBroadcast(func([]*types.Block)) }); ok {
		lb.SetLabBroadcast(func(blocks []*types.Block) {
			for _, b := range blocks {
				eth.eventMux.Post(core.NewMinedBlockEvent{Block: b})
			}
		})
	}

	// The share journal (consensus/igneum/sharejournal.go, PA-44): the
	// sealer's accepted, not yet carried shares under the datadir, replayed
	// at this start; a memory datadir keeps the pool in memory.
	if sj, ok := eth.engine.(interface {
		SetShareJournal(path string, chain consensus.ChainHeaderReader) error
	}); ok && stack.Config().DataDir != "" {
		if err := sj.SetShareJournal(stack.ResolvePath("igneum-shares.journal"), eth.blockchain); err != nil {
			return nil, err
		}
	}
	// Cut B: the chain-held vector registration's journal and its replay.
	if rj, ok := eth.engine.(interface {
		SetVectorRegistryJournal(path string, chain consensus.ChainHeaderReader) error
	}); ok && stack.Config().DataDir != "" {
		if err := rj.SetVectorRegistryJournal(stack.ResolvePath("igneum-registry.json"), eth.blockchain); err != nil {
			return nil, err
		}
	}

	if config.TxPool.Journal != "" {
		config.TxPool.Journal = stack.ResolvePath(config.TxPool.Journal)
	}
	legacyPool := legacypool.New(config.TxPool, eth.blockchain)

	// No blob pool: Igneum has no blob transactions (PA-12).
	eth.txPool, err = txpool.New(config.TxPool.PriceLimit, eth.blockchain, []txpool.SubPool{legacyPool})
	if err != nil {
		return nil, err
	}
	// Permit the downloader to use the trie cache allowance during fast sync
	cacheLimit := cacheConfig.TrieCleanLimit + cacheConfig.TrieDirtyLimit + cacheConfig.SnapshotLimit
	if eth.handler, err = newHandler(&handlerConfig{
		Database:       chainDb,
		Chain:          eth.blockchain,
		TxPool:         eth.txPool,
		Network:        networkID,
		Sync:           config.SyncMode,
		BloomCache:     uint64(cacheLimit),
		EventMux:       eth.eventMux,
		RequiredBlocks: config.RequiredBlocks,
		PropagateAll:   config.PropagateAll,
		PoolPrune:      config.PoolPrune,
	}); err != nil {
		return nil, err
	}
	// The shielded pool's proof pruner (--pool.prune; pool_prune.go):
	// where the pool's gate is on. Off by default.
	if from := params.IgneumPoolFromForChainID(chainConfig.GetChainID().Uint64()); config.PoolPrune && from != 0 {
		keep := config.PoolPruneKeep
		if keep < 256 {
			keep = 256
		}
		eth.poolPruner = newPoolPruner(eth.blockchain, chainDb, chainConfig.GetChainID().Uint64(), from, keep)
		eth.poolPruner.start()
		log.Info("Shielded pool proof pruning on", "keep", keep, "gate", from, "syncmode", config.SyncMode)
	} else if config.PoolPrune {
		log.Warn("--pool.prune: the pool is not on this chain, nothing to prune")
	}

	eth.miner = miner.New(eth, &config.Miner, eth.blockchain.Config(), eth.EventMux(), eth.engine, eth.isLocalBlock)
	eth.miner.SetExtra(makeExtraData(config.Miner.ExtraData))

	eth.APIBackend = &EthAPIBackend{stack.Config().ExtRPCEnabled(), stack.Config().AllowUnprotectedTxs, eth, nil}
	if eth.APIBackend.allowUnprotectedTxs {
		log.Info("Unprotected transactions allowed")
	}
	gpoParams := config.GPO
	if gpoParams.Default == nil {
		gpoParams.Default = config.Miner.GasPrice
	}
	eth.APIBackend.gpo = gasprice.NewOracle(eth.APIBackend, gpoParams)

	// Setup DNS discovery iterators.
	dnsclient := dnsdisc.NewClient(dnsdisc.Config{})
	eth.ethDialCandidates, err = dnsclient.NewIterator(eth.config.EthDiscoveryURLs...)
	if err != nil {
		return nil, err
	}
	eth.snapDialCandidates, err = dnsclient.NewIterator(eth.config.SnapDiscoveryURLs...)
	if err != nil {
		return nil, err
	}
	// The share gossip (eth/protocols/ishr, PA-46; the founder's order of
	// 16:0x UK, 12 October 2026): on an engine with the share lane, the
	// sub-protocol runs beside eth on the same peer links, every node
	// forwarding a valid share once and any node accepting a direct
	// submission, so a solo miner submits to any node, sealer or not.
	if se, ok := eth.engine.(ishr.ShareEngine); ok {
		if gc, ok := eth.engine.(interface {
			SetGossipChain(consensus.ChainHeaderReader)
		}); ok {
			gc.SetGossipChain(eth.blockchain)
		}
		eth.shares = ishr.NewHandler(&shareBackend{chain: eth.blockchain, engine: se})
		if gs, ok := eth.engine.(interface{ SetGossipStats(func() ishr.Stats) }); ok {
			gs.SetGossipStats(eth.shares.Stats)
		}
	}

	// Start the RPC service
	eth.netRPCService = ethapi.NewNetAPI(eth.p2pServer, networkID)

	// Register the backend on the node
	stack.RegisterAPIs(eth.APIs())
	stack.RegisterProtocols(eth.Protocols())
	stack.RegisterLifecycle(eth)

	// Successful startup; push a marker and check previous unclean shutdowns.
	eth.shutdownTracker.MarkStartup()

	return eth, nil
}

func makeExtraData(extra []byte) []byte {
	if len(extra) == 0 {
		// create default extradata
		extra, _ = rlp.EncodeToBytes([]interface{}{
			uint(params.VersionMajor<<16 | params.VersionMinor<<8 | params.VersionPatch),
			"CoreGeth",
			runtime.Version(),
			runtime.GOOS,
		})
	}
	if uint64(len(extra)) > vars.MaximumExtraDataSize {
		log.Warn("Miner extra data exceed limit", "extra", hexutil.Bytes(extra), "limit", vars.MaximumExtraDataSize)
		extra = nil
	}
	return extra
}

// APIs return the collection of RPC services the ethereum package offers.
// NOTE, some of these services probably need to be moved to somewhere else.
func (s *Ethereum) APIs() []rpc.API {
	apis := ethapi.GetAPIs(s.APIBackend)

	// Append any APIs exposed explicitly by the consensus engine
	apis = append(apis, s.engine.APIs(s.BlockChain())...)

	// Append all the local APIs and return
	return append(apis, []rpc.API{
		{
			Namespace: "eth",
			Service:   NewEthereumAPI(s),
		}, {
			Namespace: "miner",
			Service:   NewMinerAPI(s),
		}, {
			Namespace: "eth",
			Service:   downloader.NewDownloaderAPI(s.handler.downloader, s.blockchain, s.eventMux),
		}, {
			Namespace: "admin",
			Service:   NewAdminAPI(s),
		}, {
			Namespace: "debug",
			Service:   NewDebugAPI(s),
		}, {
			Namespace: "net",
			Service:   s.netRPCService,
		},
	}...)
}

func (s *Ethereum) ResetWithGenesisBlock(gb *types.Block) {
	s.blockchain.ResetWithGenesisBlock(gb)
}

func (s *Ethereum) Etherbase() (eb common.Address, err error) {
	s.lock.RLock()
	etherbase := s.etherbase
	s.lock.RUnlock()

	if etherbase != (common.Address{}) {
		return etherbase, nil
	}
	return common.Address{}, errors.New("etherbase must be explicitly specified")
}

// isLocalBlock checks whether the specified block is mined
// by local miner accounts.
//
// We regard two types of accounts as local miner account: etherbase
// and accounts specified via `txpool.locals` flag.
func (s *Ethereum) isLocalBlock(header *types.Header) bool {
	author, err := s.engine.Author(header)
	if err != nil {
		log.Warn("Failed to retrieve block author", "number", header.Number.Uint64(), "hash", header.Hash(), "err", err)
		return false
	}
	// Check whether the given address is etherbase.
	s.lock.RLock()
	etherbase := s.etherbase
	s.lock.RUnlock()
	if author == etherbase {
		return true
	}
	// Check whether the given address is specified by `txpool.local`
	// CLI flag.
	for _, account := range s.config.TxPool.Locals {
		if account == author {
			return true
		}
	}
	return false
}

// shouldPreserve checks whether we should preserve the given block
// during the chain reorg depending on whether the author of block
// is a local account.
func (s *Ethereum) shouldPreserve(header *types.Header) bool {
	// The reason we need to disable the self-reorg preserving for clique
	// is it can be probable to introduce a deadlock.
	//
	// e.g. If there are 7 available signers
	//
	// r1   A
	// r2     B
	// r3       C
	// r4         D
	// r5   A      [X] F G
	// r6    [X]
	//
	// In the round5, the in-turn signer E is offline, so the worst case
	// is A, F and G sign the block of round5 and reject the block of opponents
	// and in the round6, the last available signer B is offline, the whole
	// network is stuck.
	return s.isLocalBlock(header)
}

// SetEtherbase sets the mining reward address.
func (s *Ethereum) SetEtherbase(etherbase common.Address) {
	s.lock.Lock()
	s.etherbase = etherbase
	s.lock.Unlock()

	s.miner.SetEtherbase(etherbase)
}

// StartMining starts the miner with the given number of CPU threads. If mining
// is already running, this method adjust the number of threads allowed to use
// and updates the minimum price required by the transaction pool.
func (s *Ethereum) StartMining(threads int) error {
	// Update the thread count within the consensus engine
	type threaded interface {
		SetThreads(threads int)
	}
	if th, ok := s.engine.(threaded); ok {
		log.Info("Updated mining threads", "threads", threads)
		if threads == 0 {
			threads = -1 // Disable the miner from within
		}
		th.SetThreads(threads)
	}
	// If the miner was not running, initialize it
	if !s.IsMining() {
		// Propagate the initial price point to the transaction pool
		s.lock.RLock()
		price := s.gasPrice
		s.lock.RUnlock()
		s.txPool.SetGasTip(price)

		// Configure the local mining address
		// The payout address must be set; the worker uses it as the coinbase.
		if _, err := s.Etherbase(); err != nil {
			log.Error("Cannot start mining without etherbase", "err", err)
			return fmt.Errorf("etherbase missing: %v", err)
		}
		// If mining is started, we can disable the transaction rejection mechanism
		// introduced to speed sync times. A fresh node still at genesis under the
		// pruned sync keeps its snap sync (the miner has nothing to seal on yet).
		if s.handler.snapSync.Load() && s.blockchain.CurrentBlock().Number.Uint64() == 0 {
			s.handler.markSynced()
		} else {
			s.handler.enableSyncedFeatures()
		}

		go s.miner.Start()
	}
	return nil
}

// StopMining terminates the miner, both at the consensus engine level as well as
// at the block creation level.
func (s *Ethereum) StopMining() {
	// Update the thread count within the consensus engine
	type threaded interface {
		SetThreads(threads int)
	}
	if th, ok := s.engine.(threaded); ok {
		th.SetThreads(-1)
	}
	// Stop the block creating itself
	s.miner.Stop()
}

func (s *Ethereum) IsMining() bool      { return s.miner.Mining() }
func (s *Ethereum) Miner() *miner.Miner { return s.miner }

func (s *Ethereum) AccountManager() *accounts.Manager { return s.accountManager }
func (s *Ethereum) BlockChain() *core.BlockChain      { return s.blockchain }
func (s *Ethereum) TxPool() *txpool.TxPool            { return s.txPool }
func (s *Ethereum) EventMux() *event.TypeMux          { return s.eventMux }
func (s *Ethereum) Engine() consensus.Engine          { return s.engine }
func (s *Ethereum) ChainDb() ethdb.Database           { return s.chainDb }
func (s *Ethereum) IsListening() bool                 { return true } // Always listening
func (s *Ethereum) EthVersion() int {
	return int(eth.MakeProtocols((*ethHandler)(s.handler), s.networkID, s.config.ProtocolVersions, s.ethDialCandidates)[0].Version)
}
func (s *Ethereum) NetVersion() uint64                 { return s.networkID }
func (s *Ethereum) Downloader() *downloader.Downloader { return s.handler.downloader }
func (s *Ethereum) Synced() bool                       { return s.handler.synced.Load() }
func (s *Ethereum) SetSynced()                         { s.handler.enableSyncedFeatures() }
func (s *Ethereum) ArchiveMode() bool                  { return s.config.NoPruning }
func (s *Ethereum) BloomIndexer() *core.ChainIndexer   { return s.bloomIndexer }
func (s *Ethereum) SyncMode() downloader.SyncMode {
	mode, _ := s.handler.chainSync.modeAndLocalHead()
	return mode
}

// Protocols returns all the currently configured
// network protocols to start.
func (s *Ethereum) Protocols() []p2p.Protocol {
	protos := eth.MakeProtocols((*ethHandler)(s.handler), s.networkID, s.config.ProtocolVersions, s.ethDialCandidates)
	if s.config.SnapshotCache > 0 {
		protos = append(protos, snap.MakeProtocols((*snapHandler)(s.handler), s.snapDialCandidates)...)
	}
	if s.shares != nil {
		protos = append(protos, ishr.MakeProtocols(s.shares, nil)...)
	}
	return protos
}

// shareBackend is the share gossip's view of the node (ishr.Backend).
type shareBackend struct {
	chain  *core.BlockChain
	engine ishr.ShareEngine
}

// blockReader is what canonicalBranchBack reads: a block by hash and
// number (core.BlockChain).
type blockReader interface {
	GetBlock(hash common.Hash, number uint64) *types.Block
}

// canonicalBranchBack returns the blocks between head's parent and the
// block stopAt, oldest first, walking the parent links from head (so the
// blocks are head's own ancestors, the canonical chain once head is the
// head), at most max of them and never the genesis; an empty list when
// head's parent is stopAt (the common case: one block on the last head)
// or when a block is missing. These are the blocks a head event does not
// announce: the ones a reorg re-added or a segment brought in under its
// last.
func canonicalBranchBack(chain blockReader, head *types.Block, stopAt common.Hash, max int) []*types.Block {
	var branch []*types.Block
	for h, n := head.ParentHash(), head.NumberU64()-1; n > 0 && len(branch) < max && h != stopAt; n-- {
		blk := chain.GetBlock(h, n)
		if blk == nil {
			break
		}
		branch = append(branch, blk)
		h = blk.ParentHash()
	}
	for i, j := 0, len(branch)-1; i < j; i, j = i+1, j-1 {
		branch[i], branch[j] = branch[j], branch[i]
	}
	return branch
}

func (b *shareBackend) Chain() ishr.Chain             { return b }
func (b *shareBackend) ShareEngine() ishr.ShareEngine { return b.engine }
func (b *shareBackend) CurrentHeader() *types.Header  { return b.chain.CurrentHeader() }

// SubscribeHead adapts the blockchain's head event for the share gossip
// (ishr names no core type, so the engine may import it without a cycle).
func (b *shareBackend) SubscribeHead(fn func(*types.Header)) func() {
	ch := make(chan core.ChainHeadEvent, 16)
	sub := b.chain.SubscribeChainHeadEvent(ch)
	quit := make(chan struct{})
	// The pool learns every CANONICAL carriage here, not only this sealer's
	// own (devnet7's stall at 350, 11 October 2026: the gossip put one share
	// in every pool, the other sealer carried it, this pool kept it, every
	// template re-carried it and the import rule refused the block).
	mc, hasMark := b.engine.(interface {
		MarkCarried(consensus.ChainHeaderReader, *types.Block)
	})
	// The endorser's first-seen record (F-44) and the pool's uncarry
	// (identity 4) read the SIDE events too: a block that loses its height
	// is still the first seen there, and a share it carried comes back to
	// the pool when a reorg drops it.
	ns, hasSeen := b.engine.(interface{ NoteSeen(*types.Header) })
	uc, hasUnmark := b.engine.(interface{ UnmarkCarried(*types.Block) })
	lr, hasLand := b.engine.(interface {
		LandRegistry(consensus.ChainHeaderReader, *types.Block)
		UnlandRegistry(*types.Block)
	})
	// The arrival record (policy part 4, the past-drift refusal) is taken
	// on the LIVE path only: a head event one height above the last head
	// (a single announced block) or a side event (a sibling announced);
	// a head event further ahead is a sync's segment or a branch received
	// whole (a healed partition) and gets no record, so a minority side's
	// sealers never walk back to the fork point (the design seat's bound 1).
	na, hasArrival := b.engine.(interface{ NoteArrival(*types.Header) })
	var (
		lastHead     uint64
		lastHeadHash common.Hash
	)
	if cur := b.chain.CurrentHeader(); cur != nil {
		lastHead = cur.Number.Uint64()
		lastHeadHash = cur.Hash()
	}
	// markBranch marks the carriage of every canonical block between the
	// last head seen here and the new head's parent: the chain posts a
	// head event for the new head alone, so the blocks a reorg re-adds to
	// the canonical chain (dropped once, their shares put back) and the
	// blocks a segment brings in under its last would otherwise never be
	// marked, and their shares would stand pooled on this node while the
	// chain carries them (the ninth development chain's 785 on pod d, 12
	// October 2026, re-added at 05:57:31Z with 159 shares put back; the
	// account agreed with the chain there only because a later head's
	// carriage covered the same shares). Bounded by the recency window:
	// a share older than that has expired from the pool already.
	const markBranchMax = 64
	markBranch := func(head *types.Block) {
		if !hasMark && !hasLand {
			return
		}
		for _, blk := range canonicalBranchBack(b.chain, head, lastHeadHash, markBranchMax) {
			if hasLand {
				lr.LandRegistry(b.chain, blk)
			}
			if hasMark {
				mc.MarkCarried(b.chain, blk)
			}
		}
	}
	side := make(chan core.ChainSideEvent, 16)
	sideSub := b.chain.SubscribeChainSideEvent(side)
	// Cut B: the registry landing walks EVERY canonical block as it is
	// imported (the chain event, one per block, the initial sync from
	// genesis included), not the head event alone (the consensus chair's
	// reading, 15 October 2026); the head-event walk above stays as the
	// reorganisation's re-add path.
	chainCh := make(chan core.ChainEvent, 256)
	chainSub := b.chain.SubscribeChainEvent(chainCh)
	// The start-up rescan (the review lead's residual): the canonical chain
	// above the last journaled landing is read for registry transactions
	// before the live walk, so a lost journal write loses nothing.
	if hasLand {
		if lh, ok := b.engine.(interface{ VectorLandingHeight() uint64 }); ok {
			from := lh.VectorLandingHeight() + 1
			if cur := b.chain.CurrentBlock(); cur != nil {
				for n := from; n <= cur.Number.Uint64(); n++ {
					if blk := b.chain.GetBlockByNumber(n); blk != nil && len(blk.Transactions()) > 0 {
						lr.LandRegistry(b.chain, blk)
					}
				}
			}
		}
	}
	go func() {
		defer sideSub.Unsubscribe()
		defer chainSub.Unsubscribe()
		for {
			select {
			case ev := <-chainCh:
				if hasLand && ev.Block != nil {
					lr.LandRegistry(b.chain, ev.Block)
				}
			case ev := <-ch:
				if ev.Block != nil {
					n := ev.Block.NumberU64()
					if hasArrival && n == lastHead+1 {
						na.NoteArrival(ev.Block.Header())
					}
					if hasSeen {
						ns.NoteSeen(ev.Block.Header())
					}
					markBranch(ev.Block)
					if hasLand {
						lr.LandRegistry(b.chain, ev.Block)
					}
					if hasMark {
						mc.MarkCarried(b.chain, ev.Block)
					}
					lastHead = n
					lastHeadHash = ev.Block.Hash()
					fn(ev.Block.Header())
				}
			case ev := <-side:
				if ev.Block != nil {
					if hasArrival {
						na.NoteArrival(ev.Block.Header())
					}
					if hasSeen {
						ns.NoteSeen(ev.Block.Header())
					}
					if hasUnmark {
						uc.UnmarkCarried(ev.Block)
					}
					if hasLand {
						lr.UnlandRegistry(ev.Block)
					}
				}
			case <-sideSub.Err():
				return
			case <-chainSub.Err():
				return
			case <-sub.Err():
				return
			case <-quit:
				return
			}
		}
	}()
	return func() { sub.Unsubscribe(); close(quit) }
}

// Start implements node.Lifecycle, starting all internal goroutines needed by the
// Ethereum protocol implementation.
func (s *Ethereum) Start() error {
	eth.StartENRUpdater(s.blockchain, s.p2pServer.LocalNode())
	// The resident prover at node start (F-458): started here, after every
	// flag is applied, so the sealer's first keyed seal pays no warm-up.
	if sp, ok := s.engine.(interface{ StartPiResident() }); ok {
		sp.StartPiResident()
	}
	if s.shares != nil {
		s.shares.Start()
	}

	// Start the bloom bits servicing goroutines
	s.startBloomHandlers(vars.BloomBitsBlocks)

	// Regularly update shutdown marker
	s.shutdownTracker.Start()

	// Figure out a max peers count based on the server limits
	maxPeers := s.p2pServer.MaxPeers
	if s.config.LightServ > 0 {
		if s.config.LightPeers >= s.p2pServer.MaxPeers {
			return fmt.Errorf("invalid peer config: light peer count (%d) >= total peer count (%d)", s.config.LightPeers, s.p2pServer.MaxPeers)
		}
		maxPeers -= s.config.LightPeers
	}
	// Start the networking layer and the light server if requested
	s.handler.Start(maxPeers)
	return nil
}

// Stop implements node.Lifecycle, terminating all internal goroutines used by the
// Ethereum protocol.
func (s *Ethereum) Stop() error {
	// Stop all the peer-related stuff first.
	s.ethDialCandidates.Close()
	s.snapDialCandidates.Close()
	if s.shares != nil {
		s.shares.Stop()
	}
	s.handler.Stop()
	if s.poolPruner != nil {
		s.poolPruner.stop()
	}

	// Then stop everything else.
	s.bloomIndexer.Close()
	close(s.closeBloomHandler)
	s.txPool.Close()
	s.miner.Close()
	s.blockchain.Stop()
	s.engine.Close()

	// Clean shutdown marker as the last thing before closing db
	s.shutdownTracker.Stop()

	s.chainDb.Close()
	s.eventMux.Stop()

	return nil
}

// vectorReregistration is igneum.Reregistration (the admission moved to
// the engine for cut B's chain-held landings, 15 October 2026).
func vectorReregistration(stored, given *ctypes.IgneumVector, head uint64) (rewindTo uint64, err error) {
	return igneum.Reregistration(stored, given, head)
}

// vectorReregistrationFrom is vectorReregistration under the chain's
// notice-floor gate (F-192): a fresh node's start document at a head under
// the gate loads as the chain's nodes loaded it.
func vectorReregistrationFrom(stored, given *ctypes.IgneumVector, head uint64, noticeFrom uint64) (rewindTo uint64, err error) {
	return igneum.ReregistrationFrom(stored, given, head, noticeFrom)
}

func jsonOf(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

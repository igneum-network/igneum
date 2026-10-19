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

// Package igneum is the Igneum proof-of-work consensus engine
// (docs/protocol-a/DESIGN.md section 4; PA-06). It implements consensus.Engine
// and delegates three things: the proof of work (pow.Algorithm, chosen by name
// from the chain configuration, Phase Zero decides which), the difficulty
// (difficulty.Params, protocol engineer B's ASERT candidate) and the rewards
// (issuance.Table, protocol engineer B's schedule with the 99/1 split).
// The engine itself owns header validity, the seal binding and the mining RPC.
package igneum

import (
	"crypto/ed25519"
	"fmt"
	"math/big"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/schedule"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/rpc"
)

// DefaultMaxFutureDrift is how far ahead of the local clock a header's
// timestamp may be (D-A6, default 15 s).
const DefaultMaxFutureDrift = 15 * time.Second

// Config configures the engine.
type Config struct {
	// Algorithm is the proof of work. Required unless FakePoW is set.
	Algorithm pow.Algorithm
	// Parts are the difficulty rule, the issuance schedule and the fund
	// address. Required.
	Parts Parts
	// MaxFutureDrift bounds header.Time against the local clock; zero means
	// DefaultMaxFutureDrift.
	MaxFutureDrift time.Duration

	// FakePoW accepts every seal without verification and seals with a zero
	// nonce: for tests of everything that is not the proof of work.
	FakePoW bool
	// FakeFail makes seal verification fail at exactly this block number in
	// fake mode (zero disables).
	FakeFail uint64
	// FakeDelay sleeps this long in fake seal verification.
	FakeDelay time.Duration
	// FakeFull accepts every header and uncle list without verification, in
	// fake mode: for tests that build chains no rule would pass.
	FakeFull bool
	// FixtureDifficulty accepts a header's difficulty as given (zero included),
	// in fake mode: for the upstream fixture suites, whose blocks carry another
	// rule's difficulty (tests/igneum_translate.go, PA-09b).
	FixtureDifficulty bool
	// FixtureReward, in fake mode, is credited to the coinbase in place of the
	// issuance schedule: for the upstream fixture suites, whose post-states
	// carry another rule's reward (tests/igneum_translate.go, PA-09b).
	FixtureReward func(header *types.Header) *big.Int

	// Notify lists URLs to POST new work to (the miner notification).
	Notify []string
	// NotifyFull sends the full pending block header in the notification.
	NotifyFull bool
	// NoVerify skips verification of remotely submitted solutions (for
	// trusted pool setups only).
	NoVerify bool
	// Log overrides the engine logger.
	Log log.Logger
	// Rand seeds the CPU sealer's nonce start; nil uses crypto/rand.
	Rand *rand.Rand

	// PiVerify turns the proof pi's verification on (pi.go; the flag
	// --igneum.pi-verify): a keyed seal must carry a valid proof. PiProver
	// is the prover CLI this node's own seals use (--igneum.pi-prover) and
	// PiWorkDir its files' directory. Set through SetPi.
	PiVerify  bool
	PiProver  string
	PiWorkDir string
	// PiProverFallback is a second prover CLI (the CPU build) a seal falls
	// back to when the prover named by PiProver cannot run on this host
	// (F-89: the CUDA build aborts on a driver it cannot use, resident
	// and per-seal alike); empty keeps the per-seal PiProver as the
	// fallback.
	PiProverFallback string
	// VerifierCore is the reserved verifier thread's CPU
	// (--igneum.verifier-core): 0, the zero value and the SHIPPED
	// DEFAULT, pins the header-verification worker to the LAST CPU of
	// the process's allowed set on the TWO-CPU minimum node and nowhere
	// else (F-97: on a 96-core box it serialised four nodes' verification
	// on one CPU); N >= 1 the N-th allowed CPU on any host; -1 off (the
	// auditor's closure 22 (3): the pinned thread settles the budget only
	// as the shipped default). A pinned verifier runs one worker.
	VerifierCore int
	// PiResident runs the prover as one resident process for the sealer's
	// life (piserve.go; --igneum.pi-resident) instead of one process per
	// seal; this node's own receipts are then trusted at its own import.
	PiResident bool
	// SealWorker is the GPU worker executable the keyed search runs on
	// (sealworker.go; --igneum.seal-worker) and SealWorkerDevice the card
	// it opens (--igneum.seal-worker-device). Set through SetSealWorker; a
	// node with a worker seals on no CPU thread.
	SealWorker       string
	SealWorkerDevice string
	// VerifyBudgetMs is the verifier's global CPU budget in milliseconds a
	// second across every peer (verifybudget.go; --igneum.verify-budget-ms);
	// zero is OFF. Set through SetVerifyBudget.
	VerifyBudgetMs uint64
}

// Engine is the Igneum consensus engine.
type Engine struct {
	// unpaidLogged is the last height whose F-166 idle line was logged.
	unpaidLogged uint64
	// bondedSigner logs F-179's draw once per height.
	bondedSigner bondedSignerDraw
	config       Config
	algo         pow.Algorithm
	log          log.Logger
	// headers is the engine's own chain reader, kept from the anchor of
	// every import and seal, from which the per-period programme's anchor
	// is resolved along a header's branch (program.go); programAnchors is
	// the resolution cache by branch tip.
	headers        atomic.Value
	programMu      sync.Mutex
	programAnchors map[programKey]common.Hash
	// budget is the verifier's global CPU budget (verifybudget.go).
	budget *verifyBudget
	// VectorWork (vector.go): the per-block coordinate state by hash and
	// the coordinate this node seals under.
	vectorMu    sync.Mutex
	vectorCache map[common.Hash]*vectorState
	coordinate  uint8
	// sealPending (F-146, 15 October 2026): --igneum.coord named a
	// coordinate beyond the registration the menu holds; under a
	// chain-held registration (cut B) the coordinate may be one a registry
	// transaction added, so the seal waits for the landing that holds it
	// (sealFromRegistry) instead of refusing the start against the stored
	// menu alone.
	sealPending bool

	lock     sync.Mutex
	rand     *rand.Rand
	threads  int
	update   chan struct{}
	hashrate metrics.Meter
	// search is the keyed search's own counter (the live rate the meter's
	// one-minute average cannot read under a sealer's short duty: a search
	// of a second every twenty on the ninth development chain read 64 to
	// 2,775 on the meter against the search's own tens of thousands, 12
	// October 2026): the attempts of the search in flight and its start,
	// and the last finished search's attempts and wall time, served by
	// igneum_searchRate for the live dMin reading (the project's rule of 04:00
	// UK, 12 October 2026).
	search searchCounter
	remote *remoteSealer
	// The keyed seal worker (sealworker.go): the GPU child the keyed
	// search runs on under --igneum.seal-worker; nil without one.
	worker *sealWorker
	// heldKeyedDraw is F-52's once-per-height log (sortition.go).
	heldKeyedDraw heldDraw
	// The miner-bound work authority prototype (authority.go): the key this
	// node seals under, and the test harness's lax switch.
	bondKey      *common.Hash
	authorityLax bool
	// The sortition-gated rule (sortition.go): the node's signer and the
	// per-epoch cache with the chain it reads snapshots from.
	signer    ed25519.PrivateKey
	sortition sortitionState
	// The keyed form (the hostile-key rule, PROTOTYPE): the bond's secret
	// this node folds into its own attempts (the same 32 bytes as the
	// signer's seed) and the bond key it derives, pk = G(sk).
	bondSK    *[32]byte
	keyedBond *common.Hash
	// keyed marks the templates this node assembled that seal keyed (the
	// genesis form; sortition_genesis.go).
	keyed keyedSeals
	// piM keeps the walk's messages m_i for the body stage's proof check
	// (pi.go).
	piM piMessages
	// claims is the first-finder rule of a template (pi.go sealClaims).
	claims sealClaims
	// piSrv is the resident prover's session and own the receipts this node
	// made (piserve.go).
	piSrv piServer
	own   ownReceipts
	// The one-seal-per-height mark (sealer.go deliverSeal): the last
	// (height, parent) pair this node delivered a seal for.
	sealedMu     sync.Mutex
	sealedSet    bool
	sealedHeight uint64
	sealedParent common.Hash
	// endorse is the settlement rule's state (endorse.go): the chain id,
	// the pool, the evidence, the endorser.
	endorse endorseState
	// seals is the node-local ledger of seals by (height, key)
	// (sealledger.go): one block per key per height on the relay and the
	// fork choice, the second kept as evidence.
	seals sealLedger
	// mined and sharePool are the share lane's state (mineshares.go): the
	// stash the derivation needs again in Finalize, and the sealer's
	// accepted-share pool.
	mined     minedStash
	sharePool sharePool
	// registry is cut B's chain-held registration state (registry_landing.go).
	registry vectorRegistry
	// shareJournal is the pool's journal on disk (sharejournal.go, PA-44):
	// the accepted, not yet carried shares survive a restart.
	shareJournal shareJournal
	// lab is the research instruments' state (labmode.go): the withholding
	// sealer and the lax-endorsement sealer, refused off the lab build.
	lab labMode
	// gossip is the share gossip's engine side (sharegossip.go, PA-46):
	// the index by wire hash, the announcer and the chain reader.
	gossip shareGossip
	// memo is the authority memo (authoritymemo.go, F-39): a parent's
	// post-state facts for its children's checks, recorded at Finalize.
	memo authorityMemo
	// sealStaggerMs and lotRank are PA-52's sealer policy (stagger.go):
	// the stagger between eligible keys' search starts and its rank source.
	sealStaggerMs uint64
	lotRank       lotRanker
	// balancedCache and balancedMu are the balanced reward's window
	// cache (balanced.go): an ancestor's carried list, coordinate and
	// share difficulty by block hash.
	balancedCache map[common.Hash]*balancedBlock
	balancedMu    sync.Mutex
	lastV2        v2Terms
	// openSpreadMs is the open-seat spread (stagger.go, F-114): a
	// node's value, the slot width in milliseconds (0 off).
	openSpreadMs uint64
	// dag is the block DAG ordering's state (dagform.go): the gate, the
	// colouring by hash, the tips, the readers' records.
	dag dagState
	// proofHook runs in mine between the claim and the proof (a test's
	// stand-in for the CPU proof's 4 to 10 s; nil in production).
	proofHook func()
	// forks are the chain's rule forks (ctypes.IgneumRuleForks, set by the
	// backend from the chain id; nil runs every rule's latest form from
	// genesis): the heights at which a rule's form changed on a running
	// chain by a swap in place (clock.go sealMeasureFirstForm).
	forks *ctypes.IgneumRuleForks
	// payBoundUnscaledUntil is the last height whose minimum pay rule
	// derives with the unscaled y bound (F-192, paybound.go; set by the
	// backend from the chain id; 0 none).
	payBoundUnscaledUntil uint64
	// firstSeenFrom is the first height at which the fork choice's
	// same-height rule is in force (firstseen.go; set by the backend from
	// the chain id; 0 off).
	firstSeenFrom uint64
	// forkWeightFrom is the first height at which the served measure weighs
	// a lane's blocks by its running difficulty at the race's common
	// ancestor (vector.go laneWeightsBy; set by the backend from the chain
	// id, params.IgneumChainForkWeightFrom; 0 off: dRef as before).
	forkWeightFrom uint64
	// shareLaneFrom is the first height at which the share target's
	// difficulty is the lane's (mineshares.go shareLaneD; F-690; set by the
	// backend from the chain id, params.IgneumChainShareLaneFrom; 0 off).
	shareLaneFrom uint64
	// sponsoredFrom is the first height at which the sponsored lane's rule
	// is in force (sponsored.go sponsoredOn; set by the backend from the
	// chain id, params.IgneumChainSponsoredFrom; 0 off, F-703).
	sponsoredFrom uint64
	// poolFrom is the first height at which the shielded pool's rule is in
	// force (pool_settle.go poolOn; set by the backend from the chain id,
	// params.IgneumChainPoolFrom; 0 off: every chain of record) and
	// poolVerifier the proof verifier its spends are checked through (nil:
	// pool.NoVerifier).
	poolFrom     uint64
	poolVerifier pool.Verifier
	// poolStore is the database the retained records of pruned blocks
	// live in (pool_settle.go SetPoolStore; the pruned-sync cut).
	poolStore ethdb.KeyValueStore
	// documentRevealFrom is the first height at which a programme's reveal
	// depth is resolved from the coordinate's registration for every
	// instance of the lane's document (program.go programRevealOf; F-730;
	// set by the backend from the chain id,
	// params.IgneumChainDocumentRevealFrom; 0 the form before: the base
	// instance alone). Read on every hash's anchor resolution, so atomic.
	documentRevealFrom atomic.Uint64
	// registryNoticeFrom is the first landing height at which the notice
	// floor binds (F-192, ReregistrationFrom; set by the backend from the
	// chain id; 0 everywhere).
	registryNoticeFrom uint64
	// fullDataset switches the algorithm to full-dataset contexts once,
	// on the first CPU search (sealer.go).
	fullDataset sync.Once
	closeMu     sync.Once
}

// New builds an engine. It returns an error rather than a half-configured
// engine when a required part is missing.
func New(config Config) (*Engine, error) {
	if config.Algorithm == nil && !config.FakePoW {
		return nil, fmt.Errorf("igneum: no proof-of-work algorithm configured")
	}
	if !config.FakePoW && (config.FakeFull || config.FixtureDifficulty || config.FixtureReward != nil) {
		return nil, fmt.Errorf("igneum: the fake and fixture settings need fake mode")
	}
	if m, ok := config.Algorithm.(*vector.Menu); ok && m != nil && config.Parts.Difficulty.Min != nil {
		// The registration check (VECTORWORK.md revision 6, section 4):
		// every coordinate's reference difficulty at or above its floor
		// (its own dMin, else the chain's minimum), so no coordinate runs
		// off its schedule from block one (vectorwork2's tensor at 7,120
		// under the floor 8,192, 13 October 2026); a failing registration
		// is refused at start.
		for i := 0; i < m.Count(); i++ {
			coord, _ := m.Coordinate(uint8(i))
			// The base form and every in-place change's (LANE-MODIFICATION.md
			// section 1), each against its own floor.
			forms := []struct{ dRef, dMin *big.Int }{{coord.DRef, coord.DMin}}
			for _, ch := range m.Changes(uint8(i)) {
				forms = append(forms, struct{ dRef, dMin *big.Int }{ch.DRef, ch.DMin})
			}
			for _, f := range forms {
				floor := config.Parts.Difficulty.Min
				if f.dMin != nil {
					floor = f.dMin
				}
				if f.dRef.Cmp(floor) < 0 {
					// A warning here, not a refusal: the engine is built at
					// every start of a chain whose registration is already
					// sealed (vectorwork2's four nodes refused to restart on
					// the check, 02:19Z, 13 October 2026); the refusal is at
					// the genesis (cmd/utils/flags.go for the development
					// chain).
					log.Warn("VectorWork coordinate registers a reference under its floor (the controller runs on the floor from block one)", "coordinate", i, "name", coord.Name, "dRef", f.dRef, "floor", floor)
				}
			}
		}
	}
	if config.Parts.Difficulty.Min == nil || config.Parts.Difficulty.Target == 0 {
		return nil, fmt.Errorf("igneum: no difficulty rule configured")
	}
	if config.Parts.Issuance == nil {
		return nil, fmt.Errorf("igneum: no issuance schedule configured")
	}
	if config.MaxFutureDrift == 0 {
		config.MaxFutureDrift = DefaultMaxFutureDrift
	}
	// F-219 (16 October 2026): the references bound their per-context
	// working memory by the node's parallel verification width (the raster
	// form's buffer pool: at most VerifyWorkers slots a context, where a
	// per-OS-thread buffer grew the research chain's nodes 5 to 26 GB an
	// hour); the width is the engine's to set.
	pow.SetVerifyWidth(VerifyWorkers())
	e := &Engine{
		config: config,
		algo:   config.Algorithm,
		log:    log.Root(),
		rand:   config.Rand,
		update: make(chan struct{}),
		// Forced: the meter reads without --metrics, so igneum_getHashrate
		// and the dMin rule's live read work on every sealing node.
		hashrate:      metrics.NewMeterForced(),
		sealStaggerMs: DefaultSealStaggerMs,
	}
	if config.Log != nil {
		e.log = config.Log
	}
	// F-151: the chain-held registration's clock constants (tMin and
	// tailDelta from a constantsChanges height) reach the seal clock's
	// floors through the sortition rule's hook; the menu handle is read
	// at every call, so a landed re-registration binds without a restart.
	if m, ok := config.Algorithm.(*vector.Menu); ok && m != nil && config.Parts.Sortition != nil {
		if err := e.constantsWithinClock(m.Registration()); err != nil {
			return nil, fmt.Errorf("igneum: the vector registration's %v", err)
		}
		config.Parts.Sortition.ConstantsAt = func(height uint64) (uint64, uint64) {
			c := m.ConstantsAt(height)
			return c.TMin, c.TailDelta
		}
	}
	// The verifier's global CPU budget (verifybudget.go); OFF at zero.
	e.budget = newVerifyBudget(config.VerifyBudgetMs)
	// PA-52's rank source: the node team's lot order among the keys eligible
	// at the round (forkchoice.go).
	e.lotRank = e.LotRankAt
	e.remote = startRemoteSealer(e, config.Notify, config.NoVerify)
	return e, nil
}

// NewFromChainConfig builds the engine named by a chain configuration: the
// algorithm from the registry and the given parts.
//
// devMode comes from the --dev flag only, never from the genesis: it is the
// one thing that lets a development-only algorithm (null) run (F-02).
func NewFromChainConfig(cfg *ctypes.IgneumConfig, parts Parts, notify []string, noverify bool, devMode bool) (*Engine, error) {
	if cfg == nil {
		return nil, fmt.Errorf("igneum: chain configuration has no igneum engine block")
	}
	if cfg.Algorithm == "" {
		return nil, fmt.Errorf("igneum: no proof-of-work algorithm is selected for this chain (Phase Zero decides it); run --dev or init a genesis that names one")
	}
	var (
		algo pow.Algorithm
		err  error
	)
	build := pow.New
	if devMode {
		build = pow.NewDev
	}
	if cfg.Vector != nil {
		// VectorWork (vector.go): one relation per coordinate, dispatched
		// by the block's tag; the menu is the engine's algorithm and the
		// chain's Algorithm names its first coordinate's for the readers.
		algo, err = vector.New(cfg.Vector, build)
	} else if cfg.Schedule != nil {
		// The bounded menu: one algorithm dispatching by height over the
		// schedule's documents (consensus/igneum/pow/schedule).
		algo, err = schedule.New(cfg.Algorithm, cfg.AlgorithmParams, cfg.Schedule, build)
	} else {
		algo, err = build(cfg.Algorithm, cfg.AlgorithmParams)
	}
	if err != nil {
		return nil, err
	}
	return New(Config{
		Algorithm: algo,
		Parts:     parts,
		Notify:    notify,
		NoVerify:  noverify,
	})
}

// NewFaker returns an engine that accepts every seal; for tests.
func NewFaker(parts Parts) *Engine {
	e, err := New(Config{FakePoW: true, Parts: parts})
	if err != nil {
		panic(err)
	}
	return e
}

// Algorithm returns the proof-of-work algorithm in force (nil in fake mode).
func (e *Engine) Algorithm() pow.Algorithm { return e.algo }

// Parts returns the difficulty rule, the issuance schedule and the fund address.
func (e *Engine) Parts() Parts { return e.config.Parts }

// Close implements consensus.Engine.
func (e *Engine) Close() error {
	e.closeMu.Do(func() {
		if e.remote != nil {
			close(e.remote.requestExit)
			<-e.remote.exitCh
		}
		e.closeShareJournal()
		e.ClosePiServer()
		e.lock.Lock()
		w := e.worker
		e.lock.Unlock()
		if w != nil {
			w.Stop()
		}
	})
	return nil
}

// APIs implements consensus.Engine, returning the mining RPC (DESIGN.md 4.5):
// the ethash-compatible eth_* methods and the igneum_* object forms.
func (e *Engine) APIs(chain consensus.ChainHeaderReader) []rpc.API {
	return []rpc.API{
		{Namespace: "eth", Service: &API{e}},
		{Namespace: "igneum", Service: NewIgneumAPI(e, chain)},
	}
}

// Hashrate implements consensus.PoW: the local CPU sealer plus the remote
// workers' reported rates.
func (e *Engine) Hashrate() float64 {
	var res = make(chan uint64, 1)
	select {
	case e.remote.fetchRateCh <- res:
	case <-e.remote.exitCh:
		return e.hashrate.Snapshot().Rate1()
	}
	return e.hashrate.Snapshot().Rate1() + float64(<-res)
}

// Threads returns the CPU sealing thread count (0 means all cores, below zero
// means no local sealing).
func (e *Engine) Threads() int {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.threads
}

// SetThreads sets the CPU sealing thread count and restarts any running seal.
// The CPU sealer is for the null-algorithm devnet and tests only.
func (e *Engine) SetThreads(threads int) {
	e.lock.Lock()
	defer e.lock.Unlock()
	e.threads = threads
	select {
	case e.update <- struct{}{}:
	default:
	}
}

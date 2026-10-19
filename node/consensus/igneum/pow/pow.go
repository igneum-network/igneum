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

// Package pow is the proof-of-work boundary of the Igneum consensus engine
// (docs/protocol-a/DESIGN.md, section 4.1; PA-06).
//
// The engine never names a hash function. It names an Algorithm, looked up by
// the name the chain configuration carries, and calls it through this
// interface. Phase Zero (docs/PHASE-ZERO.md) decides which algorithm the launch
// genesis names; until the founder ratifies that selection the only algorithm
// registered by the node itself is "null", for unit tests and devnets.
//
// Every backend (the CPU reference here, CUDA, OpenCL and Metal in the worker)
// consumes the same (headerHash, height, nonce) triple and produces the same
// (mixDigest, result) pair; Verify recomputes and compares. That is the single
// header binding the plan asks for (page 8: no ambiguous header binding, no
// reusable cheap lottery attempts).
package pow

import (
	"strconv"
	"encoding/json"
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

var (
	// ErrInvalidMixDigest is returned when the header's mix digest does not
	// match the one recomputed from (headerHash, height, nonce).
	ErrInvalidMixDigest = errors.New("igneum/pow: invalid mix digest")
	// ErrResultAboveTarget is returned when the recomputed result is greater
	// than 2^256 / difficulty.
	ErrResultAboveTarget = errors.New("igneum/pow: result above target")
	// ErrInvalidDifficulty is returned for a non-positive difficulty.
	ErrInvalidDifficulty = errors.New("igneum/pow: non-positive difficulty")
	// ErrEpochNotAdmitted is returned by an algorithm with epoch state for a
	// height whose epoch lies outside the window around the chain head the
	// engine anchored it to: no cache is built for it (the specification's
	// section 11 admission bound; security's F-18).
	ErrEpochNotAdmitted = errors.New("igneum/pow: epoch not admitted: outside the window around the chain head, no cache built")
	// ErrUnknownAlgorithm is returned by New for a name no algorithm registered.
	ErrUnknownAlgorithm = errors.New("igneum/pow: unknown algorithm")
	// ErrDevOnlyAlgorithm is returned by New for an algorithm registered as
	// development-only (the null stand-in): only NewDev, reached from the --dev
	// flag and never from a genesis file, may build it.
	ErrDevOnlyAlgorithm = errors.New("igneum/pow: development-only algorithm refused outside --dev")
)

// maxTarget is 2^256 - 1, the numerator of the target: the boundary at
// difficulty 1 is the largest 256-bit value, which every result satisfies
// (SPEC-R0A-v0.md section 7 and ENVELOPE-R0A-v0.md section 7; protocol B's
// E-1 found the earlier 2^256 numerator differing from the text at
// difficulty 1 and at every power of two).
var maxTarget = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// MaxTarget returns 2^256 - 1, the boundary every result satisfies (a copy).
func MaxTarget() *big.Int { return new(big.Int).Set(maxTarget) }

// Target returns floor((2^256 - 1) / difficulty), the boundary a result must
// not exceed. A nil, non-positive or over-wide (more than 256 bits)
// difficulty yields nil.
func Target(difficulty *big.Int) *big.Int {
	if difficulty == nil || difficulty.Sign() <= 0 || difficulty.BitLen() > 256 {
		return nil
	}
	return new(big.Int).Div(maxTarget, difficulty)
}

// WorkPackage is everything a worker needs to search one header. It is the
// object form of eth_getWork (igneum_getWork) and of the miner notification.
type WorkPackage struct {
	Algorithm  string      `json:"algorithm"`  // the registered algorithm name in force
	HeaderHash common.Hash `json:"headerHash"` // Engine.SealHash(header): every field except Nonce and MixDigest
	Height     uint64      `json:"height"`     // header.Number
	// ParentHash is the template's parent (header.ParentHash): a pool
	// checks the job sits on its source's head before it hashes. Absent
	// on a package without a block job.
	ParentHash common.Hash `json:"parentHash,omitempty"`
	// StaleTemplate (F-420, 17 October 2026) names why the package
	// carries NO block job: the node's own template is not on the chain's
	// head (its height at or behind the head, or its parent not the
	// head: the sealer waits on its round, or the head moved and the
	// template is not yet re-cut). HeaderHash is zero and Target is nil
	// (null) with it, the share-only package's own shape, so no worker
	// searches a job the node would drop at the seal; the share fields
	// stay the head's successor's. Absent when the block job is live.
	StaleTemplate string `json:"staleTemplate,omitempty"`
	Epoch      uint64      `json:"epoch"`      // Algorithm.Epoch(Height)
	SeedHash   common.Hash `json:"seedHash"`   // Algorithm.SeedHash(Height)
	Target     *big.Int    `json:"target"`     // floor((2^256 - 1) / difficulty)
	// Params is the algorithm's parameter document, when it has one (the
	// IgneumHash candidate's set: dataset, scratchpad, rounds, lanes, epoch,
	// item chain, the canonical bytes). The miner passes it to the worker
	// verbatim. Absent for an algorithm without parameters.
	Params any `json:"params,omitempty"`
	// ParamsDigest is the digest of the canonical parameter encoding (P for
	// the IgneumHash candidate): the job tag a miner keys its datasets and
	// results by beside the algorithm name, the epoch and the header hash
	// (the template identifier). Absent for an algorithm without parameters.
	ParamsDigest common.Hash `json:"paramsDigest,omitempty"`
	// NextParams, NextParamsDigest and NextParamsHeight name the next
	// document of a bounded menu and the height it takes force at, whenever
	// a schedule entry lies ahead of the template's height (the project's decision,
	// 10 October 2026): the miner may build the next dataset beside its
	// mining where two fit, instead of the cold path at the boundary. Absent
	// when no entry lies ahead. The chain-bound rule still decides the
	// document in force, and the worker still refuses a template whose
	// digest it cannot match.
	NextParams       any         `json:"nextParams,omitempty"`
	NextParamsDigest common.Hash `json:"nextParamsDigest,omitempty"`
	NextParamsHeight uint64      `json:"nextParamsHeight,omitempty"`
	// Coinbase and BondKey are the template's reward account and, under the
	// miner-bound work authority prototype, the bond key the template is
	// sealed under (both inside HeaderHash; stated so a worker can see what
	// it mines for). BondKey absent without the rule.
	Coinbase common.Address `json:"coinbase"`
	BondKey  *common.Hash   `json:"bondKey,omitempty"`
	// Coord, CoordName and CoordDifficulty are VectorWork's (the template's
	// coordinate tag inside HeaderHash, the coordinate's registered name,
	// and its difficulty as the controller output the template carries, the
	// same figure Target is derived from); Params then is that coordinate's
	// document. Absent on a chain without the vector form.
	Coord           *uint8   `json:"coord,omitempty"`
	CoordName       string   `json:"coordName,omitempty"`
	CoordDifficulty *big.Int `json:"coordDifficulty,omitempty"`
	// CoordLastHeight is the coordinate's last block height on the
	// template's branch (zero while it has none; the liveness figure).
	CoordLastHeight uint64 `json:"coordLastHeight,omitempty"`
	// ProgramAnchor and AnchorHeight are the resolved anchor of the
	// template's period on its parent's branch and the anchor block's
	// height (zero with the genesis hash below the first anchor), for a
	// programmed document: a pool or a worker computes the period's
	// programme from this anchor, which only the node resolves (the box
	// soak's 2,257 refusals on the zero anchor, 13 October 2026). Absent
	// for a document without a programme.
	ProgramAnchor *common.Hash `json:"programAnchor,omitempty"`
	AnchorHeight  uint64       `json:"anchorHeight,omitempty"`
	// KeyedRounds is S of the keyed document in force at the template's
	// height (the specification's section 0.9, bit 10; the hostile-key
	// rule): every attempt folds the miner's own bond secret in at S
	// rounds, and the solution is submitted with its S shares. Zero on an
	// unkeyed document. The secret never crosses this interface.
	KeyedRounds uint64 `json:"keyedRounds,omitempty"`
	// Payouts is the sortition prototype's payout list the template
	// carries (its root's prefix is Coinbase; the list itself travels in
	// the block body): a pool checks the template names its list before
	// hashing it. Absent without a list.
	Payouts []PayoutEntry `json:"payouts,omitempty"`
	// The share lane (LAUNCH-GUARDS.md 2.9; consensus/igneum/mineshares.go):
	// ShareTarget is the network-wide share target at the template's
	// height (the block target times the operating count, from chain
	// work), SharesPerBlock the operating count, ShareHeight and
	// ShareParent what a share's header names, ShareParams and
	// ShareParamsDigest the document a share is hashed with (the unkeyed
	// twin on a keyed row). Absent without the lane. A worker builds
	// H_s = KECCAK(RLP(ShareParent, ShareHeight, address, extraNonce)),
	// searches nonce on that document until result <= ShareTarget and
	// submits igneum_submitShare(address, extraNonce, nonce, mixDigest,
	// ShareHeight).
	ShareTarget       *big.Int    `json:"shareTarget,omitempty"`
	SharesPerBlock    uint64      `json:"sharesPerBlock,omitempty"`
	ShareHeight       uint64      `json:"shareHeight,omitempty"`
	ShareParent       common.Hash `json:"shareParent,omitempty"`
	ShareParams       any         `json:"shareParams,omitempty"`
	ShareParamsDigest common.Hash `json:"shareParamsDigest,omitempty"`
	// ShareStepMax is PA-53's bound on the declared target step (absent
	// or 0: the count form, every share at the share target, weight 1):
	// a worker may search at ShareTarget >> j for any j up to it and
	// submit igneum_submitShare with the step j as its last argument; the
	// share then weighs 2^j in the payout list. The expected weight per
	// hash is the same at every step; the step trades frequency for
	// variance and nothing else.
	ShareStepMax uint64 `json:"shareStepMax,omitempty"`
	// ShareOnly marks the work package of a node that seals nothing (the
	// share gossip's launch form: a worker names one node): the share
	// fields are set, the header hash is zero and there is no seal target;
	// a worker mines shares on it and submits them to the node that served
	// it, which pools and gossips them.
	ShareOnly bool `json:"shareOnly,omitempty"`
}

// PayoutEntry is one entry of a template's payout list.
type PayoutEntry struct {
	Address common.Address `json:"address"`
	Weight  uint64         `json:"weight"`
}

// Vector is one exactness vector (HW-01): the inputs and the expected outputs
// that every backend of the algorithm must reproduce byte for byte.
type Vector struct {
	Name       string
	HeaderHash common.Hash
	Height     uint64
	Nonce      uint64
	MixDigest  common.Hash // expected
	Result     common.Hash // expected, big-endian 256-bit result
}

// Algorithm is the proof-of-work behind the Igneum engine. Implementations are
// safe for concurrent use. Verification must be bounded in time and memory on
// an ordinary node without a GPU (the plan, page 8, affordable verification):
// the light path is mandatory, the full-dataset path optional.
type Algorithm interface {
	// Name is the registered name the chain configuration refers to.
	Name() string

	// Epoch returns the dataset epoch in force at height. An algorithm with no
	// dataset returns 0 for every height.
	Epoch(height uint64) uint64

	// SeedHash returns the seed of the epoch in force at height. An algorithm
	// with no dataset returns the zero hash.
	SeedHash(height uint64) common.Hash

	// Compute returns (mixDigest, result) for the triple. result is the 256-bit
	// value compared against the target. Every backend must agree with it.
	Compute(headerHash common.Hash, height uint64, nonce uint64) (mix common.Hash, result common.Hash)

	// Verify checks a sealed header's (nonce, mixDigest) for headerHash at
	// height against difficulty. fullDataset asks for the full-dataset path
	// when the node has one; implementations without one ignore it.
	Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error

	// Vectors returns the exactness vectors shipped with the algorithm. They
	// are shared with the C++ reference and the GPU backends (HW-01).
	Vectors() []Vector
}

// Keyed is the bond-keyed rounds' entry (the specification's section 0.9,
// version 8 bit 10; the hostile-key rule of DRAFT-HOSTILE-KEY.md, frozen in
// LAUNCH-GUARDS.md section 2; PROTOTYPE on the lab build, nothing adopted):
// at S keyed rounds the attempt consumes the bond's secret sk, and the block
// carries the S shares (32 bytes each, outside the seal hash). An algorithm
// without the bit reports zero rounds at every height. Implementations are
// the reference's ignh_hash_keyed: with sk the prover's hash (the shares
// written), without sk the verifier's replay from the shares given.
type Keyed interface {
	// KeyedRounds is S for the document in force at height; zero when the
	// document is not keyed.
	KeyedRounds(height uint64) uint64
	// ComputeKeyed returns (mix, result, shares): with sk the prover's hash
	// and the shares it wrote; with sk nil the replay from shares (whose
	// length must be S x 32 bytes), shares returned as given. A zero mix
	// and result mean the entry refused (no context, a bad length).
	ComputeKeyed(headerHash common.Hash, height uint64, nonce uint64, sk *[32]byte, shares []byte) (mix, result common.Hash, out []byte)
}

// KeyedM is the keyed entry that also yields the messages m_i of the proof pi
// (SPEC-R0A-v0.md 0.9.1: m_0 the work seed, m_i for i >= 1 the eight lane
// words at keyed round i's entry), S x 32 bytes in order, on the prover's
// call and on the verifier's replay alike (the reference's
// ignh_hash_keyed_m). Optional: an algorithm without it verifies no pi.
type KeyedM interface {
	Keyed
	// ComputeKeyedM is ComputeKeyed with the messages as a fourth result
	// (nil on a refusal).
	ComputeKeyedM(headerHash common.Hash, height uint64, nonce uint64, sk *[32]byte, shares []byte) (mix, result common.Hash, out []byte, m []byte)
}

// ShareBytes is one keyed round's share size.
const ShareBytes = 32

// KeyedBit is bit 10 of the version 8 feature set (the specification's
// section 0.9): the bond-keyed rounds.
const KeyedBit = 1024

// UnkeyedTwin derives the twin document of a keyed one: features without
// bit 10, no keyed_rounds; nil for an unkeyed document. The schedule's rows
// and the vector menu's coordinates share it (0.10.54: a keyed coordinate's
// twin is what the work package's open-seat template and the share lane
// are hashed with, which the released workers run).
func UnkeyedTwin(doc []byte) ([]byte, error) {
	// A coordinate without a document (the null algorithm's, the tests')
	// has no twin; the schedule's rows always carry one.
	if len(bytes.TrimSpace(doc)) == 0 {
		return nil, nil
	}
	// The numbers are kept as they are written (json.Number): the
	// derivation is consensus code, and a field past 2^53 would be altered
	// silently through a float (the security review's guard, 17 October 2026);
	// features alone is touched, every other field's value is the source's.
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var fields map[string]any
	if err := dec.Decode(&fields); err != nil {
		return nil, err
	}
	var f uint64
	if fn, ok := fields["features"].(json.Number); ok {
		v, err := strconv.ParseUint(fn.String(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("features %q: %w", fn.String(), err)
		}
		f = v
	}
	kr, hasKR := fields["keyed_rounds"]
	if !hasKR && f&KeyedBit == 0 {
		return nil, nil
	}
	if hasKR {
		krn, ok := kr.(json.Number)
		if !ok {
			return nil, nil
		}
		if v, err := strconv.ParseInt(krn.String(), 10, 64); err != nil || v <= 0 {
			return nil, nil
		}
	}
	delete(fields, "keyed_rounds")
	if f&KeyedBit != 0 {
		fields["features"] = json.Number(strconv.FormatUint(f&^KeyedBit, 10))
	}
	return json.Marshal(fields)
}

// KeyedRoundsAt reports S for an algorithm at a height: zero for one
// without the entry.
func KeyedRoundsAt(a Algorithm, height uint64) uint64 {
	if k, ok := a.(Keyed); ok {
		return k.KeyedRounds(height)
	}
	return 0
}

// Programmed is an algorithm whose relation draws a per-period programme
// from the chain (IgneumHash versions 9 and 10, R0D and R0E: the programme
// of period p = height / PERIOD_BLOCKS is drawn from a seed over the chain
// id, the period and an ANCHOR, the hash of a block at a registered depth
// behind the period; docs/igneumhash/schedule/DEVNET11-R0E-CHAIN-FORM.md).
// The engine resolves the anchor per header along the header's own branch
// (consensus/igneum/program.go) and runs every hash of that header on the
// view Under returns, so two branches with different anchors in one period
// verify under their own programmes and a sealer's template under its
// parent's.
type Programmed interface {
	Algorithm
	// PeriodBlocks is the document's PERIOD_BLOCKS at a height; zero where
	// the document in force draws no programme (a schedule's earlier
	// entries), and then no anchor is resolved.
	PeriodBlocks(height uint64) uint64
	// Under is the algorithm bound to one period anchor: its Compute,
	// Verify and the keyed entries (when the algorithm has them) draw the
	// period's programme from this anchor. The view is as safe for
	// concurrent use as the algorithm.
	Under(anchor common.Hash) Algorithm
}

// PeriodBlocksAt is the programme's period at a height, zero on an algorithm
// without one.
func PeriodBlocksAt(a Algorithm, height uint64) uint64 {
	if p, ok := a.(Programmed); ok {
		return p.PeriodBlocks(height)
	}
	return 0
}

// SolveKeyed is the CPU reference search under a bond's secret (tests and
// devnets only): every attempt is the prover's keyed hash and the found
// solution carries its shares.
func SolveKeyed(a Keyed, headerHash common.Hash, height uint64, difficulty *big.Int, sk *[32]byte, start uint64, abort <-chan struct{}) (nonce uint64, mix common.Hash, shares []byte, ok bool) {
	return SolveKeyedTick(a, headerHash, height, difficulty, sk, start, abort, nil)
}

// SolveTickEvery is the attempt count between two calls of a search's tick.
const SolveTickEvery = 64

// SolveKeyedTick is SolveKeyed with a tick every SolveTickEvery attempts
// (the count made since the last tick), so a node's meter reads the keyed
// search LIVE while it runs and not only at a find (the project's dMin rule of
// 04:00 UK, 12 October 2026: the sealer's live keyed-search rate as the
// node's meter reads it while sealing, never a benchmark). A nil tick is
// SolveKeyed.
func SolveKeyedTick(a Keyed, headerHash common.Hash, height uint64, difficulty *big.Int, sk *[32]byte, start uint64, abort <-chan struct{}, tick func(attempts int64)) (nonce uint64, mix common.Hash, shares []byte, ok bool) {
	target := Target(difficulty)
	if target == nil {
		return 0, common.Hash{}, nil, false
	}
	var since int64
	for nonce = start; ; nonce++ {
		select {
		case <-abort:
			if tick != nil && since > 0 {
				tick(since)
			}
			return 0, common.Hash{}, nil, false
		default:
		}
		m, r, s := a.ComputeKeyed(headerHash, height, nonce, sk, nil)
		since++
		if tick != nil && since >= SolveTickEvery {
			tick(since)
			since = 0
		}
		if m == (common.Hash{}) && r == (common.Hash{}) {
			return 0, common.Hash{}, nil, false
		}
		if new(big.Int).SetBytes(r[:]).Cmp(target) <= 0 {
			if tick != nil && since > 0 {
				tick(since)
			}
			return nonce, m, s, true
		}
	}
}

// Verify is the shared verification body: recompute, compare the mix digest,
// compare the result to the target. Algorithms call it from their Verify.
func Verify(a Algorithm, headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int) error {
	// The target into a pooled big.Int and the result compared as 32 bytes
	// (big-endian, the target filled into a stack array), so the comparison
	// allocates nothing (the project's order of 20:4x UK, 12 October 2026: the
	// minimum node's allocations per verification to none).
	if difficulty == nil || difficulty.Sign() <= 0 || difficulty.BitLen() > 256 {
		return ErrInvalidDifficulty
	}
	target := targetPool.Get().(*big.Int)
	defer targetPool.Put(target)
	target.Div(maxTarget, difficulty)
	mix, result := a.Compute(headerHash, height, nonce)
	if refused(mix, result) {
		return ErrRefused
	}
	if mix != mixDigest {
		return ErrInvalidMixDigest
	}
	var tb [32]byte
	target.FillBytes(tb[:])
	if bytes.Compare(result[:], tb[:]) > 0 {
		return ErrResultAboveTarget
	}
	return nil
}

// targetPool holds the big.Ints Verify computes a target into.
var targetPool = sync.Pool{New: func() any { return new(big.Int) }}

// ErrRefused: the algorithm made no hash for the triple (a document it
// cannot run, a context it cannot build). Never a valid seal.
var ErrRefused = errors.New("igneum/pow: the algorithm refused the triple (no hash)")

// refused reports an algorithm's refusal: a zero mix and a zero result at
// once (the entries return that pair on every refusal; a real hash pair
// of two zero words has probability 2^-512 and is refused with it, stated).
// Without this a refusal read as a result of zero, under every target, and
// the fifth development chain sealed 185 blocks with zero proofs that every
// node accepted (11 October 2026).
func refused(mix, result common.Hash) bool {
	return mix == (common.Hash{}) && result == (common.Hash{})
}

// VerifyTarget is Verify against a target given directly (the reference ABI's
// boundary): recompute, compare the mix digest, compare the result to the
// target. The node's path is Verify, which derives the target from the
// difficulty; this form serves pool shares and the vector classes that name a
// boundary.
func VerifyTarget(a Algorithm, headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, target *big.Int) error {
	if target == nil || target.Sign() < 0 || target.BitLen() > 256 {
		return ErrInvalidDifficulty
	}
	mix, result := a.Compute(headerHash, height, nonce)
	if refused(mix, result) {
		return ErrRefused
	}
	if mix != mixDigest {
		return ErrInvalidMixDigest
	}
	if new(big.Int).SetBytes(result[:]).Cmp(target) > 0 {
		return ErrResultAboveTarget
	}
	return nil
}

// Solve is the CPU reference search, for tests and devnets only, never a
// product miner. It tries nonces from start upward until one satisfies the
// difficulty or abort closes. ok is false on abort.
func Solve(a Algorithm, headerHash common.Hash, height uint64, difficulty *big.Int, start uint64, abort <-chan struct{}) (nonce uint64, mix common.Hash, ok bool) {
	return SolveTick(a, headerHash, height, difficulty, start, abort, nil)
}

// SolveTick is Solve with a tick every SolveTickEvery attempts (the count
// since the last tick), for the node's live meter; a nil tick is Solve.
func SolveTick(a Algorithm, headerHash common.Hash, height uint64, difficulty *big.Int, start uint64, abort <-chan struct{}, tick func(attempts int64)) (nonce uint64, mix common.Hash, ok bool) {
	target := Target(difficulty)
	if target == nil {
		return 0, common.Hash{}, false
	}
	var since int64
	for nonce = start; ; nonce++ {
		select {
		case <-abort:
			if tick != nil && since > 0 {
				tick(since)
			}
			return 0, common.Hash{}, false
		default:
		}
		m, r := a.Compute(headerHash, height, nonce)
		since++
		if tick != nil && since >= SolveTickEvery {
			tick(since)
			since = 0
		}
		if refused(m, r) {
			return 0, common.Hash{}, false
		}
		if new(big.Int).SetBytes(r[:]).Cmp(target) <= 0 {
			if tick != nil && since > 0 {
				tick(since)
			}
			return nonce, m, true
		}
	}
}

// Constructor builds an algorithm from its configuration parameters. params is
// the raw JSON of the chain configuration's algorithm parameters (nil when
// absent); each algorithm documents what it accepts.
type Constructor func(params []byte) (Algorithm, error)

type entry struct {
	c       Constructor
	devOnly bool
}

var (
	registryMu sync.RWMutex
	registry   = map[string]entry{}
)

// Register makes an algorithm available under name. It panics on a duplicate
// name: two implementations claiming one name is a build error, not a runtime
// condition.
func Register(name string, c Constructor) { register(name, c, false) }

// RegisterDevOnly makes an algorithm available under name for development
// chains only: New refuses it, NewDev builds it. A launch or test network
// genesis naming a dev-only algorithm stops the node (F-02).
func RegisterDevOnly(name string, c Constructor) { register(name, c, true) }

func register(name string, c Constructor, devOnly bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if name == "" || c == nil {
		panic("igneum/pow: Register with empty name or nil constructor")
	}
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("igneum/pow: algorithm %q registered twice", name))
	}
	registry[name] = entry{c: c, devOnly: devOnly}
}

// IsDevOnly reports whether name is registered as development-only.
func IsDevOnly(name string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[name].devOnly
}

// New builds the algorithm registered under name. A node refuses to start on
// an unknown name (the error wraps ErrUnknownAlgorithm) and on a
// development-only name (ErrDevOnlyAlgorithm).
func New(name string, params []byte) (Algorithm, error) { return build(name, params, false) }

// NewDev is New for a development chain: it also builds development-only
// algorithms. Only the --dev path calls it.
func NewDev(name string, params []byte) (Algorithm, error) { return build(name, params, true) }

func build(name string, params []byte, dev bool) (Algorithm, error) {
	registryMu.RLock()
	e, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q (registered: %v)", ErrUnknownAlgorithm, name, Names())
	}
	if e.devOnly && !dev {
		return nil, fmt.Errorf("%w: %q", ErrDevOnlyAlgorithm, name)
	}
	a, err := e.c(params)
	if err != nil {
		return nil, fmt.Errorf("igneum/pow: constructing %q: %w", name, err)
	}
	if a.Name() != name {
		return nil, fmt.Errorf("igneum/pow: algorithm registered as %q reports name %q", name, a.Name())
	}
	return a, nil
}

// Names lists the registered algorithm names, sorted.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Ticketed is a witness relation (VectorWork's Graph coordinate,
// docs/igneumhash/vectorwork/GRAPH-REFERENCE.md): the seal is a ticket found
// under (headerHash, height, nonce) rather than a hash of them, carried in
// the header outside the seal hash and checked by the exported verifier.
// The mix digest is the ticket hash, and the ticket meets the target when
// its hash is at or under it. Compute and Verify on the base interface are
// the ABI's hash form (the reference solver over every ticket of the
// attempt, for the vectors); the node seals and verifies through these.
type Ticketed interface {
	Algorithm
	// TicketSize is the witness's canonical length in bytes.
	TicketSize() int
	// VerifyTicket checks the ticket under the triple and returns its
	// ticket hash; an error names a malformed or false ticket.
	VerifyTicket(headerHash common.Hash, height uint64, nonce uint64, ticket []byte) (common.Hash, error)
	// SolveTicket finds every ticket of the attempt's graph (none is the
	// common answer), each in canonical bytes.
	SolveTicket(headerHash common.Hash, height uint64, nonce uint64) ([][]byte, error)
}

// VerifyTicketed is the shared body for a ticketed seal: the ticket's size,
// the verifier's check, the mix digest as the ticket hash, the hash against
// the target.
func VerifyTicketed(t Ticketed, headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, ticket []byte, difficulty *big.Int) error {
	target := Target(difficulty)
	if target == nil {
		return ErrInvalidDifficulty
	}
	if len(ticket) != t.TicketSize() {
		return fmt.Errorf("%s: ticket of %d bytes (the relation's is %d)", t.Name(), len(ticket), t.TicketSize())
	}
	th, err := t.VerifyTicket(headerHash, height, nonce, ticket)
	if err != nil {
		return err
	}
	if th != mixDigest {
		return ErrInvalidMixDigest
	}
	if new(big.Int).SetBytes(th[:]).Cmp(target) > 0 {
		return ErrResultAboveTarget
	}
	return nil
}

// SolveTicketTick is the ticketed search: one graph per nonce from start,
// every ticket of the graph against the target, the first under it wins
// (the smallest when several), the tick every SolveTickEvery graphs. The
// returned mix is the winning ticket's hash.
func SolveTicketTick(t Ticketed, headerHash common.Hash, height uint64, difficulty *big.Int, start uint64, abort <-chan struct{}, tick func(attempts int64)) (nonce uint64, mix common.Hash, ticket []byte, ok bool) {
	target := Target(difficulty)
	if target == nil {
		return 0, common.Hash{}, nil, false
	}
	var since int64
	for nonce = start; ; nonce++ {
		select {
		case <-abort:
			if tick != nil && since > 0 {
				tick(since)
			}
			return 0, common.Hash{}, nil, false
		default:
		}
		tickets, err := t.SolveTicket(headerHash, height, nonce)
		since++
		if tick != nil && since >= SolveTickEvery {
			tick(since)
			since = 0
		}
		if err != nil {
			return 0, common.Hash{}, nil, false
		}
		for _, tk := range tickets {
			th, err := t.VerifyTicket(headerHash, height, nonce, tk)
			if err != nil {
				continue
			}
			if new(big.Int).SetBytes(th[:]).Cmp(target) <= 0 {
				if tick != nil && since > 0 {
					tick(since)
				}
				return nonce, th, tk, true
			}
		}
	}
}

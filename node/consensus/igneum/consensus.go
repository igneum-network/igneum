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

package igneum

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/difficulty"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	igneumshares "github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"
)

var (
	errOlderBlockTime      = errors.New("timestamp older than parent")
	errUnclesNotAllowed    = errors.New("uncles are not allowed")
	errInvalidDifficulty   = errors.New("non-positive difficulty")
	errInvalidPoW          = errors.New("invalid proof-of-work")
	errUnsupportedWithdraw = errors.New("withdrawals are not supported")
	errUnknownGrandparent  = errors.New("grandparent header unknown")
)

// Author implements consensus.Engine: the coinbase is the author.
func (e *Engine) Author(header *types.Header) (common.Address, error) {
	return header.Coinbase, nil
}

// VerifyHeader implements consensus.Engine.
func (e *Engine) VerifyHeader(chain consensus.ChainHeaderReader, header *types.Header, seal bool) error {
	if e.config.FakeFull {
		return nil
	}
	e.anchor(chain)
	e.sortitionAnchor(chain)
	number := header.Number.Uint64()
	if chain.GetHeader(header.Hash(), number) != nil {
		return nil
	}
	parent := chain.GetHeader(header.ParentHash, number-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	grandparent, err := e.grandparent(chain, parent)
	if err != nil {
		return err
	}
	return e.verifyHeader(chain, header, parent, grandparent, seal, time.Now().Unix())
}

// anchor tells an algorithm with epoch state where the chain head is, so
// that its admitted window and its caches follow the chain and never a
// peer's number (the specification's section 11; security's F-18). The head
// is read from the engine's own chain; a fake engine has no algorithm.
func (e *Engine) anchor(chain consensus.ChainHeaderReader) {
	e.setChain(chain)
	if chain != nil {
		// The settlement rule's messages name the chain id (endorse.go).
		if cfg := chain.Config(); cfg != nil && cfg.GetChainID() != nil {
			e.endorse.setChainID(cfg.GetChainID().Uint64())
		}
	}
	an, ok := e.algo.(interface{ Anchor(height uint64) })
	if !ok || chain == nil {
		return
	}
	if head := chain.CurrentHeader(); head != nil && head.Number != nil && head.Number.IsUint64() {
		an.Anchor(head.Number.Uint64())
	}
}

// grandparent returns the parent's parent from the chain, or nil when the
// parent is genesis. A missing grandparent for a known parent is an error.
func (e *Engine) grandparent(chain consensus.ChainHeaderReader, parent *types.Header) (*types.Header, error) {
	if parent.Number.Sign() == 0 {
		return nil, nil
	}
	gp := chain.GetHeader(parent.ParentHash, parent.Number.Uint64()-1)
	if gp == nil {
		return nil, errUnknownGrandparent
	}
	return gp, nil
}

// VerifyHeaders implements consensus.Engine, verifying a batch concurrently and
// returning the results in order. Parents and grandparents come from the
// batch when they are in it, from the chain otherwise.
// batchReader overlays a batch of headers under verification on the chain
// reader: a header's parents inside the batch are not in the chain yet
// (a forked node's downloader verifies a batch before it writes it), and
// the programme's anchor walk and the vector state read them here
// (vectorwork3's graph node, 13 October 2026, 04:18Z: "the period's anchor
// is not in this node's header chain" at the first parent inside the
// batch, the node stuck at its fork for an hour).
type batchReader struct {
	consensus.ChainHeaderReader
	byHash map[common.Hash]*types.Header
}

func newBatchReader(chain consensus.ChainHeaderReader, headers []*types.Header) *batchReader {
	m := make(map[common.Hash]*types.Header, len(headers))
	for _, h := range headers {
		m[h.Hash()] = h
	}
	return &batchReader{ChainHeaderReader: chain, byHash: m}
}

func (b *batchReader) GetHeader(hash common.Hash, number uint64) *types.Header {
	if h, ok := b.byHash[hash]; ok && h.Number.Uint64() == number {
		return h
	}
	return b.ChainHeaderReader.GetHeader(hash, number)
}

func (b *batchReader) GetHeaderByHash(hash common.Hash) *types.Header {
	if h, ok := b.byHash[hash]; ok {
		return h
	}
	return b.ChainHeaderReader.GetHeaderByHash(hash)
}

func (e *Engine) VerifyHeaders(chain consensus.ChainHeaderReader, headers []*types.Header, seals []bool) (chan<- struct{}, <-chan error) {
	if e.config.FakeFull || len(headers) == 0 {
		abort, results := make(chan struct{}), make(chan error, len(headers))
		for i := 0; i < len(headers); i++ {
			results <- nil
		}
		return abort, results
	}
	e.anchor(chain)
	reader := consensus.ChainHeaderReader(newBatchReader(chain, headers))
	workers := VerifyWorkers()
	if verifierPinned(e.config.VerifierCore) {
		// One worker on the reserved core: a pool pinned to one CPU is
		// one CPU's worth of verification whatever its size (F-97).
		workers = 1
	}
	if len(headers) < workers {
		workers = len(headers)
	}
	var (
		inputs = make(chan int)
		done   = make(chan int, workers)
		errs   = make([]error, len(headers))
		abort  = make(chan struct{})
		now    = time.Now().Unix()
	)
	for i := 0; i < workers; i++ {
		go func() {
			// The reserved verifier thread (verifycore_linux.go).
			defer reserveVerifierThread(e.config.VerifierCore)()
			for index := range inputs {
				errs[index] = e.verifyHeaderWorker(reader, headers, seals, index, now)
				done <- index
			}
		}()
	}
	errorsOut := make(chan error, len(headers))
	go func() {
		defer close(inputs)
		var (
			in, out = 0, 0
			checked = make([]bool, len(headers))
			inputs  = inputs
		)
		for {
			select {
			case inputs <- in:
				if in++; in == len(headers) {
					inputs = nil
				}
			case index := <-done:
				for checked[index] = true; checked[out]; out++ {
					errorsOut <- errs[out]
					if out == len(headers)-1 {
						return
					}
				}
			case <-abort:
				return
			}
		}
	}()
	return abort, errorsOut
}

func (e *Engine) verifyHeaderWorker(chain consensus.ChainHeaderReader, headers []*types.Header, seals []bool, index int, unixNow int64) error {
	var parent, grandparent *types.Header
	if index == 0 {
		parent = chain.GetHeader(headers[0].ParentHash, headers[0].Number.Uint64()-1)
	} else if headers[index-1].Hash() == headers[index].ParentHash {
		parent = headers[index-1]
	}
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	if parent.Number.Sign() > 0 {
		if index >= 2 && headers[index-2].Hash() == parent.ParentHash {
			grandparent = headers[index-2]
		} else {
			grandparent = chain.GetHeader(parent.ParentHash, parent.Number.Uint64()-1)
		}
		if grandparent == nil {
			return errUnknownGrandparent
		}
	}
	return e.verifyHeader(chain, headers[index], parent, grandparent, seals[index], unixNow)
}

// VerifyUncles implements consensus.Engine: Igneum has no uncles (D-A4 as
// revised with protocol engineer B and the security review: an empty uncle list
// is a consensus rule, so the uncle-reward question does not arise).
func (e *Engine) VerifyUncles(chain consensus.ChainReader, block *types.Block) error {
	if len(block.Uncles()) > 0 && !e.config.FakeFull {
		return errUnclesNotAllowed
	}
	return nil
}

// verifyHeader is the header validity rule set of DESIGN.md 4.2.
func (e *Engine) verifyHeader(chain consensus.ChainHeaderReader, header, parent, grandparent *types.Header, seal bool, unixNow int64) error {
	if uint64(len(header.Extra)) > vars.MaximumExtraDataSize {
		return fmt.Errorf("extra-data too long: %d > %d", len(header.Extra), vars.MaximumExtraDataSize)
	}
	// The declared time's upper bound: a bonded block under the clock
	// amendment by the chain constant sealDriftMs, every other block by
	// the node's MaxFutureDrift (clock.go verifyDeclaredTime).
	if err := e.verifyDeclaredTime(header, unixNow); err != nil {
		return err
	}
	if header.Time <= parent.Time {
		return errOlderBlockTime
	}
	if header.UncleHash != types.EmptyUncleHash {
		return errUnclesNotAllowed
	}
	if e.config.FixtureDifficulty {
		// The fixture suites' blocks carry another rule's difficulty (zero
		// after their merge); the harness vouches for it (PA-09b).
		if header.Difficulty == nil || header.Difficulty.Sign() < 0 {
			return errInvalidDifficulty
		}
	} else {
		if header.Difficulty == nil || header.Difficulty.Sign() <= 0 {
			return errInvalidDifficulty
		}
		if e.vectorMenu() != nil {
			// The development hold (devhold.go, the devhold build tag
			// alone): a held coordinate's block is refused as a future
			// block, the peer kept, until the release.
			if e.devHoldRefuses(header) {
				return consensus.ErrFutureBlock
			}
			// VectorWork (vector.go): the tag in range and the difficulty
			// the coordinate's controller output at the parent, read from
			// the coordinate's own blocks alone.
			if err := e.verifyVectorHeader(chain, header, parent); err != nil {
				return err
			}
		} else if len(header.Coord) > 0 {
			return errVectorCoordOff
		} else if e.ClockOn(header.Number.Uint64()) {
			// The round clock amendment (clock.go): a block carries one of
			// two difficulties by its form, which the stateful stage reads
			// from the parent's state; the stateless stage holds the
			// positivity above and nothing else.
		} else {
			expected := e.calcDifficulty(parent, grandparent)
			if expected.Cmp(header.Difficulty) != 0 {
				return fmt.Errorf("invalid difficulty: have %v, want %v", header.Difficulty, expected)
			}
		}
	}
	// The block DAG ordering (dagform.go): off the gate a header carrying
	// merged parents is refused; under it the parents, the selected parent
	// and the merge depth are checked and the block coloured.
	if err := e.verifyDAGHeader(chain, header); err != nil {
		return err
	}
	if header.GasLimit > vars.MaxGasLimit {
		return fmt.Errorf("invalid gasLimit: have %v, max %v", header.GasLimit, vars.MaxGasLimit)
	}
	if header.GasUsed > header.GasLimit {
		return fmt.Errorf("invalid gasUsed: have %d, gasLimit %d", header.GasUsed, header.GasLimit)
	}
	if diff := new(big.Int).Sub(header.Number, parent.Number); diff.Cmp(big.NewInt(1)) != 0 {
		return consensus.ErrInvalidNumber
	}
	// Gas limit movement and the base fee (EIP-1559 is active from block 0 on
	// Igneum; the branch keeps the engine correct for test configurations
	// without it). The constants and the floor are PB-03's, through the
	// configurator.
	if !chain.Config().IsEnabled(chain.Config().GetEIP1559Transition, header.Number) {
		if header.BaseFee != nil {
			return fmt.Errorf("invalid baseFee before fork: have %d, expected 'nil'", header.BaseFee)
		}
		if err := misc.VerifyGaslimit(parent.GasLimit, header.GasLimit); err != nil {
			return err
		}
	} else if err := eip1559.VerifyEIP1559Header(chain.Config(), parent, header); err != nil {
		return err
	}
	// Igneum does not support withdrawals (EIP-4895); the field is nil
	// unconditionally (DESIGN.md 6.3). The blob and beacon-root fields no
	// longer exist on the header (PA-12).
	// The payout list of the sortition prototype rides the withdrawals
	// list; without the rule the field is nil unconditionally.
	if header.WithdrawalsHash != nil {
		if !e.AllowsPayoutList(header) {
			return errUnsupportedWithdraw
		}
		if header.Coinbase != PayoutCoinbase(*header.WithdrawalsHash) {
			return fmt.Errorf("%w: coinbase is not the list root's prefix", errPayoutList)
		}
	}
	// The miner-bound work authority prototype's stateless part: the bond
	// key's presence by height (authority.go; the stateful part runs at the
	// parent's state in core.StateProcessor).
	if a := e.config.Parts.Authority; a != nil {
		if err := a.verifyAuthorityHeader(header); err != nil {
			return err
		}
	} else if header.BondKey != nil {
		return errBondKeyEarly
	}
	// The sortition rule's stateless part (sortition.go).
	if s := e.config.Parts.Sortition; s != nil {
		if err := s.verifySortitionHeader(header, parent); err != nil {
			return err
		}
	} else if header.Round != 0 || len(header.Sig) != 0 {
		return errSortitionEarly
	}
	// The proof pi's header part under --igneum.pi-verify (pi.go): a keyed
	// seal commits to a proof, an unkeyed one does not.
	if err := e.verifyPiHeader(header); err != nil {
		return err
	}
	if seal {
		// The authority rule's stateful part BEFORE the walk (F-37, security's
		// C10 order): the signature over the signing hash, the key's bond and
		// its eligibility for the height and round cost an Ed25519 verify and
		// a table read, where the walk costs milliseconds; a forged-signature
		// flood is refused here at its own code and never reaches the seal.
		// The parent's state is read when the chain gives it (an announced
		// block on the head; the dry run); during a batch sync the parent's
		// state does not exist yet and the walk runs first, the state
		// processor refusing the block at the same rule a moment later.
		if err := e.verifyAuthorityBeforeSeal(chain, header, parent); err != nil {
			return err
		}
		// The height fed to the proof of work is the validated chain context's
		// (the parent's number plus one), never the block's own claim: a
		// self-declared height fed to the hash is the gap the Ravencoin August
		// 2026 exploit went through (the plan, page 8).
		if err := e.verifySeal(chain, header, parent.Number.Uint64()+1, false); err != nil {
			return err
		}
	}
	return nil
}

// verifyAuthorityBeforeSeal runs the authority rule at the parent's state
// when the chain reader can give it (core.BlockChain does; a header chain
// cannot), so that a block's signature and eligibility are checked before
// its proof of work (F-37). A chain without state access, or a parent whose
// state is not retained, is no refusal: the rule runs again in the state
// processor on every imported block, this call only orders the cost.
func (e *Engine) verifyAuthorityBeforeSeal(chain consensus.ChainHeaderReader, header, parent *types.Header) error {
	a := e.config.Parts.Authority
	if a == nil || header.Number == nil || !a.Active(header.Number.Uint64()) {
		return nil
	}
	sr, ok := chain.(stateReader)
	if !ok {
		return nil
	}
	// The memo first (authoritymemo.go, F-39): when the parent's facts are
	// recorded, the rule runs without opening the parent's state; a fact
	// the memo does not hold (errMemoMiss) opens the state and runs again.
	if e.factsFor(parent.Hash()) != nil {
		err := e.verifyAuthorityAt(header, nil, true)
		if !errors.Is(err, errMemoMiss) {
			return err
		}
	}
	st, err := sr.StateAt(parent.Root)
	if err != nil || st == nil {
		return nil
	}
	return e.verifyAuthorityAt(header, st, true)
}

// verifySeal checks the proof of work through the algorithm interface. The
// hash function is never named here (PHASE-ZERO.md). height is the chain
// context's height for this header, supplied by the caller from validated
// state (the parent, or the node's own pending block); the header's Number
// must agree with it or the seal is invalid.
func (e *Engine) verifySeal(chain consensus.ChainHeaderReader, header *types.Header, height uint64, fullDataset bool) error {
	if e.config.FakePoW {
		time.Sleep(e.config.FakeDelay)
		if e.config.FakeFail != 0 && e.config.FakeFail == header.Number.Uint64() {
			return errInvalidPoW
		}
		return nil
	}
	if header.Difficulty == nil || header.Difficulty.Sign() <= 0 {
		return errInvalidDifficulty
	}
	if header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != height {
		return fmt.Errorf("%w: header declares height %v, chain context says %d", errInvalidPoW, header.Number, height)
	}
	docS := e.docKeyedRounds(header)
	if len(header.Shares) != 0 {
		if docS == 0 {
			return fmt.Errorf("%w: shares on an unkeyed document", errInvalidPoW)
		}
		return e.verifyKeyedSeal(chain, header, height, docS)
	}
	if docS > 0 {
		// A keyed document and no shares: refused here unless the genesis
		// form is on, where the open seat and the liveness round seal
		// unkeyed and the stateful rule decides which form the block owes.
		if s := e.config.Parts.Sortition; s == nil || !s.OpenSeat {
			return fmt.Errorf("%w: %v", errInvalidPoW, errKeyedShares)
		}
	}
	// The hash runs under the programme of the header's period on its own
	// branch (program.go); an algorithm without one is itself. The
	// verifier's global budget defers the hash while the bucket is in
	// debt and charges its time after (verifybudget.go).
	algo, err := e.algoForOn(chain, header)
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidPoW, err)
	}
	e.budget.waitHeader()
	start := time.Now()
	if t, ok := algo.(pow.Ticketed); ok {
		// The Graph coordinate (pow.Ticketed): the seal is the header's
		// ticket, checked by the exported verifier under the seal hash,
		// the nonce and the height; the mix is its hash.
		err = pow.VerifyTicketed(t, e.SealHash(header), height, header.Nonce.Uint64(), header.MixDigest, header.Ticket, e.sealDifficulty(header))
	} else if len(header.Ticket) > 0 {
		err = errTicketOnHash
	} else {
		err = algo.Verify(e.SealHash(header), height, header.Nonce.Uint64(), header.MixDigest, e.sealDifficulty(header), fullDataset)
	}
	e.budget.charge(time.Since(start))
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidPoW, err)
	}
	return nil
}

// errTicketOnHash refuses a ticket on a block of a hash coordinate.
var errTicketOnHash = errors.New("igneum: a ticket on a hash relation's block")

// Errors of the keyed form (the hostile-key rule's work binding; PROTOTYPE).
var (
	errKeyedShares      = errors.New("igneum keyed: shares missing or not S x 32 bytes")
	errKeyedReplay      = errors.New("igneum keyed: the replay from the shares refused")
	errKeyedOwnerShares = errors.New("igneum keyed: shares are not this node's secret's (the owner's check)")
)

// verifyKeyedSeal is the keyed document's proof check (the specification's
// section 0.9): the block carries S shares outside the seal hash; the
// verifier replays the walk with them injected at the keyed rounds and
// checks the mix digest and the result against the target. That binds the
// digest to the shares. The binding of the shares to the bond key is the
// proof pi of 0.9 ("s_i = H(sk, m_i) with pk = G(sk)"), whose circuit no
// reference defines yet: its slot is OPEN here, stated. What this node CAN
// check in full is its own key: when the header's bond key is the one this
// node holds the secret of, the shares are recomputed under the secret and
// must match (a block under our key with shares we did not write is
// refused: a renter without the secret produces no correct shares).
func (e *Engine) verifyKeyedSeal(chain consensus.ChainHeaderReader, header *types.Header, height uint64, s uint64) error {
	// The replay runs under the programme of the header's period on its
	// own branch (program.go).
	algo, err := e.algoForOn(chain, header)
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidPoW, err)
	}
	k, ok := algo.(pow.Keyed)
	if !ok {
		return fmt.Errorf("%w: keyed document on an algorithm without the keyed entry", errInvalidPoW)
	}
	if uint64(len(header.Shares)) != s*pow.ShareBytes {
		return fmt.Errorf("%w: %d bytes for S = %d", errKeyedShares, len(header.Shares), s)
	}
	target := pow.Target(e.sealDifficulty(header))
	if target == nil {
		return fmt.Errorf("%w: %v", errInvalidPoW, pow.ErrInvalidDifficulty)
	}
	sealHash := e.SealHash(header)
	nonce := header.Nonce.Uint64()
	var mix, result common.Hash
	if km, ok := algo.(pow.KeyedM); ok && e.piVerifyAt(height) {
		// The replay with the proof's messages kept for the body stage
		// (pi.go): one walk serves the seal and the proof.
		var m []byte
		mix, result, _, m = km.ComputeKeyedM(sealHash, height, nonce, nil, header.Shares)
		if mix == (common.Hash{}) && result == (common.Hash{}) {
			return errKeyedReplay
		}
		e.keepPiMessages(header, m)
	} else {
		mix, result, _ = k.ComputeKeyed(sealHash, height, nonce, nil, header.Shares)
		if mix == (common.Hash{}) && result == (common.Hash{}) {
			return errKeyedReplay
		}
	}
	if mix != header.MixDigest {
		return fmt.Errorf("%w: %v", errInvalidPoW, pow.ErrInvalidMixDigest)
	}
	if new(big.Int).SetBytes(result[:]).Cmp(target) > 0 {
		return fmt.Errorf("%w: %v", errInvalidPoW, pow.ErrResultAboveTarget)
	}
	// The owner's check.
	if sk, key := e.bondSecret(); sk != nil && header.BondKey != nil && key != nil && *header.BondKey == *key {
		_, _, own := k.ComputeKeyed(sealHash, height, nonce, sk, nil)
		if !bytes.Equal(own, header.Shares) {
			return errKeyedOwnerShares
		}
	}
	return nil
}

// calcDifficulty is B's rule (PB-01): the next difficulty from the parent's
// interval, which needs the grandparent's time (nil grandparent means the
// parent is genesis and the rule uses the target interval).
func (e *Engine) calcDifficulty(parent, grandparent *types.Header) *big.Int {
	var gpTime uint64
	if grandparent != nil {
		gpTime = grandparent.Time
	}
	return e.chainParamsAt(parent.Number.Uint64()+1).Next(parent.Number.Uint64(), parent.Time, gpTime, parent.Difficulty)
}

// chainParamsAt is the chain difficulty's controller at a height: the
// genesis set's parameters, with the floor raised to DiffMinV1Min from the
// DiffMinV1 height (ctypes.IgneumRuleForks; the security review's F-86). The floor
// is the controller's clamp, so the first block at or past the landing check reads
// the new minimum at once and the controller binds above it.
func (e *Engine) chainParamsAt(height uint64) difficulty.Params {
	p := e.config.Parts.Difficulty
	e.lock.Lock()
	f := e.forks
	e.lock.Unlock()
	if f != nil && f.DiffMinV1 != 0 && height >= f.DiffMinV1 && f.DiffMinV1Min > 0 {
		p.Min = new(big.Int).SetUint64(f.DiffMinV1Min)
	}
	return p
}

// CalcDifficulty implements consensus.Engine. The new block's time plays no
// part in the rule (it is the parent's interval that counts). It returns nil
// when the grandparent of a non-genesis parent is unknown to the chain.
func (e *Engine) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent *types.Header) *big.Int {
	grandparent, err := e.grandparent(chain, parent)
	if err != nil {
		return nil
	}
	return e.calcDifficulty(parent, grandparent)
}

// Prepare implements consensus.Engine: sets the difficulty.
func (e *Engine) Prepare(chain consensus.ChainHeaderReader, header *types.Header) error {
	e.anchor(chain)
	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	grandparent, err := e.grandparent(chain, parent)
	if err != nil {
		return err
	}
	header.Difficulty = e.calcDifficulty(parent, grandparent)
	// VectorWork (vector.go): the template's coordinate is the node's and
	// its difficulty that coordinate's controller output at the parent.
	if err := e.prepareVector(chain, header, parent); err != nil {
		return err
	}
	header.UncleHash = types.EmptyUncleHash
	if err := e.prepareAuthority(header); err != nil {
		return err
	}
	if err := e.prepareSortition(chain, header, parent); err != nil {
		return err
	}
	// The round clock amendment (clock.go): the template's difficulty by
	// its form, now that the key and the round are fixed. Under the vector
	// form the difficulty is the coordinate's controller output and the
	// verifier reads it before the clock (verifyHeader's order), so the
	// clock's write is skipped here as it is there (vectorwork1's second
	// run, 13 October 2026, 01:19 UTC: "have 36139, want 73120", the
	// clock's D_seal over the controller's output).
	if e.vectorMenu() != nil {
		return nil
	}
	return e.prepareClock(chain, header, parent)
}

// Finalize implements consensus.Engine: credits the schedule's two amounts
// (PB-02) to the coinbase and the fund address, and nothing else.
func (e *Engine) Finalize(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, uncles []*types.Header, withdrawals []*types.Withdrawal) {
	if e.config.FixtureReward != nil {
		if r := e.config.FixtureReward(header); r != nil && r.Sign() > 0 {
			creditReward(state, header.Coinbase, uint256.MustFromBig(r))
		}
		return
	}
	miner, fund := e.config.Parts.Issuance.Reward(header.Number.Uint64())
	// The block DAG ordering (dagform.go finalizeDAG): the subsidy goes to
	// the block's mergeset blues at their orders, and the block's own
	// subsidy is its merging block's to pay; nothing of the chain form's
	// pay below runs for the block itself.
	if e.DAGOn(header.Number.Uint64()) {
		if err := e.finalizeDAG(chain, header, state); err != nil {
			e.log.Error("DAG subsidy not paid (consensus fault)", "number", header.Number, "err", err)
		}
		miner, fund = new(big.Int), new(big.Int)
	}
	// The Pay rule's coordinate dimension (revision 13, section 9): the
	// block's sealer is paid its coordinate's accrued share in place of
	// the block's miner subsidy.
	coordPay := false
	if pot, ok := e.coordinatePay(chain, header); ok {
		miner, coordPay = pot, true
	}
	// The sponsored lanes' paid set (sponsored.go, F-677): the balances of
	// the block's payees before the chain's pay, read again after it.
	spSnap := e.sponsoredPaySnapshot(chain, header, state, withdrawals)
	// The global withdrawal index's count (withdrawalindex.go), advanced
	// by every list the block carries, assembly and import alike.
	e.advanceWithdrawalCount(state, header.Number.Uint64(), len(withdrawals))
	// The sealer share withheld for a shielded coinbase note (nil or zero:
	// none; pool_settle.go).
	var mint *big.Int
	if len(withdrawals) > 0 && e.AllowsPayoutList(header) {
		// The sortition prototype's payout list: the subsidy pro rata to
		// the list the bond's signer signed (sortition.go), and with it the
		// block's fees. The fee rule, stated once and the same in assembly
		// and on import (F-35): the transactions' tips are credited by the
		// EVM to the header's coinbase, which under a list is the list
		// root's prefix (an address nobody holds); Finalize moves that
		// address's whole balance into the pro-rata payment, so the list's
		// addresses take the coinbase's share, subsidy and fees alike.
		fees := state.GetBalance(header.Coinbase)
		if !fees.IsZero() {
			state.SetBalance(header.Coinbase, new(uint256.Int))
		}
		amount := new(big.Int).Add(miner, fees.ToBig())
		if amount.Sign() > 0 {
			// Under the share lane (mineshares.go) the list is the derivation
			// over the carried shares and the state's credits, settled through
			// the node team's SettleShares (the dust and the overflow into the
			// carry); without the lane, or without a stash for the block, the
			// plain pro rata.
			if !e.SharesOn(header.Number.Uint64()) || !e.settleMinedShares(header, state, amount, withdrawals) {
				if e.payV2On(header.Number.Uint64()) {
					// The second form without a stash (an imported block):
					// the same split from the list alone, no carry.
					if _, _, err := e.SettleSharesV2(state, header.Number.Uint64(), amount, igneumshares.FromWithdrawals(withdrawals), nil); err != nil {
						e.log.Error("Share settle (second form) failed (consensus fault)", "number", header.Number, "err", err)
					}
				} else {
					payProRata(state, amount, withdrawals)
				}
			}
		}
	} else if miner.Sign() > 0 {
		// The sealer share of the plain form (no list) goes through
		// paySealer (pool_settle.go): the public credit as before, or,
		// when the sealer registered a shielded payout address under the
		// pool's gate (zero everywhere of record), withheld here and
		// minted as a coinbase note by settlePool after the block's
		// records (Project Money stage two, cut two).
		if coordPay {
			// The coordinate's accrued share, whole, to the sealer.
			mint = e.paySealer(chain, header, state, miner)
		} else if e.SharesOn(header.Number.Uint64()) && e.payV2On(header.Number.Uint64()) {
			// The plain form under the Pay rule's second form (PayV2,
			// F-63's fix (b)): no list, so the sealer takes its quarter of
			// the subsidy alone and three quarters are unissued; the fees
			// the EVM credited to the coinbase are the users' and stay.
			mint = e.paySealer(chain, header, state, new(big.Int).Div(miner, big.NewInt(4)))
		} else {
			mint = e.paySealer(chain, header, state, miner)
		}
	}
	if fundAddr, moveFrom, move := e.fundAt(header.Number.Uint64()); move {
		// FundV2's height: the stand-in's whole balance to the contract
		// in one transfer, then this block's share to the contract.
		bal := state.GetBalance(moveFrom)
		if bal.Sign() > 0 {
			state.SetBalance(moveFrom, new(uint256.Int))
			creditReward(state, fundAddr, bal)
		}
		if fund.Sign() > 0 {
			creditReward(state, fundAddr, uint256.MustFromBig(fund))
		}
	} else if fund.Sign() > 0 {
		creditReward(state, fundAddr, uint256.MustFromBig(fund))
	}
	// The sponsored lanes' locks and roots carried by this block's
	// transactions (sponsored.go, the form page's sections 2 and 3):
	// admitted into the escrows' storage or refused with the value back.
	if spSnap != nil && chain != nil && chain.Config() != nil && chain.Config().GetChainID() != nil {
		e.sponsoredPaidFrom(spSnap, state, chain.Config().GetChainID().Uint64())
	}
	e.settleSponsoredTxs(chain, header, state, txs)
	// The shielded pool's shields, spends and payout registrations carried
	// by this block's transactions, then the sealer's coinbase note
	// (pool_settle.go; Project Money stages one and two), under the chain
	// id's gate IgneumPoolFrom (zero everywhere of record).
	e.settlePool(chain, header, state, txs, mint)
	// The sponsored windows' settlement and the refunds (sponsored.go, the
	// form page's sections 4 and 5), at the first block after a window
	// closes and at a span's end plus the notice.
	if chain != nil && chain.Config() != nil && chain.Config().GetChainID() != nil {
		e.settleSponsoredWindows(chain, header, state, chain.Config().GetChainID().Uint64())
	}
	// The genesis form's consensus-carried state (sortition_genesis.go):
	// B_floor every block, the units table and seed of epoch e + 2 at the
	// last block of epoch e (F-32).
	e.writeFloor(header, state)
	// The round clock amendment's state (clock.go): the two difficulties
	// and the block's time, from the parent's slots.
	if err := e.finalizeClock(chain, header, state); err != nil {
		e.log.Error("Clock not written (consensus fault)", "err", err)
	}
	if chain != nil {
		if err := e.writeTable(chain, header, state); err != nil {
			e.log.Error("Units table not written (consensus fault)", "err", err)
		}
	}
	// The authority memo (authoritymemo.go, F-39): this block's post-state
	// facts for its children's checks, after the last write above. The
	// header's hash is the block's on import (every field set); on
	// assembly it is a pre-seal hash no child looks up, evicted in turn.
	e.recordFacts(header.Hash(), header.Number.Uint64(), state)
}

// creditReward adds a reward to an account's balance and saturates at
// 2^256 - 1 instead of wrapping (F-11). A balance that would overflow is a
// consensus matter: every node must land on the same number, and a wrap
// silently destroys funds (the first devnet's faucet, allocated near the top
// of the range, read 938 IGN after 160 rewards). Saturation is deterministic
// and cannot be reached from any sane allocation; the dev faucet is 10^27 wei.
func creditReward(state *state.StateDB, addr common.Address, amount *uint256.Int) {
	sum, overflow := new(uint256.Int).AddOverflow(state.GetBalance(addr), amount)
	if overflow {
		state.SetBalance(addr, new(uint256.Int).SetAllOne())
		return
	}
	state.SetBalance(addr, sum)
}

// FinalizeAndAssemble implements consensus.Engine.
func (e *Engine) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, uncles []*types.Header, receipts []*types.Receipt, withdrawals []*types.Withdrawal) (*types.Block, error) {
	if len(withdrawals) > 0 && !e.AllowsPayoutList(header) {
		return nil, errUnsupportedWithdraw
	}
	if len(uncles) > 0 {
		return nil, errUnclesNotAllowed
	}
	if err := e.verifyPayoutList(header, withdrawals); err != nil {
		return nil, err
	}
	e.Finalize(chain, header, state, txs, nil, withdrawals)
	header.Root = state.IntermediateRoot(chain.Config().IsEnabled(chain.Config().GetEIP161dTransition, header.Number))
	var block *types.Block
	if len(withdrawals) > 0 {
		block = types.NewBlockWithWithdrawals(header, txs, nil, receipts, withdrawals, trie.NewStackTrie(nil))
	} else {
		block = types.NewBlock(header, txs, nil, receipts, trie.NewStackTrie(nil))
	}
	// The sortition prototype: the bond's signer signs the final template
	// (every field but Sig, MixDigest and Nonce), so the signature is in
	// the seal hash the worker mines.
	// The rule's state for this node's own block is the PARENT's, as on
	// import (core.StateProcessor runs the rule before the block's
	// transactions): a bond, a signer or an exit set by a transaction in
	// this very block counts from the next. The post-transaction state was
	// read here before, so a sealer accepted a block of its own whose
	// signer registration rode inside it while every other node refused it
	// at the parent's state (the fifth development chain's run 6 forked on
	// sealer A's block 11, 11 October 2026).
	ruleState := state
	if sr, ok := chain.(stateAtReader); ok {
		if parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1); parent != nil {
			if ps, err := sr.StateAt(parent.Root); err == nil {
				ruleState = ps
			}
		}
	}
	// The share lane (mineshares.go): the carried shares the worker chose
	// for this template (header.SharesRoot set by the worker from
	// TemplateShares) ride the body.
	if header.SharesRoot != nil {
		if entries, ok := e.templateEntries(*header.SharesRoot); ok {
			block = block.WithMinedShares(entries)
		} else {
			return nil, errShareNoTemplate
		}
	}
	// The settlement rule (endorse.go): the parent's endorsements this node
	// holds go into the template before the signature and the seal.
	if b, err := e.attachEndorsements(header, block, ruleState); err != nil {
		return nil, err
	} else {
		block = b
	}
	if e.config.Parts.Sortition.Active(header.Number.Uint64()) {
		keyed := e.sealIsKeyed(header, ruleState)
		h := block.Header()
		if err := e.signHeader(h); err != nil {
			return nil, err
		}
		block = block.WithSeal(h)
		// The genesis form: mark whether this template seals keyed (a
		// drawn bonded key) so the CPU search and the work package know
		// the form without state (sortition_genesis.go).
		e.keyed.set(e.SealHash(h), keyed)
		e.log.Info("Template assembled under the genesis form", "number", h.Number, "round", h.Round, "keyed", keyed, "sealhash", e.SealHash(h))
	}
	// This node's own block under the authority prototype: refused here
	// rather than sealed and then refused by every peer (the registry read
	// is at this block's state after its transactions; a bond posted in the
	// block itself does not count for it on import, so a key bonded in this
	// very block still fails there, correctly, one block later than here).
	if err := e.verifyAuthorityAt(block.Header(), ruleState, false); err != nil {
		return nil, err
	}
	return block, nil
}

// SealHash implements consensus.Engine: the keccak of the RLP of every header
// field except Nonce and MixDigest (DESIGN.md 4.3). The unsupported fields are
// a programming error if set, so they panic as they do in ethash.
func (e *Engine) SealHash(header *types.Header) (hash common.Hash) {
	hasher := sha3.NewLegacyKeccak256()
	enc := e.sealFields(header)
	// The sortition prototype's round and signature are inside H.
	if len(header.Sig) > 0 {
		enc = append(enc, header.Round, header.Sig)
	}
	rlp.Encode(hasher, enc)
	hasher.Sum(hash[:0])
	return hash
}

// sealFields is the seal hash's field list before the sortition fields.
func (e *Engine) sealFields(header *types.Header) []interface{} {
	enc := []interface{}{
		header.ParentHash,
		header.UncleHash,
		header.Coinbase,
		header.Root,
		header.TxHash,
		header.ReceiptHash,
		header.Bloom,
		header.Difficulty,
		header.Number,
		header.GasLimit,
		header.GasUsed,
		header.Time,
		header.Extra,
	}
	if header.BaseFee != nil {
		enc = append(enc, header.BaseFee)
	}
	// The authority prototype's key is inside the seal hash and so inside
	// the proof-of-work pre-image: work found under one key verifies under
	// no other (the weak form: the key, not a per-attempt secret).
	if header.BondKey != nil {
		enc = append(enc, *header.BondKey)
	}
	// The sortition prototype's payout list root (the withdrawals hash)
	// follows the key when set, so the list is sealed; without the
	// prototype the field is nil (verifyHeader refuses it). After the key
	// rather than before it so that every keyed block without a list keeps
	// the seal hash it was mined under; a valid block never has the root
	// without the key, so the two single-field forms cannot collide.
	if header.EndorsementsHash != nil {
		// The settlement rule's fields (endorse.go), INSIDE the seal hash:
		// the sealer commits to the parent's endorsements before its search.
		enc = append(enc, *header.EndorsementsHash, header.Endorsed)
	}
	if header.SharesRoot != nil {
		// The share lane's commitment (mineshares.go), INSIDE the seal hash:
		// the carried shares and the list derived from them are fixed by the
		// proof of work.
		enc = append(enc, *header.SharesRoot)
	}
	if header.WithdrawalsHash != nil {
		enc = append(enc, *header.WithdrawalsHash)
	}
	// VectorWork's coordinate tag (vector.go), INSIDE the seal hash: a
	// seal found under one coordinate is no seal for another.
	if len(header.Coord) > 0 {
		enc = append(enc, header.Coord)
	}
	// The block DAG ordering's merged parents (dagform.go), INSIDE the
	// seal hash (F-803): the work binds the mergeset, so one search cannot
	// yield many valid blocks differing in their merged parents. Empty on
	// every block of every chain without the ordering (verifyDAGHeader
	// refuses it there), so those seal hashes are what they were.
	if len(header.Parents) > 0 {
		enc = append(enc, header.Parents)
	}
	return enc
}

// fundAt is the fund's address at a height under FundV2 (the rule forks):
// the stand-in below the fork, the contract from it; move is true at the
// fork height itself, where the stand-in's balance moves to the contract.
func (e *Engine) fundAt(height uint64) (addr, standIn common.Address, move bool) {
	e.lock.Lock()
	f := e.forks
	e.lock.Unlock()
	standIn = e.config.Parts.FundAddress
	if f == nil || f.FundV2 == 0 || height < f.FundV2 {
		return standIn, standIn, false
	}
	return f.FundV2Address, standIn, height == f.FundV2
}

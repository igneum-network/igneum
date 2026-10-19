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
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/difficulty"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/pi"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// The round clock amendment (DEVNET6-RULES.md section 3; GUARD-C-CLOSURE.md
// 4.7; the project's ruling of 12:5x UK on 11 October 2026: into the seventh
// development chain's genesis at the launch clock), the consensus half
// (the node team; the protocol team's half is the sealer's search at the template's
// form and the work package). On iff the sortition parameter
// sealWindowDiv is set.
//
// Two difficulties by block form, both derived by every node from the
// PARENT's state and the parent's header, no walk:
//
//   - D(h), the chain's difficulty: PB-01's ASERT over the chain's block
//     intervals at the schedule's target, unchanged in form; an OPEN-SEAT
//     or liveness-round block carries it;
//   - D_seal(h): a second ASERT over the BONDED blocks' seal intervals
//     (the block's time less the round's floor parent.Time + r x Delta)
//     at the target (Delta - T_PI) / sealWindowDiv (4.94 s at the launch
//     clock: Delta 20 s, T_PI 0.25 s, div 4), in milliseconds so the
//     sub-second target is exact (the rule's arithmetic is homogeneous in
//     the time unit), with the same half-life and clamp as the chain's,
//     floored at D_min (the sortition parameter dMin: the floor card's
//     rate over the seal window); a BONDED block carries it. It moves only
//     when a bonded block seals (an open-seat block observes no seal
//     interval) and is seeded at the rule's first block at max(D_min,
//     D / sealWindowDiv).
//
// The clock's state (slots under the registry account, base keccak
// ("IgneumHash/R0A/clock"): 0 D(h), 1 D_seal(h), 2 the block's time, 3
// the parent's time) is written by Finalize of every block under the
// rule from the parent's slots, by the same function the check uses, so
// the verifier of h + 1 reads the parent's slots and computes the two
// expected values; the stateful stage (verifySortitionGenesis, after the
// draw) refuses a bonded block whose difficulty is not D_seal(h) and an
// open or liveness block whose difficulty is not D(h); the stateless
// stage checks only positivity under the rule. Whether the PARENT was
// bonded (so its seal interval updates D_seal) is read from its header
// against the parent's two slot values (its difficulty equal to D_seal
// and not to D); when the two coincided (D_seal seeds at D / div and
// evolves under another target, so only by chance) the parent is read as
// bonded iff it sealed below the liveness round with shares (a keyed
// document; on an unkeyed document that case reads open, stated).
//
// The per-block time floor T_min: header.Time >= parent.Time + tMin for
// every block (10 s at launch), at the stateless stage beside the round
// floor.
//
// Seam, stated for the protocol team: BlockWeight reads the header's difficulty,
// so under the amendment a bonded block (at D_seal, well under D) weighs
// less than an open-seat block at the same round; the design's weight is
// 2^-r plus the endorsement count with no D in it, which the fork choice
// should read once both forms are on one chain (their file).

// DefaultSealDriftMs is a bonded block's future drift against the node's
// clock under the clock amendment when the genesis names none: 500 ms, a
// fifth of the seal target (the project's ruling of 18:1x UK, 11 October 2026,
// on the outside review's condition that a declared time cannot move the ASERT's
// input beyond a bound near the target).
const DefaultSealDriftMs = 500

// DefaultRank0FallbackBlocks is K of the seal clock's rank-0 fallback
// (PA-48b) when the genesis names none: 16 heights. the outside review's
// condition on PA-48a (20:15 UK, 11 October 2026): a leader's absence or
// withholding must not freeze or bias the seal ASERT indefinitely, and
// the fallback must be deterministic from the chain alone. The clock
// counts the consecutive heights since the last leader's round-0 seal it
// read (Clock.Dark, slot 5; reset to zero by a leader's seal, plus one on
// every other block, open or bonded, any round); while the count at the
// parent exceeds K the step reads the parent's seal as PA-48 did, a
// bonded round-0 seal of ANY rank from the search start net of tPiMs at
// the same target, until a leader's seal resets the count. So a dark or
// withholding leader set delays the measurement by at most K heights and
// cannot freeze it; under the fallback the measurement is PA-48's (a
// rank-k seal carries k staggers of wait, PA-52's honest-sealer
// optimisation outside the adversarial bound), and the leader's return
// restores PA-48a's.
const DefaultRank0FallbackBlocks = 16

// The chain constant SealStaggerMs (sortition.go) is PA-52's sealer stagger
// as the verifier reads it, defaulting to the node setting's default
// (stagger.go DefaultSealStaggerMs, 2,000 ms) when the genesis names none
// (the design seat's amendment to PA-48b, 20:4x UK, 11 October 2026).
// Under the fallback a rank-k round-0 seal is read NET of k x SealStaggerMs
// beside tPiMs: read raw, a sustained fallback would settle the search at
// the target less k staggers, zero or less for every rank above 0 at the
// research and launch targets, and drive D_seal to its floor for as long
// as it ran (the pre-stagger PA-48 bias, unbounded); net of the constant a
// staggered seal reads its search, and a sealer that does not stagger
// reads short by k staggers and raises D_seal against everyone by that
// much, bounded by its share of round-0 seals and self-limiting (its own
// seals get harder): a small upward bias and never a freeze.

// verifyDeclaredTime bounds a header's declared time above against the
// receiving node's clock (unixNow in whole seconds): a BONDED block under
// the clock amendment, read statelessly by its carried shares (only a
// drawn bonded key's block carries them on a keyed document; on an
// unkeyed document every block reads open here, stated), by the chain
// constant SealDriftMs floored to whole seconds (zero at 500 ms: a bonded
// block declared in the next second is held up to one second); every
// other block by the node's MaxFutureDrift (15 s). A block ahead is
// consensus.ErrFutureBlock, which the chain holds in its future queue and
// imports when the time comes, never a refusal for good. The elapsed the
// seal ASERT reads is the declared time less a start derived before the
// search from the parent's state and frozen constants, so this bound is
// what caps a late declaration's purchase: at most the drift of elapsed a
// block, 2^(-drift / halfLife) of D_seal (nothing at a zero whole-second
// drift; 0.29 percent at the open seat's 15 s).
func (e *Engine) verifyDeclaredTime(header *types.Header, unixNow int64) error {
	s := e.config.Parts.Sortition
	if header.Number != nil && e.ClockOn(header.Number.Uint64()) && len(header.Shares) > 0 {
		if header.Time > uint64(unixNow)+s.SealDriftMs/1000 {
			return consensus.ErrFutureBlock
		}
		return nil
	}
	if header.Time > uint64(unixNow+int64(e.config.MaxFutureDrift.Seconds())) {
		return consensus.ErrFutureBlock
	}
	return nil
}

var (
	errClockOff        = errors.New("igneum clock: the amendment is off on this chain")
	errClockState      = errors.New("igneum clock: the clock's state could not be read")
	errSealDifficulty  = errors.New("igneum clock: a bonded block must carry the seal difficulty")
	errChainDifficulty = errors.New("igneum clock: an open-seat or liveness block must carry the chain difficulty")
	errUnderTimeFloor  = errors.New("igneum sortition: under the time floor")
)

var clockDomain = []byte("IgneumHash/R0A/clock")

func clockSlot(i int64) common.Hash {
	b := crypto.Keccak256Hash(clockDomain).Big()
	return common.BigToHash(b.Add(b, big.NewInt(i)))
}

// Clock is the clock's state after a block: the two difficulties and the
// block's and its parent's times.
type Clock struct {
	Chain, Seal *big.Int
	Time        uint64
	ParentTime  uint64
	// Rank is the block's sealer's rank among the keys eligible at its
	// round (LotRankAt at the block's Finalize; 0 the leader; -1 for an
	// open-seat, liveness or unranked block), PA-48a: the seal ASERT reads
	// the LEADER's round-0 seals only, since a rank-k seal under PA-52's
	// stagger carries k staggers of wait inside its elapsed that no
	// verifier can subtract (the stagger is a node setting).
	Rank int
	// Dark is the count of consecutive blocks ending at this one with no
	// leader's round-0 seal read by the seal ASERT (slot 5; PA-48b): zero
	// when this block is a leader's round-0 bonded seal, else the parent's
	// count plus one, capped at the chain's K (Rank0FallbackBlocks). The
	// next block's step falls back to PA-48's read of a bonded round-0
	// seal of any rank, net of its rank's staggers, while Dark is at K.
	Dark uint64
}

// ClockOn reports whether the amendment binds a block at this height.
func (e *Engine) ClockOn(height uint64) bool {
	s := e.config.Parts.Sortition
	return s != nil && s.Active(height) && s.SealWindowDiv > 0 && e.config.Parts.Authority != nil
}

// sealParams is the seal ASERT in milliseconds: the target (Delta - T_PI)
// / sealWindowDiv, the chain's half-life and clamp scaled, D_min the floor.
// SetRuleForks sets the chain's rule forks (the backend, from the chain
// id's params.IgneumChainRuleForks entry; nil for none).
func (e *Engine) SetRuleForks(f *ctypes.IgneumRuleForks) {
	// The bounded tail (TailV1): the fork's figures into the sortition
	// parameters with the height they apply from, read by every round
	// floor, clock floor and seal measure by height.
	if f != nil && f.TailV1 > 0 {
		if s := e.config.Parts.Sortition; s != nil {
			s.TailFrom = f.TailV1
			if f.TailDelta > 0 {
				s.TailDelta = f.TailDelta
			}
			if f.TailTMin > 0 {
				s.TailTMin = f.TailTMin
			}
		}
	}
	// ClockDelta (0.10.49): the re-pinned Delta must keep the clock
	// amendment's bounds against the clock constants in force (tMin plus
	// tPiMs under it; tailDelta at most it), else the entry is refused
	// here, before any block, as sortition.go refuses the genesis form.
	if f != nil && f.ClockV1 > 0 && f.ClockDelta > 0 {
		if s := e.config.Parts.Sortition; s != nil {
			tPiMs := s.TPiMs
			if f.ClockTPiMs > 0 {
				tPiMs = f.ClockTPiMs
			}
			tMin, tail := s.TMinAt(f.ClockV1), s.TailDelta
			if rt, rtd := s.registeredAt(f.ClockV1); rt > 0 || rtd > 0 {
				tMin, tail = rt, rtd
			}
			if tMin*1000+tPiMs >= f.ClockDelta*1000 || (tail > 0 && tail > f.ClockDelta) {
				e.log.Crit("The forks entry's clockDelta breaks the clock amendment's bounds", "clockV1", f.ClockV1, "clockDelta", f.ClockDelta, "tMin", tMin, "tPiMs", tPiMs, "tailDelta", tail)
			}
		}
	}
	// ClockDelta's hook (0.10.49): the round clock's Delta from ClockV1
	// through the sortition parameters, read by every round floor, the
	// seal target and the drift bound by height; nil without the entry.
	if s := e.config.Parts.Sortition; s != nil {
		s.DeltaAt = nil
		if f != nil && f.ClockV1 > 0 && f.ClockDelta > 0 {
			from, delta := f.ClockV1, f.ClockDelta
			s.DeltaAt = func(height uint64) uint64 {
				if height < from {
					return 0
				}
				return delta
			}
			tPiMs := s.TPiMs
			if f.ClockTPiMs > 0 {
				tPiMs = f.ClockTPiMs
			}
			e.log.Info("Round clock re-pinned from a height", "from", from, "deltaSec", delta, "tPiMs", tPiMs, "genesisDeltaSec", s.Delta)
		}
	}
	e.lock.Lock()
	defer e.lock.Unlock()
	// F-461: a chain whose forks entry gates the proof needs the verifier
	// in this binary (pi.Built with the pinned image id), else the node
	// would accept what it cannot check from the landing check; refused at start.
	if f != nil && f.PiVerifyV1 > 0 && !e.config.PiVerify {
		if !pi.Built {
			e.log.Crit("The chain's forks entry gates the proof pi from a height and this node carries no verifier (build tag ignpi)", "piVerifyV1", f.PiVerifyV1)
		}
		if got := pi.LibraryImageIDHex(); got != pi.ImageIDHex {
			e.log.Crit("The chain's forks entry gates the proof pi and the linked verifier's image id is not the node's constant", "piVerifyV1", f.PiVerifyV1, "linked", got, "constant", pi.ImageIDHex)
		}
		e.log.Info("Proof pi verification the chain's rule from a height", "piVerifyV1", f.PiVerifyV1, "imageId", pi.ImageIDHex[:16])
	}
	e.forks = f
}

// sealMeasureFirstForm says whether the block at height is read under the
// seal clock's FIRST measurement (every bonded parent steps the seal ASERT
// over its interval from the round floor, at the target (Delta - T_PI) /
// div): true below the chain's SealMeasureV1 fork height, never without
// one (the second form, PA-48, from genesis). devnet7 (11 October 2026):
// the second form was swapped in place at height 351, and a fresh node
// under it refused block 13, a seal step over a round-2 bonded parent;
// the gate makes the chain sync from genesis again.
func (e *Engine) sealMeasureFirstForm(height uint64) bool {
	e.lock.Lock()
	f := e.forks
	e.lock.Unlock()
	return f != nil && f.SealMeasureV1 > 0 && height < f.SealMeasureV1
}

// clockRankOn says whether the block at height writes the sealer's lot
// rank into the clock's fifth slot at Finalize and reads the leader's
// round-0 seals only (PA-48a): true from the chain's ClockRankV1 fork
// height, always without one (from genesis). devnet7 (11 October 2026):
// PA-48a was swapped in place at 20:18 UK at height 681; a fresh node
// under it wrote the slot from block 10 and refused 10 on the state root.
func (e *Engine) clockRankOn(height uint64) bool {
	e.lock.Lock()
	f := e.forks
	e.lock.Unlock()
	return f == nil || f.ClockRankV1 == 0 || height >= f.ClockRankV1
}

// clockDarkOn says whether the block at height writes the dark count into
// the clock's sixth slot at Finalize and counts it in the seal step
// (PA-48b's fallback): true from the chain's ClockDarkV1 fork height,
// always without one. devnet7 (21:45 UK, 11 October 2026): the fallback
// landed on main without a gate and the pool cell's members ran it
// against sealers that did not; their block 821 was refused on the state
// root; the gate is a planned height, with the sealer term's.
func (e *Engine) clockDarkOn(height uint64) bool {
	e.lock.Lock()
	f := e.forks
	e.lock.Unlock()
	return f == nil || f.ClockDarkV1 == 0 || height >= f.ClockDarkV1
}

// forkLengthOn says whether the fork choice's length order (F-44 part 3:
// at equal cumulative weight the longer branch wins) is in force at a
// height; zero is OFF (ctypes.IgneumRuleForks.ForkLengthV1).
func (e *Engine) forkLengthOn(height uint64) bool {
	e.lock.Lock()
	f := e.forks
	e.lock.Unlock()
	return f != nil && f.ForkLengthV1 != 0 && height >= f.ForkLengthV1
}

// policyOn says whether the second fault release's by-height parts (the
// table-share carry quorum, policy parts 4 and 5, the parent-keyed carried
// mark) are in force at a height (ctypes.IgneumRuleForks.PolicyV1); zero
// is OFF. A chain with no forks entry at all (a chain started on the
// release, a test engine) runs the parts from genesis.
func (e *Engine) policyOn(height uint64) bool {
	e.lock.Lock()
	f := e.forks
	e.lock.Unlock()
	if f == nil {
		return true
	}
	return f.PolicyV1 != 0 && height >= f.PolicyV1
}

// PolicyOn is policyOn for the miner and the backend.
func (e *Engine) PolicyOn(height uint64) bool { return e.policyOn(height) }

func (e *Engine) sealParams() difficulty.Params {
	return e.sealParamsAt(0, false)
}

// sealParamsAt is sealParams by the block's height: the first form's
// target below the chain's SealMeasureV1 fork (firstForm true), else the
// second's.
func (e *Engine) sealParamsAt(height uint64, firstForm bool) difficulty.Params {
	s := e.config.Parts.Sortition
	d := e.config.Parts.Difficulty
	tPiMs, dMin := e.clockConstsAt(height)
	if firstForm {
		target := (s.DeltaAt_(height)*1000 - tPiMs) / s.SealWindowDiv
		if target == 0 {
			target = 1
		}
		return difficulty.Params{Target: target, HalfLife: d.HalfLife * 1000, DtMax: d.DtMax * 1000, Min: new(big.Int).Set(dMin)}
	}
	// The seal target over the round-0 WINDOW that remains once the search
	// starts at the time floor: (Delta - tMin - T_PI) / sealWindowDiv,
	// 2,437 ms at the launch clock (the project's ruling of 16:4x UK, 11 October
	// 2026, on the design seat's overrun reading of PA-48: at (Delta -
	// T_PI) / 4 a 4.937 s mean search would overrun the round-1 floor on
	// 14 percent of heights against the design's 1.8; this form keeps
	// 1.8). The term follows tMin, so a T_min of 12 s moves it with no
	// other change.
	target := (s.DeltaAt_(height)*1000 - s.TMinAt(height)*1000 - tPiMs) / s.SealWindowDiv
	if target == 0 {
		target = 1
	}
	return difficulty.Params{Target: target, HalfLife: d.HalfLife * 1000, DtMax: d.DtMax * 1000, Min: new(big.Int).Set(dMin)}
}

// clockConstsAt is the seal clock's two measured constants at a height:
// the genesis document's tPiMs and dMin, or from the chain's ClockV1 fork
// height the re-pinned figures of its forks entry (the built-in table for
// the chain id; a field left zero keeps the document's). The proof budget
// and the seal floor move together at the height on every node that
// carries the entry; a node without it forks there, as under the other
// fields.
func (e *Engine) clockConstsAt(height uint64) (tPiMs uint64, dMin *big.Int) {
	s := e.config.Parts.Sortition
	tPiMs, dMin = s.TPiMs, s.DMin
	e.lock.Lock()
	f := e.forks
	e.lock.Unlock()
	if f == nil || f.ClockV1 == 0 || height < f.ClockV1 {
		return tPiMs, dMin
	}
	if f.ClockTPiMs > 0 {
		tPiMs = f.ClockTPiMs
	}
	if f.ClockDMin > 0 {
		dMin = new(big.Int).SetUint64(f.ClockDMin)
	}
	return tPiMs, dMin
}

// readClock reads the clock's state; a zero state (the rule's first block)
// reads nil.
func (e *Engine) readClock(st *state.StateDB) (*Clock, error) {
	reg := e.config.Parts.Authority.Registry
	chain := st.GetState(reg, clockSlot(0)).Big()
	if chain.Sign() == 0 {
		return nil, nil
	}
	seal := st.GetState(reg, clockSlot(1)).Big()
	t := st.GetState(reg, clockSlot(2)).Big()
	pt := st.GetState(reg, clockSlot(3)).Big()
	if seal.Sign() == 0 || !t.IsUint64() || !pt.IsUint64() {
		return nil, errClockState
	}
	// Slot 4 holds the sealer's rank plus one (PA-48a); zero, the slot of
	// every block written before the rule, reads -1: no leader's seal.
	rank := -1
	if r := st.GetState(reg, clockSlot(4)).Big(); r.Sign() > 0 && r.IsUint64() {
		rank = int(r.Uint64()) - 1
	}
	// Slot 5 holds the dark count (PA-48b); zero, the slot of every block
	// written before the rule, reads no dark height.
	dark := uint64(0)
	if d := st.GetState(reg, clockSlot(5)).Big(); d.Sign() > 0 && d.IsUint64() {
		dark = d.Uint64()
	}
	return &Clock{Chain: chain, Seal: seal, Time: t.Uint64(), ParentTime: pt.Uint64(), Rank: rank, Dark: dark}, nil
}

func (e *Engine) writeClock(st *state.StateDB, c *Clock, withRank bool) {
	e.writeClockForms(st, c, withRank, withRank)
}

// writeClockForms is writeClock with the two state gates apart: the rank
// slot from ClockRankV1, the dark count's slot from ClockDarkV1.
func (e *Engine) writeClockForms(st *state.StateDB, c *Clock, withRank, withDark bool) {
	reg := e.config.Parts.Authority.Registry
	st.SetState(reg, clockSlot(0), common.BigToHash(c.Chain))
	st.SetState(reg, clockSlot(1), common.BigToHash(c.Seal))
	st.SetState(reg, clockSlot(2), common.BigToHash(new(big.Int).SetUint64(c.Time)))
	st.SetState(reg, clockSlot(3), common.BigToHash(new(big.Int).SetUint64(c.ParentTime)))
	if !withRank {
		// Below the chain's ClockRankV1 fork the fifth slot is not
		// touched (the state root of every block before the swap).
		return
	}
	rankPlusOne := uint64(0)
	if c.Rank >= 0 {
		rankPlusOne = uint64(c.Rank) + 1
	}
	st.SetState(reg, clockSlot(4), common.BigToHash(new(big.Int).SetUint64(rankPlusOne)))
	if !withDark {
		// Below the chain's ClockDarkV1 fork the sixth slot is not touched
		// (PA-48b's dark count landed on main at cadce178 and the pool
		// cell's members ran it against sealers that did not: block 821
		// of devnet7 refused on the state root, 21:45 UK, 11 October 2026).
		return
	}
	st.SetState(reg, clockSlot(5), common.BigToHash(new(big.Int).SetUint64(c.Dark)))
}

// parentBonded reads whether the parent sealed as a bonded key, from its
// header against its clock's two values.
func (e *Engine) parentBonded(parent *types.Header, c *Clock) bool {
	s := e.config.Parts.Sortition
	if parent.BondKey == nil || parent.Round >= s.UnbondedRound {
		return false
	}
	if c.Seal.Cmp(c.Chain) != 0 {
		return parent.Difficulty.Cmp(c.Seal) == 0
	}
	return len(parent.Shares) > 0
}

// ClockNext computes the two difficulties of the block after parent from
// the parent's state: D(h) by the chain's ASERT over the parent's
// interval, D_seal(h) by the seal ASERT over the parent's seal interval
// when the parent was bonded, else unchanged; the seed at the rule's
// first block. Returns the clock the block will write, with its Time
// left for the caller.
func (e *Engine) ClockNext(parentState *state.StateDB, parent *types.Header) (*Clock, error) {
	height := parent.Number.Uint64() + 1
	if !e.ClockOn(height) {
		return nil, errClockOff
	}
	c, err := e.readClock(parentState)
	if err != nil {
		return nil, err
	}
	return e.clockNextFrom(c, parent)
}

// clockNextFrom is ClockNext's step over the parent's clock slots already
// read (c nil before the rule): the memo's path (authoritymemo.go) and the
// state's share it.
func (e *Engine) clockNextFrom(c *Clock, parent *types.Header) (*Clock, error) {
	s := e.config.Parts.Sortition
	// The seal floor at the CHILD's height (the clock stepped here is the
	// child's; ClockV1 re-pins it from its height).
	_, dMin := e.clockConstsAt(parent.Number.Uint64() + 1)
	next := &Clock{ParentTime: parent.Time}
	if c == nil {
		// The seed: the parent's difficulty is the chain's (the rule was
		// off for it); D(h) from it at the target interval (the seed does
		// not know the parent's own interval; ChainDifficulty seeds the
		// same way); D_seal at max(D_min, D / div).
		next.Chain = e.chainStep(parent.Difficulty, 0, 0, 0)
		seed := new(big.Int).Div(next.Chain, new(big.Int).SetUint64(s.SealWindowDiv))
		if seed.Cmp(dMin) < 0 {
			seed.Set(dMin)
		}
		next.Seal = seed
		return next, nil
	}
	next.Chain = e.chainStep(c.Chain, parent.Number.Uint64(), c.Time, c.ParentTime)
	next.Seal = new(big.Int).Set(c.Seal)
	if e.sealMeasureFirstForm(parent.Number.Uint64() + 1) {
		// The first measurement, below the chain's SealMeasureV1 fork
		// (devnet7 below 351): every bonded parent steps the seal ASERT
		// over its interval from the round floor, at (Delta - T_PI) / div.
		if e.parentBonded(parent, c) {
			floor := c.ParentTime + s.RoundStart(parent.Number.Uint64(), parent.Round)
			var interval uint64
			if c.Time > floor {
				interval = c.Time - floor
			}
			next.Seal = e.sealParamsAt(parent.Number.Uint64()+1, true).NextRelative(c.Seal, interval*1000)
		}
		if next.Seal.Cmp(dMin) < 0 {
			next.Seal.Set(dMin)
		}
		return next, nil
	}
	// PA-48a: the LEADER's round-0 seals only (the parent's sealer at rank 0
	// among the keys eligible at its round, written into the clock at the
	// parent's Finalize): a rank-k seal under PA-52's stagger carries k
	// staggers of wait inside its elapsed, which no verifier can subtract
	// since the stagger is a node setting; the leader starts at the floor
	// and its elapsed is the search plus the proof and nothing else.
	roundZeroBonded := parent.Round == 0 && e.parentBonded(parent, c)
	// Between devnet7's two forks (351 to 680) the rank slot was not
	// written, so the step reads every round-0 bonded seal (PA-48); from
	// the ClockRankV1 fork the leader's only (PA-48a), with the fallback.
	leader := c.Rank == 0 || !e.clockRankOn(parent.Number.Uint64()+1)
	switch {
	case roundZeroBonded && leader:
		// The leader's seal: the measurement, and the dark count's reset.
		next.Seal = e.sealStep(c, parent, 0)
		next.Dark = 0
	default:
		if !e.clockDarkOn(parent.Number.Uint64() + 1) {
			// Below the chain's ClockDarkV1 fork the count is not kept
			// and the fallback is off (PA-48a's form as the sealers ran
			// it).
			next.Dark = 0
			break
		}
		// No leader's round-0 seal at the parent: one more dark height,
		// the count capped at K (the state stays bounded; at K the
		// fallback is on).
		next.Dark = c.Dark + 1
		if next.Dark > s.Rank0FallbackBlocks {
			next.Dark = s.Rank0FallbackBlocks
		}
		if roundZeroBonded && c.Rank > 0 && c.Dark >= s.Rank0FallbackBlocks {
			// PA-48b, the fallback: K heights have passed with no
			// leader's seal, so the parent's bonded round-0 seal is read
			// as PA-48 did, net of its rank's staggers (the design
			// seat's amendment), until a leader's seal next resets the
			// count. An unranked seal (slot 4 zero: a block before
			// PA-48a, or a rank the draw could not give) is not read,
			// as under PA-48a, stated.
			next.Seal = e.sealStep(c, parent, uint64(c.Rank))
		}
	}
	if next.Seal.Cmp(dMin) < 0 {
		next.Seal.Set(dMin)
	}
	return next, nil
}

// sealStep is the seal ASERT's step over the parent's round-0 bonded seal
// (PA-48; devnet7's C17 reading; the project's ruling of 16:4x UK, 11 October
// 2026): the seal ASERT reads the ROUND-0 bonded seals only (a later
// round's seal is searched before its floor and its interval reads the
// wait, not the search), from the SEARCH START the rule fixes for the
// sealer, max(the round floor, parent.Time + tMin) (a block cannot exist
// before tMin, so an interval read from the round floor alone would carry
// the time floor and never the search), and NET of the registered proof
// budget tPiMs, so the target (Delta - tMin - T_PI) / div measures the
// search alone; a proof slower than its budget reads as search (devnet7's
// CPU proof; the reader states it). The elapsed is the parent's declared
// time less a start fixed before its search, so a late declaration buys
// at most PA-50's drift of elapsed. Under the fallback (PA-48b) the seal's
// rank is given and its k staggers (SealStaggerMs each, the chain
// constant) come off beside the proof budget; the leader's rank is 0.
func (e *Engine) sealStep(c *Clock, parent *types.Header, rank uint64) *big.Int {
	s := e.config.Parts.Sortition
	start := c.ParentTime + s.TMinAt(parent.Number.Uint64())
	if floor := c.ParentTime + s.RoundStart(parent.Number.Uint64(), parent.Round); floor > start {
		start = floor
	}
	var intervalMs uint64
	if c.Time > start {
		intervalMs = (c.Time - start) * 1000
	}
	tPiMs, _ := e.clockConstsAt(parent.Number.Uint64() + 1)
	allowance := tPiMs + rank*s.SealStaggerMs
	if intervalMs > allowance {
		intervalMs -= allowance
	} else {
		intervalMs = 0
	}
	return e.sealParams().NextRelative(c.Seal, intervalMs)
}

// chainStep is D(h) from D(parent) over the parent's interval (the
// chain's ASERT, PB-01's form): dt = parentTime minus grandparentTime; the
// target interval when the parent is genesis or the rule's seed (parent
// number 0 here stands for both). One function for the state path
// (ClockNext) and the header path (ChainDifficulty), so the two cannot
// diverge.
func (e *Engine) chainStep(dParent *big.Int, parentNumber, parentTime, grandparentTime uint64) *big.Int {
	d := e.config.Parts.Difficulty
	dt := d.Target
	if parentNumber > 0 {
		if parentTime > grandparentTime {
			dt = parentTime - grandparentTime
		} else {
			dt = 1
		}
	}
	return d.NextRelative(dParent, dt)
}

// ChainDifficulty is the chain's difficulty D(h) of a block from the
// headers alone: the header's difficulty without the amendment (an open
// block carries it under the amendment too, but the header path cannot
// tell the forms apart without state), else the chain's ASERT rolled
// forward from the last header below the rule's activation (whose
// difficulty is D) over the parents' intervals, cached by block hash so
// the walk is one step per imported block. A header whose parent the
// anchored chain does not hold reads its own difficulty (a header-only
// import ahead of its parent cannot weigh; the body import weighs it
// again at the state).
func (e *Engine) ChainDifficulty(header *types.Header) *big.Int {
	height := header.Number.Uint64()
	if !e.ClockOn(height) {
		return new(big.Int).Set(header.Difficulty)
	}
	e.sortition.mu.Lock()
	chain := e.sortition.chain
	if d := e.sortition.chainD[header.Hash()]; d != nil {
		e.sortition.mu.Unlock()
		return new(big.Int).Set(d)
	}
	e.sortition.mu.Unlock()
	if chain == nil {
		return new(big.Int).Set(header.Difficulty)
	}
	// Walk back to a known D: a cached ancestor, or the first header below
	// the rule (its difficulty is D); collect the path.
	var path []*types.Header
	h := header
	var known *big.Int
	for {
		parent := chain.GetHeader(h.ParentHash, h.Number.Uint64()-1)
		if parent == nil {
			return new(big.Int).Set(header.Difficulty)
		}
		path = append(path, h)
		if !e.ClockOn(parent.Number.Uint64()) {
			known = e.chainStep(parent.Difficulty, 0, 0, 0)
			break
		}
		e.sortition.mu.Lock()
		d := e.sortition.chainD[parent.Hash()]
		e.sortition.mu.Unlock()
		if d != nil {
			// The parent's D is known: this block's is the step over the
			// parent's interval, which needs the grandparent's time.
			gp := chain.GetHeader(parent.ParentHash, parent.Number.Uint64()-1)
			if gp == nil {
				return new(big.Int).Set(header.Difficulty)
			}
			known = e.chainStep(d, parent.Number.Uint64(), parent.Time, gp.Time)
			break
		}
		h = parent
	}
	// known is D of path's last element; roll forward to the header.
	e.sortition.mu.Lock()
	if e.sortition.chainD == nil {
		e.sortition.chainD = make(map[common.Hash]*big.Int)
	}
	if len(e.sortition.chainD) > 1<<16 {
		e.sortition.chainD = make(map[common.Hash]*big.Int)
	}
	d := known
	for i := len(path) - 1; i >= 0; i-- {
		cur := path[i]
		if i < len(path)-1 {
			// cur's parent is path[i+1] with D d; cur's D steps over the
			// parent's interval: parent.Time minus grandparent.Time.
			parent := path[i+1]
			var gpTime uint64
			if i+2 < len(path) {
				gpTime = path[i+2].Time
			} else if gp := chain.GetHeader(parent.ParentHash, parent.Number.Uint64()-1); gp != nil {
				gpTime = gp.Time
			}
			d = e.chainStep(d, parent.Number.Uint64(), parent.Time, gpTime)
		}
		e.sortition.chainD[cur.Hash()] = new(big.Int).Set(d)
	}
	e.sortition.mu.Unlock()
	return new(big.Int).Set(d)
}

// ExpectedDifficulty is the difficulty a block of the given form owes
// (the protocol team's template fills its header from it).
func (e *Engine) ExpectedDifficulty(parentState *state.StateDB, parent *types.Header, bonded bool) (*big.Int, error) {
	c, err := e.ClockNext(parentState, parent)
	if err != nil {
		return nil, err
	}
	if bonded {
		return c.Seal, nil
	}
	return c.Chain, nil
}

// templateBonded is the form a template takes under the genesis form: a
// drawn bonded key (units in the table) below the liveness round seals as
// a bonded block; the open seat and the liveness round seal as open.
func (e *Engine) templateBonded(st *state.StateDB, header *types.Header) (bool, error) {
	s := e.config.Parts.Sortition
	if header.BondKey == nil || header.Round >= s.UnbondedRound {
		return false, nil
	}
	d, err := e.drawAt(st, *header.BondKey, header.Number.Uint64())
	if err != nil {
		return false, err
	}
	return !d.open, nil
}

// prepareClock sets a template's difficulty by its form under the
// amendment (the sealer's half of the rule; Prepare calls it after
// prepareSortition has fixed the key and the round): D_seal for a bonded
// template, D for an open-seat or liveness one, both from the parent's
// state through ClockNext, so the sealed block passes verifyClock on its
// own node's validation (the first devnet7 block 10 was REFUSED by its own
// sealer at "have 8246, want 54000": the template carried the chain
// difficulty for a bonded form; 13:2x UK, 11 October 2026). Off the
// amendment it leaves Prepare's chain difficulty as it is.
func (e *Engine) prepareClock(chain consensus.ChainHeaderReader, header, parent *types.Header) error {
	if !e.ClockOn(header.Number.Uint64()) {
		return nil
	}
	sr, ok := chain.(stateAtReader)
	if !ok {
		return errSortitionNoChain
	}
	st, err := sr.StateAt(parent.Root)
	if err != nil {
		return err
	}
	bonded, err := e.templateBonded(st, header)
	if err != nil {
		return err
	}
	d, err := e.ExpectedDifficulty(st, parent, bonded)
	if err != nil {
		return err
	}
	header.Difficulty = d
	return nil
}

// verifyClockAt is the stateful check from the genesis form's rule, which
// holds no parent header: the parent is read through the anchored chain
// (sortitionAnchor; every import and dry run anchors first), and a chain
// not anchored is a fault, never a pass.
func (e *Engine) verifyClockAt(header *types.Header, parentState *state.StateDB, bonded bool) error {
	e.sortition.mu.Lock()
	chain := e.sortition.chain
	e.sortition.mu.Unlock()
	if chain == nil {
		return fmt.Errorf("%w: no chain anchored for the parent header", errClockState)
	}
	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	return e.verifyClock(header, parent, parentState, bonded)
}

// verifyClockWith is verifyClockAt over a rule reader (the memo when it
// holds the parent's clock, else the state; authoritymemo.go).
func (e *Engine) verifyClockWith(header *types.Header, r *ruleReader, bonded bool) error {
	e.sortition.mu.Lock()
	chain := e.sortition.chain
	e.sortition.mu.Unlock()
	if chain == nil {
		return fmt.Errorf("%w: no chain anchored for the parent header", errClockState)
	}
	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	if !e.ClockOn(parent.Number.Uint64() + 1) {
		return errClockOff
	}
	c, err := r.clock()
	if err != nil {
		return err
	}
	next, err := e.clockNextFrom(c, parent)
	if err != nil {
		return err
	}
	return e.compareClock(header, next, bonded)
}

// verifyClock is the stateful check: the header's difficulty against the
// form the draw gave it (bonded below the liveness round, else open).
func (e *Engine) verifyClock(header, parent *types.Header, parentState *state.StateDB, bonded bool) error {
	c, err := e.ClockNext(parentState, parent)
	if err != nil {
		return err
	}
	return e.compareClock(header, c, bonded)
}

// compareClock is the comparison both paths share. Under the vector form
// the block's difficulty is its coordinate's controller output
// (verifyVectorHeader, read before the clock in verifyHeader's order), so
// the clock's two forms do not apply and the comparison is void
// (vectorwork1's third run, 13 October 2026, 01:34 UTC: "an open-seat or
// liveness block must carry the chain difficulty: have 73120, want
// 36139" from the stateful path after the header rule had passed).
func (e *Engine) compareClock(header *types.Header, c *Clock, bonded bool) error {
	if e.vectorMenu() != nil {
		return nil
	}
	if bonded {
		if header.Difficulty.Cmp(c.Seal) != 0 {
			return fmt.Errorf("%w: have %v, want %v (chain %v)", errSealDifficulty, header.Difficulty, c.Seal, c.Chain)
		}
		return nil
	}
	if header.Difficulty.Cmp(c.Chain) != 0 {
		return fmt.Errorf("%w: have %v, want %v (seal %v)", errChainDifficulty, header.Difficulty, c.Chain, c.Seal)
	}
	return nil
}

// finalizeClock writes the block's clock in Finalize: the parent's slots
// advanced by ClockNext, the block's own time.
func (e *Engine) finalizeClock(chain consensus.ChainHeaderReader, header *types.Header, st *state.StateDB) error {
	if !e.ClockOn(header.Number.Uint64()) || chain == nil {
		return nil
	}
	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	c, err := e.ClockNext(st, parent)
	if err != nil {
		return err
	}
	c.Time = header.Time
	// PA-48a: this block's sealer's rank among the keys eligible at its
	// round, for the next block's ASERT step (0 the leader): a bonded key
	// below the liveness round ranks; an open-seat or liveness block
	// reads -1. Read at this block's Finalize from its own state, the same
	// on every node (the written tables are the epoch's; in the live-table
	// epochs a bond posted in this very block moves the units it reads,
	// deterministically, stated).
	c.Rank = -1
	s := e.config.Parts.Sortition
	rankOn := e.clockRankOn(header.Number.Uint64())
	if rankOn && header.BondKey != nil && header.Round < s.UnbondedRound {
		if rank, _, err := e.LotRankAt(st, *header.BondKey, header.Number.Uint64(), header.Round); err == nil {
			c.Rank = rank
		}
	}
	e.writeClockForms(st, c, rankOn, e.clockDarkOn(header.Number.Uint64()))
	return nil
}

// ReadClock is the clock at a state (igneum_sortition's read; the rows).
func (e *Engine) ReadClock(st *state.StateDB) (*Clock, error) {
	if e.config.Parts.Authority == nil {
		return nil, errClockOff
	}
	return e.readClock(st)
}

// RoundCensus counts the rounds of the last n blocks ending at head
// (igneum_sortition's census; C19 reads round 0's share against 0.625).
func (e *Engine) RoundCensus(chain consensus.ChainHeaderReader, head *types.Header, n int) map[uint64]int {
	out := map[uint64]int{}
	h := head
	for i := 0; i < n && h != nil && h.Number.Sign() > 0; i++ {
		out[h.Round]++
		h = chain.GetHeader(h.ParentHash, h.Number.Uint64()-1)
	}
	return out
}

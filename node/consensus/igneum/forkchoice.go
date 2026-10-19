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
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
)

var errLotRankOff = errors.New("igneum sortition: the lot rank needs the genesis form")

// The equal-weight tie-break (PA-47; security's F-41 on devnet7, the project's
// ruling of 16:3x UK, 11 October 2026): with no adversary, two sealers each
// extended their own block for four heights, because geth's fork choice
// keeps a sealer on its own branch at equal weight (the preserve rule) and
// otherwise flips a coin, so the branches resolved only on cumulative
// weight: a five-deep natural reorganisation. The rule, deterministic on
// every node: at equal length and equal weight the branch whose block at
// the FIRST contested height carries the greater endorsement weight wins
// (the endorsements a block collects ride its successor: the weight is
// the successor's Endorsed on that branch, zero while the block has no
// successor); at equal weight a bonded block beats an open-seat block,
// two bonded blocks tie on the LOWER LOT VALUE of the sealing unit at
// that height (ungrindable; the seal-hash term of 7dbf1116 superseded on
// the design seat's grinding finding), two open-seat blocks on the lower
// seal hash. No coin and no preserve: a sealer abandons its own branch
// the moment the rule prefers the other (its template follows the head),
// and an endorser endorses at most once a height (endorse.go), so its
// endorsement goes to the head it holds when it first endorses and never
// to a second block at that height, which would be its own equivocation;
// convergence comes from the sealers, within one height, since at the
// contested height itself (no successors yet) the lower seal hash decides
// at once on every node.

var errForkNoAncestor = errors.New("igneum fork choice: the two heads share no ancestor in the chain")

// PreferBranch reports whether the chain should move from current to
// extern, two heads of EQUAL weight and equal height, by the rule above.
func (e *Engine) PreferBranch(chain consensus.ChainHeaderReader, current, extern *types.Header) (bool, error) {
	if current == nil || extern == nil || current.Hash() == extern.Hash() {
		return false, nil
	}
	// Walk both branches back to the common ancestor, keeping each
	// branch's headers from the contested height up.
	cur, ext := current, extern
	var curBranch, extBranch []*types.Header
	for cur.Number.Uint64() > ext.Number.Uint64() {
		curBranch = append(curBranch, cur)
		if cur = chain.GetHeader(cur.ParentHash, cur.Number.Uint64()-1); cur == nil {
			return false, errForkNoAncestor
		}
	}
	for ext.Number.Uint64() > cur.Number.Uint64() {
		extBranch = append(extBranch, ext)
		if ext = chain.GetHeader(ext.ParentHash, ext.Number.Uint64()-1); ext == nil {
			return false, errForkNoAncestor
		}
	}
	for cur.Hash() != ext.Hash() {
		curBranch = append(curBranch, cur)
		extBranch = append(extBranch, ext)
		if cur.Number.Uint64() == 0 {
			return false, errForkNoAncestor
		}
		if cur = chain.GetHeader(cur.ParentHash, cur.Number.Uint64()-1); cur == nil {
			return false, errForkNoAncestor
		}
		if ext = chain.GetHeader(ext.ParentHash, ext.Number.Uint64()-1); ext == nil {
			return false, errForkNoAncestor
		}
	}
	if len(curBranch) == 0 || len(extBranch) == 0 {
		// One head is the other's ancestor: not a fork.
		return false, nil
	}
	// The branches are head-first; the first contested height is each
	// branch's last element, its successor the one before.
	a := curBranch[len(curBranch)-1]
	b := extBranch[len(extBranch)-1]
	var wa, wb uint64
	if len(curBranch) > 1 {
		wa = curBranch[len(curBranch)-2].Endorsed
	}
	if len(extBranch) > 1 {
		wb = extBranch[len(extBranch)-2].Endorsed
	}
	if wa != wb {
		return wb > wa, nil
	}
	// The tie term (the project's ruling of 16:4x UK on the design seat's
	// grinding finding 1e840598): the lower LOT VALUE of the sealing unit
	// at the contested height, x(K_b, j, h) over the key's units, which
	// every node computes from the seed, the key, the unit index and the
	// height and no sealer can grind; a bonded block beats an open-seat
	// block; two open-seat blocks tie on the lower seal hash; a reader
	// without the ancestor's state (the header chain's) ties on the seal
	// hash too, stated: the body import, which moves the canonical head,
	// always has it.
	ta := e.tieValue(chain, cur, a)
	tb := e.tieValue(chain, cur, b)
	if ta.bonded != tb.bonded {
		return tb.bonded, nil
	}
	if ta.bonded && ta.lot != nil && tb.lot != nil && ta.lot.Cmp(tb.lot) != 0 {
		return tb.lot.Cmp(ta.lot) < 0, nil
	}
	if e.vectorMenu() != nil {
		// Under the vector form the tie is the registered draw value
		// x(h) of the block's SEALER (VECTORWORK.md revision 9, section
		// 3; the claims gate's C-28 row 35: the seal hash is a term a
		// sealer can grind by its nonce, the draw value is fixed by the
		// seed, the sealer's key and the height before any search). For
		// an open-seat block the key is the coinbase's; the seed is the
		// epoch table's at the ancestor when the chain has one, else the
		// common ancestor's hash (fixed before the race either way).
		xa, xb := e.drawValue(chain, cur, a), e.drawValue(chain, cur, b)
		if c := xa.Cmp(xb); c != 0 {
			return xb.Cmp(xa) < 0, nil
		}
		// Equal draw values (one sealer, two blocks): the lower block
		// hash, which the sealer already paid the seal for.
		ha, hb := a.Hash(), b.Hash()
		return bytes.Compare(hb[:], ha[:]) < 0, nil
	}
	ha, hb := e.SealHash(a), e.SealHash(b)
	return bytes.Compare(hb[:], ha[:]) < 0, nil
}

// drawValue is x(h) for a block's sealer under the vector form: the lot of
// the sealer's key (the bond key when bonded, else the coinbase's keccak)
// at the block's height from the epoch seed at the ancestor, else from the
// ancestor's hash.
func (e *Engine) drawValue(chain consensus.ChainHeaderReader, ancestor, block *types.Header) *big.Int {
	key := crypto.Keccak256Hash(block.Coinbase[:])
	if block.BondKey != nil {
		key = *block.BondKey
	}
	seed := ancestor.Hash()
	if t := e.tieValue(chain, ancestor, block); t.bonded && t.lot != nil {
		return t.lot
	}
	x := Lot(seed, key, 0, block.Number.Uint64())
	return new(big.Int).SetBytes(x[:])
}

// tieBlock is a contested block's tie value: whether a bonded key sealed
// it and, with the ancestor's state at hand, the least lot over its units.
type tieBlock struct {
	bonded bool
	lot    *big.Int
}

// tieValue reads a contested block's form and lot at the common ancestor's
// state (the parent state of the contested height). Without state the lot
// is nil and the form is read from the header alone (a bond key below the
// liveness round, which the open seat's block also carries, so without
// state both read bonded and the seal hash decides).
func (e *Engine) tieValue(chain consensus.ChainHeaderReader, ancestor, block *types.Header) tieBlock {
	s := e.config.Parts.Sortition
	if s == nil || block.BondKey == nil || block.Round >= s.UnbondedRound {
		return tieBlock{}
	}
	sr, ok := chain.(stateAtReader)
	if !ok || !s.OpenSeat {
		return tieBlock{bonded: true}
	}
	st, err := sr.StateAt(ancestor.Root)
	if err != nil {
		return tieBlock{bonded: true}
	}
	height := block.Number.Uint64()
	d, err := e.drawAt(st, *block.BondKey, height)
	if err != nil || d.open || d.units == 0 {
		return tieBlock{}
	}
	return tieBlock{bonded: true, lot: minLot(d.table.seed, *block.BondKey, d.units, height)}
}

// LotRankAt is the sealer policy's rank (PA-52, the project's order of 20:2x UK,
// 11 October 2026; no consensus change): at height h and round r, the keys
// of the epoch's table at the parent's state that are ELIGIBLE at or
// below r (EligibleRound over their units; the open seat excluded, since
// it holds no unit's lot) ordered by their least lot x(K_b, j*, h)
// ascending, and key's rank among them (0 the leader; -1 when key is not
// eligible at r) with the count; deterministic on every node from the
// table and the seed. The sealer (the protocol team's prepareSortition) starts
// its search at the round's start plus rank x stagger, so the
// contested-sibling class of two round-0 keys both sealing falls by about
// exp(-stagger / target) per rank and a leader offline costs the height
// the stagger, never a block.
func (e *Engine) LotRankAt(st *state.StateDB, key common.Hash, height, round uint64) (rank, eligible int, err error) {
	s := e.config.Parts.Sortition
	a := e.config.Parts.Authority
	if s == nil || a == nil || !s.OpenSeat {
		return -1, 0, errLotRankOff
	}
	t, err := e.tableAt(st, s.EpochAt(height))
	if err != nil {
		return -1, 0, err
	}
	type ranked struct {
		key common.Hash
		lot *big.Int
	}
	var list []ranked
	for _, k := range igneumreg.Keys(st, a.Registry) {
		units := e.unitsAt(st, t, k)
		if units == 0 || igneumreg.Marked(st, a.Registry, k) {
			continue
		}
		if s.EligibleRound(t.seed, k, units, height) > round {
			continue
		}
		list = append(list, ranked{key: k, lot: minLot(t.seed, k, units, height)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].lot.Cmp(list[j].lot) < 0 })
	rank = -1
	for i, r := range list {
		if r.key == key {
			rank = i
			break
		}
	}
	return rank, len(list), nil
}

// minLot is the SEALING UNIT's lot x(K_b, j, h): the seal carries no unit
// index, and the draw defines the unit that sealed as the key's least-lot
// unit at the height (EligibleRound reads the key's round from the least
// lot over its units), so the sealing unit's lot is the least over the
// key's units, one value per block fixed by the seed, the key, its unit
// count in the table and the height; a key with more units has a lower
// minimum at every tie exactly as it has an earlier round at every draw
// (its draw share, not an edge beyond it), and a sealer naming any other
// unit could only hand itself a worse lot.
func minLot(seed, key common.Hash, units, height uint64) *big.Int {
	var best *big.Int
	for j := uint64(0); j < units; j++ {
		x := Lot(seed, key, j, height)
		xi := new(big.Int).SetBytes(x[:])
		if best == nil || xi.Cmp(best) < 0 {
			best = xi
		}
	}
	return best
}

// ForkTieBreak is the hook core's fork choice calls at an equal-weight,
// equal-height tie (core/forkchoice.go); it is PreferBranch with the
// chain the fork choice holds.
func (e *Engine) ForkTieBreak(chain consensus.ChainHeaderReader, current, extern *types.Header) (bool, error) {
	return e.PreferBranch(chain, current, extern)
}

// ForkLongerWins is the hook core's fork choice calls at an equal-weight,
// UNEQUAL-height tie (F-44 part 3): true when the length order is in
// force at the longer head's height, so the branch with more blocks wins
// and the lot term decides only at equal weight and equal length; false
// keeps geth's order (the shorter head). Read at the longer head's
// height on every node alike.
func (e *Engine) ForkLongerWins(current, extern *types.Header) bool {
	h := current.Number.Uint64()
	if x := extern.Number.Uint64(); x > h {
		h = x
	}
	return e.forkLengthOn(h)
}

// contestedOf is the first contested height's block on a branch (for the
// tests and the record).
func contestedOf(branch []*types.Header) common.Hash {
	if len(branch) == 0 {
		return common.Hash{}
	}
	return branch[len(branch)-1].Hash()
}

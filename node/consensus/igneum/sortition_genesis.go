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
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/ethereum/go-ethereum/crypto"
	"lukechampine.com/blake3"
)

// The GENESIS FORM of the sortition-gated rule (GUARD-C-CLOSURE.md section
// 4, route 2: the rule set live from block one; LAUNCH-GUARDS.md 2.2 and
// 2.4), PROTOTYPE on the lab build, nothing adopted:
//
//   - F-32: the units table and seed of each lottery epoch are written into
//     state by consensus, in Finalize of the last block of epoch e for epoch
//     e + 2, under the registry account, so a fresh node in the default
//     pruning mode replays the draw from state it holds and never from a
//     pruned snapshot. Epochs 0 and 1 have no epoch e - 2 to write them:
//     their table is computed live from the parent's state (the genesis
//     state on a launch chain, which holds no bond; the lab's first bonds
//     enter the table at epoch 2) with a seed over the epoch index alone.
//     The seed for epoch e + 2 covers the SeedBlocks canonical hashes ending
//     at the PARENT of the writing block (the block's own hash is unknown in
//     its Finalize), stated as the shift from the draft's "last K hashes of
//     the epoch".
//   - The unit: u = max(B_total, B_floor) / 2^16, B_floor = BRefBps x
//     max(Issued(h), SRef) / 10000, written to the registry's floor slot
//     every block; a key's units n_b = min(floor(B_b / u), 16,384) (the
//     quarter cap in units).
//   - The open seat: the 65,536 - n_total empty units draw as one lot
//     x_open = Lot(seed, zero key, 65,536, h) under theta_open(r) = 2^256 x
//     min(1, E x 2^r x (65,536 - n_total) / 65,536). When it is eligible at
//     a round, any signer whose key holds no units seals at that round by
//     plain proof of work (unkeyed, a free coinbase), the unbonded round's
//     rule at that round. Round 17 stays the liveness floor.
//   - The keyed seal: a drawn bonded key seals with the keyed document (its
//     shares in the block, the owner's secret inside the work); an open-seat
//     or round-17 block is unkeyed. The seal check reads the form from the
//     shares' presence; the stateful rule requires the right form.

// Storage of the table under the registry account.
var (
	tableDomain = []byte("IgneumHash/R0A/table")
	unitsDomain = []byte("IgneumHash/R0A/units")
	// NotionalUnits is the draw's unit count, 2^16.
	NotionalUnits uint64 = 1 << 16
	// MaxUnitsPerKey is the quarter cap in units.
	MaxUnitsPerKey uint64 = 1 << 14
	// OpenSeatKey and OpenSeatUnit are the open seat's lot inputs.
	OpenSeatKey  = common.Hash{}
	OpenSeatUnit = NotionalUnits
)

var (
	errTableMissing = errors.New("igneum sortition: the epoch's units table is not in state (consensus fault)")
	errKeyedSealReq = errors.New("igneum sortition: a drawn bonded key must seal with the keyed document (shares missing)")
	errUnkeyedReq   = errors.New("igneum sortition: an open-seat or unbonded block must not carry shares")
	errOpenNotDrawn = errors.New("igneum sortition: key holds no units and the open seat is not eligible at this round")
	errOpenCoinbase = errors.New("igneum sortition: an open-seat block pays whoever it names (no bond account to bind)")
)

// epochTable is one epoch's draw inputs as the chain carries them.
type epochTable struct {
	epoch  uint64
	seed   common.Hash
	u      *big.Int
	nTotal uint64
	live   bool // computed from the parent state (epochs 0 and 1), not read
}

func tableBase(epoch uint64) common.Hash {
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], epoch)
	return crypto.Keccak256Hash(tableDomain, le[:])
}

func tableSlot(epoch uint64, i int64) common.Hash {
	b := tableBase(epoch).Big()
	return common.BigToHash(b.Add(b, big.NewInt(i)))
}

// unitsSlot is a key's units in an epoch's table.
func unitsSlot(epoch uint64, key common.Hash) common.Hash {
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], epoch)
	return crypto.Keccak256Hash(unitsDomain, le[:], key[:])
}

// genesisSeed is the seed of epochs 0 and 1 (no epoch e - 2 exists).
func genesisSeed(epoch uint64) common.Hash {
	hs := blake3.New(32, nil)
	hs.Write(sortitionSeedDomain)
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], epoch)
	hs.Write(le[:])
	var out common.Hash
	hs.Sum(out[:0])
	return out
}

// unitPrice is u = max(total, floor) / 2^16.
func unitPrice(total, floor *big.Int) *big.Int {
	m := total
	if floor.Cmp(m) > 0 {
		m = floor
	}
	return new(big.Int).Rsh(m, 16)
}

// unitsOf is n_b = min(floor(bond / u), the quarter cap).
func unitsOf(bond, u *big.Int) uint64 {
	if u.Sign() == 0 || bond.Sign() <= 0 {
		return 0
	}
	n := new(big.Int).Div(bond, u)
	if !n.IsUint64() || n.Uint64() > MaxUnitsPerKey {
		return MaxUnitsPerKey
	}
	return n.Uint64()
}

// liveTable computes an epoch's table from a state (epochs 0 and 1).
func (e *Engine) liveTable(st *state.StateDB, epoch uint64) *epochTable {
	reg := e.config.Parts.Authority.Registry
	u := unitPrice(igneumreg.Total(st, reg), igneumreg.Floor(st, reg))
	t := &epochTable{epoch: epoch, seed: genesisSeed(epoch), u: u, live: true}
	for _, k := range igneumreg.Keys(st, reg) {
		t.nTotal += unitsOf(igneumreg.BondOf(st, reg, k).Amount, u)
	}
	return t
}

// tableAt reads an epoch's table from a state: the written one, or the
// live one for epochs 0 and 1.
func (e *Engine) tableAt(st *state.StateDB, epoch uint64) (*epochTable, error) {
	reg := e.config.Parts.Authority.Registry
	if st.GetState(reg, tableSlot(epoch, 3)) == (common.Hash{}) {
		if epoch < 2 {
			return e.liveTable(st, epoch), nil
		}
		return nil, fmt.Errorf("%w: epoch %d", errTableMissing, epoch)
	}
	return &epochTable{
		epoch:  epoch,
		seed:   st.GetState(reg, tableSlot(epoch, 0)),
		u:      st.GetState(reg, tableSlot(epoch, 1)).Big(),
		nTotal: st.GetState(reg, tableSlot(epoch, 2)).Big().Uint64(),
	}, nil
}

// unitsAt is a key's units in an epoch's table at a state.
func (e *Engine) unitsAt(st *state.StateDB, t *epochTable, key common.Hash) uint64 {
	reg := e.config.Parts.Authority.Registry
	if t.live {
		return unitsOf(igneumreg.BondOf(st, reg, key).Amount, t.u)
	}
	return st.GetState(reg, unitsSlot(t.epoch, key)).Big().Uint64()
}

// writeFloor writes B_floor for the block into the registry's floor slot
// (every block; the cap and the unit read it).
func (e *Engine) writeFloor(header *types.Header, st *state.StateDB) {
	a := e.config.Parts.Authority
	if a == nil || a.BRefBps == 0 {
		return
	}
	issued := e.config.Parts.Issuance.Issued(header.Number.Uint64())
	if a.SRef != nil && a.SRef.Cmp(issued) > 0 {
		issued = a.SRef
	}
	floor := new(big.Int).Mul(issued, new(big.Int).SetUint64(a.BRefBps))
	floor.Div(floor, big.NewInt(10000))
	st.SetState(a.Registry, igneumreg.SlotFloor, common.BigToHash(floor))
}

// writeTable writes the table of epoch e + 2 in Finalize of the last block
// of epoch e (F-32), from the block's state and the seed blocks ending at
// its parent.
func (e *Engine) writeTable(chain consensus.ChainHeaderReader, header *types.Header, st *state.StateDB) error {
	s := e.config.Parts.Sortition
	a := e.config.Parts.Authority
	if s == nil || !s.OpenSeat || a == nil {
		return nil
	}
	height := header.Number.Uint64()
	_, epochBlocks := s.regimeAt(height)
	if (height+1)%epochBlocks != 0 {
		return nil
	}
	epoch := height/epochBlocks + 2
	reg := a.Registry
	u := unitPrice(igneumreg.Total(st, reg), igneumreg.Floor(st, reg))
	var nTotal uint64
	for _, k := range igneumreg.Keys(st, reg) {
		n := unitsOf(igneumreg.BondOf(st, reg, k).Amount, u)
		st.SetState(reg, unitsSlot(epoch, k), common.BigToHash(new(big.Int).SetUint64(n)))
		nTotal += n
	}
	// The seed: the SeedBlocks hashes ending at the parent.
	hs := blake3.New(32, nil)
	hs.Write(sortitionSeedDomain)
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], epoch)
	hs.Write(le[:])
	var hashes []common.Hash
	h := chain.GetHeader(header.ParentHash, height-1)
	for n := uint64(0); n < s.SeedBlocks && h != nil; n++ {
		hashes = append(hashes, h.Hash())
		if h.Number.Sign() == 0 {
			break
		}
		h = chain.GetHeader(h.ParentHash, h.Number.Uint64()-1)
	}
	if uint64(len(hashes)) < s.SeedBlocks && (len(hashes) == 0 || hashes[len(hashes)-1] != chain.GetHeaderByNumber(0).Hash()) {
		return fmt.Errorf("igneum sortition: seed blocks unknown writing the table of epoch %d", epoch)
	}
	for i := len(hashes) - 1; i >= 0; i-- {
		hs.Write(hashes[i][:])
	}
	var seed common.Hash
	hs.Sum(seed[:0])
	st.SetState(reg, tableSlot(epoch, 0), seed)
	st.SetState(reg, tableSlot(epoch, 1), common.BigToHash(u))
	st.SetState(reg, tableSlot(epoch, 2), common.BigToHash(new(big.Int).SetUint64(nTotal)))
	st.SetState(reg, tableSlot(epoch, 3), common.BigToHash(big.NewInt(1)))
	e.log.Info("Units table written (the genesis form, F-32)", "forEpoch", epoch, "atHeight", height, "u", u, "units", nTotal, "openShare", float64(NotionalUnits-nTotal)/float64(NotionalUnits))
	return nil
}

// ThetaOpen is the open seat's threshold at a round: 2^256 x min(1, E x
// 2^r x (2^16 - nTotal) / 2^16).
func ThetaOpen(expected, round, nTotal uint64) *big.Int {
	if nTotal >= NotionalUnits {
		return new(big.Int)
	}
	n := new(big.Int).Lsh(new(big.Int).SetUint64(expected), uint(round))
	n.Mul(n, new(big.Int).SetUint64(NotionalUnits-nTotal))
	if n.Cmp(new(big.Int).SetUint64(NotionalUnits)) >= 0 {
		return new(big.Int).Lsh(big.NewInt(1), 256)
	}
	return n.Lsh(n, 240)
}

// OpenSeatRound is the smallest round the open seat is eligible at for a
// height under a seed and a table; MaxRound + 1 when none.
func (s *SortitionParams) OpenSeatRound(seed common.Hash, nTotal, height uint64) uint64 {
	lot := Lot(seed, OpenSeatKey, OpenSeatUnit, height)
	x := new(big.Int).SetBytes(lot[:])
	for r := uint64(0); r <= s.MaxRound; r++ {
		if x.Cmp(ThetaOpen(s.Expected, r, nTotal)) < 0 {
			return r
		}
	}
	return s.MaxRound + 1
}

// draw is a key's standing at a height under the genesis form: its units
// in the epoch's table, the round it is eligible at (bonded: its lots;
// without units: the open seat's round), and the table.
type draw struct {
	table *epochTable
	units uint64
	round uint64 // eligible round, MaxRound + 1 when none below the liveness round
	open  bool   // the open seat's draw (no units)
}

func (e *Engine) drawAt(st *state.StateDB, key common.Hash, height uint64) (*draw, error) {
	// The equivocation mark (registry version 4; penalties.go): a marked
	// key draws nothing, even from a table written before the mark that
	// still carries its units (drawWith, authoritymemo.go).
	return e.drawWith(&ruleReader{e: e, st: st}, key, height)
}

// keyedSeals marks the templates this node assembled that need the keyed
// seal (a drawn bonded key's own block), by seal hash, so the CPU search
// and the work package know the form without state (consensus.go marks,
// sealer.go reads).
type keyedSeals struct {
	mu    sync.Mutex
	marks map[common.Hash]bool
}

func (k *keyedSeals) set(h common.Hash, keyed bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.marks == nil {
		k.marks = make(map[common.Hash]bool)
	}
	if len(k.marks) > 4096 {
		k.marks = make(map[common.Hash]bool)
	}
	k.marks[h] = keyed
}

func (k *keyedSeals) get(h common.Hash) (keyed, known bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	keyed, known = k.marks[h]
	return
}

// docKeyedRounds is S of the document a header's block is hashed with: the
// ONE reader of the keyed state for the template's S, the genesis form's
// keyed question, the work package, the verifier's refusal of shares and
// igneum_sortition's view (F-433, 17 October 2026: under the vector form
// the engine's algorithm is the menu, which carries no keyed entry point,
// so every reader of pow.KeyedRoundsAt(e.algo, h) read 0 at every height
// of the public test network; its keyed genesis row and its three bonds
// were never consulted and every block was unkeyed by construction).
// Under the vector form S is the header's coordinate's document IN FORCE
// AT ITS HEIGHT (vectorAlgoFor: never the base document or a predecessor,
// F-245's lesson); a header without a coordinate tag reads 0 there. Under
// the schedule form the row in force answers, and a chain of one document
// that document.
func (e *Engine) docKeyedRounds(header *types.Header) uint64 {
	if a, vec := e.vectorAlgoFor(header); vec {
		if a == nil {
			return 0
		}
		return pow.KeyedRoundsAt(a, header.Number.Uint64())
	}
	return pow.KeyedRoundsAt(e.algo, header.Number.Uint64())
}

// ownDocKeyedRounds is docKeyedRounds for a block this node would seal at
// a height: its own coordinate's document under the vector form.
func (e *Engine) ownDocKeyedRounds(height uint64) uint64 {
	if m := e.vectorMenu(); m != nil {
		return pow.KeyedRoundsAt(m.ByCoordinateAt(e.Coordinate(), height), height)
	}
	return pow.KeyedRoundsAt(e.algo, height)
}

// keyedRoundsFor is S for a template this node assembled: under the
// genesis form only a drawn bonded key's seal is keyed; otherwise the
// document's S for every block.
func (e *Engine) keyedRoundsFor(header *types.Header) uint64 {
	s := e.config.Parts.Sortition
	docS := e.docKeyedRounds(header)
	if docS == 0 || s == nil || !s.OpenSeat {
		return docS
	}
	if keyed, known := e.keyed.get(e.SealHash(header)); known && !keyed {
		return 0
	}
	return docS
}

// sealIsKeyed reports whether a block this node assembles seals with the
// keyed document under the genesis form: a drawn bonded key below the
// liveness round, on a keyed document.
func (e *Engine) sealIsKeyed(header *types.Header, st *state.StateDB) bool {
	s := e.config.Parts.Sortition
	if s == nil || !s.OpenSeat || !s.Active(header.Number.Uint64()) || header.BondKey == nil {
		return false
	}
	if e.docKeyedRounds(header) == 0 || header.Round >= s.UnbondedRound {
		return false
	}
	d, err := e.drawAt(st, *header.BondKey, header.Number.Uint64())
	return err == nil && d.units > 0
}

// verifySortitionGenesis is the stateful rule of the genesis form at the
// parent's state: the signature (a bonded key's registered signer; the key
// itself for the open seat and the liveness round), then by the key's
// units in the epoch's table: a bonded key must be eligible at or below
// the block's round, still bonded, pay its account (or its signed list)
// and, on a keyed document, seal with its shares; a key without units
// takes the open seat, which must be eligible at or below the round, pays
// whom it names, and seals unkeyed; the liveness round admits any signer,
// unkeyed, a free coinbase.
func (e *Engine) verifySortitionGenesis(header *types.Header, st *state.StateDB, sealed bool) error {
	s := e.config.Parts.Sortition
	height := header.Number.Uint64()
	key := *header.BondKey
	docS := e.docKeyedRounds(header)
	if !sealed {
		// An unsealed template of this node's own: the seal's fields
		// (nonce, mix digest, shares) do not exist yet; the form it owes is
		// decided by the keyed mark at assembly and checked on import.
		docS = 0
	}
	h := e.SigningHash(header)
	// The parent's facts from the memo when it holds them, else the state
	// (authoritymemo.go, F-39); st may be nil on the memo-first header
	// stage, which then reads errMemoMiss and opens the state.
	r := e.rulesFor(st, header.ParentHash)
	marked, err := r.marked(key)
	if err != nil {
		return err
	}
	if marked {
		// The deterministic half of one block per key per height
		// (penalties.go): a key marked by an equivocation seals nothing
		// from the next block, at any round.
		return errKeyMarked
	}
	if header.Round >= s.UnbondedRound {
		if !ed25519.Verify(ed25519.PublicKey(key[:]), h[:], header.Sig) {
			return errSortitionBadSig
		}
		if sealed && len(header.Shares) != 0 {
			return errUnkeyedReq
		}
		if sealed && e.ClockOn(height) {
			// A liveness block carries the chain's difficulty (clock.go).
			return e.verifyClockWith(header, r, false)
		}
		return nil
	}
	d, err := e.drawWith(r, key, height)
	if err != nil {
		return err
	}
	// The round clock amendment (clock.go): the header's difficulty by
	// the block's form, on sealed blocks (a template's difficulty is the
	// sealer's to fill from ExpectedDifficulty).
	if sealed && e.ClockOn(height) {
		if err := e.verifyClockWith(header, r, !d.open); err != nil {
			return err
		}
	}
	if d.open {
		if !ed25519.Verify(ed25519.PublicKey(key[:]), h[:], header.Sig) {
			return errSortitionBadSig
		}
		if d.round > header.Round {
			return fmt.Errorf("%w: open seat eligible from round %d, sealed at %d (%d units in the table)", errOpenNotDrawn, d.round, header.Round, d.table.nTotal)
		}
		if sealed && len(header.Shares) != 0 {
			return errUnkeyedReq
		}
		return nil
	}
	// A bonded key in the table.
	b, err := r.bondOf(key)
	if err != nil {
		return err
	}
	if b.Amount == nil || b.Amount.Sign() <= 0 {
		return errBondKeyUnbonded
	}
	signer := b.Signer
	if signer == (common.Hash{}) {
		signer = key
	}
	if !ed25519.Verify(ed25519.PublicKey(signer[:]), h[:], header.Sig) {
		return errSortitionBadSig
	}
	if d.round > header.Round {
		return fmt.Errorf("%w: eligible from round %d, sealed at %d (%d units)", errSortitionNotDrawn, d.round, header.Round, d.units)
	}
	if header.WithdrawalsHash == nil && header.Coinbase != b.Account {
		return fmt.Errorf("%w: coinbase %s, bond account %s", errBondAccount, header.Coinbase.Hex(), b.Account.Hex())
	}
	if docS > 0 && uint64(len(header.Shares)) != docS*pow.ShareBytes {
		return fmt.Errorf("%w: %d bytes for S = %d", errKeyedSealReq, len(header.Shares), docS)
	}
	if docS == 0 && len(header.Shares) != 0 {
		return errUnkeyedReq
	}
	return nil
}

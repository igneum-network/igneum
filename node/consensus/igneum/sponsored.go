// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package igneum

import (
	"bytes"
	"fmt"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/holiman/uint256"
)

// The sponsored lane (SPONSORED-LANE-NODE-FORM.md, the form of record
// 7424589c3 on economics' mechanism page 7ebf22f4d; 18 October 2026): a
// coordinate whose work a sponsor pays for through the lane's escrow, its
// pay share zero at every height and the lane outside the breadth (the
// registration's shape, pow/vector), the chain's own subsidy untouched.
//
// This file is the first of the three commits: the document's mark, the
// shape's refusals and the escrow's derivation. The lock and the root
// (section 2 and 3) and the window's settlement and the refunds (sections
// 4 and 5) are the second and third.

// sponsoredOn says whether the sponsored lane's rule is in force at height
// on this chain: from the chain id's gate (params.IgneumChainSponsoredFrom,
// handed to the engine by SetSponsoredFrom; the one source with the
// registry's admission), never without one (ZERO IS OFF), so a node on the
// new build and one on the old build write the same state root below the
// height. The gate is not a field of the rule forks (F-703): a chain
// listed in the forks table with the other rules at zero loses the forms a
// nil entry gives it.
func (e *Engine) sponsoredOn(height uint64) bool {
	e.lock.Lock()
	from := e.sponsoredFrom
	e.lock.Unlock()
	return from > 0 && height >= from
}

// SponsoredGateRefusal is the registry's gate on the sponsored mark (the
// chair's ruling e881c07ca): a document that marks any coordinate sponsored
// is admitted only at a landing at or past the chain's gate
// (params.IgneumChainSponsoredFrom; 0: never), so no sponsored lane exists
// before every node of record carries its code; nil admits. Shared by the
// landing (LandRegistry) and the signer's check (igneum-regcheck with
// --rpc, the chain id from eth_chainId).
func SponsoredGateRefusal(chainID uint64, landing uint64, given *ctypes.IgneumVector) error {
	if given == nil {
		return nil
	}
	for i, c := range given.Coordinates {
		if !c.Sponsored {
			continue
		}
		from := params.IgneumSponsoredFromForChainID(chainID)
		if from == 0 {
			return fmt.Errorf("igneum registry: coordinate %d (%s) is sponsored and chain %d admits no sponsored lane", i, c.Name, chainID)
		}
		if landing < from {
			return fmt.Errorf("igneum registry: coordinate %d (%s) is sponsored and the landing %d is under the chain's sponsored gate %d", i, c.Name, landing, from)
		}
	}
	return nil
}

// sponsoredEscrowTag is the escrow derivation's domain tag.
const sponsoredEscrowTag = "igneum sponsored lane escrow"

// SponsoredEscrow is a sponsored lane's escrow address: derived from the
// lane's IDENTITY alone, the chain id and the coordinate index (security
// A's cell on b2bc73b68: a derivation over the document's params would
// move the escrow at a document change and strand an open lock under the
// old address), so it is stable across the lane's forms. No key exists for
// it; it is credited and debited by the state transition alone, as the
// fund's address is.
func SponsoredEscrow(chainID uint64, c uint8) common.Address {
	var buf [len(sponsoredEscrowTag) + 8 + 1]byte
	copy(buf[:], sponsoredEscrowTag)
	binary.BigEndian.PutUint64(buf[len(sponsoredEscrowTag):], chainID)
	buf[len(sponsoredEscrowTag)+8] = c
	return common.BytesToAddress(crypto.Keccak256(buf[:])[12:])
}

// THE LOCK AND THE ROOT (the form page's sections 2 and 3; the second
// commit). A sponsor's lock is a transaction to the lane's escrow carrying
// value and an 82-byte record; a window's input root a transaction to the
// escrow with no value and a 42-byte record. The escrow carries no code
// (the fund's form), so every such transaction moves its value in the
// EVM; Finalize of the same block reads the block's transactions to the
// sponsored escrows and ADMITS or REFUSES each by the rules below: a
// refused one's value is returned to its sender in the same block by the
// state transition (gas spent, nothing recorded, the reason on the node's
// log line), an admitted one's record is written under the escrow's own
// storage, which every node computes alike.

// sponsoredLockTag and sponsoredRootTag are the records' first bytes.
const (
	sponsoredLockTag  = 0x01
	sponsoredRootTag  = 0x02
	sponsoredLockLen  = 1 + 1 + 8 + 8 + 32 + 32
	sponsoredRootLen  = 1 + 1 + 8 + 32
	sponsoredLockMax  = 4 // lock records a lane holds at once (non-overlapping spans)
	// sponsoredWindowMax bounds the window fields before any arithmetic
	// (F-673, the security review's cell): a window above it is refused, so no
	// product of a window and the chain's window length can wrap (2^40
	// windows of 64 blocks at 12 s is about 26 million years).
	sponsoredWindowMax = uint64(1) << 40
)

// SponsoredLock is a lock's record as the data carries it.
type SponsoredLock struct {
	Coord           uint8
	FirstWindow     uint64
	LastWindow      uint64
	AmountPerWindow *big.Int
	Bond            *big.Int
}

// SponsoredRoot is a root's record as the data carries it.
type SponsoredRoot struct {
	Coord  uint8
	Window uint64
	Root   common.Hash
}

// ParseSponsoredLock reads the 82-byte lock record (big-endian).
func ParseSponsoredLock(data []byte) (SponsoredLock, bool) {
	if len(data) != sponsoredLockLen || data[0] != sponsoredLockTag {
		return SponsoredLock{}, false
	}
	return SponsoredLock{
		Coord:           data[1],
		FirstWindow:     binary.BigEndian.Uint64(data[2:10]),
		LastWindow:      binary.BigEndian.Uint64(data[10:18]),
		AmountPerWindow: new(big.Int).SetBytes(data[18:50]),
		Bond:            new(big.Int).SetBytes(data[50:82]),
	}, true
}

// ParseSponsoredRoot reads the 42-byte root record (big-endian).
func ParseSponsoredRoot(data []byte) (SponsoredRoot, bool) {
	if len(data) != sponsoredRootLen || data[0] != sponsoredRootTag {
		return SponsoredRoot{}, false
	}
	return SponsoredRoot{Coord: data[1], Window: binary.BigEndian.Uint64(data[2:10]), Root: common.BytesToHash(data[10:42])}, true
}

// The escrow's storage (consensus state): the held balance, the lock
// records (sponsoredLockMax slots of four words each) and the roots by
// window.
func sponsoredSlot(name string, args ...uint64) common.Hash {
	b := []byte(name)
	for _, a := range args {
		var w [8]byte
		binary.BigEndian.PutUint64(w[:], a)
		b = append(b, w[:]...)
	}
	return crypto.Keccak256Hash(b)
}

// sponsoredLockRecord is a lock as stored: the sponsor and the span in
// the first word, the amount, the bond and the windows paid so far.
type sponsoredLockRecord struct {
	Sponsor         common.Address
	FirstWindow     uint64
	LastWindow      uint64
	AmountPerWindow *big.Int
	Bond            *big.Int
	WindowsPaid     uint64
	RefundAt        uint64 // the refund's height, fixed at the admission (the security review's cell: a notice change inside the span never strands the lock)
	Refunded        bool
}

func (r sponsoredLockRecord) live() bool { return r.Sponsor != (common.Address{}) && !r.Refunded }

func readSponsoredLock(st *state.StateDB, escrow common.Address, k uint64) sponsoredLockRecord {
	w0 := st.GetState(escrow, sponsoredSlot("lock0", k)).Bytes()
	var r sponsoredLockRecord
	r.Sponsor = common.BytesToAddress(w0[:20])
	r.FirstWindow = binary.BigEndian.Uint64(w0[20:28])
	r.LastWindow = uint64(binary.BigEndian.Uint32(w0[28:32])) // the low 4 bytes of the last window (the high 4 in the fourth word)
	r.AmountPerWindow = st.GetState(escrow, sponsoredSlot("lock1", k)).Big()
	r.Bond = st.GetState(escrow, sponsoredSlot("lock2", k)).Big()
	w3 := st.GetState(escrow, sponsoredSlot("lock3", k)).Bytes()
	r.LastWindow |= binary.BigEndian.Uint64(w3[0:8]) << 32 // the high bytes
	r.WindowsPaid = binary.BigEndian.Uint64(w3[8:16])
	r.RefundAt = binary.BigEndian.Uint64(w3[16:24])
	r.Refunded = w3[31] == 1
	return r
}

func writeSponsoredLock(st *state.StateDB, escrow common.Address, k uint64, r sponsoredLockRecord) {
	var w0 [32]byte
	copy(w0[:20], r.Sponsor[:])
	binary.BigEndian.PutUint64(w0[20:28], r.FirstWindow)
	binary.BigEndian.PutUint32(w0[28:32], uint32(r.LastWindow))
	st.SetState(escrow, sponsoredSlot("lock0", k), common.BytesToHash(w0[:]))
	st.SetState(escrow, sponsoredSlot("lock1", k), common.BigToHash(r.AmountPerWindow))
	st.SetState(escrow, sponsoredSlot("lock2", k), common.BigToHash(r.Bond))
	var w3 [32]byte
	binary.BigEndian.PutUint64(w3[0:8], r.LastWindow>>32)
	binary.BigEndian.PutUint64(w3[8:16], r.WindowsPaid)
	binary.BigEndian.PutUint64(w3[16:24], r.RefundAt)
	if r.Refunded {
		w3[31] = 1
	}
	st.SetState(escrow, sponsoredSlot("lock3", k), common.BytesToHash(w3[:]))
}

func readSponsoredHeld(st *state.StateDB, escrow common.Address) *big.Int {
	return st.GetState(escrow, sponsoredSlot("held")).Big()
}

func writeSponsoredHeld(st *state.StateDB, escrow common.Address, v *big.Int) {
	st.SetState(escrow, sponsoredSlot("held"), common.BigToHash(v))
}

// SponsoredRootAt is the window's admitted root (zero: none).
func SponsoredRootAt(st *state.StateDB, escrow common.Address, window uint64) common.Hash {
	return st.GetState(escrow, sponsoredSlot("root", window))
}

// sponsoredFacts are the chain's facts the admission reads (the menu's and
// the schedule's), gathered by Finalize so the rules are a pure function
// for the test rows.
type sponsoredFacts struct {
	Height       uint64 // the block's height (the landing)
	Window       uint64 // the chain's pay window in blocks
	Notice       uint64 // the registration's noticeBlocks
	Active       func(c uint8, height uint64) bool
	RetiredFrom  func(c uint8) uint64
	FundPerBlock func(height uint64) *big.Int // the fund's share of a block's subsidy at the height
}

func (f sponsoredFacts) window() uint64 {
	if f.Window == 0 {
		return 64
	}
	return f.Window
}

// admitSponsoredLock applies section 2's rules to a lock carried by a
// transaction of value from sender to escrow at f.Height; "" admits and
// writes the record (the held balance raised), else the refusal's words
// (the value is the caller's to return).
func admitSponsoredLock(st *state.StateDB, escrow common.Address, sender common.Address, value *big.Int, l SponsoredLock, f sponsoredFacts) string {
	W := f.window()
	if l.FirstWindow > sponsoredWindowMax || l.LastWindow > sponsoredWindowMax {
		return "the window is past the chain's horizon"
	}
	if l.FirstWindow > l.LastWindow {
		return "the span's first window is after its last"
	}
	windows := new(big.Int).SetUint64(l.LastWindow - l.FirstWindow + 1)
	want := new(big.Int).Mul(l.AmountPerWindow, windows)
	want.Add(want, l.Bond)
	if value == nil || value.Cmp(want) != 0 {
		return "the value is not the amount a window times the windows plus the bond"
	}
	if l.AmountPerWindow.Sign() <= 0 {
		return "the amount a window is zero"
	}
	// The bond at or above one window's amount (the design's default; the
	// chair's ruling of 18 October 2026 on economics' cell).
	if l.Bond.Cmp(l.AmountPerWindow) < 0 {
		return "the bond is under one window's amount"
	}
	if l.FirstWindow*W < f.Height+f.Notice {
		return "the span opens before the landing plus the notice"
	}
	if f.Active != nil && !f.Active(l.Coord, l.FirstWindow*W) {
		return "the lane is not active at the span's first window"
	}
	if f.RetiredFrom != nil {
		if r := f.RetiredFrom(l.Coord); r != 0 && r <= l.LastWindow*W+W-1 {
			return "the span runs past the lane's retirement"
		}
	}
	if f.FundPerBlock != nil {
		// The floor (the mechanism's section 6 case 5; F-672, economics'
		// strike): one percent of the window's subsidy, which IS the
		// fund's own share of the window, the fund's share a block times
		// the window's blocks, no further percent (3.048778 IGN a window
		// on the public test network from the 12-second gate, 5.083779
		// before it).
		if fund := f.FundPerBlock(l.FirstWindow * W); fund != nil {
			floor := new(big.Int).Mul(fund, new(big.Int).SetUint64(W))
			if l.AmountPerWindow.Cmp(floor) < 0 {
				return "the amount a window is under the floor (one percent of the window's subsidy, the fund's share)"
			}
		}
	}
	// The value's arrival: the escrow's balance above what it already holds.
	held := readSponsoredHeld(st, escrow)
	free := new(big.Int).Sub(st.GetBalance(escrow).ToBig(), held)
	if free.Cmp(value) < 0 {
		return "the value did not reach the escrow"
	}
	// One sponsor a lane a span: no live record overlapping; a free slot.
	freeSlot := int64(-1)
	for k := uint64(0); k < sponsoredLockMax; k++ {
		r := readSponsoredLock(st, escrow, k)
		if !r.live() {
			if freeSlot < 0 {
				freeSlot = int64(k)
			}
			continue
		}
		if l.FirstWindow <= r.LastWindow && r.FirstWindow <= l.LastWindow {
			return "another lock holds an overlapping span on the lane"
		}
	}
	if freeSlot < 0 {
		return "the lane holds its full count of locks"
	}
	// The refund's height fixed now, under the notice in force at the
	// admission: a later change of the notice never moves it.
	writeSponsoredLock(st, escrow, uint64(freeSlot), sponsoredLockRecord{Sponsor: sender, FirstWindow: l.FirstWindow, LastWindow: l.LastWindow, AmountPerWindow: l.AmountPerWindow, Bond: l.Bond, RefundAt: (l.LastWindow+1)*W + f.Notice})
	writeSponsoredHeld(st, escrow, new(big.Int).Add(held, value))
	return ""
}

// admitSponsoredRoot applies section 3's rules: the sender the sponsor of
// a live lock covering the window, the window at least the notice ahead,
// one root a window (the first admitted stands).
func admitSponsoredRoot(st *state.StateDB, escrow common.Address, sender common.Address, value *big.Int, r SponsoredRoot, f sponsoredFacts) string {
	W := f.window()
	if value != nil && value.Sign() != 0 {
		return "a root carries no value"
	}
	if r.Root == (common.Hash{}) {
		return "the root is zero"
	}
	if r.Window > sponsoredWindowMax {
		return "the window is past the chain's horizon"
	}
	if r.Window*W < f.Height+f.Notice {
		return "the window opens under the notice"
	}
	covered := false
	for k := uint64(0); k < sponsoredLockMax; k++ {
		rec := readSponsoredLock(st, escrow, k)
		if rec.live() && rec.Sponsor == sender && rec.FirstWindow <= r.Window && r.Window <= rec.LastWindow {
			covered = true
			break
		}
	}
	if !covered {
		return "the sender holds no lock covering the window"
	}
	if SponsoredRootAt(st, escrow, r.Window) != (common.Hash{}) {
		return "the window's root is set (one root a window; the first stands)"
	}
	st.SetState(escrow, sponsoredSlot("root", r.Window), r.Root)
	return ""
}

// settleSponsoredTxs is Finalize's read of the block's transactions to the
// sponsored escrows: each lock or root admitted by the rules or refused
// with its value returned to the sender; a transaction to an escrow with
// no readable record is refused the same way (nothing is ever stuck).
func (e *Engine) settleSponsoredTxs(chain consensus.ChainHeaderReader, header *types.Header, st *state.StateDB, txs []*types.Transaction) {
	m := e.vectorMenu()
	if m == nil || len(txs) == 0 || chain == nil || chain.Config() == nil || chain.Config().GetChainID() == nil || header == nil || !e.sponsoredOn(header.Number.Uint64()) {
		return
	}
	chainID := chain.Config().GetChainID().Uint64()
	height := header.Number.Uint64()
	// The sponsored set under the registration in force at THIS height
	// (the review lead's replay condition): a replaying node and a live node
	// read the same escrows for the same block.
	escrows, v := e.sponsoredEscrowsAt(chainID, height)
	if len(escrows) == 0 || v == nil {
		return
	}
	facts := sponsoredFacts{
		Height: height, Window: m.Window(), Notice: m.NoticeBlocks(),
		Active:      func(c uint8, h uint64) bool { return m.ActiveAt(c, h) },
		RetiredFrom: func(c uint8) uint64 { return v.Coordinates[c].RetiredFrom },
		FundPerBlock: func(h uint64) *big.Int {
			if e.config.Parts.Issuance == nil {
				return nil
			}
			_, fund := e.config.Parts.Issuance.Reward(h)
			return fund
		},
	}
	for _, tx := range txs {
		if tx.To() == nil {
			continue
		}
		c, ok := escrows[*tx.To()]
		if !ok {
			continue
		}
		sender, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
		if err != nil {
			continue
		}
		escrow := *tx.To()
		value := tx.Value()
		var why string
		if l, ok := ParseSponsoredLock(tx.Data()); ok {
			if l.Coord != c {
				why = "the record names another coordinate"
			} else {
				why = admitSponsoredLock(st, escrow, sender, value, l, facts)
			}
		} else if r, ok := ParseSponsoredRoot(tx.Data()); ok {
			if r.Coord != c {
				why = "the record names another coordinate"
			} else {
				why = admitSponsoredRoot(st, escrow, sender, value, r, facts)
			}
		} else {
			why = "no readable lock or root record"
		}
		if why == "" {
			e.log.Info("Sponsored lane record admitted", "number", height, "coordinate", c, "escrow", escrow, "tx", tx.Hash(), "value", value)
			continue
		}
		// The refusal: the value back to the sender in this block (the
		// escrow's balance above its held figure, which the value is).
		if value != nil && value.Sign() > 0 {
			free := new(big.Int).Sub(st.GetBalance(escrow).ToBig(), readSponsoredHeld(st, escrow))
			if free.Cmp(value) >= 0 {
				st.SubBalance(escrow, uint256.MustFromBig(value))
				st.AddBalance(sender, uint256.MustFromBig(value))
			}
		}
		e.log.Warn("Sponsored lane record refused (the value returned)", "number", height, "coordinate", c, "tx", tx.Hash(), "why", why)
	}
}

// THE SETTLEMENT AND THE REFUNDS (the form page's sections 4 and 5; the
// third commit), and the two conditions ruled on commit two (the review lead's
// ab8aaff9e): the sponsored set is read from the registration IN FORCE AT
// THE BLOCK'S HEIGHT, never the head's menu over older blocks (a replaying
// node and a live node write one state root), and the registry API serves
// the escrow's records (SponsoredLane).

// registrationAt is the registration in force at a block's height: the
// last landing BELOW the height (a landing's own block is finalized before
// the engine swaps its menu, so its coordinates' escrows exist from the
// block after it), else the base the node started with.
func (e *Engine) registrationAt(height uint64) *ctypes.IgneumVector {
	r := &e.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	var v *ctypes.IgneumVector = r.base
	for _, l := range r.landings {
		if l.Height < height {
			v = l.Doc
		}
	}
	if v == nil {
		if m := e.vectorMenu(); m != nil && len(r.landings) == 0 {
			return m.Registration()
		}
	}
	return v
}

// sponsoredEscrowsAt is the sponsored coordinates' escrows under the
// registration in force at the height.
func (e *Engine) sponsoredEscrowsAt(chainID uint64, height uint64) (map[common.Address]uint8, *ctypes.IgneumVector) {
	v := e.registrationAt(height)
	out := map[common.Address]uint8{}
	if v == nil {
		return out, nil
	}
	for i, c := range v.Coordinates {
		if c.Sponsored {
			out[SponsoredEscrow(chainID, uint8(i))] = uint8(i)
		}
	}
	return out, v
}

// sponsoredPaySnapshot is taken in Finalize BEFORE the chain's pay when a
// sponsored lane holds a live lock covering the block's window: the
// balances of the block's candidate payees (the pay list's addresses and
// the coinbase); sponsoredPaidFrom, AFTER the pay, writes every address
// whose balance rose as PAID for the window (F-677, the security review's strike
// 75gj: the pay list's Amount is the list weight, never the wei; the wei
// is what the chain credited, read here from the state transition itself,
// the same on assembly and on import).
type sponsoredPaySnapshot struct {
	lanes  []uint8
	window uint64
	before map[common.Address]*big.Int
}

func (e *Engine) sponsoredPaySnapshot(chain consensus.ChainHeaderReader, header *types.Header, st *state.StateDB, withdrawals []*types.Withdrawal) *sponsoredPaySnapshot {
	m := e.vectorMenu()
	if m == nil || header == nil || chain == nil || chain.Config() == nil || chain.Config().GetChainID() == nil || !e.sponsoredOn(header.Number.Uint64()) {
		return nil
	}
	height := header.Number.Uint64()
	W := m.Window()
	if W == 0 {
		W = 64
	}
	escrows, v := e.sponsoredEscrowsAt(chain.Config().GetChainID().Uint64(), height)
	if len(escrows) == 0 || v == nil {
		return nil
	}
	w := height / W
	snap := &sponsoredPaySnapshot{window: w, before: map[common.Address]*big.Int{}}
	for escrow, c := range escrows {
		for k := uint64(0); k < sponsoredLockMax; k++ {
			if rec := readSponsoredLock(st, escrow, k); rec.live() && rec.FirstWindow <= w && w <= rec.LastWindow {
				snap.lanes = append(snap.lanes, c)
				break
			}
		}
	}
	if len(snap.lanes) == 0 {
		return nil
	}
	for _, wd := range withdrawals {
		snap.before[wd.Address] = st.GetBalance(wd.Address).ToBig()
	}
	snap.before[header.Coinbase] = st.GetBalance(header.Coinbase).ToBig()
	return snap
}

func (e *Engine) sponsoredPaidFrom(snap *sponsoredPaySnapshot, st *state.StateDB, chainID uint64) {
	if snap == nil {
		return
	}
	for a, b := range snap.before {
		if st.GetBalance(a).ToBig().Cmp(b) <= 0 {
			continue
		}
		for _, c := range snap.lanes {
			escrow := SponsoredEscrow(chainID, c)
			st.SetState(escrow, sponsoredPaidKeySlot(c, snap.window, a), common.BigToHash(big.NewInt(1)))
		}
	}
}

func sponsoredPaidKeySlot(c uint8, w uint64, a common.Address) common.Hash {
	b := []byte("paidkey")
	var cw [9]byte
	cw[0] = c
	binary.BigEndian.PutUint64(cw[1:], w)
	b = append(b, cw[:]...)
	b = append(b, a[:]...)
	return crypto.Keccak256Hash(b)
}

// sponsoredWindowWork is the keys' work on lane c over the window's blocks
// (the pay path's own unit: each carried share's weight times the block's
// share difficulty, and the sealer's block as its work) and the keys the
// chain's own pay lists PAID in the window (an amount above zero in a
// block's list: the standing keys, the security review's cell 75fv). The window's
// blocks are read from the parent chain by hash, so the settlement runs in
// Finalize of the FIRST block after the window closes, when every block of
// the window is an ancestor.
func (e *Engine) sponsoredWindowWork(chain consensus.ChainHeaderReader, parent *types.Header, c uint8, W uint64) (work map[common.Address]*big.Int, ok bool) {
	if parent == nil {
		return nil, false
	}
	work = map[common.Address]*big.Int{}
	cur := parent
	for i := uint64(0); i < W && cur != nil; i++ {
		b, ok := e.balancedWindowBlock(chain, cur)
		if !ok {
			// F-678: a block of the window the node cannot read settles
			// NOTHING (never a zero): the caller marks no window, logs a
			// consensus fault, and the node's state root parts from the
			// chain's, so the block fails on this node; a full node holds
			// every ancestor's body (bodies are never pruned), so the read
			// fails only on a corrupt store.
			return nil, false
		}
		if b.coord == c {
			for _, s := range b.list {
				d := new(big.Int).Lsh(b.diff, uint(s.Step))
				if work[s.Address] == nil {
					work[s.Address] = new(big.Int)
				}
				work[s.Address].Add(work[s.Address], d)
			}
			if b.block != nil && b.block.Sign() > 0 {
				if work[b.sealer] == nil {
					work[b.sealer] = new(big.Int)
				}
				work[b.sealer].Add(work[b.sealer], b.block)
			}
		}
		if cur.Number.Sign() == 0 {
			break
		}
		cur = chain.GetHeader(cur.ParentHash, cur.Number.Uint64()-1)
	}
	return work, true
}

// sponsoredNeedsRoot reads the lane's document for the root rule: a
// document carrying "sponsored_root": true pays no window without a root.
func sponsoredNeedsRoot(params []byte) bool {
	var f struct {
		SponsoredRoot bool `json:"sponsored_root"`
	}
	if err := json.Unmarshal(params, &f); err != nil {
		return false
	}
	return f.SponsoredRoot
}

// settleSponsoredWindows is Finalize's settlement: at the first block of a
// window (height % W == 0, height > 0) the window just closed (height / W
// - 1) is paid for every sponsored lane with a live lock covering it: each
// standing key's part floor(amount x work_key / work_standing) in the
// chain's unit, the remainder and the standing-less parts kept, the record
// written (the window's total paid, the root used); and at the span's end
// plus the notice the unspent lock and the bond go back to the sponsor.
func (e *Engine) settleSponsoredWindows(chain consensus.ChainHeaderReader, header *types.Header, st *state.StateDB, chainID uint64) {
	m := e.vectorMenu()
	if m == nil || header == nil || header.Number.Sign() == 0 || !e.sponsoredOn(header.Number.Uint64()) {
		return
	}
	height := header.Number.Uint64()
	W := m.Window()
	if W == 0 {
		W = 64
	}
	escrows, v := e.sponsoredEscrowsAt(chainID, height)
	if len(escrows) == 0 || v == nil {
		return
	}
	parent := chain.GetHeader(header.ParentHash, height-1)
	for escrow, c := range escrows {
		for k := uint64(0); k < sponsoredLockMax; k++ {
			rec := readSponsoredLock(st, escrow, k)
			if !rec.live() {
				continue
			}
			// The window just closed.
			if height%W == 0 {
				w := height/W - 1
				if rec.FirstWindow <= w && w <= rec.LastWindow && st.GetState(escrow, sponsoredSlot("paid", uint64(c), w)) == (common.Hash{}) {
					total, ok := e.paySponsoredWindow(chain, parent, st, escrow, c, w, W, rec, v)
					if !ok {
						e.log.Error("Sponsored window not settled: a block of the window could not be read (consensus fault)", "number", height, "coordinate", c, "window", w)
						continue
					}
					st.SetState(escrow, sponsoredSlot("paid", uint64(c), w), common.BigToHash(big.NewInt(1)))
					st.SetState(escrow, sponsoredSlot("paidTotal", uint64(c), w), common.BigToHash(total))
					rec.WindowsPaid++
					writeSponsoredLock(st, escrow, k, rec)
					held := readSponsoredHeld(st, escrow)
					writeSponsoredHeld(st, escrow, new(big.Int).Sub(held, total))
					e.log.Info("Sponsored window settled", "number", height, "coordinate", c, "window", w, "paid", total)
				}
			}
			// The span's end plus the notice: the refund at the first
			// block at or past the record's own refund height (fixed at
			// the admission), while the record is live.
			if rec.RefundAt > 0 && height >= rec.RefundAt {
				windows := new(big.Int).SetUint64(rec.LastWindow - rec.FirstWindow + 1)
				locked := new(big.Int).Mul(rec.AmountPerWindow, windows)
				spent := new(big.Int)
				for w := rec.FirstWindow; w <= rec.LastWindow; w++ {
					spent.Add(spent, st.GetState(escrow, sponsoredSlot("paidTotal", uint64(c), w)).Big())
				}
				refund := new(big.Int).Sub(locked, spent)
				refund.Add(refund, rec.Bond)
				if refund.Sign() > 0 {
					st.SubBalance(escrow, uint256.MustFromBig(refund))
					st.AddBalance(rec.Sponsor, uint256.MustFromBig(refund))
				}
				held := readSponsoredHeld(st, escrow)
				writeSponsoredHeld(st, escrow, new(big.Int).Sub(held, refund))
				rec.Refunded = true
				writeSponsoredLock(st, escrow, k, rec)
				e.log.Info("Sponsored lock refunded", "number", height, "coordinate", c, "sponsor", rec.Sponsor, "refund", refund)
			}
		}
	}
}

// paySponsoredWindow pays one window of one lock: the amount over the
// standing keys pro rata to their work on the lane, floored; the total
// paid returned (the remainder stays in the escrow).
func (e *Engine) paySponsoredWindow(chain consensus.ChainHeaderReader, parent *types.Header, st *state.StateDB, escrow common.Address, c uint8, w, W uint64, rec sponsoredLockRecord, v *ctypes.IgneumVector) (*big.Int, bool) {
	total := new(big.Int)
	if int(c) < len(v.Coordinates) && sponsoredNeedsRoot(v.Coordinates[c].AlgorithmParams) && SponsoredRootAt(st, escrow, w) == (common.Hash{}) {
		return total, true // the window unsponsored: no root where the document needs one
	}
	work, ok := e.sponsoredWindowWork(chain, parent, c, W)
	if !ok {
		return nil, false
	}
	// The standing keys: those the chain's own credits paid in the window
	// (sponsoredPaidFrom's marks); the denominator is the STANDING keys'
	// total (economics' a066a2d8f): work without standing earns nothing
	// and dilutes no one.
	standing := new(big.Int)
	keys := make([]common.Address, 0, len(work))
	for a, wk := range work {
		if wk.Sign() > 0 && st.GetState(escrow, sponsoredPaidKeySlot(c, w, a)) != (common.Hash{}) {
			standing.Add(standing, wk)
			keys = append(keys, a)
		}
	}
	if standing.Sign() == 0 {
		return total, true
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
	total.Set(splitSponsoredAmount(st, escrow, rec.AmountPerWindow, keys, work, standing))
	return total, true
}

// splitSponsoredAmount pays amount over the standing keys pro rata to
// their work, floored, in address order; the total paid returned.
func splitSponsoredAmount(st *state.StateDB, escrow common.Address, amount *big.Int, keys []common.Address, work map[common.Address]*big.Int, standing *big.Int) *big.Int {
	total := new(big.Int)
	for _, a := range keys {
		part := new(big.Int).Mul(amount, work[a])
		part.Div(part, standing)
		if part.Sign() > 0 {
			st.SubBalance(escrow, uint256.MustFromBig(part))
			st.AddBalance(a, uint256.MustFromBig(part))
			total.Add(total, part)
		}
	}
	return total
}

// SponsoredLaneInfo is igneum_sponsoredLane's answer: the lane's escrow and
// its records as the state holds them.
type SponsoredLaneInfo struct {
	Coordinate uint8                 `json:"coordinate"`
	Escrow     common.Address        `json:"escrow"`
	Balance    *big.Int              `json:"balance"`
	Held       *big.Int              `json:"held"`
	Locks      []SponsoredLockView   `json:"locks"`
	Windows    []SponsoredWindowView `json:"windows"`
}

// SponsoredLockView is one lock record.
type SponsoredLockView struct {
	Slot            uint64         `json:"slot"`
	Sponsor         common.Address `json:"sponsor"`
	FirstWindow     uint64         `json:"firstWindow"`
	LastWindow      uint64         `json:"lastWindow"`
	AmountPerWindow *big.Int       `json:"amountPerWindow"`
	Bond            *big.Int       `json:"bond"`
	WindowsPaid     uint64         `json:"windowsPaid"`
	Refunded        bool           `json:"refunded"`
}

// SponsoredWindowView is one window of a lock's span: its root and what
// was paid.
type SponsoredWindowView struct {
	Window uint64      `json:"window"`
	Root   common.Hash `json:"root"`
	Paid   bool        `json:"paid"`
	Total  *big.Int    `json:"total"`
}

// sponsoredLaneView reads a lane's escrow records from a state.
func sponsoredLaneView(st *state.StateDB, chainID uint64, c uint8) *SponsoredLaneInfo {
	escrow := SponsoredEscrow(chainID, c)
	info := &SponsoredLaneInfo{Coordinate: c, Escrow: escrow, Balance: st.GetBalance(escrow).ToBig(), Held: readSponsoredHeld(st, escrow)}
	for k := uint64(0); k < sponsoredLockMax; k++ {
		r := readSponsoredLock(st, escrow, k)
		if r.Sponsor == (common.Address{}) {
			continue
		}
		info.Locks = append(info.Locks, SponsoredLockView{Slot: k, Sponsor: r.Sponsor, FirstWindow: r.FirstWindow, LastWindow: r.LastWindow, AmountPerWindow: r.AmountPerWindow, Bond: r.Bond, WindowsPaid: r.WindowsPaid, Refunded: r.Refunded})
		for w := r.FirstWindow; w <= r.LastWindow && w-r.FirstWindow < 4096; w++ {
			info.Windows = append(info.Windows, SponsoredWindowView{Window: w, Root: SponsoredRootAt(st, escrow, w), Paid: st.GetState(escrow, sponsoredSlot("paid", uint64(c), w)) != (common.Hash{}), Total: st.GetState(escrow, sponsoredSlot("paidTotal", uint64(c), w)).Big()})
		}
	}
	return info
}

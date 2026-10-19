//go:build igneumhash

package service

// The pooled form's payout run (the project's rules of 15:2x UK, 15 October 2026, the founder's 17:00 word): every member's
// share is forwarded under the pool key (Config.ForwardAs), the chain pays the key its breadth payment, and every
// window the dues are paid by transaction from the key to every member with no hand: the run is in the pool, a
// window closes when the head has moved a window past the last close and no payout of the last window is still
// pending; its failures are retried the next window and named on /payouts; the pay table and each payout's
// transaction hash are public on /payouts so a member checks the chain's payment to the key against the pool's
// payment to them; the fee's part is named per window; nothing is held past the window after the chain's payment
// lands (a due under the minimum carries to the next window, named). The key is a secp256k1 key file the pool
// makes on first start (Config.PoolKeyFile, 0600, never printed); its address is the forwarding address.

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"igneum.network/pool/internal/shares"
)

// PayoutRow is one member's due for one window and what became of it.
type PayoutRow struct {
	Member  string `json:"member"`
	Work    uint64 `json:"paid_work"`
	FeeWork uint64 `json:"fee_work"`
	DueWei  string `json:"due_wei"`
	Carry   string `json:"carried_in_wei,omitempty"` // a due under the minimum carried from earlier windows
	Tx      string `json:"tx,omitempty"`
	Block   uint64 `json:"block,omitempty"` // the send's block (0.1.91: the income read adds the send back there)
	Status  string `json:"status"`          // sent, paid, failed, carried (under the minimum), retry (failed last window)
	Error   string `json:"error,omitempty"`
	// ByCoord is the due's parts per coordinate (0.1.85, F-497: each lane's income to the weight mined on that lane);
	// absent on a one-form chain
	ByCoord []CoordDue `json:"by_coord,omitempty"`
	// CarryOut is the member's carry after this window (0.1.88, F-501): the due when it was carried or failed (it
	// already holds the carry-in), zero when it was sent or nothing was due; a restart restores the carry from the
	// member's LAST row's carry_out, never from a sum over the rows
	CarryOut string `json:"carry_out_wei,omitempty"`
	// PaidWei (0.1.95): on a "partial" row, the part of the due the key's balance allowed to send; the rest is
	// carry_out_wei (the carry by assignment whenever present)
	PaidWei string `json:"paid_wei,omitempty"`
	// AdvanceWei is what this window's due was reduced by (0.1.92, F-510): a send the chain carried from the key to
	// the member that no row of the record named (a window closed and paid twice across a handover) is an advance,
	// taken off the member's next dues; AdvanceOut what the member still owes after this window, restored at a
	// restart from the member's LAST row by assignment (as the carry is)
	AdvanceWei string `json:"advance_wei,omitempty"`
	AdvanceOut string `json:"advance_out_wei,omitempty"`
}

// AdvanceRow is a send from the key the chain carries and no row of the record named (0.1.92, F-510), found by the
// start's reconciliation of the key's sends since the last close: an advance to the member, taken off its next dues.
type AdvanceRow struct {
	Member   string `json:"member"`
	Tx       string `json:"tx"`
	Block    uint64 `json:"block"`
	ValueWei string `json:"value_wei"`
	// GasWei is the send's gas, the key's spend too (0.1.93, settlement's note): named so the key's balance identity
	// closes to the wei (balance + advances outstanding + gas paid = carries + the fee held + the remainders)
	GasWei string `json:"gas_wei,omitempty"`
}

// LossRow is a send from the key the chain carries to an address that can never carry dues (no member row in the
// record: the fee address, a stranger), the pool's LOSS by economics' rule by the address (0.1.93, 17 October 2026):
// named on the window, never owed, never recovered from dues.
type LossRow struct {
	Member   string `json:"member"`
	Tx       string `json:"tx"`
	Block    uint64 `json:"block"`
	ValueWei string `json:"value_wei"`
	GasWei   string `json:"gas_wei,omitempty"`
}

// PayoutWindow is one closed window: the chain's payment to the key over it and the dues.
type PayoutWindow struct {
	From      uint64 `json:"from_height"`
	To        uint64 `json:"to_height"`
	ClosedAt  string `json:"closed_at"`
	IncomeWei string `json:"chain_payment_to_key_wei"`
	// IncomeRule names how the income was read (0.1.91): "balance-steps", the key's credit per block over the window's
	// heights from the archive source with the pool's own sends in them added back; the rule has no second form (an
	// unreadable archive holds the close)
	IncomeRule string `json:"income_rule,omitempty"`
	// IncomeFrom and IncomeTo are the heights the income was read over (contiguous between windows)
	IncomeFrom uint64      `json:"income_from,omitempty"`
	IncomeTo   uint64      `json:"income_to,omitempty"`
	FeeWei     string      `json:"fee_part_wei"`
	GasWei     string      `json:"gas_wei,omitempty"`
	Work       uint64      `json:"paid_work"`
	Rows       []PayoutRow `json:"rows"`
	Note       string      `json:"note,omitempty"`
	Balance    string      `json:"key_balance_wei_at_close"`
	SentOutWei string      `json:"sent_out_since_last_close_wei"`
	// Rule names the payout rule the window closed under (0.1.85): "per-coordinate" (each coordinate's share of the
	// income p_c over the paid sum, then by the members' weight on it) or "pool-wide" (a one-form chain)
	Rule string `json:"rule,omitempty"`
	// PayShares is the chain's pay share per coordinate the window split by (per-coordinate), by index
	PayShares []uint64 `json:"pay_shares,omitempty"`
	// RemainderWei is the rounding's remainder (the income less the dues less the fee), the pool's, left in the key
	RemainderWei string `json:"remainder_wei,omitempty"`
	// Edge is the journal's carry count the window closed at (0.1.90, F-503): the window is the carried lines with a
	// carry number in (the previous window's edge, Edge]; FirstBlock and LastBlock the heights of its carried blocks
	// (the income's span); MovedBackPaidWork the weight moved back from windows already paid, named, never paid twice
	Edge              uint64 `json:"carry_edge,omitempty"`
	FirstBlock        uint64 `json:"first_block,omitempty"`
	LastBlock         uint64 `json:"last_block,omitempty"`
	MovedBackPaidWork uint64 `json:"moved_back_paid_work,omitempty"`
	// RestoredPaidWork (0.1.94): the weight carried again after a move-back from a paid window, named, never paid again
	RestoredPaidWork uint64 `json:"restored_paid_work,omitempty"`
	// Coords is each coordinate's figures over the window (0.1.86): the source of lane_worth on the notify
	Coords []CoordWindowRow `json:"coords,omitempty"`
	// Advances are the key's sends the chain carried after this window's close that no row of the record named
	// (0.1.92, F-510), found at a start's reconciliation; applied to the members' dues at the next closes (advance_wei
	// on the rows), what is still owed named per row (advance_out_wei)
	Advances []AdvanceRow `json:"advances,omitempty"`
	// Losses are the key's sends to addresses with no member row (0.1.93): the pool's loss, named, never owed
	Losses []LossRow `json:"losses,omitempty"`
	// AdvancesOutstandingWei is the sum every member still owes the pool after this window's close (0.1.93,
	// economics' term): the key's receivable, so a reader closes the key's balance identity from the window alone
	AdvancesOutstandingWei string `json:"advances_outstanding_wei,omitempty"`
	// AppliedWei (0.1.95, F-540) is the sum of the window's dues applied to the members' advances (the rows'
	// advance_wei): the identity reads dues + fee + remainder + applied = the chain's payment
	AppliedWei string `json:"applied_wei,omitempty"`
}

type payoutState struct {
	key         *ecdsa.PrivateKey
	address     common.Address
	nonce       uint64
	lastClose   uint64                         // the height the last window closed at (0 before the first)
	lastBalance *big.Int                       // the key's balance read at the last close
	sentSince   *big.Int                       // value plus gas of the payouts confirmed since the last close
	ledgerAt    map[string]shares.MemberLedger // the per-member ledger at the last close
	// coordAt: the per-member per-coordinate ledger at the last close (0.1.85), keyed by member then coordinate
	coordAt map[string]map[uint8]shares.MemberCoordWork
	carry   map[string]*big.Int // dues under the minimum, carried to the next window
	pending []*PayoutRow        // sent, not yet seen in a receipt
	windows []PayoutWindow
	// lastEdge: the carry edge of the last closed window (0.1.90); edgeUnset while a record closed by height before
	// 0.1.90 has no edge, resolved at the first tick from the journal's carry count at the last close's height
	lastEdge  uint64
	edgeUnset bool
	// sentAt: what the pool's own sends took from the key per block (0.1.91), added back to the income read over a
	// window's heights; lastIncomeTo the last height the income was read to (the next window starts after it)
	sentAt       map[uint64]*big.Int
	lastIncomeTo uint64
	refusedSaid  bool
	// deltaRefusedAt: the window (its from height) whose close was last held for an unreadable archive, said once
	deltaRefusedAt uint64
	busy           int32
	file           string
	failedNotes    []string
	// loaded (0.1.92, F-510): the record has been read, which happens only under the journal's lock (the previous
	// generation holds the lock until it exits, its close in flight written first), so no generation closes a window
	// the previous one closed; reconciled: the key's chain sends since the last close have been checked against the
	// record once at this start; advance: what each member owes the pool for a send no row named
	loaded     int32
	reconciled bool
	advance    map[string]*big.Int
}

// keySend is one transaction from the pool key in a block, as the chain carries it.
type keySend struct {
	Hash, To   string
	Block      uint64
	Value, Gas *big.Int
	OK         bool // the transaction went through (its value moved)
}

// payoutInit loads or makes the pool key and the payouts record.
func (s *Service) payoutInit() error {
	if s.cfg.PoolKeyFile == "" {
		return nil
	}
	var key *ecdsa.PrivateKey
	if _, err := os.Stat(s.cfg.PoolKeyFile); err == nil {
		k, err := crypto.LoadECDSA(s.cfg.PoolKeyFile)
		if err != nil {
			return fmt.Errorf("pool key file: %w", err)
		}
		key = k
	} else {
		k, err := crypto.GenerateKey()
		if err != nil {
			return err
		}
		if err := crypto.SaveECDSA(s.cfg.PoolKeyFile, k); err != nil {
			return fmt.Errorf("pool key file: %w", err)
		}
		_ = os.Chmod(s.cfg.PoolKeyFile, 0o600)
		key = k
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	ps := &payoutState{key: key, address: addr, sentSince: new(big.Int), carry: map[string]*big.Int{}, file: s.cfg.PoolKeyFile + ".payouts.jsonl", ledgerAt: map[string]shares.MemberLedger{}, coordAt: map[string]map[uint8]shares.MemberCoordWork{}}
	s.payout = ps
	if s.cfg.ForwardAs == "" {
		s.cfg.ForwardAs = strings.ToLower(addr.Hex())
	}
	log.Printf("payout run armed: the pool key %s (the file's, never printed), every member's share forwarded under it, the dues paid every %d blocks from it; the record %s is read once the journal's lock is held (0.1.92)", addr.Hex(), s.payoutWindowBlocks(), ps.file)
	return nil
}

// payoutLoadRecord reads the record (0.1.92, F-510): called once the journal's lock is held, so a previous generation's
// close in flight (its sends made, its record written before it exits) is in the record this generation reads, and
// no window closes twice across a handover (3,371's four sends of 17 October 2026, 81.396 IGN sent twice). The run
// closes nothing before this.
func (s *Service) payoutLoadRecord() {
	ps := s.payout
	if ps == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	windows, pending, err := readPayoutRecord(ps.file)
	if err != nil {
		log.Printf("payout run: the record %s: %v (an unreadable record is a stop: no window closes)", ps.file, err)
		return
	}
	ps.windows = windows
	ps.sentAt = nil
	// the record: the closed windows and the pending rows, replayed so a restart neither pays twice nor forgets
	for wi := range ps.windows {
		w := ps.windows[wi]
		{
			ps.lastClose = w.To
			ps.lastEdge = w.Edge
			ps.edgeUnset = w.Edge == 0
			if w.IncomeTo > 0 {
				ps.lastIncomeTo = w.IncomeTo
			} else {
				ps.lastIncomeTo = w.To
			}
			for _, r := range w.Rows {
				if r.Block > 0 && r.Status == "paid" {
					if ps.sentAt == nil {
						ps.sentAt = map[uint64]*big.Int{}
					}
					if d, ok := new(big.Int).SetString(r.DueWei, 10); ok {
						if ps.sentAt[r.Block] == nil {
							ps.sentAt[r.Block] = new(big.Int)
						}
						ps.sentAt[r.Block].Add(ps.sentAt[r.Block], d)
					}
				}
			}
			if b, ok := new(big.Int).SetString(w.Balance, 10); ok {
				ps.lastBalance = b
			}
		}
	}
	_ = pending
	ps.pending = nil
	for wi := range ps.windows {
		for i := range ps.windows[wi].Rows {
			if ps.windows[wi].Rows[i].Status == "sent" {
				ps.pending = append(ps.pending, &ps.windows[wi].Rows[i])
			}
		}
	}
	{
		// F-501 (0.1.88): the carry is each member's LAST row's state, by assignment (carryFromRecord); the 0.1.8x
		// sum over every row compounded the carry at every restart
		ps.carry = carryFromRecord(ps.windows)
		for _, w := range ps.windows {
			for _, r := range w.Rows {
				l := ps.ledgerAt[r.Member]
				l.Member = r.Member
				l.CarriedWork += r.Work
				l.FeeWork += r.FeeWork
				ps.ledgerAt[r.Member] = l
				for _, cd := range r.ByCoord {
					if ps.coordAt[r.Member] == nil {
						ps.coordAt[r.Member] = map[uint8]shares.MemberCoordWork{}
					}
					cw := ps.coordAt[r.Member][cd.Coord]
					cw.CarriedWork += cd.Work
					cw.FeeWork += cd.FeeWork
					ps.coordAt[r.Member][cd.Coord] = cw
				}
			}
		}
	}
	ps.advance = advanceFromRecord(ps.windows)
	atomic.StoreInt32(&ps.loaded, 1)
	log.Printf("payout run on: the record %s read under the journal's lock (%d window(s), %d payout(s) pending, %d member(s) with an advance owed)", ps.file, len(ps.windows), len(ps.pending), len(ps.advance))
}

// readPayoutRecord reads the record's windows; a missing file is an empty record.
func readPayoutRecord(file string) (windows []PayoutWindow, pending int, err error) {
	f, err := os.Open(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var w PayoutWindow
		if err := dec.Decode(&w); err != nil {
			break
		}
		windows = append(windows, w)
		for _, r := range w.Rows {
			if r.Status == "sent" {
				pending++
			}
		}
	}
	return windows, pending, nil
}

// PayoutStart is the start without a handover (0.1.92): the journal's lock taken (so the next generation waits for this
// one), then the record read. With a handover, HandoverJournal does both.
func (s *Service) PayoutStart() error {
	s.mu.Lock()
	j := s.journal
	s.mu.Unlock()
	if j != nil {
		if err := j.Lock(); err != nil {
			return fmt.Errorf("the journal's lock: %w", err)
		}
	}
	s.payoutLoadRecord()
	return nil
}

// PayoutWait waits for a payout run in flight to finish (0.1.92, F-510: the stop waits for a close's sends and its
// record write, so the next generation reads the window as closed); true when it did within the bound.
func (s *Service) PayoutWait(bound time.Duration) bool {
	ps := s.payout
	if ps == nil {
		return true
	}
	t := time.NewTimer(bound)
	defer t.Stop()
	for {
		if atomic.LoadInt32(&ps.busy) == 0 {
			return true
		}
		select {
		case <-t.C:
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// laneWorthAt is the last closed window's lane_worth for a coordinate (0.1.86), "" before the first close, on a
// window closed under the pool-wide rule, or on a coordinate the window carried no work on (the caller holds s.mu).
func (s *Service) laneWorthAt(coord uint8) string {
	ps := s.payout
	if ps == nil || len(ps.windows) == 0 {
		return ""
	}
	w := ps.windows[len(ps.windows)-1]
	for _, r := range w.Coords {
		if r.Coord == coord {
			return LaneWorthWei(r.IncomeWei, r.Work)
		}
	}
	return ""
}

func (ps *payoutState) carryOf(m string) *big.Int {
	if c := ps.carry[m]; c != nil {
		return c
	}
	return new(big.Int)
}

func (s *Service) payoutWindowBlocks() uint64 {
	if s.cfg.PayoutWindow > 0 {
		return s.cfg.PayoutWindow
	}
	return AddressWindow
}

// payoutTick runs once a poll: receipts for the pending payouts, then a window close when due.
func (s *Service) payoutTick(ctx context.Context, head uint64) {
	ps := s.payout
	if ps == nil || head == 0 || atomic.LoadInt32(&ps.loaded) == 0 || !atomic.CompareAndSwapInt32(&ps.busy, 0, 1) {
		return
	}
	go func() {
		defer atomic.StoreInt32(&ps.busy, 0)
		s.payoutReceipts(ctx)
		if !ps.reconciled && !s.payoutReconcile(ctx, head) {
			return
		}
		// 0.1.91 (the security review's read of 0.1.90): a record whose carry edge exceeds the journal's replayed carry count
		// names lines the journal no longer holds; the run refuses to close (an empty window would pay nothing and
		// move the edge) and says so once; the recompute is the correction
		if ps.lastEdge > s.journal.CarryEdge() {
			if !ps.refusedSaid {
				ps.refusedSaid = true
				log.Printf("payout run: REFUSED: the record's carry edge %d exceeds the journal's carried lines %d (the journal does not hold the lines the record names): no window closes until the record is recomputed from the journal", ps.lastEdge, s.journal.CarryEdge())
			}
			return
		}
		if ps.edgeUnset {
			// 0.1.90: a record closed by height before this version names no carry edge: the edge is the carry
			// count of the lines carried in blocks at or under the last close's height, read once at the roll (the
			// lines carried after that close with a height inside it are F-503's lost tail: the recompute's)
			edge := s.journal.CarryEdgeAtHeight(ps.lastClose)
			s.mu.Lock()
			ps.lastEdge, ps.edgeUnset = edge, false
			s.mu.Unlock()
			log.Printf("payout run: the record's last window (to %d) names no carry edge (closed before 0.1.90): the edge taken at carry %d", ps.lastClose, edge)
		}
		if ps.lastClose == 0 {
			// the first close is a window after the run starts: the ledger and the balance at start are the base
			s.mu.Lock()
			ps.lastClose = head
			ps.lastEdge = s.journal.CarryEdge()
			s.mu.Unlock()
			bal, err := s.keyBalance(ctx)
			if err == nil {
				s.mu.Lock()
				ps.lastBalance = bal
				for _, m := range s.journal.MemberLedgers(s.cfg.ForwardAs) {
					ps.ledgerAt[m.Member] = m
				}
				ps.coordAt = s.journal.MemberCoordLedgers(s.cfg.ForwardAs)
				s.mu.Unlock()
			}
			return
		}
		if head < ps.lastClose+s.payoutWindowBlocks() || len(ps.pending) > 0 {
			return
		}
		s.payoutClose(ctx, head)
	}()
}

func (s *Service) keyBalance(ctx context.Context) (*big.Int, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var hex string
	if err := s.sourceNode().Call(cctx, "eth_getBalance", []interface{}{s.payout.address.Hex(), "latest"}, &hex); err != nil {
		return nil, err
	}
	n, ok := new(big.Int).SetString(strings.TrimPrefix(hex, "0x"), 16)
	if !ok {
		return nil, fmt.Errorf("balance %q unreadable", hex)
	}
	return n, nil
}

// keyCreditOver is the chain's credit to the pool key over the heights [from, to] (0.1.91, F-502): the sum of the key's
// balance steps block by block on the archive source (the balance at h less at h minus 1), the pool's own sends in
// those blocks added back (sentAt), so the figure is what the chain paid the key for those heights alone; an error
// when the archive cannot serve a height.
func (s *Service) keyCreditOver(ctx context.Context, from, to uint64) (*big.Int, error) {
	total := new(big.Int)
	if to < from {
		return total, nil
	}
	node := s.sourceNode()
	if s.archive != nil {
		node = s.archive
	}
	addr := s.payout.address.Hex()
	balanceAt := func(h uint64) (*big.Int, error) {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var hex string
		if err := node.Call(cctx, "eth_getBalance", []interface{}{addr, fmt.Sprintf("0x%x", h)}, &hex); err != nil {
			return nil, err
		}
		n, ok := new(big.Int).SetString(strings.TrimPrefix(hex, "0x"), 16)
		if !ok {
			return nil, fmt.Errorf("balance at %d %q unreadable", h, hex)
		}
		return n, nil
	}
	prev, err := balanceAt(from - 1)
	if err != nil {
		return nil, err
	}
	// the key's own sends in a block, from the CHAIN (F-509, 17 October 2026): every transaction from the key in the
	// block, its value when it went through and its gas either way, added back to the block's step; the record's own
	// rows are not the source (a window closed twice across a handover sent 81.396 IGN the record did not carry, and an
	// add-back from the record read that block's credit as a loss)
	sendsIn := func(h uint64) (*big.Int, error) {
		sends, err := s.keySendsIn(ctx, node, h)
		if err != nil {
			return nil, err
		}
		out := new(big.Int)
		for _, t := range sends {
			out.Add(out, t.Gas)
			if t.OK {
				out.Add(out, t.Value)
			}
		}
		return out, nil
	}
	for h := from; h <= to; h++ {
		cur, err := balanceAt(h)
		if err != nil {
			return nil, err
		}
		step := new(big.Int).Sub(cur, prev)
		v, err := sendsIn(h)
		if err != nil {
			return nil, err
		}
		step.Add(step, v)
		if step.Sign() > 0 {
			total.Add(total, step)
		}
		prev = cur
	}
	return total, nil
}

// keySendsIn is every transaction from the pool key in block h as the chain carries it (0.1.92): its hash, the
// address it went to, its value and whether it went through, and its gas cost either way.
func (s *Service) keySendsIn(ctx context.Context, node Node, h uint64) ([]keySend, error) {
	addr := strings.ToLower(s.payout.address.Hex())
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var blk struct {
		Transactions []struct {
			Hash     string `json:"hash"`
			From     string `json:"from"`
			To       string `json:"to"`
			Value    string `json:"value"`
			GasPrice string `json:"gasPrice"`
		} `json:"transactions"`
	}
	if err := node.Call(cctx, "eth_getBlockByNumber", []interface{}{fmt.Sprintf("0x%x", h), true}, &blk); err != nil {
		return nil, err
	}
	var out []keySend
	for _, tx := range blk.Transactions {
		if strings.ToLower(tx.From) != addr {
			continue
		}
		var rc struct {
			Status  string `json:"status"`
			GasUsed string `json:"gasUsed"`
			Price   string `json:"effectiveGasPrice"`
		}
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		err := node.Call(rctx, "eth_getTransactionReceipt", []interface{}{tx.Hash}, &rc)
		rcancel()
		if err != nil {
			return nil, err
		}
		price := rc.Price
		if price == "" {
			price = tx.GasPrice
		}
		t := keySend{Hash: strings.ToLower(tx.Hash), To: strings.ToLower(tx.To), Block: h, Value: new(big.Int), Gas: new(big.Int), OK: rc.Status == "0x1"}
		if g, ok := new(big.Int).SetString(strings.TrimPrefix(rc.GasUsed, "0x"), 16); ok {
			if p, ok := new(big.Int).SetString(strings.TrimPrefix(price, "0x"), 16); ok {
				t.Gas.Mul(g, p)
			}
		}
		if v, ok := new(big.Int).SetString(strings.TrimPrefix(tx.Value, "0x"), 16); ok {
			t.Value.Set(v)
		}
		out = append(out, t)
	}
	return out, nil
}

// payoutReconcile is the start's reconciliation (0.1.92, F-510): the key's sends the chain carries in the blocks after
// the last close, against the record; a send no row and no earlier reconciliation named is an advance to the member
// it went to, named on the last window and taken off that member's next dues. True once done (an unreadable chain is
// tried again at the next tick; nothing closes before).
func (s *Service) payoutReconcile(ctx context.Context, head uint64) bool {
	ps := s.payout
	s.mu.Lock()
	from, n := ps.lastClose+1, len(ps.windows)
	s.mu.Unlock()
	if n == 0 || head < from {
		ps.reconciled = true
		return true
	}
	const bound = 8192
	if head-from+1 > bound {
		log.Printf("payout run: the reconciliation of the key's sends covers the last %d blocks (%d to %d) of the %d since the last close; the blocks before are the recompute's", bound, head-bound+1, head, head-from+1)
		from = head - bound + 1
	}
	node := s.sourceNode()
	if s.archive != nil {
		node = s.archive
	}
	var sends []keySend
	for h := from; h <= head; h++ {
		ts, err := s.keySendsIn(ctx, node, h)
		if err != nil {
			log.Printf("payout run: the reconciliation of the key's sends since the last close is HELD at block %d (%v); tried again at the next tick, nothing closes before", h, err)
			return false
		}
		sends = append(sends, ts...)
	}
	s.mu.Lock()
	fresh := reconcileAdvances(ps.windows, sends)
	// economics' rule by the address (0.1.93): a send to an address with no member row in the record is the pool's
	// loss, named on the window, never owed; a send to a member key is the advance, owed
	var losses []LossRow
	if len(fresh) > 0 {
		members := map[string]struct{}{}
		for _, w := range ps.windows {
			for _, r := range w.Rows {
				members[strings.ToLower(r.Member)] = struct{}{}
			}
		}
		var owed []AdvanceRow
		for _, a := range fresh {
			if _, ok := members[a.Member]; ok {
				owed = append(owed, a)
			} else {
				losses = append(losses, LossRow(a))
			}
		}
		last := &ps.windows[len(ps.windows)-1]
		last.Advances = append(last.Advances, owed...)
		last.Losses = append(last.Losses, losses...)
		ps.advance = advanceFromRecord(ps.windows)
		last.AdvancesOutstandingWei = sumWei(ps.advance)
		fresh = owed
	}
	ps.reconciled = true
	s.mu.Unlock()
	for _, a := range fresh {
		log.Printf("payout run: ADVANCE: the chain carries a send of %s wei from the key to %s in block %d (%s) that no row of the record names: taken off that member's next dues (F-510)", a.ValueWei, a.Member, a.Block, a.Tx)
	}
	for _, l := range losses {
		log.Printf("payout run: LOSS: the chain carries a send of %s wei from the key to %s in block %d (%s), an address with no member row: the pool's loss, named, never owed (economics' rule by the address)", l.ValueWei, l.Member, l.Block, l.Tx)
	}
	log.Printf("payout run: the key's sends over blocks %d to %d reconciled against the record: %d send(s) on the chain, %d named by the record, %d advance(s), %d loss(es)", from, head, len(sends), len(sends)-len(fresh)-len(losses), len(fresh), len(losses))
	if len(fresh) > 0 || len(losses) > 0 {
		s.payoutRewrite()
	}
	return true
}

// sumWei is the decimal sum of the map's values.
func sumWei(m map[string]*big.Int) string {
	t := new(big.Int)
	for _, v := range m {
		t.Add(t, v)
	}
	return t.String()
}

// reconcileAdvances is the pure form of the reconciliation (0.1.92): the sends that went through with a value, whose
// hash no row's tx and no window's advances name, as advances to the addresses they went to, in the order given.
func reconcileAdvances(windows []PayoutWindow, sends []keySend) []AdvanceRow {
	known := map[string]struct{}{}
	for _, w := range windows {
		for _, r := range w.Rows {
			if r.Tx != "" {
				known[strings.ToLower(r.Tx)] = struct{}{}
			}
		}
		for _, a := range w.Advances {
			known[strings.ToLower(a.Tx)] = struct{}{}
		}
	}
	var out []AdvanceRow
	for _, t := range sends {
		if !t.OK || t.Value == nil || t.Value.Sign() == 0 {
			continue
		}
		if _, ok := known[t.Hash]; ok {
			continue
		}
		known[t.Hash] = struct{}{}
		row := AdvanceRow{Member: strings.ToLower(t.To), Tx: t.Hash, Block: t.Block, ValueWei: t.Value.String()}
		if t.Gas != nil && t.Gas.Sign() > 0 {
			row.GasWei = t.Gas.String()
		}
		out = append(out, row)
	}
	return out
}

// advanceFromRecord is what each member owes the pool after the record's last window (0.1.92): its LAST row's
// advance_out by assignment, plus the advances named on that row's window and the windows after it (found after the
// row was written, applied at no close yet); every advance when the member has no row.
func advanceFromRecord(windows []PayoutWindow) map[string]*big.Int {
	lastRow := map[string]int{}
	owed := map[string]*big.Int{}
	for wi, w := range windows {
		for _, r := range w.Rows {
			m := strings.ToLower(r.Member)
			lastRow[m] = wi
			v, ok := new(big.Int).SetString(r.AdvanceOut, 10)
			if !ok || v.Sign() <= 0 {
				v = new(big.Int)
			}
			owed[m] = v
		}
	}
	for wi, w := range windows {
		for _, a := range w.Advances {
			m := strings.ToLower(a.Member)
			if li, ok := lastRow[m]; ok && wi < li {
				continue // applied by a later row (its advance_out carries what is left)
			}
			v, ok := new(big.Int).SetString(a.ValueWei, 10)
			if !ok {
				continue
			}
			if owed[m] == nil {
				owed[m] = new(big.Int)
			}
			owed[m].Add(owed[m], v)
		}
	}
	for m, v := range owed {
		if v.Sign() <= 0 {
			delete(owed, m)
		}
	}
	return owed
}

// rowDecision is what becomes of a member's gross due at a close (0.1.95, F-540): the part applied to the advance it
// owes, the rest of the advance still owed, the due left after the application, and the row's status: "sent" (at or
// over the minimum), "carried" (under it), "applied to the advance" (the whole due went to the advance), or "nothing
// due" (no due at all).
type rowDecision struct {
	applied, rest, due *big.Int
	status             string
}

// decideRow is the pure form of the row's decision (its fixture: a member with an advance and a due below and above it).
func decideRow(gross, owed, min *big.Int) rowDecision {
	d := rowDecision{applied: new(big.Int), rest: new(big.Int), due: new(big.Int).Set(gross)}
	if owed != nil && owed.Sign() > 0 {
		d.applied, d.rest = applyAdvance(owed, gross)
		d.due.Sub(d.due, d.applied)
	}
	switch {
	case d.due.Sign() == 0 && d.applied.Sign() > 0:
		d.status = "applied to the advance"
	case d.due.Sign() == 0:
		d.status = "nothing due"
	case min != nil && d.due.Cmp(min) < 0:
		d.status = "carried"
	default:
		d.status = "sent"
	}
	return d
}

// gasReserveWei is the gas kept back per send of the window (21,000 gas at 2 gwei, twice the chains' price).
var gasReserveWei = big.NewInt(21000 * 2_000_000_000)

// partialSend is what the key sends of a due when its balance allows less (0.1.95): the whole due when it fits, else
// the available balance (the rest carried), and nothing when the available part is under the minimum payout.
func partialSend(due, avail, min *big.Int) (value, carry *big.Int) {
	if avail.Sign() <= 0 {
		return new(big.Int), new(big.Int).Set(due)
	}
	if due.Cmp(avail) <= 0 {
		return new(big.Int).Set(due), new(big.Int)
	}
	if min != nil && avail.Cmp(min) < 0 {
		return new(big.Int), new(big.Int).Set(due)
	}
	return new(big.Int).Set(avail), new(big.Int).Sub(due, avail)
}

// applyAdvance is what a due pays off an advance owed: as much as the due holds; the rest stays owed.
func applyAdvance(owed, due *big.Int) (applied, rest *big.Int) {
	applied = new(big.Int)
	if due.Sign() > 0 && owed.Sign() > 0 {
		applied.Set(owed)
		if applied.Cmp(due) > 0 {
			applied.Set(due)
		}
	}
	return applied, new(big.Int).Sub(owed, applied)
}

// payoutReceipts reads the receipts of the sent payouts: paid, or failed (retried next window by the carry).
func (s *Service) payoutReceipts(ctx context.Context) {
	ps := s.payout
	s.mu.Lock()
	pending := append([]*PayoutRow(nil), ps.pending...)
	s.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var still []*PayoutRow
	changed := false
	for _, r := range pending {
		var rc struct {
			Status      string `json:"status"`
			GasUsed     string `json:"gasUsed"`
			Price       string `json:"effectiveGasPrice"`
			BlockNumber string `json:"blockNumber"`
		}
		if err := s.sourceNode().Call(cctx, "eth_getTransactionReceipt", []interface{}{r.Tx}, &rc); err != nil || rc.Status == "" {
			still = append(still, r)
			continue
		}
		s.mu.Lock()
		due, _ := new(big.Int).SetString(r.DueWei, 10)
		if rc.Status == "0x1" {
			r.Status = "paid"
			ps.sentSince.Add(ps.sentSince, due)
		} else {
			r.Status = "failed"
			r.Error = "the transaction reverted; retried next window"
			ps.carry[r.Member] = new(big.Int).Set(due) // the due holds the carry-in already (F-501)
			r.CarryOut = due.String()
		}
		cost := new(big.Int)
		if g, ok := new(big.Int).SetString(strings.TrimPrefix(rc.GasUsed, "0x"), 16); ok {
			if p, ok := new(big.Int).SetString(strings.TrimPrefix(rc.Price, "0x"), 16); ok {
				cost.Mul(g, p)
				ps.sentSince.Add(ps.sentSince, cost)
			}
		}
		// 0.1.91 (F-502): the send's block and what it took from the key (the value when it went through, the gas
		// either way), so the income read over a window's heights adds the pool's own sends back
		if bn, ok := new(big.Int).SetString(strings.TrimPrefix(rc.BlockNumber, "0x"), 16); ok {
			out := new(big.Int).Set(cost)
			if rc.Status == "0x1" {
				out.Add(out, due)
			}
			if ps.sentAt == nil {
				ps.sentAt = map[uint64]*big.Int{}
			}
			if ps.sentAt[bn.Uint64()] == nil {
				ps.sentAt[bn.Uint64()] = new(big.Int)
			}
			ps.sentAt[bn.Uint64()].Add(ps.sentAt[bn.Uint64()], out)
			r.Block = bn.Uint64()
		}
		s.mu.Unlock()
		changed = true
	}
	s.mu.Lock()
	ps.pending = still
	s.mu.Unlock()
	if changed {
		s.payoutRewrite()
	}
}

// payoutClose closes the window at head: the chain's payment to the key since the last close, the dues by work,
// the transactions.
func (s *Service) payoutClose(ctx context.Context, head uint64) {
	ps := s.payout
	bal, err := s.keyBalance(ctx)
	if err != nil {
		log.Printf("payout run: the key's balance unread at height %d: %v (the window stays open)", head, err)
		return
	}
	// 0.1.91 (F-502): the income is the chain's credit to the key over the window's heights, (the last window's last
	// height, this window's last carried block], read block by block as the key's balance steps on the archive source
	// with the pool's own sends in those blocks added back; the balance delta between closes (the form before) is
	// kept only when the archive cannot serve the history, named on the window
	edge := s.journal.CarryEdge()
	spanRead := s.journal.WindowWork(s.cfg.ForwardAs, ps.lastEdge, edge)
	incomeFrom := ps.lastIncomeTo + 1
	incomeTo := spanRead.LastHeight
	if incomeTo < incomeFrom {
		incomeTo = incomeFrom - 1 // no carried block this window: no heights, no income
	}
	incomeRule := "balance-steps"
	income, err := s.keyCreditOver(ctx, incomeFrom, incomeTo)
	if err != nil {
		// the income rule has no second form (economics' rule of record, the review lead's ruling of 17 October 2026): an
		// unreadable archive HOLDS the close, named once a window, and the run tries again at the next tick; no
		// window ever pays on a balance delta between closes again (the form F-502 struck)
		if ps.deltaRefusedAt != incomeFrom {
			ps.deltaRefusedAt = incomeFrom
			log.Printf("payout run: the close of the window from %d is HELD: the key's credit over %d to %d is unreadable on the archive (%v); the income rule has no other form, the close is tried again at the next tick", incomeFrom, incomeFrom, incomeTo, err)
		}
		return
	}
	s.mu.Lock()
	type delta struct {
		member        string
		work, feeWork uint64
	}
	var ds []delta
	var total, totalFee uint64
	win := PayoutWindow{From: ps.lastClose + 1, To: head, ClosedAt: s.now().UTC().Format(time.RFC3339), IncomeWei: income.String(), Work: total, Balance: bal.String(), SentOutWei: ps.sentSince.String()}
	// 0.1.90 (F-503): the window is the carried lines with a carry number in (the last edge, the journal's carry count
	// now]: keyed by the carry's ORDER, never a height, so a block credited after a close lands in the next window
	// whatever its height; the window records its carried blocks' first and last heights (the income's span)
	win.Edge = edge
	win.IncomeRule, win.IncomeFrom, win.IncomeTo = incomeRule, incomeFrom, incomeTo
	// 0.1.85 (F-497): the window's income splits per coordinate by the chain's pay shares, then within a coordinate
	// by each member's weight on it over the pool's (the deltas of the per-member per-coordinate ledger since the
	// last close); a one-form chain keeps the pool-wide rule (one window over every member's weight)
	// 0.1.89 (F-500): the window's ledger is the journal's rows BY HEIGHT, the lines carried at heights in
	// [from, to] per member per coordinate (WindowWork), never a cumulative delta against a baseline (the 0.1.8x
	// baseline, rebuilt at a restart from the record's rows' sums, ran ahead of the journal's ledger, the negative
	// delta was zeroed and whole windows paid nothing: five windows of 17 October 2026); the per-coordinate rule
	// splits by the chain's pay shares, the pool-wide rule (a one-form chain) by every member's weight
	var windows []coordWindow
	span := spanRead
	work := span.Work
	win.FirstBlock, win.LastBlock, win.MovedBackPaidWork, win.RestoredPaidWork = span.FirstHeight, span.LastHeight, span.MovedBackPaidWork, span.RestoredPaidWork
	byCoord := map[uint8]*coordWindow{}
	perCoord := len(s.cfg.Coordinates) > 0 && len(s.payShares) > 0
	for member, coords := range work {
		if member == "" {
			continue // the pool-wide row is the sum of the members'
		}
		for c, r := range coords {
			if r.Work == 0 {
				continue
			}
			key := c
			if !perCoord {
				key = 0
			}
			cw := byCoord[key]
			if cw == nil {
				p := uint64(1)
				if perCoord {
					p = 0
					if int(c) < len(s.payShares) {
						p = s.payShares[c]
					}
				}
				cw = &coordWindow{coord: key, payShare: p, members: map[string]memberWork{}}
				byCoord[key] = cw
			}
			cw.work += r.Work
			cw.feeWork += r.FeeWork
			m := cw.members[member]
			m.Work += r.Work
			m.FeeWork += r.FeeWork
			cw.members[member] = m
		}
	}
	for _, cw := range byCoord {
		windows = append(windows, *cw)
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].coord < windows[j].coord })
	if perCoord {
		win.Rule = "per-coordinate"
		win.PayShares = append([]uint64(nil), s.payShares...)
	} else {
		win.Rule = "pool-wide"
	}
	// the pool-wide figures on the window (Work) and the rows (Work, FeeWork) are the window's by-height sums too
	total, totalFee = 0, 0
	ds = ds[:0]
	for member, coords := range work {
		if member == "" {
			continue
		}
		var w, f uint64
		for _, r := range coords {
			w += r.Work
			f += r.FeeWork
		}
		ds = append(ds, delta{member, w, f})
		total += w
		totalFee += f
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].member < ds[j].member })
	for m, c := range ps.carry {
		if c.Sign() > 0 {
			found := false
			for _, d := range ds {
				if d.member == m {
					found = true
				}
			}
			if !found {
				ds = append(ds, delta{m, 0, 0})
			}
		}
	}
	for m, a := range ps.advance {
		if a.Sign() > 0 {
			found := false
			for _, d := range ds {
				if d.member == m {
					found = true
				}
			}
			if !found {
				ds = append(ds, delta{m, 0, 0})
			}
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].member < ds[j].member })
	win.Work = total
	dues, parts, feePart, remainder, coordRows := splitByCoordRows(income, windows)
	win.FeeWei = feePart.String()
	win.RemainderWei = remainder.String()
	if win.Rule == "per-coordinate" {
		win.Coords = coordRows
	}
	min := s.cfg.MinPayoutWei
	if min == nil {
		min = new(big.Int)
	}
	appliedSum := new(big.Int)
	for _, d := range ds {
		due := new(big.Int)
		if dues[d.member] != nil {
			due.Set(dues[d.member])
		}
		row := PayoutRow{Member: d.member, Work: d.work, FeeWork: d.feeWork, ByCoord: parts[d.member]}
		if c := ps.carryOf(d.member); c.Sign() > 0 {
			row.Carry = c.String()
			due.Add(due, c)
			delete(ps.carry, d.member)
		}
		// 0.1.92 (F-510): an advance owed is taken off the due, as much as the due holds; the rest stays owed, named.
		// 0.1.95 (F-540): a row whose due went to the advance is "applied to the advance", never "nothing due"
		gross := new(big.Int).Set(due)
		dec := decideRow(due, ps.advance[d.member], min)
		if dec.applied.Sign() > 0 {
			row.AdvanceWei = dec.applied.String()
			appliedSum.Add(appliedSum, dec.applied)
		}
		if dec.rest.Sign() > 0 {
			ps.advance[d.member] = dec.rest
			row.AdvanceOut = dec.rest.String()
		} else if gross.Sign() > 0 || dec.applied.Sign() > 0 {
			delete(ps.advance, d.member)
		}
		row.DueWei = dec.due.String()
		row.Status = dec.status
		if dec.status == "carried" {
			ps.carry[d.member] = dec.due
			row.CarryOut = dec.due.String()
		}
		win.Rows = append(win.Rows, row)
	}
	ps.lastClose, ps.lastBalance, ps.sentSince, ps.lastEdge = head, bal, new(big.Int), edge
	ps.lastIncomeTo = incomeTo
	for b := range ps.sentAt {
		if b <= incomeTo {
			delete(ps.sentAt, b)
		}
	}
	// the rows through this window's edge are paid and forgotten (the security review's read of 0.1.89: the index is bounded
	// at every close)
	s.journal.PruneCarriedThrough(edge)
	win.AdvancesOutstandingWei = sumWei(ps.advance)
	win.AppliedWei = appliedSum.String()
	ps.windows = append(ps.windows, win)
	wi := len(ps.windows) - 1
	s.mu.Unlock()
	// the transactions, one a member, from the key; a send that fails is named on the row and carried. 0.1.92
	// (F-510): once the rows are decided the sends and the record's write run to the end whatever the stop (the
	// context outlives the cancel; main waits for the run before the process ends), so the next generation reads
	// the window as closed and never pays it again
	sctx := context.WithoutCancel(ctx)
	sent := 0
	// 0.1.95: the pool sends what the key's balance allows: the balance at the close less a gas reserve for the
	// window's sends is the ceiling; a due above it is sent in part and the rest carried (the row "partial"), so the
	// key never idles above a carry (the backlog of 17 October 2026 failed on funds at every close for hours)
	toSend := 0
	for i := range ps.windows[wi].Rows {
		if ps.windows[wi].Rows[i].Status == "sent" {
			toSend++
		}
	}
	avail := new(big.Int).Set(bal)
	avail.Sub(avail, new(big.Int).Mul(big.NewInt(int64(toSend)), gasReserveWei))
	for i := range ps.windows[wi].Rows {
		r := &ps.windows[wi].Rows[i]
		if r.Status != "sent" {
			continue
		}
		due, _ := new(big.Int).SetString(r.DueWei, 10)
		value, carry := partialSend(due, avail, min)
		if value.Sign() == 0 {
			s.mu.Lock()
			r.Status = "carried"
			r.Error = "the key's balance allows no send this window; carried"
			ps.carry[r.Member] = new(big.Int).Set(due)
			r.CarryOut = due.String()
			s.mu.Unlock()
			continue
		}
		tx, err := s.payoutSend(sctx, common.HexToAddress(r.Member), value)
		s.mu.Lock()
		if err != nil {
			r.Status = "failed"
			r.Error = err.Error()
			ps.carry[r.Member] = new(big.Int).Set(due) // the due holds the carry-in already (F-501)
			r.CarryOut = due.String()
		} else {
			r.Tx = tx
			if carry.Sign() > 0 {
				r.Status = "partial"
				r.PaidWei = value.String()
				r.CarryOut = carry.String()
				ps.carry[r.Member] = carry
			}
			avail.Sub(avail, value)
			ps.pending = append(ps.pending, r)
			sent++
		}
		s.mu.Unlock()
	}
	log.Printf("payout run: window %d to %d closed under the %s rule at carry edge %d (the carried blocks %d to %d; %d weight moved back from paid windows, %d restored after a move-back, named, not paid again): the chain paid the key %s wei, the fee's part %s wei, the remainder %s wei, %d member(s) due, %d payout(s) sent", win.From, win.To, win.Rule, win.Edge, win.FirstBlock, win.LastBlock, win.MovedBackPaidWork, win.RestoredPaidWork, win.IncomeWei, win.FeeWei, win.RemainderWei, len(win.Rows), sent)
	s.payoutRewrite()
}

// payoutSend signs and broadcasts one value transfer from the key.
func (s *Service) payoutSend(ctx context.Context, to common.Address, value *big.Int) (string, error) {
	ps := s.payout
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	node := s.sourceNode()
	var nonceHex, priceHex string
	if err := node.Call(cctx, "eth_getTransactionCount", []interface{}{ps.address.Hex(), "pending"}, &nonceHex); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	nonce, ok := new(big.Int).SetString(strings.TrimPrefix(nonceHex, "0x"), 16)
	if !ok {
		return "", fmt.Errorf("nonce %q unreadable", nonceHex)
	}
	if err := node.Call(cctx, "eth_gasPrice", []interface{}{}, &priceHex); err != nil {
		return "", fmt.Errorf("gas price: %w", err)
	}
	price, ok := new(big.Int).SetString(strings.TrimPrefix(priceHex, "0x"), 16)
	if !ok || price.Sign() == 0 {
		price = big.NewInt(1_000_000_000)
	}
	tx := types.NewTx(&types.LegacyTx{Nonce: nonce.Uint64(), GasPrice: price, Gas: 21000, To: &to, Value: value})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(new(big.Int).SetUint64(s.chainID)), ps.key)
	if err != nil {
		return "", err
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		return "", err
	}
	var hash string
	if err := node.Call(cctx, "eth_sendRawTransaction", []interface{}{"0x" + fmt.Sprintf("%x", raw)}, &hash); err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	return hash, nil
}

// payoutRewrite writes the record whole (the windows with their rows' states).
func (s *Service) payoutRewrite() {
	ps := s.payout
	s.mu.Lock()
	windows := make([]PayoutWindow, len(ps.windows))
	copy(windows, ps.windows)
	s.mu.Unlock()
	tmp := ps.file + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("payout run: the record: %v", err)
		return
	}
	enc := json.NewEncoder(f)
	for _, w := range windows {
		_ = enc.Encode(w)
	}
	f.Close()
	_ = os.Rename(tmp, ps.file)
}

// Payouts is the public record for /payouts: the key, the windows (newest last), the pending and carried dues.
func (s *Service) Payouts() map[string]interface{} {
	ps := s.payout
	if ps == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	windows := ps.windows
	if len(windows) > 50 {
		windows = windows[len(windows)-50:]
	}
	carry := map[string]string{}
	for m, c := range ps.carry {
		if c.Sign() > 0 {
			carry[m] = c.String()
		}
	}
	return map[string]interface{}{"pool_key": strings.ToLower(ps.address.Hex()), "window_blocks": s.payoutWindowBlocks(), "last_close_height": ps.lastClose, "pending": len(ps.pending), "carried_wei": carry, "windows": windows, "min_payout_wei": s.cfg.MinPayoutWei,
		"rule": "every member's share is forwarded under the pool key, so the chain pays the key; every window_blocks the pool pays each member by transaction from the key its share of the chain's payment to the key over the window (its carried work less the fee's part on its charged shares, over all members' carried work), the fee's part named per window and kept by the key; a due under min_payout_wei carries to the next window; a failed payout is named and retried the next window; a member checks the key's chain payment against the transaction on its row"}
}

// carryFromRecord is F-501's restore (0.1.88): each member's carry after the record's last window, by assignment from
// its last row (a carried or failed row's due, which holds the carry-in; zero after a sent, paid or nothing-due row);
// never a sum over the rows. The record of 17 October 2026 proved the sum's compounding: nine restarts took one carry
// from 85,553 to 196,085,418 units while the chain paid the key 13,068 over the same windows.
func carryFromRecord(windows []PayoutWindow) map[string]*big.Int {
	carry := map[string]*big.Int{}
	for _, w := range windows {
		for _, r := range w.Rows {
			// 0.1.95: carry_out_wei is the carry by assignment whenever present (a partial row keeps its rest)
			if r.CarryOut != "" {
				if c, ok := new(big.Int).SetString(r.CarryOut, 10); ok && c.Sign() > 0 {
					carry[r.Member] = c
				} else {
					delete(carry, r.Member)
				}
				continue
			}
			switch r.Status {
			case "carried", "failed":
				if d, ok := new(big.Int).SetString(r.DueWei, 10); ok {
					carry[r.Member] = d
				}
			case "sent", "paid", "partial", "nothing due", "applied to the advance":
				delete(carry, r.Member)
			}
		}
	}
	return carry
}

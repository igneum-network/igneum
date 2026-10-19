package shares

// The pool client's share journal (the project's order of 11 October 2026, 14:3x
// UK, from the share-rate reading on devnet7: the protocol team's PA-44 journals
// a NODE's accepted shares across its restart; this journals the POOL's,
// the members' accepted shares, so a pool restart between a share's
// acceptance and the block that pays it loses nothing accepted and
// unpaid). Append-only JSON lines in one file, the pool's own: a line per
// accepted share with everything a re-verification needs (the job's
// header, height, document digest, nonce, mix, hash; the member's
// account, worker, key and weight), a line per carriage (the share
// sequence paid by a block, with the block's hash and height), a line per
// expiry (a share past the recency at a height). At the pool's start the
// journal is replayed: every accepted share without a carriage or expiry
// line is re-verified by the caller's verifier (the same recomputation a
// submission gets) and, when it verifies, enters the window again in its
// original order; a share that no longer verifies, or whose document the
// pool cannot prepare, is dropped by name and counted. Pruned as carried
// or expired: the file is rewritten without them when it is compacted,
// never in place.

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Line is one journal record. Kind is "accepted", "carried" or "expired".
type Line struct {
	Kind string `json:"kind"`
	UTC  string `json:"utc"`
	Seq  uint64 `json:"seq"` // the pool's own sequence for the share (the journal's, not the window's)
	// ChainID is the chain the share was accepted for (the public pool's switch from devnet8 to devnet9, 12 October
	// 2026: the replay of another chain's shares anchored the validator at that chain's heights and every new
	// share was refused as invalid); the replay drops a line of another chain by name
	ChainID   uint64 `json:"chain_id,omitempty"`
	Algorithm string `json:"algorithm,omitempty"` // accepted: the job's algorithm name the share is re-verified under
	Account   string `json:"account,omitempty"`   // accepted: the member's payout address, lower-cased
	// Member is the session's own address when the share was forwarded under another account (the pooled form,
	// the project's order of 14:3x UK, 15 October 2026: every member's share under the pool key so the chain's breadth
	// payment lands on it, the pool's pay to members by their work here); empty when Account is the member's.
	Member string `json:"member,omitempty"`
	Worker string `json:"worker,omitempty"`
	Key    string `json:"key,omitempty"` // the member's signing key (hex), empty on the plain form
	JobID  string `json:"job_id,omitempty"`
	Header string `json:"header,omitempty"` // the job's header hash (hex) the share was mined on
	Height uint64 `json:"height,omitempty"`
	Digest string `json:"digest,omitempty"` // the job's document digest (the validator's key)
	Params string `json:"params,omitempty"` // the job's document, verbatim, so the replay can prepare it
	Nonce  uint64 `json:"nonce,omitempty"`
	Mix    string `json:"mix,omitempty"`
	Hash   string `json:"hash,omitempty"`
	Weight uint64 `json:"weight,omitempty"`
	// FeeBps is the session's pool fee at acceptance and FeeWaived whether the pool's declared fee was waived for it
	// (the fee-waiver handshake: a miner reporting its dev fee on under a verified signature pays no pool fee)
	FeeBps    uint32 `json:"fee_bps"`
	FeeWaived bool   `json:"fee_waived,omitempty"`

	// Suppressed: on a refused line, the refusals of the same worker and reason not journaled since the key's last
	// line (the per-second bound, 0.1.74)
	Suppressed uint64 `json:"suppressed,omitempty"`
	// Ticket is a proof-carried share's proof (the graph coordinate's 168-byte cycle ticket, hex) on an accepted
	// line; the mix and hash carry the ticket hash (WHOLEWORK.md section 10)
	Ticket string `json:"ticket,omitempty"`
	// Anchor is the period's programme anchor (hex) the share was verified under at acceptance on a programmed
	// document, so the replay verifies exactly as the acceptance did (pool-b2's roll of 17:01Z, 15 October 2026:
	// the replay recomputed the anchor from the height and dropped eleven coordinate-16 shares by name).
	Anchor  string `json:"anchor,omitempty"`
	Block   string `json:"block,omitempty"`        // carried: the block's hash
	BlockAt uint64 `json:"block_height,omitempty"` // carried: the block's height
	// CarryN is the carried line's carry number (0.1.91, the security review's read of 0.1.90): the pay window's key written
	// in the line itself, so a retention rule that ever drops old lines keeps the numbering; a line without it (before
	// 0.1.91) takes the next number on replay
	CarryN uint64 `json:"carry_n,omitempty"`
	// forwarded (the share lane): the node's answer to the share the pool forwarded under the member's address, so
	// the pool's claim and the node's answer sit on one record a stranger reads (the settlement team's ask, 12
	// October 2026): the sealer's RPC, the share's chain identity (the account, the extra nonce, the nonce, the
	// height, the parent) and the answer by name
	Sealer     string `json:"sealer,omitempty"`
	ExtraNonce uint64 `json:"extra_nonce,omitempty"`
	Parent     string `json:"parent,omitempty"`
	NodeOK     bool   `json:"node_accepted,omitempty"`
	NodeReason string `json:"node_reason,omitempty"`
	// Step is the share's declared step on a forwarded line (PA-53's step form: the share mined at the node's target
	// over 2^step and weighed 2^step by the chain); absent at step 0.
	Step uint8 `json:"step,omitempty"`
	// Coord is the share's VectorWork coordinate on an accepted or forwarded line (absent on a one-form chain;
	// HasCoord marks a coordinate 0 share so it is never read as absent).
	Coord    uint8 `json:"coord,omitempty"`
	HasCoord bool  `json:"has_coord,omitempty"`
}

// Journal is the open file and its index of outstanding shares.
type Journal struct {
	mu   sync.Mutex
	path string
	f    *os.File
	// feeRows is the fee tally per account ("" pool-wide), every chain form
	feeRows map[string]*FeeRow
	// memberRows is the per-member ledger under a forwarding account: "<account>|<member>" to its work
	memberRows map[string]*MemberLedger
	// memberCoordRows: per member per coordinate under a forwarding account (0.1.85)
	memberCoordRows map[string]map[uint8]*MemberCoordWork
	// carriedByN: the carried lines by carry number (0.1.90, F-503: the pay window keyed by the carry's order); carriedN the
	// carry number per seq for the move-back; carryN the count
	carriedByN    map[uint64]*carriedRow
	carriedN      map[uint64]uint64 // seq -> its carry number, taken ONCE (0.1.94), never forgotten
	carryN        uint64
	movedBackPaid uint64
	// restoredPaid (0.1.94): the weight of lines carried again after a move-back from a window already paid; named
	// on the next window, never paid again
	restoredPaid uint64
	// churnNamed (0.1.94): the seqs whose churn after a paid close has been named (moved back, then restored), once
	// a seq each way; further churn of a paid seq names nothing
	churnNamed map[uint64]uint8
	// feeVerdicts is the newest fee_window line per member and worker (the verifiable waiver's standing verdict,
	// restored by the next generation so a roll neither frees nor double-charges a window)
	feeVerdicts map[string]Line
	// readTo is the file offset the index has read to (the handover's catch-up reads from it)
	readTo int64
	next   uint64
	// outstanding: accepted shares with no carriage or expiry line yet, by sequence
	outstanding map[uint64]Line
	order       []uint64
	Stats       JournalStats
	// settled: per account (lower-cased) and share height, the counts already written from the node's account
	// (carried, expired by reorg, expired at the window), so a later read of the same account writes only the rise
	settled map[string]map[uint64][3]uint64
	// settledSeqs: the sequences settled per account, height and slot, in the order written, so a node that moves a
	// share back (0.9.4 puts carried shares back at a reorg and re-carries expired ones: the node team's finding of
	// 07:5xZ, 12 October 2026, 199 heights apart from the chain under the rise-only form) is mirrored by an
	// "uncarried" or "unexpired" line per share and the share returns to outstanding
	settledSeqs map[string]map[uint64][3][]uint64
	// lines: every accepted line by sequence, so a settled share can return to outstanding
	lines map[uint64]Line
	// byAccount: accepted shares per account (lower-cased) and share height, the public per-address page's count
	// (the E1 study's finding 5, 12 October 2026: no served page named a miner's own shares)
	byAccount map[string]map[uint64]uint64
	// byCoord: accepted shares per account, coordinate and share height (VectorWork's split on the per-address page)
	byCoord map[string]map[uint8]map[uint64]uint64
	// coordLedger: the outside review's per-coordinate accounting (the project's order of 01:3x UK, 13 October 2026): per coordinate,
	// pool-wide ("" account) and per account, the accepted work, the paid (carried) work, the orphans (expired by
	// reorg), the window expiries and the by-name expiries, by count and by weight (2^step), so geometric-mean
	// security never becomes additive payment that rewards flooding one coordinate
	coordLedger map[string]map[uint8]*CoordLedger
	// heldAt: per account and sealer URL, the accepted shares that sealer accepted on forward and that are still
	// outstanding (the sealer's own pooled count for the address, PA-53's per-address per-sealer bound: MaxShares;
	// the VectorWork box soak of 05:0xZ, 13 October 2026, where a sealer refused 532 receipted shares at it)
	heldAt map[string]map[string]map[uint64]struct{}
}

// HeldAt is the count of the account's outstanding shares the sealer accepted (its pooled count for the address
// as the journal approximates it: a share carried by another sealer's block leaves it here when it settles).
func (j *Journal) HeldAt(account, sealer string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.heldAt[strings.ToLower(account)][sealer])
}

func (j *Journal) hold(account, sealer string, seq uint64) {
	if j.heldAt == nil {
		j.heldAt = map[string]map[string]map[uint64]struct{}{}
	}
	if j.heldAt[account] == nil {
		j.heldAt[account] = map[string]map[uint64]struct{}{}
	}
	if j.heldAt[account][sealer] == nil {
		j.heldAt[account][sealer] = map[uint64]struct{}{}
	}
	j.heldAt[account][sealer][seq] = struct{}{}
}

func (j *Journal) release(account string, seq uint64) {
	for _, m := range j.heldAt[account] {
		delete(m, seq)
	}
}

// CoordLedger is one coordinate's accounting as the journal holds it: counts and work (the sum of the shares'
// weights, 2^step each) accepted by the pool, carried (paid) by the chain, expired by reorg (orphaned), expired at
// the window's end and expired by the pool's name (no sealer named the share within twice the recency).
type CoordLedger struct {
	Accepted          uint64 `json:"accepted"`
	AcceptedWork      uint64 `json:"accepted_work"`
	Carried           uint64 `json:"carried"`
	CarriedWork       uint64 `json:"paid_work"`
	ExpiredReorg      uint64 `json:"orphaned"`
	ExpiredReorgWork  uint64 `json:"orphaned_work"`
	ExpiredWindow     uint64 `json:"expired_window"`
	ExpiredWindowWork uint64 `json:"expired_window_work"`
	ExpiredByName     uint64 `json:"expired_by_name"`
	ExpiredByNameWork uint64 `json:"expired_by_name_work"`
	// FeeWork is the pool's fee on the paid work at each share's own session fee (a waived session's shares add
	// nothing); CarriedWaived and CarriedCharged count the paid shares by which applied.
	FeeWork        uint64 `json:"fee_work"`
	CarriedWaived  uint64 `json:"paid_waived"`
	CarriedCharged uint64 `json:"paid_charged"`
}

// FeeRow is an address's fee tally across every chain form: the shares accepted and paid by whether the pool's
// declared fee applied to their session, and the fee work on the paid shares at each share's own session fee.
type FeeRow struct {
	AcceptedWaived  uint64 `json:"accepted_waived"`
	AcceptedCharged uint64 `json:"accepted_charged"`
	// AcceptedUnknown and PaidUnknown are lines written before the session's fee rode the journal (no fee fields):
	// their fee was the pool's declared fee of their day, not readable from the line
	AcceptedUnknown uint64 `json:"accepted_unknown"`
	PaidWaived      uint64 `json:"paid_waived"`
	PaidCharged     uint64 `json:"paid_charged"`
	PaidUnknown     uint64 `json:"paid_unknown"`
	FeeWork         uint64 `json:"fee_work"`
}

func (j *Journal) feeRow(account string) *FeeRow {
	if j.feeRows == nil {
		j.feeRows = map[string]*FeeRow{}
	}
	r := j.feeRows[account]
	if r == nil {
		r = &FeeRow{}
		j.feeRows[account] = r
	}
	return r
}

// feeOn is the pool's fee on one share's work at the share's own session terms: nothing on a waived line whatever bps it
// names (a fee session's and a verified member's lines carry bps 0 and waived, a fee_window line the declared bps beside the
// verdict), else the work at the session's bps; the one rule for the fee row, the coordinate ledger and the member ledger
// (F-160's second test, 15 October 2026: the member ledger charged a waived line's bps, so a waived member's due in the
// pooled form's payout run would have carried the fee's part had a waived line ever named a bps above zero)
func feeOn(a Line, w uint64) uint64 {
	if a.FeeWaived {
		return 0
	}
	return w * uint64(a.FeeBps) / 10000
}

// feeAccount applies one accepted (slot -2), paid (slot 0) or moved-back paid share to the fee rows (pool-wide and
// the account's).
func (j *Journal) feeAccount(a Line, slot int, back bool) {
	w := a.Weight
	if w == 0 {
		w = 1
	}
	waived, charged := a.FeeWaived, !a.FeeWaived && a.FeeBps > 0
	for _, acc := range []string{"", strings.ToLower(a.Account)} {
		r := j.feeRow(acc)
		var acceptedN, paidN *uint64
		switch {
		case waived:
			acceptedN, paidN = &r.AcceptedWaived, &r.PaidWaived
		case charged:
			acceptedN, paidN = &r.AcceptedCharged, &r.PaidCharged
		default:
			acceptedN, paidN = &r.AcceptedUnknown, &r.PaidUnknown
		}
		fee := feeOn(a, w)
		switch {
		case slot == -2:
			*acceptedN++
		case slot == 0 && back:
			if r.FeeWork >= fee {
				r.FeeWork -= fee
			}
			if *paidN > 0 {
				*paidN--
			}
		case slot == 0:
			r.FeeWork += fee
			*paidN++
		}
	}
}

// Fees is an address's fee row (pool-wide when account is empty).
func (j *Journal) Fees(account string) FeeRow {
	j.mu.Lock()
	defer j.mu.Unlock()
	if r := j.feeRows[strings.ToLower(account)]; r != nil {
		return *r
	}
	return FeeRow{}
}

func (j *Journal) coordRow(account string, coord uint8) *CoordLedger {
	if j.coordLedger == nil {
		j.coordLedger = map[string]map[uint8]*CoordLedger{}
	}
	if j.coordLedger[account] == nil {
		j.coordLedger[account] = map[uint8]*CoordLedger{}
	}
	r := j.coordLedger[account][coord]
	if r == nil {
		r = &CoordLedger{}
		j.coordLedger[account][coord] = r
	}
	return r
}

// coordAccount applies one settled or moved-back share to the coordinate's rows (the pool-wide row and the
// account's): slot 0 carried, 1 expired by reorg, 2 expired at the window, -1 expired by name; back = the move back.
// MemberLedger is one member's work under a forwarding account (the pooled form): accepted and carried share
// weights, the fee's part of the carried weight (charged shares) and the waived part.
type MemberLedger struct {
	Member         string `json:"member"`
	Accepted       uint64 `json:"accepted"`
	AcceptedWork   uint64 `json:"accepted_work"`
	Carried        uint64 `json:"carried"`
	CarriedWork    uint64 `json:"paid_work"`
	FeeWork        uint64 `json:"fee_work"`
	CarriedWaived  uint64 `json:"carried_waived"`
	CarriedCharged uint64 `json:"carried_charged"`
}

func (j *Journal) memberAccount(a Line, slot int, back bool, accepted bool) {
	if a.Member == "" {
		return
	}
	if j.memberRows == nil {
		j.memberRows = map[string]*MemberLedger{}
	}
	k := strings.ToLower(a.Account) + "|" + strings.ToLower(a.Member)
	r := j.memberRows[k]
	if r == nil {
		r = &MemberLedger{Member: strings.ToLower(a.Member)}
		j.memberRows[k] = r
	}
	w := a.Weight
	if w == 0 {
		w = 1
	}
	if accepted {
		r.Accepted++
		r.AcceptedWork += w
		return
	}
	if slot != 0 {
		return
	}
	fee := feeOn(a, w)
	cnt := &r.CarriedCharged
	if a.FeeWaived || a.FeeBps == 0 {
		cnt = &r.CarriedWaived
	}
	if back {
		if r.Carried > 0 {
			r.Carried--
		}
		if r.CarriedWork >= w {
			r.CarriedWork -= w
		}
		if r.FeeWork >= fee {
			r.FeeWork -= fee
		}
		if *cnt > 0 {
			*cnt--
		}
		return
	}
	r.Carried++
	r.CarriedWork += w
	r.FeeWork += fee
	*cnt++
}

// MemberCoordWork is one member's carried share weight and fee work on one coordinate under a forwarding account
// (0.1.85, F-497: the payout per coordinate).
type MemberCoordWork struct {
	CarriedWork uint64 `json:"paid_work"`
	FeeWork     uint64 `json:"fee_work"`
}

// memberCoordAccount applies one paid or moved-back paid share to the member's row on its coordinate (0.1.85).
func (j *Journal) memberCoordAccount(a Line, back bool) {
	if a.Member == "" || !a.HasCoord {
		return
	}
	if j.memberCoordRows == nil {
		j.memberCoordRows = map[string]map[uint8]*MemberCoordWork{}
	}
	k := strings.ToLower(a.Account) + "|" + strings.ToLower(a.Member)
	if j.memberCoordRows[k] == nil {
		j.memberCoordRows[k] = map[uint8]*MemberCoordWork{}
	}
	r := j.memberCoordRows[k][a.Coord]
	if r == nil {
		r = &MemberCoordWork{}
		j.memberCoordRows[k][a.Coord] = r
	}
	w := a.Weight
	if w == 0 {
		w = 1
	}
	fee := feeOn(a, w)
	if back {
		if r.CarriedWork >= w {
			r.CarriedWork -= w
		}
		if r.FeeWork >= fee {
			r.FeeWork -= fee
		}
		return
	}
	r.CarriedWork += w
	r.FeeWork += fee
}

// HeightWork is a member's carried share weight and fee work on one coordinate in one carrying block (0.1.89, F-500:
// the pay window's ledger is the journal's rows by height, never a cumulative delta against a baseline).
type HeightWork struct {
	Work, FeeWork uint64
}

// carriedRow is one carried line as the pay window counts it (0.1.90, F-503): its carry number (the order the
// journal applied the carried line in, never a height), the member's key under its forwarding account, the
// coordinate, the weight and the fee work, and the carrying block's height for the income read.
type carriedRow struct {
	n       uint64
	key     string // account|member
	coord   uint8
	work    uint64
	feeWork uint64
	height  uint64
	moved   bool // moved back by a reorganisation
}

// carriedAccount records one carried line under the next carry number (0.1.90, F-503: the pay window is keyed by the
// carry's order in the journal, so no carry can land behind a close: a block credited after a close, whatever its
// height, lands in the next window). A move-back marks the row; its weight comes out of the window its carry number
// fell in (a paid window's row stands, counted on the next window's record).
func (j *Journal) carriedAccount(a Line, height uint64, carryN uint64) {
	// the number FIRST (the security review's read of 0.1.90): a carried line whose accepted line the replay did not keep
	// still takes its number, so the numbering is the same in every replay and the record's edge keeps its meaning;
	// the row is written only for a line with an account (a numbered row with an empty key is skipped by the prefix)
	w := a.Weight
	if w == 0 {
		w = 1
	}
	// 0.1.94: a seq takes a carry number once; a line carried again after a move-back restores its row when the row
	// still stands (the open window: paid once there), and is named as restored paid work when the row was pruned at a
	// paid close (never numbered or paid again); the same in every replay, by the file's order alone
	if n, ok := j.carriedN[a.Seq]; ok {
		if r := j.carriedByN[n]; r != nil {
			r.moved = false
		} else if j.churnNamed[a.Seq]&2 == 0 {
			j.churnNamed[a.Seq] |= 2
			j.restoredPaid += w
		}
		return
	}
	if carryN > 0 && carryN > j.carryN {
		j.carryN = carryN
	} else {
		j.carryN++
	}
	if j.carriedN == nil {
		j.carriedN = map[uint64]uint64{}
		j.churnNamed = map[uint64]uint8{}
	}
	j.carriedN[a.Seq] = j.carryN
	if a.Account == "" {
		return
	}
	coord := uint8(0)
	if a.HasCoord {
		coord = a.Coord
	}
	if j.carriedByN == nil {
		j.carriedByN = map[uint64]*carriedRow{}
	}
	j.carriedByN[j.carryN] = &carriedRow{n: j.carryN, key: strings.ToLower(a.Account) + "|" + strings.ToLower(a.Member), coord: coord, work: w, feeWork: feeOn(a, w), height: height}
}

// movedBack marks a carried line's row as moved back (its carry number kept so the window it fell in is known); a
// row already pruned (its window paid) is counted by weight under movedBackPaid, named on the next window.
func (j *Journal) movedBack(a Line, seq uint64) {
	n, ok := j.carriedN[seq]
	if !ok {
		return
	}
	// the seq keeps its number (0.1.94): a later carried line restores the row or is named as restored paid work
	if r := j.carriedByN[n]; r != nil {
		if r.moved {
			return // moved back already, named once
		}
		r.moved = true
		return
	}
	if j.churnNamed == nil {
		j.churnNamed = map[uint64]uint8{}
	}
	if j.churnNamed[seq]&1 != 0 {
		return // named once a seq
	}
	j.churnNamed[seq] |= 1
	w := a.Weight
	if w == 0 {
		w = 1
	}
	j.movedBackPaid += w
}

// CarryEdge is the journal's carry count now: the edge a pay window closes at (0.1.90).
func (j *Journal) CarryEdge() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.carryN
}

// CarryEdgeAtHeight is the carry count of the lines carried in blocks at or below height (the edge of a record closed
// by height before 0.1.90, taken once at the roll; the lines carried after that close with a height inside it are the
// recompute's, not a later window's).
func (j *Journal) CarryEdgeAtHeight(height uint64) uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	var edge uint64
	for n, r := range j.carriedByN {
		if r.height <= height && n > edge {
			edge = n
		}
	}
	return edge
}

// WindowSpan is what a pay window holds between two carry edges (0.1.90): the carried weight and fee work per member
// per coordinate (the member "" the pool-wide row), the carrying blocks' first and last height (the income's span
// in the next version), and the weight moved back from windows already paid (named, never paid twice).
type WindowSpan struct {
	Work                    map[string]map[uint8]HeightWork
	FirstHeight, LastHeight uint64
	MovedBackPaidWork       uint64
	// RestoredPaidWork (0.1.94): the weight carried again after a move-back from a paid window, named, never paid
	RestoredPaidWork uint64
}

// WindowWork is the pay window (lastEdge, edge] under a forwarding account (0.1.90): the carried lines with a carry
// number in the span that were not moved back, per member per coordinate; a line moved back from a paid window
// (its number at or under lastEdge) is counted under MovedBackPaidWork and then forgotten.
func (j *Journal) WindowWork(account string, lastEdge, edge uint64) WindowSpan {
	j.mu.Lock()
	defer j.mu.Unlock()
	pre := strings.ToLower(account) + "|"
	// a pure read (the security review's note on 0.1.91): the naming of the moved-back rows and the pruned move-backs is
	// idempotent until the prune after a successful close, so a held close reads the same figure again
	out := WindowSpan{Work: map[string]map[uint8]HeightWork{}, MovedBackPaidWork: j.movedBackPaid, RestoredPaidWork: j.restoredPaid}
	for n, r := range j.carriedByN {
		if !strings.HasPrefix(r.key, pre) {
			continue
		}
		if r.moved {
			if n <= lastEdge {
				// moved back from a window already paid: named, never paid twice (forgotten at the prune)
				out.MovedBackPaidWork += r.work
			}
			// moved back while its carry is in the open window: never paid, nothing to name (the security review's read of
			// 0.1.90); forgotten at the prune
			continue
		}
		if n <= lastEdge || n > edge {
			continue
		}
		member := strings.TrimPrefix(r.key, pre)
		for _, m := range []string{"", member} {
			if out.Work[m] == nil {
				out.Work[m] = map[uint8]HeightWork{}
			}
			t := out.Work[m][r.coord]
			t.Work += r.work
			t.FeeWork += r.feeWork
			out.Work[m][r.coord] = t
		}
		if out.FirstHeight == 0 || r.height < out.FirstHeight {
			out.FirstHeight = r.height
		}
		if r.height > out.LastHeight {
			out.LastHeight = r.height
		}
	}
	return out
}

// PruneCarriedThrough forgets the rows with a carry number at or under edge (the windows closed and paid), bounding the
// index at every close; a row moved back from a paid window is kept until the next window names it.
func (j *Journal) PruneCarriedThrough(edge uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for n := range j.carriedByN {
		if n <= edge {
			delete(j.carriedByN, n)
		}
	}
	j.movedBackPaid = 0
	j.restoredPaid = 0
}

// MemberCoordLedgers is every member's carried and fee work per coordinate under a forwarding account (0.1.85), keyed
// by the member's lower-cased address.
func (j *Journal) MemberCoordLedgers(account string) map[string]map[uint8]MemberCoordWork {
	j.mu.Lock()
	defer j.mu.Unlock()
	pre := strings.ToLower(account) + "|"
	out := map[string]map[uint8]MemberCoordWork{}
	for k, rows := range j.memberCoordRows {
		if !strings.HasPrefix(k, pre) {
			continue
		}
		m := map[uint8]MemberCoordWork{}
		for c, r := range rows {
			m[c] = *r
		}
		out[strings.TrimPrefix(k, pre)] = m
	}
	return out
}

// MemberLedgers is the per-member ledger under a forwarding account.
func (j *Journal) MemberLedgers(account string) []MemberLedger {
	j.mu.Lock()
	defer j.mu.Unlock()
	pre := strings.ToLower(account) + "|"
	out := []MemberLedger{}
	for k, r := range j.memberRows {
		if strings.HasPrefix(k, pre) {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(x, y int) bool { return out[x].Member < out[y].Member })
	return out
}

func (j *Journal) coordAccount(a Line, slot int, back bool) {
	if !a.HasCoord {
		return
	}
	w := a.Weight
	if w == 0 {
		w = 1
	}
	for _, acc := range []string{"", strings.ToLower(a.Account)} {
		r := j.coordRow(acc, a.Coord)
		var n, nw *uint64
		switch slot {
		case 0:
			n, nw = &r.Carried, &r.CarriedWork
			fee := feeOn(a, w)
			cnt := &r.CarriedCharged
			if a.FeeWaived || a.FeeBps == 0 {
				cnt = &r.CarriedWaived
			}
			if back {
				if r.FeeWork >= fee {
					r.FeeWork -= fee
				}
				if *cnt > 0 {
					*cnt--
				}
			} else {
				r.FeeWork += fee
				*cnt++
			}
		case 1:
			n, nw = &r.ExpiredReorg, &r.ExpiredReorgWork
		case 2:
			n, nw = &r.ExpiredWindow, &r.ExpiredWindowWork
		default:
			n, nw = &r.ExpiredByName, &r.ExpiredByNameWork
		}
		if back {
			if *n > 0 {
				*n--
			}
			if *nw >= w {
				*nw -= w
			}
		} else {
			*n++
			*nw += w
		}
	}
}

// CoordLedgers is the per-coordinate accounting: the pool-wide rows (account "") or one account's (lower-cased).
func (j *Journal) CoordLedgers(account string) map[uint8]CoordLedger {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[uint8]CoordLedger{}
	for c, r := range j.coordLedger[strings.ToLower(account)] {
		out[c] = *r
	}
	return out
}

// AccountByCoord is the account's accepted shares per coordinate from minHeight up, as {coord: count}.
func (j *Journal) AccountByCoord(account string, minHeight uint64) map[uint8]uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[uint8]uint64{}
	for c, hs := range j.byCoord[strings.ToLower(account)] {
		for h, n := range hs {
			if h >= minHeight {
				out[c] += n
			}
		}
	}
	return out
}

// AccountHeight is one height of an account's shares as the journal holds them: accepted by the pool,
// outstanding (no carriage or expiry line yet), carried by the chain, expired by reorg, expired at the window's
// end, and expired by the pool's own name (no node named the share within twice the recency).
type AccountHeight struct {
	Height        uint64 `json:"height"`
	Accepted      uint64 `json:"accepted"`
	Outstanding   uint64 `json:"outstanding"`
	Carried       uint64 `json:"carried"`
	ExpiredReorg  uint64 `json:"expired_reorg"`
	ExpiredWindow uint64 `json:"expired_window"`
	ExpiredByName uint64 `json:"expired_by_name"`
}

// Account is the per-height view of one account's shares from minHeight up, ascending.
func (j *Journal) Account(account string, minHeight uint64) []AccountHeight {
	j.mu.Lock()
	defer j.mu.Unlock()
	a := strings.ToLower(account)
	out := []AccountHeight{}
	for h, n := range j.byAccount[a] {
		if h < minHeight {
			continue
		}
		row := AccountHeight{Height: h, Accepted: n}
		if st, has := j.settled[a]; has {
			row.Carried, row.ExpiredReorg, row.ExpiredWindow = st[h][0], st[h][1], st[h][2]
		}
		out = append(out, row)
	}
	for _, s := range j.order {
		l := j.outstanding[s]
		if strings.ToLower(l.Account) != a || l.Height < minHeight {
			continue
		}
		for i := range out {
			if out[i].Height == l.Height {
				out[i].Outstanding++
			}
		}
	}
	for i := range out {
		settled := out[i].Outstanding + out[i].Carried + out[i].ExpiredReorg + out[i].ExpiredWindow
		if out[i].Accepted > settled {
			out[i].ExpiredByName = out[i].Accepted - settled
		}
	}
	sort.Slice(out, func(x, y int) bool { return out[x].Height < out[y].Height })
	return out
}

// JournalStats is the health line's view.
type JournalStats struct {
	Appended    uint64 `json:"appended"`
	Carried     uint64 `json:"carried"`
	Expired     uint64 `json:"expired"`
	Replayed    uint64 `json:"replayed"`
	Dropped     uint64 `json:"dropped"`
	Outstanding int    `json:"outstanding"`
	// MovedBack counts the shares the node moved back (uncarried at a reorg, unexpired when re-carried).
	MovedBack uint64 `json:"moved_back"`
}

// Verifier re-verifies a journalled share at replay: it prepares the
// share's document under its digest at its height and recomputes the mix
// and the result for the header and nonce; a mismatch with the recorded
// mix and hash, or a document it cannot prepare, drops the share.
type Verifier interface {
	Prepare(algorithm string, params []byte, digest string, height uint64) error
	Verify(algorithm string, header [32]byte, height uint64, nonce uint64) ([32]byte, [32]byte, error)
}

// AnchoredVerifier is a Verifier that can verify a share under the anchor its line recorded.
type AnchoredVerifier interface {
	VerifyUnderAnchor(algorithm string, anchor [32]byte, header [32]byte, height uint64, nonce uint64) ([32]byte, [32]byte, error)
}

// ErrUnverifiable marks a share the replay could not verify NOW for a reason that is not the share's (the anchor
// unreadable, the document unprepared): the line is kept as journalled and named, never dropped.
var ErrUnverifiable = errors.New("unverifiable now")

// OpenJournal opens or creates the journal and reads its lines into the
// index (nothing is replayed into a window until Replay).
func OpenJournal(path string) (*Journal, error) {
	j := &Journal{path: path, outstanding: map[uint64]Line{}}
	// O_APPEND: during a handover two generations hold the file for a moment, and every write lands at the end
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	j.f = f
	if _, err := j.readFrom(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	j.Stats.Outstanding = len(j.outstanding)
	return j, nil
}

// readFrom folds the file's lines from the offset into the index and records the offset read to.
func (j *Journal) readFrom(from int64) (int, error) {
	rf, err := os.Open(j.path)
	if err != nil {
		return 0, err
	}
	defer rf.Close()
	if _, err := rf.Seek(from, io.SeekStart); err != nil {
		return 0, err
	}
	r := bufio.NewReader(rf)
	n := 0
	pos := from
	for {
		raw, err := r.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			var l Line
			if json.Unmarshal(raw, &l) == nil {
				j.apply(l)
				n++
			}
			pos += int64(len(raw))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return n, err
		}
	}
	j.readTo = pos
	return n, nil
}

// Lock takes the journal's exclusive advisory lock, waiting for the generation that holds it (the handover form,
// the project's order of 21:2x UK, 13 October 2026: the new generation listens beside the old and accepts shares only
// once the old has let the journal go). The lock rides the open file and is released when the process exits.
func (j *Journal) Lock() error {
	return syscall.Flock(int(j.f.Fd()), syscall.LOCK_EX)
}

// CatchUp reads the lines another generation appended after this index was built, folds them in, and replays
// the newly outstanding ones into the window as ReplayFor does (the same verifier, the same expiries), so the
// new generation's window and sequence continue the old one's.
func (j *Journal) CatchUp(chainID uint64, v Verifier, add func(Line)) (lines, replayed, dropped int, err error) {
	j.mu.Lock()
	before := map[uint64]struct{}{}
	for seq := range j.outstanding {
		before[seq] = struct{}{}
	}
	from := j.readTo
	lines, err = j.readFrom(from)
	if err != nil {
		j.mu.Unlock()
		return lines, 0, 0, err
	}
	var fresh []Line
	for _, seq := range j.order {
		if _, old := before[seq]; !old {
			fresh = append(fresh, j.outstanding[seq])
		}
	}
	j.mu.Unlock()
	if len(fresh) == 0 {
		return lines, 0, 0, nil
	}
	replayed, dropped, err = j.replayLines(fresh, chainID, v, add)
	return lines, replayed, dropped, err
}

// apply folds one line into the index (the caller holds the lock or is
// the constructor).
func (j *Journal) apply(l Line) {
	switch l.Kind {
	case "fee_window":
		if j.feeVerdicts == nil {
			j.feeVerdicts = map[string]Line{}
		}
		j.feeVerdicts[strings.ToLower(l.Account)+"|"+l.Worker] = l
	case "accepted":
		if l.Seq > j.next {
			j.next = l.Seq
		}
		if j.lines == nil {
			j.lines = map[uint64]Line{}
		}
		if _, seen := j.lines[l.Seq]; !seen && l.Account != "" {
			if j.byAccount == nil {
				j.byAccount = map[string]map[uint64]uint64{}
			}
			a := strings.ToLower(l.Account)
			if j.byAccount[a] == nil {
				j.byAccount[a] = map[uint64]uint64{}
			}
			j.byAccount[a][l.Height]++
			if l.HasCoord {
				if j.byCoord == nil {
					j.byCoord = map[string]map[uint8]map[uint64]uint64{}
				}
				if j.byCoord[a] == nil {
					j.byCoord[a] = map[uint8]map[uint64]uint64{}
				}
				if j.byCoord[a][l.Coord] == nil {
					j.byCoord[a][l.Coord] = map[uint64]uint64{}
				}
				j.byCoord[a][l.Coord][l.Height]++
				w := l.Weight
				if w == 0 {
					w = 1
				}
				for _, acc := range []string{"", a} {
					r := j.coordRow(acc, l.Coord)
					r.Accepted++
					r.AcceptedWork += w
				}
			}
		}
		j.feeAccount(l, -2, false)
		if _, seen := j.lines[l.Seq]; !seen {
			j.memberAccount(l, 0, false, true)
		}
		j.lines[l.Seq] = l
		if _, dup := j.outstanding[l.Seq]; !dup {
			j.outstanding[l.Seq] = l
			j.order = append(j.order, l.Seq)
		}
	case "uncarried", "unexpired":
		// the node moved the share back: it is outstanding again (its accepted line kept), oldest first in the order,
		// and the settled index forgets it
		if a, has := j.lines[l.Seq]; has {
			if _, dup := j.outstanding[l.Seq]; !dup {
				j.outstanding[l.Seq] = a
				j.order = append(j.order, l.Seq)
				sort.Slice(j.order, func(x, y int) bool { return j.order[x] < j.order[y] })
			}
			j.unsettle(strings.ToLower(a.Account), l.Height, l.Seq)
			if l.Kind == "uncarried" {
				j.coordAccount(a, 0, true)
				j.feeAccount(a, 0, true)
				j.memberAccount(a, 0, true, false)
				j.memberCoordAccount(a, true)
				j.movedBack(a, l.Seq)
			} else {
				j.coordAccount(a, 1, true)
			}
		}
	case "forwarded":
		if a, has := j.lines[l.Seq]; has && l.NodeOK && l.Sealer != "" {
			if _, out := j.outstanding[l.Seq]; out {
				j.hold(strings.ToLower(a.Account), l.Sealer, l.Seq)
			}
		}
	case "carried", "expired":
		if a, has := j.lines[l.Seq]; has {
			j.release(strings.ToLower(a.Account), l.Seq)
		}
		if _, has := j.outstanding[l.Seq]; has {
			delete(j.outstanding, l.Seq)
			for i, s := range j.order {
				if s == l.Seq {
					j.order = append(j.order[:i], j.order[i+1:]...)
					break
				}
			}
		}
		if a, has := j.lines[l.Seq]; has && l.Sealer != "" {
			j.settle(strings.ToLower(a.Account), l.Height, settleSlot(l), l.Seq)
		}
		if a, has := j.lines[l.Seq]; has {
			// a carried line is carried whether a sealer's account named it (the settle) or the pool's own block did
			// (Carried: the block the pool sealed with the shares in it); an expired line names its slot by its
			// sealer's reason, expired by name without one (F-160's second test, 15 October 2026: the block-carried
			// form counted under expired by name in the per-coordinate and per-member ledgers)
			slot := -1
			if l.Kind == "carried" || l.Sealer != "" {
				slot = settleSlot(l)
			}
			j.coordAccount(a, slot, false)
			j.memberAccount(a, slot, false, false)
			if slot == 0 {
				j.memberCoordAccount(a, false)
				j.carriedAccount(a, l.BlockAt, l.CarryN)
			}
			if slot == 0 {
				j.feeAccount(a, 0, false)
			}
		}
	}
}

// settleSlot is the settled index's slot of a carried or expired line from the node's account: 0 carried, 1
// expired by reorg, 2 expired at the window; -1 for an expiry the pool wrote by name (not the node's count).
func settleSlot(l Line) int {
	switch {
	case l.Kind == "carried":
		return 0
	case strings.Contains(l.NodeReason, "by reorg"):
		return 1
	case strings.Contains(l.NodeReason, "window"):
		return 2
	}
	return -1
}

// settle records a share settled from the node's account at a height and slot (the index the rise-and-fall
// reconcile reads: Settle).
func (j *Journal) settle(account string, h uint64, slot int, seq uint64) {
	if slot < 0 {
		return
	}
	if j.settled == nil {
		j.settled = map[string]map[uint64][3]uint64{}
	}
	if j.settledSeqs == nil {
		j.settledSeqs = map[string]map[uint64][3][]uint64{}
	}
	if j.settled[account] == nil {
		j.settled[account] = map[uint64][3]uint64{}
	}
	if j.settledSeqs[account] == nil {
		j.settledSeqs[account] = map[uint64][3][]uint64{}
	}
	d := j.settled[account][h]
	d[slot]++
	j.settled[account][h] = d
	ss := j.settledSeqs[account][h]
	ss[slot] = append(ss[slot], seq)
	j.settledSeqs[account][h] = ss
}

// unsettle forgets a share the node moved back, whichever slot held it.
func (j *Journal) unsettle(account string, h uint64, seq uint64) {
	if j.settledSeqs == nil || j.settledSeqs[account] == nil {
		return
	}
	ss := j.settledSeqs[account][h]
	for slot := 0; slot < 3; slot++ {
		for i, s := range ss[slot] {
			if s == seq {
				ss[slot] = append(ss[slot][:i], ss[slot][i+1:]...)
				j.settledSeqs[account][h] = ss
				d := j.settled[account][h]
				if d[slot] > 0 {
					d[slot]--
				}
				j.settled[account][h] = d
				return
			}
		}
	}
}

func (j *Journal) write(l Line) error {
	l.UTC = time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return j.f.Sync()
}

// Accepted journals a share the pool accepted and returns its journal
// sequence; the share is outstanding until Carried or Expired names it.
func (j *Journal) Accepted(l Line) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.next++
	l.Kind, l.Seq = "accepted", j.next
	if err := j.write(l); err != nil {
		return 0, err
	}
	j.apply(l)
	j.Stats.Appended++
	j.Stats.Outstanding = len(j.outstanding)
	return l.Seq, nil
}

// Forwarded journals the node's answer to a share the pool forwarded under
// the member's address on the share lane (kind "forwarded": the sealer, the
// share's chain identity and the answer by name); it is a record beside the
// accepted line, never an outstanding share, so the replay ignores it.
// Note appends an informational line (a fee window's verdict) with no seq and no effect on the index: a reader
// sees the figures beside the shares they cover.
func (j *Journal) Note(l Line) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.apply(l)
	return j.write(l)
}

// FeeVerdicts is the newest fee_window line per "<member>|<worker>" on the journal.
func (j *Journal) FeeVerdicts() map[string]Line {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make(map[string]Line, len(j.feeVerdicts))
	for k, v := range j.feeVerdicts {
		out[k] = v
	}
	return out
}

func (j *Journal) Forwarded(l Line) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	l.Kind = "forwarded"
	j.apply(l) // the sealer's hold on the share (the per-sealer bound's count)
	return j.write(l)
}

// Carried journals that the shares with these sequences were paid by a
// block (the window a found block was distributed over).
func (j *Journal) Carried(seqs []uint64, blockHash string, height uint64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, s := range seqs {
		if _, has := j.outstanding[s]; !has {
			continue
		}
		l := Line{Kind: "carried", Seq: s, Block: blockHash, BlockAt: height, CarryN: j.carryN + 1}
		if err := j.write(l); err != nil {
			return err
		}
		j.apply(l)
		j.Stats.Carried++
	}
	j.Stats.Outstanding = len(j.outstanding)
	return nil
}

// Expire journals every outstanding share whose height is below minHeight
// (the recency: a share older than the window a block can pay is never
// paid, so it is pruned by name, counted, never silently).
func (j *Journal) Expire(minHeight uint64) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, s := range append([]uint64(nil), j.order...) {
		l := j.outstanding[s]
		if l.Height >= minHeight {
			continue
		}
		if err := j.write(Line{Kind: "expired", Seq: s, Height: l.Height}); err != nil {
			return n, err
		}
		j.apply(Line{Kind: "expired", Seq: s})
		j.Stats.Expired++
		n++
	}
	j.Stats.Outstanding = len(j.outstanding)
	return n, nil
}

// Settle writes the node's own account of an address's shares onto the
// journal (the share-lane form, where the chain pays and the pool never
// finds a block; the settlement team's finding of 12 October 2026: the
// pool's recency expiry named 452 paid shares as expired). carried, reorg
// and window are the node's counts by SHARE height for the address
// (igneum_shareAccount's by-height maps). The journal marks its outstanding
// shares of that address at each height carried up to the node's count
// (oldest first; the carried line carries the node's URL and the share
// height, since the account names the height and not the carrying block),
// then expired by reorg, then expired at the window's end, each up to the
// node's count less what it already wrote at that height. A share the node
// never names stays outstanding (the service expires it by name only when
// the node's account has had twice the recency to name it). It returns the
// counts written.
func (j *Journal) Settle(account, nodeURL string, carried, reorg, window map[uint64]uint64) (nc, nr, nw int, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	account = strings.ToLower(account)
	if j.settled == nil {
		j.settled = map[string]map[uint64][3]uint64{}
	}
	done := j.settled[account]
	if done == nil {
		done = map[uint64][3]uint64{}
		j.settled[account] = done
	}
	if j.settledSeqs == nil {
		j.settledSeqs = map[string]map[uint64][3][]uint64{}
	}
	seqs := j.settledSeqs[account]
	if seqs == nil {
		seqs = map[uint64][3][]uint64{}
		j.settledSeqs[account] = seqs
	}
	// a FALL of a counter first (the node put shares back or re-carried them): one line per share moved back,
	// newest first, the share outstanding again; then the rises below take it into its new state
	unmark := func(h, want uint64, slot int, kind, reason string) (int, error) {
		n := 0
		for done[h][slot] > want {
			lst := seqs[h][slot]
			if len(lst) == 0 {
				break
			}
			seq := lst[len(lst)-1]
			l := Line{Kind: kind, Seq: seq, Height: h, Sealer: nodeURL, NodeReason: reason}
			if err := j.write(l); err != nil {
				return n, err
			}
			j.apply(l) // the share outstanding again, the index one fewer at this slot
			n++
		}
		return n, nil
	}
	var nu int
	for h := range done {
		// a height the node's maps do not name at all is not a fall: the settled lines stand. (The account keeps
		// every height since genesis, so on devnet9 this never fires; the first read of 0.1.6 at 08:16Z,
		// 12 October 2026, moved 7,636 settled shares back by the NODE'S OWN WORD on the night's rise-only lines,
		// and the repair appended at 08:31Z on a wrong premise was undone by that word within the minute.)
		_, c1 := carried[h]
		_, c2 := reorg[h]
		_, c3 := window[h]
		if !c1 && !c2 && !c3 {
			continue
		}
		if u, e := unmark(h, carried[h], 0, "uncarried", "the node's account no longer carries the share at this height (put back by a reorg)"); e != nil {
			return nc, nr, nw, e
		} else {
			nu += u
		}
		if u, e := unmark(h, reorg[h], 1, "unexpired", "the node's account no longer counts the share expired by reorg at this height (re-carried)"); e != nil {
			return nc, nr, nw, e
		} else {
			nu += u
		}
		if u, e := unmark(h, window[h], 2, "unexpired", "the node's account no longer counts the share expired at the window at this height"); e != nil {
			return nc, nr, nw, e
		} else {
			nu += u
		}
	}
	j.Stats.MovedBack += uint64(nu)
	mark := func(h, want uint64, slot int, kind, reason string) (int, error) {
		n := 0
		for done[h][slot] < want {
			var seq uint64
			found := false
			for _, s := range j.order {
				l := j.outstanding[s]
				if l.Height == h && strings.ToLower(l.Account) == account {
					seq, found = s, true
					break
				}
			}
			if !found {
				break
			}
			l := Line{Kind: kind, Seq: seq, Height: h, Sealer: nodeURL, NodeReason: reason}
			if kind == "carried" {
				l.BlockAt = h
				l.CarryN = j.carryN + 1
			}
			if err := j.write(l); err != nil {
				return n, err
			}
			j.apply(l) // the share settled, the index one more at this slot
			n++
		}
		return n, nil
	}
	for h, c := range carried {
		n, e := mark(h, c, 0, "carried", "settled from the node's account")
		nc += n
		if e != nil {
			return nc, nr, nw, e
		}
	}
	for h, c := range reorg {
		n, e := mark(h, c, 1, "expired", "expired by reorg, from the node's account")
		nr += n
		if e != nil {
			return nc, nr, nw, e
		}
	}
	for h, c := range window {
		n, e := mark(h, c, 2, "expired", "expired at the window's end, from the node's account")
		nw += n
		if e != nil {
			return nc, nr, nw, e
		}
	}
	j.Stats.Carried += uint64(nc)
	j.Stats.Expired += uint64(nr + nw)
	j.Stats.Outstanding = len(j.outstanding)
	return nc, nr, nw, nil
}

// ExpireUnnamed expires by name the outstanding shares below minHeight that
// the node's account never named (the share-lane form's last resort, after
// twice the recency), with the reason on the line.
func (j *Journal) ExpireUnnamed(minHeight uint64, nodeURL string) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, s := range append([]uint64(nil), j.order...) {
		l := j.outstanding[s]
		if l.Height >= minHeight {
			continue
		}
		// the reason carries what the pool read at the height (the node's live counts for the address: carried and
		// expired by reorg) so a later compare sees what the pool saw (the node team's six heights of 12 October 2026,
		// the node's live counters below its own journal until a restart's rebuild)
		reason := "not in the node's account within twice the recency"
		if j.settled != nil {
			if d, ok := j.settled[strings.ToLower(l.Account)][l.Height]; ok {
				reason = fmt.Sprintf("%s (the account read at the height: carried %d, expired by reorg %d, at the window %d)", reason, d[0], d[1], d[2])
			}
		}
		if err := j.write(Line{Kind: "expired", Seq: s, Height: l.Height, Sealer: nodeURL, NodeReason: reason}); err != nil {
			return n, err
		}
		j.apply(Line{Kind: "expired", Seq: s})
		n++
	}
	j.Stats.Expired += uint64(n)
	j.Stats.Outstanding = len(j.outstanding)
	return n, nil
}

// Reconcile writes a "reconciled" line for every share the journal wrote
// as expired that the node's account carries (the devnet8 journals of the
// 11 October restarts, whose replay expired paid shares by the recency):
// the record is corrected by a line, never rewritten. carried is the node's
// count by share height for the address; the reconciled lines name, oldest
// first, the expired shares at each height up to that count less the
// journal's carried lines at the height.
func (j *Journal) Reconcile(account, nodeURL string, carried map[uint64]uint64) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	account = strings.ToLower(account)
	f, err := os.Open(j.path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	type st struct {
		height  uint64
		expired bool
		carried bool
		recon   bool
	}
	shares := map[uint64]*st{}
	carriedAt := map[uint64]uint64{}
	var order []uint64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var l Line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		switch l.Kind {
		case "accepted":
			if strings.ToLower(l.Account) == account {
				shares[l.Seq] = &st{height: l.Height}
				order = append(order, l.Seq)
			}
		case "expired":
			if s, ok := shares[l.Seq]; ok {
				s.expired = true
			}
		case "carried":
			if s, ok := shares[l.Seq]; ok {
				s.carried = true
				carriedAt[s.height]++
			}
		case "reconciled":
			if s, ok := shares[l.Seq]; ok {
				s.recon = true
				carriedAt[s.height]++
			}
		}
	}
	n := 0
	for h, want := range carried {
		have := carriedAt[h]
		for _, seq := range order {
			if have >= want {
				break
			}
			s := shares[seq]
			if s.height != h || !s.expired || s.carried || s.recon {
				continue
			}
			if err := j.write(Line{Kind: "reconciled", Seq: seq, Height: h, Sealer: nodeURL, NodeReason: "the node's account carries the share the replay expired by the recency"}); err != nil {
				return n, err
			}
			s.recon = true
			have++
			n++
		}
	}
	return n, nil
}

func strconvU(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// Accounts lists every account with an accepted line in the file.
func (j *Journal) Accounts() ([]string, error) {
	// from the index, not a scan of the file (the 0.1.20 roll of 20:47Z, 12 October 2026: the start's reconcile
	// read the 1 GiB public journal a second time and the stratum listener came up 52 s after the restart)
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]string, 0, len(j.byAccount))
	for a := range j.byAccount {
		out = append(out, a)
	}
	sort.Strings(out)
	return out, nil
}

// Outstanding lists the accepted shares with no carriage or expiry line,
// oldest first.
func (j *Journal) Outstanding() []Line {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Line, 0, len(j.order))
	for _, s := range j.order {
		out = append(out, j.outstanding[s])
	}
	return out
}

// Replay re-verifies every outstanding share with v (prepare its document
// under its digest at its height, recompute mix and result for its header
// and nonce) and hands each one that verifies to add, oldest first; a
// share that does not verify is journalled as expired (dropped by name)
// so it never replays again. Returns the counts.
func (j *Journal) Replay(v Verifier, add func(Line)) (replayed, dropped int, err error) {
	return j.ReplayFor(0, v, add)
}

// ReplayFor is Replay for a pool on chainID: an outstanding line written
// for another chain is expired by name ("another chain") and never
// prepared, so the validator is never anchored at another chain's heights
// (the public pool's devnet8 to devnet9 switch, 12 October 2026); a line
// without a chain id (the older form) is replayed as before. chainID 0
// replays every line.
func (j *Journal) ReplayFor(chainID uint64, v Verifier, add func(Line)) (replayed, dropped int, err error) {
	return j.replayLines(j.Outstanding(), chainID, v, add)
}

func (j *Journal) replayLines(lines []Line, chainID uint64, v Verifier, add func(Line)) (replayed, dropped int, err error) {
	unverifiable := 0
	for _, l := range lines {
		ok := v == nil // without a verifier the lines are taken as journalled (the tests); the pool always passes one
		why := ""
		algorithm := l.Algorithm
		if algorithm == "" {
			algorithm = "null"
		}
		if chainID != 0 && l.ChainID != 0 && l.ChainID != chainID {
			j.mu.Lock()
			if werr := j.write(Line{Kind: "expired", Seq: l.Seq, Height: l.Height, NodeReason: "another chain: the share was accepted for chain " + strconvU(l.ChainID) + ", the pool serves " + strconvU(chainID)}); werr != nil {
				j.mu.Unlock()
				return replayed, dropped, werr
			}
			j.apply(Line{Kind: "expired", Seq: l.Seq})
			j.Stats.Dropped++
			j.mu.Unlock()
			dropped++
			continue
		}
		if v != nil {
			var header, mix, hash [32]byte
			hb, e1 := hex.DecodeString(l.Header)
			mb, e2 := hex.DecodeString(l.Mix)
			rb, e3 := hex.DecodeString(l.Hash)
			if e1 == nil && e2 == nil && e3 == nil && len(hb) == 32 && len(mb) == 32 && len(rb) == 32 {
				copy(header[:], hb)
				copy(mix[:], mb)
				copy(hash[:], rb)
				perr := v.Prepare(algorithm, []byte(l.Params), l.Digest, l.Height)
				if perr == nil {
					var m2, h2 [32]byte
					var verr error
					if av, has := v.(AnchoredVerifier); has && l.Anchor != "" {
						// under the anchor the acceptance verified it with, never one recomputed
						var anchor [32]byte
						if ab, e := hex.DecodeString(l.Anchor); e == nil && len(ab) == 32 {
							copy(anchor[:], ab)
							m2, h2, verr = av.VerifyUnderAnchor(algorithm, anchor, header, l.Height, l.Nonce)
						} else {
							m2, h2, verr = v.Verify(algorithm, header, l.Height, l.Nonce)
						}
					} else {
						m2, h2, verr = v.Verify(algorithm, header, l.Height, l.Nonce)
					}
					ok = verr == nil && m2 == mix && h2 == hash
					if verr != nil && strings.Contains(verr.Error(), "proof-carried") {
						ok = true // the node verifies it; the journal keeps it
					}
					if verr != nil && errors.Is(verr, ErrUnverifiable) {
						ok = true // kept as journalled, named; the next replay verifies it
						unverifiable++
						why = verr.Error()
					} else if !ok {
						why = "the replay's re-verification disagrees with the acceptance"
						if verr != nil {
							why = "the replay's re-verification: " + verr.Error()
						}
					}
				} else {
					ok = true // the document could not be prepared now: kept, named
					unverifiable++
					why = "unverifiable now: " + perr.Error()
				}
			} else {
				why = "the line's header, mix or hash is unreadable"
			}
		}
		if !ok {
			j.mu.Lock()
			if werr := j.write(Line{Kind: "expired", Seq: l.Seq, Height: l.Height, NodeReason: why}); werr != nil {
				j.mu.Unlock()
				return replayed, dropped, werr
			}
			j.apply(Line{Kind: "expired", Seq: l.Seq})
			j.Stats.Dropped++
			j.mu.Unlock()
			dropped++
			continue
		}
		add(l)
		replayed++
	}
	j.mu.Lock()
	j.Stats.Replayed += uint64(replayed)
	j.Stats.Outstanding = len(j.outstanding)
	j.mu.Unlock()
	if unverifiable > 0 {
		log.Printf("share journal: %d share(s) kept as journalled, unverifiable now (the anchor or the document unreadable), verified at the next replay", unverifiable)
	}
	return replayed, dropped, nil
}

// Compact rewrites the file with the outstanding shares alone (a new file
// beside it, renamed over the old one), so a long-lived pool's journal
// stays the size of its live pool.
func (j *Journal) Compact() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	tmp := j.path + ".compact"
	nf, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(nf)
	for _, s := range j.order {
		b, err := json.Marshal(j.outstanding[s])
		if err != nil {
			nf.Close()
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			nf.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		nf.Close()
		return err
	}
	if err := nf.Sync(); err != nil {
		nf.Close()
		return err
	}
	nf.Close()
	if err := os.Rename(tmp, j.path); err != nil {
		return err
	}
	j.f.Close()
	f, err := os.OpenFile(j.path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("journal: reopen after compaction: %w", err)
	}
	j.f = f
	return nil
}

// Close closes the file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}

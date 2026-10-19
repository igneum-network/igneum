package service

import (
	"fmt"
	"log"
	"strings"
	"time"

	"igneum.network/pool/internal/shares"
	"igneum.network/pool/internal/stratum"
)

// The verifiable fee waiver (the founder's word of 18:2x UK, 14 October 2026: "Ember on our pool: 1 percent
// total"). The pool waives its declared fee for a member session only while that member's fee shares, mined by
// its fee session under the Ember fee address and keyed to it (the for block, or the worker name across the two
// addresses as the bridge for older Ember), meet the fee's fraction of its own shares over the settle window; a
// member whose fee is off, or any other miner, is charged. The charge is one member height in a hundred issued
// under the pool's own fee address (the share header's payee), so a charged session pays exactly the declared
// fraction and a waived one nothing, across reconnects (the accounting is keyed by member and worker, not by
// session) and with no free window (a member with no verdict yet is charged until its first window verifies).

// feeWindow is one member worker's accounting over the current window.
type feeWindow struct {
	memberN, feeN uint64    // shares accepted in the window: the member's own and its fee session's
	since         time.Time // the window's start
	verified      bool      // the standing verdict: waived
	decided       bool      // a verdict exists (a worker without one is charged)
	lastWindow    string    // the last verdict's figures, for the answer and the page
	heights       uint64    // member heights issued (every coordinate) since the window began
	charged       uint64    // member heights issued under the pool's fee address so far
	perCoord      map[string]uint64 // heights issued per coordinate: the charge is one in a hundred of EACH coordinate's
	// heights, because a worker mines one coordinate and a count across coordinates would charge it one height in
	// five hundred (pool-b2's read of 19:0x UK, 14 October 2026: five charged jobs, none on the mined coordinate)
}

func feeKey(address, worker string) string { return strings.ToLower(address) + "|" + worker }

// jobAccount is the address a job's share header was built for and its shares credit: the job's payee (the pool's
// fee address on a charged height) when set, else the member.
func jobAccount(j stratum.Job) string {
	if j.ForwardAs != "" {
		return j.ForwardAs
	}
	if j.Payee != "" {
		return j.Payee
	}
	return j.Member
}

// feeAccount folds an accepted share into its member's window: a fee session's share counts for the member it is
// keyed to (FeeFor), or, as the bridge, for the member with the same worker name on the member side of the pair.
// feeAcctInit restores the standing verdicts from the journal's fee_window lines once per generation (the
// handover form: a roll neither frees a window nor charges it twice; the counts start again, the verdict holds).
func (s *Service) feeAcctInit() {
	if s.feeAcct != nil {
		return
	}
	s.feeAcct = map[string]*feeWindow{}
	if s.journal == nil {
		return
	}
	now := s.now()
	n := 0
	for k, l := range s.journal.FeeVerdicts() {
		s.feeAcct[k] = &feeWindow{since: now, verified: l.FeeWaived, decided: true, lastWindow: l.NodeReason}
		n++
	}
	if n > 0 {
		log.Printf("fee window: %d standing verdict(s) restored from the journal", n)
	}
}

func (s *Service) feeAccount(sh stratum.Share) {
	if s.cfg.FeePercent == 0 || s.cfg.EmberFeeAddress == "" {
		return
	}
	acct := strings.ToLower(sh.Account)
	if acct == strings.ToLower(s.cfg.PoolFeeAddress) {
		return // the pool's own charge share
	}
	s.feeAcctInit()
	now := s.now()
	if acct == strings.ToLower(s.cfg.EmberFeeAddress) {
		member := sh.FeeFor
		if member == "" {
			// the bridge: the fee session carries the member's worker name; find the member worker of that name
			for k := range s.feeAcct {
				if strings.HasSuffix(k, "|"+sh.Worker) && !strings.HasPrefix(k, strings.ToLower(s.cfg.EmberFeeAddress)+"|") {
					member = strings.SplitN(k, "|", 2)[0]
					break
				}
			}
			if member == "" {
				return
			}
		}
		w := s.feeAcct[feeKey(member, sh.Worker)]
		if w == nil {
			w = &feeWindow{since: now}
			s.feeAcct[feeKey(member, sh.Worker)] = w
		}
		w.feeN++
		return
	}
	k := feeKey(acct, sh.Worker)
	w := s.feeAcct[k]
	if w == nil {
		w = &feeWindow{since: now}
		s.feeAcct[k] = w
	}
	w.memberN++
}

// feeWindows closes the windows that are due (the settle window, or sooner once the member has enough shares for a
// verdict) and sets each member worker's verdict; called from the share-lane poll under s.mu.
func (s *Service) feeWindows(now time.Time) {
	if s.cfg.FeePercent == 0 {
		return
	}
	window := time.Duration(s.cfg.ShareRecency) * time.Duration(s.cfg.BlockSeconds*float64(time.Second))
	if window <= 0 {
		window = 20 * time.Minute
	}
	min := s.cfg.FeeVerifyMinShares
	if min == 0 {
		min = 200
	}
	want := float64(s.cfg.FeePercent) / 100
	for k, w := range s.feeAcct {
		total := w.memberN + w.feeN
		if total == 0 || (now.Sub(w.since) < window && total < 4*min) {
			continue
		}
		if total < min {
			if now.Sub(w.since) < 3*window {
				continue // too few shares for a verdict yet; the standing verdict holds
			}
		}
		frac := float64(w.feeN) / float64(total)
		verified := total >= min && frac >= want*0.75
		w.lastWindow = fmt.Sprintf("%d member and %d fee shares over %.0f s (%.2f percent against %d)", w.memberN, w.feeN, now.Sub(w.since).Seconds(), frac*100, s.cfg.FeePercent)
		if !w.decided || verified != w.verified {
			log.Printf("fee window: %s: %s: the pool's fee %s", k, w.lastWindow, map[bool]string{true: "WAIVED (the fee is on the chain)", false: "CHARGED (the fee shares fall short)"}[verified])
		}
		w.verified, w.decided = verified, true
		if s.journal != nil {
			addr, worker, _ := strings.Cut(k, "|")
			_ = s.journal.Note(shares.Line{Kind: "fee_window", Account: addr, Worker: worker, NodeReason: w.lastWindow, FeeWaived: verified, FeeBps: s.cfg.FeeBps})
		}
		w.memberN, w.feeN, w.since = 0, 0, now
	}
}

// FeeDecide answers a member session's fee (stratum.FeeDecider): waived on a standing verified verdict.
func (s *Service) FeeDecide(address, worker string) (bool, string) {
	if strings.EqualFold(address, s.cfg.EmberFeeAddress) {
		// a fee session under the Ember fee address (keyed by its for block or by its worker name): its shares ARE
		// the member's fee and are never charged, so the fee is not a second penalty (the founder's fourth demonstration)
		return true, "a fee session under the Ember fee address: its shares are the member's fee, not charged"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feeAcctInit()
	w := s.feeAcct[feeKey(address, worker)]
	if w == nil || !w.decided {
		return false, "the pool's 1 percent is waived once the member's fee shares are read on the chain over the settle window; no window read yet, charged meanwhile"
	}
	if w.verified {
		return true, "the pool's 1 percent waived: " + w.lastWindow
	}
	return false, "the pool's 1 percent charged: " + w.lastWindow
}

// feePayee says whether this member height's job is the pool's charge (one in a hundred while charged) and counts
// the heights; called under s.mu by the lane as it issues a member worker's job.
func (s *Service) feePayee(address, worker string, height uint64, coord uint8, hasCoord bool) string {
	if s.cfg.FeePercent == 0 || s.cfg.PoolFeeAddress == "" || strings.EqualFold(address, s.cfg.EmberFeeAddress) || s.cfg.ForwardAs != "" {
		return "" // no pool, and no fee session, is ever charged; under the pooled form the fee is taken in the pay split
	}
	s.feeAcctInit()
	w := s.feeAcct[feeKey(address, worker)]
	if w == nil {
		w = &feeWindow{since: s.now()}
		s.feeAcct[feeKey(address, worker)] = w
	}
	if w.verified {
		return ""
	}
	if w.perCoord == nil {
		w.perCoord = map[string]uint64{}
	}
	ck := ""
	if hasCoord {
		ck = fmt.Sprint(coord)
	}
	w.heights++
	w.perCoord[ck]++
	if w.perCoord[ck]%100 == 1 {
		w.charged++
		log.Printf("fee charge: member %s worker %q height %d coordinate %q issued under the pool's fee address %s (charged height %d of %d issued; the window's verdict: %s)", address, worker, height, ck, s.cfg.PoolFeeAddress, w.charged, w.heights, map[bool]string{true: "charged", false: "none yet"}[w.decided])
		return s.cfg.PoolFeeAddress
	}
	return ""
}

// FeeWindows is the page's view of the accounting per member worker.
func (s *Service) FeeWindows(address string) map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]interface{}{}
	for k, w := range s.feeAcct {
		if strings.HasPrefix(k, strings.ToLower(address)+"|") {
			_, worker, _ := strings.Cut(k, "|")
			out[worker] = map[string]interface{}{"member_shares_this_window": w.memberN, "fee_shares_this_window": w.feeN, "window_since": w.since.UTC().Format(time.RFC3339),
				"waived": w.verified, "decided": w.decided, "last_window": w.lastWindow, "heights_charged": w.charged}
		}
	}
	return out
}

// feeRule is the fee row's rule in words, the one the pool runs under.
func (s *Service) feeRule() string {
	if s.cfg.FeePercent > 0 && s.cfg.EmberFeeAddress != "" {
		return fmt.Sprintf("the VERIFIABLE waiver (14 October 2026): the pool's %d percent is waived for a member and worker while the fee shares mined for it under the Ember fee address %s (a fee session keyed by its for block, or by the same worker name) are at least three quarters of %d percent of its own accepted shares over the settle window (%d blocks, %d shares minimum, the verdicts under windows and on the journal as fee_window lines); otherwise, and until the first verdict, one member height in a hundred is issued under the pool's fee address %s and those shares credit it (the charged heights); a fee session is never charged; the counts are this address's shares by which the session fee applied (unknown = lines written before the session's fee rode the journal) and fee_work the fee on its paid work at each share's own session fee", s.cfg.FeePercent, s.cfg.EmberFeeAddress, s.cfg.FeePercent, s.cfg.ShareRecency, s.cfg.FeeVerifyMinShares, s.cfg.PoolFeeAddress)
	}
	return "the pool's declared fee applies to every session (the founder's decision of 13 October 2026; the miner's dev fee is its own); the counts are this address's shares by which the session fee applied (unknown = lines written before the session's fee rode the journal, charged at their day's declared fee) and fee_work the fee on its paid work at each share's own session fee"
}

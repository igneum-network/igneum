// Package session implements fresh session authentication and the
// per-session bounds (docs/ecosystem/DESIGN.md, sections 2.4 and 2.5).
// A session is one connection: a server-generated id, a per-session
// extranonce1, one authorised payout address and worker name, a bounded
// set of retained jobs, duplicate detection per job and a share rate.
// Nothing carries over to another session; credit lives in the ledger.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"igneum.network/pool/internal/limits"
)

var (
	ErrNotAuthorised = errors.New("session: not authorised")
	ErrBadAddress    = errors.New("session: payout address out of bounds")
	ErrBadWorker     = errors.New("session: worker name out of bounds")
	ErrStale         = errors.New("session: stale job")
	ErrDuplicate     = errors.New("session: duplicate share")
	// ErrRefound is a share already accepted on another job of the same header (a client restarting its nonce
	// range on a re-issued job): refused, but an honest re-find, never a bad share
	ErrRefound = errors.New("session: share already accepted on another job of this header")
	ErrRate          = errors.New("session: share rate exceeded")
)

// Session is one connection's state.
type Session struct {
	ID          [16]byte
	ExtraNonce1 [limits.ExtraNonce1Bytes]byte
	Address     string
	Worker      string
	// Key is the miner's Ed25519 public key bound at authorize on a
	// non-custodial listener (zero on the plain form).
	Key [32]byte
	HasKey bool
	CreatedAt   time.Time

	mu         sync.Mutex
	authorised bool
	jobs       []string                       // retained job ids, oldest first
	seen       map[string]map[string]struct{} // job id -> submission keys
	// headerOf: the job's header key, so a share is a duplicate across every job issued on one header (a mid-height
	// re-issue at a new step, a coordinate's re-notify: the VectorWork box soak of 04:23Z, 13 October 2026, where a
	// client restarting its nonce range on a re-issued job sent 54 shares twice and the sealer refused them "carried
	// twice"); seenHeader holds the keys per header
	headerOf   map[string]string
	seenHeader map[string]map[string]struct{}
	tokens     float64
	lastRefill time.Time
	rate       float64
	// A separate, smaller bucket for submissions that claim to meet the
	// block boundary: a block solution is never lost to the share rate,
	// and a liar can only cost this many reference verifications.
	blockTokens     float64
	blockLastRefill time.Time
}

// New creates a session with a random id and extranonce1 read from r
// (crypto/rand when r is nil).
func New(r io.Reader, now time.Time) (*Session, error) {
	if r == nil {
		r = rand.Reader
	}
	s := &Session{
		CreatedAt:  now,
		seen:       map[string]map[string]struct{}{},
		tokens:     limits.MaxSharesPerSecond,
		lastRefill: now,
		rate:       limits.MaxSharesPerSecond,

		blockTokens:     limits.MaxBlockClaimsPerSecond,
		blockLastRefill: now,
	}
	if _, err := io.ReadFull(r, s.ID[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, s.ExtraNonce1[:]); err != nil {
		return nil, err
	}
	return s, nil
}

// IDHex is the session id as the wire carries it.
func (s *Session) IDHex() string { return hex.EncodeToString(s.ID[:]) }

// Authorise binds the session to a payout address and a worker name,
// both within bounds.
func (s *Session) Authorise(address, worker string) error {
	if !limits.ValidAddress(address) {
		return ErrBadAddress
	}
	if !limits.ValidWorkerName(worker) {
		return ErrBadWorker
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Address = address
	s.Worker = worker
	s.authorised = true
	return nil
}

// BindKey records the miner's Ed25519 key after the signed authorize
// verified (the server does the verification; the session only holds the
// key every share must match).
func (s *Session) BindKey(key [32]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Key = key
	s.HasKey = true
}

// Authorised reports whether Authorise succeeded.
func (s *Session) Authorised() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authorised
}

// Issue records a job sent to this session. Jobs beyond the retained
// bound are forgotten and shares on them are stale.
func (s *Session) Issue(jobID string) {
	s.IssueOn(jobID, "")
}

// IssueOn is Issue with the job's header key: jobs issued on one header share one duplicate space.
func (s *Session) IssueOn(jobID, header string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, held := s.seen[jobID]; held {
		// the job sent again to a session that holds it (the share lane's keepalive on a standing template, F-164,
		// 15 October 2026): its nonce memory and its place among the retained jobs stand, so a nonce accepted before
		// the keepalive is a duplicate after it and the retained list carries each job once
		return
	}
	s.jobs = append(s.jobs, jobID)
	s.seen[jobID] = map[string]struct{}{}
	if header != "" {
		if s.headerOf == nil {
			s.headerOf = map[string]string{}
			s.seenHeader = map[string]map[string]struct{}{}
		}
		s.headerOf[jobID] = header
		if s.seenHeader[header] == nil {
			s.seenHeader[header] = map[string]struct{}{}
		}
	}
	for len(s.jobs) > limits.MaxJobsRetained {
		old := s.jobs[0]
		delete(s.seen, old)
		if h, ok := s.headerOf[old]; ok {
			delete(s.headerOf, old)
			still := false
			for _, j := range s.jobs[1:] {
				if s.headerOf[j] == h {
					still = true
					break
				}
			}
			if !still {
				delete(s.seenHeader, h)
			}
		}
		s.jobs = s.jobs[1:]
	}
}

// Accept checks a submission against the session's bounds (authorised,
// known job, within rate, not a duplicate) and records its key. The
// algorithm's own validation happens after this, behind the interface.
func (s *Session) Accept(jobID, extraNonce2, nonce string, now time.Time) error {
	return s.accept(jobID, extraNonce2, nonce, now, false)
}

// AcceptBlockClaim is Accept for a submission whose claimed hash meets
// the block boundary: it draws on the block-claim bucket instead of the
// share bucket, so a real solution is never dropped behind shares.
func (s *Session) AcceptBlockClaim(jobID, extraNonce2, nonce string, now time.Time) error {
	return s.accept(jobID, extraNonce2, nonce, now, true)
}

func (s *Session) accept(jobID, extraNonce2, nonce string, now time.Time, blockClaim bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authorised {
		return ErrNotAuthorised
	}
	keys, ok := s.seen[jobID]
	if !ok {
		return ErrStale
	}
	if blockClaim {
		if !s.takeBlock(now) {
			return ErrRate
		}
	} else if !s.take(now) {
		return ErrRate
	}
	k := extraNonce2 + ":" + nonce
	if _, dup := keys[k]; dup {
		return ErrDuplicate
	}
	if h, ok := s.headerOf[jobID]; ok {
		hk := s.seenHeader[h]
		if _, dup := hk[k]; dup {
			return ErrRefound
		}
		hk[k] = struct{}{}
	}
	keys[k] = struct{}{}
	return nil
}

// takeBlock spends one token from the block-claim bucket.
func (s *Session) takeBlock(now time.Time) bool {
	elapsed := now.Sub(s.blockLastRefill).Seconds()
	if elapsed > 0 {
		s.blockTokens += elapsed * limits.MaxBlockClaimsPerSecond
		if s.blockTokens > limits.MaxBlockClaimsPerSecond {
			s.blockTokens = limits.MaxBlockClaimsPerSecond
		}
		s.blockLastRefill = now
	}
	if s.blockTokens < 1 {
		return false
	}
	s.blockTokens--
	return true
}

// take spends one token from the bucket, refilling by elapsed time.
func (s *Session) take(now time.Time) bool {
	elapsed := now.Sub(s.lastRefill).Seconds()
	if elapsed > 0 {
		s.tokens += elapsed * s.rate
		if s.tokens > s.rate {
			s.tokens = s.rate
		}
		s.lastRefill = now
	}
	if s.tokens < 1 {
		return false
	}
	s.tokens--
	return true
}

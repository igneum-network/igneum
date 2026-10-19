package service

// The member-node form (GUARD-C-CLOSURE.md section 3.1, the required form;
// DESIGN.md 2.17 "one template per member in turn" joined to the P2Pool
// form): every member's OWN node builds its template in Prepare under its
// own bond key and signer, takes the pool's payout list through
// miner_setPayoutList, and the pool, a share-and-payout service, issues
// each member only its own node's template, submits a member's solution
// to that member's node and holds no template, no key and no sealed
// block. A member client that also submits its solution to its own node
// directly (nc-miner -self-submit) needs no coordinator to land its block:
// that is the deterministic recovery against withholding the closure
// file requires (3.2 "withhold a solution").

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"igneum.network/pool/internal/jobsource"
	"igneum.network/pool/internal/stratum"
)

// newMemberNode opens a member node's RPC client; a test replaces it.
var newMemberNode = func(url string) Node { return jobsource.New(url) }

// member is one member's node and its current template.
type member struct {
	addr      string // lower-cased address (the payout identity the pool's list names)
	url       string
	node      Node
	current   *stratum.Job
	listSetAt time.Time
	nodeLost  bool
	resets    uint64
	errors    uint64
	lastErr   string
	// listRefused: the member node refused the coordinator's miner_setPayoutList (the hardened form); logged once
	listRefused bool
}

// MemberState is the health line's view of one member node.
type MemberState struct {
	Address    string `json:"address"`
	RPC        string `json:"rpc"`
	Height     uint64 `json:"height"`
	Header     string `json:"header"`
	ListResets uint64 `json:"list_resets"`
	Errors     uint64 `json:"errors"`
	LastError  string `json:"last_error,omitempty"`
	// ListRefused is true when the member node refused the coordinator's miner_setPayoutList (the hardened form:
	// the member's own client sets the list over the node's local IPC)
	ListRefused bool `json:"list_refused"`
}

// Members is the health line's view of the member-node form (nil on the
// single-node form).
func (s *Service) Members() []MemberState {
	if s.members == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MemberState, 0, len(s.members))
	for _, m := range s.members {
		st := MemberState{Address: m.addr, RPC: m.url, ListResets: m.resets, Errors: m.errors, LastError: m.lastErr, ListRefused: m.listRefused}
		if m.current != nil {
			st.Height = m.current.Height
			st.Header = fmt.Sprintf("%x", m.current.Header[:8])
		}
		out = append(out, st)
	}
	return out
}

// setListOn sets the pool's payout list on one node (miner_setPayoutList).
func (s *Service) setListOn(ctx context.Context, node Node) error {
	entries, err := s.listEntries()
	if err != nil {
		return err
	}
	var ok bool
	if err := node.Call(ctx, "miner_setPayoutList", []interface{}{entries}, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("miner_setPayoutList answered false")
	}
	return nil
}

// SetPayoutListMembers sets the pool's list on every member node at start;
// a member node that refuses is reported, not fatal (it is re-set on its
// next template, as the single-node form re-sets after a restart).
func (s *Service) SetPayoutListMembers(ctx context.Context) error {
	var errs []error
	for _, m := range s.members {
		if err := s.setListOn(ctx, m.node); err != nil {
			errs = append(errs, fmt.Errorf("member %s (%s): %w", m.addr, m.url, err))
			continue
		}
		s.mu.Lock()
		m.listSetAt = s.now()
		m.resets++
		s.mu.Unlock()
	}
	return errors.Join(errs...)
}

// pollMembers polls every member's node for its own template and issues
// each to that member's sessions only. One member's fault never stops the
// others: the errors are joined and reported once per poll.
func (s *Service) pollMembers(ctx context.Context) error {
	var errs []error
	for _, m := range s.members {
		if err := s.pollMember(ctx, m); err != nil {
			s.mu.Lock()
			m.errors++
			m.lastErr = err.Error()
			s.mu.Unlock()
			errs = append(errs, fmt.Errorf("member %s: %w", m.addr, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) pollMember(ctx context.Context, m *member) error {
	w, err := m.node.GetWork(ctx)
	if err != nil {
		s.mu.Lock()
		m.nodeLost = true
		s.mu.Unlock()
		return err
	}
	if len(s.cfg.PayoutList) > 0 {
		// the same rule as the single-node form, per member node: the member's template must name the pool's list before
		// any hasher works it; re-set when the node answers again after an error and on any template that does not name
		// the list, at most once per template interval; the template in hand is still refused
		s.mu.Lock()
		lost := m.nodeLost
		m.nodeLost = false
		due := s.now().Sub(m.listSetAt) >= ListResetInterval
		s.mu.Unlock()
		if lost && due {
			s.resetListOn(ctx, m, "the node answers again")
		} else if !samePayoutList(w.Payouts, s.cfg.PayoutList) {
			if due {
				s.resetListOn(ctx, m, fmt.Sprintf("the template at height %d names %s", w.Height, short(w.Payouts)))
			}
			return fmt.Errorf("work package at height %d from member %s's node names a payout list that is not the pool's (%s against %s): no job issued", w.Height, m.addr, short(w.Payouts), short(s.cfg.PayoutList))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.current != nil && m.current.Header == w.Header {
		return nil
	}
	job, err := s.jobFromWork(ctx, w, m.addr)
	if err != nil {
		return err
	}
	m.current = &job
	s.Stats.Jobs++
	s.Stats.LastJobHeight = w.Height
	s.Server.SetJob(job)
	return nil
}

func (s *Service) resetListOn(ctx context.Context, m *member, reason string) {
	if err := s.setListOn(ctx, m.node); err != nil {
		// on the hardened member-node form the member node's HTTP serves no write namespace: the coordinator's call is
		// refused by name and the member's own client sets the list over the node's local IPC; the refusal is logged
		// once per node (and the template stays refused until the member's client has set the list)
		s.mu.Lock()
		m.listSetAt = s.now()
		first := !m.listRefused
		m.listRefused = true
		s.mu.Unlock()
		if first {
			log.Printf("payout list: re-set on member %s's node REFUSED by the node (%s): %v; the member's own client sets the list over IPC on this form, the pool only reads templates and submits solutions", m.addr, reason, err)
		}
		return
	}
	s.mu.Lock()
	m.listSetAt = s.now()
	m.resets++
	s.ListResets++
	s.mu.Unlock()
	log.Printf("payout list: re-set on member %s's node (%s); the next template must name it", m.addr, reason)
}

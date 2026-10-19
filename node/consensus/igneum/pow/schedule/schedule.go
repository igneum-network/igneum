// Copyright 2026 The Igneum Authors
// This file is part of the Igneum node.
//
// The Igneum node is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The Igneum node is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the Igneum node. If not, see <http://www.gnu.org/licenses/>.

// Package schedule is the bounded parameter menu on the node (the research
// test plan's section 6, IH 09): one pow.Algorithm that dispatches every call
// by height to the parameter document in force at that height, from a
// chain-bound table of predetermined heights with an explicit terminal entry.
// The height is the chain's (the engine supplies it from validated context),
// never a peer's or a miner's; no benchmark selects anything. Each entry is
// its own algorithm instance (its own parameter digest, its own epoch
// caches), so historical validation of a block uses the document that was
// in force at its height, and a reorganisation across a transition height
// re-verifies each side with its own entry.
package schedule

import (
	"encoding/json"
	"sync"
	"errors"
	"fmt"
	"math/big"
	"reflect"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

var (
	// ErrBeyondEnd is returned for a height above the terminal end height:
	// the chain declared its end there and no block above it is valid.
	ErrBeyondEnd = errors.New("igneum/pow: height beyond the schedule's declared end")
	errSchedule  = errors.New("igneum/pow: invalid schedule")
)

// Entry is one row in force from Height.
type Entry struct {
	Name      string
	Height    uint64
	Params    []byte
	Algorithm pow.Algorithm
	// Active: the row takes force at its height. A disabled row (the
	// lifecycle menu's implemented successor) is built and never in force.
	Active bool
	// MinLifetime: the least number of blocks the row stays in force.
	MinLifetime uint64
	// Unkeyed is the keyed document's UNKEYED TWIN (the same document with
	// bit 10 cleared and keyed_rounds removed), nil for an unkeyed row: the
	// relation the open-seat and liveness-round blocks and the shares are
	// hashed with (GUARD-C-CLOSURE.md section 0: the keyed rounds bind the
	// drawn key's seal only). The keyless entry of a keyed document refuses
	// every triple, so without the twin an unkeyed block on a keyed chain
	// has no relation at all (the fifth development chain's first 185
	// blocks, 11 October 2026).
	Unkeyed pow.Algorithm
	// UnkeyedParams is the twin's document.
	UnkeyedParams []byte
}

// keyedBit is pow.KeyedBit, bit 10 of the version 8 feature set.
const keyedBit = pow.KeyedBit

// unkeyedTwin derives the twin document of a keyed one (pow.UnkeyedTwin, the
// one derivation the schedule's rows and the vector menu's coordinates
// share): features without bit 10, no keyed_rounds; nil for an unkeyed
// document.
func unkeyedTwin(doc []byte) ([]byte, error) { return pow.UnkeyedTwin(doc) }

// Algorithm is the menu behind pow.Algorithm.
type Algorithm struct {
	name      string
	entries   []Entry // ascending by Height; entries[0].Height == 0
	hold      bool
	endHeight *uint64
	// single: the one-resident-context form (SetSingleDataset): the keyed
	// row's adapter alone holds a full context, its unkeyed twin (the same
	// document, a second adapter) stays light (the node team's boundary
	// run, 13 October 2026, 04:28Z: two builds of epoch 0 at the same
	// instant, 4.12 GB, the twin's and the row's).
	single bool
	// full is the node's full-dataset choice, applied to the entry in
	// force at the anchor and its successor inside the lookahead alone
	// (the node team's minimum-node row, 13 October 2026: the choice
	// forwarded to every entry built the baseline's dataset beside the
	// head's and crossed the pod's 4 GB line).
	full     bool
	anchored bool
	anchor   uint64
}

// Lookahead is the window below an entry's height inside which the
// successor is warmed beside the entry in force.
const Lookahead = 256

// live reports whether entry i is in force at the anchor or takes force
// inside the lookahead above it; every entry before the anchor is set.
func (m *Algorithm) live(i int) bool {
	// Before the first anchor the chain is at its genesis (the node team's
	// sixth window, 13 October 2026: the resident choice arrives at start,
	// before any anchor, and "every entry live" built the genesis
	// baseline's full context, 2.15 GB on the 4 GB pod).
	anchor := m.anchor
	if !m.anchored {
		anchor = 0
	}
	e := &m.entries[i]
	if &m.entries[i] == m.At(anchor) {
		// The entry in force at the anchor is live unless its span ends
		// inside the lookahead (its active successor takes force within
		// Lookahead blocks): then the successor alone builds, and the
		// entry's last blocks verify on light contexts (the node team's
		// second reading, 13 October 2026, 04:0x UK: at a chain's start
		// the baseline entry at height 0 and its successor at height 1
		// both built, 4.1 GB on a 4 GB node).
		for j := range m.entries {
			n := &m.entries[j]
			if n.Active && n.Height > anchor && n.Height-anchor <= Lookahead {
				return false
			}
		}
		return true
	}
	return e.Active && e.Height > anchor && e.Height-anchor <= Lookahead
}

// Validate checks a schedule's shape: ascending heights above zero, one
// terminal rule, every row named with a document.
func Validate(name string, s *ctypes.IgneumSchedule) error {
	if s == nil {
		return nil
	}
	if s.Terminal.Hold == (s.Terminal.EndHeight != nil) {
		return fmt.Errorf("%w: the terminal entry must state exactly one of hold or endHeight", errSchedule)
	}
	var last, lastLife uint64
	for i, e := range s.Entries {
		if e.Height == 0 || e.Height <= last {
			return fmt.Errorf("%w: entry %d (%q) at height %d is not above the previous row", errSchedule, i, e.Name, e.Height)
		}
		if e.Name == "" || len(e.AlgorithmParams) == 0 {
			return fmt.Errorf("%w: entry %d needs a name and a document", errSchedule, i)
		}
		if i > 0 && e.Height < last+lastLife {
			return fmt.Errorf("%w: entry %d (%q) at height %d is inside the previous row's minimum lifetime (%d from %d)", errSchedule, i, e.Name, e.Height, lastLife, last)
		}
		last, lastLife = e.Height, e.MinLifetime
	}
	if s.Terminal.EndHeight != nil && *s.Terminal.EndHeight < last {
		return fmt.Errorf("%w: the end height %d is below the last row's height %d", errSchedule, *s.Terminal.EndHeight, last)
	}
	return nil
}

// New builds the menu for an algorithm name: the base document (entry 0 at
// height 0) and the schedule's rows, each through the registry (build is
// pow.New or pow.NewDev).
func New(name string, base []byte, s *ctypes.IgneumSchedule, build func(name string, params []byte) (pow.Algorithm, error)) (*Algorithm, error) {
	if err := Validate(name, s); err != nil {
		return nil, err
	}
	a0, err := build(name, base)
	if err != nil {
		return nil, err
	}
	m := &Algorithm{name: name, entries: []Entry{{Name: "base", Height: 0, Params: base, Algorithm: a0, Active: true}}}
	if err := m.entries[0].buildTwin(name, build); err != nil {
		return nil, err
	}
	if s == nil {
		m.hold = true
		return m, nil
	}
	for _, e := range s.Entries {
		ai, err := build(name, e.AlgorithmParams)
		if err != nil {
			return nil, fmt.Errorf("schedule entry %q at height %d: %w", e.Name, e.Height, err)
		}
		row := Entry{Name: e.Name, Height: e.Height, Params: e.AlgorithmParams, Algorithm: ai, Active: e.Active(), MinLifetime: e.MinLifetime}
		if err := row.buildTwin(name, build); err != nil {
			return nil, fmt.Errorf("schedule entry %q at height %d: %w", e.Name, e.Height, err)
		}
		m.entries = append(m.entries, row)
	}
	m.hold, m.endHeight = s.Terminal.Hold, s.Terminal.EndHeight
	return m, nil
}

// buildTwin builds a keyed row's unkeyed twin (no-op for an unkeyed row).
func (e *Entry) buildTwin(name string, build func(name string, params []byte) (pow.Algorithm, error)) error {
	if len(e.Params) == 0 {
		return nil
	}
	twin, err := unkeyedTwin(e.Params)
	if err != nil || twin == nil {
		return err
	}
	a, err := build(name, twin)
	if err == nil {
		// The keyed row rides its unkeyed twin's contexts (one dataset
		// for the pair; the twin holds it).
		if r, ok := e.Algorithm.(interface{ SetDonor(any) }); ok {
			r.SetDonor(a)
		}
	}
	if err != nil {
		return fmt.Errorf("the unkeyed twin: %w", err)
	}
	e.Unkeyed, e.UnkeyedParams = a, twin
	return nil
}

// plain is the algorithm an unkeyed triple is hashed with at a row: the
// twin of a keyed row, the row's own algorithm otherwise.
func (e *Entry) plain() pow.Algorithm {
	if e.Unkeyed != nil {
		return e.Unkeyed
	}
	return e.Algorithm
}

// UnkeyedWorkParamsAt is the document an UNKEYED template at a height is
// hashed with (the twin of a keyed row), and whether the row is keyed.
func (m *Algorithm) UnkeyedWorkParamsAt(height uint64) (params any, digest common.Hash, keyedRow bool) {
	e := m.At(height)
	if e.Unkeyed == nil {
		return nil, common.Hash{}, false
	}
	if w, ok := e.Unkeyed.(interface{ WorkParams() any }); ok {
		params = w.WorkParams()
	}
	if d, ok := e.Unkeyed.(interface{ ParamsDigest() common.Hash }); ok {
		digest = d.ParamsDigest()
	}
	return params, digest, true
}

// Entries returns the rows in force order.
func (m *Algorithm) Entries() []Entry { return m.entries }

// EndHeight returns the declared end, if any.
func (m *Algorithm) EndHeight() *uint64 { return m.endHeight }

// At returns the entry in force at a height.
func (m *Algorithm) At(height uint64) *Entry {
	e := &m.entries[0]
	for i := range m.entries {
		if m.entries[i].Height <= height && m.entries[i].Active {
			e = &m.entries[i]
		}
	}
	return e
}

// beyond reports whether a height lies above the declared end.
func (m *Algorithm) beyond(height uint64) bool {
	return m.endHeight != nil && height > *m.endHeight
}

// Name implements pow.Algorithm: the one algorithm every entry is a document of.
func (m *Algorithm) Name() string { return m.name }

// Epoch implements pow.Algorithm through the entry in force.
func (m *Algorithm) Epoch(height uint64) uint64 { return m.At(height).Algorithm.Epoch(height) }

// SeedHash implements pow.Algorithm through the entry in force.
func (m *Algorithm) SeedHash(height uint64) common.Hash {
	return m.At(height).Algorithm.SeedHash(height)
}

// Compute implements pow.Algorithm through the entry in force; above the
// declared end the hashes are zero (Verify refuses first).
func (m *Algorithm) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	if m.beyond(height) {
		return common.Hash{}, common.Hash{}
	}
	return m.At(height).plain().Compute(headerHash, height, nonce)
}

// Verify implements pow.Algorithm: the declared end first, then the entry
// in force at the height (its own rejection order).
func (m *Algorithm) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	if m.beyond(height) {
		return ErrBeyondEnd
	}
	return m.At(height).plain().Verify(headerHash, height, nonce, mixDigest, difficulty, fullDataset)
}

// Vectors implements pow.Algorithm: the base entry's.
func (m *Algorithm) Vectors() []pow.Vector { return m.entries[0].Algorithm.Vectors() }

// SetSingleDataset forwards the one-resident-context form to every entry.
func (m *Algorithm) SetSingleDataset(on bool) {
	m.single = on
	for i := range m.entries {
		if on {
			b := &singleBudget{}
			if s, ok := m.entries[i].Algorithm.(interface{ SetSingleBudget(interface{ Claim(interface{ FreeFull() }) func() }) }); ok {
				s.SetSingleBudget(b)
			}
			if s, ok := m.entries[i].Unkeyed.(interface{ SetSingleBudget(interface{ Claim(interface{ FreeFull() }) func() }) }); ok {
				s.SetSingleBudget(b)
			}
		}
		if s, ok := m.entries[i].Algorithm.(interface{ SetSingleDataset(bool) }); ok {
			s.SetSingleDataset(on)
		}
		if s, ok := m.entries[i].Unkeyed.(interface{ SetSingleDataset(bool) }); ok {
			s.SetSingleDataset(on)
		}
	}
}

// SetFullDataset passes a sealing node's choice (full-dataset contexts) to
// every row and twin that can take it.
func (m *Algorithm) SetFullDataset(full bool) {
	m.full = full
	m.applyFull()
}

// applyFull forwards the full-dataset choice to the live entries and the
// light form to the rest.
func (m *Algorithm) applyFull() {
	for i := range m.entries {
		on := m.full && m.live(i)
		if f, ok := m.entries[i].Algorithm.(interface{ SetFullDataset(bool) }); ok {
			f.SetFullDataset(on)
		}
		if f, ok := m.entries[i].Unkeyed.(interface{ SetFullDataset(bool) }); ok {
			// The twin takes the choice too: on a chain whose blocks are
			// open-seat the twin IS the verifier's path (the node team's
			// sixth window: every build light, the keyed row's context
			// never served). Under the single form the row and the twin
			// share one budget (SetSingleBudget), so one full context is
			// held across the pair, the adapter that builds evicting the
			// other's.
			f.SetFullDataset(on)
		}
	}
}

// singleBudget is one full context across a row and its twin: the adapter
// that builds claims it, the other's full contexts are freed first, and
// the claim is HELD across the build and its install (the node team's
// seventh window, 13 October 2026: two builds 146 ms apart each claimed
// before the other installed, so neither freed anything and both were
// held), so one flight per pair.
type singleBudget struct {
	mu    sync.Mutex
	owner interface{ FreeFull() }
}

// Claim takes the pair's lock, frees the other holder's full contexts and
// makes owner current; the returned release ends the claim after the
// build's install.
func (b *singleBudget) Claim(owner interface{ FreeFull() }) func() {
	b.mu.Lock()
	// INTERIM (the node team's eighth window, 13 October 2026, 07:06Z to
	// 07:12Z): on a chain whose blocks are keyed AND open-seat, the row
	// and its twin both serve blocks and an eviction between them
	// rebuilt the dataset at every path switch (four builds of 100 s
	// each before the first boundary). Until the pair can SHARE one
	// context (the hash team's ABI: a context created over another's
	// dataset; the twin's document differs from the row's by bit 10
	// alone), the pair holds one context EACH, no eviction between
	// them, and the single form's memory line reads two datasets on a
	// mixed chain (the 4 GB class cannot run such a chain resident until
	// the shared context exists; a pure open-seat or pure keyed chain
	// holds one). The claim still serialises the pair's builds.
	b.owner = owner
	return b.mu.Unlock
}

// Anchor passes the chain head to every entry that keeps epoch state, so
// each one's admitted window follows the chain (F-18).
func (m *Algorithm) Anchor(height uint64) {
	wasLive := make([]bool, len(m.entries))
	for i := range m.entries {
		wasLive[i] = m.live(i)
	}
	m.anchored, m.anchor = true, height
	changed := false
	for i := range m.entries {
		if wasLive[i] != m.live(i) {
			changed = true
		}
	}
	if changed && m.full {
		m.applyFull()
	}
	for i := range m.entries {
		// An entry behind the head or beyond the lookahead builds no
		// context: the anchor is not forwarded to it.
		if !m.live(i) {
			continue
		}
		if an, ok := m.entries[i].Algorithm.(interface{ Anchor(uint64) }); ok {
			an.Anchor(height)
		}
		if an, ok := m.entries[i].Unkeyed.(interface{ Anchor(uint64) }); ok {
			an.Anchor(height)
		}
	}
}

// WorkParamsAt is the parameter document in force at a height, for the work
// package of a template at that height.
func (m *Algorithm) WorkParamsAt(height uint64) any {
	if w, ok := m.At(height).Algorithm.(interface{ WorkParams() any }); ok {
		return w.WorkParams()
	}
	return nil
}

// Next is the first entry taking force after a height, or nil when the
// menu has none ahead (the final entry holds, or the end height is the
// next event and names no document).
func (m *Algorithm) Next(height uint64) *Entry {
	for i := range m.entries {
		if m.entries[i].Height > height && m.entries[i].Active {
			return &m.entries[i]
		}
	}
	return nil
}

// NextAt is the advance notice a work package carries: the next entry's
// work document, its digest and its height, and whether one lies ahead.
func (m *Algorithm) NextAt(height uint64) (params any, digest common.Hash, at uint64, ok bool) {
	e := m.Next(height)
	if e == nil {
		return nil, common.Hash{}, 0, false
	}
	if w, ok := e.Algorithm.(interface{ WorkParams() any }); ok {
		params = w.WorkParams()
	}
	if d, ok := e.Algorithm.(interface{ ParamsDigest() common.Hash }); ok {
		digest = d.ParamsDigest()
	}
	return params, digest, e.Height, true
}

// KeyedRounds implements pow.Keyed through the entry in force.
func (m *Algorithm) KeyedRounds(height uint64) uint64 {
	return pow.KeyedRoundsAt(m.At(height).Algorithm, height)
}

// ComputeKeyed implements pow.Keyed through the entry in force; an entry
// without the entry point refuses (zero hashes).
func (m *Algorithm) ComputeKeyed(headerHash common.Hash, height uint64, nonce uint64, sk *[32]byte, shares []byte) (common.Hash, common.Hash, []byte) {
	if m.beyond(height) {
		return common.Hash{}, common.Hash{}, nil
	}
	if k, ok := m.At(height).Algorithm.(pow.Keyed); ok {
		return k.ComputeKeyed(headerHash, height, nonce, sk, shares)
	}
	return common.Hash{}, common.Hash{}, nil
}

// ComputeKeyedM implements pow.KeyedM through the entry in force; an entry
// without the entry point refuses (zero hashes, nil messages).
func (m *Algorithm) ComputeKeyedM(headerHash common.Hash, height uint64, nonce uint64, sk *[32]byte, shares []byte) (common.Hash, common.Hash, []byte, []byte) {
	if m.beyond(height) {
		return common.Hash{}, common.Hash{}, nil, nil
	}
	if k, ok := m.At(height).Algorithm.(pow.KeyedM); ok {
		return k.ComputeKeyedM(headerHash, height, nonce, sk, shares)
	}
	return common.Hash{}, common.Hash{}, nil, nil
}

// ParamsDigestAt is the parameter digest in force at a height.
func (m *Algorithm) ParamsDigestAt(height uint64) common.Hash {
	if d, ok := m.At(height).Algorithm.(interface{ ParamsDigest() common.Hash }); ok {
		return d.ParamsDigest()
	}
	return common.Hash{}
}

// ValidateUpgrade is the lifecycle menu's ratification rule (PLAN-TO-WIN.md
// point 8; Phase A section 7) for a schedule given to a node whose head is
// at height head, against the schedule it holds: every row of the held
// schedule at or below the head is unchanged in the new one (historical
// verification is permanent; a row in force cannot be rewritten); every
// row the new schedule adds or alters lies at or above head + 2 x
// ActivationWindow (ratified two activation windows ahead); the terminal
// may change only by the same margin. A window of zero disables the check
// (a development chain). The caller that finds the new schedule invalid
// keeps the held one: a failed activation falls back to the previous valid
// schedule, never to discretion.
func ValidateUpgrade(held, next *ctypes.IgneumSchedule, head uint64) error {
	if held == nil || next == nil || next.ActivationWindow == 0 {
		return nil
	}
	horizon := head + 2*next.ActivationWindow
	same := func(a, b ctypes.IgneumScheduleEntry) bool {
		return a.Name == b.Name && a.Height == b.Height && sameDocument(a.AlgorithmParams, b.AlgorithmParams) && a.Active() == b.Active() && a.MinLifetime == b.MinLifetime
	}
	byHeight := map[uint64]ctypes.IgneumScheduleEntry{}
	for _, e := range next.Entries {
		byHeight[e.Height] = e
	}
	for _, e := range held.Entries {
		n, ok := byHeight[e.Height]
		if e.Height <= head {
			if !ok || !same(e, n) {
				return fmt.Errorf("%w: the row at height %d (%q) is at or below the head %d and cannot change", errSchedule, e.Height, e.Name, head)
			}
			continue
		}
		if (!ok || !same(e, n)) && e.Height < horizon {
			return fmt.Errorf("%w: the row at height %d (%q) changes inside two activation windows of the head %d (horizon %d)", errSchedule, e.Height, e.Name, head, horizon)
		}
	}
	heldBy := map[uint64]ctypes.IgneumScheduleEntry{}
	for _, e := range held.Entries {
		heldBy[e.Height] = e
	}
	for _, e := range next.Entries {
		h, ok := heldBy[e.Height]
		if (!ok || !same(h, e)) && e.Height < horizon {
			return fmt.Errorf("%w: the new row at height %d (%q) activates inside two activation windows of the head %d (horizon %d)", errSchedule, e.Height, e.Name, head, horizon)
		}
	}
	// The terminal rule: when it changes, every end height involved (the
	// held one and the given one, where present) lies at or above the
	// horizon; a hold has no height and counts as beyond it. So an end may
	// be introduced, removed or moved only beyond the margin, and an end
	// inside the margin is fixed (protocol B's reading, 14:5x UK, which
	// closed the hole of an end moved from beyond the margin to inside it).
	if !terminalEqual(held.Terminal, next.Terminal) {
		for _, e := range []*uint64{held.Terminal.EndHeight, next.Terminal.EndHeight} {
			if e != nil && *e < horizon {
				return fmt.Errorf("%w: the terminal rule changes with an end height %d inside two activation windows of the head %d (horizon %d)", errSchedule, *e, head, horizon)
			}
		}
	}
	return nil
}

func terminalEqual(a, b ctypes.IgneumTerminal) bool {
	if a.Hold != b.Hold || (a.EndHeight == nil) != (b.EndHeight == nil) {
		return false
	}
	return a.EndHeight == nil || *a.EndHeight == *b.EndHeight
}

// sameDocument compares two parameter documents as JSON values, not bytes:
// a held schedule read back from the database is re-serialised (compact,
// keys reordered) and a given one carries the operator's spacing, and the
// two must read as one document (the second chain refused its own unchanged
// schedule on the byte comparison, 10 October 2026).
func sameDocument(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	return reflect.DeepEqual(x, y)
}

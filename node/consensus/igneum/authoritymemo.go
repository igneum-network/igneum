package igneum

import (
	"errors"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
)

// The authority memo (F-39; the project's order of 16:5x UK, 12 October 2026): the
// authority stage's parent-state read was the largest part of a block's
// verification on devnet7 (security's p99 80.2 ms whole-block against the
// pi stage's 15 to 21 ms median), because every child opened the parent's
// state and read the rule's facts from the trie, at the header stage and
// again in the state processor. The facts a child needs are the PARENT's
// post-state facts, which are known the moment the parent is finalized: the
// epoch table the child draws from, every bonded key's units, bond, mark
// and signer, and the clock. Finalize records them keyed by the parent's
// hash; the header stage, the state processor and the sealer's template
// read the memo and touch the trie only on a miss (the first block after a
// start, a key the memo does not hold), when the state is opened as before.
// The memo mirrors the state exactly (it is filled from the same state
// after Finalize's last write), so the rule's verdict is unchanged.
type keyFacts struct {
	units  uint64
	bond   igneumreg.Bond
	marked bool
}

type parentFacts struct {
	epoch    uint64      // the epoch of the child's height
	table    *epochTable // the child's table (nil when missing in state)
	tableErr error
	clock    *Clock // the parent's clock slots (nil before the rule)
	clockErr error
	keys     map[common.Hash]keyFacts // every registered key at the parent
}

type authorityMemo struct {
	mu    sync.Mutex
	by    map[common.Hash]*parentFacts
	order []common.Hash
}

const authorityMemoSize = 64

var errMemoMiss = errors.New("igneum: the authority memo does not hold the fact")

// recordFacts fills the memo for the children of a block from its post
// state (the END of Finalize, after the floor and the clock are written).
func (e *Engine) recordFacts(blockHash common.Hash, height uint64, st *state.StateDB) {
	a := e.config.Parts.Authority
	s := e.config.Parts.Sortition
	if a == nil || s == nil || !s.Active(height+1) || !s.OpenSeat {
		return
	}
	f := &parentFacts{epoch: s.EpochAt(height + 1), keys: make(map[common.Hash]keyFacts)}
	f.table, f.tableErr = e.tableAt(st, f.epoch)
	if e.ClockOn(height + 1) {
		f.clock, f.clockErr = e.readClock(st)
	}
	for _, k := range igneumreg.Keys(st, a.Registry) {
		kf := keyFacts{bond: igneumreg.BondOf(st, a.Registry, k), marked: igneumreg.Marked(st, a.Registry, k)}
		if f.table != nil {
			kf.units = e.unitsAt(st, f.table, k)
		}
		f.keys[k] = kf
	}
	m := &e.memo
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.by == nil {
		m.by = make(map[common.Hash]*parentFacts)
	}
	if _, ok := m.by[blockHash]; !ok {
		m.order = append(m.order, blockHash)
		if len(m.order) > authorityMemoSize {
			delete(m.by, m.order[0])
			m.order = m.order[1:]
		}
	}
	m.by[blockHash] = f
}

func (e *Engine) factsFor(parentHash common.Hash) *parentFacts {
	m := &e.memo
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.by[parentHash]
}

// ruleReader serves the rule's reads of the parent's state: from the memo
// when it holds the fact, else from the state, and errMemoMiss when there
// is no state to fall back on (the memo-first header stage, which then
// opens the state and reads again).
type ruleReader struct {
	e  *Engine
	st *state.StateDB
	f  *parentFacts
}

func (e *Engine) rulesFor(st *state.StateDB, parentHash common.Hash) *ruleReader {
	return &ruleReader{e: e, st: st, f: e.factsFor(parentHash)}
}

func (r *ruleReader) tableAt(epoch uint64) (*epochTable, error) {
	if r.f != nil && r.f.epoch == epoch && (r.f.table != nil || r.f.tableErr != nil) {
		return r.f.table, r.f.tableErr
	}
	if r.st == nil {
		return nil, errMemoMiss
	}
	return r.e.tableAt(r.st, epoch)
}

func (r *ruleReader) key(key common.Hash) (keyFacts, error) {
	if r.f != nil {
		if kf, ok := r.f.keys[key]; ok {
			return kf, nil
		}
	}
	if r.st == nil {
		return keyFacts{}, errMemoMiss
	}
	reg := r.e.config.Parts.Authority.Registry
	kf := keyFacts{bond: igneumreg.BondOf(r.st, reg, key), marked: igneumreg.Marked(r.st, reg, key)}
	return kf, nil
}

// unitsAt is a key's units in a table: the memo's when the table is the
// memo's, else the state's.
func (r *ruleReader) unitsAt(t *epochTable, key common.Hash) (uint64, error) {
	if r.f != nil && r.f.table == t {
		if kf, ok := r.f.keys[key]; ok {
			return kf.units, nil
		}
		if r.st == nil {
			// a key the memo does not hold: not registered at the parent,
			// so no units (the memo holds every registered key)
			return 0, nil
		}
	}
	if r.st == nil {
		return 0, errMemoMiss
	}
	return r.e.unitsAt(r.st, t, key), nil
}

func (r *ruleReader) marked(key common.Hash) (bool, error) {
	kf, err := r.key(key)
	if err != nil {
		if r.f != nil && r.st == nil {
			return false, nil // not registered at the parent: unmarked
		}
		return false, err
	}
	return kf.marked, nil
}

func (r *ruleReader) bondOf(key common.Hash) (igneumreg.Bond, error) {
	kf, err := r.key(key)
	if err != nil {
		if r.f != nil && r.st == nil {
			return igneumreg.Bond{Amount: common.Big0}, nil // not registered: no bond
		}
		return igneumreg.Bond{}, err
	}
	return kf.bond, nil
}

func (r *ruleReader) clock() (*Clock, error) {
	if r.f != nil && (r.f.clock != nil || r.f.clockErr != nil || r.st == nil) {
		return r.f.clock, r.f.clockErr
	}
	if r.st == nil {
		return nil, errMemoMiss
	}
	return r.e.readClock(r.st)
}

// drawWith is drawAt over a reader.
func (e *Engine) drawWith(r *ruleReader, key common.Hash, height uint64) (*draw, error) {
	s := e.config.Parts.Sortition
	t, err := r.tableAt(s.EpochAt(height))
	if err != nil {
		return nil, err
	}
	units, err := r.unitsAt(t, key)
	if err != nil {
		return nil, err
	}
	d := &draw{table: t, units: units}
	if d.units > 0 {
		marked, err := r.marked(key)
		if err != nil {
			return nil, err
		}
		if marked {
			d.units = 0
		}
	}
	if d.units > 0 {
		d.round = s.EligibleRound(t.seed, key, d.units, height)
		return d, nil
	}
	d.open = true
	d.round = s.OpenSeatRound(t.seed, t.nTotal, height)
	return d, nil
}

// MemoSize is the memo's size (igneum_shareStats reads nothing of it; a
// test does).
func (e *Engine) MemoSize() int {
	m := &e.memo
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.by)
}

var _ = types.EmptyRootHash

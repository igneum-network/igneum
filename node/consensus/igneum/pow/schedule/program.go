package schedule

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// The per-period programme through the bounded menu (pow.Programmed): the
// period is the entry's in force at the height, zero on an entry whose
// document draws no programme, and the view under an anchor dispatches by
// height to each entry's own view. A chain that activates the programme at
// a registered height does so as a schedule entry at that height, so the
// entries below it answer zero and resolve no anchor.

// PeriodBlocks implements pow.Programmed.
func (m *Algorithm) PeriodBlocks(height uint64) uint64 {
	return pow.PeriodBlocksAt(m.At(height).Algorithm, height)
}

// Under implements pow.Programmed: the menu bound to one anchor, each entry
// bound to it where the entry is programmed and left as it is otherwise.
func (m *Algorithm) Under(anchor common.Hash) pow.Algorithm {
	v := &Algorithm{name: m.name, entries: make([]Entry, len(m.entries)), endHeight: m.endHeight}
	copy(v.entries, m.entries)
	for i := range v.entries {
		if p, ok := v.entries[i].Algorithm.(pow.Programmed); ok {
			v.entries[i].Algorithm = p.Under(anchor)
		}
	}
	return v
}

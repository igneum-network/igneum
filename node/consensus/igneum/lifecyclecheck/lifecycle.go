// Package lifecyclecheck is protocol B's independent implementation of the
// lifecycle menu's validity rules (docs/igneumhash/ENVELOPE-R0A-v0.md sections
// 9.1 and 9.2 at e96880ef; PLAN-TO-WIN.md point 8), written from the text
// alone, never from the protocol team's consensus/igneum/pow/schedule. The rules:
// rows ascend strictly from entry 0 at height 0; a row with enabled false is
// built and never takes force (At holds the last active row through it, Next
// names active rows only); minLifetime binds the next row's height to at
// least height + minLifetime; activationWindow W admits a schedule at head h
// only if every row at or below h is unchanged, every added or altered row is
// at or above h + 2W, and the terminal rule changes only with every end
// height involved (the held one and the given one, where present) at or
// above the margin, a hold counting as beyond it (revision 96ada9ec, after
// this side's reading); a refused schedule leaves the held one in force.
// The activation condition is the height alone.
package lifecyclecheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrSchedule = errors.New("lifecycle: schedule refused")

// Entry is one row of the menu: a complete parameter document in force from
// its height. Enabled defaults to true when absent.
type Entry struct {
	Name        string          `json:"name"`
	Height      uint64          `json:"height"`
	Params      json.RawMessage `json:"algorithmParams"`
	Enabled     *bool           `json:"enabled,omitempty"`
	MinLifetime uint64          `json:"minLifetime,omitempty"`
}

func (e Entry) active() bool { return e.Enabled == nil || *e.Enabled }

// Terminal states what follows the final row: hold, or an end height.
type Terminal struct {
	Hold      bool    `json:"hold,omitempty"`
	EndHeight *uint64 `json:"endHeight,omitempty"`
}

func (t Terminal) valid() bool { return t.Hold != (t.EndHeight != nil) }

func (t Terminal) equal(o Terminal) bool {
	if t.Hold != o.Hold || (t.EndHeight == nil) != (o.EndHeight == nil) {
		return false
	}
	return t.EndHeight == nil || *t.EndHeight == *o.EndHeight
}

// Schedule is igneum.schedule.
type Schedule struct {
	Entries          []Entry  `json:"entries"`
	Terminal         Terminal `json:"terminal"`
	ActivationWindow uint64   `json:"activationWindow,omitempty"`
}

// Validate is the standing rule on one schedule (section 9.1 and 9.2 (2)):
// at least one entry; entry 0 at height 0; heights ascend strictly and every
// later one is above zero; each row's successor is at least minLifetime
// later; every row carries a document; the terminal is exactly one of hold
// and endHeight, and an end height is not below the final row.
func (s Schedule) Validate() error {
	if len(s.Entries) == 0 {
		return fmt.Errorf("%w: no entries", ErrSchedule)
	}
	if s.Entries[0].Height != 0 {
		return fmt.Errorf("%w: entry 0 is at height %d, not 0", ErrSchedule, s.Entries[0].Height)
	}
	for i, e := range s.Entries {
		if len(bytes.TrimSpace(e.Params)) == 0 || string(bytes.TrimSpace(e.Params)) == "null" {
			return fmt.Errorf("%w: entry %d (%s) has no document", ErrSchedule, i, e.Name)
		}
		if i == 0 {
			continue
		}
		prev := s.Entries[i-1]
		if e.Height <= prev.Height {
			return fmt.Errorf("%w: entry %d (%s) at %d does not ascend from %d", ErrSchedule, i, e.Name, e.Height, prev.Height)
		}
		if e.Height < prev.Height+prev.MinLifetime {
			return fmt.Errorf("%w: entry %d (%s) at %d is nearer than entry %d's minimum lifetime (%d + %d)", ErrSchedule, i, e.Name, e.Height, i-1, prev.Height, prev.MinLifetime)
		}
	}
	if !s.Terminal.valid() {
		return fmt.Errorf("%w: the terminal is exactly one of hold and endHeight", ErrSchedule)
	}
	if s.Terminal.EndHeight != nil && *s.Terminal.EndHeight < s.Entries[len(s.Entries)-1].Height {
		return fmt.Errorf("%w: endHeight %d below the final row at %d", ErrSchedule, *s.Terminal.EndHeight, s.Entries[len(s.Entries)-1].Height)
	}
	return nil
}

// At is the entry in force at a height: the last row at or below the height
// whose enabled flag is not false (a disabled row is held through). The
// index is returned; -1 above an end height.
func (s Schedule) At(height uint64) int {
	if s.Terminal.EndHeight != nil && height > *s.Terminal.EndHeight {
		return -1
	}
	best := -1
	for i, e := range s.Entries {
		if e.Height > height {
			break
		}
		if e.active() {
			best = i
		}
	}
	return best
}

// Next is the first active row above the height (the advance notice names
// active rows only), or -1.
func (s Schedule) Next(height uint64) int {
	for i, e := range s.Entries {
		if e.Height > height && e.active() {
			return i
		}
	}
	return -1
}

// docEqual compares two documents as JSON values (the same document written
// with other whitespace is the same document).
func docEqual(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

func entryEqual(a, b Entry) bool {
	return a.Name == b.Name && a.Height == b.Height && a.MinLifetime == b.MinLifetime && a.active() == b.active() && docEqual(a.Params, b.Params)
}

// ValidateUpgrade admits a schedule given to a node holding `held` at head
// height h (section 9.2 (3)): the given schedule validates; with W = the
// given schedule's activationWindow (zero disables the rule), every held
// row at or below h is present unchanged at the same index; every row of
// the given schedule that is added or altered against the held one lies at
// or above h + 2W; if the terminal changes, the held terminal's rule and the
// given one's, where present, must both lie at or above h + 2W, a hold
// counting as beyond the margin (an end is introduced, removed or moved only
// beyond the margin; an end inside it is fixed). On refusal the held
// schedule stays in force; the error names the rule.
func ValidateUpgrade(held, given Schedule, head uint64) error {
	if err := given.Validate(); err != nil {
		return err
	}
	w := given.ActivationWindow
	if w == 0 {
		return nil
	}
	margin := head + 2*w
	for i, e := range held.Entries {
		if e.Height > head {
			break
		}
		if i >= len(given.Entries) || !entryEqual(e, given.Entries[i]) {
			return fmt.Errorf("%w: row %d (%s) at %d, at or below the head %d, is changed or missing (historical verification is permanent)", ErrSchedule, i, e.Name, e.Height, head)
		}
	}
	for i, e := range given.Entries {
		if i < len(held.Entries) && entryEqual(e, held.Entries[i]) {
			continue
		}
		if e.Height < margin {
			return fmt.Errorf("%w: row %d (%s) at %d is added or altered below the head %d + 2 x %d", ErrSchedule, i, e.Name, e.Height, head, w)
		}
		if i < len(held.Entries) && held.Entries[i].Height < margin {
			return fmt.Errorf("%w: row %d (%s) alters a held row at %d below the margin %d", ErrSchedule, i, e.Name, held.Entries[i].Height, margin)
		}
	}
	if len(given.Entries) < len(held.Entries) {
		for i := len(given.Entries); i < len(held.Entries); i++ {
			if held.Entries[i].Height < margin {
				return fmt.Errorf("%w: held row %d (%s) at %d is removed below the margin %d", ErrSchedule, i, held.Entries[i].Name, held.Entries[i].Height, margin)
			}
		}
	}
	if !held.Terminal.equal(given.Terminal) {
		if held.Terminal.EndHeight != nil && *held.Terminal.EndHeight < margin {
			return fmt.Errorf("%w: the held end height %d changes below the margin %d", ErrSchedule, *held.Terminal.EndHeight, margin)
		}
		if given.Terminal.EndHeight != nil && *given.Terminal.EndHeight < margin {
			return fmt.Errorf("%w: the given end height %d lies below the margin %d", ErrSchedule, *given.Terminal.EndHeight, margin)
		}
	}
	return nil
}

// Apply returns the schedule in force after a node is given `given` at the
// head: the given one when admitted, the held one otherwise (the node logs
// the refusal and runs on).
func Apply(held, given Schedule, head uint64) (Schedule, error) {
	if err := ValidateUpgrade(held, given, head); err != nil {
		return held, err
	}
	return given, nil
}

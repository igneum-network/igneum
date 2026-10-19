package params

import "testing"

// TestIsIgneumChainOfRecord (F-771): every id with a rule-fork entry, and
// the research chain, is a chain of record; the Money lab chain is not.
func TestIsIgneumChainOfRecord(t *testing.T) {
	for id := range IgneumChainRuleForks {
		if !IsIgneumChainOfRecord(id) {
			t.Errorf("chain %d carries a rule-fork entry and must be a chain of record", id)
		}
	}
	for _, id := range []uint64{7322, 7358} {
		if !IsIgneumChainOfRecord(id) {
			t.Errorf("chain %d must be a chain of record", id)
		}
	}
	for _, id := range []uint64{7402, 7397, 7311} {
		if IsIgneumChainOfRecord(id) {
			t.Errorf("chain %d is a lab id and must not be a chain of record", id)
		}
	}
}

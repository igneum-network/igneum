package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/params"
)

// TestPoolUnsafeVerifierRefusesChainsOfRecord (F-771): the lab double is
// refused outside --dev, outside the research range, and on every id that
// carries an entry in params.IgneumChainRuleForks or is the research chain,
// though those ids lie inside the research range.
func TestPoolUnsafeVerifierRefusesChainsOfRecord(t *testing.T) {
	for id := range params.IgneumChainRuleForks {
		if err := poolUnsafeVerifierRefusal(true, id); err == nil {
			t.Errorf("chain %d has a rule-fork entry: the double must be refused under --dev", id)
		}
	}
	for _, id := range []uint64{7322, 7358} {
		if err := poolUnsafeVerifierRefusal(true, id); err == nil {
			t.Errorf("chain %d is a chain of record: the double must be refused under --dev", id)
		}
	}
	if err := poolUnsafeVerifierRefusal(true, 7402); err != nil {
		t.Errorf("the lab chain 7402 under --dev must be allowed: %v", err)
	}
	if err := poolUnsafeVerifierRefusal(false, 7402); err == nil {
		t.Error("outside --dev the double must be refused")
	}
	if err := poolUnsafeVerifierRefusal(true, 1); err == nil {
		t.Error("outside the research range the double must be refused")
	}
}

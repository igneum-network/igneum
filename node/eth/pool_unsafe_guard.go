package eth

import (
	"fmt"

	"github.com/ethereum/go-ethereum/params"
)

// poolUnsafeVerifierRefusal is the guard on --dev.pool.unsafe-verifier: nil
// only under --dev on a research-range chain id that is no chain of record
// (F-771: the research range contains 7322 and 7358, so the range alone is
// not the test; a chain with an entry in params.IgneumChainRuleForks, and the
// research chain, are refused). The guard of substance is the pool's gate at
// zero on every chain of record, under which nothing consults any verifier;
// this is the second belt.
func poolUnsafeVerifierRefusal(devMode bool, id uint64) error {
	if !devMode || !params.IsIgneumResearchChainID(id) || params.IsIgneumChainOfRecord(id) {
		return fmt.Errorf("--dev.pool.unsafe-verifier is for a development chain in the research range alone, never a chain of record, not chain %d", id)
	}
	return nil
}

package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestPiVerifyGate (F-461, 17 October 2026): the proof's check is the
// node's flag OR the chain's rule from the forks entry's PiVerifyV1, read
// through one accessor: without the flag and without an entry nothing is
// checked; with the entry the check holds from its height and not below;
// the flag alone checks everywhere; the header stage follows the accessor
// (an unkeyed header without a commitment passes at every height, a keyed
// header without one is refused only where the check holds).
func TestPiVerifyGate(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	e.config.PiVerify = false
	if e.piVerifyAt(1) || e.piVerifyAt(1_000_000) {
		t.Fatal("no flag and no entry, yet the proof is checked")
	}
	e.forks = &ctypes.IgneumRuleForks{PiVerifyV1: 5000}
	if e.piVerifyAt(4999) {
		t.Fatal("the landing check holds below its height")
	}
	if !e.piVerifyAt(5000) || !e.piVerifyAt(9000) {
		t.Fatal("the gate does not hold from its height")
	}
	// The header stage through the accessor: a keyed header without a
	// commitment below the gate passes, at the landing check is refused; an unkeyed
	// header without a commitment passes at both.
	keyed := func(h uint64) *types.Header { return &types.Header{Number: new(big.Int).SetUint64(h), Shares: make([]byte, 64)} }
	plain := func(h uint64) *types.Header { return &types.Header{Number: new(big.Int).SetUint64(h)} }
	if err := e.verifyPiHeader(keyed(4999)); err != nil {
		t.Fatalf("a keyed header below the landing check refused: %v", err)
	}
	if err := e.verifyPiHeader(keyed(5000)); err == nil {
		t.Fatal("a keyed header without a commitment at the landing check accepted")
	}
	if err := e.verifyPiHeader(plain(5000)); err != nil {
		t.Fatalf("an unkeyed header at the landing check refused: %v", err)
	}
	e.forks = nil
	e.config.PiVerify = true
	if !e.piVerifyAt(1) {
		t.Fatal("the flag does not check")
	}
}

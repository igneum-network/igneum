package igneum

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/pi"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestPiFlagAndHeaderRule: with the flag off a keyed seal needs no proof and
// a carried one is not verified; with the flag on the header rule holds (a
// keyed seal commits, an unkeyed one does not); a node without the verifier
// refuses the flag; the body stage refuses a committed proof that is absent.
func TestPiFlagAndHeaderRule(t *testing.T) {
	e := newTestEngine(t)
	key := common.HexToHash("0x11")
	keyed := &types.Header{Number: common.Big1, BondKey: &key, Shares: make([]byte, 64)}
	open := &types.Header{Number: common.Big1, BondKey: &key}

	// Off: nothing required, nothing verified.
	if err := e.verifyPiHeader(keyed); err != nil {
		t.Fatalf("flag off, keyed seal without a commitment refused: %v", err)
	}
	if err := e.VerifyPi(types.NewBlockWithHeader(keyed)); err != nil {
		t.Fatalf("flag off, body stage refused: %v", err)
	}
	// On without the verifier: refused at the flag; with it: the id check.
	err := e.SetPi(true, "", t.TempDir())
	if !pi.Built {
		if err == nil {
			t.Fatal("a node without the verifier took --igneum.pi-verify")
		}
		e.config.PiVerify = true // the rule's shape, exercised without the library
	} else if err != nil {
		t.Fatal(err)
	}
	if err := e.verifyPiHeader(keyed); !errors.Is(err, errPiMissing) {
		t.Fatalf("keyed seal without a commitment: %v", err)
	}
	h := types.PiHash([]byte{2, 1, 2, 3})
	open.PiHash = &h
	if err := e.verifyPiHeader(open); !errors.Is(err, errPiUnexpected) {
		t.Fatalf("unkeyed seal with a commitment: %v", err)
	}
	keyed.PiHash = &h
	if err := e.verifyPiHeader(keyed); err != nil {
		t.Fatalf("keyed seal with a commitment: %v", err)
	}
	// The body stage: a commitment and no bytes.
	if err := e.VerifyPi(types.NewBlockWithHeader(keyed)); !errors.Is(err, errPiBody) {
		t.Fatalf("committed proof absent from the body: %v", err)
	}
	// The pre-checks on the bytes: the bound and the system byte, before any
	// verifier call (so they hold without the library too).
	over := make([]byte, pi.MaxBytes(pi.DomainSeal)+1)
	over[0] = pi.System
	if err := e.verifyPiBytes(keyed, over, make([]byte, 64)); !errors.Is(err, errPiRefused) || !contains(err.Error(), pi.CodeName(pi.EBound)) {
		t.Fatalf("over the bound: %v", err)
	}
	if err := e.verifyPiBytes(keyed, []byte{1, 2, 3}, make([]byte, 64)); !errors.Is(err, errPiRefused) || !contains(err.Error(), pi.CodeName(pi.ESystem)) {
		t.Fatalf("wrong system byte: %v", err)
	}
	// The commitment itself round-trips through the block.
	proof := []byte{2, 9, 9}
	ph := types.PiHash(proof)
	keyed.PiHash = &ph
	b := types.NewBlockWithHeader(keyed).WithPi(proof)
	if types.PiHash(b.Pi()) != *b.Header().PiHash || b.Body().Pi == nil {
		t.Fatal("the proof does not round-trip through the block and its body")
	}
	// The body stage on an algorithm without the keyed entry (the test
	// engine's null algorithm) refuses for want of messages, never accepts.
	if err := e.VerifyPi(b); !errors.Is(err, errPiNoMessages) {
		t.Fatalf("flag on, no keyed entry: %v", err)
	}
	// Without the verifier the bytes stage refuses at not-built.
	if !pi.Built {
		if err := e.verifyPiBytes(keyed, proof, make([]byte, 64)); !errors.Is(err, errPiRefused) || !contains(err.Error(), pi.CodeName(pi.ENotBuilt)) {
			t.Fatalf("without the verifier the flag-on bytes stage must refuse at not-built: %v", err)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

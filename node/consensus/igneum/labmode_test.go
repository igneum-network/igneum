//go:build ignpi

package igneum

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
)

func labBlock(n uint64) *types.Block {
	return types.NewBlockWithHeader(&types.Header{Number: new(big.Int).SetUint64(n)})
}

// TestWithholdHoldsThenReleases (the lab build's --dev.withhold, C13's
// branch races): with N armed the sealer holds each block and hides it from
// the broadcast until it holds N, then releases the whole set at once; off
// the instrument every block broadcasts at once.
func TestWithholdHoldsThenReleases(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	// Off: the block is returned for an immediate broadcast, nothing held.
	if rel, hold := e.Withhold(labBlock(1)); hold || len(rel) != 1 {
		t.Fatalf("off the instrument: hold %v released %d", hold, len(rel))
	}
	if _, err := e.SetWithhold(3); err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 2; i++ {
		if rel, hold := e.Withhold(labBlock(i)); !hold || len(rel) != 0 {
			t.Fatalf("block %d: hold %v released %d (want held, none out)", i, hold, len(rel))
		}
	}
	rel, hold := e.Withhold(labBlock(3))
	if !hold || len(rel) != 3 {
		t.Fatalf("the third: hold %v released %d (want the three at once)", hold, len(rel))
	}
	for i, b := range rel {
		if b.NumberU64() != uint64(i+1) {
			t.Fatalf("released out of seal order: %d at %d", b.NumberU64(), i)
		}
	}
	// A disarm releases a partial buffer to the caller (no broadcaster set
	// in the test).
	if _, err := e.SetWithhold(2); err != nil {
		t.Fatal(err)
	}
	e.Withhold(labBlock(4))
	released, err := e.SetWithhold(0)
	if err != nil || len(released) != 1 || released[0].NumberU64() != 4 {
		t.Fatalf("disarm: %v released %+v", err, released)
	}
	if rel, hold := e.Withhold(labBlock(5)); hold || len(rel) != 1 {
		t.Fatalf("after the disarm: hold %v released %d", hold, len(rel))
	}
}

// TestLaxEndorseFileArmsAndValidatesLength (the lab build's
// --dev.lax-endorse, C11's import-refusal classes): the file's bytes arm the
// unverified attach; a file that is not a whole number of 100-byte entries
// is refused.
func TestLaxEndorseFileArmsAndValidatesLength(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	dir := t.TempDir()
	good := filepath.Join(dir, "entries.bin")
	entries := make([]byte, 2*types.EndorsementBytes)
	for i := range entries {
		entries[i] = byte(i)
	}
	if err := os.WriteFile(good, entries, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.SetLaxEndorse(good); err != nil {
		t.Fatal(err)
	}
	if got := e.laxEndorseEntries(); len(got) != len(entries) {
		t.Fatalf("armed entries %d, want %d", len(got), len(entries))
	}
	bad := filepath.Join(dir, "bad.bin")
	if err := os.WriteFile(bad, make([]byte, types.EndorsementBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.SetLaxEndorse(bad); err == nil {
		t.Fatal("a file that is not a whole number of entries was accepted")
	}
	// Disarm.
	if err := e.SetLaxEndorse(""); err != nil || e.laxEndorseEntries() != nil {
		t.Fatalf("disarm: %v, %d", err, len(e.laxEndorseEntries()))
	}
}

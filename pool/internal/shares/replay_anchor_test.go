package shares

import (
	"encoding/hex"
	"errors"
	"testing"
)

type anchorVerifier struct {
	seen       [][32]byte
	unverified bool
}

func (a *anchorVerifier) Prepare(string, []byte, string, uint64) error { return nil }
func (a *anchorVerifier) Verify(string, [32]byte, uint64, uint64) ([32]byte, [32]byte, error) {
	return [32]byte{9}, [32]byte{9}, nil // a recomputed path that would disagree with the line
}
func (a *anchorVerifier) VerifyUnderAnchor(_ string, anchor [32]byte, _ [32]byte, _ uint64, _ uint64) ([32]byte, [32]byte, error) {
	a.seen = append(a.seen, anchor)
	if a.unverified {
		return [32]byte{}, [32]byte{}, ErrUnverifiable
	}
	return [32]byte{1}, [32]byte{2}, nil
}

// the replay verifies an anchored share under the anchor its line recorded, never one recomputed; a share the
// replay cannot verify now is kept and named, never dropped (pool-b2's roll of 17:01Z, 15 October 2026)
func TestReplayUnderTheRecordedAnchor(t *testing.T) {
	j, err := OpenJournal(t.TempDir() + "/j")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	anchor := [32]byte{7, 7, 7}
	hdr := hex.EncodeToString(make([]byte, 32))
	l := Line{Kind: "accepted", Account: "0xaa", Header: hdr, Mix: hex.EncodeToString([]byte{1}), Hash: hex.EncodeToString([]byte{2}), Height: 5919, Nonce: 3, Weight: 10, Algorithm: "igneumhash", Anchor: hex.EncodeToString(anchor[:])}
	l.Mix, l.Hash = hex.EncodeToString(append([]byte{1}, make([]byte, 31)...)), hex.EncodeToString(append([]byte{2}, make([]byte, 31)...))
	if _, err := j.Accepted(l); err != nil {
		t.Fatal(err)
	}
	v := &anchorVerifier{}
	n := 0
	replayed, dropped, err := j.Replay(v, func(Line) { n++ })
	if err != nil || replayed != 1 || dropped != 0 || n != 1 || len(v.seen) != 1 || v.seen[0] != anchor {
		t.Fatalf("replay under the recorded anchor: replayed %d dropped %d added %d seen %v err %v", replayed, dropped, n, v.seen, err)
	}
	// unverifiable now: kept, not dropped
	v2 := &anchorVerifier{unverified: true}
	n = 0
	replayed, dropped, err = j.Replay(v2, func(Line) { n++ })
	if err != nil || replayed != 1 || dropped != 0 || n != 1 {
		t.Fatalf("unverifiable kept: replayed %d dropped %d added %d err %v", replayed, dropped, n, err)
	}
	if !errors.Is(ErrUnverifiable, ErrUnverifiable) {
		t.Fatal("the error's identity")
	}
	if len(j.Outstanding()) != 1 {
		t.Fatalf("outstanding: %d", len(j.Outstanding()))
	}
}

package stratum

import (
	"testing"
	"time"
)

// TestCoolingPerWorker pins the cooling's key (the public pool of 08:27:04Z, 13 October 2026: one session of the
// founder's fleet address reached the bad-share threshold and every new session of that address was refused at
// authorize for five minutes): the worker that misbehaved is cooled, the address's other workers are not; a
// session without a worker name cools its address as before.
func TestCoolingPerWorker(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := &Server{BadShareCooling: 5 * time.Minute}
	s.coolAddress("0xFLEET", "pc-2", now)
	if !s.cooled("0xfleet", "pc-2", now.Add(time.Second)) {
		t.Fatalf("the worker that misbehaved must be cooled")
	}
	if s.cooled("0xfleet", "pc-1", now.Add(time.Second)) {
		t.Fatalf("the address's other worker must not be cooled")
	}
	if s.cooled("0xfleet", "pc-2", now.Add(6*time.Minute)) {
		t.Fatalf("the cooling must lift after its period")
	}
	s.coolAddress("0xSOLO", "", now)
	if !s.cooled("0xsolo", "", now.Add(time.Second)) {
		t.Fatalf("a nameless session cools its address")
	}
}

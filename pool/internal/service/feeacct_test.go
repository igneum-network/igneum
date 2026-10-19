package service

import (
	"testing"
	"time"

	"igneum.network/pool/internal/shares"
	"igneum.network/pool/internal/stratum"
)

// the verifiable fee waiver (the founder's word of 18:2x UK, 14 October 2026): a member whose fee session's shares
// credit the Ember fee address at the fee's fraction over the window is waived; a member whose fee is off, or any
// other miner, is charged one member height in a hundred under the pool's fee address; a reconnect keeps the
// standing verdict (keyed by member and worker) so no window is freed or charged twice.
func TestFeeWaiverWindows(t *testing.T) {
	const ember, pool = "0xF4b0Ae5D45684b56351Cc06B0420056668d1b801", "0x1111111111111111111111111111111111111111"
	member, other := "0x2222222222222222222222222222222222222222", "0x3333333333333333333333333333333333333333"
	t0 := time.Date(2026, 10, 14, 18, 0, 0, 0, time.UTC)
	now := t0
	s := &Service{cfg: Config{FeePercent: 1, EmberFeeAddress: ember, PoolFeeAddress: pool, FeeVerifyMinShares: 200, ShareRecency: 60, BlockSeconds: 20}}
	s.now = func() time.Time { return now }
	// (1) the member mines 1000 shares and its fee session 10 (1 percent) under the Ember address, keyed by the for block
	for i := 0; i < 1000; i++ {
		s.feeAccount(stratum.Share{Account: member, Worker: "worker-a"})
	}
	for i := 0; i < 10; i++ {
		s.feeAccount(stratum.Share{Account: ember, Worker: "worker-a", FeeFor: member})
	}
	// (2) another miner mines 1000 shares with no fee session
	for i := 0; i < 1000; i++ {
		s.feeAccount(stratum.Share{Account: other, Worker: "w"})
	}
	// before a verdict both are charged: one height in a hundred
	charged := func(addr, worker string, heights int) int {
		n := 0
		for i := 0; i < heights; i++ {
			if s.feePayee(addr, worker, uint64(i), 4, true) == pool {
				n++
			}
		}
		return n
	}
	if n := charged(member, "worker-a", 100); n != 1 {
		t.Fatalf("member before a verdict: %d charged heights in 100, want 1", n)
	}
	now = t0.Add(21 * time.Minute) // the window (ShareRecency x BlockSeconds = 20 min) has passed
	s.feeWindows(now)
	if ok, why := s.FeeDecide(member, "worker-a"); !ok {
		t.Fatalf("member with the fee on: not waived: %s", why)
	}
	if ok, why := s.FeeDecide(other, "w"); ok {
		t.Fatalf("another miner: waived: %s", why)
	}
	// (1) waived: no charged heights; (2) charged: one in a hundred
	if n := charged(member, "worker-a", 300); n != 0 {
		t.Fatalf("member verified: %d charged heights in 300, want 0", n)
	}
	if n := charged(other, "w", 300); n != 3 {
		t.Fatalf("another miner: %d charged heights in 300, want 3", n)
	}
	// (3) a reconnect mid-window keeps the verdict (the key is member and worker, not the session)
	for i := 0; i < 100; i++ {
		s.feeAccount(stratum.Share{Account: member, Worker: "worker-a"})
	}
	if ok, _ := s.FeeDecide(member, "worker-a"); !ok {
		t.Fatal("the verdict did not survive the reconnect's shares")
	}
	// (3) the fee switched off mid-window: the next window's verdict charges, and the charge count resumes, not restarts
	for i := 0; i < 900; i++ {
		s.feeAccount(stratum.Share{Account: member, Worker: "worker-a"})
	}
	now = now.Add(21 * time.Minute)
	s.feeWindows(now)
	if ok, why := s.FeeDecide(member, "worker-a"); ok {
		t.Fatalf("member with the fee off: still waived: %s", why)
	}
	if n := charged(member, "worker-a", 200); n != 2 {
		t.Fatalf("member after the fee went off: %d charged heights in 200, want 2", n)
	}
	// the charge is one in a hundred of EACH coordinate's heights: a worker issued jobs on five coordinates and
	// mining one of them is charged one height in a hundred on that one
	n5 := 0
	for i := 0; i < 100; i++ {
		for c := uint8(0); c < 5; c++ {
			if s.feePayee(other, "w5", uint64(i), c, true) == pool && c == 4 {
				n5++
			}
		}
	}
	if n5 != 1 {
		t.Fatalf("five coordinates: %d charged heights on coordinate 4 in 100, want 1", n5)
	}
	// the bridge for older Ember: a fee session with no for block is keyed by the member's worker name
	for i := 0; i < 1000; i++ {
		s.feeAccount(stratum.Share{Account: member, Worker: "worker-a"})
	}
	for i := 0; i < 10; i++ {
		s.feeAccount(stratum.Share{Account: ember, Worker: "worker-a"})
	}
	now = now.Add(21 * time.Minute)
	s.feeWindows(now)
	if ok, why := s.FeeDecide(member, "worker-a"); !ok {
		t.Fatalf("the bridge by worker name: not waived: %s", why)
	}
	// a fee session short of the fraction (0.5 percent against 1) is charged
	for i := 0; i < 1000; i++ {
		s.feeAccount(stratum.Share{Account: member, Worker: "worker-a"})
	}
	for i := 0; i < 5; i++ {
		s.feeAccount(stratum.Share{Account: ember, Worker: "worker-a", FeeFor: member})
	}
	now = now.Add(21 * time.Minute)
	s.feeWindows(now)
	if ok, why := s.FeeDecide(member, "worker-a"); ok {
		t.Fatalf("a fee session short of the fraction: waived: %s", why)
	}
	// a fee session under the Ember fee address is never charged: its shares are the fee (demonstration 4)
	if ok, _ := s.FeeDecide(ember, "worker-a"); !ok {
		t.Fatal("the fee session itself was charged")
	}
	if n := charged(ember, "worker-a", 300); n != 0 {
		t.Fatalf("the fee session: %d charged heights in 300, want 0", n)
	}
	// the pool's own charge shares never count toward a member's window
	s.feeAccount(stratum.Share{Account: pool, Worker: "worker-a"})
	if w := s.feeAcct[feeKey(pool, "worker-a")]; w != nil {
		t.Fatal("the pool's fee address got a window")
	}
}

// a roll (the handover form) restores the standing verdicts from the journal's fee_window lines: the next
// generation neither frees a charged member's window nor charges a waived member's first height again.
func TestFeeWaiverVerdictsSurviveTheRoll(t *testing.T) {
	const ember, pool = "0xF4b0Ae5D45684b56351Cc06B0420056668d1b801", "0x1111111111111111111111111111111111111111"
	member, other := "0x2222222222222222222222222222222222222222", "0x3333333333333333333333333333333333333333"
	path := t.TempDir() + "/shares.journal"
	j, err := shares.OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 14, 18, 0, 0, 0, time.UTC)
	now := t0
	cfg := Config{FeePercent: 1, EmberFeeAddress: ember, PoolFeeAddress: pool, FeeVerifyMinShares: 200, ShareRecency: 60, BlockSeconds: 20}
	s := &Service{cfg: cfg, journal: j}
	s.now = func() time.Time { return now }
	for i := 0; i < 1000; i++ {
		s.feeAccount(stratum.Share{Account: member, Worker: "worker-a"})
		s.feeAccount(stratum.Share{Account: other, Worker: "w"})
	}
	for i := 0; i < 10; i++ {
		s.feeAccount(stratum.Share{Account: ember, Worker: "worker-a", FeeFor: member})
	}
	now = t0.Add(21 * time.Minute)
	s.feeWindows(now)
	if ok, _ := s.FeeDecide(member, "worker-a"); !ok {
		t.Fatal("the member was not waived before the roll")
	}
	j.Close()
	// the next generation on the same journal
	j2, err := shares.OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	s2 := &Service{cfg: cfg, journal: j2}
	s2.now = func() time.Time { return now }
	if ok, why := s2.FeeDecide(member, "worker-a"); !ok {
		t.Fatalf("the waived member's verdict did not survive the roll: %s", why)
	}
	if ok, _ := s2.FeeDecide(other, "w"); ok {
		t.Fatal("the charged miner's verdict did not survive the roll")
	}
	n := 0
	for i := 0; i < 200; i++ {
		if s2.feePayee(member, "worker-a", uint64(i), 4, true) == pool {
			n++
		}
	}
	if n != 0 {
		t.Fatalf("the waived member was charged %d height(s) after the roll", n)
	}
}

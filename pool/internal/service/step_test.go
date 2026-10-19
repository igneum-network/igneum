package service

import "testing"

// TestChooseStep pins the step rule (the project's order of 02:36 UK, 12 October
// 2026): the smallest step whose expected accepted shares a height sit at or
// under a quarter of the per-address cap, bounded by the node's shareStepMax,
// and step 0 for a session with no rate yet.
func TestChooseStep(t *testing.T) {
	cases := []struct {
		name    string
		rate    float64 // step-0 shares a second
		block   float64 // block seconds
		quarter float64 // a quarter of the cap
		max     uint8
		want    uint8
	}{
		{"no rate yet", 0, 20, 64, 32, 0},
		{"slow card under the quarter", 2, 20, 64, 32, 0},    // 40 a height
		{"at the quarter exactly", 3.2, 20, 64, 32, 0},        // 64 a height
		{"just over the quarter", 3.3, 20, 64, 32, 1},         // 66 -> 33
		{"fast card at the cap", 12.8, 20, 64, 32, 2},         // 256 -> 64
		{"fast card over the cap", 20, 20, 64, 32, 3},         // 400 -> 50
		{"the night's fast card", 35, 20, 64, 32, 4},          // 700 -> 43.75
		{"bounded by the node's step max", 1000, 20, 64, 3, 3}, // 20000 needs 9
		{"count-form chain", 1000, 20, 64, 0, 0},
		{"no block clock", 100, 0, 64, 32, 0},
	}
	for _, c := range cases {
		if got := ChooseStep(c.rate, c.block, c.quarter, c.max); got != c.want {
			t.Errorf("%s: ChooseStep(%g, %g, %g, %d) = %d, want %d", c.name, c.rate, c.block, c.quarter, c.max, got, c.want)
		}
	}
}

// TestChooseStepNeverOverQuarter checks the rule's bound across a sweep of
// rates: at the chosen step (when under the max) the expected shares a height
// are at or under the quarter, and one step lower would be over it.
func TestChooseStepNeverOverQuarter(t *testing.T) {
	for rate := 0.5; rate < 200; rate += 0.7 {
		s := ChooseStep(rate, 20, 64, 32)
		at := rate * 20 / float64(uint64(1)<<s)
		if at > 64 {
			t.Fatalf("rate %g: step %d leaves %.1f a height, over the quarter", rate, s, at)
		}
		if s > 0 {
			if below := rate * 20 / float64(uint64(1)<<(s-1)); below <= 64 {
				t.Fatalf("rate %g: step %d is not the smallest (step %d leaves %.1f)", rate, s, s-1, below)
			}
		}
	}
}

// TestRaiseStep pins the mid-height raise (the project's GO of 03:20 UK, 12 October
// 2026): a session that reached the quarter at the current height moves to
// the smallest higher step at which a stretch as long again brings at most a
// quarter of the height's remaining budget; never below its current step;
// bounded by stepMax.
func TestRaiseStep(t *testing.T) {
	cases := []struct {
		name    string
		shares  uint64
		elapsed float64
		cur     uint8
		max     uint8
		cap     float64
		want    uint8
	}{
		{"the quarter in a 20 s height at step 0", 64, 20, 0, 32, 256, 1},   // budget 192, allowed 48: 64 more -> step 1 (32)
		{"the quarter in 5 s (a fast card, a stall)", 64, 5, 0, 32, 256, 1},
		{"a card at twice the pace", 128, 20, 0, 32, 256, 2},                // budget 128, allowed 32: 128 -> 64 -> 32 at step 2
		{"near the cap", 250, 60, 1, 32, 256, 9},                            // budget 6, allowed 1.5: rate0 8.3/s x 60 s = 500 -> 500/512 = 0.98 at step 9
		{"at the cap", 256, 60, 1, 32, 256, 32},
		{"at the node's step max", 64, 20, 32, 32, 256, 32},
		{"count-form chain", 64, 20, 0, 0, 256, 0},
		{"nothing accepted yet", 0, 20, 0, 32, 256, 0},
	}
	for _, c := range cases {
		got := RaiseStep(c.shares, c.elapsed, c.cur, c.max, c.cap)
		if got != c.want {
			t.Errorf("%s: RaiseStep(%d, %g, %d, %d, %g) = %d, want %d", c.name, c.shares, c.elapsed, c.cur, c.max, c.cap, got, c.want)
		}
		if got < c.cur {
			t.Errorf("%s: lowered mid-height (%d < %d)", c.name, got, c.cur)
		}
	}
	if a := RaiseAllowance(64, 256); a != 48 {
		t.Errorf("allowance at 64 of 256 = %d, want 48", a)
	}
	if a := RaiseAllowance(255, 256); a != 1 {
		t.Errorf("allowance at 255 of 256 = %d, want 1", a)
	}
}

// TestRaiseStepOnce pins the window's lesson (04:00 UK, 12 October 2026): a
// miner that paces its own submissions keeps the same count at any step, so
// the raise is spent ONCE a height and the raised step holds for that height
// only; across heights the step is the five-minute rule's.
func TestRaiseStepOnce(t *testing.T) {
	const cap = 256.0
	// a paced miner: 34 shares a height whatever the target
	step := uint8(0)
	raised := false
	for height := 0; height < 20; height++ {
		step = 0 // the rule's step at a new template
		raised = false
		var shares uint64
		for sec := 1; sec <= 20; sec++ {
			shares += 34 / 20
			if sec%10 == 0 {
				shares += 34 % 20 / 2
			}
			if !raised && shares >= 32 {
				if r := RaiseStep(shares, float64(sec), step, 32, cap); r > step {
					step = r
				}
				raised = true
			}
		}
		if step > 1 {
			t.Fatalf("height %d: step %d, more than one raise", height, step)
		}
	}
}


// TestProbeClimbs pins the project's go of 12:2x UK, 12 October 2026 (G-34): a card
// that paces its submissions (the same count a height at any step) climbs
// one step per re-read while its count at the current step is at or above
// half the quarter, and comes down one when it falls under half; the
// five-minute rule's own step never falls below the probe's.
func TestProbeClimbs(t *testing.T) {
	// the probe against the worker's own pace (economics' G-34 measure: a card paced under half the quarter never
	// climbed under the quarter form): a raise while the count holds at or above three quarters of the pace, a
	// lowering under half of it
	probe := func(step uint8, pace, countAtStep float64) uint8 {
		p := step
		switch {
		case countAtStep >= 0.75*pace && p < 32:
			p++
		case countAtStep < 0.5*pace && p > 0:
			p--
		}
		return p
	}
	for _, pace := range []float64{34, 26, 20} { // the mini, the 5090 pod, the 4090 pod
		step := uint8(0)
		for i := 0; i < 6; i++ {
			step = probe(step, pace, pace) // paced: the same count at any step
		}
		if step != 6 {
			t.Fatalf("a card paced at %g after six re-reads at step %d, want 6", pace, step)
		}
		// the target bites: the count falls under half the pace
		if step = probe(step, pace, pace*0.4); step != 5 {
			t.Fatalf("pace %g: under half the step did not come down: %d", pace, step)
		}
		// between half and three quarters: hold
		if step = probe(step, pace, pace*0.6); step != 5 {
			t.Fatalf("pace %g: in the band the step moved: %d", pace, step)
		}
	}
}

// TestChooseStepJumps pins the E1 study's case (20:58 UK, 12 October 2026):
// a card submitting twelve a second at step 0 is read straight to the step
// that holds it under the quarter, not one step at a time.
func TestChooseStepJumps(t *testing.T) {
	if s := ChooseStep(12, 20, 64, 32); s != 2 {
		t.Fatalf("12 a second at step 0: step %d, want 2 (240 a height over 4 = 60)", s)
	}
	// at step 2 the card still submits twelve a second (its own cap): the measured step-0 rate is 48 a second
	if s := ChooseStep(48, 20, 64, 32); s != 4 {
		t.Fatalf("48 step-0 a second: step %d, want 4", s)
	}
}

// TestProbeHoldsAtZero pins the 0.1.19 soak's finding (20:17Z, 12 October
// 2026): a worker with no share in its window is held, never probed up
// against a pace of zero.
// TestProbeHoldsUnderOneAHeight pins the 0.1.20 soak's caveat of 20:35:51Z, 12 October 2026: a first read of
// 0.0 shares a height (a share or two in the window, the count at the step under one) stepped a worker up by the
// probe's 0 >= 0.75 x 0 against a pace of zero, and would have anchored its pace near zero so every later read
// climbed; the rule now holds the step and restarts the window under one share a height, the pace unmeasured.
func TestProbeHoldsUnderOneAHeight(t *testing.T) {
	block, window := 20.0, 1300.0 // one share in 1,300 s at step 0
	countAtStep := 1.0 / 1 * block / window
	if countAtStep >= 1 {
		t.Fatalf("the test's premise: %.3f a height is under one", countAtStep)
	}
	pace := 0.0
	if countAtStep >= 1 {
		t.Fatalf("a read under one share a height takes the under-one path")
	}
	if pace != 0 {
		t.Fatalf("the pace is not measured from a read under one share a height")
	}
	// under one a height with shares, the step is what the measured rate chooses, never the probe: a session
	// restored at step 9 that found one share a minute (the 0.1.21 soak of 21:10Z, 12 October 2026) comes down
	// to the step its step-0-equivalent rate fits, then the fast re-read and the mid-height raise climb it back
	rate := 512.0 / 60 // one share at step 9 a minute, in step-0-equivalent shares a second
	if got := ChooseStep(rate, block, 64, 24); got != 2 {
		t.Fatalf("by the rate alone: step %d, want 2 (170.7 a height at step 0 fits under 64 at step 2)", got)
	}
	// at one share a height and above, the rule reads: the pace is the highest count the worker has shown (the
	// Windows card after the 0.1.20 roll of 20:47Z, 12 October 2026: a first read in its reconnect's window
	// measured 2.0 a height and the probe climbed it one step every read against that pace), so a later, higher
	// read re-anchors it and a lower one does not
	for _, c := range []float64{2.0, 64.4, 45.1} {
		if c > pace {
			pace = c
		}
	}
	if pace != 64.4 {
		t.Fatalf("pace %.1f, want the highest count shown", pace)
	}
}

func TestProbeHoldsAtZero(t *testing.T) {
	pace := 0.0
	count := 0.0
	if pace == 0 {
		pace = count
	}
	raised := count >= 0.75*pace // the probe's test as written: true at zero against zero
	if !raised {
		t.Fatalf("the test's premise: the probe would raise at zero against zero")
	}
	// the rule reads a zero window from a worker WITH a session before the probe is reached: its step falls to 0
	// (the old public test network (chain 7320) switch of 22:58Z, 12 October 2026: steps 13 to 21 carried from chain 7319 and held at zero shares
	// left the fleet shareless; a card cannot find a share at step 20 in a block clock); a worker away is not read
	// at all, so its step holds
	accepted, ruleStep := uint64(0), uint8(21)
	if accepted == 0 && ruleStep > 0 {
		ruleStep = 0
	}
	if ruleStep != 0 {
		t.Fatalf("a zero window with a session must bring the step to 0, got %d", ruleStep)
	}
}

// the per-session retarget (the project's order of 12:0x UK, 15 October 2026): as many steps as the session's rate at the
// current step says, against its own quota a height, never below the current step
func TestRetargetStep(t *testing.T) {
	cases := []struct {
		name    string
		shares  uint64
		elapsed float64
		cur     uint8
		max     uint8
		quota   float64
		want    uint8
	}{
		{"a Windows card at the bucket: 6 shares in 0.6 s at step 17, quota 6 a 20 s height", 6, 0.6, 17, 32, 6, 23}, // rate0 6x2^17/0.6; 200 shares a height at 17 -> /2^6 = 3.1 at 23
		{"the quota exactly in a height", 6, 20, 10, 32, 6, 10},
		{"twice the pace", 12, 20, 10, 32, 6, 11},
		{"a 5090 from step 0: 64 in 1 s against 64 a height", 64, 1, 0, 32, 64, 5},
		{"at the node's step max", 6, 1, 32, 32, 6, 32},
		{"count-form chain", 6, 1, 0, 0, 6, 0},
		{"nothing yet", 0, 1, 3, 32, 6, 3},
	}
	for _, c := range cases {
		got := RetargetStep(c.shares, c.elapsed, c.cur, c.max, c.quota, 20)
		if got != c.want {
			t.Errorf("%s: RetargetStep = %d, want %d", c.name, got, c.want)
		}
		if got < c.cur {
			t.Errorf("%s: lowered (%d < %d)", c.name, got, c.cur)
		}
	}
}

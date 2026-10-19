package service

import (
	"testing"
	"time"
)

// F-392 (17 October 2026): a present worker with no verified share accepted for silenceAfter comes down one step a
// silenceNotch toward its declared rate's step, and no further; an accepted share holds it.
func TestSilenceDecayComesDownOneStepAMinuteToTheFloor(t *testing.T) {
	s := &Service{now: time.Now}
	t0 := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	ws := &workerStep{ruleStep: 3, presentSince: t0}
	if s.silenceDecay("own-worker-1#5", ws, t0.Add(silenceAfter-time.Second), 0) {
		t.Fatalf("a notch before silenceAfter")
	}
	if !s.silenceDecay("own-worker-1#5", ws, t0.Add(silenceAfter), 0) || ws.ruleStep != 2 {
		t.Fatalf("no notch at silenceAfter: step %d", ws.ruleStep)
	}
	if s.silenceDecay("own-worker-1#5", ws, t0.Add(silenceAfter+30*time.Second), 0) {
		t.Fatalf("a second notch inside silenceNotch")
	}
	if !s.silenceDecay("own-worker-1#5", ws, t0.Add(silenceAfter+silenceNotch), 1) || ws.ruleStep != 1 {
		t.Fatalf("no notch a minute on: step %d", ws.ruleStep)
	}
	if s.silenceDecay("own-worker-1#5", ws, t0.Add(silenceAfter+2*silenceNotch), 1) || ws.ruleStep != 1 {
		t.Fatalf("a notch under the floor: step %d", ws.ruleStep)
	}
	// an accepted share resets the clock
	ws.ruleStep, ws.lastAcceptedAt = 3, t0.Add(10*time.Minute)
	if s.silenceDecay("own-worker-1#5", ws, t0.Add(10*time.Minute+silenceAfter-time.Second), 0) {
		t.Fatalf("a notch within silenceAfter of an accepted share")
	}
	if !s.silenceDecay("own-worker-1#5", ws, t0.Add(10*time.Minute+silenceAfter), 0) || ws.ruleStep != 2 {
		t.Fatalf("no notch at silenceAfter after the share: step %d", ws.ruleStep)
	}
}

// F-392: a re-dial after the grace restarts at the declared rate's step when a rate is declared, else at the
// standing step.
func TestRestartStepFollowsTheDeclaredRate(t *testing.T) {
	if got := restartStep(14, 0, 20, 16, 20); got != 14 {
		t.Fatalf("no declared rate: the standing step 14, got %d", got)
	}
	// 0.5 step-0 shares a second over a 20 s clock = 10 a height, under the quarter of 16: step 0
	if got := restartStep(14, 0.5, 20, 16, 20); got != 0 {
		t.Fatalf("a declared rate under the quarter: step 0, got %d", got)
	}
	// 64 a second = 1,280 a height: the smallest step bringing it under 16 is 7 (1280 / 128 = 10)
	if got := restartStep(0, 64, 20, 16, 20); got != 7 {
		t.Fatalf("a declared rate of 64 a second: step 7, got %d", got)
	}
}

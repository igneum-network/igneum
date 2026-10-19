package service

import (
	"strings"
	"sync"
	"testing"
	"time"

	"igneum.network/pool/internal/shares"
	"igneum.network/pool/internal/stratum"
)

// F-271 (16 October 2026): a result on an older job is the old step's evidence, never a count at the new step.
func TestCountsForRaiseAtTheCurrentStepAlone(t *testing.T) {
	if !countsForRaise(0, 0, 2744, 2744) {
		t.Fatal("a submit on the job at the current step and height counts")
	}
	if countsForRaise(0, 6, 2744, 2744) {
		t.Fatal("a result found at step 0 arriving after the raise to step 6 must not count at step 6")
	}
	if countsForRaise(6, 6, 2743, 2744) {
		t.Fatal("a submit for the height before must not count")
	}
}

// After a raise the next is considered only once a block clock has passed at the new step.
func TestRaiseAllowedWaitsABlockClockAfterARaise(t *testing.T) {
	if !raiseAllowed(false, 0, 20) {
		t.Fatal("the first raise of a height is always considered")
	}
	if raiseAllowed(true, 1, 20) {
		t.Fatal("one second after a raise no second raise is considered")
	}
	if raiseAllowed(true, 19.9, 20) {
		t.Fatal("under a block clock after a raise no second raise is considered")
	}
	if !raiseAllowed(true, 20, 20) {
		t.Fatal("a block clock after a raise the next is considered")
	}
	if !raiseAllowed(true, 1, 0) {
		t.Fatal("without a block clock the gate is open (the scripted forms)")
	}
}

// The burst of 18:19 UK on the Octominer rig's card (F-271) as the rule now reads it: 331 submissions in eight seconds, every one on the
// step-0 job after the first raise: one raise from the window's whole rate, no second within the block clock.
func TestRetargetReadsABurstAsOneStep(t *testing.T) {
	quota, block := 16.0, 20.0
	first := RetargetStep(110, 3, 0, 29, quota, block)
	if first != 6 {
		t.Fatalf("the first raise from 110 in 3 s at step 0: step 6, got %d", first)
	}
	// the backlog keeps arriving on the step-0 job: it does not count at step 6, and a block clock has not passed
	count := uint64(0)
	for i := 0; i < 221; i++ {
		if countsForRaise(0, first, 2744, 2744) {
			count++
		}
	}
	if count != 0 {
		t.Fatalf("the backlog counted %d at the new step", count)
	}
	if raiseAllowed(true, 5, block) {
		t.Fatal("a second raise five seconds after the first")
	}
}

// The refused-submit journal: eight lines a second per worker and reason, the rest counted as suppressed and carried.
func TestRefusalLogCollapsesABurst(t *testing.T) {
	l := newRefusalLog(8)
	at := time.Date(2026, 10, 16, 17, 19, 22, 0, time.UTC)
	r := stratum.Refusal{Address: "0xABC", Worker: "own-worker-1", JobID: "972e0e8311a71378", Nonce: "0x7",
		Reason: stratum.ReasonAdmission + ": " + stratum.ReasonRate + ": the session's submit bucket is empty (50 a second, a burst of 50)"}
	written := 0
	for i := 0; i < 300; i++ {
		line, w := l.line(at, r)
		if w {
			written++
			if line.Kind != "refused" || line.Member != "0xabc" || line.Worker != "own-worker-1" || line.JobID != r.JobID {
				t.Fatalf("the line: %+v", line)
			}
			if line.Suppressed != 0 {
				t.Fatalf("nothing suppressed yet on line %d: %d", written, line.Suppressed)
			}
		}
	}
	if written != 8 {
		t.Fatalf("eight lines in the second, got %d", written)
	}
	line, w := l.line(at.Add(time.Second), r)
	if !w || line.Suppressed != 292 {
		t.Fatalf("the next second's first line carries the 292 suppressed: %v %d", w, line.Suppressed)
	}
	other := r
	other.Reason = stratum.ReasonStale
	if _, w := l.line(at.Add(time.Second), other); !w {
		t.Fatal("another reason is its own key")
	}
	if got := reasonClass(r.Reason); got != stratum.ReasonAdmission+": "+stratum.ReasonRate {
		t.Fatalf("the reason's class: %q", got)
	}
	if got := reasonClass("stale"); got != "stale" {
		t.Fatalf("a bare reason is its own class: %q", got)
	}
}

// the security review's read of the first cut (18:57 UK 16 October 2026): the server calls OnRefused from every connection's
// goroutine; two sessions refused at once must not race on the collapse (run under -race).
func TestRefusedHookIsSafeFromManyGoroutines(t *testing.T) {
	s := &Service{refusals: newRefusalLog(8), now: time.Now}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				s.refused(stratum.Refusal{Address: "0xabc", Worker: "w" + string(rune('a'+g)), JobID: "deadbeefdeadbeef", Nonce: "0x1", Reason: stratum.ReasonStale})
			}
		}(g)
	}
	wg.Wait()
	s.mu.Lock()
	s.sweepRefusalsLocked(time.Now().Add(refusalKeyIdle))
	s.mu.Unlock()
	if n := len(s.refusals.keys); n != 0 {
		t.Fatalf("the sweep left %d keys", n)
	}
}

// The sweep flushes a key's trailing suppressed count as a last line and drops idle keys; the bound drops the oldest.
func TestRefusalLogSweepFlushesAndBounds(t *testing.T) {
	l := newRefusalLog(1)
	at := time.Date(2026, 10, 16, 17, 19, 22, 0, time.UTC)
	r := stratum.Refusal{Address: "0xabc", Worker: "own-worker-1", JobID: strings.Repeat("f", 40), Nonce: "zz", Reason: stratum.ReasonStale,
		Job: &stratum.Job{Height: 2744, Coord: 4, HasCoord: true}}
	line, w := l.line(at, r)
	if !w || line.JobID != strings.Repeat("f", 16) || line.Nonce != 0 {
		t.Fatalf("the first line: %v %+v", w, line)
	}
	for i := 0; i < 5; i++ {
		l.line(at, r)
	}
	var out []shares.Line
	l.sweep(at.Add(refusalKeyIdle-time.Second), refusalKeyIdle, func(x shares.Line) { out = append(out, x) })
	if len(out) != 0 || len(l.keys) != 1 {
		t.Fatalf("a live key swept: %d lines, %d keys", len(out), len(l.keys))
	}
	l.sweep(at.Add(refusalKeyIdle), refusalKeyIdle, func(x shares.Line) { out = append(out, x) })
	if len(out) != 1 || out[0].Suppressed != 5 || out[0].Height != 2744 || out[0].Coord != 4 || out[0].NodeReason != stratum.ReasonStale {
		t.Fatalf("the flushed line: %+v", out)
	}
	if len(l.keys) != 0 {
		t.Fatal("the idle key stays")
	}
	for i := 0; i < refusalKeysMax+10; i++ {
		l.line(at.Add(time.Duration(i)*time.Millisecond), stratum.Refusal{Address: "0xabc", Worker: "w" + strconvItoa(i), Reason: stratum.ReasonStale})
	}
	out = nil
	l.sweep(at.Add(time.Second), refusalKeyIdle, func(x shares.Line) { out = append(out, x) })
	if len(l.keys) != refusalKeysMax {
		t.Fatalf("the bound: %d keys", len(l.keys))
	}
	if _, ok := l.keys[refusalKey{"0xabc", "w0", stratum.ReasonStale}]; ok {
		t.Fatal("the oldest key survived the bound")
	}
}

func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

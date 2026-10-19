package service

import (
	"strconv"
	"strings"
	"time"

	"igneum.network/pool/internal/limits"
	"igneum.network/pool/internal/shares"
	"igneum.network/pool/internal/stratum"
)

// refusalLinesASecond bounds the refused-submit journal: at most this many lines a second per worker and reason; the
// refusals past it are counted and carried on the key's next line as suppressed, so the count is exact while a
// flooding session cannot grow the journal faster than this.
const refusalLinesASecond = 8

// refusalKeyIdle is how long a key (a member, worker and reason class) stays in the collapse after its last refusal
// before the sweep flushes its trailing suppressed count and drops it.
const refusalKeyIdle = 2 * time.Minute

// refusalKeysMax bounds the collapse's keys: past it the sweep drops the oldest idle keys first, whatever their age.
const refusalKeysMax = 4096

type refusalKey struct {
	member, worker, reason string
}

type refusalState struct {
	second     int64     // the unix second of the lines counted
	lines      int       // lines written in that second
	suppressed uint64    // refusals not journaled since the last line written
	seen       time.Time // the last refusal on the key
	height     uint64    // the last refusal's height and coordinate, for the flushed line
	coord      uint8
	hasCoord   bool
}

// refusalLog collapses refused submits into journal lines; every method runs under the service lock (the hook and
// the sweep take it: a Service method each).
type refusalLog struct {
	perSecond int
	keys      map[refusalKey]*refusalState
}

func newRefusalLog(perSecond int) *refusalLog {
	return &refusalLog{perSecond: perSecond, keys: map[refusalKey]*refusalState{}}
}

// reasonClass is the reason's leading words (the class, never the free text that follows a colon).
func reasonClass(reason string) string {
	if i := strings.Index(reason, ": "); i > 0 {
		head := reason[:i]
		// the admission's form "refused before acceptance: <case>: ..." keeps its case
		if head == stratum.ReasonAdmission {
			rest := reason[i+2:]
			if j := strings.Index(rest, ":"); j > 0 {
				return head + ": " + rest[:j]
			}
			return head + ": " + rest
		}
		return head
	}
	return reason
}

// line is the journal line for a refusal and whether it is written now (false = counted as suppressed).
func (l *refusalLog) line(now time.Time, r stratum.Refusal) (shares.Line, bool) {
	k := refusalKey{strings.ToLower(r.Address), r.Worker, reasonClass(r.Reason)}
	st := l.keys[k]
	sec := now.Unix()
	if st == nil {
		st = &refusalState{second: sec}
		l.keys[k] = st
	}
	st.seen = now
	if r.Job != nil {
		st.height, st.coord, st.hasCoord = r.Job.Height, r.Job.Coord, r.Job.HasCoord
	}
	if st.second != sec {
		st.second, st.lines = sec, 0
	}
	if st.lines >= l.perSecond {
		st.suppressed++
		return shares.Line{}, false
	}
	st.lines++
	line := shares.Line{Kind: "refused", UTC: now.UTC().Format(time.RFC3339), Member: k.member, Worker: r.Worker,
		JobID: jobIDOf(r.JobID), Nonce: nonceOf(r.Nonce), NodeReason: r.Reason, Suppressed: st.suppressed}
	if r.Job != nil {
		line.Height, line.Coord, line.HasCoord = r.Job.Height, r.Job.Coord, r.Job.HasCoord
	}
	st.suppressed = 0
	return line, true
}

// sweep flushes and drops the keys idle past idle (a trailing suppressed count goes out as a last line carrying the
// key's member, worker, reason class and last height), and past refusalKeysMax the oldest keys first.
// agedKey is a key with its last refusal's time, for the sweep's ordering.
type agedKey struct {
	k  refusalKey
	at time.Time
}

func (l *refusalLog) sweep(now time.Time, idle time.Duration, write func(shares.Line)) {
	var drop []agedKey
	for k, st := range l.keys {
		if now.Sub(st.seen) >= idle {
			drop = append(drop, agedKey{k, st.seen})
		}
	}
	if over := len(l.keys) - len(drop) - refusalKeysMax; over > 0 {
		var rest []agedKey
		for k, st := range l.keys {
			if now.Sub(st.seen) < idle {
				rest = append(rest, agedKey{k, st.seen})
			}
		}
		sortAged(rest)
		if over > len(rest) {
			over = len(rest)
		}
		drop = append(drop, rest[:over]...)
	}
	for _, d := range drop {
		st := l.keys[d.k]
		if st == nil {
			continue
		}
		if st.suppressed > 0 {
			write(shares.Line{Kind: "refused", UTC: now.UTC().Format(time.RFC3339), Member: d.k.member, Worker: d.k.worker,
				NodeReason: d.k.reason, Suppressed: st.suppressed, Height: st.height, Coord: st.coord, HasCoord: st.hasCoord})
		}
		delete(l.keys, d.k)
	}
}

// sortAged orders by the last refusal, oldest first (an insertion sort: the slice is small and rare).
func sortAged(a []agedKey) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j].at.Before(a[j-1].at); j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// jobIDOf is the job id as journaled: the client's text capped at the pool's own id length (an unknown id is journaled
// as sent, never longer than a pool-issued one).
func jobIDOf(text string) string {
	if len(text) > limits.JobIDHexLen {
		return text[:limits.JobIDHexLen]
	}
	return text
}

// nonceOf reads a submit's nonce text (hex, with or without 0x) as a number; 0 when it is not one.
func nonceOf(text string) uint64 {
	n, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(text)), "0x"), 16, 64)
	if err != nil {
		return 0
	}
	return n
}

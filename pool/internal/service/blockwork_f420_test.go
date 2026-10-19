package service

import (
	"strings"
	"testing"

	"igneum.network/pool/internal/jobsource"
)

func h32(b byte) [32]byte {
	var h [32]byte
	for i := range h {
		h[i] = b
	}
	return h
}

// F-420's pool half (0.1.81): a block job is issued only on a sealer's work that is on the head.
func TestBlockWorkStaleNamesTheFact(t *testing.T) {
	onHead := jobsource.Work{Header: h32(0xaa), ShareParent: h32(0x01)}
	if why := blockWorkStale(onHead, blockWork{}, false); why != "" {
		t.Fatalf("the first work seen is on the head: %q", why)
	}
	// the node's own word: a zero headerHash with staleTemplate (0.10.51)
	zero := jobsource.Work{ShareParent: h32(0x02), StaleTemplate: "round 2 waits"}
	if why := blockWorkStale(zero, blockWork{}, false); !strings.Contains(why, "headerHash is zero") || !strings.Contains(why, "round 2 waits") {
		t.Fatalf("a zero headerHash: %q", why)
	}
	// a carried parent that is not the head
	off := jobsource.Work{Header: h32(0xbb), ShareParent: h32(0x02), ParentHash: h32(0x01), HasParent: true}
	if why := blockWorkStale(off, blockWork{}, false); !strings.Contains(why, "is not the head") {
		t.Fatalf("a parent off the head: %q", why)
	}
	// the night of 16 October: the header standing while the parent moved (3,871 under parent 3,872)
	last := blockWork{header: h32(0xaa), parent: h32(0x01), height: 3871}
	moved := jobsource.Work{Header: h32(0xaa), ShareParent: h32(0x02)}
	if why := blockWorkStale(moved, last, true); !strings.Contains(why, "previous parent") {
		t.Fatalf("a header standing across a parent move: %q", why)
	}
	// the sealer refreshed: a new header at the new parent
	fresh := jobsource.Work{Header: h32(0xcc), ShareParent: h32(0x02)}
	if why := blockWorkStale(fresh, last, true); why != "" {
		t.Fatalf("a fresh header at the new parent is on the head: %q", why)
	}
	// the same package polled again inside a height is on the head
	same := jobsource.Work{Header: h32(0xaa), ShareParent: h32(0x01)}
	if why := blockWorkStale(same, last, true); why != "" {
		t.Fatalf("the same work inside a height: %q", why)
	}
}

// the security review's rider on 0.1.80: a node's answer with line breaks cannot forge a log line.
func TestRawAnswerCollapsesWhitespace(t *testing.T) {
	got := rawAnswer([]byte("{\"accepted\": true}\nBLOCK: forged line\n"))
	if strings.ContainsAny(got, "\n\r") || got != "{\"accepted\": true} BLOCK: forged line" {
		t.Fatalf("whitespace not collapsed: %q", got)
	}
	if rawAnswer([]byte("  ")) != "(none)" {
		t.Fatalf("an empty answer")
	}
	if l := len(rawAnswer([]byte(strings.Repeat("x", 500)))); l != 202 {
		t.Fatalf("not bounded at 200 + ..: %d", l)
	}
}

package jobsource

import (
	"strings"
	"testing"
)

// 0.1.83 (the security review's read of 0.1.81): a node's staleTemplate text reaches the pool's log bounded and on one line.
func TestBoundedTextCollapsesAndBounds(t *testing.T) {
	if got := BoundedText("round 2\nshare lane: forged line\r\n  waits"); got != "round 2 share lane: forged line waits" {
		t.Fatalf("whitespace not collapsed: %q", got)
	}
	if got := BoundedText(strings.Repeat("x", 500)); len(got) != 202 || !strings.HasSuffix(got, "..") {
		t.Fatalf("not bounded at 200 + ..: %d", len(got))
	}
	if BoundedText("   ") != "" {
		t.Fatalf("blank text")
	}
}

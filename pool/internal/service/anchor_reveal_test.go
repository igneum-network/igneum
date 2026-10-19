package service

import (
	"context"
	"testing"
)

type anchorNode struct {
	Node
	calls []uint64
}

func (n *anchorNode) Call(_ context.Context, method string, params interface{}, out interface{}) error {
	if method == "eth_getBlockByNumber" {
		p := params.([]interface{})
		var h uint64
		for _, c := range p[0].(string)[2:] {
			h = h*16 + uint64(map[rune]int{'0': 0, '1': 1, '2': 2, '3': 3, '4': 4, '5': 5, '6': 6, '7': 7, '8': 8, '9': 9, 'a': 10, 'b': 11, 'c': 12, 'd': 13, 'e': 14, 'f': 15}[c])
		}
		n.calls = append(n.calls, h)
		*(out.(*struct {
			Hash string `json:"hash"`
		})) = struct {
			Hash string `json:"hash"`
		}{Hash: "0x" + "ab" + "00000000000000000000000000000000000000000000000000000000000000"[:60] + "cd"}
	}
	return nil
}

// the pool resolves a period's anchor where the node does (program.go programAnchorHeight: start minus the
// coordinate's reveal D), D learned from the coordinate's named templates; the panel's cells of 18:19 UK, 15 October 2026
func TestPeriodAnchorAtStartMinusTheCoordinateReveal(t *testing.T) {
	s := &Service{cfg: Config{ProgramRevealBlocks: 2}}
	n := &anchorNode{}
	// the chain's default reveal (2) until a template teaches the coordinate's: height 6000 lies in the period
	// 5952 to 6015 (6016 is the next period's own start), so start 5952, at 5950
	if _, at, err := s.periodAnchor(context.Background(), n, 0, false, "doc16", 6000, 64); err != nil || at != 5950 {
		t.Fatalf("default reveal: at %d err %v", at, err)
	}
	// a named template taught D = 64 on doc16 (start 5952, the node's anchor height 5888)
	s.revealOf = map[string]uint64{"doc16": 64}
	if _, at, err := s.periodAnchor(context.Background(), n, 0, false, "doc16", 6000, 64); err != nil || at != 5888 {
		t.Fatalf("learned reveal: at %d err %v", at, err)
	}
	// the next period (6016 to 6079, its start 6016, the anchor at 5952) and two on
	for _, c := range []struct{ h, want uint64 }{{6020, 5952}, {6080, 6016}, {6144, 6080}} {
		if _, at, _ := s.periodAnchor(context.Background(), n, 0, false, "doc16", c.h, 64); at != c.want {
			t.Fatalf("height %d: at %d want %d", c.h, at, c.want)
		}
	}
	// another coordinate with no reveal of its own named (D 0): the period's first block
	s.revealOf["doc19"] = 0
	if _, at, _ := s.periodAnchor(context.Background(), n, 0, false, "doc19", 6000, 64); at != 5952 {
		t.Fatalf("D 0: at %d", at)
	}
	// below the reveal: the genesis
	if _, at, _ := s.periodAnchor(context.Background(), n, 0, false, "doc16", 60, 64); at != 0 {
		t.Fatalf("genesis: at %d", at)
	}
}

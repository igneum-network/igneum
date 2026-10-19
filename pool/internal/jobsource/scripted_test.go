package jobsource

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestScripted pins the scripted chain state (the product team's VectorWork router soak, 13 October 2026): one
// template per coordinate with its own header, a height per block clock, a coordinate's difficulty doubled at
// its minute (its share target halved), a coordinate emptied for its span, shares accepted on submit.
func TestScripted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	body := `{"chain_id": 7399, "block_seconds": 20, "share_target": "0x0000ffff00000000000000000000000000000000000000000000000000000000", "shares_per_block": 160,
	 "coordinates": [{"coord": 0, "algorithm": "igneumhash", "params": {"version": 8}, "difficulty": "1000", "reward_wei": "1"},
	                 {"coord": 1, "algorithm": "igneumhash", "params": {"version": 11}, "difficulty": "2000", "reward_wei": "2"}],
	 "moves": [{"at_s": 300, "coord": 1, "difficulty_x": 2}, {"at_s": 600, "coord": 0, "empty_s": 100}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }
	s, err := NewScripted(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	w0, err := s.GetWorkCoord(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	w1, err := s.GetWorkCoord(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !w0.HasCoord || w0.Coord != 0 || !w1.HasCoord || w1.Coord != 1 || w0.Header == w1.Header {
		t.Fatalf("coordinates: %d %d, headers equal %v", w0.Coord, w1.Coord, w0.Header == w1.Header)
	}
	if w0.Height != 1 || w1.Difficulty != "2000" || w1.RewardWei != "2" || !w0.HasShareLane() {
		t.Fatalf("height %d difficulty %q reward %q lane %v", w0.Height, w1.Difficulty, w1.RewardWei, w0.HasShareLane())
	}
	if _, err := s.GetWorkCoord(ctx, 2); err == nil {
		t.Fatalf("a coordinate the script lacks must be refused")
	}
	// a block clock later: the next height, a new header, the parent the previous header
	now = now.Add(21 * time.Second)
	w0b, _ := s.GetWorkCoord(ctx, 0)
	if w0b.Height != 2 || w0b.Header == w0.Header || w0b.ShareParent != w0.Header {
		t.Fatalf("height %d, header moved %v, parent is the previous header %v", w0b.Height, w0b.Header != w0.Header, w0b.ShareParent == w0.Header)
	}
	// the move at 300 s: coordinate 1's difficulty doubled, its share target halved
	now = now.Add(300 * time.Second)
	w1b, _ := s.GetWorkCoord(ctx, 1)
	if w1b.Difficulty != "4000" {
		t.Fatalf("difficulty after the move %q, want 4000", w1b.Difficulty)
	}
	t1, _ := new(big.Int).SetString(strings.Trim(string(w1.ShareTarget), "\""), 0)
	t1b, _ := new(big.Int).SetString(strings.Trim(string(w1b.ShareTarget), "\""), 0)
	if t1 == nil || t1b == nil || new(big.Int).Rsh(t1, 1).Cmp(t1b) != 0 {
		t.Fatalf("the share target did not halve: %v -> %v", t1, t1b)
	}
	// the empty span at 600 s for coordinate 0
	now = now.Add(300 * time.Second)
	if _, err := s.GetWorkCoord(ctx, 0); err == nil {
		t.Fatalf("coordinate 0 must have no template in its empty span")
	}
	if _, err := s.GetWorkCoord(ctx, 1); err != nil {
		t.Fatalf("coordinate 1 serves through coordinate 0's empty span: %v", err)
	}
	now = now.Add(101 * time.Second)
	if _, err := s.GetWorkCoord(ctx, 0); err != nil {
		t.Fatalf("coordinate 0 serves again after its span: %v", err)
	}
	// the RPCs the pool asks
	var chain string
	if err := s.Call(ctx, "eth_chainId", []interface{}{}, &chain); err != nil || chain != "0x1ce7" {
		t.Fatalf("chain id %q %v", chain, err)
	}
	var ans struct {
		Accepted bool `json:"accepted"`
	}
	if err := s.Call(ctx, "igneum_submitShare", []interface{}{"0x0"}, &ans); err != nil || !ans.Accepted {
		t.Fatalf("submit %+v %v", ans, err)
	}
	if sm := s.Summary(); sm["moves_applied"].(int) != 2 {
		t.Fatalf("moves applied %v", sm["moves_applied"])
	}
}

// TestLastHeightReadsDecimal pins the settlement team's finding of 16:30 UK, 13 October 2026: a bare-number
// coordLastHeight (128) was read as hex (296) on the pool's accounting and the notify.
func TestLastHeightReadsDecimal(t *testing.T) {
	obj := map[string]json.RawMessage{
		"headerHash": json.RawMessage(`"0x` + strings.Repeat("ab", 32) + `"`), "target": json.RawMessage(`"0x` + strings.Repeat("0f", 32) + `"`),
		"height": json.RawMessage(`129`), "algorithm": json.RawMessage(`"igneumhash"`), "coord": json.RawMessage(`1`), "coordLastHeight": json.RawMessage(`128`), "anchorHeight": json.RawMessage(`120`),
	}
	w, err := workFromObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	if w.LastHeight != 128 || w.AnchorHeight != 120 {
		t.Fatalf("last height %d anchor height %d, want 128 and 120 (decimal, not hex)", w.LastHeight, w.AnchorHeight)
	}
}

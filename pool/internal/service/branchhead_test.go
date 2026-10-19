package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"igneum.network/pool/internal/jobsource"
	"igneum.network/pool/internal/ledger"
	"igneum.network/pool/internal/validate"
)

// branchNode is a configured node with its own head, chain id and a block hash per height (its branch).
type branchNode struct {
	Node
	chain  uint64
	head   uint64
	hashAt func(h uint64) string
}

func (n *branchNode) Call(_ context.Context, method string, params interface{}, out interface{}) error {
	switch method {
	case "eth_chainId":
		*(out.(*string)) = fmt.Sprintf("0x%x", n.chain)
	case "eth_blockNumber":
		*(out.(*string)) = fmt.Sprintf("0x%x", n.head)
	case "eth_getBlockByNumber":
		var h uint64
		tag := params.([]interface{})[0].(string)
		if tag == "latest" {
			h = n.head
		} else {
			fmt.Sscanf(tag[2:], "%x", &h) //nolint:errcheck
		}
		if h > n.head {
			return fmt.Errorf("no block %d", h)
		}
		*(out.(*struct {
			Number string `json:"number"`
			Hash   string `json:"hash"`
		})) = struct {
			Number string `json:"number"`
			Hash   string `json:"hash"`
		}{Number: fmt.Sprintf("0x%x", h), Hash: n.hashAt(h)}
	default:
		return fmt.Errorf("unexpected %s", method)
	}
	return nil
}

// TestBestHeadReadsThePoolsOwnBranch pins 0.1.64 (the security review's finding on 0.1.63, F-193, 16 October 2026): the
// reference is the head of the pool's own branch, never the highest count over every node. F-143's shape: the
// source stands at H on the main; a configured node alone on a lighter sibling branch stands fourteen blocks ahead
// by count and its block at H differs from the source's, so it contributes nothing and the source's templates are
// issued; a node ahead ON the source's branch (its block at H the source's) raises the reference; a node on another
// chain id contributes nothing whatever its count.
func TestBestHeadReadsThePoolsOwnBranch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	body := `{"chain_id": 7399, "block_seconds": 600, "share_target": "0x0000ffff00000000000000000000000000000000000000000000000000000000",
	 "coordinates": [{"coord": 0, "algorithm": "null", "params": {"v": 0}, "difficulty": "1000", "reward_wei": "1"}], "moves": []}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1_700_000_000, 0)
	now := start
	script, err := jobsource.NewScripted(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc := New(Config{WindowN: 2, ShareDifficulty: 2, Maturity: 3, ShareLane: true, Coordinates: []uint8{0}, RPCURL: "http://source/", ChainID: 7399}, script, validate.Null{}, ledger.New(ledger.NewMemory()))
	svc.now = func() time.Time { return now }
	h := script.Height()
	// the source's own hash at any height, as the scripted source answers it
	srcHash := func(n uint64) string {
		var blk struct {
			Number string `json:"number"`
			Hash   string `json:"hash"`
		}
		if err := script.Call(ctx, "eth_getBlockByNumber", []interface{}{fmt.Sprintf("0x%x", n), false}, &blk); err != nil {
			t.Fatal(err)
		}
		return blk.Hash
	}
	// (1) F-143's shape: a lone node fourteen blocks ahead by count on a sibling branch (its block at H differs)
	lone := &branchNode{chain: 7399, head: h + 14, hashAt: func(n uint64) string { return fmt.Sprintf("0x%064x", n+0xdead) }}
	svc.mu.Lock()
	svc.shareNodes = []shareNode{{url: "http://lone/", node: lone}}
	svc.mu.Unlock()
	svc.readBestHead(ctx)
	if behind, best, from := svc.behindBestHead(h); behind || best != h || from != "http://source/" {
		t.Fatalf("a lone sibling branch raised the reference: behind %v best %d from %q (the source at %d)", behind, best, from, h)
	}
	// (2) a node ahead ON the source's branch (its block at H the source's): the reference rises to it
	onBranch := &branchNode{chain: 7399, head: h + 10, hashAt: func(n uint64) string {
		if n <= h {
			return srcHash(n)
		}
		return fmt.Sprintf("0x%064x", n+0xbeef)
	}}
	now = start.Add(ChainCheckEvery + time.Second)
	svc.mu.Lock()
	svc.shareNodes = []shareNode{{url: "http://lone/", node: lone}, {url: "http://on/", node: onBranch}}
	svc.mu.Unlock()
	svc.readBestHead(ctx)
	if behind, best, from := svc.behindBestHead(h); !behind || best != h+10 || from != "http://on/" {
		t.Fatalf("a node ahead on the pool's own branch did not raise the reference: behind %v best %d from %q", behind, best, from)
	}
	// (3) a node on another chain id, ahead by count and holding a block at H with the source's hash by chance:
	// nothing (the chain id read with the head)
	other := &branchNode{chain: 7322, head: h + 50, hashAt: srcHash}
	now = start.Add(2*ChainCheckEvery + 2*time.Second)
	svc.mu.Lock()
	svc.shareNodes = []shareNode{{url: "http://other/", node: other}}
	svc.mu.Unlock()
	svc.readBestHead(ctx)
	if behind, best, from := svc.behindBestHead(h); behind || best != h || from != "http://source/" {
		t.Fatalf("a node on another chain raised the reference: behind %v best %d from %q", behind, best, from)
	}
}

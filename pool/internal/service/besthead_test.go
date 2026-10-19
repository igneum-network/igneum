package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"igneum.network/pool/internal/jobsource"
	"igneum.network/pool/internal/ledger"
	"igneum.network/pool/internal/validate"
)

// headNode answers eth_blockNumber with a fixed height and igneum_getWork [c] with a package for one coordinate it
// seals (the sealers' own answer for any other: "this node seals coordinate N, not c").
type headNode struct {
	Node
	head    uint64
	seals   uint8
	hasSeal bool
	probes  int
}

func (n *headNode) Call(_ context.Context, method string, _ interface{}, out interface{}) error {
	switch method {
	case "eth_blockNumber":
		*(out.(*string)) = fmt.Sprintf("0x%x", n.head)
		return nil
	case "eth_chainId":
		*(out.(*string)) = "0x1ce7"
		return nil
	}
	return fmt.Errorf("unexpected %s", method)
}

func (n *headNode) GetWorkCoord(_ context.Context, c uint8) (jobsource.Work, error) {
	n.probes++
	if !n.hasSeal || n.seals != c {
		return jobsource.Work{}, fmt.Errorf("igneum vectorwork: the block's coordinate is missing or out of range: this node seals coordinate %d, not %d", n.seals, c)
	}
	return jobsource.Work{Coord: c, HasCoord: true, ShareHeight: n.head, Height: n.head}, nil
}

// TestBestHeadGuardHoldsALaggingTemplate pins the project's rule of 16 October 2026 (0.1.63): a template more than three
// blocks under the chain's best known head, read from every configured node and not the source alone, is never
// issued; at or within three of it, it is. The scripted source stands at its own height; a fan-out node reports the
// chain ten blocks on.
func TestBestHeadGuardHoldsALaggingTemplate(t *testing.T) {
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
	svc.Server.TrustClaimed = true
	member := "0x00000000000000000000000000000000000f1630"
	server, client := net.Pipe()
	go svc.Server.ServeConn(ctx, server)
	w := bufio.NewWriter(client)
	r := bufio.NewReader(client)
	send := func(id int, method string, params interface{}) {
		b, _ := json.Marshal(map[string]interface{}{"id": id, "method": method, "params": params})
		w.Write(append(b, '\n'))
		w.Flush()
	}
	send(1, "mining.subscribe", []string{"test/0"})
	if _, err := r.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	send(2, "mining.authorize", []string{member, "w"})
	reply, err := r.ReadBytes('\n')
	if err != nil || !strings.Contains(string(reply), `"result":true`) {
		t.Fatalf("authorize: %s %v", reply, err)
	}
	h := script.Height()
	// a fan-out node ten blocks on, ON THE SOURCE'S BRANCH (its block at the source's tip the source's own, 0.1.64):
	// the best known head is its, the scripted template is held
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
	far := &branchNode{chain: 7399, head: h + 10, hashAt: func(n uint64) string {
		if n <= h {
			return srcHash(n)
		}
		return fmt.Sprintf("0x%064x", n+0xfa4)
	}}
	svc.mu.Lock()
	svc.shareNodes = append(svc.shareNodes, shareNode{url: "http://far/", node: far})
	svc.mu.Unlock()
	svc.readBestHead(ctx)
	if got, best, from := svc.behindBestHead(h); !got || best != h+10 || from != "http://far/" {
		t.Fatalf("the best head was not read from the fan-out: %v %d %q", got, best, from)
	}
	if err := svc.pollShareLane(ctx); err == nil {
		t.Fatal("a template ten blocks under the best known head was issued")
	}
	if jobs := svc.Server.MemberJobs(member, "w"); len(jobs) != 0 {
		t.Fatalf("a job was issued on the lagging template: %+v", jobs)
	}
	// the fan-out node within three of the template: issued
	far.head = h + 3
	now = start.Add(ChainCheckEvery + time.Second)
	if err := svc.pollShareLane(ctx); err != nil {
		t.Fatalf("a template within three of the best head was held: %v", err)
	}
	if jobs := svc.Server.MemberJobs(member, "w"); len(jobs) != 1 {
		t.Fatalf("the job was not issued once the head agreed: %+v", jobs)
	}
	client.Close()
}

// TestFindSealerAsksTheConfiguredNodes pins the sealer finder (0.1.63): for a paid coordinate the configuration names
// no sealer for, the configured nodes are asked igneum_getWork [c]; the one whose package carries c becomes the
// coordinate's sealer (its template source and share target) and is not probed again inside the interval.
func TestFindSealerAsksTheConfiguredNodes(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	now := start
	a := &headNode{head: 100, seals: 0, hasSeal: true}
	b := &headNode{head: 100, seals: 3, hasSeal: true}
	svc := &Service{cfg: Config{RPCURL: "http://a/"}, node: a}
	svc.now = func() time.Time { return now }
	svc.shareNodes = []shareNode{{url: "http://b/", node: b}}
	if found := svc.findSealer(context.Background(), 3); found != b {
		t.Fatalf("the sealer of 3 was not found among the configured nodes: %v", found)
	}
	if src := svc.coordSource(3); src != b {
		t.Fatalf("the found sealer is not the coordinate's source: %v", src)
	}
	if a.probes != 1 || b.probes != 1 {
		t.Fatalf("each configured node is asked once: a %d, b %d", a.probes, b.probes)
	}
	// a coordinate no node seals: nil, and not asked again inside the interval
	if found := svc.findSealer(context.Background(), 4); found != nil {
		t.Fatalf("a sealer from nowhere for 4: %v", found)
	}
	if found := svc.findSealer(context.Background(), 4); found != nil || a.probes != 2 {
		t.Fatalf("coordinate 4 was asked again inside the interval (a %d)", a.probes)
	}
	now = start.Add(ChainCheckEvery + time.Second)
	b.seals = 4
	if found := svc.findSealer(context.Background(), 4); found != b {
		t.Fatalf("the sealer of 4 was not found after the interval: %v", found)
	}
}

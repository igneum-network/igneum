//go:build igneumhash

package service

import (
	"bufio"
	"context"
	"encoding/json"
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

// anchoredNull is the null stand-in with a period, so the share lane's anchor rule runs in the test.
type anchoredNull struct {
	validate.Null
	period uint64
}

func (anchoredNull) VerifyOn(algorithm, digest string, header [32]byte, height uint64, nonce uint64) ([32]byte, [32]byte, error) {
	return validate.Null{}.Verify(algorithm, header, height, nonce)
}
func (a anchoredNull) PeriodBlocks(string) uint64 { return a.period }
func (anchoredNull) VerifyUnder(algorithm, digest string, anchor [32]byte, header [32]byte, height uint64, nonce uint64) ([32]byte, [32]byte, error) {
	return validate.Null{}.Verify(algorithm, header, height, nonce)
}

// registryNode wraps the scripted source with a registry view naming each coordinate's reveal (igneum_vectorRegistry).
type registryNode struct {
	*jobsource.Scripted
	reveals map[uint8]uint64
	height  uint64
}

func (n *registryNode) Call(ctx context.Context, method string, params interface{}, out interface{}) error {
	if method != "igneum_vectorRegistry" {
		return n.Scripted.Call(ctx, method, params, out)
	}
	type coord struct {
		Index        uint8  `json:"index"`
		RevealBlocks uint64 `json:"revealBlocks"`
		ParamsDigest string `json:"paramsDigest"`
	}
	v := struct {
		Height      uint64  `json:"height"`
		Coordinates []coord `json:"coordinates"`
	}{Height: n.height}
	for i, d := range n.reveals {
		v.Coordinates = append(v.Coordinates, coord{Index: i, RevealBlocks: d})
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}

// TestServedAnchorTakenAsGiven pins the review lead's ruling of 14:16 UK, 18 October 2026 (F-730): the period's anchor
// is taken as the node serves it (programAnchor at anchorHeight), never re-derived from the registry's revealBlocks
// by the chain form. On the public test network every node resolved coordinate 7's anchor at the period's start
// minus 2 from height 13,800 while the registry said 64; the pool re-anchored its jobs at start minus 64 and every
// lane-7 share was refused "invalid mix digest" for three hours. With the registry saying 64 and the node naming
// start minus 2, the job carries the node's anchor; a template naming a different anchor at the same height
// re-keys the job to it; and a template naming start minus 64 is taken as given too.
func TestServedAnchorTakenAsGiven(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	body := `{"chain_id": 7399, "block_seconds": 20, "share_target": "0x0000ffff00000000000000000000000000000000000000000000000000000000",
	 "coordinates": [{"coord": 0, "algorithm": "null", "params": {"v": 0}, "difficulty": "1000", "reward_wei": "1"}], "moves": []}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	script, err := jobsource.NewScripted(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	node := &registryNode{Scripted: script, reveals: map[uint8]uint64{0: 64}, height: 1}
	ctx := context.Background()
	svc := New(Config{WindowN: 2, ShareDifficulty: 2, Maturity: 3, ShareLane: true, Coordinates: []uint8{0}, ProgramRevealBlocks: 2}, script, anchoredNull{period: 64}, ledger.New(ledger.NewMemory()))
	svc.now = func() time.Time { return now }
	svc.Server.TrustClaimed = true
	member := "0x00000000000000000000000000000000000c3a07"
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
	if reply, err := r.ReadBytes('\n'); err != nil || !strings.Contains(string(reply), `"result":true`) {
		t.Fatalf("authorize: %s %v", reply, err)
	}
	svc.readRegistryReveals(ctx, node)
	svc.mu.Lock()
	reg := svc.revealReg
	svc.mu.Unlock()
	if reg == nil || reg[0] != 64 {
		t.Fatalf("the registry's reveals: %v", reg)
	}
	now = now.Add(6100 * 20 * time.Second)
	// the registry says 64, the node names the period's start minus 2 (6078 for 6080): the job carries the node's
	work, err := script.GetWorkCoord(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	work.Height, work.ShareHeight = 6080, 6080
	work.HasAnchor, work.ProgramAnchor, work.AnchorHeight = true, [32]byte{8}, 6078
	if err := svc.pollShareLaneWork(ctx, node, work, 0, true); err != nil {
		t.Fatal(err)
	}
	jobs := svc.Server.MemberJobs(member, "w")
	if len(jobs) != 1 || jobs[0].ProgramAnchor != [32]byte{8} || jobs[0].AnchorHeight != 6078 {
		t.Fatalf("the served anchor was not taken as given: %+v", jobs)
	}
	// the node names another anchor at the same height: the job is re-keyed to the node's word
	work.ProgramAnchor, work.AnchorHeight = [32]byte{9}, 6016
	if err := svc.pollShareLaneWork(ctx, node, work, 0, true); err != nil {
		t.Fatal(err)
	}
	jobs = svc.Server.MemberJobs(member, "w")
	if len(jobs) != 1 || jobs[0].ProgramAnchor != [32]byte{9} || jobs[0].AnchorHeight != 6016 {
		t.Fatalf("a changed served anchor did not re-key the job: %+v", jobs)
	}
	// the next period's template naming start minus 64 (6080 for 6144) is taken as given as well
	now = now.Add(70 * 20 * time.Second)
	work.Height, work.ShareHeight = 6144, 6144
	work.ProgramAnchor, work.AnchorHeight = [32]byte{10}, 6080
	if err := svc.pollShareLaneWork(ctx, node, work, 0, true); err != nil {
		t.Fatal(err)
	}
	jobs = svc.Server.MemberJobs(member, "w")
	if len(jobs) != 1 || jobs[0].ProgramAnchor != [32]byte{10} || jobs[0].AnchorHeight != 6080 {
		t.Fatalf("the start-minus-64 anchor was not taken as given: %+v", jobs)
	}
	svc.mu.Lock()
	cached := svc.anchors[6080]
	svc.mu.Unlock()
	if cached != [32]byte{10} {
		t.Fatalf("the served anchor is not cached at its height: %v", cached)
	}
}

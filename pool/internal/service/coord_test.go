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

// TestShareLanePerCoordinate pins the pool's VectorWork part (the project's order of 00:5x UK, 13 October 2026): with
// -coordinates the share lane polls one template per coordinate, issues one job per (address, worker, coord)
// on one session, each notify carrying its coord, difficulty and reward, and the jobs differ by header.
func TestShareLanePerCoordinate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	body := `{"chain_id": 7399, "block_seconds": 20, "share_target": "0x0000ffff00000000000000000000000000000000000000000000000000000000",
	 "coordinates": [{"coord": 0, "algorithm": "null", "params": {"v": 0}, "difficulty": "1000", "reward_wei": "1"},
	                 {"coord": 1, "algorithm": "null", "params": {"v": 1}, "difficulty": "2000", "reward_wei": "2"}], "moves": []}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	script, err := jobsource.NewScripted(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc := New(Config{WindowN: 2, ShareDifficulty: 2, Maturity: 3, ShareLane: true, Coordinates: []uint8{0, 1}}, script, validate.Null{}, ledger.New(ledger.NewMemory()))
	svc.now = func() time.Time { return now }
	svc.Server.TrustClaimed = true
	member := "0x00000000000000000000000000000000000c3a02"
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
	if err := svc.pollShareLane(ctx); err != nil {
		t.Fatal(err)
	}
	jobs := svc.Server.MemberJobs(member, "w")
	if len(jobs) != 2 || !jobs[0].HasCoord || jobs[0].Coord != 0 || jobs[1].Coord != 1 {
		t.Fatalf("jobs per coordinate: %d %+v", len(jobs), jobs)
	}
	if jobs[0].ID == jobs[1].ID {
		t.Fatalf("the two coordinates' jobs must have different ids")
	}
	// one chain, one share parent: the same header for every coordinate, the ids still differ by coordinate and document
	same := jobs[0]
	same.Coord, same.ParamsDigest = jobs[1].Coord, jobs[1].ParamsDigest
	if same.Identity(7399) == jobs[0].ID || same.Identity(7399) != jobs[1].Identity(7399) && jobs[1].Header == jobs[0].Header {
		t.Fatalf("the id must carry the coordinate and the document")
	}
	if jobs[0].Clean || jobs[1].Clean {
		t.Fatalf("a coordinate job's notify must not be clean (a session keeps its other coordinates' jobs)")
	}
	if jobs[1].Difficulty != "2000" || jobs[1].RewardWei != "2" {
		t.Fatalf("the coordinate's difficulty and reward on the job: %q %q", jobs[1].Difficulty, jobs[1].RewardWei)
	}
	// the notifies on the wire carry coord
	seen := map[float64]bool{}
	for i := 0; i < 2; i++ {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var n struct {
			Method string `json:"method"`
			Params struct {
				Coord     *float64 `json:"coord"`
				ChainID   uint64   `json:"chain_id"`
				RewardWei string   `json:"reward_wei"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &n) != nil || n.Method != "mining.notify" || n.Params.Coord == nil {
			t.Fatalf("notify without coord: %s", line)
		}
		seen[*n.Params.Coord] = true
	}
	if !seen[0] || !seen[1] {
		t.Fatalf("notifies for both coordinates: %v", seen)
	}
	// the keys: the share-lane jobs and the steps are per coordinate
	if _, has := svc.shareJobs[jobKeyCoord(member, "w", 1, true)]; !has {
		t.Fatalf("no share job keyed by coordinate 1")
	}
	if jobKeyCoord(member, "w", 0, true) == jobKeyCoord(member, "w", 1, true) || stepKeyCoord(member, "w", 0, true) == stepKeyCoord(member, "w", 1, true) {
		t.Fatalf("keys must differ by coordinate")
	}
	if jobKeyCoord(member, "w", 0, false) != jobKeyOf(member, "w") {
		t.Fatalf("a one-form key is unchanged")
	}
}


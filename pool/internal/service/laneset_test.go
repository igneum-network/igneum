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

// TestLaneSetFollowsTheChainsPayList pins the project's rule of 20:2x UK, 15 October 2026 (the public test network's
// succession at 520): the lane set follows the chain's registration read every height, never a restart. A pool
// configured for coordinate 0 alone, on a chain whose pay list pays coordinates 0 and 1, serves 1 from the source the
// same poll (the source seals it), its log line naming the sealer to configure; a coordinate the chain pays 0 is
// not served even when configured.
func TestLaneSetFollowsTheChainsPayList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	body := `{"chain_id": 7322, "block_seconds": 600, "share_target": "0x0000ffff00000000000000000000000000000000000000000000000000000000",
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
	svc := New(Config{WindowN: 2, ShareDifficulty: 2, Maturity: 3, ShareLane: true, Coordinates: []uint8{0}}, script, validate.Null{}, ledger.New(ledger.NewMemory()))
	svc.now = func() time.Time { return now }
	svc.Server.TrustClaimed = true
	member := "0x00000000000000000000000000000000000f5200"
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
	// the chain pays 0 and 1 at the tip: the pool, configured for 0 alone, serves both the same poll
	svc.mu.Lock()
	svc.payShares, svc.payReadHeight, svc.payNames = []uint64{1, 1}, 1, []string{"bandwidth", "anvil"}
	svc.mu.Unlock()
	if got := svc.laneSet(); len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("the lane set with a paid unnamed coordinate: %v", got)
	}
	if err := svc.pollShareLane(ctx); err != nil {
		t.Fatal(err)
	}
	jobs := svc.Server.MemberJobs(member, "w")
	if len(jobs) != 2 || jobs[0].Coord != 0 || jobs[1].Coord != 1 {
		t.Fatalf("jobs on the chain's paid coordinates: %d %+v", len(jobs), jobs)
	}
	// the registration retires 0 at this height: the next poll offers 1 alone, no restart
	svc.mu.Lock()
	svc.payShares, svc.payReadHeight = []uint64{0, 1}, 2
	svc.payUnpaidSaid, svc.payUnnamedSaid = nil, nil
	svc.mu.Unlock()
	if got := svc.laneSet(); len(got) != 2 {
		t.Fatalf("the lane set lists the configured and the paid: %v", got)
	}
	if paid, known := svc.coordPaid(0); !known || paid {
		t.Fatalf("coordinate 0 retired must read unpaid: paid %v known %v", paid, known)
	}
	if paid, known := svc.coordPaid(1); !known || !paid {
		t.Fatalf("coordinate 1 must read paid: paid %v known %v", paid, known)
	}
	client.Close()
}

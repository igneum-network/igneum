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

// TestShareJobReissuedOnAnchorChange pins the anchor re-key (the protocol team's read of 19:16 UK, 13 October 2026:
// the old public test network (chain 7320) heights 3368 to 3385 keyed on the losing twin of the period's anchor block 3358 after a one-block race
// there, 146 shares refused "invalid mix digest" until the next period): a template whose program anchor changes
// at the same height and parent re-issues the member's job under the new anchor, as a parent change does.
func TestShareJobReissuedOnAnchorChange(t *testing.T) {
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
	ctx := context.Background()
	svc := New(Config{WindowN: 2, ShareDifficulty: 2, Maturity: 3, ShareLane: true, Coordinates: []uint8{0}}, script, validate.Null{}, ledger.New(ledger.NewMemory()))
	svc.now = func() time.Time { return now }
	svc.Server.TrustClaimed = true
	member := "0x00000000000000000000000000000000000c3a03"
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
	work, err := script.GetWorkCoord(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	work.HasAnchor, work.ProgramAnchor, work.AnchorHeight = true, [32]byte{1}, 10
	if err := svc.pollShareLaneWork(ctx, script, work, 0, true); err != nil {
		t.Fatal(err)
	}
	first := svc.Server.MemberJobs(member, "w")
	if len(first) != 1 {
		t.Fatalf("jobs: %d", len(first))
	}
	// the same height and parent, the same anchor: no re-issue
	if err := svc.pollShareLaneWork(ctx, script, work, 0, true); err != nil {
		t.Fatal(err)
	}
	if again := svc.Server.MemberJobs(member, "w"); again[0].ID != first[0].ID {
		t.Fatalf("an unchanged template re-issued the job")
	}
	// the anchor alone moves (a reorg at the period's anchor height): a new job under the new anchor
	work.ProgramAnchor = [32]byte{2}
	if err := svc.pollShareLaneWork(ctx, script, work, 0, true); err != nil {
		t.Fatal(err)
	}
	moved := svc.Server.MemberJobs(member, "w")
	if len(moved) != 1 || moved[0].ID == first[0].ID || moved[0].ProgramAnchor != [32]byte{2} {
		t.Fatalf("the anchor change must re-issue the job under the new anchor: %+v", moved)
	}
}

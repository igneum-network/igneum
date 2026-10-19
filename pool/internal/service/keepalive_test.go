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

// TestShareLaneKeepaliveOnAStandingTemplate pins F-164's cure (15 October 2026): while a coordinate's template stands
// the pool sends a session its standing job again after KeepaliveSilence, the same job (its id unchanged), never
// before; a new template issues a new job and starts the silence again.
func TestShareLaneKeepaliveOnAStandingTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	body := `{"chain_id": 7399, "block_seconds": 600, "share_target": "0x0000ffff00000000000000000000000000000000000000000000000000000000",
	 "coordinates": [{"coord": 18, "algorithm": "null", "params": {"v": 18}, "difficulty": "1000", "reward_wei": "1"}], "moves": []}`
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
	svc := New(Config{WindowN: 2, ShareDifficulty: 2, Maturity: 3, ShareLane: true, Coordinates: []uint8{18}}, script, validate.Null{}, ledger.New(ledger.NewMemory()))
	svc.now = func() time.Time { return now }
	svc.Server.TrustClaimed = true
	member := "0x00000000000000000000000000000000000f1640"
	server, client := net.Pipe()
	go svc.Server.ServeConn(ctx, server)
	w := bufio.NewWriter(client)
	r := bufio.NewReader(client)
	send := func(id int, method string, params interface{}) {
		b, _ := json.Marshal(map[string]interface{}{"id": id, "method": method, "params": params})
		w.Write(append(b, '\n'))
		w.Flush()
	}
	// notifyIDs reads every notify waiting on the wire within a short deadline and returns their job ids
	notifyIDs := func() []string {
		var ids []string
		for {
			client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			line, err := r.ReadBytes('\n')
			if err != nil {
				client.SetReadDeadline(time.Time{})
				return ids
			}
			var m struct {
				Method string `json:"method"`
				Params struct {
					JobID string `json:"job_id"`
				} `json:"params"`
			}
			if json.Unmarshal(line, &m) == nil && m.Method == "mining.notify" {
				ids = append(ids, m.Params.JobID)
			}
		}
	}
	send(1, "mining.subscribe", []string{"test/0"})
	if _, err := r.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	send(2, "mining.authorize", []string{member, "pool-l18"})
	reply, err := r.ReadBytes('\n')
	if err != nil || !strings.Contains(string(reply), `"result":true`) {
		t.Fatalf("authorize: %s %v", reply, err)
	}
	if err := svc.pollShareLane(ctx); err != nil {
		t.Fatal(err)
	}
	first := notifyIDs()
	if len(first) != 1 || first[0] == "" {
		t.Fatalf("the first poll issues one job: %v", first)
	}
	// ten seconds on, the template standing: silence
	now = start.Add(10 * time.Second)
	if err := svc.pollShareLane(ctx); err != nil {
		t.Fatal(err)
	}
	if got := notifyIDs(); len(got) != 0 {
		t.Fatalf("a job sent again inside the silence: %v", got)
	}
	// thirty-one seconds on: the same job again, once
	now = start.Add(31 * time.Second)
	if err := svc.pollShareLane(ctx); err != nil {
		t.Fatal(err)
	}
	if got := notifyIDs(); len(got) != 1 || got[0] != first[0] {
		t.Fatalf("the keepalive sends the standing job itself once: %v, the job %s", got, first[0])
	}
	if svc.Stats.Keepalives != 1 {
		t.Fatalf("Keepalives %d, want 1", svc.Stats.Keepalives)
	}
	// forty seconds on: nothing (the silence runs from the keepalive)
	now = start.Add(40 * time.Second)
	if err := svc.pollShareLane(ctx); err != nil {
		t.Fatal(err)
	}
	if got := notifyIDs(); len(got) != 0 {
		t.Fatalf("a second keepalive inside the silence: %v", got)
	}
	// sixty-two seconds on: the second keepalive
	now = start.Add(62 * time.Second)
	if err := svc.pollShareLane(ctx); err != nil {
		t.Fatal(err)
	}
	if got := notifyIDs(); len(got) != 1 || got[0] != first[0] {
		t.Fatalf("the second keepalive: %v", got)
	}
	// a new height: a new job, and the silence starts again from it
	now = start.Add(600 * time.Second)
	if err := svc.pollShareLane(ctx); err != nil {
		t.Fatal(err)
	}
	second := notifyIDs()
	if len(second) != 1 || second[0] == first[0] {
		t.Fatalf("a new template issues a new job: %v after %s", second, first[0])
	}
	now = start.Add(620 * time.Second)
	if err := svc.pollShareLane(ctx); err != nil {
		t.Fatal(err)
	}
	if got := notifyIDs(); len(got) != 0 {
		t.Fatalf("a keepalive inside the new job's silence: %v", got)
	}
	if svc.Stats.Keepalives != 2 {
		t.Fatalf("Keepalives %d, want 2", svc.Stats.Keepalives)
	}
	client.Close()
}

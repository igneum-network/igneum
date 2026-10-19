//go:build igneumhash

package service

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"igneum.network/pool/internal/ledger"
	"igneum.network/pool/internal/validate"
)

// The share lane's relay fan-out (the finding of the share-rate record,
// 11 October 2026: a share lives only in the pool of the node it was
// submitted to): a member's share is forwarded to the pool's node and to
// every other sealer named, the answers counted per node, the share
// accepted when any sealer accepts it and refused only when every one
// refuses.
func TestShareLaneFanOut(t *testing.T) {
	ctx := context.Background()
	reader := newFakeNode(true)
	reader.shareLane = true
	b7 := newFakeNode(true)
	b7.shareLane, b7.refuseShares = true, true
	old := newMemberNode
	newMemberNode = func(url string) Node { return b7 }
	defer func() { newMemberNode = old }()
	svc := New(Config{WindowN: 2, ShareDifficulty: 2, Maturity: 3, ShareLane: true, ShareNodes: []string{"http://b7/"}}, reader, validate.Null{}, ledger.New(ledger.NewMemory()))
	svc.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	if len(svc.shareNodes) != 2 {
		t.Fatalf("fan-out targets %d", len(svc.shareNodes))
	}
	// one member authorised over a pipe (the plain form: a 40-hex address and a worker name)
	member := "0x00000000000000000000000000000000000c3a01"
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
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), `"result":true`) {
		t.Fatalf("authorize refused: %s", reply)
	}
	// the poll builds the member's share job on its own share header
	if err := svc.pollWork(ctx); err != nil {
		t.Fatal(err)
	}
	job := svc.Server.MemberJob(member, "")
	if job == nil || job.Member != member {
		t.Fatalf("no member job after the poll: %+v", job)
	}
	if job.ShareTarget != reader.boundary {
		t.Fatalf("the job's boundary is not the share target: %x", job.ShareTarget[:4])
	}
	// the member's solution at the share target: forwarded to both sealers; b7 refuses, the reader accepts
	if err := svc.Block(ctx, *job, 7, [32]byte{}, [32]byte{}); err != nil {
		t.Fatalf("the share was refused though one sealer accepted it: %v", err)
	}
	if reader.shareSubmits != 1 || b7.shareSubmits != 1 {
		t.Fatalf("submissions reader %d b7 %d", reader.shareSubmits, b7.shareSubmits)
	}
	st := svc.ShareNodes()
	if st[0].Accepted != 1 || st[1].Refused != 1 || st[1].LastError == "" {
		t.Fatalf("per-node counts %+v", st)
	}
	if svc.Stats.SharesAccepted != 1 || svc.Stats.SharesRefused != 0 || svc.Stats.Blocks != 0 {
		t.Fatalf("stats %+v (a share is never a block)", svc.Stats)
	}
	// every sealer refusing: the share counts refused once
	reader.refuseShares = true
	if err := svc.Block(ctx, *job, 8, [32]byte{}, [32]byte{}); err == nil {
		t.Fatal("expected the refusal when every sealer refuses")
	}
	if svc.Stats.SharesRefused != 1 || svc.Stats.SharesAccepted != 1 {
		t.Fatalf("stats after the refusal %+v", svc.Stats)
	}
	client.Close()
}

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"igneum.network/pool/internal/validate"
)

// The member-node form (GUARD-C-CLOSURE 3.1): each member's own node's template reaches only that member; a member's
// solution goes to the member's node; a member node whose template does not name the pool's list gets no job and the
// list re-set; the withhold fixture drops a member's solution and counts it.
func TestMemberNodes(t *testing.T) {
	ctx := context.Background()
	list := json.RawMessage(`[{"address": "0xAAAA", "weight": "0x3"}, {"address": "0xBBBB", "weight": "0x2"}]`)
	na, nb := newFakeNode(true), newFakeNode(true)
	na.header[1], nb.header[1] = 0xa1, 0xb2
	na.payouts, nb.payouts = list, list
	fakes := map[string]Node{"http://a/": na, "http://b/": nb}
	old := newMemberNode
	newMemberNode = func(url string) Node { return fakes[url] }
	defer func() { newMemberNode = old }()
	reader := newFakeNode(true)
	svc := New(Config{WindowN: 2, ShareDifficulty: 2, Maturity: 3, PayoutList: list,
		MemberNodes: map[string]string{"0xAAAA": "http://a/", "0xbbbb": "http://b/"}, Withhold: "0xbbbb"}, reader, validate.Null{}, nil)
	now := time.Unix(1_700_000_000, 0)
	svc.now = func() time.Time { return now }
	if err := svc.SetPayoutListMembers(ctx); err != nil {
		t.Fatal(err)
	}
	if na.listSets != 1 || nb.listSets != 1 {
		t.Fatalf("the list is set on every member node at start: %d/%d", na.listSets, nb.listSets)
	}
	if err := svc.pollWork(ctx); err != nil {
		t.Fatal(err)
	}
	ja, jb := svc.Server.MemberJob("0xaaaa", ""), svc.Server.MemberJob("0xBBBB", "")
	if ja == nil || jb == nil {
		t.Fatal("each member has its own node's job after a poll")
	}
	if ja.Header != na.header || jb.Header != nb.header || ja.Member != "0xaaaa" || jb.Member != "0xbbbb" {
		t.Fatalf("a member's job is its own node's template: %x/%x %s/%s", ja.Header[:2], jb.Header[:2], ja.Member, jb.Member)
	}
	if !samePayoutList(ja.Payouts, list) {
		t.Fatal("the job carries the template's list for the member's check")
	}
	if svc.current != nil {
		t.Fatal("the pool holds no template of its own on the member-node form")
	}
	// a's solution goes to a's node only
	if err := svc.Block(ctx, *ja, 7, [32]byte{}, [32]byte{}); err != nil {
		t.Fatal(err)
	}
	if na.submits != 1 || nb.submits != 0 || reader.submits != 0 {
		t.Fatalf("the solution went to the member's own node: a %d b %d reader %d", na.submits, nb.submits, reader.submits)
	}
	// the fixture: b's solution is withheld, counted, submitted nowhere
	if err := svc.Block(ctx, *jb, 9, [32]byte{}, [32]byte{}); err != nil {
		t.Fatal(err)
	}
	if nb.submits != 0 || svc.Stats.Withheld != 1 {
		t.Fatalf("withheld: b submits %d, withheld %d", nb.submits, svc.Stats.Withheld)
	}
	// b's node loses the list (a restart): no job for b, the list re-set once per interval, a unaffected
	nb.payouts = nil
	nb.header[2] = 0x01
	na.header[2] = 0x02
	now = now.Add(ListResetInterval)
	err := svc.pollWork(ctx)
	if err == nil || !strings.Contains(err.Error(), "0xbbbb") || !strings.Contains(err.Error(), "no job issued") {
		t.Fatalf("expected b's refusal, got %v", err)
	}
	if nb.listSets != 2 || !samePayoutList(nb.listSet, list) {
		t.Fatalf("b's list re-set: %d %s", nb.listSets, nb.listSet)
	}
	if j := svc.Server.MemberJob("0xaaaa", ""); j == nil || j.Header != na.header {
		t.Fatal("a's new template still issues while b's node is refused")
	}
	if j := svc.Server.MemberJob("0xbbbb", ""); j == nil || j.Header == nb.header {
		t.Fatal("b keeps its last good job; the listless template never issues")
	}
	ms := svc.Members()
	if len(ms) != 2 {
		t.Fatalf("members in the health line: %d", len(ms))
	}
}

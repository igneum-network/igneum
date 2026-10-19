package stratum

import (
	"encoding/json"
	"testing"
	"time"

	"igneum.network/pool/internal/validate"
)

// TestSubscribeWaitsForTheFirstDocument pins 0.1.62's one order (15 October 2026): a subscribe arriving before the
// server holds a parameter document waits for the first one (the lane's, set within the bound) and carries it; one
// arriving after the bound with no document is answered without one, as before, rather than held for ever.
func TestSubscribeWaitsForTheFirstDocument(t *testing.T) {
	old := SubscribeWait
	SubscribeWait = 2 * time.Second
	defer func() { SubscribeWait = old }()
	srv, _, c := setup(t)
	doc := json.RawMessage(`{"version":15,"v":"test"}`)
	go func() {
		time.Sleep(200 * time.Millisecond)
		srv.SetLaneParams(doc, "0xd0c")
	}()
	t0 := time.Now()
	res, e := c.call(MethodSubscribe, []string{"test/0"})
	if e != nil {
		t.Fatalf("subscribe: %+v", e)
	}
	var sub SubscribeResult
	if err := json.Unmarshal(res, &sub); err != nil {
		t.Fatal(err)
	}
	if string(sub.Params) != string(doc) || sub.ParamsDigest != "0xd0c" {
		t.Fatalf("the subscribe did not wait for the first document: params %s digest %q", sub.Params, sub.ParamsDigest)
	}
	if time.Since(t0) >= SubscribeWait {
		t.Fatalf("the subscribe waited the whole bound (%s) though the document came at 200 ms", time.Since(t0))
	}
	// a second subscribe answers at once with the held document
	t0 = time.Now()
	res, e = c.call(MethodSubscribe, []string{"test/0"})
	if e != nil {
		t.Fatalf("second subscribe: %+v", e)
	}
	if err := json.Unmarshal(res, &sub); err != nil || string(sub.Params) != string(doc) || time.Since(t0) > time.Second {
		t.Fatalf("the second subscribe must answer at once with the document: %s (%s)", sub.Params, time.Since(t0))
	}
}

// TestSubscribeAnswersWithoutADocumentPastTheBound pins the bound: with no document ever set, the subscribe is
// answered after SubscribeWait with no params, never held.
func TestSubscribeAnswersWithoutADocumentPastTheBound(t *testing.T) {
	old := SubscribeWait
	SubscribeWait = 300 * time.Millisecond
	defer func() { SubscribeWait = old }()
	_, _, c := setup(t)
	t0 := time.Now()
	res, e := c.call(MethodSubscribe, []string{"test/0"})
	if e != nil {
		t.Fatalf("subscribe: %+v", e)
	}
	var sub SubscribeResult
	if err := json.Unmarshal(res, &sub); err != nil {
		t.Fatal(err)
	}
	if len(sub.Params) != 0 {
		t.Fatalf("a document from nowhere: %s", sub.Params)
	}
	if d := time.Since(t0); d < SubscribeWait || d > SubscribeWait+2*time.Second {
		t.Fatalf("the wait bound was not kept: %s", d)
	}
	_ = validate.Null{}
}

package stratum

import (
	"encoding/json"
	"testing"

	"igneum.network/pool/internal/validate"
)

// TestSubscribeDocumentIsTheLanesNeverAnEmptyOne pins 0.1.65 (16 October 2026): the subscribe's parameter document is
// the share lane's last (one answer for every subscribe), a member job's only when the lane holds none, and never
// an empty one while any held job or the lane carries a document; the count of document-less answers stays zero.
func TestSubscribeDocumentIsTheLanesNeverAnEmptyOne(t *testing.T) {
	srv, _, c := setup(t)
	// retained member jobs: one without a document (a job of a form that carries none), one with
	bare := Job{ID: "00000000000000e1", Height: 10, Algorithm: validate.NullAlgorithm, Member: "0x00000000000000000000000000000000000000e1", Coord: 2, HasCoord: true,
		ShareTarget: validate.TargetForDifficulty(16), Boundary: validate.TargetForDifficulty(1 << 20)}
	withDoc := bare
	withDoc.ID, withDoc.Member, withDoc.Coord = "00000000000000e2", "0x00000000000000000000000000000000000000e2", 3
	withDoc.Params, withDoc.ParamsDigest = json.RawMessage(`{"version":18,"v":"job"}`), "0xjob"
	srv.SetJob(bare)
	srv.SetJob(withDoc)
	for i := 0; i < 20; i++ {
		res, e := c.call(MethodSubscribe, []string{"test/0"})
		if e != nil {
			t.Fatalf("subscribe: %+v", e)
		}
		var sub SubscribeResult
		if err := json.Unmarshal(res, &sub); err != nil {
			t.Fatal(err)
		}
		if string(sub.Params) != `{"version":18,"v":"job"}` || sub.ParamsDigest != "0xjob" {
			t.Fatalf("subscribe %d: not the held job's document: %s %q", i, sub.Params, sub.ParamsDigest)
		}
	}
	// the lane's document comes: every subscribe carries it from then on, whatever the jobs hold
	srv.SetLaneParams(json.RawMessage(`{"version":21,"v":"lane"}`), "0xlane")
	for i := 0; i < 20; i++ {
		res, e := c.call(MethodSubscribe, []string{"test/0"})
		if e != nil {
			t.Fatalf("subscribe: %+v", e)
		}
		var sub SubscribeResult
		if err := json.Unmarshal(res, &sub); err != nil {
			t.Fatal(err)
		}
		if string(sub.Params) != `{"version":21,"v":"lane"}` || sub.ParamsDigest != "0xlane" {
			t.Fatalf("subscribe %d: not the lane's document: %s %q", i, sub.Params, sub.ParamsDigest)
		}
	}
	if n := srv.SubscribesWithoutDocument(); n != 0 {
		t.Fatalf("%d subscribe(s) answered without a document while one was held", n)
	}
}

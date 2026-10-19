package igneumhash

import "testing"

// The version gate of the reference-only path: version 8 and every version
// above it (9 and 10 the per-period programme, 11 the memory read) are the
// C reference's alone and route to it; versions the Go port knows (0, 1, 4,
// 6) do not; a document without a version routes nowhere (its refusal is
// the parser's).
func TestReferenceOnlyGate(t *testing.T) {
	for _, c := range []struct {
		doc     string
		ref     bool
		chainID uint64
	}{
		{`{"version": 8, "chain_id": 7319}`, true, 7319},
		{`{"version": 9, "chain_id": 7310}`, true, 7310},
		{`{"version": 10, "chain_id": 7341, "program_ops": 512, "period_blocks": 30}`, true, 7341},
		{`{"version": 11}`, true, 7310},
		{`{"version": 6, "item_bytes": 128}`, false, 0},
		{`{"version": 4, "item_bytes": 256}`, false, 0},
		{`{"version": 1}`, false, 0},
		{`{"version": 0}`, false, 0},
		{`{}`, false, 0},
		{``, false, 0},
		{`null`, false, 0},
	} {
		ref, id := referenceOnly([]byte(c.doc))
		if ref != c.ref || id != c.chainID {
			t.Fatalf("%s: reference %v chain %d, want %v %d", c.doc, ref, id, c.ref, c.chainID)
		}
	}
}

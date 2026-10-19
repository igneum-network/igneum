package service

import "testing"

// F-418's pool half (0.1.82): a block is counted on the chain's hash alone.
func TestBlockVerdictReadsTheChainAlone(t *testing.T) {
	ours := &rpcBlock{Hash: "0xAbC1", Nonce: "0x1758b00110000019", BondKey: "0x6f1e6127f4c6"}
	theirs := &rpcBlock{Hash: "0x3f91", Nonce: "0x000000000c055e06", BondKey: "0x277bcc3591f2"}
	const nonce = 0x1758b00110000019
	// the height still empty: pending
	if v := blockVerdictOf(3872, nil, 3873, "", nonce); v != verdictPending {
		t.Fatalf("an empty height: %v", v)
	}
	// the answer's hash on the chain (the node from 0.10.52): canonical
	if v := blockVerdictOf(3873, ours, 3873, "0xabc1", nonce); v != verdictCanonical {
		t.Fatalf("the answer's hash on the chain: %v", v)
	}
	// no hash in the answer: the nonce decides; and an answer hash that is pre-proof (a keyed seal) with our nonce on the chain
	if v := blockVerdictOf(3873, ours, 3873, "", nonce); v != verdictCanonical {
		t.Fatalf("our nonce on the chain: %v", v)
	}
	if v := blockVerdictOf(3873, ours, 3873, "0xdead", nonce); v != verdictCanonical {
		t.Fatalf("a pre-proof answer hash with our nonce on the chain: %v", v)
	}
	// another's block at the height, the head not yet two past: pending (a sibling may still win)
	if v := blockVerdictOf(3874, theirs, 3873, "", nonce); v != verdictPending {
		t.Fatalf("another's block with the head one past: %v", v)
	}
	// another's block with the head two past: lost
	if v := blockVerdictOf(3875, theirs, 3873, "", nonce); v != verdictLost {
		t.Fatalf("another's block with the head two past: %v", v)
	}
	if v := blockVerdictOf(3875, theirs, 3873, "0xabc1", nonce); v != verdictLost {
		t.Fatalf("another's block against the answer's hash: %v", v)
	}
	if shortKey("0x277bcc3591f2e0fe") != "0x277bcc35.." || shortKey("") != "(none)" {
		t.Fatalf("shortKey")
	}
}

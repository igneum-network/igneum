package service

import (
	"context"
	"testing"

	"igneum.network/pool/internal/stratum"
)

// TestBlockWorkerAndJobKind pins the block session (vectorwork6, 13 October 2026: coordinate 1's sealer seals
// nothing itself, so a session whose worker name ends ".block" mines the template's header at the block target
// and the pool submits its solution as the block): the worker rule, and a block job's id differing from the share
// job's on the same fields.
func TestBlockWorkerAndJobKind(t *testing.T) {
	if !stratum.IsBlockWorker("pod-4090.block") || !stratum.IsBlockWorker("r0a-5090-block") || !stratum.IsBlockWorker("pod_block") || stratum.IsBlockWorker("pod-4090") || stratum.IsBlockWorker("") {
		t.Fatalf("the block worker rule is the .block, -block or _block suffix")
	}
	j := stratum.Job{Height: 7, Algorithm: "igneumhash", Coord: 1, HasCoord: true, ParamsDigest: "0xd"}
	b := j
	b.Kind = "block"
	if j.Identity(7356) == b.Identity(7356) {
		t.Fatalf("a block job's id must differ from the share job's")
	}
}

// TestBlockSolutionStaleNotSubmitted pins the stale guard (pool-b2 on vectorwork6, 13 October 2026: 346 "work
// submitted is too old" answers beside 56 blocks): a block session's solution for a height the coordinate's
// template has left is refused by the pool without a call, counted in Stats.BlocksStale.
func TestBlockSolutionStaleNotSubmitted(t *testing.T) {
	svc := &Service{coordState: map[uint8]coordState{1: {height: 300}}}
	job := stratum.Job{Kind: "block", Coord: 1, HasCoord: true, Height: 291, Member: "0xm", Worker: "w-block"}
	if err := svc.Block(context.Background(), job, 1, [32]byte{}, [32]byte{}); err == nil {
		t.Fatal("a solution at height 291 against a template at 300 was submitted")
	}
	if svc.Stats.BlocksStale != 1 {
		t.Fatalf("BlocksStale %d, want 1", svc.Stats.BlocksStale)
	}
}

// TestBlockSolutionDuplicateHeightNotSubmitted pins one block a height a coordinate: after this pool sealed a
// coordinate at a height, a second solution for that height is not sent, counted in Stats.BlocksDuplicate.
func TestBlockSolutionDuplicateHeightNotSubmitted(t *testing.T) {
	svc := &Service{coordState: map[uint8]coordState{1: {height: 300}}, sealedAt: map[uint8]uint64{1: 300}}
	job := stratum.Job{Kind: "block", Coord: 1, HasCoord: true, Height: 300, Member: "0xm", Worker: "w-block"}
	if err := svc.Block(context.Background(), job, 2, [32]byte{}, [32]byte{}); err == nil {
		t.Fatal("a second solution at a sealed height was submitted")
	}
	if svc.Stats.BlocksDuplicate != 1 {
		t.Fatalf("BlocksDuplicate %d, want 1", svc.Stats.BlocksDuplicate)
	}
}

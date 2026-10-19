package igneumhash

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// A verification allocates nothing after warm-up (the project's order of 20:4x UK,
// 12 October 2026, on the minimum node's 15 allocations and 9.5 KB an op in
// the cached form): the pooled item deriver per epoch entry, the pooled Mix
// scratch and the pooled target. Read on the version 1 cached form and on the
// version 0 launch form, a valid proof and an invalid one.
func TestVerifyAllocatesNothingAfterWarmUp(t *testing.T) {
	for _, p := range []Params{
		{Version: Version1, DatasetBytes: 1 << 30, ScratchBytes: 0, Rounds: 32, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 32 << 20, Parents: 256},
		{Version: Version1, DatasetBytes: 1 << 30, ScratchBytes: 4 << 10, Rounds: 64, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 32 << 20, Parents: 512},
		{Version: Version0, DatasetBytes: 1 << 30, ScratchBytes: 16 << 10, Rounds: 32, Lanes: 8, EpochLength: 4096, ItemChain: 4},
	} {
		a := New(7310, p, specWork{})
		h := common.HexToHash("0x11")
		mix, _ := a.Compute(h, 10, 5)
		for i := 0; i < 3; i++ { // warm the pools
			if err := a.Verify(h, 10, 5, mix, big.NewInt(1), false); err != nil {
				t.Fatalf("%s: %v", label(p), err)
			}
		}
		valid := testing.AllocsPerRun(50, func() {
			if err := a.Verify(h, 10, 5, mix, big.NewInt(1), false); err != nil {
				t.Fatal(err)
			}
		})
		invalid := testing.AllocsPerRun(50, func() {
			if err := a.Verify(h, 10, 6, mix, big.NewInt(1), false); err == nil {
				t.Fatal("an invalid proof verified")
			}
		})
		// big.NewInt(1) inside the closure is the difficulty's own allocation,
		// the caller's; the verifier's share of the run is the rest.
		if valid > 1 || invalid > 1 {
			t.Fatalf("%s: allocations per verification valid %.1f invalid %.1f (at most the caller's difficulty)", label(p), valid, invalid)
		}
	}
}

// The pooled scratch is shared state: two verifiers at once must read only
// what their own call wrote (the hash team's and security's condition on the
// pooling). Eight proofs computed single-threaded are verified from two
// goroutines interleaved, every verification agreeing with the single run.
func TestVerifyConcurrentMatchesSingle(t *testing.T) {
	p := Params{Version: Version1, DatasetBytes: 1 << 30, ScratchBytes: 4 << 10, Rounds: 32, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 32 << 20, Parents: 256}
	a := New(7310, p, specWork{})
	type proof struct {
		h     common.Hash
		nonce uint64
		mix   common.Hash
	}
	var proofs []proof
	for i := 0; i < 8; i++ {
		h := common.BigToHash(big.NewInt(int64(1000 + i)))
		mix, _ := a.Compute(h, 10, uint64(i))
		proofs = append(proofs, proof{h, uint64(i), mix})
	}
	errs := make(chan error, 2)
	for g := 0; g < 2; g++ {
		go func(g int) {
			for round := 0; round < 50; round++ {
				for i, pr := range proofs {
					if (i+g)%2 == 0 {
						continue
					}
					if err := a.Verify(pr.h, 10, pr.nonce, pr.mix, big.NewInt(1), false); err != nil {
						errs <- err
						return
					}
					if err := a.Verify(pr.h, 10, pr.nonce+100, pr.mix, big.NewInt(1), false); err == nil {
						errs <- errorsNew("a wrong nonce verified under concurrency")
						return
					}
				}
			}
			errs <- nil
		}(g)
	}
	for g := 0; g < 2; g++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func errorsNew(s string) error { return errString(s) }

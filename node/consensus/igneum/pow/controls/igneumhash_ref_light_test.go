//go:build controls && cgo

package controls

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// F-224 (16 October 2026): the public test network's third sealer refused
// 27 of 133 coordinate-3 shares as an invalid mix digest at heights 874 to
// 899 while the GPU team's reference read every refused row valid under the
// period's anchor, 26 of 27 on the passing shares' parent, the answers in
// 0 to 1 s and the same inputs passing before and after. A wrong NON-ZERO
// mix for the same inputs is the reference's own answer, so the cells
// below pin the two ways the verifier's context can differ from the
// reference recompute on the same (header, height, nonce, anchor):
// (1) the LIGHT context (the cache, the dataset's items derived on demand:
// the form a node serves while the head epoch's full build is in flight,
// or for a late block of the last epoch under the single form) against
// the FULL one, and (2) concurrent callers over one shared store (the
// sealing coordinate's dataset ridden by the other lanes' per-call wrappers
// while shares verify on the donor), each against a standalone answer.

// rasterKeyedDoc is version 18's keyed target t256 over the 256 MiB
// dataset (the target's side 8192; the oracle test's document).
const rasterKeyedDoc = `{"version":18,"rounds":256,"epoch_blocks":4096,"period_blocks":300,"chain_id":7358,"dataset_mib":256,"cache_mib":16,"parents":256,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000"}`

// rasterKeyedDocB is a second document over the SAME base and geometry
// (the draw-1 variant of the oracle test): another lane riding the donor's
// dataset through the shared context (a document of another tile
// geometry is refused by the reference as a rider and holds its own
// context, which the first run of this cell read as two builds).
const rasterKeyedDocB = `{"version":18,"rounds":256,"epoch_blocks":4096,"period_blocks":300,"chain_id":7358,"dataset_mib":256,"cache_mib":16,"parents":256,"draw":1,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000"}`

type hashKey struct {
	height, nonce uint64
}

type hashOut struct{ mix, result common.Hash }

func rasterKeys(n int) []hashKey {
	keys := make([]hashKey, 0, n)
	for i := 0; i < n; i++ {
		keys = append(keys, hashKey{height: 8400 + uint64(i*2)%900, nonce: uint64(i)*0x9E3779B97F4A7C15 + 7})
	}
	return keys
}

// TestRasterLightMatchesFull: the light context's keyed-target hash equals
// the full context's for every (height, nonce) pair under the anchored
// view, so a node serving light while its dataset builds answers as the
// recompute does.
func TestRasterLightMatchesFull(t *testing.T) {
	build := func(full bool) *ihRef {
		alg, err := newIgneumhashRef(7358, []byte(rasterKeyedDoc))
		if err != nil {
			t.Fatal(err)
		}
		a := alg.(*ihRef)
		a.SetFullDataset(full)
		a.SetSingleDataset(true)
		return a
	}
	fullA, light := build(true), build(false)
	defer fullA.Close()
	defer light.Close()
	anchor := common.HexToHash("0x5e8")
	header := common.HexToHash("0x11")
	keys := rasterKeys(300)
	bad := 0
	for _, k := range keys {
		mf, rf := fullA.Under(anchor).Compute(header, k.height, k.nonce)
		ml, rl := light.Under(anchor).Compute(header, k.height, k.nonce)
		if rf == (common.Hash{}) || rl == (common.Hash{}) {
			t.Fatalf("height %d nonce %d: computed nothing (full %x, light %x)", k.height, k.nonce, rf[:4], rl[:4])
		}
		if mf != ml || rf != rl {
			bad++
			if bad <= 3 {
				t.Errorf("height %d nonce %d: the full context %x / %x, the light %x / %x", k.height, k.nonce, mf[:4], rf[:4], ml[:4], rl[:4])
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d pairs differ between the light and the full context (F-224's candidate)", bad, len(keys))
	}
	if b := fullA.builds.Load(); b != 1 {
		t.Fatalf("the full adapter built %d contexts, want one", b)
	}
	t.Logf("%d pairs: the light and the full context the same bytes", len(keys))
}

// TestSharesVerifiedUnderConcurrentImportsOfOtherLanes (the review lead's cell on
// F-224): under the single resident form the donor (the sealing lane's
// adapter, full) answers its own lane's hashes while two other lanes over
// the same base ride its dataset through per-call shared contexts, all
// concurrently and for seconds; every answer equals the standalone
// adapter's for the same inputs, zero refusals and zero wrong mixes.
func TestSharesVerifiedUnderConcurrentImportsOfOtherLanes(t *testing.T) {
	build := func(doc string) *ihRef {
		alg, err := newIgneumhashRef(7358, []byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		a := alg.(*ihRef)
		a.SetFullDataset(true)
		a.SetSingleDataset(true)
		return a
	}
	donor, riderA, riderB := build(rasterKeyedDoc), build(rasterKeyedDocB), build(rasterKeyedDocB)
	aloneDonor, aloneB := build(rasterKeyedDoc), build(rasterKeyedDocB)
	for _, a := range []*ihRef{donor, riderA, riderB, aloneDonor, aloneB} {
		defer a.Close()
	}
	riderA.SetDonor(donor)
	riderB.SetDonor(donor)
	anchor := common.HexToHash("0x5e8")
	header := common.HexToHash("0x11")
	keys := rasterKeys(64)
	// The standalone answers first (one dataset each, no sharing).
	wantDonor := make([]hashOut, len(keys))
	wantB := make([]hashOut, len(keys))
	for i, k := range keys {
		m, r := aloneDonor.Under(anchor).Compute(header, k.height, k.nonce)
		wantDonor[i] = hashOut{m, r}
		m, r = aloneB.Under(anchor).Compute(header, k.height, k.nonce)
		wantB[i] = hashOut{m, r}
	}
	// The donor's dataset built once by the first caller; then the race:
	// eight share verifiers on the donor, four block verifiers on each
	// rider, every call checked against the standalone answer.
	donor.Anchor(keys[0].height)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wrong, calls := 0, 0
	run := func(a *ihRef, want []hashOut, rounds int) {
		defer wg.Done()
		for r := 0; r < rounds; r++ {
			for i, k := range keys {
				m, res := a.Under(anchor).Compute(header, k.height, k.nonce)
				mu.Lock()
				calls++
				if m != want[i].mix || res != want[i].result {
					wrong++
					if wrong <= 3 {
						t.Errorf("%p height %d nonce %d: %x / %x, want %x / %x", a, k.height, k.nonce, m[:4], res[:4], want[i].mix[:4], want[i].result[:4])
					}
				}
				mu.Unlock()
			}
		}
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go run(donor, wantDonor, 3)
	}
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go run(riderA, wantB, 3)
		go run(riderB, wantB, 3)
	}
	// The import path's word of the head on every block, concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for h := keys[0].height; h < keys[0].height+200; h++ {
			donor.Anchor(h)
			riderA.Anchor(h)
			riderB.Anchor(h)
		}
	}()
	wg.Wait()
	if wrong > 0 {
		t.Fatalf("%d of %d concurrent answers differ from the standalone (F-224)", wrong, calls)
	}
	if b := donor.builds.Load(); b != 1 {
		t.Fatalf("the donor built %d contexts, want one", b)
	}
	if b := riderA.builds.Load() + riderB.builds.Load(); b != 0 {
		t.Fatalf("the riders built %d contexts of their own; they ride the donor", b)
	}
	t.Logf("%d concurrent answers over one shared dataset, every one the standalone's", calls)
}

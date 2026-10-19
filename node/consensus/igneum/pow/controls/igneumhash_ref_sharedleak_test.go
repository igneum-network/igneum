//go:build controls

package controls

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// rssMB is the process's resident size in MiB from /proc/self/status.
func rssMB(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skip("no /proc/self/status")
	}
	m := regexp.MustCompile(`VmRSS:\s+(\d+) kB`).FindSubmatch(b)
	if m == nil {
		t.Skip("no VmRSS")
	}
	kb, _ := strconv.Atoi(string(m[1]))
	return kb / 1024
}

// TestSharedContextPerCallDoesNotLeak (16 October 2026, the research chain's
// second growth under 0.10.37: every node rising 5 to 15 GB an hour with no
// context build logged): a keyed row rides its unkeyed twin's dataset
// through a SHARED context created over the donor's memory and freed after
// every call (withSharedContext, the path of every keyed hash and share
// verification); the loop runs the keyed hash many times over one held
// dataset and reads the process's resident size, which must not climb with
// the count. The documents are the pair test's (the keyed form and its
// twin, 64 MiB).
func TestSharedContextPerCallDoesNotLeak(t *testing.T) {
	keyed := []byte(`{"version": 8, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 5152, "chain_id": 7315, "keyed_rounds": 2}`)
	twinDoc := []byte(`{"version": 8, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 4128, "chain_id": 7315}`)
	build := func(doc []byte) *ihRef {
		a, err := newIgneumhashRef(7315, doc)
		if err != nil {
			t.Fatal(err)
		}
		return a.(*ihRef)
	}
	row, twin := build(keyed), build(twinDoc)
	for _, a := range []*ihRef{row, twin} {
		a.SetFullDataset(true)
		a.SetSingleDataset(true)
	}
	row.SetDonor(twin)
	header := common.HexToHash("0x11")
	var sk [32]byte
	sk[0], sk[31] = 7, 9
	k := pow.Algorithm(row).(pow.Keyed)
	// The twin's dataset built once by the first call; every keyed hash
	// after it is one shared context created over the twin's memory and
	// freed.
	if _, result, _ := k.ComputeKeyed(header, 1, 1, &sk, nil); result == (common.Hash{}) {
		t.Fatal("the first keyed hash computed nothing")
	}
	if row.donor == nil {
		t.Fatal("the row dropped its donor (the shared context refused)")
	}
	base := rssMB(t)
	const rounds = 4000
	for i := 0; i < rounds; i++ {
		if _, result, _ := k.ComputeKeyed(header, 1, uint64(i)+2, &sk, nil); result == (common.Hash{}) {
			t.Fatalf("keyed hash %d computed nothing", i)
		}
		if i == rounds/4 || i == rounds/2 {
			t.Logf("after %d shared create/free calls: rss %d MiB (base %d)", i, rssMB(t), base)
		}
	}
	if row.donor == nil {
		t.Fatal("the row dropped its donor during the loop")
	}
	after := rssMB(t)
	t.Logf("after %d shared create/free calls: rss %d MiB (base %d MiB, +%d)", rounds, after, base, after-base)
	if after-base > 256 {
		t.Fatalf("the resident size climbed %d MiB over %d shared context create/free calls: a per-call leak", after-base, rounds)
	}
	row.Close()
	twin.Close()
}

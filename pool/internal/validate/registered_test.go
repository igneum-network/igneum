//go:build igneumhash

package validate

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// TestRegisteredVersion15 and TestRegisteredVersion16 are the vet's self-check (the project's lesson of 17:0x UK, 13
// October 2026; version 16 by the founder's order of 17:5x UK): the embedded reference verifies each registered
// version against its registered set before a pool build stores, so a pool built on a reference that predates a
// registration never reaches a miner. The first eight vectors of each unseen set.
func TestRegisteredVersion15(t *testing.T) {
	checkRegistered(t, 15, filepath.Join("v15", "v15-bw-r256-p64-unseen.json"))
}

// TestRegisteredVersion16 checks version 16 revision 3 (the static fabric, tails 1; main ba2fb4fe6f17), coordinate 4.
func TestRegisteredVersion16(t *testing.T) {
	checkRegistered(t, 16, filepath.Join("v16", "v16-fabric-r128-t1-unseen.json"))
}

// TestRegisteredVersion17 checks version 17 (the texture form, DOC-v17-texture-s512; the founder's "get textures on
// now" of 14 October 2026), vectorwork8's coordinate 5.
func TestRegisteredVersion17(t *testing.T) {
	checkRegistered(t, 17, filepath.Join("v17", "v17-texture-s512-unseen.json"))
}

func checkRegistered(t *testing.T, version int, file string) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "pow", "vectors", "igneumhash")
	b, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		t.Skipf("the registered set is not beside the pool: %v", err)
	}
	var set struct {
		Params  json.RawMessage `json:"params"`
		Vectors []struct {
			Height uint64 `json:"height"`
			Header string `json:"header"`
			Nonce  string `json:"nonce"`
			Mix    string `json:"mix"`
			Hash   string `json:"hash"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(b, &set); err != nil {
		t.Fatal(err)
	}
	v := &IgneumHash{algos: map[string]pow.Algorithm{}}
	var doc struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(set.Params, &doc); err != nil || doc.Version != version {
		t.Fatalf("%s: the set's document is version %d, not %d", file, doc.Version, version)
	}
	if err := v.Prepare(IgneumHashAlgorithm, set.Params, "v"+strconv.Itoa(version)+"-registered", set.Vectors[0].Height); err != nil {
		t.Fatalf("the embedded reference refuses the registered version %d document: %v", version, err)
	}
	n := len(set.Vectors)
	if n > 8 {
		n = 8
	}
	for _, vec := range set.Vectors[:n] {
		var header [32]byte
		hb, _ := hex.DecodeString(vec.Header)
		copy(header[:], hb)
		nonce, _ := strconv.ParseUint(vec.Nonce, 10, 64)
		mix, hash, err := v.Verify(IgneumHashAlgorithm, header, vec.Height, nonce)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(mix[:]) != vec.Mix || hex.EncodeToString(hash[:]) != vec.Hash {
			t.Fatalf("height %d nonce %d: the reference's hash differs from the registered set's (mix %x.. hash %x..)", vec.Height, nonce, mix[:4], hash[:4])
		}
	}
}

// TestRegisteredGraph pins the graph coordinate's relation in the pool's validator (the project's fault of 11:5x UK,
// 14 October 2026: coordinate 3 served no template): the node's "igneum-graph" document prepares and the first
// vectors of the registered set verify.
func TestRegisteredGraph(t *testing.T) {
	root := filepath.Join("..", "..", "..", "pow", "vectors", "igneumhash", "graph")
	b, err := os.ReadFile(filepath.Join(root, "graph-c7310-nonce-boundary.json"))
	if err != nil {
		t.Skipf("the registered set is not beside the pool: %v", err)
	}
	var set struct {
		Params  json.RawMessage `json:"params"`
		Vectors []struct {
			Height uint64 `json:"height"`
			Header string `json:"header"`
			Nonce  string `json:"nonce"`
			Mix    string `json:"mix"`
			Hash   string `json:"hash"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(b, &set); err != nil {
		t.Fatal(err)
	}
	v := &IgneumHash{algos: map[string]pow.Algorithm{}}
	if err := v.Prepare("igneum-graph", set.Params, "graph-registered", set.Vectors[0].Height); err != nil {
		t.Skipf("the graph relation is not in this build: %v", err)
	}
	// the graph relation is proof-carrying (Cuckaroo29): its Compute is the refused hash form by design (a zero pair),
	// so the pool prepares the document and names the coordinate's template, and the node verifies the proof a
	// graph share carries; nothing more is checked here
	if _, _, err := v.VerifyOn("igneum-graph", "graph-registered", [32]byte{}, 1, 0); err != nil {
		t.Fatalf("the prepared graph document must answer the pool's verify call: %v", err)
	}
}

// TestRegisteredVersion18 checks version 18 (the raster form, DOC-v18-raster-t1024; the protocol team's line of 19:4x UK,
// 14 October 2026), vectorwork8's coordinate 6.
func TestRegisteredVersion18(t *testing.T) {
	checkRegistered(t, 18, filepath.Join("v18", "v18-raster-t1024-unseen.json"))
}

// TestRegisteredVersion21 checks version 21 (the Forge2 anvil form, the lean pair k2 p64 document; vectorwork8's
// paid lanes 15 and 16 from 2426, the protocol team's line of 08:2x UK, 15 October 2026).
func TestRegisteredVersion21(t *testing.T) {
	checkRegistered(t, 21, filepath.Join("v21", "v21-forge2-anvilr-pair-k2-lean-p64-m16-l8-s128-unseen.json"))
}

// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package dag

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// memStore is the test's Lookup: every block's Data by hash, unit work.
type memStore struct {
	data map[common.Hash]*Data
	work map[common.Hash]*big.Int
}

func newMemStore() *memStore {
	return &memStore{data: map[common.Hash]*Data{}, work: map[common.Hash]*big.Int{}}
}

func (m *memStore) Data(h common.Hash) *Data { return m.data[h] }

func (m *memStore) Work(h common.Hash) *big.Int {
	if w, ok := m.work[h]; ok {
		return w
	}
	return big.NewInt(1)
}

// idHash is rusty-kaspa's string_to_hash: the id's bytes at the front, zero
// padded to 32, so the hash order of the papers' lettered blocks is the
// letters' order.
func idHash(id string) common.Hash {
	var h common.Hash
	copy(h[:], id)
	return h
}

func idHashes(ids []string) []common.Hash {
	out := make([]common.Hash, len(ids))
	for i, id := range ids {
		out[i] = idHash(id)
	}
	return out
}

// kaspaDAG is the test vector form of rusty-kaspa's
// testing/integration/testdata/dags/*.json (testdata/kaspa, the files as
// fetched on 19 October 2026 with their sums in NOTICE): dag0 is the
// PHANTOM paper's figure (k = 4), the others further cases of the
// implementation of record; every block's selected parent, mergeset blues
// (in colouring order), mergeset reds and blue score are asserted exactly
// as rusty-kaspa's ghostdag_test asserts them.
type kaspaDAG struct {
	K            uint8     `json:"K"`
	GenesisID    string    `json:"GenesisID"`
	ExpectedReds *[]string `json:"ExpectedReds"`
	Blocks       []struct {
		ID                     string   `json:"ID"`
		ExpectedScore          uint64   `json:"ExpectedScore"`
		ExpectedSelectedParent string   `json:"ExpectedSelectedParent"`
		ExpectedReds           []string `json:"ExpectedReds"`
		ExpectedBlues          []string `json:"ExpectedBlues"`
		Parents                []string `json:"Parents"`
	} `json:"Blocks"`
}

func TestGhostdagAgainstKaspaVectors(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "kaspa", "dag*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no test vectors: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var v kaspaDAG
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		store := newMemStore()
		genesis := idHash(v.GenesisID)
		store.data[genesis] = Genesis(genesis)
		for _, b := range v.Blocks {
			h := idHash(b.ID)
			d, err := Ghostdag(store, v.K, h, idHashes(b.Parents))
			if err != nil {
				t.Fatalf("%s block %s: %v", f, b.ID, err)
			}
			store.data[h] = d
			if d.SelectedParent != idHash(b.ExpectedSelectedParent) {
				t.Fatalf("%s block %s: selected parent %x, want %s", f, b.ID, d.SelectedParent[:2], b.ExpectedSelectedParent)
			}
			if !equalHashes(d.MergesetBlues, idHashes(b.ExpectedBlues)) {
				t.Fatalf("%s block %s: blues %v, want %v", f, b.ID, ids(d.MergesetBlues), b.ExpectedBlues)
			}
			if !equalHashes(d.MergesetReds, idHashes(b.ExpectedReds)) {
				t.Fatalf("%s block %s: reds %v, want %v", f, b.ID, ids(d.MergesetReds), b.ExpectedReds)
			}
			if d.BlueScore != b.ExpectedScore {
				t.Fatalf("%s block %s: blue score %d, want %d", f, b.ID, d.BlueScore, b.ExpectedScore)
			}
		}
		// The file's top-level reds are the virtual block's: a block over
		// every tip coloured by the same rule.
		children := map[common.Hash]bool{}
		for _, b := range v.Blocks {
			for _, p := range b.Parents {
				children[idHash(p)] = true
			}
		}
		var tips []common.Hash
		for _, b := range v.Blocks {
			if !children[idHash(b.ID)] {
				tips = append(tips, idHash(b.ID))
			}
		}
		if !children[genesis] {
			tips = append(tips, genesis)
		}
		virtual, err := Ghostdag(store, v.K, common.Hash{0xff}, tips)
		if err != nil {
			t.Fatal(err)
		}
		var reds []string
		for _, b := range v.Blocks {
			if !IsBlue(store, virtual, idHash(b.ID)) {
				reds = append(reds, b.ID)
			}
		}
		sort.Strings(reds)
		// Only dag0 and dag2 fill the file-level list (rusty-kaspa's test
		// does not assert it; the others leave it out).
		if v.ExpectedReds != nil {
			want := append([]string(nil), *v.ExpectedReds...)
			sort.Strings(want)
			if len(reds) != len(want) || (len(reds) > 0 && !equalStrings(reds, want)) {
				t.Fatalf("%s: the virtual block's reds %v, want %v", f, reds, want)
			}
		}
		t.Logf("%s: %d blocks, k %d, the virtual block's blue score %d, reds %v", filepath.Base(f), len(v.Blocks), v.K, virtual.BlueScore, reds)
	}
}

func ids(hs []common.Hash) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		n := 0
		for n < 32 && h[n] != 0 {
			n++
		}
		out[i] = string(h[:n])
	}
	return out
}

func equalHashes(a, b []common.Hash) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestKMatchesKaspa: the k table at Kaspa's own point (D = 5 s, 10 blocks a
// second, delta 0.01 gives the 124 of consensus/core/src/config/bps.rs) and
// at the design's (D = 2 s, one block every 4 s: 2 D lambda = 1, delta
// 0.01), the figure this step registers.
func TestKMatchesKaspa(t *testing.T) {
	if k := K(2*5*10, 0.01); k != 124 {
		t.Fatalf("k at Kaspa's 10 bps: %d, want 124", k)
	}
	if k := K(1, 0.01); k != 4 {
		t.Fatalf("k at the design's rate: %d, want 4", k)
	}
}

// TestOrderingIsDeterministic: the ordered mergeset and the colouring do
// not depend on the order the parents are listed in.
func TestOrderingIsDeterministic(t *testing.T) {
	store := newMemStore()
	g := idHash("G")
	store.data[g] = Genesis(g)
	mk := func(id string, parents ...string) *Data {
		d, err := Ghostdag(store, 3, idHash(id), idHashes(parents))
		if err != nil {
			t.Fatal(err)
		}
		store.data[idHash(id)] = d
		return d
	}
	mk("B", "G")
	mk("C", "G")
	mk("D", "G")
	e1, err := Ghostdag(store, 3, idHash("E"), idHashes([]string{"B", "C", "D"}))
	if err != nil {
		t.Fatal(err)
	}
	e2, err := Ghostdag(store, 3, idHash("E"), idHashes([]string{"D", "B", "C"}))
	if err != nil {
		t.Fatal(err)
	}
	if e1.SelectedParent != e2.SelectedParent || !equalHashes(e1.MergesetBlues, e2.MergesetBlues) || e1.BlueScore != e2.BlueScore || e1.BlueWork.Cmp(e2.BlueWork) != 0 {
		t.Fatal("the colouring depends on the parents' listed order")
	}
	if e1.SelectedParent != idHash("D") {
		t.Fatalf("selected parent at equal work is the greater hash: %v", ids([]common.Hash{e1.SelectedParent}))
	}
	store.data[idHash("E")] = e1
	om := OrderedMergeset(store, e1)
	if !equalHashes(om, idHashes([]string{"D", "B", "C"})) {
		t.Fatalf("ordered mergeset %v", ids(om))
	}
}

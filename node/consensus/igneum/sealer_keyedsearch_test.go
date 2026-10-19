// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library.
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

package igneum

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// newKeyedCPUEngine is the seal worker test's engine without the worker: the
// keyed stub over the null algorithm, the bond's secret set, the CPU sealer
// turned off as --dev.cpuseal=false leaves it (threads below zero).
func newKeyedCPUEngine(t *testing.T, s uint64) *Engine {
	t.Helper()
	base, err := pow.NewDev("null", nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Algorithm: &keyedStub{Algorithm: base, s: s}, Parts: testParts()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	var sk [32]byte
	sk[0], sk[31] = 7, 9
	pk := common.Hash{0xaa}
	e.lock.Lock()
	e.bondSK, e.keyedBond = &sk, &pk
	e.lock.Unlock()
	e.SetThreads(-1)
	return e
}

// TestKeyedTemplateSearchesWithoutTheCPUSealer (F-64, the ninth development
// chain's Labs-outage exercise, 12 October 2026: the bonded stand-in under
// --dev.cpuseal=false assembled 45 keyed templates and none was solved):
// with the CPU sealer off and no seal worker, a KEYED template is still
// searched on the node's own threads and sealed with the bond's secret;
// an unkeyed template (the open seat) under the same setting is left to
// the remote workers as the flag says.
func TestKeyedTemplateSearchesWithoutTheCPUSealer(t *testing.T) {
	e := newKeyedCPUEngine(t, 2)
	parent := common.HexToHash("0xab")
	b := sealThrough(t, e, sealWorkerTemplate(parent), 10*time.Second, 0)
	if b == nil {
		t.Fatal("a keyed template found no seal with the CPU sealer off")
	}
	h := b.Header()
	if len(h.Shares) != 64 {
		t.Fatalf("shares %d bytes, want the keyed 64", len(h.Shares))
	}
	k := e.algo.(*keyedStub)
	sk, _ := e.bondSecret()
	mix, _, shares := k.ComputeKeyed(e.SealHash(h), 7, h.Nonce.Uint64(), sk, nil)
	if mix != h.MixDigest || string(shares) != string(h.Shares) {
		t.Fatal("the delivered seal is not the reference's replay under the bond's secret")
	}
	if !e.HeightSealed(7, parent) {
		t.Fatal("the pair is not marked sealed")
	}
	// The open seat under the same setting: no CPU search, the template
	// goes to the remote workers alone.
	u := newKeyedCPUEngine(t, 0)
	if sealThrough(t, u, sealWorkerTemplate(common.HexToHash("0xac")), 700*time.Millisecond, 0) != nil {
		t.Fatal("an unkeyed template was searched on the CPU with the sealer off")
	}
}

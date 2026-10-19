// Copyright 2026 The core-geth Authors
// This file is part of the core-geth library.
//
// The core-geth library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The core-geth library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the core-geth library. If not, see <http://www.gnu.org/licenses/>.

package igneumhash

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"lukechampine.com/blake3"
)

// TestBLAKE3OfficialVectors checks the pinned module against the official
// BLAKE3 test vectors (testdata/blake3-vectors.json, from the BLAKE3 team's
// repository, the module's own copy): the unkeyed and keyed modes the envelope
// uses, over the first 131 output bytes so the extendable output is covered.
func TestBLAKE3OfficialVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/blake3-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Key   string `json:"key"`
		Cases []struct {
			InputLen  int    `json:"input_len"`
			Hash      string `json:"hash"`
			KeyedHash string `json:"keyed_hash"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 20 {
		t.Fatalf("%d cases, want the official 22", len(f.Cases))
	}
	key := []byte(f.Key)
	for _, c := range f.Cases {
		input := make([]byte, c.InputLen)
		for i := range input {
			input[i] = byte(i % 251)
		}
		want, _ := hex.DecodeString(c.Hash)
		h := blake3.New(len(want), nil)
		h.Write(input)
		if got := h.Sum(nil); !bytes.Equal(got, want) {
			t.Fatalf("input_len %d: hash mismatch", c.InputLen)
		}
		wantK, _ := hex.DecodeString(c.KeyedHash)
		hk := blake3.New(len(wantK), key)
		hk.Write(input)
		if got := hk.Sum(nil); !bytes.Equal(got, wantK) {
			t.Fatalf("input_len %d: keyed hash mismatch", c.InputLen)
		}
	}
}

func testAlgo(t *testing.T) *Algorithm {
	t.Helper()
	a, err := pow.NewDev(Name, EncodeChainParams(7310, DefaultParams()))
	if err != nil {
		t.Fatal(err)
	}
	return a.(*Algorithm)
}

func TestRegistryDevOnly(t *testing.T) {
	if _, err := pow.New(Name, EncodeChainParams(7310, DefaultParams())); !errors.Is(err, pow.ErrDevOnlyAlgorithm) {
		t.Fatalf("New outside --dev: %v, want ErrDevOnlyAlgorithm", err)
	}
	if _, err := pow.NewDev(Name, []byte{1, 2, 3}); err == nil {
		t.Fatal("a non-document accepted")
	}
	// The spec's document rules (0.1, revision 0.4): null and {} are the
	// version 1 baseline, {"version": 0} the version 0 baseline, an absent
	// member takes its version's default, an unknown member is refused,
	// cache_mib and parents are version 1 members.
	for _, doc := range []string{`{"version": 1}`, `{"version": 1, "rounds": 64}`, `{"version": 1, "cache_mib": 32, "parents": 256}`} {
		c, p, err := DecodeChainParams([]byte(doc))
		if err != nil || c != DefaultChainID || p != DefaultParams() {
			t.Fatalf("document %q: %d %+v %v", doc, c, p, err)
		}
	}
	for _, doc := range []string{`{"version": 0}`, `{"version": 0, "scratch_kib": 16, "dataset_mib": 256}`} {
		c, p, err := DecodeChainParams([]byte(doc))
		if err != nil || c != DefaultChainID || p != DefaultParamsV0() {
			t.Fatalf("document %q: %d %+v %v", doc, c, p, err)
		}
	}
	if _, p, err := DecodeChainParams([]byte(`{"version": 1, "rounds": 32, "chain_id": 1, "scratch_kib": 4}`)); err != nil || p.Rounds != 32 || p.ScratchBytes != 4<<10 || p.CacheBytes != 32<<20 {
		t.Fatalf("partial document: %+v %v", p, err)
	}
	// Revision 0.5 (security's F-14): "version" is mandatory; an empty document,
	// null or {} is refused, never defaulted to the newest version.
	for _, doc := range []string{"", "null", "{}", `{"rounds": 64}`, `{"cache_mib": 32, "parents": 256}`} {
		if _, _, err := DecodeChainParams([]byte(doc)); err == nil {
			t.Fatalf("document %q accepted without a version", doc)
		}
	}
	for _, doc := range []string{`{"version": 1, "foo": 1}`, `{"version": 1, "lanes": 16}`, `{"version": 1, "dataset_mib": 100}`, `{"version": 1, "scratch_kib": 2}`, `{"version": 1, "item_chain": 2}`, `{"version": 2}`, `{"version": 1, "rounds": 0}`, `{"version": 1} {}`,
		`{"version": 0, "cache_mib": 32}`, `{"version": 0, "parents": 1}`, `{"version": 0, "scratch_kib": 0}`, `{"version": 0, "item_chain": 257}`, `{"version": 1, "cache_mib": 3}`, `{"version": 1, "cache_mib": 512}`, `{"version": 1, "parents": 0}`, `{"version": 1, "parents": 4097}`} {
		if _, _, err := DecodeChainParams([]byte(doc)); err == nil {
			t.Fatalf("document %q accepted", doc)
		}
	}
	bad := DefaultParams()
	bad.Rounds = 0
	if _, err := pow.NewDev(Name, EncodeChainParams(7310, bad)); err == nil {
		t.Fatal("zero rounds accepted")
	}
	bad = DefaultParams()
	bad.Version = 2
	if _, err := pow.NewDev(Name, EncodeChainParams(7310, bad)); err == nil {
		t.Fatal("unknown version accepted")
	}
}

func TestParamsRoundTrip(t *testing.T) {
	for _, p := range ablation() {
		c, q, err := DecodeChainParams(EncodeChainParams(7310, p))
		if err != nil || c != 7310 || q != p {
			t.Fatalf("round trip of %+v: %+v %d %v", p, q, c, err)
		}
	}
}

// ablation is the programme's matrix for the verifier contract: the dataset
// size changes only the index modulus, so one size per scratchpad and rounds
// pair measures the cost.
func ablation() []Params {
	var out []Params
	for _, scratch := range []uint32{16 << 10, 64 << 10} {
		for _, rounds := range []uint32{32, 64} {
			for _, ds := range []uint64{256 << 20, 512 << 20, 1 << 30} {
				for _, chain := range []uint32{1, 4, 16} {
					out = append(out, Params{Version: Version0, DatasetBytes: ds, ScratchBytes: scratch, Rounds: rounds, Lanes: 8, EpochLength: 4096, ItemChain: chain})
				}
			}
		}
	}
	for _, scratch := range []uint32{0, 4 << 10} {
		for _, rounds := range []uint32{32, 64} {
			for _, cache := range []uint64{32 << 20, 64 << 20} {
				for _, parents := range []uint32{256, 512} {
					out = append(out, Params{Version: Version1, DatasetBytes: 1 << 30, ScratchBytes: scratch, Rounds: rounds, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: cache, Parents: parents})
				}
			}
		}
	}
	return out
}

// TestEnvelopeSeparation: every domain input differs by chain, version
// bytes, parameter set, header, nonce and epoch; nothing collides.
func TestEnvelopeSeparation(t *testing.T) {
	p := DefaultParams()
	e1 := NewEnvelope(7310, p)
	e2 := NewEnvelope(7311, p)
	q := p
	q.Rounds = 32 // the baseline is 64
	e3 := NewEnvelope(7310, q)
	h := common.HexToHash("0x01")
	if e1.Seed(h, 1) == e2.Seed(h, 1) || e1.Seed(h, 1) == e3.Seed(h, 1) || e1.Seed(h, 1) == e1.Seed(h, 2) || e1.Seed(h, 1) == e1.Seed(common.HexToHash("0x02"), 1) {
		t.Fatal("work seeds collide across chain, parameters, nonce or header")
	}
	if e1.EpochSeed(0) == e1.EpochSeed(1) || e1.EpochSeed(0) == e2.EpochSeed(0) {
		t.Fatal("epoch seeds collide")
	}
	if Item(e1.EpochSeed(0), 1, 0) == Item(e1.EpochSeed(0), 1, 1) || Item(e1.EpochSeed(0), 1, 0) == Item(e1.EpochSeed(1), 1, 0) || Item(e1.EpochSeed(0), 1, 0) == Item(e1.EpochSeed(0), 4, 0) {
		t.Fatal("items collide across index, epoch or chain length")
	}
	// Item chain 1 is exactly the keyed XOF over tag || index; the chain's
	// first link is the 32-byte prefix of the chain-1 item.
	one := Item(e1.EpochSeed(0), 1, 5)
	k1 := blake3.New(32, func() []byte { es := e1.EpochSeed(0); return es[:] }())
	k1.Write(append(append([]byte{byte(len(tagItem))}, tagItem...), 0, 0, 0, 0, 0, 0, 0, 5))
	if !bytes.Equal(k1.Sum(nil), one[:32]) {
		t.Fatal("item chain 1 is not the keyed hash over tag || u64be(i)")
	}
	if e1.Epoch(4095) != 0 || e1.Epoch(4096) != 1 || e1.Epoch(0) != 0 {
		t.Fatal("epoch mapping")
	}
}

func TestComputeDeterministicAndVerify(t *testing.T) {
	a := testAlgo(t)
	h := common.HexToHash("0xabcdef")
	m1, r1 := a.Compute(h, 10, 77)
	m2, r2 := a.Compute(h, 10, 77)
	if m1 != m2 || r1 != r2 {
		t.Fatal("Compute is not deterministic")
	}
	if m3, _ := a.Compute(h, 4096, 77); m3 == m1 {
		t.Fatal("the epoch does not reach the mix")
	}
	// Difficulty 1: every result passes; the mix must still match.
	if err := a.Verify(h, 10, 77, m1, big.NewInt(1), false); err != nil {
		t.Fatalf("valid proof refused: %v", err)
	}
	if err := a.Verify(h, 10, 77, common.Hash{}, big.NewInt(1), false); !errors.Is(err, pow.ErrInvalidMixDigest) {
		t.Fatalf("wrong mix: %v", err)
	}
	// The height reaches the proof only through the epoch: the header's
	// Number is inside H, and the engine checks the chain context's height
	// against it before this verifier runs. Same epoch, same mix.
	if m4, _ := a.Compute(h, 11, 77); m4 != m1 {
		t.Fatal("a height change inside one epoch changed the mix")
	}
	// The envelope's rejection order: difficulty first, before any work.
	for _, d := range []*big.Int{nil, big.NewInt(0), big.NewInt(-1), new(big.Int).Lsh(big.NewInt(1), 256)} {
		if err := a.Verify(h, 10, 77, m1, d, false); !errors.Is(err, pow.ErrInvalidDifficulty) {
			t.Fatalf("difficulty %v: %v", d, err)
		}
	}
	// The maximum difficulty: target 0, only a zero result passes.
	maxD := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	if err := a.Verify(h, 10, 77, m1, maxD, false); !errors.Is(err, pow.ErrResultAboveTarget) {
		t.Fatalf("max difficulty: %v", err)
	}
	// The epoch cache: alternating heights across a boundary keep both
	// epochs resident (a seed request alone builds nothing).
	for i := 0; i < 10; i++ {
		a.Compute(h, 4095, uint64(i))
		a.Compute(h, 4096, uint64(i))
	}
	a.mu.Lock()
	held0, held1 := a.held(0), a.held(1)
	a.mu.Unlock()
	if !held0 || !held1 {
		t.Fatalf("epoch cache: %+v", a.cache)
	}
	if a.Accesses() != uint64(DefaultParams().Rounds) {
		t.Fatalf("accesses %d", a.Accesses())
	}
	wp, ok := a.WorkParams().(Document)
	if !ok || wp.Version != 1 || wp.DatasetMiB != 1024 || wp.ScratchKiB != 0 || wp.ItemChain != 1 || wp.CacheMiB != 32 || wp.Parents != 256 || wp.ChainID != 7310 || wp.EpochBlocks != 4096 {
		t.Fatalf("work params %+v", a.WorkParams())
	}
	if got := a.CacheResident(); got != 2*32<<20 {
		t.Fatalf("cache resident %d bytes, want two epochs of 32 MiB", got)
	}
}

// TestVectors is the fixture rule: the frozen vectors must exist and match.
func TestVectors(t *testing.T) {
	a := testAlgo(t)
	vs := a.Vectors()
	if len(vs) == 0 {
		if os.Getenv("IGNEUM_REQUIRE_FIXTURES") != "" {
			t.Fatal("igneumhash: no frozen vectors (write them with IGNEUM_WRITE_FIXTURES=1 on the build box)")
		}
		t.Skip("igneumhash: no frozen vectors yet")
	}
	for _, v := range vs {
		m, r := a.Compute(v.HeaderHash, v.Height, v.Nonce)
		if m != v.MixDigest || r != v.Result {
			t.Fatalf("vector %q: mix %x result %x, want %x %x", v.Name, m, r, v.MixDigest, v.Result)
		}
	}
}

// TestWriteVectors writes testdata/vectors-v0.json for the baseline parameter
// set: the envelope's owed vectors (ENVELOPE-R0A-v0.md section 10).
func TestWriteVectors(t *testing.T) {
	if os.Getenv("IGNEUM_WRITE_FIXTURES") == "" {
		t.Skip("set IGNEUM_WRITE_FIXTURES=1 to regenerate the vectors")
	}
	a := New(vectorChainID, DefaultParams(), specWork{})
	// The epoch boundary cases below build two caches (two epochs resident).
	type vec struct {
		Name       string      `json:"name"`
		HeaderHash common.Hash `json:"headerHash"`
		Height     uint64      `json:"height"`
		Nonce      uint64      `json:"nonce"`
		MixDigest  common.Hash `json:"mixDigest"`
		Result     common.Hash `json:"result"`
	}
	cases := []struct {
		name   string
		header common.Hash
		height uint64
		nonce  uint64
	}{
		{"genesis-zero", common.Hash{}, 0, 0},
		{"genesis-nonce-max", common.Hash{}, 0, ^uint64(0)},
		{"epoch0-last", common.HexToHash("0x01"), 4095, 1},
		{"epoch1-first", common.HexToHash("0x01"), 4096, 1},
		{"lo32-rollover-before", common.HexToHash("0x02"), 7, 0x00000001_ffffffff},
		{"lo32-rollover-after", common.HexToHash("0x02"), 7, 0x00000002_00000000},
		{"all-ones-header", common.MaxHash, 123456, 0xdeadbeefcafef00d},
	}
	out := struct {
		ChainID uint64 `json:"chainId"`
		Params  Params `json:"params"`
		Vectors []vec  `json:"vectors"`
	}{ChainID: vectorChainID, Params: DefaultParams()}
	for _, c := range cases {
		m, r := a.Compute(c.header, c.height, c.nonce)
		out.Vectors = append(out.Vectors, vec{c.name, c.header, c.height, c.nonce, m, r})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/vectors-v0.json", append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d vectors", len(out.Vectors))
}

// TestChainConfigRoundTrip: the chain configuration carries the params
// document as JSON and survives the config encoder (the raw-bytes form did
// not: a NUL in the version field broke "Failed to JSON encode chain config").
func TestChainConfigRoundTrip(t *testing.T) {
	cfg := &ctypes.IgneumConfig{Algorithm: Name, AlgorithmParams: EncodeChainParams(7310, DefaultParams())}
	enc, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var back ctypes.IgneumConfig
	if err := json.Unmarshal(enc, &back); err != nil {
		t.Fatal(err)
	}
	c, p, err := DecodeChainParams(back.AlgorithmParams)
	if err != nil || c != 7310 || p != DefaultParams() || back.Algorithm != Name {
		t.Fatalf("round trip: %d %+v %v (%s)", c, p, err, enc)
	}
	if !bytes.Contains(enc, []byte(`"epoch_blocks":4096`)) || !bytes.Contains(enc, []byte(`"chain_id":7310`)) {
		t.Fatalf("the document is not the spec's shape: %s", enc)
	}
}

// TestFrozenBaselineParams pins the spec's frozen baseline (SPEC-R0A-v0.md
// revision 0.3, section 0.1): the 36 canonical bytes and P over them. A
// change here is a new algorithm version, never a silent edit.
func TestFrozenBaselineParams(t *testing.T) {
	want, _ := hex.DecodeString("00000000" + "0000000010000000" + "00004000" + "00000040" + "00000008" + "0000000000001000" + "00000001")
	if got := DefaultParamsV0().Encode(); !bytes.Equal(got, want) {
		t.Fatalf("version 0 baseline params %x, the spec freezes %x", got, want)
	}
	wantP, _ := hex.DecodeString("e6a228c9f49643c0d1edfc1be79baa11aebc9c602143a8bae28332f0f9ee06f6")
	if p := DefaultParamsV0().Hash(); !bytes.Equal(p[:], wantP) {
		t.Fatalf("version 0 baseline P %x, the spec freezes %x", p, wantP)
	}
	want1, _ := hex.DecodeString("00000001" + "0000000040000000" + "00000000" + "00000040" + "00000008" + "0000000000001000" + "00000001" + "0000000002000000" + "00000100")
	if got := DefaultParams().Encode(); !bytes.Equal(got, want1) {
		t.Fatalf("version 1 baseline params %x, the spec freezes %x", got, want1)
	}
	wantP1, _ := hex.DecodeString("de6795efbe4d1992643788e9c7a8d7e7228f28cb0302459ad2dfcffeea0f3c9b")
	if p := DefaultParams().Hash(); !bytes.Equal(p[:], wantP1) {
		t.Fatalf("version 1 baseline P %x, the spec freezes %x", p, wantP1)
	}
}

// TestCacheAndParentWalk pins version 1's item derivation (SPEC-R0A-v0.md
// 5.3) against a direct transcription of the spec's text: the sequential
// cache, the index fold, the FNV parent walk, the keyed XOF over the mix.
func TestCacheAndParentWalk(t *testing.T) {
	es := NewEnvelope(7310, DefaultParams()).EpochSeed(3)
	cache := BuildCache(es, 1<<20) // 8192 items
	if len(cache) != 1<<20 {
		t.Fatal("cache size")
	}
	// cache(0) and cache(1) by hand.
	h := blake3.New(32, es[:])
	in := append([]byte{byte(len(tagCache))}, tagCache...)
	h.Write(append(in, 0, 0, 0, 0, 0, 0, 0, 0))
	var c0 [ItemBytes]byte
	h.XOF().Read(c0[:])
	if !bytes.Equal(cache[:ItemBytes], c0[:]) {
		t.Fatal("cache(0)")
	}
	h.Reset()
	h.Write(append(append(append([]byte{}, in...), 0, 0, 0, 0, 0, 0, 0, 1), c0[:32]...))
	var c1 [ItemBytes]byte
	h.XOF().Read(c1[:])
	if !bytes.Equal(cache[ItemBytes:2*ItemBytes], c1[:]) {
		t.Fatal("cache(1) does not chain on cache(0)")
	}
	// item(i) by hand, 3 parents.
	const parents = 3
	i := uint64(0x1_0000_0005)
	n := uint64(len(cache) / ItemBytes)
	var mix [32]uint32
	for k := range mix {
		mix[k] = binary.LittleEndian.Uint32(cache[(i%n)*ItemBytes+uint64(4*k):])
	}
	mix[0] ^= uint32(i)
	mix[1] ^= uint32(i >> 32)
	for p := uint32(0); p < parents; p++ {
		parent := uint64((uint32(i)^p)*0x01000193^mix[p%32]) % n
		for k := range mix {
			mix[k] = (mix[k] * 0x01000193) ^ binary.LittleEndian.Uint32(cache[parent*ItemBytes+uint64(4*k):])
		}
	}
	h.Reset()
	in = append([]byte{byte(len(tagItem))}, tagItem...)
	in = binary.BigEndian.AppendUint64(in, i)
	for k := range mix {
		in = binary.LittleEndian.AppendUint32(in, mix[k])
	}
	h.Write(in)
	var want, got [ItemBytes]byte
	h.XOF().Read(want[:])
	NewItemsV1(es, cache, parents).Item(i, &got)
	if got != want {
		t.Fatal("item(i) differs from the spec's text")
	}
	// A different epoch seed gives a different cache; a different parent
	// count a different item.
	if bytes.Equal(BuildCache(NewEnvelope(7310, DefaultParams()).EpochSeed(4), 1<<20)[:ItemBytes], c0[:]) {
		t.Fatal("cache does not depend on the epoch")
	}
	var got2 [ItemBytes]byte
	NewItemsV1(es, cache, parents+1).Item(i, &got2)
	if got2 == got {
		t.Fatal("item does not depend on the parent count")
	}
}

// TestEpochAdmission is security's F-18: anchored to the chain head, the
// adapter admits one epoch each side of the head's, refuses any other before
// a cache is built, holds at most three caches, and builds nothing for a
// thrash of headers across the window; near the end of an epoch the next
// epoch's cache is built ahead.
func TestEpochAdmission(t *testing.T) {
	p := DefaultParams()
	p.CacheBytes = 1 << 20 // the smallest cache: the test is about counts, not bytes
	a := New(vectorChainID, p, defaultWork())
	h := common.HexToHash("0x11")
	L := p.EpochLength
	a.Anchor(5*L + 10) // head in epoch 5
	for _, height := range []uint64{4 * L, 5 * L, 6 * L} {
		if err := a.Verify(h, height, 1, common.Hash{1}, big.NewInt(1), false); err != pow.ErrInvalidMixDigest {
			t.Fatalf("height %d: %v, want invalid mix digest (admitted, verified)", height, err)
		}
	}
	if got := a.Builds(); got != 3 {
		t.Fatalf("%d builds for three admitted epochs", got)
	}
	for _, height := range []uint64{3*L + 7, 7 * L, 100 * L} {
		if err := a.Verify(h, height, 1, common.Hash{1}, big.NewInt(1), false); err != pow.ErrEpochNotAdmitted {
			t.Fatalf("height %d: %v, want not admitted", height, err)
		}
	}
	if got := a.Builds(); got != 3 {
		t.Fatalf("a refused epoch built a cache (%d builds)", got)
	}
	// 1,000 headers alternating across the three admitted epochs (TP-43's
	// shape): no rebuild.
	for i := 0; i < 1000; i++ {
		height := uint64(4+i%3)*L + uint64(i)
		if err := a.Verify(h, height, uint64(i), common.Hash{byte(i)}, big.NewInt(1), false); err != pow.ErrInvalidMixDigest {
			t.Fatalf("height %d: %v", height, err)
		}
	}
	if got := a.Builds(); got != 3 {
		t.Fatalf("the cross-epoch thrash built %d caches", got-3)
	}
	if got := a.CacheResident(); got != 3*int(p.CacheBytes) {
		t.Fatalf("cache resident %d bytes, want three caches", got)
	}
	// The head moves to epoch 6: epoch 4 is released, epoch 7 is admitted and
	// built on demand; epoch 4 is refused.
	a.Anchor(6*L + 1)
	if got := a.CacheResident(); got != 2*int(p.CacheBytes) {
		t.Fatalf("after the move: %d bytes resident, want two caches", got)
	}
	if err := a.Verify(h, 4*L, 1, common.Hash{1}, big.NewInt(1), false); err != pow.ErrEpochNotAdmitted {
		t.Fatalf("epoch 4 at head 6: %v", err)
	}
	// Near the end of epoch 6 the next epoch's cache is built ahead.
	a.Anchor(7*L - PrebuildBlocks + 1)
	deadline := time.Now().Add(10 * time.Second)
	for a.Builds() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := a.Builds(); got != 4 {
		t.Fatalf("pre-build did not run (%d builds)", got)
	}
	a.mu.Lock()
	held := a.held(7)
	a.mu.Unlock()
	if !held {
		t.Fatal("the next epoch's cache is not resident after the pre-build")
	}
}

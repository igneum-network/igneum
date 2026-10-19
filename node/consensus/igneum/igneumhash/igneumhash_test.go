package igneumhash

import (
	"encoding/hex"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tiny(t *testing.T) (Params, *Dataset) {
	t.Helper()
	p := Ablation["test-tiny"]
	ds, err := p.Generate(0)
	if err != nil {
		t.Fatal(err)
	}
	return p, ds
}

func TestParamsValidate(t *testing.T) {
	for name, p := range Ablation {
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if BaselineV0.Variant() != "d256-s16-r64-c1" || Baseline.Variant() != "v1-d1024-s0-r64-k32-p256" {
		t.Fatalf("baseline variants %s %s", BaselineV0.Variant(), Baseline.Variant())
	}
	bad := BaselineV0
	bad.DatasetBytes = MaxDatasetBytes * 2
	if bad.Validate() == nil {
		t.Fatal("oversize dataset accepted")
	}
	bad = BaselineV0
	bad.DatasetBytes = 3 << 20
	if bad.Validate() == nil {
		t.Fatal("non-power-of-two dataset accepted")
	}
	bad = BaselineV0
	bad.Rounds = 0
	if bad.Validate() == nil {
		t.Fatal("zero rounds accepted")
	}
	bad = BaselineV0
	bad.Lanes = 16
	if bad.Validate() == nil {
		t.Fatal("lanes other than 8 accepted")
	}
	bad = BaselineV0
	bad.ItemChain = 0
	if bad.Validate() == nil {
		t.Fatal("zero item chain accepted")
	}
	bad = BaselineV0
	bad.ScratchBytes = 0
	if bad.Validate() == nil {
		t.Fatal("version 0 without a scratchpad accepted")
	}
	bad = BaselineV0
	bad.CacheBytes = 1 << 25
	if bad.Validate() == nil {
		t.Fatal("version 0 with a cache accepted")
	}
	bad = Baseline
	bad.ItemChain = 4
	if bad.Validate() == nil {
		t.Fatal("version 1 with an item chain accepted")
	}
	bad = Baseline
	bad.Parents = 0
	if bad.Validate() == nil {
		t.Fatal("version 1 without parents accepted")
	}
	bad = Baseline
	bad.ScratchBytes = 512 // below the 1 KiB minimum of revision 0.12 (2 KiB is admitted since then)
	if bad.Validate() == nil {
		t.Fatal("version 1 with a 512-byte scratchpad accepted")
	}
}

// Section 0.1 and 1: the 36 parameter bytes, P of the baseline (the frozen
// value), the tags' lengths, the epoch derivation, the permutation.
func TestEnvelopeEncodings(t *testing.T) {
	want := "00000000" + "0000000010000000" + "00004000" + "00000040" + "00000008" + "0000000000001000" + "00000001"
	if got := hex.EncodeToString(BaselineV0.ParamsBytes()); got != want || len(BaselineV0.ParamsBytes()) != 36 {
		t.Fatalf("v0 params bytes %s want %s", got, want)
	}
	ph := BaselineV0.ParamsHash()
	if got := hex.EncodeToString(ph[:]); got != "e6a228c9f49643c0d1edfc1be79baa11aebc9c602143a8bae28332f0f9ee06f6" {
		t.Fatalf("v0 baseline P = %s", got)
	}
	want1 := "00000001" + "0000000040000000" + "00000000" + "00000040" + "00000008" + "0000000000001000" + "00000001" + "0000000002000000" + "00000100"
	if got := hex.EncodeToString(Baseline.ParamsBytes()); got != want1 || len(Baseline.ParamsBytes()) != 48 {
		t.Fatalf("v1 params bytes %s want %s", got, want1)
	}
	ph = Baseline.ParamsHash()
	if got := hex.EncodeToString(ph[:]); got != "de6795efbe4d1992643788e9c7a8d7e7228f28cb0302459ad2dfcffeea0f3c9b" {
		t.Fatalf("v1 baseline P = %s", got)
	}
	for _, c := range []struct {
		t string
		n int
	}{{TagSeed, 19}, {TagEpoch, 20}, {TagItem, 19}, {TagDataset, 22}, {TagLanes, 20}, {TagMix, 18}, {TagFinal, 20}, {TagCache, 20}} {
		if len(c.t) != c.n || tag(c.t)[0] != byte(c.n) {
			t.Errorf("tag %q has length %d, the specification says %d", c.t, len(c.t), c.n)
		}
	}
	if Baseline.EpochOf(4095) != 0 || Baseline.EpochOf(4096) != 1 || Baseline.EpochOf(0) != 0 {
		t.Fatal("epoch derivation")
	}
	for k, want := range []int{1, 6, 11, 0, 5, 10, 15, 4, 9, 14, 3, 8, 13, 2, 7, 12} {
		if perm(k) != want {
			t.Fatalf("perm(%d) = %d want %d", k, perm(k), want)
		}
	}
	// the params document of section 0.1 at revision 0.5: version is
	// mandatory; null, {} and an empty document are refused; {"version": 1}
	// is the version 1 baseline, {"version": 0} the version 0 one
	for _, doc := range []string{"null", "{}", "", `{"rounds": 64}`} {
		if _, err := ParamsFromDocument([]byte(doc)); err == nil {
			t.Fatalf("document %q accepted without a version", doc)
		}
	}
	p, err := ParamsFromDocument([]byte(`{"version":1}`))
	if err != nil || p != Baseline {
		t.Fatalf("document version 1: %v %+v", err, p)
	}
	p, err = ParamsFromDocument([]byte(`{"version":0}`))
	if err != nil || p != BaselineV0 {
		t.Fatalf("document version 0: %v %+v", err, p)
	}
	p, err = ParamsFromDocument([]byte(`{"version":0,"dataset_mib":1024,"scratch_kib":64,"rounds":32,"item_chain":16,"epoch_blocks":4096}`))
	if err != nil || p.Variant() != "d1024-s64-r32-c16" {
		t.Fatalf("document: %v %s", err, p.Variant())
	}
	p, err = ParamsFromDocument([]byte(`{"version":1,"dataset_mib":2048,"cache_mib":64,"parents":512}`))
	if err != nil || p.Variant() != "v1-d2048-s0-r64-k64-p512" {
		t.Fatalf("document v1: %v %s", err, p.Variant())
	}
	if _, err := ParamsFromDocument([]byte(`{"version":0,"cache_mib":32}`)); err == nil {
		t.Fatal("cache_mib accepted in a version 0 document")
	}
	if _, err := ParamsFromDocument([]byte(`{"version":2}`)); err == nil {
		t.Fatal("version 2 accepted")
	}
}

// The materialised dataset and the on-demand items are the same bytes, for
// version 0 at item chains 1 and 4 and for version 1 with and without a
// scratchpad; the full and the light paths give the same proof.
func TestDatasetMatchesLight(t *testing.T) {
	cells := []Params{Ablation["test-tiny"], Ablation["test-tiny"], Ablation["test-tiny-v1"], Ablation["test-tiny-v1-s4"]}
	cells[1].ItemChain = 4
	for ci, p := range cells {
		chain := uint32(ci)
		ds, err := p.Generate(0)
		if err != nil {
			t.Fatal(err)
		}
		light := p.NewLight(0)
		if light.EpochSeed() != ds.EpochSeed() || p.DatasetID(light.EpochSeed()) != ds.ID {
			t.Fatal("epoch seed or dataset id differ between the cache and the light source")
		}
		var a, b [ItemBytes]byte
		for _, i := range []uint64{0, 1, 2, p.Items() / 2, p.Items() - 1} {
			ds.Item(i, &a)
			light.Item(i, &b)
			if a != b {
				t.Fatalf("chain %d: item %d differs", chain, i)
			}
		}
		var hh [32]byte
		full, _ := p.Compute(ds, hh, 3, 9, true)
		lite, _ := p.Compute(light, hh, 3, 9, true)
		if full.Digest != lite.Digest || full.Mix != lite.Mix {
			t.Fatalf("chain %d: full and light verification differ", chain)
		}
		if ds.Bytes() != p.DatasetBytes {
			t.Fatalf("dataset bytes %d want %d", ds.Bytes(), p.DatasetBytes)
		}
		if p.Version == Version1 {
			if len(light.Cache()) != int(p.CacheBytes) {
				t.Fatalf("cache bytes %d want %d", len(light.Cache()), p.CacheBytes)
			}
			if (full.Trace.Fold == nil) != (p.ScratchBytes == 0) {
				t.Fatal("the fold must be present exactly when there is a scratchpad")
			}
		}
	}
	p := Ablation["test-tiny"]
	ds0, _ := p.Generate(0)
	ds1, _ := p.Generate(1)
	if ds1.ID == ds0.ID || ds1.Digest() == ds0.Digest() {
		t.Fatal("epochs 0 and 1 share an id or a digest")
	}
	q := p
	q.ItemChain = 4
	dq, _ := q.Generate(0)
	if dq.Digest() == ds0.Digest() {
		t.Fatal("item chain 4 gives the same items as chain 1")
	}
}

func TestComputeBindsEveryInput(t *testing.T) {
	p, ds := tiny(t)
	var hh [32]byte
	hh[0] = 1
	a, err := p.Compute(ds, hh, 5, 42, true)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.Compute(ds, hh, 5, 42, true)
	if a.Digest != b.Digest || a.Mix != b.Mix || a.Trace == nil || len(a.Trace.Rounds) != int(p.Rounds) {
		t.Fatal("compute not deterministic or trace missing")
	}
	c, _ := p.Compute(ds, hh, 5, 43, false)
	if c.Digest == a.Digest || c.Seed == a.Seed {
		t.Fatal("nonce does not bind")
	}
	hh[31] ^= 1
	d, _ := p.Compute(ds, hh, 5, 42, false)
	if d.Digest == a.Digest {
		t.Fatal("header does not bind")
	}
	hh[31] ^= 1
	// section 2: the same (H, N) at two heights of one epoch gives the same M and F
	e, _ := p.Compute(ds, hh, 6, 42, false)
	if e.Mix != a.Mix || e.Digest != a.Digest {
		t.Fatal("heights of one epoch must give the same proof")
	}
	q := p
	q.ChainID++
	if q.Seed(hh, 42) == p.Seed(hh, 42) || q.EpochSeed(0) == p.EpochSeed(0) {
		t.Fatal("chain id does not bind")
	}
	q = p
	q.Rounds++
	if q.ParamsHash() == p.ParamsHash() || q.Seed(hh, 42) == p.Seed(hh, 42) {
		t.Fatal("parameter set does not bind")
	}
	if _, err := q.Compute(ds, hh, 5, 42, false); err == nil {
		t.Fatal("item source of another parameter set accepted")
	}
	if _, err := p.Compute(ds, hh, p.EpochLength, 42, false); err == nil {
		t.Fatal("item source of another epoch accepted")
	}
	// the adapter interface gives the same M
	var w Work
	m := w.Mix(a.Seed, ds.EpochSeed(), func(i uint64, out *[ItemBytes]byte) { ds.Item(i, out) }, p)
	if m != a.Mix {
		t.Fatal("Work.Mix differs from Compute")
	}
	if w.Accesses(p) != uint64(p.Rounds) {
		t.Fatal("Accesses")
	}
}

func TestTargetAndVerify(t *testing.T) {
	p, ds := tiny(t)
	var hh [32]byte
	st, err := p.Compute(ds, hh, 1, 7, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Target(nil); ok {
		t.Fatal("nil difficulty accepted")
	}
	if _, ok := Target(big.NewInt(0)); ok {
		t.Fatal("zero difficulty accepted")
	}
	if _, ok := Target(new(big.Int).Lsh(big.NewInt(1), 256)); ok {
		t.Fatal("2^256 accepted")
	}
	one, _ := Target(big.NewInt(1))
	for i := range one {
		if one[i] != 0xff {
			t.Fatal("difficulty 1 must give the all-ones target")
		}
	}
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	one1, _ := Target(max)
	var wantOne [32]byte
	wantOne[31] = 1
	if one1 != wantOne {
		t.Fatalf("difficulty 2^256 - 1 must give target 1, got %x", one1)
	}
	two, _ := Target(big.NewInt(2))
	if two[0] != 0x7f || two[31] != 0xff {
		t.Fatalf("difficulty 2: %x", two)
	}
	if _, err := p.Verify(ds, hh, 1, 7, st.Mix, big.NewInt(0)); err != ErrDifficulty {
		t.Fatalf("zero difficulty: %v", err)
	}
	if _, err := p.Verify(ds, hh, 1, 7, st.Mix, big.NewInt(1)); err != nil {
		t.Fatalf("difficulty 1 must accept: %v", err)
	}
	if _, err := p.Verify(nil, hh, 1, 7, st.Mix, big.NewInt(1)); err != nil {
		t.Fatalf("light verification must accept: %v", err)
	}
	if _, err := p.Verify(ds, hh, 1, 7, st.Mix, max); err != ErrTarget {
		t.Fatalf("target 1 must reject an ordinary digest: %v", err)
	}
	wrong := st.Mix
	wrong[0] ^= 1
	if _, err := p.Verify(ds, hh, 1, 7, wrong, big.NewInt(1)); err != ErrMix {
		t.Fatalf("wrong mix: %v", err)
	}
	f := new(big.Int).SetBytes(st.Digest[:])
	if f.Sign() > 0 {
		d := new(big.Int).Div(max, f)
		if _, err := p.Verify(ds, hh, 1, 7, st.Mix, d); err != nil {
			t.Fatalf("boundary difficulty must accept: %v", err)
		}
	}
}

// The baseline known-answer vector the hash team published (height 1,
// d256-s16-r64-c1), computed by light verification: the first cross-check
// of the two independent implementations.
func TestBaselineKnownAnswer(t *testing.T) {
	hh, _ := hex32("6468beee66bb5264f1191b196797b748220bf33cf2072630a7f1c030c520c307")
	st, err := BaselineV0.Compute(BaselineV0.NewLight(0), hh, 1, 4312426439069536300, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(st.Mix[:]); got != "eec644f5195d87ebd8a9326580303ef03856112a6d8d751939184e89c47dc086" {
		t.Errorf("M = %s", got)
	}
	if got := hex.EncodeToString(st.Digest[:]); got != "ce6898a3ea75f0061dcdd5f6058643135ec856f79f65b6b92f7046dc390e9901" {
		t.Errorf("F = %s", got)
	}
}

// TestReferenceVectors runs every reference vector file of the tree
// (pow/vectors/igneumhash/<variant>/<class>.json, the reference's own
// classes) and any under testdata/ref/<variant>/. With no file it fails under
// IGNEUM_REQUIRE_FIXTURES=1 (a mandatory fixture that is missing must fail,
// the plan page 24) and skips otherwise. Any divergence fails the test and
// names the first diverging stage.
func TestReferenceVectors(t *testing.T) {
	files, _ := filepath.Glob("../../../../pow/vectors/igneumhash/*/*.json")
	extra, _ := filepath.Glob("testdata/ref/*/*.json")
	files = append(files, extra...)
	if len(files) == 0 {
		if os.Getenv("IGNEUM_REQUIRE_FIXTURES") != "" {
			t.Fatal("no reference vectors under testdata/ref/")
		}
		t.Skip("no reference vectors under testdata/ref/")
	}
	// IGNEUM_VECTOR_FILTER names a substring of the set's directory (the launch document's tag, say) to run alone
	filter := os.Getenv("IGNEUM_VECTOR_FILTER")
	ran, notReached := 0, 0
	for _, f := range files {
		d := filepath.Base(filepath.Dir(f))
		if strings.HasPrefix(d, "v2-") || strings.HasPrefix(d, "v3-") || strings.HasPrefix(d, "v5-") || strings.HasPrefix(d, "v7-") {
			t.Logf("skip %s: versions 2 (the R0-B schedule), 3 (the item-width variant), 5 (the multiply-high map alone) and 7 (v6b) are not implemented by this package", f)
			continue
		}
		if filter != "" && !strings.Contains(d, filter) {
			continue
		}
		vf, err := LoadReference(f)
		if err != nil {
			t.Fatal(err)
		}
		_, divs, err := vf.Check()
		if errors.Is(err, ErrNotReached) {
			// version 8's wider state (REGISTERS 32 or 64): accepted at the rule, not computed by this port; named, never a pass
			t.Logf("not reached %s: %v", f, err)
			notReached++
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, d := range divs {
			t.Errorf("%s: %s", f, d)
		}
		skipped := 0
		for _, v := range vf.Vectors {
			if v.Skip != "" {
				skipped++
			}
		}
		if skipped == len(vf.Vectors) {
			t.Logf("skipped %s (%d vectors not run by this checker)", f, skipped)
			continue
		}
		t.Logf("exact %s (%d vectors)", f, len(vf.Vectors))
		ran++
	}
	if notReached > 0 {
		t.Logf("%d set files not reached by this port (the wider state)", notReached)
	}
	if ran == 0 {
		t.Fatal("no reference vector file of a supported version")
	}
}

// The Go side's own vectors for the tiny cell, frozen so a change in this
// implementation is seen (the reference agreement is the test above).
func TestSelfVectors(t *testing.T) {
	const path = "testdata/self-tiny.json"
	vf, err := LoadVectors(path)
	if err != nil {
		if os.Getenv("IGNEUM_WRITE_VECTORS") == "" {
			t.Skip("no self vectors yet; run with IGNEUM_WRITE_VECTORS=1 to write them")
		}
		vf = &VectorFile{Algorithm: Name, Spec: "SPEC-R0A-v0 revision 0.3", Params: Ablation["test-tiny"]}
		for i := uint64(0); i < 8; i++ {
			var hh [32]byte
			hh[0] = byte(i)
			hh[31] = byte(255 - i)
			vf.Vectors = append(vf.Vectors, Vector{Name: "tiny-" + string(rune('a'+i)), HeaderHash: hexOf(hh), Height: i * 37, Nonce: i*1_000_003 + 1, Difficulty: "1"})
		}
		results, _, err := vf.Check()
		if err != nil {
			t.Fatal(err)
		}
		vf.Fill(results, true)
		raw, _ := jsonIndent(vf)
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("wrote " + path)
		return
	}
	_, divs, err := vf.Check()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range divs {
		t.Errorf("%s", d)
	}
}

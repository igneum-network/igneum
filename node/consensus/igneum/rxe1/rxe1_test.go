package rxe1

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"lukechampine.com/blake3"
)

// A small test-only record: 1 MiB dataset (8192 items), 16 KiB cache, R 32, K 8.
func testParams(mode Mode, b uint32) Params {
	return Params{DatasetSpecDigest: DatasetSpecDigest, DatasetBytes: 1 << 20, Parents: 256, CacheBytes: 16 << 10, R: 32, K: 8, B: b, Mode: mode, Schema: Schema}
}

func testInput(nonce uint64) Input {
	in := Input{ChainID: []byte("rxe1-test"), HeaderBlob: []byte("header"), EpochNumber: 3, Nonce: nonce}
	for i := range in.EpochSeed {
		in.EpochSeed[i] = byte(200 - i)
	}
	return in
}

func TestEncodingExact(t *testing.T) {
	// LP and Enc byte for byte (section 6)
	if got := LP([]byte("ab")); !bytes.Equal(got, []byte{2, 0, 0, 0, 'a', 'b'}) {
		t.Fatalf("LP %x", got)
	}
	if got := LP(nil); !bytes.Equal(got, []byte{0, 0, 0, 0}) {
		t.Fatalf("LP empty %x", got)
	}
	enc := Enc("T", []byte{1}, le32(7))
	want := append(append(append([]byte{1, 0, 0, 0, 'T'}, 1, 0, 0, 0, 1), 4, 0, 0, 0), 7, 0, 0, 0)
	if !bytes.Equal(enc, want) {
		t.Fatalf("Enc %x want %x", enc, want)
	}
	if h := H("T", []byte{1}, le32(7)); h != blake3.Sum256(want) {
		t.Fatal("H is not BLAKE3 over Enc")
	}
	x := X("T", 64, []byte{1}, le32(7))
	if !bytes.Equal(x[:32], h32(want)) {
		t.Fatal("X's first 32 bytes are not the hash")
	}
	// the parameter record: 72 bytes in the specified order and widths
	p := testParams(ModeF, 2)
	rec := p.Record()
	if len(rec) != 72 {
		t.Fatalf("record %d bytes", len(rec))
	}
	if !bytes.Equal(rec[:32], p.DatasetSpecDigest[:]) || binary.LittleEndian.Uint64(rec[32:]) != p.DatasetBytes ||
		binary.LittleEndian.Uint32(rec[40:]) != p.Parents || binary.LittleEndian.Uint64(rec[44:]) != p.CacheBytes ||
		binary.LittleEndian.Uint32(rec[52:]) != p.R || binary.LittleEndian.Uint32(rec[56:]) != p.K ||
		binary.LittleEndian.Uint32(rec[60:]) != p.B || binary.LittleEndian.Uint32(rec[64:]) != uint32(p.Mode) ||
		binary.LittleEndian.Uint32(rec[68:]) != p.Schema {
		t.Fatal("record layout")
	}
	if p.PID() != H("RXE1/params", rec) {
		t.Fatal("pid")
	}
}

func h32(b []byte) []byte { h := blake3.Sum256(b); return h[:] }

func TestValidation(t *testing.T) {
	prod := Params{DatasetSpecDigest: DatasetSpecDigest, DatasetBytes: 2 << 30, Parents: 256, CacheBytes: 32 << 20, R: 32, K: 8, B: 0, Mode: ModeM, Schema: Schema}
	if err := prod.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := prod.ValidateTestOnly(); err != nil {
		t.Fatal("a production record is also a valid shape for the test validator")
	}
	tp := testParams(ModeM, 0)
	if err := tp.ValidateTestOnly(); err != nil {
		t.Fatal(err)
	}
	if tp.Validate() == nil {
		t.Fatal("a small record must never validate as production (by construction)")
	}
	small := prod
	small.DatasetBytes = 1 << 20
	if small.Validate() == nil {
		t.Fatal("a small production dataset accepted")
	}
	wrongCache := prod
	wrongCache.CacheBytes = 64 << 20
	if wrongCache.Validate() == nil {
		t.Fatal("a cache other than the pinned 32 MiB accepted")
	}
	wrongDigest := prod
	wrongDigest.DatasetSpecDigest[0] ^= 1
	if wrongDigest.Validate() == nil {
		t.Fatal("a record with another dataset-spec digest accepted")
	}
	if hex.EncodeToString(DatasetSpecDigest[:]) != "ead585e08733f3b891d3f4f9d77197d448a289c6334e0f274411832f62fc7109" {
		t.Fatalf("binding constant %x", DatasetSpecDigest)
	}
	// the M screening record's 72 bytes and pid (REFERENCE-NOTES.md section 4)
	rec := hex.EncodeToString(prod.Record())
	want := "ead585e08733f3b891d3f4f9d77197d448a289c6334e0f274411832f62fc7109" + "0000008000000000" + "00010000" + "0000000200000000" + "20000000" + "08000000" + "00000000" + "00000000" + "01000000"
	if rec != want {
		t.Fatalf("M record\n%s\n%s", rec, want)
	}
	if pid := prod.PID(); hex.EncodeToString(pid[:]) != "853b090471c7f6ef9562e0f5c7acc7fb7b66c4ed4d71860d091098705d1b6e9d" {
		t.Fatalf("M pid %x", pid)
	}
	e2 := prod
	e2.Mode, e2.B = ModeE, 2
	if pid := e2.PID(); hex.EncodeToString(pid[:]) != "1891e1763b08ec7c7eab99d3f2faf3d8fb5bad55b7309b9d3f72a19febc24f4b" {
		t.Fatalf("E b2 pid %x", pid)
	}
	bad := prod
	bad.B = 1
	if bad.Validate() == nil {
		t.Fatal("M with B accepted")
	}
	bad = prod
	bad.Mode, bad.B = ModeF, 3
	if bad.Validate() == nil {
		t.Fatal("B = 3 accepted")
	}
	bad = prod
	bad.R, bad.K = 32, 16
	if bad.Validate() != nil {
		t.Fatal("R = K must be allowed")
	}
	bad.R = 16
	if bad.Validate() == nil {
		t.Fatal("R below K accepted")
	}
	bad = prod
	bad.Mode = 3
	if bad.Validate() == nil {
		t.Fatal("mode 3 accepted")
	}
	// the test schemas: refused by Validate, accepted by ValidateTestOnly, distinct pids
	ts := tp
	ts.Schema = TestSchema
	if ts.Validate() == nil || ts.ValidateTestOnly() != nil || ts.PID() == tp.PID() {
		t.Fatal("the TEST schema")
	}
	ts2 := ts
	ts2.Schema = TestSchema2
	if ts2.Validate() == nil || ts2.ValidateTestOnly() != nil || ts2.Semantics() != 2 || ts2.Variant() != "s2-test-m-b0-r32-k8-d1-p256-c0" {
		t.Fatalf("the TEST schema 2: %s", ts2.Variant())
	}
	s2 := prod
	s2.Schema = Schema2
	if s2.Validate() != nil || s2.Variant() != "s2-m-b0-r32-k8-d2048-p256-c32" || s2.PID() == prod.PID() {
		t.Fatal("schema 2")
	}
	if (Params{Schema: 6}).Semantics() != 0 || (Params{Schema: Schema5}).Semantics() != 5 || (Params{Schema: 3}).Semantics() != 3 || (Params{Schema: TestSchema3}).Semantics() != 3 || (Params{Schema: Schema4}).Semantics() != 4 || (Params{Schema: TestSchema4}).Semantics() != 4 {
		t.Fatal("schema semantics")
	}
	if (Input{}).Validate() == nil {
		t.Fatal("empty chain id accepted")
	}
	if (Input{ChainID: make([]byte, 65)}).Validate() == nil {
		t.Fatal("65-byte chain id accepted")
	}
	if (Input{ChainID: []byte{1}, HeaderBlob: make([]byte, 4097)}).Validate() == nil {
		t.Fatal("4097-byte header accepted")
	}
	if (Input{ChainID: []byte{1}, HeaderBlob: make([]byte, 4096)}).Validate() != nil {
		t.Fatal("4096-byte header refused")
	}
}

func TestOpcodes(t *testing.T) {
	x, y, z := uint32(0x01234567), uint32(0x89ABCDEF), uint32(0xFEDCBA98)
	cases := []struct {
		op   Op
		want uint32
	}{
		{Op{Opcode: 0, Rotate: 5}, x + rotl32(y^z, 5)},
		{Op{Opcode: 1, Rotate: 9}, x ^ rotl32(y+z, 9)},
		{Op{Opcode: 2, Rotate: 1}, x + y*(z|1)},
		{Op{Opcode: 3, Rotate: 31}, rotl32(x^y, 31) + z},
	}
	for _, c := range cases {
		if got := c.op.Apply(x, y, z); got != c.want {
			t.Fatalf("opcode %d: %x want %x", c.op.Opcode, got, c.want)
		}
	}
	if rotl32(1, 0) != 1 || rotl32(1, 32) != 1 || rotl32(1, 33) != 2 {
		t.Fatal("rotate masking")
	}
}

func TestScheduleRule(t *testing.T) {
	p := testParams(ModeE, 4)
	in := testInput(0)
	pid := p.PID()
	ps, ok := ProgramSeed(p, in, pid)
	if !ok {
		t.Fatal("E has a program seed")
	}
	if ps != H("RXE1/epoch", in.ChainID, pid[:], le64(3), in.EpochSeed[:]) {
		t.Fatal("E program seed")
	}
	pf := p
	pf.Mode = ModeF
	if fs, _ := ProgramSeed(pf, in, pf.PID()); fs != H("RXE1/fixed", pidOf(pf)) {
		t.Fatal("F program seed")
	}
	if _, ok := ProgramSeed(testParams(ModeM, 0), in, pid); ok {
		t.Fatal("M has no program seed")
	}
	sched := GenerateSchedule(p, ps)
	if len(sched) != 32 || len(sched[0]) != 4 || len(sched[0][0]) != 8 {
		t.Fatal("schedule shape")
	}
	// every descriptor follows the rule and writes its own destination once
	for j := range sched {
		for b := range sched[j] {
			seen := map[uint32]bool{}
			for d, op := range sched[j][b] {
				q := X("RXE1/op", 4, ps[:], le32(uint32(j)), le32(uint32(b)), le32(uint32(d)))
				k := p.K
				if op.Dst != uint32(d) || op.Src1 != (uint32(d)+1+uint32(q[0])%(k-1))%k || op.Src2 != (uint32(d)+1+uint32(q[1])%(k-1))%k ||
					op.Rotate != 1+uint32(q[2])%31 || op.Opcode != (uint32(d)+uint32(q[3])%4)%4 {
					t.Fatalf("descriptor j %d b %d d %d", j, b, d)
				}
				if op.Src1 == op.Dst || op.Src2 == op.Dst || op.Rotate == 0 || op.Rotate > 31 || op.Opcode > 3 || seen[op.Dst] {
					t.Fatalf("descriptor range j %d b %d d %d: %+v", j, b, d, op)
				}
				seen[op.Dst] = true
			}
		}
	}
	// schema 2: src2 never equals src1 or d, and the other fields are schema 1's
	p2 := p
	p2.Schema = TestSchema2
	ps2, _ := ProgramSeed(p2, in, p2.PID())
	s2 := GenerateSchedule(p2, ps2)
	p1 := p2
	p1.Schema = TestSchema
	s1 := GenerateSchedule(p1, ps2) // the same program seed, to compare the rule alone
	for j := range s2 {
		for b := range s2[j] {
			for d, op := range s2[j][b] {
				if op.Src2 == op.Src1 || op.Src2 == uint32(d) || op.Src1 == uint32(d) {
					t.Fatalf("schema 2 j %d b %d d %d: %+v", j, b, d, op)
				}
				o1 := s1[j][b][d]
				if op.Src1 != o1.Src1 || op.Rotate != o1.Rotate || op.Opcode != o1.Opcode {
					t.Fatal("schema 2 changed a field other than src2")
				}
				k := p.K
				q := X("RXE1/op", 4, ps2[:], le32(uint32(j)), le32(uint32(b)), le32(uint32(d)))
				r1 := (op.Src1 + k - uint32(d) - 1) % k
				tt := uint32(q[1]) % (k - 2)
				if tt >= r1 {
					tt++
				}
				if op.Src2 != (uint32(d)+1+tt)%k {
					t.Fatal("schema 2 src2 rule")
				}
			}
		}
	}
	// the header and nonce never select the schedule
	in2 := testInput(99)
	in2.HeaderBlob = []byte("other")
	if ps2, _ := ProgramSeed(p, in2, pid); ps2 != ps {
		t.Fatal("header or nonce selected the schedule")
	}
	// snapshot semantics: a block reads T, not the registers it has written
	s := newState(Lanes, 8)
	for l := range s {
		for r := range s[l] {
			s[l][r] = uint32(l*8 + r + 1)
		}
	}
	block := []Op{{Dst: 0, Src1: 1, Src2: 2, Rotate: 3, Opcode: 0}, {Dst: 1, Src1: 0, Src2: 2, Rotate: 3, Opcode: 0}}
	before := s.clone()
	executeBlock(s, block)
	if s[5][1] != block[1].Apply(before[5][1], before[5][0], before[5][2]) {
		t.Fatal("the second descriptor read the first's write")
	}
}

func pidOf(p Params) []byte { h := p.PID(); return h[:] }

func TestLoopBindingAndDeterminism(t *testing.T) {
	for _, mode := range []struct {
		m Mode
		b uint32
	}{{ModeM, 0}, {ModeF, 1}, {ModeE, 2}} {
		p := testParams(mode.m, mode.b)
		in := testInput(1)
		light := NewLight(p, in.EpochSeed)
		full := NewFull(light)
		pid := p.PID()
		var sched Schedule
		if ps, ok := ProgramSeed(p, in, pid); ok {
			sched = GenerateSchedule(p, ps)
		}
		a, err := Compute(p, in, full, sched, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := Compute(p, in, light, sched, nil, true)
		if a.Out != b.Out || TraceDigest(a) != TraceDigest(b) {
			t.Fatalf("%s: full and light differ", mode.m)
		}
		if len(a.Trace.Rounds) != 32 || len(a.Trace.Rounds[0].AfterBlocks) != int(mode.b) {
			t.Fatalf("%s: trace shape", mode.m)
		}
		// every input binds
		for name, mut := range map[string]func(*Input){
			"nonce":   func(i *Input) { i.Nonce++ },
			"header":  func(i *Input) { i.HeaderBlob = append(i.HeaderBlob, 0) },
			"chain":   func(i *Input) { i.ChainID = []byte("rxe1-tesu") },
			"epochNo": func(i *Input) { i.EpochNumber++ },
		} {
			in2 := testInput(1)
			mut(&in2)
			c, err := Compute(p, in2, full, sched, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if c.Out == a.Out || c.Seed == a.Seed {
				t.Fatalf("%s: %s does not bind", mode.m, name)
			}
		}
		// the parameter record binds through pid
		q := p
		q.R = 64
		if q.PID() == pid {
			t.Fatal("pid does not bind the record")
		}
		// the item source must match N
		if _, err := Compute(p, in, &Full{n: 16}, sched, nil, false); err == nil {
			t.Fatal("mismatched item source accepted")
		}
		// a schedule is required when B > 0
		if mode.b > 0 {
			if _, err := Compute(p, in, full, nil, nil, false); err == nil {
				t.Fatal("missing schedule accepted")
			}
		}
	}
	// M, F and E differ on the same inputs (the instruction stage has an effect)
	in := testInput(1)
	pm := testParams(ModeM, 0)
	pf := testParams(ModeF, 1)
	lm := NewFull(NewLight(pm, in.EpochSeed))
	rm, _ := Compute(pm, in, lm, nil, nil, false)
	rf, _ := Compute(pf, in, lm, GenerateSchedule(pf, mustPS(pf, in)), nil, false)
	if rm.Out == rf.Out {
		t.Fatal("M and F agree")
	}
	// the dataset depends only on its parameters and the epoch seed, never on the mode (section 13)
	lf := NewFull(NewLight(pf, in.EpochSeed))
	if lf.Digest() != lm.Digest() {
		t.Fatal("dataset bytes differ between modes")
	}
}

func mustPS(p Params, in Input) [32]byte {
	ps, _ := ProgramSeed(p, in, p.PID())
	return ps
}

func TestAccept(t *testing.T) {
	var out, tgt [32]byte
	if !Accept(out, tgt) {
		t.Fatal("equal must accept")
	}
	out[31] = 1 // the most significant byte of a little-endian integer
	if Accept(out, tgt) {
		t.Fatal("larger high byte accepted")
	}
	tgt[31] = 1
	out[0] = 0xff // 2^248 + 255 against 2^248: above
	if Accept(out, tgt) {
		t.Fatal("out above target accepted (the low byte must count)")
	}
	tgt[0] = 0xff // equal
	if !Accept(out, tgt) {
		t.Fatal("equal must accept")
	}
	out[1] = 1
	tgt[0] = 0 // 2^248 + 256 + 255 against 2^248 + 0: above
	tgt[1] = 2 // ... against 2^248 + 512: below
	if !Accept(out, tgt) {
		t.Fatal("out below target refused")
	}
}

// Reference vectors (IH 04) under testdata/ref/*.json in this file's
// format, or the reference's own once its loader exists. Mandatory under
// IGNEUM_REQUIRE_FIXTURES=1, skipped otherwise.
func TestReferenceVectors(t *testing.T) {
	files, _ := filepath.Glob("testdata/ref/*.json")
	if len(files) == 0 {
		if os.Getenv("IGNEUM_REQUIRE_FIXTURES") != "" {
			t.Fatal("no RXE1 reference vectors under testdata/ref/")
		}
		t.Skip("no RXE1 reference vectors yet (the hash team's IH 04)")
	}
	for _, f := range files {
		vf, err := LoadVectors(f)
		if err != nil {
			t.Fatal(err)
		}
		_, divs, err := vf.Check(vf.Params.Validate() != nil)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, d := range divs {
			t.Errorf("%s: %s", f, d)
		}
	}
}

// TestReferenceTree runs the reference's own spec-form files under
// tests/rxe1/vectors/<variant>/ (known, trace, schedule, dataset): mandatory
// under IGNEUM_REQUIRE_FIXTURES=1. The dataset class materialises the 2 GiB
// datasets only under IGNEUM_FULL_DATASETS=1.
func TestReferenceTree(t *testing.T) {
	files, _ := filepath.Glob("../../../../tests/rxe1/vectors/*/*.json")
	if len(files) == 0 {
		if os.Getenv("IGNEUM_REQUIRE_FIXTURES") != "" {
			t.Fatal("no reference files under tests/rxe1/vectors/")
		}
		t.Skip("no reference files under tests/rxe1/vectors/")
	}
	for _, f := range files {
		divs, err := CheckReferenceFile(f, os.Getenv("IGNEUM_FULL_DATASETS") != "")
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, d := range divs {
			t.Errorf("%s: %s", f, d)
		}
	}
}

// The Go side's own vectors on the tiny test-only record, every mode, frozen.
func TestSelfVectors(t *testing.T) {
	for _, mode := range []struct {
		m Mode
		b uint32
	}{{ModeM, 0}, {ModeF, 1}, {ModeE, 4}} {
		p := testParams(mode.m, mode.b)
		path := filepath.Join("testdata", "self-"+p.Variant()+".json")
		vf, err := LoadVectors(path)
		if err != nil {
			if os.Getenv("IGNEUM_WRITE_VECTORS") == "" {
				t.Skipf("no self vectors at %s; run with IGNEUM_WRITE_VECTORS=1", path)
			}
			vf = &VectorFile{Algorithm: ID, Spec: "SPEC-TEXT-v0.1 sections 6 to 8", Params: p}
			es := testInput(0).EpochSeed
			for i, nonce := range []uint64{0, 1, 1<<32 - 1, 1 << 32, 1<<64 - 1, 7} {
				hb := "686561646572"
				if i == 5 {
					hb = ""
				}
				vf.Vectors = append(vf.Vectors, Vector{Name: p.Variant() + "-" + string(rune('a'+i)), ChainID: hex.EncodeToString([]byte("rxe1-test")),
					HeaderBlob: hb, EpochSeed: hex.EncodeToString(es[:]), EpochNumber: 3, Nonce: nonce, Target: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"})
				vf.Vectors[i].Expect.TraceDigest = "pending"
			}
			vf.KeepTraces = mode.m == ModeM
			results, _, err := vf.Check(true)
			if err != nil {
				t.Fatal(err)
			}
			vf.Fill(results, mode.m == ModeM) // the full trace on one file, the trace digest on the others
			raw, _ := jsonIndent(vf)
			os.MkdirAll("testdata", 0o755)
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Log("wrote " + path)
			continue
		}
		_, divs, err := vf.Check(true)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range divs {
			t.Errorf("%s", d)
		}
	}
}

func s3(mode Mode, b, k, g, route, chunks, payload uint32) Params {
	p := testParams(mode, b)
	p.Schema, p.K, p.Group, p.Route, p.Chunks, p.Payload = TestSchema3, k, g, route, chunks, payload
	return p
}

func run(t *testing.T, p Params, in Input, src ItemSource) Result {
	t.Helper()
	pid := p.PID()
	var sched Schedule
	if ps, ok := ProgramSeed(p, in, pid); ok {
		sched = GenerateSchedule(p, ps)
	}
	var routing Routing
	if rs, ok := RouteSeed(p, in, pid); ok {
		routing = GenerateRouting(p, rs)
	}
	res, err := Compute(p, in, src, sched, routing, true)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Schema 3 (SPEC-TEXT-v0.2-A2A3.md): the record, the constraints, the v0.1
// point's word-for-word reproduction of schema 2, and each lever's effect.
func TestSchemaThreeCells(t *testing.T) {
	in := testInput(5)
	base := s3(ModeF, 1, 8, 16, RouteFixed, 1, 128) // the v0.1 point
	if err := base.ValidateTestOnly(); err != nil {
		t.Fatal(err)
	}
	if len(base.Record()) != 88 || base.Variant() != "s3-test-f-b1-r32-k8-g16-u1-x1-y128-d1-p256-c0" {
		t.Fatalf("record %d bytes, variant %s", len(base.Record()), base.Variant())
	}
	// the record's four trailing fields in order
	rec := base.Record()
	if binary.LittleEndian.Uint32(rec[72:]) != 16 || binary.LittleEndian.Uint32(rec[76:]) != 1 || binary.LittleEndian.Uint32(rec[80:]) != 1 || binary.LittleEndian.Uint32(rec[84:]) != 128 {
		t.Fatal("schema 3 trailing fields")
	}
	src := NewFull(NewLight(base, in.EpochSeed))
	// the v0.1 point reproduces schema 2's loop word for word: the same program
	// seed gives the same intermediates (pid differs, so compare under one seed)
	two := base
	two.Schema, two.Group, two.Route, two.Chunks, two.Payload = TestSchema2, 0, 0, 0, 0
	// the two records differ in pid, so the seeds differ; the loop's identity at
	// the v0.1 point is checked by running both records through loopFromSeed
	// (the loop on a given seed and schedule) and comparing every intermediate
	seed := Seed(in, two.PID())
	sched := GenerateSchedule(two, mustPS(two, in))
	r2 := loopFromSeed(two, seed, src, sched, nil)
	r3same := loopFromSeed(base, seed, src, sched, nil)
	if r2.Out != r3same.Out || TraceDigest(r2) != TraceDigest(r3same) {
		t.Fatal("the schema 3 v0.1 point does not reproduce the schema 2 loop")
	}
	r3 := run(t, base, in, src)
	if len(r3.Trace.Rounds) != 32 || len(r3.Trace.Rounds[0].Words) != 32 || len(r3.Trace.Rounds[0].As) != 1 {
		t.Fatal("schema 3 trace shape at the v0.1 point")
	}
	// each lever changes the output; route 0 and 3 need no descriptors, route 2 does
	outs := map[string][32]byte{"base": r3.Out}
	for name, p := range map[string]Params{
		"local":   s3(ModeF, 1, 8, 16, RouteLocal, 1, 128),
		"epoch":   s3(ModeF, 1, 8, 16, RouteEpoch, 1, 128),
		"state":   s3(ModeF, 1, 8, 16, RouteState, 1, 128),
		"chunks2": s3(ModeF, 1, 8, 16, RouteFixed, 2, 128),
		"chunks4": s3(ModeF, 1, 8, 16, RouteFixed, 4, 256),
		"pay256":  s3(ModeF, 1, 8, 16, RouteFixed, 1, 256),
		"g8":      s3(ModeF, 1, 8, 8, RouteFixed, 1, 128),
		"g32":     s3(ModeF, 1, 8, 32, RouteFixed, 1, 128),
		"k4":      s3(ModeF, 1, 4, 16, RouteFixed, 1, 128),
		"k4g8":    s3(ModeF, 1, 4, 8, RouteState, 4, 256),
	} {
		if err := p.ValidateTestOnly(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		r := run(t, p, in, NewFull(NewLight(p, in.EpochSeed)))
		for other, o := range outs {
			if o == r.Out {
				t.Fatalf("%s and %s give the same out", name, other)
			}
		}
		outs[name] = r.Out
		if p.Chunks > 1 && len(r.Trace.Rounds[0].As) != int(p.Chunks) {
			t.Fatalf("%s: %d chunk addresses", name, len(r.Trace.Rounds[0].As))
		}
		if len(r.Trace.Rounds[0].Words) != int(p.Payload/4) || len(r.Trace.Init) != int(p.Group) {
			t.Fatalf("%s: trace shape", name)
		}
	}
	// route 2 requires its descriptors
	pe := s3(ModeF, 1, 8, 16, RouteEpoch, 1, 128)
	if _, err := Compute(pe, in, src, GenerateSchedule(pe, mustPS(pe, in)), nil, false); err == nil {
		t.Fatal("route 2 without routing accepted")
	}
	// routing descriptors: off never 0, in range, same for every nonce of the epoch
	rs, _ := RouteSeed(pe, in, pe.PID())
	rt := GenerateRouting(pe, rs)
	for j := range rt {
		for k, ro := range rt[j] {
			if ro.Off == 0 || ro.Off >= 16 || ro.Src >= 8 || ro.Rot == 0 || ro.Rot > 31 || ro.Op > 1 {
				t.Fatalf("routing j %d k %d: %+v", j, k, ro)
			}
		}
	}
	in2 := testInput(77)
	in2.HeaderBlob = []byte("x")
	if rs2, _ := RouteSeed(pe, in2, pe.PID()); rs2 != rs {
		t.Fatal("the routing seed depends on the header or nonce")
	}
	if _, ok := RouteSeed(base, in, base.PID()); ok {
		t.Fatal("route 1 has a routing seed")
	}
	// mulhi32
	if mulhi32(0xFFFFFFFF, 0xFFFFFFFF) != 0xFFFFFFFE || mulhi32(1<<16, 1<<16) != 1 || mulhi32(3, 5) != 0 {
		t.Fatal("mulhi32")
	}
	// constraints
	for name, p := range map[string]Params{
		"g12":      s3(ModeF, 1, 8, 12, RouteFixed, 1, 128),
		"route4":   s3(ModeF, 1, 8, 16, 4, 1, 128),
		"chunks3":  s3(ModeF, 1, 8, 16, RouteFixed, 3, 128),
		"pay64":    s3(ModeF, 1, 8, 16, RouteFixed, 1, 64),
		"chunk32":  s3(ModeF, 1, 8, 16, RouteFixed, 4, 128),
		"words<g":  s3(ModeF, 1, 8, 32, RouteFixed, 1, 64),
		"k4-s2":    func() Params { p := testParams(ModeF, 1); p.Schema, p.K = TestSchema2, 4; return p }(),
		"s2-group": func() Params { p := testParams(ModeF, 1); p.Schema, p.Group = TestSchema2, 16; return p }(),
	} {
		if p.ValidateTestOnly() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// a production schema 3 record
	prod := Params{DatasetSpecDigest: DatasetSpecDigest, DatasetBytes: 2 << 30, Parents: 256, CacheBytes: 32 << 20, R: 64, K: 8, B: 1, Mode: ModeF, Schema: Schema3, Group: 16, Route: RouteState, Chunks: 1, Payload: 256}
	if err := prod.Validate(); err != nil {
		t.Fatal(err)
	}
	if prod.Variant() != "s3-f-b1-r64-k8-g16-u3-x1-y256-d2048-p256-c32" {
		t.Fatal(prod.Variant())
	}
}

// TestModesPAndS covers the A2/A3 text's section 5 (mode P: a program per
// nonce) and section 9 (mode S: state-selected compiled blocks) from the
// text: validation, the record's encoding against the first-screen pid the
// hash lead published, the family's coverage, the schedule of executions,
// the straight-line semantics, and per-nonce programs under P.
func TestModesPAndS(t *testing.T) {
	s3 := Params{DatasetSpecDigest: DatasetSpecDigest, DatasetBytes: 2 << 30, Parents: 256, CacheBytes: 32 << 20, R: 64, K: 8, B: 1, Mode: ModeP, Schema: Schema3, Group: 16, Route: RouteState, Chunks: 1, Payload: 256}
	if err := s3.Validate(); err != nil {
		t.Fatal(err)
	}
	if s3.Variant() != "s3-p-b1-r64-k8-g16-u3-x1-y256-d2048-p256-c32" {
		t.Fatal(s3.Variant())
	}
	bad := s3
	bad.Schema = Schema2
	bad.Group, bad.Route, bad.Chunks, bad.Payload = 0, 0, 0, 0
	if bad.Validate() == nil {
		t.Fatal("mode P under schema 2 accepted")
	}
	s4 := Params{DatasetSpecDigest: DatasetSpecDigest, DatasetBytes: 2 << 30, Parents: 256, CacheBytes: 32 << 20, R: 64, K: 8, B: 1, Mode: ModeS, Schema: Schema4, Group: 16, Route: RouteLocal, Chunks: 1, Payload: 256, BlockLen: 8, Family: 32}
	if err := s4.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(s4.Record()) != 96 || s4.Variant() != "s4-s-b1-r64-k8-g16-u0-x1-y256-l8-n32-d2048-p256-c32" {
		t.Fatal(len(s4.Record()), s4.Variant())
	}
	// the first-screen configuration's pid as the hash team published it (the record's encoding)
	pid := s4.PID()
	if got := hex.EncodeToString(pid[:]); got != "8be16a26db804dfc0ba1248cd0f81b7e1e7e3363ff728e7546954a21f628d936" {
		t.Fatalf("s4 first-screen pid %s", got)
	}
	for _, f := range []func(p *Params){
		func(p *Params) { p.Mode = ModeF },
		func(p *Params) { p.Schema = Schema3 },
		func(p *Params) { p.BlockLen = 12 },
		func(p *Params) { p.Family = 24 },
		func(p *Params) { p.B, p.BlockLen = 2, 8 },
		func(p *Params) { p.K, p.BlockLen = 16, 8 },
	} {
		q := s4
		f(&q)
		if q.Validate() == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
	// the family: NB blocks of L; d = i mod K; opcode (i + b) mod 4; sources distinct from d and each other
	fam := GenerateFamily(s4, FamilySeed(pid))
	if len(fam) != 32 {
		t.Fatal(len(fam))
	}
	for b, blk := range fam {
		if len(blk) != 8 {
			t.Fatal(len(blk))
		}
		census := [4]int{}
		for i, op := range blk {
			census[op.Opcode]++
			if op.Dst != uint32(i)%8 || op.Opcode != (uint32(i)+uint32(b))%4 || op.Src1 == op.Dst || op.Src2 == op.Dst || op.Src1 == op.Src2 || op.Rotate < 1 || op.Rotate > 31 {
				t.Fatalf("block %d op %d: %+v", b, i, op)
			}
		}
		if census != [4]int{2, 2, 2, 2} {
			t.Fatalf("block %d census %v", b, census)
		}
	}
	// a small record: one execution per round at L = B * K, one every second round at L = 16, every fourth at 32
	small := Params{DatasetSpecDigest: DatasetSpecDigest, DatasetBytes: 1 << 20, Parents: 256, CacheBytes: 16 << 10, R: 32, K: 8, B: 1, Mode: ModeS, Schema: TestSchema4, Group: 16, Route: RouteLocal, Chunks: 1, Payload: 256, BlockLen: 8, Family: 16}
	if err := small.ValidateTestOnly(); err != nil {
		t.Fatal(err)
	}
	in := Input{ChainID: []byte("t"), EpochSeed: [32]byte{1}, EpochNumber: 0, HeaderBlob: []byte("h"), Nonce: 7}
	ds := NewFull(NewLight(small, in.EpochSeed))
	outs := map[[32]byte]bool{}
	for _, l := range []uint32{8, 16, 32} {
		q := small
		q.BlockLen = l
		res, err := Compute(q, in, ds, nil, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for j, rt := range res.Trace.Rounds {
			n += len(rt.Executions)
			if len(rt.Executions) != len(rt.AfterBlocks) {
				t.Fatal("executions and block states differ")
			}
			if want := (uint32(j)+1)%(l/8) == 0; (len(rt.Executions) == 1) != want {
				t.Fatalf("L %d round %d: %d executions", l, j, len(rt.Executions))
			}
			for _, x := range rt.Executions {
				if x.E != uint32(j+1)/(l/8)-1 || x.Sel >= 16 {
					t.Fatalf("L %d round %d: %+v", l, j, x)
				}
			}
		}
		if n != int(32*8/l) {
			t.Fatalf("L %d: %d executions", l, n)
		}
		if res.ProgramSeed != [32]byte{} {
			t.Fatal("mode S has no program seed")
		}
		outs[res.Out] = true
	}
	if len(outs) != 3 {
		t.Fatal("block length did not change out")
	}
	// straight-line semantics differ from the snapshot semantics on the same block
	st := InitialState([32]byte{9}, 16, 8)
	a, b := st.clone(), st.clone()
	executeSequential(a, fam[0])
	executeBlock(b, fam[0])
	if fmt.Sprint(a) == fmt.Sprint(b) {
		t.Fatal("sequential and snapshot execution agree on a block with chained dependencies")
	}
	// the selection reads register e mod K of every lane
	h1, _ := SelectBlock(st, 3, 8, 16)
	st2 := st.clone()
	st2[5][3] ^= 1
	h2, _ := SelectBlock(st2, 3, 8, 16)
	st3 := st.clone()
	st3[5][4] ^= 1
	h3, _ := SelectBlock(st3, 3, 8, 16)
	if h1 == h2 || h1 != h3 {
		t.Fatal("selection fold reads the wrong register")
	}
	// mode P: a program per nonce, from the seed; an epoch schedule is refused
	ps := small
	ps.Mode, ps.Schema, ps.BlockLen, ps.Family = ModeP, TestSchema3, 0, 0
	if err := ps.ValidateTestOnly(); err != nil {
		t.Fatal(err)
	}
	r1, err := Compute(ps, in, ds, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	in2 := in
	in2.Nonce++
	r2, err := Compute(ps, in2, ds, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if r1.ProgramSeed == r2.ProgramSeed || r1.ProgramSeed != NonceProgramSeed(r1.Seed) || r1.Out == r2.Out {
		t.Fatal("mode P program seeds")
	}
	if _, err := Compute(ps, in, ds, GenerateSchedule(ps, r1.ProgramSeed), nil, false); err == nil {
		t.Fatal("mode P accepted an epoch schedule")
	}
	// on one seed, mode P's loop is mode F's loop under that nonce's program (the record differs, so pids and seeds do)
	pf := ps
	pf.Mode = ModeF
	lf := loopFromSeed(pf, r1.Seed, ds, GenerateSchedule(pf, r1.ProgramSeed), nil)
	lp := loopFromSeed(ps, r1.Seed, ds, GenerateSchedule(ps, r1.ProgramSeed), nil)
	if lf.Out != lp.Out || lp.Out != r1.Out {
		t.Fatal("mode P's loop on a nonce is mode F's loop under that nonce's program")
	}
}

// TestSchemaFive covers section 11 from the text: the record's zero memory
// fields, the admitted modes, the tag, the four lane documents' pids as the
// hash lead published them, the register operand from the pre-injection
// snapshot, and the memory-free loop needing no item source.
func TestSchemaFive(t *testing.T) {
	lanes := map[string]string{
		"s5-e-b1-r32-k8-g16-u1-x1-y128":  "b10a5d22",
		"s5-e-b1-r64-k16-g32-u3-x1-y128": "0cd94e16",
		"s5-e-b1-r64-k16-g8-u0-x1-y128":  "ccbe8d8b",
		"s5-p-b1-r64-k8-g16-u1-x1-y128":  "066dce1c",
	}
	for tag, pid := range lanes {
		var mode Mode
		var b, r, k, g, u, x, y uint32
		var mc byte
		if _, err := fmt.Sscanf(tag, "s5-%c-b%d-r%d-k%d-g%d-u%d-x%d-y%d", &mc, &b, &r, &k, &g, &u, &x, &y); err != nil {
			t.Fatal(tag, err)
		}
		mode = map[byte]Mode{'f': ModeF, 'e': ModeE, 'p': ModeP}[mc]
		p := Params{Schema: Schema5, R: r, K: k, B: b, Mode: mode, Group: g, Route: u, Chunks: x, Payload: y}
		if err := p.Validate(); err != nil {
			t.Fatal(tag, err)
		}
		if p.Variant() != tag || len(p.Record()) != 88 {
			t.Fatal(p.Variant(), len(p.Record()))
		}
		if got := hex.EncodeToString(func() []byte { h := p.PID(); return h[:] }())[:8]; got != pid {
			t.Fatalf("%s: pid %s, the lane document says %s", tag, got, pid)
		}
		doc := fmt.Sprintf(`{"schema": 5, "mode": %d, "blocks": %d, "rounds": %d, "registers": %d, "dataset_bytes": 0, "group": %d, "route": %d, "chunks": %d, "payload_bytes": %d}`, mode, b, r, k, g, u, x, y)
		q, testOnly, err := ParamsFromDocument([]byte(doc))
		if err != nil || testOnly || q != p {
			t.Fatal(tag, q, testOnly, err)
		}
	}
	base := Params{Schema: Schema5, R: 32, K: 8, B: 1, Mode: ModeF, Group: 16, Route: RouteFixed, Chunks: 1, Payload: 128}
	for _, f := range []func(q *Params){
		func(q *Params) { q.DatasetBytes = 2 << 30 },
		func(q *Params) { q.Parents = 256 },
		func(q *Params) { q.CacheBytes = 32 << 20 },
		func(q *Params) { q.DatasetSpecDigest = DatasetSpecDigest },
		func(q *Params) { q.Mode, q.B = ModeM, 0 },
		func(q *Params) { q.Mode = ModeS },
	} {
		q := base
		f(&q)
		if q.Validate() == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
	for _, bad := range []string{
		`{"schema": 5, "mode": 1, "blocks": 1, "rounds": 32, "registers": 8}`,
		`{"schema": 5, "mode": 1, "blocks": 1, "rounds": 32, "registers": 8, "dataset_bytes": 2147483648}`,
		`{"schema": 5, "mode": 1, "blocks": 1, "rounds": 32, "registers": 8, "dataset_bytes": 0, "dataset_spec_digest": "` + hex.EncodeToString(DatasetSpecDigest[:]) + `"}`,
	} {
		if _, _, err := ParamsFromDocument([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	// the loop without an item source; the operand rule checked on the trace
	in := Input{ChainID: []byte("t"), EpochSeed: [32]byte{1}, EpochNumber: 0, HeaderBlob: []byte("h"), Nonce: 7}
	for _, p := range []Params{base, {Schema: Schema5, R: 64, K: 16, B: 1, Mode: ModeE, Group: 8, Route: RouteLocal, Chunks: 1, Payload: 128}, {Schema: Schema5, R: 64, K: 8, B: 1, Mode: ModeP, Group: 16, Route: RouteFixed, Chunks: 2, Payload: 256}} {
		pid := p.PID()
		var sched Schedule
		if ps, ok := ProgramSeed(p, in, pid); ok && p.Mode != ModeP {
			sched = GenerateSchedule(p, ps)
		}
		res, err := Compute(p, in, nil, sched, nil, true)
		if err != nil {
			t.Fatal(p.Variant(), err)
		}
		k, g, c := p.K, p.G(), p.C()
		wpl := p.Payload / 4 / g
		perChunk := p.Payload / 4 / c
		prev := res.Trace.Init
		for j, rt := range res.Trace.Rounds {
			if len(rt.Regions) != 0 || len(rt.As) != int(c) {
				t.Fatal("schema 5 trace carries regions or the wrong chunk count")
			}
			for q := uint32(0); q < p.Payload/4; q++ {
				ci := q / perChunk
				l, m := q/wpl, q%wpl
				want := prev[l][(uint32(j)+m+k/2)%k] ^ rt.As[ci]
				if rt.Words[q] != want {
					t.Fatalf("%s round %d word %d: %d, want %d from the snapshot", p.Variant(), j, q, rt.Words[q], want)
				}
			}
			prev = rt.AfterExchang
		}
		// a different nonce gives a different out; the digest folds a and no region (its form is the harness's)
		in2 := in
		in2.Nonce++
		res2, _ := Compute(p, in2, nil, sched, nil, false)
		if res2.Out == res.Out {
			t.Fatal("nonce does not reach out")
		}
	}
}

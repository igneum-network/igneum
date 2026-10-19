package forgeb1

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/bits"
	"testing"

	"lukechampine.com/blake3"
)

func small(graph uint32) Params {
	return Params{Schema: TestSchema, Graph: graph, MemoryBytes: 1 << 20, LabelBytes: 128, Challenges: 8, Batch: 64} // N = 8192
}

func TestParams(t *testing.T) {
	p := Params{Schema: Schema, Graph: GraphDRS, MemoryBytes: 64 << 20, LabelBytes: 128, Challenges: 32, Batch: 64}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Variant() != "b1-g0-m64-l128-q32-b64" || p.N() != 1<<19 || p.D() != 19 || len(p.Record()) != 28 {
		t.Fatal(p.Variant(), p.N(), p.D())
	}
	if hex.EncodeToString(p.Record()) != "01000000"+"00000000"+"0000000400000000"+"80000000"+"20000000"+"40000000" {
		t.Fatal(hex.EncodeToString(p.Record()))
	}
	pid := p.PID()
	if pid != blake3.Sum256(p.Record()) || ProgramID(pid) != blake3.Sum256(append([]byte("\x0aB1/programforge-b1/v1"), pid[:]...)) {
		t.Fatal("pid or program id preimage")
	}
	if p.ProofBytes() != 32+32*(8+256+3*19*32) {
		t.Fatal(p.ProofBytes())
	}
	for _, f := range []func(q *Params){
		func(q *Params) { q.Graph = 2 },
		func(q *Params) { q.LabelBytes = 64 },
		func(q *Params) { q.MemoryBytes = 96 << 20 },
		func(q *Params) { q.Challenges = 0 },
		func(q *Params) { q.Batch = 0 },
		func(q *Params) { q.Schema = TestSchema },
	} {
		q := p
		f(&q)
		if q.Validate() == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
	doc := `{"schema": 1, "graph": 0, "memory_bytes": 67108864, "label_bytes": 128, "challenges": 32, "batch": 64}`
	q, testOnly, err := ParamsFromDocument([]byte(doc))
	if err != nil || testOnly || q != p {
		t.Fatal(q, testOnly, err)
	}
	if _, _, err := ParamsFromDocument([]byte(`{"schema": 1, "graph": 0, "memory_bytes": 67108864, "label_bytes": 128, "challenges": 32, "batch": 64, "extra": 1}`)); err == nil {
		t.Fatal("unknown member accepted")
	}
	if _, _, err := ParamsFromDocument([]byte(`{"schema": 1, "graph": 0, "memory_bytes": 67108864, "label_bytes": 128, "challenges": 32}`)); err == nil {
		t.Fatal("missing batch accepted")
	}
	if q, err := ParamsFromTag("b1-g0-m64-l128-q32-b64"); err != nil || q != p {
		t.Fatal(q, err)
	}
	if small(0).ValidateTestOnly() != nil || (Params{Schema: TestSchema, Graph: 0, MemoryBytes: 1 << 19, LabelBytes: 128, Challenges: 8, Batch: 1}).ValidateTestOnly() == nil {
		t.Fatal("test-only bounds")
	}
}

func TestTopology(t *testing.T) {
	es := [32]byte{3}
	p0, p1 := small(GraphDRS), small(GraphBRG)
	n := p0.N()
	if Parent(p0, es, 1) != 0 {
		t.Fatal("node 1's parent is 0")
	}
	for v := uint64(2); v < n; v++ {
		r := Parent(p0, es, v)
		// a DRSample parent: 0 <= r <= v - 2 (rr >= 2), with rr <= 2^(floor(log2 u) + 1)
		u := v + 1
		if r > v-2 {
			t.Fatalf("v %d r %d", v, r)
		}
		if rr := u - (r + 1); r != 0 && (rr < 2 || rr > uint64(1)<<(uint64(bits.Len64(u)))) {
			t.Fatalf("v %d rr %d", v, rr)
		}
	}
	// DRS+BRG: the first half as DRSample, the second half the bit-reversed permutation of the first half
	seen := map[uint64]int{}
	for v := uint64(2); v < n; v++ {
		r := Parent(p1, es, v)
		if v < n/2 {
			if r != Parent(p0, es, v) {
				t.Fatalf("first half differs from DRSample at %d", v)
			}
			continue
		}
		if r >= n/2 {
			t.Fatalf("second-half parent %d outside the first half", r)
		}
		seen[r]++
	}
	if len(seen) != int(n/2) {
		t.Fatalf("reverse_m is not a permutation of the first half: %d distinct", len(seen))
	}
	if reverseBits(1, 12) != 1<<11 || reverseBits(0b1011, 4) != 0b1101 {
		t.Fatal("reverse bits")
	}
	// the topology depends on the epoch seed and v only (the same for every fill seed), and changes with the epoch seed
	if Parent(p0, [32]byte{4}, 5000) == Parent(p0, es, 5000) && Parent(p0, [32]byte{4}, 5001) == Parent(p0, es, 5001) && Parent(p0, [32]byte{4}, 5002) == Parent(p0, es, 5002) {
		t.Fatal("the topology ignores the epoch seed")
	}
}

func TestProveVerifyAndNegativeCases(t *testing.T) {
	for _, graph := range []uint32{GraphDRS, GraphBRG} {
		p := small(graph)
		es, fs, ts := [32]byte{1}, [32]byte{2}, [32]byte{3}
		l := Fill(p, es, fs)
		root := l.Commit()
		prog := ProgramID(p.PID())
		// the labels by their definitions
		if !bytes.Equal(l.Label(0), Label0(p, fs)) || !bytes.Equal(l.Label(1), Label1(p, fs, l.Label(0))) || !bytes.Equal(l.Label(700), LabelFrom(p, fs, 700, l.Label(699), l.Label(Parent(p, es, 700)))) {
			t.Fatal("labels")
		}
		// keyed: a different key changes X[0]
		if bytes.Equal(Label0(p, fs), Label0(p, [32]byte{9})) {
			t.Fatal("X[0] ignores the fill seed")
		}
		for v := uint64(0); v < p.N(); v += 331 {
			if !checkPath(p, root, v, Leaf(v, l.Label(v)), l.Path(v)) {
				t.Fatalf("path of %d", v)
			}
		}
		proof := l.Prove(ts)
		if len(proof) != p.ProofBytes() {
			t.Fatal(len(proof))
		}
		if r, err := Verify(p, es, fs, ts, prog, proof); err != nil || r != root {
			t.Fatal(err)
		}
		// the eleven negative cases of section 8
		cases := map[string]func() ([32]byte, [32]byte, [32]byte, [32]byte, []byte){
			"altered fill_seed":  func() ([32]byte, [32]byte, [32]byte, [32]byte, []byte) { return es, [32]byte{7}, ts, prog, proof },
			"altered epoch_seed": func() ([32]byte, [32]byte, [32]byte, [32]byte, []byte) { return [32]byte{7}, fs, ts, prog, proof },
			"wrong program id":   func() ([32]byte, [32]byte, [32]byte, [32]byte, []byte) { return es, fs, ts, [32]byte{7}, proof },
			"truncated proof":    func() ([32]byte, [32]byte, [32]byte, [32]byte, []byte) { return es, fs, ts, prog, proof[:len(proof)-1] },
			"extended proof": func() ([32]byte, [32]byte, [32]byte, [32]byte, []byte) {
				return es, fs, ts, prog, append(append([]byte(nil), proof...), 0)
			},
			"flipped root": func() ([32]byte, [32]byte, [32]byte, [32]byte, []byte) {
				b := flip(proof, 0)
				return es, fs, ts, prog, b
			},
			"flipped label byte": func() ([32]byte, [32]byte, [32]byte, [32]byte, []byte) {
				b := flip(proof, 32+8+3)
				return es, fs, ts, prog, b
			},
			"flipped path node": func() ([32]byte, [32]byte, [32]byte, [32]byte, []byte) {
				b := flip(proof, 32+8+2*128+5)
				return es, fs, ts, prog, b
			},
			"wrong index": func() ([32]byte, [32]byte, [32]byte, [32]byte, []byte) {
				b := flip(proof, 32)
				return es, fs, ts, prog, b
			},
			"another parameter set": func() ([32]byte, [32]byte, [32]byte, [32]byte, []byte) {
				q := p
				q.Challenges = 9
				return es, fs, ts, ProgramID(q.PID()), proof
			},
		}
		for name, c := range cases {
			e, f, tr, pg, pf := c()
			if _, err := Verify(p, e, f, tr, pg, pf); err == nil {
				t.Fatalf("graph %d: %s accepted", graph, name)
			}
		}
		// a non-zero unused field at an index below 2 (reading R-1 as settled): a 1,024-node record so the
		// search over 2,000 transcripts hits both indices with certainty
		tiny := Params{Schema: TestSchema, Graph: graph, MemoryBytes: 1 << 20, LabelBytes: 1024, Challenges: 8, Batch: 64}
		if err := tiny.ValidateTestOnly(); err != nil {
			t.Fatal(err)
		}
		tl := Fill(tiny, es, fs)
		tprog := ProgramID(tiny.PID())
		tper := 8 + 2*1024 + 3*int(tiny.D())*32
		hit := map[uint64]bool{}
		for s := 0; s < 2000 && len(hit) < 2; s++ {
			tr := [32]byte{byte(s), byte(s >> 8), 5}
			for k, i := range Challenges(tiny, tr) {
				if i >= 2 || hit[i] {
					continue
				}
				hit[i] = true
				pf := tl.Prove(tr)
				if _, err := Verify(tiny, es, fs, tr, tprog, pf); err != nil {
					t.Fatalf("honest proof with index %d refused: %v", i, err)
				}
				off := 32 + k*tper + 8 + 1024 + 1 // inside X[r(i)], unused at i < 2
				if _, err := Verify(tiny, es, fs, tr, tprog, flip(pf, off)); err == nil {
					t.Fatal("non-zero unused field accepted")
				}
				if i == 1 {
					// revision 2, section 6: the opened X[0] must be the fill seed's
					if _, err := Verify(tiny, es, fs, tr, tprog, flip(pf, off-1024)); err == nil {
						t.Fatal("an opened X[0] that is not the fill seed's accepted")
					}
				}
				break
			}
		}
		if len(hit) != 2 {
			t.Fatalf("graph %d: indices below 2 not both reached in 2,000 transcripts at N = %d, q = %d: %v", graph, tiny.N(), tiny.Challenges, hit)
		}
		// a repeated challenge repeats its entry: equal entries for equal indices
		for s := byte(0); s < 255; s++ {
			tr := [32]byte{s, 6}
			idx := Challenges(p, tr)
			dup := -1
			for a := range idx {
				for b := a + 1; b < len(idx); b++ {
					if idx[a] == idx[b] {
						dup = a*len(idx) + b
					}
				}
			}
			if dup < 0 {
				continue
			}
			a, b := dup/len(idx), dup%len(idx)
			pf := l.Prove(tr)
			per := 8 + 2*128 + 3*int(p.D())*32
			if !bytes.Equal(pf[32+a*per:32+(a+1)*per], pf[32+b*per:32+(b+1)*per]) {
				t.Fatal("repeated challenge entries differ")
			}
			if _, err := Verify(p, es, fs, tr, prog, flip(pf, 32+b*per)); err == nil {
				t.Fatal("repeated entry with a wrong index accepted")
			}
			break
		}
	}
}

func flip(b []byte, k int) []byte {
	out := append([]byte(nil), b...)
	out[k] ^= 1
	return out
}

func TestVectorsRoundTrip(t *testing.T) {
	for _, graph := range []uint32{GraphDRS, GraphBRG} {
		doc, _ := json.Marshal(small(graph))
		z := hex.EncodeToString(make([]byte, 32))
		f := hex.EncodeToString(bytes.Repeat([]byte{0xff}, 32))
		vf := &VectorFile{Params: doc, Vectors: []Vector{{EpochSeed: z, FillSeed: z, Transcript: z}, {EpochSeed: f, FillSeed: f, Transcript: f}, {EpochSeed: "01" + z[2:], FillSeed: "02" + z[2:], Transcript: "03" + z[2:]}}}
		if err := vf.Fill(16); err != nil {
			t.Fatal(err)
		}
		if len(vf.Vectors[0].Labels) != 16 || len(vf.Vectors[0].Parents) != 14 {
			t.Fatal("localiser sizes")
		}
		divs, err := vf.Check(nil)
		if err != nil || len(divs) != 0 {
			t.Fatal(divs, err)
		}
		bad := *vf
		bad.Vectors = append([]Vector(nil), vf.Vectors...)
		bad.Vectors[1].Parents = append([]uint64(nil), vf.Vectors[1].Parents...)
		bad.Vectors[1].Parents[3] = 99999
		divs, _ = bad.Check(nil)
		if len(divs) != 1 || divs[0].Stage != "parent r(5)" {
			t.Fatal(divs)
		}
		bad.Vectors[1] = vf.Vectors[1]
		bad.Vectors[2].Root = z
		divs, _ = bad.Check(nil)
		if len(divs) != 1 || divs[0].Stage != "root" {
			t.Fatal(divs)
		}
	}
}

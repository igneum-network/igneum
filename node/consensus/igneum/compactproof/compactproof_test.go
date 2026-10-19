package compactproof

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/bits"
	"os"
	"path/filepath"
	"testing"

	"lukechampine.com/blake3"
)

func small() Params {
	return Params{Schema: TestSchema, MemoryBytes: 32 * 1024, LabelBytes: 32, Challenges: 8} // T = 1024
}

func TestParams(t *testing.T) {
	p := Params{Schema: Schema, MemoryBytes: 64 << 20, LabelBytes: 32, Challenges: 70}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Variant() != "cp-m64-l32-c70" || p.T() != 1<<21 || p.LogT() != 21 || len(p.Record()) != 20 {
		t.Fatal(p.Variant(), p.T(), p.LogT())
	}
	if p.ProofBytes() != 32+70*(8+64+3*21*32) {
		t.Fatal(p.ProofBytes())
	}
	// the record's bytes: schema 1, 64 MiB, 32, 70, little-endian; the pid both sides gave; the program id's preimage
	if hex.EncodeToString(p.Record()) != "01000000"+"0000000400000000"+"20000000"+"46000000" {
		t.Fatal(hex.EncodeToString(p.Record()))
	}
	pid := p.PID()
	if hex.EncodeToString(pid[:]) != "77862f3476d73781bcf3badf2a127236590cf67f48dd3cf49ec45e0aefafc3c4" {
		t.Fatal(hex.EncodeToString(pid[:]))
	}
	if ProgramID(pid) != blake3.Sum256(append([]byte("\x0aCP/programcompactproof/v1"), pid[:]...)) {
		t.Fatal("program id preimage")
	}
	for _, f := range []func(q *Params){
		func(q *Params) { q.MemoryBytes = 96 << 20 },
		func(q *Params) { q.LabelBytes = 48 },
		func(q *Params) { q.Challenges = 0 },
		func(q *Params) { q.Schema = 2 },
		func(q *Params) { q.Schema = TestSchema },
	} {
		q := p
		f(&q)
		if q.Validate() == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
	if err := small().ValidateTestOnly(); err != nil {
		t.Fatal(err)
	}
	doc := `{"schema": 1, "memory_bytes": 67108864, "label_bytes": 32, "challenges": 70}`
	q, testOnly, err := ParamsFromDocument([]byte(doc))
	if err != nil || testOnly || q != p {
		t.Fatal(q, testOnly, err)
	}
	if q, err := ParamsFromTag("cp-m64-l32-c70"); err != nil || q != p {
		t.Fatal(q, err)
	}
}

func TestParentRule(t *testing.T) {
	pid := small().PID()
	if GetParent(pid, 2) != 1 {
		t.Fatal("vertex 2's parent is 1")
	}
	for v := uint64(3); v < 5000; v++ {
		u := GetParent(pid, v)
		logv := uint64(bits.Len64(v) - 1)
		// parent in [1, v - 2]: r >= 2 always, r <= g <= v, clamped at 0
		if u < 1 || u > v-2 {
			t.Fatalf("v %d parent %d", v, u)
		}
		// r = v - u lies in [2, 2^(floor(log2 v) + 1)] unless clamped
		if r := v - u; u != 1 && (r < 2 || r > (uint64(1)<<(logv+1))) {
			t.Fatalf("v %d r %d", v, r)
		}
		if Parent0(pid, v-1) != u-1 {
			t.Fatal("p(i) = GetParent(i + 1) - 1")
		}
	}
	// the draw is a function of pid and v alone
	other := Params{Schema: TestSchema, MemoryBytes: 64 * 1024, LabelBytes: 32, Challenges: 8}.PID()
	same := 0
	for v := uint64(3); v < 1000; v++ {
		if GetParent(pid, v) == GetParent(other, v) {
			same++
		}
	}
	if same == 997 {
		t.Fatal("parent draw ignores pid")
	}
}

func TestProveVerify(t *testing.T) {
	p := small()
	seed := [32]byte{1, 2, 3}
	l := Fill(p, seed)
	R := l.Commit()
	prog := ProgramID(l.PID)
	// labels: X[0], X[1] from the seed; X[i] from its two parents
	if hex.EncodeToString(l.Label(0)) != hex.EncodeToString(Label0(p, seed)) || hex.EncodeToString(l.Label(1)) != hex.EncodeToString(Label1(p, seed, l.Label(0))) {
		t.Fatal("X[0], X[1]")
	}
	i := uint64(500)
	if hex.EncodeToString(l.Label(i)) != hex.EncodeToString(LabelFrom(p, seed, i, l.Label(i-1), l.Label(Parent0(l.PID, i)))) {
		t.Fatal("X[i]")
	}
	// every leaf's path reaches the root
	for i := uint64(0); i < p.T(); i += 97 {
		if !checkPath(p, R, i, Leaf(i, l.Label(i)), l.Path(i)) {
			t.Fatalf("path of %d", i)
		}
	}
	proof, y := l.Prove()
	if len(proof) != p.ProofBytes() || hex.EncodeToString(proof[:32]) != hex.EncodeToString(R[:]) {
		t.Fatal(len(proof))
	}
	if err := Verify(p, seed, prog, y, proof); err != nil {
		t.Fatal(err)
	}
	// another program id is refused before anything else; another seed, y or any proof byte fails
	if err := Verify(p, seed, [32]byte{}, y, proof); err == nil {
		t.Fatal("wrong program id accepted")
	}
	if err := Verify(p, [32]byte{9}, prog, y, proof); err == nil {
		t.Fatal("wrong seed accepted")
	}
	y2 := y
	y2[0] ^= 1
	if err := Verify(p, seed, prog, y2, proof); err == nil {
		t.Fatal("wrong y accepted")
	}
	for k := 0; k < len(proof); k += 101 {
		bad := append([]byte(nil), proof...)
		bad[k] ^= 1
		if err := Verify(p, seed, prog, y, bad); err == nil {
			t.Fatalf("flipped byte %d (%s) accepted", k, proofField(p, k))
		}
	}
	if err := Verify(p, seed, prog, y, proof[:len(proof)-1]); err == nil {
		t.Fatal("truncated proof accepted")
	}
	// seeds whose chains visit index 0 or 1 exercise reading R-3; different seeds give different y
	hit := 0
	ys := map[[32]byte]bool{}
	for s := byte(0); s < 40; s++ {
		l := Fill(p, [32]byte{s, 7})
		idx, y := l.Chain()
		ys[y] = true
		for _, i := range idx {
			if i < 2 {
				hit++
				proof, y := l.Prove()
				if err := Verify(p, l.Seed, prog, y, proof); err != nil {
					t.Fatalf("seed %d visiting %d: %v", s, i, err)
				}
			}
		}
	}
	if len(ys) != 40 {
		t.Fatal("seeds collide on y")
	}
	if hit == 0 {
		t.Log("no chain visited index 0 or 1 in 40 seeds at T = 1024")
	}
}

func TestVectorsRoundTrip(t *testing.T) {
	doc, _ := json.Marshal(small())
	vf := &VectorFile{Params: doc, Vectors: []Vector{
		{Seed: hex.EncodeToString(make([]byte, 32))},
		{Seed: hex.EncodeToString(bytes.Repeat([]byte{0xff}, 32))},
		{Seed: "0102030000000000000000000000000000000000000000000000000000000000"},
	}}
	if err := vf.Fill(8); err != nil {
		t.Fatal(err)
	}
	divs, err := vf.Check(nil)
	if err != nil || len(divs) != 0 {
		t.Fatal(divs, err)
	}
	// a changed label, root, index, y and proof byte are each named
	bad := *vf
	bad.Vectors = append([]Vector(nil), vf.Vectors...)
	bad.Vectors[0].Labels = append([]string(nil), vf.Vectors[0].Labels...)
	bad.Vectors[0].Labels[3] = hex.EncodeToString(make([]byte, 32))
	divs, _ = bad.Check(nil)
	if len(divs) != 1 || divs[0].Stage != "label X[3]" {
		t.Fatal(divs)
	}
	bad.Vectors[0] = vf.Vectors[0]
	bad.Vectors[1].Y = hex.EncodeToString(make([]byte, 32))
	divs, _ = bad.Check(nil)
	if len(divs) != 1 || divs[0].Stage != "y" {
		t.Fatal(divs)
	}
	if os.Getenv("IGNEUM_WRITE_VECTORS") != "" {
		dir := filepath.Join("..", "..", "..", "..", "tests", "compactproof", "vectors", small().Variant())
		os.MkdirAll(dir, 0o755)
		b, _ := json.MarshalIndent(vf, "", " ")
		os.WriteFile(filepath.Join(dir, "self.json"), b, 0o644)
	}
}

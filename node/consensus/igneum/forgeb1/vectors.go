package forgeb1

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// VectorFile is the reference's known.json form (section 8) and this side's:
// the parameter document, pid and program_id at the head; per vector the
// triple (epoch_seed, fill_seed, transcript), r(v) for v in [2, 16), X[0..16),
// root, the challenge indices and the proof, hex without prefix.
type VectorFile struct {
	Candidate string          `json:"candidate,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	PID       string          `json:"pid,omitempty"`
	ProgramID string          `json:"program_id,omitempty"`
	Vectors   []Vector        `json:"vectors"`
}

type Vector struct {
	EpochSeed  string   `json:"epoch_seed"`
	FillSeed   string   `json:"fill_seed"`
	Transcript string   `json:"transcript"`
	Parents    []uint64 `json:"parents,omitempty"` // r(2 .. 16)
	Labels     []string `json:"labels,omitempty"`  // X[0 .. 16)
	Root       string   `json:"root"`
	Indices    []uint64 `json:"challenges,omitempty"` // the challenge indices i_0 .. i_{q-1} (the reference's key)
	Proof      string   `json:"proof"`
}

type refDocument struct {
	Schema      *uint32 `json:"schema"`
	Graph       *uint32 `json:"graph"`
	MemoryBytes *uint64 `json:"memory_bytes"`
	LabelBytes  *uint32 `json:"label_bytes"`
	Challenges  *uint32 `json:"challenges"`
	Batch       *uint32 `json:"batch"`
	TestOnly    *uint32 `json:"test_only"`
}

// ParamsFromDocument reads the document of section 1 (every member mandatory, unknown members refused).
func ParamsFromDocument(doc []byte) (Params, bool, error) {
	var d refDocument
	if err := json.Unmarshal(doc, &d); err != nil {
		return Params{}, false, fmt.Errorf("params document: %w", err)
	}
	var raw map[string]json.RawMessage
	json.Unmarshal(doc, &raw)
	for k := range raw {
		switch k {
		case "schema", "graph", "memory_bytes", "label_bytes", "challenges", "batch", "test_only":
		default:
			return Params{}, false, fmt.Errorf("%w: unknown member %q", ErrParams, k)
		}
	}
	for name, ok := range map[string]bool{"schema": d.Schema != nil, "graph": d.Graph != nil, "memory_bytes": d.MemoryBytes != nil, "label_bytes": d.LabelBytes != nil, "challenges": d.Challenges != nil, "batch": d.Batch != nil} {
		if !ok {
			return Params{}, false, fmt.Errorf("%w: document without %s", ErrParams, name)
		}
	}
	p := Params{Schema: *d.Schema, Graph: *d.Graph, MemoryBytes: *d.MemoryBytes, LabelBytes: *d.LabelBytes, Challenges: *d.Challenges, Batch: *d.Batch}
	testOnly := (d.TestOnly != nil && *d.TestOnly != 0) || p.Schema == TestSchema
	var err error
	if testOnly {
		err = p.ValidateTestOnly()
	} else {
		err = p.Validate()
	}
	return p, testOnly, err
}

// ParamsFromTag reads b1-g<GRAPH>-m<mib>-l<L>-q<q>-b<B> (a production record).
func ParamsFromTag(tag string) (Params, error) {
	var mib uint64
	var g, lb, q, b uint32
	if _, err := fmt.Sscanf(tag, "b1-g%d-m%d-l%d-q%d-b%d", &g, &mib, &lb, &q, &b); err != nil {
		return Params{}, fmt.Errorf("%w: tag %q", ErrParams, tag)
	}
	p := Params{Schema: Schema, Graph: g, MemoryBytes: mib << 20, LabelBytes: lb, Challenges: q, Batch: b}
	return p, p.Validate()
}

func hex32(name, s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return out, fmt.Errorf("%s: not 32 hex bytes", name)
	}
	copy(out[:], b)
	return out, nil
}

type Divergence struct {
	Vector string
	Stage  string
	Have   string
	Want   string
}

func (d Divergence) String() string {
	return fmt.Sprintf("%s: %s differs: have %s want %s", d.Vector, d.Stage, d.Have, d.Want)
}

func (v Vector) triple() (e, f, t [32]byte, err error) {
	if e, err = hex32("epoch_seed", v.EpochSeed); err != nil {
		return
	}
	if f, err = hex32("fill_seed", v.FillSeed); err != nil {
		return
	}
	t, err = hex32("transcript", v.Transcript)
	return
}

// Check recomputes every vector from its triple and returns the first
// divergence per vector: pid, program id, r(2..16), X[0..16), root, the
// indices, the proof bytes, the verification of both proofs.
func (vf *VectorFile) Check(params *Params) ([]Divergence, error) {
	var p Params
	if params != nil {
		p = *params
	} else {
		var err error
		if p, _, err = ParamsFromDocument(vf.Params); err != nil {
			return nil, err
		}
	}
	pid := p.PID()
	prog := ProgramID(pid)
	if vf.PID != "" && vf.PID != hex.EncodeToString(pid[:]) {
		return []Divergence{{"file", "pid", hex.EncodeToString(pid[:]), vf.PID}}, nil
	}
	if vf.ProgramID != "" && vf.ProgramID != hex.EncodeToString(prog[:]) {
		return []Divergence{{"file", "program id", hex.EncodeToString(prog[:]), vf.ProgramID}}, nil
	}
	var divs []Divergence
	for n, v := range vf.Vectors {
		name := fmt.Sprintf("%s-%02d", p.Variant(), n)
		es, fs, ts, err := v.triple()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		d := func(stage, have, want string) { divs = append(divs, Divergence{name, stage, have, want}) }
		if k := firstParentDiff(p, es, v.Parents); k >= 0 {
			d(fmt.Sprintf("parent r(%d)", k+2), fmt.Sprint(Parent(p, es, uint64(k+2))), fmt.Sprint(v.Parents[k]))
			continue
		}
		l := Fill(p, es, fs)
		if k := firstLabelDiff(l, v.Labels); k >= 0 {
			d(fmt.Sprintf("label X[%d]", k), hex.EncodeToString(l.Label(uint64(k))), v.Labels[k])
			continue
		}
		root := l.Commit()
		if v.Root != "" && v.Root != hex.EncodeToString(root[:]) {
			d("root", hex.EncodeToString(root[:]), v.Root)
			continue
		}
		idx := Challenges(p, ts)
		if k := firstIndexDiff(idx, v.Indices); k >= 0 {
			d(fmt.Sprintf("challenge index i_%d", k), fmt.Sprint(idx[k]), fmt.Sprint(v.Indices[k]))
			continue
		}
		proof := l.Prove(ts)
		if v.Proof != "" {
			want, err := hex.DecodeString(v.Proof)
			if err != nil {
				return nil, fmt.Errorf("%s: proof is not hex", name)
			}
			if k := firstByteDiff(proof, want); k >= 0 {
				d(fmt.Sprintf("proof byte %d (%s)", k, proofField(p, k)), fmt.Sprintf("%02x", at(proof, k)), fmt.Sprintf("%02x", at(want, k)))
				continue
			}
			if _, err := Verify(p, es, fs, ts, prog, want); err != nil {
				d("verification of the file's proof", err.Error(), "accepted")
				continue
			}
		}
		if _, err := Verify(p, es, fs, ts, prog, proof); err != nil {
			d("verification of this side's proof", err.Error(), "accepted")
		}
	}
	return divs, nil
}

func proofField(p Params, k int) string {
	if k < 32 {
		return "root"
	}
	k -= 32
	lb, pl := int(p.LabelBytes), 32*int(p.D())
	per := 8 + 2*lb + 3*pl
	j, o := k/per, k%per
	switch {
	case o < 8:
		return fmt.Sprintf("challenge %d index", j)
	case o < 8+lb:
		return fmt.Sprintf("challenge %d X[i-1]", j)
	case o < 8+2*lb:
		return fmt.Sprintf("challenge %d X[r(i)]", j)
	case o < 8+2*lb+pl:
		return fmt.Sprintf("challenge %d path(i-1) node %d", j, (o-8-2*lb)/32)
	case o < 8+2*lb+2*pl:
		return fmt.Sprintf("challenge %d path(r(i)) node %d", j, (o-8-2*lb-pl)/32)
	default:
		return fmt.Sprintf("challenge %d path(i) node %d", j, (o-8-2*lb-2*pl)/32)
	}
}

func at(b []byte, k int) int {
	if k < len(b) {
		return int(b[k])
	}
	return -1
}

func firstByteDiff(a, b []byte) int {
	for k := 0; k < max(len(a), len(b)); k++ {
		if k >= len(a) || k >= len(b) || a[k] != b[k] {
			return k
		}
	}
	return -1
}

func firstLabelDiff(l *Labels, want []string) int {
	for k, w := range want {
		if uint64(k) >= l.P.N() || hex.EncodeToString(l.Label(uint64(k))) != w {
			return k
		}
	}
	return -1
}

func firstParentDiff(p Params, es [32]byte, want []uint64) int {
	for k, w := range want {
		if Parent(p, es, uint64(k+2)) != w {
			return k
		}
	}
	return -1
}

func firstIndexDiff(have, want []uint64) int {
	for k, w := range want {
		if k >= len(have) || have[k] != w {
			return k
		}
	}
	return -1
}

// Fill writes this side's values into every vector from its triple.
func (vf *VectorFile) Fill(labels int) error {
	p, _, err := ParamsFromDocument(vf.Params)
	if err != nil {
		return err
	}
	pid := p.PID()
	prog := ProgramID(pid)
	vf.Candidate = ID
	vf.PID = hex.EncodeToString(pid[:])
	vf.ProgramID = hex.EncodeToString(prog[:])
	for n := range vf.Vectors {
		v := &vf.Vectors[n]
		es, fs, ts, err := v.triple()
		if err != nil {
			return err
		}
		l := Fill(p, es, fs)
		root := l.Commit()
		v.Root = hex.EncodeToString(root[:])
		v.Indices = Challenges(p, ts)
		v.Parents, v.Labels = nil, nil
		for k := 0; k < labels && uint64(k) < p.N(); k++ {
			v.Labels = append(v.Labels, hex.EncodeToString(l.Label(uint64(k))))
			if k >= 2 {
				v.Parents = append(v.Parents, Parent(p, es, uint64(k)))
			}
		}
		v.Proof = hex.EncodeToString(l.Prove(ts))
	}
	return nil
}

func ReadFile(path string) (*VectorFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var vf VectorFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &vf, nil
}

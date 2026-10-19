package compactproof

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// VectorFile is the reference's known.json form and this side's own: the
// parameter document, pid and program_id at the head; per vector seed, root,
// labels X[0..k) and parents p(2..k) (0-based), y, proof (R then the
// openings), all hex without prefix; this side adds the chain's indices.
type VectorFile struct {
	Candidate string          `json:"candidate,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	PID       string          `json:"pid,omitempty"`
	ProgramID string          `json:"program_id,omitempty"`
	Vectors   []Vector        `json:"vectors"`
}

type Vector struct {
	Seed    string   `json:"seed"`
	Root    string   `json:"root"`
	Labels  []string `json:"labels,omitempty"`
	Parents []uint64 `json:"parents,omitempty"`
	Indices []uint64 `json:"indices,omitempty"`
	Y       string   `json:"y"`
	Proof   string   `json:"proof"`
}

type refDocument struct {
	Schema      *uint32 `json:"schema"`
	MemoryBytes *uint64 `json:"memory_bytes"`
	LabelBytes  *uint32 `json:"label_bytes"`
	Challenges  *uint32 `json:"challenges"`
	TestOnly    *uint32 `json:"test_only"`
}

// ParamsFromDocument reads the parameter document.
func ParamsFromDocument(doc []byte) (Params, bool, error) {
	var d refDocument
	if err := json.Unmarshal(doc, &d); err != nil {
		return Params{}, false, fmt.Errorf("params document: %w", err)
	}
	if d.Schema == nil || d.MemoryBytes == nil {
		return Params{}, false, fmt.Errorf("%w: document without schema or memory_bytes", ErrParams)
	}
	p := Params{Schema: *d.Schema, MemoryBytes: *d.MemoryBytes, LabelBytes: 32, Challenges: 70}
	if d.LabelBytes != nil {
		p.LabelBytes = *d.LabelBytes
	}
	if d.Challenges != nil {
		p.Challenges = *d.Challenges
	}
	testOnly := (d.TestOnly != nil && *d.TestOnly != 0) || p.Schema == TestSchema
	var err error
	if testOnly {
		err = p.ValidateTestOnly()
	} else {
		err = p.Validate()
	}
	return p, testOnly, err
}

// ParamsFromTag reads cp-m<mib>-l<label_bytes>-c<L> (a production record).
func ParamsFromTag(tag string) (Params, error) {
	var mib uint64
	var lb, l uint32
	if _, err := fmt.Sscanf(tag, "cp-m%d-l%d-c%d", &mib, &lb, &l); err != nil {
		return Params{}, fmt.Errorf("%w: tag %q", ErrParams, tag)
	}
	p := Params{Schema: Schema, MemoryBytes: mib << 20, LabelBytes: lb, Challenges: l}
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

// Divergence is the first differing value of one vector.
type Divergence struct {
	Vector string
	Stage  string
	Have   string
	Want   string
}

func (d Divergence) String() string {
	return fmt.Sprintf("%s: %s differs: have %s want %s", d.Vector, d.Stage, d.Have, d.Want)
}

// Check recomputes every vector from its seed under p (the file's document
// when p is nil) and returns the first divergence per vector, in the order
// pid, program id, labels, parents, root, indices, y, proof bytes, then the
// light verification of the file's proof and of this side's.
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
		seed, err := hex32("seed", v.Seed)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		d := func(stage, have, want string) { divs = append(divs, Divergence{name, stage, have, want}) }
		l := Fill(p, seed)
		if k := firstLabelDiff(l, v.Labels); k >= 0 {
			d(fmt.Sprintf("label X[%d]", k), hex.EncodeToString(l.Label(uint64(k))), v.Labels[k])
			continue
		}
		if k := firstParentDiff(l, v.Parents); k >= 0 {
			d(fmt.Sprintf("parent p(%d)", k+2), fmt.Sprint(Parent0(pid, uint64(k+2))), fmt.Sprint(v.Parents[k]))
			continue
		}
		R := l.Commit()
		if v.Root != "" && v.Root != hex.EncodeToString(R[:]) {
			d("root", hex.EncodeToString(R[:]), v.Root)
			continue
		}
		proof, y := l.Prove()
		idx, _ := l.Chain()
		if k := firstIndexDiff(idx, v.Indices); k >= 0 {
			d(fmt.Sprintf("index i_%d", k+1), fmt.Sprint(idx[k]), fmt.Sprint(v.Indices[k]))
			continue
		}
		if v.Y != "" && v.Y != hex.EncodeToString(y[:]) {
			d("y", hex.EncodeToString(y[:]), v.Y)
			continue
		}
		if v.Proof != "" {
			want, err := hex.DecodeString(v.Proof)
			if err != nil {
				return nil, fmt.Errorf("%s: proof is not hex", name)
			}
			if k := firstByteDiff(proof, want); k >= 0 {
				d(fmt.Sprintf("proof byte %d (%s)", k, proofField(p, k)), fmt.Sprintf("%02x", at(proof, k)), fmt.Sprintf("%02x", at(want, k)))
				continue
			}
			if err := Verify(p, seed, prog, y, want); err != nil {
				d("light verification of the file's proof", err.Error(), "accepted")
				continue
			}
		}
		if err := Verify(p, seed, prog, y, proof); err != nil {
			d("light verification of this side's proof", err.Error(), "accepted")
		}
	}
	return divs, nil
}

// proofField names the field a proof byte offset falls in.
func proofField(p Params, k int) string {
	if k < 32 {
		return "root R"
	}
	k -= 32
	lb, pl := int(p.LabelBytes), 32*int(p.LogT())
	per := 8 + 2*lb + 3*pl
	j, o := k/per, k%per
	switch {
	case o < 8:
		return fmt.Sprintf("step %d index", j)
	case o < 8+lb:
		return fmt.Sprintf("step %d X[i-1]", j)
	case o < 8+2*lb:
		return fmt.Sprintf("step %d X[p(i)]", j)
	case o < 8+2*lb+pl:
		return fmt.Sprintf("step %d path(i-1) node %d", j, (o-8-2*lb)/32)
	case o < 8+2*lb+2*pl:
		return fmt.Sprintf("step %d path(p(i)) node %d", j, (o-8-2*lb-pl)/32)
	default:
		return fmt.Sprintf("step %d path(i) node %d", j, (o-8-2*lb-2*pl)/32)
	}
}

func at(b []byte, k int) int {
	if k < len(b) {
		return int(b[k])
	}
	return -1
}

func firstByteDiff(a, b []byte) int {
	n := max(len(a), len(b))
	for k := 0; k < n; k++ {
		if k >= len(a) || k >= len(b) || a[k] != b[k] {
			return k
		}
	}
	return -1
}

func firstLabelDiff(l *Labels, want []string) int {
	for k, w := range want {
		if uint64(k) >= l.P.T() || hex.EncodeToString(l.Label(uint64(k))) != w {
			return k
		}
	}
	return -1
}

func firstParentDiff(l *Labels, want []uint64) int {
	for k, w := range want {
		if Parent0(l.PID, uint64(k+2)) != w {
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

// Fill writes this side's values into every vector from its seed: root, the
// indices, the first `labels` labels and the parents of 2 .. labels, y, proof.
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
		seed, err := hex32("seed", v.Seed)
		if err != nil {
			return err
		}
		l := Fill(p, seed)
		R := l.Commit()
		proof, y := l.Prove()
		idx, _ := l.Chain()
		v.Root = hex.EncodeToString(R[:])
		v.Indices = idx
		v.Labels, v.Parents = nil, nil
		for k := 0; k < labels && uint64(k) < p.T(); k++ {
			v.Labels = append(v.Labels, hex.EncodeToString(l.Label(uint64(k))))
			if k >= 2 {
				v.Parents = append(v.Parents, Parent0(pid, uint64(k)))
			}
		}
		v.Y = hex.EncodeToString(y[:])
		v.Proof = hex.EncodeToString(proof)
	}
	return nil
}

// ReadFile loads a vector file.
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

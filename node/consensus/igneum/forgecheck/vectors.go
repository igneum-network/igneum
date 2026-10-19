package forgecheck

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// Vector is one tuple of the protocol team's vector form (pow/vectors/forge/f0/vectors.json)
// and of this side's own files: inputs, then x, seed, ticket, qualifies and
// the public digest for the given y and program id. Hex without prefix; the
// nonce as 8 big-endian bytes.
type Vector struct {
	Name      string `json:"name"`
	ChainID   uint64 `json:"chain_id"`
	Version   uint32 `json:"version"`
	ParentID  string `json:"parent_id"`
	Reward    string `json:"reward"`
	Contents  string `json:"contents"`
	Target    string `json:"target"`
	ParamsID  string `json:"params_id"`
	Nonce     string `json:"nonce"`
	Y         string `json:"y"`
	ProgramID string `json:"program_id"`
	X         string `json:"x"`
	Seed      string `json:"seed"`
	Ticket    string `json:"ticket"`
	Qualifies bool   `json:"qualifies"`
	Public    string `json:"public"`
}

type VectorFile struct {
	Relation string   `json:"relation"`
	Hash     string   `json:"hash"`
	XLen     int      `json:"x_len"`
	Domains  []string `json:"domains"`
	Vectors  []Vector `json:"vectors"`
	Rejected []string `json:"rejected_encodings"`
}

// Divergence is the first differing value of one vector.
type Divergence struct {
	Vector, Stage, Have, Want string
}

func (d Divergence) String() string {
	return fmt.Sprintf("%s: %s: have %s want %s", d.Vector, d.Stage, d.Have, d.Want)
}

func fixed(name, s string, n int, out []byte) error {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != n {
		return fmt.Errorf("%s: not %d hex bytes", name, n)
	}
	copy(out, b)
	return nil
}

// Fields decodes a vector's inputs.
func (v Vector) Fields() (Fields, error) {
	var f Fields
	f.ChainID, f.Version = v.ChainID, v.Version
	var nonce [8]byte
	for _, e := range []error{
		fixed("parent_id", v.ParentID, 32, f.ParentID[:]),
		fixed("reward", v.Reward, 20, f.Reward[:]),
		fixed("contents", v.Contents, 32, f.Contents[:]),
		fixed("target", v.Target, 32, f.Target[:]),
		fixed("params_id", v.ParamsID, 32, f.ParamsID[:]),
		fixed("nonce", v.Nonce, 8, nonce[:]),
	} {
		if e != nil {
			return f, e
		}
	}
	for _, b := range nonce {
		f.Nonce = f.Nonce<<8 | uint64(b)
	}
	return f, nil
}

// Check recomputes every vector from its inputs and returns the first
// divergence per vector: x (the first differing byte), the strict decode
// round trip, seed, ticket, qualifies, public.
func (vf *VectorFile) Check() ([]Divergence, error) {
	var divs []Divergence
	for n, v := range vf.Vectors {
		name := fmt.Sprintf("f0-%02d %s", n, v.Name)
		f, err := v.Fields()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		var y, pid [32]byte
		if err := fixed("y", v.Y, 32, y[:]); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := fixed("program_id", v.ProgramID, 32, pid[:]); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		x := f.Encode()
		d := func(stage, have, want string) { divs = append(divs, Divergence{name, stage, have, want}) }
		if v.X != "" {
			want, err := hex.DecodeString(v.X)
			if err != nil {
				return nil, fmt.Errorf("%s: x is not hex", name)
			}
			if k := firstDiff(x, want); k >= 0 {
				d(fmt.Sprintf("x byte %d (%s)", k, xField(k)), at(x, k), at(want, k))
				continue
			}
		}
		if g, err := Decode(x); err != nil || g != f {
			d("strict decode of x", fmt.Sprint(err), "the tuple")
			continue
		}
		seed := Seed(x)
		if v.Seed != "" && v.Seed != hex.EncodeToString(seed[:]) {
			d("seed", hex.EncodeToString(seed[:]), v.Seed)
			continue
		}
		t := Ticket(x, y)
		if v.Ticket != "" && v.Ticket != hex.EncodeToString(t[:]) {
			d("ticket", hex.EncodeToString(t[:]), v.Ticket)
			continue
		}
		if q := Qualifies(t, f.Target); q != v.Qualifies {
			d("qualifies", fmt.Sprint(q), fmt.Sprint(v.Qualifies))
			continue
		}
		pub := Public(pid, x, y)
		if v.Public != "" && v.Public != hex.EncodeToString(pub[:]) {
			d("public", hex.EncodeToString(pub[:]), v.Public)
		}
	}
	return divs, nil
}

// Fill writes this side's values into every vector's outputs.
func (vf *VectorFile) Fill() error {
	vf.Relation = "Igneum Forge F0 relation version 1, protocol B's independent implementation (docs/igneumhash/forge/F0-RELATION.md)"
	vf.Hash = "BLAKE3-256 (one-byte domain length, domain bytes, then the fields)"
	vf.XLen = XLen
	vf.Domains = []string{DomainX, DomainSeed, DomainTicket, DomainPublic}
	for n := range vf.Vectors {
		v := &vf.Vectors[n]
		f, err := v.Fields()
		if err != nil {
			return err
		}
		var y, pid [32]byte
		if err := fixed("y", v.Y, 32, y[:]); err != nil {
			return err
		}
		if err := fixed("program_id", v.ProgramID, 32, pid[:]); err != nil {
			return err
		}
		x := f.Encode()
		seed, t, pub := Seed(x), Ticket(x, y), Public(pid, x, y)
		v.X, v.Seed, v.Ticket = hex.EncodeToString(x), hex.EncodeToString(seed[:]), hex.EncodeToString(t[:])
		v.Qualifies = Qualifies(t, f.Target)
		v.Public = hex.EncodeToString(pub[:])
	}
	return nil
}

// xField names the field of x an offset falls in (section 2.2).
func xField(k int) string {
	switch {
	case k < 1:
		return "domain length"
	case k < 18:
		return "domain"
	case k < 26:
		return "chain id"
	case k < 30:
		return "relation version"
	case k < 62:
		return "parent id"
	case k < 63:
		return "reward length"
	case k < 83:
		return "reward destination"
	case k < 115:
		return "contents commitment"
	case k < 147:
		return "consensus target"
	case k < 179:
		return "parameter id"
	case k < 187:
		return "nonce"
	}
	return "beyond 187"
}

func at(b []byte, k int) string {
	if k < len(b) {
		return fmt.Sprintf("%02x", b[k])
	}
	return "(none)"
}

func firstDiff(a, b []byte) int {
	n := max(len(a), len(b))
	for k := 0; k < n; k++ {
		if k >= len(a) || k >= len(b) || a[k] != b[k] {
			return k
		}
	}
	return -1
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

func marshal(vf *VectorFile) ([]byte, error) { return json.MarshalIndent(vf, "", " ") }

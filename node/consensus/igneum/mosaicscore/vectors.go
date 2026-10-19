package mosaicscore

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
)

// VectorFile is the protocol team's form (pow/vectors/mosaic/vectors.json) and this
// side's own: decimal strings for every integer.
type VectorFile struct {
	Relation        string             `json:"relation"`
	NormScale       string             `json:"norm_scale"`
	ResearchWeights Weights            `json:"research_weights"`
	ScoreScale      json.Number        `json:"score_scale"`
	EpochLength     json.Number        `json:"epoch_length_screen"`
	Work            []WorkVector       `json:"work"`
	Normalise       []NormaliseVector  `json:"normalise"`
	Score           []ScoreVector      `json:"score"`
	ChainScore      []ChainScoreVector `json:"chain_score"`
	Compare         []CompareVector    `json:"compare"`
}

type WorkVector struct {
	Name       string `json:"name"`
	Difficulty string `json:"difficulty,omitempty"`
	Target     string `json:"target"`
	Work       string `json:"work"`
	Work256    string `json:"work_256_form"`
}

type NormaliseVector struct {
	Name       string `json:"name"`
	Work       string `json:"work"`
	RefWork    string `json:"ref_work"`
	Normalised string `json:"normalised"`
}

type ScoreVector struct {
	Name    string   `json:"name"`
	W       []string `json:"W"`
	Score   string   `json:"score"`
	Weights Weights  `json:"weights"`
}

type ChainScoreVector struct {
	Name       string     `json:"name"`
	Epochs     [][]string `json:"epochs"`
	ChainScore string     `json:"chain_score"`
	Weights    *Weights   `json:"weights,omitempty"`
}

type CompareVector struct {
	Name    string `json:"name"`
	AScore  string `json:"a_score"`
	AHeight uint64 `json:"a_height"`
	AHash   string `json:"a_hash"`
	BScore  string `json:"b_score"`
	BHeight uint64 `json:"b_height"`
	BHash   string `json:"b_hash"`
	Result  int    `json:"result"` // 1: a chosen; -1: b chosen
}

type Divergence struct{ Vector, Stage, Have, Want string }

func (d Divergence) String() string {
	return fmt.Sprintf("%s: %s: have %s want %s", d.Vector, d.Stage, d.Have, d.Want)
}

func dec(name, s string) (*big.Int, error) {
	v, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok {
		return nil, fmt.Errorf("%s: %q is not a decimal integer", name, s)
	}
	return v, nil
}

func hash32(name, s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 32 {
		return out, fmt.Errorf("%s: not 32 hex bytes", name)
	}
	copy(out[:], b)
	return out, nil
}

// Check recomputes every vector and returns the first divergence per vector.
func (vf *VectorFile) Check() ([]Divergence, error) {
	var divs []Divergence
	d := func(v, stage, have, want string) { divs = append(divs, Divergence{v, stage, have, want}) }
	if vf.NormScale != "" && vf.NormScale != two64.String() {
		d("file", "norm_scale", two64.String(), vf.NormScale)
	}
	if vf.ScoreScale != "" && vf.ScoreScale.String() != vf.ResearchWeights.Scale().String() {
		d("file", "score_scale", vf.ResearchWeights.Scale().String(), vf.ScoreScale.String())
	}
	for _, v := range vf.Work {
		name := "work: " + v.Name
		t, err := dec("target", v.Target)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if v.Difficulty != "" {
			dd, err := dec("difficulty", v.Difficulty)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			tt, err := Target(dd)
			if err != nil {
				d(name, "target from difficulty", err.Error(), v.Target)
				continue
			}
			if tt.Cmp(t) != 0 {
				d(name, "target from difficulty", tt.String(), v.Target)
				continue
			}
		}
		w, err := Work(t)
		if err != nil {
			d(name, "work", err.Error(), v.Work)
			continue
		}
		if w.String() != v.Work {
			d(name, "work", w.String(), v.Work)
			continue
		}
		w2, err := Work256(t)
		if err != nil {
			d(name, "work (256-bit form)", err.Error(), v.Work256)
			continue
		}
		if w2.String() != v.Work256 || w2.Cmp(w) != 0 {
			d(name, "work (256-bit form)", w2.String(), v.Work256)
		}
	}
	for _, v := range vf.Normalise {
		name := "normalise: " + v.Name
		w, err := dec("work", v.Work)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		ref, err := dec("ref_work", v.RefWork)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		n, err := Normalise(w, ref)
		if err != nil {
			d(name, "normalised", err.Error(), v.Normalised)
			continue
		}
		if n.String() != v.Normalised {
			d(name, "normalised", n.String(), v.Normalised)
		}
	}
	for _, v := range vf.Score {
		name := "score: " + v.Name
		lanes, err := decs(v.W)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		wts := v.Weights
		if wts == (Weights{}) {
			wts = vf.ResearchWeights
		}
		s, err := Score(wts, lanes)
		if err != nil {
			d(name, "score", err.Error(), v.Score)
			continue
		}
		if s.String() != v.Score {
			d(name, "score", s.String(), v.Score)
		}
	}
	for _, v := range vf.ChainScore {
		name := "chain score: " + v.Name
		var epochs [][]*big.Int
		for _, e := range v.Epochs {
			lanes, err := decs(e)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			epochs = append(epochs, lanes)
		}
		wts := vf.ResearchWeights
		if v.Weights != nil {
			wts = *v.Weights
		}
		s, err := ChainScore(wts, epochs)
		if err != nil {
			d(name, "chain score", err.Error(), v.ChainScore)
			continue
		}
		if s.String() != v.ChainScore {
			d(name, "chain score", s.String(), v.ChainScore)
		}
	}
	for _, v := range vf.Compare {
		name := "compare: " + v.Name
		as, err := dec("a_score", v.AScore)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		bs, err := dec("b_score", v.BScore)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		ah, err := hash32("a_hash", v.AHash)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		bh, err := hash32("b_hash", v.BHash)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		a, b := Tip{as, v.AHeight, ah}, Tip{bs, v.BHeight, bh}
		got := Compare(a, b)
		if got != v.Result {
			d(name, "result", fmt.Sprint(got), fmt.Sprint(v.Result))
			continue
		}
		if Compare(b, a) != -got {
			d(name, "antisymmetry", fmt.Sprint(Compare(b, a)), fmt.Sprint(-got))
		}
	}
	return divs, nil
}

func decs(ss []string) ([]*big.Int, error) {
	out := make([]*big.Int, len(ss))
	for i, s := range ss {
		v, err := dec(fmt.Sprintf("lane %d", i), s)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// Own writes this side's vectors in the same form.
func Own() *VectorFile {
	vf := &VectorFile{Relation: "Mosaic-VF Phase A consensus objects, protocol B's independent implementation (docs/igneumhash/mosaic/PHASE-A-CONSENSUS.md sections 4 and 5)",
		NormScale: two64.String(), ResearchWeights: Research, ScoreScale: json.Number(Research.Scale().String()), EpochLength: "100"}
	for _, c := range []struct {
		name string
		d    string
		t    string
	}{
		{"difficulty 1", "1", ""}, {"difficulty 3", "3", ""}, {"difficulty 1000", "1000", ""}, {"difficulty 2^32", "4294967296", ""},
		{"target 1", "", "1"}, {"target 2", "", "2"}, {"target 2^255 - 1", "", new(big.Int).Sub(new(big.Int).Lsh(one, 255), one).String()},
		{"target 2^256 - 1 (maximum)", "", max256.String()}, {"target 2^128", "", new(big.Int).Lsh(one, 128).String()},
	} {
		var t *big.Int
		if c.d != "" {
			dd, _ := dec("", c.d)
			t, _ = Target(dd)
		} else {
			t, _ = dec("", c.t)
		}
		w, _ := Work(t)
		w2, _ := Work256(t)
		vf.Work = append(vf.Work, WorkVector{c.name, c.d, t.String(), w.String(), w2.String()})
	}
	for _, c := range []struct {
		name, w, ref string
	}{{"at the reference", "12345", "12345"}, {"work 3, reference 7 (floor)", "3", "7"}, {"work 2^255, reference 1", new(big.Int).Lsh(one, 255).String(), "1"}, {"work 1, reference 2^255", "1", new(big.Int).Lsh(one, 255).String()}} {
		w, _ := dec("", c.w)
		r, _ := dec("", c.ref)
		n, _ := Normalise(w, r)
		vf.Normalise = append(vf.Normalise, NormaliseVector{c.name, c.w, c.ref, n.String()})
	}
	u := two64.String()
	for _, c := range []struct {
		name string
		w    []string
	}{{"five at one unit", []string{u, u, u, u, u}}, {"one lane empty", []string{u, u, u, u, "0"}}, {"one lane at 255 units, four at zero", []string{new(big.Int).Mul(two64, big.NewInt(255)).String(), "0", "0", "0", "0"}},
		{"two lanes, one at three", []string{u, new(big.Int).Mul(two64, big.NewInt(3)).String()}}, {"one lane", []string{"7"}}, {"zeros", []string{"0", "0", "0"}}} {
		lanes, _ := decs(c.w)
		s, _ := Score(Research, lanes)
		vf.Score = append(vf.Score, ScoreVector{c.name, c.w, s.String(), Research})
	}
	for _, c := range []struct {
		name   string
		epochs [][]string
	}{{"two balanced epochs", [][]string{{u, u, u}, {u, u, u}}}, {"an ancient one-lane epoch, then balanced", [][]string{{u, "0", "0"}, {u, u, u}, {u, u, u}}}, {"a partial epoch at zero", [][]string{{u, u, u}, {"0", "0", "0"}}}} {
		var ep [][]*big.Int
		for _, e := range c.epochs {
			l, _ := decs(e)
			ep = append(ep, l)
		}
		s, _ := ChainScore(Research, ep)
		vf.ChainScore = append(vf.ChainScore, ChainScoreVector{c.name, c.epochs, s.String(), nil})
	}
	z := "0x" + strings.Repeat("00", 32)
	ff := "0x" + strings.Repeat("00", 31) + "ff"
	for _, c := range []struct {
		name      string
		as        string
		ah        uint64
		ahash, bs string
		bh        uint64
		bhash     string
	}{{"greater score wins over height", "11", 1, ff, "10", 9, z}, {"equal score, greater height wins", "10", 9, ff, "10", 8, z}, {"equal score and height, lower hash wins", "10", 5, ff, "10", 5, z}, {"identical tips", "10", 5, z, "10", 5, z}} {
		as, _ := dec("", c.as)
		bs, _ := dec("", c.bs)
		ah, _ := hash32("", c.ahash)
		bh, _ := hash32("", c.bhash)
		vf.Compare = append(vf.Compare, CompareVector{c.name, c.as, c.ah, c.ahash, c.bs, c.bh, c.bhash, Compare(Tip{as, c.ah, ah}, Tip{bs, c.bh, bh})})
	}
	return vf
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

package selectioncheck

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
)

// Vector is one case of the protocol team's pow/vectors/selection/vectors.json.
type Vector struct {
	Name      string   `json:"name"`
	Lookback  uint64   `json:"lookback"`
	Sustain   uint64   `json:"sustain"`
	GrowthNum uint64   `json:"growth_num"`
	GrowthDen uint64   `json:"growth_den"`
	Period    uint64   `json:"period"`
	Parents   []string `json:"parents_oldest_first"`
	Height    uint64   `json:"height"`
	Reference string   `json:"reference_median"`
	Signal    bool     `json:"signal_fires"`
	Floor     bool     `json:"floor_fires"`
	Opens     bool     `json:"opens"`
}

type VectorFile struct {
	Rule     string          `json:"rule"`
	Research json.RawMessage `json:"research"`
	Vectors  []Vector        `json:"vectors"`
}

type Divergence struct{ Vector, Stage, Have, Want string }

func (d Divergence) String() string {
	return fmt.Sprintf("%s: %s: have %s want %s", d.Vector, d.Stage, d.Have, d.Want)
}

func (vf *VectorFile) Check() ([]Divergence, error) {
	var divs []Divergence
	d := func(v, stage string, have, want any) {
		divs = append(divs, Divergence{v, stage, fmt.Sprint(have), fmt.Sprint(want)})
	}
	for _, v := range vf.Vectors {
		p := Params{v.Lookback, v.Sustain, v.GrowthNum, v.GrowthDen, v.Period}
		parents := make([]*big.Int, len(v.Parents))
		for i, s := range v.Parents {
			x, ok := new(big.Int).SetString(s, 10)
			if !ok {
				return nil, fmt.Errorf("%s: parent %d", v.Name, i)
			}
			parents[i] = x
		}
		r, ok := p.Reference(parents, v.Height)
		ref := ""
		if ok {
			ref = r.String()
		}
		switch {
		case ref != v.Reference:
			d(v.Name, "reference median", ref, v.Reference)
		case p.SignalFires(parents, v.Height) != v.Signal:
			d(v.Name, "signal fires", p.SignalFires(parents, v.Height), v.Signal)
		case p.FloorFires(v.Height) != v.Floor:
			d(v.Name, "floor fires", p.FloorFires(v.Height), v.Floor)
		case p.Opens(parents, v.Height) != v.Opens:
			d(v.Name, "opens", p.Opens(parents, v.Height), v.Opens)
		}
	}
	return divs, nil
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

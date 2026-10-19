package recoverycheck

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
)

type Vector struct {
	Name        string   `json:"name"`
	Parents     []string `json:"parents"`
	Difficulty  string   `json:"difficulty"`
	WorkD       string   `json:"work_D"`
	Reference   string   `json:"reference_difficulty"`
	DRef        string   `json:"reference_work_D_REF"`
	FractionNum string   `json:"fraction_num"`
	FractionDen string   `json:"fraction_den"`
	Subsidy     string   `json:"subsidy"`
	Paid        string   `json:"paid"`
	Unpaid      string   `json:"unpaid"`
}

type VectorFile struct {
	Band    string   `json:"band"`
	Window  int      `json:"window"`
	Rule    string   `json:"rule"`
	Vectors []Vector `json:"vectors"`
}

type Divergence struct{ Vector, Stage, Have, Want string }

func (d Divergence) String() string {
	return fmt.Sprintf("%s: %s: have %s want %s", d.Vector, d.Stage, d.Have, d.Want)
}

func dec(s string) (*big.Int, bool) { return new(big.Int).SetString(s, 10) }

func (vf *VectorFile) Check() ([]Divergence, error) {
	var divs []Divergence
	if vf.Window != 0 && vf.Window != Window {
		divs = append(divs, Divergence{"file", "window", fmt.Sprint(Window), fmt.Sprint(vf.Window)})
	}
	if vf.Band != "" && vf.Band != "7/8" {
		divs = append(divs, Divergence{"file", "band", "7/8", vf.Band})
	}
	for _, v := range vf.Vectors {
		parents := make([]*big.Int, len(v.Parents))
		for i, p := range v.Parents {
			d, ok := dec(p)
			if !ok {
				return nil, fmt.Errorf("%s: parent %d", v.Name, i)
			}
			parents[i] = d
		}
		diff, ok1 := dec(v.Difficulty)
		sub, ok2 := dec(v.Subsidy)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("%s: difficulty or subsidy", v.Name)
		}
		b := Evaluate(parents, diff, sub)
		d := func(stage string, have *big.Int, want string) bool {
			if want != "" && have.String() != want {
				divs = append(divs, Divergence{v.Name, stage, have.String(), want})
				return true
			}
			return false
		}
		switch {
		case d("work D", b.D, v.WorkD):
		case d("reference difficulty", b.Reference, v.Reference):
		case d("D_REF", b.DRef, v.DRef):
		case d("fraction num", b.Num, v.FractionNum):
		case d("fraction den", b.Den, v.FractionDen):
		case d("paid", b.Paid, v.Paid):
		case d("unpaid", b.Unpaid, v.Unpaid):
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

package mosaicv2

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
)

// The four vector files of pow/vectors/mosaic/gate0/ (section 6), read as
// the hash team's tools write them; every check replays this side's objects
// over the file's inputs and names the first differing field.

type Divergence struct{ Vector, Stage, Have, Want string }

func (d Divergence) String() string {
	return fmt.Sprintf("%s: %s: have %s want %s", d.Vector, d.Stage, d.Have, d.Want)
}

type scoreFile struct {
	Class   string   `json:"class"`
	Quotas  []uint64 `json:"quotas"`
	Vectors []struct {
		W   []uint64 `json:"W"`
		M   int      `json:"m"`
		Num string   `json:"num"`
		Den string   `json:"den"`
		SF  float64  `json:"S_f"`
	} `json:"vectors"`
	Compare []int `json:"compare"`
	Checks  struct {
		SQQ         json.RawMessage `json:"S_q_q"`
		Multiplier  json.RawMessage `json:"multiplier"`
		Outage      json.RawMessage `json:"outage"`
		Portfolio22 json.RawMessage `json:"portfolio_2_2"`
		Portfolio19 json.RawMessage `json:"portfolio_1_9"`
	} `json:"checks"`
}

type transitionFile struct {
	Window uint64 `json:"window"`
	Menu   []struct {
		Ratified   uint64   `json:"ratified"`
		Activation uint64   `json:"activation"`
		Doc        []uint64 `json:"doc"`
		Valid      bool     `json:"valid"`
	} `json:"menu"`
	Active []struct {
		H     uint64 `json:"h"`
		Entry int    `json:"entry"`
	} `json:"active"`
}

type reservoirFile struct {
	Params struct {
		Emission   uint64 `json:"EMISSION"`
		DRef       uint64 `json:"D_REF"`
		Stable     uint64 `json:"STABLE"`
		ReleaseCap uint64 `json:"RELEASE_CAP"`
		Timeout    uint64 `json:"TIMEOUT"`
	} `json:"params"`
	Quotas []uint64 `json:"quotas"`
	Blocks []struct {
		H            uint64      `json:"h"`
		Lane         int         `json:"lane"`
		Miner        uint64      `json:"miner"`
		D            uint64      `json:"D"`
		B            uint64      `json:"B"`
		Imm          uint64      `json:"imm"`
		Unpaid       uint64      `json:"unpaid"`
		Released     [][2]uint64 `json:"released"`
		Burned       [][2]uint64 `json:"burned"`
		BalanceAfter uint64      `json:"balance_after"`
	} `json:"blocks"`
}

type overflowFile struct {
	Params struct {
		ImmPct     uint64 `json:"IMM_PCT"`
		FeeCap     uint64 `json:"FEE_CAP"`
		FeeWindow  int    `json:"FEE_WINDOW"`
		FeeTimeout uint64 `json:"FEE_TIMEOUT"`
	} `json:"params"`
	Quotas []uint64 `json:"quotas"`
	Blocks []struct {
		H               uint64      `json:"h"`
		Lane            int         `json:"lane"`
		Assembler       uint64      `json:"assembler"`
		D               uint64      `json:"D"`
		F               uint64      `json:"F"`
		Imm             uint64      `json:"imm"`
		Dust            uint64      `json:"dust"`
		Shares          []uint64    `json:"shares"`
		Due             uint64      `json:"due"`
		Rel             uint64      `json:"rel"`
		Overflow        uint64      `json:"overflow"`
		FeeBalanceAfter uint64      `json:"fee_balance_after"`
		FeeBurned       [][2]uint64 `json:"fee_burned"`
		AssemblerTotal  uint64      `json:"assembler_total"`
	} `json:"blocks"`
}

func docOf(q []uint64) Document {
	if len(q) == 0 {
		return V02
	}
	return Document{Q: q}
}

// CheckFile reads one of the four files by its class and returns the divergences.
func CheckFile(path string) (class string, n int, divs []Divergence, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0, nil, err
	}
	var head struct {
		Class string `json:"class"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return "", 0, nil, fmt.Errorf("%s: %w", path, err)
	}
	d := func(v, stage, have, want string) { divs = append(divs, Divergence{v, stage, have, want}) }
	switch head.Class {
	case "score":
		var f scoreFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return head.Class, 0, nil, err
		}
		doc := docOf(f.Quotas)
		n = len(f.Vectors) + len(f.Compare)
		var nums, dens []*big.Int
		for i, v := range f.Vectors {
			name := fmt.Sprintf("score-%02d", i)
			m, num, den, err := Score(doc, v.W)
			if err != nil {
				d(name, "score", err.Error(), v.Num)
				continue
			}
			nums, dens = append(nums, num), append(dens, den)
			switch {
			case m != v.M:
				d(name, "minimum lane", fmt.Sprint(m), fmt.Sprint(v.M))
			case num.String() != v.Num:
				d(name, "num", num.String(), v.Num)
			case den.String() != v.Den:
				d(name, "den", den.String(), v.Den)
			case ScoreFloat(doc, v.W) != v.SF:
				d(name, "S_f", fmt.Sprintf("%.17g", ScoreFloat(doc, v.W)), fmt.Sprintf("%.17g", v.SF))
			}
		}
		for i, want := range f.Compare {
			if i+1 >= len(nums) {
				break
			}
			if got := CompareScores(nums[i], dens[i], nums[i+1], dens[i+1]); got != want {
				d(fmt.Sprintf("compare-%02d", i), "sign of score i against i+1", fmt.Sprint(got), fmt.Sprint(want))
			}
		}
	case "transition":
		var f transitionFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return head.Class, 0, nil, err
		}
		menu := make([]Entry, len(f.Menu))
		for i, e := range f.Menu {
			menu[i] = Entry{e.Ratified, e.Activation, Document{Q: e.Doc}}
		}
		valid := Validity(menu, f.Window)
		n = len(menu) + len(f.Active)
		for i, e := range f.Menu {
			if valid[i] != e.Valid {
				d(fmt.Sprintf("menu-%02d", i), "validity", fmt.Sprint(valid[i]), fmt.Sprint(e.Valid))
			}
		}
		for _, a := range f.Active {
			if got := Active(menu, valid, a.H); got != a.Entry {
				d(fmt.Sprintf("active-h%d", a.H), "active entry", fmt.Sprint(got), fmt.Sprint(a.Entry))
			}
		}
	case "reservoir":
		var f reservoirFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return head.Class, 0, nil, err
		}
		p := ReservoirParams{f.Params.Emission, f.Params.DRef, f.Params.Stable, f.Params.ReleaseCap, f.Params.Timeout}
		r, err := NewReservoir(p, docOf(f.Quotas))
		if err != nil {
			return head.Class, 0, nil, err
		}
		n = len(f.Blocks)
		for i, b := range f.Blocks {
			name := fmt.Sprintf("block-%03d (h %d)", i, b.H)
			rec, err := r.Block(b.H, b.Lane, b.Miner, b.D)
			if err != nil {
				d(name, "block", err.Error(), "a record")
				break
			}
			if dv := firstDiff(name, rec, b.B, b.Imm, b.Unpaid, b.Released, b.Burned, b.BalanceAfter); dv != nil {
				divs = append(divs, *dv)
				break
			}
		}
	case "overflow":
		var f overflowFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return head.Class, 0, nil, err
		}
		p := FeeParams{f.Params.ImmPct, f.Params.FeeCap, f.Params.FeeWindow, f.Params.FeeTimeout}
		r, err := NewFeeReservoir(p, docOf(f.Quotas))
		if err != nil {
			return head.Class, 0, nil, err
		}
		n = len(f.Blocks)
		for i, b := range f.Blocks {
			name := fmt.Sprintf("block-%03d (h %d)", i, b.H)
			rec, err := r.Block(b.H, b.Lane, b.Assembler, b.D, b.F)
			if err != nil {
				d(name, "block", err.Error(), "a record")
				break
			}
			var dv *Divergence
			switch {
			case rec.Imm != b.Imm:
				dv = &Divergence{name, "imm", fmt.Sprint(rec.Imm), fmt.Sprint(b.Imm)}
			case rec.Dust != b.Dust:
				dv = &Divergence{name, "dust", fmt.Sprint(rec.Dust), fmt.Sprint(b.Dust)}
			case fmt.Sprint(rec.Shares) != fmt.Sprint(b.Shares):
				dv = &Divergence{name, "shares", fmt.Sprint(rec.Shares), fmt.Sprint(b.Shares)}
			case rec.Due != b.Due:
				dv = &Divergence{name, "due", fmt.Sprint(rec.Due), fmt.Sprint(b.Due)}
			case rec.Rel != b.Rel:
				dv = &Divergence{name, "rel", fmt.Sprint(rec.Rel), fmt.Sprint(b.Rel)}
			case rec.Overflow != b.Overflow:
				dv = &Divergence{name, "overflow", fmt.Sprint(rec.Overflow), fmt.Sprint(b.Overflow)}
			case rec.FeeBalanceAfter != b.FeeBalanceAfter:
				dv = &Divergence{name, "fee_balance_after", fmt.Sprint(rec.FeeBalanceAfter), fmt.Sprint(b.FeeBalanceAfter)}
			case burnsStr(rec.FeeBurned) != fmt.Sprint(b.FeeBurned):
				dv = &Divergence{name, "fee_burned", burnsStr(rec.FeeBurned), fmt.Sprint(b.FeeBurned)}
			case rec.AssemblerTotal != b.AssemblerTotal:
				dv = &Divergence{name, "assembler_total", fmt.Sprint(rec.AssemblerTotal), fmt.Sprint(b.AssemblerTotal)}
			}
			if dv != nil {
				divs = append(divs, *dv)
				break
			}
		}
	default:
		return head.Class, 0, nil, fmt.Errorf("%s: unknown class %q", path, head.Class)
	}
	return head.Class, n, divs, nil
}

func paysStr(p []Pay) string {
	out := make([][2]uint64, len(p))
	for i, x := range p {
		out[i] = [2]uint64{x.Miner, x.Amount}
	}
	return fmt.Sprint(out)
}

func burnsStr(b []Burn) string {
	out := make([][2]uint64, len(b))
	for i, x := range b {
		out[i] = [2]uint64{uint64(x.Lane), x.Amount}
	}
	return fmt.Sprint(out)
}

func firstDiff(name string, rec BlockRecord, B, imm, unpaid uint64, released, burned [][2]uint64, after uint64) *Divergence {
	switch {
	case rec.B != B:
		return &Divergence{name, "B", fmt.Sprint(rec.B), fmt.Sprint(B)}
	case rec.Imm != imm:
		return &Divergence{name, "imm", fmt.Sprint(rec.Imm), fmt.Sprint(imm)}
	case rec.Unpaid != unpaid:
		return &Divergence{name, "unpaid", fmt.Sprint(rec.Unpaid), fmt.Sprint(unpaid)}
	case paysStr(rec.Released) != fmt.Sprint(released):
		return &Divergence{name, "released", paysStr(rec.Released), fmt.Sprint(released)}
	case burnsStr(rec.Burned) != fmt.Sprint(burned):
		return &Divergence{name, "burned", burnsStr(rec.Burned), fmt.Sprint(burned)}
	case rec.BalanceAfter != after:
		return &Divergence{name, "balance_after", fmt.Sprint(rec.BalanceAfter), fmt.Sprint(after)}
	}
	return nil
}

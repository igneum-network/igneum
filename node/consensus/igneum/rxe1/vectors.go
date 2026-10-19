// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package rxe1

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// The vector file of this implementation. Inputs are mandatory; every
// expectation is optional and compared when present. Hex has no prefix.
// The reference's own format (IH 04) gets a loader when it lands; the
// differential tool reports the first diverging intermediate either way.
type VectorFile struct {
	// KeepTraces keeps every computed trace in the results (for Fill with
	// the full trace); off, each trace is digested and dropped per nonce so a
	// 10,000-nonce campaign stays small.
	KeepTraces bool `json:"-"`

	Algorithm string   `json:"algorithm"`
	Spec      string   `json:"spec"`
	Generator string   `json:"generator,omitempty"`
	Params    Params   `json:"params"`
	Vectors   []Vector `json:"vectors"`
}

type Vector struct {
	Name        string `json:"name"`
	ChainID     string `json:"chainId"`    // hex bytes
	HeaderBlob  string `json:"headerBlob"` // hex bytes, may be empty
	EpochSeed   string `json:"epochSeed"`
	EpochNumber uint64 `json:"epochNumber"`
	Nonce       uint64 `json:"nonce"`
	Target      string `json:"target,omitempty"` // hex, 32 bytes, little-endian integer
	Valid       *bool  `json:"valid,omitempty"`
	Expect      Expect `json:"expect"`
}

type Expect struct {
	PID         string       `json:"pid,omitempty"`
	Seed        string       `json:"seed,omitempty"`
	ProgramSeed string       `json:"programSeed,omitempty"`
	Init        [][]uint32   `json:"init,omitempty"` // [lane][register]
	Rounds      []RoundExpec `json:"rounds,omitempty"`
	Out         string       `json:"out,omitempty"`
	TraceDigest string       `json:"traceDigest,omitempty"` // BLAKE3 over the full trace (the campaign form)
}

type RoundExpec struct {
	A           *uint32      `json:"a,omitempty"`
	Index       *uint64      `json:"index,omitempty"`
	As          []uint32     `json:"as,omitempty"`      // schema 3: every chunk's a
	Regions     []uint64     `json:"regions,omitempty"` // schema 3: every chunk's region
	Item        []uint32     `json:"item,omitempty"`    // the injected words
	AfterInject [][]uint32   `json:"afterInject,omitempty"`
	Executions  []ExecExpec  `json:"executions,omitempty"`  // mode S: before each afterBlocks entry
	AfterBlocks [][][]uint32 `json:"afterBlocks,omitempty"` // [block][lane][register]
	AfterExch   [][]uint32   `json:"afterExchange,omitempty"`
}

// ExecExpec is one expected mode S execution; h is optional in the reference's form.
type ExecExpec struct {
	E   uint32  `json:"e"`
	H   *uint32 `json:"h,omitempty"`
	Sel uint32  `json:"sel"`
}

func (v Vector) Input() (Input, error) {
	var in Input
	var err error
	if in.ChainID, err = hex.DecodeString(v.ChainID); err != nil {
		return in, fmt.Errorf("%s: chainId: %w", v.Name, err)
	}
	if in.HeaderBlob, err = hex.DecodeString(v.HeaderBlob); err != nil {
		return in, fmt.Errorf("%s: headerBlob: %w", v.Name, err)
	}
	es, err := hex.DecodeString(v.EpochSeed)
	if err != nil || len(es) != 32 {
		return in, fmt.Errorf("%s: epochSeed must be 32 bytes", v.Name)
	}
	copy(in.EpochSeed[:], es)
	in.EpochNumber, in.Nonce = v.EpochNumber, v.Nonce
	return in, nil
}

func LoadVectors(path string) (*VectorFile, error) {
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

// Divergence names the first intermediate at which the computed result
// differs from the expectation (round, block, lane, register where it applies).
type Divergence struct {
	Vector string
	Stage  string
	Have   string
	Want   string
}

func (d Divergence) String() string {
	return fmt.Sprintf("%s: %s differs: have %s want %s", d.Vector, d.Stage, d.Have, d.Want)
}

func stateOut(s State) [][]uint32 {
	out := make([][]uint32, len(s))
	for l := range s {
		out[l] = append([]uint32(nil), s[l]...)
	}
	return out
}

func cmpState(vector, stage string, have State, want [][]uint32) *Divergence {
	if want == nil {
		return nil
	}
	for l := range want {
		for r := range want[l] {
			if l >= len(have) || r >= len(have[l]) {
				return &Divergence{vector, fmt.Sprintf("%s lane %d register %d", stage, l, r), "(none)", fmt.Sprint(want[l][r])}
			}
			if have[l][r] != want[l][r] {
				return &Divergence{vector, fmt.Sprintf("%s lane %d register %d", stage, l, r), fmt.Sprint(have[l][r]), fmt.Sprint(want[l][r])}
			}
		}
	}
	return nil
}

func sliceDiff32(want, have []uint32) int {
	for k := range want {
		if k >= len(have) || have[k] != want[k] {
			return k
		}
	}
	return -1
}

func sliceDiff64(want, have []uint64) int {
	for k := range want {
		if k >= len(have) || have[k] != want[k] {
			return k
		}
	}
	return -1
}

func at32(s []uint32, k int) string {
	if k < len(s) {
		return fmt.Sprint(s[k])
	}
	return "(none)"
}

func at64(s []uint64, k int) string {
	if k < len(s) {
		return fmt.Sprint(s[k])
	}
	return "(none)"
}

func cmpHex(vector, stage string, have [32]byte, want string) *Divergence {
	if want == "" {
		return nil
	}
	h := hex.EncodeToString(have[:])
	if h != want {
		return &Divergence{vector, stage, h, want}
	}
	return nil
}

// TraceDigest is the campaign's compact form, as adopted by the reference
// (REFERENCE-NOTES.md): H("RXE1/trace-digest", [seed || initial state ||
// per round (LE32 a || LE64 index || state after injection || state after
// each block || state after the exchange) || out]), every state as
// lane-major LE32 words. The item words are in the trace class, not in the
// digest.
func TraceDigest(res Result) [32]byte {
	if res.Trace == nil {
		return [32]byte{}
	}
	buf := make([]byte, 0, 1<<16)
	buf = append(buf, res.Seed[:]...)
	buf = append(buf, res.Trace.Init.bytesLaneMajor()...)
	for _, rt := range res.Trace.Rounds {
		// per chunk LE32 a, LE64 region (one chunk at the v0.1 point: the adopted form; reading R-3 for more)
		for ci := range rt.As {
			buf = binary.LittleEndian.AppendUint32(buf, rt.As[ci])
			if ci < len(rt.Regions) {
				buf = binary.LittleEndian.AppendUint64(buf, rt.Regions[ci]) // schema 5 folds a and no region (section 11.2)
			}
		}
		buf = append(buf, rt.AfterInject.bytesLaneMajor()...)
		for bi, b := range rt.AfterBlocks {
			if bi < len(rt.Executions) {
				// mode S: LE32 e and LE32 sel before the state after the block (the harness convention)
				buf = binary.LittleEndian.AppendUint32(buf, rt.Executions[bi].E)
				buf = binary.LittleEndian.AppendUint32(buf, rt.Executions[bi].Sel)
			}
			buf = append(buf, b.bytesLaneMajor()...)
		}
		buf = append(buf, rt.AfterExchang.bytesLaneMajor()...)
	}
	buf = append(buf, res.Out[:]...)
	return H("RXE1/trace-digest", buf)
}

// Check computes every vector (item sources and schedules cached by epoch
// seed) and returns the results and the first divergence per vector.
// testOnly selects ValidateTestOnly for the small-dataset campaign.
func (vf *VectorFile) Check(testOnly bool) ([]Result, []Divergence, error) {
	p := vf.Params
	var err error
	if testOnly {
		err = p.ValidateTestOnly()
	} else {
		err = p.Validate()
	}
	if err != nil {
		return nil, nil, err
	}
	sources := map[[32]byte]ItemSource{}
	schedules := map[[32]byte]Schedule{}
	routings := map[[32]byte]Routing{}
	var results []Result
	var divs []Divergence
	for _, v := range vf.Vectors {
		in, err := v.Input()
		if err != nil {
			return nil, nil, err
		}
		src, ok := sources[in.EpochSeed]
		if !ok && p.MemoryFree() {
			src, ok = nil, true // section 11.3: the context builds nothing at dataset_bytes 0
		}
		if !ok {
			light := NewLight(p, in.EpochSeed)
			if p.DatasetBytes <= 64<<20 {
				src = NewFull(light)
			} else {
				src = light
			}
			sources[in.EpochSeed] = src
		}
		pid := p.PID()
		var sched Schedule
		if ps, ok := ProgramSeed(p, in, pid); ok && p.Mode != ModeP {
			key := ps
			if sched, ok = schedules[key]; !ok {
				sched = GenerateSchedule(p, ps)
				schedules[key] = sched
			}
		}
		var routing Routing
		if rs, ok := RouteSeed(p, in, pid); ok {
			if routing, ok = routings[rs]; !ok {
				routing = GenerateRouting(p, rs)
				routings[rs] = routing
			}
		}
		e := v.Expect
		withTrace := e.Init != nil || len(e.Rounds) > 0 || e.TraceDigest != ""
		res, err := Compute(p, in, src, sched, routing, withTrace)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", v.Name, err)
		}
		if res.Trace != nil {
			res.TraceDigest = TraceDigest(res)
		}
		keep := res
		if !vf.KeepTraces {
			keep.Trace = nil
		}
		results = append(results, keep)
		var d *Divergence
		switch {
		case cmpHex(v.Name, "pid", res.PID, e.PID) != nil:
			d = cmpHex(v.Name, "pid", res.PID, e.PID)
		case cmpHex(v.Name, "seed", res.Seed, e.Seed) != nil:
			d = cmpHex(v.Name, "seed", res.Seed, e.Seed)
		case cmpHex(v.Name, "programSeed", res.ProgramSeed, e.ProgramSeed) != nil:
			d = cmpHex(v.Name, "programSeed", res.ProgramSeed, e.ProgramSeed)
		case res.Trace != nil && cmpState(v.Name, "init", res.Trace.Init, e.Init) != nil:
			d = cmpState(v.Name, "init", res.Trace.Init, e.Init)
		}
		for j := 0; d == nil && j < len(e.Rounds); j++ {
			if j >= len(res.Trace.Rounds) {
				d = &Divergence{v.Name, fmt.Sprintf("round %d", j), "(none)", "a round"}
				break
			}
			rt, er := res.Trace.Rounds[j], e.Rounds[j]
			stage := fmt.Sprintf("round %d ", j)
			if p.MemoryFree() {
				// section 11.2: the chunk record is {"c", "a"}; a region or index the file carries is
				// the emitter's filler (DIFF-RXE1.md F-5) and no intermediate of the function
				er.Regions, er.Index = nil, nil
			}
			switch {
			case sliceDiff32(er.As, rt.As) >= 0:
				k := sliceDiff32(er.As, rt.As)
				d = &Divergence{v.Name, fmt.Sprintf("%schunk %d address word", stage, k), at32(rt.As, k), fmt.Sprint(er.As[k])}
			case sliceDiff64(er.Regions, rt.Regions) >= 0:
				k := sliceDiff64(er.Regions, rt.Regions)
				d = &Divergence{v.Name, fmt.Sprintf("%schunk %d region", stage, k), at64(rt.Regions, k), fmt.Sprint(er.Regions[k])}
			case er.A != nil && *er.A != rt.A:
				d = &Divergence{v.Name, stage + "address word", fmt.Sprint(rt.A), fmt.Sprint(*er.A)}
			case er.Index != nil && *er.Index != rt.Index:
				d = &Divergence{v.Name, stage + "index", fmt.Sprint(rt.Index), fmt.Sprint(*er.Index)}
			case sliceDiff32(er.Item, rt.Words) >= 0:
				k := sliceDiff32(er.Item, rt.Words)
				d = &Divergence{v.Name, fmt.Sprintf("%sinjected word %d", stage, k), at32(rt.Words, k), fmt.Sprint(er.Item[k])}
			case cmpState(v.Name, stage+"after injection", rt.AfterInject, er.AfterInject) != nil:
				d = cmpState(v.Name, stage+"after injection", rt.AfterInject, er.AfterInject)
			}
			for x := 0; d == nil && x < len(er.Executions); x++ {
				ee := er.Executions[x]
				switch {
				case x >= len(rt.Executions):
					d = &Divergence{v.Name, fmt.Sprintf("%sexecution %d", stage, x), "(none)", fmt.Sprintf("e %d sel %d", ee.E, ee.Sel)}
				case rt.Executions[x].E != ee.E:
					d = &Divergence{v.Name, fmt.Sprintf("%sexecution %d number", stage, x), fmt.Sprint(rt.Executions[x].E), fmt.Sprint(ee.E)}
				case ee.H != nil && rt.Executions[x].H != *ee.H:
					d = &Divergence{v.Name, fmt.Sprintf("%sexecution %d fold h", stage, x), fmt.Sprint(rt.Executions[x].H), fmt.Sprint(*ee.H)}
				case rt.Executions[x].Sel != ee.Sel:
					d = &Divergence{v.Name, fmt.Sprintf("%sexecution %d selected block", stage, x), fmt.Sprint(rt.Executions[x].Sel), fmt.Sprint(ee.Sel)}
				}
			}
			for b := 0; d == nil && b < len(er.AfterBlocks); b++ {
				if b >= len(rt.AfterBlocks) {
					d = &Divergence{v.Name, fmt.Sprintf("%sblock %d", stage, b), "(none)", "a block"}
					break
				}
				d = cmpState(v.Name, fmt.Sprintf("%safter block %d", stage, b), rt.AfterBlocks[b], er.AfterBlocks[b])
			}
			if d == nil {
				d = cmpState(v.Name, stage+"after exchange", rt.AfterExchang, er.AfterExch)
			}
		}
		if d == nil {
			d = cmpHex(v.Name, "out", res.Out, e.Out)
		}
		if d == nil && e.TraceDigest != "" && e.TraceDigest != "pending" {
			d = cmpHex(v.Name, "traceDigest", res.TraceDigest, e.TraceDigest)
		}
		if d == nil && v.Valid != nil && v.Target != "" {
			tb, err := hex.DecodeString(v.Target)
			if err != nil || len(tb) != 32 {
				return nil, nil, fmt.Errorf("%s: target must be 32 bytes", v.Name)
			}
			var tgt [32]byte
			copy(tgt[:], tb)
			if got := Accept(res.Out, tgt); got != *v.Valid {
				d = &Divergence{v.Name, "valid", fmt.Sprint(got), fmt.Sprint(*v.Valid)}
			}
		}
		if d != nil {
			divs = append(divs, *d)
		}
	}
	return results, divs, nil
}

// Fill writes this implementation's results into the expectations: the
// digests always, the full trace when full is set, else the trace digest.
func (vf *VectorFile) Fill(results []Result, full bool) {
	for i := range vf.Vectors {
		res := results[i]
		e := &vf.Vectors[i].Expect
		e.PID = hex.EncodeToString(res.PID[:])
		e.Seed = hex.EncodeToString(res.Seed[:])
		if vf.Params.Mode != ModeM {
			e.ProgramSeed = hex.EncodeToString(res.ProgramSeed[:])
		}
		e.Out = hex.EncodeToString(res.Out[:])
		if res.TraceDigest != [32]byte{} {
			e.TraceDigest = hex.EncodeToString(res.TraceDigest[:])
		}
		if res.Trace != nil {
			if full {
				e.Init = stateOut(res.Trace.Init)
				e.Rounds = e.Rounds[:0]
				for _, rt := range res.Trace.Rounds {
					a, idx := rt.A, rt.Index
					er := RoundExpec{A: &a, Index: &idx, Item: append([]uint32(nil), rt.Words...), AfterInject: stateOut(rt.AfterInject), AfterExch: stateOut(rt.AfterExchang)}
					if len(rt.As) > 1 {
						er.As, er.Regions = append([]uint32(nil), rt.As...), append([]uint64(nil), rt.Regions...)
					}
					for _, x := range rt.Executions {
						h := x.H
						er.Executions = append(er.Executions, ExecExpec{E: x.E, H: &h, Sel: x.Sel})
					}
					for _, b := range rt.AfterBlocks {
						er.AfterBlocks = append(er.AfterBlocks, stateOut(b))
					}
					e.Rounds = append(e.Rounds, er)
				}
			}
		}
		if vf.Vectors[i].Target != "" && vf.Vectors[i].Valid == nil {
			if tb, err := hex.DecodeString(vf.Vectors[i].Target); err == nil && len(tb) == 32 {
				var tgt [32]byte
				copy(tgt[:], tb)
				valid := Accept(res.Out, tgt)
				vf.Vectors[i].Valid = &valid
			}
		}
	}
}

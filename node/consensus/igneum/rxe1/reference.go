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
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// The reference's spec-form files (REFERENCE-NOTES.md section 5,
// tests/rxe1/vectors/<variant>/): known (seed and out per vector), trace
// (every intermediate), schedule (the descriptors and the opcode census per
// epoch) and dataset (cache and dataset digests per epoch). This file reads
// them into this implementation's checks. Nonces are decimal strings;
// bytes are hex without prefix.

type refDocument struct {
	Schema       *uint32 `json:"schema"`
	Mode         *uint32 `json:"mode"`
	Blocks       *uint32 `json:"blocks"`
	Rounds       *uint32 `json:"rounds"`
	Registers    *uint32 `json:"registers"`
	DatasetBytes *uint64 `json:"dataset_bytes"`
	Parents      *uint32 `json:"parents"`
	CacheBytes   *uint64 `json:"cache_bytes"`
	ChainID      *uint64 `json:"chain_id"`
	EpochBlocks  *uint64 `json:"epoch_blocks"`
	TestOnly     *uint32 `json:"test_only"`
	Digest       string  `json:"dataset_spec_digest"`
	Group        *uint32 `json:"group"`
	Route        *uint32 `json:"route"`
	Chunks       *uint32 `json:"chunks"`
	Payload      *uint32 `json:"payload_bytes"`
	BlockLen     *uint32 `json:"block_len"`
	Family       *uint32 `json:"family"`
}

// ParamsFromDocument reads the reference's params document (the notes'
// section 3): schema, mode, blocks, rounds, registers and dataset_bytes
// mandatory; parents 256, cache_bytes 32 MiB by default; dataset_spec_digest
// must equal the binding's constant when named. testOnly reports the
// document's test_only flag.
func ParamsFromDocument(doc []byte) (p Params, testOnly bool, err error) {
	var d refDocument
	if err := json.Unmarshal(doc, &d); err != nil {
		return p, false, fmt.Errorf("params document: %w", err)
	}
	for name, ptr := range map[string]bool{"schema": d.Schema != nil, "mode": d.Mode != nil, "blocks": d.Blocks != nil, "rounds": d.Rounds != nil, "registers": d.Registers != nil, "dataset_bytes": d.DatasetBytes != nil} {
		if !ptr {
			return p, false, fmt.Errorf("%w: params document without %s", ErrParams, name)
		}
	}
	p = Params{DatasetSpecDigest: DatasetSpecDigest, DatasetBytes: *d.DatasetBytes, Parents: 256, CacheBytes: ProductionCacheBytes,
		R: *d.Rounds, K: *d.Registers, B: *d.Blocks, Mode: Mode(*d.Mode), Schema: *d.Schema}
	if d.Parents != nil {
		p.Parents = *d.Parents
	}
	if d.CacheBytes != nil {
		p.CacheBytes = *d.CacheBytes
	}
	if p.Semantics() >= 3 {
		// the v0.1 point by default (section 1): group 16, route 1, chunks 1, payload 128
		p.Group, p.Route, p.Chunks, p.Payload = 16, RouteFixed, 1, 128
		if d.Group != nil {
			p.Group = *d.Group
		}
		if d.Route != nil {
			p.Route = *d.Route
		}
		if d.Chunks != nil {
			p.Chunks = *d.Chunks
		}
		if d.Payload != nil {
			p.Payload = *d.Payload
		}
	} else if d.Group != nil || d.Route != nil || d.Chunks != nil || d.Payload != nil {
		return p, false, fmt.Errorf("%w: group, route, chunks or payload_bytes in a schema %d document", ErrParams, p.Schema)
	}
	if p.Semantics() == 4 {
		if d.BlockLen == nil || d.Family == nil {
			return p, false, fmt.Errorf("%w: a schema 4 document carries block_len and family", ErrParams)
		}
		p.BlockLen, p.Family = *d.BlockLen, *d.Family
	} else if d.BlockLen != nil || d.Family != nil {
		return p, false, fmt.Errorf("%w: block_len or family in a schema %d document", ErrParams, p.Schema)
	}
	if p.Semantics() == 5 {
		// section 11.1: dataset_bytes mandatory and 0; cache_bytes and parents absent or their defaults, zeroed;
		// dataset_spec_digest must not be given; the digest field is zero
		if p.DatasetBytes != 0 {
			return p, false, fmt.Errorf("%w: a schema 5 document carries dataset_bytes 0, got %d", ErrParams, p.DatasetBytes)
		}
		if d.Digest != "" {
			return p, false, fmt.Errorf("%w: dataset_spec_digest in a schema 5 document", ErrParams)
		}
		p.DatasetSpecDigest, p.Parents, p.CacheBytes = [32]byte{}, 0, 0
	}
	if d.Digest != "" {
		b, err := hex.DecodeString(d.Digest)
		if err != nil || len(b) != 32 {
			return p, false, fmt.Errorf("%w: dataset_spec_digest", ErrParams)
		}
		copy(p.DatasetSpecDigest[:], b)
	}
	testOnly = (d.TestOnly != nil && *d.TestOnly != 0) || p.IsTestSchema()
	if testOnly {
		err = p.ValidateTestOnly()
	} else {
		err = p.Validate()
	}
	return p, testOnly, err
}

type refHeader struct {
	Candidate string          `json:"candidate"`
	Class     string          `json:"class"`
	Variant   string          `json:"variant"`
	Generator string          `json:"generator"`
	Params    json.RawMessage `json:"params"`
	PID       string          `json:"pid"`
	ChainID   string          `json:"chain_id"`
}

type refKnown struct {
	EpochNumber uint64 `json:"epoch_number"`
	EpochSeed   string `json:"epoch_seed"`
	HeaderBlob  string `json:"header_blob"`
	Nonce       string `json:"nonce"`
	Seed        string `json:"seed"`
	Out         string `json:"out"`
}

type refTraceVector struct {
	refKnown
	PID          string     `json:"pid"`
	ChainID      string     `json:"chain_id"`
	ProgramSeed  string     `json:"program_seed"`
	InitialState [][]uint32 `json:"initial_state"`
	Rounds       []struct {
		J      uint32     `json:"j"`
		A      *uint32    `json:"a"`     // schemas 1 and 2
		Index  *uint64    `json:"index"` // schemas 1 and 2
		Item   []uint32   `json:"item"`  // schemas 1 and 2: the 32 item words
		Chunks []struct { // schema 3 (text revision 2, section 7)
			C      uint32 `json:"c"`
			A      uint32 `json:"a"`
			Region uint64 `json:"region"`
		} `json:"chunks"`
		Payload        []uint32   `json:"payload"` // schema 3: the PAYLOAD / 4 injected words
		AfterInjection [][]uint32 `json:"after_injection"`
		Executions     []struct { // mode S (section 9.6): the executions of this round
			E   uint32  `json:"e"`
			H   *uint32 `json:"h"`
			Sel uint32  `json:"sel"`
		} `json:"executions"`
		Blocks        [][][]uint32 `json:"blocks"`
		AfterExchange [][]uint32   `json:"after_exchange"`
	} `json:"rounds"`
}

type refSchedule struct {
	EpochNumber  uint64 `json:"epoch_number"`
	EpochSeed    string `json:"epoch_seed"`
	OpcodeCensus []int  `json:"opcode_census"`
	Schedule     struct {
		Mode        uint32          `json:"mode"`
		Nonce       string          `json:"nonce"` // mode P: the nonce whose program this is
		Seed        string          `json:"seed"`  // mode P: its work seed
		ProgramSeed string          `json:"program_seed"`
		Rounds      uint32          `json:"rounds"`
		Blocks      json.RawMessage `json:"blocks"` // modes F, E, P: the block count; mode S: the family, NB arrays of L descriptors
		Registers   uint32          `json:"registers"`
		Descriptors []struct {
			J, B, D, Src1, Src2, Rotate, Opcode uint32
		} `json:"descriptors"`
		FamilySeed string `json:"family_seed"` // mode S
		BlockLen   uint32 `json:"block_len"`
		Family     uint32 `json:"family"`
	} `json:"schedule"`
}

// refFamilyOp is one descriptor of the reference's family dump (mode S).
type refFamilyOp struct {
	I, D, Src1, Src2, Rotate, Opcode uint32
}

type refDataset struct {
	EpochNumber   uint64 `json:"epoch_number"`
	EpochSeed     string `json:"epoch_seed"`
	CacheBytes    string `json:"cache_bytes"`
	CacheDigest   string `json:"cache_digest"`
	DatasetBytes  string `json:"dataset_bytes"`
	DatasetDigest string `json:"dataset_digest"`
}

// IsReferenceFile reports whether a file has the reference's header.
func IsReferenceFile(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var h refHeader
	return json.Unmarshal(raw, &h) == nil && h.Candidate == ID
}

func hexBytes(name, s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return b, nil
}

func hex32v(name, s string) ([32]byte, error) {
	var out [32]byte
	b, err := hexBytes(name, s)
	if err != nil || len(b) != 32 {
		return out, fmt.Errorf("%s: want 32 bytes", name)
	}
	copy(out[:], b)
	return out, nil
}

// CheckReferenceFile runs one reference file of any class and returns the
// divergences. fullDatasets materialises the datasets of the dataset class
// (2 GiB each) for the dataset_digest comparison.
func CheckReferenceFile(path string, fullDatasets bool) ([]Divergence, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var h refHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if h.Candidate != ID {
		return nil, fmt.Errorf("%s: candidate %q", path, h.Candidate)
	}
	p, testOnly, err := ParamsFromDocument(h.Params)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pid := p.PID()
	if d := cmpHex(h.Variant, "pid (the file's record)", pid, h.PID); d != nil {
		return []Divergence{*d}, nil
	}
	chain, err := hexBytes("chain_id", h.ChainID)
	if err != nil {
		return nil, err
	}
	switch h.Class {
	case "known", "trace":
		var body struct {
			Vectors []json.RawMessage `json:"vectors"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		vf := &VectorFile{Algorithm: ID, Params: p, Generator: h.Generator}
		for i, rv := range body.Vectors {
			var tv refTraceVector
			if err := json.Unmarshal(rv, &tv); err != nil {
				return nil, fmt.Errorf("%s vector %d: %w", path, i, err)
			}
			nonce, err := strconv.ParseUint(tv.Nonce, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s vector %d: nonce %q", path, i, tv.Nonce)
			}
			cid := h.ChainID
			if tv.ChainID != "" {
				cid = tv.ChainID
			}
			v := Vector{Name: fmt.Sprintf("%s-%s-%02d", h.Variant, h.Class, i), ChainID: cid, HeaderBlob: tv.HeaderBlob, EpochSeed: tv.EpochSeed, EpochNumber: tv.EpochNumber, Nonce: nonce}
			v.Expect.PID = h.PID
			if tv.PID != "" {
				v.Expect.PID = tv.PID
			}
			v.Expect.Seed, v.Expect.Out, v.Expect.ProgramSeed = tv.Seed, tv.Out, tv.ProgramSeed
			if h.Class == "trace" {
				v.Expect.Init = tv.InitialState
				for _, r := range tv.Rounds {
					er := RoundExpec{A: r.A, Index: r.Index, Item: r.Item, AfterInject: r.AfterInjection, AfterBlocks: r.Blocks, AfterExch: r.AfterExchange}
					if len(r.Chunks) > 0 {
						for _, ch := range r.Chunks {
							er.As = append(er.As, ch.A)
							er.Regions = append(er.Regions, ch.Region)
						}
						er.Item = r.Payload
					}
					for _, x := range r.Executions {
						er.Executions = append(er.Executions, ExecExpec{E: x.E, H: x.H, Sel: x.Sel})
					}
					v.Expect.Rounds = append(v.Expect.Rounds, er)
				}
			}
			vf.Vectors = append(vf.Vectors, v)
		}
		_, divs, err := vf.Check(testOnly)
		return divs, err
	case "schedule":
		var body struct {
			Epochs []refSchedule `json:"epochs"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		var divs []Divergence
		for _, e := range body.Epochs {
			name := fmt.Sprintf("%s-schedule-epoch-%d", h.Variant, e.EpochNumber)
			es, err := hex32v("epoch_seed", e.EpochSeed)
			if err != nil {
				return nil, err
			}
			in := Input{ChainID: chain, EpochSeed: es, EpochNumber: e.EpochNumber}
			if p.Mode == ModeS {
				// the family dump (section 9.6): family_seed and every block's descriptors
				name = fmt.Sprintf("%s-family", h.Variant)
				fs := FamilySeed(pid)
				if d := cmpHex(name, "family seed", fs, e.Schedule.FamilySeed); d != nil {
					divs = append(divs, *d)
					continue
				}
				var blocks [][]refFamilyOp
				if err := json.Unmarshal(e.Schedule.Blocks, &blocks); err != nil {
					return nil, fmt.Errorf("%s: family blocks: %w", name, err)
				}
				fam := GenerateFamily(p, fs)
				var first *Divergence
				for b := 0; first == nil && b < len(blocks); b++ {
					if b >= len(fam) {
						first = &Divergence{name, fmt.Sprintf("block %d", b), "(none)", "a block"}
						break
					}
					for _, ro := range blocks[b] {
						if ro.I >= p.BlockLen {
							first = &Divergence{name, fmt.Sprintf("block %d operation %d", b, ro.I), "(out of range)", "a descriptor"}
							break
						}
						op := fam[b][ro.I]
						if op.Dst != ro.D || op.Src1 != ro.Src1 || op.Src2 != ro.Src2 || op.Rotate != ro.Rotate || op.Opcode != ro.Opcode {
							first = &Divergence{name, fmt.Sprintf("block %d operation %d", b, ro.I),
								fmt.Sprintf("d %d src1 %d src2 %d rotate %d opcode %d", op.Dst, op.Src1, op.Src2, op.Rotate, op.Opcode),
								fmt.Sprintf("d %d src1 %d src2 %d rotate %d opcode %d", ro.D, ro.Src1, ro.Src2, ro.Rotate, ro.Opcode)}
							break
						}
					}
				}
				if first == nil && uint32(len(blocks)) != p.Family {
					first = &Divergence{name, "family size", fmt.Sprint(p.Family), fmt.Sprint(len(blocks))}
				}
				if first != nil {
					divs = append(divs, *first)
				}
				continue
			}
			ps, ok := ProgramSeed(p, in, pid)
			if p.Mode == ModeP {
				// the program of one nonce, from the work seed the file carries (section 5)
				name = fmt.Sprintf("%s-schedule-epoch-%d-nonce-%s", h.Variant, e.EpochNumber, e.Schedule.Nonce)
				seed, err := hex32v("seed", e.Schedule.Seed)
				if err != nil {
					return nil, err
				}
				ps, ok = NonceProgramSeed(seed), true
			}
			if !ok {
				if e.Schedule.ProgramSeed != "" && e.Schedule.ProgramSeed != hex.EncodeToString(make([]byte, 32)) {
					divs = append(divs, Divergence{name, "program seed", "(none: mode M)", e.Schedule.ProgramSeed})
				}
				continue
			}
			if d := cmpHex(name, "program seed", ps, e.Schedule.ProgramSeed); d != nil {
				divs = append(divs, *d)
				continue
			}
			sched := GenerateSchedule(p, ps)
			census := make([]int, 4)
			var first *Divergence
			for _, rd := range e.Schedule.Descriptors {
				if rd.J >= p.R || rd.B >= p.B || rd.D >= p.K {
					first = &Divergence{name, fmt.Sprintf("descriptor j %d b %d d %d", rd.J, rd.B, rd.D), "(out of range)", "a descriptor"}
					break
				}
				op := sched[rd.J][rd.B][rd.D]
				census[op.Opcode]++
				if op.Src1 != rd.Src1 || op.Src2 != rd.Src2 || op.Rotate != rd.Rotate || op.Opcode != rd.Opcode {
					first = &Divergence{name, fmt.Sprintf("descriptor j %d b %d d %d", rd.J, rd.B, rd.D),
						fmt.Sprintf("src1 %d src2 %d rotate %d opcode %d", op.Src1, op.Src2, op.Rotate, op.Opcode),
						fmt.Sprintf("src1 %d src2 %d rotate %d opcode %d", rd.Src1, rd.Src2, rd.Rotate, rd.Opcode)}
					break
				}
			}
			if first == nil && len(e.OpcodeCensus) == 4 {
				for k := 0; k < 4; k++ {
					if census[k] != e.OpcodeCensus[k] {
						first = &Divergence{name, fmt.Sprintf("opcode %d census", k), fmt.Sprint(census[k]), fmt.Sprint(e.OpcodeCensus[k])}
						break
					}
				}
			}
			if first != nil {
				divs = append(divs, *first)
			}
		}
		return divs, nil
	case "dataset":
		var body struct {
			Epochs []refDataset `json:"epochs"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		var divs []Divergence
		for _, e := range body.Epochs {
			name := fmt.Sprintf("%s-dataset-epoch-%d", h.Variant, e.EpochNumber)
			es, err := hex32v("epoch_seed", e.EpochSeed)
			if err != nil {
				return nil, err
			}
			if cb, _ := strconv.ParseUint(e.CacheBytes, 10, 64); cb != p.CacheBytes {
				divs = append(divs, Divergence{name, "cache_bytes", fmt.Sprint(p.CacheBytes), e.CacheBytes})
				continue
			}
			if p.MemoryFree() {
				divs = append(divs, Divergence{name, "dataset class", "(none: schema 5 binds no dataset)", "a dataset"})
				continue
			}
			var light *Light
			if !p.MemoryFree() {
				light = NewLight(p, es)
			}
			if d := cmpHex(name, "cache digest", light.CacheDigest(), e.CacheDigest); d != nil {
				divs = append(divs, *d)
				continue
			}
			if e.DatasetDigest != "" && (fullDatasets || p.DatasetBytes <= 64<<20) {
				if d := cmpHex(name, "dataset digest", NewFull(light).Digest(), e.DatasetDigest); d != nil {
					divs = append(divs, *d)
				}
			}
		}
		return divs, nil
	}
	return nil, fmt.Errorf("%s: class %q not handled", path, h.Class)
}

// The reference's campaign form (REFERENCE-NOTES.md): one JSON line per
// nonce with chain_id, epoch_seed, header_blob (hex), epoch_number, nonce
// (decimal) and the computed out and trace_digest. CheckCampaign recomputes
// every line for the record and returns the differing lines; WriteCampaign
// writes this side's results in the same form so the reference's
// campaign-check reads them.

type CampaignLine struct {
	ChainID     string `json:"chain_id"`
	EpochSeed   string `json:"epoch_seed"`
	EpochNumber uint64 `json:"epoch_number"`
	HeaderBlob  string `json:"header_blob"`
	Nonce       string `json:"nonce"`
	Out         string `json:"out"`
	TraceDigest string `json:"trace_digest"`
}

// CheckCampaign recomputes a JSONL campaign file for the record p.
func CheckCampaign(path string, p Params, testOnly bool) (lines int, divs []Divergence, err error) {
	if testOnly {
		err = p.ValidateTestOnly()
	} else {
		err = p.Validate()
	}
	if err != nil {
		return 0, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()
	pid := p.PID()
	sources := map[[32]byte]ItemSource{}
	schedules := map[[32]byte]Schedule{}
	routings := map[[32]byte]Routing{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		lines++
		var cl CampaignLine
		if err := json.Unmarshal(raw, &cl); err != nil {
			return lines, divs, fmt.Errorf("line %d: %w", lines, err)
		}
		name := fmt.Sprintf("line %d nonce %s", lines, cl.Nonce)
		chain, err := hexBytes("chain_id", cl.ChainID)
		if err != nil {
			return lines, divs, err
		}
		hb, err := hexBytes("header_blob", cl.HeaderBlob)
		if err != nil {
			return lines, divs, err
		}
		es, err := hex32v("epoch_seed", cl.EpochSeed)
		if err != nil {
			return lines, divs, err
		}
		nonce, err := strconv.ParseUint(cl.Nonce, 10, 64)
		if err != nil {
			return lines, divs, fmt.Errorf("%s: nonce", name)
		}
		in := Input{ChainID: chain, HeaderBlob: hb, EpochSeed: es, EpochNumber: cl.EpochNumber, Nonce: nonce}
		src, ok := sources[es]
		if !ok {
			var src *Light
			if !p.MemoryFree() {
				src = NewLight(p, es)
			}
			sources[es] = src
		}
		var sched Schedule
		if ps, ok := ProgramSeed(p, in, pid); ok && p.Mode != ModeP {
			if sched, ok = schedules[ps]; !ok {
				sched = GenerateSchedule(p, ps)
				schedules[ps] = sched
			}
		}
		var routing Routing
		if rs, ok := RouteSeed(p, in, pid); ok {
			if routing, ok = routings[rs]; !ok {
				routing = GenerateRouting(p, rs)
				routings[rs] = routing
			}
		}
		res, err := Compute(p, in, src, sched, routing, true)
		if err != nil {
			return lines, divs, fmt.Errorf("%s: %w", name, err)
		}
		if d := cmpHex(name, "out", res.Out, cl.Out); d != nil {
			divs = append(divs, *d)
			continue
		}
		if d := cmpHex(name, "trace_digest", TraceDigest(res), cl.TraceDigest); d != nil {
			divs = append(divs, *d)
		}
	}
	return lines, divs, sc.Err()
}

// WriteCampaign writes this side's results for the inputs of a vector file
// in the reference's JSONL form.
func WriteCampaign(path string, vf *VectorFile, results []Result) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for i, v := range vf.Vectors {
		res := results[i]
		cl := CampaignLine{ChainID: v.ChainID, EpochSeed: v.EpochSeed, EpochNumber: v.EpochNumber, HeaderBlob: v.HeaderBlob, Nonce: strconv.FormatUint(v.Nonce, 10),
			Out: hex.EncodeToString(res.Out[:]), TraceDigest: hex.EncodeToString(res.TraceDigest[:])}
		raw, _ := json.Marshal(cl)
		w.Write(raw)
		w.WriteByte('\n')
	}
	return w.Flush()
}

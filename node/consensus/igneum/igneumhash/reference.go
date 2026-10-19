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

package igneumhash

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The reference's vector files (pow/vectors/igneumhash/<variant>/<class>.json,
// written by pow/ref/tools/ignpow-vectors; the specification's section 9):
// a header with candidate, class, generator, seed, the params document
// (null for the baseline's defaults) and the vectors of the class. This file
// reads them into VectorFile so one checker serves both formats.

type refFile struct {
	Candidate string            `json:"candidate"`
	Class     string            `json:"class"`
	Generator string            `json:"generator"`
	Seed      string            `json:"seed"`
	Params    json.RawMessage   `json:"params"`
	Count     int               `json:"count"`
	Vectors   []json.RawMessage `json:"vectors"`
	// the fp class (version 8's floating forms): rows in three arrays, no vectors
	Edge   []refFPRow `json:"edge"`
	Random []refFPRow `json:"random"`
	Wide   []refFPRow `json:"wide"`
}

// refFPRow is one row of the fp class: the floating form of opcode 2 on
// (x, y, z) (edge and random) or the wide form with its opcode (wide).
type refFPRow struct {
	Op  *uint32 `json:"op"`
	X   uint32  `json:"x"`
	Y   uint32  `json:"y"`
	Z   uint32  `json:"z"`
	Out uint32  `json:"out"`
}

// refParams is the params document of section 0.1: integer members only,
// absent members at their defaults, which depend on the version.
type refParams struct {
	Version     *uint32 `json:"version"`
	DatasetMib  *uint64 `json:"dataset_mib"`
	ScratchKib  *uint32 `json:"scratch_kib"`
	Rounds      *uint32 `json:"rounds"`
	Lanes       *uint32 `json:"lanes"`
	EpochBlocks *uint64 `json:"epoch_blocks"`
	ItemChain   *uint32 `json:"item_chain"`
	ChainID     *uint64 `json:"chain_id"`
	CacheMib    *uint64 `json:"cache_mib"`
	Parents     *uint32 `json:"parents"`
	ItemBytes   *uint32 `json:"item_bytes"` // version 4 (section 0.4), mandatory there
	// version 8 (sections 0.8 and 0.9): blocks and registers mandatory, features optional, keyed_rounds mandatory
	// under bit 10 and refused without it
	Blocks      *uint32 `json:"blocks"`
	Registers   *uint32 `json:"registers"`
	Features    *uint32 `json:"features"`
	KeyedRounds *uint32 `json:"keyed_rounds"`
}

// ParamsFromDocument derives Params from the params document (section
// 0.1, revision 0.5): "version" is mandatory; a document without it, null,
// {} or an absent document is refused (security's F-14: a missing version
// is never a default); every other absent member takes its version's
// default; cache_mib and parents are rejected under version 0.
func ParamsFromDocument(doc []byte) (Params, error) {
	trimmed := strings.TrimSpace(string(doc))
	if trimmed == "" || trimmed == "null" {
		return Params{}, fmt.Errorf("%w: absent params document (version is mandatory, revision 0.5)", ErrParams)
	}
	var rp refParams
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.DisallowUnknownFields() // the reference's parser refuses a member it does not know (section 0.1)
	if err := dec.Decode(&rp); err != nil {
		return Params{}, fmt.Errorf("%w: params document: %v", ErrParams, err)
	}
	if rp.Version == nil {
		return Params{}, fmt.Errorf("%w: params document without version (mandatory, revision 0.5)", ErrParams)
	}
	version := *rp.Version
	if version == 2 || version == 3 || version == 5 || version == 7 {
		// version 2 (the R0-B schedule), 3 (the item-width variant, superseded by version 4 as an instrument),
		// 5 (the multiply-high map on its own) and 7 (v6b) are the record only; none is implemented by this package
		return Params{}, fmt.Errorf("%w: version %d is not implemented by this package", ErrParams, version)
	}
	if version != Version0 && version != Version1 && version != Version4 && version != Version6 && version != Version8 {
		return Params{}, fmt.Errorf("%w: version %d", ErrParams, version)
	}
	p := Defaults(version)
	if version == Version4 || version == Version6 || version == Version8 {
		// versions 4, 6 and 8 are version 1 at the proposed point of section 12 with item_bytes mandatory (the
		// reference's defaults: dataset_mib 1024, cache_mib 32, parents 256, rounds 64, epoch_blocks 4096)
		p = Params{ChainID: 7310, Version: version, DatasetBytes: 1 << 30, ScratchBytes: 0, Rounds: 64, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 1 << 25, Parents: 256}
		if rp.ItemBytes == nil {
			return Params{}, fmt.Errorf("%w: a version %d document carries item_bytes", ErrParams, version)
		}
		p.ItemBytes = *rp.ItemBytes
	} else if rp.ItemBytes != nil {
		return Params{}, fmt.Errorf("%w: item_bytes in a version %d document", ErrParams, version)
	}
	if version == Version8 {
		// section 0.8: blocks and registers mandatory, features optional (0 absent); section 0.9: keyed_rounds mandatory
		// with bit 10 and refused without it
		if rp.Blocks == nil || rp.Registers == nil {
			return Params{}, fmt.Errorf("%w: a version 8 document carries blocks and registers", ErrParams)
		}
		p.Blocks, p.Registers = *rp.Blocks, *rp.Registers
		if rp.Features != nil {
			p.Features = *rp.Features
		}
		if p.Features&FeatKeyedRounds != 0 {
			if rp.KeyedRounds == nil {
				return Params{}, fmt.Errorf("%w: bit 10 without keyed_rounds", ErrParams)
			}
			p.KeyedRounds = *rp.KeyedRounds
		} else if rp.KeyedRounds != nil {
			return Params{}, fmt.Errorf("%w: keyed_rounds without bit 10", ErrParams)
		}
	} else if rp.Blocks != nil || rp.Registers != nil || rp.Features != nil || rp.KeyedRounds != nil {
		return Params{}, fmt.Errorf("%w: blocks, registers, features and keyed_rounds are version 8 fields", ErrParams)
	}
	if rp.DatasetMib != nil {
		p.DatasetBytes = *rp.DatasetMib << 20
	}
	if rp.ScratchKib != nil {
		p.ScratchBytes = *rp.ScratchKib << 10
	}
	if rp.Rounds != nil {
		p.Rounds = *rp.Rounds
	}
	if rp.Lanes != nil {
		p.Lanes = *rp.Lanes
	}
	if rp.EpochBlocks != nil {
		p.EpochLength = *rp.EpochBlocks
	}
	if rp.ItemChain != nil {
		p.ItemChain = *rp.ItemChain
	}
	if rp.ChainID != nil {
		p.ChainID = *rp.ChainID
	}
	if rp.CacheMib != nil || rp.Parents != nil {
		if version == Version0 {
			return p, fmt.Errorf("%w: cache_mib and parents in a version 0 document", ErrParams)
		}
		if rp.CacheMib != nil {
			p.CacheBytes = *rp.CacheMib << 20
		}
		if rp.Parents != nil {
			p.Parents = *rp.Parents
		}
	}
	return p, p.Validate()
}

type refVector struct {
	Height        uint64          `json:"height"`
	Header        string          `json:"header"`
	Nonce         string          `json:"nonce"` // decimal
	Mix           string          `json:"mix"`
	Hash          string          `json:"hash"`
	CtxHeight     *uint64         `json:"ctx_height"`
	Boundary      string          `json:"boundary"`
	Expect        string          `json:"expect"`
	Epoch         *uint64         `json:"epoch"`
	E             string          `json:"E"`
	D             string          `json:"D"`
	DatasetDigest string          `json:"dataset_digest"`
	CacheDigest   string          `json:"cache_digest"`
	DatasetBytes  string          `json:"dataset_bytes"`
	Trace         *refTrace       `json:"trace"`
	Start         json.RawMessage `json:"start"` // tails class (not run here)
	// section 0.9 and the closure classes (11 October 2026)
	TestSK      string `json:"test_sk"`      // keyed: the test secret
	Shares      string `json:"shares"`       // keyed: the prover's shares, KEYED_ROUNDS x 32 bytes as hex; tails: the share count (decimal), read as SharesCount
	// tails: the range's count, the target, the first nonce of the smallest digest and that digest
	NonceCount  string `json:"nonce_count"`
	ShareTarget string `json:"share_target"`
	MinNonce    string `json:"min_nonce"`
	MinHash     string `json:"min_hash"`
	SharesCount string `json:"-"`
	M           string `json:"m"`            // keyed: the messages m_0..m_(S-1) as hex
	KeyedShares string `json:"keyed_shares"` // malformed under bit 10: the test secret's shares for the replay
	Form        string `json:"form"`         // key-forms: keyless, null-shares, wrong-sk, flipped-share
	DocHex      string `json:"doc_hex"`      // params-rule, registers-rule: the document under test
	Note        string `json:"note"`
}

type refTrace struct {
	S      string     `json:"S"`
	E      string     `json:"E"`
	P      string     `json:"P"`
	TestSK string     `json:"test_sk"` // a keyed document's trace is the prover's under the test secret
	Init   [][]uint32 `json:"init"`
	Fill   [][]uint32 `json:"fill"`
	Rounds []refRound `json:"rounds"`
	Fold   [][]uint32 `json:"fold"`
	M      string     `json:"M"`
	F      string     `json:"F"`
}

type refRound struct {
	R     uint32     `json:"r"`
	Idx   uint64     `json:"idx"`
	Share string     `json:"share"` // version 8 bit 10: the keyed round's share
	H     *uint32    `json:"h"`     // version 4: the item word, the tree's root
	Block [][]uint32 `json:"block"` // version 8: the registers after the block schedule
	Ops   string     `json:"ops"`   // version 8: the opcode census
	Item  []uint32   `json:"item"`  // the item words as read (version 4; version 3's record carried them too)
	Regs  [][]uint32 `json:"regs"`
}

// LoadReference reads a reference vector file of any class into a
// VectorFile. The tails class (share counts over nonce ranges) is loaded
// with no expectations; the differential tool runs it separately.
func LoadReference(path string) (*VectorFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = widthFromTag(raw, filepath.Base(filepath.Dir(path)))
	var rf refFile
	if err := json.Unmarshal(raw, &rf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if rf.Candidate != Name {
		return nil, fmt.Errorf("%s: candidate %q is not %q", path, rf.Candidate, Name)
	}
	// revision 0.5: the file's document must name its version (the hash team's
	// files do since 5026b2d); a file without one is refused, not guessed
	p, err := ParamsFromDocument(rf.Params)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	vf := &VectorFile{Algorithm: Name, Spec: "reference " + rf.Class, Generator: rf.Generator, Params: p, Class: rf.Class}
	if rf.Class == "fp" {
		// the fp class: every row's out recomputed from (x, y, z) and the opcode (section 0.8, bits 4 and 5)
		add := func(name string, rows []refFPRow) {
			for i, r := range rows {
				row := r
				vf.Vectors = append(vf.Vectors, Vector{Name: fmt.Sprintf("%s-fp-%s-%04d", p.Variant(), name, i), FP: &FPRow{Op: row.Op, X: row.X, Y: row.Y, Z: row.Z, Out: row.Out}})
			}
		}
		add("edge", rf.Edge)
		add("random", rf.Random)
		add("wide", rf.Wide)
		return vf, nil
	}
	for i, rawv := range rf.Vectors {
		var rv refVector
		if err := json.Unmarshal(rawv, &rv); err != nil {
			return nil, fmt.Errorf("%s: vector %d: %w", path, i, err)
		}
		nonce, err := strconv.ParseUint(rv.Nonce, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: vector %d: nonce %q", path, i, rv.Nonce)
		}
		v := Vector{Name: fmt.Sprintf("%s-%s-%02d", p.Variant(), rf.Class, i), Height: rv.Height, Nonce: nonce, HeaderHash: rv.Header}
		if rf.Class == "tails" {
			rv.SharesCount = rv.Shares // the one key serving two classes: a decimal count here, hex shares under keyed
		}
		switch rf.Class {
		case "keyed":
			// section 0.9: the prover's path under the row's test secret must give the row's shares, mix and hash;
			// the verifier's replay from the shares alone the same mix and hash; the messages when the row carries them
			v.Expect.Mix = rv.Mix
			v.Expect.Digest = rv.Hash
			v.TestSK = rv.TestSK
			v.Shares = rv.Shares
			v.M = rv.M
		case "key-forms":
			// the keyed entry's deterministic refusals (the outside review's closure reading): the row carries the true mix and hash
			v.Expect.Mix = rv.Mix
			v.Expect.Digest = rv.Hash
			v.Form = rv.Form
			v.Reason = rv.Expect
		case "params-rule", "registers-rule":
			// the document under test as the hex of its JSON text; the parse verdict expected: ok or params
			d, err := hex.DecodeString(rv.DocHex)
			if err != nil {
				return nil, fmt.Errorf("%s: vector %d: doc_hex: %w", path, i, err)
			}
			v.HeaderHash = hex.EncodeToString(make([]byte, 32))
			v.Doc = string(d)
			v.Note = rv.Note
			v.Reason = rv.Expect
		case "dataset":
			// E, D and the diagnostic digest per epoch; no proof
			v.HeaderHash = hex.EncodeToString(make([]byte, 32))
			v.Expect.Epoch = rv.Epoch
			v.Expect.EpochSeed = rv.E
			v.Expect.DatasetID = rv.D
			v.Expect.DatasetDigest = rv.DatasetDigest
			v.Expect.CacheDigest = rv.CacheDigest
			v.DatasetOnly = true
		case "malformed":
			v.CtxHeight = rv.CtxHeight
			v.MixGiven = rv.Mix
			v.Target = rv.Boundary
			v.Reason = rv.Expect
			v.Shares = rv.KeyedShares // a keyed document's rows: the test secret's shares for the replay
		case "trace":
			v.Expect.Mix = rv.Mix
			v.Expect.Digest = rv.Hash
			if rv.Trace != nil {
				v.Expect.ParamsHash = rv.Trace.P
				v.Expect.Seed = rv.Trace.S
				v.Expect.EpochSeed = rv.Trace.E
				v.TestSK = rv.Trace.TestSK
				v.Expect.Trace = &TraceExpect{Init: rv.Trace.Init, Fill: rv.Trace.Fill, Fold: rv.Trace.Fold}
				for _, r := range rv.Trace.Rounds {
					v.Expect.Trace.Rounds = append(v.Expect.Trace.Rounds, TraceRound{R: r.R, Idx: r.Idx, H: r.H, Item: r.Item, Regs: r.Regs, Share: r.Share, Block: r.Block, Ops: r.Ops})
				}
				if rv.Trace.M != "" {
					v.Expect.Mix = rv.Trace.M
				}
				if rv.Trace.F != "" {
					v.Expect.Digest = rv.Trace.F
				}
			}
		case "tails":
			// the search over a nonce range: the first nonce of the smallest digest and the count at or under the target
			v.Tails = true
			if v.NonceCount, err = strconv.ParseUint(rv.NonceCount, 10, 64); err != nil {
				return nil, fmt.Errorf("%s: vector %d: nonce_count %q", path, i, rv.NonceCount)
			}
			if v.MinNonce, err = strconv.ParseUint(rv.MinNonce, 10, 64); err != nil {
				return nil, fmt.Errorf("%s: vector %d: min_nonce %q", path, i, rv.MinNonce)
			}
			if v.ShareCount, err = strconv.ParseUint(rv.SharesCount, 10, 64); err != nil {
				return nil, fmt.Errorf("%s: vector %d: shares %q", path, i, rv.SharesCount)
			}
			v.ShareTarget = rv.ShareTarget
			v.MinHash = rv.MinHash
		default: // unseen, nonce-boundary, transitions
			v.Expect.Mix = rv.Mix
			v.Expect.Digest = rv.Hash
		}
		vf.Vectors = append(vf.Vectors, v)
	}
	return vf, nil
}

// TraceExpect is the trace class's intermediates: registers after lane
// initialisation (4.1), after the scratch fill (4.2), after each round with
// its item index, and the fold words (4.7).
type TraceExpect struct {
	Init   [][]uint32
	Fill   [][]uint32
	Rounds []TraceRound
	Fold   [][]uint32
}

type TraceRound struct {
	R    uint32
	Idx  uint64
	H    *uint32  // version 4
	Item []uint32 // version 4
	Regs [][]uint32
	// version 8: the keyed round's share (hex; empty when the round was not keyed), the registers after the block
	// schedule and the opcode census (section 0.8)
	Share string
	Block [][]uint32
	Ops   string
}

// cmpRegs compares lane register files; it returns the first (lane, word)
// that differs, or -1, -1.
func cmpRegs(have [][LaneWords]uint32, want [][]uint32) (int, int) {
	for l := range want {
		if l >= len(have) {
			return l, -1
		}
		for k := range want[l] {
			if k >= LaneWords || have[l][k] != want[l][k] {
				return l, k
			}
		}
	}
	return -1, -1
}

// cmpTrace reports the first diverging intermediate of the trace class, in
// the order of section 4. The reference records, for round r, the item index
// it loaded in that round (section 4.4) and the registers after the round's
// exchange (4.5); the Go side's FirstIdx and RoundIdx[r-1] are the indexes
// loaded in round r, so both are compared on that reading and a divergence
// in the index alone is reported as such.
func cmpTrace(vector string, have *Trace, want *TraceExpect) *Divergence {
	if have == nil {
		return &Divergence{Vector: vector, Stage: "trace", Byte: -1, Have: "(none)", Want: "trace"}
	}
	if l, k := cmpRegs(have.AfterInit, want.Init); l >= 0 {
		return &Divergence{Vector: vector, Stage: fmt.Sprintf("init lane %d word %d", l, k), Byte: -1, Have: regStr(have.AfterInit, l, k), Want: wantStr(want.Init, l, k)}
	}
	if l, k := cmpRegs(have.AfterFill, want.Fill); l >= 0 {
		return &Divergence{Vector: vector, Stage: fmt.Sprintf("fill lane %d word %d", l, k), Byte: -1, Have: regStr(have.AfterFill, l, k), Want: wantStr(want.Fill, l, k)}
	}
	for i, wr := range want.Rounds {
		r := int(wr.R)
		var haveIdx uint64
		if r == 0 {
			haveIdx = uint64(have.FirstIdx)
		} else if r-1 < len(have.RoundIdx) {
			haveIdx = uint64(have.RoundIdx[r-1])
		}
		if haveIdx != wr.Idx {
			return &Divergence{Vector: vector, Stage: fmt.Sprintf("round %d item index", r), Byte: -1, Have: fmt.Sprint(haveIdx), Want: fmt.Sprint(wr.Idx)}
		}
		if r >= len(have.Rounds) {
			return &Divergence{Vector: vector, Stage: fmt.Sprintf("round %d", r), Byte: -1, Have: "(none)", Want: fmt.Sprintf("%d rounds", len(want.Rounds))}
		}
		// version 4: the item words as read, then h (the leaves are item[j] ^ ((j + 1) * MUL))
		if len(wr.Item) > 0 && r < len(have.Items) {
			for j := range wr.Item {
				if j >= len(have.Items[r]) || have.Items[r][j] != wr.Item[j] {
					hv := "(none)"
					if j < len(have.Items[r]) {
						hv = fmt.Sprint(have.Items[r][j])
					}
					return &Divergence{Vector: vector, Stage: fmt.Sprintf("round %d item word %d", r, j), Byte: -1, Have: hv, Want: fmt.Sprint(wr.Item[j])}
				}
			}
		}
		// version 8 bit 10: the keyed round's share, recorded inside the round's record
		if r < len(have.Shares) {
			hs := hex.EncodeToString(have.Shares[r])
			if hs != wr.Share {
				return &Divergence{Vector: vector, Stage: fmt.Sprintf("round %d share", r), Byte: -1, Have: hs, Want: wr.Share}
			}
		} else if wr.Share != "" {
			return &Divergence{Vector: vector, Stage: fmt.Sprintf("round %d share", r), Byte: -1, Have: "(none)", Want: wr.Share}
		}
		if wr.H != nil && r < len(have.H) && have.H[r] != *wr.H {
			return &Divergence{Vector: vector, Stage: fmt.Sprintf("round %d item word h", r), Byte: -1, Have: fmt.Sprint(have.H[r]), Want: fmt.Sprint(*wr.H)}
		}
		// version 8: the registers after the block schedule and the opcode census
		if len(wr.Block) > 0 {
			if r >= len(have.Block) {
				return &Divergence{Vector: vector, Stage: fmt.Sprintf("round %d block", r), Byte: -1, Have: "(none)", Want: "block registers"}
			}
			if l, k := cmpRegs(have.Block[r], wr.Block); l >= 0 {
				return &Divergence{Vector: vector, Stage: fmt.Sprintf("round %d block lane %d word %d", r, l, k), Byte: -1, Have: regStr(have.Block[r], l, k), Want: wantStr(wr.Block, l, k)}
			}
		}
		if wr.Ops != "" && (r >= len(have.Ops) || have.Ops[r] != wr.Ops) {
			hv := "(none)"
			if r < len(have.Ops) {
				hv = have.Ops[r]
			}
			return &Divergence{Vector: vector, Stage: fmt.Sprintf("round %d ops", r), Byte: -1, Have: hv, Want: wr.Ops}
		}
		if l, k := cmpRegs(have.Rounds[r], wr.Regs); l >= 0 {
			return &Divergence{Vector: vector, Stage: fmt.Sprintf("round %d lane %d word %d", r, l, k), Byte: -1, Have: regStr(have.Rounds[r], l, k), Want: wantStr(wr.Regs, l, k)}
		}
		_ = i
	}
	// With no scratchpad the fold is absent (section 4.7); the reference's
	// trace class records it as zeros, which this side reads as absent
	// (DIFF-R0A.md E-4). A non-zero fold against no scratchpad is a divergence.
	if have.Fold == nil {
		for l := range want.Fold {
			for j := range want.Fold[l] {
				if want.Fold[l][j] != 0 {
					return &Divergence{Vector: vector, Stage: fmt.Sprintf("fold lane %d word %d", l, j), Byte: -1, Have: "(absent: no scratchpad)", Want: fmt.Sprint(want.Fold[l][j])}
				}
			}
		}
		return nil
	}
	for l := range want.Fold {
		for j := range want.Fold[l] {
			if l >= len(have.Fold) || j >= LineWords || have.Fold[l][j] != want.Fold[l][j] {
				hv := "(none)"
				if l < len(have.Fold) && j < LineWords {
					hv = fmt.Sprint(have.Fold[l][j])
				}
				return &Divergence{Vector: vector, Stage: fmt.Sprintf("fold lane %d word %d", l, j), Byte: -1, Have: hv, Want: fmt.Sprint(want.Fold[l][j])}
			}
		}
	}
	return nil
}

func regStr(regs [][LaneWords]uint32, l, k int) string {
	if l < len(regs) && k >= 0 && k < LaneWords {
		return fmt.Sprint(regs[l][k])
	}
	return "(none)"
}

func wantStr(regs [][]uint32, l, k int) string {
	if l < len(regs) && k >= 0 && k < len(regs[l]) {
		return fmt.Sprint(regs[l][k])
	}
	return "(none)"
}

var widthTag = regexp.MustCompile(`-w(64|128|256|512|1024)$`)

// widthFromTag supplies item_bytes to a version 4 document that omits it
// (section 0.4 makes the key mandatory; the reference's golden sets at
// 90c2abc0 carry the width in the directory tag only: DIFF-STRATA.md F-1),
// from the set's directory tag v4-...-w<W>. Any other document is returned
// unchanged, so the text's rule still refuses a document without the key.
func widthFromTag(raw []byte, dir string) []byte {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return raw
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(top["params"], &params) != nil {
		return raw
	}
	var version uint32
	if json.Unmarshal(params["version"], &version) != nil || (version != Version4 && version != Version6) {
		return raw
	}
	if _, ok := params["item_bytes"]; ok {
		return raw
	}
	m := widthTag.FindStringSubmatch(dir)
	if m == nil {
		return raw
	}
	params["item_bytes"] = json.RawMessage(m[1])
	pb, _ := json.Marshal(params)
	top["params"] = pb
	out, err := json.Marshal(top)
	if err != nil {
		return raw
	}
	return out
}

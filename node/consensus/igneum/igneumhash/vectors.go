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
	"errors"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"sync"
)

// VectorFile is the interchange format between the reference (pow/ref) and
// this implementation. Inputs are mandatory; every expected stage is
// optional and compared when present. Hex strings carry no 0x prefix.
//
// The reference may publish only (mix, digest) per vector; the differential
// tool then reports the first diverging stage it can see. Publishing the
// intermediate stages (seed, epochSeed, datasetRoot, scratchInit, the round
// digests) lets the tool name the first diverging round and byte, which is
// what turns a mismatch into a specification correction in one step.
type VectorFile struct {
	Algorithm string   `json:"algorithm"`
	Spec      string   `json:"spec,omitempty"`      // the specification revision the vectors were made from
	Generator string   `json:"generator,omitempty"` // the reference build string
	Params    Params   `json:"params"`
	Class     string   `json:"class,omitempty"` // the reference's class when loaded from one
	Vectors   []Vector `json:"vectors"`
}

type Vector struct {
	Name       string `json:"name"`
	HeaderHash string `json:"headerHash"`
	Height     uint64 `json:"height"`
	Nonce      uint64 `json:"nonce"`
	Difficulty string `json:"difficulty,omitempty"` // decimal; target = floor((2^256 - 1) / difficulty)
	Target     string `json:"target,omitempty"`     // hex, 32 bytes; used when difficulty is absent
	Valid      *bool  `json:"valid,omitempty"`      // expected verify outcome
	Expect     Expect `json:"expect"`

	// the reference's malformed class: the proof carries MixGiven and is
	// verified against Target with an item source for CtxHeight's epoch;
	// Reason is the expected first failure: ok, boundary, mix, epoch, height
	CtxHeight *uint64 `json:"ctxHeight,omitempty"`
	MixGiven  string  `json:"mixGiven,omitempty"`
	Reason    string  `json:"reason,omitempty"`
	// the reference's dataset class: only E, D and the dataset digest
	DatasetOnly bool `json:"datasetOnly,omitempty"`
	// a class this checker does not run
	Skip string `json:"skip,omitempty"`

	// version 8 bit 10 (section 0.9) and the closure classes of 11 October 2026.
	// keyed: TestSK the prover's secret, Shares the expected shares (hex, KEYED_ROUNDS x 32 bytes), M the messages;
	// trace under a keyed document: TestSK; malformed under a keyed document: Shares the replay's shares.
	TestSK string `json:"testSk,omitempty"`
	Shares string `json:"shares,omitempty"`
	M      string `json:"m,omitempty"`
	// key-forms: Form is keyless, null-shares, wrong-sk or flipped-share; Reason the expected verdict (arg or mix)
	Form string `json:"form,omitempty"`
	// params-rule and registers-rule: Doc the document's JSON text; Reason ok or params; Note the row's note
	Doc  string `json:"doc,omitempty"`
	Note string `json:"note,omitempty"`
	// fp: one row of the floating forms
	FP *FPRow `json:"fp,omitempty"`
	// tails: the search over the nonce range [Nonce, Nonce + NonceCount) at Height: the first nonce of the smallest
	// digest (MinNonce, MinHash) and the count of digests at or under ShareTarget (ShareCount)
	Tails       bool   `json:"tails,omitempty"`
	NonceCount  uint64 `json:"nonceCount,omitempty"`
	ShareTarget string `json:"shareTarget,omitempty"`
	MinNonce    uint64 `json:"minNonce,omitempty"`
	MinHash     string `json:"minHash,omitempty"`
	ShareCount  uint64 `json:"shareCount,omitempty"`
}

// tailsSearch runs the tails class's search: every nonce of the range hashed
// (the prover's path under the test secret on a keyed document), the first
// nonce of the strictly smallest digest kept, the digests at or under the
// target counted. The range is split across the cores; the reduction keeps
// the reference's order (the first nonce wins a tie).
func (p Params) tailsSearch(src ItemSource, hh [32]byte, height, nonce, count uint64, target [32]byte) (minNonce uint64, minHash [32]byte, shares uint64, err error) {
	type part struct {
		minNonce uint64
		minHash  [32]byte
		shares   uint64
		err      error
	}
	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	if uint64(workers) > count {
		workers = int(count)
	}
	parts := make([]part, workers)
	var wg sync.WaitGroup
	chunk := count / uint64(workers)
	for w := 0; w < workers; w++ {
		lo := nonce + uint64(w)*chunk
		hi := lo + chunk
		if w == workers-1 {
			hi = nonce + count
		}
		wg.Add(1)
		go func(w int, lo, hi uint64) {
			defer wg.Done()
			var best [32]byte
			for i := range best {
				best[i] = 0xff
			}
			bestN, n := uint64(0), uint64(0)
			var sk [32]byte
			if p.Keyed() {
				sk = TestSK(hh)
			}
			for x := lo; x < hi; x++ {
				var st Stages
				var e error
				if p.Keyed() {
					st, e = p.ComputeKeyed(src, hh, height, x, &sk, nil, false)
				} else {
					st, e = p.Compute(src, hh, height, x, false)
				}
				if e != nil {
					parts[w] = part{err: e}
					return
				}
				if lessOrEqual(st.Digest, target) {
					n++
				}
				if lessStrict(st.Digest, best) {
					best, bestN = st.Digest, x
				}
			}
			parts[w] = part{minNonce: bestN, minHash: best, shares: n}
		}(w, lo, hi)
	}
	wg.Wait()
	for i := range minHash {
		minHash[i] = 0xff
	}
	for _, pt := range parts {
		if pt.err != nil {
			return 0, minHash, 0, pt.err
		}
		shares += pt.shares
		if lessStrict(pt.minHash, minHash) {
			minHash, minNonce = pt.minHash, pt.minNonce
		}
	}
	return minNonce, minHash, shares, nil
}

// lessStrict is uint256be(a) < uint256be(b).
func lessStrict(a, b [32]byte) bool {
	for i := 0; i < 32; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// FPRow is one row of the fp class: out = V8FOp(x, y, z) without an opcode,
// V8FOpWide(op, x, y, z) with one.
type FPRow struct {
	Op  *uint32 `json:"op,omitempty"`
	X   uint32  `json:"x"`
	Y   uint32  `json:"y"`
	Z   uint32  `json:"z"`
	Out uint32  `json:"out"`
}

// sharesFromHex splits the concatenated shares of a row.
func sharesFromHex(s string, n uint32) ([][32]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != int(n)*32 {
		return nil, fmt.Errorf("want %d shares of 32 bytes, got %d bytes", n, len(b))
	}
	out := make([][32]byte, n)
	for i := range out {
		copy(out[i][:], b[32*i:])
	}
	return out, nil
}

func hexShares(s [][32]byte) string {
	out := make([]byte, 0, 32*len(s))
	for i := range s {
		out = append(out, s[i][:]...)
	}
	return hex.EncodeToString(out)
}

type Expect struct {
	ParamsHash    string       `json:"paramsHash,omitempty"` // P
	Seed          string       `json:"seed,omitempty"`       // S
	Epoch         *uint64      `json:"epoch,omitempty"`
	EpochSeed     string       `json:"epochSeed,omitempty"`     // E
	DatasetID     string       `json:"datasetId,omitempty"`     // D
	DatasetDigest string       `json:"datasetDigest,omitempty"` // BLAKE3 over the whole dataset (diagnostic)
	CacheDigest   string       `json:"cacheDigest,omitempty"`   // version 1: BLAKE3 over the cache in item order
	Trace         *TraceExpect `json:"trace,omitempty"`
	Mix           string       `json:"mix,omitempty"`
	Digest        string       `json:"digest,omitempty"`
}

// LoadVectors reads a vector file.
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

func hex32(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, err
	}
	if len(b) != 32 {
		return out, fmt.Errorf("want 32 bytes, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}

// Divergence names the first stage and byte at which a computed result
// differs from an expectation.
type Divergence struct {
	Vector string
	Stage  string // "paramsHash", "seed", "epoch", "epochSeed", "datasetId", "scratchInit", "round N", "mix", "digest", "valid"
	Byte   int    // -1 when the stage is not byte-valued
	Have   string
	Want   string
}

func (d Divergence) String() string {
	if d.Byte >= 0 {
		return fmt.Sprintf("%s: %s differs at byte %d: have %s want %s", d.Vector, d.Stage, d.Byte, d.Have, d.Want)
	}
	return fmt.Sprintf("%s: %s differs: have %s want %s", d.Vector, d.Stage, d.Have, d.Want)
}

func firstDiff(a, b []byte) int {
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	if len(b) > len(a) {
		return len(a)
	}
	return -1
}

// firstDiffHex is the first differing byte of two hex strings, or -1.
func firstDiffHex(a, b string) int {
	ab, err1 := hex.DecodeString(a)
	bb, err2 := hex.DecodeString(b)
	if err1 != nil || err2 != nil {
		return -1
	}
	return firstDiff(ab, bb)
}

func cmpStage(name, vector string, have [32]byte, want string) *Divergence {
	if want == "" {
		return nil
	}
	w, err := hex32(want)
	if err != nil {
		return &Divergence{Vector: vector, Stage: name, Byte: -1, Have: hex.EncodeToString(have[:]), Want: want + " (unparseable)"}
	}
	if i := firstDiff(have[:], w[:]); i >= 0 {
		return &Divergence{Vector: vector, Stage: name, Byte: i, Have: hex.EncodeToString(have[:]), Want: want}
	}
	return nil
}

// Check computes every vector in the file (light item sources, cached by
// epoch) and returns the first divergence per vector, in stage order.
// FullDatasets makes the dataset class's diagnostic digest run for sizes
// above 256 MiB (it materialises the dataset).
var FullDatasets = os.Getenv("IGNEUM_FULL_DATASETS") != ""

func (vf *VectorFile) Check() (results []Stages, divs []Divergence, err error) {
	p := vf.Params
	if err := p.Validate(); err != nil {
		return nil, nil, err
	}
	lights := map[uint64]*Light{}
	light := func(epoch uint64) *Light {
		src, ok := lights[epoch]
		if !ok {
			src = p.NewLight(epoch)
			lights[epoch] = src
		}
		return src
	}
	for _, v := range vf.Vectors {
		if v.Skip != "" {
			results = append(results, Stages{})
			continue
		}
		e := v.Expect
		if v.FP != nil {
			// the fp class: the floating forms on raw words (section 0.8, bits 4 and 5)
			var got uint32
			if v.FP.Op == nil {
				got = V8FOp(v.FP.X, v.FP.Y, v.FP.Z)
			} else {
				got = V8FOpWide(*v.FP.Op, v.FP.X, v.FP.Y, v.FP.Z)
			}
			results = append(results, Stages{})
			if got != v.FP.Out {
				divs = append(divs, Divergence{Vector: v.Name, Stage: "fp out", Byte: -1, Have: fmt.Sprint(got), Want: fmt.Sprint(v.FP.Out)})
			}
			continue
		}
		if v.Tails {
			hh, err := hex32(v.HeaderHash)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: headerHash: %w", v.Name, err)
			}
			tgt, err := hex32(v.ShareTarget)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: share_target: %w", v.Name, err)
			}
			minNonce, minHash, shares, err := p.tailsSearch(light(p.EpochOf(v.Height)), hh, v.Height, v.Nonce, v.NonceCount, tgt)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", v.Name, err)
			}
			results = append(results, Stages{Digest: minHash})
			mh := hex.EncodeToString(minHash[:])
			switch {
			case minNonce != v.MinNonce:
				divs = append(divs, Divergence{Vector: v.Name, Stage: "tails min nonce", Byte: -1, Have: fmt.Sprint(minNonce), Want: fmt.Sprint(v.MinNonce)})
			case mh != v.MinHash:
				divs = append(divs, Divergence{Vector: v.Name, Stage: "tails min hash", Byte: firstDiffHex(mh, v.MinHash), Have: mh, Want: v.MinHash})
			case shares != v.ShareCount:
				divs = append(divs, Divergence{Vector: v.Name, Stage: "tails share count", Byte: -1, Have: fmt.Sprint(shares), Want: fmt.Sprint(v.ShareCount)})
			}
			continue
		}
		if v.Doc != "" {
			// params-rule and registers-rule: the parse verdict on the document under test, ok or params
			reason := "ok"
			if _, err := ParamsFromDocument([]byte(v.Doc)); err != nil {
				reason = "params"
			}
			results = append(results, Stages{})
			if reason != v.Reason {
				divs = append(divs, Divergence{Vector: v.Name, Stage: "params verdict (" + v.Note + ")", Byte: -1, Have: reason, Want: v.Reason})
			}
			continue
		}
		if v.DatasetOnly {
			var st Stages
			epoch := p.EpochOf(v.Height)
			if e.Epoch != nil {
				epoch = *e.Epoch
			}
			st.Epoch = epoch
			st.EpochSeed = p.EpochSeed(epoch)
			st.DatasetID = p.DatasetID(st.EpochSeed)
			results = append(results, st)
			var d *Divergence
			switch {
			case cmpStage("epochSeed", v.Name, st.EpochSeed, e.EpochSeed) != nil:
				d = cmpStage("epochSeed", v.Name, st.EpochSeed, e.EpochSeed)
			case cmpStage("datasetId", v.Name, st.DatasetID, e.DatasetID) != nil:
				d = cmpStage("datasetId", v.Name, st.DatasetID, e.DatasetID)
			case e.CacheDigest != "" && p.Version == Version1 && cmpStage("cacheDigest", v.Name, light(epoch).CacheDigest(), e.CacheDigest) != nil:
				d = cmpStage("cacheDigest", v.Name, light(epoch).CacheDigest(), e.CacheDigest)
			case e.DatasetDigest != "" && (p.DatasetBytes <= 256<<20 || FullDatasets):
				ds, err := p.Generate(epoch)
				if err != nil {
					return nil, nil, err
				}
				d = cmpStage("datasetDigest", v.Name, ds.Digest(), e.DatasetDigest)
			}
			if d != nil {
				divs = append(divs, *d)
			}
			continue
		}
		hh, err := hex32(v.HeaderHash)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: headerHash: %w", v.Name, err)
		}
		if v.Form != "" {
			// key-forms (section 0.9): the keyed entry's deterministic refusals on a keyed document
			if !p.Keyed() {
				return nil, nil, fmt.Errorf("%s: key-forms needs a bit 10 document", v.Name)
			}
			sk := TestSK(hh)
			src := light(p.EpochOf(v.Height))
			want, err := hex32(e.Mix)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: mix: %w", v.Name, err)
			}
			wantF, err := hex32(e.Digest)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: hash: %w", v.Name, err)
			}
			var reason string
			switch v.Form {
			case "keyless":
				if _, err := p.Compute(src, hh, v.Height, v.Nonce, false); errors.Is(err, ErrKeyed) {
					reason = "arg"
				} else {
					reason = "computed"
				}
			case "null-shares":
				if _, err := p.ComputeKeyed(src, hh, v.Height, v.Nonce, nil, nil, false); errors.Is(err, ErrKeyed) {
					reason = "arg"
				} else {
					reason = "computed"
				}
			case "wrong-sk":
				bad := sk
				bad[0] ^= 1
				st, err := p.ComputeKeyed(src, hh, v.Height, v.Nonce, &bad, nil, false)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", v.Name, err)
				}
				if st.Mix != want && st.Digest != wantF {
					reason = "mix"
				} else {
					reason = "same"
				}
			case "flipped-share":
				st, err := p.ComputeKeyed(src, hh, v.Height, v.Nonce, &sk, nil, false)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", v.Name, err)
				}
				if st.Mix != want {
					reason = "true row differs"
					break
				}
				flat := make([]byte, 0, 32*len(st.Shares))
				for i := range st.Shares {
					flat = append(flat, st.Shares[i][:]...)
				}
				at := 40
				if at > len(flat)-1 {
					at = len(flat) - 1
				}
				flat[at] ^= 1
				flipped := make([][32]byte, len(st.Shares))
				for i := range flipped {
					copy(flipped[i][:], flat[32*i:])
				}
				st2, err := p.ComputeKeyed(src, hh, v.Height, v.Nonce, nil, flipped, false)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", v.Name, err)
				}
				if st2.Mix != want && st2.Digest != wantF {
					reason = "mix"
				} else {
					reason = "same"
				}
			default:
				return nil, nil, fmt.Errorf("%s: key form %q", v.Name, v.Form)
			}
			results = append(results, Stages{})
			if reason != v.Reason {
				divs = append(divs, Divergence{Vector: v.Name, Stage: "key form " + v.Form, Byte: -1, Have: reason, Want: v.Reason})
			}
			continue
		}
		if v.Reason != "" {
			// the malformed class: the verifier's first failure in section 8's order
			ctx := v.Height
			if v.CtxHeight != nil {
				ctx = *v.CtxHeight
			}
			var st Stages
			var reason string
			switch {
			case v.Reason == "height":
				// the reference ABI's height cap (2^31 - 1) is not consensus (section 8); not a Go-side rule
				reason = "height"
			case p.EpochOf(ctx) != p.EpochOf(v.Height):
				reason = "epoch"
			default:
				if p.Keyed() {
					// a keyed document: the verifier's replay from the row's shares, then section 8's order
					given, err := sharesFromHex(v.Shares, p.KeyedRounds)
					if err != nil {
						return nil, nil, fmt.Errorf("%s: keyed_shares: %w", v.Name, err)
					}
					st, err = p.ComputeKeyed(light(p.EpochOf(ctx)), hh, v.Height, v.Nonce, nil, given, false)
				} else {
					st, err = p.Compute(light(p.EpochOf(ctx)), hh, v.Height, v.Nonce, false)
				}
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", v.Name, err)
				}
				given, err := hex32(v.MixGiven)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: mix: %w", v.Name, err)
				}
				tgt, err := hex32(v.Target)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: boundary: %w", v.Name, err)
				}
				switch {
				case st.Mix != given:
					reason = "mix"
				case !lessOrEqual(st.Digest, tgt):
					reason = "boundary"
				default:
					reason = "ok"
				}
			}
			results = append(results, st)
			if reason != v.Reason {
				divs = append(divs, Divergence{Vector: v.Name, Stage: "verify reason", Byte: -1, Have: reason, Want: v.Reason})
			}
			continue
		}
		epoch := p.EpochOf(v.Height)
		var st Stages
		if p.Keyed() {
			// a keyed document's rows (unseen, nonce-boundary, transitions, trace, keyed) are the prover's path under the
			// test secret sk = BLAKE3("IgneumHash/R0A/test-sk" || header); the keyed class carries it and the shares
			sk := TestSK(hh)
			if v.TestSK != "" {
				if given, err := hex32(v.TestSK); err == nil && given != sk {
					divs = append(divs, Divergence{Vector: v.Name, Stage: "test_sk", Byte: -1, Have: hex.EncodeToString(sk[:]), Want: v.TestSK})
					results = append(results, Stages{})
					continue
				}
			}
			st, err = p.ComputeKeyed(light(epoch), hh, v.Height, v.Nonce, &sk, nil, e.Trace != nil)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", v.Name, err)
			}
			if v.Shares != "" {
				// the keyed class: the prover's shares and messages, then the verifier's replay from the shares alone
				if hs := hexShares(st.Shares); hs != v.Shares {
					divs = append(divs, Divergence{Vector: v.Name, Stage: "shares", Byte: firstDiffHex(hs, v.Shares), Have: hs, Want: v.Shares})
					results = append(results, st)
					continue
				}
				if v.M != "" {
					if hm := hexShares(st.M); hm != v.M {
						divs = append(divs, Divergence{Vector: v.Name, Stage: "m", Byte: firstDiffHex(hm, v.M), Have: hm, Want: v.M})
						results = append(results, st)
						continue
					}
				}
				replay, err := p.ComputeKeyed(light(epoch), hh, v.Height, v.Nonce, nil, st.Shares, false)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: replay: %w", v.Name, err)
				}
				if replay.Mix != st.Mix || replay.Digest != st.Digest {
					divs = append(divs, Divergence{Vector: v.Name, Stage: "replay mix", Byte: -1, Have: hex.EncodeToString(replay.Mix[:]), Want: hex.EncodeToString(st.Mix[:])})
					results = append(results, st)
					continue
				}
			}
		} else {
			st, err = p.Compute(light(epoch), hh, v.Height, v.Nonce, e.Trace != nil)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", v.Name, err)
			}
		}
		results = append(results, st)
		var d *Divergence
		switch {
		case cmpStage("paramsHash", v.Name, st.ParamsHash, e.ParamsHash) != nil:
			d = cmpStage("paramsHash", v.Name, st.ParamsHash, e.ParamsHash)
		case cmpStage("seed", v.Name, st.Seed, e.Seed) != nil:
			d = cmpStage("seed", v.Name, st.Seed, e.Seed)
		case e.Epoch != nil && *e.Epoch != st.Epoch:
			d = &Divergence{Vector: v.Name, Stage: "epoch", Byte: -1, Have: fmt.Sprint(st.Epoch), Want: fmt.Sprint(*e.Epoch)}
		case cmpStage("epochSeed", v.Name, st.EpochSeed, e.EpochSeed) != nil:
			d = cmpStage("epochSeed", v.Name, st.EpochSeed, e.EpochSeed)
		case cmpStage("datasetId", v.Name, st.DatasetID, e.DatasetID) != nil:
			d = cmpStage("datasetId", v.Name, st.DatasetID, e.DatasetID)
		}
		if d == nil && e.Trace != nil {
			d = cmpTrace(v.Name, st.Trace, e.Trace)
		}
		if d == nil {
			d = cmpStage("mix", v.Name, st.Mix, e.Mix)
		}
		if d == nil {
			d = cmpStage("digest", v.Name, st.Digest, e.Digest)
		}
		if d == nil && v.Valid != nil && (v.Target != "" || v.Difficulty != "") {
			var tgt [32]byte
			if v.Difficulty != "" {
				diff, ok := new(big.Int).SetString(v.Difficulty, 10)
				if !ok {
					return nil, nil, fmt.Errorf("%s: difficulty %q", v.Name, v.Difficulty)
				}
				tgt, ok = Target(diff)
				if !ok {
					tgt = [32]byte{}
				}
			} else if tgt, err = hex32(v.Target); err != nil {
				return nil, nil, fmt.Errorf("%s: target: %w", v.Name, err)
			}
			valid := lessOrEqual(st.Digest, tgt)
			if valid != *v.Valid {
				d = &Divergence{Vector: v.Name, Stage: "valid", Byte: -1, Have: fmt.Sprint(valid), Want: fmt.Sprint(*v.Valid)}
			}
		}
		if d != nil {
			divs = append(divs, *d)
		}
	}
	return results, divs, nil
}

// Fill writes this implementation's stages into the file's expectations
// (for publishing the Go side's vectors so the reference can diff against
// them as well). withTrace includes the section 4 intermediates.
func (vf *VectorFile) Fill(results []Stages, withTrace bool) {
	for i := range vf.Vectors {
		st := results[i]
		e := &vf.Vectors[i].Expect
		e.ParamsHash = hex.EncodeToString(st.ParamsHash[:])
		e.Seed = hex.EncodeToString(st.Seed[:])
		ep := st.Epoch
		e.Epoch = &ep
		e.EpochSeed = hex.EncodeToString(st.EpochSeed[:])
		e.DatasetID = hex.EncodeToString(st.DatasetID[:])
		e.Mix = hex.EncodeToString(st.Mix[:])
		e.Digest = hex.EncodeToString(st.Digest[:])
		if len(st.Shares) > 0 {
			vf.Vectors[i].Shares = hexShares(st.Shares)
			vf.Vectors[i].M = hexShares(st.M)
			if vf.Vectors[i].TestSK == "" {
				if hh, err := hex32(vf.Vectors[i].HeaderHash); err == nil {
					sk := TestSK(hh)
					vf.Vectors[i].TestSK = hex.EncodeToString(sk[:])
				}
			}
		}
		if vf.Vectors[i].Difficulty != "" && vf.Vectors[i].Valid == nil {
			if diff, ok := new(big.Int).SetString(vf.Vectors[i].Difficulty, 10); ok {
				tgt, ok := Target(diff)
				valid := ok && lessOrEqual(st.Digest, tgt)
				vf.Vectors[i].Valid = &valid
			}
		}
		if withTrace && st.Trace != nil {
			te := &TraceExpect{Init: regsOut(st.Trace.AfterInit), Fill: regsOut(st.Trace.AfterFill)}
			for r := range st.Trace.Rounds {
				idx := uint64(st.Trace.FirstIdx)
				if r > 0 {
					idx = uint64(st.Trace.RoundIdx[r-1])
				}
				tr := TraceRound{R: uint32(r), Idx: idx, Regs: regsOut(st.Trace.Rounds[r])}
				if r < len(st.Trace.H) {
					h := st.Trace.H[r]
					tr.H, tr.Item = &h, st.Trace.Items[r]
				}
				if r < len(st.Trace.Block) {
					tr.Block, tr.Ops = regsOut(st.Trace.Block[r]), st.Trace.Ops[r]
					if st.Trace.Shares[r] != nil {
						tr.Share = hex.EncodeToString(st.Trace.Shares[r])
					}
				}
				te.Rounds = append(te.Rounds, tr)
			}
			for l := range st.Trace.Fold {
				te.Fold = append(te.Fold, st.Trace.Fold[l][:])
			}
			e.Trace = te
		}
	}
}

func regsOut(regs [][LaneWords]uint32) [][]uint32 {
	out := make([][]uint32, len(regs))
	for l := range regs {
		out[l] = append([]uint32(nil), regs[l][:]...)
	}
	return out
}

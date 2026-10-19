// Copyright 2026 The Igneum Authors
// This file is part of go-ethereum (Igneum fork).
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/consensus/igneum/igneumhash"
)

// reffill IN.json OUT.json: reads one of the reference's vector files
// (pow/vectors/igneumhash/<tag>/<class>.json), computes every row with
// this implementation and writes a file in the REFERENCE's own line form
// (the header lines copied from IN, every row's results this side's), so
// pow/ref's `ignpow-vectors check igneumhash OUT.json` reads it as it reads
// its own output: the second implementation checked by the first's tool,
// exact or nothing (the admission rule, docs/igneumhash/succession/ADMISSION.md).
// Classes: unseen, nonce-boundary, transitions, trace (the mix and hash;
// the trace object written in the reference's key form), keyed (the test
// secret, the shares and the messages), fp (every row's out). The verdict
// classes (malformed, key-forms, params-rule, registers-rule, dataset,
// tails) are checked by `check` and are not refilled.
func reffill(args []string) {
	if len(args) < 2 {
		usage()
	}
	in, out := args[0], args[1]
	raw, err := os.ReadFile(in)
	if err != nil {
		fail(err)
	}
	vf, err := igneumhash.LoadReference(in)
	if err != nil {
		fail(err)
	}
	cls := vf.Class
	switch cls {
	case "unseen", "nonce-boundary", "transitions", "trace", "keyed", "fp", "tails":
	default:
		fail(fmt.Errorf("reffill: class %q is a verdict class; use check", cls))
	}
	withTrace := cls == "trace"
	if withTrace {
		for i := range vf.Vectors {
			if vf.Vectors[i].Expect.Trace == nil {
				vf.Vectors[i].Expect.Trace = &igneumhash.TraceExpect{}
			}
		}
	}
	results, divs, err := vf.Check()
	if err != nil {
		fail(err)
	}
	// the header lines of the source file, up to and including the "vectors": [ line (or the fp arrays' start)
	var header []string
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 1<<20), 1<<28)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "  \"vectors\": [") || (cls == "fp" && strings.HasPrefix(line, "  \"edge\": [")) {
			break
		}
		header = append(header, line)
	}
	w, err := os.Create(out)
	if err != nil {
		fail(err)
	}
	defer w.Close()
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	for _, h := range header {
		fmt.Fprintln(bw, h)
	}
	if cls == "fp" {
		writeFP(bw, vf)
		fmt.Fprintf(bw, "  \"contract_differs\": 0,\n  \"wide_rows\": %d,\n  \"wide_contract_differs\": 0,\n  \"denormal_reachable\": false\n}\n", countWide(vf))
		fmt.Fprintf(os.Stderr, "reffill %s: %d rows, %d divergences against the source\n", cls, len(vf.Vectors), len(divs))
		return
	}
	fmt.Fprintln(bw, "  \"vectors\": [")
	for i, v := range vf.Vectors {
		st := results[i]
		if cls == "tails" {
			// this side's search result in the reference's row form (Check ran the search; the stage carries the minimum)
			minNonce, shares := v.MinNonce, v.ShareCount
			for _, d := range divs {
				if d.Vector == v.Name {
					minNonce, shares = 0, 0 // a divergence is printed by the reference's check, never hidden by copying the source
				}
			}
			fmt.Fprintf(bw, "    {\"height\": %d, \"header\": \"%s\", \"nonce\": \"%d\", \"nonce_count\": \"%d\", \"share_target\": \"%s\", \"min_nonce\": \"%d\", \"min_hash\": \"%s\", \"shares\": \"%d\"}",
				v.Height, v.HeaderHash, v.Nonce, v.NonceCount, v.ShareTarget, minNonce, hex.EncodeToString(st.Digest[:]), shares)
			if i+1 < len(vf.Vectors) {
				fmt.Fprint(bw, ",")
			}
			fmt.Fprintln(bw)
			continue
		}
		fmt.Fprintf(bw, "    {\"height\": %d, \"header\": \"%s\", \"nonce\": \"%d\", \"mix\": \"%s\", \"hash\": \"%s\"",
			v.Height, v.HeaderHash, v.Nonce, hex.EncodeToString(st.Mix[:]), hex.EncodeToString(st.Digest[:]))
		switch cls {
		case "trace":
			fmt.Fprintf(bw, ", \"trace\": %s", traceObject(vf.Params, v, st))
		case "keyed":
			sk := v.TestSK
			if sk == "" {
				if hh, err := hex.DecodeString(v.HeaderHash); err == nil && len(hh) == 32 {
					var h [32]byte
					copy(h[:], hh)
					s := igneumhash.TestSK(h)
					sk = hex.EncodeToString(s[:])
				}
			}
			fmt.Fprintf(bw, ", \"test_sk\": \"%s\", \"shares\": \"%s\", \"m\": \"%s\"", sk, flatHex(st.Shares), flatHex(st.M))
		}
		fmt.Fprint(bw, "}")
		if i+1 < len(vf.Vectors) {
			fmt.Fprint(bw, ",")
		}
		fmt.Fprintln(bw)
	}
	fmt.Fprintln(bw, "  ]\n}")
	fmt.Fprintf(os.Stderr, "reffill %s: %d rows, %d divergences against the source\n", cls, len(vf.Vectors), len(divs))
}

func flatHex(s [][32]byte) string {
	b := make([]byte, 0, 32*len(s))
	for i := range s {
		b = append(b, s[i][:]...)
	}
	return hex.EncodeToString(b)
}

func countWide(vf *igneumhash.VectorFile) int {
	n := 0
	for _, v := range vf.Vectors {
		if v.FP != nil && v.FP.Op != nil {
			n++
		}
	}
	return n
}

// writeFP writes the fp class's three arrays in the reference's line form, one row per line, out this side's.
func writeFP(bw *bufio.Writer, vf *igneumhash.VectorFile) {
	groups := []struct{ name, prefix string }{{"edge", "-fp-edge-"}, {"random", "-fp-random-"}, {"wide", "-fp-wide-"}}
	for gi, g := range groups {
		fmt.Fprintf(bw, "  \"%s\": [\n", g.name)
		var rows []igneumhash.Vector
		for _, v := range vf.Vectors {
			if v.FP != nil && strings.Contains(v.Name, g.prefix) {
				rows = append(rows, v)
			}
		}
		for i, v := range rows {
			var got uint32
			if v.FP.Op == nil {
				got = igneumhash.V8FOp(v.FP.X, v.FP.Y, v.FP.Z)
				fmt.Fprintf(bw, "    {\"x\": %d, \"y\": %d, \"z\": %d, \"out\": %d}", v.FP.X, v.FP.Y, v.FP.Z, got)
			} else {
				got = igneumhash.V8FOpWide(*v.FP.Op, v.FP.X, v.FP.Y, v.FP.Z)
				fmt.Fprintf(bw, "    {\"op\": %d, \"x\": %d, \"y\": %d, \"z\": %d, \"out\": %d}", *v.FP.Op, v.FP.X, v.FP.Y, v.FP.Z, got)
			}
			if i+1 < len(rows) {
				fmt.Fprint(bw, ",")
			}
			fmt.Fprintln(bw)
		}
		if gi+1 < len(groups) {
			fmt.Fprintln(bw, "  ],")
		} else {
			fmt.Fprintln(bw, "  ],")
		}
	}
}

// traceObject writes the trace of one row in the reference's key form (ignh_trace): S, E, P, [test_sk,] init, fill,
// rounds [{r, idx, [share,] h, block, ops, item, regs}], [fold,] M, F.
func traceObject(p igneumhash.Params, v igneumhash.Vector, st igneumhash.Stages) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "{\"S\": \"%s\", \"E\": \"%s\", \"P\": \"%s\", ", hex.EncodeToString(st.Seed[:]), hex.EncodeToString(st.EpochSeed[:]), hex.EncodeToString(st.ParamsHash[:]))
	if p.Keyed() {
		if hh, err := hex.DecodeString(v.HeaderHash); err == nil && len(hh) == 32 {
			var h [32]byte
			copy(h[:], hh)
			s := igneumhash.TestSK(h)
			fmt.Fprintf(&sb, "\"test_sk\": \"%s\", ", hex.EncodeToString(s[:]))
		}
	}
	tr := st.Trace
	regs := func(name string, R [][igneumhash.LaneWords]uint32) {
		fmt.Fprintf(&sb, "\"%s\": [", name)
		for l := range R {
			if l > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("[")
			for k := 0; k < igneumhash.LaneWords; k++ {
				if k > 0 {
					sb.WriteString(", ")
				}
				fmt.Fprintf(&sb, "%d", R[l][k])
			}
			sb.WriteString("]")
		}
		sb.WriteString("]")
	}
	regs("init", tr.AfterInit)
	sb.WriteString(", ")
	regs("fill", tr.AfterFill)
	sb.WriteString(", \"rounds\": [")
	for r := range tr.Rounds {
		idx := tr.FirstIdx
		if r > 0 {
			idx = tr.RoundIdx[r-1]
		}
		if r > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "{\"r\": %d, \"idx\": %d, ", r, idx)
		if r < len(tr.Shares) && tr.Shares[r] != nil {
			fmt.Fprintf(&sb, "\"share\": \"%s\", ", hex.EncodeToString(tr.Shares[r]))
		}
		if r < len(tr.H) {
			fmt.Fprintf(&sb, "\"h\": %d, ", tr.H[r])
		}
		if r < len(tr.Block) {
			regs("block", tr.Block[r])
			fmt.Fprintf(&sb, ", \"ops\": \"%s\", ", tr.Ops[r])
		}
		if r < len(tr.Items) {
			sb.WriteString("\"item\": [")
			for k, w := range tr.Items[r] {
				if k > 0 {
					sb.WriteString(", ")
				}
				fmt.Fprintf(&sb, "%d", w)
			}
			sb.WriteString("], ")
		}
		regs("regs", tr.Rounds[r])
		sb.WriteString("}")
	}
	sb.WriteString("]")
	if tr.Fold != nil {
		sb.WriteString(", \"fold\": [")
		for l := range tr.Fold {
			if l > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "[%d, %d, %d, %d]", tr.Fold[l][0], tr.Fold[l][1], tr.Fold[l][2], tr.Fold[l][3])
		}
		sb.WriteString("]")
	}
	fmt.Fprintf(&sb, ", \"M\": \"%s\", \"F\": \"%s\"}", hex.EncodeToString(st.Mix[:]), hex.EncodeToString(st.Digest[:]))
	return sb.String()
}

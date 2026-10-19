// Package forgeb1 is the second implementation of the hash team's Forge F1
// reference in Forge-B1's shape (docs/igneumhash/forge/F1-B1.md, text
// revision 2 at ca9f39a9: the commitment and challenge domains are protocol
// A's F0 revision 2 domains), written from that text alone, never from the
// reference's code. WorkF takes epoch_seed, fill_seed and transcript (32
// bytes each) and the parameter document; the protocol team's job, fill_seed
// derivation, tickets and transcript enter by name only. Research only;
// nothing here is a claim of security and nothing activates on a network.
//
// Reading R-1, settled in revision 2's section 6: for i_k = 1 the opened
// X[0] must equal the X[0] derived from fill_seed, its path is checked at
// position 0, the X[r] field and path are zero, and X[1] is recomputed from
// that X[0]; for i_k = 0 the verifier derives X[0] from fill_seed, requires
// both parent fields and paths zero, and checks only X[0]'s own path.
package forgeb1

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"

	"lukechampine.com/blake3"
)

const ID = "forge-b1"

const (
	Schema     uint32 = 1
	TestSchema uint32 = 0x54455354
	GraphDRS   uint32 = 0 // DRSample, the theorem-aligned control
	GraphBRG   uint32 = 1 // DRS+BRG, the engineering control
)

var ErrParams = errors.New("forge-b1: parameter record rejected")
var ErrProof = errors.New("forge-b1: proof rejected")

// Params is the parameter document of section 1.
type Params struct {
	Schema      uint32 `json:"schema"`
	Graph       uint32 `json:"graph"`
	MemoryBytes uint64 `json:"memory_bytes"`
	LabelBytes  uint32 `json:"label_bytes"`
	Challenges  uint32 `json:"challenges"`
	Batch       uint32 `json:"batch"`
}

// N is the node count M / L.
func (p Params) N() uint64 { return p.MemoryBytes / uint64(p.LabelBytes) }

// D is the tree depth log2 N.
func (p Params) D() uint32 { return uint32(bits.TrailingZeros64(p.N())) }

// Record is SCHEMA u32 || GRAPH u32 || M u64 || L u32 || q u32 || B u32, 28 little-endian bytes.
func (p Params) Record() []byte {
	out := make([]byte, 0, 28)
	out = binary.LittleEndian.AppendUint32(out, p.Schema)
	out = binary.LittleEndian.AppendUint32(out, p.Graph)
	out = binary.LittleEndian.AppendUint64(out, p.MemoryBytes)
	out = binary.LittleEndian.AppendUint32(out, p.LabelBytes)
	out = binary.LittleEndian.AppendUint32(out, p.Challenges)
	out = binary.LittleEndian.AppendUint32(out, p.Batch)
	return out
}

// PID is BLAKE3(record), no tag.
func (p Params) PID() [32]byte { return blake3.Sum256(p.Record()) }

// ProgramID is BLAKE3(tag("B1/program") || "forge-b1/v1" || pid), the string its raw 11 ASCII bytes.
func ProgramID(pid [32]byte) [32]byte { return h32(tag("B1/program"), []byte("forge-b1/v1"), pid[:]) }

// Variant is b1-g<GRAPH>-m<mib>-l<L>-q<q>-b<B>, prefixed "test-" for a test-only record.
func (p Params) Variant() string {
	t := ""
	if p.Schema == TestSchema {
		t = "test-"
	}
	return fmt.Sprintf("b1-%sg%d-m%d-l%d-q%d-b%d", t, p.Graph, p.MemoryBytes>>20, p.LabelBytes, p.Challenges, p.Batch)
}

func (p Params) validateShape() error {
	switch {
	case p.Schema != Schema && p.Schema != TestSchema:
		return fmt.Errorf("%w: schema %d", ErrParams, p.Schema)
	case p.Graph != GraphDRS && p.Graph != GraphBRG:
		return fmt.Errorf("%w: graph %d (0 DRSample, 1 DRS+BRG)", ErrParams, p.Graph)
	case p.LabelBytes != 128 && p.LabelBytes != 256 && p.LabelBytes != 512 && p.LabelBytes != 1024:
		return fmt.Errorf("%w: label_bytes %d (128, 256, 512 or 1024)", ErrParams, p.LabelBytes)
	case p.MemoryBytes%uint64(p.LabelBytes) != 0 || p.N() < 4 || p.N()&(p.N()-1) != 0:
		return fmt.Errorf("%w: memory_bytes %d is not a power-of-two count of labels (at least 4)", ErrParams, p.MemoryBytes)
	case p.Challenges == 0:
		return fmt.Errorf("%w: challenges 0", ErrParams)
	case p.Batch == 0:
		return fmt.Errorf("%w: batch 0", ErrParams)
	}
	return nil
}

// Validate admits the cells of section 1: 64 MiB, 256 MiB, 1 GiB, 2 GiB.
func (p Params) Validate() error {
	if err := p.validateShape(); err != nil {
		return err
	}
	if p.Schema == TestSchema {
		return fmt.Errorf("%w: test-only schema", ErrParams)
	}
	switch p.MemoryBytes {
	case 64 << 20, 256 << 20, 1 << 30, 2 << 30:
		return nil
	}
	return fmt.Errorf("%w: memory_bytes %d (64 MiB, 256 MiB, 1 GiB or 2 GiB)", ErrParams, p.MemoryBytes)
}

// ValidateTestOnly admits memory_bytes from 2^20 under the test schema.
func (p Params) ValidateTestOnly() error {
	if err := p.validateShape(); err != nil {
		return err
	}
	if p.Schema != TestSchema {
		return fmt.Errorf("%w: a test-only record carries schema 0x54455354", ErrParams)
	}
	if p.MemoryBytes < 1<<20 {
		return fmt.Errorf("%w: test_only admits memory_bytes from 2^20, got %d", ErrParams, p.MemoryBytes)
	}
	return nil
}

func tag(t string) []byte          { return append([]byte{byte(len(t))}, t...) }
func le32(v uint32) []byte         { return binary.LittleEndian.AppendUint32(nil, v) }
func le64(v uint64) []byte         { return binary.LittleEndian.AppendUint64(nil, v) }
func h32(parts ...[]byte) [32]byte { return blake3.Sum256(bytes.Join(parts, nil)) }

// xof is XOF(m, n); xofk is XOFk(key, m, n), the keyed form.
func xof(n int, parts ...[]byte) []byte {
	h := blake3.New(32, nil)
	h.Write(bytes.Join(parts, nil))
	out := make([]byte, n)
	h.XOF().Read(out)
	return out
}

func xofk(key [32]byte, n int, parts ...[]byte) []byte {
	h := blake3.New(32, key[:])
	h.Write(bytes.Join(parts, nil))
	out := make([]byte, n)
	h.XOF().Read(out)
	return out
}

// drsParent is DRSample's draw for the 1-based u >= 3 over the epoch seed
// (section 2, GRAPH 0): the 1-based parent, clamped to 1.
func drsParent(epochSeed [32]byte, v uint64) uint64 {
	u := v + 1
	h := h32(tag("B1/edge"), epochSeed[:], le64(v))
	u1 := binary.LittleEndian.Uint64(h[0:8])
	u2 := binary.LittleEndian.Uint64(h[8:16])
	logu := uint64(bits.Len64(u) - 1)
	gp := 1 + u1%(logu+1)
	g := uint64(1) << gp
	if g > u {
		g = u
	}
	lo := g / 2
	if lo < 2 {
		lo = 2
	}
	rr := lo + u2%(g-lo+1)
	if rr >= u {
		return 1
	}
	return u - rr
}

// reverseBits reverses the m low bits of b.
func reverseBits(b uint64, m uint32) uint64 {
	var out uint64
	for i := uint32(0); i < m; i++ {
		if b&(1<<i) != 0 {
			out |= 1 << (m - 1 - i)
		}
	}
	return out
}

// Parent is r(v), the second parent of node v >= 2 (the first is v - 1): a
// function of epoch_seed, the graph, N and v only. Node 1's parent is 0.
func Parent(p Params, epochSeed [32]byte, v uint64) uint64 {
	if v < 2 {
		return 0
	}
	n := p.N()
	if p.Graph == GraphBRG && v >= n/2 {
		return reverseBits(v-n/2, uint32(bits.TrailingZeros64(n/2)))
	}
	return drsParent(epochSeed, v) - 1
}

// Labels is one fill: N labels of L bytes, contiguous, with its tree once committed.
type Labels struct {
	P         Params
	EpochSeed [32]byte
	FillSeed  [32]byte
	Data      []byte
	nodes     [][32]byte // heap order: root at 0, leaf v at N - 1 + v
}

func (l *Labels) Label(v uint64) []byte {
	lb := uint64(l.P.LabelBytes)
	return l.Data[v*lb : (v+1)*lb]
}

// Label0 is X[0] = XOFk(fill_seed, tag("B1/label0") || LE32(SCHEMA) || LE32(GRAPH), L).
func Label0(p Params, fillSeed [32]byte) []byte {
	return xofk(fillSeed, int(p.LabelBytes), tag("B1/label0"), le32(p.Schema), le32(p.Graph))
}

// Label1 is X[1] = XOFk(fill_seed, tag("B1/label") || LE32(SCHEMA) || LE64(1) || X[0], L).
func Label1(p Params, fillSeed [32]byte, x0 []byte) []byte {
	return xofk(fillSeed, int(p.LabelBytes), tag("B1/label"), le32(p.Schema), le64(1), x0)
}

// LabelFrom is X[v] = XOFk(fill_seed, tag("B1/label") || LE32(SCHEMA) || LE64(v) || X[v-1] || X[r(v)], L) for v >= 2.
func LabelFrom(p Params, fillSeed [32]byte, v uint64, prev, parent []byte) []byte {
	return xofk(fillSeed, int(p.LabelBytes), tag("B1/label"), le32(p.Schema), le64(v), prev, parent)
}

// Fill computes the fresh labels of (epoch_seed, fill_seed) (section 3).
func Fill(p Params, epochSeed, fillSeed [32]byte) *Labels {
	l := &Labels{P: p, EpochSeed: epochSeed, FillSeed: fillSeed}
	n := p.N()
	lb := uint64(p.LabelBytes)
	l.Data = make([]byte, n*lb)
	copy(l.Data[0:lb], Label0(p, fillSeed))
	copy(l.Data[lb:2*lb], Label1(p, fillSeed, l.Data[0:lb]))
	for v := uint64(2); v < n; v++ {
		r := Parent(p, epochSeed, v)
		copy(l.Data[v*lb:(v+1)*lb], LabelFrom(p, fillSeed, v, l.Data[(v-1)*lb:v*lb], l.Data[r*lb:(r+1)*lb]))
	}
	return l
}

func Leaf(v uint64, x []byte) [32]byte   { return h32(tag("Igneum/Forge/leaf/v2"), le64(v), x) }
func Node(left, right [32]byte) [32]byte { return h32(tag("Igneum/Forge/node/v2"), left[:], right[:]) }

// Commit builds the tree of section 4 (heap order, root at 0) and returns the root.
func (l *Labels) Commit() [32]byte {
	if l.nodes != nil {
		return l.nodes[0]
	}
	n := l.P.N()
	l.nodes = make([][32]byte, 2*n-1)
	for v := uint64(0); v < n; v++ {
		l.nodes[n-1+v] = Leaf(v, l.Label(v))
	}
	for k := int64(n) - 2; k >= 0; k-- {
		l.nodes[k] = Node(l.nodes[2*k+1], l.nodes[2*k+2])
	}
	return l.nodes[0]
}

// Path is the D sibling nodes of leaf v from the leaf up.
func (l *Labels) Path(v uint64) []byte {
	l.Commit()
	n := l.P.N()
	out := make([]byte, 0, 32*l.P.D())
	for idx := n - 1 + v; idx > 0; idx = (idx - 1) / 2 {
		sib := idx + 1
		if idx%2 == 0 {
			sib = idx - 1
		}
		out = append(out, l.nodes[sib][:]...)
	}
	return out
}

func checkPath(p Params, root [32]byte, v uint64, leaf [32]byte, path []byte) bool {
	cur := leaf
	idx := p.N() - 1 + v
	for k := uint32(0); k < p.D(); k++ {
		var sib [32]byte
		copy(sib[:], path[32*k:32*k+32])
		if idx%2 == 1 { // a left child
			cur = Node(cur, sib)
		} else {
			cur = Node(sib, cur)
		}
		idx = (idx - 1) / 2
	}
	return cur == root
}

// Challenges is i_k = LE64(C[8k .. 8k + 8)) mod N with C = XOF(tag("Igneum/Forge/challenge/v2") || ticket_j, 8q):
// the one definition is F0 revision 2 section 7; the transcript input is the 32-byte ticket itself (section 5).
func Challenges(p Params, transcript [32]byte) []uint64 {
	c := xof(8*int(p.Challenges), tag("Igneum/Forge/challenge/v2"), transcript[:])
	out := make([]uint64, p.Challenges)
	for k := range out {
		out[k] = binary.LittleEndian.Uint64(c[8*k:]) % p.N()
	}
	return out
}

// ProofBytes is 32 + q x (8 + 2L + 3 x D x 32).
func (p Params) ProofBytes() int {
	return 32 + int(p.Challenges)*(8+2*int(p.LabelBytes)+3*int(p.D())*32)
}

// Prove writes the proof of section 6 for the transcript's challenges.
func (l *Labels) Prove(transcript [32]byte) []byte {
	root := l.Commit()
	lb := int(l.P.LabelBytes)
	pl := 32 * int(l.P.D())
	out := make([]byte, 0, l.P.ProofBytes())
	out = append(out, root[:]...)
	for _, i := range Challenges(l.P, transcript) {
		out = append(out, le64(i)...)
		switch {
		case i == 0:
			out = append(out, make([]byte, 2*lb+2*pl)...)
		case i == 1:
			out = append(out, l.Label(0)...)
			out = append(out, make([]byte, lb)...)
			out = append(out, l.Path(0)...)
			out = append(out, make([]byte, pl)...)
		default:
			r := Parent(l.P, l.EpochSeed, i)
			out = append(out, l.Label(i-1)...)
			out = append(out, l.Label(r)...)
			out = append(out, l.Path(i-1)...)
			out = append(out, l.Path(r)...)
		}
		out = append(out, l.Path(i)...)
	}
	return out
}

// Verify is section 6's verifier: program id first, the fixed length, the
// indices recomputed from the transcript, every unused field zero, every
// path and label. It returns the root the proof commits to.
func Verify(p Params, epochSeed, fillSeed, transcript, programID [32]byte, proof []byte) ([32]byte, error) {
	var zero [32]byte
	if programID != ProgramID(p.PID()) {
		return zero, fmt.Errorf("%w: program id is not this parameter set's", ErrParams)
	}
	if len(proof) != p.ProofBytes() {
		return zero, fmt.Errorf("%w: %d bytes, the layout has %d", ErrProof, len(proof), p.ProofBytes())
	}
	lb := int(p.LabelBytes)
	pl := 32 * int(p.D())
	var root [32]byte
	copy(root[:], proof[0:32])
	off := 32
	for k, want := range Challenges(p, transcript) {
		got := binary.LittleEndian.Uint64(proof[off : off+8])
		if got != want {
			return zero, fmt.Errorf("%w: challenge %d opens index %d, the transcript says %d", ErrProof, k, got, want)
		}
		i := want
		prev := proof[off+8 : off+8+lb]
		par := proof[off+8+lb : off+8+2*lb]
		pathPrev := proof[off+8+2*lb : off+8+2*lb+pl]
		pathPar := proof[off+8+2*lb+pl : off+8+2*lb+2*pl]
		pathI := proof[off+8+2*lb+2*pl : off+8+2*lb+3*pl]
		off += 8 + 2*lb + 3*pl
		var x []byte
		switch {
		case i == 0:
			if !isZero(prev) || !isZero(par) || !isZero(pathPrev) || !isZero(pathPar) {
				return zero, fmt.Errorf("%w: challenge %d (index 0) has non-zero unused fields", ErrProof, k)
			}
			x = Label0(p, fillSeed)
		case i == 1:
			if !isZero(par) || !isZero(pathPar) {
				return zero, fmt.Errorf("%w: challenge %d (index 1) has non-zero unused fields", ErrProof, k)
			}
			if !bytes.Equal(prev, Label0(p, fillSeed)) {
				return zero, fmt.Errorf("%w: challenge %d (index 1), the opened X[0] is not the fill seed's", ErrProof, k)
			}
			if !checkPath(p, root, 0, Leaf(0, prev), pathPrev) {
				return zero, fmt.Errorf("%w: challenge %d, the path of X[0] does not reach the root", ErrProof, k)
			}
			x = Label1(p, fillSeed, prev)
		default:
			r := Parent(p, epochSeed, i)
			if !checkPath(p, root, i-1, Leaf(i-1, prev), pathPrev) {
				return zero, fmt.Errorf("%w: challenge %d, the path of X[%d] does not reach the root", ErrProof, k, i-1)
			}
			if !checkPath(p, root, r, Leaf(r, par), pathPar) {
				return zero, fmt.Errorf("%w: challenge %d, the path of X[r(%d)] = X[%d] does not reach the root", ErrProof, k, i, r)
			}
			x = LabelFrom(p, fillSeed, i, prev, par)
		}
		if !checkPath(p, root, i, Leaf(i, x), pathI) {
			return zero, fmt.Errorf("%w: challenge %d, the path of X[%d] does not reach the root", ErrProof, k, i)
		}
	}
	return root, nil
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

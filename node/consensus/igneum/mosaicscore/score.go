// Package mosaicscore is protocol B's independent implementation of the
// Mosaic-VF Phase A consensus objects of docs/igneumhash/mosaic/PHASE-A-CONSENSUS.md
// (protocol-a/mosaic-a at dbc58fed) sections 4 and 5, written from that text
// alone, never from the protocol team's node/consensus/igneum/mosaic: the canonical
// intrinsic work, the lane normalisation, the scoring epochs, the integer
// epoch score, the chain score and the fork-choice order. For M10 (cheap-work
// normalisation and overflow) and M15 (client disagreement). Research only.
package mosaicscore

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
)

var (
	one      = big.NewInt(1)
	two256   = new(big.Int).Lsh(one, 256)
	max256   = new(big.Int).Sub(two256, one)
	two64    = new(big.Int).Lsh(one, 64)
	ErrRange = errors.New("mosaic: out of range")
)

// Weights are the consensus rationals A = ANum/ADen and e = ENum/EDen.
type Weights struct {
	ANum, ADen, ENum, EDen int64
}

// Research is the research point A = 5, e = 1/50.
var Research = Weights{ANum: 5, ADen: 1, ENum: 1, EDen: 50}

// Scale is A_den x e_den, the factor the integer score carries.
func (w Weights) Scale() *big.Int { return big.NewInt(w.ADen * w.EDen) }

// Target is T = floor((2^256 - 1) / D) for a difficulty D >= 1.
func Target(d *big.Int) (*big.Int, error) {
	if d.Sign() <= 0 {
		return nil, fmt.Errorf("%w: difficulty %s", ErrRange, d)
	}
	return new(big.Int).Div(max256, d), nil
}

// Work is work(T) = floor(2^256 / (T + 1)) for T in [1, 2^256 - 1]; T = 0 and
// T at or above 2^256 are rejected, never computed (section 4). The proof
// value is not an input.
func Work(t *big.Int) (*big.Int, error) {
	if t.Sign() <= 0 || t.Cmp(max256) > 0 {
		return nil, fmt.Errorf("%w: target %s", ErrRange, t)
	}
	return new(big.Int).Div(two256, new(big.Int).Add(t, one)), nil
}

// Work256 is the 256-bit form floor((2^256 - 1 - T) / (T + 1)) + 1, which
// must equal Work on every target in range; computed exactly here.
func Work256(t *big.Int) (*big.Int, error) {
	if t.Sign() <= 0 || t.Cmp(max256) > 0 {
		return nil, fmt.Errorf("%w: target %s", ErrRange, t)
	}
	a := new(big.Int).Sub(max256, t)
	b := new(big.Int).Add(t, one)
	return new(big.Int).Add(new(big.Int).Div(a, b), one), nil
}

// Fits256 reports whether the 256-bit form's intermediates a = 2^256 - 1 - T
// and b = T + 1 both lie below 2^256 for this target (the text's claim; it
// fails at T = 2^256 - 1, where b = 2^256: finding M-1, an M10 overflow).
func Fits256(t *big.Int) bool {
	a := new(big.Int).Sub(max256, t)
	b := new(big.Int).Add(t, one)
	return a.Sign() >= 0 && a.Cmp(two256) < 0 && b.Cmp(two256) < 0
}

// Normalise is norm = floor(work x 2^64 / ref), ref the lane's work at its
// target at the scoring epoch's first height (ref >= 1).
func Normalise(work, ref *big.Int) (*big.Int, error) {
	if ref.Sign() <= 0 || work.Sign() < 0 {
		return nil, fmt.Errorf("%w: work %s over reference %s", ErrRange, work, ref)
	}
	return new(big.Int).Div(new(big.Int).Mul(work, two64), ref), nil
}

// Epoch is r for a height: heights [r x L, (r + 1) x L).
func Epoch(height, l uint64) uint64 { return height / l }

// Score is the integer epoch score S' = A_num x e_den x min_i(W_i) + e_num x
// A_den x sum_i(W_i) over the lanes' normalised work of one epoch (section 5).
func Score(w Weights, lanes []*big.Int) (*big.Int, error) {
	if len(lanes) == 0 {
		return nil, fmt.Errorf("%w: no lanes", ErrRange)
	}
	min := new(big.Int).Set(lanes[0])
	sum := new(big.Int)
	for _, x := range lanes {
		if x.Sign() < 0 {
			return nil, fmt.Errorf("%w: negative lane work", ErrRange)
		}
		if x.Cmp(min) < 0 {
			min.Set(x)
		}
		sum.Add(sum, x)
	}
	s := new(big.Int).Mul(min, big.NewInt(w.ANum*w.EDen))
	s.Add(s, new(big.Int).Mul(sum, big.NewInt(w.ENum*w.ADen)))
	return s, nil
}

// ChainScore is the sum of S_r over the completed epochs and the partial one.
func ChainScore(w Weights, epochs [][]*big.Int) (*big.Int, error) {
	total := new(big.Int)
	for _, e := range epochs {
		s, err := Score(w, e)
		if err != nil {
			return nil, err
		}
		total.Add(total, s)
	}
	return total, nil
}

// Tip is a branch's tip for the fork choice.
type Tip struct {
	Score  *big.Int
	Height uint64
	Hash   [32]byte
}

// Compare is the total order of section 5: the greater chain score, then the
// greater height, then the lower tip hash bytewise. It returns 1 when a is
// chosen over b, -1 when b is, 0 only for identical tips.
func Compare(a, b Tip) int {
	if c := a.Score.Cmp(b.Score); c != 0 {
		return c
	}
	switch {
	case a.Height > b.Height:
		return 1
	case a.Height < b.Height:
		return -1
	}
	return -bytes.Compare(a.Hash[:], b.Hash[:])
}

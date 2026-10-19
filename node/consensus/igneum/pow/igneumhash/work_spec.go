// Copyright 2026 The core-geth Authors
// This file is part of the core-geth library.
//
// The core-geth library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The core-geth library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the core-geth library. If not, see <http://www.gnu.org/licenses/>.

package igneumhash

import (
	"encoding/binary"
	"math/bits"
	"sync"

	"lukechampine.com/blake3"
)

// specWork is WORK(S, E) = M of SPEC-R0A-v0.md revision 0.4, sections 3 and
// 4, written from the text alone (the project's default, 10 October 2026: a third
// implementation beside the C reference and protocol B's, checked against
// the hash team's vectors). Every value is a u32, little-endian from bytes;
// + and * wrap; rotates and shifts take their amount masked & 31; the item
// and scratch reductions are masks (both sizes are powers of two).
type specWork struct {
	// trace, when set (tests only), receives the intermediates the reference's
	// trace vectors record: "init" and "fill" with the registers, each round
	// with its item index and the registers after the exchange, "fold" with
	// the scratch fold.
	trace func(stage string, round uint32, idx uint32, R *[8][laneWords]uint32, fold *[8][4]uint32)
	// traceWide (tests only) receives version 4's round record: the item
	// index, the item word h, the item's words as read, and the registers
	// after the exchange.
	traceWide func(round uint32, idx uint32, h uint32, item []uint32, R *[8][laneWords]uint32)
}

// WideItemFunc derives a version 4 dataset item of the set's width into out.
type WideItemFunc func(i uint64, out []byte)

const (
	laneWords = 16
	lineWords = 4
	specMul   = 0x9E3779B1
	tagLanes  = "IgneumHash/R0A/lanes"
	tagMix    = "IgneumHash/R0A/mix"
)

func (specWork) Accesses(p Params) uint64 { return uint64(p.Rounds) }

func (w specWork) emit(stage string, round uint32, idx uint32, R *[8][laneWords]uint32, fold *[8][4]uint32) {
	if w.trace != nil {
		w.trace(stage, round, idx, R, fold)
	}
}

// mixScratch is one Mix's working memory (the lanes hasher, L, the scratch
// backing and its lane slices, the state and the final input), pooled so a
// verification allocates nothing after warm-up (the project's order of 20:4x UK,
// 12 October 2026); the buffers are grown to a parameter set's need once and
// kept. Not safe for concurrent use; the pool hands each Mix its own.
type mixScratch struct {
	h       *blake3.Hasher
	L       []byte
	backing []uint32
	SL      [][]uint32
	st      []byte
	in      []byte
	// the lane state, the lanes input, the item and the folds live here rather
	// than on Mix's stack: handed to the trace and item function values they
	// would escape to the heap once a proof each
	R     [8][laneWords]uint32
	lanes [1 + len(tagLanes) + 32]byte
	W     [ItemBytes]byte
	folds [8][4]uint32
}

var mixPool = sync.Pool{New: func() any { return &mixScratch{h: blake3.New(32, nil)} }}

func (s *mixScratch) grow(lanes, sliceWords int) {
	if cap(s.L) < 64*lanes {
		s.L = make([]byte, 64*lanes)
	}
	s.L = s.L[:64*lanes]
	if sliceWords > 0 {
		if cap(s.backing) < sliceWords*lanes {
			s.backing = make([]uint32, sliceWords*lanes)
		}
		s.backing = s.backing[:sliceWords*lanes]
		if cap(s.SL) < lanes {
			s.SL = make([][]uint32, lanes)
		}
		s.SL = s.SL[:lanes]
	}
	if cap(s.st) < 64*lanes+16*lanes {
		s.st = make([]byte, 0, 64*lanes+16*lanes)
	}
	s.st = s.st[:0]
	if cap(s.in) < 1+len(tagMix)+32+64*lanes+16*lanes {
		s.in = make([]byte, 0, 1+len(tagMix)+32+64*lanes+16*lanes)
	}
	s.in = s.in[:0]
}

func (w specWork) Mix(seed, epochSeed [32]byte, item ItemFunc, p Params) [32]byte {
	lanes := int(p.Lanes) // 8, fixed by Validate
	sliceWords := int(p.ScratchBytes) / (4 * lanes)
	sc := mixPool.Get().(*mixScratch)
	defer mixPool.Put(sc)
	sc.grow(lanes, sliceWords)

	// 4.1 Lane initialisation: L = BLAKE3xof(tag(T_LANES) || S, 64 * LANES).
	R := &sc.R
	*R = [8][laneWords]uint32{}
	{
		in := &sc.lanes
		in[0] = byte(len(tagLanes))
		copy(in[1:], tagLanes)
		copy(in[1+len(tagLanes):], seed[:])
		h := sc.h
		h.Reset()
		h.Write(in[:])
		L := sc.L
		h.XOF().Read(L)
		for l := 0; l < lanes; l++ {
			for k := 0; k < laneWords; k++ {
				R[l][k] = binary.LittleEndian.Uint32(L[64*l+4*k:])
			}
		}
	}

	w.emit("init", 0, 0, R, nil)

	// 4.2 Scratch fill (absent when SCRATCH_BYTES = 0).
	sliceLines := sliceWords / lineWords
	var SL [][]uint32
	if sliceWords > 0 {
		backing := sc.backing
		SL = sc.SL
		for l := 0; l < lanes; l++ {
			SL[l] = backing[l*sliceWords : (l+1)*sliceWords]
			for j := 0; j < sliceWords; j++ {
				a := R[l][j%laneWords]
				b := R[l][(j+5)%laneWords]
				a = (a ^ bits.RotateLeft32(b, 13)) * specMul
				a = a + (b >> 7)
				R[l][j%laneWords] = a
				SL[l][j] = a ^ R[l][(j+11)%laneWords]
			}
		}
	}

	w.emit("fill", 0, 0, R, nil)

	// 4.3 First address.
	itemMask := uint32(p.Items() - 1)
	idx := (R[0][0] ^ R[4][8]) & itemMask

	W := &sc.W
	var X [8][laneWords]uint32
	for r := uint32(0); r < p.Rounds; r++ {
		// 4.4 Item load and arithmetic.
		item(uint64(idx), W)
		for l := 0; l < lanes; l++ {
			for k := 0; k < 4; k++ {
				w := binary.LittleEndian.Uint32(W[4*(4*l+k):])
				a, b, c, d := R[l][4*k], R[l][4*k+1], R[l][4*k+2], R[l][4*k+3]
				a = a + w
				b = b ^ a
				c = c + b*specMul
				d = bits.RotateLeft32(d^c, -int(a&31))
				a = a ^ (d >> (w & 31))
				b = bits.RotateLeft32(b+d, 7)
				c = c ^ bits.RotateLeft32(a, 17)
				d = d + b*c
				R[l][4*k], R[l][4*k+1], R[l][4*k+2], R[l][4*k+3] = a, b, c, d
			}
			// The register permutation: R[l][k] = T[(5k + 1) mod 16].
			T := R[l]
			for k := 0; k < laneWords; k++ {
				R[l][k] = T[(5*k+1)%laneWords]
			}
		}

		// 4.5 Scratch access (absent without a scratchpad).
		if sliceWords > 0 {
			for l := 0; l < lanes; l++ {
				line := int(R[l][0] & uint32(sliceLines-1))
				for j := 0; j < 4; j++ {
					s := SL[l][4*line+j]
					R[l][8+j] = bits.RotateLeft32(R[l][8+j]^s, 5) + R[l][j]
					SL[l][4*line+j] = (s + R[l][12+j]) ^ R[l][8+j]
				}
			}
		}
		// 4.5 Exchange, with snapshot semantics.
		X = *R
		for l := 0; l < lanes; l++ {
			pp := l ^ (1 << (r % 3))
			q := (l + 1 + int(X[l][1]%7)) % 8
			R[l][0] = X[l][0] + X[pp][0]
			R[l][1] = X[l][1] ^ X[q][1]
			R[l][2] = X[l][2] + bits.RotateLeft32(X[pp][2], 11)
			R[l][3] = X[l][3] ^ bits.RotateLeft32(X[q][3], 23)
		}

		w.emit("round", r, idx, R, nil)

		// 4.6 Next address.
		g := int(r+1) % lanes
		idx = R[g][0] & itemMask
	}

	// 4.7 Finalisation: the scratch fold and the state bytes.
	st := sc.st
	for l := 0; l < lanes; l++ {
		for k := 0; k < laneWords; k++ {
			st = binary.LittleEndian.AppendUint32(st, R[l][k])
		}
	}
	if sliceWords > 0 {
		folds := &sc.folds
		*folds = [8][4]uint32{}
		for l := 0; l < lanes; l++ {
			var f [4]uint32
			for i := 0; i < sliceLines; i++ {
				for j := 0; j < 4; j++ {
					f[j] = bits.RotateLeft32(f[j]^SL[l][4*i+j], 9) + SL[l][4*i+(j+1)%4]
				}
			}
			for j := 0; j < 4; j++ {
				st = binary.LittleEndian.AppendUint32(st, f[j])
			}
			folds[l] = f
		}
		w.emit("fold", 0, 0, R, folds)
	}
	in := sc.in
	in = append(in, byte(len(tagMix)))
	in = append(in, tagMix...)
	in = append(in, seed[:]...)
	in = append(in, st...)
	return blake3.Sum256(in)
}

// MixWide is version 4's WORK (SPEC-R0A-v0.md section 0.4, the whole-item
// rule): 4.1 to 4.3 as Mix, then per round the W-byte item absorbed word by
// word into the registers (at 1024 bytes every register takes two words),
// the balanced domain-separated reduction tree to the item word h, the 4.4
// cell once per lane with w = h, the exchange, and the next address from
// R[g][0] ^ h. No scratchpad (version 4 fixes 0). The verifier reads the
// macro-item as one: a Riptide cell's split and fused miner forms verify to
// this one scalar reconstruction.
func (w specWork) MixWide(seed, epochSeed [32]byte, item WideItemFunc, p Params) [32]byte {
	lanes := int(p.Lanes)
	var R [8][laneWords]uint32
	{
		var in [1 + len(tagLanes) + 32]byte
		in[0] = byte(len(tagLanes))
		copy(in[1:], tagLanes)
		copy(in[1+len(tagLanes):], seed[:])
		h := blake3.New(32, nil)
		h.Write(in[:])
		L := make([]byte, 64*lanes)
		h.XOF().Read(L)
		for l := 0; l < lanes; l++ {
			for k := 0; k < laneWords; k++ {
				R[l][k] = binary.LittleEndian.Uint32(L[64*l+4*k:])
			}
		}
	}
	w.emit("init", 0, 0, &R, nil)
	w.emit("fill", 0, 0, &R, nil)
	itemMask := uint32(p.Items() - 1)
	idx := (R[0][0] ^ R[4][8]) & itemMask
	n := int(p.Width / 4)
	keyed := p.Version == Version6
	W := make([]byte, p.Width)
	words := make([]uint32, n)
	z := make([]uint32, n)
	var X [8][laneWords]uint32
	for r := uint32(0); r < p.Rounds; r++ {
		item(uint64(idx), W)
		for j := 0; j < n; j++ {
			words[j] = binary.LittleEndian.Uint32(W[4*j:])
		}
		// 2 (version 6, the keyed reduction: section 0.6): the leaves take the
		// absorb register's value as it stands before the absorb.
		if keyed {
			for j := 0; j < n; j++ {
				z[j] = words[j] ^ R[j%lanes][(j/lanes)%laneWords] ^ (uint32(j+1) * specMul)
			}
		} else {
			for j := 0; j < n; j++ {
				z[j] = words[j] ^ (uint32(j+1) * specMul)
			}
		}
		// 1. Absorb: every word into its own register.
		for j := 0; j < n; j++ {
			R[j%lanes][(j/lanes)%laneWords] += words[j]
		}
		// 2. The item word: the tree over the leaves.
		for m := n; m > 1; m /= 2 {
			for i := 0; i < m/2; i++ {
				z[i] = ((z[2*i] ^ bits.RotateLeft32(z[2*i+1], 13)) * specMul) + (z[2*i+1] >> 7)
			}
		}
		h := z[0]
		if keyed {
			// The finaliser (version 6).
			h ^= h >> 16
			h *= specMul
			h ^= h >> 15
		}
		// 3. The cell once per lane with w = h, then the permutation.
		for l := 0; l < lanes; l++ {
			for k := 0; k < 4; k++ {
				a, b, c, d := R[l][4*k], R[l][4*k+1], R[l][4*k+2], R[l][4*k+3]
				a = a + h
				b = b ^ a
				c = c + b*specMul
				d = bits.RotateLeft32(d^c, -int(a&31))
				a = a ^ (d >> (h & 31))
				b = bits.RotateLeft32(b+d, 7)
				c = c ^ bits.RotateLeft32(a, 17)
				d = d + b*c
				R[l][4*k], R[l][4*k+1], R[l][4*k+2], R[l][4*k+3] = a, b, c, d
			}
			T := R[l]
			for k := 0; k < laneWords; k++ {
				R[l][k] = T[(5*k+1)%laneWords]
			}
		}
		// 4. The exchange, with snapshot semantics.
		X = R
		for l := 0; l < lanes; l++ {
			pp := l ^ (1 << (r % 3))
			q := (l + 1 + int(X[l][1]%7)) % 8
			R[l][0] = X[l][0] + X[pp][0]
			R[l][1] = X[l][1] ^ X[q][1]
			R[l][2] = X[l][2] + bits.RotateLeft32(X[pp][2], 11)
			R[l][3] = X[l][3] ^ bits.RotateLeft32(X[q][3], 23)
		}
		if w.traceWide != nil {
			w.traceWide(r, idx, h, words, &R)
		}
		// 5. The next address: the item word reaches it directly.
		g := int(r+1) % lanes
		idx = (R[g][0] ^ h) & itemMask
	}
	st := make([]byte, 0, 64*lanes)
	for l := 0; l < lanes; l++ {
		for k := 0; k < laneWords; k++ {
			st = binary.LittleEndian.AppendUint32(st, R[l][k])
		}
	}
	in := make([]byte, 0, 1+len(tagMix)+32+len(st))
	in = append(in, byte(len(tagMix)))
	in = append(in, tagMix...)
	in = append(in, seed[:]...)
	in = append(in, st...)
	return blake3.Sum256(in)
}

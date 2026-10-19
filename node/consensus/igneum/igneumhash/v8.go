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

// Version 8, the blended relation (SPEC-R0A-v0.md section 0.8) with the
// bond-keyed rounds (section 0.9, bit 10): the second implementation,
// written from the specification and read against the reference's text
// (pow/ref/src/igneumhash.cpp) only where the specification names the
// reference's form. Every function here is pure; the round loop in
// igneumhash.go calls them in the order of section 0.8.
//
// The feature bits of a version 8 document (FEATURES, section 0.8):
//   bit 0    the lane's scratch slice read and written inside the block (needs SCRATCH_BYTES > 0)
//   bit 1    the predicated select: a data-dependent predicate picks the opcode's neighbour
//   bit 2    two chains of K / 2 registers; bit 3 four chains (not both)
//   bit 4    opcode 2 in binary32 (option (b)); bit 5 every opcode in binary32 (wide), superseding bit 4
//   bit 6    reserved (refused)
//   bit 7    the cross-lane second source (a shuffle from another lane of the snapshot)
//   bit 8    walk-only (measurement: the block's output replaced by a constant); bit 9 programme-only (no fetch); never both
//   bit 10   the bond-keyed rounds (section 0.9): KEYED_ROUNDS shares enter the fetched item before the absorb
//   bit 11   revision 2c form (a): the narrow select (registers 16, no chains); bit 12 form (b): no data-indexed register,
//            both sources shuffles from item-chosen lanes (the launch document's form); never both

import (
	"encoding/binary"
	"math"
)

func float32frombits(b uint32) float32 { return math.Float32frombits(b) }
func float32bits(f float32) uint32     { return math.Float32bits(f) }

// Version8 is the blended relation (section 0.8): version 4's whole-item
// round with the RXE1 block schedule between the absorb and the reduction
// tree, the programme drawn from the fetched item (draw revision 2 with
// the opcode-class quotas of revision 2b and the cross-lane form of 2c),
// and, under bit 10, the bond-keyed rounds of section 0.9.
const Version8 uint32 = 8

// The feature bits by name.
const (
	FeatScratch      uint32 = 1 << 0
	FeatPredicated   uint32 = 1 << 1
	FeatTwoChains    uint32 = 1 << 2
	FeatFourChains   uint32 = 1 << 3
	FeatFP           uint32 = 1 << 4
	FeatFPWide       uint32 = 1 << 5
	FeatReserved6    uint32 = 1 << 6
	FeatCrossLane    uint32 = 1 << 7
	FeatWalkOnly     uint32 = 1 << 8
	FeatProgrammeOnly uint32 = 1 << 9
	FeatKeyedRounds  uint32 = 1 << 10
	FeatFormA        uint32 = 1 << 11
	FeatFormB        uint32 = 1 << 12
	FeatMax          uint32 = 8191
)

// walkConst stands in for the block's output under walk-only (bit 8).
const walkConst uint32 = 0x9E3779B9

// Keyed reads bit 10: the document's work needs the bond's secret (the
// prover) or the shares the prover produced (the verifier's replay).
func (p Params) Keyed() bool { return p.Version == Version8 && p.Features&FeatKeyedRounds != 0 }

// KeyedRound reports whether round r is one of the S keyed rounds and which
// (section 0.9: the S rounds spread evenly, round r keyed iff (r S) mod R < S,
// its index i = (r S) div R).
func (p Params) KeyedRound(r uint32) (i uint32, keyed bool) {
	s := uint64(p.KeyedRounds)
	if !p.Keyed() || s == 0 {
		return 0, false
	}
	x := uint64(r) * s
	if x%uint64(p.Rounds) < s {
		return uint32(x / uint64(p.Rounds)), true
	}
	return 0, false
}

// v8desc is one decoded descriptor of the block schedule: the two source
// registers (slot-fixed under form (b)), the rotation, the opcode (the class
// quota of revision 2b), the predicate bit, the scratch salt and the two
// source lanes (shift for the second source under bit 7 or bit 12, shift1
// for the first under bit 12).
type v8desc struct {
	src1, src2, rot, opcode, pbit, salt, shift, shift1 uint8
}

// v8decode draws the round's programme from the fetched item's words W
// (n of them): descriptor i = b K + d takes the programme word
// p = W[i mod n] + (i div n) MUL and decodes it by aligned bit fields
// (section 0.8, draw revision 2):
//   bits 0..3 the first source offset, 4..7 the second, 8..12 the rotation,
//   13..14 the block's opcode phase (read from descriptor (b, 0) only),
//   15..19 the predicate bit, 20..22 the second source's lane offset (| 1),
//   24..31 the salt;
// under form (b) (bit 12) the sources are slot-fixed (d + 1, d + 2 in the
// chain) and bits 0..2 and 4..6 name the two source lanes; under form (a)
// (bit 11) each source is two bits among four candidates of the slot's
// window. The opcode is (d + phase) & 3 (revision 2b: fixed class counts).
func v8decode(W []uint32, B, K, features uint32, desc []v8desc) []v8desc {
	n := uint32(len(W))
	if cap(desc) < int(B*K) {
		desc = make([]v8desc, B*K)
	}
	desc = desc[:B*K]
	C := uint32(1)
	if features&FeatFourChains != 0 {
		C = 4
	} else if features&FeatTwoChains != 0 {
		C = 2
	}
	M := K / C
	for b := uint32(0); b < B; b++ {
		p0 := W[(b*K)%n] + ((b*K)/n)*Mul
		phase := (p0 >> 13) & 3
		for d := uint32(0); d < K; d++ {
			i := b*K + d
			pw := W[i%n] + (i/n)*Mul
			x := &desc[i]
			base, dd := (d/M)*M, d%M
			switch {
			case features&FeatFormB != 0:
				x.src1 = uint8(base + ((dd + 1) & (M - 1)))
				x.src2 = uint8(base + ((dd + 2) & (M - 1)))
				x.shift1 = uint8(pw & 7)
				x.shift = uint8((pw >> 4) & 7)
			case features&FeatFormA != 0:
				x.src1 = uint8(base + ((dd + 1 + (pw & 3)) & (M - 1)))
				x.src2 = uint8(base + ((dd + 5 + ((pw >> 4) & 3)) & (M - 1)))
				x.shift1 = 0
			default:
				x.src1 = uint8(base + ((dd + 1 + (pw & 15)) & (M - 1)))
				x.src2 = uint8(base + ((dd + 1 + ((pw >> 4) & 15)) & (M - 1)))
				x.shift1 = 0
			}
			x.rot = uint8((pw >> 8) & 31)
			x.opcode = uint8((d + phase) & 3)
			x.pbit = uint8((pw >> 15) & 31)
			if features&FeatFormB == 0 {
				x.shift = uint8(((pw >> 20) & 7) | 1)
			}
			x.salt = uint8(pw >> 24)
		}
	}
	return desc
}

// v8op is the integer form of the four opcodes (section 0.8).
func v8op(opcode uint8, x, y, z, rot uint32) uint32 {
	switch opcode {
	case 0:
		return x + rotl(y^z, rot)
	case 1:
		return x ^ rotl(y+z, rot)
	case 2:
		return x + y*(z|1)
	default:
		return rotl(x^y, rot) + z
	}
}

// v8mask reads a word as a positive normal binary32 in [2^-63, 2^0):
// the sign cleared, the exponent's top bit cleared, bit 29 set.
func v8mask(w uint32) float32 {
	return float32frombits((w & 0x3FFFFFFF) | 0x20000000)
}

// v8fop is option (b), the floating form of opcode 2 (bit 4): the operands
// masked to positive normals, the product and the sum each rounded once to
// nearest-even (the explicit float32 conversions forbid fusion: the Go
// specification lets an implementation fuse operations unless a conversion
// rounds the intermediate), the result's bits XORed into the destination.
func v8fop(x, y, z uint32) uint32 {
	xf, yf, zf := v8mask(x), v8mask(y), v8mask(z)
	prod := float32(yf * zf)
	sum := float32(prod + xf)
	return float32bits(sum) ^ x
}

// v8fopWide is bit 5: every opcode's arithmetic as binary32 on the masked
// range (the xor a subtraction, the rotate dropped), two roundings per
// opcode, never contracted; the result's bits XORed into the destination.
func v8fopWide(opcode uint8, x, y, z uint32) uint32 {
	xf, yf, zf := v8mask(x), v8mask(y), v8mask(z)
	var f float32
	switch opcode {
	case 0:
		inner := float32(yf - zf)
		f = float32(xf + inner)
	case 1:
		inner := float32(yf + zf)
		f = float32(xf - inner)
	case 2:
		inner := float32(yf * zf)
		f = float32(inner + xf)
	default:
		inner := float32(xf - yf)
		f = float32(inner + zf)
	}
	return float32bits(f) ^ x
}

// V8FOp and V8FOpWide are the floating forms on raw words, for the fp
// vector class (section 0.8: the rows record where a contracting build
// would differ).
func V8FOp(x, y, z uint32) uint32                   { return v8fop(x, y, z) }
func V8FOpWide(opcode uint32, x, y, z uint32) uint32 { return v8fopWide(uint8(opcode&3), x, y, z) }

// v8block runs step 1b of section 0.8 on the registers R (every lane's
// registers 0..K-1), B blocks of K descriptors with snapshot semantics per
// block (every destination reads the block's entry state T), the same
// descriptors on every lane. SL is the lane's scratch slice under bit 0 (nil
// without a scratchpad). desc is the round's decoded programme.
func v8block(R [][LaneWords]uint32, desc []v8desc, B, K, F uint32, SL [][]uint32, sliceWords uint32) {
	lanes := len(R)
	var T [8][LaneWords]uint32
	for b := uint32(0); b < B; b++ {
		for l := 0; l < lanes; l++ {
			T[l] = R[l]
		}
		for d := uint32(0); d < K; d++ {
			x := &desc[b*K+d]
			for l := 0; l < lanes; l++ {
				var y uint32
				if F&FeatFormB != 0 {
					y = T[(l+int(x.shift1))%lanes][x.src1]
				} else {
					y = T[l][x.src1]
				}
				var ra uint32
				useScratch := F&FeatScratch != 0 && SL != nil
				if useScratch {
					ra = (T[l][x.src2] ^ (uint32(x.salt) * Mul)) % sliceWords
					y ^= SL[l][ra]
				}
				opc := x.opcode
				if F&FeatPredicated != 0 && (y>>x.pbit)&1 == 1 {
					opc = (x.opcode + 1) % 4
				}
				var zz uint32
				if F&(FeatCrossLane|FeatFormB) != 0 {
					zz = T[(l+int(x.shift))%lanes][x.src2]
				} else {
					zz = T[l][x.src2]
				}
				var out uint32
				switch {
				case F&FeatFPWide != 0:
					out = v8fopWide(opc, T[l][d], y, zz)
				case F&FeatFP != 0 && opc == 2:
					out = v8fop(T[l][d], y, zz)
				default:
					out = v8op(opc, T[l][d], y, zz, uint32(x.rot))
				}
				R[l][d] = out
				if useScratch {
					SL[l][(ra+1+(out%(sliceWords-1)))%sliceWords] = out ^ T[l][d]
				}
			}
		}
	}
}

// The keyed functions of section 0.9.1, each on its own so a prover, an
// oracle or a test cannot drift from the walk.

// TagBondPK is the tag of G: pk = BLAKE3(tag("IgneumHash/R0A/bond-pk") || sk).
const TagBondPK = "IgneumHash/R0A/bond-pk"

// BondPK is G(sk), the bond's public key.
func BondPK(sk [32]byte) [32]byte {
	return sum256(append(tag(TagBondPK), sk[:]...))
}

// BondShare is s_i = BLAKE3_keyed(sk, "bond" || LE32(i) || m_i), the keyed
// share of round i over its message (m_0 the work seed S; m_i for i >= 1 the
// eight lane words LE32(R[l][0]) at the keyed round's entry).
func BondShare(sk [32]byte, i uint32, m [32]byte) [32]byte {
	msg := make([]byte, 0, 4+4+32)
	msg = append(msg, "bond"...)
	msg = binary.LittleEndian.AppendUint32(msg, i)
	msg = append(msg, m[:]...)
	return keyedSum(sk, msg)
}

// TestSK is the vector sets' and the trace's secret, public by construction
// and never a bond's: sk = BLAKE3("IgneumHash/R0A/test-sk" || header) (the
// 22 tag bytes raw, no length prefix).
func TestSK(headerHash [32]byte) [32]byte {
	return sum256(append([]byte("IgneumHash/R0A/test-sk"), headerHash[:]...))
}

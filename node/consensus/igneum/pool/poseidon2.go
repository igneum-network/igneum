// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import "math/bits"

// Poseidon2 over the Goldilocks field, the instance Miden VM 0.36.1 runs
// natively (miden-crypto 0.36.1, hash/algebraic_sponge/poseidon2; the
// Plonky3 Goldilocks width-12 permutation with the HorizenLabs round
// constants): state width 12, rate 8, capacity 4, the S-box x^7, four
// initial and four terminal external rounds, twenty-two internal rounds.
// This is H, the ADR's in-circuit hash (section 4.3 and 4.7), implemented
// here in Go for the node so the node, the wallet and the prover compute
// one commitment, one nullifier and one tree; the three agree on the
// fixed vectors in tools/money/vectors/money-stage1-vectors.json. Only the
// two-to-one merge is used: H(a, b) = the first word of the permutation of
// [a, b, 0, 0, 0, 0].

// goldilocks is the field's modulus, 2^64 - 2^32 + 1.
const goldilocks uint64 = 0xFFFF_FFFF_0000_0001

// epsilon is 2^32 - 1: 2^64 is congruent to it modulo the prime.
const epsilon uint64 = 0xFFFF_FFFF

// Felt is a field element in canonical form (below the modulus).
type Felt = uint64

// Word is four field elements, Miden's word (a digest, a key, a value).
type Word [4]Felt

func fadd(a, b uint64) uint64 {
	s, c := bits.Add64(a, b, 0)
	if c == 1 {
		s += epsilon
	}
	if s >= goldilocks {
		s -= goldilocks
	}
	return s
}

func fmul(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	hiHi := hi >> 32
	hiLo := hi & epsilon
	t0, borrow := bits.Sub64(lo, hiHi, 0)
	if borrow == 1 {
		t0 -= epsilon
	}
	t1 := hiLo * epsilon
	t2, c := bits.Add64(t0, t1, 0)
	if c == 1 {
		t2 += epsilon
	}
	if t2 >= goldilocks {
		t2 -= goldilocks
	}
	return t2
}

func sbox(x uint64) uint64 {
	x2 := fmul(x, x)
	x4 := fmul(x2, x2)
	x6 := fmul(x4, x2)
	return fmul(x6, x)
}

// matmulExternal is the external linear layer: the 4x4 circulant block
// M4 = [2 3 1 1; 1 2 3 1; 1 1 2 3; 3 1 1 2] on each block of four, then
// each element plus the sum of its position across the three blocks.
func matmulExternal(s *[12]uint64) {
	for i := 0; i < 3; i++ {
		x := s[4*i : 4*i+4]
		t01 := fadd(x[0], x[1])
		t23 := fadd(x[2], x[3])
		t0123 := fadd(t01, t23)
		t01123 := fadd(t0123, x[1])
		t01233 := fadd(t0123, x[3])
		x3 := fadd(t01233, fadd(x[0], x[0]))
		x1 := fadd(t01123, fadd(x[2], x[2]))
		x0 := fadd(t01123, t01)
		x2 := fadd(t01233, t23)
		x[0], x[1], x[2], x[3] = x0, x1, x2, x3
	}
	var stored [4]uint64
	for j := 0; j < 3; j++ {
		for l := 0; l < 4; l++ {
			stored[l] = fadd(stored[l], s[4*j+l])
		}
	}
	for i := range s {
		s[i] = fadd(s[i], stored[i%4])
	}
}

// matmulInternal is the internal linear layer: s_i = s_i * diag_i + sum(s).
func matmulInternal(s *[12]uint64) {
	var sum uint64
	for _, v := range s {
		sum = fadd(sum, v)
	}
	for i := range s {
		s[i] = fadd(fmul(s[i], poseidon2MatDiag[i]), sum)
	}
}

// permute is the Poseidon2 permutation on a twelve-element state.
func permute(s *[12]uint64) {
	matmulExternal(s)
	for r := 0; r < 4; r++ {
		for i := range s {
			s[i] = sbox(fadd(s[i], poseidon2ArkExtInitial[r][i]))
		}
		matmulExternal(s)
	}
	for r := 0; r < 22; r++ {
		s[0] = sbox(fadd(s[0], poseidon2ArkInt[r]))
		matmulInternal(s)
	}
	for r := 0; r < 4; r++ {
		for i := range s {
			s[i] = sbox(fadd(s[i], poseidon2ArkExtTerminal[r][i]))
		}
		matmulExternal(s)
	}
}

// Merge is H(a, b): the two-to-one hash.
func Merge(a, b Word) Word {
	var s [12]uint64
	copy(s[0:4], a[:])
	copy(s[4:8], b[:])
	permute(&s)
	var out Word
	copy(out[:], s[0:4])
	return out
}

// WordBytes is a word's 32 bytes: each element little-endian, in order.
func WordBytes(w Word) (out [32]byte) {
	for i := 0; i < 4; i++ {
		v := w[i]
		for j := 0; j < 8; j++ {
			out[8*i+j] = byte(v >> (8 * j))
		}
	}
	return out
}

// WordOf reads a word from 32 bytes; ok is false when an element is not
// below the modulus (not every 32-byte string is a word).
func WordOf(b [32]byte) (w Word, ok bool) {
	for i := 0; i < 4; i++ {
		var v uint64
		for j := 7; j >= 0; j-- {
			v = v<<8 | uint64(b[8*i+j])
		}
		if v >= goldilocks {
			return Word{}, false
		}
		w[i] = v
	}
	return w, true
}

// The round constants (miden-crypto 0.36.1 constants.rs, equal to
// Plonky3's GOLDILOCKS_POSEIDON2_RC_12_* and MATRIX_DIAG_12_GOLDILOCKS).
var poseidon2MatDiag = [12]uint64{
	0xfffffffeffffffff, 0x0000000000000001, 0x0000000000000002, 0x7fffffff80000001,
	0x0000000000000003, 0x0000000000000004, 0x7fffffff80000000, 0xfffffffefffffffe,
	0xfffffffefffffffd, 0xbfffffff40000001, 0x3fffffffc0000000, 0xdfffffff20000001,
}

var poseidon2ArkExtInitial = [4][12]uint64{
	{0x13dcf33aba214f46, 0x30b3b654a1da6d83, 0x1fc634ada6159b56, 0x937459964dc03466, 0xedd2ef2ca7949924, 0xede9affde0e22f68, 0x8515b9d6bac9282d, 0x6b5c07b4e9e900d8, 0x1ec66368838c8a08, 0x9042367d80d1fbab, 0x400283564a3c3799, 0x4a00be0466bca75e},
	{0x7913beee58e3817f, 0xf545e88532237d90, 0x22f8cb8736042005, 0x6f04990e247a2623, 0xfe22e87ba37c38cd, 0xd20e32c85ffe2815, 0x117227674048fe73, 0x4e9fb7ea98a6b145, 0xe0866c232b8af08b, 0x00bbc77916884964, 0x7031c0fb990d7116, 0x240a9e87cf35108f},
	{0x2e6363a5a12244b3, 0x5e1c3787d1b5011c, 0x4132660e2a196e8b, 0x3a013b648d3d4327, 0xf79839f49888ea43, 0xfe85658ebafe1439, 0xb6889825a14240bd, 0x578453605541382b, 0x4508cda8f6b63ce9, 0x9c3ef35848684c91, 0x0812bde23c87178c, 0xfe49638f7f722c14},
	{0x8e3f688ce885cbf5, 0xb8e110acf746a87d, 0xb4b2e8973a6dabef, 0x9e714c5da3d462ec, 0x6438f9033d3d0c15, 0x24312f7cf1a27199, 0x23f843bb47acbf71, 0x9183f11a34be9f01, 0x839062fbb9d45dbf, 0x24b56e7e6c2e43fa, 0xe1683da61c962a72, 0xa95c63971a19bfa7},
}

var poseidon2ArkInt = [22]uint64{
	0x4adf842aa75d4316, 0xf8fbb871aa4ab4eb, 0x68e85b6eb2dd6aeb, 0x07a0b06b2d270380, 0xd94e0228bd282de4, 0x8bdd91d3250c5278,
	0x209c68b88bba778f, 0xb5e18cdab77f3877, 0xb296a3e808da93fa, 0x8370ecbda11a327e, 0x3f9075283775dad8, 0xb78095bb23c6aa84,
	0x3f36b9fe72ad4e5f, 0x69bc96780b10b553, 0x3f1d341f2eb7b881, 0x4e939e9815838818, 0xda366b3ae2a31604, 0xbc89db1e7287d509,
	0x6102f411f9ef5659, 0x58725c5e7ac1f0ab, 0x0df5856c798883e7, 0xf7bb62a8da4c961b,
}

var poseidon2ArkExtTerminal = [4][12]uint64{
	{0xc68be7c94882a24d, 0xaf996d5d5cdaedd9, 0x9717f025e7daf6a5, 0x6436679e6e7216f4, 0x8a223d99047af267, 0xbb512e35a133ba9a, 0xfbbf44097671aa03, 0xf04058ebf6811e61, 0x5cca84703fac7ffb, 0x9b55c7945de6469f, 0x8e05bf09808e934f, 0x2ea900de876307d7},
	{0x7748fff2b38dfb89, 0x6b99a676dd3b5d81, 0xac4bb7c627cf7c13, 0xadb6ebe5e9e2f5ba, 0x2d33378cafa24ae3, 0x1e5b73807543f8c2, 0x09208814bfebb10f, 0x782e64b6bb5b93dd, 0xadd5a48eac90b50f, 0xadd4c54c736ea4b1, 0xd58dbb86ed817fd8, 0x6d5ed1a533f34ddd},
	{0x28686aa3e36b7cb9, 0x591abd3476689f36, 0x047d766678f13875, 0xa2a11112625f5b49, 0x21fd10a3f8304958, 0xf9b40711443b0280, 0xd2697eb8b2bde88e, 0x3493790b51731b3f, 0x11caf9dd73764023, 0x7acfb8f72878164e, 0x744ec4db23cefc26, 0x1e00e58f422c6340},
	{0x21dd28d906a62dda, 0xf32a46ab5f465b5f, 0xbfce13201f3f7e6b, 0xf30d2e7adb5304e2, 0xecdf4ee4abad48e9, 0xf94e82182d395019, 0x4ee52e3744d887c5, 0xa1341c7cac0083b2, 0x2302fb26c30c834a, 0xaea3c587273bf7d3, 0xf798e24961823ec7, 0x962deba3e9a2cd94},
}

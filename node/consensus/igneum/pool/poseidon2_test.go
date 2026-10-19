// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import "testing"

// The permutation's known answer (miden-crypto 0.36.1 poseidon2/test.rs,
// permutation_test_vector): the state [0, 1, ..., 11].
func TestPoseidon2PermutationKnownAnswer(t *testing.T) {
	var s [12]uint64
	for i := range s {
		s[i] = uint64(i)
	}
	permute(&s)
	want := [12]uint64{
		0xf292ab67c0f14b03, 0x0a32f1b37656544c, 0x053c61ab895498de, 0x02ff92e55b196ffb,
		0x58176e8f6f58cab2, 0xb0aa1206e7aec0f8, 0xe90c13f3dce83ca4, 0xf4da15333edf39c2,
		0x23b701c053c2ca6c, 0xd233d593dcdfbf58, 0x4effa5f9516fb52e, 0x0aaf4489f1f40166,
	}
	if s != want {
		t.Fatalf("permutation differs:\n got %x\nwant %x", s, want)
	}
}

func TestGoldilocksArithmetic(t *testing.T) {
	pm1 := goldilocks - 1
	if fadd(pm1, 1) != 0 || fadd(pm1, pm1) != goldilocks-2 || fadd(0, 0) != 0 {
		t.Fatal("add")
	}
	if fmul(pm1, pm1) != 1 || fmul(1<<32, 1<<32) != epsilon || fmul(pm1, 2) != goldilocks-2 {
		t.Fatalf("mul: %x %x %x", fmul(pm1, pm1), fmul(1<<32, 1<<32), fmul(pm1, 2))
	}
	w := Word{1, 2, goldilocks - 1, 0}
	b := WordBytes(w)
	back, ok := WordOf(b)
	if !ok || back != w {
		t.Fatal("word bytes round trip")
	}
	for j := 0; j < 8; j++ {
		b[j] = 0xff
	}
	if _, ok := WordOf(b); ok {
		t.Fatal("a non-canonical element read as a word")
	}
}

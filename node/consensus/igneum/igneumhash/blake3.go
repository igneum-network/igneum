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
	"encoding/binary"

	"lukechampine.com/blake3"
)

// BLAKE3 is the envelope's reviewed primitive. The module is
// lukechampine.com/blake3 v1.4.1 (MIT), pinned in node/go.mod and checked
// against the official test vectors in blake3_test.go before anything here
// is trusted: the vectors decide, not the module. Two uses, as the envelope
// names them: BLAKE3(x) (sum256) and BLAKE3k(key, x) with extendable output
// (keyedXOF).

func sum256(data []byte) [32]byte {
	return blake3.Sum256(data)
}

// keyedXOF fills out with the extendable output of the keyed hash of msg
// (the first len(out) bytes of the XOF).
func keyedXOF(key [32]byte, msg []byte, out []byte) {
	h := blake3.New(32, key[:])
	h.Write(msg)
	xof := h.XOF()
	if _, err := xof.Read(out); err != nil {
		panic("igneumhash: blake3 xof: " + err.Error())
	}
}

// xofFill fills words (little-endian) from the keyed XOF of (key, msg).
// Used by the work function's initialisation (pending SPEC: the exact fill).
func xofFill(key [32]byte, msg []byte, words []uint32) {
	buf := make([]byte, 4*len(words))
	keyedXOF(key, msg, buf)
	for i := range words {
		words[i] = binary.LittleEndian.Uint32(buf[4*i:])
	}
}

// keyedSum is BLAKE3k(key, msg), 32 bytes.
func keyedSum(key [32]byte, msg []byte) [32]byte {
	h := blake3.New(32, key[:])
	h.Write(msg)
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// hashXOF fills out with BLAKE3xof(msg, len(out)): the extendable output in
// hash mode (its first 32 bytes are BLAKE3(msg)).
func hashXOF(msg []byte, out []byte) {
	h := blake3.New(32, nil)
	h.Write(msg)
	if _, err := h.XOF().Read(out); err != nil {
		panic("igneumhash: blake3 xof: " + err.Error())
	}
}

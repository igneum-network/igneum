package igneumhash

import (
	"bytes"
	"encoding/hex"
	"testing"

	"lukechampine.com/blake3"
)

// The official BLAKE3 test vectors (BLAKE3-team/BLAKE3 test_vectors.json)
// for the inputs of length 0 and 1: hash, keyed_hash with the key
// "whats the Elvish word for friend" and derive_key with the context
// "BLAKE3 2019-12-27 16:29:52 test vectors context". Input bytes are the
// sequence 0, 1, 2, ... of the given length.
func TestBlake3KnownAnswers(t *testing.T) {
	key := []byte("whats the Elvish word for friend")
	ctx := "BLAKE3 2019-12-27 16:29:52 test vectors context"
	cases := []struct {
		n                      int
		hash, keyed, derivekey string
	}{
		{0, "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262",
			"92b2b75604ed3c761f9d6f62392c8a9227ad0ea3f09573e783f1498a4ed60d26",
			"2cc39783c223154fea8dfb7c1b1660f2ac2dcbd1c1de8277b0b0dd39b7e50d7d"},
		{1, "2d3adedff11b61f14c886e35afa036736dcd87a74d27b5c1510225d0f592e213",
			"6d7878dfff2f485635d39013278ae14f1454b8c0a3a2d34bc1ab38228a80c95b",
			"b3e2e340a117a499c6cf2398a19ee0d29cca2bb7404c73063382693bf66cb06c"},
	}
	for _, c := range cases {
		in := make([]byte, c.n)
		for i := range in {
			in[i] = byte(i)
		}
		h := blake3.Sum256(in)
		if hex.EncodeToString(h[:]) != c.hash {
			t.Errorf("hash(len %d): %x", c.n, h)
		}
		kh := blake3.New(32, key)
		kh.Write(in)
		if got := kh.Sum(nil); hex.EncodeToString(got) != c.keyed {
			t.Errorf("keyed(len %d): %x", c.n, got)
		}
		var dk [32]byte
		blake3.DeriveKey(dk[:], ctx, in)
		if hex.EncodeToString(dk[:]) != c.derivekey {
			t.Errorf("derive_key(len %d): %x", c.n, dk)
		}
	}
}

// The XOF's first 32 bytes are the hash; the stream is deterministic and
// longer reads extend it without changing the prefix.
func TestBlake3XOF(t *testing.T) {
	var key [32]byte
	copy(key[:], "whats the Elvish word for friend")
	a := make([]byte, 32)
	keyedXOF(key, nil, a)
	b := make([]byte, 4096)
	keyedXOF(key, nil, b)
	if !bytes.Equal(a, b[:32]) {
		t.Fatal("xof prefix differs between read lengths")
	}
	kh := blake3.New(32, key[:])
	if got := kh.Sum(nil); !bytes.Equal(got, a) {
		t.Fatalf("xof prefix %x is not the keyed hash %x", a, got)
	}
	words := make([]uint32, 8)
	xofFill(key, []byte("m"), words)
	words2 := make([]uint32, 8)
	xofFill(key, []byte("m"), words2)
	for i := range words {
		if words[i] != words2[i] {
			t.Fatal("xofFill not deterministic")
		}
	}
}

// hashXOF's prefix is the plain hash; keyedSum is the keyed XOF's prefix.
func TestBlake3HashXOFAndKeyedSum(t *testing.T) {
	msg := []byte("IgneumHash")
	out := make([]byte, 96)
	hashXOF(msg, out)
	if h := blake3.Sum256(msg); !bytes.Equal(out[:32], h[:]) {
		t.Fatal("hashXOF prefix is not the hash")
	}
	var key [32]byte
	copy(key[:], "whats the Elvish word for friend")
	x := make([]byte, 64)
	keyedXOF(key, msg, x)
	if k := keyedSum(key, msg); !bytes.Equal(x[:32], k[:]) {
		t.Fatal("keyedSum is not the keyed XOF's prefix")
	}
}

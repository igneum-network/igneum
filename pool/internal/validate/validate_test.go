package validate

import (
	"encoding/hex"
	"errors"
	"testing"
)

func TestNullIsDeterministicAndBound(t *testing.T) {
	var header [32]byte
	header[0] = 1
	m1, h1, err := Null{}.Verify(NullAlgorithm, header, 7, 99)
	if err != nil {
		t.Fatal(err)
	}
	m2, h2, _ := Null{}.Verify(NullAlgorithm, header, 7, 99)
	if m1 != m2 || h1 != h2 {
		t.Fatal("not deterministic")
	}
	_, h3, _ := Null{}.Verify(NullAlgorithm, header, 7, 100)
	if h3 == h1 {
		t.Fatal("nonce not bound")
	}
	_, h4, _ := Null{}.Verify(NullAlgorithm, header, 8, 99)
	if h4 == h1 {
		t.Fatal("height not bound")
	}
	if _, _, err := (Null{}).Verify("progpow", header, 7, 99); !errors.Is(err, ErrAlgorithm) {
		t.Fatalf("unknown algorithm: %v", err)
	}
}

// The frozen HW-01 vectors of the node's null algorithm
// (node/consensus/igneum/pow/null/null.go, generated on a build box on
// 9 October 2026): the pool must match them byte for byte.
func TestNullMatchesTheNodeVectors(t *testing.T) {
	vectors := []struct {
		name, header string
		height       uint64
		nonce        uint64
		mix, result  string
	}{
		{"zero", "0000000000000000000000000000000000000000000000000000000000000000", 0, 0x0,
			"c980e59163ce244bb4bb6211f48c7b46f88a4f40943e84eb99bdc41e129bd293", "c4b92ebaf240cc21f71044ad7a470dd24fc3aef5d2ef4a05bd2435a5ba970ab6"},
		{"one", "0000000000000000000000000000000000000000000000000000000000000001", 1, 0x1,
			"a49ad3687c629a6cf4240a3a9b993f7de5248149cafd7411d8951cf9867acf10", "e6f420148dcdc9f1198b7c3cc64b18a15cb60603dc6848a84e55a8bd68d9e9ba"},
		{"max-nonce", "00000000000000000000000000000000000000000000000000000000000000ff", 1099511627776, 0xffffffffffffffff,
			"3e718513c97e1b2990e7ab21eb9079bda264571b65e2c3efd5d5554fd7aedd93", "b9227d4986232fb90b84e93c6b459a27a8a75cf7ce11d8c53b7ab8d7a4630982"},
		{"pattern", "0102030405060708091011121314151617181920212223242526272829303132", 123456789, 0x123456789abcdef,
			"8f27b594e46c3706375a3e59e9577a3a9143735e5597c218dbe9f74722439e9f", "0988ee95baa64958ba329ff2df12e9b77b20067b35365f78cbd27e18ccfbd7f1"},
	}
	for _, v := range vectors {
		var header [32]byte
		b, _ := hex.DecodeString(v.header)
		copy(header[:], b)
		mix, result, err := Null{}.Verify(NullAlgorithm, header, v.height, v.nonce)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(mix[:]) != v.mix || hex.EncodeToString(result[:]) != v.result {
			t.Fatalf("%s: mix %x result %x", v.name, mix, result)
		}
	}
}

func TestTargetsAndDifficulty(t *testing.T) {
	var max [32]byte
	for i := range max {
		max[i] = 0xff
	}
	if Difficulty(max) != 1 {
		t.Fatalf("max target difficulty %d", Difficulty(max))
	}
	for _, d := range []uint64{1, 2, 16, 1000, 1 << 40} {
		tg := TargetForDifficulty(d)
		got := Difficulty(tg)
		if got < d {
			t.Fatalf("difficulty %d: target gives %d", d, got)
		}
	}
	half := TargetForDifficulty(2)
	var low, high [32]byte
	high[0] = 0x80
	low[0] = 0x7f
	if !MeetsTarget(low, half) || MeetsTarget(high, half) {
		t.Fatal("MeetsTarget boundary wrong")
	}
	if !MeetsTarget(half, half) {
		t.Fatal("equal must meet")
	}
}

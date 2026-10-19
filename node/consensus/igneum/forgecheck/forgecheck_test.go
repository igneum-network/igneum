package forgecheck

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"lukechampine.com/blake3"
)

func TestEncodingExact(t *testing.T) {
	f := Fields{ChainID: 7310, Version: 1, Nonce: 0x0102030405060708}
	for i := range f.ParentID {
		f.ParentID[i], f.Contents[i], f.Target[i], f.ParamsID[i] = 0xa0+byte(i), 0xb0+byte(i), 0xc0+byte(i), 0xd0+byte(i)
	}
	for i := range f.Reward {
		f.Reward[i] = 0xe0 + byte(i)
	}
	x := f.Encode()
	if len(x) != XLen {
		t.Fatal(len(x))
	}
	// the layout, byte for byte (section 2.2)
	want := []byte{17}
	want = append(want, "Igneum/Forge/x/v1"...)
	want = append(want, 0, 0, 0, 0, 0, 0, 0x1c, 0x8e) // 7310 big-endian
	want = append(want, 0, 0, 0, 1)
	want = append(want, f.ParentID[:]...)
	want = append(want, 20)
	want = append(want, f.Reward[:]...)
	want = append(want, f.Contents[:]...)
	want = append(want, f.Target[:]...)
	want = append(want, f.ParamsID[:]...)
	want = append(want, 1, 2, 3, 4, 5, 6, 7, 8)
	if !bytes.Equal(x, want) {
		t.Fatalf("x\n%x\n%x", x, want)
	}
	g, err := Decode(x)
	if err != nil || g != f {
		t.Fatal(err)
	}
	// H_d: one byte of domain length, the domain, then m
	seed := Seed(x)
	if seed != blake3.Sum256(append(append([]byte{20}, DomainSeed...), x...)) {
		t.Fatal("H_seed")
	}
	var y [32]byte
	y[31] = 9
	if Ticket(x, y) != blake3.Sum256(append(append(append([]byte{22}, DomainTicket...), x...), y[:]...)) {
		t.Fatal("H_ticket")
	}
	var pid [32]byte
	pid[0] = 5
	if Public(pid, x, y) != blake3.Sum256(append(append(append(append([]byte{22}, DomainPublic...), pid[:]...), x...), y[:]...)) {
		t.Fatal("H_public")
	}
	for _, d := range []string{DomainX, DomainSeed, DomainTicket, DomainPublic} {
		if n := map[string]int{DomainX: 17, DomainSeed: 20, DomainTicket: 22, DomainPublic: 22}[d]; len(d) != n {
			t.Fatalf("%s has %d bytes, the table says %d", d, len(d), n)
		}
	}
}

func TestStrict(t *testing.T) {
	x := Fields{Version: 1}.Encode()
	for name, bad := range map[string][]byte{
		"186 bytes":          x[:186],
		"188 bytes":          append(append([]byte(nil), x...), 0),
		"domain length 16":   func() []byte { b := append([]byte(nil), x...); b[0] = 16; return b }(),
		"domain bytes":       func() []byte { b := append([]byte(nil), x...); b[5] ^= 0x20; return b }(),
		"reward length 19":   func() []byte { b := append([]byte(nil), x...); b[62] = 19; return b }(),
		"relation version 2": func() []byte { b := append([]byte(nil), x...); b[29] = 2; return b }(),
		"relation version 0": func() []byte { b := append([]byte(nil), x...); b[29] = 0; return b }(),
		"empty":              nil,
	} {
		if _, err := Decode(bad); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := CheckY(make([]byte, 31)); err == nil {
		t.Fatal("31-byte y accepted")
	}
	// one tuple, one encoding: every field change changes x, and decode is the inverse
	f := Fields{Version: 1}
	seen := map[string]bool{hex.EncodeToString(f.Encode()): true}
	for _, g := range []Fields{
		{Version: 1, ChainID: 1}, {Version: 1, Nonce: 1}, {Version: 1, ParentID: [32]byte{1}}, {Version: 1, Reward: [20]byte{1}},
		{Version: 1, Contents: [32]byte{1}}, {Version: 1, Target: [32]byte{1}}, {Version: 1, ParamsID: [32]byte{1}},
	} {
		x := g.Encode()
		if seen[hex.EncodeToString(x)] {
			t.Fatal("two tuples, one x")
		}
		seen[hex.EncodeToString(x)] = true
		if h, err := Decode(x); err != nil || h != g {
			t.Fatal("decode is not the inverse")
		}
	}
	// qualifies: inclusive, big-endian
	var tk, tg [32]byte
	tk[0], tg[0] = 1, 1
	if !Qualifies(tk, tg) || Qualifies([32]byte{2}, tg) || !Qualifies([32]byte{0, 0xff}, tg) {
		t.Fatal("qualifies")
	}
}

func TestOwnVectors(t *testing.T) {
	z32, z20 := hex.EncodeToString(make([]byte, 32)), hex.EncodeToString(make([]byte, 20))
	f32, f20 := hex.EncodeToString(bytes.Repeat([]byte{0xff}, 32)), hex.EncodeToString(bytes.Repeat([]byte{0xff}, 20))
	vf := &VectorFile{Vectors: []Vector{
		{Name: "zero fields", Version: 1, ParentID: z32, Reward: z20, Contents: z32, Target: z32, ParamsID: z32, Nonce: "0000000000000000", Y: z32, ProgramID: z32},
		{Name: "every field at its maximum", ChainID: ^uint64(0), Version: 1, ParentID: f32, Reward: f20, Contents: f32, Target: f32, ParamsID: f32, Nonce: "ffffffffffffffff", Y: f32, ProgramID: f32},
		{Name: "development chain 7310", ChainID: 7310, Version: 1, ParentID: "0a" + z32[2:], Reward: "0b" + z20[2:], Contents: "0c" + z32[2:], Target: "00ff" + z32[4:], ParamsID: "0d" + z32[2:], Nonce: "00000000000004d2", Y: "0e" + z32[2:], ProgramID: "0f" + z32[2:]},
	}}
	if err := vf.Fill(); err != nil {
		t.Fatal(err)
	}
	divs, err := vf.Check()
	if err != nil || len(divs) != 0 {
		t.Fatal(divs, err)
	}
	if !vf.Vectors[1].Qualifies || vf.Vectors[0].Qualifies {
		t.Fatal("qualifies at the maximum target, not at zero")
	}
	bad := *vf
	bad.Vectors = append([]Vector(nil), vf.Vectors...)
	bad.Vectors[2].X = bad.Vectors[2].X[:100] + "ff" + bad.Vectors[2].X[102:] // byte 50: inside the parent id, zero in this tuple
	divs, _ = bad.Check()
	if len(divs) != 1 || divs[0].Stage != "x byte 50 (parent id)" {
		t.Fatal(divs)
	}
	if os.Getenv("IGNEUM_WRITE_VECTORS") != "" {
		dir := filepath.Join("..", "..", "..", "..", "tests", "forge", "f0")
		os.MkdirAll(dir, 0o755)
		b, _ := marshal(vf)
		os.WriteFile(filepath.Join(dir, "own.json"), b, 0o644)
	}
}

package igneumhash

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// Version 4 (Strata, section 0.4), from the text: the four frozen documents'
// P values (STEP1-NOTES.md section 3), the 52-byte record, the whole-item
// rule's claims (the absorb is a bijection in the item for a fixed state;
// the tree depends on every word and on their positions), the dataset item
// at every width, and the inertness of versions 0 and 1.

func strataDoc(w uint32) Params {
	return Params{ChainID: 7310, Version: Version4, DatasetBytes: 1 << 31, ScratchBytes: 0, Rounds: 64, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 1 << 25, Parents: 256, ItemBytes: w}
}

func TestStrataDocuments(t *testing.T) {
	want := map[uint32]string{
		64:  "ee50122701d3b2c8bf04113cb3db24e839aae67deff3b5694ae701283c9469ce",
		128: "69e4a4e7211aac188e589cbdb04d8e6ad5f45c0b059318d88897f3a3dcfc5c72",
		256: "612bf2d834abc7ab34b7a08e86958d0f7f3a215e7c7b95d0f783bd07c7a12f12",
		512: "e10d0be2528542feabd405137cee768051c6535bc6eb19d87db3d486e5134c98",
	}
	// the Riptide documents' P values (strata/RIPTIDE.md section 1): rounds R(W) = 64 x 128 / W
	riptide := []struct {
		w, r    uint32
		dataset uint64
		p       string
	}{{256, 32, 1 << 31, "6e63020b3be1065502e256cd28844b16ccdf1c304efaf3ecf537ebce76aa2602"}, {512, 16, 1 << 31, "cd1566d5055fd784d57fccb312c91d17e69935803fd676e3f1e2e918b1474f52"}, {1024, 8, 1 << 31, "15b7dbe8b0ae717fec0dad062eb80c0d59e7b1d06171669ca027a37d57f62d50"}, {512, 16, 1 << 32, "4a0bdadb679f0d0fbe05716e439cfbb0b781d6111831818f27caed7b27b4b75c"}}
	for _, c := range riptide {
		d := strataDoc(c.w)
		d.Rounds, d.DatasetBytes = c.r, c.dataset
		if err := d.Validate(); err != nil {
			t.Fatal(c.w, err)
		}
		if got := hexOf(d.ParamsHash()); got != c.p {
			t.Fatalf("riptide w %d r %d: P %s, the document says %s", c.w, c.r, got, c.p)
		}
	}
	for w, p := range want {
		d := strataDoc(w)
		if err := d.Validate(); err != nil {
			t.Fatal(err)
		}
		if len(d.ParamsBytes()) != 52 || binary.BigEndian.Uint32(d.ParamsBytes()[48:]) != w {
			t.Fatalf("w %d: record %x", w, d.ParamsBytes())
		}
		if got := hexOf(d.ParamsHash()); got != p {
			t.Fatalf("w %d: P %s, the frozen document says %s", w, got, p)
		}
		if d.Variant() != "v4-d2048-s0-r64-k32-p256-w"+itoa(w) || d.Items() != (1<<31)/uint64(w) {
			t.Fatal(d.Variant(), d.Items())
		}
		doc := `{"version": 4, "dataset_mib": 2048, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": ` + itoa(w) + `}`
		q, err := ParamsFromDocument([]byte(doc))
		if err != nil || q != d {
			t.Fatal(q, err)
		}
	}
	for _, bad := range []string{
		`{"version": 4, "dataset_mib": 2048, "rounds": 64, "cache_mib": 32, "parents": 256}`,
		`{"version": 4, "dataset_mib": 2048, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 96}`,
		`{"version": 1, "item_bytes": 256}`,
		`{"version": 3, "item_bytes": 256}`,
	} {
		if _, err := ParamsFromDocument([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	// no version 1 pid moves
	if hexOf(Defaults(Version1).ParamsHash()) == hexOf(strataDoc(128).ParamsHash()) {
		t.Fatal("a version 4 document shares a version 1 pid")
	}
}

// tree is section 0.4's step 2, written independently of the round for the test.
func tree(words []uint32) uint32 {
	z := make([]uint32, len(words))
	for j := range words {
		z[j] = words[j] ^ (uint32(j+1) * Mul)
	}
	for m := len(z); m >= 2; m /= 2 {
		for i := 0; i < m/2; i++ {
			x, y := z[2*i], z[2*i+1]
			z[i] = ((x ^ rotl(y, 13)) * Mul) + (y >> 7)
		}
	}
	return z[0]
}

func TestStrataTreeAndAbsorb(t *testing.T) {
	for _, n := range []int{16, 32, 64, 128} {
		words := make([]uint32, n)
		for j := range words {
			words[j] = uint32(j*7 + 3)
		}
		h := tree(words)
		// every word moves h; a swapped pair moves h (positions are bound); the node is not symmetric
		for j := range words {
			w2 := append([]uint32(nil), words...)
			w2[j] ^= 1
			if tree(w2) == h {
				t.Fatalf("n %d: word %d does not reach h", n, j)
			}
		}
		w3 := append([]uint32(nil), words...)
		w3[0], w3[1] = w3[1], w3[0]
		if tree(w3) == h {
			t.Fatal("a swapped pair gives the same h")
		}
		if ((uint32(1)^rotl(2, 13))*Mul + (2 >> 7)) == ((uint32(2)^rotl(1, 13))*Mul + (1 >> 7)) {
			t.Fatal("node symmetric")
		}
	}
	// the absorb: each word into a distinct register up to 128 words (a bijection in the item for a fixed
	// state); at 256 words (W 1024) each register receives exactly two
	for _, n := range []int{16, 32, 64, 128, 256} {
		seen := map[[2]int]int{}
		for j := 0; j < n; j++ {
			seen[[2]int{j % 8, (j / 8) % LaneWords}]++
		}
		for k, c := range seen {
			if want := (n + 127) / 128; c != want {
				t.Fatalf("n %d: register %v receives %d words, want %d", n, k, c, want)
			}
		}
	}
	// the tree at 256 leaves: eight levels, every leaf reaching the root
	w256 := make([]uint32, 256)
	for j := range w256 {
		w256[j] = uint32(j * 11)
	}
	h := tree(w256)
	for j := 0; j < 256; j += 17 {
		x := append([]uint32(nil), w256...)
		x[j] ^= 2
		if tree(x) == h {
			t.Fatalf("leaf %d of 256 does not reach h", j)
		}
	}
}

func TestStrataRoundsAndItems(t *testing.T) {
	// a test-only small point at every width: eight items of cache, a 64 KiB dataset
	for _, w := range []uint32{64, 128, 256, 512, 1024} {
		p := Params{ChainID: 7310, Version: Version4, DatasetBytes: 1 << 16, ScratchBytes: 0, Rounds: 8, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 1 << 20, Parents: 4, ItemBytes: w}
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
		light := p.NewLight(0)
		ds, err := p.Generate(0)
		if err != nil {
			t.Fatal(err)
		}
		// the dataset and the light path give the same items at every width
		buf := make([]byte, w)
		buf2 := make([]byte, w)
		for i := uint64(0); i < p.Items(); i += 37 {
			light.WideItem(i, buf)
			ds.WideItem(i, buf2)
			if string(buf) != string(buf2) {
				t.Fatalf("w %d: item %d differs between light and dataset", w, i)
			}
		}
		// W = 128 is version 1's item word for word; W = 64 is version 1's walk cut to 64 bytes
		// (under version 4's own epoch seed and cache: E hashes P, so a version 1 light is another epoch)
		if w == 128 || w == 64 {
			var item [ItemBytes]byte
			for i := uint64(0); i < 64; i++ {
				light.WideItem(i, buf)
				ItemIntoV1(light.EpochSeed(), light.Cache(), p.Parents, i, &item)
				if w == 128 && string(buf) != string(item[:]) {
					t.Fatalf("W 128 item %d is not version 1's", i)
				}
				if w == 64 {
					var cut [64]byte
					// the XOF cut to 64 bytes: the same keyed input as version 1's, the first 64 bytes equal
					copy(cut[:], item[:64])
					if string(buf) != string(cut[:]) {
						t.Fatalf("W 64 item %d is not version 1's walk cut to 64", i)
					}
				}
			}
		}
		var hdr [32]byte
		hdr[0] = byte(w)
		st, err := p.Compute(light, hdr, 0, 1, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Trace.H) != 8 || len(st.Trace.Items[0]) != int(w/4) {
			t.Fatal("trace shape")
		}
		// the trace's h is the tree over the item as read; the next address is (R[g][0] ^ h) mod N
		for r := range st.Trace.H {
			if tree(st.Trace.Items[r]) != st.Trace.H[r] {
				t.Fatalf("w %d: round %d h", w, r)
			}
			g := (r + 1) % 8
			if st.Trace.RoundIdx[r] != uint64((st.Trace.Rounds[r][g][0]^st.Trace.H[r])&uint32(p.Items()-1)) {
				t.Fatalf("w %d: round %d next address", w, r)
			}
		}
		// the dataset source gives the same mix; a verify round trip holds
		st2, _ := p.Compute(ds, hdr, 0, 1, false)
		if st2.Mix != st.Mix {
			t.Fatal("mix differs between light and dataset")
		}
	}
	// versions 0 and 1 are inert: their traces carry no h and their round is unchanged (the fixtures tree covers the values)
	p := Defaults(Version1)
	p.DatasetBytes, p.CacheBytes, p.Rounds = 1<<20, 1<<20, 4
	st, err := p.Compute(p.NewLight(0), [32]byte{1}, 0, 1, true)
	if err != nil || len(st.Trace.H) != 0 || len(st.Trace.Items) != 0 {
		t.Fatal("version 1 trace carries version 4 fields", err)
	}
	_ = hex.EncodeToString
}

// Version 6 (section 0.6): the keyed reduction and the finalised root; the
// two documents' P values from STEP1-NOTES.md section 9; the key is the
// register each word enters as it stands before the absorb, so the same
// item at two states gives two roots, and version 4's root is not version
// 6's; versions 4 and 6 differ only in step 2.
func TestVersionSix(t *testing.T) {
	for w, want := range map[uint32]string{128: "18d733c6e7e4244cd727d75baa9d94da3d75f8eed45c07158d0ed01c3e6941b8", 256: "5caa3f31d8684e38e673ec0d4a9b66e7a1457f237eea85dd96515066866f221b"} {
		d := strataDoc(w)
		d.Version = Version6
		if err := d.Validate(); err != nil {
			t.Fatal(err)
		}
		if d.Variant() != "v6-d2048-s0-r64-k32-p256-w"+itoa(w) || len(d.ParamsBytes()) != 52 {
			t.Fatal(d.Variant())
		}
		if want != "" {
			if got := hexOf(d.ParamsHash()); got != want {
				t.Fatalf("v6 w %d: P %s, the document says %s", w, got, want)
			}
		}
		q, err := ParamsFromDocument([]byte(`{"version": 6, "dataset_mib": 2048, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": ` + itoa(w) + `}`))
		if err != nil || q != d {
			t.Fatal(q, err)
		}
	}
	for _, w := range []uint32{128, 256, 1024} {
		p4 := Params{ChainID: 7310, Version: Version4, DatasetBytes: 1 << 16, ScratchBytes: 0, Rounds: 8, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 1 << 20, Parents: 4, ItemBytes: w}
		p6 := p4
		p6.Version = Version6
		l6 := p6.NewLight(0)
		var hdr [32]byte
		st, err := p6.Compute(l6, hdr, 0, 1, true)
		if err != nil {
			t.Fatal(err)
		}
		// h is keyed by the pre-absorb registers and finalised: recomputed here from the trace
		regs := st.Trace.AfterFill
		for r, item := range st.Trace.Items {
			n := len(item)
			z := make([]uint32, n)
			for j := 0; j < n; j++ {
				z[j] = item[j] ^ regs[j%8][(j/8)%LaneWords] ^ (uint32(j+1) * Mul)
			}
			for m := n; m >= 2; m /= 2 {
				for i := 0; i < m/2; i++ {
					x, y := z[2*i], z[2*i+1]
					z[i] = ((x ^ rotl(y, 13)) * Mul) + (y >> 7)
				}
			}
			h := z[0]
			h ^= h >> 16
			h *= Mul
			h ^= h >> 15
			if h != st.Trace.H[r] {
				t.Fatalf("w %d round %d: h %d, recomputed %d", w, r, st.Trace.H[r], h)
			}
			if tree(item) == st.Trace.H[r] {
				t.Fatalf("w %d round %d: version 6's h equals version 4's bare root", w, r)
			}
			regs = st.Trace.Rounds[r]
		}
		// the same seed under version 4 gives another mix (the pids differ, so the seeds do too; the loop differs at step 2)
		st4, _ := p4.Compute(p4.NewLight(0), hdr, 0, 1, false)
		if st4.Mix == st.Mix {
			t.Fatal("version 6 mix equals version 4's")
		}
	}
	// scratch_kib 1 and 2 admitted (revision 0.12): the 2 KiB control's document validates
	c := strataDoc(128)
	c.ScratchBytes = 2 << 10
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.ScratchBytes = 1 << 10
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.ScratchBytes = 3 << 10
	if c.Validate() == nil {
		t.Fatal("3 KiB accepted")
	}
}

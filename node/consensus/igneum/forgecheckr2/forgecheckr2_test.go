package forgecheckr2

import (
	"bytes"
	"testing"

	"lukechampine.com/blake3"
)

func TestEncodingExact(t *testing.T) {
	j := Job{ChainID: 7310, Version: 2, Height: 0x0102030405060708, EpochID: 9}
	for i := range j.ParentID {
		j.ParentID[i], j.ParamsID[i], j.Target[i], j.TxCommitment[i], j.Reward[i] = 0xa0+byte(i), 0xb0+byte(i), 0xc0+byte(i), 0xd0+byte(i), 0xe0+byte(i)
	}
	x := j.Encode()
	want := []byte{19}
	want = append(want, "Igneum/Forge/job/v2"...)
	want = append(want, 0x8e, 0x1c, 0, 0, 0, 0, 0, 0) // 7310 little-endian
	want = append(want, 2, 0, 0, 0)
	want = append(want, j.ParentID[:]...)
	want = append(want, 8, 7, 6, 5, 4, 3, 2, 1)
	want = append(want, 9, 0, 0, 0, 0, 0, 0, 0)
	want = append(want, j.ParamsID[:]...)
	want = append(want, j.Target[:]...)
	want = append(want, j.TxCommitment[:]...)
	want = append(want, j.Reward[:]...)
	if len(x) != JobLen || !bytes.Equal(x, want) {
		t.Fatalf("job\n%x\n%x", x, want)
	}
	if g, err := Decode(x); err != nil || g != j {
		t.Fatal(err)
	}
	if JobID(x) != blake3.Sum256(x) {
		t.Fatal("job id is the plain hash")
	}
	// H_d framing and the nine domain lengths of 2.1
	for d, n := range map[string]int{DomainJob: 19, DomainReward: 22, DomainEpoch: 21, DomainFill: 20, DomainLeaf: 20, DomainNode: 20, DomainTicket: 22, DomainChallenge: 25, DomainPublic: 22} {
		if len(d) != n {
			t.Fatalf("%s has %d bytes, the table says %d", d, len(d), n)
		}
	}
	var a [32]byte
	a[0] = 1
	if EpochSeed(a, 5, j.ParamsID) != blake3.Sum256(append(append(append(append([]byte{21}, DomainEpoch...), a[:]...), 5, 0, 0, 0, 0, 0, 0, 0), j.ParamsID[:]...)) {
		t.Fatal("H_epoch")
	}
	r, err := RewardCommitment([20]byte{1}, []byte("ab"))
	if err != nil {
		t.Fatal(err)
	}
	pre := append([]byte{22}, DomainReward...)
	pre = append(pre, 20)
	pre = append(pre, make([]byte, 20)...)
	pre[len(pre)-20] = 1
	pre = append(pre, 2, 'a', 'b')
	if r != blake3.Sum256(pre) {
		t.Fatal("H_reward")
	}
}

func TestStrictAndObjects(t *testing.T) {
	x := Job{Version: 2}.Encode()
	for name, bad := range map[string][]byte{
		"207 bytes":        x[:207],
		"209 bytes":        append(append([]byte(nil), x...), 0),
		"domain length 18": func() []byte { b := append([]byte(nil), x...); b[0] = 18; return b }(),
		"domain bytes":     func() []byte { b := append([]byte(nil), x...); b[3] ^= 0x20; return b }(),
		"version 1":        func() []byte { b := append([]byte(nil), x...); b[28] = 1; return b }(),
		"version 3":        func() []byte { b := append([]byte(nil), x...); b[28] = 3; return b }(),
	} {
		if _, err := Decode(bad); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := RewardCommitment([20]byte{}, make([]byte, 33)); err == nil {
		t.Fatal("33 bytes of extra data accepted")
	}
	if _, err := RewardCommitment([20]byte{}, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := Root([][]byte{{1}, {2}, {3}}); err == nil {
		t.Fatal("three leaves accepted as a commitment")
	}
	// the tree: two leaves is one node; four is two levels
	l := [][]byte{{1}, {2}, {3}, {4}}
	r4, _ := Root(l)
	if r4 != Node(Node(Leaf(0, l[0]), Leaf(1, l[1])), Node(Leaf(2, l[2]), Leaf(3, l[3]))) {
		t.Fatal("tree shape")
	}
	var jid, root, pid [32]byte
	if _, err := Ticket(jid, root, 0, 3, 3, pid); err == nil {
		t.Fatal("j = B accepted")
	}
	t0, _ := Ticket(jid, root, 0, 0, 3, pid)
	t1, _ := Ticket(jid, root, 0, 1, 3, pid)
	tb, _ := Ticket(jid, root, 0, 0, 4, pid)
	if t0 == t1 || t0 == tb {
		t.Fatal("j and B are bound")
	}
	// qualifies: inclusive, big-endian
	if !Qualifies([32]byte{1}, [32]byte{1}) || Qualifies([32]byte{2}, [32]byte{1}) || !Qualifies([32]byte{0, 0xff}, [32]byte{1}) {
		t.Fatal("qualifies")
	}
	// the challenge stream: the k-th word of one XOF, so the first four of q = 8 equal the first four of q = 4
	c8, c4 := Challenges(t0, 8, 1024), Challenges(t0, 4, 1024)
	for k := range c4 {
		if c4[k] != c8[k] || c4[k] >= 1024 {
			t.Fatal("challenge stream")
		}
	}
	if AnchorHeight(3, 100, 100) != 200 || AnchorHeight(0, 100, 100) != 0 || AnchorHeight(1, 100, 150) != 0 {
		t.Fatal("anchor height")
	}
}

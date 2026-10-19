//go:build igneumhash

package stratum

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"lukechampine.com/blake3"

	"igneum.network/pool/internal/validate"
)

// The fee-waiver handshake (ED-08, ED-17; the founder's overnight order of 11 October 2026): the company pool
// declares 100 bps and waives it for a session whose miner reports its dev fee on under a signature the pool
// verified; the answer is on the subscribe result and inside every receipt's signed message (version 1).

func feeSetup(t *testing.T, feeBps uint32, waiver bool) (*Server, *recordingSink, *client) {
	t.Helper()
	sink := &recordingSink{}
	srv := NewServer(validate.Null{}, sink)
	srv.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	srv.SignedShares = true
	srv.Verifier = Ed25519Blake3{}
	srv.DevFeeVerifier = Ed25519Blake3{}
	srv.ChainID = 7317
	r, err := NewEd25519Receipts()
	if err != nil {
		t.Fatal(err)
	}
	srv.Receipts = r
	srv.FeeBps = feeBps
	srv.FeeWaiver = waiver
	a, b := net.Pipe()
	go srv.ServeConn(context.Background(), a)
	t.Cleanup(func() { b.Close() })
	return srv, sink, &client{t: t, conn: b, r: bufio.NewReader(b)}
}

func signedDevFee(priv ed25519.PrivateKey, pub ed25519.PublicKey, percent uint32, worker string) DevFee {
	d := DevFee{Percent: percent, Address: "0x00000000000000000000000000000000000000fe", ManifestSHA256: strings.Repeat("ab", 32), Worker: worker, SessionKey: hex.EncodeToString(pub)}
	d.Signature = hex.EncodeToString(ed25519.Sign(priv, DevFeeMessage(d)))
	return d
}

func authSig(session [16]byte, address string, key ed25519.PrivateKey) string {
	addr, _ := hex.DecodeString(strings.TrimPrefix(address, "0x"))
	h := blake3.New(32, nil)
	h.Write([]byte("ember-authorize"))
	h.Write(session[:])
	h.Write(addr)
	return hex.EncodeToString(ed25519.Sign(key, h.Sum(nil)))
}

func subscribeWith(t *testing.T, c *client, d *DevFee) SubscribeResult {
	t.Helper()
	var params interface{} = []string{"test/0"}
	if d != nil {
		params = []interface{}{"test/0", map[string]interface{}{"dev_fee": d}}
	}
	res, e := c.call(MethodSubscribe, params)
	if e != nil {
		t.Fatalf("subscribe: %+v", e)
	}
	var sub SubscribeResult
	if err := json.Unmarshal(res, &sub); err != nil {
		t.Fatal(err)
	}
	return sub
}

// shareSignature signs the share's message as Ed25519Blake3.Share verifies it.
func shareSignature(chainID uint64, poolID, header [32]byte, nonce uint64, mix, hash [32]byte, priv ed25519.PrivateKey) string {
	var u [8]byte
	h := blake3.New(32, nil)
	h.Write([]byte("IgneumPool/nc-share/v0"))
	binary.BigEndian.PutUint64(u[:], chainID)
	h.Write(u[:])
	h.Write(poolID[:])
	h.Write(header[:])
	binary.BigEndian.PutUint64(u[:], nonce)
	h.Write(u[:])
	h.Write(mix[:])
	h.Write(hash[:])
	return hex.EncodeToString(ed25519.Sign(priv, h.Sum(nil)))
}

// submitForReceipt sends a mining.submit and reads frames to its reply, returning the mining.receipt notification
// the pool writes before the reply (the receipt is pushed inside the submit's handling).
func (c *client) submitForReceipt(params interface{}) (Receipt, *Error) {
	c.t.Helper()
	c.id++
	p, _ := json.Marshal(params)
	line, _ := json.Marshal(map[string]interface{}{"id": c.id, "method": MethodSubmit, "params": json.RawMessage(p)})
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		c.t.Fatal(err)
	}
	var rc Receipt
	for {
		frame, err := ReadFrame(c.r)
		if err != nil {
			c.t.Fatal(err)
		}
		var n struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Error  *Error          `json:"error"`
		}
		if err := json.Unmarshal(frame, &n); err != nil {
			c.t.Fatal(err)
		}
		if n.Method == "mining.receipt" {
			if err := json.Unmarshal(n.Params, &rc); err != nil {
				c.t.Fatal(err)
			}
			continue
		}
		if n.Method != "" {
			continue
		}
		return rc, n.Error
	}
}

// TestFeeOnEverySessionAndReceiptV1 pins the founder's decision of 13 October 2026: the declared fee applies to a
// session whose dev fee is on and verified (nothing waived), the receipt carries it, the dev-fee state named.
func TestFeeOnEverySessionAndReceiptV1(t *testing.T) {
	srv, sink, c := feeSetup(t, 100, true)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	d := signedDevFee(priv, pub, 1, "worker1")
	sub := subscribeWith(t, c, &d)
	if sub.PoolFee == nil || sub.PoolFee.Waived || sub.PoolFee.Bps != 100 || !strings.Contains(sub.PoolFee.Reason, "dev fee 1 percent declared and verified") {
		t.Fatalf("the declared fee must apply with the dev fee on: %+v", sub.PoolFee)
	}
	var sess [16]byte
	sb, _ := hex.DecodeString(sub.SessionID)
	copy(sess[:], sb)
	if _, e := c.call(MethodAuthorize, []string{addr, "worker1", hex.EncodeToString(pub), authSig(sess, addr, priv)}); e != nil {
		t.Fatalf("authorize: %+v", e)
	}
	job := Job{ID: "00000000000000a1", Height: 10, Algorithm: validate.NullAlgorithm,
		ShareTarget: validate.TargetForDifficulty(16), Boundary: validate.TargetForDifficulty(1 << 20), Clean: true}
	job.Header[0] = 0xab
	srv.SetJob(job)
	c.readNotify()
	start, _ := strconv.ParseUint(sub.NonceStart, 10, 64)
	var poolID [32]byte
	pb, _ := hex.DecodeString(sub.PoolID)
	copy(poolID[:], pb)
	nonce, mix, hash := findNonce(t, job, start, job.ShareTarget)
	sig := shareSignature(srv.ChainID, poolID, job.Header, nonce, mix, hash, priv)
	submit := []interface{}{job.ID, strconv.FormatUint(nonce, 10), hex.EncodeToString(mix[:]), hex.EncodeToString(hash[:]), map[string]string{"key": hex.EncodeToString(pub), "sig": sig}}
	rc, e := c.submitForReceipt(submit)
	if e != nil {
		t.Fatalf("submit: %+v", e)
	}
	if shares, _ := sink.count(); shares != 1 {
		t.Fatalf("shares %d", shares)
	}
	// the receipt: version 2, the fee terms and the share's chain identity (the session's extranonce, the job's
	// height and parent) inside the signed message, verifiable by the member from the receipt alone
	if rc.Version != 2 || rc.DevFeePercent != 1 || rc.PoolFeeBps != 100 || rc.Height != job.Height || rc.Parent != hex.EncodeToString(job.Parent[:]) {
		t.Fatalf("receipt terms %+v", rc)
	}
	var sessExtra uint64
	eb, _ := hex.DecodeString(sub.ExtraNonce1)
	for _, b := range eb {
		sessExtra = sessExtra<<8 | uint64(b)
	}
	if rc.ExtraNonce != sessExtra {
		t.Fatalf("receipt extra nonce %d against the session's %d", rc.ExtraNonce, sessExtra)
	}
	sh, _ := hex.DecodeString(rc.ShareHash)
	kb, _ := hex.DecodeString(rc.Key)
	pv, _ := hex.DecodeString(rc.Prev)
	sg, _ := hex.DecodeString(rc.Sig)
	pr, _ := hex.DecodeString(rc.Parent)
	var u [8]byte
	var u4 [4]byte
	h := blake3.New(32, nil)
	h.Write([]byte("IgneumPool/receipt/v2"))
	binary.BigEndian.PutUint64(u[:], rc.Seq)
	h.Write(u[:])
	h.Write(sh)
	h.Write(kb)
	binary.BigEndian.PutUint64(u[:], rc.Weight)
	h.Write(u[:])
	binary.BigEndian.PutUint64(u[:], rc.Total)
	h.Write(u[:])
	h.Write(pv)
	binary.BigEndian.PutUint32(u4[:], rc.DevFeePercent)
	h.Write(u4[:])
	binary.BigEndian.PutUint32(u4[:], rc.PoolFeeBps)
	h.Write(u4[:])
	binary.BigEndian.PutUint64(u[:], rc.ExtraNonce)
	h.Write(u[:])
	binary.BigEndian.PutUint64(u[:], rc.Height)
	h.Write(u[:])
	h.Write(pr)
	msg := h.Sum(nil)
	pk := srv.Receipts.PublicKey()
	if hex.EncodeToString(msg) != rc.Digest || !ed25519.Verify(ed25519.PublicKey(pk[:]), msg, sg) {
		t.Fatalf("receipt v2 does not verify: %+v", rc)
	}
	fs := srv.FeeStats()
	if fs["sessions_waived"].(uint64) != 0 || fs["sessions_charged"].(uint64) != 1 {
		t.Fatalf("fee stats %+v", fs)
	}
}

// TestFeeOnEveryDevFeeState pins the declared fee on every dev-fee state (the founder's decision of 13 October
// 2026): another key, a bad signature, no report, the dev fee off, another pool's fee, and no declared fee.
func TestFeeOnEveryDevFeeState(t *testing.T) {
	srv, _, c := feeSetup(t, 100, true)
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
	d := signedDevFee(privA, pubA, 1, "worker1")
	sub := subscribeWith(t, c, &d)
	if sub.PoolFee == nil || sub.PoolFee.Waived || sub.PoolFee.Bps != 100 {
		t.Fatalf("the declared fee on subscribe: %+v", sub.PoolFee)
	}
	var sess [16]byte
	sb, _ := hex.DecodeString(sub.SessionID)
	copy(sess[:], sb)
	if _, e := c.call(MethodAuthorize, []string{addr, "worker1", hex.EncodeToString(pubB), authSig(sess, addr, privB)}); e != nil {
		t.Fatalf("authorize: %+v", e)
	}
	fs := srv.FeeStats()
	if fs["sessions_charged"].(uint64) != 1 || fs["sessions_waived"].(uint64) != 0 {
		t.Fatalf("every session is charged: %+v", fs)
	}
	// a bad signature: no waiver on the subscribe
	_, _, c2 := feeSetup(t, 100, true)
	pub2, priv2, _ := ed25519.GenerateKey(rand.Reader)
	bad := signedDevFee(priv2, pub2, 1, "worker1")
	bad.Signature = strings.Repeat("00", 64)
	sub2 := subscribeWith(t, c2, &bad)
	if sub2.PoolFee == nil || sub2.PoolFee.Waived || sub2.PoolFee.Bps != 100 || !strings.Contains(sub2.PoolFee.Reason, "signature not verified") {
		t.Fatalf("bad signature: %+v", sub2.PoolFee)
	}
	// no report: the declared fee; dev fee off: the declared fee
	_, _, c3 := feeSetup(t, 100, true)
	sub3 := subscribeWith(t, c3, nil)
	if sub3.PoolFee == nil || sub3.PoolFee.Waived || sub3.PoolFee.Bps != 100 {
		t.Fatalf("no report: %+v", sub3.PoolFee)
	}
	_, _, c4 := feeSetup(t, 100, true)
	off := signedDevFee(priv2, pub2, 0, "worker1")
	sub4 := subscribeWith(t, c4, &off)
	if sub4.PoolFee == nil || sub4.PoolFee.Waived || sub4.PoolFee.Bps != 100 || !strings.Contains(sub4.PoolFee.Reason, "dev fee off") {
		t.Fatalf("dev fee off: %+v", sub4.PoolFee)
	}
	// another pool without the waiver: its declared fee is its own charge whatever the miner reports
	_, _, c5 := feeSetup(t, 50, false)
	on := signedDevFee(priv2, pub2, 1, "worker1")
	sub5 := subscribeWith(t, c5, &on)
	if sub5.PoolFee == nil || sub5.PoolFee.Waived || sub5.PoolFee.Bps != 50 {
		t.Fatalf("no waiver pool: %+v", sub5.PoolFee)
	}
	// a pool with no declared fee answers no pool_fee
	_, _, c6 := feeSetup(t, 0, false)
	sub6 := subscribeWith(t, c6, &on)
	if sub6.PoolFee != nil {
		t.Fatalf("no fee pool answered %+v", sub6.PoolFee)
	}
}

//go:build igneumhash

package stratum

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"lukechampine.com/blake3"
)

// TestShareSignatureBindsChainID pins the chain binding of the share signature and the transition it needs (the
// old public test network (chain 7320) switch of 22:58Z, 12 October 2026: the installed app signed over the chain it read from the job, 7319,
// and the pool verified over 7320, so every share read "invalid"): a signature over one chain id verifies over
// that chain id alone, so Server.ChainIDPrev is the only way a share signed over the previous chain is accepted.
func TestShareSignatureBindsChainID(t *testing.T) {
	pk, sk, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var poolID, header, mix, hash [32]byte
	poolID[0], header[1], mix[2], hash[3] = 1, 2, 3, 4
	nonce := uint64(0x1234)
	sign := func(chainID uint64) string {
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
		return hex.EncodeToString(ed25519.Sign(sk, h.Sum(nil)))
	}
	key := hex.EncodeToString(pk)
	v := Ed25519Blake3{}
	sig := sign(7319)
	if _, ok := v.Share(7319, poolID, header, nonce, mix, hash, key, sig); !ok {
		t.Fatalf("a signature over 7319 must verify over 7319")
	}
	if _, ok := v.Share(7320, poolID, header, nonce, mix, hash, key, sig); ok {
		t.Fatalf("a signature over 7319 must not verify over 7320: the binding is the point")
	}
	// the server's transition: the current chain first, then the previous one, the share still the current job's
	srv := &Server{ChainID: 7320, ChainIDPrev: 7319, Verifier: v}
	_, okNow := srv.Verifier.Share(srv.ChainID, poolID, header, nonce, mix, hash, key, sig)
	_, okPrev := srv.Verifier.Share(srv.ChainIDPrev, poolID, header, nonce, mix, hash, key, sig)
	if okNow || !okPrev {
		t.Fatalf("the transition reads the previous chain id when the current fails: now %v prev %v", okNow, okPrev)
	}
}

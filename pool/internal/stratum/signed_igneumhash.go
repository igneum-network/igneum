//go:build igneumhash

// The non-custodial form's signature checks: Ed25519 (the standard
// library) over BLAKE3 messages (the node module's lukechampine.com/blake3,
// available in the igneumhash build through go.ih.mod). The plain
// vendored build carries no verifier and refuses the signed form.
package stratum

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"strings"

	"lukechampine.com/blake3"
)

// Ed25519Blake3 implements ShareVerifier.
type Ed25519Blake3 struct{}

func decodeKeySig(keyHex, sigHex string) (ed25519.PublicKey, []byte, bool) {
	k, err := hex.DecodeString(strings.TrimPrefix(keyHex, "0x"))
	if err != nil || len(k) != ed25519.PublicKeySize {
		return nil, nil, false
	}
	s, err := hex.DecodeString(strings.TrimPrefix(sigHex, "0x"))
	if err != nil || len(s) != ed25519.SignatureSize {
		return nil, nil, false
	}
	return ed25519.PublicKey(k), s, true
}

// Authorize implements ShareVerifier.
func (Ed25519Blake3) Authorize(sessionID [16]byte, address, keyHex, sigHex string) ([32]byte, bool) {
	var out [32]byte
	pk, sig, ok := decodeKeySig(keyHex, sigHex)
	if !ok {
		return out, false
	}
	addr, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(address), "0x"))
	if err != nil || len(addr) != 20 {
		return out, false
	}
	h := blake3.New(32, nil)
	h.Write([]byte("ember-authorize"))
	h.Write(sessionID[:])
	h.Write(addr)
	if !ed25519.Verify(pk, h.Sum(nil), sig) {
		return out, false
	}
	copy(out[:], pk)
	return out, true
}

// Share implements ShareVerifier.
func (Ed25519Blake3) Share(chainID uint64, poolID [32]byte, header [32]byte, nonce uint64, mix, hash [32]byte, keyHex, sigHex string) ([32]byte, bool) {
	var out [32]byte
	pk, sig, ok := decodeKeySig(keyHex, sigHex)
	if !ok {
		return out, false
	}
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
	if !ed25519.Verify(pk, h.Sum(nil), sig) {
		return out, false
	}
	copy(out[:], pk)
	return out, true
}

// Ed25519Receipts signs the pool's per-share receipts under a key generated
// at start (the private half never leaves the process; the public half is on
// the subscribe result as receipt_key).
type Ed25519Receipts struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

// NewEd25519Receipts generates the receipt key.
func NewEd25519Receipts() (*Ed25519Receipts, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	return &Ed25519Receipts{pub: pub, priv: priv}, nil
}

// PublicKey implements ReceiptSigner.
func (r *Ed25519Receipts) PublicKey() [32]byte {
	var out [32]byte
	copy(out[:], r.pub)
	return out
}

// Sign implements ReceiptSigner: the message is BLAKE3 over the receipt's
// fields in order; the running digest the next receipt chains on is that
// message.
func (r *Ed25519Receipts) Sign(seq uint64, shareHash, key [32]byte, weight, total uint64, digest [32]byte) ([64]byte, [32]byte) {
	var u [8]byte
	h := blake3.New(32, nil)
	h.Write([]byte("IgneumPool/receipt/v0"))
	binary.BigEndian.PutUint64(u[:], seq)
	h.Write(u[:])
	h.Write(shareHash[:])
	h.Write(key[:])
	binary.BigEndian.PutUint64(u[:], weight)
	h.Write(u[:])
	binary.BigEndian.PutUint64(u[:], total)
	h.Write(u[:])
	h.Write(digest[:])
	var msg [32]byte
	copy(msg[:], h.Sum(nil))
	var sig [64]byte
	copy(sig[:], ed25519.Sign(r.priv, msg[:]))
	return sig, msg
}

// SignV1 implements ReceiptSigner: the v0 fields and then the session's fee
// terms, u32be(devFeePercent) || u32be(poolFeeBps), under the v1 label.
func (r *Ed25519Receipts) SignV1(seq uint64, shareHash, key [32]byte, weight, total uint64, digest [32]byte, devFeePercent, poolFeeBps uint32) ([64]byte, [32]byte) {
	var u [8]byte
	var u4 [4]byte
	h := blake3.New(32, nil)
	h.Write([]byte("IgneumPool/receipt/v1"))
	binary.BigEndian.PutUint64(u[:], seq)
	h.Write(u[:])
	h.Write(shareHash[:])
	h.Write(key[:])
	binary.BigEndian.PutUint64(u[:], weight)
	h.Write(u[:])
	binary.BigEndian.PutUint64(u[:], total)
	h.Write(u[:])
	h.Write(digest[:])
	binary.BigEndian.PutUint32(u4[:], devFeePercent)
	h.Write(u4[:])
	binary.BigEndian.PutUint32(u4[:], poolFeeBps)
	h.Write(u4[:])
	var msg [32]byte
	copy(msg[:], h.Sum(nil))
	var sig [64]byte
	copy(sig[:], ed25519.Sign(r.priv, msg[:]))
	return sig, msg
}

// SignV2 implements ReceiptSigner: the v1 fields and then the share's chain
// identity, u64be(extraNonce) || u64be(height) || parent, under the v2
// label, so a stranger joins the receipt to its carried share from the
// receipt alone (the settlement team's ledger, 12 October 2026).
func (r *Ed25519Receipts) SignV2(seq uint64, shareHash, key [32]byte, weight, total uint64, digest [32]byte, devFeePercent, poolFeeBps uint32, extraNonce, height uint64, parent [32]byte) ([64]byte, [32]byte) {
	var u [8]byte
	var u4 [4]byte
	h := blake3.New(32, nil)
	h.Write([]byte("IgneumPool/receipt/v2"))
	binary.BigEndian.PutUint64(u[:], seq)
	h.Write(u[:])
	h.Write(shareHash[:])
	h.Write(key[:])
	binary.BigEndian.PutUint64(u[:], weight)
	h.Write(u[:])
	binary.BigEndian.PutUint64(u[:], total)
	h.Write(u[:])
	h.Write(digest[:])
	binary.BigEndian.PutUint32(u4[:], devFeePercent)
	h.Write(u4[:])
	binary.BigEndian.PutUint32(u4[:], poolFeeBps)
	h.Write(u4[:])
	binary.BigEndian.PutUint64(u[:], extraNonce)
	h.Write(u[:])
	binary.BigEndian.PutUint64(u[:], height)
	h.Write(u[:])
	h.Write(parent[:])
	var msg [32]byte
	copy(msg[:], h.Sum(nil))
	var sig [64]byte
	copy(sig[:], ed25519.Sign(r.priv, msg[:]))
	return sig, msg
}

// VerifyDevFee implements DevFeeVerifier: plain Ed25519 by the session key
// over DevFeeMessage (the product team's Ember form, no pre-hash).
func (Ed25519Blake3) VerifyDevFee(d DevFee) ([32]byte, bool) {
	var out [32]byte
	pk, sig, ok := decodeKeySig(d.SessionKey, d.Signature)
	if !ok {
		return out, false
	}
	if !ed25519.Verify(pk, DevFeeMessage(d), sig) {
		return out, false
	}
	copy(out[:], pk)
	return out, true
}

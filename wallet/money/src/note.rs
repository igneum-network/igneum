//! The note and its sealing (ADR-0001 section 4.3 items 2 and 6).
//!
//! A note is (v, pk, r) with d and a memo riding along for the recipient; cm = H(v, pk, r). An output as a block
//! carries it is (cm, ct_kem, body):
//!
//! ```text
//! (ct_kem, K) = ML-KEM-768.Encaps(ek_recipient; 32 fresh random bytes)          ct_kem 1,088 bytes
//! key         = B("Igneum Money stage 1 note key", K || ct_kem)                   32 bytes
//! plaintext   = 0x02 || v (16, LE) || r (32) || d (11) || memo_len (1) || memo padded to 128    189 bytes
//! body        = ChaCha20-Poly1305(key, nonce = 12 zero bytes, aad = cm, plaintext)               205 bytes
//! output      = cm (32) || ct_kem (1,088) || body (205)                                           1,325 bytes
//! ```
//!
//! One key, one message, so the nonce is fixed; binding ct_kem into the key and cm into the AAD ties the body to
//! its KEM ciphertext and to the commitment beside it. Every output is the same length whatever the memo: one
//! output shape (ADR section 4.7, the fingerprinting note). The recipient opens the body, recomputes cm from (v,
//! its own pk, r) and keeps the note only when it matches, so a body that opens by accident of the AEAD's
//! forgery bound is still refused.
//!
//! The sender's own copy (section 4.3 item 6): `OutgoingRecord`, the same plaintext plus the recipient's pk,
//! sealed under the sender's ivk with the commitment as its AAD, so the sender can later produce a disclosure
//! receipt for that one output from its seed alone (stage 4's receipt is not this cut's).

use chacha20poly1305::aead::{Aead, KeyInit, Payload};
use chacha20poly1305::{ChaCha20Poly1305, Key, Nonce};
use libcrux_ml_kem::mlkem768::{self, MlKem768Ciphertext};
use rand_core::{OsRng, RngCore};
use zeroize::Zeroizing;

use crate::hash::{b_derive, reduce_word, CircuitHash, H};
use crate::keys::{Address, FullViewingKey};
use crate::sizes;
use crate::{err, Result};

const CTX_NOTE_KEY: &str = "Igneum Money stage 1 note key";
const CTX_OUTGOING_KEY: &str = "Igneum Money stage 1 outgoing record key";
const PLAINTEXT_VERSION: u8 = 2;

/// A note as the owner holds it.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Note {
    /// The amount in wei, 128 bits (cut four's value rule: four 32-bit limbs in one word of the circuit).
    pub value: u128,
    /// The owner's recipient key commitment.
    pub pk: [u8; 32],
    /// The commitment's randomness.
    pub r: [u8; 32],
    /// The address's diversifier the sender used (F-756: it means nothing yet).
    pub d: [u8; sizes::DIVERSIFIER],
    /// Up to 128 bytes.
    pub memo: Vec<u8>,
}

impl Note {
    pub fn commitment(&self) -> [u8; 32] {
        H::commitment(self.value, &self.pk, &self.r)
    }

    fn plaintext(&self) -> Result<Zeroizing<[u8; sizes::NOTE_PLAINTEXT]>> {
        if self.memo.len() > sizes::MEMO_MAX {
            return err(format!("memo: {} bytes, the most is {}", self.memo.len(), sizes::MEMO_MAX));
        }
        let mut p = Zeroizing::new([0u8; sizes::NOTE_PLAINTEXT]);
        p[0] = PLAINTEXT_VERSION;
        p[1..17].copy_from_slice(&self.value.to_le_bytes());
        p[17..49].copy_from_slice(&self.r);
        p[49..60].copy_from_slice(&self.d);
        p[60] = self.memo.len() as u8;
        p[61..61 + self.memo.len()].copy_from_slice(&self.memo);
        Ok(p)
    }

    fn from_plaintext(p: &[u8], pk: [u8; 32]) -> Result<Note> {
        if p.len() != sizes::NOTE_PLAINTEXT || p[0] != PLAINTEXT_VERSION {
            return err("note: not a version 2 plaintext");
        }
        let memo_len = p[60] as usize;
        if memo_len > sizes::MEMO_MAX {
            return err("note: the memo length is past 128");
        }
        let mut value = [0u8; 16];
        value.copy_from_slice(&p[1..17]);
        let mut r = [0u8; 32];
        r.copy_from_slice(&p[17..49]);
        let mut d = [0u8; sizes::DIVERSIFIER];
        d.copy_from_slice(&p[49..60]);
        Ok(Note { value: u128::from_le_bytes(value), pk, r, d, memo: p[61..61 + memo_len].to_vec() })
    }
}

/// One output as a block carries it.
#[derive(Clone, PartialEq, Eq)]
pub struct Output {
    pub cm: [u8; 32],
    pub kem_ct: [u8; sizes::KEM_CIPHERTEXT],
    pub body: [u8; sizes::NOTE_BODY],
}

impl std::fmt::Debug for Output {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Output(cm {})", crate::hex::encode(&self.cm))
    }
}

impl Output {
    pub fn to_bytes(&self) -> Vec<u8> {
        let mut v = Vec::with_capacity(sizes::OUTPUT);
        v.extend_from_slice(&self.cm);
        v.extend_from_slice(&self.kem_ct);
        v.extend_from_slice(&self.body);
        v
    }

    pub fn from_bytes(bytes: &[u8]) -> Result<Output> {
        if bytes.len() != sizes::OUTPUT {
            return err(format!("output: {} bytes, not {}", bytes.len(), sizes::OUTPUT));
        }
        let mut cm = [0u8; 32];
        let mut kem_ct = [0u8; sizes::KEM_CIPHERTEXT];
        let mut body = [0u8; sizes::NOTE_BODY];
        cm.copy_from_slice(&bytes[..32]);
        kem_ct.copy_from_slice(&bytes[32..32 + sizes::KEM_CIPHERTEXT]);
        body.copy_from_slice(&bytes[32 + sizes::KEM_CIPHERTEXT..]);
        Ok(Output { cm, kem_ct, body })
    }

    /// The ciphertext alone (the commitment excluded): 1,293 bytes.
    pub fn ciphertext_len() -> usize {
        sizes::OUTPUT_CIPHERTEXT
    }
}

/// The sender's sealed copy of what it sent (section 4.3 item 6), under its own ivk.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct OutgoingRecord {
    pub cm: [u8; 32],
    /// ChaCha20-Poly1305 over (recipient pk 32 || note plaintext 181) under B(ivk-context, ivk || cm): 229 bytes.
    pub body: Vec<u8>,
}

fn aead_key(context: &str, material: &[u8]) -> Zeroizing<[u8; 32]> {
    Zeroizing::new(b_derive(context, material))
}

/// Seal a note of `value` with `memo` to `to`. Returns the note as the sender knows it and the output for the
/// block. The randomness r and the KEM's encapsulation randomness come from the operating system.
pub fn seal(to: &Address, value: u128, memo: &[u8]) -> Result<(Note, Output)> {
    let mut r = [0u8; 32];
    OsRng.fill_bytes(&mut r);
    let r = reduce_word(&r);
    let mut encaps_randomness = Zeroizing::new([0u8; 32]);
    OsRng.fill_bytes(encaps_randomness.as_mut());
    seal_with(to, value, memo, r, *encaps_randomness)
}

/// `seal` with the randomness given (the vectors; never a caller's own randomness in a wallet); r must be a word.
pub fn seal_with(
    to: &Address,
    value: u128,
    memo: &[u8],
    r: [u8; 32],
    encaps_randomness: [u8; 32],
) -> Result<(Note, Output)> {
    if !crate::hash::is_word(&r) {
        return err("seal: the randomness is not a word");
    }
    let note = Note { value, pk: to.pk, r, d: to.d, memo: memo.to_vec() };
    let plaintext = note.plaintext()?;
    let cm = note.commitment();
    let (kem_ct, shared) = mlkem768::encapsulate(&to.kem_public_key(), encaps_randomness);
    let shared = Zeroizing::new(shared);
    let mut material = Zeroizing::new(Vec::with_capacity(32 + sizes::KEM_CIPHERTEXT));
    material.extend_from_slice(shared.as_ref());
    material.extend_from_slice(kem_ct.as_slice());
    let key = aead_key(CTX_NOTE_KEY, &material);
    let cipher = ChaCha20Poly1305::new(Key::from_slice(key.as_ref()));
    let sealed = cipher
        .encrypt(Nonce::from_slice(&[0u8; 12]), Payload { msg: plaintext.as_ref(), aad: &cm })
        .map_err(|_| crate::Error("seal: the AEAD refused".into()))?;
    let mut body = [0u8; sizes::NOTE_BODY];
    body.copy_from_slice(&sealed);
    let mut kem = [0u8; sizes::KEM_CIPHERTEXT];
    kem.copy_from_slice(kem_ct.as_slice());
    Ok((note, Output { cm, kem_ct: kem, body }))
}

/// Trial-decrypt one output with the viewing key. `Some(note)` when the output is this wallet's: the body opens
/// under the decapsulated key and the recomputed commitment equals the one carried.
pub fn open(fvk: &FullViewingKey, output: &Output) -> Option<Note> {
    let kem_ct = MlKem768Ciphertext::from(output.kem_ct);
    let shared = Zeroizing::new(mlkem768::decapsulate(fvk.kem_private_key(), &kem_ct));
    let mut material = Zeroizing::new(Vec::with_capacity(32 + sizes::KEM_CIPHERTEXT));
    material.extend_from_slice(shared.as_ref());
    material.extend_from_slice(&output.kem_ct);
    let key = aead_key(CTX_NOTE_KEY, &material);
    let cipher = ChaCha20Poly1305::new(Key::from_slice(key.as_ref()));
    let plaintext = cipher
        .decrypt(Nonce::from_slice(&[0u8; 12]), Payload { msg: &output.body, aad: &output.cm })
        .ok()?;
    let plaintext = Zeroizing::new(plaintext);
    let note = Note::from_plaintext(&plaintext, *fvk.pk()).ok()?;
    if note.commitment() != output.cm {
        return None;
    }
    Some(note)
}

/// The sender's record of one sealed output, under its own ivk.
pub fn record_outgoing(ivk: &[u8; 32], note: &Note, cm: &[u8; 32]) -> Result<OutgoingRecord> {
    let plaintext = note.plaintext()?;
    let mut msg = Zeroizing::new(Vec::with_capacity(32 + sizes::NOTE_PLAINTEXT));
    msg.extend_from_slice(&note.pk);
    msg.extend_from_slice(plaintext.as_ref());
    let mut material = Zeroizing::new([0u8; 64]);
    material[..32].copy_from_slice(ivk);
    material[32..].copy_from_slice(cm);
    let key = aead_key(CTX_OUTGOING_KEY, material.as_ref());
    let cipher = ChaCha20Poly1305::new(Key::from_slice(key.as_ref()));
    let body = cipher
        .encrypt(Nonce::from_slice(&[0u8; 12]), Payload { msg: msg.as_ref(), aad: cm })
        .map_err(|_| crate::Error("outgoing record: the AEAD refused".into()))?;
    Ok(OutgoingRecord { cm: *cm, body })
}

/// The sender reads its record back: the recipient's pk and the note.
pub fn open_outgoing(ivk: &[u8; 32], record: &OutgoingRecord) -> Option<Note> {
    let mut material = Zeroizing::new([0u8; 64]);
    material[..32].copy_from_slice(ivk);
    material[32..].copy_from_slice(&record.cm);
    let key = aead_key(CTX_OUTGOING_KEY, material.as_ref());
    let cipher = ChaCha20Poly1305::new(Key::from_slice(key.as_ref()));
    let msg = cipher.decrypt(Nonce::from_slice(&[0u8; 12]), Payload { msg: &record.body, aad: &record.cm }).ok()?;
    let msg = Zeroizing::new(msg);
    if msg.len() != 32 + sizes::NOTE_PLAINTEXT {
        return None;
    }
    let mut pk = [0u8; 32];
    pk.copy_from_slice(&msg[..32]);
    let note = Note::from_plaintext(&msg[32..], pk).ok()?;
    if note.commitment() != record.cm {
        return None;
    }
    Some(note)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::keys::Wallet;

    #[test]
    fn seal_and_open_round_trip_with_every_memo_length() {
        let w = Wallet::generate();
        for memo_len in [0usize, 1, 37, 128] {
            let memo: Vec<u8> = (0..memo_len).map(|i| i as u8).collect();
            let (note, out) = seal(&w.address(), 1_000_000_007u128 << 70, &memo).unwrap();
            assert_eq!(out.to_bytes().len(), sizes::OUTPUT);
            assert_eq!(sizes::OUTPUT, 1325);
            assert_eq!(sizes::OUTPUT_CIPHERTEXT, 1293);
            let got = open(w.fvk(), &out).expect("opens");
            assert_eq!(got, note);
            assert_eq!(got.commitment(), out.cm);
            assert_eq!(Output::from_bytes(&out.to_bytes()).unwrap(), out);
        }
        assert!(seal(&w.address(), 1, &[0u8; 129]).is_err());
    }

    #[test]
    fn another_wallet_cannot_open_and_tampering_is_refused() {
        let w = Wallet::generate();
        let other = Wallet::generate();
        let (_, out) = seal(&w.address(), 5, b"hello").unwrap();
        assert!(open(other.fvk(), &out).is_none());
        let mut t = out.clone();
        t.body[10] ^= 1;
        assert!(open(w.fvk(), &t).is_none());
        let mut t = out.clone();
        t.kem_ct[0] ^= 1;
        assert!(open(w.fvk(), &t).is_none());
        let mut t = out.clone();
        t.cm[0] ^= 1;
        assert!(open(w.fvk(), &t).is_none());
    }

    #[test]
    fn the_outgoing_record_round_trips_under_ivk_only() {
        let sender = Wallet::generate();
        let to = Wallet::generate();
        let (note, out) = seal(&to.address(), 42, b"rent").unwrap();
        let rec = record_outgoing(sender.fvk().ivk(), &note, &out.cm).unwrap();
        assert_eq!(open_outgoing(sender.fvk().ivk(), &rec).unwrap(), note);
        assert!(open_outgoing(to.fvk().ivk(), &rec).is_none());
    }
}

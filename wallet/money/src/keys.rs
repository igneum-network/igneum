//! Keys from one seed (ADR-0001 section 4.3 item 1), written down exactly.
//!
//! B is BLAKE3 in derive_key mode (`hash::b_derive`), the context strings below verbatim, the key material the
//! 32-byte seed s. H is the in-circuit hash (`hash::H`; the stand-in until the prover's instance lands).
//!
//! ```text
//! s          = 32 bytes from the operating system's randomness (getrandom)
//! sk_spend   = B("Igneum Money stage 1 spend",     s)            32 bytes   the spend authority (a preimage)
//! nk         = B("Igneum Money stage 1 nullifier", s)            32 bytes   the nullifier key
//! ivk        = B("Igneum Money stage 1 view",      s)            32 bytes   the outgoing-record key (section 4.3 item 6)
//! kem_seed   = B("Igneum Money stage 1 kem",       s)[0..64]     64 bytes   d || z of FIPS 203 algorithm 19
//! (ek, dk)   = ML-KEM-768.KeyGen_internal(kem_seed)              1,184 and 2,400 bytes
//! pk         = H(sk_spend, nk)                                   32 bytes   the recipient key commitment
//! address    = (pk, ek, d), d = 11 zero bytes                    1,227 bytes
//! ```
//!
//! The full viewing key is (pk, nk, kem_seed, ivk): it opens every incoming note (dk from kem_seed), computes every
//! nullifier (nk), and reads the wallet's own outgoing records (ivk). It cannot spend: sk_spend is derived from s
//! alone, pk hides it, and nothing in the viewing key reaches it. The ADR names the four derivations from s; this
//! cut keeps them as written and gives ivk the one role the ADR leaves it, the outgoing records.
//!
//! The diversifier d (F-756, OPEN): carried so the address has its final shape; it enters the note plaintext and
//! nothing else; two addresses under one seed differ only in d and are linkable by inspection, as the ADR says.
//! The per-d derivation is the panel's stage 1 design item, not this cut's.

use libcrux_ml_kem::mlkem768::{self, MlKem768KeyPair, MlKem768PrivateKey, MlKem768PublicKey};
use rand_core::{OsRng, RngCore};
use zeroize::{Zeroize, ZeroizeOnDrop, Zeroizing};

use crate::hash::{b_derive, b_derive_n, b_hash, reduce_word, CircuitHash, H};
use crate::sizes;
use crate::{err, hex, Result};

pub const CTX_SPEND: &str = "Igneum Money stage 1 spend";
pub const CTX_NULLIFIER: &str = "Igneum Money stage 1 nullifier";
pub const CTX_VIEW: &str = "Igneum Money stage 1 view";
pub const CTX_KEM: &str = "Igneum Money stage 1 kem";
const CTX_ADDRESS_CHECK: &str = "Igneum Money stage 1 address checksum";

/// The address encoding's version byte.
pub const ADDRESS_VERSION: u8 = 1;
/// The full viewing key encoding's version byte.
pub const FVK_VERSION: u8 = 1;
/// The address's text prefix.
pub const ADDRESS_PREFIX: &str = "money1";

/// The one secret: 32 bytes. Zeroised when dropped.
#[derive(Clone, Zeroize, ZeroizeOnDrop)]
pub struct Seed([u8; 32]);

impl Seed {
    /// A fresh seed from the operating system's randomness.
    pub fn generate() -> Seed {
        let mut s = [0u8; 32];
        OsRng.fill_bytes(&mut s);
        Seed(s)
    }

    pub fn from_bytes(bytes: [u8; 32]) -> Seed {
        Seed(bytes)
    }

    pub fn as_bytes(&self) -> &[u8; 32] {
        &self.0
    }
}

impl std::fmt::Debug for Seed {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Seed(..)")
    }
}

/// The part of the key material that cannot spend.
pub struct FullViewingKey {
    pk: [u8; 32],
    nk: [u8; 32],
    ivk: Zeroizing<[u8; 32]>,
    kem_seed: Zeroizing<[u8; 64]>,
    kem: MlKem768KeyPair,
}

impl Clone for FullViewingKey {
    fn clone(&self) -> FullViewingKey {
        // the KEM key pair type does not clone; it regenerates from the seed, deterministically
        FullViewingKey::assemble(self.pk, self.nk, *self.ivk, *self.kem_seed)
    }
}

impl std::fmt::Debug for FullViewingKey {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "FullViewingKey(pk {})", hex::encode(&self.pk))
    }
}

impl FullViewingKey {
    fn assemble(pk: [u8; 32], nk: [u8; 32], ivk: [u8; 32], kem_seed: [u8; 64]) -> FullViewingKey {
        let kem = mlkem768::generate_key_pair(kem_seed);
        FullViewingKey { pk, nk, ivk: Zeroizing::new(ivk), kem_seed: Zeroizing::new(kem_seed), kem }
    }

    pub fn pk(&self) -> &[u8; 32] {
        &self.pk
    }

    pub fn nk(&self) -> &[u8; 32] {
        &self.nk
    }

    /// The outgoing-record key.
    pub fn ivk(&self) -> &[u8; 32] {
        &self.ivk
    }

    pub fn kem_public_key(&self) -> &MlKem768PublicKey {
        self.kem.public_key()
    }

    pub fn kem_private_key(&self) -> &MlKem768PrivateKey {
        self.kem.private_key()
    }

    /// The address with the default diversifier (eleven zero bytes; F-756).
    pub fn address(&self) -> Address {
        self.address_with(&[0u8; sizes::DIVERSIFIER])
    }

    pub fn address_with(&self, d: &[u8; sizes::DIVERSIFIER]) -> Address {
        let mut ek = [0u8; sizes::KEM_PUBLIC_KEY];
        ek.copy_from_slice(self.kem.pk());
        Address { pk: self.pk, ek, d: *d }
    }

    /// The nullifier of a note of this wallet at a position in the commitment tree.
    pub fn nullifier(&self, cm: &[u8; 32], position: u64) -> [u8; 32] {
        H::nullifier(&self.nk, cm, position)
    }

    /// The serialised form: version, pk, nk, kem_seed, ivk. 161 bytes. A secret (it reads every amount).
    pub fn to_bytes(&self) -> Zeroizing<Vec<u8>> {
        let mut v = Vec::with_capacity(sizes::FULL_VIEWING_KEY);
        v.push(FVK_VERSION);
        v.extend_from_slice(&self.pk);
        v.extend_from_slice(&self.nk);
        v.extend_from_slice(self.kem_seed.as_ref());
        v.extend_from_slice(self.ivk.as_ref());
        Zeroizing::new(v)
    }

    pub fn from_bytes(bytes: &[u8]) -> Result<FullViewingKey> {
        if bytes.len() != sizes::FULL_VIEWING_KEY {
            return err(format!("viewing key: {} bytes, not {}", bytes.len(), sizes::FULL_VIEWING_KEY));
        }
        if bytes[0] != FVK_VERSION {
            return err(format!("viewing key: version {}, not {FVK_VERSION}", bytes[0]));
        }
        let mut pk = [0u8; 32];
        let mut nk = [0u8; 32];
        let mut kem_seed = [0u8; 64];
        let mut ivk = [0u8; 32];
        pk.copy_from_slice(&bytes[1..33]);
        nk.copy_from_slice(&bytes[33..65]);
        kem_seed.copy_from_slice(&bytes[65..129]);
        ivk.copy_from_slice(&bytes[129..161]);
        Ok(FullViewingKey::assemble(pk, nk, ivk, kem_seed))
    }
}

/// The whole key material of one wallet: the seed, the spending secret and the full viewing key.
#[derive(Clone)]
pub struct Wallet {
    seed: Seed,
    sk_spend: Zeroizing<[u8; 32]>,
    fvk: FullViewingKey,
}

impl std::fmt::Debug for Wallet {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Wallet(pk {})", hex::encode(&self.fvk.pk))
    }
}

impl Wallet {
    /// A new wallet from fresh randomness.
    pub fn generate() -> Wallet {
        Wallet::from_seed(Seed::generate())
    }

    /// Every key from the seed, by the derivation at the top of this module.
    pub fn from_seed(seed: Seed) -> Wallet {
        let s = seed.as_bytes();
        let sk_spend = Zeroizing::new(reduce_word(&b_derive(CTX_SPEND, s)));
        let nk = reduce_word(&b_derive(CTX_NULLIFIER, s));
        let ivk = b_derive(CTX_VIEW, s);
        let kem_seed: [u8; 64] = b_derive_n(CTX_KEM, s);
        let pk = H::pk(&sk_spend, &nk);
        let fvk = FullViewingKey::assemble(pk, nk, ivk, kem_seed);
        Wallet { seed, sk_spend, fvk }
    }

    pub fn seed(&self) -> &Seed {
        &self.seed
    }

    /// The spend authority: the preimage the spend statement proves knowledge of. Never printed by the tool.
    pub fn sk_spend(&self) -> &[u8; 32] {
        &self.sk_spend
    }

    pub fn fvk(&self) -> &FullViewingKey {
        &self.fvk
    }

    pub fn address(&self) -> Address {
        self.fvk.address()
    }
}

/// An address: (pk, ek, d), ADR section 4.3 item 1. 1,227 bytes; carried as text by `encode`.
#[derive(Clone, PartialEq, Eq)]
pub struct Address {
    pub pk: [u8; 32],
    pub ek: [u8; sizes::KEM_PUBLIC_KEY],
    pub d: [u8; sizes::DIVERSIFIER],
}

impl std::fmt::Debug for Address {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Address(pk {} d {})", hex::encode(&self.pk), hex::encode(&self.d))
    }
}

impl Address {
    pub fn to_bytes(&self) -> Vec<u8> {
        let mut v = Vec::with_capacity(sizes::ADDRESS);
        v.extend_from_slice(&self.pk);
        v.extend_from_slice(&self.ek);
        v.extend_from_slice(&self.d);
        v
    }

    pub fn from_bytes(bytes: &[u8]) -> Result<Address> {
        if bytes.len() != sizes::ADDRESS {
            return err(format!("address: {} bytes, not {}", bytes.len(), sizes::ADDRESS));
        }
        let mut pk = [0u8; 32];
        let mut ek = [0u8; sizes::KEM_PUBLIC_KEY];
        let mut d = [0u8; sizes::DIVERSIFIER];
        pk.copy_from_slice(&bytes[..32]);
        ek.copy_from_slice(&bytes[32..32 + sizes::KEM_PUBLIC_KEY]);
        d.copy_from_slice(&bytes[32 + sizes::KEM_PUBLIC_KEY..]);
        let a = Address { pk, ek, d };
        if !mlkem768::validate_public_key(&a.kem_public_key()) {
            return err("address: the encapsulation key fails FIPS 203's check");
        }
        Ok(a)
    }

    pub fn kem_public_key(&self) -> MlKem768PublicKey {
        MlKem768PublicKey::from(self.ek)
    }

    /// The short form of ADR section 4.3 item 1: B over the 1,227 bytes; meaningful only once the full address is
    /// published beside it. Not an address by itself.
    pub fn short_hash(&self) -> [u8; 32] {
        b_hash(&self.to_bytes())
    }

    /// The text form: `money1` then hex of (version, pk, ek, d, checksum 4). 2,470 characters; a QR code or a link,
    /// never typed. The checksum is the first four bytes of B("...address checksum", version || pk || ek || d).
    pub fn encode(&self) -> String {
        let mut body = Vec::with_capacity(1 + sizes::ADDRESS + 4);
        body.push(ADDRESS_VERSION);
        body.extend_from_slice(&self.to_bytes());
        let check = b_derive(CTX_ADDRESS_CHECK, &body);
        body.extend_from_slice(&check[..4]);
        format!("{ADDRESS_PREFIX}{}", hex::encode(&body))
    }

    pub fn decode(text: &str) -> Result<Address> {
        let text = text.trim();
        let Some(body_hex) = text.strip_prefix(ADDRESS_PREFIX) else {
            return err(format!("address: no '{ADDRESS_PREFIX}' prefix"));
        };
        let body = hex::decode(body_hex)?;
        if body.len() != 1 + sizes::ADDRESS + 4 {
            return err(format!("address: {} bytes, not {}", body.len(), 1 + sizes::ADDRESS + 4));
        }
        let (payload, check) = body.split_at(1 + sizes::ADDRESS);
        if payload[0] != ADDRESS_VERSION {
            return err(format!("address: version {}, not {ADDRESS_VERSION}", payload[0]));
        }
        let expect = b_derive(CTX_ADDRESS_CHECK, payload);
        if check != &expect[..4] {
            return err("address: the checksum fails; a character is wrong");
        }
        Address::from_bytes(&payload[1..])
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_derivation_is_deterministic_and_the_keys_differ() {
        let w1 = Wallet::generate();
        let w2 = Wallet::from_seed(w1.seed().clone());
        assert_eq!(w1.sk_spend(), w2.sk_spend());
        assert_eq!(w1.fvk().nk(), w2.fvk().nk());
        assert_eq!(w1.fvk().ivk(), w2.fvk().ivk());
        assert_eq!(w1.address(), w2.address());
        assert_ne!(w1.sk_spend(), w1.fvk().nk());
        assert_ne!(w1.sk_spend(), w1.fvk().ivk());
        assert_ne!(w1.fvk().nk(), w1.fvk().ivk());
        let other = Wallet::generate();
        assert_ne!(w1.address(), other.address());
    }

    #[test]
    fn sizes_hold() {
        let w = Wallet::generate();
        assert_eq!(w.fvk().kem_public_key().as_slice().len(), sizes::KEM_PUBLIC_KEY);
        assert_eq!(w.fvk().kem_private_key().as_slice().len(), sizes::KEM_PRIVATE_KEY);
        assert_eq!(w.address().to_bytes().len(), sizes::ADDRESS);
        assert_eq!(sizes::ADDRESS, 1227);
        assert_eq!(w.fvk().to_bytes().len(), sizes::FULL_VIEWING_KEY);
    }

    #[test]
    fn the_address_text_round_trips_and_a_wrong_character_is_caught() {
        let w = Wallet::generate();
        let a = w.address();
        let text = a.encode();
        assert!(text.starts_with(ADDRESS_PREFIX));
        assert_eq!(Address::decode(&text).unwrap(), a);
        let mut chars: Vec<char> = text.chars().collect();
        let i = 100;
        chars[i] = if chars[i] == '0' { '1' } else { '0' };
        let wrong: String = chars.into_iter().collect();
        assert!(Address::decode(&wrong).is_err());
        assert!(Address::decode(&text[..text.len() - 2]).is_err());
    }

    #[test]
    fn the_viewing_key_round_trips_and_matches_the_wallet() {
        let w = Wallet::generate();
        let bytes = w.fvk().to_bytes();
        let fvk = FullViewingKey::from_bytes(&bytes).unwrap();
        assert_eq!(fvk.address(), w.address());
        assert_eq!(fvk.nk(), w.fvk().nk());
        assert_eq!(fvk.kem_private_key().as_slice(), w.fvk().kem_private_key().as_slice());
        assert!(FullViewingKey::from_bytes(&bytes[..bytes.len() - 1]).is_err());
    }
}

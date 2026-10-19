//! Recoverable secp256k1 signatures over 32-byte hashes, with the message hashes of EIP-191 and EIP-712.
//! Deterministic nonces (RFC 6979) through `k256`; `s` is always in the low half, as every EVM client requires.

use k256::ecdsa::{RecoveryId, Signature as K256Signature, SigningKey, VerifyingKey};
use serde::{Deserialize, Serialize};

use crate::{hex, keccak::keccak256, Address, Error, Result};

#[derive(Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct Signature {
    #[serde(with = "hex32")]
    pub r: [u8; 32],
    #[serde(with = "hex32")]
    pub s: [u8; 32],
    /// the recovery id, 0 or 1 (the EIP-155 `v` and the message `v` are derived from it)
    pub recovery_id: u8,
}

impl Signature {
    pub fn sign(key: &SigningKey, hash: &[u8; 32]) -> Result<Self> {
        let (sig, recid): (K256Signature, RecoveryId) =
            key.sign_prehash_recoverable(hash).map_err(|e| Error::Key(format!("signing failed: {e}")))?;
        // k256 normalises s on signing; keep the guard so a future change cannot produce a high s unnoticed
        let (sig, recid) = match sig.normalize_s() {
            Some(low) => (low, RecoveryId::from_byte(recid.to_byte() ^ 1).expect("0 or 1")),
            None => (sig, recid),
        };
        let r_bytes = sig.r().to_bytes();
        let s_bytes = sig.s().to_bytes();
        let mut r = [0u8; 32];
        let mut s = [0u8; 32];
        r.copy_from_slice(&r_bytes);
        s.copy_from_slice(&s_bytes);
        Ok(Signature { r, s, recovery_id: recid.to_byte() })
    }

    /// The signer's address, recovered from the hash.
    pub fn recover(&self, hash: &[u8; 32]) -> Result<Address> {
        let sig = K256Signature::from_scalars(self.r, self.s).map_err(|e| Error::Key(format!("bad signature: {e}")))?;
        let recid = RecoveryId::from_byte(self.recovery_id).ok_or_else(|| Error::Key("bad recovery id".into()))?;
        let key = VerifyingKey::recover_from_prehash(hash, &sig, recid)
            .map_err(|e| Error::Key(format!("recovery failed: {e}")))?;
        Ok(Address::from_public_key(&key))
    }

    /// The 65-byte `r || s || v` form with `v = 27 + recovery_id`, as personal_sign and ERC-1271 use it.
    pub fn to_rsv(&self) -> [u8; 65] {
        let mut out = [0u8; 65];
        out[..32].copy_from_slice(&self.r);
        out[32..64].copy_from_slice(&self.s);
        out[64] = 27 + self.recovery_id;
        out
    }

    pub fn from_rsv(bytes: &[u8]) -> Result<Self> {
        if bytes.len() != 65 {
            return Err(Error::Input(format!("a signature has 65 bytes, not {}", bytes.len())));
        }
        let v = bytes[64];
        let recovery_id = match v {
            0 | 1 => v,
            27 | 28 => v - 27,
            _ => return Err(Error::Input(format!("bad v byte {v}"))),
        };
        let mut r = [0u8; 32];
        let mut s = [0u8; 32];
        r.copy_from_slice(&bytes[..32]);
        s.copy_from_slice(&bytes[32..64]);
        Ok(Signature { r, s, recovery_id })
    }
}

impl std::fmt::Debug for Signature {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Signature({})", hex::encode(&self.to_rsv()))
    }
}

/// EIP-191 version 0x45: keccak256("\x19Ethereum Signed Message:\n" + len + message).
pub fn personal_message_hash(message: &[u8]) -> [u8; 32] {
    let mut data = Vec::with_capacity(message.len() + 40);
    data.extend_from_slice(b"\x19Ethereum Signed Message:\n");
    data.extend_from_slice(message.len().to_string().as_bytes());
    data.extend_from_slice(message);
    keccak256(&data)
}

/// EIP-712: keccak256(0x19 0x01 || domainSeparator || hashStruct(message)).
pub fn typed_data_hash(domain_separator: &[u8; 32], struct_hash: &[u8; 32]) -> [u8; 32] {
    let mut data = Vec::with_capacity(66);
    data.extend_from_slice(&[0x19, 0x01]);
    data.extend_from_slice(domain_separator);
    data.extend_from_slice(struct_hash);
    keccak256(&data)
}

/// Serde for fixed 32-byte fields as hex.
pub mod hex32 {
    use serde::{Deserialize, Deserializer, Serializer};

    pub fn serialize<S: Serializer>(v: &[u8; 32], s: S) -> std::result::Result<S::Ok, S::Error> {
        s.serialize_str(&crate::hex::encode(v))
    }

    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> std::result::Result<[u8; 32], D::Error> {
        let text = String::deserialize(d)?;
        let bytes = crate::hex::decode(&text).map_err(serde::de::Error::custom)?;
        if bytes.len() != 32 {
            return Err(serde::de::Error::custom("expected 32 bytes"));
        }
        let mut out = [0u8; 32];
        out.copy_from_slice(&bytes);
        Ok(out)
    }
}

/// Serde for variable byte fields as hex.
pub mod hexbytes {
    use serde::{Deserialize, Deserializer, Serializer};

    pub fn serialize<S: Serializer>(v: &[u8], s: S) -> std::result::Result<S::Ok, S::Error> {
        s.serialize_str(&crate::hex::encode(v))
    }

    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> std::result::Result<Vec<u8>, D::Error> {
        let text = String::deserialize(d)?;
        crate::hex::decode(&text).map_err(serde::de::Error::custom)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn key() -> SigningKey {
        let bytes = hex::decode("0x4646464646464646464646464646464646464646464646464646464646464646").unwrap();
        SigningKey::from_slice(&bytes).unwrap()
    }

    #[test]
    fn sign_and_recover() {
        let k = key();
        let addr = Address::from_public_key(k.verifying_key());
        assert_eq!(addr.to_checksum(), "0x9d8A62f656a8d1615C1294fd71e9CFb3E4855A4F");
        let h = keccak256(b"a message");
        let sig = Signature::sign(&k, &h).unwrap();
        assert_eq!(sig.recover(&h).unwrap(), addr);
        // s is low
        assert!(sig.s[0] < 0x80);
        let rsv = sig.to_rsv();
        assert!(rsv[64] == 27 || rsv[64] == 28);
        assert_eq!(Signature::from_rsv(&rsv).unwrap(), sig);
        // a different hash does not recover the same address
        let other = keccak256(b"another message");
        assert_ne!(sig.recover(&other).unwrap(), addr);
    }

    #[test]
    fn personal_hash_vector() {
        // "hello world" under EIP-191: the value MetaMask and ethers agree on
        assert_eq!(
            hex::encode(&personal_message_hash(b"hello world")),
            "0xd9eba16ed0ecae432b71fe008c98cc872bb4cc214d3220a36f365326cf807d68"
        );
    }
}

//! An account address: 20 bytes, written with the EIP-55 checksum, read with the checksum verified when the text is
//! mixed case.

use std::fmt;

use k256::ecdsa::VerifyingKey;
use serde::{Deserialize, Deserializer, Serialize, Serializer};

use crate::{hex, keccak::keccak256, Error, Result};

#[derive(Clone, Copy, PartialEq, Eq, Hash, Default, PartialOrd, Ord)]
pub struct Address(pub [u8; 20]);

impl Address {
    pub fn from_slice(b: &[u8]) -> Result<Self> {
        if b.len() != 20 {
            return Err(Error::Input(format!("an address has 20 bytes, not {}", b.len())));
        }
        let mut a = [0u8; 20];
        a.copy_from_slice(b);
        Ok(Address(a))
    }

    /// The last 20 bytes of keccak256 of the uncompressed public key without its 0x04 prefix.
    pub fn from_public_key(key: &VerifyingKey) -> Self {
        let point = key.to_encoded_point(false);
        let bytes = point.as_bytes();
        let h = keccak256(&bytes[1..]);
        let mut a = [0u8; 20];
        a.copy_from_slice(&h[12..]);
        Address(a)
    }

    /// Parses `0x` plus 40 hex digits. All-lower or all-upper text is accepted as is; mixed case must be a valid
    /// EIP-55 checksum, so a mistyped address from a checksummed source is caught.
    pub fn parse(s: &str) -> Result<Self> {
        let t = s.trim();
        let body = t.strip_prefix("0x").or_else(|| t.strip_prefix("0X")).unwrap_or(t);
        if body.len() != 40 {
            return Err(Error::Input(format!("an address has 40 hex digits, not {}", body.len())));
        }
        let bytes = hex::decode(body)?;
        let addr = Address::from_slice(&bytes)?;
        let has_lower = body.bytes().any(|c| c.is_ascii_lowercase());
        let has_upper = body.bytes().any(|c| c.is_ascii_uppercase());
        if has_lower && has_upper && addr.to_checksum() != format!("0x{body}") {
            return Err(Error::Input("the address fails its checksum: one character is wrong".into()));
        }
        Ok(addr)
    }

    /// EIP-55: hash the lower-case hex, upper-case each digit whose hash nibble is 8 or more.
    pub fn to_checksum(&self) -> String {
        let lower = hex::encode(&self.0)[2..].to_string();
        let h = keccak256(lower.as_bytes());
        let mut out = String::with_capacity(42);
        out.push_str("0x");
        for (i, c) in lower.chars().enumerate() {
            let nibble = if i % 2 == 0 { h[i / 2] >> 4 } else { h[i / 2] & 15 };
            if c.is_ascii_alphabetic() && nibble >= 8 {
                out.push(c.to_ascii_uppercase());
            } else {
                out.push(c);
            }
        }
        out
    }

    /// The first 6 and last 4 hex digits with an ellipsis: what the window and a hardware device both show.
    pub fn short(&self) -> String {
        let c = self.to_checksum();
        format!("{}\u{2026}{}", &c[..8], &c[38..])
    }

    pub fn is_zero(&self) -> bool {
        self.0.iter().all(|b| *b == 0)
    }
}

impl fmt::Display for Address {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.to_checksum())
    }
}

impl fmt::Debug for Address {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.to_checksum())
    }
}

impl Serialize for Address {
    fn serialize<S: Serializer>(&self, s: S) -> std::result::Result<S::Ok, S::Error> {
        s.serialize_str(&self.to_checksum())
    }
}

impl<'de> Deserialize<'de> for Address {
    fn deserialize<D: Deserializer<'de>>(d: D) -> std::result::Result<Self, D::Error> {
        let s = String::deserialize(d)?;
        Address::parse(&s).map_err(serde::de::Error::custom)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn eip55_vectors() {
        for a in [
            "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
            "0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359",
            "0xdbF03B407c01E7cD3CBea99509d93f8DDDC8C6FB",
            "0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb",
        ] {
            let parsed = Address::parse(a).unwrap();
            assert_eq!(parsed.to_checksum(), a);
            assert_eq!(Address::parse(&a.to_lowercase()).unwrap(), parsed);
        }
        // one flipped case character fails
        assert!(Address::parse("0x5aaeb6053F3E94C9b9A09f33669435E7Ef1BeAed").is_err());
        assert!(Address::parse("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAe").is_err());
    }

    #[test]
    fn short_form() {
        let a = Address::parse("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed").unwrap();
        assert_eq!(a.short(), "0x5aAeb6\u{2026}eAed");
    }
}

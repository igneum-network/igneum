//! A 256-bit unsigned integer kept as 32 big-endian bytes. Only what a wallet needs: construction, comparison,
//! decimal and hex text, addition and small multiplication for parsing amounts, and the trimmed bytes RLP wants.
//! Derived ordering on the byte array is numeric ordering because the bytes are big-endian and fixed width.

use std::fmt;

use serde::{Deserialize, Deserializer, Serialize, Serializer};

use crate::{hex, Error, Result};

#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Default, Hash)]
pub struct U256(pub [u8; 32]);

impl U256 {
    pub const ZERO: U256 = U256([0u8; 32]);
    pub const MAX: U256 = U256([0xffu8; 32]);

    pub fn from_u64(v: u64) -> Self {
        Self::from_u128(v as u128)
    }

    pub fn from_u128(v: u128) -> Self {
        let mut b = [0u8; 32];
        b[16..].copy_from_slice(&v.to_be_bytes());
        U256(b)
    }

    /// Big-endian bytes of any length up to 32, left-padded.
    pub fn from_be_bytes(bytes: &[u8]) -> Result<Self> {
        if bytes.len() > 32 {
            return Err(Error::Input(format!("number of {} bytes does not fit 256 bits", bytes.len())));
        }
        let mut b = [0u8; 32];
        b[32 - bytes.len()..].copy_from_slice(bytes);
        Ok(U256(b))
    }

    pub fn is_zero(&self) -> bool {
        self.0.iter().all(|b| *b == 0)
    }

    /// The bytes with leading zeros removed; empty for zero (what RLP and JSON-RPC quantities use).
    pub fn to_be_trimmed(&self) -> &[u8] {
        let start = self.0.iter().position(|b| *b != 0).unwrap_or(32);
        &self.0[start..]
    }

    pub fn to_u128(&self) -> Option<u128> {
        if self.0[..16].iter().any(|b| *b != 0) {
            return None;
        }
        let mut b = [0u8; 16];
        b.copy_from_slice(&self.0[16..]);
        Some(u128::from_be_bytes(b))
    }

    pub fn to_u64(&self) -> Option<u64> {
        self.to_u128().and_then(|v| u64::try_from(v).ok())
    }

    /// Lossy, for displays and the settlement arithmetic only.
    pub fn to_f64(&self) -> f64 {
        self.0.iter().fold(0f64, |acc, b| acc * 256.0 + *b as f64)
    }

    pub fn checked_add(&self, other: &U256) -> Option<U256> {
        let mut out = [0u8; 32];
        let mut carry = 0u16;
        for i in (0..32).rev() {
            let s = self.0[i] as u16 + other.0[i] as u16 + carry;
            out[i] = (s & 0xff) as u8;
            carry = s >> 8;
        }
        if carry != 0 {
            None
        } else {
            Some(U256(out))
        }
    }

    pub fn checked_sub(&self, other: &U256) -> Option<U256> {
        if other > self {
            return None;
        }
        let mut out = [0u8; 32];
        let mut borrow = 0i16;
        for i in (0..32).rev() {
            let mut d = self.0[i] as i16 - other.0[i] as i16 - borrow;
            if d < 0 {
                d += 256;
                borrow = 1;
            } else {
                borrow = 0;
            }
            out[i] = d as u8;
        }
        Some(U256(out))
    }

    /// Multiplication by a small factor, for parsing and fee arithmetic.
    pub fn checked_mul_u64(&self, m: u64) -> Option<U256> {
        let mut out = [0u8; 32];
        let mut carry = 0u128;
        for i in (0..32).rev() {
            let p = self.0[i] as u128 * m as u128 + carry;
            out[i] = (p & 0xff) as u8;
            carry = p >> 8;
        }
        if carry != 0 {
            None
        } else {
            Some(U256(out))
        }
    }

    /// Division by a small divisor, returning (quotient, remainder).
    pub fn div_rem_u64(&self, d: u64) -> (U256, u64) {
        assert!(d != 0, "division by zero");
        let mut out = [0u8; 32];
        let mut rem = 0u128;
        for (o, b) in out.iter_mut().zip(self.0.iter()) {
            let cur = (rem << 8) | *b as u128;
            *o = (cur / d as u128) as u8;
            rem = cur % d as u128;
        }
        (U256(out), rem as u64)
    }

    pub fn pow10(n: u32) -> Option<U256> {
        let mut v = U256::from_u64(1);
        for _ in 0..n {
            v = v.checked_mul_u64(10)?;
        }
        Some(v)
    }

    pub fn from_dec_str(s: &str) -> Result<Self> {
        let t = s.trim();
        if t.is_empty() || !t.bytes().all(|c| c.is_ascii_digit()) {
            return Err(Error::Input(format!("not a decimal number: {s:?}")));
        }
        let mut v = U256::ZERO;
        for c in t.bytes() {
            v = v
                .checked_mul_u64(10)
                .and_then(|v| v.checked_add(&U256::from_u64((c - b'0') as u64)))
                .ok_or_else(|| Error::Input("number does not fit 256 bits".into()))?;
        }
        Ok(v)
    }

    pub fn to_dec_string(&self) -> String {
        if self.is_zero() {
            return "0".to_string();
        }
        let mut digits = Vec::new();
        let mut v = *self;
        while !v.is_zero() {
            let (q, r) = v.div_rem_u64(10);
            digits.push(b'0' + r as u8);
            v = q;
        }
        digits.reverse();
        String::from_utf8(digits).expect("ascii digits")
    }

    /// A JSON-RPC quantity (`0x1a`) or a plain hex number of any length up to 64 digits.
    pub fn from_hex(s: &str) -> Result<Self> {
        let bytes = hex::decode_quantity(s)?;
        Self::from_be_bytes(&bytes)
    }

    /// Minimal hex with `0x`, `0x0` for zero: the JSON-RPC quantity form.
    pub fn to_hex(&self) -> String {
        hex::encode_quantity(&self.0)
    }
}

impl fmt::Debug for U256 {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "U256({})", self.to_dec_string())
    }
}

impl fmt::Display for U256 {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.to_dec_string())
    }
}

impl From<u64> for U256 {
    fn from(v: u64) -> Self {
        U256::from_u64(v)
    }
}

impl Serialize for U256 {
    fn serialize<S: Serializer>(&self, s: S) -> std::result::Result<S::Ok, S::Error> {
        s.serialize_str(&self.to_hex())
    }
}

impl<'de> Deserialize<'de> for U256 {
    fn deserialize<D: Deserializer<'de>>(d: D) -> std::result::Result<Self, D::Error> {
        let s = String::deserialize(d)?;
        if s.starts_with("0x") || s.starts_with("0X") {
            U256::from_hex(&s).map_err(serde::de::Error::custom)
        } else {
            U256::from_dec_str(&s).map_err(serde::de::Error::custom)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn text_round_trips() {
        let one_ign = U256::from_dec_str("1000000000000000000").unwrap();
        assert_eq!(one_ign.to_dec_string(), "1000000000000000000");
        assert_eq!(one_ign.to_hex(), "0xde0b6b3a7640000");
        assert_eq!(U256::from_hex("0xde0b6b3a7640000").unwrap(), one_ign);
        assert_eq!(U256::ZERO.to_hex(), "0x0");
        assert_eq!(U256::ZERO.to_dec_string(), "0");
        assert_eq!(U256::from_hex("0x0").unwrap(), U256::ZERO);
        let max = U256::MAX;
        assert_eq!(
            max.to_dec_string(),
            "115792089237316195423570985008687907853269984665640564039457584007913129639935"
        );
        assert_eq!(U256::from_dec_str(&max.to_dec_string()).unwrap(), max);
    }

    #[test]
    fn arithmetic() {
        let a = U256::from_u64(250);
        let b = U256::from_u64(10);
        assert_eq!(a.checked_add(&b).unwrap(), U256::from_u64(260));
        assert_eq!(a.checked_sub(&b).unwrap(), U256::from_u64(240));
        assert!(b.checked_sub(&a).is_none());
        assert_eq!(a.checked_mul_u64(4).unwrap(), U256::from_u64(1000));
        assert!(U256::MAX.checked_add(&b).is_none());
        assert!(U256::MAX.checked_mul_u64(2).is_none());
        let (q, r) = U256::from_u64(1001).div_rem_u64(10);
        assert_eq!(q, U256::from_u64(100));
        assert_eq!(r, 1);
        assert_eq!(U256::pow10(18).unwrap().to_dec_string(), "1000000000000000000");
        assert!(U256::from_u64(5) < U256::from_u64(6));
        assert!(U256::from_u128(1u128 << 100) > U256::from_u64(u64::MAX));
        assert_eq!(U256::from_u64(0x1234).to_be_trimmed(), &[0x12, 0x34]);
        assert_eq!(U256::ZERO.to_be_trimmed(), &[] as &[u8]);
    }

    #[test]
    fn serde_hex() {
        let v = U256::from_u64(255);
        assert_eq!(serde_json::to_string(&v).unwrap(), "\"0xff\"");
        let back: U256 = serde_json::from_str("\"0xff\"").unwrap();
        assert_eq!(back, v);
        let dec: U256 = serde_json::from_str("\"255\"").unwrap();
        assert_eq!(dec, v);
    }
}

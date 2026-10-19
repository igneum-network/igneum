//! Hex with a `0x` prefix, as every EVM tool writes it.

use crate::{Error, Result};

const DIGITS: &[u8; 16] = b"0123456789abcdef";

pub fn encode(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(2 + bytes.len() * 2);
    s.push_str("0x");
    for b in bytes {
        s.push(DIGITS[(b >> 4) as usize] as char);
        s.push(DIGITS[(b & 15) as usize] as char);
    }
    s
}

/// Accepts an optional `0x`; refuses an odd length and any non-hex character.
pub fn decode(s: &str) -> Result<Vec<u8>> {
    let t = s.trim();
    let t = t.strip_prefix("0x").or_else(|| t.strip_prefix("0X")).unwrap_or(t);
    if t.len() % 2 != 0 {
        return Err(Error::Input(format!("hex of odd length ({})", t.len())));
    }
    let mut out = Vec::with_capacity(t.len() / 2);
    let b = t.as_bytes();
    for i in (0..b.len()).step_by(2) {
        let hi = val(b[i])?;
        let lo = val(b[i + 1])?;
        out.push((hi << 4) | lo);
    }
    Ok(out)
}

/// A quantity as JSON-RPC writes it: `0x` then minimal hex digits, `0x0` for zero.
pub fn encode_quantity(bytes_be: &[u8]) -> String {
    let trimmed: Vec<u8> = bytes_be.iter().copied().skip_while(|b| *b == 0).collect();
    if trimmed.is_empty() {
        return "0x0".to_string();
    }
    let full = encode(&trimmed);
    // drop one leading zero nibble when the first byte is below 0x10
    if trimmed[0] < 0x10 {
        format!("0x{}", &full[3..])
    } else {
        full
    }
}

/// Parses a quantity (`0x1a`, odd lengths allowed) into big-endian bytes.
pub fn decode_quantity(s: &str) -> Result<Vec<u8>> {
    let t = s.trim();
    let t = t.strip_prefix("0x").or_else(|| t.strip_prefix("0X")).unwrap_or(t);
    if t.is_empty() {
        return Err(Error::Input("empty quantity".into()));
    }
    let padded = if t.len() % 2 == 1 { format!("0{t}") } else { t.to_string() };
    decode(&padded)
}

fn val(c: u8) -> Result<u8> {
    match c {
        b'0'..=b'9' => Ok(c - b'0'),
        b'a'..=b'f' => Ok(c - b'a' + 10),
        b'A'..=b'F' => Ok(c - b'A' + 10),
        _ => Err(Error::Input(format!("not a hex digit: {}", c as char))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trip() {
        assert_eq!(encode(&[0, 1, 0xab, 0xff]), "0x0001abff");
        assert_eq!(decode("0x0001abff").unwrap(), vec![0, 1, 0xab, 0xff]);
        assert_eq!(decode("0001ABFF").unwrap(), vec![0, 1, 0xab, 0xff]);
        assert!(decode("0x123").is_err());
        assert!(decode("0xzz").is_err());
    }

    #[test]
    fn quantities() {
        assert_eq!(encode_quantity(&[0, 0]), "0x0");
        assert_eq!(encode_quantity(&[0, 0x1a]), "0x1a");
        assert_eq!(encode_quantity(&[0x04, 0x00]), "0x400");
        assert_eq!(decode_quantity("0x400").unwrap(), vec![4, 0]);
        assert_eq!(decode_quantity("0x0").unwrap(), vec![0]);
    }
}

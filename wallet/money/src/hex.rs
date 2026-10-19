//! Lower-case hex without a prefix, the form the command-line tool reads and writes.

use crate::{err, Result};

pub fn encode(bytes: &[u8]) -> String {
    const DIGITS: &[u8; 16] = b"0123456789abcdef";
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push(DIGITS[(b >> 4) as usize] as char);
        s.push(DIGITS[(b & 15) as usize] as char);
    }
    s
}

pub fn decode(s: &str) -> Result<Vec<u8>> {
    let s = s.trim();
    let s = s.strip_prefix("0x").unwrap_or(s);
    if s.len() % 2 != 0 {
        return err("hex: an odd number of digits");
    }
    let mut out = Vec::with_capacity(s.len() / 2);
    let digits = s.as_bytes();
    for i in (0..digits.len()).step_by(2) {
        let hi = nibble(digits[i])?;
        let lo = nibble(digits[i + 1])?;
        out.push((hi << 4) | lo);
    }
    Ok(out)
}

/// Exactly `N` bytes or a refusal that names the length.
pub fn decode_array<const N: usize>(s: &str, what: &str) -> Result<[u8; N]> {
    let v = decode(s)?;
    if v.len() != N {
        return err(format!("{what}: {} bytes, not {N}", v.len()));
    }
    let mut a = [0u8; N];
    a.copy_from_slice(&v);
    Ok(a)
}

fn nibble(c: u8) -> Result<u8> {
    match c {
        b'0'..=b'9' => Ok(c - b'0'),
        b'a'..=b'f' => Ok(c - b'a' + 10),
        b'A'..=b'F' => Ok(c - b'A' + 10),
        _ => err(format!("hex: '{}' is not a digit", c as char)),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trip() {
        let bytes: Vec<u8> = (0..=255).collect();
        assert_eq!(decode(&encode(&bytes)).unwrap(), bytes);
        assert!(decode("abc").is_err());
        assert!(decode("zz").is_err());
        assert!(decode_array::<4>("00112233", "x").is_ok());
        assert!(decode_array::<4>("001122", "x").is_err());
    }
}

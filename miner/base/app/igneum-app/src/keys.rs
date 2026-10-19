//! The payout key: a secp256k1 private key made on this machine, its EVM address, and the wallet file.
//! The wallet lives in the app data directory (`wallet.json`) with the permissions locked to the user:
//! 0600 on macOS and Linux, an icacls grant to the signed-in user alone on Windows.

use k256::elliptic_curve::sec1::ToEncodedPoint;
use k256::SecretKey;
use serde::{Deserialize, Serialize};
use sha3::{Digest, Keccak256};
use std::path::Path;

#[derive(Clone, Serialize, Deserialize)]
pub struct Wallet {
    pub address: String,     // 0x + 40 lower-case hex
    pub private_key: String, // 0x + 64 hex, secp256k1
    pub created: u64,        // unix seconds
}

pub fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

/// The EVM address of a secp256k1 private key: keccak256 of the uncompressed public key (without the 0x04 prefix), last 20 bytes.
pub fn address_of(sk: &SecretKey) -> String {
    let pk = sk.public_key();
    let point = pk.to_encoded_point(false);
    let bytes = point.as_bytes();
    let h = Keccak256::digest(&bytes[1..]);
    format!("0x{}", hex(&h[12..]))
}

/// A fresh key from the operating system's randomness.
pub fn generate() -> Wallet {
    loop {
        let mut raw = [0u8; 32];
        getrandom::getrandom(&mut raw).expect("os randomness");
        if let Ok(sk) = SecretKey::from_slice(&raw) {
            let address = address_of(&sk);
            return Wallet { address, private_key: format!("0x{}", hex(&raw)), created: crate::platform::unix_now() };
        }
    }
}

/// EIP-55 mixed-case form of a lower-case address, for display.
pub fn checksum(addr: &str) -> String {
    let body = addr.trim_start_matches("0x").to_ascii_lowercase();
    let h = Keccak256::digest(body.as_bytes());
    let mut out = String::with_capacity(42);
    out.push_str("0x");
    for (i, c) in body.chars().enumerate() {
        let nibble = (h[i / 2] >> (if i % 2 == 0 { 4 } else { 0 })) & 0xf;
        if c.is_ascii_alphabetic() && nibble >= 8 {
            out.push(c.to_ascii_uppercase());
        } else {
            out.push(c);
        }
    }
    out
}

/// True for 0x followed by 40 hex characters.
pub fn valid_address(s: &str) -> bool {
    let s = s.trim();
    s.len() == 42 && s.starts_with("0x") && s[2..].chars().all(|c| c.is_ascii_hexdigit())
}

pub fn save(path: &Path, w: &Wallet) -> std::io::Result<()> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)?;
        crate::platform::lock_permissions(dir, true);
    }
    let text = serde_json::to_string_pretty(w).expect("wallet json");
    std::fs::write(path, text)?;
    crate::platform::lock_permissions(path, false);
    Ok(())
}

pub fn load(path: &Path) -> Option<Wallet> {
    let text = std::fs::read_to_string(path).ok()?;
    serde_json::from_str(&text).ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn known_vector() {
        // The well-known test key 0x01: address 0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf
        let mut raw = [0u8; 32];
        raw[31] = 1;
        let sk = SecretKey::from_slice(&raw).unwrap();
        assert_eq!(checksum(&address_of(&sk)), "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf");
    }
}

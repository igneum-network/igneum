//! The bonded identity's secret in Ember (the hostile-key rule, docs/igneumhash/succession/DRAFT-HOSTILE-KEY.md;
//! the keyed rounds, SPEC-R0A-v0.md section 0.9, FEATURES bit 10 with KEYED_ROUNDS = S): under a keyed document
//! every attempt needs the secret, so the miner carries it to the worker inside the job and recomputes the S shares
//! for every result it submits. The rule's whole point is that the secret is the bond: Ember therefore keeps it in
//! memory only (zeroed at exit), takes it at start from the wallet or the operator (standard input, or an
//! environment variable it scrubs from its own environment at once), never writes it to a file, a log, a state
//! line or a report, and never passes it to a worker that has not declared the extension. The at-rest custody is
//! the wallet's (the product team), not this process's.
//!
//! Byte forms (the SPEC's): sk is 32 bytes; pk = BLAKE3(tag("IgneumHash/R0A/bond-pk") || sk) with
//! tag(T) = u8(len(T)) || T; the shares the verifier returns are S x 32 bytes in keyed-round order.
use igneum_worker_proto::{hex_encode, hex_to_32, SecretHex};
use std::io::Read;

pub const BOND_PK_TAG: &[u8] = b"IgneumHash/R0A/bond-pk";
/// The environment variable the secret may arrive in (hex of 32 bytes); removed from the environment on read.
pub const BOND_SK_ENV: &str = "EMBER_BOND_SK";
/// FEATURES bit 10 (value 1024): the bond-keyed rounds.
pub const FEATURE_KEYED: u64 = 1 << 10;

/// The secret, zeroed on drop, never formatted.
pub struct BondSecret {
    sk: [u8; 32],
}

impl std::fmt::Debug for BondSecret {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "BondSecret(pk {})", self.public_hex())
    }
}

impl Drop for BondSecret {
    fn drop(&mut self) {
        self.sk.fill(0);
    }
}

impl BondSecret {
    pub fn from_bytes(sk: [u8; 32]) -> BondSecret {
        BondSecret { sk }
    }

    /// From hex of 32 bytes (0x optional); the input string is not retained.
    pub fn from_hex(s: &str) -> Result<BondSecret, String> {
        let s = s.trim();
        let s = s.strip_prefix("0x").or_else(|| s.strip_prefix("0X")).unwrap_or(s);
        hex_to_32(s).map(BondSecret::from_bytes).ok_or_else(|| "the bond secret is not hex of 32 bytes".to_string())
    }

    /// The environment variable, taken once and removed so no child (a worker, a shell) inherits it.
    pub fn from_env() -> Result<Option<BondSecret>, String> {
        match std::env::var(BOND_SK_ENV) {
            Ok(v) => {
                std::env::remove_var(BOND_SK_ENV);
                BondSecret::from_hex(&v).map(Some)
            }
            Err(_) => Ok(None),
        }
    }

    /// One line of hex on a reader (standard input when the wallet or the operator pipes it).
    pub fn from_reader(r: &mut dyn Read) -> Result<BondSecret, String> {
        let mut buf = [0u8; 80];
        let mut n = 0;
        loop {
            let got = r.read(&mut buf[n..]).map_err(|e| format!("reading the bond secret: {e}"))?;
            if got == 0 || n + got >= buf.len() {
                n += got;
                break;
            }
            n += got;
            if buf[..n].contains(&b'\n') {
                break;
            }
        }
        let line = String::from_utf8_lossy(&buf[..n]).lines().next().unwrap_or("").to_string();
        buf.fill(0);
        BondSecret::from_hex(&line)
    }

    /// The public key pk = BLAKE3(tag || sk), the chain's registered form.
    pub fn public(&self) -> [u8; 32] {
        let mut h = blake3::Hasher::new();
        h.update(&[BOND_PK_TAG.len() as u8]);
        h.update(BOND_PK_TAG);
        h.update(&self.sk);
        *h.finalize().as_bytes()
    }

    pub fn public_hex(&self) -> String {
        hex_encode(&self.public())
    }

    /// The job line's form (a copy the worker's stdin takes; zeroed when that job is dropped).
    pub fn job_field(&self) -> SecretHex {
        SecretHex::from_bytes(&self.sk)
    }

    pub fn bytes(&self) -> &[u8; 32] {
        &self.sk
    }
}

/// The document's keyed rounds S when FEATURES carries bit 10 and keyed_rounds is at least 1; None for an unkeyed
/// document (every document before the rule, and any without the bit).
pub fn keyed_rounds(doc: &serde_json::Value) -> Option<u32> {
    let features = doc.get("features").and_then(|f| f.as_u64()).unwrap_or(0);
    if features & FEATURE_KEYED == 0 {
        return None;
    }
    doc.get("keyed_rounds").and_then(|s| s.as_u64()).filter(|s| *s >= 1).map(|s| s as u32)
}

/// What a submission carries beside nonce, mix and hash under a keyed document: the public key and the S shares.
#[derive(Clone, PartialEq)]
pub struct KeyedProof {
    pub pk: [u8; 32],
    /// S x 32 bytes.
    pub shares: Vec<u8>,
}

impl std::fmt::Debug for KeyedProof {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "KeyedProof(pk {}, {} shares)", hex_encode(&self.pk), self.shares.len() / 32)
    }
}

impl KeyedProof {
    pub fn to_json(&self) -> serde_json::Value {
        serde_json::json!({"pk": format!("0x{}", hex_encode(&self.pk)), "shares": format!("0x{}", hex_encode(&self.shares))})
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_secret_never_prints_and_the_public_key_is_the_tagged_hash() {
        let sk = [0x42u8; 32];
        let b = BondSecret::from_bytes(sk);
        let dbg = format!("{b:?}");
        assert!(!dbg.contains(&hex_encode(&sk)), "{dbg}");
        let mut h = blake3::Hasher::new();
        h.update(&[22u8]);
        h.update(b"IgneumHash/R0A/bond-pk");
        h.update(&sk);
        assert_eq!(b.public(), *h.finalize().as_bytes());
        let field = b.job_field();
        assert_eq!(format!("{field:?}"), "[withheld]");
        assert_eq!(field.to_bytes(), Some(sk));
    }

    #[test]
    fn the_environment_is_scrubbed_on_read() {
        std::env::set_var(BOND_SK_ENV, format!("0x{}", "ab".repeat(32)));
        let b = BondSecret::from_env().unwrap().expect("a secret");
        assert_eq!(b.bytes(), &[0xabu8; 32]);
        assert!(std::env::var(BOND_SK_ENV).is_err(), "the variable is gone after the read");
        assert!(BondSecret::from_env().unwrap().is_none());
        std::env::set_var(BOND_SK_ENV, "not hex");
        assert!(BondSecret::from_env().is_err());
        assert!(std::env::var(BOND_SK_ENV).is_err());
    }

    #[test]
    fn a_reader_gives_one_line() {
        let mut r = std::io::Cursor::new(format!("{}\nmore\n", "cd".repeat(32)).into_bytes());
        let b = BondSecret::from_reader(&mut r).unwrap();
        assert_eq!(b.bytes(), &[0xcdu8; 32]);
    }

    #[test]
    fn keyed_rounds_reads_bit_10_and_s() {
        let keyed = serde_json::json!({"version": 8, "features": 1056, "keyed_rounds": 2});
        assert_eq!(keyed_rounds(&keyed), Some(2));
        let unkeyed = serde_json::json!({"version": 8, "features": 32});
        assert_eq!(keyed_rounds(&unkeyed), None);
        let bit_no_s = serde_json::json!({"version": 8, "features": 1024});
        assert_eq!(keyed_rounds(&bit_no_s), None, "the bit without S is not a keyed document");
        assert_eq!(keyed_rounds(&serde_json::Value::Null), None);
    }
}

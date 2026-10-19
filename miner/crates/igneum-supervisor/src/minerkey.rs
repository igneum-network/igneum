//! The miner's work-authority key (the founder's word, 10 October 2026 23:54 UK): an Ed25519 key generated on
//! first run and kept under the user's config directory, never printed; its 32-byte public key goes into every
//! share a non-custodial pool takes, with a signature over the share's digest, so the pool credits the key and
//! cannot redirect what the key earned. The key-to-payout-address binding is signed once at sign-in.
//!
//! Byte forms (agreed with ecosystem and security, 11 October 2026 00:1x UK):
//! - share digest = BLAKE3("IgneumPool/nc-share/v0" || u64be(chain_id) || pool_id (32) || header_hash (32)
//!   || u64be(nonce) || mix (32) || hash (32)); the signature is Ed25519 over the 32-byte digest;
//! - authorize digest = BLAKE3("ember-authorize" || session_id (16 raw bytes) || address (20 raw bytes)).
use ed25519_dalek::{Signer, SigningKey, VerifyingKey};
use std::path::{Path, PathBuf};

pub const SHARE_TAG: &[u8] = b"IgneumPool/nc-share/v0";
pub const AUTHORIZE_TAG: &[u8] = b"ember-authorize";
/// The derivation tag of the fee session's key (MinerKey::derived; DESIGN.md 16.3).
pub const FEE_SESSION_TAG: &[u8] = b"ember-fee-session/v1";

pub struct MinerKey {
    key: SigningKey,
    pub path: PathBuf,
}

impl std::fmt::Debug for MinerKey {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        // the public half only; the secret is never formatted
        write!(f, "MinerKey({})", self.public_hex())
    }
}

impl MinerKey {
    /// Load the key at `path`, or generate one there (32 random bytes from the OS, mode 0600 on Unix).
    pub fn load_or_create(path: &Path) -> Result<MinerKey, String> {
        if path.is_file() {
            let bytes = std::fs::read(path).map_err(|e| format!("{}: {e}", path.display()))?;
            let arr: [u8; 32] = bytes.as_slice().try_into().map_err(|_| format!("{}: not a 32-byte key", path.display()))?;
            return Ok(MinerKey { key: SigningKey::from_bytes(&arr), path: path.to_path_buf() });
        }
        let mut seed = [0u8; 32];
        getrandom::getrandom(&mut seed).map_err(|e| format!("no randomness for the key: {e}"))?;
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
        }
        std::fs::write(path, seed).map_err(|e| format!("{}: {e}", path.display()))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600));
        }
        let key = SigningKey::from_bytes(&seed);
        seed.fill(0);
        Ok(MinerKey { key, path: path.to_path_buf() })
    }

    /// A key derived from this one under `tag` (BLAKE3 keyed by the secret over the tag): the fee session's own
    /// session key (DESIGN.md 16.3; the pool keys its receipt chain and running total per session key, so the
    /// member's and the fee's sessions each carry their own). Never written anywhere; the same every start.
    pub fn derived(&self, tag: &[u8]) -> MinerKey {
        let mut seed = *blake3::keyed_hash(self.key.as_bytes(), tag).as_bytes();
        let key = SigningKey::from_bytes(&seed);
        seed.fill(0);
        MinerKey { key, path: self.path.clone() }
    }

    pub fn public(&self) -> VerifyingKey {
        self.key.verifying_key()
    }

    pub fn public_hex(&self) -> String {
        hex(&self.public().to_bytes())
    }

    /// The share's signature (hex, 128 chars) over its digest.
    pub fn sign_share(&self, chain_id: u64, pool_id: &[u8; 32], header: &[u8; 32], nonce: u64, mix: &[u8], hash: &[u8; 32]) -> String {
        let d = share_digest(chain_id, pool_id, header, nonce, mix, hash);
        hex(&self.key.sign(&d).to_bytes())
    }

    /// The sign-in binding's signature (hex) over the authorize digest.
    pub fn sign_authorize(&self, session_id: &[u8], address: &[u8; 20]) -> String {
        let d = authorize_digest(session_id, address);
        hex(&self.key.sign(&d).to_bytes())
    }

    /// The dev-fee declaration's signature (hex, 128 chars): plain ed25519 over devfee::declaration_bytes, no
    /// pre-hash (ecosystem's wire form of 11 October 2026, the fee-waiver handshake).
    /// The fee session's "for" block signature (0.2.11, the founder's "1 percent total" by verifiable accounting; pool
    /// 0.1.36): Ed25519 by the MEMBER session's key over "igneum-fee-for\n<address lower>\n<fee session key hex lower>\n<percent>\n<window_shares>\n".
    pub fn sign_fee_for(&self, address_hex: &str, fee_session_key_hex: &str, percent: u8, window_shares: u64) -> String {
        let b = format!("igneum-fee-for\n{}\n{}\n{percent}\n{window_shares}\n", address_hex.to_ascii_lowercase(), fee_session_key_hex.to_ascii_lowercase());
        hex(&self.key.sign(b.as_bytes()).to_bytes())
    }

    pub fn sign_dev_fee(&self, percent: u8, address_hex: &str, manifest_sha256: &str, worker: &str) -> String {
        let b = crate::devfee::declaration_bytes(percent, address_hex, manifest_sha256, worker);
        hex(&self.key.sign(&b).to_bytes())
    }
}

pub fn share_digest(chain_id: u64, pool_id: &[u8; 32], header: &[u8; 32], nonce: u64, mix: &[u8], hash: &[u8; 32]) -> [u8; 32] {
    let mut h = blake3::Hasher::new();
    h.update(SHARE_TAG);
    h.update(&chain_id.to_be_bytes());
    h.update(pool_id);
    h.update(header);
    h.update(&nonce.to_be_bytes());
    h.update(mix);
    h.update(hash);
    *h.finalize().as_bytes()
}

pub fn authorize_digest(session_id: &[u8], address: &[u8; 20]) -> [u8; 32] {
    let mut h = blake3::Hasher::new();
    h.update(AUTHORIZE_TAG);
    h.update(session_id);
    h.update(address);
    *h.finalize().as_bytes()
}

/// A verifier's side of the same forms (the pool's; here for the tests and the CLI's self-check).
pub fn verify_share(public_hex: &str, sig_hex: &str, chain_id: u64, pool_id: &[u8; 32], header: &[u8; 32], nonce: u64, mix: &[u8], hash: &[u8; 32]) -> bool {
    let (Some(pk), Some(sig)) = (unhex(public_hex), unhex(sig_hex)) else { return false };
    let (Ok(pk), Ok(sig)) = (<[u8; 32]>::try_from(pk.as_slice()), <[u8; 64]>::try_from(sig.as_slice())) else { return false };
    let Ok(vk) = VerifyingKey::from_bytes(&pk) else { return false };
    let d = share_digest(chain_id, pool_id, header, nonce, mix, hash);
    vk.verify_strict(&d, &ed25519_dalek::Signature::from_bytes(&sig)).is_ok()
}

pub fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

pub fn unhex(s: &str) -> Option<Vec<u8>> {
    let s = s.trim_start_matches("0x");
    if s.len() % 2 != 0 {
        return None;
    }
    (0..s.len()).step_by(2).map(|i| u8::from_str_radix(&s[i..i + 2], 16).ok()).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_key_is_made_once_and_kept() {
        let dir = std::env::temp_dir().join(format!("ember-key-{}", std::process::id()));
        let path = dir.join("miner.key");
        let a = MinerKey::load_or_create(&path).unwrap();
        let b = MinerKey::load_or_create(&path).unwrap();
        assert_eq!(a.public_hex(), b.public_hex());
        assert_eq!(a.public_hex().len(), 64);
        assert_eq!(std::fs::read(&path).unwrap().len(), 32);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_signed_share_verifies_and_a_moved_one_does_not() {
        let dir = std::env::temp_dir().join(format!("ember-key2-{}", std::process::id()));
        let k = MinerKey::load_or_create(&dir.join("miner.key")).unwrap();
        let pool = [7u8; 32];
        let header = [9u8; 32];
        let hash = [3u8; 32];
        let mix = vec![5u8; 32];
        let sig = k.sign_share(7310, &pool, &header, 42, &mix, &hash);
        assert_eq!(sig.len(), 128);
        assert!(verify_share(&k.public_hex(), &sig, 7310, &pool, &header, 42, &mix, &hash));
        // another pool, another chain, another nonce: the share does not carry over
        assert!(!verify_share(&k.public_hex(), &sig, 7310, &[8u8; 32], &header, 42, &mix, &hash));
        assert!(!verify_share(&k.public_hex(), &sig, 7311, &pool, &header, 42, &mix, &hash));
        assert!(!verify_share(&k.public_hex(), &sig, 7310, &pool, &header, 43, &mix, &hash));
        // the digest's byte form is fixed: the tag, then big-endian fields, no length prefixes
        let d = share_digest(7310, &pool, &header, 42, &mix, &hash);
        let mut h = blake3::Hasher::new();
        h.update(b"IgneumPool/nc-share/v0");
        h.update(&7310u64.to_be_bytes());
        h.update(&pool);
        h.update(&header);
        h.update(&42u64.to_be_bytes());
        h.update(&mix);
        h.update(&hash);
        assert_eq!(d, *h.finalize().as_bytes());
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn the_derived_key_is_its_own_and_the_same_every_time() {
        let dir = std::env::temp_dir().join(format!("ember-key-derived-{}", std::process::id()));
        let k = MinerKey::load_or_create(&dir.join("key")).unwrap();
        let f1 = k.derived(FEE_SESSION_TAG);
        let f2 = k.derived(FEE_SESSION_TAG);
        assert_eq!(f1.public_hex(), f2.public_hex(), "the same tag gives the same key");
        assert_ne!(f1.public_hex(), k.public_hex(), "its own key, not the member's");
        assert_ne!(f1.public_hex(), k.derived(b"other").public_hex(), "another tag, another key");
        let sig = f1.sign_authorize(&[7u8; 16], &[9u8; 20]);
        let d = authorize_digest(&[7u8; 16], &[9u8; 20]);
        let vk = f1.public();
        let s = ed25519_dalek::Signature::from_bytes(&<[u8; 64]>::try_from(unhex(&sig).unwrap().as_slice()).unwrap());
        assert!(vk.verify_strict(&d, &s).is_ok());
        assert!(k.public().verify_strict(&d, &s).is_err(), "the member's key does not verify the fee key's signature");
        assert!(!dir.join("key").with_extension("fee").exists(), "nothing written for the derived key");
        let _ = std::fs::remove_dir_all(&dir);
    }
}

//! The vault: one JSON file holding the secret (a phrase or a raw key) sealed with Argon2id and
//! XChaCha20-Poly1305 under the person's password, next to the public facts (address, kind, creation time).
//! Nothing else is ever written to disk about the key.

use std::time::{SystemTime, UNIX_EPOCH};

use argon2::{Algorithm, Argon2, Params, Version};
use chacha20poly1305::{
    aead::{Aead, KeyInit},
    XChaCha20Poly1305, XNonce,
};
use rand_core::{OsRng, RngCore};
use serde::{Deserialize, Serialize};
use zeroize::{Zeroize, Zeroizing};

use crate::{hex, Address, Error, Result};

pub const VERSION: u32 = 1;

/// Argon2id parameters. The defaults are the design's starting point (256 MiB, 3 passes, 1 lane); they live in the
/// file so they can rise without a format change, and the box measures them before they are fixed.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct KdfParams {
    pub algorithm: String,
    pub m_kib: u32,
    pub t: u32,
    pub p: u32,
    pub salt: String,
}

impl KdfParams {
    pub fn default_desktop() -> Self {
        Self::with(256 * 1024, 3, 1)
    }

    /// For tests and constrained machines; never the default of a release.
    pub fn light() -> Self {
        Self::with(8 * 1024, 1, 1)
    }

    fn with(m_kib: u32, t: u32, p: u32) -> Self {
        let mut salt = [0u8; 16];
        OsRng.fill_bytes(&mut salt);
        KdfParams { algorithm: "argon2id".into(), m_kib, t, p, salt: hex::encode(&salt) }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct CipherParams {
    pub algorithm: String,
    pub nonce: String,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum SecretKind {
    Phrase,
    Key,
}

/// What the file says in clear: enough to show the address and the state without the password.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct Public {
    pub address: Address,
    pub kind: SecretKind,
    pub created_unix: u64,
    pub chain_id_hint: Option<u64>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct VaultFile {
    pub version: u32,
    pub kdf: KdfParams,
    pub cipher: CipherParams,
    /// the sealed secret, hex
    pub sealed: String,
    /// bound to the sealed secret as associated data: a swapped address is detected at open
    pub public: Public,
    /// mutable without the password: whether the words were written down and checked
    #[serde(default)]
    pub backed_up: bool,
}

/// The secret in clear, only ever in a zeroizing buffer.
#[derive(Serialize, Deserialize)]
pub struct Secret {
    pub kind: SecretKind,
    pub text: String,
}

impl Drop for Secret {
    fn drop(&mut self) {
        self.text.zeroize();
    }
}

pub fn now_unix() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

fn derive_key(password: &[u8], kdf: &KdfParams) -> Result<Zeroizing<[u8; 32]>> {
    if kdf.algorithm != "argon2id" {
        return Err(Error::Vault(format!("unknown kdf {}", kdf.algorithm)));
    }
    let salt = hex::decode(&kdf.salt)?;
    let params = Params::new(kdf.m_kib, kdf.t, kdf.p, Some(32))
        .map_err(|e| Error::Vault(format!("kdf parameters: {e}")))?;
    let argon = Argon2::new(Algorithm::Argon2id, Version::V0x13, params);
    let mut key = Zeroizing::new([0u8; 32]);
    argon
        .hash_password_into(password, &salt, key.as_mut())
        .map_err(|e| Error::Vault(format!("kdf: {e}")))?;
    Ok(key)
}

/// Seals the secret under the password. `password` must be at least 8 bytes (the window enforces 8 characters).
pub fn seal(secret: &Secret, password: &[u8], public: Public, kdf: KdfParams) -> Result<VaultFile> {
    if password.len() < 8 {
        return Err(Error::Vault("the password needs at least 8 characters".into()));
    }
    let key = derive_key(password, &kdf)?;
    let cipher = XChaCha20Poly1305::new(key.as_ref().into());
    let mut nonce_bytes = [0u8; 24];
    OsRng.fill_bytes(&mut nonce_bytes);
    let nonce = XNonce::from_slice(&nonce_bytes);
    let plain = Zeroizing::new(serde_json::to_vec(secret)?);
    // the public block is bound as associated data: a swapped address is detected at open
    let aad = serde_json::to_vec(&public)?;
    let sealed = cipher
        .encrypt(nonce, chacha20poly1305::aead::Payload { msg: &plain, aad: &aad })
        .map_err(|_| Error::Vault("sealing failed".into()))?;
    Ok(VaultFile {
        version: VERSION,
        kdf,
        cipher: CipherParams { algorithm: "xchacha20poly1305".into(), nonce: hex::encode(&nonce_bytes) },
        sealed: hex::encode(&sealed),
        public,
        backed_up: false,
    })
}

/// Opens the vault. A wrong password and a tampered file give the same words: nothing distinguishes them.
pub fn open(vault: &VaultFile, password: &[u8]) -> Result<Secret> {
    if vault.version != VERSION {
        return Err(Error::Vault(format!("vault version {} is not supported", vault.version)));
    }
    if vault.cipher.algorithm != "xchacha20poly1305" {
        return Err(Error::Vault(format!("unknown cipher {}", vault.cipher.algorithm)));
    }
    let key = derive_key(password, &vault.kdf)?;
    let cipher = XChaCha20Poly1305::new(key.as_ref().into());
    let nonce_bytes = hex::decode(&vault.cipher.nonce)?;
    if nonce_bytes.len() != 24 {
        return Err(Error::Vault("bad nonce".into()));
    }
    let sealed = hex::decode(&vault.sealed)?;
    let aad = serde_json::to_vec(&vault.public)?;
    let plain = Zeroizing::new(
        cipher
            .decrypt(XNonce::from_slice(&nonce_bytes), chacha20poly1305::aead::Payload { msg: &sealed, aad: &aad })
            .map_err(|_| Error::Vault("wrong password, or the vault file is damaged".into()))?,
    );
    let secret: Secret = serde_json::from_slice(&plain)?;
    Ok(secret)
}

/// The previous wallet's vault file (OLD-BASE.md step 3, 11 October 2026): `vault.json` with the address and the
/// source in clear, Argon2id (64 MiB, 3 passes, 1 lane by default; the parameters in the file) and
/// XChaCha20-Poly1305 over the JSON of `{private_key, mnemonic}` with no associated data. Read once, on the
/// password, and re-sealed in this format by the signer; never written again.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct LegacyVault {
    pub version: u32,
    pub address: String,
    pub source: String,
    #[serde(default)]
    pub created: u64,
    pub kdf: KdfParamsLegacy,
    pub cipher: String,
    pub nonce: String,
    pub ciphertext: String,
    #[serde(default)]
    pub backed_up: bool,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct KdfParamsLegacy {
    pub algo: String,
    pub m_kib: u32,
    pub t: u32,
    pub p: u32,
    pub salt: String,
}

#[derive(Serialize, Deserialize)]
struct LegacySecret {
    private_key: String,
    mnemonic: Option<String>,
}

impl Drop for LegacySecret {
    fn drop(&mut self) {
        self.private_key.zeroize();
        if let Some(m) = self.mnemonic.as_mut() {
            m.zeroize();
        }
    }
}

/// True when the text is the previous wallet's file (its `ciphertext` and `address` fields) and not this format.
pub fn is_legacy_text(text: &str) -> bool {
    serde_json::from_str::<serde_json::Value>(text)
        .map(|v| v.get("ciphertext").is_some() && v.get("address").is_some() && v.get("sealed").is_none())
        .unwrap_or(false)
}

/// Opens the previous wallet's vault with the password and returns the secret in this wallet's form: the phrase
/// when the old file carried one (the key is re-derived from it at index 0, as the old wallet did), else the key.
/// The old file's `backed_up` is returned beside it.
pub fn open_legacy(v: &LegacyVault, password: &[u8]) -> Result<(Secret, bool)> {
    if v.kdf.algo != "argon2id" || v.cipher != "xchacha20poly1305" {
        return Err(Error::Vault("this old vault was written by another wallet".into()));
    }
    let kdf = KdfParams { algorithm: v.kdf.algo.clone(), m_kib: v.kdf.m_kib, t: v.kdf.t, p: v.kdf.p, salt: v.kdf.salt.trim_start_matches("0x").to_string() };
    let key = derive_key(password, &kdf)?;
    let cipher = XChaCha20Poly1305::new(key.as_ref().into());
    let nonce_bytes = hex::decode(&with_0x(&v.nonce))?;
    if nonce_bytes.len() != 24 {
        return Err(Error::Vault("bad nonce in the old vault".into()));
    }
    let sealed = hex::decode(&with_0x(&v.ciphertext))?;
    let plain = Zeroizing::new(
        cipher
            .decrypt(XNonce::from_slice(&nonce_bytes), sealed.as_ref())
            .map_err(|_| Error::Vault("wrong password, or the old vault file is damaged".into()))?,
    );
    let old: LegacySecret = serde_json::from_slice(&plain)?;
    let secret = match &old.mnemonic {
        Some(m) if !m.trim().is_empty() => Secret { kind: SecretKind::Phrase, text: m.clone() },
        _ => Secret { kind: SecretKind::Key, text: with_0x(&old.private_key) },
    };
    Ok((secret, v.backed_up))
}

fn with_0x(s: &str) -> String {
    if s.starts_with("0x") || s.starts_with("0X") {
        s.to_string()
    } else {
        format!("0x{s}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The old wallet's seal, replayed with the same crates (its vault.rs: Argon2id, XChaCha20-Poly1305 over the
    /// JSON secret, no associated data, hex without 0x), so the reader is tested against a file the old code would
    /// have written.
    fn legacy_file(private_key: &str, mnemonic: Option<&str>, password: &str) -> LegacyVault {
        let salt = [7u8; 16];
        let nonce = [9u8; 24];
        let kdf = KdfParams { algorithm: "argon2id".into(), m_kib: 8 * 1024, t: 1, p: 1, salt: hex::encode(&salt)[2..].to_string() };
        let key = derive_key(password.as_bytes(), &kdf).unwrap();
        let cipher = XChaCha20Poly1305::new(key.as_ref().into());
        let plain = serde_json::to_vec(&serde_json::json!({"private_key": private_key, "mnemonic": mnemonic})).unwrap();
        let ct = cipher.encrypt(XNonce::from_slice(&nonce), plain.as_ref()).unwrap();
        LegacyVault {
            version: 1,
            address: "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf".into(),
            source: if mnemonic.is_some() { "seed".into() } else { "key".into() },
            created: 1,
            kdf: KdfParamsLegacy { algo: "argon2id".into(), m_kib: 8 * 1024, t: 1, p: 1, salt: hex::encode(&salt)[2..].to_string() },
            cipher: "xchacha20poly1305".into(),
            nonce: hex::encode(&nonce)[2..].to_string(),
            ciphertext: hex::encode(&ct)[2..].to_string(),
            backed_up: true,
        }
    }

    #[test]
    fn legacy_vault_opens_and_is_told_apart() {
        let words = "test test test test test test test test test test test junk";
        let v = legacy_file("0x0000000000000000000000000000000000000000000000000000000000000001", Some(words), "correct horse");
        let text = serde_json::to_string(&v).unwrap();
        assert!(is_legacy_text(&text));
        let (s, backed) = open_legacy(&v, b"correct horse").unwrap();
        assert_eq!(s.kind, SecretKind::Phrase);
        assert_eq!(s.text, words);
        assert!(backed);
        assert!(open_legacy(&v, b"correct horsf").is_err());
        let k = legacy_file("0000000000000000000000000000000000000000000000000000000000000001", None, "correct horse");
        let (s, _) = open_legacy(&k, b"correct horse").unwrap();
        assert_eq!(s.kind, SecretKind::Key);
        assert_eq!(s.text, "0x0000000000000000000000000000000000000000000000000000000000000001");
        // this format's file is not legacy
        let f = seal(&Secret { kind: SecretKind::Key, text: "0x01".into() }, b"correct horse", public(), KdfParams::light()).unwrap();
        assert!(!is_legacy_text(&serde_json::to_string(&f).unwrap()));
    }

    fn public() -> Public {
        Public {
            address: Address::parse("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266").unwrap(),
            kind: SecretKind::Phrase,
            created_unix: 1,
            chain_id_hint: Some(7310),
        }
    }

    #[test]
    fn seal_and_open() {
        let secret = Secret { kind: SecretKind::Phrase, text: "test test test junk".into() };
        let v = seal(&secret, b"correct horse", public(), KdfParams::light()).unwrap();
        let json = serde_json::to_string(&v).unwrap();
        assert!(!json.contains("test test"));
        let back: VaultFile = serde_json::from_str(&json).unwrap();
        let opened = open(&back, b"correct horse").unwrap();
        assert_eq!(opened.text, "test test test junk");
        assert!(open(&back, b"wrong password").is_err());
    }

    #[test]
    fn tampered_public_block_is_detected() {
        let secret = Secret { kind: SecretKind::Key, text: "0x01".into() };
        let mut v = seal(&secret, b"correct horse", public(), KdfParams::light()).unwrap();
        v.public.address = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        assert!(open(&v, b"correct horse").is_err());
    }

    #[test]
    fn short_password_refused() {
        let secret = Secret { kind: SecretKind::Key, text: "0x01".into() };
        assert!(seal(&secret, b"short", public(), KdfParams::light()).is_err());
    }
}

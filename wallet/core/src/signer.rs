//! The signing path. One trait, three implementations: a software key from the vault, a hardware device (the
//! transport lands with the device), and watch-only, which refuses and points to the envelope. Every transaction
//! signature goes through `sign_transaction`, which recomputes the sentences the person saw and refuses a mismatch.

use k256::ecdsa::SigningKey;
use serde::{Deserialize, Serialize};
use zeroize::Zeroizing;

use crate::{
    display, hex, mnemonic,
    sign::{personal_message_hash, Signature},
    tx::Transaction,
    Address, Error, Result,
};

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SignerKind {
    Software,
    Hardware,
    WatchOnly,
}

pub trait Signer {
    fn address(&self) -> Address;
    fn kind(&self) -> SignerKind;
    /// Signs a 32-byte hash the caller has already bound to a payload and its sentences.
    fn sign_hash(&self, hash: &[u8; 32]) -> Result<Signature>;
}

/// A key in memory, zeroized on drop (k256's SigningKey zeroizes itself).
pub struct SoftwareSigner {
    key: SigningKey,
    address: Address,
}

impl SoftwareSigner {
    pub fn from_key(key: SigningKey) -> Self {
        let address = Address::from_public_key(key.verifying_key());
        SoftwareSigner { key, address }
    }

    pub fn from_key_bytes(bytes: &[u8]) -> Result<Self> {
        let key = SigningKey::from_slice(bytes).map_err(|e| Error::Key(format!("not a valid key: {e}")))?;
        Ok(Self::from_key(key))
    }

    pub fn from_key_hex(text: &str) -> Result<Self> {
        let bytes = Zeroizing::new(hex::decode(text)?);
        if bytes.len() != 32 {
            return Err(Error::Key(format!("a private key has 32 bytes, not {}", bytes.len())));
        }
        Self::from_key_bytes(&bytes)
    }

    pub fn from_phrase(phrase: &str, index: u32) -> Result<Self> {
        Ok(Self::from_key(mnemonic::derive(phrase, index)?))
    }

    /// The raw key as hex, for the backup screen only; the caller zeroizes.
    pub fn export_key_hex(&self) -> Zeroizing<String> {
        Zeroizing::new(hex::encode(&self.key.to_bytes()))
    }
}

impl Signer for SoftwareSigner {
    fn address(&self) -> Address {
        self.address
    }

    fn kind(&self) -> SignerKind {
        SignerKind::Software
    }

    fn sign_hash(&self, hash: &[u8; 32]) -> Result<Signature> {
        Signature::sign(&self.key, hash)
    }
}

/// An address with no key here: everything but signing works; signing exports an envelope instead.
pub struct WatchOnly {
    address: Address,
}

impl WatchOnly {
    pub fn new(address: Address) -> Self {
        WatchOnly { address }
    }
}

impl Signer for WatchOnly {
    fn address(&self) -> Address {
        self.address
    }

    fn kind(&self) -> SignerKind {
        SignerKind::WatchOnly
    }

    fn sign_hash(&self, _hash: &[u8; 32]) -> Result<Signature> {
        Err(Error::Refused("this wallet is watch-only: prepare the transaction and sign it where the key is".into()))
    }
}

/// The hardware path (DESIGN.md 1.8): Ledger first over HID. The transport is not in this skeleton; the type
/// exists so the engine's state and the window's words are already right for it.
pub struct HardwareSigner {
    address: Address,
    pub device: String,
    pub path: String,
}

impl HardwareSigner {
    pub fn placeholder(address: Address, device: &str, index: u32) -> Self {
        HardwareSigner { address, device: device.to_string(), path: mnemonic::path(index) }
    }
}

impl Signer for HardwareSigner {
    fn address(&self) -> Address {
        self.address
    }

    fn kind(&self) -> SignerKind {
        SignerKind::Hardware
    }

    fn sign_hash(&self, _hash: &[u8; 32]) -> Result<Signature> {
        Err(Error::Refused(format!("the {} transport is not built yet; it lands with the device", self.device)))
    }
}

/// A signed transaction: the signature, the raw bytes and the hash the chain will know.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct SignedTransaction {
    pub signature: Signature,
    #[serde(with = "crate::sign::hexbytes")]
    pub raw: Vec<u8>,
    #[serde(with = "crate::sign::hex32")]
    pub hash: [u8; 32],
}

/// Signs a transaction after checking three things: the signer's address is `from`, the chain id is the one
/// expected, and the sentences the window showed are exactly the ones this payload produces.
pub fn sign_transaction(
    signer: &dyn Signer,
    tx: &Transaction,
    from: &Address,
    network: &str,
    shown: &[String],
) -> Result<SignedTransaction> {
    if signer.address() != *from {
        return Err(Error::Refused(format!(
            "the transaction is from {} but this signer is {}",
            from.short(),
            signer.address().short()
        )));
    }
    let expected = display::sentences(tx, from, network);
    if expected != shown {
        return Err(Error::Refused(
            "what was shown is not what would be signed; the request was changed after it was displayed".into(),
        ));
    }
    let hash = tx.signing_hash();
    let signature = signer.sign_hash(&hash)?;
    // belt and braces: the signature recovers to the signer
    if signature.recover(&hash)? != signer.address() {
        return Err(Error::Key("the signature does not recover to the signer's address".into()));
    }
    let raw = tx.encode_signed(&signature);
    let hash = tx.hash_signed(&signature);
    Ok(SignedTransaction { signature, raw, hash })
}

/// EIP-191 personal_sign. The message is shown in full by the caller; a message that parses as a transaction or
/// typed data is refused by the engine before it reaches here.
pub fn sign_personal_message(signer: &dyn Signer, message: &[u8]) -> Result<Signature> {
    signer.sign_hash(&personal_message_hash(message))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tx::Eip1559Tx;
    use crate::U256;

    const PHRASE: &str = "test test test test test test test test test test test junk";

    fn tx(to: Address) -> Transaction {
        Transaction::Eip1559(Eip1559Tx {
            chain_id: 7310,
            nonce: 0,
            max_priority_fee_per_gas: U256::from_u64(1_000_000_000),
            max_fee_per_gas: U256::from_u64(2_000_000_000),
            gas_limit: 21000,
            to: Some(to),
            value: U256::from_u64(1),
            data: vec![],
        })
    }

    #[test]
    fn software_signs_when_the_words_match() {
        let s = SoftwareSigner::from_phrase(PHRASE, 0).unwrap();
        let to = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let t = tx(to);
        let shown = display::sentences(&t, &s.address(), "Igneum development chain 7310");
        let signed = sign_transaction(&s, &t, &s.address(), "Igneum development chain 7310", &shown).unwrap();
        assert_eq!(signed.raw[0], 0x02);
        assert_eq!(signed.signature.recover(&t.signing_hash()).unwrap(), s.address());
    }

    #[test]
    fn refuses_changed_words_and_wrong_from() {
        let s = SoftwareSigner::from_phrase(PHRASE, 0).unwrap();
        let to = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let t = tx(to);
        let mut shown = display::sentences(&t, &s.address(), "Igneum development chain 7310");
        shown[0] = "You send 0.0001 IGN to somebody".into();
        let e = sign_transaction(&s, &t, &s.address(), "Igneum development chain 7310", &shown).unwrap_err();
        assert!(matches!(e, Error::Refused(_)));
        let shown = display::sentences(&t, &to, "Igneum development chain 7310");
        let e = sign_transaction(&s, &t, &to, "Igneum development chain 7310", &shown).unwrap_err();
        assert!(matches!(e, Error::Refused(_)));
    }

    #[test]
    fn watch_only_refuses() {
        let a = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let w = WatchOnly::new(a);
        assert_eq!(w.kind(), SignerKind::WatchOnly);
        let e = w.sign_hash(&[0u8; 32]).unwrap_err();
        assert!(e.to_string().contains("watch-only"));
    }

    #[test]
    fn key_import_and_export() {
        let s = SoftwareSigner::from_key_hex("0x4646464646464646464646464646464646464646464646464646464646464646").unwrap();
        assert_eq!(s.address().to_checksum(), "0x9d8A62f656a8d1615C1294fd71e9CFb3E4855A4F");
        assert_eq!(&*s.export_key_hex(), "0x4646464646464646464646464646464646464646464646464646464646464646");
        assert!(SoftwareSigner::from_key_hex("0x46").is_err());
        let sig = sign_personal_message(&s, b"hello world").unwrap();
        assert_eq!(sig.recover(&personal_message_hash(b"hello world")).unwrap(), s.address());
    }
}

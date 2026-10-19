//! BIP-39 words (the `bip39` crate: 12 to 24 words, so a phrase from any wallet imports) and BIP-32 derivation
//! (the `bip32` crate) on the path every hardware wallet's Ethereum app uses (decision D-B02): `m/44'/60'/0'/0/i`.
//! New wallets get 24 words (256 bits of entropy), as the kept look says.

use bip32::{DerivationPath, XPrv};
use bip39::{Language, Mnemonic};
use k256::ecdsa::SigningKey;
use rand_core::OsRng;
use zeroize::Zeroizing;

use crate::{Error, Result};

pub const PATH_PREFIX: &str = "m/44'/60'/0'/0";

/// A fresh 24-word phrase from the operating system's randomness.
pub fn new_phrase() -> Zeroizing<String> {
    let mut rng = OsRng;
    let m = Mnemonic::generate_in_with(&mut rng, Language::English, 24).expect("24 words");
    Zeroizing::new(m.to_string())
}

/// Checks the words and their checksum; returns the phrase normalised to single spaces and lower case.
pub fn check_phrase(phrase: &str) -> Result<Zeroizing<String>> {
    let normalised: String = phrase.split_whitespace().map(|w| w.to_lowercase()).collect::<Vec<_>>().join(" ");
    let count = normalised.split(' ').filter(|w| !w.is_empty()).count();
    if !(count == 12 || count == 15 || count == 18 || count == 21 || count == 24) {
        return Err(Error::Input(format!("{count} words: a phrase has 12, 15, 18, 21 or 24")));
    }
    Mnemonic::parse_in_normalized(Language::English, &normalised)
        .map_err(|_| Error::Input("the words are not a valid phrase: one is misspelt or the order is wrong".into()))?;
    Ok(Zeroizing::new(normalised))
}

pub fn path(index: u32) -> String {
    format!("{PATH_PREFIX}/{index}")
}

/// The signing key at `m/44'/60'/0'/0/index` for the phrase (an empty BIP-39 passphrase).
pub fn derive(phrase: &str, index: u32) -> Result<SigningKey> {
    let normalised = check_phrase(phrase)?;
    let m = Mnemonic::parse_in_normalized(Language::English, &normalised)
        .map_err(|_| Error::Input("the words are not a valid phrase".into()))?;
    let seed = Zeroizing::new(m.to_seed_normalized(""));
    let p: DerivationPath = path(index).parse().map_err(|e| Error::Key(format!("derivation path: {e}")))?;
    let xprv = XPrv::derive_from_path(seed.as_ref(), &p).map_err(|e| Error::Key(format!("derivation: {e}")))?;
    let bytes = Zeroizing::new(xprv.to_bytes());
    SigningKey::from_slice(bytes.as_ref()).map_err(|e| Error::Key(format!("derived key: {e}")))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::Address;

    // the phrase every wallet's test suite uses; its addresses on m/44'/60'/0'/0/i are known everywhere
    const TEST_PHRASE: &str = "test test test test test test test test test test test junk";

    #[test]
    fn derives_the_known_addresses() {
        let key = derive(TEST_PHRASE, 0).unwrap();
        let addr = Address::from_public_key(key.verifying_key());
        assert_eq!(addr.to_checksum(), "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266");
        let key1 = derive(TEST_PHRASE, 1).unwrap();
        assert_eq!(
            Address::from_public_key(key1.verifying_key()).to_checksum(),
            "0x70997970C51812dc3A010C7d01b50e0d17dc79C8"
        );
        let key2 = derive(TEST_PHRASE, 2).unwrap();
        assert_eq!(
            Address::from_public_key(key2.verifying_key()).to_checksum(),
            "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"
        );
    }

    #[test]
    fn new_phrases_are_24_words_and_valid() {
        let p = new_phrase();
        assert_eq!(p.split(' ').count(), 24);
        let q = new_phrase();
        assert_ne!(*p, *q);
        check_phrase(&p).unwrap();
        derive(&p, 0).unwrap();
    }

    #[test]
    fn rejects_bad_phrases() {
        assert!(check_phrase("test test").is_err());
        // twelve words with a wrong checksum
        assert!(check_phrase("test test test test test test test test test test test test").is_err());
        assert!(check_phrase("zzzz test test test test test test test test test test junk").is_err());
        assert_eq!(&*check_phrase("  Test   test test test test test test test test test test JUNK ").unwrap(), TEST_PHRASE);
    }
}

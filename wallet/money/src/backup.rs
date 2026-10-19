//! Backup and restore (ADR-0001 section 4.5, "Key backup"): the 32-byte seed as 24 BIP-39 English words. The
//! words encode the seed itself (the standard's entropy field, 256 bits) with the standard's 8-bit checksum; no
//! passphrase, no PBKDF2 stretch, because the seed is already the key material and the derivation of `keys` is
//! fixed. Restoring is `Wallet::from_seed` of the decoded entropy and a rescan from the wallet's birthday.

use bip39::{Language, Mnemonic};
use zeroize::Zeroizing;

use crate::keys::{Seed, Wallet};
use crate::{err, Result};

/// The 24 words, space-separated.
pub fn words(seed: &Seed) -> Zeroizing<String> {
    let m = Mnemonic::from_entropy_in(Language::English, seed.as_bytes()).expect("32 bytes are 24 words");
    Zeroizing::new(m.to_string())
}

/// The seed from the words. Refuses a wrong count, a word outside the list and a checksum that fails.
pub fn seed_from_words(phrase: &str) -> Result<Seed> {
    let normalised: String = phrase.split_whitespace().map(|w| w.to_lowercase()).collect::<Vec<_>>().join(" ");
    let count = normalised.split(' ').filter(|w| !w.is_empty()).count();
    if count != 24 {
        return err(format!("{count} words: an Money backup has 24"));
    }
    let m = Mnemonic::parse_in_normalized(Language::English, &normalised)
        .map_err(|_| crate::Error("the words are not a valid backup: one is misspelt, missing or out of order".into()))?;
    let entropy = Zeroizing::new(m.to_entropy());
    if entropy.len() != 32 {
        return err("the words do not carry a 32-byte seed");
    }
    let mut s = [0u8; 32];
    s.copy_from_slice(&entropy);
    Ok(Seed::from_bytes(s))
}

pub fn restore(phrase: &str) -> Result<Wallet> {
    Ok(Wallet::from_seed(seed_from_words(phrase)?))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_words_restore_the_same_wallet() {
        let w = Wallet::generate();
        let phrase = words(w.seed());
        assert_eq!(phrase.split(' ').count(), 24);
        let back = restore(&phrase).unwrap();
        assert_eq!(back.address(), w.address());
        assert_eq!(back.sk_spend(), w.sk_spend());
        assert_eq!(back.fvk().to_bytes(), w.fvk().to_bytes());
    }

    #[test]
    fn the_checksum_catches_a_changed_word_and_the_count_is_enforced() {
        let w = Wallet::generate();
        let phrase = words(w.seed());
        let mut ws: Vec<&str> = phrase.split(' ').collect();
        // swapping two different words keeps every word valid and (almost always) breaks the checksum or the seed:
        // either the parse refuses or the restored address differs; both are a caught fault
        let (a, b) = (3, 17);
        if ws[a] != ws[b] {
            ws.swap(a, b);
            let swapped = ws.join(" ");
            match restore(&swapped) {
                Err(_) => {}
                Ok(other) => assert_ne!(other.address(), w.address()),
            }
        }
        let mut ws: Vec<&str> = phrase.split(' ').collect();
        ws[5] = "notaword";
        assert!(restore(&ws.join(" ")).is_err());
        assert!(restore(&phrase.split(' ').take(23).collect::<Vec<_>>().join(" ")).is_err());
        assert!(restore(&phrase.to_uppercase()).is_ok());
    }
}

//! The bond registry's calls as the wallet builds and reads them: the data field of a transaction to the registry
//! (a genesis system contract with no ABI; the calldata's length and its tag byte select the operation, protocol
//! A's convention in `consensus/igneum/authority.go`).
//!
//! Version 2 (devnet4, the prototype): a 32-byte key with value bonds (or reads at value 0); 64 bytes (key, signer)
//! rotates the signing key. Version 3 (the frozen rule set, LAUNCH-GUARDS.md section 2.2 "the exit rule" and the
//! proof pi of SPEC-R0A-v0.md 0.9.1; D-B15, a PROPOSAL of the wallet team until the protocol team lands the registry that reads
//! it): 52 bytes with value (key, exit address) bonds and names the exit address in the bond transaction; 52 bytes
//! with no value sets the exit address after the lock; the claim is `key || to as a 32-byte word || s_0 || pi`
//! (96 bytes then the proof's wire form `u8(2) || receipt`, at most PI_MAX_BYTES), where pi is the key-bound proof
//! under domain 1 with K 1 over the claim message m_0 = BLAKE3(tag("IgneumHash/R0A/hostile") || C || pk ||
//! LE64(nonce_b) || to), s_0 the keyed share the journal carries, and the registry verifies the receipt against
//! PI_IMAGE_ID and the journal `u8(1) || LE32(1) || pk || m_0 || s_0`. A claim to any address but the registered
//! exit address is hostile: the whole balance at once to `to` less phi (one quarter) burnt to the fund, the key
//! removed from the next epoch table, its pending rewards burnt. A claim to the registered exit address is the
//! holder's own release (after the lock, nothing burnt): the release and the hostile claim are one form, as the
//! hostile-key draft says ("the release path and the hostile claim coincide"). The bond key is pk = G(sk) =
//! BLAKE3(tag("IgneumHash/R0A/bond-pk") || sk), never an Ed25519 key; the sortition's signing key K_m stays Ed25519
//! and separate. Every chain refuses the version 3 forms until the registry carries them; the wallet labels them
//! DEV-ONLY and says so in the sentence.

use crate::{Address, Error, Result, U256};

/// The share of a bond burnt to the fund on a hostile claim (the frozen rule set: one quarter).
pub const PHI_NUMERATOR: u64 = 1;
pub const PHI_DENOMINATOR: u64 = 4;
/// The share burnt on equivocation (one half); the wallet never builds that transaction (it is a reporter's).
pub const PHI_E_NUMERATOR: u64 = 1;
pub const PHI_E_DENOMINATOR: u64 = 2;
/// The tags of the bond key's function G and of the claim message (SPEC-R0A 0.9.1), written on the wire as
/// `u8(len) || ASCII` (the spec's `tag`).
pub const TAG_BOND_PK: &[u8] = b"IgneumHash/R0A/bond-pk";
pub const TAG_HOSTILE: &[u8] = b"IgneumHash/R0A/hostile";
/// The proof system identifier of pi's wire form and its size bounds (LAUNCH-GUARDS.md 2.2, PI-NOTES.md): a block's
/// composite receipt is bounded at 294,912 bytes; a claim's proof is the Groth16 wrap (SPEC 0.9.1 revision 0.14,
/// the project's ruling of 10:30 UK, 11 October 2026), 700 bytes on the wire and bounded at 2,048, inside the node's
/// 128 KiB transaction bound.
pub const PI_SYSTEM: u8 = 2;
pub const PI_MAX_BYTES: usize = 294_912;
pub const PI_CLAIM_MAX_BYTES: usize = 2_048;
/// The claim's domain and K in the journal.
pub const CLAIM_DOMAIN: u8 = 1;
pub const CLAIM_K: u32 = 1;
/// The claim form's fixed head: key, the claimant's word, s_0.
pub const CLAIM_HEAD: usize = 96;

/// A registry call decoded from the data field (and the value) of a transaction to the registry.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RegistryCall {
    /// value > 0 with a 32-byte key: bond (a fresh key records the caller as the bond's account); with value and
    /// 52 bytes (version 3) the bond names its exit address in the same transaction, as the frozen rule set says
    Bond { key: [u8; 32], exit: Option<Address> },
    /// the holder's own release at or past the lock (version 4, operation 0x12 || key, no value): the whole bond
    /// to the registered exit address, the key off the table
    Release { key: [u8; 32] },
    /// value 0 with a 32-byte key: a read, which as a transaction changes nothing
    Read { key: [u8; 32] },
    /// 64 bytes, no value: rotate the signing key (version 2 and 3)
    SetSigner { key: [u8; 32], signer: [u8; 32] },
    /// 96 bytes, no value: the lab registry's STAND-IN claim (devnet5 until the risc0 verifier is in the node):
    /// the bond's secret itself, the claimant's word and a zero word; the secret becomes readable on the chain
    ClaimReveal { to: Address },
    /// 96 bytes then pi: the claim (version 3, the verified form): hostile to any address but the registered exit,
    /// the holder's own release to the exit address; `pi_bytes` is the proof's length, the proof in the data
    Claim { key: [u8; 32], to: Address, share: [u8; 32], pi_bytes: usize },
    /// a length the registry has no operation for
    Unknown { bytes: usize },
}

fn key_of(data: &[u8]) -> [u8; 32] {
    let mut k = [0u8; 32];
    k.copy_from_slice(&data[..32]);
    k
}

/// Reads a call to the registry from its data and value.
pub fn decode(data: &[u8], value: &U256) -> RegistryCall {
    match data.len() {
        33 if data[0] == 0x12 && value.is_zero() => {
            let mut key = [0u8; 32];
            key.copy_from_slice(&data[1..33]);
            RegistryCall::Release { key }
        }
        32 => {
            let key = key_of(data);
            if value.is_zero() {
                RegistryCall::Read { key }
            } else {
                RegistryCall::Bond { key, exit: None }
            }
        }
        // 64 bytes: with value the bond with its exit address as a right-aligned word (the lab's form of the frozen
        // "exit address set in the bond transaction"; by the account, a change of the exit with the top-up); with
        // no value the signer rotation
        64 if !value.is_zero() => match word_address(&data[32..64]) {
            Some(exit) => RegistryCall::Bond { key: key_of(data), exit: Some(exit) },
            None => RegistryCall::Unknown { bytes: 64 },
        },
        64 => {
            let mut signer = [0u8; 32];
            signer.copy_from_slice(&data[32..]);
            RegistryCall::SetSigner { key: key_of(data), signer }
        }
        96 if value.is_zero() && data[64..96].iter().all(|b| *b == 0) => match word_address(&data[32..64]) {
            Some(to) => RegistryCall::ClaimReveal { to },
            None => RegistryCall::Unknown { bytes: 96 },
        },
        n if n > CLAIM_HEAD && n <= CLAIM_HEAD + PI_MAX_BYTES && data[CLAIM_HEAD] == PI_SYSTEM => {
            // the claimant's address is a 32-byte word, left-padded with zeros
            if data[32..44].iter().any(|b| *b != 0) {
                return RegistryCall::Unknown { bytes: n };
            }
            let to = match Address::from_slice(&data[44..64]) {
                Ok(a) => a,
                Err(_) => return RegistryCall::Unknown { bytes: n },
            };
            let mut share = [0u8; 32];
            share.copy_from_slice(&data[64..96]);
            RegistryCall::Claim { key: key_of(data), to, share, pi_bytes: n - CLAIM_HEAD }
        }
        n => RegistryCall::Unknown { bytes: n },
    }
}

/// The data of a bond or a read: the key alone (the value says which).
pub fn bond_data(key: &[u8; 32]) -> Vec<u8> {
    key.to_vec()
}

/// The data of a signer rotation (version 2).
/// The release (version 4, KEYED-FORM.md: `0x12 || key`, 50,000 gas, by the bond's account at or past the lock
/// height): the whole bond to the exit address, the key removed.
pub fn release_data(key: &[u8; 32]) -> Vec<u8> {
    let mut d = vec![0x12u8];
    d.extend_from_slice(key);
    d
}

pub fn set_signer_data(key: &[u8; 32], signer: &[u8; 32]) -> Vec<u8> {
    let mut d = key.to_vec();
    d.extend_from_slice(signer);
    d
}

/// An address from a 32-byte word (left-padded with zeros), None when the pad is dirty.
fn word_address(word: &[u8]) -> Option<Address> {
    if word.len() != 32 || word[..12].iter().any(|b| *b != 0) {
        return None;
    }
    Address::from_slice(&word[12..]).ok()
}

fn address_word(a: &Address) -> [u8; 32] {
    let mut w = [0u8; 32];
    w[12..].copy_from_slice(&a.0);
    w
}

/// The data of a bond that names its exit address (version 3, sent with value): the key and the exit address as a
/// word; by the account on a bonded key it changes the exit address with the top-up (the lab build models no lock).
pub fn set_exit_data(key: &[u8; 32], exit: &Address) -> Vec<u8> {
    let mut d = key.to_vec();
    d.extend_from_slice(&address_word(exit));
    d
}

/// The data of the lab registry's stand-in claim: the bond's SECRET, the claimant's word, a zero word. The secret
/// goes on the chain in the clear; the sentence says so. The wallet builds it only when asked for the reveal.
pub fn claim_reveal_data(sk: &[u8; 32], to: &Address) -> Vec<u8> {
    let mut d = sk.to_vec();
    d.extend_from_slice(&address_word(to));
    d.extend_from_slice(&[0u8; 32]);
    d
}

/// The data of a claim (version 3): the key, the claimant's word, the share s_0 and the proof's wire form. Refuses
/// a proof that is not system 2's or is over the claim's bound (the wrap's 2,048), before anything is signed.
pub fn claim_data(key: &[u8; 32], to: &Address, share: &[u8; 32], pi: &[u8]) -> Result<Vec<u8>> {
    check_pi_bounded(pi, PI_CLAIM_MAX_BYTES)?;
    let mut d = Vec::with_capacity(CLAIM_HEAD + pi.len());
    d.extend_from_slice(key);
    d.extend_from_slice(&[0u8; 12]);
    d.extend_from_slice(&to.0);
    d.extend_from_slice(share);
    d.extend_from_slice(pi);
    Ok(d)
}

/// The structural checks on a proof the wallet carries: the identifier byte and the bound (the STARK itself is the
/// registry's to verify, with the prover's tool as the wallet's own check before sending).
pub fn check_pi(pi: &[u8]) -> Result<()> {
    check_pi_bounded(pi, PI_MAX_BYTES)
}

/// The same checks under a given bound: a claim's proof under the wrap's 2,048, a block's under 294,912.
pub fn check_pi_bounded(pi: &[u8], bound: usize) -> Result<()> {
    if pi.is_empty() || pi[0] != PI_SYSTEM {
        return Err(Error::Input("the proof is not proof system 2's wire form (u8(2) || receipt)".into()));
    }
    if pi.len() > bound {
        return Err(Error::Input(format!(
            "the proof is {} bytes, over the bound of {}{}",
            pi.len(),
            bound,
            if bound == PI_CLAIM_MAX_BYTES { " (a claim carries the Groth16 wrap, about 700 bytes, never the composite receipt)" } else { "" }
        )));
    }
    Ok(())
}

/// The spec's `tag(x)`: `u8(len) || ASCII`.
pub fn tag(ascii: &[u8]) -> Vec<u8> {
    let mut t = Vec::with_capacity(ascii.len() + 1);
    t.push(ascii.len() as u8);
    t.extend_from_slice(ascii);
    t
}

/// A chain's `C` in the claim message: the chain id as u64 big-endian, the envelope's form (the project's ruling of
/// 10:30 UK, 11 October 2026). The vector set's 32-byte test C stays only on devnet4's rows, labelled.
pub fn chain_c(chain_id: u64) -> [u8; 8] {
    chain_id.to_be_bytes()
}

/// The bytes the claim message m_0 is BLAKE3 of (the hash is the authority crate's; the core carries no hash but
/// keccak): tag("IgneumHash/R0A/hostile") || C || pk || LE64(nonce_b) || to. `C` as the chain gives it: u64be(chain
/// id), 8 bytes, on every chain but devnet4, whose rows carry the vector set's 32-byte test C.
pub fn claim_preimage(c: &[u8], pk: &[u8; 32], nonce_b: u64, to: &Address) -> Vec<u8> {
    let mut p = tag(TAG_HOSTILE);
    p.extend_from_slice(c);
    p.extend_from_slice(pk);
    p.extend_from_slice(&nonce_b.to_le_bytes());
    p.extend_from_slice(&to.0);
    p
}

/// The bytes the bond key is BLAKE3 of: tag("IgneumHash/R0A/bond-pk") || sk.
pub fn bond_pk_preimage(sk: &[u8; 32]) -> Vec<u8> {
    let mut p = tag(TAG_BOND_PK);
    p.extend_from_slice(sk);
    p
}

/// The journal the registry compares a claim's receipt against: u8(1) || LE32(1) || pk || m_0 || s_0.
pub fn claim_journal(pk: &[u8; 32], m0: &[u8; 32], s0: &[u8; 32]) -> Vec<u8> {
    let mut j = vec![CLAIM_DOMAIN];
    j.extend_from_slice(&CLAIM_K.to_le_bytes());
    j.extend_from_slice(pk);
    j.extend_from_slice(m0);
    j.extend_from_slice(s0);
    j
}

/// The registry's read result (96 bytes in version 2: amount, account as a word, signer).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BondRead {
    pub amount: U256,
    pub account: Address,
    pub signer: [u8; 32],
    /// the exit address (version 3's 128-byte read; None on version 2's 96 bytes)
    pub exit: Option<Address>,
}

pub fn decode_read(out: &[u8]) -> Result<BondRead> {
    if out.len() < 64 {
        return Err(Error::Rpc(format!("the registry returned {} bytes, not a bond", out.len())));
    }
    let amount = U256::from_be_bytes(&out[..32])?;
    let account = Address::from_slice(&out[44..64])?;
    let mut signer = [0u8; 32];
    if out.len() >= 96 {
        signer.copy_from_slice(&out[64..96]);
    }
    let exit = if out.len() >= 128 { word_address(&out[96..128]) } else { None };
    Ok(BondRead { amount, account, signer, exit })
}

/// The burnt part of a hostile claim: floor(balance / 4).
pub fn phi_of(balance: &U256) -> U256 {
    let (q, _) = balance.div_rem_u64(PHI_DENOMINATOR);
    q.checked_mul_u64(PHI_NUMERATOR).unwrap_or(U256::ZERO)
}

/// The identity key as 32 bytes from hex.
pub fn parse_key(s: &str) -> Result<[u8; 32]> {
    let b = crate::hex::decode(s)?;
    if b.len() != 32 {
        return Err(Error::Input(format!("an identity key has 32 bytes, not {}", b.len())));
    }
    let mut k = [0u8; 32];
    k.copy_from_slice(&b);
    Ok(k)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn addr() -> Address {
        Address::parse("0x8EfB30577370A071D77c63f3BaB7Da0A7af96bE0").unwrap()
    }

    #[test]
    fn every_form_round_trips() {
        let key = [7u8; 32];
        let one = U256::from_u64(1);
        assert_eq!(decode(&bond_data(&key), &one), RegistryCall::Bond { key, exit: None });
        assert_eq!(decode(&bond_data(&key), &U256::ZERO), RegistryCall::Read { key });
        let signer = [9u8; 32];
        assert_eq!(decode(&set_signer_data(&key, &signer), &U256::ZERO), RegistryCall::SetSigner { key, signer });
        // the lab's 64-byte forms: with value the bond with its exit word, without value the signer rotation
        let with_exit = set_exit_data(&key, &addr());
        assert_eq!(with_exit.len(), 64);
        assert_eq!(decode(&with_exit, &one), RegistryCall::Bond { key, exit: Some(addr()) });
        assert!(matches!(decode(&with_exit, &U256::ZERO), RegistryCall::SetSigner { .. }));
        let mut dirty = with_exit.clone();
        dirty[33] = 1;
        assert_eq!(decode(&dirty, &one), RegistryCall::Unknown { bytes: 64 });
        // the lab's stand-in claim: 96 bytes, no value, the last word zero
        let reveal = claim_reveal_data(&[8u8; 32], &addr());
        assert_eq!(reveal.len(), 96);
        assert_eq!(decode(&reveal, &U256::ZERO), RegistryCall::ClaimReveal { to: addr() });
        assert!(matches!(decode(&reveal, &one), RegistryCall::Unknown { bytes: 96 }));
        // the claim: a stand-in proof of system 2 (the registry verifies the wrap; the wallet checks the shape)
        let share = [5u8; 32];
        let mut pi = vec![PI_SYSTEM];
        pi.extend_from_slice(&[0xabu8; 699]);
        let d = claim_data(&key, &addr(), &share, &pi).unwrap();
        assert_eq!(d.len(), CLAIM_HEAD + 700);
        assert_eq!(decode(&d, &U256::ZERO), RegistryCall::Claim { key, to: addr(), share, pi_bytes: 700 });
        // a claim word with a dirty pad is not a claim
        let mut bad = d.clone();
        bad[33] = 1;
        assert!(matches!(decode(&bad, &U256::ZERO), RegistryCall::Unknown { .. }));
        // a proof of another system, an empty proof, and one over the claim's bound (a composite receipt) are
        // refused before signing; a block's proof keeps its own bound
        assert!(claim_data(&key, &addr(), &share, &[1u8; 10]).is_err());
        assert!(claim_data(&key, &addr(), &share, &[]).is_err());
        assert!(claim_data(&key, &addr(), &share, &vec![PI_SYSTEM; PI_CLAIM_MAX_BYTES + 1]).is_err());
        assert!(claim_data(&key, &addr(), &share, &vec![PI_SYSTEM; PI_CLAIM_MAX_BYTES]).is_ok());
        assert!(check_pi(&vec![PI_SYSTEM; PI_MAX_BYTES]).is_ok());
        assert!(check_pi(&vec![PI_SYSTEM; PI_MAX_BYTES + 1]).is_err());
        // the proof form's head alone (no proof, the share not zero) and 33 bytes are unknown
        assert!(matches!(decode(&d[..96], &U256::ZERO), RegistryCall::Unknown { bytes: 96 }));
        assert!(matches!(decode(&[0u8; 33], &U256::ZERO), RegistryCall::Unknown { bytes: 33 }));
        assert_eq!(decode(&[1, 2, 3], &U256::ZERO), RegistryCall::Unknown { bytes: 3 });
    }

    #[test]
    fn the_read_decodes_the_prototype_layout() {
        let mut out = vec![0u8; 96];
        let amount = U256::from_dec_str("1000000000000000000000").unwrap();
        out[..32].copy_from_slice(&amount.0);
        out[44..64].copy_from_slice(&addr().0);
        out[64..96].copy_from_slice(&[0xa8u8; 32]);
        let r = decode_read(&out).unwrap();
        assert_eq!(r.amount, amount);
        assert_eq!(r.account, addr());
        assert_eq!(r.signer, [0xa8u8; 32]);
        assert_eq!(r.exit, None);
        assert!(decode_read(&[0u8; 10]).is_err());
        // version 3's 128 bytes carry the exit address as a fourth word
        let mut out3 = out.clone();
        out3.extend_from_slice(&address_word(&addr()));
        assert_eq!(decode_read(&out3).unwrap().exit, Some(addr()));
    }

    #[test]
    fn phi_tags_and_preimages_are_the_spec_s() {
        assert_eq!(phi_of(&U256::from_u64(1000)), U256::from_u64(250));
        assert_eq!(phi_of(&U256::from_u64(7)), U256::from_u64(1));
        // tag: one length byte then the ASCII; 23 bytes for the hostile tag (22 ASCII), as 0.9.1 says
        assert_eq!(tag(TAG_HOSTILE).len(), 23);
        assert_eq!(tag(TAG_HOSTILE)[0], 22);
        assert_eq!(tag(TAG_BOND_PK).len(), 23);
        let p = claim_preimage(&[0xccu8; 32], &[1u8; 32], 2, &addr());
        assert_eq!(p.len(), 23 + 32 + 32 + 8 + 20);
        assert_eq!(&p[23..55], &[0xccu8; 32]);
        assert_eq!(&p[87..95], &2u64.to_le_bytes());
        // the chain's C: u64be(chain id), 8 bytes (devnet5 is 7315)
        assert_eq!(chain_c(7315), [0, 0, 0, 0, 0, 0, 0x1c, 0x93]);
        let p = claim_preimage(&chain_c(7315), &[1u8; 32], 0, &addr());
        assert_eq!(p.len(), 23 + 8 + 32 + 8 + 20);
        assert_eq!(&p[23..31], &chain_c(7315));
        assert_eq!(bond_pk_preimage(&[3u8; 32]).len(), 23 + 32);
        let j = claim_journal(&[1u8; 32], &[2u8; 32], &[3u8; 32]);
        assert_eq!(j.len(), 5 + 32 + 64);
        assert_eq!(j[0], 1);
        assert_eq!(&j[1..5], &1u32.to_le_bytes());
    }
}

//! The hostile claim and the holder's release (DRAFT-HOSTILE-KEY.md section 2 under SPEC-R0A-v0.md 0.9.1): the
//! bond's secret `sk` is presented as the key-bound proof `pi` under domain 1 with K 1 over the claim message
//! m_0 = BLAKE3(tag("IgneumHash/R0A/hostile") || C || pk || LE64(nonce_b) || to), where pk = G(sk) =
//! BLAKE3(tag("IgneumHash/R0A/bond-pk") || sk). The proof is the RISC Zero receipt of the pi guest (proof system 2,
//! PI_IMAGE_ID), produced by the hash team's prover (`pow/pi`: `pi-claim prove`, about 2 s on a server CPU, longer
//! on a laptop; a claim is rare) and verified by the registry; the wallet computes pk and m_0 itself, runs the
//! prover by path on the secret's file (the secret never passes through the wallet's memory as anything but the
//! bytes G reads), checks the proof's shape, and carries it. DEV-ONLY: no registry reads this today (D-B15).
//!
//! The sortition's signing key K_m is a separate Ed25519 key (`signing_key`); the bond key is never Ed25519.

use ed25519_dalek::SigningKey;
use igneum_wallet_core::{authority, Address, Error, Result};
use std::path::Path;
use std::process::Command;

/// A 32-byte seed read from hex (the bond key's secret or the signing key's seed), from a file the person names.
pub fn seed_from_hex(s: &str) -> Result<[u8; 32]> {
    let b = igneum_wallet_core::hex::decode(s.trim())?;
    if b.len() != 32 {
        return Err(Error::Input(format!("a 32-byte secret has 32 bytes, not {}", b.len())));
    }
    let mut k = [0u8; 32];
    k.copy_from_slice(&b);
    Ok(k)
}

/// The bond key pk = G(sk): the identity the registry names.
pub fn bond_pk(sk: &[u8; 32]) -> [u8; 32] {
    *blake3::hash(&authority::bond_pk_preimage(sk)).as_bytes()
}

/// The sortition's signing key: the Ed25519 public key of a seed (K_m, rotated under the bond by `set-signer`).
pub fn signing_key(seed: &[u8; 32]) -> [u8; 32] {
    SigningKey::from_bytes(seed).verifying_key().to_bytes()
}

/// The vector set's test C (BLAKE3("IgneumHash/R0A/test-C")): devnet4's rows only, labelled (the project's ruling of
/// 10:30 UK, 11 October 2026); every other chain's C is `authority::chain_c`, u64be(chain id).
pub fn test_c() -> [u8; 32] {
    *blake3::hash(b"IgneumHash/R0A/test-C").as_bytes()
}

/// The C a chain's claim message carries, and the word that labels it: the test C on devnet4 (whose version 2
/// registry reads no claim, so its rows are fixtures), u64be(chain id) everywhere else.
pub fn default_c(chain: &igneum_wallet_core::chain::Chain) -> (Vec<u8>, &'static str) {
    // the chain id as u64 big-endian on every chain (SPEC 0.9.1 revision 0.14; the hash team's prover takes the
    // chain id only); the old test C is no default and stays in the crate for the morning's devnet4 rows alone
    (authority::chain_c(chain.chain_id).to_vec(), "the chain id as u64 big-endian (the envelope's C)")
}

/// What the holder is told before the prover runs: the wrap is made behind the risc0 Groth16 container (x86 and
/// docker on the claimant's machine), about 23 s warm on a server, about 51 s cold with the image pull, longer on a
/// laptop; a claim is rare, so the wait is the price of a proof that reveals nothing.
pub const PROVER_WORDS: &str = "the claim's proof is the Groth16 wrap, made behind the risc0 container (x86 and docker on this machine): about 23 s warm on a server, 51 s cold with the image pull, longer on a laptop";

/// The claim message m_0.
pub fn message(c: &[u8], pk: &[u8; 32], nonce_b: u64, to: &Address) -> [u8; 32] {
    *blake3::hash(&authority::claim_preimage(c, pk, nonce_b, to)).as_bytes()
}

/// What the prover returns: the public values beside the proof's wire form.
#[derive(Clone, Debug)]
pub struct ClaimProof {
    pub pk: [u8; 32],
    pub m0: [u8; 32],
    pub share: [u8; 32],
    pub pi: Vec<u8>,
    pub prove_ms: f64,
}

/// Runs the hash team's prover on the secret's file and reads the proof back: `pi-claim prove <sk-file> <C>
/// <nonce_b> <to> <out>`. The wallet never reads the secret for this; the prover does, by path.
pub fn prove_with(prover: &Path, sk_file: &Path, chain_id: u64, nonce_b: u64, to: &Address, out: &Path) -> Result<ClaimProof> {
    // the hash team's per-domain API (11 October, main bf6604ed): the chain id in decimal is the C, the proof the wrap
    let c = authority::chain_c(chain_id);
    let hex = |b: &[u8]| igneum_wallet_core::hex::encode(b)[2..].to_string();
    let run = Command::new(prover)
        .arg("prove")
        .arg(sk_file)
        .arg(chain_id.to_string())
        .arg(nonce_b.to_string())
        .arg(hex(&to.0))
        .arg(out)
        .output()
        .map_err(|e| Error::Input(format!("the prover {} did not run: {e}", prover.display())))?;
    if !run.status.success() {
        return Err(Error::Refused(format!("the prover refused: {}", String::from_utf8_lossy(&run.stderr).trim())));
    }
    let v: serde_json::Value = serde_json::from_slice(&run.stdout).map_err(|e| Error::Input(format!("the prover's reply is not JSON: {e}")))?;
    let field = |k: &str| -> Result<[u8; 32]> {
        let s = v.get(k).and_then(|x| x.as_str()).ok_or_else(|| Error::Input(format!("the prover's reply has no {k}")))?;
        seed_from_hex(s)
    };
    let pi = std::fs::read(out)?;
    authority::check_pi_bounded(&pi, authority::PI_CLAIM_MAX_BYTES)?;
    let pk = field("pk")?;
    let m0 = field("m0")?;
    // the message the wallet computes must be the prover's: the same C, pk, nonce and claimant
    if m0 != message(&c, &pk, nonce_b, to) {
        return Err(Error::Refused("the prover's claim message is not the wallet's: C, the nonce or the claimant differ".into()));
    }
    Ok(ClaimProof { pk, m0, share: field("share")?, pi, prove_ms: v.get("prove_ms").and_then(|x| x.as_f64()).unwrap_or(0.0) })
}

/// Checks a proof file's shape and reads the share from its journal position when the receipt's journal is readable
/// through the prover's `verify`; without the prover only the shape is checked (the STARK is the registry's).
pub fn verify_with(prover: &Path, pk: &[u8; 32], chain_id: u64, nonce_b: u64, to: &Address, pi_file: &Path) -> Result<String> {
    let hex = |b: &[u8]| igneum_wallet_core::hex::encode(b)[2..].to_string();
    let run = Command::new(prover)
        .arg("verify")
        .arg(hex(pk))
        .arg(chain_id.to_string())
        .arg(nonce_b.to_string())
        .arg(hex(&to.0))
        .arg(pi_file)
        .output()
        .map_err(|e| Error::Input(format!("the prover {} did not run: {e}", prover.display())))?;
    let text = String::from_utf8_lossy(&run.stdout).trim().to_string();
    if !run.status.success() {
        return Err(Error::Refused(if text.is_empty() { String::from_utf8_lossy(&run.stderr).trim().to_string() } else { text }));
    }
    Ok(text)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_bond_key_and_the_message_are_the_spec_s_functions() {
        // G is BLAKE3 over the length-prefixed tag and the secret: recomputed here by hand
        let sk = [5u8; 32];
        let mut h = blake3::Hasher::new();
        h.update(&[22u8]);
        h.update(b"IgneumHash/R0A/bond-pk");
        h.update(&sk);
        assert_eq!(bond_pk(&sk), *h.finalize().as_bytes());
        // the message binds C, pk, the nonce and the claimant
        let to = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let other = Address::parse("0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC").unwrap();
        let c = [0xccu8; 32];
        let pk = bond_pk(&sk);
        let m = message(&c, &pk, 0, &to);
        assert_ne!(m, message(&c, &pk, 0, &other));
        assert_ne!(m, message(&c, &pk, 1, &to));
        assert_ne!(m, message(&[0xcdu8; 32], &pk, 0, &to));
        assert_ne!(m, message(&c, &bond_pk(&[6u8; 32]), 0, &to));
        let mut h = blake3::Hasher::new();
        h.update(&[22u8]);
        h.update(b"IgneumHash/R0A/hostile");
        h.update(&c);
        h.update(&pk);
        h.update(&0u64.to_le_bytes());
        h.update(&to.0);
        assert_eq!(m, *h.finalize().as_bytes());
        // the signing key is Ed25519 and is not the bond key
        assert_ne!(signing_key(&sk), bond_pk(&sk));
    }

    #[test]
    fn the_default_c_is_the_chain_id_on_every_chain() {
        use igneum_wallet_core::chain::Chain;
        let (c4, l4) = default_c(&Chain::igneum_devnet4());
        assert_eq!(c4, 7310u64.to_be_bytes().to_vec());
        assert!(l4.contains("big-endian"));
        let (c5, _) = default_c(&Chain::igneum_devnet5());
        assert_eq!(c5, 7315u64.to_be_bytes().to_vec());
        assert_eq!(default_c(&Chain::unpinned(7315, None)).0, c5);
        assert_ne!(test_c().to_vec(), c4);
    }

    #[test]
    fn the_vector_set_s_wrap_row_message_matches() {
        // pow/vectors/pi/v8-...-k2/pi.json, row "claim" (the hash team's landing of 11 October, main bf6604ed):
        // chain id 7315, nonce_b 0, to 0x11 x 20, under the K 2 set's test secret; the wallet's m_0 over
        // u64be(7315) must equal the prover's, which the box build checks against the set's row (RUNS.md)
        let to = Address::from_slice(&[0x11u8; 20]).unwrap();
        let pk = seed_from_hex("9e5f6270f0506ea38c26cf765b3775cf1a6c989eb3b1ac0583a09f6dfacd5e6e").unwrap();
        let m = message(&authority::chain_c(7315), &pk, 0, &to);
        // the preimage is tag || u64be(7315) || pk || LE64(0) || to: 23 + 8 + 32 + 8 + 20 bytes
        assert_eq!(authority::claim_preimage(&authority::chain_c(7315), &pk, 0, &to).len(), 91);
        assert_ne!(m, message(&test_c(), &pk, 0, &to));
    }

    #[test]
    fn the_vector_set_s_claim_row_message_matches() {
        // pow/vectors/pi/v8-...-k2/pi.json, row "claim": C = BLAKE3("IgneumHash/R0A/test-C"), to = 0x11 x 20,
        // nonce 0, pk 9e5f6270...5e6e, m_0 6cc969d1...cc15 (the hash team's prover and this crate agree on the message)
        let c = *blake3::hash(b"IgneumHash/R0A/test-C").as_bytes();
        let pk = seed_from_hex("9e5f6270f0506ea38c26cf765b3775cf1a6c989eb3b1ac0583a09f6dfacd5e6e").unwrap();
        let to = Address::from_slice(&[0x11u8; 20]).unwrap();
        let m = message(&c, &pk, 0, &to);
        assert_eq!(igneum_wallet_core::hex::encode(&m), "0x6cc969d1ab879e6c9457e52eb7c6b470860f4a81a080b10de5ecc78dc142cc15");
    }
}

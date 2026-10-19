//! The proof `pi` of SPEC-R0A-v0.md section 0.9.1 (the carried shares bound to the bond's public key).
//!
//! `pi_prove(domain, sk, m) -> (pk, shares, pi)` and `pi_verify(domain, pk, m, shares, pi)` are the entry points the
//! lab build fills its `pi` slot with (the protocol team). The statement is the guest under `pow/pi/methods/guest`; its
//! image id `PI_IMAGE_ID` is the consensus constant, PINNED here as bytes (the verifier never derives it from a build:
//! the guest a build produces must print this id, which the `prove` feature's test `pinned_id_is_the_guest` asserts).
//! Proof system 2 is the RISC Zero zkVM (3.0.6); no other system is defined, and the identifier byte of the wire form
//! is checked before anything else. The verifier alone (feature `prove` off) links without the guest, rzup or docker:
//! the node's C ABI staticlib under `pow/pi/ffi` is built that way on any box.
use risc0_zkvm::{sha::Digest, InnerReceipt, Receipt};
#[cfg(feature = "prove")]
use risc0_zkvm::{default_prover, ExecutorEnv, ProverOpts};
use std::fmt;
#[cfg(feature = "prove")]
use std::time::Instant;

#[cfg(feature = "prove")]
pub use pi_methods::{PI_GUEST_ELF, PI_GUEST_ID};

/// The proof system identifier of the wire form `pi = u8(2) || receipt`.
pub const SYSTEM_RISC0: u8 = 2;
/// `PI_IMAGE_ID`: the guest's image id, the consensus constant of section 0.9.1, host-independent (risc0-build's docker
/// mode printed it byte for byte on two build boxes, 11 Oct 2026; PI-NOTES.md section 2). A guest, lock or
/// builder-image change moves it and is a consensus change: this constant changes only with a ratified row.
pub const PI_IMAGE_ID: [u8; 32] = [
    0x00, 0x6b, 0xf4, 0x05, 0x03, 0x89, 0x26, 0xb4, 0x56, 0xe1, 0xc6, 0x05, 0x6f, 0x45, 0x64, 0x35, 0x8e, 0x0f, 0xb1, 0xca,
    0xb7, 0x0f, 0x5d, 0x5d, 0x7f, 0x32, 0x57, 0xcb, 0x8a, 0x83, 0x78, 0x7c,
];
/// The size bound of a block's `pi` (domain 0) on the wire: the one-segment composite receipt at K 64 measured
/// 276,453 bytes on a build box (11 Oct 2026), rounded up to the next 16 KiB (PI-NOTES.md section 2); K 2 measures 221,541.
pub const PI_MAX_BYTES: usize = 294_912;
/// The size bound of a claim's `pi` (domain 1): the Groth16 wrap (sub-kilobyte; PI-NOTES.md section 2 carries the
/// measured size); 2 KiB so the claim rides inside a transaction's data far under the node's 128 KiB bound.
pub const PI_CLAIM_MAX_BYTES: usize = 2_048;
/// `K` (KEYED_ROUNDS) admitted by the guest.
pub const K_MAX: usize = 64;
/// The journal's length for `K`: `u8(domain) || LE32(K) || pk || m_0..m_(K-1) || s_0..s_(K-1)`.
pub const fn journal_len(k: usize) -> usize {
    5 + 32 + 64 * k
}

const TAG_BOND_PK: &[u8] = b"IgneumHash/R0A/bond-pk";
const TAG_HOSTILE: &[u8] = b"IgneumHash/R0A/hostile";

/// `PI_IMAGE_ID` as 32 hex bytes (the digest's canonical byte order): the pinned constant.
pub fn image_id_hex() -> String {
    Digest::from_bytes(PI_IMAGE_ID).to_string()
}

/// The image id of the guest THIS build embeds, as hex: equal to `image_id_hex()` on a build of the ratified guest
/// (the test `pinned_id_is_the_guest`); a native build at another path, or another lock, prints another id.
#[cfg(feature = "prove")]
pub fn guest_image_id_hex() -> String {
    Digest::from(PI_GUEST_ID).to_string()
}

/// `pk = G(sk) = BLAKE3(tag("IgneumHash/R0A/bond-pk") || sk)`, `tag(T) = u8(len(T)) || T`.
pub fn bond_pk(sk: &[u8; 32]) -> [u8; 32] {
    let mut g = blake3::Hasher::new();
    g.update(&[TAG_BOND_PK.len() as u8]);
    g.update(TAG_BOND_PK);
    g.update(sk);
    *g.finalize().as_bytes()
}

/// `s_i = H(sk, i, m_i) = BLAKE3k(sk, "bond" || LE32(i) || m_i)` (section 0.9's keyed share, as the reference computes it).
pub fn share(sk: &[u8; 32], i: u32, m: &[u8; 32]) -> [u8; 32] {
    let mut h = blake3::Hasher::new_keyed(sk);
    h.update(b"bond");
    h.update(&i.to_le_bytes());
    h.update(m);
    *h.finalize().as_bytes()
}

/// The hostile claim's message `m_0 = BLAKE3(tag("IgneumHash/R0A/hostile") || C || pk || LE64(nonce_b) || to)` (domain 1,
/// K 1), with `C = u64be(chain id)` as the envelope (section 1) writes it (the project's ruling of 10:25 UK, 11 Oct 2026).
pub fn claim_message(chain_id: u64, pk: &[u8; 32], nonce_b: u64, to: &[u8; 20]) -> [u8; 32] {
    let mut h = blake3::Hasher::new();
    h.update(&[TAG_HOSTILE.len() as u8]);
    h.update(TAG_HOSTILE);
    h.update(&chain_id.to_be_bytes());
    h.update(pk);
    h.update(&nonce_b.to_le_bytes());
    h.update(to);
    *h.finalize().as_bytes()
}

/// The journal the verifier expects.
pub fn journal_bytes(domain: u8, pk: &[u8; 32], m: &[[u8; 32]], s: &[[u8; 32]]) -> Vec<u8> {
    let k = m.len();
    let mut j = Vec::with_capacity(journal_len(k));
    j.push(domain);
    j.extend_from_slice(&(k as u32).to_le_bytes());
    j.extend_from_slice(pk);
    for x in m {
        j.extend_from_slice(x);
    }
    for x in s {
        j.extend_from_slice(x);
    }
    j
}

/// The receipt kind a proof system produces and admits.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    /// The research kind: the recursion-lifted receipt (constant size; measured 5.5 s to prove and 23 to 25 ms to
    /// verify at K 2 on a build box, over the 20 ms budget, at the same size as the one-segment composite).
    Succinct,
    /// The block's kind (domain 0): the composite receipt of EXACTLY ONE segment (the guest is one segment at every K
    /// to 64); measured 2.0 s to prove and 14.6 ms to verify at K 2 on a build box. A composite of more segments is refused.
    Composite,
    /// The claim's kind (domain 1): the Groth16 wrap, sub-kilobyte, so a claim rides inside a transaction's data
    /// (the node's 128 KiB bound refuses the 221 KB composite); about a minute of CPU proving behind a container,
    /// which a rare claim affords and a block cannot (the project's ruling of 10:25 UK, 11 Oct 2026).
    Groth16,
}

/// The receipt kind the chain admits for a domain: the block's composite of one segment, the claim's Groth16 wrap.
pub const fn kind_for(domain: u8) -> Kind {
    match domain {
        1 => Kind::Groth16,
        _ => Kind::Composite,
    }
}

/// The wire bound for a domain.
pub const fn max_bytes_for(domain: u8) -> usize {
    match domain {
        1 => PI_CLAIM_MAX_BYTES,
        _ => PI_MAX_BYTES,
    }
}

/// The verifier's refusals: ONE code set, in the order the checks run (the node's validity rule reads the code; the
/// text is for logs). Codes 1 to 7 are the C ABI's `IGNPI_E_*` (pow/pi/ffi/include/ignpi.h) and PI-NOTES.md 5.1.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum VerifyError {
    /// 1: domain not 0 or 1, K 0 or over 64, or the share count not K (a malformed call, never a proof's verdict).
    Arg,
    /// 2: `pi` longer than the domain's bound, refused before any parsing.
    Bound { len: usize, bound: usize },
    /// 3: `pi` empty or its identifier byte not 2.
    System,
    /// 4: the receipt does not decode.
    Decode(String),
    /// 5: the receipt's kind is not the domain's (a composite of more than one segment, a succinct receipt, the claim's
    /// Groth16 wrap in a block, the block's composite in a claim).
    Kind { domain: u8 },
    /// 6: the STARK or SNARK does not verify against `PI_IMAGE_ID`.
    Proof(String),
    /// 7: the journal differs from `u8(domain) || LE32(K) || pk || m || s` (pk, an m_i or a share is not the proof's).
    Journal,
}

impl VerifyError {
    /// The refusal code (1 to 7; 0 is OK).
    pub const fn code(&self) -> i32 {
        match self {
            VerifyError::Arg => 1,
            VerifyError::Bound { .. } => 2,
            VerifyError::System => 3,
            VerifyError::Decode(_) => 4,
            VerifyError::Kind { .. } => 5,
            VerifyError::Proof(_) => 6,
            VerifyError::Journal => 7,
        }
    }
}

impl fmt::Display for VerifyError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            VerifyError::Arg => write!(f, "domain or K out of range"),
            VerifyError::Bound { len, bound } => write!(f, "pi over the bound: {len} > {bound}"),
            VerifyError::System => write!(f, "pi system identifier is not 2"),
            VerifyError::Decode(e) => write!(f, "receipt decode: {e}"),
            VerifyError::Kind { domain } => {
                let kind = kind_for(*domain);
                write!(f, "receipt kind refused (domain {domain} admits {kind:?}{})", if kind == Kind::Composite { " of one segment" } else { "" })
            }
            VerifyError::Proof(e) => write!(f, "receipt verify: {e}"),
            VerifyError::Journal => write!(f, "journal mismatch (pk, m or shares differ from the proof's)"),
        }
    }
}

impl std::error::Error for VerifyError {}

/// A proof with the public values the prover derived from the secret.
#[derive(Clone, Debug)]
pub struct Proof {
    pub pk: [u8; 32],
    pub shares: Vec<[u8; 32]>,
    /// The wire form `u8(system) || receipt`.
    pub pi: Vec<u8>,
    pub prove_ms: f64,
    pub user_cycles: u64,
    pub total_cycles: u64,
}

/// The proof-system trait: one statement (the guest), one wire form, one identifier per system.
pub trait ProofSystem {
    const ID: u8;
    #[cfg(feature = "prove")]
    fn prove(&self, domain: u8, sk: &[u8; 32], m: &[[u8; 32]]) -> Result<Proof, String>;
    fn verify(&self, domain: u8, pk: &[u8; 32], m: &[[u8; 32]], s: &[[u8; 32]], pi: &[u8]) -> Result<(), VerifyError>;
}

/// Proof system 2: the RISC Zero zkVM.
#[derive(Clone, Copy, Debug)]
pub struct Risc0 {
    pub kind: Kind,
}

impl Risc0 {
    /// The chain's system for a domain (the kind the rule admits there).
    pub const fn for_domain(domain: u8) -> Risc0 {
        Risc0 { kind: kind_for(domain) }
    }

    #[cfg(feature = "prove")]
    fn input_bytes(domain: u8, sk: &[u8; 32], m: &[[u8; 32]]) -> Vec<u8> {
        let mut v = Vec::with_capacity(37 + 32 * m.len());
        v.push(domain);
        v.extend_from_slice(&(m.len() as u32).to_le_bytes());
        v.extend_from_slice(sk);
        for x in m {
            v.extend_from_slice(x);
        }
        v
    }

    /// Decode and verify the receipt alone (no journal comparison): the identifier, the domain's bound, the kind, the proof.
    pub fn verify_receipt(&self, domain: u8, pi: &[u8]) -> Result<Receipt, VerifyError> {
        let bound = max_bytes_for(domain);
        if pi.len() > bound {
            return Err(VerifyError::Bound { len: pi.len(), bound });
        }
        if pi.is_empty() || pi[0] != Self::ID {
            return Err(VerifyError::System);
        }
        let receipt: Receipt = bincode::deserialize(&pi[1..]).map_err(|e| VerifyError::Decode(e.to_string()))?;
        let ok_kind = match (&receipt.inner, self.kind) {
            (InnerReceipt::Succinct(_), Kind::Succinct) => true,
            (InnerReceipt::Composite(c), Kind::Composite) => c.segments.len() == 1,
            (InnerReceipt::Groth16(_), Kind::Groth16) => true,
            _ => false,
        };
        if !ok_kind {
            return Err(VerifyError::Kind { domain });
        }
        receipt.verify(Digest::from_bytes(PI_IMAGE_ID)).map_err(|e| VerifyError::Proof(e.to_string()))?;
        Ok(receipt)
    }
}

impl ProofSystem for Risc0 {
    const ID: u8 = SYSTEM_RISC0;

    #[cfg(feature = "prove")]
    fn prove(&self, domain: u8, sk: &[u8; 32], m: &[[u8; 32]]) -> Result<Proof, String> {
        if domain > 1 || m.is_empty() || m.len() > K_MAX {
            return Err("domain or K out of range".into());
        }
        let input = Self::input_bytes(domain, sk, m);
        let env = ExecutorEnv::builder().write(&input).map_err(|e| e.to_string())?.build().map_err(|e| e.to_string())?;
        let opts = match self.kind {
            Kind::Succinct => ProverOpts::succinct(),
            Kind::Composite => ProverOpts::composite(),
            Kind::Groth16 => ProverOpts::groth16(),
        };
        let t0 = Instant::now();
        let info = default_prover().prove_with_opts(env, PI_GUEST_ELF, &opts).map_err(|e| e.to_string())?;
        let prove_ms = t0.elapsed().as_secs_f64() * 1e3;
        let j = &info.receipt.journal.bytes;
        let k = m.len();
        if j.len() != journal_len(k) {
            return Err("journal length".into());
        }
        let mut pk = [0u8; 32];
        pk.copy_from_slice(&j[5..37]);
        let shares = (0..k)
            .map(|i| {
                let mut s = [0u8; 32];
                s.copy_from_slice(&j[37 + 32 * k + 32 * i..37 + 32 * k + 32 * (i + 1)]);
                s
            })
            .collect();
        let mut pi = vec![Self::ID];
        pi.extend(bincode::serialize(&info.receipt).map_err(|e| e.to_string())?);
        Ok(Proof { pk, shares, pi, prove_ms, user_cycles: info.stats.user_cycles, total_cycles: info.stats.total_cycles })
    }

    fn verify(&self, domain: u8, pk: &[u8; 32], m: &[[u8; 32]], s: &[[u8; 32]], pi: &[u8]) -> Result<(), VerifyError> {
        if domain > 1 || m.is_empty() || m.len() > K_MAX || s.len() != m.len() {
            return Err(VerifyError::Arg);
        }
        let receipt = self.verify_receipt(domain, pi)?;
        let expected = journal_bytes(domain, pk, m, s);
        if receipt.journal.bytes != expected {
            return Err(VerifyError::Journal);
        }
        Ok(())
    }
}

/// The lab build's entry point: prove once per block after the nonce (domain 0, the one-segment composite; never per
/// attempt), or once per claim (domain 1, the Groth16 wrap: about a minute of CPU proving behind a container).
#[cfg(feature = "prove")]
pub fn pi_prove(domain: u8, sk: &[u8; 32], m: &[[u8; 32]]) -> Result<Proof, String> {
    Risc0::for_domain(domain).prove(domain, sk, m)
}

/// The lab build's entry point: verify a block's `pi` (domain 0) against the header's `BondKey`, the replay's `m_i` and the
/// carried shares, or a claim's `pi` (domain 1, the registry's precompile) against `pk`, the claim message and its share.
/// The error is the refusal (`VerifyError::code()` is the node's code; `to_string()` the log line).
pub fn pi_verify(domain: u8, pk: &[u8; 32], m: &[[u8; 32]], s: &[[u8; 32]], pi: &[u8]) -> Result<(), VerifyError> {
    Risc0::for_domain(domain).verify(domain, pk, m, s, pi)
}

#[cfg(all(test, feature = "prove"))]
mod tests {
    use super::*;

    /// The guest this build embeds is the ratified one: its id equals the pinned constant. A build whose guest prints
    /// another id (a native build at another path, another lock, another builder image) fails here before any set
    /// is generated under it.
    #[test]
    fn pinned_id_is_the_guest() {
        assert_eq!(guest_image_id_hex(), image_id_hex(), "the guest's image id is not PI_IMAGE_ID: a consensus change, or a build off the docker-mode recipe");
        assert_eq!(Digest::from(PI_GUEST_ID).as_bytes(), &PI_IMAGE_ID);
    }
}

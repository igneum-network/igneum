//! Igneum Wallet, the Money shielded side (ADR-0001, docs/money/ADR-0001-PRIVATE-PQ-SAFE-MONEY-2026-10-18.md,
//! section 4.3; stage one, cuts 3 and 4 of 18 October 2026). A library and a command-line tool, independent of the node:
//! nothing here talks to a chain; blocks arrive as bytes and leave as notes.
//!
//! The pieces, each its own module:
//! - `keys`: one 32-byte seed from the operating system's randomness; from it the spending secret, the nullifier
//!   key, the ML-KEM-768 key pair for receiving and the outgoing-record key; the full viewing key (everything but
//!   the spending secret) decrypts incoming notes and computes nullifiers and cannot spend; the address
//!   (pk, ek, d) with d the diversifier the ADR's F-756 leaves OPEN (carried as eleven zero bytes, doing nothing,
//!   claimed as nothing).
//! - `hash`: H, the in-circuit hash of the construction (pk, the commitment, the nullifier): Poseidon2 over
//!   Goldilocks, the prover's instance (stage one, cut four; ADR section 4.7), in plain Rust, checked against the
//!   vectors the node and the prover share. B, the hash outside the circuit, is BLAKE3 and is final.
//! - `note`: the note (value, owner pk, randomness, diversifier, memo) sealed to a recipient's ML-KEM-768
//!   encapsulation key with ChaCha20-Poly1305 under a key derived from the KEM's shared secret; one output shape,
//!   every ciphertext the same length.
//! - `scan`: a stream of blocks (outputs and nullifiers) trial-decrypted with the full viewing key; the wallet's
//!   notes with their positions, their nullifiers, and which are spent; the balance as two numbers.
//! - `backup`: the seed as 24 BIP-39 words with the standard's checksum; the words alone restore everything by a
//!   rescan.
//! - `witness`: the two statements' witnesses in the prover's form (tools/money/stage1-prover): a shield (one
//!   note sealed to an address, its value public) and a spend (two inputs, a dummy padding one, their paths
//!   under an anchor the node served, two outputs: the payment and the change, the fee and the public value);
//!   the proof is the prover's and is not here.
//!
//! The dependency reasons (the workspace's rule, DESIGN.md 1.12): `libcrux-ml-kem` is the formally verified
//! ML-KEM (FIPS 203) used by Signal and Mozilla NSS and the only lattice code here; no primitive is hand-rolled.
//! `chacha20poly1305`, `blake3`, `bip39` and `zeroize` are lines the workspace already pins.
//!
//! Nothing in this crate says "private", "safe" or "PQ" about itself; the claims belong to ADR-0001
//! section 4.6, whose verdict is "Money remains research", and this crate is one of its measured rows.

pub mod backup;
pub mod hash;
pub mod hex;
pub mod keys;
pub mod note;
pub mod scan;
pub mod vault;
pub mod witness;

/// Every size the construction fixes, in bytes, in one place.
pub mod sizes {
    /// The seed, the spending secret, the nullifier key, the outgoing-record key, pk, a commitment, a nullifier.
    pub const SECRET: usize = 32;
    /// The randomness ML-KEM-768 key generation consumes (d and z, FIPS 203 algorithm 19).
    pub const KEM_SEED: usize = libcrux_ml_kem::KEY_GENERATION_SEED_SIZE;
    /// ML-KEM-768 encapsulation key (FIPS 203 table 3).
    pub const KEM_PUBLIC_KEY: usize = 1184;
    /// ML-KEM-768 decapsulation key.
    pub const KEM_PRIVATE_KEY: usize = 2400;
    /// ML-KEM-768 ciphertext.
    pub const KEM_CIPHERTEXT: usize = 1088;
    /// The diversifier (ADR section 4.3 item 1; F-756 OPEN: it does nothing yet).
    pub const DIVERSIFIER: usize = 11;
    /// The address: pk, ek, d.
    pub const ADDRESS: usize = SECRET + KEM_PUBLIC_KEY + DIVERSIFIER;
    /// The memo's longest form (ADR section 4.3 item 6).
    pub const MEMO_MAX: usize = 128;
    /// The note plaintext: version 2, value 16, randomness 32, diversifier 11, memo length 1, memo 128.
    pub const NOTE_PLAINTEXT: usize = 1 + 16 + SECRET + DIVERSIFIER + 1 + MEMO_MAX;
    /// The AEAD's tag.
    pub const AEAD_TAG: usize = 16;
    /// The sealed note body: the plaintext and the tag.
    pub const NOTE_BODY: usize = NOTE_PLAINTEXT + AEAD_TAG;
    /// The ciphertext a block carries for one output, the commitment excluded: the KEM ciphertext and the body.
    pub const OUTPUT_CIPHERTEXT: usize = KEM_CIPHERTEXT + NOTE_BODY;
    /// One output as carried: the commitment and the ciphertext.
    pub const OUTPUT: usize = SECRET + OUTPUT_CIPHERTEXT;
    /// The full viewing key's serialised form: version 1, pk 32, nk 32, the KEM seed 64, ivk 32.
    pub const FULL_VIEWING_KEY: usize = 1 + SECRET + SECRET + KEM_SEED + SECRET;
    /// The commitment tree's depth (ADR section 4.3 item 2).
    pub const TREE_DEPTH: usize = 32;
}

/// The crate's one error type: an input the library refuses, said in words.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Error(pub String);

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for Error {}

pub type Result<T> = std::result::Result<T, Error>;

pub(crate) fn err<T>(s: impl Into<String>) -> Result<T> {
    Err(Error(s.into()))
}

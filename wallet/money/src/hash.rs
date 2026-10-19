//! H, the in-circuit hash of ADR-0001 section 4.3 (pk = H(sk_spend, nk); cm = H(H(v, pk), r); nf = H(H(nk, cm),
//! [position, 0, 0, 0])), fixed by stage one's cut four as Poseidon2 over the Goldilocks field, the instance
//! Miden VM 0.36.1 runs natively (miden-crypto 0.36.1: width 12, rate 8, capacity 4, x^7, four plus twenty-two
//! plus four rounds, the Plonky3 Goldilocks permutation with the HorizenLabs constants); every use is the
//! two-to-one merge H(a, b): the permutation of [a, b, 0, 0, 0, 0], the first four elements out. It is written
//! here in plain Rust with no dependency on the prover's crates (the workspace keeps its rust-version) and is
//! checked against the vectors the node, the prover and this crate share (tools/money/vectors). B, the hash
//! outside the circuit (BLAKE3, the chain's own family), is the free functions below.
//!
//! A 32-byte value in H is a WORD: four field elements, each canonical (below the prime), little-endian in
//! order. Keys and randomness drawn as bytes are reduced into words (`reduce_word`) before use, so every
//! value this crate hands the circuit or the node is a word; the node refuses a commitment, a nullifier or an
//! anchor that is not one.

/// The Goldilocks prime, 2^64 - 2^32 + 1.
pub const P: u64 = 0xFFFF_FFFF_0000_0001;
const EPS: u64 = 0xFFFF_FFFF;

/// Four field elements.
pub type Word = [u64; 4];

#[inline]
fn fadd(a: u64, b: u64) -> u64 {
    let (s, c) = a.overflowing_add(b);
    let s = if c { s.wrapping_add(EPS) } else { s };
    if s >= P { s - P } else { s }
}

#[inline]
fn fmul(a: u64, b: u64) -> u64 {
    let x = (a as u128) * (b as u128);
    let lo = x as u64;
    let hi = (x >> 64) as u64;
    let hi_hi = hi >> 32;
    let hi_lo = hi & EPS;
    let (t0, borrow) = lo.overflowing_sub(hi_hi);
    let t0 = if borrow { t0.wrapping_sub(EPS) } else { t0 };
    let t1 = hi_lo * EPS;
    let (t2, c) = t0.overflowing_add(t1);
    let t2 = if c { t2.wrapping_add(EPS) } else { t2 };
    if t2 >= P { t2 - P } else { t2 }
}

#[inline]
fn sbox(x: u64) -> u64 {
    let x2 = fmul(x, x);
    let x4 = fmul(x2, x2);
    fmul(fmul(x4, x2), x)
}

fn matmul_external(s: &mut [u64; 12]) {
    for i in 0..3 {
        let x = &mut s[4 * i..4 * i + 4];
        let t01 = fadd(x[0], x[1]);
        let t23 = fadd(x[2], x[3]);
        let t0123 = fadd(t01, t23);
        let t01123 = fadd(t0123, x[1]);
        let t01233 = fadd(t0123, x[3]);
        let x3 = fadd(t01233, fadd(x[0], x[0]));
        let x1 = fadd(t01123, fadd(x[2], x[2]));
        let x0 = fadd(t01123, t01);
        let x2 = fadd(t01233, t23);
        x[0] = x0;
        x[1] = x1;
        x[2] = x2;
        x[3] = x3;
    }
    let mut stored = [0u64; 4];
    for j in 0..3 {
        for l in 0..4 {
            stored[l] = fadd(stored[l], s[4 * j + l]);
        }
    }
    for (i, v) in s.iter_mut().enumerate() {
        *v = fadd(*v, stored[i % 4]);
    }
}

fn matmul_internal(s: &mut [u64; 12]) {
    let mut sum = 0u64;
    for v in s.iter() {
        sum = fadd(sum, *v);
    }
    for i in 0..12 {
        s[i] = fadd(fmul(s[i], MAT_DIAG[i]), sum);
    }
}

/// The Poseidon2 permutation on twelve elements.
pub fn permute(s: &mut [u64; 12]) {
    matmul_external(s);
    for r in 0..4 {
        for i in 0..12 {
            s[i] = sbox(fadd(s[i], ARK_EXT_INITIAL[r][i]));
        }
        matmul_external(s);
    }
    for r in 0..22 {
        s[0] = sbox(fadd(s[0], ARK_INT[r]));
        matmul_internal(s);
    }
    for r in 0..4 {
        for i in 0..12 {
            s[i] = sbox(fadd(s[i], ARK_EXT_TERMINAL[r][i]));
        }
        matmul_external(s);
    }
}

/// H(a, b).
pub fn merge(a: Word, b: Word) -> Word {
    let mut s = [0u64; 12];
    s[..4].copy_from_slice(&a);
    s[4..8].copy_from_slice(&b);
    permute(&mut s);
    [s[0], s[1], s[2], s[3]]
}

/// A word's 32 bytes.
pub fn word_to_bytes(w: Word) -> [u8; 32] {
    let mut out = [0u8; 32];
    for i in 0..4 {
        out[8 * i..8 * i + 8].copy_from_slice(&w[i].to_le_bytes());
    }
    out
}

/// A word from 32 bytes, or None when an element is not canonical.
pub fn word_from_bytes(b: &[u8; 32]) -> Option<Word> {
    let mut w = [0u64; 4];
    for i in 0..4 {
        w[i] = u64::from_le_bytes(b[8 * i..8 * i + 8].try_into().unwrap());
        if w[i] >= P {
            return None;
        }
    }
    Some(w)
}

/// Whether 32 bytes are a word.
pub fn is_word(b: &[u8; 32]) -> bool {
    word_from_bytes(b).is_some()
}

/// Bytes drawn at random or derived, made a word: each element reduced modulo the prime (a bias of 2^-32 on
/// one element in 2^32 draws; the node and the circuit take words only).
pub fn reduce_word(b: &[u8; 32]) -> [u8; 32] {
    let mut w = [0u64; 4];
    for i in 0..4 {
        w[i] = u64::from_le_bytes(b[8 * i..8 * i + 8].try_into().unwrap()) % P;
    }
    word_to_bytes(w)
}

fn w(b: &[u8; 32]) -> Word {
    word_from_bytes(b).unwrap_or_else(|| word_from_bytes(&reduce_word(b)).unwrap())
}

/// A 128-bit value as four 32-bit limbs, limb 0 the least significant.
pub fn value_word(v: u128) -> Word {
    [(v & 0xffff_ffff) as u64, ((v >> 32) & 0xffff_ffff) as u64, ((v >> 64) & 0xffff_ffff) as u64, ((v >> 96) & 0xffff_ffff) as u64]
}

/// A tree node H(left, right) over 32-byte words.
pub fn node(left: &[u8; 32], right: &[u8; 32]) -> [u8; 32] {
    word_to_bytes(merge(w(left), w(right)))
}

/// The in-circuit hash's three uses.
pub trait CircuitHash {
    /// The instance's name for the record.
    const NAME: &'static str;
    /// The recipient key commitment pk = H(sk_spend, nk).
    fn pk(sk_spend: &[u8; 32], nk: &[u8; 32]) -> [u8; 32];
    /// The note commitment cm = H(H(v, pk), r), v as four 32-bit limbs.
    fn commitment(value: u128, pk: &[u8; 32], r: &[u8; 32]) -> [u8; 32];
    /// The nullifier nf = H(H(nk, cm), [position, 0, 0, 0]).
    fn nullifier(nk: &[u8; 32], cm: &[u8; 32], position: u64) -> [u8; 32];
}

/// The instance of record.
pub struct Poseidon2;

impl CircuitHash for Poseidon2 {
    const NAME: &'static str = "poseidon2-goldilocks-v1";

    fn pk(sk_spend: &[u8; 32], nk: &[u8; 32]) -> [u8; 32] {
        word_to_bytes(merge(w(sk_spend), w(nk)))
    }

    fn commitment(value: u128, pk: &[u8; 32], r: &[u8; 32]) -> [u8; 32] {
        word_to_bytes(merge(merge(value_word(value), w(pk)), w(r)))
    }

    fn nullifier(nk: &[u8; 32], cm: &[u8; 32], position: u64) -> [u8; 32] {
        word_to_bytes(merge(merge(w(nk), w(cm)), [position, 0, 0, 0]))
    }
}

/// The instance this build runs on.
pub type H = Poseidon2;

/// B: a 32-byte key derived from `material` under a fixed context string (BLAKE3's derive_key mode).
pub fn b_derive(context: &str, material: &[u8]) -> [u8; 32] {
    blake3::derive_key(context, material)
}

/// B: `N` bytes derived from `material` under a fixed context string (BLAKE3's derive_key mode, extended output).
pub fn b_derive_n<const N: usize>(context: &str, material: &[u8]) -> [u8; N] {
    let mut h = blake3::Hasher::new_derive_key(context);
    h.update(material);
    let mut out = [0u8; N];
    h.finalize_xof().fill(&mut out);
    out
}

/// B: a plain 32-byte hash of one byte string.
pub fn b_hash(bytes: &[u8]) -> [u8; 32] {
    *blake3::hash(bytes).as_bytes()
}

const MAT_DIAG: [u64; 12] = [
    0xfffffffeffffffff, 0x0000000000000001, 0x0000000000000002, 0x7fffffff80000001, 0x0000000000000003, 0x0000000000000004,
    0x7fffffff80000000, 0xfffffffefffffffe, 0xfffffffefffffffd, 0xbfffffff40000001, 0x3fffffffc0000000, 0xdfffffff20000001,
];

const ARK_EXT_INITIAL: [[u64; 12]; 4] = [
    [0x13dcf33aba214f46, 0x30b3b654a1da6d83, 0x1fc634ada6159b56, 0x937459964dc03466, 0xedd2ef2ca7949924, 0xede9affde0e22f68, 0x8515b9d6bac9282d, 0x6b5c07b4e9e900d8, 0x1ec66368838c8a08, 0x9042367d80d1fbab, 0x400283564a3c3799, 0x4a00be0466bca75e],
    [0x7913beee58e3817f, 0xf545e88532237d90, 0x22f8cb8736042005, 0x6f04990e247a2623, 0xfe22e87ba37c38cd, 0xd20e32c85ffe2815, 0x117227674048fe73, 0x4e9fb7ea98a6b145, 0xe0866c232b8af08b, 0x00bbc77916884964, 0x7031c0fb990d7116, 0x240a9e87cf35108f],
    [0x2e6363a5a12244b3, 0x5e1c3787d1b5011c, 0x4132660e2a196e8b, 0x3a013b648d3d4327, 0xf79839f49888ea43, 0xfe85658ebafe1439, 0xb6889825a14240bd, 0x578453605541382b, 0x4508cda8f6b63ce9, 0x9c3ef35848684c91, 0x0812bde23c87178c, 0xfe49638f7f722c14],
    [0x8e3f688ce885cbf5, 0xb8e110acf746a87d, 0xb4b2e8973a6dabef, 0x9e714c5da3d462ec, 0x6438f9033d3d0c15, 0x24312f7cf1a27199, 0x23f843bb47acbf71, 0x9183f11a34be9f01, 0x839062fbb9d45dbf, 0x24b56e7e6c2e43fa, 0xe1683da61c962a72, 0xa95c63971a19bfa7],
];

const ARK_INT: [u64; 22] = [
    0x4adf842aa75d4316, 0xf8fbb871aa4ab4eb, 0x68e85b6eb2dd6aeb, 0x07a0b06b2d270380, 0xd94e0228bd282de4, 0x8bdd91d3250c5278, 0x209c68b88bba778f, 0xb5e18cdab77f3877,
    0xb296a3e808da93fa, 0x8370ecbda11a327e, 0x3f9075283775dad8, 0xb78095bb23c6aa84, 0x3f36b9fe72ad4e5f, 0x69bc96780b10b553, 0x3f1d341f2eb7b881, 0x4e939e9815838818,
    0xda366b3ae2a31604, 0xbc89db1e7287d509, 0x6102f411f9ef5659, 0x58725c5e7ac1f0ab, 0x0df5856c798883e7, 0xf7bb62a8da4c961b,
];

const ARK_EXT_TERMINAL: [[u64; 12]; 4] = [
    [0xc68be7c94882a24d, 0xaf996d5d5cdaedd9, 0x9717f025e7daf6a5, 0x6436679e6e7216f4, 0x8a223d99047af267, 0xbb512e35a133ba9a, 0xfbbf44097671aa03, 0xf04058ebf6811e61, 0x5cca84703fac7ffb, 0x9b55c7945de6469f, 0x8e05bf09808e934f, 0x2ea900de876307d7],
    [0x7748fff2b38dfb89, 0x6b99a676dd3b5d81, 0xac4bb7c627cf7c13, 0xadb6ebe5e9e2f5ba, 0x2d33378cafa24ae3, 0x1e5b73807543f8c2, 0x09208814bfebb10f, 0x782e64b6bb5b93dd, 0xadd5a48eac90b50f, 0xadd4c54c736ea4b1, 0xd58dbb86ed817fd8, 0x6d5ed1a533f34ddd],
    [0x28686aa3e36b7cb9, 0x591abd3476689f36, 0x047d766678f13875, 0xa2a11112625f5b49, 0x21fd10a3f8304958, 0xf9b40711443b0280, 0xd2697eb8b2bde88e, 0x3493790b51731b3f, 0x11caf9dd73764023, 0x7acfb8f72878164e, 0x744ec4db23cefc26, 0x1e00e58f422c6340],
    [0x21dd28d906a62dda, 0xf32a46ab5f465b5f, 0xbfce13201f3f7e6b, 0xf30d2e7adb5304e2, 0xecdf4ee4abad48e9, 0xf94e82182d395019, 0x4ee52e3744d887c5, 0xa1341c7cac0083b2, 0x2302fb26c30c834a, 0xaea3c587273bf7d3, 0xf798e24961823ec7, 0x962deba3e9a2cd94],
];

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_permutation_known_answer() {
        let mut s: [u64; 12] = core::array::from_fn(|i| i as u64);
        permute(&mut s);
        assert_eq!(
            s,
            [
                0xf292ab67c0f14b03, 0x0a32f1b37656544c, 0x053c61ab895498de, 0x02ff92e55b196ffb, 0x58176e8f6f58cab2, 0xb0aa1206e7aec0f8,
                0xe90c13f3dce83ca4, 0xf4da15333edf39c2, 0x23b701c053c2ca6c, 0xd233d593dcdfbf58, 0x4effa5f9516fb52e, 0x0aaf4489f1f40166
            ]
        );
    }

    #[test]
    fn the_three_uses_are_separated() {
        let a = reduce_word(&[1u8; 32]);
        let b = reduce_word(&[2u8; 32]);
        assert_ne!(H::pk(&a, &b), H::pk(&b, &a));
        assert_ne!(H::commitment(1, &a, &b), H::commitment(2, &a, &b));
        assert_ne!(H::commitment(1, &a, &b), H::commitment(1 << 64, &a, &b));
        assert_ne!(H::nullifier(&a, &b, 0), H::nullifier(&a, &b, 1));
        assert_ne!(H::pk(&a, &b), H::nullifier(&a, &b, 0));
        assert!(is_word(&H::pk(&a, &b)));
        let mut top = [0xffu8; 32];
        assert!(!is_word(&top));
        top = reduce_word(&top);
        assert!(is_word(&top));
    }

    #[test]
    fn derive_n_prefix_matches_derive() {
        let full: [u8; 64] = b_derive_n("ctx", b"m");
        assert_eq!(&full[..32], &b_derive("ctx", b"m"));
    }
}

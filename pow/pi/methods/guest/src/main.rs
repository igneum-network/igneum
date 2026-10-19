//! The guest of SPEC-R0A-v0.md section 0.9.1: the statement PI.
//!
//! Input (the host's private bytes): `u8(domain) || LE32(K) || sk[32] || m_0 || ... || m_(K-1)`.
//! Journal (public): `u8(domain) || LE32(K) || pk || m_0 || ... || m_(K-1) || s_0 || ... || s_(K-1)`,
//! with `pk = BLAKE3(tag("IgneumHash/R0A/bond-pk") || sk)` and `s_i = BLAKE3k(sk, "bond" || LE32(i) || m_i)`.
//! `domain` 0 is a block, 1 the hostile claim or release; `K` in 1 to 64. The secret never leaves the guest.
use risc0_zkvm::guest::env;

const TAG_BOND_PK: &[u8] = b"IgneumHash/R0A/bond-pk";

fn main() {
    let input: Vec<u8> = env::read();
    assert!(input.len() >= 37, "input shorter than the header and the secret");
    let domain = input[0];
    assert!(domain <= 1, "domain must be 0 (block) or 1 (claim)");
    let k = u32::from_le_bytes([input[1], input[2], input[3], input[4]]) as usize;
    assert!((1..=64).contains(&k), "K must be in 1 to 64");
    assert_eq!(input.len(), 37 + 32 * k, "input length is not 37 + 32 K");
    let mut sk = [0u8; 32];
    sk.copy_from_slice(&input[5..37]);

    let mut journal = Vec::with_capacity(5 + 32 + 64 * k);
    journal.push(domain);
    journal.extend_from_slice(&(k as u32).to_le_bytes());

    let mut g = blake3::Hasher::new();
    g.update(&[TAG_BOND_PK.len() as u8]);
    g.update(TAG_BOND_PK);
    g.update(&sk);
    journal.extend_from_slice(g.finalize().as_bytes());

    journal.extend_from_slice(&input[37..]);
    for i in 0..k {
        let m = &input[37 + 32 * i..37 + 32 * (i + 1)];
        let mut h = blake3::Hasher::new_keyed(&sk);
        h.update(b"bond");
        h.update(&(i as u32).to_le_bytes());
        h.update(m);
        journal.extend_from_slice(h.finalize().as_bytes());
    }
    env::commit_slice(&journal);
}

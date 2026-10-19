//! The stub algorithm `stub-v0`: a placeholder mixing function so the stub worker and the supervisor's stub
//! verifier agree. It is not a consensus hash, not a candidate, and says nothing about any card. It exists so
//! the whole path (jobs, shares, CPU verification, the tuner, the economics display) runs before Phase Zero
//! picks the real algorithm. Like the candidates behind `ignpow.h`, the epoch is a function of the height.

pub const STUB_ALGO: &str = "stub-v0";
pub const STUB_NOTE: &str = "stub worker: placeholder hash, not consensus, no card is mined";
/// Blocks per epoch for the stub.
pub const STUB_EPOCH_LENGTH: u64 = 100;

pub fn epoch_of(height: u64) -> u64 {
    height / STUB_EPOCH_LENGTH
}

/// The 32-byte seed of an epoch (the stub's stand-in for a light cache).
pub fn seed_of_epoch(epoch: u64) -> [u8; 32] {
    let mut s = [0u8; 32];
    s[..8].copy_from_slice(&epoch.to_le_bytes());
    let mut x = mix64(epoch ^ 0x5365_6564_0000_0001);
    for i in 1..4 {
        x = mix64(x);
        s[i * 8..i * 8 + 8].copy_from_slice(&x.to_le_bytes());
    }
    s
}

#[inline]
fn mix64(mut z: u64) -> u64 {
    // splitmix64 finaliser
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z ^ (z >> 31)
}

fn words(b: &[u8; 32]) -> [u64; 4] {
    let mut w = [0u64; 4];
    for (i, chunk) in b.chunks(8).enumerate() {
        let mut a = [0u8; 8];
        a.copy_from_slice(chunk);
        w[i] = u64::from_le_bytes(a);
    }
    w
}

fn bytes(w: &[u64; 4]) -> [u8; 32] {
    let mut b = [0u8; 32];
    for (i, x) in w.iter().enumerate() {
        b[i * 8..i * 8 + 8].copy_from_slice(&x.to_le_bytes());
    }
    b
}

/// (mix, hash) for a header, a seed and a nonce. `hash` is big-endian compared against the target.
pub fn stub_hash(header: &[u8; 32], seed: &[u8; 32], nonce: u64) -> ([u8; 32], [u8; 32]) {
    let h = words(header);
    let s = words(seed);
    let mut state = 0x9E37_79B9_7F4A_7C15u64 ^ nonce;
    let mut mix = [0u64; 4];
    for i in 0..4 {
        state = mix64(state ^ h[i].rotate_left((i as u32) * 13 + 1) ^ s[i]);
        mix[i] = state;
    }
    for _ in 0..8 {
        for i in 0..4 {
            let prev = mix[(i + 3) % 4];
            mix[i] = mix64(mix[i] ^ prev.rotate_left(17) ^ nonce);
        }
    }
    let mut hash = [0u64; 4];
    let mut acc = mix64(mix[0] ^ mix[1] ^ mix[2] ^ mix[3]);
    for (i, h) in hash.iter_mut().enumerate() {
        acc = mix64(acc ^ mix[i] ^ s[(i + 1) % 4]);
        *h = acc;
    }
    let mut out = [0u8; 32];
    for (i, x) in hash.iter().enumerate() {
        out[i * 8..i * 8 + 8].copy_from_slice(&x.to_be_bytes());
    }
    (bytes(&mix), out)
}

/// (mix, hash) for a height, a header and a nonce: the shape of `ignpow_hash`.
pub fn stub_hash_at(height: u64, header: &[u8; 32], nonce: u64) -> ([u8; 32], [u8; 32]) {
    stub_hash(header, &seed_of_epoch(epoch_of(height)), nonce)
}

/// Big-endian 256-bit comparison: a hash meets the target when it is not above it.
pub fn meets_target(hash: &[u8; 32], target: &[u8; 32]) -> bool {
    hash <= target
}

/// The target whose expected hashes per result is `difficulty` (2^256 / difficulty), big-endian.
pub fn target_for_difficulty(difficulty: f64) -> [u8; 32] {
    let mut t = [0xffu8; 32];
    if !(difficulty > 1.0) {
        return t;
    }
    let hi = (3.402_823_669_209_385e38_f64 / difficulty) as u128;
    t[..16].copy_from_slice(&hi.to_be_bytes());
    t
}

/// Expected hashes per result for a target.
pub fn difficulty_for_target(target: &[u8; 32]) -> f64 {
    let mut hi = [0u8; 16];
    hi.copy_from_slice(&target[..16]);
    let hi = u128::from_be_bytes(hi);
    if hi == 0 {
        return 3.402_823_669_209_385e38_f64 * 3.402_823_669_209_385e38_f64;
    }
    3.402_823_669_209_385e38_f64 / (hi as f64)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deterministic_and_nonce_sensitive() {
        let h = [1u8; 32];
        let (m1, x1) = stub_hash_at(150, &h, 1);
        let (m2, x2) = stub_hash_at(199, &h, 1);
        let (_, x3) = stub_hash_at(150, &h, 2);
        let (_, x4) = stub_hash_at(200, &h, 1);
        assert_eq!(m1, m2, "same epoch, same mix");
        assert_eq!(x1, x2);
        assert_ne!(x1, x3);
        assert_ne!(x1, x4, "a new epoch changes the hash");
        assert_eq!(epoch_of(199), 1);
        assert_eq!(epoch_of(200), 2);
    }

    #[test]
    fn target_round_trip() {
        for d in [2.0, 1000.0, 1e6, 1e12] {
            let t = target_for_difficulty(d);
            let back = difficulty_for_target(&t);
            assert!((back / d - 1.0).abs() < 1e-6, "{d} -> {back}");
        }
        assert_eq!(target_for_difficulty(0.5), [0xff; 32]);
    }

    #[test]
    fn hit_rate_is_about_the_difficulty() {
        let h = [7u8; 32];
        let t = target_for_difficulty(64.0);
        let hits = (0..64_000u64).filter(|n| meets_target(&stub_hash_at(5, &h, *n).1, &t)).count();
        assert!((700..1300).contains(&hits), "{hits}");
    }
}

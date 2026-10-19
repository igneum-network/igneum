//! The sortition draw as the node computes it (`consensus/igneum/sortition.go`, the frozen rule set's section 1 and
//! 2): units n_b = floor(B_b / u) with u = B_total / 2^16; the lot x = BLAKE3("IgneumHash/R0A/lot" || V || K_b ||
//! LE64(j) || LE64(h)); theta_r = 2^256 x min(1, E x 2^r / 2^16); the eligible round is the smallest r with any
//! unit's lot under theta_r, MaxRound + 1 when none. The wallet recomputes it so "your share of the draw" is read
//! from the chain's own inputs (the seed, the snapshot's bond and total) and not from a node's word.

use igneum_wallet_core::U256;

pub const LOT_DOMAIN: &[u8] = b"IgneumHash/R0A/lot";
pub const SEED_DOMAIN: &[u8] = b"IgneumHash/R0A/sortition";
/// the units of a whole epoch, 2^16
pub const UNITS: u64 = 1 << 16;

/// The lottery's parameters the draw needs (from `igneum_sortition`'s `params`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Params {
    pub expected: u64,
    pub max_round: u64,
    pub unbonded_round: u64,
    pub delta_secs: u64,
}

/// n_b = floor(B_b / u), u = B_total / 2^16, capped at 2^16; 0 when u or the bond is 0.
pub fn units(bond: &U256, total: &U256) -> u64 {
    let (u, _) = total.div_rem_u64(UNITS);
    if u.is_zero() || bond.is_zero() {
        return 0;
    }
    // bond / u by long division (one call per reading)
    let n = div(bond, &u);
    match n.to_u64() {
        Some(x) if x <= UNITS => x,
        _ => UNITS,
    }
}

fn div(a: &U256, b: &U256) -> U256 {
    let mut q = U256::ZERO;
    let mut r = U256::ZERO;
    for i in 0..256 {
        r = r.checked_mul_u64(2).unwrap_or(U256::MAX);
        let byte = a.0[i / 8];
        if (byte >> (7 - (i % 8))) & 1 == 1 {
            r.0[31] |= 1;
        }
        q = q.checked_mul_u64(2).unwrap_or(U256::MAX);
        if r >= *b {
            r = r.checked_sub(b).unwrap_or(U256::ZERO);
            q.0[31] |= 1;
        }
    }
    q
}

/// x(b, j, h) as 32 big-endian bytes.
pub fn lot(seed: &[u8; 32], key: &[u8; 32], unit: u64, height: u64) -> [u8; 32] {
    let mut h = blake3::Hasher::new();
    h.update(LOT_DOMAIN);
    h.update(seed);
    h.update(key);
    h.update(&unit.to_le_bytes());
    h.update(&height.to_le_bytes());
    *h.finalize().as_bytes()
}

/// theta_r as a 256-bit value; `None` means 2^256 (every unit eligible).
pub fn theta(expected: u64, round: u64) -> Option<U256> {
    if round >= 64 {
        return None;
    }
    let n = (expected as u128) << round;
    if n >= UNITS as u128 {
        return None;
    }
    // n << 240: n < 2^16 so the product fits
    let mut t = [0u8; 32];
    let n16 = n as u16;
    t[0] = (n16 >> 8) as u8;
    t[1] = n16 as u8;
    Some(U256(t))
}

/// The smallest round a key with `units` units is eligible at for a height under a seed.
pub fn eligible_round(p: &Params, seed: &[u8; 32], key: &[u8; 32], units: u64, height: u64) -> u64 {
    if units == 0 {
        return p.max_round + 1;
    }
    let mut min_lot: Option<U256> = None;
    for j in 0..units {
        let x = U256(lot(seed, key, j, height));
        if min_lot.map(|m| x < m).unwrap_or(true) {
            min_lot = Some(x);
        }
    }
    let m = min_lot.unwrap_or(U256::MAX);
    for r in 0..=p.max_round {
        match theta(p.expected, r) {
            None => return r,
            Some(t) if m < t => return r,
            _ => {}
        }
    }
    p.max_round + 1
}

/// What a bonded identity holds of the draw: its units, its share of the bonded total, and the chance that at least
/// one of its units is eligible at round 0 of a height, 1 - (1 - E / 2^16)^n.
#[derive(Clone, Debug, PartialEq)]
pub struct Share {
    pub units: u64,
    /// bond / total, as a fraction
    pub share: f64,
    /// the chance of round-0 eligibility at a height
    pub round0_chance: f64,
    /// the expected number of heights per epoch (of `epoch_blocks`) at which the key is eligible at round 0
    pub round0_heights_per_epoch: f64,
}

pub fn share(bond: &U256, total: &U256, expected: u64, epoch_blocks: u64) -> Share {
    let n = units(bond, total);
    let share = if total.is_zero() { 0.0 } else { bond.to_f64() / total.to_f64() };
    let p_unit = (expected as f64) / (UNITS as f64);
    let round0 = 1.0 - (1.0 - p_unit).powi(n as i32);
    Share { units: n, share, round0_chance: round0, round0_heights_per_epoch: round0 * epoch_blocks as f64 }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn units_follow_the_node() {
        let ign = |n: u64| U256::from_u64(n).checked_mul_u64(1_000_000_000).unwrap().checked_mul_u64(1_000_000_000).unwrap();
        // devnet4 at 11 October 2026: a 1000 IGN bond of a 2000 IGN total is 32,768 units (the node's reading)
        assert_eq!(units(&ign(1000), &ign(2000)), 32_768);
        assert_eq!(units(&ign(2000), &ign(2000)), UNITS);
        assert_eq!(units(&ign(1), &ign(2001)), 32);
        assert_eq!(units(&U256::ZERO, &ign(2000)), 0);
        assert_eq!(units(&ign(5), &U256::ZERO), 0);
        // a bond under one unit draws nothing
        assert_eq!(units(&U256::from_u64(1), &ign(2000)), 0);
    }

    #[test]
    fn theta_is_the_node_s() {
        // E = 1, r = 0: 1 << 240
        let t = theta(1, 0).unwrap();
        let mut want = [0u8; 32];
        want[1] = 1;
        assert_eq!(t, U256(want));
        // E = 1, r = 15: 2^15 << 240 = 0x8000 << 240
        let t = theta(1, 15).unwrap();
        let mut want = [0u8; 32];
        want[0] = 0x80;
        assert_eq!(t, U256(want));
        // r = 16: every unit
        assert!(theta(1, 16).is_none());
        assert!(theta(4, 14).is_none());
        assert!(theta(4, 13).is_some());
    }

    #[test]
    fn eligible_round_is_monotone_in_units_and_bounded() {
        let p = Params { expected: 1, max_round: 16, unbonded_round: 17, delta_secs: 6 };
        let seed = [0x11u8; 32];
        let key = [0x4bu8; 32];
        assert_eq!(eligible_round(&p, &seed, &key, 0, 100), 17);
        // with every unit the key is eligible by round 16 at the latest
        for h in 100..110 {
            let r = eligible_round(&p, &seed, &key, UNITS, h);
            assert!(r <= 16);
            // more units never give a later round
            assert!(eligible_round(&p, &seed, &key, 32_768, h) >= r);
        }
    }

    #[test]
    fn share_words() {
        let ign = |n: u64| U256::from_u64(n).checked_mul_u64(1_000_000_000).unwrap().checked_mul_u64(1_000_000_000).unwrap();
        let s = share(&ign(1000), &ign(2000), 1, 100);
        assert_eq!(s.units, 32_768);
        assert!((s.share - 0.5).abs() < 1e-12);
        // 1 - (1 - 2^-16)^32768 = 1 - e^(-0.5) about 0.3935
        assert!((s.round0_chance - 0.3935).abs() < 0.001, "{}", s.round0_chance);
        assert!((s.round0_heights_per_epoch - 39.35).abs() < 0.1);
    }
}

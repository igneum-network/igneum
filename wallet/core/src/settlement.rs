//! The settlement-confidence model (docs/product-b/DESIGN.md, section 2). A transaction is submitted, included, or
//! settled by a policy the person or the application chose; never "final". The inputs are measured by the engine:
//! depth, work on top as minutes of the network's recent average work, the deepest reorganisation seen, head age.
//! The default policy is a wallet policy with its bands on screen, not a network rule.

use serde::{Deserialize, Serialize};

use crate::{display, U256};

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(tag = "state", rename_all = "snake_case")]
pub enum State {
    /// the node holds it in its pool; no block carries it
    Submitted { sent_unix: u64 },
    /// a block at `block` carries it and the execution succeeded
    Included { block: u64 },
    /// a block carries it and the execution failed; the fee was still paid
    Reverted { block: u64 },
    /// the pool no longer carries it and no block does
    Dropped { sent_unix: u64 },
}

/// What the engine measured itself.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Observations {
    pub head: u64,
    pub head_unix: u64,
    pub now_unix: u64,
    /// the sum of difficulty after the including block, as minutes of the recent average rate
    pub work_minutes_on_top: f64,
    pub deepest_reorg_24h: u64,
    pub deepest_reorg_7d: u64,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Band {
    /// amounts up to and including this many wei; None for the open top band
    pub up_to: Option<U256>,
    pub blocks: u64,
    pub work_minutes: f64,
    pub note: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Policy {
    pub name: String,
    pub source: String,
    pub bands: Vec<Band>,
}

impl Policy {
    /// The wallet's default bands (DESIGN.md 2.3): a starting table for a new network, replaced by the published
    /// analysis when it lands; the source line on screen names no internal role (the project's audit, 15 October 2026).
    pub fn default_wallet() -> Self {
        let ign = |n: u64| U256::pow10(18).unwrap().checked_mul_u64(n).unwrap();
        Policy {
            name: "wallet default".into(),
            source: "A starting table for a new network".into(),
            bands: vec![
                Band { up_to: Some(ign(10)), blocks: 6, work_minutes: 2.0, note: None },
                Band { up_to: Some(ign(1_000)), blocks: 30, work_minutes: 8.0, note: None },
                Band { up_to: Some(ign(100_000)), blocks: 120, work_minutes: 30.0, note: None },
                Band {
                    up_to: None,
                    blocks: 480,
                    work_minutes: 120.0,
                    note: Some("ask the receiver what they require".into()),
                },
            ],
        }
    }

    pub fn band_for(&self, value: &U256) -> &Band {
        for b in &self.bands {
            match &b.up_to {
                Some(top) if value <= top => return b,
                None => return b,
                _ => {}
            }
        }
        self.bands.last().expect("a policy has at least one band")
    }
}

/// The words the window shows for one transaction.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct View {
    /// sent, included, settled, failed, dropped
    pub word: String,
    pub why: String,
    pub depth: Option<u64>,
    /// Some(true) when the policy's band is met, Some(false) when not yet, None when it does not apply
    pub met: Option<bool>,
    pub band_blocks: Option<u64>,
    pub band_work_minutes: Option<f64>,
}

fn ago(secs: u64) -> String {
    if secs < 60 {
        format!("{secs} s ago")
    } else if secs < 3600 {
        format!("{} min ago", secs / 60)
    } else {
        format!("{} h ago", secs / 3600)
    }
}

pub fn view(state: &State, value: &U256, obs: &Observations, policy: &Policy) -> View {
    match state {
        State::Submitted { sent_unix } => View {
            word: "sent".into(),
            why: format!("waiting for a block; sent {}", ago(obs.now_unix.saturating_sub(*sent_unix))),
            depth: None,
            met: None,
            band_blocks: None,
            band_work_minutes: None,
        },
        State::Dropped { sent_unix } => View {
            word: "dropped".into(),
            why: format!(
                "not in a block and no longer in the pool, sent {}; the fee was too low or it was replaced",
                ago(obs.now_unix.saturating_sub(*sent_unix))
            ),
            depth: None,
            met: None,
            band_blocks: None,
            band_work_minutes: None,
        },
        State::Reverted { block } => {
            let depth = obs.head.saturating_sub(*block) + 1;
            View {
                word: "failed".into(),
                why: format!("in block {block}, {depth} deep; the execution failed and the fee was still paid"),
                depth: Some(depth),
                met: None,
                band_blocks: None,
                band_work_minutes: None,
            }
        }
        State::Included { block } => {
            let depth = if obs.head >= *block { obs.head - block + 1 } else { 0 };
            let band = policy.band_for(value);
            let reorg_block = obs.deepest_reorg_7d >= band.blocks;
            let met = depth >= band.blocks && obs.work_minutes_on_top >= band.work_minutes && !reorg_block;
            let head_note = if obs.now_unix.saturating_sub(obs.head_unix) > 120 {
                format!("; the node's head is {} old", ago(obs.now_unix.saturating_sub(obs.head_unix)))
            } else {
                String::new()
            };
            if met {
                View {
                    word: "settled".into(),
                    why: format!(
                        "in block {block}, {depth} blocks deep with {:.0} minutes of network work on top; meets the {} policy for {} IGN (at least {} blocks and {:.0} minutes); no reorganisation deeper than {} seen in 7 days{}",
                        obs.work_minutes_on_top,
                        policy.name,
                        display::ign(value, 4),
                        band.blocks,
                        band.work_minutes,
                        obs.deepest_reorg_7d,
                        band.note.as_ref().map(|n| format!("; {n}")).unwrap_or_default()
                    ),
                    depth: Some(depth),
                    met: Some(true),
                    band_blocks: Some(band.blocks),
                    band_work_minutes: Some(band.work_minutes),
                }
            } else {
                let mut why = format!(
                    "in block {block}, {depth} of {} blocks for {} IGN, {:.0} of {:.0} minutes of network work since it (the minutes only rise)",
                    band.blocks,
                    display::ign(value, 4),
                    // a hair below zero in the first minute printed "-0" (the study's first participant, 12 October 2026)
                    obs.work_minutes_on_top.max(0.0),
                    band.work_minutes
                );
                if reorg_block {
                    why.push_str(&format!(
                        "; a reorganisation {} deep was seen in the last 7 days, deeper than this band allows",
                        obs.deepest_reorg_7d
                    ));
                }
                why.push_str(&head_note);
                View {
                    word: "included".into(),
                    why,
                    depth: Some(depth),
                    met: Some(false),
                    band_blocks: Some(band.blocks),
                    band_work_minutes: Some(band.work_minutes),
                }
            }
        }
    }
}

/// Minutes of network work represented by the blocks after the including block: the sum of their difficulties
/// over the recent average difficulty per second. A slow stretch counts as less security than it looks.
pub fn work_minutes(difficulties_after: &[U256], recent_difficulty_per_second: f64) -> f64 {
    if recent_difficulty_per_second <= 0.0 {
        return 0.0;
    }
    let sum: f64 = difficulties_after.iter().map(|d| d.to_f64()).sum();
    sum / recent_difficulty_per_second / 60.0
}

/// The recent average difficulty per second from a window of (difficulty, timestamp) pairs in chain order.
pub fn recent_rate(window: &[(U256, u64)]) -> f64 {
    if window.len() < 2 {
        return 0.0;
    }
    let span = window.last().unwrap().1.saturating_sub(window[0].1);
    if span == 0 {
        return 0.0;
    }
    let sum: f64 = window.iter().skip(1).map(|(d, _)| d.to_f64()).sum();
    sum / span as f64
}

#[cfg(test)]
mod tests {
    use super::*;

    fn obs(head: u64, work: f64, reorg: u64) -> Observations {
        Observations {
            head,
            head_unix: 1_000_000,
            now_unix: 1_000_010,
            work_minutes_on_top: work,
            deepest_reorg_24h: reorg,
            deepest_reorg_7d: reorg,
        }
    }

    fn ign(n: u64) -> U256 {
        U256::pow10(18).unwrap().checked_mul_u64(n).unwrap()
    }

    #[test]
    fn bands_by_value() {
        let p = Policy::default_wallet();
        assert_eq!(p.band_for(&ign(1)).blocks, 6);
        assert_eq!(p.band_for(&ign(10)).blocks, 6);
        assert_eq!(p.band_for(&ign(11)).blocks, 30);
        assert_eq!(p.band_for(&ign(1_000)).blocks, 30);
        assert_eq!(p.band_for(&ign(50_000)).blocks, 120);
        assert_eq!(p.band_for(&ign(1_000_000)).blocks, 480);
    }

    #[test]
    fn words_never_say_final() {
        let p = Policy::default_wallet();
        let v = view(&State::Submitted { sent_unix: 999_970 }, &ign(1), &obs(100, 0.0, 0), &p);
        assert_eq!(v.word, "sent");
        assert_eq!(v.why, "waiting for a block; sent 40 s ago");
        let v = view(&State::Included { block: 98 }, &ign(1), &obs(100, 1.0, 0), &p);
        assert_eq!(v.word, "included");
        assert_eq!(v.depth, Some(3));
        assert_eq!(v.met, Some(false));
        assert!(v.why.starts_with("in block 98, 3 of 6 blocks for 1 IGN, 1 of 2 minutes of network work since it"));
        let v = view(&State::Included { block: 90 }, &ign(1), &obs(100, 3.0, 0), &p);
        assert_eq!(v.word, "settled");
        assert_eq!(v.met, Some(true));
        assert!(v.why.contains("meets the wallet default policy for 1 IGN"));
        assert!(!v.why.to_lowercase().contains("final"));
        // a deep reorg seen in the week un-meets the band
        let v = view(&State::Included { block: 90 }, &ign(1), &obs(100, 3.0, 6), &p);
        assert_eq!(v.word, "included");
        assert!(v.why.contains("reorganisation 6 deep"));
        let v = view(&State::Reverted { block: 99 }, &ign(1), &obs(100, 0.0, 0), &p);
        assert_eq!(v.word, "failed");
        let v = view(&State::Dropped { sent_unix: 990_000 }, &ign(1), &obs(100, 0.0, 0), &p);
        assert_eq!(v.word, "dropped");
        // the top band carries its note
        let v = view(&State::Included { block: 1 }, &ign(1_000_000), &obs(1_000, 200.0, 0), &p);
        assert!(v.why.contains("ask the receiver what they require"));
    }

    #[test]
    fn work_arithmetic() {
        // 4 blocks of difficulty 60 at 1 difficulty per second: 4 minutes
        let d = vec![U256::from_u64(60); 4];
        assert!((work_minutes(&d, 1.0) - 4.0).abs() < 1e-9);
        assert_eq!(work_minutes(&d, 0.0), 0.0);
        let window = vec![(U256::from_u64(15), 0), (U256::from_u64(15), 15), (U256::from_u64(15), 30)];
        assert!((recent_rate(&window) - 1.0).abs() < 1e-9);
        assert_eq!(recent_rate(&window[..1]), 0.0);
    }
}

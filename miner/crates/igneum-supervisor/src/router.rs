//! Ember's router for VectorWork (docs/product-a/ROUTER.md, 13 October 2026): per card, the coordinate with the
//! greatest marginal reward from the chain state the job carries (each coordinate's difficulty and the chain's
//! subsidy), with hysteresis, one move per window, a probe of every coordinate's rate per card before the first
//! choice, and the restoration of an emptied coordinate within the registered bound. Pure: nothing here reads a
//! clock, a socket or a card; the supervisor feeds it the job's coordinate table, the slots' rates and the heights,
//! and acts on the assignments it returns. Every constant is a first value (ROUTER.md section 9), set by the rows.

use std::collections::BTreeMap;

/// The margin another coordinate's reward must exceed a card's own by, for two consecutive reads, before the card
/// moves (the cost of a switch expressed as the gain that pays for it).
pub const HYSTERESIS: f64 = 0.08;
/// The window, in blocks of the chain, between scheduled re-reads.
pub const WINDOW_BLOCKS: u64 = 16;
/// A coordinate's difficulty moving by more than this since the last decision re-reads at once.
pub const DIFFICULTY_STEP: f64 = 0.25;
/// A coordinate whose last block is more than this many blocks behind the head is EMPTY and is restored.
pub const EMPTY_BLOCKS: u64 = 12;
/// The probe window per coordinate per card, in seconds of mining, before a rate exists for it.
pub const PROBE_S: u64 = 45;
/// A stint shorter than the probe still leaves a rate the router may keep when the card moves off the lane: this many
/// seconds of mining with a rate read (the worker's stats carry hashes within a few seconds of its launch; a stint cut
/// short by a restoration or a price read at 41 s read as "no rate" before 16 October 2026 and the lane was probed again).
pub const KNOWN_AFTER_S: u64 = 15;
/// Whether a stint's rate is kept at the move off its lane (pure, for the test).
pub fn rate_known_at_move(stint_s: u64, rate: f64) -> bool {
    stint_s >= KNOWN_AFTER_S && rate > 0.0
}
/// F-E59 (the GPU team's row of 15 October 2026, 09:03 UK: the restore rule moved a card 6 to 0 to 2 faster than its worker
/// could compile each version and build its dataset, so it mined nothing for ten minutes): a card that has produced no
/// result yet on its coordinate is not moved again, by a restoration or a window read, until SETTLE_S have passed since
/// the move (a compile and a dataset build fit inside); a probe's end and a refused coordinate still move it.
pub const SETTLE_S: u64 = 180;
/// The settle's bound while the worker is still opening or preparing (a compile and a dataset build on a cold cache);
/// past it the router moves the card whatever the slot says, so a stuck worker never pins a card.
pub const SETTLE_MAX_S: u64 = 600;

/// The card's state as the settle reads it (the project's order of 15 October 2026, 10:0x UK: one invariant over the card's
/// states, with the table of which events may move the card in each; the three misses of the morning, the
/// restoration, the window read and the probe, were each one row of this table found on a pod).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CardState {
    /// the worker process is starting (no hello yet)
    Opening,
    /// the worker compiles its programme or builds its dataset (hello in, not ready)
    Preparing,
    /// ready on a probe (PROBE_S), no result yet
    Probing,
    /// ready on a chosen coordinate, no result yet
    Ready,
    /// results flow on the coordinate (a probe's or a stint's)
    Mining,
    /// the coordinate's document refused for this card (by the reference, the worker's configure or the source)
    Refused,
}

/// What asks the router to move a card.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MoveEvent {
    /// an emptied coordinate's restoration (the fleet order, ROUTER.md section 6)
    Restoration,
    /// the card's own scheduled window read
    WindowRead,
    /// a fleet signal read (the difficulty step, a new coordinate)
    SignalRead,
    /// the probe's end (its rate learned)
    ProbeEnd,
    /// a template or the tick with no read due (an unrouted card's first choice)
    FirstChoice,
    /// the coordinate refused for this card
    CoordinateRefused,
    /// the pin names another coordinate
    Pin,
}

/// THE SETTLE INVARIANT: whether `event` may move a card in `state`, `since_s` seconds after its last move.
///
/// | state     | Refused | Pin | Restoration | WindowRead | SignalRead | ProbeEnd | FirstChoice |
/// |-----------|---------|-----|-------------|------------|------------|----------|-------------|
/// | Opening   | yes     | yes | SETTLE_MAX  | SETTLE_MAX | SETTLE_MAX | no       | SETTLE_MAX  |
/// | Preparing | yes     | yes | SETTLE_MAX  | SETTLE_MAX | SETTLE_MAX | no       | SETTLE_MAX  |
/// | Probing   | yes     | yes | SETTLE      | SETTLE     | SETTLE     | yes      | SETTLE      |
/// | Ready     | yes     | yes | SETTLE      | SETTLE     | SETTLE     | no       | SETTLE      |
/// | Mining    | yes     | yes | yes         | yes        | yes        | yes      | yes         |
/// | Refused   | yes     | yes | yes         | yes        | yes        | yes      | yes         |
///
/// "SETTLE" = only once `since_s` reaches SETTLE_S; "SETTLE_MAX" = only once it reaches SETTLE_MAX_S (a stuck worker
/// never pins a card); a probe's end moves a probing card the moment its rate is learned and never an unready one.
pub fn settle_allows(state: CardState, event: MoveEvent, since_s: u64) -> bool {
    settle_allows_mined(state, event, since_s, since_s)
}

/// The same with the time since the card's FIRST RESULT on its coordinate (`mined_s`; 0 before one): a restoration
/// takes only a card that has mined SETTLE_S on its coordinate (the GPU team's row of 15 October 2026, 10:24 UK: the settle
/// released on the first result and the restore rule took the card to an always-empty coordinate before any share).
pub fn settle_allows_mined(state: CardState, event: MoveEvent, since_s: u64, mined_s: u64) -> bool {
    use CardState::*;
    use MoveEvent::*;
    match (state, event) {
        (_, CoordinateRefused) | (_, Pin) => true,
        (Mining, Restoration) => mined_s >= SETTLE_S,
        (Mining, _) | (Refused, _) => true,
        (Opening, ProbeEnd) | (Preparing, ProbeEnd) | (Ready, ProbeEnd) => false,
        (Probing, ProbeEnd) => true,
        (Opening, _) | (Preparing, _) => since_s >= SETTLE_MAX_S,
        (Probing, _) | (Ready, _) => since_s >= SETTLE_S,
    }
}

#[cfg(test)]
mod settle_tests {
    use super::*;
    use CardState::*;
    use MoveEvent::*;

    /// Every row of the table, every column: the three misses of 15 October 2026 are rows here.
    #[test]
    fn a_lane_reads_empty_after_its_own_intervals_not_the_chains() {
        // three lanes at a 12 s chain: a lane sealing every 36 s is behind the head by three blocks between its own
        assert_eq!(empty_bound(3), EMPTY_BLOCKS * 3);
        assert_eq!(empty_bound(1), EMPTY_BLOCKS);
        assert_eq!(empty_bound(0), EMPTY_BLOCKS);
        let mut t = BTreeMap::new();
        for (c, h) in [(5u8, 980u64), (6, 1000), (7, 1000)] {
            t.insert(c, CoordState { coord: c, digest: "d".into(), difficulty: 1.0, last_height: Some(h), lane_worth: None, lane_worth_unknown: false, lane_workers: None });
        }
        assert!(empty_coords(&t, 1000).is_empty(), "twenty chain blocks behind on a three-lane chain is seven of its own intervals: not empty");
        assert_eq!(empty_coords(&t, 1017), vec![5], "past twelve of its own intervals it is");
    }

    #[test]
    fn the_settle_table_walked_row_by_row() {
        let moves = [Restoration, WindowRead, SignalRead, FirstChoice];
        for st in [Opening, Preparing] {
            for ev in moves {
                assert!(!settle_allows(st, ev, 0), "{st:?} {ev:?} at 0");
                assert!(!settle_allows(st, ev, SETTLE_S), "{st:?} {ev:?} at SETTLE_S (the worker is still not ready)");
                assert!(!settle_allows(st, ev, SETTLE_MAX_S - 1), "{st:?} {ev:?} under SETTLE_MAX_S");
                assert!(settle_allows(st, ev, SETTLE_MAX_S), "{st:?} {ev:?} at SETTLE_MAX_S (a stuck worker never pins a card)");
            }
            assert!(!settle_allows(st, ProbeEnd, 10_000), "{st:?}: no probe ends before the worker is ready");
            assert!(settle_allows(st, CoordinateRefused, 0) && settle_allows(st, Pin, 0));
        }
        for st in [Probing, Ready] {
            for ev in moves {
                assert!(!settle_allows(st, ev, 7), "{st:?} {ev:?} seven seconds in (the 09:36 miss: a probing card moved by a restoration)");
                assert!(!settle_allows(st, ev, SETTLE_S - 1), "{st:?} {ev:?} under SETTLE_S");
                assert!(settle_allows(st, ev, SETTLE_S), "{st:?} {ev:?} at SETTLE_S");
            }
            assert!(settle_allows(st, CoordinateRefused, 0) && settle_allows(st, Pin, 0));
        }
        assert!(settle_allows(Probing, ProbeEnd, 0), "a probe's end moves a probing card at once");
        assert!(!settle_allows(Ready, ProbeEnd, 10_000), "a card not probing has no probe to end");
        for st in [Mining, Refused] {
            for ev in [WindowRead, SignalRead, ProbeEnd, FirstChoice, CoordinateRefused, Pin] {
                assert!(settle_allows(st, ev, 0), "{st:?} {ev:?}: a card with results, or refused, moves on any event");
            }
        }
        assert!(settle_allows(Refused, Restoration, 0));
        // a restoration takes only a card that has mined SETTLE_S on its coordinate (10:24: the first result released the
        // settle and the restore rule took the card before any share)
        assert!(!settle_allows_mined(Mining, Restoration, 300, 1), "mining one second: no restoration yet");
        assert!(!settle_allows_mined(Mining, Restoration, 300, SETTLE_S - 1));
        assert!(settle_allows_mined(Mining, Restoration, 300, SETTLE_S));
        assert!(settle_allows_mined(Mining, WindowRead, 300, 1), "a window read moves a mining card as before");
        // the morning's three misses by name
        assert!(!settle_allows(Preparing, Restoration, 7), "09:03: the restore rule moved a card mid-compile");
        assert!(!settle_allows(Ready, WindowRead, 60), "09:14: the window read moved a card before its first result");
        assert!(!settle_allows(Probing, Restoration, 7), "09:36: the restore rule moved a probing card");
    }
}
/// the outside review's audit (main, 01:4x UK 13 October 2026): thousands of identical routers must not switch together.
/// Each card's scheduled read is offset inside the window by a value seeded from the card and the address
/// (`offset_blocks`), the difficulty view is damped (the decision reads a mean of the last reads), a switch pays
/// its cost (the kernel warm-up and the share in flight) in the marginal-reward comparison, the switches per
/// window are capped, and a pool's advertised reward that disagrees with the chain state's by more than the bound
/// is refused (the chain state governs).
pub const DAMPING_READS: usize = 3;
/// The switch's cost in seconds of the card's current reward (the kernel warm-up on a return to a cached document,
/// plus the share in flight); a move pays only when its gain over the rest of the window exceeds it.
pub const SWITCH_COST_S: f64 = 30.0;
/// The chain's block interval the window is counted in, for the cost comparison.
pub const BLOCK_S: f64 = 20.0;
pub const MAX_SWITCHES_PER_WINDOW: u8 = 1;
/// A pool's `reward_wei` more than this fraction away from subsidy / difficulty is refused as advertised.
pub const REWARD_BOUND: f64 = 0.10;

/// The per-card offset inside the window (0..WINDOW_BLOCKS) from the card's fingerprint and the payout address: a
/// stable hash, no clock, no randomness at run time, the same on every machine for the same card and address.
pub fn offset_blocks(fingerprint: &str, address: &str) -> u64 {
    let mut h: u64 = 0xcbf2_9ce4_8422_2325;
    for b in fingerprint.bytes().chain(address.bytes()) {
        h ^= b as u64;
        h = h.wrapping_mul(0x0100_0000_01b3);
    }
    h % WINDOW_BLOCKS
}

/// The damped difficulty view: the mean of the last DAMPING_READS difficulties per coordinate, so one delayed or
/// stale view of the chain does not move the fleet; `history` holds the reads newest last.
pub fn damped(history: &[BTreeMap<u8, CoordState>]) -> BTreeMap<u8, CoordState> {
    let n = history.len().min(DAMPING_READS);
    let last = &history[history.len() - 1];
    last.iter().map(|(c, st)| {
        let vals: Vec<f64> = history[history.len() - n..].iter().filter_map(|t| t.get(c)).map(|t| t.difficulty).collect();
        let mean = vals.iter().sum::<f64>() / vals.len().max(1) as f64;
        (*c, CoordState { difficulty: mean, ..st.clone() })
    }).collect()
}

/// A pool's advertised reward per block for a coordinate against the chain state's (subsidy, difficulty): the
/// advertised value is taken only inside the bound; else refused and the chain's used.
pub fn advertised_reward_ok(advertised_ign: f64, subsidy: f64, difficulty: f64, rate: f64) -> bool {
    let chain = reward(subsidy, rate, difficulty);
    if chain <= 0.0 {
        return false;
    }
    ((advertised_ign * rate / difficulty.max(1.0)) - chain).abs() / chain <= REWARD_BOUND
}

/// One coordinate's state as the job carries it (ROUTER.md section 2).
#[derive(Debug, Clone, PartialEq)]
pub struct CoordState {
    pub coord: u8,
    /// The relation's document digest (the params document the coordinate's jobs carry).
    pub digest: String,
    /// D_c(next): the difficulty the coordinate's next block must meet.
    pub difficulty: f64,
    /// The height of the coordinate's last block, when the pool states it.
    pub last_height: Option<u64>,
    /// F-496: the pool's worth of this lane's share weight against the chain's pay (one window behind), when sent.
    pub lane_worth: Option<f64>,
    /// F-496: the pool sent the worth as "" (unknown): the lane reads as uncovered until a number comes.
    pub lane_worth_unknown: bool,
    /// F-543: the pool's workers with a sealer-accepted share on this lane in the last 64 blocks, when sent.
    pub lane_workers: Option<u64>,
}

impl CoordState {
    /// F-543: a paid lane no worker of the pool covers (the pool says 0 workers, or sends an unknown worth and no
    /// count): under the chain's minimum rule the pool's pay is the minimum over its lanes, so an uncovered lane
    /// pays nothing to every member whatever the others mine. Coverage comes before any reward choice.
    pub fn uncovered(&self) -> bool {
        match self.lane_workers {
            Some(n) => n == 0,
            None => self.lane_worth_unknown,
        }
    }
    /// F-543: a card on this lane is its only cover (the pool counts one worker, or none yet while the card's first
    /// shares seal): it holds there; a move for reward would uncover the lane again.
    pub fn held_by_one(&self) -> bool {
        match self.lane_workers {
            Some(n) => n <= 1,
            None => self.lane_worth_unknown,
        }
    }
}

/// A card's standing with the router.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct CardRoute {
    /// The coordinate the card mines now; None before the first choice.
    pub coord: Option<u8>,
    /// Measured rate per coordinate (results per second over the probe or the mining so far).
    pub rates: BTreeMap<u8, f64>,
    /// Coordinates the card's worker cannot run (refused documents).
    pub refused: Vec<u8>,
    /// The number of consecutive reads a better coordinate beat the current one by the margin.
    pub better_reads: u8,
    /// The height of the card's last move (one move per window).
    pub moved_at: u64,
    /// True while the card is on a probe (its rate for the coordinate not yet known).
    pub probing: bool,
    /// The card's offset inside the window (offset_blocks), the switches made in the current window, and the index
    /// of the last window this card took its scheduled read in.
    pub offset: u64,
    pub switches_this_window: u8,
    pub window_start: u64,
    pub last_window_read: u64,
}

/// What the router decided for one card and why.
#[derive(Debug, Clone, PartialEq)]
pub struct Decision {
    pub coord: u8,
    pub reason: String,
    /// true: a probe window, not a reward choice.
    pub probe: bool,
}

/// The reward rate of a card on a coordinate: subsidy x rate / difficulty (IGN per second, fees ignored). A zero
/// difficulty is treated as the floor 1.
pub fn reward(subsidy: f64, rate: f64, difficulty: f64) -> f64 {
    subsidy * rate / difficulty.max(1.0)
}

/// F-496: the value of a card on a coordinate for the router: the reward scaled by the pool's lane worth when the
/// pool sent one (the lane's share weight against the chain's pay, one window behind; the pool's accounting, not the
/// chain's difficulty alone, decides what a share on the lane is worth to the key); the reward alone when none.
pub fn lane_value(subsidy: f64, rate: f64, c: &CoordState) -> f64 {
    reward(subsidy, rate, c.difficulty) * c.lane_worth.unwrap_or(1.0)
}

/// The worths rank the candidates only when every lane in the table carries one (the security review's read of 3bb52f9f: the
/// pool omits the worth of a lane the last window carried no work on, and a worth is income over share weight, a scale
/// of its own, so one missing worth beside the others would undervalue that lane by the whole scale); else the reward
/// alone ranks, as before the worths.
pub fn worths_rank(table: &BTreeMap<u8, CoordState>) -> bool {
    !table.is_empty() && table.values().all(|c| c.lane_worth.is_some())
}

/// The value the router ranks by: `lane_value` when the worths rank, the reward alone when they do not.
pub fn rank_value(subsidy: f64, rate: f64, c: &CoordState, worths: bool) -> f64 {
    if worths { lane_value(subsidy, rate, c) } else { reward(subsidy, rate, c.difficulty) }
}

/// F-469 (the Linux proof host's one-card row of 0.2.24, 17 October 2026: the router chose the raster lane by the
/// reward signal alone, the GL worker's attempts counted as hashes against a low lane difficulty, while the pool's
/// share target decided the real share rate, so the card earned little): the accepted shares a lane needs before its
/// measured share rate replaces the stint meter's hash rate in the reward.
pub const MEASURED_SHARES_MIN: u64 = 3;

/// F-469: the expected hashes a share at `target` (big-endian, 32 bytes) costs: 2^256 over the target, so a share's
/// weight is the work the pool's step asks for it; a target of all ones weighs one hash, a zero target the maximum.
pub fn share_weight(target: &[u8; 32]) -> f64 {
    let mut t = 0.0f64;
    for b in target {
        t = t * 256.0 + *b as f64;
    }
    if t < 1.0 {
        return 2f64.powi(256);
    }
    2f64.powi(256) / t
}

/// F-469: the rate the reward reads for a lane: the measured share rate (the accepted shares' weights over the
/// stint's seconds) once the stint has run SETTLE_S past the step's last rise with MEASURED_SHARES_MIN shares; the
/// stint meter's hash rate (the probe's) until then.
pub fn lane_rate(hash_rate: f64, work: f64, shares: u64, stint_s: u64, target_stood_s: u64) -> f64 {
    if shares >= MEASURED_SHARES_MIN && stint_s >= SETTLE_S && target_stood_s >= SETTLE_S && stint_s > 0 {
        work / stint_s as f64
    } else {
        hash_rate
    }
}

/// The coordinates a card can mine: in the table, not refused.
fn mineable<'a>(table: &'a BTreeMap<u8, CoordState>, card: &CardRoute) -> impl Iterator<Item = &'a CoordState> {
    let refused = card.refused.clone();
    table.values().filter(move |c| !refused.contains(&c.coord))
}

/// The coordinates emptied at this head (ROUTER.md section 6): no block of its own for EMPTY_BLOCKS of the LANE'S expected
/// intervals, which on a chain of N lanes is EMPTY_BLOCKS times N chain blocks (the mini's one card, 16 October 2026: with
/// the chain at 12 s and three lanes sealing about every 36 s, twelve chain blocks read every thin lane as empty and the
/// router bounced the card between lanes every few minutes, a worker relaunch each time).
pub fn empty_coords(table: &BTreeMap<u8, CoordState>, head: u64) -> Vec<u8> {
    let bound = empty_bound(table.len());
    table.values().filter(|c| c.last_height.map_or(false, |h| head.saturating_sub(h) > bound)).map(|c| c.coord).collect()
}

/// The chain blocks a lane may go without a block before it reads as empty: EMPTY_BLOCKS of its own expected intervals.
pub fn empty_bound(lanes: usize) -> u64 {
    EMPTY_BLOCKS * (lanes.max(1) as u64)
}

/// Whether a scheduled or signalled read is due: the window elapsed since the last decision height, or any
/// coordinate's difficulty moved by more than the step since the table of the last decision.
pub fn read_due(head: u64, last_decision_height: u64, table: &BTreeMap<u8, CoordState>, last_table: &BTreeMap<u8, CoordState>) -> Option<&'static str> {
    if head >= last_decision_height + WINDOW_BLOCKS {
        return Some("the window");
    }
    read_due_signal(table, last_table)
}

/// The per-card window read: the card reads once per window, at the first tick whose head has passed its own
/// offset block inside that window, never with every other card (the soak's first window, 05:4x UK 13 October
/// 2026: the earlier form compared against a window start the same tick had just moved, and no card ever read).
pub fn card_window_due(head: u64, card: &CardRoute) -> bool {
    let window = head / WINDOW_BLOCKS;
    window > card.last_window_read && (head % WINDOW_BLOCKS) >= card.offset
}

fn read_due_signal(table: &BTreeMap<u8, CoordState>, last_table: &BTreeMap<u8, CoordState>) -> Option<&'static str> {
    for c in table.values() {
        if let Some(p) = last_table.get(&c.coord) {
            let moved = (c.difficulty - p.difficulty).abs() / p.difficulty.max(1.0);
            if moved > DIFFICULTY_STEP {
                return Some("the difficulty signal");
            }
        } else {
            return Some("a new coordinate");
        }
    }
    None
}

/// The decision for one card at one read. `restore_to` names the emptied coordinate this card is assigned by the
/// fleet order (ROUTER.md section 6), when any; it overrides the reward choice.
pub fn decide(card: &CardRoute, table: &BTreeMap<u8, CoordState>, subsidy: f64, head: u64, restore_to: Option<u8>) -> Option<Decision> {
    let mut candidates: Vec<&CoordState> = mineable(table, card).collect();
    if candidates.is_empty() {
        return None;
    }
    candidates.sort_by_key(|c| c.coord);
    // the restoration of an emptied coordinate comes before any price
    if let Some(r) = restore_to {
        if let Some(t) = candidates.iter().find(|c| c.coord == r) {
            if card.coord != Some(r) {
                let reason = if t.uncovered() {
                    format!("coordinate {r} has no worker of the pool on it (lane_workers {}); this card covers it before any reward choice (F-543)", t.lane_workers.map_or("unknown".to_string(), |n| n.to_string()))
                } else {
                    format!("coordinate {r} is empty (no block for more than {EMPTY_BLOCKS} blocks); this card restores it")
                };
                return Some(Decision { coord: r, reason, probe: false });
            }
        }
    }
    // F-543: a lane's only cover probes nothing and never leaves it for a reward (the pool's pay is the minimum over
    // its lanes; a probe elsewhere would uncover the lane for PROBE_S and the pool's count would still read it covered)
    if let Some(cur) = card.coord {
        if !card.probing && table.get(&cur).map_or(false, |c| c.held_by_one()) {
            return None;
        }
    }
    // a coordinate with no rate yet is probed first, in coordinate order
    if let Some(c) = candidates.iter().find(|c| !card.rates.contains_key(&c.coord)) {
        if card.coord != Some(c.coord) || !card.probing {
            return Some(Decision { coord: c.coord, reason: format!("probing coordinate {} for {PROBE_S} s to learn this card's rate on it", c.coord), probe: true });
        }
        return None;
    }
    // the greatest marginal value (F-496: the reward by the pool's lane worth when every lane carries one)
    let worths = worths_rank(table);
    let best = candidates.iter().map(|c| (c.coord, rank_value(subsidy, card.rates[&c.coord], c, worths))).fold(None, |acc: Option<(u8, f64)>, x| match acc { Some(a) if a.1 >= x.1 => Some(a), _ => Some(x) })?;
    let Some(cur) = card.coord else {
        return Some(Decision { coord: best.0, reason: format!("the first choice: coordinate {} pays the most for this card", best.0), probe: false });
    };
    if best.0 == cur {
        return None;
    }
    let cur_reward = table.get(&cur).map(|c| rank_value(subsidy, *card.rates.get(&cur).unwrap_or(&0.0), c, worths)).unwrap_or(0.0);
    let gain = if cur_reward > 0.0 { best.1 / cur_reward - 1.0 } else { f64::INFINITY };
    if gain <= HYSTERESIS {
        return None;
    }
    // the switch's cost: the gain over the rest of the window must exceed SWITCH_COST_S of the current reward
    let window_s = WINDOW_BLOCKS as f64 * BLOCK_S;
    if cur_reward > 0.0 && (best.1 - cur_reward) * window_s <= cur_reward * SWITCH_COST_S {
        return None;
    }
    // two consecutive reads over the margin, the cap on switches per window, one move per window
    if card.better_reads < 1 {
        return None;
    }
    if card.switches_this_window >= MAX_SWITCHES_PER_WINDOW && head < card.window_start + WINDOW_BLOCKS {
        return None;
    }
    if head < card.moved_at + WINDOW_BLOCKS && card.moved_at > 0 {
        return None;
    }
    Some(Decision { coord: best.0, reason: format!("coordinate {}'s reward exceeds coordinate {cur}'s by {:.0} percent over two reads", best.0, gain * 100.0), probe: false })
}

/// Whether this read counts a "better" read for the card (the hysteresis's second read).
pub fn better_this_read(card: &CardRoute, table: &BTreeMap<u8, CoordState>, subsidy: f64) -> bool {
    let Some(cur) = card.coord else { return false };
    let Some(curc) = table.get(&cur) else { return false };
    if curc.held_by_one() {
        return false;
    }
    let worths = worths_rank(table);
    let cur_reward = rank_value(subsidy, *card.rates.get(&cur).unwrap_or(&0.0), curc, worths);
    mineable(table, card).any(|c| c.coord != cur && card.rates.get(&c.coord).map_or(false, |r| rank_value(subsidy, *r, c, worths) > cur_reward * (1.0 + HYSTERESIS)))
}

/// The fleet order for restoring emptied coordinates: the cards (by fingerprint, so every machine reading the same
/// chain picks the same card) and the empty coordinates; one card per empty coordinate, the card whose reward loss
/// is smallest. Returns (card index, coordinate).
pub fn restore_plan(cards: &[(String, CardRoute)], table: &BTreeMap<u8, CoordState>, subsidy: f64, head: u64) -> Vec<(usize, u8)> {
    // F-543: the uncovered paid lanes first (coverage before reward), then the emptied ones; a lane once in the list
    let mut empty: Vec<u8> = table.values().filter(|c| c.uncovered()).map(|c| c.coord).collect();
    for e in empty_coords(table, head) {
        if !empty.contains(&e) {
            empty.push(e);
        }
    }
    let worths = worths_rank(table);
    let mut plan = Vec::new();
    let mut taken: Vec<usize> = Vec::new();
    for e in empty {
        if cards.iter().any(|(_, c)| c.coord == Some(e)) {
            continue; // someone mines it already
        }
        // the card whose reward loss is smallest; ties by fingerprint
        let mut best: Option<(usize, f64, &str)> = None;
        for (i, (fp, c)) in cards.iter().enumerate() {
            if taken.contains(&i) || c.refused.contains(&e) {
                continue;
            }
            // F-543: a lane's only cover stays (taking it would uncover its own lane)
            if c.coord.map_or(false, |cur| cur != e && table.get(&cur).map_or(false, |t| t.held_by_one())) {
                continue;
            }
            let loss = match (c.coord, c.rates.get(&e)) {
                (Some(cur), Some(re)) => {
                    let cur_reward = table.get(&cur).map(|t| rank_value(subsidy, *c.rates.get(&cur).unwrap_or(&0.0), t, worths)).unwrap_or(0.0);
                    cur_reward - rank_value(subsidy, *re, &table[&e], worths)
                }
                _ => 0.0,
            };
            let better = match best { None => true, Some((_, l, f)) => loss < l || (loss == l && fp.as_str() < f) };
            if better {
                best = Some((i, loss, fp.as_str()));
            }
        }
        if let Some((i, _, _)) = best {
            taken.push(i);
            plan.push((i, e));
        }
    }
    plan
}

#[cfg(test)]
mod tests {
    fn lane(c: u8, difficulty: f64, workers: Option<u64>, worth: Option<f64>, unknown: bool) -> super::CoordState {
        super::CoordState { coord: c, digest: format!("doc-{c}"), difficulty, last_height: Some(1_000), lane_worth: worth, lane_worth_unknown: unknown, lane_workers: workers }
    }

    #[test]
    fn an_uncovered_paid_lane_is_covered_before_any_reward_choice() {
        use super::*;
        // three lanes; lane 5 has no worker of the pool; two cards, both earning more on 6; the card that loses least covers 5
        let mut t = BTreeMap::new();
        t.insert(5, lane(5, 1e5, Some(0), Some(1.0), false));
        t.insert(6, lane(6, 1e6, Some(2), Some(1.0), false));
        t.insert(7, lane(7, 1e6, Some(1), Some(1.0), false));
        let mut a = CardRoute { coord: Some(6), ..Default::default() };
        a.rates.insert(5, 1_000.0); a.rates.insert(6, 1e7); a.rates.insert(7, 1e6);
        let mut b = CardRoute { coord: Some(6), ..Default::default() };
        b.rates.insert(5, 1_000.0); b.rates.insert(6, 1e6); b.rates.insert(7, 1e5);
        let plan = restore_plan(&[("a".into(), a.clone()), ("b".into(), b.clone())], &t, 1.0, 1_000);
        assert_eq!(plan, vec![(1, 5)], "the card with the smaller loss covers the uncovered lane");
        let d = decide(&b, &t, 1.0, 1_000, Some(5)).unwrap();
        assert_eq!(d.coord, 5);
        assert!(d.reason.contains("no worker of the pool") && d.reason.contains("F-543"), "{}", d.reason);
        // a lane someone mines already is not planned twice
        b.coord = Some(5);
        assert!(restore_plan(&[("a".into(), a.clone()), ("b".into(), b.clone())], &t, 1.0, 1_000).is_empty());
    }

    #[test]
    fn a_lanes_only_cover_holds_against_a_better_reward() {
        use super::*;
        let mut t = BTreeMap::new();
        t.insert(5, lane(5, 1e5, Some(1), Some(1.0), false));
        t.insert(6, lane(6, 1e6, Some(3), Some(1.0), false));
        let mut c = CardRoute { coord: Some(5), better_reads: 3, ..Default::default() };
        c.rates.insert(5, 1.0); c.rates.insert(6, 1e9);
        assert!(decide(&c, &t, 1.0, 10_000, None).is_none(), "the only cover of lane 5 never leaves it for lane 6's reward");
        assert!(!better_this_read(&c, &t, 1.0), "no better read counts while the card is the lane's only cover");
        // with a second worker of the pool on the lane the card is free to move
        t.get_mut(&5).unwrap().lane_workers = Some(2);
        assert_eq!(decide(&c, &t, 1.0, 10_000, None).map(|d| d.coord), Some(6));
        // a held card with no rate on another coordinate probes nothing (the security review's read of 3bb52f9f)
        t.get_mut(&5).unwrap().lane_workers = Some(1);
        let mut h = CardRoute { coord: Some(5), ..Default::default() };
        h.rates.insert(5, 1.0);
        assert!(decide(&h, &t, 1.0, 10_000, None).is_none(), "the only cover of lane 5 does not leave it to probe lane 6");
        // a card still probing the lane it covers finishes its probe (the rate is learned on the probe's end)
        let mut p = CardRoute { coord: Some(5), probing: true, ..Default::default() };
        p.rates.insert(6, 1e9);
        assert!(decide(&p, &t, 1.0, 10_000, None).is_none(), "a probe on the covered lane runs to its end");
        // the only cover is not taken for another lane's restoration either
        t.insert(7, lane(7, 1e6, Some(0), Some(1.0), false));
        t.get_mut(&5).unwrap().lane_workers = Some(1);
        assert!(restore_plan(&[("c".into(), c.clone())], &t, 1.0, 10_000).is_empty(), "the one card stays on the lane it alone covers");
    }

    #[test]
    fn a_card_restored_to_an_uncovered_lane_stays_through_a_probe_cycle_until_a_second_cover_shows() {
        use super::*;
        // F-550: three lanes, the card has a rate on none; lane 5 uncovered (0 workers), 6 and 7 covered
        let mut t = BTreeMap::new();
        t.insert(5, lane(5, 1e5, Some(0), None, false));
        t.insert(6, lane(6, 1e6, Some(2), None, false));
        t.insert(7, lane(7, 1e6, Some(2), None, false));
        let mut c = CardRoute::default();
        let plan = restore_plan(&[("c".into(), c.clone())], &t, 1.0, 1_000);
        assert_eq!(plan, vec![(0, 5)], "the unrouted card covers lane 5 first");
        let d = decide(&c, &t, 1.0, 1_000, Some(5)).unwrap();
        assert_eq!((d.coord, d.probe), (5, false));
        c.coord = Some(5);
        // the pool counts this card once its shares seal; through the reads of a whole window the card stays, no probe of 6 or 7
        for (workers, head) in [(Some(0), 1_010u64), (Some(1), 1_020), (Some(1), 1_040), (Some(1), 1_100)] {
            t.get_mut(&5).unwrap().lane_workers = workers;
            assert!(decide(&c, &t, 1.0, head, None).is_none(), "at {head} with {workers:?} workers the only cover probes nothing");
            assert!(!better_this_read(&c, &t, 1.0));
            assert!(restore_plan(&[("c".into(), c.clone())], &t, 1.0, head).is_empty());
        }
        // a second cover on lane 5: the card is free, and its first move is a probe (its own lane first, whose rate it
        // never learned while held, then the others in coordinate order)
        t.get_mut(&5).unwrap().lane_workers = Some(2);
        let d = decide(&c, &t, 1.0, 1_200, None).unwrap();
        assert!(d.probe && d.coord == 5, "{d:?}");
        c.rates.insert(5, 1.0);
        let d = decide(&c, &t, 1.0, 1_200, None).unwrap();
        assert!(d.probe && d.coord == 6, "{d:?}");
    }

    #[test]
    fn the_lane_worth_scales_the_reward_and_an_unknown_worth_reads_as_uncovered() {
        use super::*;
        let mut t = BTreeMap::new();
        t.insert(5, lane(5, 1e6, None, Some(4.0), false));
        t.insert(6, lane(6, 1e6, None, Some(1.0), false));
        let mut c = CardRoute { coord: None, ..Default::default() };
        c.rates.insert(5, 1.0); c.rates.insert(6, 2.0);
        assert_eq!(decide(&c, &t, 1.0, 100, None).map(|d| d.coord), Some(5), "half the rate at four times the worth wins");
        assert!((lane_value(1.0, 1.0, &t[&5]) - 4.0 / 1e6).abs() < 1e-12);
        // one lane without a worth (the pool omits the key for a lane the last window carried no work on): the worths
        // rank nothing, the reward alone decides (lane 6 at twice the rate), never lane 6's worth against a missing one
        t.get_mut(&5).unwrap().lane_worth = None;
        assert!(!worths_rank(&t));
        assert_eq!(decide(&c, &t, 1.0, 100, None).map(|d| d.coord), Some(6));
        t.get_mut(&6).unwrap().lane_worth = Some(1e9);
        assert_eq!(decide(&c, &t, 1.0, 100, None).map(|d| d.coord), Some(6), "a worth on one lane alone never ranks");
        assert!((rank_value(1.0, 2.0, &t[&6], false) - 2.0 / 1e6).abs() < 1e-15);
        // every lane with a worth: the worths rank
        t.get_mut(&5).unwrap().lane_worth = Some(4e9);
        assert!(worths_rank(&t));
        assert_eq!(decide(&c, &t, 1.0, 100, None).map(|d| d.coord), Some(5));
        // no worth anywhere: the reward alone, as before
        t.get_mut(&5).unwrap().lane_worth = None;
        t.get_mut(&6).unwrap().lane_worth = None;
        assert_eq!(decide(&c, &t, 1.0, 100, None).map(|d| d.coord), Some(6));
        // an unknown worth ("") with no worker count reads as uncovered; a count of 2 overrides it
        let u = lane(7, 1e6, None, None, true);
        assert!(u.uncovered() && u.held_by_one());
        let u = lane(7, 1e6, Some(2), None, true);
        assert!(!u.uncovered() && !u.held_by_one());
        // nothing sent at all: covered, free (a one-form chain or a pool before 0.1.96)
        let u = lane(7, 1e6, None, None, false);
        assert!(!u.uncovered() && !u.held_by_one());
    }

    #[test]
    fn a_stint_cut_short_keeps_its_rate_when_it_read_one() {
        use super::rate_known_at_move;
        assert!(rate_known_at_move(41, 1.6e6));
        assert!(rate_known_at_move(15, 1.0));
        assert!(!rate_known_at_move(14, 1.6e6), "under the bound the stats may not have arrived");
        assert!(!rate_known_at_move(60, 0.0), "no hashes read: no rate to keep");
    }

    use super::*;

    fn table(d: &[(u8, f64, Option<u64>)]) -> BTreeMap<u8, CoordState> {
        d.iter().map(|(c, diff, h)| (*c, CoordState { coord: *c, digest: format!("doc-{c}"), difficulty: *diff, last_height: *h, lane_worth: None, lane_worth_unknown: false, lane_workers: None })).collect()
    }

    #[test]
    fn probes_every_coordinate_before_choosing() {
        let t = table(&[(0, 1e6, Some(100)), (1, 1e6, Some(100)), (2, 1e6, Some(100))]);
        let mut card = CardRoute::default();
        let d = decide(&card, &t, 10.0, 100, None).unwrap();
        assert!(d.probe && d.coord == 0, "{d:?}");
        card.rates.insert(0, 5.0);
        let d = decide(&card, &t, 10.0, 100, None).unwrap();
        assert!(d.probe && d.coord == 1);
        card.rates.insert(1, 1.0);
        card.rates.insert(2, 2.0);
        let d = decide(&card, &t, 10.0, 100, None).unwrap();
        assert!(!d.probe && d.coord == 0, "the greatest reward at equal difficulty is the fastest coordinate: {d:?}");
    }

    #[test]
    fn hysteresis_needs_two_reads_and_the_margin() {
        let mut t = table(&[(0, 1e6, Some(100)), (1, 1e6, Some(100))]);
        let mut card = CardRoute { coord: Some(0), rates: [(0, 5.0), (1, 5.0)].into_iter().collect(), moved_at: 50, ..Default::default() };
        // coordinate 1's difficulty falls 5 percent: inside the margin, no move
        t.get_mut(&1).unwrap().difficulty = 0.95e6;
        assert!(!better_this_read(&card, &t, 10.0));
        assert!(decide(&card, &t, 10.0, 120, None).is_none());
        // it falls 20 percent: over the margin, but the first read only arms it
        t.get_mut(&1).unwrap().difficulty = 0.8e6;
        assert!(better_this_read(&card, &t, 10.0));
        assert!(decide(&card, &t, 10.0, 120, None).is_none());
        card.better_reads = 1;
        let d = decide(&card, &t, 10.0, 120, None).unwrap();
        assert_eq!(d.coord, 1, "{d:?}");
        assert!(d.reason.contains("exceeds"));
    }

    #[test]
    fn one_move_per_window() {
        let t = table(&[(0, 1e6, Some(100)), (1, 0.5e6, Some(100))]);
        let card = CardRoute { coord: Some(0), rates: [(0, 5.0), (1, 5.0)].into_iter().collect(), better_reads: 1, moved_at: 110, ..Default::default() };
        assert!(decide(&card, &t, 10.0, 120, None).is_none(), "moved 10 blocks ago: not yet");
        assert!(decide(&card, &t, 10.0, 126, None).is_some(), "16 blocks on: the move");
    }

    #[test]
    fn the_emptied_coordinate_is_restored_before_any_price() {
        // F-332: three lanes, so a lane is empty past thirty-six chain blocks (twelve of its own intervals); coordinate 2 is forty behind
        let t = table(&[(0, 1e6, Some(100)), (1, 1e6, Some(100)), (2, 1e6, Some(60))]);
        assert_eq!(empty_coords(&t, 100), vec![2]);
        let cards = vec![
            ("b".to_string(), CardRoute { coord: Some(0), rates: [(0, 5.0), (1, 5.0), (2, 4.0)].into_iter().collect(), ..Default::default() }),
            ("a".to_string(), CardRoute { coord: Some(1), rates: [(0, 5.0), (1, 5.0), (2, 1.0)].into_iter().collect(), ..Default::default() }),
        ];
        let plan = restore_plan(&cards, &t, 10.0, 100);
        assert_eq!(plan, vec![(0, 2)], "the card with the smallest loss (b, rate 4 on 2) restores it: {plan:?}");
        let d = decide(&cards[0].1, &t, 10.0, 100, Some(2)).unwrap();
        assert_eq!(d.coord, 2);
        assert!(d.reason.contains("empty"));
        // a coordinate someone already mines is not restored again
        let cards2 = vec![("a".to_string(), CardRoute { coord: Some(2), rates: [(2, 1.0)].into_iter().collect(), ..Default::default() })];
        assert!(restore_plan(&cards2, &t, 10.0, 100).is_empty());
    }

    #[test]
    fn a_read_is_due_on_the_window_or_the_step() {
        let t0 = table(&[(0, 1e6, None), (1, 1e6, None)]);
        let mut t1 = t0.clone();
        assert_eq!(read_due(110, 100, &t1, &t0), None);
        assert_eq!(read_due(116, 100, &t1, &t0), Some("the window"));
        t1.get_mut(&1).unwrap().difficulty = 1.3e6;
        assert_eq!(read_due(101, 100, &t1, &t0), Some("the difficulty signal"));
        t1.insert(3, CoordState { coord: 3, digest: "doc-3".into(), difficulty: 1e6, last_height: None, lane_worth: None, lane_worth_unknown: false, lane_workers: None });
        assert!(read_due(101, 100, &t1, &t0).is_some());
    }

    #[test]
    fn refused_documents_are_never_chosen() {
        let t = table(&[(0, 1e6, Some(100)), (1, 1e3, Some(100))]);
        let card = CardRoute { coord: Some(0), rates: [(0, 5.0)].into_iter().collect(), refused: vec![1], better_reads: 1, ..Default::default() };
        assert!(decide(&card, &t, 10.0, 200, None).is_none(), "coordinate 1 pays a thousand times more and is refused by the worker");
        assert!(decide(&card, &t, 10.0, 200, Some(1)).is_none(), "nor restored by this card");
    }

    #[test]
    fn the_offset_is_stable_and_spreads_cards_inside_the_window() {
        let a = offset_blocks("GPU-b4efff91", "0x8e79");
        assert_eq!(a, offset_blocks("GPU-b4efff91", "0x8e79"), "the same card and address: the same offset on every machine");
        assert!(a < WINDOW_BLOCKS);
        let set: std::collections::BTreeSet<u64> = (0..64).map(|i| offset_blocks(&format!("GPU-{i}"), "0x8e79")).collect();
        assert!(set.len() >= 8, "64 cards spread over at least 8 of the 16 offsets: {set:?}");
        let card = CardRoute { offset: a, last_window_read: 6, ..Default::default() };
        assert!(!card_window_due(6 * WINDOW_BLOCKS + 15, &card), "the window already read: not again");
        assert!(!card_window_due(7 * WINDOW_BLOCKS + a.saturating_sub(1), &card) || a == 0, "before the offset block: not yet");
        assert!(card_window_due(7 * WINDOW_BLOCKS + a, &card), "the offset block of the next window: due");
        assert!(card_window_due(7 * WINDOW_BLOCKS + 15, &card), "a tick that skipped the offset block still reads once");
    }

    #[test]
    fn the_damped_view_ignores_one_stale_read() {
        let steady = table(&[(0, 1e6, None), (1, 1e6, None)]);
        let mut spike = steady.clone();
        spike.get_mut(&1).unwrap().difficulty = 4e6;
        let d = damped(&[steady.clone(), steady.clone(), spike.clone()]);
        assert!((d[&1].difficulty - 2e6).abs() < 1.0, "the mean of three reads: {}", d[&1].difficulty);
        let d2 = damped(&[spike.clone()]);
        assert_eq!(d2[&1].difficulty, 4e6, "one read is its own mean");
    }

    #[test]
    fn the_switch_pays_its_cost_and_the_cap_holds() {
        // a 9 percent gain over a 320 s window against a 30 s cost: 0.09 x 320 = 28.8 s of reward, under the cost
        let t = table(&[(0, 1e6, Some(100)), (1, 1e6 / 1.09, Some(100))]);
        let card = CardRoute { coord: Some(0), rates: [(0, 5.0), (1, 5.0)].into_iter().collect(), better_reads: 1, moved_at: 50, window_start: 50, ..Default::default() };
        assert!(decide(&card, &t, 10.0, 120, None).is_none(), "a gain that does not pay the switch is not taken");
        // a 50 percent gain pays it
        let t2 = table(&[(0, 1e6, Some(100)), (1, 1e6 / 1.5, Some(100))]);
        assert!(decide(&card, &t2, 10.0, 120, None).is_some());
        // the cap: a card that switched in this window does not switch again inside it
        let capped = CardRoute { switches_this_window: 1, window_start: 110, ..card.clone() };
        assert!(decide(&capped, &t2, 10.0, 120, None).is_none());
        assert!(decide(&capped, &t2, 10.0, 126, None).is_some(), "the next window frees it");
    }

    #[test]
    fn an_advertised_reward_outside_the_bound_is_refused() {
        assert!(advertised_reward_ok(10.0, 10.0, 1e6, 5.0));
        assert!(advertised_reward_ok(10.9, 10.0, 1e6, 5.0));
        assert!(!advertised_reward_ok(12.0, 10.0, 1e6, 5.0), "20 percent over the chain's: refused");
        assert!(!advertised_reward_ok(10.0, 0.0, 1e6, 5.0), "no chain reward to compare: refused");
    }

    #[test]
    fn two_machines_reading_the_same_chain_decide_alike() {
        // the no-oracle property: the decision is a function of the table and the card's own state
        let t = table(&[(0, 2e6, Some(100)), (1, 1e6, Some(100)), (2, 1.5e6, Some(100)), (3, 1e6, Some(100))]);
        let card = CardRoute { rates: [(0, 8.0), (1, 3.0), (2, 4.0), (3, 2.9)].into_iter().collect(), ..Default::default() };
        let a = decide(&card, &t, 10.0, 100, None);
        let b = decide(&card.clone(), &t.clone(), 10.0, 100, None);
        assert_eq!(a, b);
        assert_eq!(a.unwrap().coord, 0, "8/2e6 = 4e-6 beats 3/1e6, 4/1.5e6 and 2.9/1e6");
    }

    /// F-469: a lane's rate in the reward is the measured share rate once settled, the hash rate until then.
    #[test]
    fn the_lane_rate_is_the_measured_share_rate_once_settled() {
        assert!((share_weight(&[0xff; 32]) - 1.0).abs() < 1e-9, "a target of all ones costs one hash");
        let mut t = [0xff; 32]; t[0] = 0x00;
        assert!((share_weight(&t) - 256.0).abs() < 1e-6, "a target one byte lower costs 256 hashes");
        assert!(share_weight(&[0; 32]) > 1e76, "a zero target costs the maximum");
        // the raster lane: the worker reports 1.5e9 attempts a second but the step's shares say 1.0e5 hashes a second
        let work = 1.0e5 * 200.0; // the accepted shares' weights over a 200 s stint
        assert_eq!(lane_rate(1.5e9, work, 3, 200, 200), 1.0e5, "settled: the measured share rate");
        assert_eq!(lane_rate(1.5e9, work, 2, 200, 200), 1.5e9, "two shares are not a measure");
        assert_eq!(lane_rate(1.5e9, work, 3, 100, 200), 1.5e9, "a stint under the settle keeps the probe's rate");
        assert_eq!(lane_rate(1.5e9, work, 3, 200, 30), 1.5e9, "a step that just rose keeps the probe's rate");
        assert_eq!(lane_rate(1.5e9, 0.0, 0, 0, 0), 1.5e9, "no shares: the hash rate");
    }
}

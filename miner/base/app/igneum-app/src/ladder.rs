//! The miner's first month, as this machine records it (miner-ui-5, mission item 6, 7 October 2026). The ladder's
//! rungs are chain facts (src/chainfacts.rs reads them from `getFinalityWeights` and `getFinalityCheckpoints`); this
//! module keeps what only this machine knows: when its first block came and on which card, the blocks it found per day
//! (the days-mined ring), every checkpoint its miners signed and which of those locked (the signing streak), the shards
//! its cards were paid for, and the block card to show (the first block, every 100th, 1,000th and 10,000th, the first
//! on a new card). Persisted as `<app dir>/ladder.json`; pure, no clock of its own (every call takes `now`).
//!
//! A block is matched to its hash by the nonce: the miner prints `ACCEPTED block nonce=0x..` for ours, the node prints
//! `PoW accepted <hash> by .. (daa N, .., nonce 0x..)` for every block it validates, ours and relayed alike, so the
//! nonce is the join and nothing else is.
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

pub const DAY_S: f64 = 86_400.0;
/// days kept in the ring (the 30-day window plus a month of slack for the view)
const KEEP_DAYS: u64 = 60;
const KEEP_BLOCKS: usize = 30;
const KEEP_SHARDS: usize = 50;
const KEEP_PENDING: usize = 400;
const KEEP_SIGNED: usize = 400;
/// the block counts that raise a card (then every 10,000th)
pub const MILESTONES: [u64; 4] = [1, 100, 1_000, 10_000];

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct DayBlocks {
    /// unix day number (unix seconds / 86,400, UTC)
    pub day: u64,
    pub blocks: u64,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct BlockFound {
    pub hash: String,
    pub daa: u64,
    pub nonce: String,
    pub at: f64,
    pub card: String,
    pub card_name: String,
    /// this machine's lifetime count at that block (1 = the first)
    pub count: u64,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct ShardPaid {
    pub block: u64,
    pub shard: u64,
    /// wei as a decimal string (u128 does not survive every JSON reader)
    pub wei: String,
    pub at: f64,
}

/// earned-windows-26 (the founder's audit, 8 October 2026, item 2): one row per coinbase the node credited and per shard
/// paid, with the wei it paid at the time. `wei` is empty until the block is priced (the chain facts' reward at the
/// block's time, read from the engine's cache when the block lands or on the next read that has it); a priced row
/// never changes, so a window is the sum of what the chain paid, never today's reward applied to old blocks.
#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct Earned {
    pub at: f64,
    #[serde(default)]
    pub shard: bool,
    #[serde(default)]
    pub wei: String,
}

/// One earned window for the dashboard: the wei as a decimal string, the IGN as a number for the view, the counts,
/// and whether every row in it is priced (`priced` false while a block waits for its reward figure).
#[derive(Clone, Debug, Default, Serialize, PartialEq)]
pub struct Window {
    pub wei: String,
    pub ign: f64,
    pub blocks: u64,
    pub shards: u64,
    pub priced: bool,
}

#[derive(Clone, Debug, Default, Serialize, PartialEq)]
pub struct EarnedWindows {
    pub h24: Window,
    pub d7: Window,
    pub all: Window,
    /// the all-time figure was back-filled once from the lifetime count at one reward (a record from before the rows)
    pub estimated: bool,
}

/// the earned log keeps 7 days plus an hour of rows; the all-time window is a running total
const KEEP_EARNED_S: f64 = 7.0 * DAY_S + 3600.0;
const WEI_PER_IGN: f64 = 1e18;

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct Milestone {
    /// first | hundred | thousand | ten_thousand | card
    pub kind: String,
    pub count: u64,
    pub card: String,
    pub card_name: String,
    pub hash: String,
    pub daa: u64,
    pub at: f64,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct Ladder {
    #[serde(default)]
    pub first_block_at: f64,
    #[serde(default)]
    pub first_block_card: String,
    #[serde(default)]
    pub first_block_hash: String,
    #[serde(default)]
    pub first_block_daa: u64,
    #[serde(default)]
    pub days: Vec<DayBlocks>,
    /// card key -> unix s of its first block
    #[serde(default)]
    pub cards_first: BTreeMap<String, f64>,
    #[serde(default)]
    pub blocks: Vec<BlockFound>,
    /// earnings-26 (8 October 2026): the unix time of every block the node credited in the last day, so the Earnings tab's
    /// earned line is the measured 24 hours (`blocks` keeps only the last KEEP_BLOCKS; a fast card finds more in a day).
    /// Trimmed to the day on every read; a record from before the field reads its times from `blocks` once.
    #[serde(default)]
    pub block_times: Vec<f64>,
    /// earned-windows-26: the rows of the last 7 days and the all-time running totals (wei as a decimal string)
    #[serde(default)]
    pub earned_log: Vec<Earned>,
    #[serde(default)]
    pub earned_all_wei: String,
    #[serde(default)]
    pub earned_all_blocks: u64,
    #[serde(default)]
    pub earned_all_shards: u64,
    /// the one-time back-fill of a record from before the rows (the lifetime count at the reward then read)
    #[serde(default)]
    pub earned_backfilled: bool,
    #[serde(default)]
    pub earned_estimated: bool,
    #[serde(default)]
    pub votes_signed: u64,
    #[serde(default)]
    pub last_vote_index: u64,
    #[serde(default)]
    pub last_vote_at: f64,
    /// checkpoints signed and not yet seen locked
    #[serde(default)]
    pub signed_open: Vec<u64>,
    #[serde(default)]
    pub last_signed_locked: u64,
    #[serde(default)]
    pub signed_locked_count: u64,
    /// when the unbroken run of votes began (0 = no vote yet)
    #[serde(default)]
    pub streak_since: f64,
    #[serde(default)]
    pub shards: Vec<ShardPaid>,
    #[serde(default)]
    pub milestone: Option<Milestone>,
    /// the first-hour timeline's marks: when this install's node first synced and a card first mined (0 = not yet)
    #[serde(default)]
    pub first_synced_at: f64,
    #[serde(default)]
    pub first_mining_at: f64,
    /// node-accepted blocks waiting for the miner's own ACCEPTED line (nonce -> hash, daa), and the reverse case
    #[serde(default)]
    pending: Vec<(String, String, u64)>,
    /// first-block-21 (the founder, 7 October 2026: "the 'you got your first block' situation only shows for the miner's
    /// first ever block"; the project's reading: SEEN once): set when a window has shown the first-block card (the page
    /// reports it through POST /api/card/seen on display or dismiss), never when it is raised. The card is raised when
    /// the lifetime count the engine persists (settings.json accepted_total) crosses from 0 to 1 and waits, across
    /// restarts and updates, until a window shows it: a block found overnight or across an auto-update greets the
    /// miner the first time they open the app, and after that never again. Kept across restarts, updates and a kept
    /// data directory; never cleared by a key changing identities or a card being swapped (the count is the
    /// machine's, not a key's or a card's). A count that starts over on a record carrying the flag (settings.json
    /// lost, the ladder kept) raises no first card again; a an earlier release record (schema 0, no flag) whose first block already
    /// came (count 1 or more, or first_block_at set) takes the flag at `start_run`, since an earlier release showed it every run.
    #[serde(default)]
    pub first_block_shown: bool,
    /// a window has shown the current `milestone` (set with it by `card_seen`); a seen card is dropped at the next
    /// `start_run`, an unseen one waits for its window
    #[serde(default)]
    pub milestone_seen: bool,
    /// the record's shape: 0 = written by an earlier release (no first-block flag), 1 = first-block-21
    #[serde(default)]
    pub schema: u32,
}

/// What the dashboard reads (api/state.ladder): the record above summarised at `now`.
#[derive(Clone, Debug, Default, Serialize)]
pub struct LadderState {
    pub first_block_at: f64,
    pub first_block_card: String,
    pub first_block_hash: String,
    pub first_block_daa: u64,
    /// distinct UTC days with at least one block in the last 30 days
    pub days_mined_30: u64,
    pub blocks_30d: u64,
    pub blocks_today: u64,
    /// blocks the node credited to this address in the last 24 hours (the coinbase reads), never a projection
    pub blocks_24h: u64,
    /// earned-windows-26: what the chain paid this machine in the last 24 hours, 7 days and all time
    pub earned: EarnedWindows,
    pub days: Vec<DayBlocks>,
    pub cards_first: BTreeMap<String, f64>,
    pub blocks: Vec<BlockFound>,
    pub votes_signed: u64,
    pub last_vote_index: u64,
    pub last_vote_at: f64,
    pub last_signed_locked: u64,
    pub signed_locked_count: u64,
    /// seconds of unbroken signing (0 = none)
    pub streak_s: f64,
    pub shards: Vec<ShardPaid>,
    pub milestone: Option<Milestone>,
    pub first_synced_at: f64,
    pub first_mining_at: f64,
    /// first-block-21: the first-block card has been raised once on this machine (see `Ladder::first_block_shown`)
    pub first_block_shown: bool,
}

fn parse_wei(s: &str) -> u128 {
    s.trim().parse::<u128>().unwrap_or(0)
}

pub fn day_of(unix: f64) -> u64 {
    (unix.max(0.0) / DAY_S).floor() as u64
}

impl Ladder {
    pub fn load(path: &std::path::Path) -> Ladder {
        std::fs::read_to_string(path).ok().and_then(|t| serde_json::from_str(&t).ok()).unwrap_or_default()
    }
    pub fn save(&self, path: &std::path::Path) {
        if let Some(d) = path.parent() {
            let _ = std::fs::create_dir_all(d);
        }
        let mut me = self.clone();
        me.schema = 1;
        if let Ok(t) = serde_json::to_string_pretty(&me) {
            let _ = std::fs::write(path, t);
        }
    }

    /// The engine's run begins over this record (first-block-21, 7 October 2026). `accepted_total` is the lifetime
    /// count settings.json holds. A block card a window has shown (`milestone_seen`) is dropped here, so a restart
    /// or an update never shows "your first block" (or block 100) a second time; a card no window has shown yet
    /// waits (found overnight, or across an auto-update restart). A an earlier release record (schema 0) carries no flag and
    /// showed its card on every run, so one whose first block already came (the count at 1 or more, or
    /// first_block_at set) takes the flag now and its card is dropped. Returns true when the record changed and
    /// wants saving.
    pub fn start_run(&mut self, accepted_total: u64) -> bool {
        let mut dirty = false;
        if self.schema == 0 {
            if accepted_total >= 1 || self.first_block_at > 0.0 {
                self.first_block_shown = true;
                self.milestone = None;
            }
            self.schema = 1;
            dirty = true;
        }
        if self.milestone.is_some() && self.milestone_seen {
            self.milestone = None;
            self.milestone_seen = false;
            dirty = true;
        }
        dirty
    }

    /// The miner's `dev-fee block <hash>` line, which follows a fee template's ACCEPTED line: a first card raised by that
    /// block (within two seconds) is withdrawn, since the block paid the dev address, not the user's. Returns true when
    /// the record changed.
    pub fn on_fee_block(&mut self, now: f64) -> bool {
        match self.milestone.as_ref() {
            Some(m) if m.kind == "first" && !self.milestone_seen && (now - m.at).abs() < 2.0 => {
                self.milestone = None;
                true
            }
            _ => false,
        }
    }

    /// The first card a window has, as the mark settings.json keeps per address: (the block's hash, its time).
    pub fn first_card_mark(&self) -> Option<(String, f64)> {
        self.milestone.as_ref().filter(|m| m.kind == "first").map(|m| (m.hash.clone(), m.at))
    }

    /// A window showed (or the user dismissed) the card for the milestone at `count` and `at` (first-block-21): the
    /// card is spent, and a first-block card sets the lifetime flag. Returns true when the record changed.
    pub fn card_seen(&mut self, count: u64, at: f64) -> bool {
        let m = match self.milestone.as_ref() {
            Some(m) if m.count == count && (m.at - at).abs() < 1.0 => m,
            _ => return false,
        };
        let mut dirty = false;
        if !self.milestone_seen {
            self.milestone_seen = true;
            dirty = true;
        }
        if m.kind == "first" && !self.first_block_shown {
            self.first_block_shown = true;
            dirty = true;
        }
        dirty
    }

    /// The node validated a block (its own log line): remembered by nonce until the miner's line claims it.
    pub fn on_node_accepted(&mut self, hash: &str, daa: u64, nonce: &str) {
        let nonce = norm_nonce(nonce);
        if nonce.is_empty() || hash.is_empty() {
            return;
        }
        // a block of ours whose miner line came first: fill the hash in now
        if let Some(b) = self.blocks.iter_mut().find(|b| b.hash.is_empty() && b.nonce == nonce) {
            b.hash = hash.to_string();
            b.daa = daa;
            if self.first_block_hash.is_empty() && self.first_block_at == b.at {
                self.first_block_hash = hash.to_string();
                self.first_block_daa = daa;
            }
            if let Some(m) = self.milestone.as_mut() {
                if m.hash.is_empty() && m.at == b.at {
                    m.hash = hash.to_string();
                    m.daa = daa;
                }
            }
            return;
        }
        self.pending.push((nonce, hash.to_string(), daa));
        if self.pending.len() > KEEP_PENDING {
            let n = self.pending.len() - KEEP_PENDING;
            self.pending.drain(0..n);
        }
    }

    /// The miner's own ACCEPTED line: `total` is this machine's lifetime count including this block. Returns the
    /// milestone this block raised, if any.
    pub fn on_block(&mut self, now: f64, card: &str, card_name: &str, nonce: &str, total: u64) -> Option<Milestone> {
        // the machine-level rule as an earlier release had it: the first card when the lifetime count crosses to 1 on a record with
        // no block and no flag (the engine passes the per-address rule through `on_block_for`, v0.2.1)
        let shown = self.first_block_shown || total != 1 || self.first_block_at != 0.0;
        self.on_block_for(now, card, card_name, nonce, total, shown)
    }

    /// v0.2.1 (the founder, 8 October 2026): the first-block card shows once per payout address, ever; the engine reads
    /// `shown_for_address` from settings.json (`first_block_shown[address]`) and the card is raised whenever the address
    /// has no mark, whatever the lifetime count says. A dev-fee block never keeps it: the miner's `dev-fee block` line
    /// follows its ACCEPTED line, and `on_fee_block` withdraws a first card raised by that block.
    pub fn on_block_for(&mut self, now: f64, card: &str, card_name: &str, nonce: &str, total: u64, shown_for_address: bool) -> Option<Milestone> {
        let nonce = norm_nonce(nonce);
        let (hash, daa) = match self.pending.iter().position(|p| !nonce.is_empty() && p.0 == nonce) {
            Some(i) => {
                let p = self.pending.remove(i);
                (p.1, p.2)
            }
            None => (String::new(), 0),
        };
        let day = day_of(now);
        match self.days.iter_mut().find(|d| d.day == day) {
            Some(d) => d.blocks += 1,
            None => self.days.push(DayBlocks { day, blocks: 1 }),
        }
        self.days.sort_by_key(|d| d.day);
        if self.days.len() > KEEP_DAYS as usize {
            let n = self.days.len() - KEEP_DAYS as usize;
            self.days.drain(0..n);
        }
        // the first-block card: once in the app's life, when the persisted lifetime count crosses from 0 to 1 on a
        // record with no block and no flag (first-block-21); a count that starts over raises nothing here
        let first = !shown_for_address;
        if self.first_block_at == 0.0 {
            self.first_block_at = now;
            self.first_block_card = card.to_string();
            self.first_block_hash = hash.clone();
            self.first_block_daa = daa;
        }
        let new_card = !card.is_empty() && !self.cards_first.contains_key(card);
        if new_card {
            self.cards_first.insert(card.to_string(), now);
        }
        self.blocks.insert(0, BlockFound { hash: hash.clone(), daa, nonce, at: now, card: card.to_string(), card_name: card_name.to_string(), count: total });
        self.blocks.truncate(KEEP_BLOCKS);
        self.block_times.push(now);
        self.block_times.retain(|t| *t > now - DAY_S - 3600.0);
        self.earned_log.push(Earned { at: now, shard: false, wei: String::new() });
        self.earned_all_blocks += 1;
        self.trim_earned(now);
        let kind = if first { "first" } else if total == 100 { "hundred" } else if total == 1_000 { "thousand" } else if total >= 10_000 && total % 10_000 == 0 { "ten_thousand" } else if new_card && total > 1 { "card" } else { "" };
        if kind.is_empty() {
            return None;
        }
        let m = Milestone { kind: kind.into(), count: total, card: card.into(), card_name: card_name.into(), hash, daa, at: now };
        self.milestone = Some(m.clone());
        self.milestone_seen = false;
        self.schema = 1;
        Some(m)
    }

    /// A `VOTE index=N` line from a miner: the streak holds while votes keep coming inside `gap_s`.
    pub fn on_vote(&mut self, now: f64, index: u64, gap_s: f64) {
        self.votes_signed += 1;
        if self.streak_since == 0.0 || (self.last_vote_at > 0.0 && now - self.last_vote_at > gap_s) {
            self.streak_since = now;
        }
        self.last_vote_at = now;
        if index > self.last_vote_index {
            self.last_vote_index = index;
        }
        if index > 0 && !self.signed_open.contains(&index) {
            self.signed_open.push(index);
            if self.signed_open.len() > KEEP_SIGNED {
                let n = self.signed_open.len() - KEEP_SIGNED;
                self.signed_open.drain(0..n);
            }
        }
    }

    /// A `LOCK checkpoint N` line: every signed index at or under N is in a locked certificate now.
    pub fn on_lock(&mut self, index: u64) {
        let (locked, open): (Vec<u64>, Vec<u64>) = self.signed_open.iter().partition(|&&i| i <= index);
        if let Some(&top) = locked.iter().max() {
            if top > self.last_signed_locked {
                self.last_signed_locked = top;
            }
            self.signed_locked_count += locked.len() as u64;
        }
        self.signed_open = open;
    }

    /// The timeline's first-time marks ("synced", "mining"): set once, kept. Returns true when it was new.
    pub fn mark(&mut self, what: &str, now: f64) -> bool {
        let slot = match what { "synced" => &mut self.first_synced_at, "mining" => &mut self.first_mining_at, _ => return false };
        if *slot > 0.0 { return false; }
        *slot = now;
        true
    }

    pub fn on_shard_paid(&mut self, now: f64, block: u64, shard: u64, wei: u128) {
        if self.shards.iter().any(|s| s.block == block && s.shard == shard) {
            return;
        }
        self.shards.insert(0, ShardPaid { block, shard, wei: wei.to_string(), at: now });
        self.shards.truncate(KEEP_SHARDS);
        self.earned_log.push(Earned { at: now, shard: true, wei: wei.to_string() });
        self.earned_all_shards += 1;
        self.earned_all_wei = (parse_wei(&self.earned_all_wei) + wei).to_string();
        self.trim_earned(now);
    }

    fn trim_earned(&mut self, now: f64) {
        self.earned_log.retain(|e| e.at > now - KEEP_EARNED_S);
    }

    /// Price every block row that has no wei yet at `reward_wei` (the chain facts' miner reward when the engine reads it;
    /// a block lands and is priced within the same minute when the cache is warm). Returns how many rows it priced.
    pub fn price_blocks(&mut self, reward_wei: u128) -> usize {
        if reward_wei == 0 {
            return 0;
        }
        let mut n = 0usize;
        let mut add: u128 = 0;
        for e in self.earned_log.iter_mut() {
            if !e.shard && e.wei.is_empty() {
                e.wei = reward_wei.to_string();
                add += reward_wei;
                n += 1;
            }
        }
        if n > 0 {
            self.earned_all_wei = (parse_wei(&self.earned_all_wei) + add).to_string();
        }
        n
    }

    /// A record from before the rows (an earlier release and earlier): the all-time window starts from the lifetime count the engine
    /// persists (settings.json accepted_total) at the reward read now, once; the shards kept in the list join it. The
    /// windows then say `estimated` for all time until the counts are all from rows.
    pub fn backfill_earned(&mut self, lifetime_blocks: u64, reward_wei: u128) -> bool {
        if self.earned_backfilled || reward_wei == 0 {
            return false;
        }
        self.earned_backfilled = true;
        if lifetime_blocks > self.earned_all_blocks {
            let missing = lifetime_blocks - self.earned_all_blocks;
            self.earned_all_blocks = lifetime_blocks;
            self.earned_all_wei = (parse_wei(&self.earned_all_wei) + reward_wei * missing as u128).to_string();
            self.earned_estimated = true;
        }
        let logged_shards = self.earned_log.iter().filter(|e| e.shard).count() as u64;
        if self.shards.len() as u64 > logged_shards {
            let extra: Vec<&ShardPaid> = self.shards.iter().skip(logged_shards as usize).collect();
            let wei: u128 = extra.iter().map(|s| parse_wei(&s.wei)).sum();
            self.earned_all_shards += extra.len() as u64;
            self.earned_all_wei = (parse_wei(&self.earned_all_wei) + wei).to_string();
        }
        true
    }

    /// The three windows at `now`: 24 hours and 7 days from the rows, all time from the running totals.
    pub fn earned_windows(&self, now: f64) -> EarnedWindows {
        let window = |from: f64| -> Window {
            let mut w = Window { priced: true, ..Default::default() };
            let mut wei: u128 = 0;
            for e in self.earned_log.iter().filter(|e| e.at > from) {
                if e.shard { w.shards += 1; } else { w.blocks += 1; }
                if e.wei.is_empty() { w.priced = false; } else { wei += parse_wei(&e.wei); }
            }
            w.wei = wei.to_string();
            w.ign = wei as f64 / WEI_PER_IGN;
            w
        };
        let all_wei = parse_wei(&self.earned_all_wei);
        EarnedWindows {
            h24: window(now - DAY_S),
            d7: window(now - 7.0 * DAY_S),
            all: Window { wei: all_wei.to_string(), ign: all_wei as f64 / WEI_PER_IGN, blocks: self.earned_all_blocks, shards: self.earned_all_shards, priced: !self.earned_log.iter().any(|e| !e.shard && e.wei.is_empty()) },
            estimated: self.earned_estimated,
        }
    }

    /// Seconds of unbroken signing at `now` (0 when the last vote is older than `gap_s`).
    pub fn streak_s(&self, now: f64, gap_s: f64) -> f64 {
        if self.streak_since == 0.0 || self.last_vote_at == 0.0 || now - self.last_vote_at > gap_s {
            return 0.0;
        }
        (now - self.streak_since).max(0.0)
    }

    fn blocks_since(&self, from: f64) -> u64 {
        // the day ring is daily; the finer windows read the block list and fall back to the ring
        self.days.iter().filter(|d| (d.day as f64 + 1.0) * DAY_S > from).map(|d| d.blocks).sum()
    }

    /// the blocks credited in the last 24 hours: `block_times`, or the block list for a record from before the field
    pub fn blocks_24h(&self, now: f64) -> u64 {
        let from = now - DAY_S;
        if self.block_times.is_empty() {
            return self.blocks.iter().filter(|b| b.at > from).count() as u64;
        }
        self.block_times.iter().filter(|t| **t > from).count() as u64
    }

    pub fn state(&self, now: f64, gap_s: f64) -> LadderState {
        let today = day_of(now);
        let window_from = now - 30.0 * DAY_S;
        let days_mined_30 = self.days.iter().filter(|d| d.blocks > 0 && (d.day as f64 + 1.0) * DAY_S > window_from).count() as u64;
        LadderState {
            first_block_at: self.first_block_at,
            first_block_card: self.first_block_card.clone(),
            first_block_hash: self.first_block_hash.clone(),
            first_block_daa: self.first_block_daa,
            days_mined_30,
            blocks_30d: self.blocks_since(window_from),
            blocks_today: self.days.iter().find(|d| d.day == today).map(|d| d.blocks).unwrap_or(0),
            blocks_24h: self.blocks_24h(now),
            earned: self.earned_windows(now),
            days: self.days.iter().filter(|d| (d.day as f64 + 1.0) * DAY_S > window_from).cloned().collect(),
            cards_first: self.cards_first.clone(),
            blocks: self.blocks.iter().take(10).cloned().collect(),
            votes_signed: self.votes_signed,
            last_vote_index: self.last_vote_index,
            last_vote_at: self.last_vote_at,
            last_signed_locked: self.last_signed_locked,
            signed_locked_count: self.signed_locked_count,
            streak_s: self.streak_s(now, gap_s),
            shards: self.shards.clone(),
            milestone: self.milestone.clone(),
            first_synced_at: self.first_synced_at,
            first_mining_at: self.first_mining_at,
            first_block_shown: self.first_block_shown,
        }
    }
}

/// The Activity line a milestone raises (the dashboard's EVENT_MAP reads these shapes).
pub fn milestone_event(m: &Milestone) -> String {
    match m.kind.as_str() {
        "first" => format!("first block: found by {} (block card)", m.card_name),
        "hundred" => format!("block 100 of this machine: found by {} (block card)", m.card_name),
        "thousand" => format!("block 1,000 of this machine: found by {} (block card)", m.card_name),
        "ten_thousand" => format!("block {} of this machine: found by {} (block card)", with_commas(m.count), m.card_name),
        _ => format!("first block on {} (block card)", m.card_name),
    }
}

fn with_commas(n: u64) -> String {
    let s = n.to_string();
    let mut out = String::new();
    for (i, c) in s.chars().enumerate() {
        if i > 0 && (s.len() - i) % 3 == 0 { out.push(','); }
        out.push(c);
    }
    out
}

fn norm_nonce(n: &str) -> String {
    let t = n.trim().trim_start_matches("0x").trim_start_matches('0').to_ascii_lowercase();
    if t.is_empty() && !n.trim().is_empty() { "0".into() } else { t }
}

/// `PoW accepted <hash> by <engine> (daa N, ..., nonce 0x..)` -> (hash, daa, nonce)
pub fn parse_node_accepted(text: &str) -> Option<(String, u64, String)> {
    let rest = text.split("PoW accepted ").nth(1)?;
    let hash = rest.split_whitespace().next()?.to_string();
    if hash.len() != 64 || !hash.chars().all(|c| c.is_ascii_hexdigit()) {
        return None;
    }
    let daa = rest.split("(daa ").nth(1)?.split(|c: char| !c.is_ascii_digit()).next()?.parse::<u64>().ok()?;
    let nonce = rest.split("nonce ").nth(1)?.trim_end_matches(')').split_whitespace().next()?.to_string();
    Some((hash, daa, nonce))
}

/// `... ACCEPTED block nonce=0x.. (...)` -> the nonce
pub fn parse_miner_accepted(text: &str) -> Option<String> {
    let rest = text.split("ACCEPTED block nonce=").nth(1)?;
    Some(rest.split_whitespace().next()?.to_string())
}

/// `... VOTE index=N ...` -> N
pub fn parse_vote_index(text: &str) -> Option<u64> {
    text.split("VOTE index=").nth(1)?.split(|c: char| !c.is_ascii_digit()).next()?.parse().ok()
}

#[cfg(test)]
mod tests {
    // v0.2.1: the first card once per address (the engine's mark gates it, not the lifetime count); a dev-fee block's line
    // withdraws a first card it raised; a later block of the user's raises it again
    #[test]
    fn the_first_card_follows_the_address_mark_and_a_dev_fee_block_withdraws_it() {
        let mut l = Ladder::default();
        let t0 = 1_791_400_000.0;
        assert!(l.on_block_for(t0, "c", "c", "", 1, true).is_none(), "an address with a mark: nothing at count 1");
        assert!(l.first_card_mark().is_none());
        let m = l.on_block_for(t0 + 10.0, "c", "c", "", 7, false).expect("the first card for a new address");
        assert_eq!(m.kind, "first");
        assert_eq!(l.first_card_mark().map(|x| x.1), Some(t0 + 10.0));
        assert!(l.on_fee_block(t0 + 10.2), "the dev-fee line right after withdraws it");
        assert!(l.milestone.is_none());
        assert!(!l.on_fee_block(t0 + 10.3), "nothing to withdraw twice");
        let m2 = l.on_block_for(t0 + 60.0, "c", "c", "", 8, false).expect("raised again by the next block of the user's");
        assert_eq!(m2.kind, "first");
        assert!(!l.on_fee_block(t0 + 70.0), "a fee line long after touches nothing");
        assert!(l.first_card_mark().is_some());
        // the an earlier release wrapper keeps the machine rule: count 2 on a fresh record raises no first card (a card milestone at most)
        let mut o = Ladder::default();
        assert_ne!(o.on_block(t0, "c", "c", "", 2).map(|m| m.kind), Some("first".to_string()));
        let mut f = Ladder::default();
        assert_eq!(f.on_block(t0 + 1.0, "c", "c", "", 1).map(|m| m.kind), Some("first".to_string()));
    }

    // earned-windows-26: three windows over a fixed record; a block at the 24 h edge counts once; the wei is what the
    // chain paid at the time, never today's reward applied to old blocks; the back-fill runs once
    #[test]
    fn the_earned_windows_sum_what_the_chain_paid_at_the_time_and_the_edge_block_counts_once() {
        let mut l = Ladder::default();
        let t0 = 1_791_400_000.0;
        let reward_a: u128 = 4_880_000_000_000_000_000; // 4.88 IGN
        let reward_b: u128 = 4_000_000_000_000_000_000; // 4.00 IGN, the ramp moved
        // eight days ago: a block at reward_a (outside every window but all time)
        l.on_block(t0 - 8.0 * DAY_S, "c", "c", "", 1);
        assert_eq!(l.price_blocks(reward_a), 1);
        // three days ago: two blocks at reward_a and a shard
        l.on_block(t0 - 3.0 * DAY_S, "c", "c", "", 2);
        l.on_block(t0 - 3.0 * DAY_S + 1.0, "c", "c", "", 3);
        assert_eq!(l.price_blocks(reward_a), 2);
        l.on_shard_paid(t0 - 3.0 * DAY_S + 2.0, 100, 3, 120_000_000_000_000_000); // 0.12 IGN
        // exactly 24 h ago plus a second: inside the day; exactly at the edge: outside (strict)
        l.on_block(t0 - DAY_S + 1.0, "c", "c", "", 4);
        l.on_block(t0 - DAY_S, "c", "c", "", 5);
        assert_eq!(l.price_blocks(reward_b), 2);
        // an hour ago: a block not yet priced
        l.on_block(t0 - 3600.0, "c", "c", "", 6);
        let w = l.earned_windows(t0);
        assert_eq!(w.h24.blocks, 2, "the edge block counts once, on the far side");
        assert_eq!(w.h24.shards, 0);
        assert_eq!(w.h24.wei, reward_b.to_string());
        assert!(!w.h24.priced, "the last block waits for its reward");
        assert_eq!(w.d7.blocks, 5);
        assert_eq!(w.d7.shards, 1);
        assert_eq!(w.d7.wei, (2 * reward_a + 2 * reward_b + 120_000_000_000_000_000).to_string());
        assert!((w.d7.ign - 17.88).abs() < 1e-9);
        assert_eq!(w.all.blocks, 6);
        assert_eq!(w.all.shards, 1);
        assert_eq!(w.all.wei, (3 * reward_a + 2 * reward_b + 120_000_000_000_000_000).to_string(), "all time holds the eight-day-old block at its own reward");
        assert!(!w.estimated);
        // the late reward prices the waiting block only; priced rows never move
        assert_eq!(l.price_blocks(reward_a), 1);
        let w2 = l.earned_windows(t0);
        assert!(w2.h24.priced);
        assert_eq!(w2.h24.wei, (reward_a + reward_b).to_string());
        assert_eq!(l.price_blocks(reward_b), 0);
        assert_eq!(l.earned_windows(t0).h24.wei, (reward_a + reward_b).to_string());
        // the log keeps seven days plus an hour: the eight-day block is gone from the rows, kept in all time
        assert_eq!(l.earned_log.len(), 6);
        assert_eq!(l.state(t0, 600.0).earned.all.blocks, 6);
    }

    #[test]
    fn a_record_from_before_the_rows_is_back_filled_once_from_the_lifetime_count_and_says_estimated() {
        let mut l = Ladder::default();
        let t0 = 1_791_400_000.0;
        l.shards.push(ShardPaid { block: 1, shard: 1, wei: "250000000000000000".into(), at: t0 - 100.0 });
        let reward: u128 = 4_880_000_000_000_000_000;
        assert!(l.backfill_earned(40, reward));
        let w = l.earned_windows(t0);
        assert_eq!(w.all.blocks, 40);
        assert_eq!(w.all.shards, 1);
        assert_eq!(w.all.wei, (40 * reward + 250_000_000_000_000_000).to_string());
        assert!(w.estimated);
        assert_eq!(w.h24.blocks, 0, "the back-fill never invents rows in a window");
        assert!(!l.backfill_earned(41, reward), "once");
        assert_eq!(l.earned_windows(t0).all.blocks, 40);
        // a fresh record with rows for every block is not estimated
        let mut f = Ladder::default();
        f.on_block(t0, "c", "c", "", 1);
        f.price_blocks(reward);
        assert!(f.backfill_earned(1, reward));
        let fw = f.earned_windows(t0 + 1.0);
        assert!(!fw.estimated);
        assert_eq!(fw.all.blocks, 1);
        assert_eq!(fw.all.wei, reward.to_string());
        // a reward of zero back-fills nothing and leaves the flag for the next read
        let mut z = Ladder::default();
        assert!(!z.backfill_earned(5, 0));
        assert!(!z.earned_backfilled);
    }

    #[test]
    fn the_24_hour_count_reads_every_coinbase_of_the_day_past_the_list_cap_and_rolls_off() {
        let mut l = Ladder::default();
        let t0 = 1_791_400_000.0;
        for i in 0..40u64 {
            l.blocks.insert(0, BlockFound { hash: format!("{i}"), daa: i, nonce: String::new(), at: t0 + i as f64 * 600.0, card: "c".into(), card_name: "c".into(), count: i + 1 });
            l.blocks.truncate(KEEP_BLOCKS);
            l.block_times.push(t0 + i as f64 * 600.0);
        }
        let now = t0 + 39.0 * 600.0 + 1.0;
        assert_eq!(l.blocks.len(), KEEP_BLOCKS);
        assert_eq!(l.state(now, 600.0).blocks_24h, 40, "every block of the day counts, past the list's cap");
        assert_eq!(l.blocks_24h(now + DAY_S - 20.0 * 600.0), 20, "the first 20 rolled off the day");
        assert_eq!(l.blocks_24h(now + 2.0 * DAY_S), 0);
        // a record from before the field reads its block list
        let mut old = Ladder::default();
        old.blocks.push(BlockFound { hash: "a".into(), daa: 1, nonce: String::new(), at: now - 100.0, card: "c".into(), card_name: "c".into(), count: 1 });
        old.blocks.push(BlockFound { hash: "b".into(), daa: 1, nonce: String::new(), at: now - DAY_S - 100.0, card: "c".into(), card_name: "c".into(), count: 1 });
        assert_eq!(old.blocks_24h(now), 1);
    }
    use super::*;
    const NODE: &str = "2026-10-04 20:08:38.486+00:00 [INFO ] PoW accepted 28abaa8c28ffb4c3eab96b5dc202f8328df7038dc7b2653c9e4b7dd6369ae5ce by igneum-lottery-v2-bound (daa 38637, epoch seed c8b574fbbbd6ba93b34f8c9a81042b6970b140ffb24219e381d82b653673ee81, day 20730, nonce 0x267dd27200a91c80)";
    const MINER: &str = "1791140918.500 ACCEPTED block nonce=0x267dd27200a91c80 (gpu worker, cpu re-check ok, identity mac-01234567-1)";

    #[test]
    fn the_node_and_miner_lines_parse() {
        let (h, daa, n) = parse_node_accepted(NODE).unwrap();
        assert_eq!(h, "28abaa8c28ffb4c3eab96b5dc202f8328df7038dc7b2653c9e4b7dd6369ae5ce");
        assert_eq!(daa, 38637);
        assert_eq!(n, "0x267dd27200a91c80");
        assert_eq!(parse_miner_accepted(MINER).unwrap(), "0x267dd27200a91c80");
        assert_eq!(parse_vote_index("1791140918.500 VOTE index=1240 checkpoint=abcd signed").unwrap(), 1240);
        assert!(parse_node_accepted("PoW rejected 00 by x").is_none());
    }

    #[test]
    fn the_first_block_is_matched_to_its_hash_by_nonce_and_raises_the_first_card() {
        let mut l = Ladder::default();
        let (h, daa, n) = parse_node_accepted(NODE).unwrap();
        l.on_node_accepted(&h, daa, &n);
        // a relayed block from another miner sits in pending too and never matches
        l.on_node_accepted("ef21a8454b2f3fcaf6c32a8b9c959df936770edfdffff61c7198989d9431ea4b", 38643, "0x4bc93aba007eabaa");
        let m = l.on_block(1_791_140_918.5, "apple::Apple M5 Max", "Apple M5 Max", &parse_miner_accepted(MINER).unwrap(), 1).unwrap();
        assert_eq!(m.kind, "first");
        assert_eq!(m.hash, h);
        assert_eq!(m.daa, 38637);
        assert_eq!(l.first_block_hash, h);
        assert_eq!(l.first_block_daa, 38637);
        assert_eq!(l.first_block_card, "apple::Apple M5 Max");
        assert_eq!(l.blocks.len(), 1);
        assert_eq!(l.pending.len(), 1, "the relayed block stays unmatched");
        let s = l.state(1_791_140_920.0, 600.0);
        assert_eq!(s.days_mined_30, 1);
        assert_eq!(s.blocks_30d, 1);
        assert_eq!(s.blocks_today, 1);
    }

    #[test]
    fn a_miner_line_before_the_node_line_gets_its_hash_filled_in() {
        let mut l = Ladder::default();
        l.on_block(100.0, "c", "Card", "0x267dd27200a91c80", 1);
        assert_eq!(l.first_block_hash, "");
        let (h, daa, n) = parse_node_accepted(NODE).unwrap();
        l.on_node_accepted(&h, daa, &n);
        assert_eq!(l.blocks[0].hash, h);
        assert_eq!(l.first_block_hash, h);
        assert_eq!(l.milestone.as_ref().unwrap().hash, h);
        assert_eq!(l.milestone.as_ref().unwrap().daa, 38637);
    }

    #[test]
    fn the_cards_come_at_1_100_1000_10000_and_on_a_new_card() {
        let mut l = Ladder::default();
        let mut kinds = vec![];
        for total in 1..=1_000u64 {
            if let Some(m) = l.on_block(1000.0 + total as f64, "a", "A", "", total) { kinds.push((total, m.kind)); }
        }
        assert_eq!(kinds, vec![(1, "first".to_string()), (100, "hundred".to_string()), (1000, "thousand".to_string())]);
        assert_eq!(l.on_block(5000.0, "b", "B", "", 1001).unwrap().kind, "card");
        assert!(l.on_block(5001.0, "b", "B", "", 1002).is_none());
        assert_eq!(l.on_block(5002.0, "a", "A", "", 10_000).unwrap().kind, "ten_thousand");
        assert_eq!(l.on_block(5003.0, "a", "A", "", 20_000).unwrap().kind, "ten_thousand");
        assert_eq!(l.blocks.len(), KEEP_BLOCKS);
    }

    #[test]
    fn days_mined_counts_distinct_days_inside_the_window_only() {
        let mut l = Ladder::default();
        let now = 40.0 * DAY_S + 100.0;
        // blocks on days 5, 20, 20, 39 and 40 (today); day 5 is outside the 30-day window
        for (d, total) in [(5.0, 1), (20.0, 2), (20.0, 3), (39.0, 4), (40.0, 5)] {
            l.on_block(d * DAY_S + 50.0, "a", "A", "", total);
        }
        let s = l.state(now, 600.0);
        assert_eq!(s.days_mined_30, 3);
        assert_eq!(s.blocks_30d, 4);
        assert_eq!(s.blocks_today, 1);
        assert_eq!(s.days.len(), 3);
    }

    #[test]
    fn the_signing_streak_holds_across_votes_and_breaks_on_a_gap() {
        let mut l = Ladder::default();
        l.on_vote(1000.0, 10, 600.0);
        l.on_vote(1030.0, 11, 600.0);
        l.on_vote(1060.0, 12, 600.0);
        assert_eq!(l.streak_s(1090.0, 600.0), 90.0);
        assert_eq!(l.votes_signed, 3);
        assert_eq!(l.last_vote_index, 12);
        // the lock at 11 puts 10 and 11 in locked certificates; 12 stays open
        l.on_lock(11);
        assert_eq!(l.last_signed_locked, 11);
        assert_eq!(l.signed_locked_count, 2);
        assert_eq!(l.signed_open, vec![12]);
        // a gap longer than the presence window ends the streak; the next vote starts a new one
        assert_eq!(l.streak_s(1700.0, 600.0), 0.0);
        l.on_vote(1700.0, 33, 600.0);
        assert_eq!(l.streak_s(1730.0, 600.0), 30.0);
        assert_eq!(l.state(1730.0, 600.0).streak_s, 30.0);
    }

    #[test]
    fn the_milestone_events_read_in_the_users_words() {
        let m = |kind: &str, count: u64| Milestone { kind: kind.into(), count, card: "a".into(), card_name: "RTX 4070".into(), hash: String::new(), daa: 0, at: 0.0 };
        assert_eq!(milestone_event(&m("first", 1)), "first block: found by RTX 4070 (block card)");
        assert_eq!(milestone_event(&m("hundred", 100)), "block 100 of this machine: found by RTX 4070 (block card)");
        assert_eq!(milestone_event(&m("ten_thousand", 20_000)), "block 20,000 of this machine: found by RTX 4070 (block card)");
        assert_eq!(milestone_event(&m("card", 7)), "first block on RTX 4070 (block card)");
    }

    #[test]
    fn the_timeline_marks_are_set_once() {
        let mut l = Ladder::default();
        assert!(l.mark("synced", 10.0));
        assert!(!l.mark("synced", 20.0));
        assert!(l.mark("mining", 30.0));
        assert!(!l.mark("other", 40.0));
        let s = l.state(50.0, 600.0);
        assert_eq!(s.first_synced_at, 10.0);
        assert_eq!(s.first_mining_at, 30.0);
    }

    #[test]
    fn paid_shards_are_kept_once_each_newest_first() {
        let mut l = Ladder::default();
        l.on_shard_paid(10.0, 1_284_117, 3, 1_150_000_000_000_000_000);
        l.on_shard_paid(11.0, 1_284_117, 3, 1_150_000_000_000_000_000);
        l.on_shard_paid(12.0, 1_284_120, 0, 2_000_000_000_000_000_000);
        assert_eq!(l.shards.len(), 2);
        assert_eq!(l.shards[0].block, 1_284_120);
        assert_eq!(l.shards[1].wei, "1150000000000000000");
    }

    #[test]
    fn the_record_survives_a_save_and_load() {
        let mut l = Ladder::default();
        l.on_block(100.0, "a", "A", "0x1", 1);
        l.on_vote(130.0, 5, 600.0);
        let dir = std::env::temp_dir().join(format!("igneum-ladder-test-{}", std::process::id()));
        let path = dir.join("ladder.json");
        l.save(&path);
        let back = Ladder::load(&path);
        assert_eq!(back.first_block_at, 100.0);
        assert_eq!(back.votes_signed, 1);
        assert_eq!(back.blocks, l.blocks);
        let _ = std::fs::remove_dir_all(&dir);
        assert_eq!(Ladder::load(&dir.join("missing.json")).votes_signed, 0);
    }

    // ---- first-block-21 (the founder, 7 October 2026): the first-block card SEEN once in the app's life ----

    /// The record as an earlier release wrote it after the first block: the milestone persisted, no flag field, no schema.
    const RECORD_0320: &str = r#"{"first_block_at":1791140918.5,"first_block_card":"nvidia:0:RTX 4070","first_block_hash":"28abaa8c28ffb4c3eab96b5dc202f8328df7038dc7b2653c9e4b7dd6369ae5ce","first_block_daa":38637,"days":[{"day":20730,"blocks":1}],"cards_first":{"nvidia:0:RTX 4070":1791140918.5},"blocks":[{"hash":"28abaa8c28ffb4c3eab96b5dc202f8328df7038dc7b2653c9e4b7dd6369ae5ce","daa":38637,"nonce":"267dd27200a91c80","at":1791140918.5,"card":"nvidia:0:RTX 4070","card_name":"NVIDIA GeForce RTX 4070","count":1}],"votes_signed":2,"last_vote_index":8496,"last_vote_at":1791140950.0,"signed_open":[8496],"last_signed_locked":0,"signed_locked_count":0,"streak_since":1791140930.0,"shards":[],"milestone":{"kind":"first","count":1,"card":"nvidia:0:RTX 4070","card_name":"NVIDIA GeForce RTX 4070","hash":"28abaa8c28ffb4c3eab96b5dc202f8328df7038dc7b2653c9e4b7dd6369ae5ce","daa":38637,"at":1791140918.5},"first_synced_at":1791140000.0,"first_mining_at":1791140100.0,"pending":[]}"#;

    fn round_trip(l: &Ladder) -> Ladder {
        // one directory per call: the tests run in parallel and a shared path let one test's remove wipe another's file
        static N: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let dir = std::env::temp_dir().join(format!("igneum-ladder-first-{}-{}", std::process::id(), N.fetch_add(1, std::sync::atomic::Ordering::SeqCst)));
        let path = dir.join("ladder.json");
        l.save(&path);
        let back = Ladder::load(&path);
        let _ = std::fs::remove_dir_all(&dir);
        back
    }

    #[test]
    fn known_failed_a_second_run_with_the_lifetime_count_at_1_showed_the_first_card_again() {
        // the known-failed case: before this change a second run loaded the persisted milestone and the dashboard
        // showed "your first block" again (every restart, every update, until the dismiss in that window's storage)
        let mut l: Ladder = serde_json::from_str(RECORD_0320).unwrap();
        assert_eq!(l.milestone.as_ref().map(|m| m.kind.as_str()), Some("first"), "the fixture carries the persisted card");
        assert!(!l.first_block_shown, "the 0.1.20 record has no flag");
        assert_eq!(l.schema, 0);
        assert!(l.start_run(1), "the record changes and wants saving");
        let s = l.state(1791150000.0, 600.0);
        assert!(s.milestone.is_none(), "a card 0.1.20 showed on every run is never shown again");
        assert!(s.first_block_shown);
        assert_eq!(s.first_block_at, 1791140918.5, "the ladder's first-block facts stay");
        assert_eq!(l.schema, 1);
    }

    #[test]
    fn the_first_ever_block_raises_the_card_and_a_window_showing_it_sets_the_flag() {
        let mut l = Ladder::default();
        l.start_run(0);
        let m = l.on_block(100.0, "a", "RTX 4070", "0x1", 1).unwrap();
        assert_eq!(m.kind, "first");
        assert!(!l.first_block_shown, "raised, not yet seen");
        assert!(!l.card_seen(7, 100.0), "another card's report changes nothing");
        assert!(l.card_seen(1, 100.0));
        assert!(l.first_block_shown);
        assert!(!l.card_seen(1, 100.0), "a second report (the dismiss) changes nothing");
        let s = l.state(101.0, 600.0);
        assert!(s.first_block_shown);
        assert_eq!(s.milestone.as_ref().unwrap().kind, "first", "the card stays on screen through this run");
    }

    #[test]
    fn a_first_block_found_with_no_window_greets_the_miner_at_the_first_open_and_never_again() {
        // the project's case: the block overnight (no window), an engine restart (an auto-update), the first open shows the
        // card once; a second open (the next run) does not
        let mut night = Ladder::default();
        night.start_run(0);
        night.on_block(100.0, "a", "RTX 4070", "0x1", 1);
        let mut morning = round_trip(&night);
        assert!(!morning.start_run(1), "an unseen card waits: nothing changes at start");
        let s = morning.state(30_000.0, 600.0);
        assert_eq!(s.milestone.as_ref().unwrap().kind, "first", "the first open shows the card");
        assert!(!s.first_block_shown);
        assert!(morning.card_seen(1, 100.0));
        assert!(morning.first_block_shown);
        let mut later = round_trip(&morning);
        assert!(later.start_run(1), "the seen card is dropped");
        assert!(later.state(60_000.0, 600.0).milestone.is_none(), "a second open shows nothing");
        assert!(later.first_block_shown);
        assert!(later.on_block(60_100.0, "a", "RTX 4070", "0x2", 2).is_none());
    }

    #[test]
    fn a_restart_with_the_count_at_1_shows_nothing_and_block_2_raises_no_card() {
        let mut run1 = Ladder::default();
        run1.on_block(100.0, "a", "RTX 4070", "0x1", 1);
        assert!(run1.card_seen(1, 100.0), "the window was open");
        let mut run2 = round_trip(&run1);
        assert!(run2.first_block_shown, "the flag survives the save");
        assert_eq!(run2.schema, 1);
        assert!(run2.start_run(1), "the seen card is dropped");
        assert!(run2.state(200.0, 600.0).milestone.is_none());
        assert!(run2.on_block(300.0, "a", "RTX 4070", "0x2", 2).is_none(), "block 2 is an ordinary block");
        assert_eq!(run2.blocks[0].count, 2);
    }

    #[test]
    fn an_update_with_the_count_at_1_takes_the_flag_from_the_older_record_shape() {
        // the new version opens a an earlier release record: the flag is set from the count, the card dropped, and the flag
        // survives the next save in the new shape
        let mut l: Ladder = serde_json::from_str(RECORD_0320).unwrap();
        assert!(l.start_run(1));
        assert!(l.first_block_shown);
        assert!(l.milestone.is_none());
        let mut back = round_trip(&l);
        assert!(back.first_block_shown);
        assert_eq!(back.schema, 1);
        assert!(!back.start_run(1), "a new-shape record with nothing pending changes nothing");
        assert!(back.on_block(1791150000.0, "nvidia:0:RTX 4070", "NVIDIA GeForce RTX 4070", "0x3", 2).is_none());
        // a an earlier release record whose count is still 0 but whose first_block_at is set (settings.json rewritten) is shown too
        let mut l2: Ladder = serde_json::from_str(RECORD_0320).unwrap();
        assert!(l2.start_run(0));
        assert!(l2.first_block_shown);
        assert!(l2.milestone.is_none());
    }

    #[test]
    fn a_kept_data_directory_with_the_count_at_0_and_the_flag_unset_shows_it_on_the_first_block() {
        // a kept directory from a machine that synced and voted but never found a block
        let mut l = Ladder::default();
        l.mark("synced", 10.0);
        l.mark("mining", 20.0);
        l.on_vote(30.0, 5, 600.0);
        let mut kept = round_trip(&l);
        assert!(!kept.start_run(0), "nothing to reconcile: no block yet");
        assert!(!kept.first_block_shown);
        let m = kept.on_block(100.0, "a", "RTX 4070", "0x1", 1).unwrap();
        assert_eq!(m.kind, "first");
        assert!(kept.card_seen(1, 100.0));
        assert!(kept.first_block_shown);
    }

    #[test]
    fn a_count_that_starts_over_on_a_record_already_shown_raises_no_first_card() {
        // settings.json lost (the count back at 0), the ladder kept with the flag: the next block is total 1 again
        let mut l = Ladder::default();
        l.on_block(100.0, "a", "RTX 4070", "0x1", 1);
        l.card_seen(1, 100.0);
        assert!(l.on_block(200.0, "a", "RTX 4070", "0x2", 1).is_none(), "no second first-block card");
        assert!(l.on_block(300.0, "b", "RTX 5090", "0x3", 1).is_none(), "nor a new-card card at a count of 1");
        assert!(l.first_block_shown);
        assert_eq!(l.state(301.0, 600.0).blocks.len(), 3, "the blocks are still recorded");
        // the same with the card still unseen: the waiting card stays the one card, no second is raised
        let mut u = Ladder::default();
        u.on_block(100.0, "a", "RTX 4070", "0x1", 1);
        assert!(u.on_block(200.0, "a", "RTX 4070", "0x2", 1).is_none());
        assert_eq!(u.milestone.as_ref().unwrap().at, 100.0);
    }

    #[test]
    fn block_2_is_an_ordinary_block_with_no_card() {
        let mut l = Ladder::default();
        l.on_block(100.0, "a", "RTX 4070", "0x1", 1);
        l.card_seen(1, 100.0);
        assert!(l.on_block(130.0, "a", "RTX 4070", "0x2", 2).is_none());
        let s = l.state(131.0, 600.0);
        assert_eq!(s.blocks[0].count, 2);
        assert_eq!(s.blocks_today, 2);
        assert_eq!(s.milestone.as_ref().unwrap().count, 1, "the first card of this run stays until the run ends");
    }
}

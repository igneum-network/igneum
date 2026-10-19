//! The economics display (design section 2.3): estimated receipts, electricity, disclosed fees, other entered
//! costs; estimates distinct from settled payouts; negative outcomes shown.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Window {
    /// Minutes from midnight, inclusive.
    pub start_min: u32,
    /// Minutes from midnight, exclusive; at or before `start_min` means the window wraps midnight.
    pub end_min: u32,
    pub rate: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Tariff {
    Flat { rate: f64 },
    Schedule { windows: Vec<Window> },
}

impl Default for Tariff {
    fn default() -> Self {
        Tariff::Flat { rate: 0.0 }
    }
}

pub const MAX_WINDOWS: usize = 8;

impl Tariff {
    pub fn validate(&self) -> Result<(), String> {
        match self {
            Tariff::Flat { rate } => {
                if !rate.is_finite() || *rate < 0.0 || *rate > 100.0 {
                    return Err("the rate must be between 0 and 100 per kWh".into());
                }
            }
            Tariff::Schedule { windows } => {
                if windows.is_empty() || windows.len() > MAX_WINDOWS {
                    return Err(format!("between 1 and {MAX_WINDOWS} windows"));
                }
                for w in windows {
                    if w.start_min >= 1440 || w.end_min > 1440 || !w.rate.is_finite() || w.rate < 0.0 || w.rate > 100.0 {
                        return Err("a window needs minutes inside the day and a rate between 0 and 100".into());
                    }
                }
            }
        }
        Ok(())
    }

    /// The rate at a minute of the day. Minutes no window covers cost the mean of the windows (and the display
    /// says so), never zero.
    pub fn rate_at(&self, minute: u32) -> f64 {
        match self {
            Tariff::Flat { rate } => *rate,
            Tariff::Schedule { windows } => {
                let m = minute % 1440;
                for w in windows {
                    let inside = if w.end_min > w.start_min { m >= w.start_min && m < w.end_min } else { m >= w.start_min || m < w.end_min };
                    if inside {
                        return w.rate;
                    }
                }
                if windows.is_empty() {
                    0.0
                } else {
                    windows.iter().map(|w| w.rate).sum::<f64>() / windows.len() as f64
                }
            }
        }
    }

    /// The mean rate over a day, minute by minute.
    pub fn mean_rate(&self) -> f64 {
        match self {
            Tariff::Flat { rate } => *rate,
            Tariff::Schedule { .. } => (0..1440).map(|m| self.rate_at(m)).sum::<f64>() / 1440.0,
        }
    }

    pub fn has_gaps(&self) -> bool {
        match self {
            Tariff::Flat { .. } => false,
            Tariff::Schedule { windows } => (0..1440).any(|m| {
                !windows.iter().any(|w| if w.end_min > w.start_min { m >= w.start_min && m < w.end_min } else { m >= w.start_min || m < w.end_min })
            }),
        }
    }
}

/// The chain's figures a pool estimate stands on (F-E20): the network's hash rate (hashes a second), the mean block
/// time (seconds) and the payees' part of a block (IGN, the issuance less the fund's part), with the read's minute.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ChainFigures {
    pub network_hps: f64,
    pub block_time_s: f64,
    pub payees_ign_per_block: f64,
    /// Where the figures came from, for the note ("the chain's feed", "the pool's notify").
    pub source: String,
    pub read_at_unix: u64,
}

/// F-E25 (E2, 14 October 2026): the ledger's paid reading for the payout address on the pool's chain (the Live Work
/// Ledger's miner view), read by the supervisor's own poll so the command line carries it: the paid total, the
/// block count, and the last paid block's figures.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct LedgerPaid {
    pub chain_id: u64,
    pub paid_ign: f64,
    pub blocks: u64,
    pub last_height: u64,
    pub last_paid_ign: f64,
    pub last_carried: u64,
    pub last_weight: u64,
    pub last_list_total: u64,
    pub record: String,
    pub read_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Inputs {
    /// Validated work rate, hashes per second (mean over the last 10 minutes).
    pub rate_hps: f64,
    /// The worker-reported rate (hashes per second) beside the validated one, for the receipts note.
    pub reported_hps: f64,
    /// The dev fee in force, percent; None when it is off.
    pub dev_fee_percent: Option<u8>,
    /// The payout address's balance on the chain in IGN, when the app has read it (eth_getBalance).
    pub balance_ign: Option<f64>,
    /// Expected hashes per block.
    pub difficulty: f64,
    pub block_interval_s: f64,
    /// Block subsidy at the current height, IGN; none when the source does not report it yet.
    pub subsidy_ign: Option<f64>,
    /// The miner's share of `subsidy_ign`: 1.0 when the source already reports the miner's part after the
    /// protocol allocation (`igneum_blockReward.miner`); 0.99 when it reports the gross subsidy (P-I04).
    pub miner_share: f64,
    /// Mean priority fees per block over recent blocks, IGN.
    pub fees_ign: f64,
    /// Pool fee as a fraction; 0 for solo; none when the pool's disclosure document was not read.
    pub pool_fee: Option<f64>,
    pub solo: bool,
    /// The protocol fund's part of the newest block as the chain reports it, IGN (the consensus rule, 1 percent
    /// of each block; never a setting).
    pub fund_ign: f64,
    /// The user's request to the pool to waive its own fee (a pool-side matter; the protocol fund is not optional).
    pub no_pool_fee: bool,
    /// F-E20 (E2, 14 October 2026): the chain's own figures for a pool estimate. On the share lane the job's boundary
    /// is the SHARE boundary at the worker's step, so hashes over that boundary count shares, never blocks; a pool
    /// estimate stands on the miner's share of the network's rate, the chain's block time and the payees' IGN a
    /// block, read from the chain's feed (or the notify when the pool carries them). None: no figures yet, no number.
    #[serde(default)]
    pub chain: Option<ChainFigures>,
    /// F-E25: the ledger's paid reading, when read.
    #[serde(default)]
    pub ledger: Option<LedgerPaid>,
    /// The user's meter reading for the whole machine, when entered.
    pub wall_watts: Option<f64>,
    /// Sum of device telemetry power.
    pub device_watts: f64,
    /// Host allowance added to device telemetry when no meter reading exists (PA-05).
    pub host_allowance_w: f64,
    pub tariff: Tariff,
    /// Price of one IGN in the user's currency; none means IGN only.
    pub price_per_ign: Option<f64>,
    pub other_per_day: f64,
    pub currency: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum PowerSource {
    Meter,
    Estimated { device_w: f64, allowance_w: f64 },
    None,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Estimate {
    /// Solo: blocks a day over the chain's boundary. On a pool this is SHARES a day over the share boundary
    /// (F-E20: the job's boundary is the share boundary), named so in the lines; the receipts never multiply it.
    pub blocks_per_day: f64,
    /// A pool estimate's standing (F-E20): the miner's share of the network's rate and the chain's blocks a day,
    /// when the chain's figures are known.
    #[serde(default)]
    pub share_of_network: Option<f64>,
    #[serde(default)]
    pub chain_blocks_per_day: Option<f64>,
    pub receipts_ign: f64,
    pub receipts_ccy: Option<f64>,
    pub electricity_ccy: f64,
    pub pool_fee_ign: f64,
    pub pool_fee_ccy: Option<f64>,
    pub other_ccy: f64,
    pub net_ccy: Option<f64>,
    pub time_to_block_s: Option<f64>,
    pub p_block_30d: f64,
    pub wall_watts: f64,
    pub power_source: PowerSource,
    pub mean_rate_per_kwh: f64,
}

/// The arithmetic from design section 2.3, exactly.
pub fn estimate(i: &Inputs) -> Estimate {
    let h = i.rate_hps.max(0.0);
    let d = if i.difficulty > 0.0 { i.difficulty } else { f64::INFINITY };
    let blocks_per_day = if d.is_finite() { h / d * 86400.0 } else { 0.0 };
    let per_block = i.subsidy_ign.unwrap_or(0.0) * i.miner_share + i.fees_ign;
    // F-E20: a pool estimate stands on the chain's figures (the miner's share of the network's rate, the chain's
    // blocks a day, the payees' IGN a block); without them a pool shows no number (the share boundary's count
    // multiplied by a block's pay read 134,991,635.9 IGN a day for one card on E2's host)
    let (share_of_network, chain_blocks_per_day) = match (&i.chain, i.solo) {
        (Some(c), false) if c.network_hps > 0.0 && c.block_time_s > 0.0 => (Some((h / c.network_hps).min(1.0)), Some(86400.0 / c.block_time_s)),
        _ => (None, None),
    };
    let gross_ign = if i.solo {
        blocks_per_day * per_block
    } else {
        match (&i.chain, share_of_network, chain_blocks_per_day) {
            (Some(c), Some(sh), Some(b)) => sh * b * c.payees_ign_per_block,
            _ => 0.0,
        }
    };
    let fee = if i.solo { 0.0 } else { i.pool_fee.unwrap_or(0.0).clamp(0.0, 1.0) };
    let receipts_ign = gross_ign * (1.0 - fee);
    let pool_fee_ign = gross_ign * fee;

    let (wall_watts, power_source) = match i.wall_watts {
        Some(w) if w > 0.0 => (w, PowerSource::Meter),
        _ if i.device_watts > 0.0 || i.host_allowance_w > 0.0 => (i.device_watts + i.host_allowance_w, PowerSource::Estimated { device_w: i.device_watts, allowance_w: i.host_allowance_w }),
        _ => (0.0, PowerSource::None),
    };
    // minute by minute over the day so a time-of-use tariff is summed, not averaged
    let electricity_ccy = match &i.tariff {
        Tariff::Flat { rate } => wall_watts * 24.0 / 1000.0 * rate,
        t => (0..1440).map(|m| wall_watts / 1000.0 * t.rate_at(m) / 60.0).sum::<f64>(),
    };
    let p = i.price_per_ign.filter(|p| p.is_finite() && *p >= 0.0);
    let receipts_ccy = p.map(|p| receipts_ign * p);
    let pool_fee_ccy = p.map(|p| pool_fee_ign * p);
    let other_ccy = i.other_per_day.max(0.0);
    let net_ccy = receipts_ccy.map(|r| r - electricity_ccy - other_ccy);
    let time_to_block_s = if h > 0.0 && d.is_finite() { Some(d / h) } else { None };
    let p_block_30d = 1.0 - (-30.0 * blocks_per_day).exp();
    Estimate {
        blocks_per_day,
        share_of_network,
        chain_blocks_per_day,
        receipts_ign,
        receipts_ccy,
        electricity_ccy,
        pool_fee_ign,
        pool_fee_ccy,
        other_ccy,
        net_ccy,
        time_to_block_s,
        p_block_30d,
        wall_watts,
        power_source,
        mean_rate_per_kwh: i.tariff.mean_rate(),
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Line {
    pub eyebrow: String,
    pub label: String,
    pub value: String,
    pub note: String,
    pub negative: bool,
}

pub fn fmt_ccy(code: &str, v: f64) -> String {
    let sym = match code {
        "GBP" => "£",
        "USD" => "$",
        "EUR" => "€",
        _ => "",
    };
    let s = if v.abs() >= 100.0 { format!("{:.0}", v.abs()) } else { format!("{:.2}", v.abs()) };
    let sign = if v < 0.0 { "-" } else { "" };
    if sym.is_empty() {
        format!("{sign}{s} {code}")
    } else {
        format!("{sign}{sym}{s}")
    }
}

/// A hash rate in the unit that reads best (H/s, kH/s, MH/s, GH/s).
pub fn fmt_rate(hps: f64) -> String {
    if hps >= 1e9 { format!("{:.2} GH/s", hps / 1e9) }
    else if hps >= 1e6 { format!("{:.2} MH/s", hps / 1e6) }
    else if hps >= 1e3 { format!("{:.1} kH/s", hps / 1e3) }
    else { format!("{hps:.0} H/s") }
}

/// A share of the network as a percentage with the digits the size needs.
pub fn fmt_share(sh: f64) -> String {
    let p = sh * 100.0;
    if p >= 10.0 { format!("{p:.0}%") } else if p >= 1.0 { format!("{p:.1}%") } else if p >= 0.01 { format!("{p:.2}%") } else { format!("{p:.4}%") }
}

pub fn fmt_ign(v: f64) -> String {
    if v >= 100.0 {
        format!("{v:.1} IGN")
    } else if v >= 1.0 {
        format!("{v:.3} IGN")
    } else {
        format!("{v:.5} IGN")
    }
}

pub fn fmt_duration(s: f64) -> String {
    if !s.is_finite() {
        return "never at this rate".into();
    }
    if s < 90.0 {
        format!("about {:.0} seconds", s)
    } else if s < 5400.0 {
        format!("about {:.0} minutes", s / 60.0)
    } else if s < 2.0 * 86400.0 {
        format!("about {:.0} hours", s / 3600.0)
    } else if s < 400.0 * 86400.0 {
        format!("about {:.0} days", s / 86400.0)
    } else {
        format!("about {:.1} years", s / (365.25 * 86400.0))
    }
}

/// The Earnings page's lines, in the design's order and with its labels.
pub fn lines(i: &Inputs, e: &Estimate) -> Vec<Line> {
    let c = i.currency.as_str();
    let mut v = Vec::new();
    let receipts_value = match e.receipts_ccy {
        Some(x) => format!("{} a day", fmt_ccy(c, x)),
        None => format!("{} a day", fmt_ign(e.receipts_ign)),
    };
    let per_block = |r: f64| format!("{} a block to the miner{}", fmt_ign(r * i.miner_share), if i.fees_ign > 0.0 { format!(" plus {} in fees on average", fmt_ign(i.fees_ign)) } else { String::new() });
    let receipts_note = if !i.solo {
        // F-E20: the pool estimate's standing, in the chain's own figures; never the share count times a block's pay
        match (&i.chain, e.share_of_network, e.chain_blocks_per_day) {
            (Some(c), Some(sh), Some(b)) => format!(
                "{}your share of the network's rate is about {} ({} of {}); the chain pays about {} a block to the payees, about {:.0} blocks a day at {:.1} s ({}); about {:.0} shares a day at the pool's step, paid by step weight, not by count (a session held at a low step earns less than its share count suggests)",
                match e.receipts_ccy { Some(_) => format!("{} a day at your price; ", fmt_ign(e.receipts_ign)), None => String::new() },
                fmt_share(sh), fmt_rate(i.rate_hps), fmt_rate(c.network_hps), fmt_ign(c.payees_ign_per_block), b, c.block_time_s, c.source, e.blocks_per_day
            ),
            _ => "no chain figures yet (the network's rate, the block time and the payees' IGN a block come from the chain's feed within a minute); the pool's share boundary counts shares, not blocks, so no number is shown from it".into(),
        }
    } else {
        match (i.subsidy_ign, e.receipts_ccy) {
            (None, _) => "this source does not report the block subsidy yet, so receipts cannot be estimated".into(),
            (Some(r), Some(_)) => format!("{} a day at your price; {}", fmt_ign(e.receipts_ign), per_block(r)),
            (Some(r), None) => format!("{}; set a price per IGN in Settings to see this in money", per_block(r)),
        }
    };
    // the rate the estimate stands on, named: the validated rate is what the pool accepted (shares at its target);
    // the reported rate is the worker's own count; the gap is the share target's coarseness, not lost work
    let rate_words = if i.reported_hps > 0.0 {
        // F-E27 (E2, 14 October 2026): which rate is the owner's, and why the validated one moves
        format!("; your rate is the validated one, {}: the work the pool accepted as shares at its step, which is what it pays; the worker's own count is {} and stands for the card's speed; validated moves with the share step (a higher step, fewer and heavier shares, the same work), not with lost work", fmt_rate(i.rate_hps), fmt_rate(i.reported_hps))
    } else {
        String::new()
    };
    v.push(Line { eyebrow: "estimate".into(), label: "Estimated receipts".into(), value: receipts_value, note: format!("{receipts_note}{rate_words}"), negative: false });
    // F-E25: the paid-per-block reading from the ledger (the chain's balance steps at this address), on the
    // command line as in the app
    if let Some(l) = &i.ledger {
        let per_share = if l.last_carried > 0 { format!("; about {} a share at that block", fmt_ign(l.last_paid_ign / l.last_carried as f64)) } else { String::new() };
        v.push(Line {
            eyebrow: "chain".into(),
            label: "Paid".into(),
            value: format!("{} in {} block{}", fmt_ign(l.paid_ign), l.blocks, if l.blocks == 1 { "" } else { "s" }),
            note: format!("on chain {}: the last block {} paid {} for {} share{} of weight {} against the list's {}{}; the ledger's record {} read {}", l.chain_id, l.last_height, fmt_ign(l.last_paid_ign), l.last_carried, if l.last_carried == 1 { "" } else { "s" }, l.last_weight, l.last_list_total, per_share, l.record, l.read_at),
            negative: false,
        });
    }
    if let Some(b) = i.balance_ign {
        v.push(Line { eyebrow: "chain".into(), label: "Balance".into(), value: format!("{} IGN", fmt_ign(b)), note: "the payout address's balance on the chain (eth_getBalance through the node), the real receipts so far; the estimate above is a projection".into(), negative: false });
    }

    let (src, src_note) = match &e.power_source {
        PowerSource::Meter => ("your meter reading", String::new()),
        PowerSource::Estimated { device_w, allowance_w } => ("estimated from device readings", format!(": {device_w:.0} W from the devices plus {allowance_w:.0} W for the rest of the machine; measure at the wall for a real number")),
        PowerSource::None => ("no power reading yet", String::new()),
    };
    let gaps = if i.tariff.has_gaps() { "; hours outside your windows use the mean of your rates" } else { "" };
    v.push(Line {
        eyebrow: "estimate".into(),
        label: "Electricity".into(),
        value: format!("{} a day", fmt_ccy(c, e.electricity_ccy)),
        note: format!("{:.0} W at {} per kWh ({src}{src_note}){gaps}", e.wall_watts, fmt_ccy(c, e.mean_rate_per_kwh)),
        negative: e.electricity_ccy > 0.0,
    });

    if i.solo {
        v.push(Line { eyebrow: "estimate".into(), label: "Pool fee".into(), value: "Solo: no pool fee".into(), note: String::new(), negative: false });
    } else {
        let val = match e.pool_fee_ccy {
            Some(x) => format!("{} a day", fmt_ccy(c, x)),
            None => format!("{} a day", fmt_ign(e.pool_fee_ign)),
        };
        let note = match i.pool_fee {
            // F-E27: the pool's charged count can run a share or two ahead of Ember's receipts (a receipt follows
            // each accept; E2 read 1,242 charged against 1,240 receipts), so the counts are named as what they are
            Some(f) => format!("{:.2}% of receipts, as the pool states it, on every session (the dev fee's own too); the pool's charged count can exceed Ember's receipts by the shares accepted after the last receipt was read, never the other way", f * 100.0),
            None => "the pool's disclosure document could not be read, so its fee is not known".into(),
        };
        v.push(Line { eyebrow: "estimate".into(), label: "Pool fee".into(), value: val, note, negative: e.pool_fee_ign > 0.0 });
    }

    match i.dev_fee_percent {
        Some(p) if p > 0 => v.push(Line { eyebrow: "disclosed".into(), label: "Software fee".into(), value: format!("{p} percent"), note: format!("the dev fee: one template in {} is mined for the dev payout address from the signed manifest; --dev-fee 0 (or dev_fee = 0 in the config file) turns it off", if p > 0 { 100 / p as u32 } else { 0 }), negative: true }),
        _ => v.push(Line { eyebrow: "disclosed".into(), label: "Software fee".into(), value: "none".into(), note: "the dev fee is off; this software takes nothing from your work".into(), negative: false }),
    }
    {
        let (value, note) = if i.fund_ign > 0.0 {
            let total = i.subsidy_ign.unwrap_or(0.0) + i.fund_ign;
            let pct = if total > 0.0 { i.fund_ign / total * 100.0 } else { 0.0 };
            (format!("{:.4} IGN a block ({pct:.1} percent)", i.fund_ign), "protocol fund, 1 percent of each block, by consensus; read from the chain per block (igneum_blockReward.fund), not optional".to_string())
        } else {
            ("1 percent of each block, by consensus".to_string(), "protocol fund; the chain's rule, read per block once the source reports it".to_string())
        };
        v.push(Line { eyebrow: "chain".into(), label: "Protocol fund".into(), value, note, negative: false });
    }
    if i.no_pool_fee && !i.solo {
        v.push(Line { eyebrow: "requested".into(), label: "Pool fee waiver".into(), value: "requested".into(), note: "asked of the pool at sign-in; the pool's own accounting decides, and the pool fee line above shows what it charges".into(), negative: false });
    }

    v.push(Line { eyebrow: "yours".into(), label: "Other costs (yours)".into(), value: format!("{} a day", fmt_ccy(c, e.other_ccy)), note: "wear, rent, anything you entered in Settings".into(), negative: e.other_ccy > 0.0 });

    match e.net_ccy {
        Some(n) => {
            let note = if n < 0.0 { format!("Mining this at this tariff loses about {} a day", fmt_ccy(c, -n)) } else { "receipts less electricity, fees and your other costs".into() };
            v.push(Line { eyebrow: "estimate".into(), label: "Estimated net".into(), value: format!("{} a day", fmt_ccy(c, n)), note, negative: n < 0.0 });
        }
        None => {
            v.push(Line { eyebrow: "estimate".into(), label: "Estimated net".into(), value: "needs a price".into(), note: "set a price per IGN to compare receipts with costs".into(), negative: false });
        }
    }

    if i.solo {
        let value = match e.time_to_block_s {
            Some(t) => format!("one block every {}", fmt_duration(t).trim_start_matches("about ")),
            None => "no validated work yet".into(),
        };
        v.push(Line {
            eyebrow: "solo".into(),
            label: "Luck".into(),
            value,
            note: format!("the chance of at least one block in 30 days is {:.0}%; actual luck varies", e.p_block_30d * 100.0),
            negative: false,
        });
    }
    v
}

#[cfg(test)]
mod tests {
    use super::*;

    fn base() -> Inputs {
        Inputs {
            rate_hps: 0.0,
            reported_hps: 0.0,
            dev_fee_percent: None,
            balance_ign: None,
            difficulty: 1e12,
            block_interval_s: 15.0,
            subsidy_ign: Some(10.0),
            miner_share: 0.99,
            fees_ign: 0.1,
            pool_fee: Some(0.01),
            solo: false,
            fund_ign: 0.1,
            no_pool_fee: false,
            chain: None,
            ledger: None,
            wall_watts: Some(150.0),
            device_watts: 0.0,
            host_allowance_w: 40.0,
            tariff: Tariff::Flat { rate: 0.20 },
            price_per_ign: None,
            other_per_day: 0.0,
            currency: "USD".into(),
        }
    }

    #[test]
    fn the_plans_illustration_150w_at_20c_is_72c_a_day() {
        let e = estimate(&base());
        assert!((e.electricity_ccy - 0.72).abs() < 1e-9, "{}", e.electricity_ccy);
        assert_eq!(e.power_source, PowerSource::Meter);
    }

    #[test]
    fn the_ledgers_paid_reading_is_a_line_on_the_command_line_too() {
        let mut i = base();
        i.ledger = Some(LedgerPaid { chain_id: 7320, paid_ign: 10253.28, blocks: 3968, last_height: 5083, last_paid_ign: 5.8959, last_carried: 62, last_weight: 2826240, last_list_total: 3771478, record: "20261014T022039Z".into(), read_at: "2026-10-14T02:20:39Z".into() });
        let e = estimate(&i);
        let l = lines(&i, &e).into_iter().find(|l| l.label == "Paid").expect("the Paid line");
        assert_eq!(l.value, "10253.3 IGN in 3968 blocks");
        assert!(l.note.contains("on chain 7320: the last block 5083 paid 5.896 IGN for 62 shares of weight 2826240 against the list's 3771478; about 0.09510 IGN a share"), "{}", l.note);
        assert!(l.note.contains("record 20261014T022039Z"));
    }

    #[test]
    fn receipts_follow_rate_difficulty_subsidy_and_fee() {
        let mut i = base();
        i.solo = true;
        i.pool_fee = Some(0.0);
        i.rate_hps = 1e9;
        let e = estimate(&i);
        let blocks = 1e9 / 1e12 * 86400.0;
        assert!((e.blocks_per_day - blocks).abs() < 1e-9);
        let gross = blocks * (10.0 * 0.99 + 0.1);
        assert!((e.receipts_ign - gross).abs() < 1e-9);
        i.solo = false;
        i.pool_fee = Some(0.01);
        // F-E20: on a pool the boundary is the share boundary; without the chain's figures there is no number
        let e = estimate(&i);
        assert_eq!(e.receipts_ign, 0.0);
        assert!(e.share_of_network.is_none());
        let note = lines(&i, &e).iter().find(|l| l.label == "Estimated receipts").unwrap().note.clone();
        assert!(note.contains("no chain figures yet") && note.contains("counts shares, not blocks"), "{note}");
        // with them: the share of the network's rate times the chain's blocks a day times the payees' IGN a block
        i.chain = Some(ChainFigures { network_hps: 1e11, block_time_s: 18.6, payees_ign_per_block: 7.868, source: "the chain's feed".into(), read_at_unix: 0 });
        let e = estimate(&i);
        let gross = (1e9 / 1e11) * (86400.0 / 18.6) * 7.868;
        assert!((e.receipts_ign - gross * 0.99).abs() < 1e-9, "{} vs {}", e.receipts_ign, gross * 0.99);
        assert!((e.share_of_network.unwrap() - 0.01).abs() < 1e-12);
        let note = lines(&i, &e).iter().find(|l| l.label == "Estimated receipts").unwrap().note.clone();
        assert!(note.contains("your share of the network's rate is about 1.0%") && note.contains("7.868 IGN a block") && note.contains("4645 blocks a day") && note.contains("shares a day at the pool's step, paid by step weight, not by count"), "{note}");
        // E2's figures (14 October 2026): one 3060 at 15 MH/s over a share boundary of 75 read 17,330,760 "blocks" a day
        i.chain = None;
        i.rate_hps = 15e6;
        i.difficulty = 75.0;
        let e = estimate(&i);
        assert!(e.blocks_per_day > 1e7, "the share count stands as shares: {}", e.blocks_per_day);
        assert_eq!(e.receipts_ign, 0.0, "never 134,991,635.9 IGN a day");
        i.solo = true;
        i.pool_fee = Some(0.0);
        i.rate_hps = 1e9;
        i.difficulty = 1e12;
        i.chain = None;
        let e = estimate(&i);
        let blocks = 1e9 / 1e12 * 86400.0;
        let gross = blocks * (10.0 * 0.99 + 0.1);
        i.solo = false;
        i.pool_fee = Some(0.01);
        i.chain = Some(ChainFigures { network_hps: 1e9 / (blocks / (86400.0 / 15.0)), block_time_s: 15.0, payees_ign_per_block: 10.0 * 0.99 + 0.1, source: "test".into(), read_at_unix: 0 });
        let e = estimate(&i);
        assert!((e.receipts_ign - gross * 0.99).abs() < 1e-9);
        assert!((e.pool_fee_ign - gross * 0.01).abs() < 1e-9);
        assert!(e.net_ccy.is_none());
        i.price_per_ign = Some(2.0);
        let e = estimate(&i);
        assert!((e.net_ccy.unwrap() - (gross * 0.99 * 2.0 - 0.72)).abs() < 1e-9);
    }

    #[test]
    fn negative_net_is_shown_not_hidden() {
        let mut i = base();
        i.rate_hps = 1.0;
        i.price_per_ign = Some(1.0);
        let e = estimate(&i);
        let l = lines(&i, &e);
        let net = l.iter().find(|x| x.label == "Estimated net").unwrap();
        assert!(net.negative);
        assert!(net.note.starts_with("Mining this at this tariff loses"));
        assert!(l.iter().any(|x| x.label == "Software fee" && x.value == "none"));
    }

    #[test]
    fn estimated_power_adds_the_allowance_and_says_so() {
        let mut i = base();
        i.wall_watts = None;
        i.device_watts = 110.0;
        let e = estimate(&i);
        assert_eq!(e.wall_watts, 150.0);
        assert!(matches!(e.power_source, PowerSource::Estimated { .. }));
        let l = lines(&i, &e);
        assert!(l[1].note.contains("estimated from device readings"));
    }

    #[test]
    fn time_of_use_is_summed_minute_by_minute() {
        let mut i = base();
        i.tariff = Tariff::Schedule { windows: vec![Window { start_min: 0, end_min: 420, rate: 0.10 }, Window { start_min: 420, end_min: 1440, rate: 0.30 }] };
        let e = estimate(&i);
        let expected = 0.150 * (7.0 * 0.10 + 17.0 * 0.30);
        assert!((e.electricity_ccy - expected).abs() < 1e-6, "{}", e.electricity_ccy);
        assert!(!i.tariff.has_gaps());
        let wrap = Tariff::Schedule { windows: vec![Window { start_min: 1380, end_min: 60, rate: 0.05 }] };
        assert_eq!(wrap.rate_at(1400), 0.05);
        assert_eq!(wrap.rate_at(30), 0.05);
        assert!(wrap.has_gaps());
        assert_eq!(wrap.rate_at(720), 0.05);
        assert!(Tariff::Schedule { windows: vec![] }.validate().is_err());
    }

    #[test]
    fn solo_luck_line() {
        let mut i = base();
        i.solo = true;
        i.rate_hps = 1e6;
        let e = estimate(&i);
        assert!((e.time_to_block_s.unwrap() - 1e6).abs() < 1e-6);
        let l = lines(&i, &e);
        assert!(l.iter().any(|x| x.label == "Luck" && x.value == "one block every 12 days"), "{:?}", l.iter().find(|x| x.label == "Luck"));
        assert!(l.iter().any(|x| x.label == "Pool fee" && x.value.starts_with("Solo")));
    }

    #[test]
    fn money_formats() {
        assert_eq!(fmt_ccy("GBP", 1.234), "£1.23");
        assert_eq!(fmt_ccy("USD", -0.5), "-$0.50");
        assert_eq!(fmt_ccy("SEK", 123.4), "123 SEK");
        assert_eq!(fmt_duration(100.0), "about 2 minutes");
    }
}

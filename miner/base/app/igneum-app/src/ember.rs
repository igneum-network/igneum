//! Ember Tune: every card tuned for MH per watt out of the box, and the fleet's results folded into a prior that a
//! new card starts from (docs/plans/ember-tune.md). Two knobs per card: the power limit (percent of the card's
//! default) and the core clock cap (MHz; 0 = unlocked). The memory clock is never touched, and a step that drags it
//! down is marked and cannot win. This file is the logic, driven by an explicit clock so the tests run without a
//! card: the plans (full, confirm, baseline), the per-step rows with their marks, the choice rule, the fleet record,
//! the prior lookup and the state machine. The engine (src/engine.rs, `tick_tune` and the `Cmd::Tune*` commands)
//! owns the processes: NVIDIA through nvidia-smi (`-pl`, `-lgc 0,<mhz>`, `-rgc`; administrator rights, so only with
//! Power control on), AMD through igneum-gpu-telemetry (`--set-plimit`, `--set-gmax`, `--reset`; no elevation on
//! Windows), Apple measure only.
//!
//! The lines in the app log (and on stdout under --sweep, which the PC job reads):
//!   TUNE start card=<label> name=<name> plan=full|confirm|baseline steps=<n> clock_max=<MHz> default=<W> ...
//!   TUNE card=<label> step=<i> clock=<MHz|0> cap=<pct> limit=<W> watts=<W> mhs=<x> eff=<MH/W> gclk=<MHz> mclk=<MHz> tmax=<C> mark=ok|...
//!   TUNE chosen card=<label> clock=<MHz|0> cap=<pct> limit=<W> watts=<W> mhs=<x> eff=<MH/W>
//!   TUNE {json}            the fleet record (one per finished plan; the intake receives it with the log upload)
//!   TUNE aborted card=<label> reason=<text>

use std::time::{Duration, Instant};

/// The power steps, percent of the card's default limit, highest first (the same ladder as src/sweep.rs).
pub const POWER_STEPS_PCT: [u32; 6] = [100, 90, 80, 70, 60, 50];
/// The clock steps, percent of the card's maximum core clock, highest first; 100 = unlocked (the card's own boost).
/// 6 October 2026, run 6: the 5090's best MH/W sat on the 60% floor (1,854 MHz: 0.563 MH/W, the rate within 0.15%),
/// so the ladder and the floor go to 45% of the maximum; the 1% rate tolerance is the guard below that
pub const CLOCK_STEPS_PCT: [u32; 7] = [100, 90, 80, 70, 60, 50, 45];
/// A card's clock floor when the vendor reports none: this share of its maximum core clock. 7 October 2026, the the first Windows machine
/// efficiency passes (docs/bench-log.md): the 5090's best MH per watt sat at 1,200 to 1,300 MHz (39 to 42 percent of
/// 3,090) and the rate fell past 5 percent only at 1,200 (class v3) and 1,100 (class v4), under the old 45 percent
/// floor; so the ladder continues below 45 percent in [`CLOCK_FINE_STEP_MHZ`] steps down to 20 percent of the maximum
/// (618 MHz on the 5090) and the stop rule, not the floor, ends the search.
pub const CLOCK_FLOOR_PCT: u32 = 20;
/// Below the percent ladder's last rung (45 percent) the clock ladder descends in steps of this many MHz until the
/// rate falls more than the tolerance under the cap point's rate (the knee), a step faults, or the floor is reached.
pub const CLOCK_FINE_STEP_MHZ: u32 = 100;
/// The measured efficient point per card (docs/bench-log.md: the 5090 at 308 W for 122 MH/s, the 9070 XT at 199 W for
/// 18 MH/s), the tuner's ceiling. Rule (main, 7 October 2026, after the second Windows machine dropped nine minutes after the tuner asked its
/// 5090 for 575 W with proving on the same card): the tuner never requests more than the card's measured efficient point
/// unless Power control is on and the user raised the cap above the default; a card not in this table is held at the
/// cap it runs at. A point below the vendor's minimum limit clamps up to that minimum (the 5090 floors at 400 W).
/// RX 9070 XT: 149 W from the first measured grid on the AMD knob (the first Windows machine, 8 October 2026, 12:10 UK, run
/// run-ca3-pc1-amd-grid-9070-20261008-b, 24 of 24 rows ok): the rate is flat at 18.93 to 18.98 MH/s over the whole ladder,
/// the knob moves watts only, and the efficient point is both floors (clock offset -500, about 2,920 MHz, the ADLX clamp;
/// power limit -30): 149.3 W at 18.96 MH/s, 0.127 MH/W, 24 percent under stock's 195.8 W. The 199 W before it was the
/// 7 October read at stock.
pub const EFFICIENT_W: &[(&str, f64)] = &[("RTX 5090", 308.0), ("RX 9070 XT", 149.0)];
/// The app's cap when the user chose none (engine.rs requested_watts); a cap above it is one the user raised.
pub const DEFAULT_CAP_PCT: u32 = 80;

/// The search's prior by architecture (the sweep lane, 8 October 2026, nine capped-against-uncapped pairs on rented
/// cards on class v4): a power cap holds the full rate until the SM clock falls under about 1,800 MHz on Ampere and
/// 2,400 MHz on Ada (a 4070 Super at half its stock cap keeps 31.26 MH/s; a 3080 Ti capped to 63 percent with the SM
/// at 749 MHz loses 24 to 28 percent); on Blackwell the measured lock knees (the 5090 at 1,300, the 5080 at 1,000, the
/// efficiency passes of 7 to 8 October). The ladders start near the prior and walk from there; the measured knee
/// still decides. 0 = no prior (the full ladder).
pub fn knee_clock_prior(name: &str) -> u32 {
    let n = name.to_ascii_uppercase();
    let has = |k: &str| n.contains(k);
    if has("5090") {
        1300
    } else if has("5080") {
        1000
    } else if has("RTX 40") || has("4060") || has("4070") || has("4080") || has("4090") || has("ADA") {
        2400
    } else if has("RTX 30") || has("3060") || has("3070") || has("3080") || has("3090") || has("A4000") || has("A5000") || has("A6000") || has("A100") {
        1800
    } else {
        0
    }
}

/// The percent rung the power ladder starts its walk at for a prior clock: the draw follows the clock cubed on the
/// V/F curve (P ~ f V^2, V ~ f over the range), rounded up to the next rung, never under the lowest rung.
pub fn prior_power_pct(limits: &Limits) -> Option<u32> {
    if limits.prior_knee_mhz == 0 || limits.clock_max_mhz == 0 || limits.prior_knee_mhz >= limits.clock_max_mhz {
        return None;
    }
    let r = limits.prior_knee_mhz as f64 / limits.clock_max_mhz as f64;
    let pct = (r * r * r * 100.0).ceil() as u32;
    let rung = ((pct + 9) / 10) * 10;
    Some(rung.clamp(POWER_STEPS_PCT[POWER_STEPS_PCT.len() - 1], 100))
}

/// The measured efficient watts of a card by its name, when the table has it.
pub fn efficient_watts(name: &str) -> Option<f64> {
    let n = name.to_ascii_uppercase();
    EFFICIENT_W.iter().find(|(k, _)| n.contains(&k.to_ascii_uppercase())).map(|(_, w)| *w)
}

/// The tuner's power ceiling in watts for a card: its measured efficient point, or the cap it runs at when the table has
/// no entry; the user's own cap instead when Power control is on and that cap is above the default (they raised it).
pub fn power_ceiling(limits: &Limits, name: &str, before: Point, power_control: bool) -> f64 {
    let cap = Limits { power_ceiling_w: 0.0, ..limits.clone() }.watts_for(if before.power_pct == 0 { DEFAULT_CAP_PCT } else { before.power_pct });
    let raised = power_control && before.power_pct > DEFAULT_CAP_PCT;
    match efficient_watts(name) {
        Some(e) if !raised => e.min(cap),
        _ => cap,
    }
}
/// A point may lose this much rate against the fastest point and still win on MH per watt (the manifest can change it).
pub const RATE_TOLERANCE_PCT: f64 = 1.0;
/// A step whose hottest GPU reading reaches this is marked hot and cannot win (the engine aborts at 90).
pub const HOT_C: f64 = 85.0;
/// A step whose mean memory clock falls below this share of the baseline's is marked and cannot win.
pub const MCLK_HOLD: f64 = 0.95;
/// A fleet prior is used by a new card when it carries at least this many samples (the manifest can change it).
pub const PRIOR_MIN_SAMPLES: u32 = 5;
/// A confirm check that beats its prior by more than this on MH per watt asks for the full plan.
pub const CONFIRM_GAIN_PCT: f64 = 1.0;
/// A tune repeats this often (the manifest can change it).
pub const PERIOD_S: u64 = 7 * 86_400;
/// The worker must have mined this long before a tune starts, and this much must remain to the hour boundary.
pub const STABLE_S: u64 = 120;
pub const NEEDS_S: i64 = 600;

/// What a card's vendor allows (never exceeded, never undercut).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Limits {
    pub power_default_w: f64,
    pub power_min_w: f64,
    pub power_max_w: f64,
    /// the maximum core clock the vendor reports (nvidia-smi clocks.max.gr; the ADLX gmax_range top); 0 = unknown
    pub clock_max_mhz: u32,
    /// the lowest cap the vendor allows (the ADLX gmax_range floor); 0 = CLOCK_FLOOR_PCT of the maximum
    pub clock_min_mhz: u32,
    /// Ember 2: the memory clock the card runs at by default and the vendor's maximum (nvidia-smi clocks.mem and
    /// clocks.max.mem); 0 = no memory knob (AMD through ADLX on RDNA 4 exposes none)
    pub mem_default_mhz: u32,
    pub mem_max_mhz: u32,
    /// the tuner's ceiling (power_ceiling), 0 = none: no step asks for more watts than this
    pub power_ceiling_w: f64,
    /// the search's prior (8 October 2026, the sweep lane's nine capped-against-uncapped pairs): the SM clock under
    /// which a power cap starts to cost rate on this architecture, 0 = no prior; the ladders start near it
    #[allow(dead_code)]
    pub prior_knee_mhz: u32,
}

impl Limits {
    /// The ceiling as the vendor allows it: never below the card's minimum limit; 0 when there is none.
    pub fn ceiling(&self) -> f64 {
        if self.power_ceiling_w <= 0.0 { 0.0 } else { self.power_ceiling_w.max(self.power_min_w) }
    }
    /// The percent of the default that the ceiling is, for the first step of the power ladder.
    pub fn ceiling_pct(&self) -> u32 {
        let c = self.ceiling();
        if c <= 0.0 || self.power_default_w <= 0.0 { 100 } else { (c / self.power_default_w * 100.0).round().clamp(1.0, 100.0) as u32 }
    }
    pub fn clock_floor(&self) -> u32 {
        if self.clock_min_mhz > 0 {
            self.clock_min_mhz
        } else {
            self.clock_max_mhz * CLOCK_FLOOR_PCT / 100
        }
    }
    /// A clock cap inside the vendor's range; 0 stays 0 (unlocked).
    pub fn clamp_clock(&self, mhz: u32) -> u32 {
        if mhz == 0 || self.clock_max_mhz == 0 {
            return 0;
        }
        mhz.clamp(self.clock_floor(), self.clock_max_mhz)
    }
    /// A memory clock inside the vendor's range; 0 stays 0 (the default).
    pub fn clamp_mem(&self, mhz: u32) -> u32 {
        if mhz == 0 || self.mem_max_mhz == 0 || self.mem_default_mhz == 0 {
            return 0;
        }
        mhz.clamp(self.mem_default_mhz, self.mem_max_mhz)
    }
    /// The watts a power percent asks for, inside the card's min and max, rounded to a watt.
    pub fn watts_for(&self, pct: u32) -> f64 {
        let mut w = self.power_default_w * pct as f64 / 100.0;
        if self.power_min_w > 0.0 {
            w = w.max(self.power_min_w);
        }
        if self.power_max_w > 0.0 {
            w = w.min(self.power_max_w);
        }
        let c = self.ceiling();
        if c > 0.0 {
            w = w.min(c);
        }
        w.round()
    }
}

/// One setting of the knobs (Ember 2 adds the memory clock: 0 = the driver's default).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub struct Point {
    /// the core clock cap in MHz; 0 = unlocked
    pub clock_mhz: u32,
    /// the power limit, percent of the default
    pub power_pct: u32,
    /// the memory clock in MHz (NVIDIA `-lmc`, locked to one value); 0 = the driver's default
    pub mem_mhz: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    /// the card as it runs now (the before number; the only step a measure-only card gets)
    Baseline,
    Power,
    Clock,
    /// the fleet prior and one neighbour
    Confirm,
    /// Ember 2: a hill-climb probe
    Climb,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Step {
    pub point: Point,
    /// the limit to set, watts
    pub watts: f64,
    pub kind: Kind,
}

/// What the tune is for (Settings > Ember Tune > goal). The rate floor is the share of the best rate seen a point
/// must keep to win on MH per watt: efficiency keeps 90%, balanced 99% (the 1% rule of lever 3), maximum rate
/// takes the fastest point and uses MH per watt only to break ties.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Goal {
    Efficiency,
    Balanced,
    MaxRate,
}

impl Goal {
    pub fn parse(s: &str) -> Goal {
        match s {
            "efficiency" | "eff" => Goal::Efficiency,
            "rate" | "max_rate" | "maximum" => Goal::MaxRate,
            _ => Goal::Balanced,
        }
    }
    pub fn name(&self) -> &'static str {
        match self {
            Goal::Efficiency => "efficiency",
            Goal::Balanced => "balanced",
            Goal::MaxRate => "rate",
        }
    }
    /// The rate tolerance the choice rule uses, percent under the best rate.
    pub fn tolerance_pct(&self, manifest_default: f64) -> f64 {
        match self {
            Goal::Efficiency => 10.0,
            Goal::Balanced => manifest_default,
            Goal::MaxRate => 0.0,
        }
    }
}

/// Pounds a day for a draw at a price in pence per kWh: watts × 24 h / 1000 × price / 100.
pub fn pounds_per_day(watts: f64, pence_per_kwh: f64) -> f64 {
    watts * 24.0 / 1000.0 * pence_per_kwh / 100.0
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum PlanKind {
    Full,
    Confirm,
    Baseline,
    /// Ember 2: the hill-climb over memory up and core down from the start point
    Climb,
}

impl PlanKind {
    pub fn name(&self) -> &'static str {
        match self {
            PlanKind::Full => "full",
            PlanKind::Confirm => "confirm",
            PlanKind::Baseline => "baseline",
            PlanKind::Climb => "climb",
        }
    }
}

/// The steps of a tune. The full plan is a coordinate search: the power ladder at the unlocked clock, then the clock
/// ladder at the power point that ladder chose (asked for through `next`, so the second half depends on the rows).
#[derive(Clone, Debug, PartialEq)]
pub struct Plan {
    pub kind: PlanKind,
    pub limits: Limits,
    pub before: Point,
    pub tolerance_pct: f64,
    power: Vec<Step>,
    clock_pcts: Vec<u32>,
    /// the clock ladder below the percent rungs: MHz values from the last rung minus one fine step down to the floor
    /// (the core-clock knob of 7 October 2026; empty when the card has no readable maximum clock)
    clock_fine: Vec<u32>,
    fixed: Vec<Step>,
    /// Ember 2 (Climb): the start point, the step sizes and the step budget
    climb: Option<Climb>,
}

/// The hill-climb's shape: from `start`, each probe moves the memory clock up by `mem_step` or the core clock down
/// by `core_step` (or both), keeps the move when the goal's score improves, else turns to the other knob; a
/// refused step (a fault on it) backs that knob off for good. At most `budget` steps including the start.
#[derive(Clone, Debug, PartialEq)]
pub struct Climb {
    pub start: Point,
    pub mem_step: u32,
    pub core_step: u32,
    pub budget: usize,
    pub goal: Goal,
}

impl Plan {
    /// Ember 2: the hill-climb. The start is the fleet prior (or the card's current point); a probe step is 5% of
    /// the memory range above the default (0 when the card has no memory knob) and 5% of the maximum core clock;
    /// five steps of 60 s converge in under 10 minutes.
    pub fn climb(limits: &Limits, start: Point, goal: Goal, tolerance_pct: f64) -> Plan {
        let mem_step = if limits.mem_max_mhz > limits.mem_default_mhz { ((limits.mem_max_mhz - limits.mem_default_mhz) / 20).max(25) } else { 0 };
        let core_step = if limits.clock_max_mhz > 0 { (limits.clock_max_mhz / 20).max(25) } else { 0 };
        let start = Point { clock_mhz: limits.clamp_clock(start.clock_mhz), power_pct: start.power_pct.clamp(50, 100), mem_mhz: limits.clamp_mem(start.mem_mhz) };
        Plan { kind: PlanKind::Climb, limits: limits.clone(), before: start, tolerance_pct: goal.tolerance_pct(tolerance_pct), power: Vec::new(), clock_pcts: Vec::new(), clock_fine: Vec::new(), fixed: Vec::new(), climb: Some(Climb { start, mem_step, core_step, budget: 5, goal }) }
    }

    /// The goal's score of a row: MH per watt for efficiency and balanced, the rate for maximum rate.
    pub fn score(&self, r: &Row) -> f64 {
        match self.climb.as_ref().map(|c| c.goal) {
            Some(Goal::MaxRate) => r.mhs,
            _ => r.eff,
        }
    }

    /// The climb's next probe from the rows so far: None when the budget is spent or no move is left.
    fn climb_next(&self, rows: &[Row]) -> Option<Step> {
        let c = self.climb.as_ref()?;
        let step = |p: Point| Step { point: p, watts: self.limits.watts_for(p.power_pct), kind: Kind::Climb };
        if rows.is_empty() {
            return Some(step(c.start));
        }
        if rows.len() >= c.budget {
            return None;
        }
        // the best usable row so far is the hill's top; a knob that produced a marked (refused) row is backed off
        let best = rows.iter().filter(|r| r.usable()).max_by(|a, b| self.score(a).partial_cmp(&self.score(b)).unwrap_or(std::cmp::Ordering::Equal))?;
        let refused_mem = rows.iter().any(|r| !r.usable() && r.point.mem_mhz > best.point.mem_mhz);
        let refused_core = rows.iter().any(|r| !r.usable() && r.point.clock_mhz != 0 && (best.point.clock_mhz == 0 || r.point.clock_mhz < best.point.clock_mhz));
        let last = rows.last()?;
        let mem_up = |p: Point| -> Option<Point> {
            if c.mem_step == 0 || refused_mem { return None; }
            let base = if p.mem_mhz == 0 { self.limits.mem_default_mhz } else { p.mem_mhz };
            let m = self.limits.clamp_mem(base + c.mem_step);
            (m > 0 && m != p.mem_mhz && m != base).then_some(Point { mem_mhz: m, ..p })
        };
        let core_down = |p: Point| -> Option<Point> {
            if c.core_step == 0 || refused_core { return None; }
            let base = if p.clock_mhz == 0 { self.limits.clock_max_mhz } else { p.clock_mhz };
            let k = self.limits.clamp_clock(base.saturating_sub(c.core_step));
            (k > 0 && k != p.clock_mhz).then_some(Point { clock_mhz: k, ..p })
        };
        let tried = |p: Point| rows.iter().any(|r| r.point == p);
        // the last move improved: keep going the same way from the top; else turn: memory first, then core, then both
        let last_improved = last.usable() && last.point == best.point && rows.len() > 1;
        let last_was_mem = rows.len() > 1 && last.point.mem_mhz != rows[rows.len() - 2].point.mem_mhz;
        let candidates: Vec<Option<Point>> = if last_improved && last_was_mem {
            vec![mem_up(best.point), core_down(best.point)]
        } else if last_improved {
            vec![core_down(best.point), mem_up(best.point)]
        } else {
            vec![mem_up(best.point), core_down(best.point), mem_up(best.point).and_then(core_down)]
        };
        candidates.into_iter().flatten().find(|p| !tried(*p)).map(step)
    }

    /// Power 100% to 50% at the unlocked clock (duplicate watts dropped, as the card's floor clamps them), then the
    /// clock ladder 90% to the floor of the maximum core clock at the chosen power. A card without a readable
    /// maximum clock gets the power ladder only; a card without a default limit gets the clock ladder only.
    pub fn full(limits: &Limits, before: Point, tolerance_pct: f64) -> Plan {
        let mut power = Vec::new();
        if limits.power_default_w > 0.0 {
            // the ceiling first (its own percent), then the ladder; every step above it clamps to it and is dropped as a duplicate
            let top = if limits.ceiling() > 0.0 { vec![limits.ceiling_pct()] } else { Vec::new() };
            // the prior (8 October 2026): the stock row stays (the max tier's row), the walk starts one rung above the prior
            let start = prior_power_pct(limits);
            for pct in top.into_iter().chain(POWER_STEPS_PCT) {
                if let Some(st) = start {
                    if pct < 100 && pct > st + 10 {
                        continue;
                    }
                }
                let w = limits.watts_for(pct);
                if power.last().map(|s: &Step| (s.watts - w).abs() < 0.5).unwrap_or(false) {
                    continue;
                }
                power.push(Step { point: Point { clock_mhz: 0, power_pct: pct, mem_mhz: 0 }, watts: w, kind: Kind::Power });
            }
        }
        // a narrow range (the AMD knob, an earlier release: the 9070 XT's floor sits 500 MHz under stock, above the 45 percent rung) takes
        // no percent rungs, only the fine ladder from one step under the maximum down to the floor
        let narrow = limits.clock_max_mhz > 0 && limits.clock_floor() > limits.clock_max_mhz * CLOCK_STEPS_PCT[CLOCK_STEPS_PCT.len() - 1] / 100;
        let mut clock_pcts = if limits.clock_max_mhz > 0 && !narrow { CLOCK_STEPS_PCT[1..].to_vec() } else { Vec::new() };
        // the prior: the percent rungs more than one rung above the prior clock are skipped (the walk starts near the knee)
        if limits.prior_knee_mhz > 0 && limits.clock_max_mhz > limits.prior_knee_mhz {
            let keep_from = limits.prior_knee_mhz + limits.clock_max_mhz / 10;
            clock_pcts.retain(|&p| limits.clock_max_mhz * p / 100 <= keep_from);
        }
        // the fine ladder: from the last percent rung down to the floor in CLOCK_FINE_STEP_MHZ steps (the knob of
        // 7 October 2026; the stop rule in `next` ends it at the knee)
        let mut clock_fine = Vec::new();
        if narrow {
            let floor = limits.clock_floor();
            let mut m = limits.clock_max_mhz.saturating_sub(CLOCK_FINE_STEP_MHZ);
            while m >= floor && m > 0 {
                clock_fine.push(m);
                m = m.saturating_sub(CLOCK_FINE_STEP_MHZ);
            }
        } else if limits.clock_max_mhz > 0 {
            let last_pct = limits.clamp_clock(limits.clock_max_mhz * CLOCK_STEPS_PCT[CLOCK_STEPS_PCT.len() - 1] / 100);
            let floor = limits.clock_floor();
            let mut m = (last_pct / CLOCK_FINE_STEP_MHZ) * CLOCK_FINE_STEP_MHZ;
            if m >= last_pct {
                m = m.saturating_sub(CLOCK_FINE_STEP_MHZ);
            }
            while m >= floor && m > 0 {
                clock_fine.push(m);
                m = m.saturating_sub(CLOCK_FINE_STEP_MHZ);
            }
        }
        if limits.prior_knee_mhz > 0 && limits.clock_max_mhz > limits.prior_knee_mhz {
            let keep_from = limits.prior_knee_mhz + CLOCK_FINE_STEP_MHZ * 2;
            clock_fine.retain(|&m| m <= keep_from);
        }
        Plan { kind: PlanKind::Full, limits: limits.clone(), before, tolerance_pct, power, clock_pcts, clock_fine, fixed: Vec::new(), climb: None }
    }

    /// The prior's point, then one neighbour: the next clock step up when the prior caps the clock (is the cap
    /// costing rate?), else one power step down (is there efficiency left?).
    pub fn confirm(limits: &Limits, prior: Point, before: Point, tolerance_pct: f64) -> Plan {
        let p = Point { clock_mhz: limits.clamp_clock(prior.clock_mhz), power_pct: prior.power_pct.clamp(50, 100), mem_mhz: limits.clamp_mem(prior.mem_mhz) };
        let first = Step { point: p, watts: limits.watts_for(p.power_pct), kind: Kind::Confirm };
        let neighbour = if p.clock_mhz > 0 && limits.clock_max_mhz > 0 {
            let up = p.clock_mhz + limits.clock_max_mhz / 10;
            let clock = if up >= limits.clock_max_mhz { 0 } else { limits.clamp_clock(up) };
            Point { clock_mhz: clock, power_pct: p.power_pct, mem_mhz: p.mem_mhz }
        } else {
            Point { clock_mhz: p.clock_mhz, power_pct: (p.power_pct.saturating_sub(10)).max(50), mem_mhz: p.mem_mhz }
        };
        let mut fixed = vec![first];
        if neighbour != p {
            fixed.push(Step { point: neighbour, watts: limits.watts_for(neighbour.power_pct), kind: Kind::Confirm });
        }
        Plan { kind: PlanKind::Confirm, limits: limits.clone(), before, tolerance_pct, power: Vec::new(), clock_pcts: Vec::new(), clock_fine: Vec::new(), fixed, climb: None }
    }

    /// One step at the card's current point: the before number, and all a measure-only card (Apple, or NVIDIA
    /// with Power control off) reports.
    pub fn baseline(limits: &Limits, before: Point, tolerance_pct: f64) -> Plan {
        let fixed = vec![Step { point: before, watts: limits.watts_for(before.power_pct), kind: Kind::Baseline }];
        Plan { kind: PlanKind::Baseline, limits: limits.clone(), before, tolerance_pct, power: Vec::new(), clock_pcts: Vec::new(), clock_fine: Vec::new(), fixed, climb: None }
    }

    /// How many steps the plan has at most (the clock ladder counts whether or not it runs).
    pub fn len(&self) -> usize {
        if let Some(c) = &self.climb {
            return c.budget;
        }
        self.fixed.len() + self.power.len() + self.clock_pcts.len() + self.clock_fine.len()
    }

    /// The cap point's row: the power ladder's choice (the row the clock search is read against), else the first row.
    pub fn cap_row(&self, rows: &[Row]) -> Option<Row> {
        if self.power.is_empty() {
            rows.first().cloned()
        } else {
            choose(&rows[..self.power.len().min(rows.len())], self.tolerance_pct).or_else(|| rows.first().cloned())
        }
    }

    /// Why the clock search ended after `rows`, in words, or None while it runs: a faulted clock row (a rejected or
    /// mismatched hash during the hold: the fingerprint check), the knee (the rate under the cap point's by more than
    /// the tolerance), or the floor.
    pub fn clock_stop_reason(&self, rows: &[Row]) -> Option<String> {
        let last = rows.last()?;
        if last.point.clock_mhz == 0 || !matches!(self.kind, PlanKind::Full) {
            return None;
        }
        let clock_rows = rows.len().saturating_sub(self.power.len());
        if clock_rows == 0 {
            return None;
        }
        if last.mark == Some(Mark::Faulted) {
            return Some(format!("fingerprint mismatch at {} MHz, clocks reset", last.point.clock_mhz));
        }
        if let Some(cap) = self.cap_row(rows) {
            if last.usable() && cap.usable() && cap.mhs > 0.0 && last.mhs < cap.mhs * (1.0 - self.tolerance_pct.max(0.0) / 100.0) {
                return Some(format!("rate fell {:.1} percent at {} MHz", 100.0 * (cap.mhs - last.mhs) / cap.mhs, last.point.clock_mhz));
            }
        }
        if clock_rows >= self.clock_pcts.len() + self.clock_fine.len() {
            return Some(format!("the floor at {} MHz", last.point.clock_mhz));
        }
        None
    }
    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }

    /// The next step after `rows` (one row per step done so far), or None when the plan is complete.
    pub fn next(&self, rows: &[Row]) -> Option<Step> {
        if self.climb.is_some() {
            return self.climb_next(rows);
        }
        let i = rows.len();
        if !self.fixed.is_empty() {
            return self.fixed.get(i).cloned();
        }
        if i < self.power.len() {
            return Some(self.power[i].clone());
        }
        let k = i - self.power.len();
        // the stop rule (7 October 2026): a faulted clock row or the knee ends the search; the choice is made among
        // the rows so far
        if k > 0 {
            if let Some(reason) = self.clock_stop_reason(rows) {
                if !reason.starts_with("the floor") {
                    return None;
                }
            }
        }
        let clock = if k < self.clock_pcts.len() {
            self.limits.clamp_clock(self.limits.clock_max_mhz * self.clock_pcts[k] / 100)
        } else {
            *self.clock_fine.get(k - self.clock_pcts.len())?
        };
        // the clock ladder rides the power point the power ladder chose (the before point when nothing won)
        let power_pct = if self.power.is_empty() { self.before.power_pct } else { choose(&rows[..self.power.len()], self.tolerance_pct).map(|r| r.point.power_pct).unwrap_or(self.before.power_pct) };
        // a step whose clamp lands on the previous step's clock is dropped (the floor was reached)
        if rows.last().map(|r| r.point.clock_mhz == clock).unwrap_or(false) {
            return None;
        }
        Some(Step { point: Point { clock_mhz: clock, power_pct, mem_mhz: 0 }, watts: self.limits.watts_for(power_pct), kind: Kind::Clock })
    }
}

/// Why a step cannot win, or Ok.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Mark {
    Ok,
    NoReadings,
    /// a rejected or mismatched hash during the hold: the step was reverted and marked
    Faulted,
    /// the GPU reached HOT_C during the hold
    Hot,
    /// the memory clock fell under MCLK_HOLD of the baseline's: the knob dragged it
    MemoryClock,
    /// the setting did not take (the readback disagreed)
    Unapplied,
}

impl Mark {
    pub fn name(&self) -> &'static str {
        match self {
            Mark::Ok => "ok",
            Mark::NoReadings => "no_readings",
            Mark::Faulted => "faulted",
            Mark::Hot => "hot",
            Mark::MemoryClock => "memory_clock_dropped",
            Mark::Unapplied => "unapplied",
        }
    }
}

/// One step's result. `watts` is the mean draw during the hold (what the miner pays for), `limit` the cap set,
/// `eff` MH per watt of draw (0 when the step had no readings).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Row {
    pub point: Point,
    pub limit: f64,
    pub watts: f64,
    pub mhs: f64,
    pub eff: f64,
    pub draws: usize,
    pub rates: usize,
    pub gclk: f64,
    pub mclk: f64,
    pub tmax: f64,
    pub faults: u32,
    pub mark: Option<Mark>,
}

/// What a hold collected.
#[derive(Clone, Debug, Default)]
pub struct Samples {
    pub draws: Vec<f64>,
    pub rates: Vec<f64>,
    pub gclks: Vec<f64>,
    pub mclks: Vec<f64>,
    pub tmax: f64,
    pub faults: u32,
    pub unapplied: bool,
}

fn mean(v: &[f64]) -> f64 {
    if v.is_empty() {
        0.0
    } else {
        v.iter().sum::<f64>() / v.len() as f64
    }
}

impl Row {
    /// `baseline_mclk` is the memory clock of the first row (0 = unknown): the memory-clock guard.
    pub fn from_samples(step: &Step, s: &Samples, baseline_mclk: f64) -> Row {
        let watts = mean(&s.draws);
        let mhs = mean(&s.rates);
        let mclk = mean(&s.mclks);
        let usable = s.draws.len() >= 3 && !s.rates.is_empty() && watts > 1.0;
        let mark = if s.faults > 0 {
            Mark::Faulted
        } else if s.unapplied {
            Mark::Unapplied
        } else if !usable {
            Mark::NoReadings
        } else if s.tmax >= HOT_C {
            Mark::Hot
        } else if baseline_mclk > 0.0 && mclk > 0.0 && mclk < MCLK_HOLD * baseline_mclk {
            Mark::MemoryClock
        } else {
            Mark::Ok
        };
        Row { point: step.point, limit: step.watts, watts, mhs, eff: if usable { mhs / watts } else { 0.0 }, draws: s.draws.len(), rates: s.rates.len(), gclk: mean(&s.gclks), mclk, tmax: s.tmax, faults: s.faults, mark: Some(mark) }
    }
    pub fn usable(&self) -> bool {
        self.eff > 0.0 && self.mark == Some(Mark::Ok)
    }
    /// `TUNE card=<label> step=<i> clock=<MHz> cap=<pct> limit=<W> watts=<W> mhs=<x> eff=<MH/W> gclk=<MHz> mclk=<MHz> tmax=<C> mark=<m>`
    pub fn line(&self, card: &str, i: usize) -> String {
        format!(
            "TUNE card={card} step={i} clock={} cap={} mem={} limit={:.0} watts={:.1} mhs={:.2} eff={:.4} gclk={:.0} mclk={:.0} tmax={:.0} mark={}{}",
            self.point.clock_mhz,
            self.point.power_pct,
            self.point.mem_mhz,
            self.limit,
            self.watts,
            self.mhs,
            self.eff,
            self.gclk,
            self.mclk,
            self.tmax,
            self.mark.unwrap_or(Mark::NoReadings).name(),
            if self.faults > 0 { format!(" faults={}", self.faults) } else { String::new() }
        )
    }
    pub fn json(&self) -> serde_json::Value {
        serde_json::json!({ "clock_mhz": self.point.clock_mhz, "power_pct": self.point.power_pct, "mem_mhz": self.point.mem_mhz, "limit_w": self.limit, "watts": r1(self.watts), "mhs": r2(self.mhs), "eff": r4(self.eff), "gclk": self.gclk.round(), "mclk": self.mclk.round(), "tmax": self.tmax.round(), "faults": self.faults, "mark": self.mark.unwrap_or(Mark::NoReadings).name() })
    }
}

fn r1(x: f64) -> f64 {
    (x * 10.0).round() / 10.0
}
fn r2(x: f64) -> f64 {
    (x * 100.0).round() / 100.0
}
fn r4(x: f64) -> f64 {
    (x * 10_000.0).round() / 10_000.0
}

/// The choice: among the usable rows within `tolerance_pct` of the fastest row's rate, the best MH per watt; within
/// 1% on efficiency the higher rate wins; within 1% on both, the lower draw. A card never gives up more than the
/// tolerance in blocks for the saving. Marked rows never win.
pub fn choose(rows: &[Row], tolerance_pct: f64) -> Option<Row> {
    let top = rows.iter().filter(|r| r.usable()).map(|r| r.mhs).fold(0.0, f64::max);
    if top <= 0.0 {
        return None;
    }
    let floor = top * (1.0 - tolerance_pct.max(0.0) / 100.0);
    let mut best: Option<&Row> = None;
    for r in rows.iter().filter(|r| r.usable() && r.mhs >= floor) {
        best = Some(match best {
            None => r,
            Some(b) => {
                let eff_tie = (r.eff - b.eff).abs() <= 0.01 * b.eff.max(r.eff);
                if !eff_tie {
                    if r.eff > b.eff { r } else { b }
                } else {
                    let mhs_tie = (r.mhs - b.mhs).abs() <= 0.01 * b.mhs.max(r.mhs);
                    if !mhs_tie {
                        if r.mhs > b.mhs { r } else { b }
                    } else if r.watts < b.watts {
                        r
                    } else {
                        b
                    }
                }
            }
        });
    }
    best.cloned()
}

/// Ember Tune tiers (8 October 2026, the founder's definition relayed by main): the card is tuned once (the search at
/// first steady mining, weekly, or on Tune) and the three tiers are rows of THAT search, applied at once with no
/// re-search. `efficiency` is the best MH per watt row outright; `balanced` is the best MH per watt row within one
/// percent of the top rate (the default from install); `max` is the top-rate row. `source` is "stock" when the row
/// is the unlocked 100 percent point (the search's first row), else "measured". Nothing is interpolated.
#[derive(Clone, Debug, PartialEq)]
pub struct Tier {
    pub id: &'static str,
    pub row: Row,
    pub source: &'static str,
}

impl Tier {
    pub fn json(&self) -> serde_json::Value {
        serde_json::json!({ "id": self.id, "clock_mhz": self.row.point.clock_mhz, "power_pct": self.row.point.power_pct, "w": r1(self.row.watts), "mhs": r2(self.row.mhs), "mhw": r4(self.row.eff), "source": self.source, "limit_w": r1(self.row.limit), "mem_mhz": self.row.point.mem_mhz })
    }
}

pub const TIER_IDS: [&str; 3] = ["efficiency", "balanced", "max"];
pub const BALANCED_TOLERANCE_PCT: f64 = 1.0;

fn is_stock(r: &Row) -> bool {
    r.point.clock_mhz == 0 && r.point.power_pct >= 100 && r.point.mem_mhz == 0
}

/// The three tiers of a search, or none when no row is usable. With one usable row all three name it.
pub fn tiers(rows: &[Row]) -> Vec<Tier> {
    let usable: Vec<&Row> = rows.iter().filter(|r| r.usable()).collect();
    if usable.is_empty() {
        return Vec::new();
    }
    let efficiency = usable.iter().copied().fold(None::<&Row>, |b, r| match b { Some(b) if b.eff >= r.eff => Some(b), _ => Some(r) }).cloned().unwrap();
    let max = usable.iter().copied().fold(None::<&Row>, |b, r| match b { Some(b) if b.mhs >= r.mhs => Some(b), _ => Some(r) }).cloned().unwrap();
    let balanced = choose(rows, BALANCED_TOLERANCE_PCT).unwrap_or_else(|| efficiency.clone());
    let src = |r: &Row| if is_stock(r) { "stock" } else { "measured" };
    vec![
        Tier { id: "efficiency", source: src(&efficiency), row: efficiency },
        Tier { id: "balanced", source: src(&balanced), row: balanced },
        Tier { id: "max", source: src(&max), row: max },
    ]
}

/// The one tier of a card with no lever: its stock row, and the reason in the note.
pub fn stock_tier(row: &Row) -> Vec<Tier> {
    vec![Tier { id: "max", source: "stock", row: row.clone() }]
}

/// The tier's point and cap back from the card state's json (what /api/tune/tier applies with no re-search).
pub fn tier_from_json(tiers: &[serde_json::Value], id: &str) -> Option<(Point, f64, f64, f64)> {
    let t = tiers.iter().find(|t| t.get("id").and_then(|v| v.as_str()) == Some(id))?;
    let n = |k: &str| t.get(k).and_then(|v| v.as_f64()).unwrap_or(0.0);
    Some((Point { clock_mhz: n("clock_mhz") as u32, power_pct: (n("power_pct") as u32).clamp(50, 100), mem_mhz: n("mem_mhz") as u32 }, n("limit_w"), n("mhs"), n("w")))
}

/// The class flip (8 October 2026): tiers measured under one program class never apply under another. Stale when the
/// stored class and the card's current class are both known and differ.
pub fn tiers_stale(tiers_class: &str, program_class: &str) -> bool {
    !tiers_class.is_empty() && !program_class.is_empty() && tiers_class != program_class
}

/// The refusal and the strip line for a stale set.
pub fn stale_text(card: &str, tiers_class: &str, program_class: &str) -> String {
    format!("re-measuring {card} for class {program_class}: the stored tiers were measured under class {tiers_class}")
}

/// The rate loss at the same lock under a new class against the old set: the new efficiency row against the old row at
/// the same clock and cap when both exist, else the table of measured losses by card name (the 5090's class v5 rows at
/// the 1,300 MHz lock, 8 October 2026), else 0 (unknown). Percent; negative = a gain.
pub const KNEE_LOSS_TABLE: &[(&str, &str, f64)] = &[];
pub fn knee_loss_pct(card: &str, new_class: &str, old: &[serde_json::Value], new: &[serde_json::Value]) -> f64 {
    let row = |v: &[serde_json::Value]| tier_from_json(v, "efficiency");
    if let (Some((op, _, omhs, _)), Some((np, _, nmhs, _))) = (row(old), row(new)) {
        if op.clock_mhz == np.clock_mhz && op.power_pct == np.power_pct && omhs > 0.0 && nmhs > 0.0 {
            return ((100.0 * (omhs - nmhs) / omhs) * 10.0).round() / 10.0;
        }
    }
    let n = card.to_ascii_uppercase();
    KNEE_LOSS_TABLE.iter().find(|(k, c, _)| n.contains(&k.to_ascii_uppercase()) && *c == new_class).map(|(_, _, l)| *l).unwrap_or(0.0)
}

/// The row a tier name points at in a tier list.
pub fn tier_row(tiers: &[Tier], id: &str) -> Option<Row> {
    tiers.iter().find(|t| t.id == id).map(|t| t.row.clone())
}

/// The first-run line the strip shows: "Ember Tune set <card> to Balanced: 122.1 MH/s at 221 W, 77 W under stock".
pub fn tier_set_line(card: &str, id: &str, row: &Row, stock_w: f64) -> String {
    let mut c = id.chars();
    let title = match c.next() { Some(f) => f.to_uppercase().collect::<String>() + c.as_str(), None => String::new() };
    let saving = if stock_w > row.watts { format!(", {:.0} W under stock", stock_w - row.watts) } else { String::new() };
    format!("Ember Tune set {card} to {title}: {:.1} MH/s at {:.0} W{saving}", row.mhs, row.watts)
}

/// A confirm check's verdict: the prior stands, or the neighbour beat it by over CONFIRM_GAIN_PCT (the full plan is
/// due), or nothing could be read.
#[derive(Clone, Debug, PartialEq)]
pub enum Verdict {
    Keep(Row),
    FullDue { prior: Row, better: Row },
    NoReadings,
}

pub fn confirm_verdict(rows: &[Row], tolerance_pct: f64) -> Verdict {
    let Some(prior) = rows.first().filter(|r| r.usable()).cloned() else {
        return match choose(rows, tolerance_pct) {
            Some(r) => Verdict::FullDue { prior: Row::default(), better: r },
            None => Verdict::NoReadings,
        };
    };
    match choose(rows, tolerance_pct) {
        Some(best) if best.point != prior.point && best.eff > prior.eff * (1.0 + CONFIRM_GAIN_PCT / 100.0) => Verdict::FullDue { prior, better: best },
        _ => Verdict::Keep(prior),
    }
}

// ---- the fleet prior ------------------------------------------------------------------------------------------

/// The key a prior is filed under: card model (spaces as underscores), driver major, program class.
pub fn prior_key(card: &str, driver: &str, class: &str) -> String {
    format!("{}|{}|{}", card.trim().replace(' ', "_"), driver_major(driver), if class.is_empty() { "v2" } else { class })
}

/// "581.57" -> "581", "32.0.15801" -> "32", "" -> "0".
pub fn driver_major(driver: &str) -> String {
    let d: String = driver.trim().chars().take_while(|c| c.is_ascii_digit()).collect();
    if d.is_empty() { "0".into() } else { d }
}

/// The program class from a race line's features (loads and wide loads per hash); the generator fixes both today.
pub fn program_class(loads: u32, wide: u32) -> String {
    if loads == 0 { "v2".into() } else { format!("l{loads}w{wide}") }
}

/// A fleet prior as the signed manifest carries it under tuning.priors.<key>.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Prior {
    pub point: Point,
    pub eff: f64,
    pub mhs: f64,
    pub watts: f64,
    /// the spread of the chosen efficiency across the samples, percent of the median
    pub spread_pct: f64,
    pub samples: u32,
}

/// The tuning section's Ember settings: the kill switch and the thresholds (defaults when absent).
#[derive(Clone, Debug, PartialEq)]
pub struct Settings {
    pub enabled: bool,
    pub min_samples: u32,
    pub tolerance_pct: f64,
    pub period_s: u64,
}

impl Default for Settings {
    fn default() -> Settings {
        Settings { enabled: true, min_samples: PRIOR_MIN_SAMPLES, tolerance_pct: RATE_TOLERANCE_PCT, period_s: PERIOD_S }
    }
}

/// Reads `tuning.ember` (absent = the defaults; `enabled: false` is the fleet-wide kill switch).
pub fn settings_of(tuning: Option<&serde_json::Value>) -> Settings {
    let d = Settings::default();
    let Some(e) = tuning.and_then(|t| t.get("ember")).filter(|e| e.is_object()) else { return d };
    Settings {
        enabled: e.get("enabled").and_then(|v| v.as_bool()).unwrap_or(d.enabled),
        min_samples: e.get("min_samples").and_then(|v| v.as_u64()).map(|v| v as u32).unwrap_or(d.min_samples),
        tolerance_pct: e.get("rate_tolerance_pct").and_then(|v| v.as_f64()).filter(|v| (0.0..=25.0).contains(v)).unwrap_or(d.tolerance_pct),
        period_s: e.get("period_s").and_then(|v| v.as_u64()).filter(|v| *v >= 3600).unwrap_or(d.period_s),
    }
}

/// The prior for a key, or None when the manifest has none or it carries under `min_samples` samples.
pub fn prior_of(tuning: Option<&serde_json::Value>, key: &str, min_samples: u32) -> Option<Prior> {
    let p = tuning?.get("priors")?.get(key)?;
    let n = |k: &str| p.get(k).and_then(|v| v.as_f64()).unwrap_or(0.0);
    let samples = p.get("samples").and_then(|v| v.as_u64()).unwrap_or(0) as u32;
    if samples < min_samples.max(1) {
        return None;
    }
    let point = Point { clock_mhz: n("clock_mhz") as u32, power_pct: (n("power_pct") as u32).clamp(50, 100), mem_mhz: n("mem_mhz") as u32 };
    if point.power_pct == 0 && point.clock_mhz == 0 {
        return None;
    }
    Some(Prior { point, eff: n("eff"), mhs: n("mhs"), watts: n("watts"), spread_pct: n("spread_pct"), samples })
}

/// The fleet record: one `TUNE {json}` line. `machine` is a hash of the install id (never the id, never an address).
#[allow(clippy::too_many_arguments)]
pub fn record_json(ts: f64, machine_hash: &str, app: &str, os: &str, card: &str, vendor: &str, driver: &str, class: &str, plan: PlanKind, rows: &[Row], chosen: Option<&Row>, before: Option<&Row>, floor: bool) -> serde_json::Value {
    serde_json::json!({
        "ts": ts.round(),
        "machine": machine_hash,
        "app": app,
        "os": os,
        "card": card.replace(' ', "_"),
        "vendor": vendor,
        "driver": driver,
        "driver_major": driver_major(driver),
        "class": if class.is_empty() { "v2" } else { class },
        "key": prior_key(card, driver, class),
        "plan": plan.name(),
        "steps": rows.iter().map(|r| r.json()).collect::<Vec<_>>(),
        "chosen": chosen.map(|r| r.json()),
        "before": before.map(|r| r.json()),
        "eff": chosen.map(|r| r4(r.eff)).unwrap_or(0.0),
        "mhs": chosen.map(|r| r2(r.mhs)).unwrap_or(0.0),
        "watts": chosen.map(|r| r1(r.watts)).unwrap_or(0.0),
        "floor": floor,
        "note": if floor { "floor, not optimum" } else { "" },
    })
}

// ---- the state machine -----------------------------------------------------------------------------------------

pub use crate::sweep::Timing;

/// Where the tune stands, for the card row.
#[derive(Clone, Debug, PartialEq)]
pub enum Phase {
    /// the step's setting was (or is about to be) requested; waiting for the readback or the acknowledgement
    Applying { since: Instant, sent: bool },
    Settling { since: Instant },
    Holding { since: Instant },
    /// the chosen point was requested; waiting for the readback
    Finishing { since: Instant, sent: bool },
    Done,
}

/// What the engine must do after a tick.
#[derive(Clone, Debug, PartialEq)]
pub enum Out {
    /// set this point on the card (the watts are the limit for its power percent)
    Apply(Step),
    /// a step finished: log its line (its index is the row count before it)
    Row(Row),
    /// the plan finished and the chosen point is in force
    Finished(Row),
    /// the plan failed; the engine restores the point from before
    Failed(String),
}

/// What the card reports now, for the readback.
#[derive(Clone, Copy, Debug, Default)]
pub struct Readback {
    /// the power limit in force, watts (0 = unknown)
    pub limit_w: f64,
    /// the set command for the current request was acknowledged (nvidia-smi answered, or the helper logged it)
    pub acked: bool,
}

pub struct Run {
    pub card: usize,
    pub device: String,
    pub label: String,
    pub plan: Plan,
    pub current: Option<Step>,
    pub phase: Phase,
    pub rows: Vec<Row>,
    pub chosen: Option<Row>,
    pub before: Point,
    pub before_w: f64,
    pub started: Instant,
    pub forced: bool,
    pub timing: Timing,
    /// the request number of the setting in flight (the engine acknowledges it by number)
    pub seq: u64,
    samples: Samples,
}

impl Run {
    #[allow(clippy::too_many_arguments)]
    pub fn new(card: usize, device: &str, label: &str, plan: Plan, before_w: f64, forced: bool, timing: Timing, now: Instant) -> Run {
        let before = plan.before;
        let current = plan.next(&[]);
        Run { card, device: device.into(), label: label.into(), plan, current, phase: Phase::Applying { since: now, sent: false }, rows: Vec::new(), chosen: None, before, before_w, started: now, forced, timing, seq: 0, samples: Samples::default() }
    }

    /// A worker STATUS interval rate (MH/s); counted while holding.
    pub fn sample_rate(&mut self, mhs: f64) {
        if matches!(self.phase, Phase::Holding { .. }) && mhs > 0.0 {
            self.samples.rates.push(mhs);
        }
    }
    /// A rate from a windowed meter (the worker layer's 10 s reported rate, MH/s): counted while holding, and only once
    /// the hold has outlasted the window, so no sample carries the settle or the step before it.
    pub fn sample_rate_after(&mut self, mhs: f64, window: Duration, now: Instant) {
        if let Phase::Holding { since } = self.phase {
            if now.duration_since(since) >= window {
                self.sample_rate(mhs);
            }
        }
    }
    /// A telemetry reading (draw, core clock, memory clock, GPU temperature); counted while holding.
    pub fn sample_telemetry(&mut self, draw_w: f64, gclk: f64, mclk: f64, temp_c: f64) {
        if !matches!(self.phase, Phase::Holding { .. }) {
            return;
        }
        if draw_w > 0.0 {
            self.samples.draws.push(draw_w);
        }
        if gclk > 0.0 {
            self.samples.gclks.push(gclk);
        }
        if mclk > 0.0 {
            self.samples.mclks.push(mclk);
        }
        if temp_c > self.samples.tmax {
            self.samples.tmax = temp_c;
        }
    }
    /// A rejected or mismatched hash while this step holds: the step is marked and cannot win.
    pub fn sample_fault(&mut self) {
        if matches!(self.phase, Phase::Holding { .. } | Phase::Settling { .. }) {
            self.samples.faults += 1;
        }
    }
    /// The engine's readback said the setting in force is not the one requested (checked during the hold).
    pub fn mark_unapplied(&mut self) {
        if matches!(self.phase, Phase::Holding { .. }) {
            self.samples.unapplied = true;
        }
    }

    pub fn baseline_mclk(&self) -> f64 {
        self.rows.first().map(|r| r.mclk).unwrap_or(0.0)
    }

    /// Seconds left in the plan from here: the rest of this step plus (settle + hold + a 10 s apply) per step to come.
    pub fn eta_s(&self, now: Instant) -> i64 {
        let per = (self.timing.settle + self.timing.hold).as_secs() as i64 + 10;
        let this = match &self.phase {
            Phase::Applying { .. } => per,
            Phase::Settling { since } => per - 10 - now.duration_since(*since).as_secs().min(per as u64) as i64,
            Phase::Holding { since } => (self.timing.hold.as_secs() as i64 - now.duration_since(*since).as_secs() as i64).max(0),
            Phase::Finishing { .. } | Phase::Done => 0,
        };
        let left = self.plan.len().saturating_sub(self.rows.len() + 1) as i64;
        this.max(0) + left * per
    }

    /// The phase in words for the card row.
    pub fn words(&self, now: Instant) -> String {
        let what = |p: &Point| {
            let mut w = if p.clock_mhz > 0 { format!("{} MHz · {}%", p.clock_mhz, p.power_pct) } else { format!("{}%", p.power_pct) };
            if p.mem_mhz > 0 {
                w.push_str(&format!(" · mem {}", p.mem_mhz));
            }
            w
        };
        let cur = self.current.as_ref().map(|s| what(&s.point)).unwrap_or_default();
        let n = self.rows.len() + 1;
        let of = self.plan.len();
        match &self.phase {
            Phase::Applying { .. } => format!("tuning: setting {cur} (step {n} of {of})"),
            Phase::Settling { since } => format!("tuning: {cur} settling · {} s (step {n} of {of})", self.timing.settle.as_secs().saturating_sub(now.duration_since(*since).as_secs())),
            Phase::Holding { since } => format!("tuning: holding {cur} · {} s (step {n} of {of})", self.timing.hold.as_secs().saturating_sub(now.duration_since(*since).as_secs())),
            Phase::Finishing { .. } => format!("tuning: setting the best point ({})", self.chosen.as_ref().map(|r| what(&r.point)).unwrap_or_default()),
            Phase::Done => "tuning: done".into(),
        }
    }

    /// A step is applied when its power limit reads back (within 1.5 W) and, when it caps the clock or changes
    /// nothing in watts, the set command was acknowledged.
    fn applied(step: &Step, rb: Readback, before_w: f64) -> bool {
        let power_ok = rb.limit_w <= 0.0 || (rb.limit_w - step.watts).abs() < 1.5;
        let power_changed = (step.watts - before_w).abs() >= 1.5;
        if step.point.clock_mhz > 0 || step.point.mem_mhz > 0 || !power_changed {
            rb.acked && power_ok
        } else if rb.limit_w <= 0.0 {
            // a card with no limit readback at all (AMD through ADLX reports an offset, never watts; run 6 on
            // 6 October 2026 aborted the 9070 XT's ladder at its first step on "card reports 0 W"): the
            // acknowledgement is the proof
            rb.acked
        } else {
            power_ok
        }
    }

    /// Drives the state machine. `rb` is what the card reports now.
    pub fn tick(&mut self, now: Instant, rb: Readback) -> Vec<Out> {
        let mut out = Vec::new();
        let settle = self.timing.settle;
        let hold = self.timing.hold;
        let apply = self.timing.apply;
        match self.phase.clone() {
            Phase::Applying { since, sent } => {
                let Some(step) = self.current.clone() else {
                    self.phase = Phase::Done;
                    out.push(Out::Failed("no steps".into()));
                    return out;
                };
                if !sent {
                    self.phase = Phase::Applying { since: now, sent: true };
                    self.seq += 1;
                    out.push(Out::Apply(step));
                } else if Run::applied(&step, rb, self.last_watts()) {
                    self.phase = Phase::Settling { since: now };
                } else if now.duration_since(since) > apply {
                    if step.kind == Kind::Baseline {
                        // the baseline is the card as it runs: nothing to apply, nothing to wait for
                        self.phase = Phase::Settling { since: now };
                    } else {
                        self.phase = Phase::Done;
                        out.push(Out::Failed(format!("the setting ({} MHz, {}% = {:.0} W) did not take within {} s (card reports {:.0} W, acknowledged {})", step.point.clock_mhz, step.point.power_pct, step.watts, apply.as_secs(), rb.limit_w, rb.acked)));
                    }
                }
            }
            Phase::Settling { since } => {
                if now.duration_since(since) >= settle {
                    let faults = self.samples.faults;
                    self.samples = Samples { faults, ..Samples::default() };
                    self.phase = Phase::Holding { since: now };
                }
            }
            Phase::Holding { since } => {
                if now.duration_since(since) >= hold {
                    let step = self.current.clone().expect("a step while holding");
                    let row = Row::from_samples(&step, &self.samples, self.baseline_mclk());
                    self.rows.push(row.clone());
                    out.push(Out::Row(row));
                    self.current = self.plan.next(&self.rows);
                    match self.current.clone() {
                        Some(next) => {
                            self.phase = Phase::Applying { since: now, sent: true };
                            self.seq += 1;
                            out.push(Out::Apply(next));
                        }
                        None => match self.decide() {
                            Some(best) => {
                                self.chosen = Some(best.clone());
                                self.phase = Phase::Finishing { since: now, sent: true };
                                self.seq += 1;
                                out.push(Out::Apply(Step { point: best.point, watts: best.limit, kind: Kind::Confirm }));
                            }
                            None => {
                                self.phase = Phase::Done;
                                out.push(Out::Failed("no step had readings (no draw from the telemetry or no STATUS line from the worker)".into()));
                            }
                        },
                    }
                }
            }
            Phase::Finishing { since, sent } => {
                let best = self.chosen.clone().expect("a chosen row while finishing");
                let step = Step { point: best.point, watts: best.limit, kind: Kind::Confirm };
                if !sent {
                    self.phase = Phase::Finishing { since: now, sent: true };
                    self.seq += 1;
                    out.push(Out::Apply(step));
                } else if Run::applied(&step, rb, self.last_watts()) || self.plan.kind == PlanKind::Baseline {
                    self.phase = Phase::Done;
                    out.push(Out::Finished(best));
                } else if now.duration_since(since) > apply {
                    self.phase = Phase::Done;
                    out.push(Out::Failed(format!("the chosen point ({} MHz, {:.0} W) did not take within {} s", best.point.clock_mhz, best.limit, apply.as_secs())));
                }
            }
            Phase::Done => {}
        }
        out
    }

    /// The limit the previous step left (the before limit for the first).
    fn last_watts(&self) -> f64 {
        self.rows.last().map(|r| r.limit).unwrap_or(self.before_w)
    }

    /// What the plan concludes: the full plan's choice; the confirm plan keeps its prior unless the neighbour beat
    /// it (then the prior still holds the card and the engine schedules the full plan); the baseline is itself.
    fn decide(&self) -> Option<Row> {
        match self.plan.kind {
            PlanKind::Baseline => self.rows.first().cloned().filter(|r| r.usable()),
            PlanKind::Confirm => match confirm_verdict(&self.rows, self.plan.tolerance_pct) {
                Verdict::Keep(r) => Some(r),
                Verdict::FullDue { prior, .. } if prior.usable() => Some(prior),
                Verdict::FullDue { better, .. } => Some(better),
                Verdict::NoReadings => None,
            },
            PlanKind::Full => choose(&self.rows, self.plan.tolerance_pct),
            PlanKind::Climb => {
                if self.plan.climb.as_ref().map(|c| c.goal) == Some(Goal::MaxRate) {
                    self.rows.iter().filter(|r| r.usable()).max_by(|a, b| a.mhs.partial_cmp(&b.mhs).unwrap_or(std::cmp::Ordering::Equal)).cloned()
                } else {
                    choose(&self.rows, self.plan.tolerance_pct)
                }
            }
        }
    }

    /// After a confirm plan: did the neighbour beat the prior (the full plan is due)?
    pub fn full_due(&self) -> bool {
        self.plan.kind == PlanKind::Confirm && matches!(confirm_verdict(&self.rows, self.plan.tolerance_pct), Verdict::FullDue { .. })
    }
}

/// The card row's line once a card is tuned: "Tuned: 122.3 MH/s at 290 W (0.422 MH/W)".
pub fn tuned_line(mhs: f64, watts: f64, eff: f64) -> String {
    format!("Tuned: {mhs:.1} MH/s at {watts:.0} W ({eff:.3} MH/W)")
}

/// HH:MM UTC of a unix time (the day is not shown: "since 18:39 UTC").
pub fn hhmm_utc(unix: f64) -> String {
    let s = unix.max(0.0) as u64 % 86_400;
    format!("{:02}:{:02}", s / 3600, (s % 3600) / 60)
}

/// Horizon polish Q83: the one sentence every surface shows while finality is paused. The cause is the node's own
/// (`reason`, with who holds it) when it carries one, else the plain two-thirds line; never the word "final".
pub fn finality_paused_line(since_unix: f64, reason: &str, held_by: &str) -> String {
    let cause = if reason.trim().is_empty() {
        "under two thirds of the weight is signing".to_string()
    } else if held_by.trim().is_empty() {
        reason.trim().to_string()
    } else {
        format!("{} (held by {})", reason.trim(), held_by.trim())
    };
    format!("Finality paused since {} UTC: {cause}", hhmm_utc(since_unix))
}

/// The node's structured finality fields on igneum_getProvingStatus (the node lane, an earlier release): "finalityReason"
/// (empty when live), "finalityProvisional", "heldBy" {tableIndex, stayersShareBps, expiresDaa} or null,
/// "pausedSinceMs" or null. Returns (reason, held-by words, provisional, paused_since unix seconds).
pub struct FinalityStatus {
    /// the node's own word on whether finality is live (b2e21447: finalityActive); None on an older node
    pub active: Option<bool>,
    pub reason: String,
    pub held_by: String,
    pub provisional: bool,
    pub paused_since: Option<f64>,
}

pub fn parse_finality_status(v: &serde_json::Value) -> Option<FinalityStatus> {
    // the node's words begin "paused: ..." (b2e21447); the app's sentence already says "Finality paused since", so
    // that prefix goes; a reason that already names who holds it ("held by weight table 7, ...") is not repeated
    let reason = v.get("finalityReason")?.as_str().unwrap_or("").trim().trim_start_matches("paused:").trim().to_string();
    let active = v.get("finalityActive").and_then(|b| b.as_bool());
    let provisional = v.get("finalityProvisional").and_then(|b| b.as_bool()).unwrap_or(false);
    let held_by = match v.get("heldBy") {
        Some(h) if h.is_object() => {
            let idx = h.get("tableIndex").and_then(|x| x.as_u64()).unwrap_or(0);
            let bps = h.get("stayersShareBps").and_then(|x| x.as_u64()).unwrap_or(0);
            let exp = h.get("expiresDaa").and_then(|x| x.as_u64()).unwrap_or(0);
            format!("weight table {idx}, {}.{:02}% still signing, expires at DAA {exp}", bps / 100, bps % 100)
        }
        _ => String::new(),
    };
    let held_by = if reason.contains("held by") { String::new() } else { held_by };
    let paused_since = v.get("pausedSinceMs").and_then(|x| x.as_u64()).filter(|ms| *ms > 0).map(|ms| ms as f64 / 1000.0);
    Some(FinalityStatus { active, reason, held_by, provisional, paused_since })
}

/// The node's pause line, when it carries one: `... finality_reason=<words or "quoted words"> held_by=<id>`.
/// Returns (reason, held_by); None when the line is not one.
pub fn parse_finality_line(text: &str) -> Option<(String, String)> {
    let i = text.find("finality_reason=")?;
    let rest = &text[i + "finality_reason=".len()..];
    let (reason, after) = if let Some(q) = rest.strip_prefix('"') {
        let end = q.find('"')?;
        (q[..end].to_string(), &q[end + 1..])
    } else {
        let end = rest.find(' ').unwrap_or(rest.len());
        (rest[..end].replace('_', " "), &rest[end..])
    };
    let held_by = after.find("held_by=").map(|j| after[j + 8..].split_whitespace().next().unwrap_or("").to_string()).unwrap_or_default();
    Some((reason, held_by))
}

/// Miner UI 4 (6 October 2026): the fleet's draw now: the sum over cards that mine and have a draw reading under
/// `max_age_s` old (a stale reading is not a draw).
pub fn fleet_watts<'a>(cards: impl Iterator<Item = (&'a str, f64, f64)>, now: f64, max_age_s: f64) -> f64 {
    cards.filter(|(state, w, at)| *state == "mining" && *w > 0.0 && now - *at < max_age_s).map(|(_, w, _)| w).sum()
}

/// A hex quantity (0x-prefixed, as eth_getBalance answers) as a decimal string; None when it is not one.
pub fn wei_from_hex(hex: &str) -> Option<String> {
    let h = hex.trim().strip_prefix("0x").unwrap_or(hex.trim());
    if h.is_empty() {
        return Some("0".into());
    }
    u128::from_str_radix(h, 16).ok().map(|v| v.to_string())
}

/// Miner UI 4 (6 October 2026): a card's own goal when set, else the global one.
pub fn goal_for(card_goal: &str, global: &str) -> Goal {
    Goal::parse(if card_goal.trim().is_empty() { global } else { card_goal })
}

/// (D) May a step be SET on this card? Measure-only (no control: Apple, NVIDIA without Power control, an AMD card
/// whose probe gave no tune line) sets nothing: the run notices the missing acknowledgement and measures. An AMD
/// card also needs its ADLX ordinal. 6 October 2026, the first Windows machine 17:57Z: a measure-only 9070 XT was sent a set, refused.
pub fn may_set(vendor: &str, tune_control: bool, amd_ordinal: i64) -> bool {
    match vendor {
        "nvidia" => tune_control,
        "amd" => tune_control && amd_ordinal >= 0,
        _ => false,
    }
}

/// (E) The row's words while a back-off from a failed tune holds: when the retry comes and why it stopped.
pub fn retry_note(remaining_s: u64, last_note: &str) -> String {
    let mins = (remaining_s + 59) / 60;
    let why = last_note.trim_start_matches("tuning stopped: ").trim_start_matches("tuning: ").trim();
    if why.is_empty() {
        format!("tuning: retry in {mins} min")
    } else {
        format!("tuning: retry in {mins} min ({why})")
    }
}

/// The row's line for a baseline (measure-only) result: the card was measured as it runs, nothing was set.
/// (6 October 2026: the first Windows machine's rows read "Tuned: 114.2 MH/s at 305 W" for a baseline, which is not a tune.)
pub fn measured_line(mhs: f64, watts: f64, eff: f64) -> String {
    format!("Measured: {mhs:.1} MH/s at {watts:.0} W ({eff:.3} MH/W)")
}

/// The row's line for a plan's result.
pub fn result_line(kind: PlanKind, mhs: f64, watts: f64, eff: f64) -> String {
    if kind == PlanKind::Baseline {
        measured_line(mhs, watts, eff)
    } else {
        tuned_line(mhs, watts, eff)
    }
}

/// The core-clock knob's result on a card (7 October 2026; the UI lane's field shape): the chosen lock against the cap
/// point's unlocked row, and the stop reason in words.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct LockResult {
    /// the chosen core clock cap (0 = unlocked)
    pub lock_mhz: u32,
    pub lock_mhs: f64,
    pub lock_w: f64,
    pub lock_mhw: f64,
    /// the cap point's row (clock 0): the rate and draw the lock is read against
    pub unlocked_mhs: f64,
    pub unlocked_w: f64,
    /// "rate fell 5.1 percent at 1,200 MHz", "fingerprint mismatch at 1,400 MHz, clocks reset", "the floor at 618 MHz",
    /// "no lever" (a card without a clock cap), "" while nothing ran
    pub lock_note: String,
}

/// The knob's result from a finished plan's rows and its chosen row.
pub fn lock_result(plan: &Plan, rows: &[Row], chosen: &Row) -> LockResult {
    let cap = plan.cap_row(rows);
    let (unlocked_mhs, unlocked_w) = cap.as_ref().map(|c| (c.mhs, c.watts)).unwrap_or((0.0, 0.0));
    let ran_clocks = rows.iter().any(|r| r.point.clock_mhz > 0);
    let note = if plan.limits.clock_max_mhz == 0 {
        "no lever".to_string()
    } else if !ran_clocks {
        String::new()
    } else {
        plan.clock_stop_reason(rows).unwrap_or_else(|| format!("stopped at {} MHz", rows.last().map(|r| r.point.clock_mhz).unwrap_or(0)))
    };
    LockResult { lock_mhz: chosen.point.clock_mhz, lock_mhs: chosen.mhs, lock_w: chosen.watts, lock_mhw: chosen.eff, unlocked_mhs, unlocked_w, lock_note: note }
}

/// What igneum-gpu-telemetry's `tune` line says about an AMD card (src/engine.rs parse_amd_tune, the fields the knob
/// needs): the max-clock value and range, the power-limit offset range, and whether the line read ok.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct AmdTuneShape {
    pub ok: bool,
    pub error: String,
    pub gmax: f64,
    pub gmax_min: f64,
    pub gmax_max: f64,
    pub plimit_min: f64,
    pub plimit_max: f64,
}

/// The AMD core-clock and power-cap knob (an earlier release, the project's order of 8 October 2026: the first Windows machine's RX 9070 XT measured 18.9 MH/s
/// at 202 W and stopped, no lever). ADLX gives two levers, both offsets: the power limit in percent of the default
/// (`plimit_range -30 10` on the 9070 XT) and the max GPU clock in MHz from the stock boost (`gmax 0 gmax_range -500
/// 1000`); older drivers give the clock as absolute MHz. The knob carries the stock clock, so the ladder runs in
/// absolute MHz like NVIDIA's and the apply sends the offset. Set from an unelevated process (ADLX manual tuning needs
/// no elevation, confirmed on the first Windows machine, 5 October 2026): the no-prompt rule holds with no Power Helper verb.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct AmdKnob {
    /// the stock boost clock the offsets apply to (the card's clock under load when the probe answered); 0 = absolute MHz
    pub stock_mhz: u32,
    pub gmax_offset: bool,
    pub clock_max_mhz: u32,
    pub clock_min_mhz: u32,
    pub plimit_min: f64,
    pub plimit_max: f64,
}

/// The knob, or "not available (<reason>)" and nothing set. Known-failed first on no AMD device.
pub fn amd_knob(shape: Option<&AmdTuneShape>, gclk_now_mhz: f64, linux: bool) -> Result<AmdKnob, String> {
    let Some(t) = shape else { return Err("not available (no AMD device answered igneum-gpu-telemetry --tune: no card, the tool not next to the app, or no ADLX)".into()) };
    if !t.ok {
        return Err(format!("not available (igneum-gpu-telemetry: {})", if t.error.is_empty() { "the tune line read no ok" } else { t.error.as_str() }));
    }
    if linux {
        return Err("not available (Linux: the clock and power limits under /sys need root; the tune's helper takes them in a later cut)".into());
    }
    // the first Windows machine's integrated Radeon (8 October 2026, the exe's read-back): `gmax - gmax_range - - plimit - plimit_range - - factory 0 ok`,
    // every field a dash (-1 after the parser), an ok line with no tuning interface behind it
    if t.gmax_max <= t.gmax_min && t.plimit_max <= t.plimit_min {
        return Err("not available (the driver exposes no tuning interface for this card: the tune line carries no clock or power range)".into());
    }
    if t.plimit_max < t.plimit_min || t.plimit_min <= -100.0 {
        return Err(format!("not available (the power-limit range {} to {} percent is not usable)", t.plimit_min, t.plimit_max));
    }
    let offset = t.gmax_min < 0.0;
    let (clock_max, clock_min, stock) = if offset {
        let stock = gclk_now_mhz.round() as u32;
        if stock == 0 {
            return Err("not available (the max clock is an offset from the stock clock and the stock clock is not known until the card mines)".into());
        }
        (stock, (stock as f64 + t.gmax_min).max(1.0) as u32, stock)
    } else if t.gmax_max > 0.0 {
        (t.gmax_max as u32, if t.gmax_min > 0.0 { t.gmax_min as u32 } else { 0 }, 0)
    } else {
        (0, 0, 0)
    };
    Ok(AmdKnob { stock_mhz: stock, gmax_offset: offset, clock_max_mhz: clock_max, clock_min_mhz: clock_min, plimit_min: t.plimit_min, plimit_max: t.plimit_max })
}

/// The plan's limits for the knob: the power ladder on the percent scale (default 100, floor 100 + plimit_min, ceiling
/// 100 + plimit_max), the clock ladder from the stock clock down to stock + gmax_min.
pub fn amd_limits(k: &AmdKnob) -> Limits {
    Limits { power_default_w: 100.0, power_min_w: 100.0 + k.plimit_min, power_max_w: 100.0 + k.plimit_max, clock_max_mhz: k.clock_max_mhz, clock_min_mhz: k.clock_min_mhz, mem_default_mhz: 0, mem_max_mhz: 0, power_ceiling_w: 0.0, prior_knee_mhz: 0 }
}

/// The `--set-gmax` argument for a clock cap: the offset from stock when the driver speaks offsets, else the MHz.
pub fn amd_gmax_arg(clock_mhz: u32, stock_mhz: u32, offset: bool) -> i64 {
    if offset { clock_mhz as i64 - stock_mhz as i64 } else { clock_mhz as i64 }
}

/// Why a card cannot be tuned beyond measuring, or None when both knobs are available.
pub fn control_reason(vendor: &str, limits: &Limits, device: &str, power_control: bool, amd_helper: bool) -> Option<String> {
    match vendor {
        "nvidia" if limits.power_default_w <= 0.0 || device.is_empty() => Some("measure only: nvidia-smi did not report this card's limits".into()),
        "nvidia" if !power_control => Some("measure only until Power control is on in Settings (Windows asks for administrator rights once)".into()),
        "nvidia" => None,
        "amd" if !amd_helper => Some("measure only: not available (igneum-gpu-telemetry gave no tune line for this card: not next to the app, an older helper, or no ADLX)".into()),
        "amd" if cfg!(target_os = "linux") => Some("measure only on Linux: the clock and power limits under /sys need root".into()),
        "amd" => None,
        "apple" => Some("measure only on Apple silicon: the system sets the clocks and the power; no control exposed".into()),
        _ => Some("measure only: no power or clock control for this card".into()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The 9070 XT's tune line as the first Windows machine answered it (ember-tune-pc1-1, 6 October 2026, 22:30 UTC): offsets, not MHz.
    fn t9070() -> AmdTuneShape {
        AmdTuneShape { ok: true, error: String::new(), gmax: 0.0, gmax_min: -500.0, gmax_max: 1000.0, plimit_min: -30.0, plimit_max: 10.0 }
    }

    /// Known-failed first (an earlier release, the project's order of 8 October 2026): no AMD device means "not available (<reason>)" and
    /// nothing set; the 9070 XT's offset range used to close the clock knob (an earlier release to an earlier release: `clock_max_mhz: 0` when
    /// gmax_min < 0), so the card measured one point and stopped.
    #[test]
    fn the_amd_knob_reports_not_available_with_its_reason_and_sets_nothing() {
        let e = amd_knob(None, 2970.0, false).unwrap_err();
        assert!(e.starts_with("not available (no AMD device"), "{e}");
        let bad = AmdTuneShape { ok: false, error: "no tuning services (ADLX too old, or no AMD driver)".into(), ..Default::default() };
        let e = amd_knob(Some(&bad), 2970.0, false).unwrap_err();
        assert_eq!(e, "not available (igneum-gpu-telemetry: no tuning services (ADLX too old, or no AMD driver))");
        let e = amd_knob(Some(&t9070()), 0.0, false).unwrap_err();
        assert!(e.contains("stock clock is not known until the card mines"), "{e}");
        let e = amd_knob(Some(&t9070()), 2970.0, true).unwrap_err();
        assert!(e.starts_with("not available (Linux"), "{e}");
        // the integrated Radeon's line on the first Windows machine (8 October 2026): ok, every range a dash; known-failed first (it read as an
        // offset knob with a one-MHz ladder and a 99 percent power range)
        let igpu = AmdTuneShape { ok: true, error: String::new(), gmax: -1.0, gmax_min: -1.0, gmax_max: -1.0, plimit_min: -1.0, plimit_max: -1.0 };
        let e = amd_knob(Some(&igpu), 2100.0, false).unwrap_err();
        assert!(e.starts_with("not available (the driver exposes no tuning interface"), "{e}");
        assert!(control_reason("amd", &Limits::default(), "amd:gfx1201", true, false).unwrap().contains("not available ("));
    }

    /// The 9070 XT under load at 2,970 MHz (the shape of its boost clock, not a measurement): the plan carries the power
    /// ladder on the percent scale and the clock ladder from stock down to stock minus 500 in 100 MHz steps.
    #[test]
    fn the_9070_xt_gets_a_power_ladder_and_a_clock_ladder_from_its_stock_clock() {
        let k = amd_knob(Some(&t9070()), 2970.0, false).unwrap();
        assert_eq!(k, AmdKnob { stock_mhz: 2970, gmax_offset: true, clock_max_mhz: 2970, clock_min_mhz: 2470, plimit_min: -30.0, plimit_max: 10.0 });
        let l = amd_limits(&k);
        assert_eq!((l.power_default_w, l.power_min_w, l.power_max_w), (100.0, 70.0, 110.0));
        assert_eq!(l.clock_floor(), 2470, "the floor is the range's, not 20 percent of the stock");
        let p = Plan::full(&l, Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, 1.0);
        let pw: Vec<u32> = p.power.iter().map(|s| s.point.power_pct).collect();
        assert!(pw.contains(&100) && pw.contains(&70) && !pw.contains(&60), "{pw:?}: the AMD power ladder stops at the range floor");
        let clocks: Vec<u32> = p.clock_pcts.iter().map(|pct| l.clamp_clock(l.clock_max_mhz * pct / 100)).chain(p.clock_fine.iter().copied()).collect();
        assert_eq!(clocks, vec![2870, 2770, 2670, 2570, 2470], "a narrow range: no percent rungs (they would all clamp to the floor), the fine ladder in 100 MHz steps to the floor");
        assert_eq!(amd_gmax_arg(2670, 2970, true), -300, "the apply sends the offset");
        assert_eq!(amd_gmax_arg(2670, 0, false), 2670, "an absolute driver gets the MHz");
        // an absolute range (older drivers): MHz straight through
        let abs = AmdTuneShape { ok: true, gmax: 2400.0, gmax_min: 500.0, gmax_max: 2600.0, plimit_min: -20.0, plimit_max: 15.0, ..Default::default() };
        let k = amd_knob(Some(&abs), 0.0, false).unwrap();
        assert_eq!((k.stock_mhz, k.gmax_offset, k.clock_max_mhz, k.clock_min_mhz), (0, false, 2600, 500));
    }

    /// A declared ladder in the 9070 XT's shape (18.9 MH/s at 202 W unlocked; flat to the knee, then the rate falls): the
    /// knob's result names the lock, the unlocked row and the stop reason, as on NVIDIA.
    #[test]
    fn the_9070_xt_ladder_locks_at_the_knee() {
        let k = amd_knob(Some(&t9070()), 2970.0, false).unwrap();
        let l = amd_limits(&k);
        let p = Plan::full(&l, Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, 1.0);
        // the power ladder first (100 to 70 percent on the AMD scale; 70 is the knee), then the clock ladder at the cap point
        let mut rows = Vec::new();
        for (pct, w, mhs) in [(100, 202.0, 18.9), (90, 190.0, 18.9), (80, 175.0, 18.85), (70, 160.0, 17.5)] {
            rows.push(row_at(Point { clock_mhz: 0, power_pct: pct, mem_mhz: 0 }, w, mhs));
        }
        assert_eq!(p.power.len(), 4, "{:?}", p.power.iter().map(|s| s.point.power_pct).collect::<Vec<_>>());
        for (mhz, w, mhs) in [(2870, 170.0, 18.85), (2770, 165.0, 18.85), (2670, 160.0, 18.8), (2570, 152.0, 17.5)] {
            rows.push(row_at(Point { clock_mhz: mhz, power_pct: 80, mem_mhz: 0 }, w, mhs));
        }
        let best = rows.iter().cloned().max_by(|a, b| a.eff.partial_cmp(&b.eff).unwrap()).unwrap();
        let r = lock_result(&p, &rows, &best);
        assert_eq!(r.lock_mhz, 2670, "{r:?}");
        assert!((r.unlocked_mhs - 18.85).abs() < 1e-9 && (r.unlocked_w - 175.0).abs() < 1e-9, "the cap point is the 80 percent row: {r:?}");
        assert_ne!(r.lock_note, "no lever", "{r:?}");
    }

    /// the first Windows machine's RTX 5090 (nvidia-smi, 4 and 5 October 2026): default 575 W, min 400 W, max 600 W; clocks.max.gr is
    /// read at the first tune (3,090 MHz is the shape used here, not a measurement).
    fn l5090() -> Limits {
        Limits { power_default_w: 575.0, power_min_w: 400.0, power_max_w: 600.0, clock_max_mhz: 3090, clock_min_mhz: 0, mem_default_mhz: 13801, mem_max_mhz: 14001, power_ceiling_w: 0.0, prior_knee_mhz: 0 }
    }

    fn with_ceiling(name: &str, before: Point, power_control: bool) -> Limits {
        let mut l = l5090();
        l.power_ceiling_w = power_ceiling(&l, name, before, power_control);
        l
    }
    fn max_watts(plan: &Plan) -> f64 {
        let mut rows = Vec::new();
        let mut top: f64 = 0.0;
        while let Some(s) = plan.next(&rows) {
            top = top.max(s.watts);
            rows.push(row_at(s.point, s.watts, 100.0));
            if rows.len() > 40 { break; }
        }
        top
    }

    /// the second Windows machine, 7 October 2026: the tuner asked the 5090 for 100% (575 W) while proving ran on the same card; the PC dropped
    /// nine minutes later. The known-failed shape first: without a ceiling the full plan opens at 575 W.
    #[test]
    fn without_a_ceiling_the_full_plan_asks_the_5090_for_575_w() {
        let plan = Plan::full(&l5090(), Point { clock_mhz: 0, power_pct: 70, mem_mhz: 0 }, 1.0);
        assert_eq!(plan.next(&[]).unwrap().watts, 575.0);
    }

    #[test]
    fn the_tuner_never_asks_above_the_measured_efficient_point() {
        // the 5090's measured point is 308 W; the vendor's minimum is 400 W, so the ceiling clamps up to 400
        let l = with_ceiling("NVIDIA GeForce RTX 5090", Point { clock_mhz: 0, power_pct: 70, mem_mhz: 0 }, false);
        assert_eq!(l.power_ceiling_w, 308.0);
        assert_eq!(l.ceiling(), 400.0);
        let plan = Plan::full(&l, Point { clock_mhz: 0, power_pct: 70, mem_mhz: 0 }, 1.0);
        let first = plan.next(&[]).unwrap();
        assert_eq!((first.watts, first.point.power_pct, first.kind), (400.0, 70, Kind::Power), "one power step at the ceiling, with its own percent");
        // the knob (an earlier release): the six percent rungs below unlocked plus the 100 MHz fine steps from 45 to 20 percent of the 5090's 3,090 MHz
        assert_eq!(plan.len(), 1 + 6 + 7, "one power step (every ladder step clamps to 400 W), the six clock rungs and the seven fine steps");
        assert!(max_watts(&plan) <= 400.0, "no step above the ceiling");
        // the confirm plan and the climb honour it too: a fleet prior at 100% is clamped
        let prior = Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 };
        assert!(max_watts(&Plan::confirm(&l, prior, Point { clock_mhz: 0, power_pct: 70, mem_mhz: 0 }, 1.0)) <= 400.0);
        assert!(max_watts(&Plan::climb(&l, prior, Goal::Efficiency, 1.0)) <= 400.0);
    }

    #[test]
    fn power_control_on_with_a_cap_the_user_raised_lifts_the_ceiling_to_that_cap() {
        let l = with_ceiling("NVIDIA GeForce RTX 5090", Point { clock_mhz: 0, power_pct: 90, mem_mhz: 0 }, true);
        assert_eq!(l.ceiling(), 518.0, "the user's 90% (518 W), never the vendor's 575 W");
        assert!(max_watts(&Plan::full(&l, Point { clock_mhz: 0, power_pct: 90, mem_mhz: 0 }, 1.0)) <= 518.0);
        // Power control on at the default cap is not a raise: the measured point holds
        let d = with_ceiling("NVIDIA GeForce RTX 5090", Point { clock_mhz: 0, power_pct: DEFAULT_CAP_PCT, mem_mhz: 0 }, true);
        assert_eq!(d.ceiling(), 400.0);
        // a raised cap with Power control OFF does not count
        let off = with_ceiling("NVIDIA GeForce RTX 5090", Point { clock_mhz: 0, power_pct: 90, mem_mhz: 0 }, false);
        assert_eq!(off.ceiling(), 400.0);
    }

    #[test]
    fn a_card_not_in_the_table_is_held_at_the_cap_it_runs_at() {
        let l = with_ceiling("NVIDIA GeForce RTX 4070", Point { clock_mhz: 0, power_pct: 70, mem_mhz: 0 }, false);
        assert_eq!(l.ceiling(), 403.0, "70% of 575 W, the card's own cap");
        let zero = with_ceiling("NVIDIA GeForce RTX 4070", Point::default(), false);
        assert_eq!(zero.ceiling(), 460.0, "no chosen cap: the app's default 80%");
        assert_eq!(efficient_watts("AMD Radeon RX 9070 XT"), Some(149.0), "the 8 October grid: both floors, 149.3 W at 18.96 MH/s");
        assert_eq!(efficient_watts("gfx1036"), None);
    }

    fn row_at(p: Point, watts: f64, mhs: f64) -> Row {
        Row { point: p, limit: watts, watts, mhs, eff: if watts > 0.0 { mhs / watts } else { 0.0 }, draws: 12, rates: 6, gclk: 0.0, mclk: 0.0, tmax: 60.0, faults: 0, mark: Some(Mark::Ok) }
    }

    #[test]
    fn the_full_plan_is_the_power_ladder_then_the_clock_ladder_at_the_chosen_power() {
        let plan = Plan::full(&l5090(), Point { clock_mhz: 0, power_pct: 80, mem_mhz: 0 }, 1.0);
        // five power steps (60% and 50% clamp to 400 W; one kept), six percent rungs (90% down to 45% = 1,390) and the
        // fine ladder 1,300 down to the 20% floor (618): 1,300, 1,200, ..., 700 = 7 steps
        assert_eq!(plan.len(), 5 + 6 + 7);
        let first = plan.next(&[]).unwrap();
        assert_eq!((first.point, first.watts, first.kind), (Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, 575.0, Kind::Power));
        // the power ladder: 575, 518, 460, 403, 400
        let mut rows = Vec::new();
        let mut watts_seen = Vec::new();
        for i in 0..5 {
            let s = plan.next(&rows).unwrap();
            assert_eq!(s.kind, Kind::Power, "step {i}");
            watts_seen.push(s.watts);
            // the draw never reaches the cap (the first Windows machine's shape: 290 W under every limit); the rate is flat
            rows.push(row_at(s.point, 290.0 + i as f64 * 0.1, 124.0));
        }
        assert_eq!(watts_seen, vec![575.0, 518.0, 460.0, 403.0, 400.0]);
        // the clock ladder rides the chosen power point: a flat ladder ties on efficiency and rate, the lowest draw wins (100%)
        let s = plan.next(&rows).unwrap();
        assert_eq!(s.kind, Kind::Clock);
        assert_eq!(s.point, Point { clock_mhz: 2781, power_pct: 100, mem_mhz: 0 });
        rows.push(row_at(s.point, 250.0, 123.8));
        let s = plan.next(&rows).unwrap();
        assert_eq!(s.point.clock_mhz, 2472);
        rows.push(row_at(s.point, 220.0, 123.5));
        let s = plan.next(&rows).unwrap();
        assert_eq!(s.point.clock_mhz, 2163, "70%");
        rows.push(row_at(s.point, 200.0, 118.0));
        // the stop rule (7 October 2026): 118 is 4.8% under the cap point's 124, past the 1% tolerance, so the search
        // ends here (the 6 October ladder went on to 1,854, 1,545 and 1,390)
        assert_eq!(plan.next(&rows), None);
        assert_eq!(plan.clock_stop_reason(&rows).as_deref(), Some("rate fell 4.8 percent at 2163 MHz"));
        // the choice: 2,472 MHz keeps 99.6% of the top rate at 220 W = 0.561 MH/W; 2,163 MHz (118 MH/s) is outside the 1% tolerance
        let best = choose(&rows, 1.0).unwrap();
        assert_eq!(best.point, Point { clock_mhz: 2472, power_pct: 100, mem_mhz: 0 });
        // a wider tolerance lets the 2,163 MHz step (0.590 MH/W, 4.8% slower) win
        assert_eq!(choose(&rows, 5.0).unwrap().point.clock_mhz, 2163);
        // no power limits, clocks only; no clocks, power only; nothing, empty
        // clocks only: six percent rungs (1,800 .. 900) then the fine ladder 800 .. 400 (the 20% floor) = 5 more
        assert_eq!(Plan::full(&Limits { clock_max_mhz: 2000, ..Default::default() }, Point::default(), 1.0).len(), 6 + 5);
        assert_eq!(Plan::full(&Limits { power_default_w: 300.0, ..Default::default() }, Point::default(), 1.0).len(), 6);
        assert!(Plan::full(&Limits::default(), Point::default(), 1.0).is_empty());
    }

    #[test]
    fn a_power_step_on_a_card_without_a_limit_readback_is_applied_on_the_acknowledgement() {
        // AMD through ADLX: the limit reads back as an offset, never watts (run 6, 6 October 2026)
        let step = Step { point: Point { clock_mhz: 0, power_pct: 90, mem_mhz: 0 }, watts: 90.0, kind: Kind::Power };
        assert!(Run::applied(&step, Readback { limit_w: 0.0, acked: true }, 100.0));
        assert!(!Run::applied(&step, Readback { limit_w: 0.0, acked: false }, 100.0));
        // NVIDIA: the watts must read back
        assert!(!Run::applied(&step, Readback { limit_w: 100.0, acked: true }, 100.0));
        assert!(Run::applied(&step, Readback { limit_w: 90.0, acked: false }, 100.0));
    }

    #[test]
    fn the_fleet_draw_sums_mining_cards_with_a_fresh_reading_and_the_balance_reads_in_wei() {
        let now = 1_791_300_000.0;
        let cards = vec![("mining", 226.8, now - 5.0), ("mining", 75.6, now - 10.0), ("mining", 201.0, now - 120.0), ("off", 30.0, now - 1.0), ("mining", 0.0, now)];
        let w = fleet_watts(cards.iter().map(|(s, w, at)| (*s, *w, *at)), now, 60.0);
        assert!((w - 302.4).abs() < 1e-9, "the stale 9070 XT reading and the card that is off do not count: {w}");
        assert!((pounds_per_day(302.4, 28.5) - 2.068416).abs() < 1e-6);
        assert_eq!(wei_from_hex("0x1a"), Some("26".into()));
        assert_eq!(wei_from_hex("0x0"), Some("0".into()));
        assert_eq!(wei_from_hex("0x"), Some("0".into()));
        assert_eq!(wei_from_hex("0xde0b6b3a7640000"), Some("1000000000000000000".into()), "one IGN");
        assert_eq!(wei_from_hex("soon"), None);
    }

    #[test]
    fn the_finality_pause_line_names_the_time_and_the_cause_and_never_says_final() {
        // 6 October 2026 18:39:00Z
        let since = 1_791_311_940.0;
        assert_eq!(hhmm_utc(since), "18:39");
        let plain = finality_paused_line(since, "", "");
        assert_eq!(plain, "Finality paused since 18:39 UTC: under two thirds of the weight is signing");
        assert!(!plain.to_ascii_lowercase().contains("final "), "no 'final' word while paused: {plain}");
        assert_eq!(finality_paused_line(since, "a checkpoint vote is split", "ae432dc7"), "Finality paused since 18:39 UTC: a checkpoint vote is split (held by ae432dc7)");
        assert_eq!(parse_finality_line("1791311940 finality paused finality_reason=\"vote split at index 412\" held_by=ae432dc7"), Some(("vote split at index 412".into(), "ae432dc7".into())));
        assert_eq!(parse_finality_line("x finality_reason=under_two_thirds"), Some(("under two thirds".into(), String::new())));
        assert_eq!(parse_finality_line("status: accepted 3 blocks"), None);
        // the structured carrier on igneum_getProvingStatus (the node lane's field names)
        // the node's own words (b2e21447): "paused: under two thirds ... ({p}%; the table frozen at lock {j} has {q}% signing)"
        let v: serde_json::Value = serde_json::from_str(r#"{"v1":{"active":true},"finalityActive":false,"finalityReason":"paused: under two thirds of the weight is signing (61%; the table frozen at lock 7 has 58% signing)","finalityProvisional":false,"heldBy":null,"pausedSinceMs":1791311940000}"#).unwrap();
        let f = parse_finality_status(&v).unwrap();
        assert_eq!((f.active, f.provisional, f.paused_since), (Some(false), false, Some(1_791_311_940.0)));
        assert_eq!(finality_paused_line(f.paused_since.unwrap(), &f.reason, &f.held_by), "Finality paused since 18:39 UTC: under two thirds of the weight is signing (61%; the table frozen at lock 7 has 58% signing)");
        // the held-by form: the reason names the table itself, so heldBy is not repeated
        let v2: serde_json::Value = serde_json::from_str(r#"{"finalityActive":false,"finalityReason":"paused: held by weight table 7, 61% of it signing, expires at DAA 205000","finalityProvisional":true,"heldBy":{"tableIndex":7,"stayersShareBps":6150,"expiresDaa":205000},"pausedSinceMs":1791311940000}"#).unwrap();
        let g = parse_finality_status(&v2).unwrap();
        assert!(g.provisional && g.held_by.is_empty());
        assert_eq!(finality_paused_line(g.paused_since.unwrap(), &g.reason, &g.held_by), "Finality paused since 18:39 UTC: held by weight table 7, 61% of it signing, expires at DAA 205000");
        // a reason without the table words keeps the heldBy suffix
        let v3: serde_json::Value = serde_json::from_str(r#"{"finalityActive":false,"finalityReason":"paused: no checkpoint determined above the latest lock","heldBy":{"tableIndex":7,"stayersShareBps":6150,"expiresDaa":205000}}"#).unwrap();
        assert_eq!(parse_finality_status(&v3).unwrap().held_by, "weight table 7, 61.50% still signing, expires at DAA 205000");
        // live finality: an empty reason, null heldBy, null pausedSinceMs
        let live: serde_json::Value = serde_json::from_str(r#"{"finalityReason":"","finalityProvisional":false,"heldBy":null,"pausedSinceMs":null}"#).unwrap();
        let l = parse_finality_status(&live).unwrap();
        assert!(l.reason.is_empty() && l.held_by.is_empty() && !l.provisional && l.paused_since.is_none() && l.active.is_none());
        // a node before an earlier release carries none of it
        assert!(parse_finality_status(&serde_json::json!({"v1":{"active":true}})).is_none());
    }

    #[test]
    fn a_card_goal_overrides_the_global_goal_and_empty_follows_it() {
        assert_eq!(goal_for("", "balanced"), Goal::Balanced);
        assert_eq!(goal_for("rate", "balanced"), Goal::MaxRate);
        assert_eq!(goal_for("efficiency", "rate"), Goal::Efficiency);
        assert_eq!(goal_for("  ", "efficiency"), Goal::Efficiency);
    }

    #[test]
    fn a_measure_only_card_is_never_set_and_a_back_off_says_when_the_retry_comes() {
        assert!(!may_set("amd", false, 1), "the probe gave no tune line: measure only");
        assert!(may_set("amd", true, 1));
        assert!(!may_set("amd", true, -1), "no ADLX ordinal");
        assert!(!may_set("nvidia", false, -1), "Power control off: measure only");
        assert!(may_set("nvidia", true, -1));
        assert!(!may_set("apple", true, -1));
        assert_eq!(retry_note(3599, "tuning stopped: the card refused the setting (see the log)"), "tuning: retry in 60 min (the card refused the setting (see the log))");
        assert_eq!(retry_note(30, ""), "tuning: retry in 1 min");
    }

    #[test]
    fn a_baseline_result_reads_measured_and_a_tune_reads_tuned() {
        assert_eq!(result_line(PlanKind::Baseline, 114.2, 305.0, 0.3744), "Measured: 114.2 MH/s at 305 W (0.374 MH/W)");
        assert_eq!(result_line(PlanKind::Full, 127.71, 226.8, 0.5632), "Tuned: 127.7 MH/s at 227 W (0.563 MH/W)");
        assert_eq!(result_line(PlanKind::Confirm, 127.71, 226.8, 0.5632), "Tuned: 127.7 MH/s at 227 W (0.563 MH/W)");
    }

    #[test]
    fn limits_never_exceed_the_vendor_or_undercut_the_floor() {
        let l = l5090();
        assert_eq!(l.clock_floor(), 618, "20% of 3,090 (7 October 2026; the 45% floor of 6 October sat on the 5090's knee)");
        assert_eq!(l.clamp_clock(1000), 1000);
        assert_eq!(l.clamp_clock(500), 618);
        assert_eq!(l.clamp_clock(5000), 3090);
        assert_eq!(l.clamp_clock(0), 0, "unlocked stays unlocked");
        assert_eq!(Limits { clock_max_mhz: 3000, clock_min_mhz: 2100, ..Default::default() }.clamp_clock(1500), 2100, "the vendor's floor wins over the 20% rule");
        assert_eq!(l.watts_for(100), 575.0);
        assert_eq!(l.watts_for(50), 400.0);
        assert_eq!(Limits { power_default_w: 300.0, power_max_w: 250.0, ..Default::default() }.watts_for(100), 250.0);
    }

    #[test]
    fn the_choice_keeps_the_best_mh_per_watt_within_the_rate_tolerance() {
        let p = |c: u32, pct: u32| Point { clock_mhz: c, power_pct: pct, mem_mhz: 0 };
        // a card that gets more efficient as the cap drops until it collapses: 70% wins (0.280 MH/W), 60% is 35% slower
        let rows = vec![row_at(p(0, 100), 560.0, 124.0), row_at(p(0, 90), 510.0, 123.5), row_at(p(0, 80), 455.0, 123.2), row_at(p(0, 70), 400.0, 122.9), row_at(p(0, 60), 345.0, 80.0)];
        assert_eq!(choose(&rows, 1.0).unwrap().point, p(0, 70));
        // the tolerance: a 2% slower point with better MH/W loses at 1% and wins at 3%
        let rows = vec![row_at(p(0, 100), 300.0, 100.0), row_at(p(2000, 100), 240.0, 98.0)];
        assert_eq!(choose(&rows, 1.0).unwrap().point, p(0, 100));
        assert_eq!(choose(&rows, 3.0).unwrap().point, p(2000, 100));
        // within 1% on efficiency the higher rate wins; within 1% on both the lower draw
        let rows = vec![row_at(p(0, 100), 500.0, 125.0), row_at(p(0, 80), 400.5, 100.3)];
        assert_eq!(choose(&rows, 25.0).unwrap().point, p(0, 100));
        let flat = vec![row_at(p(0, 100), 290.0, 124.0), row_at(p(0, 90), 290.5, 123.5), row_at(p(0, 80), 289.8, 124.2), row_at(p(0, 70), 290.2, 123.9)];
        assert_eq!(choose(&flat, 1.0).unwrap().point, p(0, 80), "289.8 W is the lowest draw");
        // marked rows never win, whatever their numbers
        let mut faulted = row_at(p(1800, 100), 150.0, 123.0);
        faulted.mark = Some(Mark::Faulted);
        let mut hot = row_at(p(1900, 100), 160.0, 123.0);
        hot.mark = Some(Mark::Hot);
        let mut mem = row_at(p(2000, 100), 170.0, 123.0);
        mem.mark = Some(Mark::MemoryClock);
        assert_eq!(choose(&[faulted.clone(), hot, mem, row_at(p(0, 100), 290.0, 124.0)], 1.0).unwrap().point, p(0, 100));
        assert!(choose(&[faulted], 1.0).is_none());
        assert!(choose(&[], 1.0).is_none());
    }

    #[test]
    fn the_guards_mark_a_step_so_it_cannot_win() {
        let step = Step { point: Point { clock_mhz: 2000, power_pct: 100, mem_mhz: 0 }, watts: 575.0, kind: Kind::Clock };
        let good = Samples { draws: vec![250.0, 251.0, 249.0], rates: vec![123.0], gclks: vec![1998.0], mclks: vec![2505.0], tmax: 70.0, faults: 0, unapplied: false };
        assert_eq!(Row::from_samples(&step, &good, 2505.0).mark, Some(Mark::Ok));
        // one rejected or mismatched hash: faulted
        assert_eq!(Row::from_samples(&step, &Samples { faults: 1, ..good.clone() }, 2505.0).mark, Some(Mark::Faulted));
        // the GPU at 85 C: hot
        assert_eq!(Row::from_samples(&step, &Samples { tmax: 85.0, ..good.clone() }, 2505.0).mark, Some(Mark::Hot));
        // the memory clock dragged under 95% of the baseline's: marked (the AMD gmax path on RDNA 4 cannot hold it)
        assert_eq!(Row::from_samples(&step, &Samples { mclks: vec![2300.0], ..good.clone() }, 2505.0).mark, Some(Mark::MemoryClock));
        assert_eq!(Row::from_samples(&step, &Samples { mclks: vec![2400.0], ..good.clone() }, 2505.0).mark, Some(Mark::Ok), "2,400 is 95.8%");
        // the readback disagreed: unapplied
        assert_eq!(Row::from_samples(&step, &Samples { unapplied: true, ..good.clone() }, 2505.0).mark, Some(Mark::Unapplied));
        // under 3 draws or no rate: no readings, and the line says so
        let r = Row::from_samples(&step, &Samples { draws: vec![250.0], ..good.clone() }, 0.0);
        assert_eq!(r.mark, Some(Mark::NoReadings));
        assert!(!r.usable());
        assert!(r.line("c", 3).contains("mark=no_readings"), "{}", r.line("c", 3));
        let r = Row::from_samples(&step, &good, 0.0);
        assert_eq!(r.line("nvidia-ae432dc7-1", 7), "TUNE card=nvidia-ae432dc7-1 step=7 clock=2000 cap=100 mem=0 limit=575 watts=250.0 mhs=123.00 eff=0.4920 gclk=1998 mclk=2505 tmax=70 mark=ok");
    }

    #[test]
    fn a_fault_during_a_step_reverts_it_and_the_run_goes_on() {
        let t0 = Instant::now();
        let timing = Timing { settle: Duration::from_secs(2), hold: Duration::from_secs(4), apply: Duration::from_secs(30) };
        let limits = Limits { power_default_w: 300.0, power_min_w: 150.0, power_max_w: 300.0, clock_max_mhz: 0, clock_min_mhz: 0, mem_default_mhz: 0, mem_max_mhz: 0, power_ceiling_w: 0.0, prior_knee_mhz: 0 };
        let plan = Plan::full(&limits, Point { clock_mhz: 0, power_pct: 80, mem_mhz: 0 }, 1.0);
        let mut run = Run::new(0, "0", "c", plan, 240.0, false, timing, t0);
        let mut t = t0;
        let mut limit = 240.0;
        let mut applied: Vec<f64> = Vec::new();
        let mut rows = Vec::new();
        let mut finished = None;
        for _ in 0..600 {
            for o in run.tick(t, Readback { limit_w: limit, acked: true }) {
                match o {
                    Out::Apply(s) => {
                        applied.push(s.watts);
                        limit = s.watts;
                    }
                    Out::Row(r) => rows.push(r),
                    Out::Finished(r) => finished = Some(r),
                    Out::Failed(e) => panic!("{e}"),
                }
            }
            if matches!(run.phase, Phase::Holding { .. }) {
                run.sample_telemetry(limit.min(260.0), 2500.0, 1300.0, 60.0);
                run.sample_telemetry(limit.min(260.0) + 1.0, 2500.0, 1300.0, 60.0);
                run.sample_telemetry(limit.min(260.0) - 1.0, 2500.0, 1300.0, 60.0);
                run.sample_rate(100.0);
                // the 70% step (210 W) sees a mismatched hash
                if (limit - 210.0).abs() < 0.5 {
                    run.sample_fault();
                }
            }
            if run.phase == Phase::Done {
                break;
            }
            t += Duration::from_secs(1);
        }
        assert_eq!(rows.len(), 6, "300, 270, 240, 210, 180, 150 W");
        assert_eq!(rows[3].point.power_pct, 70);
        assert_eq!(rows[3].mark, Some(Mark::Faulted), "the faulted step is marked");
        assert!(rows[3].faults >= 1);
        // 100, 90 and 80% read the same 260 W draw at the same rate; 60% (150 W, the clamp) too in this model
        // because the draw model caps at 260 W: the lowest draw wins among the ties, never the faulted 70%
        let f = finished.expect("finished");
        assert_ne!(f.point.power_pct, 70);
        assert_eq!(applied.len(), 7, "every step's cap, then the chosen one");
        assert_eq!(*applied.last().unwrap(), f.limit);
    }

    #[test]
    fn the_confirm_plan_checks_the_prior_and_its_neighbour() {
        let l = l5090();
        let prior = Point { clock_mhz: 2472, power_pct: 100, mem_mhz: 0 };
        let plan = Plan::confirm(&l, prior, Point { clock_mhz: 0, power_pct: 80, mem_mhz: 0 }, 1.0);
        assert_eq!(plan.len(), 2);
        let a = plan.next(&[]).unwrap();
        assert_eq!((a.point, a.kind), (prior, Kind::Confirm));
        let b = plan.next(&[row_at(prior, 220.0, 123.5)]).unwrap();
        assert_eq!(b.point, Point { clock_mhz: 2781, power_pct: 100, mem_mhz: 0 }, "one clock step up");
        // the neighbour within 1%: the prior stands
        let rows = vec![row_at(prior, 220.0, 123.5), row_at(b.point, 250.0, 123.8)];
        assert!(matches!(confirm_verdict(&rows, 1.0), Verdict::Keep(r) if r.point == prior));
        // the neighbour 3% better on MH/W: the full plan is due
        let rows = vec![row_at(prior, 220.0, 120.0), row_at(b.point, 212.0, 120.0)];
        assert!(matches!(confirm_verdict(&rows, 1.0), Verdict::FullDue { .. }));
        assert_eq!(confirm_verdict(&[], 1.0), Verdict::NoReadings);
        // an unlocked prior at 100%: the neighbour is one power step down; at the top clock the neighbour is unlocked
        let plan = Plan::confirm(&l, Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, Point::default(), 1.0);
        assert_eq!(plan.next(&[row_at(Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, 1.0, 1.0)]).unwrap().point, Point { clock_mhz: 0, power_pct: 90, mem_mhz: 0 });
        let plan = Plan::confirm(&l, Point { clock_mhz: 2900, power_pct: 100, mem_mhz: 0 }, Point::default(), 1.0);
        assert_eq!(plan.next(&[row_at(Point { clock_mhz: 2900, power_pct: 100, mem_mhz: 0 }, 1.0, 1.0)]).unwrap().point.clock_mhz, 0);
        // a prior outside the vendor's range is clamped, never applied as is
        let plan = Plan::confirm(&l, Point { clock_mhz: 9000, power_pct: 30, mem_mhz: 0 }, Point::default(), 1.0);
        assert_eq!(plan.next(&[]).unwrap().point, Point { clock_mhz: 3090, power_pct: 50, mem_mhz: 0 });
    }

    #[test]
    fn a_baseline_plan_measures_the_card_as_it_runs() {
        let t0 = Instant::now();
        let timing = Timing { settle: Duration::from_secs(1), hold: Duration::from_secs(2), apply: Duration::from_secs(3) };
        let before = Point { clock_mhz: 0, power_pct: 80, mem_mhz: 0 };
        let mut run = Run::new(0, "0", "c", Plan::baseline(&l5090(), before, 1.0), 460.0, false, timing, t0);
        // nothing acknowledges the request (Power control is off): the apply window passes and the hold starts anyway
        let mut t = t0;
        let mut finished = None;
        for _ in 0..40 {
            for o in run.tick(t, Readback { limit_w: 460.0, acked: false }) {
                match o {
                    Out::Finished(r) => finished = Some(r),
                    Out::Failed(e) => panic!("{e}"),
                    _ => {}
                }
            }
            if matches!(run.phase, Phase::Holding { .. }) {
                for w in [290.0, 291.0, 289.0] {
                    run.sample_telemetry(w, 2700.0, 10500.0, 66.0);
                }
                run.sample_rate(122.3);
            }
            if run.phase == Phase::Done {
                break;
            }
            t += Duration::from_secs(1);
        }
        let f = finished.expect("the baseline finished");
        assert_eq!(f.point, before);
        assert!((f.eff - 122.3 / 290.0).abs() < 1e-6);
        assert_eq!(tuned_line(f.mhs, f.watts, f.eff), "Tuned: 122.3 MH/s at 290 W (0.422 MH/W)");
    }

    #[test]
    fn the_record_and_the_prior_round_trip_through_the_manifest_shape() {
        let chosen = row_at(Point { clock_mhz: 2472, power_pct: 100, mem_mhz: 0 }, 220.0, 123.5);
        let rec = record_json(1_791_230_000.0, "8f3a2c1d", "0.1.10", "windows", "NVIDIA GeForce RTX 5090", "nvidia", "581.57", "l128w16", PlanKind::Full, &[chosen.clone()], Some(&chosen), None, false);
        assert_eq!(rec["floor"], false);
        assert_eq!(rec["card"], "NVIDIA_GeForce_RTX_5090");
        assert_eq!(rec["key"], "NVIDIA_GeForce_RTX_5090|581|l128w16");
        assert_eq!(rec["driver_major"], "581");
        assert_eq!(rec["plan"], "full");
        assert_eq!(rec["chosen"]["clock_mhz"], 2472);
        assert_eq!(rec["eff"], 0.5614);
        assert!(rec.get("address").is_none() && rec.get("host").is_none());
        // the manifest's tuning section: the kernel-variant cards object stays, priors and ember sit beside it
        let tuning: serde_json::Value = serde_json::json!({
            "updated": "2026-10-05T22:00:00Z",
            "cards": { "NVIDIA_GeForce_RTX_5090": { "variant": "u2-ldg", "race": true, "candidates": ["u2-ldg", "ldg", "base"] } },
            "ember": { "enabled": true, "min_samples": 5, "rate_tolerance_pct": 1.0 },
            "priors": { "NVIDIA_GeForce_RTX_5090|581|l128w16": { "clock_mhz": 2472, "power_pct": 100, "eff": 0.561, "mhs": 123.5, "watts": 220.0, "spread_pct": 2.1, "samples": 7 },
                        "AMD_Radeon_RX_9070_XT|32|l128w16": { "clock_mhz": 2600, "power_pct": 90, "eff": 0.1, "mhs": 17.7, "watts": 177.0, "spread_pct": 4.0, "samples": 3 } }
        });
        let s = settings_of(Some(&tuning));
        assert_eq!(s, Settings { enabled: true, min_samples: 5, tolerance_pct: 1.0, period_s: PERIOD_S });
        let p = prior_of(Some(&tuning), "NVIDIA_GeForce_RTX_5090|581|l128w16", s.min_samples).unwrap();
        assert_eq!(p.point, Point { clock_mhz: 2472, power_pct: 100, mem_mhz: 0 });
        assert_eq!(p.samples, 7);
        assert!(prior_of(Some(&tuning), "AMD_Radeon_RX_9070_XT|32|l128w16", 5).is_none(), "3 samples are under the floor");
        assert!(prior_of(Some(&tuning), "AMD_Radeon_RX_9070_XT|32|l128w16", 3).is_some());
        assert!(prior_of(Some(&tuning), "nothing|0|v2", 5).is_none());
        assert!(prior_of(None, "x", 5).is_none());
        // the kill switch, and the defaults without a section
        let off: serde_json::Value = serde_json::json!({ "cards": {}, "ember": { "enabled": false } });
        assert!(!settings_of(Some(&off)).enabled);
        assert_eq!(settings_of(None), Settings::default());
        assert_eq!(settings_of(Some(&serde_json::json!({ "cards": {}, "ember": { "rate_tolerance_pct": 99 } }))).tolerance_pct, 1.0, "out of range: the default");
        assert_eq!(prior_key("AMD Radeon RX 9070 XT", "32.0.15801.1", ""), "AMD_Radeon_RX_9070_XT|32|v2");
        assert_eq!(driver_major(""), "0");
        assert_eq!(program_class(128, 16), "l128w16");
        assert_eq!(program_class(0, 0), "v2");
    }

    /// Ember 2: a synthetic memory-latency-bound card. Rate rises 1% per 100 MHz of memory above the default and
    /// falls only 0.3% per 100 MHz of core below the maximum; draw falls 8 W per 100 MHz of core and rises 2 W per
    /// 100 MHz of memory. The climb must walk memory up and core down and converge in under five probes.
    fn synthetic(p: Point) -> Row {
        let mem = if p.mem_mhz == 0 { 13801.0 } else { p.mem_mhz as f64 };
        let core = if p.clock_mhz == 0 { 3090.0 } else { p.clock_mhz as f64 };
        let mhs = 127.0 * (1.0 + 0.015 * (mem - 13801.0) / 100.0) * (1.0 - 0.003 * (3090.0 - core) / 100.0);
        let watts = 310.0 - 8.0 * (3090.0 - core) / 100.0 + 2.0 * (mem - 13801.0) / 100.0;
        Row { point: p, limit: 575.0, watts, mhs, eff: mhs / watts, draws: 12, rates: 6, gclk: core, mclk: mem, tmax: 65.0, faults: 0, mark: Some(Mark::Ok) }
    }

    #[test]
    fn the_climb_walks_memory_up_and_core_down_and_converges_in_five_probes() {
        // a 2 GHz memory range above the default (the headroom a 5090 has in practice; the first Windows machine's driver reports 14,001)
        let l = Limits { mem_max_mhz: 15801, ..l5090() };
        let plan = Plan::climb(&l, Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, Goal::Balanced, 1.0);
        assert_eq!(plan.len(), 5);
        let mut rows = Vec::new();
        while let Some(step) = plan.next(&rows) {
            assert_eq!(step.kind, Kind::Climb);
            rows.push(synthetic(step.point));
        }
        assert_eq!(rows.len(), 5, "the budget");
        assert_eq!(rows[0].point, Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, "it starts at the start point");
        // every probe moved one knob the right way: memory never down, core never up, and both inside the vendor's range
        for w in rows.windows(2) {
            let (a, b) = (w[0].point, w[1].point);
            assert!(b.mem_mhz >= a.mem_mhz || b.mem_mhz == 0, "{a:?} -> {b:?}");
            assert!(b.mem_mhz <= 15801 && (b.clock_mhz == 0 || b.clock_mhz >= 1854), "{b:?}");
        }
        let best = choose(&rows, plan.tolerance_pct).unwrap();
        assert!(best.eff > rows[0].eff, "the chosen point beats the start: {:.4} > {:.4}", best.eff, rows[0].eff);
        assert!(best.point.mem_mhz > 13801 || (best.point.clock_mhz > 0 && best.point.clock_mhz < 3090), "it moved a knob");
    }

    #[test]
    fn a_refused_probe_backs_that_knob_off_for_good() {
        let l = Limits { mem_max_mhz: 15801, ..l5090() };
        let plan = Plan::climb(&l, Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, Goal::Balanced, 1.0);
        let mut rows = vec![synthetic(plan.next(&[]).unwrap().point)];
        // the first probe (memory up) draws a mismatched hash: refused
        let probe = plan.next(&rows).unwrap();
        assert!(probe.point.mem_mhz > 13801, "memory first: {:?}", probe.point);
        let mut bad = synthetic(probe.point);
        bad.faults = 1;
        bad.mark = Some(Mark::Faulted);
        rows.push(bad);
        // from here every probe leaves the memory clock alone
        while let Some(step) = plan.next(&rows) {
            assert_eq!(step.point.mem_mhz, 0, "memory backed off: {:?}", step.point);
            rows.push(synthetic(step.point));
        }
        assert!(rows.len() >= 3 && rows.len() <= 5);
        assert!(choose(&rows, 1.0).unwrap().point.mem_mhz == 0);
    }

    #[test]
    fn each_goal_picks_its_point() {
        let l = l5090();
        let p = |c: u32, m: u32| Point { clock_mhz: c, power_pct: 100, mem_mhz: m };
        // three points: the fastest (slightly less efficient), the most efficient (4% slower), and the start
        let rows = vec![synthetic(p(0, 0)), synthetic(p(0, 14001)), synthetic(p(1854, 0))];
        let eff_plan = Plan::climb(&l, p(0, 0), Goal::Efficiency, 1.0);
        let bal_plan = Plan::climb(&l, p(0, 0), Goal::Balanced, 1.0);
        let rate_plan = Plan::climb(&l, p(0, 0), Goal::MaxRate, 1.0);
        assert_eq!(eff_plan.tolerance_pct, 10.0);
        assert_eq!(bal_plan.tolerance_pct, 1.0);
        assert_eq!(rate_plan.tolerance_pct, 0.0);
        let by = |plan: &Plan| rows.iter().filter(|r| r.usable() && r.mhs >= rows.iter().map(|x| x.mhs).fold(0.0, f64::max) * (1.0 - plan.tolerance_pct / 100.0)).max_by(|a, b| plan.score(a).partial_cmp(&plan.score(b)).unwrap()).unwrap().point;
        assert_eq!(by(&rate_plan), p(0, 14001), "maximum rate takes the fastest point");
        assert_eq!(by(&eff_plan), p(1854, 0), "efficiency takes the most MH/W inside 10%");
        assert_eq!(by(&bal_plan), p(0, 14001), "balanced keeps within 1% of the top rate");
        assert_eq!(Goal::parse("efficiency"), Goal::Efficiency);
        assert_eq!(Goal::parse("rate"), Goal::MaxRate);
        assert_eq!(Goal::parse("anything"), Goal::Balanced);
        assert!((pounds_per_day(310.0, 28.5) - 2.1204).abs() < 1e-3, "310 W a day at 28.5 p/kWh = £2.12");
    }

    #[test]
    fn control_reasons_per_vendor() {
        let l = l5090();
        assert!(control_reason("nvidia", &l, "0", true, false).is_none());
        assert!(control_reason("nvidia", &l, "0", false, false).unwrap().contains("Power control"));
        assert!(control_reason("nvidia", &Limits::default(), "0", true, false).unwrap().contains("limits"));
        assert!(control_reason("apple", &Limits::default(), "", true, false).unwrap().contains("Apple silicon"));
        assert!(control_reason("amd", &Limits::default(), "1", false, false).unwrap().contains("igneum-gpu-telemetry"));
        if cfg!(target_os = "linux") {
            assert!(control_reason("amd", &Limits::default(), "1", false, true).unwrap().contains("root"));
        } else {
            assert!(control_reason("amd", &Limits::default(), "1", false, true).is_none());
        }
    }

    /// The core-clock knob (7 October 2026, the the first Windows machine efficiency passes): a flat ladder walks below the old 45 percent
    /// floor in 100 MHz steps to the 20 percent floor, and the result names the floor.
    #[test]
    fn the_clock_ladder_continues_below_45_percent_in_100_mhz_steps_to_the_floor() {
        let plan = Plan::full(&l5090(), Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, 1.0);
        let mut rows = Vec::new();
        for _ in 0..5 {
            let s = plan.next(&rows).unwrap();
            rows.push(row_at(s.point, 300.0, 136.8));
        }
        let mut clocks = Vec::new();
        while let Some(s) = plan.next(&rows) {
            assert_eq!(s.kind, Kind::Clock);
            clocks.push(s.point.clock_mhz);
            // the rate holds (memory-bound): the draw falls with the clock
            rows.push(row_at(s.point, 300.0 - clocks.len() as f64 * 10.0, 136.0));
        }
        assert_eq!(clocks, vec![2781, 2472, 2163, 1854, 1545, 1390, 1300, 1200, 1100, 1000, 900, 800, 700]);
        assert_eq!(plan.clock_stop_reason(&rows).as_deref(), Some("the floor at 700 MHz"));
        let chosen = choose(&rows, 1.0).unwrap();
        assert_eq!(chosen.point.clock_mhz, 700, "flat rate: the lowest draw wins");
        let r = lock_result(&plan, &rows, &chosen);
        assert_eq!((r.lock_mhz, r.lock_w, r.unlocked_mhs, r.unlocked_w), (700, 170.0, 136.8, 300.0));
        assert_eq!(r.lock_note, "the floor at 700 MHz");
    }

    /// The stop rule on the first Windows machine's RTX 5090 rows of 7 October 2026 (class v3, the card alone): the first clock row more
    /// than the tolerance under the cap point's rate ends the search and the best MH per watt among the rows within
    /// tolerance is chosen. At the 1 percent tolerance the 5090's rate (136.6 at 2,781) is 1.24 percent down at
    /// 1,545 MHz, so the search ends there and 1,854 MHz (135.6 MH/s at 239.6 W) is the point; at 1.5 percent it runs
    /// on to 1,200 (5.2 percent down) and 1,300 MHz is the point. The tolerance is the manifest's.
    #[test]
    fn the_clock_search_stops_at_the_knee_and_names_it() {
        let measured: Vec<(u32, f64, f64)> = vec![(2781, 317.9, 136.6), (2472, 276.0, 136.4), (2163, 252.4, 136.1), (1854, 239.6, 135.6), (1545, 232.1, 134.9), (1390, 229.0, 134.85), (1300, 223.3, 134.6), (1200, 215.7, 129.5)];
        let walk = |tolerance: f64| -> (Plan, Vec<Row>) {
            let plan = Plan::full(&Limits { clock_max_mhz: 3090, ..Default::default() }, Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, tolerance);
            let mut rows = vec![];
            for (mhz, w, mhs) in &measured {
                let Some(s) = plan.next(&rows) else { break };
                assert_eq!(s.point.clock_mhz, *mhz);
                rows.push(row_at(s.point, *w, *mhs));
            }
            (plan, rows)
        };
        let (plan, rows) = walk(1.0);
        assert_eq!(rows.last().unwrap().point.clock_mhz, 1545, "the search ends on the first row over 1 percent under the cap row");
        assert_eq!(plan.next(&rows), None);
        let reason = plan.clock_stop_reason(&rows).unwrap();
        assert!(reason.starts_with("rate fell 1.2 percent at 1545 MHz"), "{reason}");
        let chosen = choose(&rows, 1.0).unwrap();
        assert_eq!(chosen.point.clock_mhz, 1854, "the best MH per watt within 1 percent of the fastest row");
        let r = lock_result(&plan, &rows, &chosen);
        assert_eq!((r.lock_mhz, r.unlocked_mhs), (1854, 136.6));
        assert!((r.lock_mhw - 135.6 / 239.6).abs() < 1e-6);
        let (plan, rows) = walk(1.5);
        assert_eq!(rows.last().unwrap().point.clock_mhz, 1200);
        assert_eq!(plan.next(&rows), None);
        assert!(plan.clock_stop_reason(&rows).unwrap().starts_with("rate fell 5.2 percent at 1200 MHz"));
        assert_eq!(choose(&rows, 1.5).unwrap().point.clock_mhz, 1300);
    }

    /// The fingerprint rule: a clock row marked Faulted (a rejected or mismatched hash during the hold) ends the search
    /// at once; the choice is made among the usable rows and the note says why.
    #[test]
    fn a_faulted_clock_row_ends_the_search_and_the_note_says_so() {
        let plan = Plan::full(&Limits { clock_max_mhz: 3090, ..Default::default() }, Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, 1.0);
        let mut rows = vec![];
        for (mhz, w, mhs) in [(2781, 317.9, 136.6), (2472, 276.0, 136.4)] {
            let s = plan.next(&rows).unwrap();
            assert_eq!(s.point.clock_mhz, mhz);
            rows.push(row_at(s.point, w, mhs));
        }
        let s = plan.next(&rows).unwrap();
        assert_eq!(s.point.clock_mhz, 2163);
        let mut bad = row_at(s.point, 252.4, 136.1);
        bad.faults = 1;
        bad.mark = Some(Mark::Faulted);
        rows.push(bad);
        assert_eq!(plan.next(&rows), None, "the search ends on the faulted row");
        assert_eq!(plan.clock_stop_reason(&rows).as_deref(), Some("fingerprint mismatch at 2163 MHz, clocks reset"));
        let chosen = choose(&rows, 1.0).unwrap();
        assert_eq!(chosen.point.clock_mhz, 2472, "the faulted row never wins");
        assert_eq!(lock_result(&plan, &rows, &chosen).lock_note, "fingerprint mismatch at 2163 MHz, clocks reset");
    }

    /// The same through the state machine with a fake helper (the known-failed case first: a mismatch mid-search must
    /// reset and abort): the run applies 2,781 and 2,472, a fault lands during 2,163's hold, the row comes out Faulted,
    /// the next step is none, and the run's final Apply is the chosen 2,472 point (the reset), then Finished.
    #[test]
    fn a_mismatch_mid_search_resets_to_the_chosen_point_and_finishes() {
        let plan = Plan::full(&Limits { clock_max_mhz: 3090, ..Default::default() }, Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, 1.0);
        let timing = Timing { settle: Duration::from_secs(1), hold: Duration::from_secs(2), apply: Duration::from_secs(3) };
        let t0 = Instant::now();
        let mut run = Run::new(0, "0", "card-0", plan, 300.0, false, timing, t0);
        let mut t = t0;
        let mut applied: Vec<Step> = Vec::new();
        let mut finished: Option<Row> = None;
        let watts_for = |mhz: u32| -> f64 { match mhz { 2781 => 317.9, 2472 => 276.0, _ => 252.4 } };
        for _ in 0..200 {
            t += Duration::from_millis(500);
            let acked = true;
            let limit = run.current.as_ref().map(|s| s.watts).unwrap_or(0.0);
            // the fake helper: every setting takes; during 2,163's hold the worker reports a mismatched hash
            if let Some(cur) = run.current.clone() {
                if matches!(run.phase, Phase::Holding { .. }) {
                    run.sample_rate(136.4);
                    run.sample_telemetry(watts_for(cur.point.clock_mhz), cur.point.clock_mhz as f64, 13801.0, 60.0);
                    if cur.point.clock_mhz == 2163 {
                        run.sample_fault();
                    }
                }
            }
            for o in run.tick(t, Readback { limit_w: limit, acked }) {
                match o {
                    Out::Apply(s) => applied.push(s),
                    Out::Finished(r) => finished = Some(r),
                    Out::Failed(e) => panic!("the run failed: {e}"),
                    Out::Row(_) => {}
                }
            }
            if finished.is_some() {
                break;
            }
        }
        let clocks: Vec<u32> = applied.iter().map(|s| s.point.clock_mhz).collect();
        assert_eq!(clocks, vec![2781, 2472, 2163, 2472], "2,781, 2,472, the faulted 2,163, then the reset to the chosen 2,472");
        assert_eq!(applied.last().unwrap().kind, Kind::Confirm);
        let f = finished.expect("finished");
        assert_eq!(f.point.clock_mhz, 2472);
        assert_eq!(run.rows.len(), 3);
        assert_eq!(run.rows[2].mark, Some(Mark::Faulted));
        assert_eq!(run.plan.clock_stop_reason(&run.rows).as_deref(), Some("fingerprint mismatch at 2163 MHz, clocks reset"));
    }

    // ---- the search's prior by architecture (8 October 2026) ----
    #[test]
    fn a_prior_starts_the_ladders_near_the_knee_and_no_prior_keeps_the_full_ladder() {
        // known-failed first: a 5090 without the prior walks the whole percent ladder and the fine ladder from the top
        let full = Plan::full(&l5090(), Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, 1.0);
        let l = Limits { prior_knee_mhz: knee_clock_prior("NVIDIA GeForce RTX 5090"), ..l5090() };
        assert_eq!(l.prior_knee_mhz, 1300);
        let p = Plan::full(&l, Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, 1.0);
        assert!(p.len() < full.len(), "the prior shortens the search: {} against {}", p.len(), full.len());
        // the percent rungs above 1,300 + 309 MHz are gone; the fine ladder starts within two fine steps of the prior
        assert!(p.clock_pcts.iter().all(|&pc| 3090 * pc / 100 <= 1300 + 309), "{:?}", p.clock_pcts);
        assert!(p.clock_fine.iter().all(|&m| m <= 1300 + 2 * CLOCK_FINE_STEP_MHZ), "{:?}", p.clock_fine);
        assert!(p.clock_fine.contains(&1300) || p.clock_pcts.iter().any(|&pc| (3090 * pc / 100).abs_diff(1300) < 200));
        // the stock row stays first on the power ladder
        assert_eq!(p.power.first().map(|s| s.point.power_pct), full.power.first().map(|s| s.point.power_pct));
    }
    #[test]
    fn the_architecture_priors_read_the_sweep_lanes_numbers() {
        assert_eq!(knee_clock_prior("NVIDIA GeForce RTX 3080 Ti"), 1800);
        assert_eq!(knee_clock_prior("NVIDIA RTX A4000"), 1800);
        assert_eq!(knee_clock_prior("NVIDIA GeForce RTX 4070 SUPER"), 2400);
        assert_eq!(knee_clock_prior("NVIDIA GeForce RTX 5080"), 1000);
        assert_eq!(knee_clock_prior("AMD Radeon RX 9070 XT"), 0);
        assert_eq!(knee_clock_prior("Apple M5 Max"), 0);
        // the power rung for an Ada card: 2,400 of 2,790 MHz is 0.64 cubed, 64 percent, the 70 rung
        let ada = Limits { power_default_w: 220.0, power_min_w: 110.0, power_max_w: 240.0, clock_max_mhz: 2790, clock_min_mhz: 0, mem_default_mhz: 0, mem_max_mhz: 0, power_ceiling_w: 0.0, prior_knee_mhz: 2400 };
        assert_eq!(prior_power_pct(&ada), Some(70));
        let p = Plan::full(&ada, Point { clock_mhz: 0, power_pct: 100, mem_mhz: 0 }, 1.0);
        let pcts: Vec<u32> = p.power.iter().map(|s| s.point.power_pct).collect();
        assert_eq!(pcts, vec![100, 80, 70, 60, 50], "the stock row, then from one rung above the prior down");
        // no prior, or a prior at or above the maximum: the full ladder
        assert_eq!(prior_power_pct(&l5090()), None);
        assert_eq!(prior_power_pct(&Limits { prior_knee_mhz: 3200, ..l5090() }), None);
    }
    // ---- tiers (8 October 2026): the 5090 and 5080 curves of the night of 7 to 8 October as fixtures ----
    fn curve_row(clock: u32, pct: u32, watts: f64, mhs: f64) -> Row {
        Row { point: Point { clock_mhz: clock, power_pct: pct, mem_mhz: 0 }, limit: watts, watts, mhs, eff: mhs / watts, draws: 12, rates: 6, gclk: clock as f64, mclk: 0.0, tmax: 60.0, faults: 0, mark: Some(Mark::Ok) }
    }
    /// the first Windows machine's RTX 5090, the app's stored full curve (8 October 2026, 06:08Z read): stock 122.71 at 298.4 W, the clock
    /// ladder 2,781 down to 1,390 MHz
    fn curve_5090() -> Vec<Row> {
        vec![
            curve_row(0, 100, 298.4, 122.71), curve_row(0, 90, 301.0, 122.69), curve_row(0, 80, 301.5, 122.53), curve_row(0, 70, 302.3, 123.05), curve_row(0, 60, 302.7, 122.29),
            curve_row(2781, 100, 292.7, 122.16), curve_row(2472, 100, 255.2, 122.4), curve_row(2163, 100, 231.1, 121.06), curve_row(1854, 100, 221.0, 122.06), curve_row(1545, 100, 213.7, 120.91), curve_row(1390, 100, 204.3, 118.56),
        ]
    }
    #[test]
    fn tiers_of_the_5090_curve_are_three_measured_rows_of_that_search() {
        let t = tiers(&curve_5090());
        assert_eq!(t.len(), 3);
        // efficiency: the best MH/W outright is the 1,390 MHz row (0.5803)
        assert_eq!((t[0].id, t[0].row.point.clock_mhz, t[0].source), ("efficiency", 1390, "measured"));
        // max: the top rate is the 70 percent cap row (123.05), a measured row, not stock
        assert_eq!((t[2].id, t[2].row.point.power_pct, t[2].row.point.clock_mhz, t[2].source), ("max", 70, 0, "measured"));
        // balanced: within 1 percent of 123.05 (121.82 and up) the best MH/W is 1,854 MHz (0.5522)
        assert_eq!((t[1].id, t[1].row.point.clock_mhz, t[1].source), ("balanced", 1854, "measured"));
        assert!(t[1].row.mhs >= 0.99 * t[2].row.mhs);
        assert!(t[0].row.eff >= t[1].row.eff && t[1].row.eff >= t[2].row.eff);
    }
    #[test]
    fn the_max_tier_reads_stock_when_the_unlocked_row_is_the_top() {
        // the first Windows machine's RTX 5080 climb (06:05Z): the stock row at 80 percent is not the stock point; a synthetic stock row on top
        let rows = vec![curve_row(0, 100, 160.0, 61.0), curve_row(2936, 80, 123.2, 60.27), curve_row(0, 80, 149.8, 60.13)];
        let t = tiers(&rows);
        assert_eq!((t[2].id, t[2].source, t[2].row.watts), ("max", "stock", 160.0));
        assert_eq!((t[0].id, t[0].row.point.clock_mhz, t[0].source), ("efficiency", 2936, "measured"));
        // balanced: 60.27 is within 1 percent of 61.0 (60.39)? no: 60.27 < 60.39, so balanced is the stock row itself
        assert_eq!((t[1].id, t[1].source), ("balanced", "stock"));
    }
    #[test]
    fn no_usable_row_gives_no_tier_and_one_row_names_all_three() {
        assert!(tiers(&[]).is_empty());
        let mut bad = curve_row(0, 100, 300.0, 120.0);
        bad.mark = Some(Mark::Faulted);
        assert!(tiers(&[bad]).is_empty());
        let one = vec![curve_row(1854, 100, 221.0, 122.06)];
        let t = tiers(&one);
        assert_eq!(t.iter().map(|x| x.id).collect::<Vec<_>>(), vec!["efficiency", "balanced", "max"]);
        assert!(t.iter().all(|x| x.row.point.clock_mhz == 1854 && x.source == "measured"));
        assert_eq!(tier_row(&t, "balanced").unwrap().point.clock_mhz, 1854);
        assert!(tier_row(&t, "nothing").is_none());
    }
    #[test]
    fn a_tier_read_back_from_the_card_state_json_is_the_row_that_was_written() {
        let t = tiers(&curve_5090());
        let js: Vec<serde_json::Value> = t.iter().map(|x| x.json()).collect();
        let (p, limit, mhs, w) = tier_from_json(&js, "efficiency").unwrap();
        assert_eq!((p.clock_mhz, p.power_pct, p.mem_mhz), (1390, 100, 0));
        assert_eq!((limit, mhs, w), (204.3, 118.56, 204.3));
        assert!(tier_from_json(&js, "nothing").is_none());
        assert!(tier_from_json(&[], "balanced").is_none());
    }
    #[test]
    fn tiers_stored_under_v4_read_stale_under_v5_and_never_apply_as_current() {
        // known-failed first: the stored class and the live class differ
        assert!(tiers_stale("v4", "v5"));
        assert_eq!(stale_text("RTX 5090", "v4", "v5"), "re-measuring RTX 5090 for class v5: the stored tiers were measured under class v4");
        // the same class, or an unknown on either side, is not stale
        assert!(!tiers_stale("v4", "v4"));
        assert!(!tiers_stale("", "v5"));
        assert!(!tiers_stale("v4", ""));
    }
    #[test]
    fn the_knee_loss_reads_the_new_efficiency_row_against_the_old_at_the_same_lock() {
        let old: Vec<serde_json::Value> = tiers(&curve_5090()).iter().map(|t| t.json()).collect();
        let mut new_rows = curve_5090();
        for r in new_rows.iter_mut() { r.mhs *= 0.95; r.eff = r.mhs / r.watts; }
        let new: Vec<serde_json::Value> = tiers(&new_rows).iter().map(|t| t.json()).collect();
        assert_eq!(knee_loss_pct("NVIDIA GeForce RTX 5090", "v5", &old, &new), 5.0);
        // no old set and no table row: unknown reads 0
        assert_eq!(knee_loss_pct("NVIDIA GeForce RTX 5090", "v5", &[], &new), 0.0);
    }
    #[test]
    fn the_tier_json_and_the_set_line_carry_the_fields_the_ui_reads() {
        let t = tiers(&curve_5090());
        let j = t[1].json();
        for k in ["id", "clock_mhz", "power_pct", "w", "mhs", "mhw", "source"] {
            assert!(j.get(k).is_some(), "{k} missing");
        }
        assert_eq!(j["w"], 221.0);
        assert_eq!(j["mhs"], 122.06);
        assert_eq!(tier_set_line("NVIDIA GeForce RTX 5090", "balanced", &t[1].row, 298.4), "Ember Tune set NVIDIA GeForce RTX 5090 to Balanced: 122.1 MH/s at 221 W, 77 W under stock");
        assert_eq!(tier_set_line("card", "max", &t[2].row, 298.4), "Ember Tune set card to Max: 123.0 MH/s at 302 W");
        let st = stock_tier(&curve_row(0, 100, 202.0, 18.9));
        assert_eq!((st.len(), st[0].id, st[0].source), (1, "max", "stock"));
    }

    #[test]
    fn a_windowed_rate_counts_only_once_the_hold_has_outlasted_its_window() {
        let limits = Limits { power_default_w: 300.0, power_min_w: 150.0, power_max_w: 360.0, clock_max_mhz: 2700, clock_min_mhz: 1000, mem_default_mhz: 0, mem_max_mhz: 0, power_ceiling_w: 0.0, prior_knee_mhz: 0 };
        let plan = Plan::baseline(&limits, Point { clock_mhz: 0, power_pct: 80, mem_mhz: 0 }, 1.0);
        let t0 = Instant::now();
        let timing = Timing { settle: Duration::from_secs(1), hold: Duration::from_secs(60), apply: Duration::from_secs(3) };
        let mut run = Run::new(0, "0", "card-0", plan, 240.0, false, timing, t0);
        run.phase = Phase::Holding { since: t0 };
        run.sample_rate_after(120.0, Duration::from_secs(10), t0 + Duration::from_secs(9));
        assert_eq!(run.samples.rates.len(), 0, "the window still holds the settle");
        run.sample_rate_after(120.0, Duration::from_secs(10), t0 + Duration::from_secs(10));
        run.sample_rate_after(121.0, Duration::from_secs(10), t0 + Duration::from_secs(11));
        assert_eq!(run.samples.rates, vec![120.0, 121.0]);
        run.phase = Phase::Settling { since: t0 };
        run.sample_rate_after(120.0, Duration::from_secs(10), t0 + Duration::from_secs(30));
        assert_eq!(run.samples.rates.len(), 2, "not while settling");
    }

}

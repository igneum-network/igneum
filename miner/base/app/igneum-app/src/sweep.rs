//! The efficiency sweep: hash per watt, the number miners compare. For one NVIDIA card at a time the engine steps
//! the power cap from 100% of the card's default limit down to 50% in 10% steps, holds each step for 60 s after a
//! 15 s settle, averages the card's reported draw (nvidia-smi power.draw, the telemetry child) and the worker's
//! interval rate (the STATUS lines), computes MH per watt per step, picks the best step and leaves the cap there.
//! It runs on the live kernel: the worker is never restarted and the hour's program is never lost. It never starts
//! while a remote job holds the GPU, and it aborts the moment the card leaves "mining" (a fault, an hour boundary,
//! a pause, a job).
//!
//! This file is the logic: the step plan, the per-step rows, the choice, the parser for nvidia-smi's power query and
//! the state machine, all driven by an explicit clock so the tests run without a card. The engine (src/engine.rs,
//! `tick_sweep` and the `Cmd::Sweep*` commands) owns the processes: it sets the cap (directly when the engine runs
//! elevated, else through one elevated helper that polls a command file, so a sweep costs one administrator prompt
//! instead of six), reads the limit back through the telemetry child, feeds the samples in and writes the table to
//! the app log:
//!   SWEEP start card=<label> name=<name> steps=100,90,80,70,60,50
//!   SWEEP card=<label> cap=<pct> limit=<set W> watts=<mean draw W> mhs=<MH/s> eff=<MH per W>
//!   SWEEP chosen card=<label> cap=<pct> limit=<W> watts=<W> mhs=<x> eff=<MH per W>
//!   SWEEP aborted card=<label> reason=<text>
//! Apple silicon and AMD are reported unsupported with the reason (no cap to set; powermetrics needs root; no AMD
//! power reading in this app).

use std::collections::HashMap;
use std::time::{Duration, Instant};

/// The cap steps, percent of the card's default limit, highest first.
pub const STEPS_PCT: [u32; 6] = [100, 90, 80, 70, 60, 50];
/// The lowest cap the slider and the sweep may set (the former floor was 60; the sweep needs 50).
pub const MIN_PCT: u32 = 50;
/// A sweep repeats this often when the toggle is on.
pub const PERIOD_S: u64 = 7 * 86_400;
/// The sweep needs at least this long before the hour boundary (6 steps of 75 s plus the cap latencies).
pub const NEEDS_S: i64 = 600;
/// The worker must have been mining this long before a sweep starts (the first status lines are warm-up).
pub const STABLE_S: u64 = 120;

#[derive(Clone, Copy, Debug)]
pub struct Timing {
    pub settle: Duration,
    pub hold: Duration,
    /// how long a cap may take to show in the readback before the sweep gives up
    pub apply: Duration,
}

impl Timing {
    pub fn standard() -> Timing {
        Timing { settle: Duration::from_secs(15), hold: Duration::from_secs(60), apply: Duration::from_secs(30) }
    }
    /// IGNEUM_APP_SWEEP_FAST=1: a dry run in seconds (settle 2 s, hold 6 s), for a local check of the plumbing.
    pub fn from_env() -> Timing {
        if std::env::var("IGNEUM_APP_SWEEP_FAST").map(|v| v == "1").unwrap_or(false) {
            Timing { settle: Duration::from_secs(2), hold: Duration::from_secs(6), apply: Duration::from_secs(30) }
        } else {
            Timing::standard()
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct Step {
    pub pct: u32,
    /// the limit to set, watts, inside the card's min and max
    pub watts: f64,
}

/// The steps for a card: each percent of the default limit, clamped to the card's min and max, rounded to a watt;
/// a step whose watts equal the previous step's (the clamp bit) is dropped, so a card whose floor is 70% of its
/// default sweeps 100, 90, 80, 70 and once more at the floor.
pub fn plan_steps(default_w: f64, min_w: f64, max_w: f64) -> Vec<Step> {
    let mut out: Vec<Step> = Vec::new();
    if default_w <= 0.0 {
        return out;
    }
    for pct in STEPS_PCT {
        let mut w = default_w * pct as f64 / 100.0;
        if min_w > 0.0 {
            w = w.max(min_w);
        }
        if max_w > 0.0 {
            w = w.min(max_w);
        }
        let w = w.round();
        if out.last().map(|s| (s.watts - w).abs() < 0.5).unwrap_or(false) {
            continue;
        }
        out.push(Step { pct, watts: w });
    }
    out
}

/// One step's result. `watts` is the mean draw the card reported during the hold (what the miner pays for),
/// `limit` the cap that was set. `eff` is MH per watt of draw; 0 when the step had no readings.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Row {
    pub pct: u32,
    pub limit: f64,
    pub watts: f64,
    pub mhs: f64,
    pub eff: f64,
    pub draws: usize,
    pub rates: usize,
}

impl Row {
    pub fn from_samples(step: &Step, draws: &[f64], rates: &[f64]) -> Row {
        let mean = |v: &[f64]| if v.is_empty() { 0.0 } else { v.iter().sum::<f64>() / v.len() as f64 };
        let watts = mean(draws);
        let mhs = mean(rates);
        let usable = draws.len() >= 3 && !rates.is_empty() && watts > 1.0;
        Row { pct: step.pct, limit: step.watts, watts, mhs, eff: if usable { mhs / watts } else { 0.0 }, draws: draws.len(), rates: rates.len() }
    }
    pub fn usable(&self) -> bool {
        self.eff > 0.0
    }
    /// `SWEEP card=<label> cap=<pct> limit=<W> watts=<W> mhs=<x> eff=<MH/W>`; a step without readings says so.
    pub fn line(&self, card: &str) -> String {
        if self.usable() {
            format!("SWEEP card={card} cap={} limit={:.0} watts={:.1} mhs={:.2} eff={:.4}", self.pct, self.limit, self.watts, self.mhs, self.eff)
        } else {
            format!("SWEEP card={card} cap={} limit={:.0} watts={:.1} mhs={:.2} eff=0 reason=no_readings draws={} rates={}", self.pct, self.limit, self.watts, self.mhs, self.draws, self.rates)
        }
    }
}

/// The best step: the highest MH per watt. Within 1% of the best, the higher hash rate wins (more blocks for the
/// same efficiency); within 1% on both, the lower cap wins (the same output with more headroom from the limit).
pub fn choose(rows: &[Row]) -> Option<Row> {
    let mut best: Option<&Row> = None;
    for r in rows.iter().filter(|r| r.usable()) {
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
                    } else if r.pct < b.pct {
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

/// Parses `nvidia-smi --query-gpu=index,power.draw --format=csv,noheader,nounits` (and the same query with units,
/// or with a header) into index -> watts. A card that reports `[N/A]` or `[Not Supported]` is left out.
pub fn parse_power_draw(text: &str) -> HashMap<String, f64> {
    let mut out = HashMap::new();
    for line in text.lines() {
        let p: Vec<&str> = line.split(',').map(|s| s.trim()).collect();
        if p.len() < 2 || p[0].is_empty() || !p[0].chars().all(|c| c.is_ascii_digit()) {
            continue;
        }
        let w = p[1].trim_end_matches('W').trim();
        if let Ok(v) = w.parse::<f64>() {
            if v > 0.0 {
                out.insert(p[0].to_string(), v);
            }
        }
    }
    out
}

/// Where the sweep stands, for the tile ("holding 70% · 41 s").
#[derive(Clone, Debug, PartialEq)]
pub enum Phase {
    /// the cap for step `i` was (or is about to be) requested; waiting for the readback to match
    Applying { since: Instant, sent: bool },
    Settling { since: Instant },
    Holding { since: Instant },
    /// the chosen cap was requested; waiting for the readback
    Finishing { since: Instant, sent: bool },
    Done,
}

/// What the engine must do after a tick.
#[derive(Clone, Debug, PartialEq)]
pub enum Out {
    /// set this cap (watts) on the card
    Apply(f64),
    /// a step finished: log its line
    Row(Row),
    /// the sweep finished and the chosen cap is in force
    Finished(Row),
    /// the sweep failed; the engine restores the cap from before
    Failed(String),
}

pub struct Run {
    pub card: usize,
    #[allow(dead_code)]
    pub key: String,
    pub device: String,
    pub label: String,
    pub steps: Vec<Step>,
    pub i: usize,
    pub phase: Phase,
    pub rows: Vec<Row>,
    pub chosen: Option<Row>,
    /// the cap percent and the limit in force before the sweep (restored on abort)
    pub before_pct: u32,
    pub before_w: f64,
    pub started: Instant,
    pub forced: bool,
    pub timing: Timing,
    draws: Vec<f64>,
    rates: Vec<f64>,
}

impl Run {
    pub fn new(card: usize, key: &str, device: &str, label: &str, steps: Vec<Step>, before_pct: u32, before_w: f64, forced: bool, timing: Timing, now: Instant) -> Run {
        Run {
            card,
            key: key.into(),
            device: device.into(),
            label: label.into(),
            steps,
            i: 0,
            phase: Phase::Applying { since: now, sent: false },
            rows: Vec::new(),
            chosen: None,
            before_pct,
            before_w,
            started: now,
            forced,
            timing,
            draws: Vec::new(),
            rates: Vec::new(),
        }
    }

    pub fn current(&self) -> Option<&Step> {
        self.steps.get(self.i)
    }

    /// A worker STATUS interval rate (MH/s); counted while holding.
    pub fn sample_rate(&mut self, mhs: f64) {
        if matches!(self.phase, Phase::Holding { .. }) && mhs > 0.0 {
            self.rates.push(mhs);
        }
    }

    /// A telemetry draw reading (watts); counted while holding.
    pub fn sample_draw(&mut self, w: f64) {
        if matches!(self.phase, Phase::Holding { .. }) && w > 0.0 {
            self.draws.push(w);
        }
    }

    /// The phase in words for the tile.
    pub fn words(&self, now: Instant) -> String {
        let pct = self.current().map(|s| s.pct).unwrap_or(0);
        match &self.phase {
            Phase::Applying { .. } => format!("sweep: setting {pct}%"),
            Phase::Settling { since } => format!("sweep: {pct}% settling · {} s", self.timing.settle.as_secs().saturating_sub(now.duration_since(*since).as_secs())),
            Phase::Holding { since } => format!("sweep: holding {pct}% · {} s", self.timing.hold.as_secs().saturating_sub(now.duration_since(*since).as_secs())),
            Phase::Finishing { .. } => format!("sweep: setting the best cap ({}%)", self.chosen.as_ref().map(|r| r.pct).unwrap_or(0)),
            Phase::Done => "sweep: done".into(),
        }
    }

    /// Drives the state machine. `limit_w` is the limit the card reports now (the telemetry readback; 0 = unknown).
    pub fn tick(&mut self, now: Instant, limit_w: f64) -> Vec<Out> {
        let mut out = Vec::new();
        let settle = self.timing.settle;
        let hold = self.timing.hold;
        let apply = self.timing.apply;
        match self.phase.clone() {
            Phase::Applying { since, sent } => {
                let Some(step) = self.current().cloned() else {
                    self.phase = Phase::Done;
                    out.push(Out::Failed("no steps".into()));
                    return out;
                };
                if !sent {
                    self.phase = Phase::Applying { since: now, sent: true };
                    out.push(Out::Apply(step.watts));
                } else if limit_w > 0.0 && (limit_w - step.watts).abs() < 1.5 {
                    self.phase = Phase::Settling { since: now };
                } else if now.duration_since(since) > apply {
                    self.phase = Phase::Done;
                    out.push(Out::Failed(format!("the {}% cap ({:.0} W) did not take within {} s (card reports {:.0} W)", step.pct, step.watts, apply.as_secs(), limit_w)));
                }
            }
            Phase::Settling { since } => {
                if now.duration_since(since) >= settle {
                    self.draws.clear();
                    self.rates.clear();
                    self.phase = Phase::Holding { since: now };
                }
            }
            Phase::Holding { since } => {
                if now.duration_since(since) >= hold {
                    let step = self.current().cloned().expect("a step while holding");
                    let row = Row::from_samples(&step, &self.draws, &self.rates);
                    self.rows.push(row.clone());
                    out.push(Out::Row(row));
                    self.i += 1;
                    if self.i < self.steps.len() {
                        let next = self.steps[self.i].watts;
                        self.phase = Phase::Applying { since: now, sent: true };
                        out.push(Out::Apply(next));
                    } else {
                        match choose(&self.rows) {
                            Some(best) => {
                                self.chosen = Some(best.clone());
                                self.phase = Phase::Finishing { since: now, sent: true };
                                out.push(Out::Apply(best.limit));
                            }
                            None => {
                                self.phase = Phase::Done;
                                out.push(Out::Failed("no step had readings (no draw from nvidia-smi or no STATUS line from the worker)".into()));
                            }
                        }
                    }
                }
            }
            Phase::Finishing { since, sent } => {
                let best = self.chosen.clone().expect("a chosen row while finishing");
                if !sent {
                    self.phase = Phase::Finishing { since: now, sent: true };
                    out.push(Out::Apply(best.limit));
                } else if limit_w > 0.0 && (limit_w - best.limit).abs() < 1.5 {
                    self.phase = Phase::Done;
                    out.push(Out::Finished(best));
                } else if now.duration_since(since) > apply {
                    self.phase = Phase::Done;
                    out.push(Out::Failed(format!("the chosen cap ({:.0} W) did not take within {} s", best.limit, apply.as_secs())));
                }
            }
            Phase::Done => {}
        }
        out
    }
}

/// The unix helper for a tune (run through pkexec sh). The Windows script of the same protocol went with the engine's
/// legacy elevated branch (an earlier release): on Windows the Igneum Power Helper task is the only helper (src/powertask.rs).
pub fn helper_script_unix() -> &'static str {
    r#"#!/bin/sh
# igneum sweep helper: $1 dir, $2 nvidia-smi, $3 device, $4 restore watts
dir="$1"; smi="$2"; dev="$3"; restore="$4"
last=""; idle=$(date +%s)
echo "$(date -u +%FT%TZ) helper started: device $dev, restore $restore W" >> "$dir/helper.log"
while true; do
  c=""; [ -f "$dir/cmd.txt" ] && c=$(cat "$dir/cmd.txt" 2>/dev/null | tr -d '\r\n')
  if [ -n "$c" ] && [ "$c" != "$last" ]; then
    last="$c"; idle=$(date +%s)
    case "$c" in quit|[0-9]*" quit") echo "$(date -u +%FT%TZ) quit" >> "$dir/helper.log"; break;; esac
    printf '%s\n' "$c" | while IFS= read -r line; do
      set -- $line
      [ $# -ge 2 ] || continue
      op="$2"; v="${line##* }"
      [ $# -eq 2 ] && op=pl
      case "$op" in
        pl) case "$v" in ''|*[!0-9]*) ;; *) echo "$(date -u +%FT%TZ) $1 -pl $v : $("$smi" -i "$dev" -pl "$v" 2>&1)" >> "$dir/helper.log";; esac ;;
        lgc) case "$v" in ''|*[!0-9]*) ;; *) echo "$(date -u +%FT%TZ) $1 -lgc 0,$v : $("$smi" -i "$dev" -lgc "0,$v" 2>&1)" >> "$dir/helper.log";; esac ;;
        rgc) echo "$(date -u +%FT%TZ) $1 -rgc : $("$smi" -i "$dev" -rgc 2>&1)" >> "$dir/helper.log" ;;
        lmc) case "$v" in ''|*[!0-9]*) ;; *) echo "$(date -u +%FT%TZ) $1 -lmc $v,$v : $("$smi" -i "$dev" -lmc "$v,$v" 2>&1)" >> "$dir/helper.log";; esac ;;
        rmc) echo "$(date -u +%FT%TZ) $1 -rmc : $("$smi" -i "$dev" -rmc 2>&1)" >> "$dir/helper.log" ;;
      esac
    done
  fi
  if [ $(( $(date +%s) - idle )) -gt 1200 ]; then
    echo "$(date -u +%FT%TZ) idle 20 min: restored $restore W, clocks reset: $("$smi" -i "$dev" -pl "$restore" 2>&1) / $("$smi" -i "$dev" -rgc 2>&1)" >> "$dir/helper.log"; break
  fi
  sleep 0.5
done
exit 0
"#
}

/// Why a card cannot be swept, or None when it can (NVIDIA with a readable default limit and a device index).
pub fn unsupported_reason(vendor: &str, power_default_w: f64, device: &str) -> Option<&'static str> {
    match vendor {
        "nvidia" if power_default_w > 0.0 && !device.is_empty() => None,
        "nvidia" => Some("not available: nvidia-smi did not report this card's power limits"),
        // Ember Tune (src/ember.rs, 5 October 2026): AMD is tuned through igneum-gpu-telemetry, Apple measures only;
        // the tune itself says which at its start (the card row's note)
        "apple" | "amd" => None,
        // the first Intel card, the B580 on the first Windows machine (7 October 2026, docs/plans/intel-arc.md section 2.3): OpenCL
        // exposes neither a cap nor a reading; Intel's path is IGCL (ControlLib.dll), the telemetry helper's next piece
        "intel" => Some("not available: Intel Arc exposes no power cap or reading through OpenCL (the driver's IGCL counters come next)"),
        _ => Some("not available: no power reading or cap for this card"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// the first Windows machine's RTX 5090 (4 October 2026 app log): default 575 W, the 80% cap 460 W, min 400 W, max 600 W. The
    /// 60% and 50% steps clamp to the 400 W floor, so the plan ends at 70% plus one step at the floor.
    #[test]
    fn plan_clamps_to_the_card_floor_and_drops_duplicates() {
        let s = plan_steps(575.0, 400.0, 600.0);
        assert_eq!(s.iter().map(|s| s.pct).collect::<Vec<_>>(), vec![100, 90, 80, 70, 60]);
        assert_eq!(s.iter().map(|s| s.watts).collect::<Vec<_>>(), vec![575.0, 518.0, 460.0, 403.0, 400.0]);
        // a card with no floor reported: six steps
        assert_eq!(plan_steps(320.0, 0.0, 0.0).len(), 6);
        assert_eq!(plan_steps(320.0, 0.0, 0.0)[5], Step { pct: 50, watts: 160.0 });
        // the max caps the 100% step: a card whose max is under its default (rare, but nvidia-smi allows it)
        assert_eq!(plan_steps(300.0, 100.0, 250.0)[0], Step { pct: 100, watts: 250.0 });
        assert!(plan_steps(0.0, 0.0, 0.0).is_empty());
    }

    /// Recorded on the first Windows machine (RTX 5090, 4 October 2026): the telemetry query answered "0, 290.12, 65, [N/A], 460.00"
    /// (draw, GPU temperature, memory temperature not supported, limit). The draw query is the same shape.
    #[test]
    fn parses_nvidia_smi_power_draw() {
        let nounits = "0, 290.12\r\n";
        assert_eq!(parse_power_draw(nounits).get("0"), Some(&290.12));
        // two cards, one without a reading
        let two = "0, 290.12\n1, [N/A]\n";
        let m = parse_power_draw(two);
        assert_eq!(m.len(), 1);
        assert_eq!(m.get("0"), Some(&290.12));
        // with units and a header (the same query without noheader,nounits)
        let units = "index, power.draw [W]\n0, 290.12 W\n1, 45.50 W\n";
        let m = parse_power_draw(units);
        assert_eq!(m.get("0"), Some(&290.12));
        assert_eq!(m.get("1"), Some(&45.5));
        // garbage and a warning line
        assert!(parse_power_draw("NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver.\n").is_empty());
        assert!(parse_power_draw("0, [Not Supported]\n").is_empty());
        assert!(parse_power_draw("").is_empty());
    }

    fn row(pct: u32, watts: f64, mhs: f64) -> Row {
        Row { pct, limit: watts, watts, mhs, eff: if watts > 0.0 { mhs / watts } else { 0.0 }, draws: 12, rates: 2 }
    }

    #[test]
    fn chooses_the_best_mh_per_watt_then_rate_then_the_lower_cap() {
        // a card that gets more efficient as the cap drops, until it collapses
        let rows = vec![row(100, 560.0, 124.0), row(90, 510.0, 123.0), row(80, 455.0, 121.0), row(70, 400.0, 112.0), row(60, 345.0, 80.0)];
        assert_eq!(choose(&rows).unwrap().pct, 70); // 0.280 beats 0.266, 0.241, 0.221, 0.232
        // the first Windows machine's case: the draw never reaches the cap (290 W under every limit), so every step reads the same;
        // ties on efficiency and rate go to the lowest cap
        let flat = vec![row(100, 290.0, 124.0), row(90, 290.5, 123.5), row(80, 289.8, 124.2), row(70, 290.2, 123.9)];
        assert_eq!(choose(&flat).unwrap().pct, 70);
        // within 1% on efficiency, the higher rate wins
        let close = vec![row(100, 500.0, 125.0), row(80, 400.5, 100.3)];
        assert_eq!(choose(&close).unwrap().pct, 100);
        // a step without readings never wins; none usable = no choice
        let mut r = row(60, 300.0, 90.0);
        r.eff = 0.0;
        assert_eq!(choose(&[r.clone(), row(100, 500.0, 120.0)]).unwrap().pct, 100);
        assert!(choose(&[r]).is_none());
        assert!(choose(&[]).is_none());
    }

    #[test]
    fn row_lines_carry_the_table_fields() {
        let s = Step { pct: 80, watts: 460.0 };
        let r = Row::from_samples(&s, &[290.0, 291.0, 289.0, 290.0], &[124.0, 123.0]);
        assert!((r.eff - 123.5 / 290.0).abs() < 1e-9);
        assert_eq!(r.line("nvidia-ae432dc7-1"), "SWEEP card=nvidia-ae432dc7-1 cap=80 limit=460 watts=290.0 mhs=123.50 eff=0.4259");
        // under 3 draws or no rate: no efficiency, and the line says why
        let r = Row::from_samples(&s, &[290.0, 291.0], &[124.0]);
        assert!(!r.usable());
        assert!(r.line("c").contains("eff=0 reason=no_readings draws=2 rates=1"));
    }

    /// A full sweep with a fake clock: three steps, the readback follows each cap after 4 s, two draws and one rate
    /// per hold. The engine's view is the sequence of Out values.
    #[test]
    fn state_machine_runs_a_sweep() {
        let t0 = Instant::now();
        let timing = Timing { settle: Duration::from_secs(15), hold: Duration::from_secs(60), apply: Duration::from_secs(30) };
        let steps = plan_steps(300.0, 200.0, 300.0); // 300, 270, 240, 210, 200 (60% and 50% clamp to 200)
        assert_eq!(steps.len(), 5);
        let mut run = Run::new(0, "nvidia:0:x", "0", "nvidia-test-1", steps.clone(), 80, 240.0, false, timing, t0);
        let mut limit = 240.0;
        let mut t = t0;
        let mut applied: Vec<f64> = Vec::new();
        let mut pending: Option<(f64, Instant)> = None;
        let mut rows: Vec<Row> = Vec::new();
        let mut finished: Option<Row> = None;
        // the draw at each cap: the card draws min(cap, 260); once the cap bites, the rate falls with the square of
        // the cap (faster than the watts), so efficiency drops below the caps that do not bite
        let draw_at = |cap: f64| cap.min(260.0);
        let rate_at = |cap: f64| if cap >= 240.0 { 100.0 } else { 100.0 * (cap / 240.0) * (cap / 240.0) };
        for _ in 0..2000 {
            // the readback: the telemetry shows the new limit 4 s after the request
            if let Some((w, at)) = pending {
                if t.duration_since(at) >= Duration::from_secs(4) {
                    limit = w;
                    pending = None;
                }
            }
            for o in run.tick(t, limit) {
                match o {
                    Out::Apply(w) => {
                        applied.push(w);
                        pending = Some((w, t));
                    }
                    Out::Row(r) => rows.push(r),
                    Out::Finished(r) => finished = Some(r),
                    Out::Failed(e) => panic!("failed: {e}"),
                }
            }
            if matches!(run.phase, Phase::Holding { .. }) {
                run.sample_draw(draw_at(limit));
                run.sample_draw(draw_at(limit) + 1.0);
                run.sample_draw(draw_at(limit) - 1.0);
                run.sample_rate(rate_at(limit));
            }
            if run.phase == Phase::Done {
                break;
            }
            t += Duration::from_secs(1);
        }
        assert_eq!(rows.len(), 5, "one row per step");
        assert_eq!(rows.iter().map(|r| r.pct).collect::<Vec<_>>(), vec![100, 90, 80, 70, 60]);
        // the first three caps sit above the 260 W draw: the same draw, the same rate (0.385 MH/W), so the lowest
        // of them (80%) wins the tie; 70% and 60% bite and lose rate faster than watts (0.337 and 0.347 MH/W)
        let f = finished.expect("finished");
        assert_eq!(f.pct, 80);
        assert_eq!(applied, vec![300.0, 270.0, 240.0, 210.0, 200.0, 240.0], "every step's cap, then the chosen one");
        assert!(run.rows.iter().all(|r| r.draws >= 3 && r.rates >= 1));
        // timing: five steps of apply (4 s) + settle 15 + hold 60 = 79 s each, plus the final apply
        let elapsed = t.duration_since(t0).as_secs();
        assert!((395..=420).contains(&elapsed), "elapsed {elapsed} s");
    }

    #[test]
    fn state_machine_fails_when_a_cap_never_takes() {
        let t0 = Instant::now();
        let timing = Timing { settle: Duration::from_secs(1), hold: Duration::from_secs(1), apply: Duration::from_secs(30) };
        let mut run = Run::new(0, "k", "0", "c", plan_steps(300.0, 0.0, 0.0), 80, 240.0, false, timing, t0);
        assert_eq!(run.tick(t0, 240.0), vec![Out::Apply(300.0)]);
        assert!(run.tick(t0 + Duration::from_secs(29), 240.0).is_empty());
        match run.tick(t0 + Duration::from_secs(31), 240.0).as_slice() {
            [Out::Failed(e)] => assert!(e.contains("did not take"), "{e}"),
            other => panic!("{other:?}"),
        }
        assert_eq!(run.phase, Phase::Done);
    }

    #[test]
    fn state_machine_fails_without_readings() {
        let t0 = Instant::now();
        let timing = Timing { settle: Duration::from_secs(1), hold: Duration::from_secs(1), apply: Duration::from_secs(30) };
        let mut run = Run::new(0, "k", "0", "c", vec![Step { pct: 100, watts: 300.0 }], 80, 240.0, false, timing, t0);
        run.tick(t0, 0.0);
        run.tick(t0 + Duration::from_secs(1), 300.0); // settling
        run.tick(t0 + Duration::from_secs(3), 300.0); // holding
        let out = run.tick(t0 + Duration::from_secs(5), 300.0);
        assert!(matches!(out.as_slice(), [Out::Row(_), Out::Failed(_)]), "{out:?}");
    }

    #[test]
    fn unsupported_reasons() {
        assert!(unsupported_reason("nvidia", 575.0, "0").is_none());
        assert!(unsupported_reason("nvidia", 0.0, "0").unwrap().contains("power limits"));
        assert!(unsupported_reason("apple", 0.0, "").is_none(), "measure only, said by the tune");
        assert!(unsupported_reason("amd", 0.0, "1").is_none(), "tuned through igneum-gpu-telemetry");
        assert!(unsupported_reason("other", 0.0, "1").is_some());
    }

    #[test]
    fn helper_scripts_carry_the_protocol() {
        for s in [helper_script_unix()] {
            assert!(s.contains("cmd.txt") && s.contains("quit") && s.contains("-pl") && s.contains("20 min"));
            // V6-06: the engine writes "<seq> quit" (the Windows helper's guard); the unix helper takes that form too
            assert!(s.contains(r#"case "$c" in quit|[0-9]*" quit")"#), "{s}");
            assert!(s.contains("-lgc") && s.contains("-rgc"), "the clock cap and its reset");
            assert!(s.contains("-lmc") && s.contains("-rmc"), "Ember 2: the memory clock and its reset");
        }
    }
}

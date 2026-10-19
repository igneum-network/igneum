//! The tuner (design section 2.2): a coordinate search over the knobs the worker declared, judged by the mode,
//! stopped by instability, never touching anything the worker does not own.

use crate::modes::{judge, Limits, Measurement, Mode, Safety, Verdict};
use igneum_worker_proto::{Knob, Params};
use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, HashSet};
use std::time::Duration;

/// Something that can run a device at a parameter set and measure it. The live implementation drives a worker
/// through the supervisor; the tests use a simulated device.
pub trait Bench {
    fn measure(&mut self, params: &Params) -> Result<Measurement, String>;
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TuneConfig {
    pub budget: Duration,
    pub settle: Duration,
    pub window: Duration,
    /// The tune stops at this many unsafe candidates.
    pub max_unsafe: u32,
}

impl Default for TuneConfig {
    fn default() -> Self {
        TuneConfig { budget: Duration::from_secs(12 * 60), settle: Duration::from_secs(15), window: Duration::from_secs(45), max_unsafe: 2 }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Candidate {
    pub params: Params,
    pub measurement: Option<Measurement>,
    pub verdict: Verdict,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum StopReason {
    Converged,
    Budget,
    Unstable { reason: String },
    BenchFailed { reason: String },
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TuneResult {
    pub mode: Mode,
    /// The chosen point: the best Ok candidate, else the start point (nothing unsafe is ever chosen).
    pub chosen: Params,
    pub chosen_is_start: bool,
    pub candidates: Vec<Candidate>,
    pub stopped: StopReason,
    pub seconds_used: f64,
}

fn key(p: &Params) -> String {
    p.iter().map(|(k, v)| format!("{k}={v:.6}")).collect::<Vec<_>>().join(";")
}

/// The start point: hints snap onto the knobs' ladders; a pinned value is held as the user set it, clamped to
/// the knob's range only (the worker snaps it if it must, and reports what it applied).
pub fn start_point(knobs: &[Knob], hint: &Params, pinned: &Params) -> Params {
    let mut p = Params::new();
    for k in knobs {
        let v = match pinned.get(&k.name) {
            Some(v) if v.is_finite() => v.max(k.min).min(k.max),
            _ => k.clamp(hint.get(&k.name).copied().unwrap_or(k.default)),
        };
        p.insert(k.name.clone(), v);
    }
    p
}

/// The search. `elapsed` is supplied by the caller so the budget is wall time, not bench time.
pub fn search<B: Bench>(knobs: &[Knob], start: &Params, pinned: &Params, mode: Mode, limits: &Limits, safety: &Safety, cfg: &TuneConfig, bench: &mut B, elapsed: &mut dyn FnMut() -> Duration) -> TuneResult {
    let start = start_point(knobs, start, pinned);
    let mut candidates: Vec<Candidate> = Vec::new();
    let mut seen: HashSet<String> = HashSet::new();
    let mut best: Option<(f64, Params)> = None;
    let mut best_rate = 0.0f64;
    let mut unsafe_count = 0u32;
    let mut stopped: Option<StopReason> = None;

    // judge once more at the end with the final best_rate so Efficient's floor is relative to the whole tune
    let mut raw: Vec<(Params, Measurement)> = Vec::new();

    let eval = |p: &Params, bench: &mut B, candidates: &mut Vec<Candidate>, raw: &mut Vec<(Params, Measurement)>, best: &mut Option<(f64, Params)>, best_rate: &mut f64, unsafe_count: &mut u32| -> Result<Option<f64>, StopReason> {
        match bench.measure(p) {
            Err(e) => Err(StopReason::BenchFailed { reason: e }),
            Ok(m) => {
                let v = judge(mode, limits, safety, &m, best_rate.max(m.rate_hps));
                let score = match &v {
                    Verdict::Ok { score, .. } => Some(*score),
                    _ => None,
                };
                if matches!(v, Verdict::Unsafe { .. }) {
                    *unsafe_count += 1;
                }
                if !matches!(v, Verdict::Unsafe { .. }) && m.rate_hps > *best_rate {
                    *best_rate = m.rate_hps;
                }
                if let Some(s) = score {
                    if best.as_ref().map_or(true, |(b, _)| s > *b) {
                        *best = Some((s, p.clone()));
                    }
                }
                raw.push((p.clone(), m.clone()));
                candidates.push(Candidate { params: p.clone(), measurement: Some(m), verdict: v.clone() });
                if let Verdict::Unsafe { reason } = v {
                    if *unsafe_count >= cfg.max_unsafe {
                        return Err(StopReason::Unstable { reason });
                    }
                }
                Ok(score)
            }
        }
    };

    let mut current = start.clone();
    seen.insert(key(&current));
    match eval(&current, bench, &mut candidates, &mut raw, &mut best, &mut best_rate, &mut unsafe_count) {
        Ok(_) => {}
        Err(r) => stopped = Some(r),
    }

    'passes: while stopped.is_none() {
        let mut moved = false;
        for k in knobs {
            if pinned.contains_key(&k.name) {
                continue;
            }
            let here = *current.get(&k.name).unwrap_or(&k.default);
            let mut local_best: Option<(f64, Params)> = best.as_ref().filter(|(_, p)| *p == current).cloned();
            for up in [false, true] {
                if elapsed() >= cfg.budget {
                    stopped = Some(StopReason::Budget);
                    break 'passes;
                }
                let Some(nv) = k.neighbour(here, up) else { continue };
                let mut cand = current.clone();
                cand.insert(k.name.clone(), nv);
                let kk = key(&cand);
                if !seen.insert(kk) {
                    continue;
                }
                match eval(&cand, bench, &mut candidates, &mut raw, &mut best, &mut best_rate, &mut unsafe_count) {
                    Ok(Some(score)) => {
                        if local_best.as_ref().map_or(true, |(b, _)| score > *b) {
                            local_best = Some((score, cand));
                        }
                    }
                    Ok(None) => {}
                    Err(r) => {
                        stopped = Some(r);
                        break 'passes;
                    }
                }
            }
            if let Some((_, p)) = local_best {
                if p != current {
                    current = p;
                    moved = true;
                }
            }
        }
        if !moved {
            stopped = Some(StopReason::Converged);
        }
    }

    // re-judge every measured candidate against the final best rate (Efficient's floor) and pick the best Ok
    let mut final_best: Option<(f64, Params)> = None;
    let mut final_candidates = Vec::with_capacity(raw.len());
    for (p, m) in raw {
        let v = judge(mode, limits, safety, &m, best_rate);
        if let Verdict::Ok { score, .. } = &v {
            if final_best.as_ref().map_or(true, |(b, _)| *score > *b) {
                final_best = Some((*score, p.clone()));
            }
        }
        final_candidates.push(Candidate { params: p, measurement: Some(m), verdict: v });
    }
    let _ = candidates;
    let (chosen, chosen_is_start) = match final_best {
        Some((_, p)) => (p, false),
        None => (start.clone(), true),
    };
    TuneResult { mode, chosen, chosen_is_start, candidates: final_candidates, stopped: stopped.unwrap_or(StopReason::Converged), seconds_used: elapsed().as_secs_f64() }
}

// ---------------------------------------------------------------------------------------------------------------
// the tune store: what was chosen, keyed by device, worker, driver and algorithm; separate from user settings

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct StoredTune {
    pub chosen: Params,
    pub mode: Mode,
    pub when_unix: u64,
    pub worker_version: String,
    pub driver: String,
    pub algo: String,
    pub stopped: StopReason,
    pub candidates: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Default)]
pub struct TuneStore {
    #[serde(default = "tune_store_version")]
    pub version: u32,
    #[serde(default)]
    pub entries: BTreeMap<String, StoredTune>,
}

fn tune_store_version() -> u32 {
    1
}

impl TuneStore {
    pub const RETUNE_AFTER_SECS: u64 = 7 * 24 * 3600;

    pub fn key(fingerprint: &str, worker_version: &str, driver: &str, algo: &str) -> String {
        format!("{fingerprint}|{worker_version}|{driver}|{algo}")
    }

    pub fn get(&self, k: &str) -> Option<&StoredTune> {
        self.entries.get(k)
    }

    /// A stored tune is due again after 7 days (design section 2.2).
    pub fn is_fresh(&self, k: &str, now_unix: u64) -> bool {
        self.get(k).map_or(false, |t| now_unix.saturating_sub(t.when_unix) < Self::RETUNE_AFTER_SECS)
    }

    pub fn load(path: &std::path::Path) -> Self {
        std::fs::read_to_string(path).ok().and_then(|s| serde_json::from_str(&s).ok()).unwrap_or_default()
    }

    pub fn save(&self, path: &std::path::Path) -> std::io::Result<()> {
        crate::settings::write_atomic(path, &serde_json::to_vec_pretty(self).map_err(std::io::Error::other)?)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Instant;

    /// A simulated device: rate rises with intensity to a knee, power rises faster; above 48 the device
    /// produces invalid results; duty scales both.
    struct Sim {
        calls: u32,
    }
    impl Bench for Sim {
        fn measure(&mut self, p: &Params) -> Result<Measurement, String> {
            self.calls += 1;
            let i = *p.get("intensity").unwrap_or(&16.0);
            let duty = *p.get("duty").unwrap_or(&1.0);
            let rate = (1e6 * (1.0 - (-i / 20.0).exp())) * duty;
            let power = (40.0 + 3.0 * i + 0.02 * i * i) * duty;
            let temp = 40.0 + power / 6.0;
            Ok(Measurement { rate_hps: rate, power_w: Some(power), temp_max_c: Some(temp), invalid: if i > 48.0 { 1 } else { 0 }, seconds: 45.0, ..Default::default() })
        }
    }

    fn knobs() -> Vec<Knob> {
        vec![
            Knob::additive("intensity", 1.0, 64.0, 4.0, 16.0, ""),
            Knob::additive("duty", 0.5, 1.0, 0.25, 1.0, ""),
        ]
    }

    #[test]
    fn max_climbs_towards_the_knee_and_never_picks_unsafe() {
        let mut sim = Sim { calls: 0 };
        let t0 = Instant::now();
        let r = search(&knobs(), &Params::new(), &Params::new(), Mode::Max, &Limits::default(), &Safety::default(), &TuneConfig::default(), &mut sim, &mut || t0.elapsed());
        let i = r.chosen["intensity"];
        assert!(i > 16.0, "{i}");
        assert!(i <= 48.0, "{i}");
        assert!(!r.chosen_is_start);
        assert!(sim.calls > 3);
    }

    #[test]
    fn efficient_prefers_lower_intensity_above_the_floor() {
        let mut sim = Sim { calls: 0 };
        let t0 = Instant::now();
        let r = search(&knobs(), &Params::new(), &Params::new(), Mode::Efficient, &Limits::default(), &Safety::default(), &TuneConfig::default(), &mut sim, &mut || t0.elapsed());
        let i = r.chosen["intensity"];
        assert!(i < 24.0, "{i}");
        // every Ok candidate sits above the floor relative to the best safe rate seen
        let best = r.candidates.iter().filter(|c| !matches!(c.verdict, Verdict::Unsafe { .. })).map(|c| c.measurement.as_ref().unwrap().rate_hps).fold(0.0f64, f64::max);
        for c in &r.candidates {
            if let Verdict::Ok { .. } = c.verdict {
                assert!(c.measurement.as_ref().unwrap().rate_hps >= 0.6 * best - 1e-6);
            }
        }
    }

    #[test]
    fn a_pinned_knob_is_held() {
        let mut sim = Sim { calls: 0 };
        let mut pins = Params::new();
        pins.insert("intensity".into(), 20.0);
        let t0 = Instant::now();
        let r = search(&knobs(), &Params::new(), &pins, Mode::Max, &Limits::default(), &Safety::default(), &TuneConfig::default(), &mut sim, &mut || t0.elapsed());
        assert_eq!(r.chosen["intensity"], 20.0);
    }

    #[test]
    fn an_unstable_device_stops_the_tune_and_falls_back_to_safe() {
        struct Flaky;
        impl Bench for Flaky {
            fn measure(&mut self, p: &Params) -> Result<Measurement, String> {
                let i = *p.get("intensity").unwrap_or(&16.0);
                Ok(Measurement { rate_hps: 1e6 * i, power_w: Some(100.0), temp_max_c: Some(50.0), invalid: if i > 16.0 { 1 } else { 0 }, seconds: 45.0, ..Default::default() })
            }
        }
        let t0 = Instant::now();
        let k = vec![Knob::additive("intensity", 1.0, 64.0, 4.0, 16.0, "")];
        let r = search(&k, &Params::new(), &Params::new(), Mode::Max, &Limits::default(), &Safety::default(), &TuneConfig::default(), &mut Flaky, &mut || t0.elapsed());
        assert!(matches!(r.stopped, StopReason::Unstable { .. }));
        assert!(r.chosen["intensity"] <= 16.0);
    }

    #[test]
    fn budget_stops_the_search() {
        let mut sim = Sim { calls: 0 };
        let mut n = 0u64;
        let cfg = TuneConfig { budget: Duration::from_secs(100), ..Default::default() };
        let r = search(&knobs(), &Params::new(), &Params::new(), Mode::Max, &Limits::default(), &Safety::default(), &cfg, &mut sim, &mut || {
            n += 60;
            Duration::from_secs(n)
        });
        assert!(matches!(r.stopped, StopReason::Budget));
    }

    #[test]
    fn store_keys_and_freshness() {
        let mut s = TuneStore::default();
        let k = TuneStore::key("fp", "0.1.0", "550.1", "stub-v0");
        s.entries.insert(k.clone(), StoredTune { chosen: Params::new(), mode: Mode::Max, when_unix: 1_000_000, worker_version: "0.1.0".into(), driver: "550.1".into(), algo: "stub-v0".into(), stopped: StopReason::Converged, candidates: 3 });
        assert!(s.is_fresh(&k, 1_000_000 + 3600));
        assert!(!s.is_fresh(&k, 1_000_000 + TuneStore::RETUNE_AFTER_SECS + 1));
        assert!(!s.is_fresh("other", 0));
    }
}

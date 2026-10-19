//! The three user modes (plan page 11) as the tuner judges a measured candidate.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "lowercase")]
pub enum Mode {
    /// Useful work within chosen power, temperature and noise limits.
    Quiet,
    /// Best validated energy efficiency within the user's minimum-performance requirement.
    #[default]
    Efficient,
    /// Highest sustained validated throughput within safe limits.
    Max,
}

impl Mode {
    pub fn label(self) -> &'static str {
        match self {
            Mode::Quiet => "Quiet",
            Mode::Efficient => "Efficient",
            Mode::Max => "Maximum throughput",
        }
    }
    pub fn meaning(self) -> &'static str {
        match self {
            Mode::Quiet => "as much validated work as fits under your power, temperature and fan caps",
            Mode::Efficient => "the most validated work per watt of device power, keeping at least your minimum rate",
            Mode::Max => "the highest validated rate this device sustains inside the safety limits",
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Limits {
    /// Quiet: device power cap, W.
    pub power_cap_w: Option<f64>,
    /// Quiet: temperature cap, C.
    pub temp_cap_c: Option<f64>,
    /// Quiet: fan cap, percent, where the driver reports fan speed.
    pub fan_cap_pct: Option<f64>,
    /// Efficient: the floor as a fraction of the best rate seen in this tune (PA-03 default 0.6).
    pub min_fraction: f64,
    /// Efficient: an absolute floor, hashes per second, when the user prefers one.
    pub min_rate_hps: Option<f64>,
}

impl Default for Limits {
    fn default() -> Self {
        Limits { power_cap_w: None, temp_cap_c: None, fan_cap_pct: None, min_fraction: 0.6, min_rate_hps: None }
    }
}

impl Limits {
    pub fn validate(&self) -> Result<(), String> {
        for (name, v, lo, hi) in [
            ("power cap", self.power_cap_w, 5.0, 5000.0),
            ("temperature cap", self.temp_cap_c, 30.0, 110.0),
            ("fan cap", self.fan_cap_pct, 0.0, 100.0),
            ("minimum rate", self.min_rate_hps, 0.0, 1e15),
        ] {
            if let Some(v) = v {
                if !v.is_finite() || v < lo || v > hi {
                    return Err(format!("{name} must be between {lo} and {hi}"));
                }
            }
        }
        if !(0.0..=1.0).contains(&self.min_fraction) {
            return Err("the minimum fraction must be between 0 and 1".into());
        }
        Ok(())
    }
}

/// The safety set, always on, from what the device reports.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Safety {
    /// The driver's slowdown temperature; 85 C when it reports none.
    pub slowdown_c: f64,
    /// Margin under the slowdown temperature.
    pub margin_c: f64,
    /// The share of a window the device may spend throttled for thermal or power reasons.
    pub max_throttle_frac: f64,
}

impl Default for Safety {
    fn default() -> Self {
        Safety { slowdown_c: 85.0, margin_c: 5.0, max_throttle_frac: 0.10 }
    }
}

impl Safety {
    pub fn for_device(slowdown_c: Option<f64>) -> Self {
        Safety { slowdown_c: slowdown_c.unwrap_or(85.0), ..Default::default() }
    }
}

/// What one candidate measured over its window.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Default)]
pub struct Measurement {
    /// Validated work rate (results verified on the CPU times the tuning difficulty, per second).
    pub rate_hps: f64,
    /// Mean device power over the window, when reported (device telemetry: diagnostic).
    pub power_w: Option<f64>,
    pub temp_max_c: Option<f64>,
    pub fan_max_pct: Option<f64>,
    pub throttle_frac: f64,
    /// Worst Apple thermal pressure seen: nominal, fair, serious, critical.
    pub thermal_pressure_worst: Option<String>,
    pub invalid: u32,
    pub hangs: u32,
    pub seconds: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Verdict {
    Ok { score: f64, note: String },
    Rejected { reason: String },
    Unsafe { reason: String },
}

/// Judge a candidate. `best_rate` is the best validated rate seen so far in this tune (for Efficient's floor).
pub fn judge(mode: Mode, limits: &Limits, safety: &Safety, m: &Measurement, best_rate: f64) -> Verdict {
    // the safety set first: instability is never selected
    if m.invalid > 0 {
        return Verdict::Unsafe { reason: format!("{} invalid result(s) in the window", m.invalid) };
    }
    if m.hangs > 0 {
        return Verdict::Unsafe { reason: format!("{} hang(s) in the window", m.hangs) };
    }
    let ceiling = safety.slowdown_c - safety.margin_c;
    if let Some(t) = m.temp_max_c {
        if t > ceiling {
            return Verdict::Rejected { reason: format!("{t:.0} C is over the {ceiling:.0} C ceiling") };
        }
    }
    if m.throttle_frac > safety.max_throttle_frac {
        return Verdict::Rejected { reason: format!("throttled {:.0}% of the window", m.throttle_frac * 100.0) };
    }
    if let Some(tp) = &m.thermal_pressure_worst {
        if tp == "serious" || tp == "critical" {
            return Verdict::Rejected { reason: format!("thermal pressure reached {tp}") };
        }
    }
    if !(m.rate_hps > 0.0) {
        return Verdict::Rejected { reason: "no validated work in the window".into() };
    }
    match mode {
        Mode::Quiet => {
            if let (Some(cap), Some(p)) = (limits.power_cap_w, m.power_w) {
                if p > cap {
                    return Verdict::Rejected { reason: format!("{p:.0} W is over your {cap:.0} W cap") };
                }
            }
            if let (Some(cap), Some(t)) = (limits.temp_cap_c, m.temp_max_c) {
                if t > cap {
                    return Verdict::Rejected { reason: format!("{t:.0} C is over your {cap:.0} C cap") };
                }
            }
            if let (Some(cap), Some(f)) = (limits.fan_cap_pct, m.fan_max_pct) {
                if f > cap {
                    return Verdict::Rejected { reason: format!("fan at {f:.0}% is over your {cap:.0}% cap") };
                }
            }
            Verdict::Ok { score: m.rate_hps, note: String::new() }
        }
        Mode::Efficient => {
            let floor = match limits.min_rate_hps {
                Some(abs) => abs,
                None => limits.min_fraction * best_rate,
            };
            if m.rate_hps < floor {
                return Verdict::Rejected { reason: format!("{:.3} MH/s is under your floor of {:.3} MH/s", m.rate_hps / 1e6, floor / 1e6) };
            }
            match m.power_w {
                Some(p) if p > 0.0 => Verdict::Ok { score: m.rate_hps / p, note: String::new() },
                _ => Verdict::Ok { score: m.rate_hps, note: "this device reports no power, so Efficient scored by rate alone".into() },
            }
        }
        Mode::Max => Verdict::Ok { score: m.rate_hps, note: String::new() },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn m(rate: f64, power: f64, temp: f64) -> Measurement {
        Measurement { rate_hps: rate, power_w: Some(power), temp_max_c: Some(temp), seconds: 45.0, ..Default::default() }
    }

    #[test]
    fn unsafe_beats_everything() {
        let mut x = m(1e9, 100.0, 50.0);
        x.invalid = 1;
        assert!(matches!(judge(Mode::Max, &Limits::default(), &Safety::default(), &x, 1e9), Verdict::Unsafe { .. }));
    }

    #[test]
    fn efficient_scores_per_watt_with_a_floor() {
        let l = Limits::default();
        let s = Safety::default();
        match judge(Mode::Efficient, &l, &s, &m(1e6, 100.0, 60.0), 1e6) {
            Verdict::Ok { score, .. } => assert!((score - 1e4).abs() < 1e-6),
            v => panic!("{v:?}"),
        }
        assert!(matches!(judge(Mode::Efficient, &l, &s, &m(0.5e6, 50.0, 60.0), 1e6), Verdict::Rejected { .. }));
    }

    #[test]
    fn quiet_respects_caps_and_safety_ceiling() {
        let l = Limits { power_cap_w: Some(120.0), temp_cap_c: Some(70.0), ..Default::default() };
        let s = Safety::default();
        assert!(matches!(judge(Mode::Quiet, &l, &s, &m(1e6, 130.0, 60.0), 1e6), Verdict::Rejected { .. }));
        assert!(matches!(judge(Mode::Quiet, &l, &s, &m(1e6, 100.0, 75.0), 1e6), Verdict::Rejected { .. }));
        assert!(matches!(judge(Mode::Quiet, &l, &s, &m(1e6, 100.0, 60.0), 1e6), Verdict::Ok { .. }));
        assert!(matches!(judge(Mode::Max, &Limits::default(), &s, &m(1e6, 300.0, 82.0), 1e6), Verdict::Rejected { .. }));
    }
}

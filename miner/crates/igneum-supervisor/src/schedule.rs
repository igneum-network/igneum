//! Gaming-aware pause and time-of-use scheduling (design section 2.5): optional, local, reversible, and never
//! starting mining by themselves without the permission the user gave on the screen that explains it.

use crate::economics::Tariff;
use serde::{Deserialize, Serialize};
use std::time::Duration;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TimeOfUse {
    pub enabled: bool,
    /// Pause while the tariff is above this rate per kWh.
    pub pause_above_rate: f64,
    /// Resume by itself when the rate falls (its own switch; off by default, PA-13).
    pub resume_by_itself: bool,
}

impl Default for TimeOfUse {
    fn default() -> Self {
        TimeOfUse { enabled: false, pause_above_rate: 0.0, resume_by_itself: false }
    }
}

impl TimeOfUse {
    pub fn validate(&self) -> Result<(), String> {
        if !self.pause_above_rate.is_finite() || self.pause_above_rate < 0.0 || self.pause_above_rate > 100.0 {
            return Err("the pause rate must be between 0 and 100 per kWh".into());
        }
        Ok(())
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GamingPause {
    pub enabled: bool,
    /// Resume by itself after the full-screen application has gone (its own switch; off by default).
    pub resume_by_itself: bool,
    /// How long the full-screen application must be gone before an automatic resume.
    pub resume_after_s: u64,
}

impl Default for GamingPause {
    fn default() -> Self {
        GamingPause { enabled: false, resume_by_itself: false, resume_after_s: 120 }
    }
}

/// Whether another application holds the display full screen. Platform probes implement this; where none is
/// available the feature shows "unavailable on this system" and stays off.
pub trait FullscreenProbe: Send {
    fn available(&self) -> bool;
    /// `Some(true)` when a full-screen application other than the miner is in front.
    fn other_app_fullscreen(&mut self) -> Option<bool>;
}

pub struct NoProbe;
impl FullscreenProbe for NoProbe {
    fn available(&self) -> bool {
        false
    }
    fn other_app_fullscreen(&mut self) -> Option<bool> {
        None
    }
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum PauseCause {
    Rate,
    Game,
    Battery,
    Sleep,
    User,
    /// Heat mode (the old miner's Ember Heat, carried by the engine on the old base): the room is at the set point,
    /// the cards rest until the engine's own reading releases them; the schedule never lifts it.
    Heat,
    /// The OS memory-pressure guard (the 8 GB class): paused at the warning level, resumed at normal by the
    /// guard itself, never by the schedule.
    Memory,
    /// The work source is unreachable (the project's read of the three-card test machine, 17:2x UK, 12 October 2026: the worker relaunched every
    /// thirty seconds while the pool was down, a toast on the screen each time): the cards idle, the pool alone is
    /// retried on a backoff, and the supervisor lifts it when the pool answers; never the schedule.
    Source,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Decision {
    /// Keep going.
    Run,
    /// Pause now for this cause.
    Pause(PauseCause),
    /// Stay paused; the user must tap to resume (no permission for automatic resume).
    StayPaused(PauseCause),
    /// The cause has gone and automatic resume is permitted.
    Resume,
}

/// The scheduler's inputs at one moment.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Moment {
    pub minute_of_day: u32,
    pub other_app_fullscreen: Option<bool>,
    pub on_battery: Option<bool>,
    /// How long the game has been gone, if a game pause is in force.
    pub game_gone_for: Option<Duration>,
    /// What the miner is paused for right now, if anything.
    pub paused_for: Option<PauseCause>,
}

/// One decision from one moment. A user pause is never resumed by the scheduler.
pub fn decide(tariff: &Tariff, tou: &TimeOfUse, gp: &GamingPause, pause_on_battery: bool, m: &Moment) -> Decision {
    match m.paused_for {
        Some(PauseCause::User) => return Decision::StayPaused(PauseCause::User),
        Some(PauseCause::Sleep) => return Decision::StayPaused(PauseCause::Sleep),
        Some(PauseCause::Memory) => return Decision::StayPaused(PauseCause::Memory),
        Some(PauseCause::Heat) => return Decision::StayPaused(PauseCause::Heat),
        Some(PauseCause::Source) => return Decision::StayPaused(PauseCause::Source),
        _ => {}
    }
    let rate_high = tou.enabled && tariff.rate_at(m.minute_of_day) > tou.pause_above_rate;
    let game = gp.enabled && m.other_app_fullscreen == Some(true);
    let battery = pause_on_battery && m.on_battery == Some(true);

    match m.paused_for {
        None => {
            if battery {
                Decision::Pause(PauseCause::Battery)
            } else if game {
                Decision::Pause(PauseCause::Game)
            } else if rate_high {
                Decision::Pause(PauseCause::Rate)
            } else {
                Decision::Run
            }
        }
        Some(PauseCause::Battery) => {
            if battery {
                Decision::StayPaused(PauseCause::Battery)
            } else {
                // leaving battery is an OS condition the user set up once ("pause on battery"): resume is allowed
                Decision::Resume
            }
        }
        Some(PauseCause::Game) => {
            if game {
                Decision::StayPaused(PauseCause::Game)
            } else if gp.resume_by_itself && m.game_gone_for.map_or(false, |d| d >= Duration::from_secs(gp.resume_after_s)) {
                Decision::Resume
            } else {
                Decision::StayPaused(PauseCause::Game)
            }
        }
        Some(PauseCause::Rate) => {
            if rate_high {
                Decision::StayPaused(PauseCause::Rate)
            } else if tou.resume_by_itself {
                Decision::Resume
            } else {
                Decision::StayPaused(PauseCause::Rate)
            }
        }
        Some(PauseCause::User) | Some(PauseCause::Sleep) | Some(PauseCause::Memory) | Some(PauseCause::Heat) | Some(PauseCause::Source) => unreachable!(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::economics::Window;

    fn tariff() -> Tariff {
        Tariff::Schedule { windows: vec![Window { start_min: 0, end_min: 420, rate: 0.10 }, Window { start_min: 420, end_min: 1440, rate: 0.30 }] }
    }

    fn moment(minute: u32) -> Moment {
        Moment { minute_of_day: minute, other_app_fullscreen: None, on_battery: None, game_gone_for: None, paused_for: None }
    }

    #[test]
    fn off_by_default_means_run() {
        let d = decide(&tariff(), &TimeOfUse::default(), &GamingPause::default(), true, &moment(600));
        assert_eq!(d, Decision::Run);
    }

    #[test]
    fn rate_pause_needs_permission_to_resume() {
        let tou = TimeOfUse { enabled: true, pause_above_rate: 0.20, resume_by_itself: false };
        assert_eq!(decide(&tariff(), &tou, &GamingPause::default(), true, &moment(600)), Decision::Pause(PauseCause::Rate));
        let mut m = moment(100);
        m.paused_for = Some(PauseCause::Rate);
        assert_eq!(decide(&tariff(), &tou, &GamingPause::default(), true, &m), Decision::StayPaused(PauseCause::Rate));
        let tou2 = TimeOfUse { resume_by_itself: true, ..tou };
        assert_eq!(decide(&tariff(), &tou2, &GamingPause::default(), true, &m), Decision::Resume);
    }

    #[test]
    fn game_pause_waits_for_the_quiet_period() {
        let gp = GamingPause { enabled: true, resume_by_itself: true, resume_after_s: 120 };
        let mut m = moment(100);
        m.other_app_fullscreen = Some(true);
        assert_eq!(decide(&tariff(), &TimeOfUse::default(), &gp, true, &m), Decision::Pause(PauseCause::Game));
        m.paused_for = Some(PauseCause::Game);
        m.other_app_fullscreen = Some(false);
        m.game_gone_for = Some(Duration::from_secs(30));
        assert_eq!(decide(&tariff(), &TimeOfUse::default(), &gp, true, &m), Decision::StayPaused(PauseCause::Game));
        m.game_gone_for = Some(Duration::from_secs(121));
        assert_eq!(decide(&tariff(), &TimeOfUse::default(), &gp, true, &m), Decision::Resume);
    }

    #[test]
    fn a_user_pause_is_never_resumed_here() {
        let mut m = moment(100);
        m.paused_for = Some(PauseCause::User);
        assert_eq!(decide(&tariff(), &TimeOfUse::default(), &GamingPause::default(), true, &m), Decision::StayPaused(PauseCause::User));
    }

    #[test]
    fn battery() {
        let mut m = moment(100);
        m.on_battery = Some(true);
        assert_eq!(decide(&tariff(), &TimeOfUse::default(), &GamingPause::default(), true, &m), Decision::Pause(PauseCause::Battery));
        assert_eq!(decide(&tariff(), &TimeOfUse::default(), &GamingPause::default(), false, &m), Decision::Run);
    }
}

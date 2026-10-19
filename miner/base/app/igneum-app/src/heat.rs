//! Ember Heat (mission item 7, 7 October 2026, docs/plans/ember-heat.md): the card is a heater that also earns. Heat mode
//! holds a room temperature, or a schedule of them, and the hash follows the duty cycle: the cards mine for a share of
//! every ten-minute period and rest for the rest of it. The chain sees a miner with a schedule, nothing else.
//!
//! The temperature source is what the machine has: a reading the miner types from their own thermometer (fresh for
//! two hours), else the card's own sensor once the card has rested long enough to cool to the room plus an idle
//! offset (default 8 degrees, approximate; learned from a typed reading taken while the card rests). No hardware
//! the app does not have. Without any reading the loop heats 70 percent of every period and says so.
//!
//! The loop is proportional plus integral, decided once per period on the reading at the period's start: the
//! integral carries the steady-state share of heat the room needs, the proportional term pulls it back when the
//! room drifts. The engine (src/engine.rs, `tick_heat`) owns the processes: it stops the miners for a rest and
//! re-arms them for the heating slice; nothing here touches a card. The log carries one `HEAT` line every 30 s and
//! tools/heat-gate.mjs reads it for the the first Windows machine gate (held within 1 degree for 4 hours, hash following the duty).

/// One period: the heating slice first, the rest after it.
pub const PERIOD_S: f64 = 600.0;
/// Duty per degree under the set point (2 degrees under = a full period of heat).
pub const KP: f64 = 0.5;
/// Duty per degree per period added to the integral (the steady-state share).
pub const KI: f64 = 0.05;
/// The card sensor stands for the room only after this long at rest (the die cools toward the room plus the idle
/// offset; what is left of the cooling tail is taken off by its slope, COOL_TAU_S).
pub const SETTLE_S: f64 = 180.0;
/// The cooling tail of a stopped card as one time constant, seconds, approximate: the estimate adds tau times the
/// (negative) slope of the sensor, which is exact for a single exponential and partial otherwise.
pub const COOL_TAU_S: f64 = 60.0;
/// A typed room reading is the room for this long.
pub const TYPED_FRESH_S: f64 = 7200.0;
/// An idle card's sensor above the room, degrees, approximate, until a typed reading teaches the real offset.
pub const OFFSET_DEFAULT_C: f64 = 8.0;
/// The stated error of the card-sensor estimate, degrees.
pub const OFFSET_ERROR_C: f64 = 3.0;
/// With the card sensor as the only source, every period keeps a rest long enough to read the room.
pub const CARD_DUTY_MAX: f64 = 1.0 - SETTLE_S / PERIOD_S;
/// With no reading at all: heat this share of every period (the rest long enough for the card to read the room) and say why.
pub const FIND_DUTY: f64 = CARD_DUTY_MAX;
/// The set point the app accepts, degrees.
pub const SET_MIN_C: f64 = 5.0;
pub const SET_MAX_C: f64 = 30.0;
/// The log line and the gate tool's sample spacing.
pub const LOG_EVERY_S: f64 = 30.0;

/// One entry of a schedule: from this minute of the day the set point is `set_c`, until the next entry.
#[derive(Clone, Debug, PartialEq, serde::Serialize, serde::Deserialize)]
pub struct Slot {
    pub minute: u32,
    pub set_c: f64,
}

/// "06:00 20, 22:00 16" to slots, sorted by minute. Empty text = no schedule (the one set point all day).
pub fn parse_schedule(text: &str) -> Result<Vec<Slot>, String> {
    let mut out = Vec::new();
    for part in text.split(|c| c == ',' || c == ';' || c == '\n') {
        let part = part.trim();
        if part.is_empty() {
            continue;
        }
        let mut it = part.split_whitespace();
        let (Some(time), Some(temp)) = (it.next(), it.next()) else {
            return Err(format!("\"{part}\": write a time and a temperature, for example 06:00 20"));
        };
        let (h, m) = time.split_once(':').ok_or_else(|| format!("\"{time}\": a time is HH:MM"))?;
        let h: u32 = h.parse().map_err(|_| format!("\"{time}\": a time is HH:MM"))?;
        let m: u32 = m.parse().map_err(|_| format!("\"{time}\": a time is HH:MM"))?;
        if h > 23 || m > 59 {
            return Err(format!("\"{time}\": a time is HH:MM, 00:00 to 23:59"));
        }
        let set_c: f64 = temp.trim_end_matches("°C").trim_end_matches('C').parse().map_err(|_| format!("\"{temp}\": a temperature is a number of degrees"))?;
        if !(SET_MIN_C..=SET_MAX_C).contains(&set_c) {
            return Err(format!("{set_c} degrees: the set point is {SET_MIN_C:.0} to {SET_MAX_C:.0}"));
        }
        out.push(Slot { minute: h * 60 + m, set_c });
    }
    out.sort_by_key(|s| s.minute);
    out.dedup_by_key(|s| s.minute);
    Ok(out)
}

/// Slots back to the text the settings page shows.
pub fn schedule_text(slots: &[Slot]) -> String {
    slots.iter().map(|s| format!("{:02}:{:02} {}", s.minute / 60, s.minute % 60, trim_c(s.set_c))).collect::<Vec<_>>().join(", ")
}

fn trim_c(c: f64) -> String {
    if (c - c.round()).abs() < 0.05 { format!("{:.0}", c) } else { format!("{:.1}", c) }
}

/// The set point in force at a minute of the day: the latest slot at or before it; before the first slot, the last
/// slot of the day (the schedule wraps at midnight). No slots: the default.
pub fn set_point_at(default_c: f64, slots: &[Slot], minute_of_day: u32) -> f64 {
    if slots.is_empty() {
        return default_c;
    }
    slots.iter().rev().find(|s| s.minute <= minute_of_day).or_else(|| slots.last()).map(|s| s.set_c).unwrap_or(default_c)
}

/// The minute of the day in the schedule's own clock: unix seconds plus the window's offset east of UTC in minutes.
pub fn minute_of_day(unix: f64, tz_east_min: i32) -> u32 {
    let local = unix as i64 + tz_east_min as i64 * 60;
    ((local.rem_euclid(86_400)) / 60) as u32
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Source {
    Typed,
    Card,
    None,
}

impl Source {
    pub fn name(&self) -> &'static str {
        match self {
            Source::Typed => "typed",
            Source::Card => "card",
            Source::None => "none",
        }
    }
}

/// What the loop knows about the room right now.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Reading {
    pub room_c: f64,
    pub source: Source,
    /// the stated error of the estimate, degrees (0 for a typed reading: the thermometer is the miner's)
    pub error_c: f64,
    /// how old the reading is, seconds
    pub age_s: f64,
}

impl Reading {
    pub fn none() -> Reading {
        Reading { room_c: 0.0, source: Source::None, error_c: 0.0, age_s: 0.0 }
    }
}

/// The room estimate: a typed reading while it is fresh, else the coolest card's sensor minus the idle offset once
/// the cards have rested SETTLE_S (the cooling tail still in the sensor taken off by its slope), else nothing.
/// `typed` = (degrees, unix s typed); `card_c` = 0 when no card reports; `card_slope` = degrees per second over the
/// last half minute (negative while cooling; a rising sensor is not corrected).
pub fn room(now: f64, typed: Option<(f64, f64)>, card_c: f64, card_slope: f64, rest_for_s: f64, offset_c: f64) -> Reading {
    if let Some((c, at)) = typed {
        if c > -50.0 && at > 0.0 && now - at < TYPED_FRESH_S {
            return Reading { room_c: c, source: Source::Typed, error_c: 0.0, age_s: (now - at).max(0.0) };
        }
    }
    if card_c > 0.0 && rest_for_s >= SETTLE_S {
        let off = if offset_c > 0.0 { offset_c } else { OFFSET_DEFAULT_C };
        let tail = COOL_TAU_S * card_slope.min(0.0);
        return Reading { room_c: card_c + tail - off, source: Source::Card, error_c: OFFSET_ERROR_C, age_s: 0.0 };
    }
    Reading::none()
}

/// The slope of sensor samples (unix s, degrees) at the END of the window, degrees per second: the least-squares
/// line (its slope belongs to the window's middle) brought forward by the exponential tail's own decay over half
/// the window. 0 with fewer than two samples.
pub fn slope_of<I: Iterator<Item = (f64, f64)>>(samples: I) -> f64 {
    let v: Vec<(f64, f64)> = samples.collect();
    if v.len() < 2 {
        return 0.0;
    }
    let n = v.len() as f64;
    let (mt, mc) = (v.iter().map(|s| s.0).sum::<f64>() / n, v.iter().map(|s| s.1).sum::<f64>() / n);
    let (mut num, mut den) = (0.0, 0.0);
    for (t, c) in &v {
        num += (t - mt) * (c - mc);
        den += (t - mt) * (t - mt);
    }
    if den <= 0.0 {
        return 0.0;
    }
    let span = v.iter().map(|s| s.0).fold(f64::MIN, f64::max) - v.iter().map(|s| s.0).fold(f64::MAX, f64::min);
    num / den * (-(span / 2.0) / COOL_TAU_S).exp()
}

/// A typed reading taken while the cards have rested teaches the idle offset (card minus room, 0 to 20 degrees).
pub fn learned_offset(card_c: f64, rest_for_s: f64, typed_c: f64) -> Option<f64> {
    if card_c <= 0.0 || rest_for_s < SETTLE_S {
        return None;
    }
    Some((card_c - typed_c).clamp(0.0, 20.0))
}

/// The loop's memory between ticks.
#[derive(Clone, Debug, PartialEq)]
pub struct Controller {
    /// the steady-state share of heat the room needs, 0 to 1 (starts at a half)
    pub integral: f64,
    pub period_start: f64,
    /// this period's duty, 0 to 1
    pub duty: f64,
    /// this period's heating slice, seconds
    pub on_s: f64,
    started: bool,
}

/// What the engine does this tick.
#[derive(Clone, Debug, PartialEq)]
pub struct Decision {
    pub heating: bool,
    pub duty: f64,
    pub set_c: f64,
    pub reading: Reading,
    /// seconds until the slice changes (heating to rest, or the next period)
    pub until_s: f64,
    /// a new period began on this tick
    pub new_period: bool,
}

impl Default for Controller {
    fn default() -> Controller {
        Controller::new()
    }
}

impl Controller {
    pub fn new() -> Controller {
        Controller { integral: 0.5, period_start: 0.0, duty: 0.0, on_s: 0.0, started: false }
    }

    /// The duty a reading asks for, and the integral it leaves: the proportional term on the error, the integral on
    /// the steady-state share; with no reading the find share and an untouched integral.
    pub fn duty_for(&mut self, set_c: f64, reading: &Reading) -> f64 {
        match reading.source {
            Source::None => FIND_DUTY,
            src => {
                let err = set_c - reading.room_c;
                self.integral = (self.integral + KI * err).clamp(0.0, 1.0);
                let d = (KP * err + self.integral).clamp(0.0, 1.0);
                if src == Source::Card { d.min(CARD_DUTY_MAX) } else { d }
            }
        }
    }

    /// One tick. A new period is decided on the reading at its start; inside a period only the clock moves.
    pub fn step(&mut self, now: f64, set_c: f64, reading: Reading) -> Decision {
        let mut new_period = false;
        if !self.started || now >= self.period_start + PERIOD_S {
            new_period = true;
            self.started = true;
            self.period_start = now;
            self.duty = self.duty_for(set_c, &reading);
            self.on_s = if self.duty >= 0.999 { PERIOD_S } else if self.duty <= 0.001 { 0.0 } else { (self.duty * PERIOD_S).round() };
        }
        let heating = now < self.period_start + self.on_s;
        let until_s = if heating { self.period_start + self.on_s - now } else { self.period_start + PERIOD_S - now };
        Decision { heating, duty: self.duty, set_c, reading, until_s: until_s.max(0.0), new_period }
    }

    /// Heat mode switched off or on again: the next tick starts a fresh period; the learned share stays.
    pub fn reset(&mut self) {
        self.started = false;
    }
}

/// The `HEAT` log line the gate tool reads (tools/heat-gate.mjs): one every LOG_EVERY_S while heat mode is on.
pub fn log_line(unix: f64, phase: &str, set_c: f64, reading: &Reading, duty: f64, heat_w: f64, hash_mhs: f64, until_s: f64) -> String {
    format!(
        "HEAT t={} phase={} set={:.1} room={} src={} duty={:.2} heat_w={:.0} hash={:.1} until={:.0}",
        unix as u64,
        phase,
        set_c,
        if reading.source == Source::None { "-".to_string() } else { format!("{:.1}", reading.room_c) },
        reading.source.name(),
        duty,
        heat_w,
        hash_mhs,
        until_s
    )
}

/// The one line under the switch: what the loop is doing, in the miner's words.
pub fn words(on: bool, phase: &str, set_c: f64, reading: &Reading, duty: f64, until_s: f64) -> String {
    if !on {
        return "Off. The cards mine whenever the node is synced.".into();
    }
    let room = match reading.source {
        Source::Typed => format!("room {:.1} °C from your reading {}", reading.room_c, if reading.age_s < 90.0 { "just now".to_string() } else { format!("{} min ago", (reading.age_s / 60.0).round() as u64) }),
        Source::Card => format!("room about {:.0} °C from the card's sensor (within {:.0} degrees)", reading.room_c, reading.error_c),
        Source::None => "no room reading yet: type one, or the card reads it after 2 minutes of rest".into(),
    };
    let doing = match phase {
        "heating" => format!("heating, {} to go", mins(until_s)),
        "resting" => format!("resting, heats again in {}", mins(until_s)),
        "paused" => "mining is paused, so nothing heats".into(),
        "waiting" => "waiting for work".into(),
        _ => phase.to_string(),
    };
    format!("Holding {:.1} °C: {}. {}. Heat {:.0}% of the time.", set_c, room, doing, duty * 100.0)
}

fn mins(s: f64) -> String {
    let m = (s / 60.0).round() as u64;
    if s < 45.0 { "under a minute".into() } else if m <= 1 { "a minute".into() } else { format!("{m} min") }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A room as a first-order store: C joules per degree, K watts per degree of loss to the outside, P_full watts
    /// from the cards while they heat. The card's die sits offset_c above the room at rest and rise_c more while
    /// mining, settling with a one-minute time constant.
    struct Sim {
        room_c: f64,
        out_c: f64,
        c_j_per_k: f64,
        k_w_per_k: f64,
        p_full_w: f64,
        card_rise_c: f64,
        offset_c: f64,
        rise_now: f64,
    }

    impl Sim {
        fn new(room_c: f64) -> Sim {
            Sim { room_c, out_c: 10.0, c_j_per_k: 400_000.0, k_w_per_k: 20.0, p_full_w: 300.0, card_rise_c: 40.0, offset_c: 8.0, rise_now: 0.0 }
        }
        fn tick(&mut self, heating: bool, dt: f64) {
            let p = if heating { self.p_full_w } else { 0.0 };
            self.room_c += (p - self.k_w_per_k * (self.room_c - self.out_c)) * dt / self.c_j_per_k;
            let target = if heating { self.card_rise_c } else { 0.0 };
            self.rise_now += (target - self.rise_now) * (1.0 - (-dt / 60.0).exp());
        }
        fn card_c(&self) -> f64 {
            self.room_c + self.offset_c + self.rise_now
        }
    }

    /// Four hours after the first hour, the room stays within a degree of the set point and the hash is on exactly
    /// while the slice heats: the gate's shape (docs/plans/ember-heat.md), on a model room with the typed reading
    /// refreshed every half hour, as a miner with a thermometer on the desk would do.
    #[test]
    fn a_typed_reading_holds_the_room_within_a_degree_for_four_hours() {
        let mut sim = Sim::new(19.0);
        let mut ctl = Controller::new();
        let set = 20.0;
        let t0 = 1000.0;
        let mut t = t0;
        let mut typed = (sim.room_c, t0);
        let mut worst: f64 = 0.0;
        let mut heating_s = 0.0;
        let mut hash_on_s = 0.0;
        while t < t0 + 5.0 * 3600.0 {
            if t - typed.1 >= 1800.0 {
                typed = (sim.room_c, t);
            }
            let r = room(t, Some(typed), sim.card_c(), 0.0, 0.0, 0.0);
            assert_eq!(r.source, Source::Typed);
            let d = ctl.step(t, set, r);
            let hash = if d.heating { 100.0 } else { 0.0 };
            sim.tick(d.heating, 1.0);
            if t >= t0 + 3600.0 {
                worst = worst.max((sim.room_c - set).abs());
                if d.heating { heating_s += 1.0; }
                if hash > 0.0 { hash_on_s += 1.0; }
            }
            t += 1.0;
        }
        assert!(worst < 1.0, "the room left the band: worst {worst:.2} degrees");
        assert_eq!(heating_s, hash_on_s, "the hash was on exactly while the slice heated");
        // the room needs 20 W per degree over 10 degrees = 200 W of 300: a duty near two thirds
        assert!((ctl.integral - 0.667).abs() < 0.15, "the integral found the steady share: {:.2}", ctl.integral);
        assert!(heating_s / (4.0 * 3600.0) > 0.5 && heating_s / (4.0 * 3600.0) < 0.85, "the hash followed the duty: {:.2}", heating_s / (4.0 * 3600.0));
    }

    /// The card's own sensor as the only source: every period keeps three minutes of rest, the reading is taken after
    /// that rest with the cooling tail taken off, and the room still holds within a degree over the four hours after
    /// the first. The sensor's slope comes from the last half minute of samples, as the engine takes it.
    #[test]
    fn the_card_sensor_alone_holds_the_room_within_a_degree() {
        let mut sim = Sim::new(19.0);
        let mut ctl = Controller::new();
        let set = 20.0;
        let mut t = 0.0;
        let mut rest_since: Option<f64> = None;
        let mut worst: f64 = 0.0;
        let mut estimate_err: f64 = 0.0;
        let mut samples: std::collections::VecDeque<(f64, f64)> = std::collections::VecDeque::new();
        while t < 5.0 * 3600.0 {
            let rest_for = rest_since.map(|s| t - s).unwrap_or(0.0);
            samples.push_back((t, sim.card_c()));
            while samples.front().map(|(s, _)| t - s > 30.0).unwrap_or(false) { samples.pop_front(); }
            let slope = slope_of(samples.iter().copied());
            let r = room(t, None, sim.card_c(), slope, rest_for, 0.0);
            let d = ctl.step(t, set, r);
            if d.new_period {
                assert!(d.duty <= CARD_DUTY_MAX + 1e-9, "a card-sensed period keeps its rest: {}", d.duty);
                if r.source == Source::Card {
                    estimate_err = estimate_err.max((r.room_c - sim.room_c).abs());
                }
            }
            sim.tick(d.heating, 1.0);
            rest_since = if d.heating { None } else { Some(rest_since.unwrap_or(t)) };
            if t >= 3600.0 {
                worst = worst.max((sim.room_c - set).abs());
            }
            t += 1.0;
        }
        assert!(worst < 1.0, "the room left the band: worst {worst:.2} degrees");
        assert!(estimate_err < OFFSET_ERROR_C, "the card estimate stayed inside its stated error: {estimate_err:.2}");
    }

    /// Without the slope correction the sensor still carries the cooling tail at the end of the rest and the
    /// estimate reads high by more than the stated error: the correction is what makes the card path honest.
    #[test]
    fn the_cooling_tail_is_taken_off_by_the_slope() {
        let mut sim = Sim::new(19.0);
        for _ in 0..600 { sim.tick(true, 1.0); }
        let mut samples: std::collections::VecDeque<(f64, f64)> = std::collections::VecDeque::new();
        let mut t = 0.0;
        for _ in 0..(SETTLE_S as usize) { sim.tick(false, 1.0); t += 1.0; samples.push_back((t, sim.card_c())); while samples.front().map(|(s, _)| t - s > 30.0).unwrap_or(false) { samples.pop_front(); } }
        let slope = slope_of(samples.iter().copied());
        let raw = room(t, None, sim.card_c(), 0.0, SETTLE_S, 8.0);
        let fixed = room(t, None, sim.card_c(), slope, SETTLE_S, 8.0);
        assert!(raw.room_c - sim.room_c > 1.0, "the raw sensor still reads the tail: {:.2} over", raw.room_c - sim.room_c);
        assert!((fixed.room_c - sim.room_c).abs() < 0.5, "the corrected estimate is the room: {:.2} off", fixed.room_c - sim.room_c);
    }

    #[test]
    fn without_a_reading_the_loop_heats_the_find_share_and_says_so() {
        let mut ctl = Controller::new();
        let d = ctl.step(1000.0, 20.0, Reading::none());
        assert_eq!(d.duty, FIND_DUTY);
        assert!(d.heating);
        assert_eq!(ctl.integral, 0.5, "no reading leaves the integral alone");
        assert!(words(true, "heating", 20.0, &d.reading, d.duty, d.until_s).contains("no room reading yet"));
        // the slice ends at 80% of the period, the rest runs to the period's end
        let d2 = ctl.step(1000.0 + FIND_DUTY * PERIOD_S + 1.0, 20.0, Reading::none());
        assert!(!d2.heating);
        assert!(!d2.new_period);
        assert!(d2.until_s <= (1.0 - FIND_DUTY) * PERIOD_S);
    }

    #[test]
    fn the_room_source_order_is_typed_then_card_then_none() {
        let typed = Some((19.5, 1000.0));
        assert_eq!(room(1500.0, typed, 30.0, 0.0, 300.0, 0.0).source, Source::Typed);
        let stale = room(1000.0 + TYPED_FRESH_S + 1.0, typed, 30.0, 0.0, 300.0, 0.0);
        assert_eq!(stale.source, Source::Card);
        assert!((stale.room_c - 22.0).abs() < 1e-9, "card 30 minus the default offset 8");
        assert_eq!(room(1500.0, None, 30.0, 0.0, 60.0, 0.0).source, Source::None, "a card still warm from mining is not the room");
        assert_eq!(room(1500.0, None, 0.0, 0.0, 600.0, 0.0).source, Source::None, "no card sensor at all");
        assert!((room(1500.0, None, 30.0, 0.02, 300.0, 0.0).room_c - 22.0).abs() < 1e-9, "a rising sensor is not corrected");
        let learned = room(1500.0, None, 30.0, 0.0, 300.0, 11.0);
        assert!((learned.room_c - 19.0).abs() < 1e-9, "a learned offset replaces the default");
        assert_eq!(learned_offset(30.0, 300.0, 19.0), Some(11.0));
        assert_eq!(learned_offset(30.0, 30.0, 19.0), None, "a card that has not rested teaches nothing");
    }

    #[test]
    fn a_schedule_parses_sorts_and_wraps_at_midnight() {
        let s = parse_schedule("22:00 16, 06:00 20").unwrap();
        assert_eq!(s, vec![Slot { minute: 360, set_c: 20.0 }, Slot { minute: 1320, set_c: 16.0 }]);
        assert_eq!(schedule_text(&s), "06:00 20, 22:00 16");
        assert_eq!(set_point_at(19.0, &s, 7 * 60), 20.0);
        assert_eq!(set_point_at(19.0, &s, 23 * 60), 16.0);
        assert_eq!(set_point_at(19.0, &s, 2 * 60), 16.0, "before the first slot the last one of the day holds");
        assert_eq!(set_point_at(19.0, &[], 2 * 60), 19.0);
        assert!(parse_schedule("06:00").is_err());
        assert!(parse_schedule("25:00 20").is_err());
        assert!(parse_schedule("06:00 40").is_err(), "the set point is 5 to 30");
        assert_eq!(parse_schedule("").unwrap(), vec![]);
        assert_eq!(minute_of_day(0.0, 60), 60, "one hour east of UTC at midnight UTC is 01:00");
        assert_eq!(minute_of_day(0.0, -300), 19 * 60, "five hours west wraps to the evening before");
    }

    #[test]
    fn the_log_line_carries_what_the_gate_reads() {
        let r = Reading { room_c: 19.6, source: Source::Typed, error_c: 0.0, age_s: 30.0 };
        let l = log_line(1_759_800_000.0, "heating", 20.0, &r, 0.6, 180.0, 74.2, 312.0);
        assert_eq!(l, "HEAT t=1759800000 phase=heating set=20.0 room=19.6 src=typed duty=0.60 heat_w=180 hash=74.2 until=312");
        let l2 = log_line(1.0, "resting", 20.0, &Reading::none(), 0.8, 0.0, 0.0, 10.0);
        assert!(l2.contains("room=- src=none"));
    }

    #[test]
    fn the_words_name_the_source_the_slice_and_the_share() {
        let r = Reading { room_c: 19.6, source: Source::Typed, error_c: 0.0, age_s: 2400.0 };
        assert_eq!(words(true, "heating", 20.0, &r, 0.6, 312.0), "Holding 20.0 °C: room 19.6 °C from your reading 40 min ago. heating, 5 min to go. Heat 60% of the time.");
        let c = Reading { room_c: 19.0, source: Source::Card, error_c: 3.0, age_s: 0.0 };
        assert_eq!(words(true, "resting", 20.0, &c, 0.5, 100.0), "Holding 20.0 °C: room about 19 °C from the card's sensor (within 3 degrees). resting, heats again in 2 min. Heat 50% of the time.");
        assert!(words(false, "off", 20.0, &c, 0.0, 0.0).starts_with("Off."));
    }
}

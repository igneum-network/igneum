//! The engine's watchdog, as state machines with no clock and no process (seconds come from the caller), so the
//! rules can be unit-tested with recorded miner lines.
//!
//! Per card (`CardWatch`): a miner that prints no status line for 90 s, or reports a hash rate of 0 for 60 s while
//! the node is ready, is restarted; when that recurs before five minutes of healthy status the next restart waits
//! longer (10 s, 30 s, 2 min, 5 min, then every 5 min, for ever: the founder, 7 October 2026, "it needs to be truly plug,
//! tune, play"; no fault is permanent and the old one-restart-then-faulted state no longer exists). The reason stays on
//! the card in plain words and the hash resumes on its own. A miner is judged only while the node is READY: synced
//! and with an executed tip (`igneum_getExecStatus`); with no template it cannot print status, so every silence clock
//! holds while the node catches up (the first Windows machine, 7 October 2026: three workers faulted "no status line" during the node's
//! catch-up and stayed faulted until a cards-API bounce). The miner's own worker restarts (`WORKER FAULT`,
//! `worker exited`) suspend both rules until the worker is ready again, so the app never restarts a miner that is
//! already restarting its worker (no double restarts); if the worker is not back within 180 s the app steps in.
//! Exit code 43 (the miner gave up on its worker after three guard trips) is a watchdog restart on the same ladder.
//!
//! Per node (`NodeWatch`): a node of ours that gives no sign of life for 120 s is restarted by the app, with a
//! growing delay when it repeats inside ten minutes. Its catch-up never counts: until the node has been read as
//! synced once since its start, silence is not held against it (a node that never answers at all is restarted after
//! 30 minutes), and any RPC answer (the watch reading, the exec status probe, an accepted block) is a sign of life.
//!
//! Review round 4, X21 (4 October 2026): the app now reads `mismatched=` and `faults=` from STATUS lines and the
//! `WORKER FAULT` lines, and shows them on the card.

/// No status line from a running miner for this long: restart it.
pub const NO_STATUS_S: f64 = 90.0;
/// A worker that gives no status within this many seconds of its start, with the node synced, is one restart then
/// faulted (the first Windows machine, 7 October 2026 04:51Z: the row asks for a fault line at 60 s).
pub const START_S: f64 = 60.0;
/// The start of a restart reason the node's readiness, not the card, explains: the ladder resets when the node syncs.
const NODE_FAULTS: [&str; 2] = ["no status line from the miner", "the worker gave no status within"];
/// Seconds before the n-th restart of a card for a repeated fault (the last value repeats for ever).
pub const RETRY_LADDER_S: [u64; 4] = [10, 30, 120, 300];
/// A node that has never answered since its start is given this long before the watchdog restarts it.
pub const NODE_STARTUP_CAP_S: f64 = 1800.0;
/// A worker that has not reported its program loaded (`ready`) this long after its start is restarted on the ladder.
/// The status clocks start at `ready`, never before (MF-4, 7 October 2026: two cards sat in "loading the program"
/// behind a third card's export storm and were faulted at 90 s).
pub const LOAD_S: f64 = 300.0;
/// A miner that exits (any code but 0, 42, 43, 44) within this many seconds of its start is in a crash loop: its
/// restarts follow the ladder instead of the 5 to 60 s jitter.
pub const EARLY_EXIT_S: f64 = 120.0;
/// A card whose worker failed its self-test is held this long before the next try (or until its driver changes).
pub const SELF_TEST_HOLD_S: u64 = 1800;

/// MF-14 (the second Windows machine, 7 October 2026 18:34 UK: two rows sat on "exporting this hour's program" for 18 minutes while a third
/// card mined): a worker never waits on the program export longer than one retry interval without the reason shown;
/// past two intervals the wait is over, the row names what blocked it and the worker retries on its own interval.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ExportWait {
    /// inside one interval: the row keeps "exporting this hour's program"
    Waiting,
    /// past one interval: the row says how long and what blocks the export
    Say(String),
    /// past two intervals: the wait ends; the reason, for the restart on the ladder
    GiveUp(String),
}

/// `waited_s` since the export was asked, `interval_s` the card's current retry interval (the ladder rung, at least
/// 30 s), `blocker` what holds the export now (another card's export, the pack directory lock, the node not at the
/// epoch, the last export error), or empty when nothing is known.
pub fn export_wait(waited_s: f64, interval_s: f64, blocker: &str) -> ExportWait {
    let interval = interval_s.max(30.0);
    let what = if blocker.trim().is_empty() { "the export has not returned".to_string() } else { blocker.trim().to_string() };
    if waited_s >= 2.0 * interval {
        ExportWait::GiveUp(format!("the program export did not return in {} s ({what}); the worker retries on its own interval", waited_s as u64))
    } else if waited_s >= interval {
        ExportWait::Say(format!("exporting this hour's program: {} s so far ({what})", waited_s as u64))
    } else {
        ExportWait::Waiting
    }
}

/// The delay before restart number `attempt` (1-based) of a card: the ladder, then its last step for ever.
pub fn retry_delay_s(attempt: u32) -> u64 {
    let i = (attempt.max(1) as usize - 1).min(RETRY_LADDER_S.len() - 1);
    RETRY_LADDER_S[i]
}
/// Hash rate 0 while the node is synced and the worker is ready for this long: restart the miner.
pub const ZERO_RATE_S: f64 = 60.0;
/// The miner is restarting its own worker: the app waits this long for `ready` before it steps in.
pub const WORKER_RESTART_GRACE_S: f64 = 180.0;
/// Continuous healthy status (rate above 0) that renews the one-restart budget.
pub const HEALTHY_RESET_S: f64 = 300.0;
/// `igneum-miner` exit code when its guards gave up on the worker (three trips in ten minutes).
pub const MINER_GAVE_UP_CODE: i32 = 43;
/// No `watch` reading from our node for this long: restart it.
pub const NODE_SILENT_S: f64 = 120.0;
/// `igneum-miner` exit code when its worker refused the program pack it was started with and the miner could not
/// rebuild it (or rebuilt it `PACK_REBUILD_CAP` times this epoch): the app exports the pack from the node again
/// before the next start, at most `PACK_REBUILD_CAP` times per epoch, then shows the card.
pub const PACK_OUT_OF_DATE_CODE: i32 = 44;
/// Pack rebuilds per epoch seed before the app stops restarting the miner on it.
pub const PACK_REBUILD_CAP: u32 = 3;
/// Node restarts inside this window grow the delay before the next one.
pub const NODE_WINDOW_S: f64 = 600.0;

/// What the watchdog reads from one `STATUS` line of `igneum-miner`.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct Status {
    /// MH/s of the last interval (`now=`; older miners: the average `hash=`)
    pub hash_now: f64,
    pub mismatched: u64,
    pub faults: u64,
    pub restarts: u64,
    pub synced: bool,
    /// seconds the miner has waited for a template with none known (`template_wait=`, an earlier release miners; 0 before)
    pub template_wait_s: f64,
    /// the node's last template time in ms (`template_ms=`; 0 when the line has none)
    pub template_ms: f64,
    /// identities the miner fetches for now (`identities_active=`; 0 when the line has none)
    pub identities_active: u64,
}

/// Parses a miner STATUS line; None for any other line.
pub fn parse_status(text: &str) -> Option<Status> {
    if !text.contains(" STATUS '") {
        return None;
    }
    let avg = kv_f64(text, "hash").unwrap_or(0.0);
    Some(Status {
        hash_now: kv_f64(text, "now").unwrap_or(avg),
        mismatched: kv_u64(text, "mismatched").unwrap_or(0),
        faults: kv_u64(text, "faults").unwrap_or(0),
        restarts: kv_u64(text, "restarts").unwrap_or(0),
        synced: kv(text, "synced") == Some("true"),
        template_wait_s: kv_f64(text, "template_wait").unwrap_or(0.0),
        template_ms: kv_f64(text, "template_ms").unwrap_or(0.0),
        identities_active: kv_u64(text, "identities_active").unwrap_or(0),
    })
}

/// The reason text of a `WORKER FAULT` line, without the timestamp and the trailing restart note.
/// The card's words for a worker fault: a program swap at the epoch cut reads as the dataset being prepared for the new
/// epoch (with the seed's first eight hex), never as "seed mismatch" (the chain's seed is not what moved; the founder
/// read it as the node's seed being wrong, 9 October 2026 10:3x UK). Every other fault reads as the miner wrote it.
pub fn card_words(fault: &str) -> String {
    if fault.contains("seed mismatch") {
        let seed = fault.split("epoch ").nth(1).and_then(|s| s.split(|c: char| !c.is_ascii_hexdigit()).next()).filter(|s| s.len() >= 8).map(|s| &s[..8])
            .or_else(|| fault.split("the node is at ").nth(1).and_then(|s| s.split(|c: char| !c.is_ascii_hexdigit()).next()).filter(|s| s.len() >= 8).map(|s| &s[..8]));
        match seed { Some(s) => format!("preparing the new epoch's dataset (epoch {s})"), None => "preparing the new epoch's dataset".to_string() }
    } else {
        fault.to_string()
    }
}

pub fn fault_reason(text: &str) -> Option<String> {
    let i = text.find("WORKER FAULT ")?;
    let rest = &text["WORKER FAULT ".len() + i..];
    let end = rest.find("; restarting the worker").or_else(|| rest.find("; killing the worker")).unwrap_or(rest.len());
    Some(rest[..end].trim().to_string())
}

#[derive(Debug, Clone, PartialEq)]
pub enum Event<'a> {
    /// The app started the miner process.
    Started,
    /// The worker said ready.
    Ready,
    /// A STATUS line.
    Status(&'a Status),
    /// The miner killed its worker (a guard) or saw it exit; it restarts the worker itself.
    WorkerRestart(&'a str),
    /// the worker's "prepared" line: its pair for the epoch is resident
    Prepared,
    /// The miner process ended with this code.
    Exited(i32),
    /// The app stopped the miner on purpose (pause, settings, node restart).
    Stopped,
    /// The user changed the card's settings: a faulted card may try again.
    Reset,
    /// The node became synced: a card faulted for silence while the node was not ready tries again (the first Windows machine, 7 October
    /// 2026 04:51Z: every card faulted "no status line" during the post-relaunch sync and never came back).
    NodeSynced,
    /// The worker failed its self-test (MF-4): the card is held for `SELF_TEST_HOLD_S` with the reason on its row.
    SelfTestFailed(&'a str),
    /// The card's driver or platform changed (a re-enumeration): a held card tries again now.
    DriverChanged,
    /// The miner printed "template fetch timed out": it is alive and the node is not answering templates (the first Windows machine,
    /// 7 October 2026 04:51Z: the node reported synced from the first second while its finality replay blocked the
    /// template RPC for three minutes; with no template the miner prints no status line). Silence is not the card's
    /// fault while this goes on; the miner is judged again once templates flow.
    TemplateTimeout,
}

#[derive(Debug, Clone, PartialEq)]
pub enum Action {
    None,
    /// Stop the miner and start it again after `delay_s`, for this reason (`attempt` counts restarts since the card
    /// was last healthy for five minutes).
    Restart { reason: String, delay_s: u64, attempt: u32 },
}

#[derive(Debug, Default)]
pub struct CardWatch {
    started_s: Option<f64>,
    last_status_s: Option<f64>,
    ready: bool,
    /// when the worker reported its program loaded: the status clocks start here
    ready_s: Option<f64>,
    /// held after a self-test failure until the driver changes or the hold passes
    driver_hold: bool,
    zero_since: Option<f64>,
    healthy_since: Option<f64>,
    worker_restart_since: Option<f64>,
    /// the card's measured prepare time: its worker's first start to its ready line, then every restart to the line
    /// that brings it back (ready, prepared, or the first hashes); the restart grace is twice this, 180 s the floor
    prepare_s: Option<f64>,
    /// app-level restarts without five healthy minutes since (the ladder position)
    restarts: u32,
    /// the reason of the last restart (a node-caused one resets the ladder when the node syncs)
    last_reason: String,
    last_fault: String,
    /// the miner's last line was a template timeout (the node not answering), cleared by the next status line
    templates_blocked: bool,
    /// v0.2.2: the node holds the worker (no peers, or no template for the window); every clock holds and the card
    /// reads this cause, never a worker fault
    held: Option<&'static str>,
}

impl CardWatch {
    /// True while the miner's last word was a template timeout: the node, not the card, holds the hashing.
    pub fn templates_blocked(&self) -> bool {
        self.templates_blocked
    }
}

impl CardWatch {
    pub fn new() -> Self {
        Self::default()
    }

    /// Restarts since the card was last healthy for five minutes: the ladder position.
    pub fn watchdog_restarts(&self) -> u32 {
        self.restarts
    }

    /// The card's measured prepare time in seconds (None before its worker's first ready line).
    pub fn prepare_s(&self) -> Option<f64> {
        self.prepare_s
    }

    /// How long the miner's own worker restart gets before the app steps in: twice the card's measured prepare time,
    /// WORKER_RESTART_GRACE_S the floor (the mini, 9 October 2026: a fixed 180 s is shorter than a slow card's prepare).
    pub fn restart_grace_s(&self) -> f64 {
        match self.prepare_s {
            Some(p) if p * 2.0 > WORKER_RESTART_GRACE_S => p * 2.0,
            _ => WORKER_RESTART_GRACE_S,
        }
    }

    /// The worker is back (its ready or prepared line, or hashes again): the restart clock stops and the time it
    /// took is the card's measured prepare.
    fn worker_back(&mut self, now_s: f64) {
        let since = self.worker_restart_since.take().or(if self.ready_s.is_none() { self.started_s } else { None });
        if let Some(t) = since {
            let took = now_s - t;
            if took > 0.0 { self.prepare_s = Some(self.prepare_s.map_or(took, |p| p.max(took))); }
        }
    }

    fn arm(&mut self, now_s: f64) {
        self.started_s = Some(now_s);
        self.last_status_s = None;
        self.ready = false;
        self.ready_s = None;
        self.zero_since = None;
        self.healthy_since = None;
        self.worker_restart_since = None;
    }

    fn escalate(&mut self, reason: String) -> Action {
        self.started_s = None;
        self.last_status_s = None;
        self.ready = false;
        self.zero_since = None;
        self.healthy_since = None;
        self.worker_restart_since = None;
        self.restarts = self.restarts.saturating_add(1);
        self.last_reason = reason.clone();
        Action::Restart { reason, delay_s: retry_delay_s(self.restarts), attempt: self.restarts }
    }

    pub fn event(&mut self, now_s: f64, ev: Event<'_>) -> Action {
        match ev {
            Event::Started => {
                self.arm(now_s);
                Action::None
            }
            Event::Ready => {
                self.worker_back(now_s);
                self.ready = true;
                self.ready_s = Some(now_s);
                self.driver_hold = false;
                Action::None
            }
            // the worker answered a prepare with its pair resident: it is back, whatever it says next (the mini,
            // 9 October 2026: after a prepare the worker says "prepared", never "ready" again, and the app killed a
            // mining miner at 180 s twelve times in an hour)
            Event::Prepared => {
                self.worker_back(now_s);
                self.ready = true;
                if self.ready_s.is_none() { self.ready_s = Some(now_s); }
                Action::None
            }
            Event::Status(s) => {
                self.last_status_s = Some(now_s);
                if s.hash_now > 0.0 && self.worker_restart_since.is_some() { self.worker_back(now_s); self.ready = true; }
                if s.template_wait_s > 0.0 && s.hash_now <= 0.0 {
                    // MF-5: the miner is alive and waiting on the node for a template; a zero rate here is the
                    // node's latency, never the card's fault
                    self.templates_blocked = true;
                    self.zero_since = None;
                    self.healthy_since = None;
                    return Action::None;
                }
                self.templates_blocked = false;
                if s.hash_now > 0.0 {
                    self.zero_since = None;
                    let since = *self.healthy_since.get_or_insert(now_s);
                    if now_s - since >= HEALTHY_RESET_S {
                        self.restarts = 0;
                    }
                } else {
                    self.healthy_since = None;
                    if self.ready && self.worker_restart_since.is_none() {
                        self.zero_since.get_or_insert(now_s);
                    }
                }
                Action::None
            }
            Event::WorkerRestart(reason) => {
                self.last_fault = reason.to_string();
                self.worker_restart_since = Some(now_s);
                self.ready = false;
                self.zero_since = None;
                self.healthy_since = None;
                Action::None
            }
            Event::Exited(code) => {
                let started = self.started_s;
                self.started_s = None;
                if code == MINER_GAVE_UP_CODE && started.is_some() {
                    let why = if self.last_fault.is_empty() { "its guards tripped three times in ten minutes".to_string() } else { self.last_fault.clone() };
                    self.escalate(format!("the miner gave up on its worker: {why}"))
                } else {
                    match started {
                        // a crash loop: the ladder, not the 5 to 60 s jitter (MF-4)
                        Some(t) if !matches!(code, 0 | 42 | 43 | PACK_OUT_OF_DATE_CODE) && now_s - t < EARLY_EXIT_S => {
                            self.escalate(format!("the miner exited with code {code} {:.0} s after starting", now_s - t))
                        }
                        _ => Action::None,
                    }
                }
            }
            Event::SelfTestFailed(reason) => {
                self.started_s = None;
                self.ready = false;
                self.driver_hold = true;
                self.last_reason = format!("not usable on this driver: {reason}");
                Action::Restart { reason: self.last_reason.clone(), delay_s: SELF_TEST_HOLD_S, attempt: self.restarts.max(1) }
            }
            Event::DriverChanged => {
                if self.driver_hold {
                    self.driver_hold = false;
                    self.restarts = 0;
                }
                Action::None
            }
            Event::Stopped => {
                self.started_s = None;
                self.last_status_s = None;
                self.ready = false;
                self.zero_since = None;
                self.worker_restart_since = None;
                Action::None
            }
            Event::Reset => {
                *self = Self::default();
                Action::None
            }
            Event::TemplateTimeout => {
                // the miner's own heartbeat: every silence clock starts over from this line
                self.started_s = Some(now_s);
                if self.last_status_s.is_some() {
                    self.last_status_s = Some(now_s);
                }
                self.zero_since = None;
                self.templates_blocked = true;
                Action::None
            }
            Event::NodeSynced => {
                // restarts the node's readiness explained do not count against the card
                if self.restarted_for_node() {
                    self.restarts = 0;
                    self.last_reason.clear();
                }
                Action::None
            }
        }
    }

    /// True while the card waits out a self-test failure (released by a driver change or the hold's end).
    pub fn driver_hold(&self) -> bool {
        self.driver_hold
    }

    /// True when the last restart was for a reason the node's readiness explains (released by Event::NodeSynced).
    pub fn restarted_for_node(&self) -> bool {
        NODE_FAULTS.iter().any(|p| self.last_reason.starts_with(p))
    }

    /// What the node holds the worker with, while it does (v0.2.2): "no block templates: the node has no peers" or
    /// "no block templates: the node answers none for the window"; None when the node is not in the way.
    pub fn held_by_node(&self) -> Option<&'static str> {
        self.held
    }

    /// Called every engine tick while the miner process is alive. `node_ready`: synced AND an executed tip.
    /// `no_peers`: the node reports zero peers (v0.2.2, the founder's call of 8 October 2026: a worker idle for lack of
    /// templates is the node's doing; every clock holds, the restart grace included, and the card names the node).
    pub fn tick(&mut self, now_s: f64, node_ready: bool, no_peers: bool) -> Action {
        let Some(started) = self.started_s else { return Action::None };
        if no_peers || self.templates_blocked {
            self.held = Some(if no_peers { "no block templates: the node has no peers" } else { "no block templates: the node answers none for the window" });
            self.started_s = Some(now_s);
            if self.last_status_s.is_some() {
                self.last_status_s = Some(now_s);
            }
            if self.worker_restart_since.is_some() {
                self.worker_restart_since = Some(now_s);
            }
            self.zero_since = None;
            return Action::None;
        }
        self.held = None;
        if let Some(t) = self.worker_restart_since {
            // the miner is restarting its worker: its own guards own the card until the worker is ready
            let grace = self.restart_grace_s();
            if now_s - t > grace {
                let why = card_words(&self.last_fault);
                return self.escalate(format!("the worker did not come back within {} s of the miner restarting it ({why})", grace as u64));
            }
            return Action::None;
        }
        let node_synced = node_ready;
        if !node_synced {
            // a miner is judged only while the node is ready: with no template it cannot print status, so the silence
            // clocks (start, no status, zero rate) hold at now until the node is back (the first Windows machine, 7 October 2026)
            self.started_s = Some(now_s);
            if self.last_status_s.is_some() {
                self.last_status_s = Some(now_s);
            }
            self.zero_since = None;
            return Action::None;
        }
        // the status clocks start when the worker reports its program loaded (MF-4), never before; loading itself
        // is bounded by LOAD_S
        let Some(ready_at) = self.ready_s else {
            if now_s - started > LOAD_S {
                // the founder's word (8 October 2026): with no program to load the worker is waiting for the node's execution
                // state for this epoch (the node serves no epoch state stream); the restart keeps the worker fresh for when it
                // arrives, and the row says what is waited for, never that the worker failed
                return self.escalate(format!("waiting for the work source's execution state for this epoch: no program to load {} s after starting", LOAD_S as u64));
            }
            return Action::None;
        };
        if self.last_status_s.is_none() && now_s - ready_at > START_S {
            return self.escalate(format!("the worker gave no status within {} s of loading its program", START_S as u64));
        }
        let last = self.last_status_s.unwrap_or(ready_at);
        if now_s - last > NO_STATUS_S {
            return self.escalate(format!("no status line from the miner for {} s", NO_STATUS_S as u64));
        }
        if node_synced && self.ready {
            if let Some(z) = self.zero_since {
                if now_s - z >= ZERO_RATE_S {
                    return self.escalate(format!("hash rate 0 for {} s while the node is synced", ZERO_RATE_S as u64));
                }
            }
        }
        Action::None
    }
}

/// The node watchdog: no reading for `NODE_SILENT_S` restarts our node, with a growing delay when it repeats.
#[derive(Debug, Default)]
pub struct NodeWatch {
    restarts_s: Vec<f64>,
}

impl NodeWatch {
    pub fn new() -> Self {
        Self::default()
    }

    /// `silent_s`: seconds since the node's last sign of life (a watch reading, an exec status answer, an accepted
    /// block; or since the node started, when it never answered). `settled`: the node has been read as synced at
    /// least once since its start; before that its catch-up never counts, only `NODE_STARTUP_CAP_S` of total silence
    /// does. Returns the delay in seconds before the restart when one is due.
    pub fn tick(&mut self, now_s: f64, ours: bool, silent_s: f64, accepted_recent: bool, settled: bool) -> Option<u64> {
        if !ours || accepted_recent {
            return None;
        }
        if silent_s < if settled { NODE_SILENT_S } else { NODE_STARTUP_CAP_S } {
            return None;
        }
        self.restarts_s.retain(|t| now_s - *t <= NODE_WINDOW_S);
        self.restarts_s.push(now_s);
        let n = self.restarts_s.len();
        Some((3u64.saturating_mul(1u64 << (n.saturating_sub(1) * 2).min(8))).min(300))
    }

    pub fn restarts_in_window(&self) -> usize {
        self.restarts_s.len()
    }
}

fn kv<'a>(line: &'a str, key: &str) -> Option<&'a str> {
    let pat = format!(" {key}=");
    let i = line.find(&pat)? + pat.len();
    let rest = &line[i..];
    let end = rest.find(|c: char| c == ' ' || c == ',' || c == ')' || c == ';').unwrap_or(rest.len());
    Some(&rest[..end])
}

fn kv_u64(line: &str, key: &str) -> Option<u64> {
    kv(line, key)?.parse().ok()
}

fn kv_f64(line: &str, key: &str) -> Option<f64> {
    kv(line, key)?.trim_end_matches('s').parse().ok()
}

/// The decision after a pack refusal (exit `PACK_OUT_OF_DATE_CODE`, or a `PACK OUT OF DATE` / `error 0 pack` line
/// from the miner): rebuild the pack before the restart, or stop for this epoch. 5 October 2026: the app restarted
/// two workers every 20 to 40 s for an hour on a pack neither it nor the miner had re-exported in between.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum PackAction {
    /// Export the pack from the node again (the n-th time this epoch) and start the miner on it now.
    Rebuild { n: u32, cap: u32 },
    /// The cap for this epoch is reached: show the card with this reason and wait for the next epoch (or a reset).
    GiveUp { n: u32, reason: String },
}

/// Pack rebuilds per epoch seed (the epoch the exported pack names in its seeds.txt).
#[derive(Debug, Default)]
pub struct PackRebuilds {
    epoch: Option<String>,
    n: u32,
}

impl PackRebuilds {
    pub fn new() -> Self {
        Self::default()
    }

    /// The miner exited over its pack while the exported pack is for `epoch`; `why` is the miner's reason.
    pub fn decide(&mut self, epoch: &str, why: &str) -> PackAction {
        if self.epoch.as_deref() != Some(epoch) {
            self.epoch = Some(epoch.to_string());
            self.n = 0;
        }
        if self.n >= PACK_REBUILD_CAP {
            return PackAction::GiveUp { n: self.n, reason: format!("the program pack still fails after {} rebuilds this epoch ({why}); the worker and the pack disagree", self.n) };
        }
        self.n += 1;
        PackAction::Rebuild { n: self.n, cap: PACK_REBUILD_CAP }
    }

    pub fn count(&self) -> u32 {
        self.n
    }
}

/// A miner line that names a pack refusal: the worker's own `error 0 pack <dir>: <why>` (relayed as
/// `worker error: ...`) or the miner's `PACK OUT OF DATE <dir>: <why>`. Returns the reason, in plain words.
pub fn pack_refusal(text: &str) -> Option<String> {
    if let Some(i) = text.find("PACK OUT OF DATE") {
        let rest = text[i + "PACK OUT OF DATE".len()..].trim_start_matches(':').trim();
        return Some(rest.split_once(": ").map(|(_, why)| why).unwrap_or(rest).to_string());
    }
    if let Some(i) = text.find("error 0 pack ") {
        let rest = &text[i + "error 0 pack ".len()..];
        let (_, why) = rest.split_once(": ")?;
        return Some(why.trim().to_string());
    }
    None
}

/// The epoch seed hex an exported pack names (`epoch_seed_hex <hex>` in its seeds.txt), 16 chars.
pub fn pack_epoch_of(seeds_txt: &str) -> Option<String> {
    seeds_txt.lines().find_map(|l| l.strip_prefix("epoch_seed_hex ")).map(|h| h.trim().chars().take(16).collect())
}

#[cfg(test)]
mod tests {
    use super::*;

    // The refusal as the first Windows machine and the second Windows machine logged it on 5 October 2026 (epoch 34), the miner's own line, and non-refusals.
    const REFUSAL: &str = "! 1791224840.037 worker error: error 0 pack packs\\devnet: the epoch seed bytes do not give the pack's IGNEUM_SEEDW_INIT (wrong seeds.txt for this pack?)";
    const MINER_LINE: &str = "1791224840.100 PACK OUT OF DATE packs\\devnet: the worker refused its program pack (the epoch seed bytes do not give the pack's IGNEUM_SEEDW_INIT); rebuilding the program pack before the restart";

    #[test]
    fn pack_refusal_reads_both_lines_and_nothing_else() {
        assert_eq!(pack_refusal(REFUSAL).unwrap(), "the epoch seed bytes do not give the pack's IGNEUM_SEEDW_INIT (wrong seeds.txt for this pack?)");
        assert!(pack_refusal(MINER_LINE).unwrap().starts_with("the worker refused its program pack"));
        assert_eq!(pack_refusal("! 1791224651.247 worker error: error 5838 epoch seed mismatch: this worker holds epoch bed7ab62cbece66c"), None);
        assert_eq!(pack_refusal(STATUS_OK), None);
        assert_eq!(pack_epoch_of("epoch_seed_hex 009858237e118f69abc8d096e9b1af21c24539eaecdfd1b896588825660a69ec\nday_seed_hex 69676e65\n").as_deref(), Some("009858237e118f69"));
        assert_eq!(pack_epoch_of("day_seed_hex 69676e65\n"), None);
    }

    /// Known-good: a refusal rebuilds the pack, once per refusal, and a new epoch starts the count again.
    #[test]
    fn pack_rebuilds_known_good() {
        let mut p = PackRebuilds::new();
        assert_eq!(p.decide("009858237e118f69", "x"), PackAction::Rebuild { n: 1, cap: PACK_REBUILD_CAP });
        assert_eq!(p.decide("009858237e118f69", "x"), PackAction::Rebuild { n: 2, cap: PACK_REBUILD_CAP });
        assert_eq!(p.decide("5e5d0c3b2a19f8e7", "x"), PackAction::Rebuild { n: 1, cap: PACK_REBUILD_CAP });
        assert_eq!(p.count(), 1);
    }

    /// Known-mismatched: a pack the worker refuses after every rebuild stops at the cap with the reason in plain
    /// words, and stays stopped for that epoch.
    #[test]
    fn pack_rebuilds_known_mismatched_gives_up_at_the_cap() {
        let mut p = PackRebuilds::new();
        for n in 1..=PACK_REBUILD_CAP {
            assert_eq!(p.decide("009858237e118f69", "the epoch seed bytes do not give the pack's IGNEUM_SEEDW_INIT"), PackAction::Rebuild { n, cap: PACK_REBUILD_CAP });
        }
        match p.decide("009858237e118f69", "the epoch seed bytes do not give the pack's IGNEUM_SEEDW_INIT") {
            PackAction::GiveUp { n, reason } => {
                assert_eq!(n, PACK_REBUILD_CAP);
                assert_eq!(reason, "the program pack still fails after 3 rebuilds this epoch (the epoch seed bytes do not give the pack's IGNEUM_SEEDW_INIT); the worker and the pack disagree");
            }
            a => panic!("{a:?}"),
        }
        assert!(matches!(p.decide("009858237e118f69", "x"), PackAction::GiveUp { .. }));
        assert_eq!(p.decide("5e5d0c3b2a19f8e7", "x"), PackAction::Rebuild { n: 1, cap: PACK_REBUILD_CAP });
    }

    // Lines as igneum-miner 0.3.x prints them (devnet v4, 4 October 2026; identities and labels shortened).
    const STATUS_OK: &str = "1791138616.597 STATUS 'win-1' [worker]: 70s jobs=486 accepted=3 rejected=0 mismatched=0 extra=0 rate=0.04 blocks/s hash=123.90 MH/s wall (124.20 MH/s inside jobs) now=124.10 MH/s wall (124.30 MH/s inside jobs, 70 jobs, seed walk 0 calls) template_age=0.31s synced=true idle=0.2% (last 10s: 0.1%) queued=2 restarts=0 faults=0 identities=8 accepted_by_identity=1/0/1/0/0/1/0/0";
    const STATUS_ZERO: &str = "1791138626.597 STATUS 'win-1' [worker]: 80s jobs=486 accepted=3 rejected=0 mismatched=0 extra=0 rate=0.04 blocks/s hash=108.41 MH/s wall (124.20 MH/s inside jobs) now=0.00 MH/s wall (0.00 MH/s inside jobs, 0 jobs, seed walk 0 calls) template_age=0.31s synced=true idle=12.5% (last 10s: 100.0%) queued=2 restarts=0 faults=0 identities=8 accepted_by_identity=1/0/1/0/0/1/0/0";
    const STATUS_MISMATCH: &str = "1791138636.597 STATUS 'win-1' [worker]: 90s jobs=500 accepted=3 rejected=0 mismatched=3 extra=0 rate=0.03 blocks/s hash=110.00 MH/s wall (124.20 MH/s inside jobs) now=124.00 MH/s wall (124.30 MH/s inside jobs, 70 jobs, seed walk 0 calls) template_age=0.31s synced=true idle=0.2% (last 10s: 0.1%) queued=2 restarts=1 faults=1 identities=8 accepted_by_identity=1/0/1/0/0/1/0/0";
    const FAULT_LINE: &str = "1791138640.100 WORKER FAULT the last 10 s ran at 4300000.00 MH/s inside jobs against 3.30 MH/s over this worker's healthy intervals, over 10x: the worker is not running its kernel; restarting the worker (fault 1)";

    #[test]
    fn parses_status_and_fault_lines() {
        let s = parse_status(STATUS_OK).unwrap();
        assert_eq!(s, Status { hash_now: 124.10, mismatched: 0, faults: 0, restarts: 0, synced: true, template_wait_s: 0.0, template_ms: 0.0, identities_active: 0 });
        // a an earlier release miner waiting on a slow node (MF-5)
        let w = parse_status("1791151000.000 STATUS 'win-1' [worker]: 120s jobs=0 accepted=0 rejected=0 fee=0 mismatched=0 extra=0 rate=0.00 blocks/s hash=0.00 MH/s wall (0.00 MH/s inside jobs) now=0.00 MH/s wall (0.00 MH/s inside jobs, 0 jobs, seed walk 0 calls) template_age=0.00s synced=true idle=100.0% (last 10s: 100.0%) queued=0 restarts=0 faults=0 identities=24 accepted_by_identity=0 tip_age_s=0 template_wait=37s template_ms=8120 identities_active=1").unwrap();
        assert_eq!((w.template_wait_s, w.template_ms, w.identities_active), (37.0, 8120.0, 1));
        let m = parse_status(STATUS_MISMATCH).unwrap();
        assert_eq!((m.mismatched, m.faults, m.restarts), (3, 1, 1));
        assert_eq!(parse_status(STATUS_ZERO).unwrap().hash_now, 0.0);
        assert_eq!(parse_status("1791138640.100 worker: ready metal Apple_M5_Max prepare 1"), None);
        assert_eq!(fault_reason(FAULT_LINE).unwrap(), "the last 10 s ran at 4300000.00 MH/s inside jobs against 3.30 MH/s over this worker's healthy intervals, over 10x: the worker is not running its kernel");
        assert_eq!(fault_reason(STATUS_OK), None);
    }

    fn healthy(w: &mut CardWatch, from: f64, to: f64) {
        let s = parse_status(STATUS_OK).unwrap();
        let mut t = from;
        while t <= to {
            assert_eq!(w.event(t, Event::Status(&s)), Action::None);
            assert_eq!(w.tick(t, true, false), Action::None);
            t += 10.0;
        }
    }

    fn restart(a: &Action) -> (String, u64, u32) {
        match a {
            Action::Restart { reason, delay_s, attempt } => (reason.clone(), *delay_s, *attempt),
            Action::None => panic!("expected a restart, got None"),
        }
    }

    #[test]
    fn the_ladder() {
        assert_eq!((1..=6).map(retry_delay_s).collect::<Vec<_>>(), vec![10, 30, 120, 300, 300, 300]);
        assert_eq!(retry_delay_s(0), 10);
    }

    #[test]
    fn healthy_miner_is_left_alone() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(2.0, Event::Ready);
        healthy(&mut w, 10.0, 3600.0);
        assert_eq!(w.watchdog_restarts(), 0);
    }

    /// the founder's rule (7 October 2026): no fault is permanent. A zero rate restarts the miner on the ladder 10, 30,
    /// 120, 300, 300 ... s, the reason stays in plain words, and the hash resumes on its own when the worker is back.
    #[test]
    fn zero_rate_restarts_on_the_ladder_for_ever_and_the_hash_resumes() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(2.0, Event::Ready);
        healthy(&mut w, 10.0, 100.0);
        let z = parse_status(STATUS_ZERO).unwrap();
        let mut t = 110.0;
        let mut seen = Vec::new();
        for expect in [(10u64, 1u32), (30, 2), (120, 3), (300, 4), (300, 5), (300, 6)] {
            // the app restarts it after the delay; still zero: the next rung
            w.event(t, Event::Started);
            w.event(t + 2.0, Event::Ready);
            let mut a = Action::None;
            let mut k = 0.0;
            while a == Action::None && k < 200.0 {
                w.event(t + 10.0 + k, Event::Status(&z));
                a = w.tick(t + 10.0 + k, true, false);
                k += 10.0;
            }
            let (reason, delay, attempt) = restart(&a);
            assert!(reason.starts_with("hash rate 0 for 60 s"), "{reason}");
            assert!(!reason.contains("once already"), "the permanent state no longer exists: {reason}");
            assert_eq!((delay, attempt), expect, "rung {}", expect.1);
            seen.push(delay);
            t += 10.0 + k + delay as f64;
        }
        assert_eq!(seen, vec![10, 30, 120, 300, 300, 300]);
        // the worker is healthy again: five healthy minutes and the ladder starts over at 10 s
        w.event(t, Event::Started);
        w.event(t + 2.0, Event::Ready);
        healthy(&mut w, t + 10.0, t + 320.0);
        assert_eq!(w.watchdog_restarts(), 0);
        let a = { let mut a = Action::None; let mut k = 0.0; while a == Action::None { w.event(t + 330.0 + k, Event::Status(&z)); a = w.tick(t + 330.0 + k, true, false); k += 10.0; } a };
        assert_eq!(restart(&a).1, 10);
    }

    #[test]
    fn zero_rate_only_counts_with_a_ready_node_and_a_ready_worker() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        let z = parse_status(STATUS_ZERO).unwrap();
        // not ready yet (program loading): no zero timer
        for t in [10.0, 20.0, 30.0, 40.0, 50.0] {
            w.event(t, Event::Status(&z));
            assert_eq!(w.tick(t, true, false), Action::None);
        }
        w.event(52.0, Event::Ready);
        // node not ready (syncing, or no executed tip yet): no zero timer either
        for t in [60.0, 70.0, 80.0, 90.0, 100.0, 110.0, 120.0, 130.0] {
            w.event(t, Event::Status(&z));
            assert_eq!(w.tick(t, false, false), Action::None);
        }
        assert_eq!(w.tick(140.0, true, false), Action::None, "the timer starts at the first zero status after ready");
        for t in [150.0, 160.0, 170.0, 180.0, 190.0, 200.0] {
            w.event(t, Event::Status(&z));
            w.tick(t, true, false);
        }
        assert!(matches!(w.tick(210.0, true, false), Action::Restart { .. }));
    }

    /// the first Windows machine, 7 October 2026: three workers started while the node caught up, printed nothing (no template), were
    /// restarted once and then marked faulted for good. Now: the silence clocks hold while the node is not ready, a
    /// restart the node explains does not climb the ladder once the node syncs, and nothing is ever permanent.
    #[test]
    fn a_worker_started_while_the_node_catches_up_is_never_faulted() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        // the node is not ready for ten minutes: no action, no climb
        let mut t = 1.0;
        while t < 600.0 {
            assert_eq!(w.tick(t, false, false), Action::None);
            t += 1.0;
        }
        // ready now, the worker has loaded but prints nothing: a restart after START_S, on rung 1
        w.event(t, Event::Ready);
        let mut a = Action::None;
        while a == Action::None && t < 700.0 {
            a = w.tick(t, true, false);
            t += 1.0;
        }
        let (reason, delay, attempt) = restart(&a);
        assert!(reason.starts_with("the worker gave no status within"), "{reason}");
        assert_eq!((delay, attempt), (10, 1));
        assert!(w.restarted_for_node());
        // the node drops back to catching up and syncs again: the ladder resets for a node-caused restart
        w.event(t, Event::NodeSynced);
        assert_eq!(w.watchdog_restarts(), 0);
        w.event(t + 10.0, Event::Started);
        w.event(t + 12.0, Event::Ready);
        healthy(&mut w, t + 20.0, t + 60.0);
    }

    #[test]
    fn no_status_for_90s_restarts() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(2.0, Event::Ready);
        healthy(&mut w, 10.0, 60.0);
        assert_eq!(w.tick(149.0, true, false), Action::None);
        let (reason, delay, _) = restart(&w.tick(151.0, true, false));
        assert!(reason.contains("no status line"), "{reason}");
        assert_eq!(delay, 10);
    }

    #[test]
    fn no_status_from_the_start() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        // loading: the status clock has not started; a worker that never loads is restarted at LOAD_S
        assert_eq!(w.tick(290.0, true, false), Action::None);
        let (reason, delay, _) = restart(&w.tick(301.0, true, false));
        // the founder's word (8 October 2026): a worker with no program to load is waiting for the node's execution state
        // for this epoch (the node serves no epoch state stream); the message says that, never "did not load its program"
        assert!(reason.contains("waiting for the work source's execution state for this epoch"), "{reason}");
        assert!(!reason.contains("did not load its program"), "{reason}");
        assert_eq!(delay, 10);
        // loaded at 200 s (a slow self-test behind another card's export): the 60 s status clock starts there
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(200.0, Event::Ready);
        assert_eq!(w.tick(259.0, true, false), Action::None);
        let (reason, _, _) = restart(&w.tick(261.0, true, false));
        assert!(reason.contains("gave no status within 60 s of loading"), "{reason}");
    }

    /// MF-4 (the first Windows machine, 7 October 2026): a worker that fails its self-test is held for 30 minutes with the reason on its
    /// row, not restarted every few seconds; a driver change releases it; a miner in a crash loop climbs the ladder.
    #[test]
    fn self_test_failure_is_held_and_a_crash_loop_climbs_the_ladder() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        let (reason, delay, _) = restart(&w.event(3.0, Event::SelfTestFailed("3 of 96 vectors mismatched")));
        assert_eq!(reason, "not usable on this driver: 3 of 96 vectors mismatched");
        assert_eq!(delay, SELF_TEST_HOLD_S);
        assert!(w.driver_hold());
        w.event(100.0, Event::DriverChanged);
        assert!(!w.driver_hold());
        // a crash loop: exits 2 s after each start climb 10, 30, 120 s
        let mut w = CardWatch::new();
        let mut t = 0.0;
        let mut delays = Vec::new();
        for _ in 0..3 {
            w.event(t, Event::Started);
            let (reason, delay, _) = restart(&w.event(t + 2.0, Event::Exited(3)));
            assert!(reason.contains("exited with code 3"), "{reason}");
            delays.push(delay);
            t += 2.0 + delay as f64;
        }
        assert_eq!(delays, vec![10, 30, 120]);
        // an exit after a long healthy run is the engine's own jittered restart
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(2.0, Event::Ready);
        healthy(&mut w, 10.0, 600.0);
        assert_eq!(w.event(700.0, Event::Exited(3)), Action::None);
    }

    #[test]
    fn the_miners_own_worker_restart_is_not_doubled() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(2.0, Event::Ready);
        healthy(&mut w, 10.0, 570.0);
        w.event(580.0, Event::WorkerRestart(&fault_reason(FAULT_LINE).unwrap()));
        let z = parse_status(STATUS_ZERO).unwrap();
        for t in [580.0, 590.0, 600.0, 610.0, 620.0, 630.0, 640.0, 650.0, 660.0] {
            w.event(t, Event::Status(&z));
            assert_eq!(w.tick(t, true, false), Action::None, "the miner owns the restart at {t}");
        }
        w.event(665.0, Event::Ready);
        healthy(&mut w, 670.0, 900.0);
        assert_eq!(w.watchdog_restarts(), 0, "no app restart happened");
        // a worker restart that never comes back: the app steps in after the grace
        w.event(910.0, Event::WorkerRestart("no job completed for 60 s with 2 queued"));
        for t in (920..=1080).step_by(10) {
            w.event(t as f64, Event::Status(&z));
            assert_eq!(w.tick(t as f64, true, false), Action::None);
        }
        let (reason, delay, _) = restart(&w.tick(1091.0, true, false));
        assert!(reason.contains("did not come back within 180 s"), "{reason}");
        assert_eq!(delay, 10);
    }

    /// Known failed first (the mini, 9 October 2026, 10:3x UK; every M-series and every slow card at every epoch cut): the
    /// Metal worker's dataset prepare for the new epoch takes minutes on an M6 at 4 GiB; the miner restarts the worker at
    /// the cut ("seed mismatch: the worker holds another program; preparing the current pair epoch e35c20d4… for it"),
    /// the fixed 180 s grace ends it, and the next attempt starts from nothing: the card reads "waiting" for the hour.
    /// The grace is the card's own measured prepare time times two, 180 s the floor; the measurement is the worker's
    /// first start to its ready line, then every restart to its ready line.
    #[test]
    fn the_restart_grace_is_twice_the_cards_measured_prepare_time() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(400.0, Event::Ready); // the M6 took 400 s to prepare its pair on the first start
        assert_eq!(w.prepare_s(), Some(400.0));
        assert_eq!(w.restart_grace_s(), 800.0, "twice the measured prepare, above the 180 s floor");
        healthy(&mut w, 410.0, 460.0);
        w.event(3600.0, Event::WorkerRestart("seed mismatch: the worker holds another program; preparing the current pair epoch e35c20d423a912df day 20735 for it (fault 2)"));
        for t in (3620..=4380).step_by(20) {
            assert_eq!(w.tick(t as f64, true, false), Action::None, "at {t}: the worker gets its measured minutes ({} s) before the app steps in", 800);
        }
        // past twice the measured time the app steps in, naming the dataset, never a seed
        let a = w.tick(4420.0, true, false);
        let reason = match a { Action::Restart { reason, .. } => reason, other => panic!("expected a restart past the measured grace, got {other:?}") };
        assert!(reason.contains("did not come back within 800 s"), "{reason}");
        assert!(reason.contains("preparing the new epoch's dataset"), "{reason}");
        assert!(!reason.contains("seed mismatch"), "a worker's program swap never reads as a seed to a user: {reason}");
        // the mini's exact class: after the restart the worker says "prepared" (never "ready" again) and hashes; no
        // escalation follows at any clock
        let mut m = CardWatch::new();
        m.event(0.0, Event::Started);
        m.event(20.0, Event::Ready);
        healthy(&mut m, 30.0, 90.0);
        m.event(100.0, Event::WorkerRestart("seed mismatch: the worker holds another program; preparing the current pair epoch e35c20d423a912df day 20735 for it (fault 1)"));
        m.event(102.5, Event::Prepared);
        // the worker hashes from 11 s after the fault, as the mini's miner log shows (status lines with hashes)
        healthy(&mut m, 110.0, 700.0);
        assert_eq!(m.watchdog_restarts(), 0, "the prepared worker is back; the app never kills a mining miner");
        assert_eq!(m.prepare_s(), Some(20.0), "the measured prepare stays the larger of the first start and the restart's return (2.5 s)");
        // a fast card keeps the floor
        let mut f = CardWatch::new();
        f.event(0.0, Event::Started);
        f.event(20.0, Event::Ready);
        assert_eq!(f.restart_grace_s(), WORKER_RESTART_GRACE_S);
    }

    /// The card's words for the worker's program swap at the cut: the dataset, the epoch's seed prefix, never "seed mismatch".
    #[test]
    fn the_cards_words_for_a_program_swap_name_the_dataset() {
        assert_eq!(card_words("seed mismatch: the worker holds another program; preparing the current pair epoch e35c20d423a912df3f0964312c9ce4cfdf3fddd9947f4a4b0927ba58a37b125f day 20735 for it (fault 2)"), "preparing the new epoch's dataset (epoch e35c20d4)");
        assert_eq!(card_words("epoch seed mismatch: this worker holds epoch bed7ab12, the node is at 9b980b2f"), "preparing the new epoch's dataset (epoch 9b980b2f)");
        assert_eq!(card_words("no job completed for 60 s with 2 queued"), "no job completed for 60 s with 2 queued");
    }

    /// v0.2.2 (the founder, 8 October 2026, 19:25 on the mini): with the node at 0 peers the worker idles for lack of
    /// templates; the watchdog names the node, never the worker. Every clock holds, the restart grace included, while
    /// the node has no peers or serves no template for the window. Known-failed first: the grace escalated with
    /// "did not come back within 180 s of the miner restarting it" on a node with no peers.
    #[test]
    fn no_peers_or_no_templates_hold_every_clock_and_name_the_node() {
        let z = parse_status(STATUS_ZERO).unwrap();
        // the founder's case: a worker restart while the node has no peers (not ready), the grace long past
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(2.0, Event::Ready);
        healthy(&mut w, 10.0, 60.0);
        w.event(70.0, Event::WorkerRestart("seed mismatch: the node's seed changed under the worker"));
        for t in (80..=600).step_by(20) {
            assert_eq!(w.tick(t as f64, false, true), Action::None, "no peers at {t}: the node holds the worker, not a fault");
        }
        assert_eq!(w.watchdog_restarts(), 0);
        assert_eq!(w.held_by_node(), Some("no block templates: the node has no peers"));
        // the node is back with peers and templates: the grace clock starts from there, not from the restart
        assert_eq!(w.tick(610.0, true, false), Action::None);
        assert_eq!(w.held_by_node(), None);
        w.event(620.0, Event::Ready);
        healthy(&mut w, 630.0, 700.0);
        // a ready node that answers no template for the window (the template timeouts) holds the clocks the same way
        let mut v = CardWatch::new();
        v.event(0.0, Event::Started);
        v.event(2.0, Event::Ready);
        healthy(&mut v, 10.0, 60.0);
        v.event(70.0, Event::WorkerRestart("no job completed for 60 s with 2 queued"));
        for t in (80..=600).step_by(20) {
            v.event(t as f64, Event::TemplateTimeout);
            assert_eq!(v.tick(t as f64, true, false), Action::None, "no template at {t}");
        }
        assert_eq!(v.held_by_node(), Some("no block templates: the node answers none for the window"));
        // with peers and templates the old grace rule stands
        let mut u = CardWatch::new();
        u.event(0.0, Event::Started);
        u.event(2.0, Event::Ready);
        healthy(&mut u, 10.0, 60.0);
        u.event(70.0, Event::WorkerRestart("no job completed for 60 s with 2 queued"));
        for t in (80..=240).step_by(10) {
            u.event(t as f64, Event::Status(&z));
            assert_eq!(u.tick(t as f64, true, false), Action::None);
        }
        let (reason, _, _) = restart(&u.tick(251.0, true, false));
        assert!(reason.contains("did not come back within 180 s"), "{reason}");
    }

    #[test]
    fn exit_43_is_a_restart_on_the_ladder() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(2.0, Event::Ready);
        healthy(&mut w, 10.0, 60.0);
        w.event(70.0, Event::WorkerRestart("cpu re-check: 3 consecutive mismatches (mismatched=3 in this run): the worker computes a wrong program"));
        let (reason, delay, attempt) = restart(&w.event(75.0, Event::Exited(43)));
        assert!(reason.contains("gave up") && reason.contains("wrong program"), "{reason}");
        assert_eq!((delay, attempt), (10, 1));
        w.event(85.0, Event::Started);
        let (_, delay, attempt) = restart(&w.event(300.0, Event::Exited(43)));
        assert_eq!((delay, attempt), (30, 2));
        // an exit 5 s after the start is the crash-loop class (MF-4): the ladder, not the 5 to 60 s jitter
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        assert!(matches!(w.event(5.0, Event::Exited(1)), Action::Restart { delay_s: 10, .. }));
    }

    #[test]
    fn template_timeouts_are_the_miners_heartbeat() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(2.0, Event::Ready);
        healthy(&mut w, 10.0, 60.0);
        // the node stops answering templates for four minutes while the app still reads it as ready
        let mut t = 70.0;
        while t < 300.0 {
            w.event(t, Event::TemplateTimeout);
            assert_eq!(w.tick(t + 1.0, true, false), Action::None, "a heartbeat at {t} is not silence");
            t += 5.0;
        }
        assert!(w.templates_blocked());
        healthy(&mut w, 310.0, 400.0);
        assert!(!w.templates_blocked());
    }

    /// MF-5 (the first Windows machine, 7 October 2026): the node answered templates past 5 s; the miner now prints STATUS every interval
    /// with template_wait= while it waits, and the watchdog measures the worker, never the node.
    #[test]
    fn a_slow_node_never_faults_the_card() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(2.0, Event::Ready);
        healthy(&mut w, 10.0, 60.0);
        let slow = Status { hash_now: 0.0, template_wait_s: 8.0, template_ms: 8120.0, identities_active: 1, synced: true, ..Default::default() };
        let mut t = 70.0;
        while t < 700.0 {
            w.event(t, Event::Status(&slow));
            assert_eq!(w.tick(t, true, false), Action::None, "waiting on the node at {t} is not a fault");
            t += 10.0;
        }
        assert!(w.templates_blocked());
        healthy(&mut w, 710.0, 800.0);
        assert_eq!(w.watchdog_restarts(), 0);
    }

    /// MF-14: known-failed first (an export that never returns), then the known-good shapes.
    #[test]
    fn an_export_that_never_returns_is_named_and_given_up_within_two_intervals() {
        // the the second Windows machine shape: 18 minutes on "exporting" behind another card's export; with a 30 s interval the row
        // names the blocker at 30 s and the wait ends at 60 s
        assert_eq!(export_wait(31.0, 30.0, "another card's export holds the pack lock (Intel Arc B580, 31 s)"), ExportWait::Say("exporting this hour's program: 31 s so far (another card's export holds the pack lock (Intel Arc B580, 31 s))".into()));
        match export_wait(1080.0, 30.0, "") {
            ExportWait::GiveUp(r) => { assert!(r.starts_with("the program export did not return in 1080 s (the export has not returned)")); assert!(r.ends_with("retries on its own interval")); }
            other => panic!("{other:?}"),
        }
        assert!(matches!(export_wait(60.0, 30.0, "the node is not at the epoch yet"), ExportWait::GiveUp(_)));
        // a normal export (5 to 25 s) says nothing
        assert_eq!(export_wait(5.0, 30.0, ""), ExportWait::Waiting);
        assert_eq!(export_wait(25.0, 120.0, ""), ExportWait::Waiting);
        // a longer rung widens the wait, never below 30 s
        assert_eq!(export_wait(100.0, 120.0, "x"), ExportWait::Waiting);
        assert!(matches!(export_wait(125.0, 120.0, "x"), ExportWait::Say(_)));
        assert!(matches!(export_wait(45.0, 10.0, "x"), ExportWait::Say(_)), "the floor is 30 s even on the 10 s rung");
    }

    #[test]
    fn a_deliberate_stop_is_not_a_fault() {
        let mut w = CardWatch::new();
        w.event(0.0, Event::Started);
        w.event(2.0, Event::Ready);
        w.event(30.0, Event::Stopped);
        assert_eq!(w.tick(500.0, true, false), Action::None);
        assert_eq!(w.event(500.0, Event::Exited(0)), Action::None);
        w.event(600.0, Event::Reset);
        assert_eq!(w.watchdog_restarts(), 0);
    }

    /// The node's catch-up never counts against its watchdog (the first Windows machine, 7 October 2026: a 40-second restart loop).
    #[test]
    fn node_watch_waits_out_a_catch_up_and_restarts_a_dead_node_with_growing_delay() {
        let mut n = NodeWatch::new();
        // catching up (never read as synced since its start): 25 minutes of silence is not a restart
        assert_eq!(n.tick(100.0, true, 1500.0, false, false), None);
        // a node that never answers at all: restarted at the 30-minute cap
        assert_eq!(n.tick(100.0, true, 1801.0, false, false), Some(3));
        let mut n = NodeWatch::new();
        assert_eq!(n.tick(100.0, true, 119.0, false, true), None);
        assert_eq!(n.tick(100.0, false, 500.0, false, true), None, "an external node is never restarted");
        assert_eq!(n.tick(100.0, true, 500.0, true, true), None, "our block was accepted in the last minute: the node is alive");
        assert_eq!(n.tick(100.0, true, 120.0, false, true), Some(3));
        assert_eq!(n.tick(300.0, true, 120.0, false, true), Some(12));
        assert_eq!(n.tick(500.0, true, 120.0, false, true), Some(48));
        assert_eq!(n.restarts_in_window(), 3);
        assert_eq!(n.tick(2000.0, true, 120.0, false, true), Some(3), "the window passed");
    }
}

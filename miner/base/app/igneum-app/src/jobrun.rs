//! The remote-job runner (the model is src/jobs.rs). Driven from the engine's tick like the updater:
//!   poll (40 s after start, then every 2 minutes) and, since an earlier release, a wake: one thread long-polls the relay's public
//!   /wake and the engine fetches the moment publish-jobs.sh records a new jobs-file stamp (seconds, not minutes;
//!   backoff 5, 15, 60 s while the relay is unreachable, the 2-minute poll carries on). The fetch: the signed
//!   envelope igneum-jobs.signed.json (an earlier release; the pair igneum-jobs.json + .sig when no envelope is published) from
//!   the folder of the update manifest, verify with the OTA public key, parse, keep the jobs this machine has not run that target it
//!   (machine id, platform, requirements probed here: wsl, wsl-prover, nvidia)
//!   -> run them one at a time, in file order; each id at most once (jobs-state.json, written before the run)
//!   -> kinds: run (a script, optionally elevated, optionally with the miners stopped first), fetch (a file by
//!      https and sha256 into the app data dir), collect (files by glob or a command's output to the log intake),
//!      restart (miners, node or the app), update-now (the updater checks and installs at once), shard-benchmark
//!      (miners stopped, GPU idle, the prove package fetched, prove-shard.sh inside WSL under a time cap, every
//!      RESULT and STAGE line and the results/*.json to the intake, miners back), build (the node and the app
//!      engine for Linux and Windows inside WSL2 as root, mining untouched, outputs zstd-compressed to the relay;
//!      the plan and the stage scripts are src/jobbuild.rs)
//!   -> every job reports to the log intake as run_id job-<id>-<machine id8>: the first line is a JSON summary
//!      (SUMMARY {...}), then the captured output; long jobs report every 5 minutes while they run
//!   -> the dashboard shows the running job (strip) and the history (Settings), and the switch "Allow remote jobs
//!      from Igneum (signed)" with the key fingerprint; off aborts the running job and stops polling
//! Environment (tests): IGNEUM_APP_JOBS_URL overrides the jobs file URL, IGNEUM_APP_JOBS_CHECK_SECS the interval,
//! IGNEUM_APP_JOBS_FIRST_SECS the first delay, IGNEUM_APP_JOBS_WAKE_URL the wake endpoint (set and empty: no waker).

use crate::engine::{Cmd, Shared};
use crate::jobbuild as jb;
use crate::jobs::{self, Eligibility, Job, Ledger};
use crate::manifest;
use serde_json::{json, Value};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

/// The safety-net poll. Before an earlier release this was 600 s and a published job waited up to 10 minutes on every PC (the founder,
/// 5 October 2026: "why is it taking so long for pc2 and pc1s tasks to spin up?"); the wake below makes it seconds.
const CHECK_EVERY_S: u64 = 120;
const RETRY_AFTER_ERROR_S: u64 = 300;
/// The wake endpoint (relay/api/wake.mjs): a public, rate-limited long-poll that answers the moment publish-jobs.sh
/// records a new jobs-file stamp. No token: the apps hold none. IGNEUM_APP_JOBS_WAKE_URL overrides it.
const WAKE_URL: &str = "https://relay.igneum.network/wake";
/// The relay holds a request this long (its function is capped at 60 s); curl's max-time sits 13 s above it.
const WAKE_HOLD_S: u64 = 45;
/// Two wake requests are never closer than this (the relay allows 30 a minute per address, and both PCs share one).
const WAKE_FLOOR_S: u64 = 10;
/// Waits after the 1st, 2nd and every later failed wake request.
const WAKE_BACKOFF_S: [u64; 3] = [5, 15, 60];
const PROGRESS_REPORT_EVERY_S: u64 = 300;
const GPU_IDLE_PCT: f64 = 5.0;
const GPU_IDLE_WAIT_S: u64 = 180;
const DEFAULT_DISTRO: &str = "Ubuntu-24.04";
/// WSL jobs run as root by default: the Ubuntu the app sees is the one of the account the app runs under, and a
/// personal user (the owner's account on the second Windows machine) need not exist there (4 October 2026: `getpwnam(<user>) failed`).
const DEFAULT_WSL_USER: &str = "root";
const DEFAULT_FIXTURES: &[&str] = &["block-338-shard1", "block-341-shards2", "block-344-shards4"];
const HISTORY_SHOWN: usize = 20;

pub enum Event {
    Fetched(Result<Fetched, String>),
    /// the relay's stamp moved (the waker thread): fetch the jobs file now
    Wake(String),
    Progress { id: String, stage: String },
    Finished { id: String, outcome: Outcome },
}

pub struct Fetched {
    pub published_at: String,
    pub total: usize,
    pub runnable: Vec<Job>,
    /// jobs for this machine whose requirements are not met yet: (id, missing)
    pub pending: Vec<(String, Vec<String>)>,
}

#[derive(Clone, Debug, Default)]
pub struct Outcome {
    /// done | failed | timeout | aborted
    pub status: String,
    pub exit: i64,
    pub summary: String,
    pub results: Vec<String>,
    pub uploaded: bool,
}

/// What the engine must do for the running job.
pub enum Action {
    /// Stop the miners (the node keeps running) and hold them; then call `miners_stopped`.
    StopMiners(String),
    RestartMiners,
    RestartNode,
    /// The signed `cards` kind: apply these per-card choices through the app's own card path (persisted), then
    /// call `cards_applied` with the read-back.
    ApplyCards(Vec<crate::engine::CardChoice>),
    /// The relaunch helper was started; the engine quits now.
    RestartApp,
    UpdateNow,
    /// `--cards-off <key,key>` (6 October 2026): switch these cards off through the app's own card path before the
    /// script starts; then call `cards_off_done` with the exact choices that put them back.
    CardsOff(Vec<String>),
    /// The job ended (done, failed, timeout, aborted, the app quitting): put the cards it held back exactly as they were.
    RestoreCards(Vec<crate::engine::CardChoice>),
}

/// The cards a job holds switched off, and the exact choices that restore them (6 October 2026, the watts job
/// run-ca3-pc1-amd-watts-20261006: a script switched the 9070 XT off itself, an app update killed the script mid-run,
/// its finally never ran, and the card stayed off until a hand POST; so the runner owns the switch and the restore).
/// `take` hands the restore list out once: whoever ends the job (Finished, abort on quit) applies it, never twice.
#[derive(Default, Clone)]
pub struct CardHold {
    restore: Vec<crate::engine::CardChoice>,
    taken: bool,
}

impl CardHold {
    pub fn hold(restore: Vec<crate::engine::CardChoice>) -> Self {
        CardHold { restore, taken: false }
    }
    pub fn is_empty(&self) -> bool {
        self.restore.is_empty()
    }
    pub fn keys(&self) -> String {
        self.restore.iter().map(|c| c.key.as_str()).collect::<Vec<_>>().join(", ")
    }
    pub fn take(&mut self) -> Vec<crate::engine::CardChoice> {
        if self.taken {
            return vec![];
        }
        self.taken = true;
        self.restore.clone()
    }
}

/// `vendor:N:code` -> `vendor:code` (the settings.json key with the device index against the live state's key without it:
/// the 6 October finding that one form switched nothing).
pub fn key_without_index(k: &str) -> String {
    let p: Vec<&str> = k.split(':').collect();
    if p.len() >= 3 && !p[1].is_empty() && p[1].bytes().all(|b| b.is_ascii_digit()) {
        format!("{}:{}", p[0], p[2..].join(":"))
    } else {
        k.to_string()
    }
}

/// The restore list of a `cards_leave_off` job: the same entries (identities and cap kept) with enabled=false, so the
/// runner's restore on any exit leaves the card off, persisted by the app's own card path.
pub fn leave_off(restore: Vec<crate::engine::CardChoice>) -> Vec<crate::engine::CardChoice> {
    restore.into_iter().map(|mut c| { c.enabled = false; c }).collect()
}

/// One live card as `cards_off_choices` sees it: key, enabled, identities, power_pct, present.
pub type LiveCard = (String, bool, u32, u32, bool);

/// The `--cards-off` match: every present live card whose key equals a requested key with or without the device
/// index. Returns (the choices that switch them off, the choices that put them back exactly: enabled flag, identities
/// and cap as they were). A requested key that matches no live card switches nothing (apply_cards skips unknown keys).
pub fn cards_off_choices(requested: &[String], live: &[LiveCard]) -> (Vec<crate::engine::CardChoice>, Vec<crate::engine::CardChoice>) {
    let mut off = Vec::new();
    let mut restore = Vec::new();
    for (key, enabled, identities, power_pct, present) in live {
        if !present {
            continue;
        }
        let bare = key_without_index(key);
        let hit = requested.iter().any(|r| r == key || key_without_index(r) == bare);
        if !hit {
            continue;
        }
        let pp = if *power_pct > 0 { Some(*power_pct) } else { None };
        off.push(crate::engine::CardChoice { key: key.clone(), enabled: false, identities: *identities, power_pct: pp });
        restore.push(crate::engine::CardChoice { key: key.clone(), enabled: *enabled, identities: *identities, power_pct: pp });
    }
    (off, restore)
}

/// Shared with the job's thread: the abort flag and the child's pid, so the engine can end a job on quit.
#[derive(Default)]
pub struct Ctl {
    abort: AtomicBool,
    pid: Mutex<Option<u32>>,
}

impl Ctl {
    fn aborted(&self) -> bool {
        self.abort.load(Ordering::Relaxed)
    }
}

struct Active {
    job: Job,
    run_id: String,
    started: Instant,
    started_unix: u64,
    waiting_for_miners: bool,
    holds_miners: bool,
    /// `--cards-off`: the engine switches the cards before the script runs
    waiting_for_cards: bool,
    cards: CardHold,
    ctl: Arc<Ctl>,
}

pub struct Jobs {
    url: String,
    allowed: bool,
    data_root: PathBuf,
    dir: PathBuf,
    ledger: Ledger,
    ledger_path: PathBuf,
    busy: bool,
    next_check: Instant,
    queue: Vec<Job>,
    active: Option<Active>,
    needs_logged: std::collections::HashSet<String>,
    fingerprint: String,
    wake: Arc<WakeCtl>,
    /// a wake arrived while a fetch was in flight: fetch again as soon as it ends
    wake_pending: bool,
    /// the published_at of the last fetched file: an unchanged file is not logged every 2 minutes
    last_published: String,
}

impl Jobs {
    pub fn new(shared: &Arc<Shared>) -> Jobs {
        let env = |k: &str| std::env::var(k).ok().filter(|v| !v.is_empty());
        let url = env("IGNEUM_APP_JOBS_URL").unwrap_or_else(|| jobs::jobs_url_from_manifest(&shared.packaged.update_manifest));
        let app_dir = shared.runtime.app_dir.clone();
        let dir = app_dir.join("jobs");
        let _ = std::fs::create_dir_all(&dir);
        let ledger_path = app_dir.join("jobs-state.json");
        let mut ledger = Ledger::load(&ledger_path);
        let now_unix = crate::platform::unix_now();
        for id in ledger.abandon_running(now_unix) {
            shared.log(&format!("job {id}: was running when the app last stopped; marked aborted (it does not run again)"));
        }
        let _ = ledger.save(&ledger_path);
        let jitter = shared.runtime.machine_id.bytes().fold(0u64, |a, b| a.wrapping_mul(31).wrapping_add(b as u64));
        let first = env("IGNEUM_APP_JOBS_FIRST_SECS").and_then(|v| v.parse().ok()).unwrap_or(40 + jitter % 20);
        // F14: the public miner runs no remote jobs at all (no signing root for them, no url); the lab build does
        let allowed = crate::edition::remote_jobs_possible() && shared.settings.lock().unwrap().remote_jobs;
        let url = if crate::edition::remote_jobs_possible() { url } else { String::new() };
        let data_root = crate::platform::data_root();
        let j = Jobs {
            url,
            allowed,
            data_root,
            dir,
            ledger,
            ledger_path,
            busy: false,
            next_check: Instant::now() + Duration::from_secs(first),
            queue: Vec::new(),
            active: None,
            needs_logged: std::collections::HashSet::new(),
            fingerprint: crate::edition::jobs_key().map(manifest::fingerprint).unwrap_or_default(),
            wake: Arc::new(WakeCtl::new(allowed, &shared.runtime.id8())),
            wake_pending: false,
            last_published: String::new(),
        };
        j.publish(shared);
        // the ping names the last job this machine ran, across restarts (the newest record in jobs-state.json)
        if let Some(last) = j.ledger.records.values().max_by_key(|r| r.started_at) {
            j.wake.set_last_job(&last.id);
        }
        #[cfg(feature = "lab")]
        if !j.url.is_empty() {
            let wake_url = wake_url_of(std::env::var("IGNEUM_APP_JOBS_WAKE_URL").ok());
            if !wake_url.is_empty() {
                let (sh, ctl) = (shared.clone(), j.wake.clone());
                std::thread::spawn(move || wake_loop(sh, wake_url, ctl));
            }
        }
        let sh = shared.clone();
        std::thread::spawn(move || {
            let ctx = account_context();
            sh.log(&format!("account: {ctx}"));
            let w = account_warning(&ctx);
            if !w.is_empty() {
                sh.log(&format!("account: {w}"));
                sh.state.lock().unwrap().jobs.account = w;
            }
        });
        j
    }

    pub fn active(&self) -> bool {
        self.active.is_some()
    }

    /// True while a job has the miners stopped; the engine restarts them when this turns false.
    pub fn holds_miners(&self) -> bool {
        self.active.as_ref().map(|a| a.holds_miners).unwrap_or(false)
    }

    // ---- state for the dashboard ---------------------------------------------------------------------------------

    fn publish(&self, shared: &Arc<Shared>) {
        let mut st = shared.state.lock().unwrap();
        let j = &mut st.jobs;
        j.allowed = self.allowed;
        j.key_fingerprint = self.fingerprint.clone();
        j.url_set = !self.url.is_empty();
        j.queued = self.queue.len() as u32;
        if let Some(a) = &self.active {
            j.active = true;
            j.id = a.job.id.clone();
            j.kind = a.job.kind.clone();
            j.title = a.job.label();
            j.run_id = a.run_id.clone();
            j.started_at = a.started_unix as f64;
            if a.waiting_for_miners {
                j.stage = "stopping the miners".into();
            }
        } else {
            j.active = false;
            j.id = String::new();
            j.kind = String::new();
            j.title = String::new();
            j.run_id = String::new();
            j.stage = String::new();
            j.started_at = 0.0;
            j.results.clear();
        }
        j.history = self
            .ledger
            .history(HISTORY_SHOWN)
            .into_iter()
            .map(|r| crate::state::JobHistory { id: r.id, kind: r.kind, title: r.title, status: r.status, started_at: r.started_at as f64, finished_at: r.finished_at as f64, exit: r.exit, run_id: r.run_id, uploaded: r.uploaded, summary: r.summary })
            .collect();
    }

    pub fn set_allowed(&mut self, shared: &Arc<Shared>, on: bool) {
        if on && !crate::edition::remote_jobs_possible() {
            shared.event("info", "this is the public build: it runs no remote jobs (the lab build does)");
            return;
        }
        self.allowed = on;
        self.wake.on.store(on, Ordering::Relaxed);
        {
            let mut s = shared.settings.lock().unwrap();
            s.remote_jobs = on;
            s.save(&shared.settings_path);
        }
        shared.state.lock().unwrap().settings.remote_jobs = on;
        if on {
            shared.event("info", "remote jobs allowed: signed jobs from Igneum run on this machine");
            self.next_check = Instant::now() + Duration::from_secs(2);
        } else {
            shared.event("info", "remote jobs switched off: nothing from the jobs file runs here");
            self.queue.clear();
            self.abort(shared, "remote jobs switched off");
        }
        self.publish(shared);
    }

    pub fn check_now(&mut self, shared: &Arc<Shared>) -> Option<Action> {
        if !self.allowed || self.busy {
            return None;
        }
        self.next_check = Instant::now();
        self.tick(shared)
    }

    /// Ends the running job (quit, or the switch turned off). The engine releases the miners itself.
    /// Returns the cards the job held, for the engine to put back BEFORE it goes on (on quit this runs on the engine
    /// thread ahead of the exit, so an app update under a job no longer leaves a card off: 6 October 2026, 17:52Z).
    pub fn abort(&mut self, shared: &Arc<Shared>, why: &str) -> Option<Action> {
        let Some(a) = self.active.as_mut() else { return None };
        a.ctl.abort.store(true, Ordering::Relaxed);
        if let Some(pid) = *a.ctl.pid.lock().unwrap() {
            kill_tree(pid);
        }
        shared.log(&format!("job {}: aborted ({why})", a.job.id));
        let id = a.job.id.clone();
        let restore = a.cards.take();
        self.ledger.finish(&id, "aborted", -1, why, false, crate::platform::unix_now());
        let _ = self.ledger.save(&self.ledger_path);
        self.active = None;
        self.publish(shared);
        if restore.is_empty() {
            None
        } else {
            shared.event("info", &format!("job {}: cards restored by the runner (aborted: {why}): {}", id, restore.iter().map(|c| c.key.as_str()).collect::<Vec<_>>().join(", ")));
            Some(Action::RestoreCards(restore))
        }
    }

    // ---- the tick ------------------------------------------------------------------------------------------------

    pub fn tick(&mut self, shared: &Arc<Shared>) -> Option<Action> {
        let now = Instant::now();
        if self.allowed && !self.busy && now >= self.next_check {
            self.start_fetch(shared);
        }
        if self.active.is_none() && self.allowed {
            if let Some(job) = self.queue.first().cloned() {
                self.queue.remove(0);
                let a = self.start(shared, job);
                self.publish(shared);
                return a;
            }
        }
        None
    }

    fn start_fetch(&mut self, shared: &Arc<Shared>) {
        let every = std::env::var("IGNEUM_APP_JOBS_CHECK_SECS").ok().and_then(|v| v.parse().ok()).unwrap_or(CHECK_EVERY_S);
        self.next_check = Instant::now() + Duration::from_secs(every);
        self.wake_pending = false;
        if self.url.is_empty() {
            let mut st = shared.state.lock().unwrap();
            st.jobs.error = "no jobs URL in this build (no update manifest configured)".into();
            return;
        }
        self.busy = true;
        let url = self.url.clone();
        let dir = self.dir.clone();
        let seen: std::collections::HashSet<String> = self.ledger.records.keys().cloned().collect();
        let machine_id = shared.runtime.machine_id.clone();
        let shared2 = shared.clone();
        std::thread::spawn(move || {
            let r = fetch_jobs(&url, &dir).map(|(f, skipped)| {
                for id in &skipped {
                    shared2.log(&format!("job {id}: its kind is unknown to this version ({}); skipped, it waits for an app update", crate::engine::VERSION));
                }
                let now = crate::platform::unix_now();
                let platform = manifest::platform_name();
                let probes: Mutex<std::collections::HashMap<String, bool>> = Mutex::new(std::collections::HashMap::new());
                let env_user = std::env::var("IGNEUM_APP_WSL_USER").unwrap_or_default();
                let mut runnable = Vec::new();
                let mut pending = Vec::new();
                for j in &f.jobs {
                    if seen.contains(&j.id) {
                        continue;
                    }
                    // the probe runs once per requirement and WSL user per poll; every negative result is logged
                    // with its exit code and output (the second Windows machine, an earlier release: "needs wsl-prover" on a machine that had it)
                    let user = { let u = j.str_param("wsl_user"); if u.is_empty() { env_user.clone() } else { u } };
                    let have = |req: &str| -> bool {
                        let key = format!("{req}|{user}");
                        if let Some(v) = probes.lock().unwrap().get(&key) {
                            return *v;
                        }
                        let v = match probe(req, &user) {
                            Ok(how) => {
                                shared2.log(&format!("probe {req}: ok ({how})"));
                                true
                            }
                            Err(e) => {
                                shared2.log(&format!("probe {req}{}: NOT met: {e}", if user.is_empty() { String::new() } else { format!(" (user {user})") }));
                                false
                            }
                        };
                        probes.lock().unwrap().insert(key, v);
                        v
                    };
                    match jobs::eligibility(j, &machine_id, platform, now, &have) {
                        Eligibility::Run => runnable.push(j.clone()),
                        Eligibility::Needs(m) => pending.push((j.id.clone(), m)),
                        _ => {}
                    }
                }
                Fetched { published_at: f.published_at, total: f.jobs.len(), runnable, pending }
            });
            shared2.send(Cmd::Job(Event::Fetched(r)));
        });
    }

    pub fn event(&mut self, shared: &Arc<Shared>, ev: Event) -> Option<Action> {
        match ev {
            Event::Fetched(r) => {
                self.busy = false;
                shared.state.lock().unwrap().jobs.checked_at = crate::platform::unix_now_f();
                match r {
                    Err(e) => {
                        let quiet = e.starts_with("no jobs file");
                        if !quiet {
                            self.next_check = Instant::now() + Duration::from_secs(RETRY_AFTER_ERROR_S);
                        }
                        shared.log(&format!("jobs: {e}"));
                        shared.state.lock().unwrap().jobs.error = if quiet { String::new() } else { e };
                    }
                    Ok(f) => {
                        shared.state.lock().unwrap().jobs.error = String::new();
                        for (id, needs) in &f.pending {
                            if self.needs_logged.insert(id.clone()) {
                                shared.log(&format!("job {id}: for this machine but needs {} (checked again every {} min)", needs.join(", "), CHECK_EVERY_S / 60));
                            }
                        }
                        let mut added = 0;
                        for j in f.runnable {
                            if !self.ledger.seen(&j.id) && !self.queue.iter().any(|q| q.id == j.id) && self.active.as_ref().map(|a| a.job.id != j.id).unwrap_or(true) {
                                self.queue.push(j);
                                added += 1;
                            }
                        }
                        // polled every 2 minutes since an earlier release: the line is for a changed file or new work, not every poll
                        if added > 0 || f.published_at != self.last_published {
                            shared.log(&format!("jobs: file of {} (published {}): {} new for this machine, {} queued, {} waiting on requirements", f.total, f.published_at, added, self.queue.len(), f.pending.len()));
                        }
                        self.last_published = f.published_at.clone();
                        if added > 0 {
                            shared.event("info", &format!("{added} remote job{} received from Igneum", if added == 1 { "" } else { "s" }));
                        }
                    }
                }
                if self.wake_pending {
                    // the file moved while this fetch ran: the next tick fetches again, whatever the retry delay
                    self.wake_pending = false;
                    self.next_check = Instant::now();
                }
                self.publish(shared);
                None
            }
            Event::Wake(_stamp) => {
                if self.busy {
                    self.wake_pending = true;
                } else {
                    self.next_check = Instant::now();
                }
                None
            }
            Event::Progress { id, stage } => {
                if self.active.as_ref().map(|a| a.job.id == id).unwrap_or(false) {
                    shared.state.lock().unwrap().jobs.stage = stage;
                }
                None
            }
            Event::Finished { id, outcome } => {
                let Some(a) = self.active.as_ref() else { return None };
                if a.job.id != id {
                    return None;
                }
                let mut a = self.active.take().unwrap();
                let restore = a.cards.take();
                let now = crate::platform::unix_now();
                self.ledger.finish(&id, &outcome.status, outcome.exit, &outcome.summary, outcome.uploaded, now);
                let _ = self.ledger.save(&self.ledger_path);
                let mins = a.started.elapsed().as_secs() / 60;
                let kind = if outcome.status == "done" { "ok" } else { "error" };
                shared.event(kind, &format!("job {} ({}) {} after {} min, exit {}{}: {}", a.job.id, a.job.label(), outcome.status, mins, outcome.exit, if outcome.uploaded { ", report uploaded" } else { ", report NOT uploaded" }, short(&outcome.summary, 200)));
                for r in outcome.results.iter().take(12) {
                    shared.log(&format!("  {r}"));
                }
                {
                    let mut st = shared.state.lock().unwrap();
                    st.jobs.last = crate::state::JobHistory { id: id.clone(), kind: a.job.kind.clone(), title: a.job.label(), status: outcome.status.clone(), started_at: a.started_unix as f64, finished_at: now as f64, exit: outcome.exit, run_id: a.run_id.clone(), uploaded: outcome.uploaded, summary: outcome.summary.clone() };
                    st.jobs.last_results = outcome.results.clone();
                }
                self.publish(shared);
                if restore.is_empty() {
                    None
                } else {
                    shared.event("info", &format!("job {}: cards restored by the runner ({}): {}", id, outcome.status, restore.iter().map(|c| c.key.as_str()).collect::<Vec<_>>().join(", ")));
                    Some(Action::RestoreCards(restore))
                }
            }
        }
    }

    /// Called by the engine once the miners are stopped for a job that asked for it.
    pub fn miners_stopped(&mut self, shared: &Arc<Shared>) {
        let Some(a) = self.active.as_mut() else { return };
        if !a.waiting_for_miners {
            return;
        }
        a.waiting_for_miners = false;
        a.holds_miners = true;
        let (job, run_id, ctl, started) = (a.job.clone(), a.run_id.clone(), a.ctl.clone(), a.started_unix);
        self.spawn_run(shared, job, run_id, ctl, started);
        self.publish(shared);
    }

    fn start(&mut self, shared: &Arc<Shared>, job: Job) -> Option<Action> {
        let now = crate::platform::unix_now();
        let run_id = job.run_id(&shared.runtime.machine_id);
        if !self.ledger.start(&job, &run_id, now) {
            return None;
        }
        if let Err(e) = self.ledger.save(&self.ledger_path) {
            shared.log(&format!("job {}: cannot write jobs-state.json ({e}); not running it", job.id));
            return None;
        }
        shared.event("info", &format!("job {} ({}) starts: {}", job.id, job.kind, job.label()));
        self.wake.set_last_job(&job.id);
        let ctl = Arc::new(Ctl::default());
        let needs_miners_stopped = job.kind == "shard-benchmark" || (job.kind == "run" && job.bool_param("stop_miners_first"));
        let cards_off: Vec<String> = if job.kind == "run" { job.list_param("cards_off") } else { vec![] };
        self.active = Some(Active { job: job.clone(), run_id: run_id.clone(), started: Instant::now(), started_unix: now, waiting_for_miners: needs_miners_stopped, holds_miners: false, waiting_for_cards: !cards_off.is_empty(), cards: CardHold::default(), ctl: ctl.clone() });
        match job.kind.as_str() {
            "cards" => {
                // engine-side: refused at once for a card this machine does not have; else applied through the
                // app's own card path and reported with the read-back (cards_applied)
                let live: Vec<(String, bool, u32)> = shared.state.lock().unwrap().mining.cards.iter().filter(|c| c.present()).map(|c| (c.key.clone(), c.enabled, c.identities)).collect();
                let (choices, missing) = cards_job_choices(&job, &live);
                let sink = Sink::new(shared, &job, &self.dir);
                if !missing.is_empty() {
                    let summary = format!("refused: this machine has no card {}", missing.join(", "));
                    sink.line(&format!("cards: {summary}; present: {}", live.iter().map(|c| c.0.as_str()).collect::<Vec<_>>().join(", ")));
                    let uploaded = report(shared, &job, &sink, "failed", 2, now, crate::platform::unix_now(), &summary, json!({ "present": live.iter().map(|c| c.0.clone()).collect::<Vec<_>>() }));
                    let o = Outcome { status: "failed".into(), exit: 2, summary, results: vec![], uploaded };
                    self.event(shared, Event::Finished { id: job.id.clone(), outcome: o });
                    return None;
                }
                sink.line(&format!("cards: applying {}", choices.iter().map(|c| format!("{} enabled={} identities={}{}", c.key, c.enabled, c.identities, c.power_pct.map(|p| format!(" power_pct={p}")).unwrap_or_default())).collect::<Vec<_>>().join("; ")));
                return Some(Action::ApplyCards(choices));
            }
            "restart" | "update-now" => {
                // engine-side; the report says what was asked and the ledger closes at once
                let what = job.str_param("what");
                let (summary, action) = match (job.kind.as_str(), what.as_str()) {
                    ("update-now", _) => ("update check and install asked".to_string(), Action::UpdateNow),
                    (_, "miners") => ("miners restarted".to_string(), Action::RestartMiners),
                    (_, "node") => ("node restarted (the miners follow)".to_string(), Action::RestartNode),
                    _ => ("app restarting".to_string(), Action::RestartApp),
                };
                // the app restart's relaunch is the ENGINE's decision (Action::RestartApp, src/engine.rs restart_plan): under the
                // window host the host's own ladder restarts the engine, and a helper here would race it for the single
                // instance (the first Windows machine, 6 and 8 October 2026: "the relaunch helper did not bring it back", twice); the helper is
                // for an engine with no host
                let sink = Sink::new(shared, &job, &self.dir);
                sink.line(&format!("{}: {summary}", job.kind));
                let uploaded = report(shared, &job, &sink, "done", 0, now, crate::platform::unix_now(), &summary, json!({}));
                let o = Outcome { status: "done".into(), exit: 0, summary, results: vec![], uploaded };
                self.event(shared, Event::Finished { id: job.id.clone(), outcome: o });
                Some(action)
            }
            // the cards first (the engine answers through cards_off_done), then the miners, then the script
            _ if !cards_off.is_empty() => Some(Action::CardsOff(cards_off)),
            _ if needs_miners_stopped => Some(Action::StopMiners(format!("job {}: {}", job.id, job.label()))),
            _ => {
                self.spawn_run(shared, job, run_id, ctl, now);
                None
            }
        }
    }

    /// The running job's id, for the hold rule (a hold belongs to the job that took it).
    pub fn active_id(&self) -> Option<String> {
        self.active.as_ref().map(|a| a.job.id.clone())
    }

    /// The running job's cap in minutes (the hold's hard cap).
    pub fn active_cap_minutes(&self) -> u64 {
        self.active.as_ref().map(|a| a.job.timeout_minutes()).unwrap_or(60)
    }

    /// Called by the engine once a `cards` job's choices are applied: the report carries the read-back of every
    /// card named (what the engine holds now) and the job closes done.
    pub fn cards_applied(&mut self, shared: &Arc<Shared>, readback: Vec<(String, bool, u32, u32)>) -> Option<Action> {
        let Some(a) = self.active.as_ref() else { return None };
        if a.job.kind != "cards" {
            return None;
        }
        let (job, started) = (a.job.clone(), a.started_unix);
        let sink = Sink::new(shared, &job, &self.dir);
        let lines: Vec<String> = readback.iter().map(|(k, e, i, p)| format!("{k} enabled={e} identities={i} power_pct={p}")).collect();
        for l in &lines {
            sink.line(&format!("cards: read back {l}"));
        }
        let summary = format!("cards applied: {}", lines.join("; "));
        let uploaded = report(shared, &job, &sink, "done", 0, started, crate::platform::unix_now(), &summary, json!({ "cards": readback.iter().map(|(k, e, i, p)| json!({ "key": k, "enabled": e, "identities": i, "power_pct": p })).collect::<Vec<_>>() }));
        let o = Outcome { status: "done".into(), exit: 0, summary, results: lines, uploaded };
        self.event(shared, Event::Finished { id: job.id.clone(), outcome: o })
    }

    /// Called by the engine once a job's `--cards-off` cards are switched off; `restore` puts them back exactly.
    /// Next: the miners, if the job asked for them too, else the script.
    pub fn cards_off_done(&mut self, shared: &Arc<Shared>, restore: Vec<crate::engine::CardChoice>) -> Option<Action> {
        let Some(a) = self.active.as_mut() else { return None };
        if !a.waiting_for_cards {
            return None;
        }
        a.waiting_for_cards = false;
        // cards_leave_off (7 October 2026, intel-arc): the hold still ends through the app's own card path on any
        // exit, but with enabled=false, so the card stays off and persisted (settings.json) after the job: the way
        // to hold a card out of mining past a job without a script touching api/cards (the 6 October rule).
        let restore = if a.job.bool_param("cards_leave_off") { leave_off(restore) } else { restore };
        a.cards = CardHold::hold(restore);
        if a.waiting_for_miners {
            return Some(Action::StopMiners(format!("job {}: {}", a.job.id, a.job.label())));
        }
        let (job, run_id, ctl, started) = (a.job.clone(), a.run_id.clone(), a.ctl.clone(), a.started_unix);
        self.spawn_run(shared, job, run_id, ctl, started);
        self.publish(shared);
        None
    }

    fn spawn_run(&self, shared: &Arc<Shared>, job: Job, run_id: String, ctl: Arc<Ctl>, started: u64) {
        let shared2 = shared.clone();
        let dir = self.dir.clone();
        let data_root = self.data_root.clone();
        let jobs_url = self.url.clone();
        let held = self.active.as_ref().map(|a| a.cards.clone()).unwrap_or_default();
        let asked: Vec<String> = if job.kind == "run" { job.list_param("cards_off") } else { vec![] };
        std::thread::spawn(move || {
            let sink = Sink::new(&shared2, &job, &dir);
            sink.line(&format!("job {} ({}) on {} machine {} run {run_id}, started {}", job.id, job.kind, shared2.runtime.host, shared2.runtime.machine_id, jobs::format_time(started)));
            if !asked.is_empty() {
                // the runner's own lines: what it switched off and what it will put back, whatever the script does
                if held.is_empty() {
                    sink.line(&format!("cards-off: {} asked, no present card matched (nothing switched; the script's card may be loaded)", asked.join(",")));
                } else {
                    if job.bool_param("cards_leave_off") {
                        sink.line(&format!("cards-off: {} switched off by the runner before this script; cards LEFT OFF: {} by the runner on any exit (done, failed, timeout, aborted, app quit), enabled=false with their own identities and cap, persisted by the app (cards_leave_off)", held.keys(), held.keys()));
                    } else {
                        sink.line(&format!("cards-off: {} switched off by the runner before this script; cards restored: {} by the runner on any exit (done, failed, timeout, aborted, app quit), with their own enabled flag, identities and cap", held.keys(), held.keys()));
                    }
                }
            }
            let ctx = account_context();
            sink.line(&format!("account: {ctx}"));
            let warn = account_warning(&ctx);
            if !warn.is_empty() {
                sink.line(&format!("account: {warn}"));
            }
            let r = match job.kind.as_str() {
                "run" => run_script(&shared2, &job, &sink, &dir, &data_root, &ctl, started),
                "fetch" => run_fetch(&job, &sink, &dir, &data_root, &shared2.runtime.app_dir, &ctl),
                "collect" => run_collect(&shared2, &job, &sink, &data_root, &ctl),
                "shard-benchmark" => run_shard_benchmark(&shared2, &job, &sink, &data_root, &jobs_url, &ctl, started),
                "build" => run_build(&shared2, &job, &sink, &dir, &ctl, started),
                k => Err(format!("kind {k} is not run on this side")),
            };
            let finished = crate::platform::unix_now();
            let (status, exit, summary, extra) = match r {
                Ok(done) => (done.status.clone(), done.exit, done.summary.clone(), done.extra.clone()),
                Err(e) => (if ctl.aborted() { "aborted".to_string() } else { "failed".to_string() }, -1, e, json!({})),
            };
            sink.line(&format!("job {}: {status} (exit {exit}) after {} s: {}", job.id, finished.saturating_sub(started), summary));
            let uploaded = report(&shared2, &job, &sink, &status, exit, started, finished, &summary, extra);
            let results = sink.results.lock().unwrap().clone();
            shared2.send(Cmd::Job(Event::Finished { id: job.id.clone(), outcome: Outcome { status, exit, summary, results, uploaded } }));
        });
    }
}

/// What a kind returns when it ran to the end (its own status: a non-zero exit is "failed", a cap is "timeout").
struct Done {
    status: String,
    exit: i64,
    summary: String,
    extra: Value,
}

// ---- the output sink ------------------------------------------------------------------------------------------------

/// Every line a job produces: appended to <jobs>/<id>/job.log, the last one on the dashboard, RESULT and STAGE
/// lines kept for the summary and shown as they arrive.
pub struct Sink {
    shared: Arc<Shared>,
    id: String,
    pub dir: PathBuf,
    pub log_path: PathBuf,
    file: Mutex<Option<std::fs::File>>,
    pub results: Mutex<Vec<String>>,
}

impl Sink {
    fn new(shared: &Arc<Shared>, job: &Job, jobs_dir: &Path) -> Sink {
        let dir = jobs_dir.join(&job.id);
        let _ = std::fs::create_dir_all(&dir);
        let log_path = dir.join("job.log");
        let file = std::fs::OpenOptions::new().create(true).append(true).open(&log_path).ok();
        Sink { shared: shared.clone(), id: job.id.clone(), dir, log_path, file: Mutex::new(file), results: Mutex::new(Vec::new()) }
    }
    fn line(&self, text: &str) {
        let text = text.replace('\0', "");
        if let Some(f) = self.file.lock().unwrap().as_mut() {
            let _ = writeln!(f, "{text}");
        }
        let t = text.trim_end();
        let is_result = t.starts_with("RESULT") || t.starts_with("STAGE") || t.starts_with("BUILD FAILED");
        if is_result {
            let mut r = self.results.lock().unwrap();
            r.push(t.to_string());
            if r.len() > 200 {
                r.remove(0);
            }
            self.shared.log(&format!("job {}: {t}", self.id));
        }
        let mut st = self.shared.state.lock().unwrap();
        if st.jobs.id == self.id {
            st.jobs.message = short(t, 160);
            if is_result {
                st.jobs.results.push(t.to_string());
                if st.jobs.results.len() > 40 {
                    st.jobs.results.remove(0);
                }
            }
        }
    }
    fn stage(&self, text: &str) {
        self.line(&format!("== {text} =="));
        self.shared.log(&format!("job {}: {text}", self.id));
        self.shared.send(Cmd::Job(Event::Progress { id: self.id.clone(), stage: text.to_string() }));
    }
}

/// The report file (SUMMARY line, then the last 200 KB of the job log) to the intake. True when the intake took it.
fn report(shared: &Arc<Shared>, job: &Job, sink: &Sink, status: &str, exit: i64, started: u64, finished: u64, summary: &str, extra: Value) -> bool {
    let p = &shared.packaged;
    let results = sink.results.lock().unwrap().clone();
    let head = jobs::summary_line(job, &shared.runtime.machine_id, &shared.runtime.host, status, exit, started, finished, summary, &results, extra);
    let mut body = std::fs::read(&sink.log_path).unwrap_or_default();
    if body.len() > 200_000 {
        body = body.split_off(body.len() - 200_000);
    }
    let path = sink.dir.join("report.log");
    let mut out = head.into_bytes();
    out.push(b'\n');
    out.extend_from_slice(&body);
    if std::fs::write(&path, &out).is_err() {
        return false;
    }
    if p.upload_url.is_empty() || p.upload_key.is_empty() {
        return false;
    }
    let machine = format!("{}-{}", shared.runtime.host, shared.runtime.id8());
    crate::update::upload_log(&p.upload_url, &p.upload_key, &format!("job-{}", job.kind), &machine, &job.run_id(&shared.runtime.machine_id), &path, &shared.upload_header())
}

/// One file to the intake under the job's run id, labelled file-<name>. The intake keeps the last 256 KB.
fn upload_file(shared: &Arc<Shared>, job: &Job, path: &Path, label_prefix: &str) -> bool {
    let p = &shared.packaged;
    if p.upload_url.is_empty() || p.upload_key.is_empty() {
        return false;
    }
    let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    let label: String = format!("{label_prefix}-{name}").chars().take(80).collect();
    let machine = format!("{}-{}", shared.runtime.host, shared.runtime.id8());
    crate::update::upload_log(&p.upload_url, &p.upload_key, &label, &machine, &job.run_id(&shared.runtime.machine_id), path, &shared.upload_header())
}

// ---- the wake signal (an earlier release) ----------------------------------------------------------------------------------------
// The PCs have no inbound ports and may sit off the LAN (one miner is in the US), so a new jobs file is announced over
// an outbound long-poll: GET <WAKE_URL>?since=<stamp> is held up to 45 s by the relay and answered the moment the
// stamp recorded by publish-jobs.sh changes. One thread per app; the engine fetches on Event::Wake. The thread never
// busy-loops: a reply that came back early is followed by the rest of a 10 s floor, a failing endpoint backs off
// 5, 15, then 60 s, and an empty stamp (nothing published yet) waits a full hold.

/// Shared with the waker thread: it polls only while remote jobs are allowed. The ping (MF-11, an earlier release): every wake
/// request names this machine (its id8, no secret), the app version and the last job it ran, so the relay can show a
/// machine whose job channel has gone quiet ("silent since <time>, last job <name>") the moment 15 minutes pass with no
/// poll; a dead app polls nothing, and before this nothing on the console said so until the next upload gap was noticed.
pub struct WakeCtl {
    on: AtomicBool,
    machine: String,
    last_job: Mutex<String>,
}

impl WakeCtl {
    pub fn new(on: bool, machine: &str) -> WakeCtl {
        WakeCtl { on: AtomicBool::new(on), machine: machine.to_string(), last_job: Mutex::new(String::new()) }
    }
    pub fn set_last_job(&self, id: &str) {
        *self.last_job.lock().unwrap() = id.to_string();
    }
    /// The query fragment of the ping: machine=<id8>&v=<version>[&job=<id>]; values are the app's own, URL-safe by shape.
    pub fn ping(&self) -> String {
        ping_query(&self.machine, crate::engine::VERSION, &self.last_job.lock().unwrap())
    }
}

/// machine and job ids as the relay reads them: id8 is 8 hex; a job id is the publisher's `[\w.-]` name; anything else
/// is dropped from the query rather than escaped (the relay clips and validates on its side too).
pub fn ping_query(machine: &str, version: &str, last_job: &str) -> String {
    let safe = |s: &str, max: usize| -> String { s.chars().filter(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '-' | '_' | ':')).take(max).collect() };
    let m = safe(machine, 16);
    if m.is_empty() {
        return String::new();
    }
    let mut q = format!("machine={m}&v={}", safe(version, 32));
    let j = safe(last_job, 80);
    if !j.is_empty() {
        q.push_str(&format!("&job={j}"));
    }
    q
}

/// The stamp logic, free of I/O for the tests.
#[derive(Default, Debug)]
struct WakeState {
    stamp: String,
    seeded: bool,
    failures: u32,
}

#[derive(Debug, PartialEq)]
enum WakeReply {
    /// the first stamp seen: remembered, nothing to fetch (the first poll covers the start)
    Seeded,
    Same,
    /// the stamp moved: fetch now
    Changed { from: String },
    /// the relay holds no stamp yet
    Empty,
}

impl WakeState {
    /// A successful reply. The bool says the endpoint had been failing (one recovery line in the log).
    fn reply(&mut self, stamp: &str) -> (WakeReply, bool) {
        let recovered = self.failures > 0;
        self.failures = 0;
        if stamp.is_empty() {
            return (WakeReply::Empty, recovered);
        }
        if !self.seeded {
            self.seeded = true;
            self.stamp = stamp.to_string();
            return (WakeReply::Seeded, recovered);
        }
        if stamp == self.stamp {
            return (WakeReply::Same, recovered);
        }
        let from = std::mem::replace(&mut self.stamp, stamp.to_string());
        (WakeReply::Changed { from }, recovered)
    }

    /// A failed request: how long to wait (5, 15, 60, 60, ... s) and whether this is the first failure of a run.
    fn failed(&mut self) -> (Duration, bool) {
        self.failures += 1;
        let i = (self.failures as usize - 1).min(WAKE_BACKOFF_S.len() - 1);
        (Duration::from_secs(WAKE_BACKOFF_S[i]), self.failures == 1)
    }

    /// What to wait after a reply so two requests are never closer than the floor.
    fn pause_after(elapsed: Duration) -> Duration {
        Duration::from_secs(WAKE_FLOOR_S).saturating_sub(elapsed)
    }
}

/// The wake URL: the environment overrides the built-in one; set and empty switches the waker off.
fn wake_url_of(env: Option<String>) -> String {
    match env {
        None => WAKE_URL.to_string(),
        Some(u) => u.trim().to_string(),
    }
}

fn wake_query(url: &str, since: &str, ping: &str) -> String {
    let mut out = url.to_string();
    let mut sep = if url.contains('?') { '&' } else { '?' };
    if !since.is_empty() {
        out.push(sep);
        out.push_str(&format!("since={since}"));
        sep = '&';
    }
    if !ping.is_empty() {
        out.push(sep);
        out.push_str(ping);
    }
    out
}

/// One long-poll. Ok carries the relay's stamp (empty when it holds none).
fn wake_request(url: &str, since: &str, ping: &str) -> Result<String, String> {
    let full = wake_query(url, since, ping);
    let max_time = (WAKE_HOLD_S + 13).to_string();
    let (code, out) = run_capture(Command::new(crate::platform::tool("curl")).args(["-fsS", "--max-time", &max_time, &full]), Duration::from_secs(WAKE_HOLD_S + 20));
    if code != Some(0) {
        return Err(format!("curl exit {code:?}: {}", short_out(&out)));
    }
    let v: Value = serde_json::from_str(out.trim()).map_err(|e| format!("bad reply ({e}): {}", short_out(&out)))?;
    if v.get("ok").and_then(|b| b.as_bool()) != Some(true) {
        return Err(format!("relay: {}", short_out(&out)));
    }
    Ok(v.get("stamp").and_then(|s| s.as_str()).unwrap_or("").to_string())
}

fn wake_loop(shared: Arc<Shared>, url: String, ctl: Arc<WakeCtl>) {
    let mut st = WakeState::default();
    loop {
        if !ctl.on.load(Ordering::Relaxed) {
            std::thread::sleep(Duration::from_secs(5));
            continue;
        }
        let t0 = Instant::now();
        match wake_request(&url, &st.stamp, &ctl.ping()) {
            Ok(stamp) => {
                let (r, recovered) = st.reply(&stamp);
                if recovered {
                    shared.log("wake: the relay answers again; a new jobs file is fetched within seconds from here");
                }
                match r {
                    WakeReply::Seeded => shared.log(&format!("wake: listening at {url} (jobs stamp {stamp}); a new jobs file is fetched within seconds")),
                    WakeReply::Changed { from } => {
                        shared.log(&format!("wake: jobs stamp {from} -> {stamp}; fetching the jobs file now"));
                        shared.send(Cmd::Job(Event::Wake(stamp)));
                    }
                    WakeReply::Same => {}
                    WakeReply::Empty => std::thread::sleep(Duration::from_secs(WAKE_HOLD_S)),
                }
                std::thread::sleep(WakeState::pause_after(t0.elapsed()));
            }
            Err(e) => {
                let (wait, first) = st.failed();
                if first {
                    shared.log(&format!("wake: {url} unreachable ({e}); the {}-minute poll carries on; retrying in {} s, then {} s, then every {} s", CHECK_EVERY_S / 60, WAKE_BACKOFF_S[0], WAKE_BACKOFF_S[1], WAKE_BACKOFF_S[2]));
                }
                std::thread::sleep(wait);
            }
        }
    }
}

// ---- fetching the jobs file and probing requirements -----------------------------------------------------------------

fn curl(args: &[&str], limit: Duration) -> Result<(), String> {
    let mut c = Command::new(crate::platform::tool("curl"));
    c.args(args);
    let (code, out) = run_capture(&mut c, limit);
    let t = out.trim();
    if code == Some(0) { Ok(()) } else { Err(if t.is_empty() { format!("curl exit {code:?}") } else { t.lines().last().unwrap_or("curl failed").to_string() }) }
}

/// The jobs file, verified; jobs of a kind this version does not know come back as skipped ids.
///
/// One request: the signed envelope `igneum-jobs.signed.json` (jobs.rs, an earlier release) carries the file and its
/// signature together, so the two can never come from two deployments (5 October 2026, 13:19:41Z: this function
/// fetched `igneum-jobs.json` and then `.sig` while a deploy was landing on the edge and refused the pair). When
/// the folder has no envelope (a publisher before an earlier release), the pair is fetched as before. `Cache-Control: no-cache`
/// asks the edge for the current object, as the Mac's own verify does.
fn fetch_jobs(url: &str, dir: &Path) -> Result<(jobs::JobsFile, Vec<String>), String> {
    let jf = dir.join("jobs.json.new");
    let sf = dir.join("jobs.json.sig.new");
    let ef = dir.join("jobs.signed.json.new");
    for f in [&jf, &sf, &ef] {
        let _ = std::fs::remove_file(f);
    }
    let signed_url = jobs::signed_jobs_url(url);
    let (f, skipped) = match curl(&["-fsSL", "--max-time", "20", "-H", "Cache-Control: no-cache", "-o", &ef.display().to_string(), &signed_url], Duration::from_secs(25)) {
        Ok(()) => {
            let bytes = std::fs::read(&ef).map_err(|e| e.to_string())?;
            let key = crate::edition::jobs_key().ok_or("this build runs no remote jobs")?;
            let (f, skipped, inner) = jobs::verify_and_parse_signed_lenient(&bytes, key)?;
            let _ = std::fs::write(&jf, &inner);
            let _ = std::fs::rename(&ef, dir.join("jobs.signed.json"));
            (f, skipped)
        }
        Err(e) if e.contains("404") => {
            // no envelope published: the pair, two requests (a publisher before an earlier release)
            curl(&["-fsSL", "--max-time", "20", "-H", "Cache-Control: no-cache", "-o", &jf.display().to_string(), url], Duration::from_secs(25)).map_err(|e| if e.contains("404") { "no jobs file published".to_string() } else { format!("jobs file: {e}") })?;
            curl(&["-fsSL", "--max-time", "20", "-H", "Cache-Control: no-cache", "-o", &sf.display().to_string(), &format!("{url}.sig")], Duration::from_secs(25)).map_err(|e| format!("jobs signature: {e}"))?;
            let bytes = std::fs::read(&jf).map_err(|e| e.to_string())?;
            let sig = std::fs::read_to_string(&sf).map_err(|e| e.to_string())?;
            let key = crate::edition::jobs_key().ok_or("this build runs no remote jobs")?;
            let r = jobs::verify_and_parse_lenient(&bytes, sig.trim(), key)?;
            let _ = std::fs::rename(&sf, dir.join("jobs.json.sig"));
            r
        }
        Err(e) => return Err(format!("signed jobs file: {e}")),
    };
    let _ = std::fs::rename(&jf, dir.join("jobs.json"));
    Ok((f, skipped))
}

/// What the toolchain probe runs inside the distro: the cargo and sp1 paths set by hand (a login shell from a
/// process without a console need not source ~/.cargo/env), then the three things the prover needs.
const PROVER_PROBE: &str = "export PATH=\"$HOME/.cargo/bin:$HOME/.sp1/bin:$PATH\"; [ -f \"$HOME/.cargo/env\" ] && . \"$HOME/.cargo/env\"; command -v cargo && ls -d \"$HOME/.sp1\" \"$HOME/igneum-prove\" && echo \"user $(id -un) home $HOME\"";

fn short_out(s: &str) -> String {
    let t: String = s.replace(['\r', '\n'], " ").split_whitespace().collect::<Vec<_>>().join(" ");
    t.chars().take(200).collect()
}

/// Does this machine meet a requirement? Probed on the fetch thread, once per poll and WSL user. Ok carries how it
/// was met, Err the exit code and the first 200 characters of the output, for the engine log.
fn probe(req: &str, wsl_user: &str) -> Result<String, String> {
    let wsl = crate::platform::tool("wsl");
    match req {
        "nvidia" => {
            let (code, out) = run_capture(Command::new(crate::platform::tool("nvidia-smi")).arg("-L"), Duration::from_secs(20));
            if code == Some(0) { Ok(short_out(&out)) } else { Err(format!("nvidia-smi -L exit {code:?}: {}", short_out(&out))) }
        }
        "wsl" => {
            if !cfg!(windows) {
                return Err("not Windows".into());
            }
            let (code, out) = run_capture(Command::new(&wsl).arg("--status"), Duration::from_secs(30));
            if code == Some(0) { Ok("wsl --status".into()) } else { Err(format!("wsl --status exit {code:?}: {}", short_out(&out))) }
        }
        "wsl-prover" => {
            if !cfg!(windows) {
                return Err("not Windows".into());
            }
            // the configured user first (the job's wsl_user or IGNEUM_APP_WSL_USER), then root, then the distro's default user
            let mut tried = Vec::new();
            let mut users: Vec<&str> = Vec::new();
            if !wsl_user.is_empty() && wsl_user != DEFAULT_WSL_USER {
                users.push(wsl_user);
            }
            users.push(DEFAULT_WSL_USER);
            users.push("");
            for u in users {
                let file = crate::wslhost::write_script("prover-probe", PROVER_PROBE).map_err(|e| format!("cannot write the WSL probe script: {e}"))?;
                let mut c = crate::wslhost::command(&wsl, DEFAULT_DISTRO, Some(u), &file.path, true, &[]);
                let (code, out) = run_capture(&mut c, Duration::from_secs(90));
                if code == Some(0) {
                    return Ok(format!("toolchain in {DEFAULT_DISTRO}{}: {}", if u.is_empty() { " (default user)".to_string() } else { format!(" as {u}") }, short_out(&out)));
                }
                tried.push(format!("{}: exit {code:?}: {}", if u.is_empty() { "default user".to_string() } else { format!("user {u}") }, short_out(&out)));
            }
            // the payload ships a Linux prover next to the app (wsl2\bin\igneum-prove-host): that meets it too,
            // provided WSL answers at all
            if let Some(dir) = std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.to_path_buf())) {
                let shipped = dir.join("wsl2").join("bin").join("igneum-prove-host");
                if shipped.is_file() {
                    let (code, out) = run_capture(Command::new(&wsl).args(["-d", DEFAULT_DISTRO, "--", "true"]), Duration::from_secs(60));
                    if code == Some(0) {
                        return Ok(format!("shipped prover {} and {DEFAULT_DISTRO} answers", shipped.display()));
                    }
                    tried.push(format!("shipped prover present but {DEFAULT_DISTRO} does not answer: exit {code:?}: {}", short_out(&out)));
                }
            }
            Err(tried.join(" | "))
        }
        _ => Err("unknown requirement".into()),
    }
}

/// Runs a command to the end or the limit; stdout and stderr folded, no window on Windows. (None, ...) = killed.
fn run_capture(cmd: &mut Command, limit: Duration) -> (Option<i32>, String) {
    cmd.stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::piped());
    crate::platform::quiet(cmd);
    let Ok(mut child) = cmd.spawn() else { return (None, "could not start".into()) };
    let out = child.stdout.take();
    let err = child.stderr.take();
    let reader = std::thread::spawn(move || {
        let mut s = String::new();
        if let Some(o) = out {
            let _ = std::io::Read::read_to_string(&mut std::io::BufReader::new(o), &mut s);
        }
        let mut e = String::new();
        if let Some(x) = err {
            let _ = std::io::Read::read_to_string(&mut std::io::BufReader::new(x), &mut e);
        }
        (s, e)
    });
    let deadline = Instant::now() + limit;
    let mut code = None;
    loop {
        match child.try_wait() {
            Ok(Some(st)) => {
                code = st.code();
                break;
            }
            Ok(None) if Instant::now() < deadline => std::thread::sleep(Duration::from_millis(50)),
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                break;
            }
        }
    }
    let (s, e) = reader.join().unwrap_or_default();
    (code, format!("{s}{e}").replace('\0', ""))
}

// ---- running a child with the output streamed into the sink, under a cap and the abort flag ----------------------------

struct Ran {
    /// None when killed (timeout or abort)
    code: Option<i32>,
    timed_out: bool,
}

fn run_streamed(cmd: &mut Command, sink: &Sink, ctl: &Ctl, limit: Duration, shared: &Arc<Shared>, job: &Job, started: u64, summary_while_running: &str) -> Result<Ran, String> {
    cmd.stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::piped());
    crate::platform::quiet(cmd);
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        cmd.process_group(0);
    }
    let mut child = cmd.spawn().map_err(|e| format!("could not start: {e}"))?;
    *ctl.pid.lock().unwrap() = Some(child.id());
    let mut readers = Vec::new();
    for (stream, is_err) in [(child.stdout.take().map(|s| Box::new(s) as Box<dyn std::io::Read + Send>), false), (child.stderr.take().map(|s| Box::new(s) as Box<dyn std::io::Read + Send>), true)] {
        let Some(stream) = stream else { continue };
        let sink_shared = sink.shared.clone();
        let sink_id = sink.id.clone();
        let sink_dir = sink.dir.clone();
        let sink_log = sink.log_path.clone();
        readers.push(std::thread::spawn(move || {
            // a light sink clone: the same file (append), the same state slot
            let s = Sink { shared: sink_shared, id: sink_id, dir: sink_dir, log_path: sink_log.clone(), file: Mutex::new(std::fs::OpenOptions::new().append(true).open(&sink_log).ok()), results: Mutex::new(Vec::new()) };
            let reader = std::io::BufReader::new(stream);
            for line in std::io::BufRead::split(reader, b'\n') {
                let Ok(bytes) = line else { break };
                let text = String::from_utf8_lossy(&bytes).trim_end_matches('\r').to_string();
                s.line(&if is_err { format!("! {text}") } else { text });
            }
            s.results.into_inner().unwrap_or_default()
        }));
    }
    let deadline = Instant::now() + limit;
    let mut next_report = Instant::now() + Duration::from_secs(PROGRESS_REPORT_EVERY_S);
    let mut timed_out = false;
    let code = loop {
        match child.try_wait() {
            Ok(Some(st)) => break st.code(),
            Ok(None) => {
                if ctl.aborted() || Instant::now() >= deadline {
                    timed_out = !ctl.aborted();
                    sink.line(&format!("{}: ending the process", if timed_out { "time cap reached" } else { "aborted" }));
                    kill_tree(child.id());
                    let _ = child.kill();
                    let _ = child.wait();
                    break None;
                }
                if Instant::now() >= next_report {
                    next_report = Instant::now() + Duration::from_secs(PROGRESS_REPORT_EVERY_S);
                    report(shared, job, sink, "running", 0, started, 0, summary_while_running, json!({}));
                }
                std::thread::sleep(Duration::from_millis(200));
            }
            Err(e) => {
                sink.line(&format!("wait failed: {e}"));
                break None;
            }
        }
    };
    *ctl.pid.lock().unwrap() = None;
    for r in readers {
        if let Ok(lines) = r.join() {
            let mut all = sink.results.lock().unwrap();
            for l in lines {
                if !all.contains(&l) {
                    all.push(l);
                }
            }
        }
    }
    Ok(Ran { code, timed_out })
}

/// Ends a process and everything it started. Windows: taskkill /T; unix: the process group (set at spawn).
fn kill_tree(pid: u32) {
    #[cfg(windows)]
    {
        let mut c = Command::new(crate::platform::tool("taskkill"));
        c.args(["/T", "/F", "/PID", &pid.to_string()]);
        crate::platform::quiet(&mut c);
        let _ = c.output();
    }
    #[cfg(unix)]
    unsafe {
        libc::kill(-(pid as i32), libc::SIGTERM);
        std::thread::sleep(Duration::from_millis(500));
        libc::kill(-(pid as i32), libc::SIGKILL);
    }
}

fn file_name_from_url(url: &str) -> String {
    let name = url.rsplit('/').next().unwrap_or("file").split('?').next().unwrap_or("file");
    let clean: String = name.chars().filter(|c| c.is_ascii_alphanumeric() || *c == '.' || *c == '-' || *c == '_').collect();
    if clean.is_empty() || clean.starts_with('.') { "file".into() } else { clean }
}

/// Downloads with resume into <final>.part, checks the size (when given) and the sha256, renames.
fn fetch_file(url: &str, final_path: &Path, sha256: &str, size: Option<u64>, sink: &Sink) -> Result<u64, String> {
    let part = PathBuf::from(format!("{}.part", final_path.display()));
    if final_path.is_file() && manifest::sha256_file(final_path).map(|s| s == sha256).unwrap_or(false) {
        sink.line(&format!("{} is already there with the right sha256", final_path.display()));
        return Ok(std::fs::metadata(final_path).map(|m| m.len()).unwrap_or(0));
    }
    let _ = std::fs::remove_file(final_path);
    if let Some(d) = final_path.parent() {
        let _ = std::fs::create_dir_all(d);
    }
    sink.line(&format!("downloading {url}"));
    curl(&["-fsSL", "--retry", "3", "--retry-delay", "5", "-C", "-", "--max-time", "3600", "-o", &part.display().to_string(), url], Duration::from_secs(3660))?;
    let got = std::fs::metadata(&part).map(|m| m.len()).unwrap_or(0);
    if let Some(want) = size {
        if want > 0 && got != want {
            let _ = std::fs::remove_file(&part);
            return Err(format!("size mismatch: got {got} bytes, the job says {want}"));
        }
    }
    let sum = manifest::sha256_file(&part).map_err(|e| e.to_string())?;
    if sum != sha256.to_ascii_lowercase() {
        let _ = std::fs::remove_file(&part);
        return Err("sha256 mismatch: the file is not what the job signed".into());
    }
    std::fs::rename(&part, final_path).map_err(|e| e.to_string())?;
    sink.line(&format!("downloaded {} bytes, sha256 ok", got));
    Ok(got)
}

/// tar -xf (bsdtar on Windows 10 1803+ and macOS reads zip); PowerShell Expand-Archive as the Windows fallback.
fn extract(archive: &Path, into: &Path, sink: &Sink) -> Result<(), String> {
    let _ = std::fs::create_dir_all(into);
    let (code, out) = run_capture(Command::new(crate::platform::tool("tar")).args(["-xf", &archive.display().to_string(), "-C", &into.display().to_string()]), Duration::from_secs(600));
    if code == Some(0) {
        sink.line(&format!("extracted {} into {}", archive.display(), into.display()));
        return Ok(());
    }
    if cfg!(windows) {
        let script = format!("Expand-Archive -LiteralPath '{}' -DestinationPath '{}' -Force", archive.display().to_string().replace('\'', "''"), into.display().to_string().replace('\'', "''"));
        let (c2, o2) = run_capture(Command::new(crate::platform::tool("powershell")).args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &script]), Duration::from_secs(600));
        if c2 == Some(0) {
            sink.line(&format!("extracted {} into {} (Expand-Archive)", archive.display(), into.display()));
            return Ok(());
        }
        return Err(format!("extract failed: tar: {}; Expand-Archive: {}", out.lines().last().unwrap_or(""), o2.lines().last().unwrap_or("")));
    }
    Err(format!("extract failed: {}", out.lines().last().unwrap_or("tar failed")))
}

fn shell_for(job: &Job) -> String {
    let s = job.str_param("shell");
    if !s.is_empty() {
        return s;
    }
    if cfg!(windows) { "powershell".into() } else { "bash".into() }
}

fn job_env(cmd: &mut Command, shared: &Arc<Shared>, job: &Job, job_dir: &Path, data_root: &Path) {
    cmd.env("IGNEUM_JOB_ID", &job.id);
    cmd.env("IGNEUM_JOB_DIR", job_dir);
    cmd.env("IGNEUM_JOB_RUN_ID", job.run_id(&shared.runtime.machine_id));
    cmd.env("IGNEUM_APP_DATA", data_root);
    cmd.env("IGNEUM_APP_DIR", &shared.runtime.app_dir);
    cmd.env("IGNEUM_LOG_DIR", &shared.runtime.log_dir);
    cmd.env("IGNEUM_MACHINE_ID", &shared.runtime.machine_id);
    cmd.env("IGNEUM_INTAKE_URL", &shared.packaged.upload_url);
    cmd.env("IGNEUM_UPLOAD_KEY", &shared.packaged.upload_key);
    cmd.env("IGNEUM_APP_VERSION", crate::engine::VERSION);
}

// ---- kind: run ---------------------------------------------------------------------------------------------------------

fn run_script(shared: &Arc<Shared>, job: &Job, sink: &Sink, jobs_dir: &Path, data_root: &Path, ctl: &Ctl, started: u64) -> Result<Done, String> {
    let dir = jobs_dir.join(&job.id);
    let shell = shell_for(job);
    let body = job.str_param("script").replace("\r\n", "\n");
    // the founder's word at 20:21 UK (8 October 2026): a body that ends a process by name never runs (the relay agent's
    // Check-Task carries the same refusal, exit 77 with the matched line)
    if let Some(line) = jobs::kill_by_name_line(&body) {
        sink.line(&format!("refused: the script ends a process by name ({line}); a pid is the only way"));
        return Ok(Done { status: "failed".into(), exit: 77, summary: format!("refused: the script ends a process by name ({line}); a pid is the only way"), extra: json!({ "refused": "kill_by_name", "line": line }) });
    }
    let script = dir.join(if shell == "powershell" { "script.ps1" } else { "script.sh" });
    let text = if shell == "powershell" { body.replace('\n', "\r\n") } else { body };
    std::fs::write(&script, if shell == "powershell" { [b"\xEF\xBB\xBF".as_slice(), text.as_bytes()].concat() } else { text.into_bytes() }).map_err(|e| format!("cannot write the script: {e}"))?;
    let elevated = cfg!(windows) && job.bool_param("elevated");
    let limit = Duration::from_secs(job.timeout_minutes() * 60);
    sink.stage(&format!("running the {shell} script{} (cap {} min)", if elevated { " as administrator" } else { "" }, job.timeout_minutes()));
    let mut cmd;
    let out_file = dir.join("elevated-output.log");
    if elevated {
        // the elevated process has its own environment: a wrapper sets the IGNEUM_* values, runs the script and
        // sends everything to a file this side reads afterwards. One UAC prompt appears on the PC; with nobody
        // there it times out and the job fails.
        let env_lines: String = [("IGNEUM_JOB_ID", job.id.clone()), ("IGNEUM_JOB_DIR", dir.display().to_string()), ("IGNEUM_JOB_RUN_ID", job.run_id(&shared.runtime.machine_id)), ("IGNEUM_APP_DATA", data_root.display().to_string()), ("IGNEUM_APP_DIR", shared.runtime.app_dir.display().to_string()), ("IGNEUM_LOG_DIR", shared.runtime.log_dir.display().to_string()), ("IGNEUM_MACHINE_ID", shared.runtime.machine_id.clone()), ("IGNEUM_INTAKE_URL", shared.packaged.upload_url.clone()), ("IGNEUM_UPLOAD_KEY", shared.packaged.upload_key.clone()), ("IGNEUM_APP_VERSION", crate::engine::VERSION.to_string())]
            .iter()
            .map(|(k, v)| format!("$env:{k} = '{}'\r\n", v.replace('\'', "''")))
            .collect();
        let wrapper = dir.join("elevated.ps1");
        let w = elevated_wrapper(&env_lines, &script.display().to_string(), &out_file.display().to_string());
        std::fs::write(&wrapper, [b"\xEF\xBB\xBF".as_slice(), w.as_bytes()].concat()).map_err(|e| e.to_string())?;
        let _ = std::fs::remove_file(&out_file);
        let inner = format!("-NoProfile -ExecutionPolicy Bypass -File \"{}\"", wrapper.display());
        cmd = crate::platform::elevated_command("powershell.exe", &inner);
    } else if shell == "powershell" {
        cmd = Command::new(crate::platform::tool("powershell"));
        cmd.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", &script.display().to_string()]);
    } else {
        cmd = Command::new(crate::platform::tool("bash"));
        cmd.arg(&script);
    }
    cmd.current_dir(&dir);
    job_env(&mut cmd, shared, job, &dir, data_root);
    // the elevated script's output reaches this side through a file: follow it while the script runs, so the
    // 5-minute progress reports carry its lines (6 October 2026: a 35-minute run that never mined showed only
    // "script running" until it ended; the lines that said why were in the file the whole time)
    let follow = if elevated { Some(follow_file(sink, out_file.clone())) } else { None };
    let ran = run_streamed(&mut cmd, sink, ctl, limit, shared, job, started, "script running")?;
    if let Some(f) = follow {
        f.stop.store(true, std::sync::atomic::Ordering::Relaxed);
        let seen = f.handle.join().unwrap_or(0);
        // the tail the follower had not read when the script ended
        if let Ok(t) = std::fs::read_to_string(&out_file) {
            for l in t.lines().skip(seen) {
                sink.line(l);
            }
        }
    }
    finish_ran(ran, "script")
}

/// Follows a file another process writes (the elevated script's output), feeding each new complete line to the
/// sink every 2 s until stopped; returns how many lines it delivered, so the caller can hand over the remainder.
struct Follow {
    stop: Arc<std::sync::atomic::AtomicBool>,
    handle: std::thread::JoinHandle<usize>,
}

fn follow_file(sink: &Sink, path: PathBuf) -> Follow {
    let stop = Arc::new(std::sync::atomic::AtomicBool::new(false));
    let stop2 = stop.clone();
    let s = Sink { shared: sink.shared.clone(), id: sink.id.clone(), dir: sink.dir.clone(), log_path: sink.log_path.clone(), file: Mutex::new(std::fs::OpenOptions::new().append(true).open(&sink.log_path).ok()), results: Mutex::new(Vec::new()) };
    let handle = std::thread::spawn(move || {
        let mut seen = 0usize;
        loop {
            if let Ok(t) = std::fs::read_to_string(&path) {
                let lines: Vec<&str> = t.lines().collect();
                // only complete lines (the writer may be mid-line): keep the last one for the next pass unless the
                // text ends with a newline
                let complete = if t.ends_with('\n') { lines.len() } else { lines.len().saturating_sub(1) };
                for l in lines.iter().take(complete).skip(seen) {
                    s.line(l);
                }
                seen = seen.max(complete);
            }
            if stop2.load(std::sync::atomic::Ordering::Relaxed) {
                break seen;
            }
            std::thread::sleep(Duration::from_secs(2));
        }
    });
    Follow { stop, handle }
}

/// The PowerShell wrapper an elevated job runs (its own process, its own environment): the IGNEUM_* values, then one
/// line about its console (the elevated process cannot inherit the engine's headless console and gets one of its own;
/// `-WindowStyle Hidden` on the launch keeps it hidden, and this line is the running measurement of that on every
/// elevated job: "elevated console: hwnd N visible False"), then the script, everything into `out_file` for the engine
/// to read back. The console-window class, the first Windows machine, 5 October 2026 (tools/windows/console-watch-elevated.ps1).
fn elevated_wrapper(env_lines: &str, script: &str, out_file: &str) -> String {
    let (script, out) = (crate::platform::ps_quote(script), crate::platform::ps_quote(out_file));
    format!(
        "{env_lines}$ErrorActionPreference = 'Continue'\r\n\
         $igc = ''\r\n\
         try {{ Add-Type -Name IgCon -Namespace Igneum -MemberDefinition '[DllImport(\"kernel32.dll\")] public static extern System.IntPtr GetConsoleWindow(); [DllImport(\"user32.dll\")] public static extern bool IsWindowVisible(System.IntPtr h);'; $h = [Igneum.IgCon]::GetConsoleWindow(); $igc = \"elevated console: hwnd $h visible $([Igneum.IgCon]::IsWindowVisible($h))\" }} catch {{ $igc = \"elevated console: unknown ($_)\" }}\r\n\
         $igc | Out-File -FilePath '{out}' -Encoding utf8\r\n\
         & '{script}' *>&1 | Out-File -FilePath '{out}' -Encoding utf8 -Append\r\n\
         exit $LASTEXITCODE\r\n"
    )
}

/// The choices a `cards` job asks for, matched to the machine's present cards (key exact, or the key with the device
/// index left out: `vendor::name`); missing keys are returned for the refusal. A field the job leaves out keeps the
/// card's current value.
pub fn cards_job_choices(job: &Job, live: &[(String, bool, u32)]) -> (Vec<crate::engine::CardChoice>, Vec<String>) {
    let mut out = Vec::new();
    let mut missing = Vec::new();
    let list = job.params.get("cards").and_then(|v| v.as_array()).cloned().unwrap_or_default();
    for c in list {
        let key = c.get("key").and_then(|v| v.as_str()).unwrap_or("").trim().to_string();
        let found = live.iter().find(|(k, _, _)| *k == key).or_else(|| {
            let parts: Vec<&str> = key.splitn(3, ':').collect();
            live.iter().find(|(k, _, _)| { let lp: Vec<&str> = k.splitn(3, ':').collect(); parts.len() == 3 && lp.len() == 3 && lp[0] == parts[0] && lp[2] == parts[2] && (parts[1].is_empty() || parts[1] == lp[1]) })
        });
        match found {
            Some((k, enabled, identities)) => out.push(crate::engine::CardChoice {
                key: k.clone(),
                enabled: c.get("enabled").and_then(|v| v.as_bool()).unwrap_or(*enabled),
                identities: c.get("identities").and_then(|v| v.as_u64()).map(|n| n as u32).unwrap_or(*identities),
                power_pct: c.get("power_pct").and_then(|v| v.as_u64()).map(|n| n as u32),
            }),
            None => missing.push(key),
        }
    }
    (out, missing)
}

/// The hold rule (MF-6, the first Windows machine, 7 October 2026: a read-only job that followed a --stop-miners job kept the cards off
/// for its whole run): a hold belongs to the job that took it and releases the moment that job is no longer the
/// running one, whatever runs next, or when the owner's own cap has passed. Returns the reason to release, or None.
pub fn hold_release(owner: Option<&str>, active: Option<&str>, held_s: f64, cap_s: f64) -> Option<&'static str> {
    match owner {
        None => Some("no job owns the hold"),
        Some(o) => {
            if active != Some(o) {
                Some("the job that took the hold is no longer running")
            } else if held_s >= cap_s {
                Some("the hold passed the job's own cap")
            } else {
                None
            }
        }
    }
}

fn finish_ran(ran: Ran, what: &str) -> Result<Done, String> {
    match ran.code {
        Some(0) => Ok(Done { status: "done".into(), exit: 0, summary: format!("{what} finished, exit 0"), extra: json!({}) }),
        Some(251) => Ok(Done { status: "failed".into(), exit: 251, summary: format!("{what} did not start: the administrator prompt was refused, cancelled or timed out (click Yes within 2 minutes)"), extra: json!({}) }),
        Some(c) => Ok(Done { status: "failed".into(), exit: c as i64, summary: format!("{what} exited with code {c}"), extra: json!({}) }),
        None if ran.timed_out => Ok(Done { status: "timeout".into(), exit: -1, summary: format!("{what} hit the time cap and was ended"), extra: json!({}) }),
        None => Err(format!("{what} was ended")),
    }
}

// ---- kind: fetch -------------------------------------------------------------------------------------------------------

fn fetch_base(dir: &str, job: &Job, jobs_dir: &Path, data_root: &Path, app_dir: &Path) -> PathBuf {
    match dir {
        "prove" => data_root.join("prove"),
        "packs" => app_dir.join("packs"),
        "updates" => app_dir.join("updates"),
        _ => jobs_dir.join(&job.id),
    }
}

fn run_fetch(job: &Job, sink: &Sink, jobs_dir: &Path, data_root: &Path, app_dir: &Path, ctl: &Ctl) -> Result<Done, String> {
    let url = job.str_param("url");
    let base = fetch_base(&job.str_param("dir"), job, jobs_dir, data_root, app_dir);
    let to = job.str_param("to");
    let name = if to.is_empty() { PathBuf::from(file_name_from_url(&url)) } else { jobs::safe_rel_path(&to).ok_or("bad 'to'")? };
    let dest = base.join(&name);
    sink.stage(&format!("fetching {} to {}", file_name_from_url(&url), dest.display()));
    let size = fetch_file(&url, &dest, &job.str_param("sha256"), job.u64_param("size"), sink)?;
    if ctl.aborted() {
        return Err("aborted".into());
    }
    let mut summary = format!("{} ({size} bytes, sha256 ok)", dest.display());
    if job.bool_param("extract") {
        let sub = job.str_param("extract_dir");
        let into = if sub.is_empty() { base.clone() } else { base.join(jobs::safe_rel_path(&sub).ok_or("bad extract_dir")?) };
        if job.bool_param("fresh") && !sub.is_empty() && into.starts_with(&base) && into != base {
            sink.line(&format!("fresh: removing {}", into.display()));
            let _ = std::fs::remove_dir_all(&into);
        }
        sink.stage("extracting");
        extract(&dest, &into, sink)?;
        summary.push_str(&format!(", extracted into {}", into.display()));
    }
    Ok(Done { status: "done".into(), exit: 0, summary, extra: json!({ "path": dest.display().to_string(), "size": size }) })
}

// ---- kind: collect -----------------------------------------------------------------------------------------------------

fn run_collect(shared: &Arc<Shared>, job: &Job, sink: &Sink, data_root: &Path, ctl: &Ctl) -> Result<Done, String> {
    let mut uploaded = 0u32;
    let mut failed = 0u32;
    let mut names = Vec::new();
    let globs = job.list_param("globs");
    if !globs.is_empty() {
        sink.stage(&format!("collecting {} pattern{}", globs.len(), if globs.len() == 1 { "" } else { "s" }));
        for g in &globs {
            let files = jobs::glob_files(data_root, g);
            sink.line(&format!("{g}: {} file{}", files.len(), if files.len() == 1 { "" } else { "s" }));
            for f in files.iter().take(50) {
                if ctl.aborted() {
                    return Err("aborted".into());
                }
                let ok = upload_file(shared, job, f, "file");
                let name = f.strip_prefix(data_root).unwrap_or(f).display().to_string();
                sink.line(&format!("  {} {name} ({} bytes)", if ok { "uploaded" } else { "NOT uploaded" }, std::fs::metadata(f).map(|m| m.len()).unwrap_or(0)));
                if ok {
                    uploaded += 1;
                    names.push(name);
                } else {
                    failed += 1;
                }
            }
        }
    }
    let command = job.str_param("command");
    let mut ran: Option<Ran> = None;
    if !command.trim().is_empty() {
        sink.stage(&format!("running: {}", short(&command, 120)));
        let mut cmd = if cfg!(windows) {
            let mut c = Command::new(crate::platform::tool("powershell"));
            c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &command]);
            c
        } else {
            let mut c = Command::new(crate::platform::tool("bash"));
            c.args(["-lc", &command]);
            c
        };
        cmd.current_dir(data_root);
        job_env(&mut cmd, shared, job, &sink.dir, data_root);
        let r = run_streamed(&mut cmd, sink, ctl, Duration::from_secs(600), shared, job, crate::platform::unix_now(), "collect command running")?;
        sink.line(&format!("command exit {:?}", r.code));
        ran = Some(r);
    }
    Ok(collect_done(uploaded, failed, names, ran))
}

/// The outcome of a collect job from its parts: the upload counts and, when a command ran, how it ended. A command
/// that exits non-zero fails the job and its code is the job's exit; one ended by the cap is a timeout; one ended
/// otherwise is failed (4 October 2026, collect-pc1-board3: PowerShell could not parse the command, "command exit
/// Some(1)" was in the report and the job still said "done (exit 0)" because only the upload count was judged).
fn collect_done(uploaded: u32, failed: u32, names: Vec<String>, ran: Option<Ran>) -> Done {
    let files = format!("{uploaded} file{} uploaded{}", if uploaded == 1 { "" } else { "s" }, if failed > 0 { format!(", {failed} failed") } else { String::new() });
    let extra = json!({ "uploaded_files": names });
    let by_files = || (if failed > 0 { "failed" } else { "done" }.to_string(), failed as i64);
    match ran {
        None => { let (status, exit) = by_files(); Done { status, exit, summary: files, extra } }
        Some(Ran { code: Some(0), .. }) => { let (status, exit) = by_files(); Done { status, exit, summary: format!("{files}, command exit 0, output in the report"), extra } }
        Some(Ran { code: Some(c), .. }) => Done { status: "failed".into(), exit: c as i64, summary: format!("{files}, command exited with code {c}, output in the report"), extra },
        Some(Ran { code: None, timed_out: true }) => Done { status: "timeout".into(), exit: -1, summary: format!("{files}, command hit the time cap and was ended"), extra },
        Some(Ran { code: None, timed_out: false }) => Done { status: "failed".into(), exit: -1, summary: format!("{files}, command was ended"), extra },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// cards_leave_off (7 October 2026): the restore entries keep their identities and cap and carry enabled=false,
    /// so the job's exit leaves the card off through the same path; without the param they keep their own flag.
    #[test]
    fn cards_leave_off_restores_the_card_as_off_with_its_settings_kept() {
        let live: Vec<LiveCard> = vec![("other:Intel(R) Arc(TM) B580 Graphics".into(), true, 8, 0, true), ("nvidia:NVIDIA GeForce RTX 5090".into(), true, 2, 80, true)];
        let (off, restore) = cards_off_choices(&["other:Intel(R) Arc(TM) B580 Graphics".to_string()], &live);
        assert_eq!(off.len(), 1);
        assert!(restore[0].enabled, "the plain restore puts the card back on");
        let left = leave_off(restore.clone());
        assert_eq!(left.len(), 1);
        assert_eq!((left[0].key.as_str(), left[0].enabled, left[0].identities, left[0].power_pct), ("other:Intel(R) Arc(TM) B580 Graphics", false, 8, restore[0].power_pct));
        // the job's param decides, through the same hold
        let j = jobs::parse(r#"{"jobs":[{"id":"x","kind":"run","expires_at":"2099-01-01T00:00:00Z","target":{"machine_ids":["ae432dc7"]},"params":{"script":"ls","cards_off":["other:Intel(R) Arc(TM) B580 Graphics"],"cards_leave_off":true}}]}"#).unwrap().jobs.remove(0);
        assert!(j.bool_param("cards_leave_off"));
        assert_eq!(j.list_param("cards_off"), vec!["other:Intel(R) Arc(TM) B580 Graphics".to_string()]);
    }

    #[test]
    fn cards_off_matches_keys_with_and_without_the_device_index_and_restores_exactly() {
        // the first Windows machine, 6 October 2026: settings.json holds amd:1:gfx1201 and amd:3:gfx1201, the live state's key is amd:gfx1201
        let live: Vec<LiveCard> = vec![
            ("amd:gfx1201".into(), true, 8, 0, true),
            ("nvidia:NVIDIA GeForce RTX 5090".into(), true, 8, 75, true),
            ("nvidia:NVIDIA GeForce RTX 4070".into(), false, 2, 80, true),
            ("amd:gfx1036".into(), false, 1, 0, false),
        ];
        let (off, restore) = cards_off_choices(&["amd:1:gfx1201".to_string()], &live);
        assert_eq!(off.len(), 1);
        assert_eq!((off[0].key.as_str(), off[0].enabled, off[0].identities, off[0].power_pct), ("amd:gfx1201", false, 8, None));
        assert_eq!((restore[0].key.as_str(), restore[0].enabled, restore[0].identities), ("amd:gfx1201", true, 8));
        // the live key itself, and the name form of an NVIDIA card: its cap rides along; a disabled card restores as disabled
        let (off, restore) = cards_off_choices(&["amd:gfx1201".to_string(), "nvidia:1:NVIDIA GeForce RTX 4070".to_string()], &live);
        assert_eq!(off.iter().map(|c| c.key.as_str()).collect::<Vec<_>>(), vec!["amd:gfx1201", "nvidia:NVIDIA GeForce RTX 4070"]);
        assert_eq!((restore[1].enabled, restore[1].identities, restore[1].power_pct), (false, 2, Some(80)));
        // a key that matches nothing switches nothing; a removed card is never touched
        assert!(cards_off_choices(&["amd:gfx1036".to_string(), "intel:0:x".to_string()], &live).0.is_empty());
        assert_eq!(key_without_index("nvidia:0:NVIDIA GeForce RTX 5090"), "nvidia:NVIDIA GeForce RTX 5090");
        assert_eq!(key_without_index("amd:gfx1201"), "amd:gfx1201");
    }

    #[test]
    fn a_card_hold_restores_once_on_any_exit() {
        // the known-failed case: the script dies mid-run (exit 1, or the app quits under it); the restore list comes
        // out exactly once, whichever path ends the job, and never a second time
        let restore = vec![crate::engine::CardChoice { key: "amd:gfx1201".into(), enabled: true, identities: 8, power_pct: None }];
        let mut done = CardHold::hold(restore.clone());
        assert_eq!(done.take(), restore, "the known-finished case (done) restores");
        assert!(done.take().is_empty(), "never twice");
        let mut failed = CardHold::hold(restore.clone());
        assert_eq!(failed.take(), restore, "the known-failed case (the script died, exit 1) restores the same list");
        let mut quit = CardHold::hold(restore.clone());
        assert_eq!(quit.take(), restore, "the app quitting under the job (abort) restores the same list");
        assert!(quit.take().is_empty());
        assert!(CardHold::default().take().is_empty(), "a job without --cards-off restores nothing");
        assert_eq!(CardHold::hold(restore).keys(), "amd:gfx1201");
    }

    #[test]
    fn a_refused_administrator_prompt_is_a_failure_not_done() {
        // 5 October 2026: the elevated launcher exited 0 after Windows cancelled an unanswered UAC prompt
        let d = finish_ran(Ran { code: Some(251), timed_out: false }, "script").unwrap();
        assert_eq!((d.status.as_str(), d.exit), ("failed", 251));
        assert!(d.summary.contains("administrator prompt"), "{}", d.summary);
        let d = finish_ran(Ran { code: Some(0), timed_out: false }, "script").unwrap();
        assert_eq!(d.status, "done");
        // the launcher string itself (platform::elevated_ps_line since 13755b9): a thrown Start-Process must not fall
        // through to `exit $p.ExitCode`
        let l = crate::platform::elevated_ps_line("powershell.exe", "-NoProfile -File x.ps1");
        assert!(l.contains("-Verb RunAs -Wait -WindowStyle Hidden -PassThru -ErrorAction Stop } catch {"), "{l}");
        assert!(l.contains("if ($null -eq $p) { Write-Error 'elevated launch failed: no process'; exit 251 }"), "{l}");
    }

    #[test]
    fn elevated_wrapper_reports_its_console_then_runs_the_script() {
        let w = elevated_wrapper("$env:IGNEUM_JOB_ID = 'j1'\r\n", r"C:\jobs\j1\script.ps1", r"C:\jobs\it's\elevated-output.log");
        assert!(w.starts_with("$env:IGNEUM_JOB_ID = 'j1'\r\n$ErrorActionPreference = 'Continue'\r\n"), "{w}");
        assert!(w.contains("GetConsoleWindow()") && w.contains("IsWindowVisible("), "{w}");
        assert!(w.contains("$igc | Out-File -FilePath 'C:\\jobs\\it''s\\elevated-output.log' -Encoding utf8\r\n"), "{w}");
        assert!(w.contains("& 'C:\\jobs\\j1\\script.ps1' *>&1 | Out-File -FilePath 'C:\\jobs\\it''s\\elevated-output.log' -Encoding utf8 -Append\r\n"), "{w}");
        assert!(w.ends_with("exit $LASTEXITCODE\r\n"), "{w}");
        // every line ends in CRLF (the file is written for Windows PowerShell): the env line and six of its own
        assert_eq!(w.matches("\r\n").count(), 7, "{w:?}");
        assert_eq!(w.matches('\n').count(), 7, "{w:?}");
    }

    #[test]
    fn collect_outcome_follows_the_command_exit() {
        let d = collect_done(0, 0, vec![], None);
        assert_eq!((d.status.as_str(), d.exit, d.summary.as_str()), ("done", 0, "0 files uploaded"));
        let d = collect_done(2, 0, vec!["a".into(), "b".into()], Some(Ran { code: Some(0), timed_out: false }));
        assert_eq!((d.status.as_str(), d.exit), ("done", 0));
        assert_eq!(d.summary, "2 files uploaded, command exit 0, output in the report");
        assert_eq!(d.extra["uploaded_files"].as_array().map(|a| a.len()), Some(2));
        // the board3 case: the command failed, the job must not say done
        let d = collect_done(0, 0, vec![], Some(Ran { code: Some(1), timed_out: false }));
        assert_eq!((d.status.as_str(), d.exit), ("failed", 1));
        assert_eq!(d.summary, "0 files uploaded, command exited with code 1, output in the report");
        // a failed upload fails the job even when the command was fine
        let d = collect_done(1, 2, vec!["a".into()], Some(Ran { code: Some(0), timed_out: false }));
        assert_eq!((d.status.as_str(), d.exit), ("failed", 2));
        assert!(d.summary.starts_with("1 file uploaded, 2 failed, command exit 0"));
        let d = collect_done(0, 0, vec![], Some(Ran { code: None, timed_out: true }));
        assert_eq!((d.status.as_str(), d.exit), ("timeout", -1));
        let d = collect_done(0, 0, vec![], Some(Ran { code: None, timed_out: false }));
        assert_eq!((d.status.as_str(), d.exit), ("failed", -1));
    }

    #[test]
    fn wake_state_seeds_then_reports_each_change_once() {
        let mut w = WakeState::default();
        assert_eq!(w.reply(""), (WakeReply::Empty, false));
        assert_eq!(w.reply("S1"), (WakeReply::Seeded, false));
        assert_eq!(w.reply("S1"), (WakeReply::Same, false));
        assert_eq!(w.reply("S2"), (WakeReply::Changed { from: "S1".into() }, false));
        assert_eq!(w.reply("S2"), (WakeReply::Same, false));
        assert_eq!(w.stamp, "S2");
        // an empty reply after seeding keeps the last stamp, so the next request still carries it
        assert_eq!(w.reply(""), (WakeReply::Empty, false));
        assert_eq!(w.stamp, "S2");
    }

    #[test]
    fn wake_backoff_is_5_15_60_and_stays_there_until_a_reply() {
        let mut w = WakeState::default();
        w.reply("S1");
        assert_eq!(w.failed(), (Duration::from_secs(5), true));
        assert_eq!(w.failed(), (Duration::from_secs(15), false));
        assert_eq!(w.failed(), (Duration::from_secs(60), false));
        assert_eq!(w.failed(), (Duration::from_secs(60), false));
        // the reply after an outage reports the recovery once and is still compared with the stamp from before it
        assert_eq!(w.reply("S2"), (WakeReply::Changed { from: "S1".into() }, true));
        assert_eq!(w.reply("S2"), (WakeReply::Same, false));
        assert_eq!(w.failed(), (Duration::from_secs(5), true));
    }

    #[test]
    fn wake_never_busy_loops() {
        assert_eq!(WakeState::pause_after(Duration::from_millis(300)), Duration::from_millis(9_700));
        assert_eq!(WakeState::pause_after(Duration::from_secs(45)), Duration::ZERO);
        assert!(WAKE_BACKOFF_S.iter().all(|s| *s >= 5));
        assert!(WAKE_HOLD_S + 13 < 60, "curl's max-time must stay under the relay function's 60 s cap");
        assert!(CHECK_EVERY_S <= 120, "the safety-net poll is the fallback when the relay is down");
    }

    #[test]
    fn wake_url_and_query() {
        assert_eq!(wake_url_of(None), WAKE_URL);
        assert_eq!(wake_url_of(Some(String::new())), "");
        assert_eq!(wake_url_of(Some(" http://127.0.0.1:4180/wake ".into())), "http://127.0.0.1:4180/wake");
        assert_eq!(wake_query("https://r/wake", "", ""), "https://r/wake");
        assert_eq!(wake_query("https://r/wake", "2026-10-05T11:02:17Z.5e7b56f5", ""), "https://r/wake?since=2026-10-05T11:02:17Z.5e7b56f5");
        assert_eq!(wake_query("https://r/api/wake?x=1", "S", ""), "https://r/api/wake?x=1&since=S");
        // the ping (MF-11): the machine, the version and the last job ride on every wake request
        assert_eq!(wake_query("https://r/wake", "", "machine=1ccfe586&v=0.1.21"), "https://r/wake?machine=1ccfe586&v=0.1.21");
        assert_eq!(wake_query("https://r/wake", "S", "machine=1ccfe586&v=0.1.21&job=update-now-0319"), "https://r/wake?since=S&machine=1ccfe586&v=0.1.21&job=update-now-0319");
        assert_eq!(ping_query("1ccfe586", "0.1.21", ""), "machine=1ccfe586&v=0.1.21");
        assert_eq!(ping_query("1ccfe586", "0.1.21", "update-now-0319-1ccfe586"), "machine=1ccfe586&v=0.1.21&job=update-now-0319-1ccfe586");
        assert_eq!(ping_query("", "0.1.21", "x"), "", "no machine, no ping");
        assert_eq!(ping_query("1ccfe586", "0.1.21", "a b&c=d"), "machine=1ccfe586&v=0.1.21&job=abcd", "only the safe characters travel");
    }
}

// ---- kind: shard-benchmark ----------------------------------------------------------------------------------------------

fn gpu_utilisation() -> Option<Vec<f64>> {
    let (code, out) = run_capture(Command::new(crate::platform::tool("nvidia-smi")).args(["--query-gpu=utilization.gpu", "--format=csv,noheader,nounits"]), Duration::from_secs(15));
    if code != Some(0) {
        return None;
    }
    let v: Vec<f64> = out.lines().filter_map(|l| l.trim().parse().ok()).collect();
    if v.is_empty() { None } else { Some(v) }
}

fn wait_gpu_idle(sink: &Sink, ctl: &Ctl) {
    let deadline = Instant::now() + Duration::from_secs(GPU_IDLE_WAIT_S);
    let mut idle_streak = 0;
    while Instant::now() < deadline && !ctl.aborted() {
        match gpu_utilisation() {
            None => {
                sink.line("nvidia-smi gave no utilisation reading; waiting 10 s and going on");
                std::thread::sleep(Duration::from_secs(10));
                return;
            }
            Some(v) => {
                let max = v.iter().cloned().fold(0.0, f64::max);
                if max < GPU_IDLE_PCT {
                    idle_streak += 1;
                    if idle_streak >= 2 {
                        sink.line(&format!("GPU idle: utilisation {max:.0}%"));
                        return;
                    }
                } else {
                    idle_streak = 0;
                    sink.line(&format!("GPU still busy: utilisation {max:.0}%"));
                }
            }
        }
        std::thread::sleep(Duration::from_secs(3));
    }
    sink.line("GPU did not go idle within the wait; going on anyway");
}

fn run_shard_benchmark(shared: &Arc<Shared>, job: &Job, sink: &Sink, data_root: &Path, jobs_url: &str, ctl: &Ctl, started: u64) -> Result<Done, String> {
    let distro = { let d = job.str_param("distro"); if d.is_empty() { DEFAULT_DISTRO.to_string() } else { d } };
    let user = { let u = job.str_param("wsl_user"); if !u.is_empty() { u } else { std::env::var("IGNEUM_APP_WSL_USER").ok().filter(|v| !v.is_empty()).unwrap_or_else(|| DEFAULT_WSL_USER.to_string()) } };
    let mut fixtures = job.list_param("fixtures");
    if fixtures.is_empty() {
        fixtures = DEFAULT_FIXTURES.iter().map(|s| s.to_string()).collect();
    }
    let zip_url = { let u = job.str_param("zip_url"); if u.is_empty() { jobs::jobs_url_from_manifest(jobs_url).replace(jobs::JOBS_FILE, "igneum-prove-wsl2.zip") } else { u } };
    if !zip_url.starts_with("https://") && !zip_url.starts_with("http://127.0.0.1:") {
        return Err("no https zip_url and no jobs URL to derive it from".into());
    }
    let base = data_root.join("prove");
    let pkg = base.join("igneum-prove-wsl2");
    let cap_min = job.timeout_minutes();
    let overall = Instant::now() + Duration::from_secs(cap_min * 60);

    sink.stage("waiting for the GPU to go idle");
    wait_gpu_idle(sink, ctl);
    if ctl.aborted() {
        return Err("aborted".into());
    }

    sink.stage("fetching the prove package");
    let zip = base.join("igneum-prove-wsl2.zip");
    fetch_file(&zip_url, &zip, &job.str_param("sha256"), job.u64_param("size"), sink)?;
    if pkg.exists() {
        sink.line(&format!("fresh: removing {}", pkg.display()));
        std::fs::remove_dir_all(&pkg).map_err(|e| format!("cannot remove the old package: {e}"))?;
    }
    extract(&zip, &base, sink)?;
    let script = pkg.join("prove-shard.sh");
    if !script.is_file() {
        return Err(format!("{} is missing after the extract", script.display()));
    }
    let shard = fixtures[0].clone();
    let blocks = fixtures[1..].join(" ");
    // what this run sees inside WSL: the account's own Ubuntu, so say who we are there
    let ctx = crate::wslhost::write_script("wsl-context", "echo \"wsl user $(id -un) uid $(id -u) home $HOME\"; nvidia-smi -L 2>&1 | head -1; ls -d \"$HOME/.sp1\" \"$HOME/igneum-prove\" 2>&1 | head -2").map_err(|e| format!("cannot write the WSL context script: {e}"))?;
    let (ccode, cout) = run_capture(&mut crate::wslhost::command(&crate::platform::tool("wsl"), &distro, Some(&user), &ctx.path, false, &[]), Duration::from_secs(60));
    sink.line(&format!("wsl context (-u {user}): exit {ccode:?}: {}", short_out(&cout)));
    // the payload ships the Linux host next to the app (wsl2\bin): no cargo, no toolchain, run it fixture by fixture;
    // params.build = true forces the package's prove-shard.sh (cargo build inside the distro) instead
    let shipped = std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.join("wsl2").join("bin").join("igneum-prove-host"))).filter(|p| p.is_file());
    let (ran, what) = match shipped {
        Some(host) if !job.bool_param("build") => {
            let host_wsl = jobs::to_wsl_path(&host.display().to_string()).ok_or("the shipped prover path has no drive letter")?;
            let fixdir = { let a = pkg.join("package").join("proving").join("fixtures"); if a.is_dir() { a } else { host.parent().and_then(|b| b.parent()).map(|w| w.join("fixtures")).unwrap_or(a) } };
            let fixdir_wsl = jobs::to_wsl_path(&fixdir.display().to_string()).ok_or("the fixtures path has no drive letter")?;
            let results = pkg.join("results");
            let _ = std::fs::create_dir_all(&results);
            let results_wsl = jobs::to_wsl_path(&results.display().to_string()).ok_or("the results path has no drive letter")?;
            sink.stage(&format!("proving with the shipped prover as {user}: {shard} (shard 0: execute, core, compressed), then {} (cap {cap_min} min)", if blocks.is_empty() { "nothing else".to_string() } else { format!("block mode on {blocks}") }));
            let mut last = Ran { code: Some(0), timed_out: false };
            for (i, f) in fixtures.iter().enumerate() {
                let left = overall.saturating_duration_since(Instant::now());
                if left < Duration::from_secs(60) {
                    sink.line(&format!("time cap reached before {f}"));
                    last = Ran { code: None, timed_out: true };
                    break;
                }
                let mode = if i == 0 { "shard --shard 0" } else { "block" };
                let line = format!("export PATH=\"/usr/local/cuda/bin:$PATH\"; CUDA_DIR=$(ls -d /usr/local/cuda-12.* 2>/dev/null | sort -V | tail -1); export LD_LIBRARY_PATH=\"/usr/lib/wsl/lib:${{CUDA_DIR:+$CUDA_DIR/lib64:}}${{LD_LIBRARY_PATH:-}}\"; echo \"=== GPU run: {f} --mode {mode} (SP1_PROVER=cuda) ===\"; SP1_PROVER=cuda RUST_LOG=info '{host_wsl}' '{fixdir_wsl}/{f}.json' --mode {mode} --out '{results_wsl}/{f}-cuda-{started}.json'; rc=$?; echo \"run exit $rc at $(date -u +%FT%TZ)\"; exit $rc");
                let run = crate::wslhost::write_script("gpu-run", &line).map_err(|e| format!("cannot write the WSL run script: {e}"))?;
                let mut cmd = crate::wslhost::command(&crate::platform::tool("wsl"), &distro, Some(&user), &run.path, false, &[]);
                cmd.current_dir(&pkg);
                let r = run_streamed(&mut cmd, sink, ctl, left, shared, job, started, "shard benchmark running")?;
                if r.code != Some(0) {
                    last = r;
                    break;
                }
            }
            (last, "shipped prover")
        }
        _ => {
            let wsl_script = jobs::to_wsl_path(&script.display().to_string()).ok_or("the package path has no drive letter; WSL cannot see it")?;
            sink.stage(&format!("proving with prove-shard.sh as {user} (cargo build inside {distro}): {shard} then {} (cap {cap_min} min)", if blocks.is_empty() { "nothing else".to_string() } else { blocks.clone() }));
            let mut cmd = Command::new(crate::platform::tool("wsl"));
            cmd.args(["-d", &distro, "-u", &user, "--", "bash", &wsl_script, &shard, &blocks]);
            cmd.current_dir(&pkg);
            let remaining = overall.saturating_duration_since(Instant::now()).max(Duration::from_secs(60));
            (run_streamed(&mut cmd, sink, ctl, remaining, shared, job, started, "shard benchmark running")?, "prove-shard.sh")
        }
    };
    if ran.code.is_none() {
        // the Linux side outlives wsl.exe: end the prover there too
        if let Ok(kill) = crate::wslhost::write_script("prover-kill", "pkill -f igneum-prove-host; pkill -f prove-shard.sh; true") {
            let _ = run_capture(&mut crate::wslhost::command(&crate::platform::tool("wsl"), &distro, Some(&user), &kill.path, false, &[]), Duration::from_secs(30));
        }
    }
    sink.stage("uploading the results");
    let mut uploaded = Vec::new();
    let results_dir = pkg.join("results");
    let mut files: Vec<PathBuf> = std::fs::read_dir(&results_dir).map(|rd| rd.flatten().map(|e| e.path()).filter(|p| p.extension().map(|x| x == "json").unwrap_or(false)).collect()).unwrap_or_default();
    files.sort();
    for f in &files {
        let ok = upload_file(shared, job, f, "result");
        sink.line(&format!("  {} {}", if ok { "uploaded" } else { "NOT uploaded" }, f.display()));
        if ok {
            uploaded.push(f.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default());
        }
    }
    let mut logs: Vec<PathBuf> = std::fs::read_dir(&pkg).map(|rd| rd.flatten().map(|e| e.path()).filter(|p| p.file_name().map(|n| n.to_string_lossy().starts_with("prove-shards-")).unwrap_or(false)).collect()).unwrap_or_default();
    logs.sort();
    if let Some(l) = logs.last() {
        let ok = upload_file(shared, job, l, "prove-log");
        sink.line(&format!("  {} {}", if ok { "uploaded" } else { "NOT uploaded" }, l.display()));
    }
    let results = sink.results.lock().unwrap().clone();
    let n_results = results.iter().filter(|r| r.starts_with("RESULT")).count();
    let mut done = finish_ran(ran, what)?;
    done.summary = format!("{}; {n_results} RESULT line{}, {} result file{} uploaded", done.summary, if n_results == 1 { "" } else { "s" }, uploaded.len(), if uploaded.len() == 1 { "" } else { "s" });
    done.extra = json!({ "fixtures": fixtures, "distro": distro, "wsl_user": user, "prover": what, "result_files": uploaded, "package": pkg.display().to_string() });
    Ok(done)
}

// ---- kind: build ---------------------------------------------------------------------------------------------------------

/// One stage inside the distro: the script written to the job folder (LF only), run as
/// `wsl -d <distro> -u <user> -- bash /mnt/c/.../stage-<name>.sh` under the stage's cap and the abort flag, every
/// line into the sink, STAGE lines with UTC times around it. A cap ends wsl.exe and then the Linux side.
#[allow(clippy::too_many_arguments)]
fn build_stage(shared: &Arc<Shared>, job: &Job, sink: &Sink, ctl: &Ctl, p: &jb::BuildParams, dir: &Path, stage: &str, script: &str, started: u64, overall: Instant) -> Result<Ran, String> {
    let path = dir.join(format!("stage-{stage}.sh"));
    std::fs::write(&path, script.replace("\r\n", "\n")).map_err(|e| format!("cannot write the {stage} script: {e}"))?;
    let wsl_path = jobs::to_wsl_path(&path.display().to_string()).ok_or("the job folder has no drive letter; WSL cannot see it")?;
    let left = overall.saturating_duration_since(Instant::now());
    if left < Duration::from_secs(30) {
        sink.line(&format!("STAGE {stage} skipped {}: the budget is used up", jobs::format_time(crate::platform::unix_now())));
        return Ok(Ran { code: None, timed_out: true });
    }
    let cap = jb::stage_cap(p, stage, left);
    sink.line(&format!("STAGE {stage} start {} cap {} min", jobs::format_time(crate::platform::unix_now()), cap.as_secs() / 60));
    let t0 = Instant::now();
    let mut cmd = Command::new(crate::platform::tool("wsl"));
    cmd.args(["-d", &p.distro, "-u", &p.wsl_user, "--", "bash", &wsl_path]);
    cmd.current_dir(dir);
    let ran = run_streamed(&mut cmd, sink, ctl, cap, shared, job, started, &format!("build: {stage}"))?;
    if ran.code.is_none() {
        if let Ok(kill) = crate::wslhost::write_script("build-kill", jb::kill_script()) {
            let _ = run_capture(&mut crate::wslhost::command(&crate::platform::tool("wsl"), &p.distro, Some(&p.wsl_user), &kill.path, false, &[]), Duration::from_secs(30));
        }
    }
    sink.line(&format!("STAGE {stage} {} {} {} s exit {}", if ran.timed_out { "timeout" } else { "end" }, jobs::format_time(crate::platform::unix_now()), t0.elapsed().as_secs(), ran.code.map(|c| c.to_string()).unwrap_or_else(|| "none".into())));
    Ok(ran)
}

/// One output to the relay: fn=upload for a client token (the upload key is allowed for upload and drop; round 4,
/// X23 keeps it away from run and task posts), the bytes PUT straight to Vercel Blob, then fn=drop with the Blob
/// URL so the file is an item the Mac finds by its title (jobbuild::upload_title). Returns (item id, blob url).
fn relay_upload(shared: &Arc<Shared>, p: &jb::BuildParams, path: &Path, title: &str, body: &str) -> Result<(u64, String), String> {
    let key = shared.packaged.upload_key.clone();
    if key.is_empty() {
        return Err("this build carries no upload key; nothing can be uploaded".into());
    }
    let size = std::fs::metadata(path).map(|m| m.len()).map_err(|e| e.to_string())?;
    if size > jb::MAX_UPLOAD_BYTES {
        return Err(format!("{size} bytes is over the relay's {} MB cap", jb::MAX_UPLOAD_BYTES / 1024 / 1024));
    }
    let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    let api = format!("{}/api/relay?fn=", p.relay_url);
    let key_header = format!("x-igneum-key: {key}");
    let post = |fn_name: &str, body: &str, limit: u64| -> Result<String, String> {
        let (code, out) = run_capture(Command::new(crate::platform::tool("curl")).args(["-sS", "--max-time", &limit.to_string(), "-X", "POST", &format!("{api}{fn_name}"), "-H", "Content-Type: application/json", "-H", &key_header, "--data-binary", body]), Duration::from_secs(limit + 10));
        if code != Some(0) {
            return Err(format!("relay {fn_name}: curl exit {code:?}: {}", short_out(&out)));
        }
        if out.contains("\"ok\":false") {
            return Err(format!("relay {fn_name}: {}", short_out(&out)));
        }
        Ok(out)
    };
    let t = post("upload", &json!({ "name": name, "size": size }).to_string(), 60)?;
    let token = jb::json_str(&t, "token").ok_or("relay upload: no token in the reply")?;
    let put_url = jb::json_str(&t, "put_url").ok_or("relay upload: no put_url in the reply")?;
    let api_version = jb::json_str(&t, "api_version").unwrap_or_else(|| "11".into());
    let (code, out) = run_capture(
        Command::new(crate::platform::tool("curl")).args(["-sS", "--max-time", "900", "-X", "PUT", &put_url, "-H", &format!("authorization: Bearer {token}"), "-H", &format!("x-api-version: {api_version}"), "-H", "x-add-random-suffix: 1", "-H", "x-content-type: application/octet-stream", "--data-binary", &format!("@{}", path.display())]),
        Duration::from_secs(910),
    );
    if code != Some(0) {
        return Err(format!("blob PUT: curl exit {code:?}: {}", short_out(&out)));
    }
    let url = jb::json_str(&out, "url").ok_or_else(|| format!("blob PUT: no url in the reply: {}", short_out(&out)))?;
    if !url.contains(".public.blob.vercel-storage.com/") {
        return Err(format!("blob PUT: unexpected url {}", short_out(&url)));
    }
    let from = format!("{}-{}", shared.runtime.host, shared.runtime.id8());
    let d = post("drop", &json!({ "from": from, "to": "mac", "kind": "file", "title": title, "body": body, "file_name": name, "file_url": url, "size": size }).to_string(), 60)?;
    let id = jb::json_u64(&d, "id").ok_or("relay drop: no id in the reply")?;
    Ok((id, url))
}

/// `build`: the plan is in src/jobbuild.rs. Here: the free-space check (the Windows drive and the distro), the
/// inputs zip (sha256 checked) and its manifest, then setup, extract, the targets, the tests, pack and upload,
/// each as a stage under its cap. The miners are never touched. Required failures mark the job failed after every
/// stage that can still run has run, so the binaries of a good target reach the Mac even when another failed.
fn run_build(shared: &Arc<Shared>, job: &Job, sink: &Sink, jobs_dir: &Path, ctl: &Ctl, started: u64) -> Result<Done, String> {
    if !cfg!(windows) {
        return Err("build runs on a Windows PC with WSL2 (the Linux build happens inside the distro)".into());
    }
    let p = jb::BuildParams::from_job(job);
    let dir = jobs_dir.join(&job.id);
    let _ = std::fs::create_dir_all(&dir);
    let overall = Instant::now() + Duration::from_secs(p.budget_min * 60);
    let now = || jobs::format_time(crate::platform::unix_now());
    let wsl = crate::platform::tool("wsl");
    sink.line(&format!("STAGE plan {} targets {} budget {} min, stage caps {:?}, free floor {} GB, tests {}, nice {}, distro {} as {}, relay {}", now(), p.targets.join("+"), p.budget_min, p.stage_min, p.min_free_gb, p.tests, p.nice, p.distro, p.wsl_user, p.relay_url));
    sink.line("the miners keep mining: this job stops nothing");
    let mut stages: serde_json::Map<String, Value> = serde_json::Map::new();
    let mut failures: Vec<String> = Vec::new();

    // ---- check: free space on the Windows drive (the vhdx grows into it) and inside the distro
    sink.stage("checking free space");
    let drive = dir.display().to_string().trim_start_matches("\\\\?\\").chars().next().filter(|c| c.is_ascii_alphabetic()).unwrap_or('C');
    let ps = format!("[math]::Floor(([IO.DriveInfo]::new('{drive}')).AvailableFreeSpace/1GB)");
    let (wc, wo) = run_capture(Command::new(crate::platform::tool("powershell")).args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &ps]), Duration::from_secs(40));
    let win_free = if wc == Some(0) { jb::parse_free_gb(&wo) } else { None };
    let free = crate::wslhost::write_script("free-gb", jb::free_gb_script()).map_err(|e| format!("cannot write the WSL free-space script: {e}"))?;
    let (lc, lo) = run_capture(&mut crate::wslhost::command(&wsl, &p.distro, Some(&p.wsl_user), &free.path, false, &[]), Duration::from_secs(120));
    let wsl_free = if lc == Some(0) { jb::parse_free_gb(&lo) } else { None };
    sink.line(&format!("RESULT check {} drive {drive}: {} GB free, {} /root: {} GB free, floor {} GB", now(), win_free.map(|g| g.to_string()).unwrap_or_else(|| format!("unknown (powershell exit {wc:?}: {})", short_out(&wo))), p.distro, wsl_free.map(|g| g.to_string()).unwrap_or_else(|| format!("unknown (wsl exit {lc:?}: {})", short_out(&lo))), p.min_free_gb));
    let Some(wf) = win_free else { return Err("cannot read the free space of the Windows drive".into()) };
    let Some(lf) = wsl_free else { return Err(format!("{} does not answer the free-space probe; is WSL installed and the distro present?", p.distro)) };
    if wf < p.min_free_gb || lf < p.min_free_gb {
        return Err(format!("not enough free space: drive {drive} {wf} GB, distro {lf} GB, floor {} GB", p.min_free_gb));
    }
    if ctl.aborted() {
        return Err("aborted".into());
    }

    // ---- fetch: the inputs zip and its manifest
    sink.stage("fetching the build inputs");
    let t0 = Instant::now();
    let zip = dir.join("build-inputs.zip");
    let size = fetch_file(&job.str_param("zip_url"), &zip, &job.str_param("sha256"), job.u64_param("size"), sink)?;
    let member = format!("{}/manifest.json", jb::ZIP_ROOT);
    let _ = std::fs::remove_dir_all(dir.join(jb::ZIP_ROOT));
    let (tc, to) = run_capture(Command::new(crate::platform::tool("tar")).args(["-xf", &zip.display().to_string(), "-C", &dir.display().to_string(), &member]), Duration::from_secs(120));
    if tc != Some(0) {
        return Err(format!("cannot read {member} out of the zip: tar exit {tc:?}: {}", short_out(&to)));
    }
    let manifest_text = std::fs::read_to_string(dir.join(jb::ZIP_ROOT).join("manifest.json")).map_err(|e| format!("manifest.json: {e}"))?;
    let m = jb::parse_manifest(&manifest_text)?;
    sink.line(&format!("RESULT fetch {} {size} bytes sha256 ok, node {} {}{}, app {}, {} build unit{}, {} test unit{}, {} s", now(), m.node_branch, m.node_commit, if m.node_dirty { " (dirty worktree)" } else { "" }, m.app_version, m.builds.len(), if m.builds.len() == 1 { "" } else { "s" }, m.tests.len(), if m.tests.len() == 1 { "" } else { "s" }, t0.elapsed().as_secs()));
    stages.insert("fetch".into(), json!({ "exit": 0, "secs": t0.elapsed().as_secs() }));

    // ---- the stages inside the distro
    let record = |name: &str, ran: &Ran, required: bool, stages: &mut serde_json::Map<String, Value>, failures: &mut Vec<String>| {
        stages.insert(name.into(), json!({ "exit": ran.code, "timeout": ran.timed_out }));
        let ok = ran.code == Some(0);
        if !ok && required {
            failures.push(format!("{name} {}", if ran.timed_out { "hit its cap".to_string() } else { format!("exit {}", ran.code.map(|c| c.to_string()).unwrap_or_else(|| "none".into())) }));
        }
        ok
    };
    sink.stage("setup inside the distro");
    let ran = build_stage(shared, job, sink, ctl, &p, &dir, "setup", &jb::setup_script(&p, &job.id), started, overall)?;
    if !record("setup", &ran, true, &mut stages, &mut failures) {
        return Err(format!("setup failed: {}", failures.join("; ")));
    }
    sink.stage("extracting the sources");
    let zip_wsl = jobs::to_wsl_path(&zip.display().to_string()).ok_or("the zip path has no drive letter")?;
    let ran = build_stage(shared, job, sink, ctl, &p, &dir, "extract", &jb::extract_script(&p, &job.id, &zip_wsl), started, overall)?;
    if !record("extract", &ran, true, &mut stages, &mut failures) {
        return Err(format!("extract failed: {}", failures.join("; ")));
    }
    let mut built_any = false;
    for target in ["linux", "windows"] {
        if !p.wants(target) || ctl.aborted() {
            continue;
        }
        sink.stage(&format!("building for {target}"));
        let ran = build_stage(shared, job, sink, ctl, &p, &dir, target, &jb::build_script(&p, &job.id, &m, target), started, overall)?;
        if record(target, &ran, true, &mut stages, &mut failures) {
            built_any = true;
        }
    }
    if p.tests && !m.tests.is_empty() && !ctl.aborted() {
        sink.stage("running the tests");
        let ran = build_stage(shared, job, sink, ctl, &p, &dir, "test", &jb::test_script(&p, &job.id, &m), started, overall)?;
        record("test", &ran, true, &mut stages, &mut failures);
    }
    if ctl.aborted() {
        return Err("aborted".into());
    }

    // ---- pack and upload what was built
    let out_dir = dir.join("out");
    let _ = std::fs::remove_dir_all(&out_dir);
    let _ = std::fs::create_dir_all(&out_dir);
    let mut outputs: Vec<Value> = Vec::new();
    let mut uploaded = 0usize;
    let mut uploaded_bytes = 0u64;
    if built_any {
        sink.stage("packing the binaries");
        let out_wsl = jobs::to_wsl_path(&out_dir.display().to_string()).ok_or("the out folder has no drive letter")?;
        let ran = build_stage(shared, job, sink, ctl, &p, &dir, "pack", &jb::pack_script(&p, &job.id, &m, &out_wsl), started, overall)?;
        if record("pack", &ran, true, &mut stages, &mut failures) {
            sink.stage("uploading to the relay");
            let t0 = Instant::now();
            let listed = std::fs::read_to_string(out_dir.join(jb::OUTPUTS_FILE)).map_err(|e| format!("{}: {e}", jb::OUTPUTS_FILE))?;
            let files = jb::parse_outputs(&listed)?;
            let cap = jb::stage_cap(&p, "upload", overall.saturating_duration_since(Instant::now()).max(Duration::from_secs(120)));
            let deadline = Instant::now() + cap;
            let mut all_ok = true;
            for o in &files {
                if ctl.aborted() {
                    return Err("aborted".into());
                }
                let path = out_dir.join(&o.zst);
                let mut entry = json!({ "name": o.name, "target": o.target, "bytes": o.bytes, "sha256": o.sha256, "zst": o.zst, "zst_bytes": o.zst_bytes, "zst_sha256": o.zst_sha256 });
                if Instant::now() >= deadline {
                    sink.line(&format!("RESULT upload {} {} skipped: the upload cap is used up", now(), o.zst));
                    all_ok = false;
                } else {
                    let body = format!("job {}\nnode {} {}\napp {}\n{} {} {} bytes sha256 {}\n{} {} bytes sha256 {}\n", job.id, m.node_branch, m.node_commit, m.app_version, o.target, o.name, o.bytes, o.sha256, o.zst, o.zst_bytes, o.zst_sha256);
                    match relay_upload(shared, &p, &path, &jb::upload_title(&job.id, &o.zst), &body) {
                        Ok((id, url)) => {
                            uploaded += 1;
                            uploaded_bytes += o.zst_bytes;
                            sink.line(&format!("RESULT upload {} {} relay item {id} {} bytes sha256 {} (unpacked {} {} bytes sha256 {})", now(), o.zst, o.zst_bytes, o.zst_sha256, o.name, o.bytes, o.sha256));
                            entry["relay_id"] = json!(id);
                            entry["url"] = json!(url);
                        }
                        Err(e) => {
                            sink.line(&format!("RESULT upload {} {} FAILED: {e}", now(), o.zst));
                            all_ok = false;
                        }
                    }
                }
                outputs.push(entry);
            }
            let list_path = out_dir.join(jb::OUTPUTS_FILE);
            match relay_upload(shared, &p, &list_path, &jb::upload_title(&job.id, jb::OUTPUTS_FILE), &format!("job {}\nnode {} {}\napp {}\n{} files\n", job.id, m.node_branch, m.node_commit, m.app_version, files.len())) {
                Ok((id, _)) => sink.line(&format!("RESULT upload {} {} relay item {id}", now(), jb::OUTPUTS_FILE)),
                Err(e) => {
                    sink.line(&format!("RESULT upload {} {} FAILED: {e}", now(), jb::OUTPUTS_FILE));
                    all_ok = false;
                }
            }
            stages.insert("upload".into(), json!({ "exit": if all_ok { 0 } else { 1 }, "secs": t0.elapsed().as_secs(), "files": uploaded }));
            if !all_ok {
                failures.push("upload incomplete".into());
            }
        }
    } else {
        sink.line(&format!("RESULT pack {} skipped: no target built", now()));
    }
    let _ = std::fs::remove_file(&zip);

    let mins = (crate::platform::unix_now().saturating_sub(started)) / 60;
    let summary = format!("node {} {} app {}: {}{}; {uploaded} file{} uploaded ({} MB); {mins} min of {}", m.node_branch, m.node_commit, m.app_version, if failures.is_empty() { "every stage ok".to_string() } else { format!("FAILED: {}", failures.join(", ")) }, if built_any { "" } else { "; nothing built" }, if uploaded == 1 { "" } else { "s" }, uploaded_bytes / 1024 / 1024, p.budget_min);
    sink.line(&format!("RESULT build {} {} {summary}", now(), if failures.is_empty() { "done" } else { "failed" }));
    sink.line("the miners were never stopped by this job");
    let extra = json!({ "node_branch": m.node_branch, "node_commit": m.node_commit, "node_dirty": m.node_dirty, "app_version": m.app_version, "targets": p.targets, "stages": stages, "outputs": outputs, "uploaded_files": uploaded, "relay_url": p.relay_url, "failures": failures });
    let status = if failures.is_empty() { "done" } else if failures.iter().any(|f| f.contains("hit its cap")) { "timeout" } else { "failed" };
    Ok(Done { status: status.into(), exit: if failures.is_empty() { 0 } else { 1 }, summary, extra })
}

// ---- kind: restart app -----------------------------------------------------------------------------------------------

/// A detached helper that starts the app again a few seconds after this engine has gone.
pub fn spawn_relaunch_helper(shared: &Arc<Shared>) -> Result<(), String> {
    let mut c;
    #[cfg(target_os = "macos")]
    {
        let b = crate::platform::bundle_path().ok_or("not running from Ember.app")?;
        c = Command::new(crate::platform::tool("nohup"));
        c.args(["bash", "-c", &format!("sleep 8; open -n '{}'", b.display().to_string().replace('\'', "'\\''"))]);
    }
    #[cfg(windows)]
    {
        let dir = std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.to_path_buf())).ok_or("cannot find the install folder")?;
        let exe = dir.join("igneum-app.exe");
        // console: igneum-app.exe is a windows-subsystem program in release builds (main.rs), it never gets a console; SW_HIDE would hide the window host it opens
        let ps = format!("Start-Sleep 8; Start-Process -FilePath '{}' -ArgumentList '--launch' -WorkingDirectory '{}'", exe.display().to_string().replace('\'', "''"), dir.display().to_string().replace('\'', "''"));
        c = Command::new(crate::platform::tool("powershell"));
        c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-Command", &ps]);
    }
    #[cfg(not(any(target_os = "macos", windows)))]
    {
        let exe = std::env::current_exe().map_err(|e| e.to_string())?;
        c = Command::new(crate::platform::tool("nohup"));
        c.args(["bash", "-c", &format!("sleep 8; '{}' --no-open &", exe.display())]);
    }
    c.stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null());
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        c.process_group(0);
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000); // CREATE_NO_WINDOW only, never DETACHED_PROCESS (powershell exits without a console)
    }
    shared.log("job: relaunch helper started; the app quits and opens again in about 10 s");
    c.spawn().map(|_| ()).map_err(|e| e.to_string())
}

static ACCOUNT: std::sync::OnceLock<String> = std::sync::OnceLock::new();

/// Who the engine runs as: Windows "user=<domain\\name> sid=<S-1-...> elevated=<True|False> console=<the signed-in
/// user>" from one PowerShell call (cached), "user=<name>" elsewhere. WSL distros belong to the Windows account, so
/// this decides which Ubuntu a job sees (the second Windows machine, 4 October 2026).
fn account_context() -> String {
    ACCOUNT
        .get_or_init(|| {
            #[cfg(windows)]
            {
                let ps = "$i = [Security.Principal.WindowsIdentity]::GetCurrent(); $p = New-Object Security.Principal.WindowsPrincipal($i); $e = $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator); $c = ''; try { $c = (Get-CimInstance Win32_ComputerSystem).UserName } catch { }; Write-Output ('user=' + $i.Name + ' sid=' + $i.User.Value + ' elevated=' + $e + ' console=' + $c)";
                let (code, out) = run_capture(Command::new(crate::platform::tool("powershell")).args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps]), Duration::from_secs(40));
                let t = short_out(&out);
                if code == Some(0) && t.contains("user=") { t } else { format!("user={} (powershell exit {code:?}: {t})", std::env::var("USERNAME").unwrap_or_default()) }
            }
            #[cfg(not(windows))]
            {
                format!("user={}", std::env::var("USER").unwrap_or_default())
            }
        })
        .clone()
}

/// The dashboard note when the app's account is elevated or not the signed-in user's; empty otherwise.
fn account_warning(ctx: &str) -> String {
    let get = |k: &str| ctx.split_whitespace().find_map(|p| p.strip_prefix(&format!("{k}="))).unwrap_or("").to_string();
    let user = get("user");
    let console = get("console");
    let elevated = get("elevated").eq_ignore_ascii_case("true");
    let other = !console.is_empty() && !user.is_empty() && !user.eq_ignore_ascii_case(&console);
    if !elevated && !other {
        return String::new();
    }
    let mut s = format!("The app runs as {user}{}.", if elevated { " (elevated)" } else { "" });
    if other {
        s.push_str(&format!(" The signed-in user is {console}; WSL and its tools belong to that account."));
    } else {
        s.push_str(" WSL tools set up without elevation belong to the signed-in user.");
    }
    s.push_str(" Jobs run WSL as root inside the Ubuntu this account sees.");
    s
}

fn short(s: &str, n: usize) -> String {
    if s.chars().count() <= n { s.to_string() } else { format!("{}...", s.chars().take(n).collect::<String>()) }
}

#[cfg(test)]
mod hold_tests {
    use super::hold_release;

    /// MF-6 (the first Windows machine, 7 October 2026): a --stop-miners job's hold outlived it into a read-only watch job for three minutes.
    #[test]
    fn a_hold_belongs_to_the_job_that_took_it() {
        // the owner is still running, under its cap: the hold stays
        assert_eq!(hold_release(Some("job-a"), Some("job-a"), 30.0, 3600.0), None);
        // another job runs now: released at once, whatever that job is
        assert_eq!(hold_release(Some("job-a"), Some("job-b"), 30.0, 3600.0), Some("the job that took the hold is no longer running"));
        // no job runs: released
        assert_eq!(hold_release(Some("job-a"), None, 30.0, 3600.0), Some("the job that took the hold is no longer running"));
        // the owner's own cap passed: released and logged
        assert_eq!(hold_release(Some("job-a"), Some("job-a"), 3601.0, 3600.0), Some("the hold passed the job's own cap"));
        // a hold with no owner (an older engine state) never sticks
        assert_eq!(hold_release(None, Some("job-b"), 1.0, 3600.0), Some("no job owns the hold"));
    }
}

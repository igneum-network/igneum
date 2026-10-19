//! The miner's supervisor (design section 1): holds the network and the settings, launches one worker per
//! device on protocol v0, partitions nonces, verifies every share on the CPU, runs the tuner, computes the
//! economics display, applies the schedule, and keeps the first-run journal. It never holds a key and never
//! elevates.

pub mod devfee;
pub mod devices;
pub mod economics;
pub mod journal;
pub mod modes;
pub mod net;
pub mod pools;
pub mod schedule;
pub mod settings;
pub mod state;
pub mod tuner;
pub mod verify;
pub mod bond;
pub mod minerkey;
pub mod pressure;
pub mod work;
pub mod worker;
pub mod gllibs;
pub mod router;
pub mod registry;

use chrono::Timelike;
use devices::{refusal_sentence, support_label, Device, Horizon, Labelled};
use economics::{Estimate, Inputs, Line};
use igneum_worker_proto::{self as proto, stubhash, Cancel, Configure, Envelope, FromWorker, Job, Knob, Params, Prepare, Set, Telemetry, ToWorker, Transition as WorkerTransition, WorkerState};
use modes::{Measurement, Safety};
use schedule::{decide, Decision, FullscreenProbe, Moment, NoProbe, PauseCause};
use settings::{Settings, WorkSourceSetting};
use state::{EnergyView, MiningState, Notice, SlotState, SlotView, Snapshot, SourceView, Stage, Totals, Transition, TransitionRecord, TuneView};
use std::collections::{BTreeMap, BTreeSet, HashMap, VecDeque};
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering as AtomicOrdering};
use std::path::{Path, PathBuf};
use std::sync::mpsc::{sync_channel, Receiver, SyncSender, TrySendError};
use std::sync::{Arc, Mutex, MutexGuard};
use std::thread;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use tuner::{Bench, StoredTune, TuneConfig, TuneStore};
use verify::Verifier;
use work::{JobTemplate, Share, SubmitOutcome, WorkSource};
use worker::{Inbound, Launcher, WorkerError, WorkerProcess};

pub const HEARTBEAT_TIMEOUT: Duration = Duration::from_secs(10);
pub const STAT_MS: u64 = 1000;
/// F-E8 (13 October 2026): a chunk is a sixteenth of a pool session's lane, never the whole lane, so a worker that
/// idles before a chunk's end is given the next sixteenth rather than the lane again (the re-found shares behind F-E5)
/// and the card does not wait a block out after one chunk.
pub const NONCE_CHUNK: u64 = 1 << 28;
/// Blocks before an epoch boundary at which the next dataset's build is asked for (the GPU team: as soon as the next
/// epoch's first height is known; the build runs in slices beside mining and the earlier it starts the earlier it
/// ends; the lookahead of next_boundary is 256).
pub const PREPARE_LOOKAHEAD_BLOCKS: u64 = 32;
/// Messages taken from one worker, and verified results taken, in one tick.
const DRAIN_PER_TICK: usize = 2048;
/// Results waiting for a verifier thread before new ones are dropped unverified.
const VERIFY_QUEUE: usize = 512;

/// A result queued for the verifier threads.
struct VerifyReq {
    slot: usize,
    /// The verifier for the document the job was issued under (a document change swaps it; results in flight
    /// are checked against the one they were issued under and then cancelled by their digest).
    verifier: Arc<dyn Verifier>,
    height: u64,
    header: [u8; 32],
    mix: Vec<u8>,
    hash: [u8; 32],
    share: proto::Share,
    queued: Instant,
    /// The bond's secret under a keyed document: the check runs the prover's path and yields the shares.
    sk: Option<Arc<bond::BondSecret>>,
    /// The verify-queue cell (0.2.24): a result under the job's share target, worth submitting; checked before
    /// measurement-only results and never dropped by the queue's time bound.
    share_class: bool,
    /// 0.2.11: a result the submission pace holds back, checked all the same (the no-result watch sees it; a wrong
    /// result is counted invalid) and never submitted.
    held: bool,
}

struct VerifyRes {
    slot: usize,
    share: proto::Share,
    valid: bool,
    /// The S shares of a valid result under a keyed document (what the submission carries beside the hash).
    shares: Option<Vec<u8>>,
    /// Queued to checked, ms.
    latency_ms: u64,
    held: bool,
}
pub const MAX_LOG_LINES: usize = 5000;

#[derive(Debug, thiserror::Error)]
pub enum SupError {
    #[error("{0}")]
    Settings(#[from] settings::SettingsError),
    #[error("{0}")]
    Worker(#[from] worker::WorkerError),
    #[error("{0}")]
    Message(String),
}

impl From<String> for SupError {
    fn from(s: String) -> Self {
        SupError::Message(s)
    }
}

#[derive(Debug, Clone)]
pub struct Config {
    pub app_name: String,
    pub version: String,
    pub data_dir: PathBuf,
    pub launchers: Vec<Launcher>,
    /// Workers bound to one coordinate by their file name (`igneum-worker-<backend>-coord<N>`, 0.2.11: the exact VERSION 17
    /// texture worker beside the versions 8 to 16 worker): the base launcher's stem, the coordinate, the launcher.
    pub coord_launchers: Vec<(String, u8, Launcher)>,
    /// The workers bound to a document version (igneum-worker-<base>-v<N>): base stem, version, launcher.
    pub version_launchers: Vec<(String, u64, Launcher)>,
    pub horizon: Horizon,
    pub tune: TuneConfig,
    /// Where the miner's work-authority key lives (generated on first use, never printed); None turns the
    /// signed-share path off.
    pub key_path: Option<PathBuf>,
    /// The bonded identity's secret for a keyed document (the hostile-key rule): held in memory only, taken at
    /// start from the wallet or the operator (bond.rs), carried to a worker inside the job and never anywhere else.
    /// None: an unkeyed document mines as before; a keyed one refuses to start.
    pub bond_secret: Option<Arc<bond::BondSecret>>,
    /// A portable machine: the host allowance default is 10 W instead of 40 W (PA-05).
    pub portable: bool,
    /// Where workers were looked for, in order, with what each directory held: the sentence when none is found
    /// (the second Windows machine, 11 October 2026: "no worker installed" named no directory).
    pub searched: Vec<(PathBuf, Vec<String>)>,
    /// The dev fee (devfee.rs, ED-05, the founder's word of 11 October 2026: on at 1 percent by default, shown,
    /// switchable off): the resolved fee, or why it is off. Set by the host from `devfee::resolve` against the
    /// workers' directory and the operator's `--dev-fee`.
    pub dev_fee: Result<devfee::DevFee, devfee::Off>,
}

impl Config {
    pub fn new(app_name: &str, version: &str, resources_dir: &Path, data_dir: PathBuf) -> Config {
        let mut searched: Vec<(PathBuf, Vec<String>)> = Vec::new();
        let list = |d: &Path| -> Vec<String> { std::fs::read_dir(d).map(|rd| rd.flatten().filter_map(|e| e.file_name().to_str().map(String::from)).collect()).unwrap_or_default() };
        let mut launchers = worker::discover(resources_dir, &data_dir.join("workers"));
        searched.push((resources_dir.to_path_buf(), list(resources_dir)));
        if launchers.is_empty() {
            // the directory beside the executable: on Windows the exe and its workers travel together
            if let Some(beside) = std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.to_path_buf())) {
                if beside != resources_dir {
                    launchers = worker::discover(&beside, &data_dir.join("workers"));
                    searched.push((beside.clone(), list(&beside)));
                }
            }
        }
        Config { app_name: app_name.to_string(), version: version.to_string(), data_dir: data_dir.clone(), launchers, coord_launchers: Vec::new(), version_launchers: Vec::new(), horizon: Horizon::default(), tune: TuneConfig::default(), key_path: Some(data_dir.join("miner.key")), bond_secret: None, portable: false, searched, dev_fee: Err(devfee::Off::NoSignedAddress("the host has not resolved the fee".into())) }
    }
    /// The dev fee resolved against the workers' directory (the signed manifest beside them) and the operator's
    /// percent (None: the manifest's default; Some(0): off). Fail closed: no verified address, no fee.
    pub fn with_dev_fee(mut self, operator_percent: Option<u8>) -> Config {
        let dir = self.launchers.first().and_then(|l| l.exe.parent().map(|p| p.to_path_buf())).unwrap_or_else(|| self.data_dir.clone());
        self.dev_fee = devfee::resolve(&dir, operator_percent);
        self
    }
    pub fn settings_path(&self) -> PathBuf {
        self.data_dir.join("settings.json")
    }
    pub fn tune_path(&self) -> PathBuf {
        self.data_dir.join("tune.json")
    }
    pub fn journal_path(&self) -> PathBuf {
        self.data_dir.join("first-run-journal.jsonl")
    }
}

fn now_unix() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

/// A sliding mean of a quantity per second over a window.
struct RateMeter {
    samples: VecDeque<(Instant, f64)>,
    window: Duration,
    first: Option<Instant>,
}

impl RateMeter {
    fn new(window: Duration) -> Self {
        RateMeter { samples: VecDeque::new(), window, first: None }
    }
    fn add(&mut self, v: f64) {
        let now = Instant::now();
        if self.first.is_none() {
            self.first = Some(now);
        }
        self.samples.push_back((now, v));
        self.trim(now);
    }
    fn trim(&mut self, now: Instant) {
        while let Some((t, _)) = self.samples.front() {
            if now.duration_since(*t) > self.window {
                self.samples.pop_front();
            } else {
                break;
            }
        }
    }
    /// Seconds since the meter's first sample (the stint meter: since the first report of the stint); 0 before one.
    fn age(&self) -> f64 {
        self.first.map(|t| Instant::now().duration_since(t).as_secs_f64()).unwrap_or(0.0)
    }
    fn rate(&mut self) -> f64 {
        let now = Instant::now();
        self.trim(now);
        let Some(first) = self.first else { return 0.0 };
        let span = now.duration_since(first).min(self.window).as_secs_f64().max(1.0);
        self.samples.iter().map(|(_, v)| v).sum::<f64>() / span
    }
    /// The rate over the last `d` only (counts a second), for a window shorter than the meter's: the parity rows
    /// read three consecutive minutes from the state line without a pipe tee (11 October 2026).
    fn rate_over(&mut self, d: Duration) -> f64 {
        let now = Instant::now();
        self.trim(now);
        let Some(first) = self.first else { return 0.0 };
        let span = now.duration_since(first).min(d).as_secs_f64().max(1.0);
        self.samples.iter().filter(|(t, _)| now.duration_since(*t) <= d).map(|(_, v)| v).sum::<f64>() / span
    }
    /// The mean of the samples over the last `d` only (readings).
    fn mean_over(&mut self, d: Duration) -> Option<f64> {
        let now = Instant::now();
        self.trim(now);
        let v: Vec<f64> = self.samples.iter().filter(|(t, _)| now.duration_since(*t) <= d).map(|(_, v)| *v).collect();
        if v.is_empty() {
            return None;
        }
        Some(v.iter().sum::<f64>() / v.len() as f64)
    }
    /// The mean of the samples in the window (for readings, not counts).
    fn mean(&mut self) -> Option<f64> {
        self.trim(Instant::now());
        if self.samples.is_empty() {
            return None;
        }
        Some(self.samples.iter().map(|(_, v)| v).sum::<f64>() / self.samples.len() as f64)
    }
    /// The window starts over (F-696: a slot whose worker changed reads the new worker's stats alone).
    fn reset(&mut self) {
        self.samples.clear();
        self.first = None;
    }
}

/// The bound on one reference check of a document (its cache build and one light hash); a reference that does
/// not return inside it is a fault named in the state, never a silent miner.
pub const SELFCHECK_BOUND: Duration = Duration::from_secs(90);

/// One bounded hash through the reference for the document the verifier was built on: the self-check every
/// template's document passes before the first share (F-22). Returns the check's time, or why it failed.
fn reference_selfcheck(v: Arc<dyn Verifier>, height: u64) -> Result<u64, String> {
    let (tx, rx) = std::sync::mpsc::channel::<Result<u64, String>>();
    let t0 = Instant::now();
    let _ = thread::Builder::new().name("verify-selfcheck".into()).spawn(move || {
        let header = [0x5au8; 32];
        let mix = [0u8; 32];
        let hash = [0u8; 32];
        // a wrong result is the expected answer; what is checked is that the reference answers at all, and how fast
        // (a keyed document answers on its keyed path, under a throwaway secret)
        let answer = if v.keyed_rounds().is_some() { v.verify_keyed(height, &header, 1, &mix, &hash, &[0x5au8; 32]).map(|s| s.is_some()) } else { v.verify(height, &header, 1, &mix, &hash) };
        let r = match answer {
            Ok(_) => Ok(t0.elapsed().as_millis() as u64),
            Err(e) => Err(format!("the reference cannot check this document: {e}")),
        };
        let _ = tx.send(r);
    });
    match rx.recv_timeout(SELFCHECK_BOUND) {
        Ok(r) => r,
        Err(_) => Err(format!("the reference check for this document did not return within {} s", SELFCHECK_BOUND.as_secs())),
    }
}

/// The measurement difficulty's ceiling: doubling on a flood must never reach infinity (F-25: the tick thread died
/// converting an infinite interval to a Duration).
pub const MAX_MEASURE_DIFFICULTY: f64 = 1.0e15;

/// Seconds to a Duration, guarded: a non-finite or negative value is clamped (a day at most) and reported by the
/// caller, never converted raw (F-25).
pub fn secs_guarded(x: f64) -> (Duration, bool) {
    if x.is_finite() && x >= 0.0 {
        (Duration::from_secs_f64(x.min(86_400.0)), x <= 86_400.0)
    } else {
        (Duration::from_secs(86_400), false)
    }
}

/// Energy per validated hash from a rate (hashes a second) and a mean power (watts), with the grade named.
fn energy_view(rate_hps: f64, power_w: f64, grade: &str) -> Option<EnergyView> {
    if rate_hps <= 0.0 || power_w <= 0.0 {
        return None;
    }
    let j = power_w / rate_hps;
    Some(EnergyView { joules_per_hash: j, mh_per_wh: rate_hps * 3600.0 / power_w / 1e6, power_w, grade: grade.into() })
}

/// What a tune window collects for one candidate.
struct TuneAcc {
    difficulty: f64,
    collecting: bool,
    valid: u64,
    invalid: u32,
    hangs: u32,
    power_sum: f64,
    power_n: u32,
    temp_max: Option<f64>,
    fan_max: Option<f64>,
    throttle_hits: u32,
    telem_n: u32,
    pressure_worst: Option<String>,
    window_started: Option<Instant>,
    current: Params,
}

impl TuneAcc {
    fn new() -> Self {
        TuneAcc { difficulty: 10_000.0, collecting: false, valid: 0, invalid: 0, hangs: 0, power_sum: 0.0, power_n: 0, temp_max: None, fan_max: None, throttle_hits: 0, telem_n: 0, pressure_worst: None, window_started: None, current: Params::new() }
    }
    fn reset(&mut self) {
        self.valid = 0;
        self.invalid = 0;
        self.hangs = 0;
        self.power_sum = 0.0;
        self.power_n = 0;
        self.temp_max = None;
        self.fan_max = None;
        self.throttle_hits = 0;
        self.telem_n = 0;
        self.pressure_worst = None;
        self.window_started = None;
    }
    fn note_telemetry(&mut self, t: &Telemetry) {
        if !self.collecting {
            return;
        }
        self.telem_n += 1;
        if let Some(p) = t.power_w {
            self.power_sum += p;
            self.power_n += 1;
        }
        if let Some(c) = t.temp_c {
            self.temp_max = Some(self.temp_max.map_or(c, |m| m.max(c)));
        }
        if let Some(f) = t.fan_pct {
            self.fan_max = Some(self.fan_max.map_or(f, |m| m.max(f)));
        }
        if t.throttle.iter().any(|f| f == "thermal" || f == "power") {
            self.throttle_hits += 1;
        }
        if let Some(p) = &t.thermal_pressure {
            let rank = |s: &str| match s {
                "critical" => 3,
                "serious" => 2,
                "fair" => 1,
                _ => 0,
            };
            if self.pressure_worst.as_deref().map_or(true, |w| rank(p) > rank(w)) {
                self.pressure_worst = Some(p.clone());
            }
        }
    }
    fn measurement(&self, seconds: f64) -> Measurement {
        Measurement {
            rate_hps: self.valid as f64 * self.difficulty / seconds.max(1.0),
            power_w: if self.power_n > 0 { Some(self.power_sum / self.power_n as f64) } else { None },
            temp_max_c: self.temp_max,
            fan_max_pct: self.fan_max,
            throttle_frac: if self.telem_n > 0 { self.throttle_hits as f64 / self.telem_n as f64 } else { 0.0 },
            thermal_pressure_worst: self.pressure_worst.clone(),
            invalid: self.invalid,
            hangs: self.hangs,
            seconds,
        }
    }
}

/// The target a worker is given for a job, from the measurement target and the source's share target (big-endian
/// 32-byte targets; a larger target is easier). Off the share lane, the easier of the two: results under the
/// measurement target feed the validated rate, those under the source's are submitted. On the share lane, the
/// pacing rule (the project's ruling of 11 October 2026, after the 4070 row on devnet7 at D 54k, where the share target
/// was a hash in 340 and the card met it tens of thousands of times a second): the job target is the measurement
/// target and never easier, so the worker reports about one result a second per device and keeps hashing at its
/// rate; every result verified in time and under the share target is submitted (the 20 a second cap kept), the
/// rest is never counted as work. At launch difficulty the share target is the harder of the two and the rule
/// never binds: every share is under the measurement target and is reported as before.
pub fn job_target_for(measure: &[u8; 32], share_target: &[u8; 32], share_lane: bool) -> [u8; 32] {
    job_target_under(measure, share_target, share_lane, false)
}

/// The dev fee's lines in the totals (devfee.rs): the words, the percent and the address are the configuration's
/// and survive every session reset (`Totals::default()` at a start); the counts are the session's.
pub fn fee_lines_into(t: &mut Totals, fee: &Result<devfee::DevFee, devfee::Off>) {
    t.dev_fee_words = devfee::words(fee);
    if let Ok(f) = fee {
        t.dev_fee_percent = f.percent;
        t.dev_fee_address = f.address_hex.clone();
    } else {
        t.dev_fee_percent = 0;
        t.dev_fee_address.clear();
    }
}

/// The lab form of the pacing rule (the project's order of 11 October 2026, the devnet8 stop-gap while PA-53's declared
/// target step waits for devnet9): with `chain_target` on, a share-lane job's target is the CHAIN's share target
/// alone, no per-card measurement target, so every result under the chain's target is submitted and the node's cap
/// (256 a height per address) bounds the flood; the share counts are then proportional to the cards' rates. Off,
/// the pacing rule above. Set by the environment variable EMBER_PACE_CHAIN_TARGET=1 (the CLI's --pace-chain-target);
/// the devnet8 rows only, never a release default.
pub fn job_target_under(measure: &[u8; 32], share_target: &[u8; 32], share_lane: bool, chain_target: bool) -> [u8; 32] {
    if share_lane {
        return if chain_target { *share_target } else { *measure };
    }
    if measure > share_target { *measure } else { *share_target }
}

/// A pool session's results a second per device: the pool sets one share target for every card today (no vardiff),
/// so a fast card's results came by the thousand a second (an RTX 4090, 12 October 2026: 81,864 held back by the
/// pace in 139 s, the tick at 15 s, the validated rate reading zero because nothing reached the check). The worker's
/// job target on a pool session is the measurement target eased to this many results a second (the measurement
/// difficulty is tuned to one a second), under the submission pace of 12 a second; every result under it feeds the
/// validated rate, those under the pool's share target are submitted.
pub const POOL_RESULTS_PER_S: f64 = 10.0;

/// The measurement difficulty for a pool session's job target: the per-device difficulty tuned to one result a
/// second, eased to POOL_RESULTS_PER_S of them, never under one hash.
/// A notice clears the moment its cause clears (the project's rule of 19:2x UK, 15 October 2026, from the mini's paused capture:
/// "No device could start." standing while the card had mined 45 shares since it; a notice that outlives its cause reads
/// as a broken app). The bad notices are re-read against the cards on every tick, not only when another notice with the
/// same words arrives: the whole-engine starting notices go the moment any card mines; a card's own notice (its name
/// leads the text: "<name>: ...", "<name> stopped ...", "<name> cannot mine ...") goes the moment that card mines.
/// Warnings and the notices with their own clearing rule (the source, the reference check, memory pressure) stand.
/// A card closed on a configure refusal is reopened by a fresh pool session (the project's rule of 20:0x UK, 15 October 2026,
/// from the public pool's 7322 frame of 19:52 on its 7320 sessions: every 0.2.16 card closed on "the reference refused
/// the params document" and stayed closed across the pool's roll and its reconnect, each session holding jobs and
/// hashing nothing). The refusal is the session document's fault, not the card's: when the source is dialled again
/// and answers, every enabled card closed that way is reopened on the new session's document. A card closed for its
/// own reason (hangs, malformed output, a fault that was not a configure refusal) stays closed.
/// Document versions mined only by a worker bound to them (igneum-worker-<base>-v<N>): the raster lane's version 18 (GPU
/// B's Metal raster engine, the GPU team's GL protocol worker). the two-card Windows host's read of 0.2.18 (21:47 to 22:03 UK, 15 October 2026): the
/// Windows install carries no GL worker, the router's probe sent both cards to the raster coordinate and the base CUDA
/// worker took its version 18 document, computing for fifteen minutes with no result; the base workers never take one.
pub const BOUND_ONLY_VERSIONS: &[u64] = &[18];
/// The versions a BASE worker of a given form never takes beyond the list above: none in this version. 0.2.19 made
/// version 21 bound-only on Metal after the mini's read of 0.2.18 (the base worker at 0.06 MH/s on the Anvil lane with no
/// share in 38 minutes); the GPU team's read of record (gb-f189__apple-m6-mini__shares-1, 16 October 2026) shows the Metal
/// worker e5193b3d RUNS version 21 (the Metal engine's forge2 path, exact on the M6) and finds shares on it (63 in 120 s
/// at 2^-16, 32 kH/s), so the refusal was Ember's own gate against a worker that runs the lane; under the minimum rule a
/// key with no work on a lane is paid nothing, so the gate is lifted (0.2.20): every base worker takes version 21, the
/// Metal form slowly until the GPU team's compiled Metal programme cut; the card's rate reads from the worker's stat lines.
/// The lane's name for a document version, the words a miner reads (the project's read of the 0.2.19 sheets, 16 October
/// 2026: lanes by their names, a worker named only in the Logs): 15 the bandwidth lane, 21 the programme lane, 18 the
/// raster lane; another version reads as its lane number.
pub fn lane_word(version: Option<u64>, coord: Option<u8>) -> String {
    match (version, coord) {
        (Some(15), _) => "the bandwidth lane".to_string(),
        (Some(21), _) => "the programme lane".to_string(),
        (Some(18), _) => "the raster lane".to_string(),
        (_, Some(c)) => format!("lane {c}"),
        _ => String::new(),
    }
}
/// A card's figures on one lane this session (the Cards page's lane rows).
#[derive(Debug, Clone, Default)]
struct LaneStat { accepted: u64, hps: f64, seconds: f64, /// F-469: the accepted shares' weights and count over the current stint on the lane (reset at the move onto it)
    stint_work: f64, stint_shares: u64 }
pub fn bound_only_for_base(_base_stem: &str, version: u64) -> bool {
    BOUND_ONLY_VERSIONS.contains(&version)
}
/// A coordinate whose document version is bound-only is refused for a card when no worker bound to that version is in
/// the install (the Linux GL-libs rule's twin: the card mines the other lanes, the router moves it on, never a fatal
/// close and never the base worker on the document).
pub fn coordinate_needs_absent_bound_worker(version: Option<u64>, bound_present: bool) -> bool {
    coordinate_needs_absent_bound_worker_for("", version, bound_present)
}
pub fn coordinate_needs_absent_bound_worker_for(base_stem: &str, version: Option<u64>, bound_present: bool) -> bool {
    matches!(version, Some(v) if bound_only_for_base(base_stem, v)) && !bound_present
}

pub fn reopens_on_fresh_session(state: &SlotState, document_fault: bool, enabled: bool) -> bool {
    enabled && document_fault && matches!(state, SlotState::Faulted)
}

pub fn notices_still_standing(notices: &[Notice], mining_cards: &[String]) -> Vec<Notice> {
    if mining_cards.is_empty() {
        return notices.to_vec();
    }
    const WHOLE_ENGINE: &[&str] = &[
        "No device could start.",
        "No worker is installed with this app.",
        "The pinned coordinate is not served.",
        "The pinned coordinate's document is refused by the worker.",
        "The network's mining parameters cannot be matched by this miner.",
    ];
    notices
        .iter()
        .filter(|n| {
            if n.level != "bad" {
                return true;
            }
            if WHOLE_ENGINE.contains(&n.text.as_str()) {
                return false;
            }
            !mining_cards.iter().any(|name| {
                n.text.starts_with(&format!("{name}:")) || n.text.starts_with(&format!("{name} stopped")) || n.text.starts_with(&format!("{name} cannot mine"))
            })
        })
        .cloned()
        .collect()
}

pub fn pool_measure_difficulty(measure_difficulty: f64) -> f64 {
    (measure_difficulty / POOL_RESULTS_PER_S).max(1.0)
}

/// The block boundary a worker is given with its job: on a pool session the job target itself, because a block
/// through a pool is the pool's and the pool's job names its easy share target as the boundary, so a worker that
/// reports every result under either reported past the eased measurement target by the hundred thousand (an RTX
/// 5090 on the public pool, 12 October 2026: 234,554 not submitted in 160 s, the validated figure 15.7 against the
/// card's 39.6); on a node session the chain's boundary as before (a block is found under it and submitted at once).
pub fn job_boundary_for(pool_session: bool, boundary: &[u8; 32], job_target: &[u8; 32]) -> [u8; 32] {
    if pool_session { *job_target } else { *boundary }
}

/// The word for the pacing in force on the share lane (the minute line and the record name it).
pub fn share_pacing_word(chain_target: bool) -> &'static str {
    if chain_target { "chain-target (lab: the job target is the chain's share target, no measurement target, every result under it submitted)" } else { "measurement (the job target is the measurement target, every verified share submitted)" }
}

/// EMBER_PACE_CHAIN_TARGET read once: "1", "true", "yes" or "on" turns the lab pacing on.
pub fn pace_chain_target_from_env() -> bool {
    matches!(std::env::var("EMBER_PACE_CHAIN_TARGET").ok().as_deref().map(|v| v.trim().to_ascii_lowercase()).as_deref(), Some("1") | Some("true") | Some("yes") | Some("on"))
}

/// The cards a job names, bound by fingerprint alone (the founder's word of 11 October 2026: the 5080's job on
/// the first Windows machine and the 4070's row 08 on the second Windows machine both ran on that PC's 5090 after the enclosure moves, because the job named
/// no card and the lane's index mask pointed elsewhere). Every named fingerprint must be among the cards the
/// workers see, or the start is refused with the fingerprint and the cards seen; an index is refused as a name
/// (the fingerprint is the one the parity rows read, `ember devices` prints it); nothing falls through to
/// device 0. Returns the chosen fingerprints in the inventory's order.
pub fn devices_named(named: &[String], inventory: &[SlotView]) -> Result<Vec<String>, String> {
    let seen: Vec<(String, String, u32)> = inventory.iter().map(|d| (d.fingerprint.clone(), d.name.clone(), d.index)).collect();
    devices_named_among(named, &seen)
}

/// `devices_named` over (fingerprint, name, index) triples: the cards the workers see.
pub fn devices_named_among(named: &[String], seen: &[(String, String, u32)]) -> Result<Vec<String>, String> {
    let list = || if seen.is_empty() { "none".to_string() } else { seen.iter().map(|(f, n, i)| format!("{f} {n} (index {i})")).collect::<Vec<_>>().join("; ") };
    let mut chosen: Vec<String> = Vec::new();
    for n in named {
        let n = n.trim();
        if n.is_empty() {
            continue;
        }
        if n.chars().all(|c| c.is_ascii_digit()) {
            return Err(format!("--device {n}: name the card by its fingerprint, not an index (ember devices prints it); the cards seen: {}", list()));
        }
        match seen.iter().find(|(f, _, _)| f == n) {
            Some((f, _, _)) => {
                if !chosen.contains(f) {
                    chosen.push(f.clone());
                }
            }
            None => return Err(format!("--device {n}: no card with this fingerprint is among the cards the workers see, so nothing starts; the cards seen: {}", list())),
        }
    }
    if chosen.is_empty() {
        return Err("--device named no card".into());
    }
    Ok(seen.iter().filter(|(f, _, _)| chosen.contains(f)).map(|(f, _, _)| f.clone()).collect())
}

/// How a worker is reached. Every worker today is a child of the supervisor over its pipes (`LocalPipes`); the
/// forms to come are named so the rule below holds for them before they exist: a worker on this machine over
/// loopback, and a worker on another machine. Security's rule (SECURITY-GAPS.md, the project's order of 11 October 2026):
/// a keyed job carries the bond's secret, and the secret never leaves this machine over a plain socket, so a keyed
/// job goes only to a worker whose transport is local; the share lane and the open seat (unkeyed) are unaffected.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Transport {
    LocalPipes,
    Loopback(String),
    Remote(String),
}

impl Transport {
    pub fn is_local(&self) -> bool {
        matches!(self, Transport::LocalPipes | Transport::Loopback(_))
    }
    pub fn words(&self) -> String {
        match self {
            Transport::LocalPipes => "this machine, over pipes".into(),
            Transport::Loopback(a) => format!("this machine, over loopback {a}"),
            Transport::Remote(a) => format!("another machine, over {a}"),
        }
    }
}

/// The reason a worker is left off under a keyed document, or None when it can take the keyed job: no keyed-job
/// extension in its hello, or a transport that is not this machine's (the secret would cross a plain socket).
pub fn keyed_refusal(keyed_job: bool, transport: &Transport) -> Option<String> {
    if !keyed_job {
        return Some("cannot take the bonded rounds (no keyed-job extension in its hello); off under this document".into());
    }
    if !transport.is_local() {
        return Some(format!("keyed sealing runs only on this machine's cards: this worker is on {}; off under this document until a keyed transport exists", transport.words()));
    }
    None
}

/// Whether a job carries the bond's secret: only under a keyed document, to a worker that declared the extension
/// and is reached locally. Never otherwise, whatever the document says.
pub fn job_takes_the_secret(keyed_document: bool, keyed_job: bool, transport: &Transport) -> bool {
    keyed_document && keyed_refusal(keyed_job, transport).is_none()
}

#[derive(Clone)]
struct Sent {
    template: JobTemplate,
    /// The candidate id the job was issued for; a share is counted only against the same id.
    algo: String,
    /// The parameter document digest and the epoch the job was issued under (the tag of section 9).
    params_digest: String,
    epoch: u64,
    /// The measurement difficulty of the share target the worker was given.
    difficulty: f64,
    tuning: bool,
    slot: usize,
}

/// The ordinal a worker's `--device` takes for a hello device: the number after the backend in its id ("cuda:1" gives 1),
/// else its position in the hello (F-E41).
/// A coordinate-bound worker by its file name: `igneum-worker-<backend>-coord<N>` (0.2.11) gives (the base stem
/// `igneum-worker-<backend>`, N); any other name is None.
/// F-E48: the launcher a pinned coordinate takes from the first launch: the worker bound to that coordinate beside the
/// device's base worker (same base stem), with the base launcher's environment and working directory; None when
/// nothing is pinned or the coordinate has no bound worker (the base worker serves it).
pub fn pin_bound_launcher(coord_launchers: &[(String, u8, Launcher)], pin: Option<u8>, base: &Launcher, device_name: &str) -> Option<Launcher> {
    pin_bound_launcher_v(coord_launchers, &[], pin, None, base, device_name)
}

/// `pin_bound_launcher` with the version-bound table and the pinned coordinate's document version when its context
/// is already read (the raster lane: coordinate 17's version 18 document binds igneum-worker-gl-v18 from the first launch).
pub fn pin_bound_launcher_v(coord_launchers: &[(String, u8, Launcher)], version_launchers: &[(String, u64, Launcher)], pin: Option<u8>, version: Option<u64>, base: &Launcher, device_name: &str) -> Option<Launcher> {
    let p = pin?;
    let base_stem = base.exe.file_stem().map(|x| x.to_string_lossy().to_string()).unwrap_or_default();
    let bound = bound_launcher_for_version(coord_launchers, version_launchers, p, version, &base_stem)?;
    let mut l = bound.clone();
    l.env = base.env.clone();
    l.workdir = base.workdir.clone();
    gl_device_name_arg(&mut l, device_name);
    Some(l)
}

/// The GL raster worker opens an EGL device by name (the GPU team's binaries from fd71bd953 take `--device-name <GL renderer
/// substring>`; the card's name as the CUDA or OpenCL base reports it, "NVIDIA GeForce RTX 5090", is a substring of the
/// renderer string; the first binary b0b932b7 ignores the flag and opens the first display), so a many-card host's
/// slots do not all open the first card.
pub fn gl_device_name_arg(l: &mut Launcher, device_name: &str) {
    let stem = l.exe.file_stem().map(|x| x.to_string_lossy().to_string()).unwrap_or_default();
    if stem.starts_with(GL_BASE) && !device_name.trim().is_empty() && !l.args.iter().any(|a| a == "--device-name") {
        l.args.push("--device-name".into());
        l.args.push(device_name.trim().to_string());
    }
}

/// The base worker name of the one raster worker for every card that is not Apple's (the founder's order of 15 October
/// 2026: a public miner mines all three lanes; the GPU team's GL text under the GPU team's protocol mode runs on NVIDIA, AMD and
/// Intel alike, so igneum-worker-gl-coord6 binds coordinate 6 beside a CUDA or an OpenCL base; the Metal raster
/// worker binds it beside the Metal base by the exact rule).
pub const GL_BASE: &str = "igneum-worker-gl";
/// The worker bound to coordinate `c` for a card whose base worker is `base_stem`: the exact base's bound worker
/// first (igneum-worker-cuda-coord5 beside igneum-worker-cuda), else the GL one for any base that is not Metal or CPU.
pub fn bound_launcher_for(coord_launchers: &[(String, u8, Launcher)], c: u8, base_stem: &str) -> Option<Launcher> {
    bound_launcher_for_version(coord_launchers, &[], c, None, base_stem)
}

/// The worker bound to coordinate `c` (whose registered document is `version`) for a card whose base worker is
/// `base_stem`: the coordinate-bound worker of the exact base first, then the version-bound one of the exact base,
/// then, for any base that is not Metal or CPU, the GL one of either form.
pub fn bound_launcher_for_version(coord_launchers: &[(String, u8, Launcher)], version_launchers: &[(String, u64, Launcher)], c: u8, version: Option<u64>, base_stem: &str) -> Option<Launcher> {
    if let Some((_, _, l)) = coord_launchers.iter().find(|(b, cc, _)| *cc == c && b == base_stem) {
        return Some(l.clone());
    }
    if let Some(v) = version {
        if let Some((_, _, l)) = version_launchers.iter().find(|(b, vv, _)| *vv == v && b == base_stem) {
            return Some(l.clone());
        }
    }
    if base_stem == "igneum-worker-metal" || base_stem == "igneum-worker-cpu" || base_stem.is_empty() {
        return None;
    }
    if let Some((_, _, l)) = coord_launchers.iter().find(|(b, cc, _)| *cc == c && b == GL_BASE) {
        return Some(l.clone());
    }
    version.and_then(|v| version_launchers.iter().find(|(b, vv, _)| *vv == v && b == GL_BASE).map(|(_, _, l)| l.clone()))
}

#[cfg(test)]
mod bound_for_tests {
    use super::*;
    #[test]
    fn f383_a_launch_is_finished_only_into_the_slot_that_holds_it_and_a_late_tick_counts_no_hang() {
        assert!(launch_arrival(Some(7), 7), "the slot's own launch is finished");
        assert!(!launch_arrival(Some(8), 7), "a re-opened slot's older launch is ended");
        assert!(!launch_arrival(None, 7), "a closed slot's launch is ended");
        assert!(!tick_ran_late(Duration::from_millis(900)), "a tick that ran counts hangs");
        assert!(tick_ran_late(Duration::from_secs(47)), "a frozen tick counts no hang");
    }

    /// F-470: a programme build on the anchor set is given the build's allowance, the ten-second rule otherwise.
    #[test]
    fn a_programme_build_on_the_anchor_set_is_not_a_hang() {
        assert_eq!(hang_silence_bound(true), worker::HELLO_BOUND, "the build's allowance while the worker rebuilds on the set");
        assert_eq!(hang_silence_bound(false), HEARTBEAT_TIMEOUT, "the ten-second rule once the worker mines again");
        assert!(Duration::from_secs(47) > hang_silence_bound(false) && Duration::from_secs(47) < hang_silence_bound(true), "47 s of silence: a hang while mining, a build after the set");
    }

    /// F-564: a share for a still-valid job of the slot's coordinate is not cancelled.
    #[test]
    fn a_share_for_a_job_of_the_live_height_or_one_behind_is_still_valid_work() {
        use super::superseded_job;
        assert!(!superseded_job(Some(5), 8100, Some(5), 8100), "the live height: valid");
        assert!(!superseded_job(Some(5), 8099, Some(5), 8100), "one height behind: still valid, the pool answers in the open");
        assert!(superseded_job(Some(5), 8098, Some(5), 8100), "two behind: superseded");
        assert!(superseded_job(Some(5), 8100, Some(6), 8100), "another coordinate than the slot mines: superseded");
        assert!(!superseded_job(None, 42, None, 42), "the session's one-form job at its height: valid");
        assert!(superseded_job(None, 40, None, 42));
    }

    /// F-562: the silence bound and the rule.
    #[test]
    fn a_hashing_worker_with_no_result_past_the_lanes_silence_bound_is_restarted() {
        use super::{result_silence_bound, silence_clock, silent_while_hashing, SILENCE_FLOOR_S};
        assert_eq!(result_silence_bound(None).as_secs(), SILENCE_FLOOR_S, "no interval known: the floor");
        assert_eq!(result_silence_bound(Some(10.0)).as_secs(), SILENCE_FLOOR_S, "a fast lane never under the floor");
        assert_eq!(result_silence_bound(Some(450.0)).as_secs(), 2700, "the Mac's raster lane (one share in 450 s): six intervals, no cap");
        assert_eq!(result_silence_bound(Some(1000.0)).as_secs(), 6000, "a hard lane on a slow card: six intervals still");
        assert_eq!(result_silence_bound(Some(120.0)).as_secs(), 720, "six expected intervals");
        assert_eq!(result_silence_bound(Some(f64::NAN)).as_secs(), SILENCE_FLOOR_S);
        // the clock: the stint's start, then the last result; a silent restart's own time gives the next window whole
        let t0 = Instant::now(); let t1 = t0 + Duration::from_secs(100); let before = t0 - Duration::from_secs(100);
        assert_eq!(silence_clock(t0, None), t0);
        assert_eq!(silence_clock(t0, Some(before)), t0, "a result before this stint does not count");
        assert_eq!(silence_clock(t0, Some(t1)), t1, "the last result, or the silent restart's own time, starts the window");
        let b = Duration::from_secs(1800);
        assert!(silent_while_hashing(true, 890.0, Duration::from_secs(1801), b), "hashing, nothing found past the bound: restarted");
        assert!(!silent_while_hashing(true, 890.0, Duration::from_secs(1700), b), "inside the bound");
        assert!(!silent_while_hashing(true, 0.0, Duration::from_secs(5000), b), "a worker reporting no work is the hang rule's, not this one's");
        assert!(!silent_while_hashing(false, 890.0, Duration::from_secs(5000), b), "a slot not mining is not judged");
    }

    /// F-479: a slot still opening past its build's allowance with no stat is restarted.
    #[test]
    fn a_slot_opening_past_the_allowance_with_no_stat_is_restarted() {
        let long = Duration::from_secs(601); let short = Duration::from_secs(30);
        assert!(opening_past_allowance(true, true, long), "opening, the allowance over, ten minutes without a stat: restarted");
        assert!(!opening_past_allowance(true, false, long), "the allowance still stands: the build's time");
        assert!(!opening_past_allowance(true, true, short), "a stat came inside the bound");
        assert!(!opening_past_allowance(false, true, long), "a mining slot is the hang rule's, not this one's");
    }

    /// F-470's worker half: a "building" stat is the allowance's open on the wire, read as preparation on the slot.
    #[test]
    fn a_building_stat_opens_the_build_allowance() {
        let j = serde_json::json!({"t": "stat", "job_id": "j", "hashes": "0", "elapsed_ms": 0, "state": "building", "building": "the period's programme on the set", "telemetry": {}});
        let st: proto::Stat = serde_json::from_value(j).expect("a building stat parses");
        assert_eq!(st.state, WorkerState::Building);
        assert_eq!(st.building.as_deref(), Some("the period's programme on the set"));
        let m: proto::Stat = serde_json::from_value(serde_json::json!({"t": "stat", "job_id": "j", "hashes": "5", "elapsed_ms": 1000, "state": "mining", "telemetry": {}})).expect("a mining stat parses");
        assert_eq!(m.state, WorkerState::Mining);
        assert!(m.building.is_none());
    }

    /// F-476: a worker is never configured on an empty document.
    #[test]
    fn a_worker_waits_for_the_pools_first_document() {
        assert!(configure_waits_for_document(false, false), "no coordinate document and a session document without a version: the worker waits");
        assert!(!configure_waits_for_document(true, false), "a coordinate document registered: configured on it");
        assert!(!configure_waits_for_document(false, true), "a session document naming its version (solo, the lab): configured on it");
        assert!(!configure_waits_for_document(true, true));
    }
    #[test]
    fn f384_a_same_pool_redial_keeps_the_lanes_and_a_quiet_lane_pauses_once() {
        assert!(same_session(Some("pool.example:3342"), "pool.example:3342"), "the same pool: the lanes kept");
        assert!(!same_session(Some("pool.example:3342"), "other.example:3342"), "another pool: a clean start");
        assert!(!same_session(None, "pool.example:3342"), "no old session: a clean start");
        assert!(!same_session(Some(""), ""), "an empty name is no session");
        assert!(lane_pauses(false), "a quiet lane is paused");
        assert!(!lane_pauses(true), "a paused lane is not paused again");
    }
    #[test]
    fn f437_a_card_with_no_work_in_five_minutes_at_fresh_jobs_is_moved_and_a_hashing_card_without_a_share_stays() {
        assert!(lane_dead(300, true, false), "five minutes without an attempt at a live lane: the card moves on");
        assert!(!lane_dead(299, true, false), "under five minutes: the settle holds");
        assert!(!lane_dead(900, false, false), "a lane without fresh jobs is the pool's quiet, not the card's");
        assert!(!lane_dead(900, true, true), "a probe has its own clock");
        // F-439: a hashing card's clock is its last stat with hashes, so a card at 880 attempts a second with its shares
        // minutes apart reads 0 s since work at every tick and is never moved by the settle
        let since_work_of_a_hashing_card = 0u64;
        assert!(!lane_dead(since_work_of_a_hashing_card, true, false), "hashing, no share: the card waits on the pool's step");
    }
    #[test]
    fn f301b_a_waiting_card_reopens_while_the_engine_is_still_starting() {
        assert!(reopen_allowed(&MiningState::Starting), "the lone card's reopen before any card mines");
        assert!(reopen_allowed(&MiningState::Mining), "the reopen while mining");
        assert!(!reopen_allowed(&MiningState::Paused), "no reopen while paused");
        assert!(!reopen_allowed(&MiningState::Idle), "no reopen while idle");
    }
    #[test]
    fn f392_the_step_watch_names_a_raised_step_and_an_opening_mismatch_and_nothing_else() {
        let m = Duration::from_secs(60);
        assert!(step_mismatch(10_000, 2_400_000, 20 * m, 6 * m, true, true).is_some(), "five minutes of too-low-target results with no share is the raised step");
        assert!(step_mismatch(2_000_000, 2_400_000, 20 * m, 6 * m, true, false).is_none(), "no refused result and a fair declared rate: the card may simply be slow at its step");
        assert!(step_mismatch(10_000, 2_400_000, 3 * m, 3 * m, true, false).is_some(), "a tenth of the reported rate declared at the dial and no share since: the opening mismatch");
        assert!(step_mismatch(10_000, 2_400_000, 3 * m, m, false, false).is_none(), "a share since the dial: the pool's retarget is at work");
        assert!(step_mismatch(2_000_000, 2_400_000, 3 * m, 3 * m, true, false).is_none(), "the declared rate near the reported one: no mismatch");
        assert!(step_mismatch(0, 0, 20 * m, 20 * m, true, true).is_none(), "no reported rate: nothing to judge");
    }
    #[test]
    fn f437_a_pinned_card_is_never_moved_off_its_pin_by_the_bounded_settle() {
        assert!(settle_exempt(Some(5), 5), "the pinned coordinate: exempt");
        assert!(!settle_exempt(Some(5), 7), "another coordinate under a pin: on the clock");
        assert!(!settle_exempt(None, 5), "no pin: on the clock");
    }
    fn l(p: &str) -> Launcher { Launcher { exe: std::path::PathBuf::from(p), expected_sha256: None, workdir: std::path::PathBuf::from("/w"), env: vec![], args: vec![] } }
    #[test]
    fn the_gl_raster_worker_binds_coordinate_6_beside_any_base_but_metal_and_cpu() {
        let table = vec![
            ("igneum-worker-cuda".to_string(), 5u8, l("/w/igneum-worker-cuda-coord5")),
            ("igneum-worker-gl".to_string(), 6u8, l("/w/igneum-worker-gl-coord6")),
            ("igneum-worker-metal".to_string(), 6u8, l("/w/igneum-worker-metal-coord6")),
        ];
        let stem = |o: Option<Launcher>| o.map(|x| x.exe.file_name().unwrap().to_string_lossy().to_string());
        assert_eq!(stem(bound_launcher_for(&table, 5, "igneum-worker-cuda")), Some("igneum-worker-cuda-coord5".into()));
        assert_eq!(stem(bound_launcher_for(&table, 5, "igneum-worker-opencl")), None);
        assert_eq!(stem(bound_launcher_for(&table, 6, "igneum-worker-cuda")), Some("igneum-worker-gl-coord6".into()));
        assert_eq!(stem(bound_launcher_for(&table, 6, "igneum-worker-opencl")), Some("igneum-worker-gl-coord6".into()));
        assert_eq!(stem(bound_launcher_for(&table, 6, "igneum-worker-metal")), Some("igneum-worker-metal-coord6".into()));
        assert_eq!(stem(bound_launcher_for(&table, 6, "igneum-worker-cpu")), None);
        assert_eq!(stem(bound_launcher_for(&table, 7, "igneum-worker-cuda")), None);
        // the version-bound form: igneum-worker-gl-v18 serves coordinate 17 (the raster lane after 6 retired) and 6 alike, by the document's version
        let vt = vec![("igneum-worker-gl".to_string(), 18u64, l("/w/igneum-worker-gl-v18")), ("igneum-worker-metal".to_string(), 18u64, l("/w/igneum-worker-metal-v18"))];
        assert_eq!(stem(bound_launcher_for_version(&[], &vt, 17, Some(18), "igneum-worker-cuda")), Some("igneum-worker-gl-v18".into()));
        assert_eq!(stem(bound_launcher_for_version(&[], &vt, 6, Some(18), "igneum-worker-opencl")), Some("igneum-worker-gl-v18".into()));
        assert_eq!(stem(bound_launcher_for_version(&[], &vt, 17, Some(18), "igneum-worker-metal")), Some("igneum-worker-metal-v18".into()));
        assert_eq!(stem(bound_launcher_for_version(&[], &vt, 17, Some(21), "igneum-worker-cuda")), None);
        assert_eq!(stem(bound_launcher_for_version(&[], &vt, 17, None, "igneum-worker-cuda")), None);
        // the coordinate form wins over the version form for the same base
        assert_eq!(stem(bound_launcher_for_version(&table, &vt, 6, Some(18), "igneum-worker-cuda")), Some("igneum-worker-gl-coord6".into()));
        assert_eq!(version_bound_stem(std::path::Path::new("/w/igneum-worker-gl-v18")), Some(("igneum-worker-gl".to_string(), 18)));
        assert_eq!(version_bound_stem(std::path::Path::new("/w/igneum-worker-metal-v18.exe")), Some(("igneum-worker-metal".to_string(), 18)));
        assert_eq!(version_bound_stem(std::path::Path::new("/w/igneum-worker-cuda-coord5")), None);
        assert_eq!(version_bound_stem(std::path::Path::new("/w/igneum-worker-cuda")), None);
        // the pin takes the same rule, the base's env carried
        let mut base = l("/w/igneum-worker-cuda"); base.env.push(("CUDA_VISIBLE_DEVICES".into(), "1".into()));
        let p = pin_bound_launcher(&table, Some(6), &base, "NVIDIA GeForce RTX 5090").unwrap();
        assert_eq!(p.exe.file_name().unwrap().to_string_lossy(), "igneum-worker-gl-coord6");
        assert_eq!(p.env, base.env);
        assert_eq!(p.args, vec!["--device-name".to_string(), "NVIDIA GeForce RTX 5090".to_string()]);
        // the CUDA-bound worker takes no device name; an empty name adds nothing
        assert!(pin_bound_launcher(&table, Some(5), &base, "NVIDIA GeForce RTX 5090").unwrap().args.is_empty());
        assert!(pin_bound_launcher(&table, Some(6), &base, " ").unwrap().args.is_empty());
    }

    /// F-471: the launch gate's rule (half the cores and at least one in flight; the rest queued).
    #[test]
    fn launch_gate_rules() {
        assert_eq!(launch_cap_for(2), 1, "the second Linux proof host: one launch at a time on two cores");
        assert_eq!(launch_cap_for(1), 1);
        assert_eq!(launch_cap_for(0), 1);
        assert_eq!(launch_cap_for(8), 4);
        assert_eq!(launch_cap_for(32), 16);
        assert!(launch_may_spawn(0, 1), "the first launch always goes");
        assert!(!launch_may_spawn(1, 1), "the second waits behind the first on a two-core host");
        assert!(launch_may_spawn(3, 4));
        assert!(!launch_may_spawn(4, 4));
        assert!(launch_may_spawn(0, 0), "a cap of zero never blocks the first launch");
    }

    /// F-475: the gate is a queue in arrival order; a newcomer never jumps it.
    #[test]
    fn the_launch_gate_is_a_queue_in_arrival_order() {
        assert!(launch_spawns_now(0, 1, 0), "nothing queued, room: the newcomer goes");
        assert!(!launch_spawns_now(0, 1, 1), "a queued launch goes before the newcomer even with room");
        assert!(!launch_spawns_now(1, 1, 0), "no room: the newcomer queues");
        // the queue holds the AMD cards (slots 6 and 7, arrived at generations 7 and 8) and a re-launch of slot 0 at 20
        assert_eq!(next_queued_launch(&[(0, 20), (6, 7), (7, 8)]), Some(6), "the earliest arrival goes, never the lowest slot");
        assert_eq!(next_queued_launch(&[(0, 20), (7, 8)]), Some(7));
        assert_eq!(next_queued_launch(&[]), None);
    }
}

/// F-E46 and F-E49: the refusal's words, the served set printed or its absence named.
/// F-E62: how long after the session's first coordinate template the pin waits for its own coordinate's template before
/// it is judged against the served set (a pool sends one coordinate's template at a time, seconds apart).
pub const PIN_GRACE_S: u64 = 60;

pub fn pin_refusal_words(pin: u8, served: &[u8]) -> String {
    if served.is_empty() {
        format!("coordinate {pin} is pinned but this source serves no coordinates; nothing mines until the pin is cleared or the source serves it")
    } else {
        let set: Vec<String> = served.iter().map(|c| c.to_string()).collect();
        format!("coordinate {pin} is pinned but the source serves coordinates {} alone; nothing mines until the pin names a served coordinate", set.join(", "))
    }
}

/// Whether a period anchor change on a coordinate must RECONFIGURE a worker (its configure document carries the
/// anchor; the raster worker reads it from the document alone and answers the r0d_anchor knob with "unknown knob"),
/// or the knob set suffices (the CUDA and Metal workers list r0d_anchor in their hello). The raster document's
/// anchor rotates every period_blocks (300 on the research chain: anchorHeight 5098, 5398, 5698, 5998, 15 October
/// 2026); the GPU team's driver that reconfigured on the digest alone lost every share from 5698 ("invalid mix digest").
pub fn anchor_change_needs_reconfigure(knobs: &[Knob]) -> bool {
    !knobs.iter().any(|k| k.name == "r0d_anchor")
}

/// The anchor a coordinate's template carries against the context's: a change on the same document (the same
/// digest) is a period boundary, which reconfigures the knob-less workers and sets the knob on the others; a new
/// digest is a new document (reconfigure every worker); the same anchor on the same digest is no change.
#[derive(Debug, PartialEq, Eq)]
pub enum AnchorMove { None, Period, Document }
pub fn anchor_move(ctx_digest: &str, ctx_anchor: Option<&str>, digest: &str, anchor: Option<&str>) -> AnchorMove {
    if ctx_digest != digest { return AnchorMove::Document; }
    if ctx_anchor != anchor { return AnchorMove::Period; }
    AnchorMove::None
}

/// A coordinate template at a period boundary carrying the PREVIOUS period's anchor (the 7322 pool's journal read by the
/// ecosystem seat, 16 October 2026: the node's getWork named the old anchor for the first 6 to 32 s of each period at 600,
/// 900, 1500 and 2100; the pool issued it, Ember and the pool verified under it, the sealer under the period's own, and
/// every share on those jobs was refused "invalid mix digest"). The document's period_blocks and reveal_blocks (2 unless
/// named) give the period's anchor height; a job whose anchor height is exactly one period behind it is stale and is not
/// mined: the next template carries the period's own. Any other difference is not this fault and is left alone.
pub fn anchor_is_one_period_stale(height: u64, job_anchor_height: Option<u64>, doc: &serde_json::Value) -> bool {
    let period = doc.get("period_blocks").and_then(|v| v.as_u64()).unwrap_or(0);
    let reveal = doc.get("reveal_blocks").and_then(|v| v.as_u64()).unwrap_or(2);
    let Some(ah) = job_anchor_height else { return false };
    if period == 0 || height / period == 0 {
        return false;
    }
    let own = registry::anchor_height(height, period, reveal);
    own >= period && ah == own - period
}

#[cfg(test)]
mod period_boundary_tests {
    use super::*;
    fn knob(name: &str) -> Knob { Knob::additive(name, 0.0, 1.0, 1.0, 0.0, "") }
    #[test]
    fn a_boundary_template_carrying_the_previous_periods_anchor_is_stale_and_not_mined() {
        let doc = serde_json::json!({"version": 18, "period_blocks": 300});
        // the mini's 13 shares at 600 under period 1's anchor (height 298) when the period's own is 598
        assert!(anchor_is_one_period_stale(600, Some(298), &doc));
        assert!(!anchor_is_one_period_stale(600, Some(598), &doc));
        // mid-period under the period's own anchor: not this fault; a job naming no anchor height: not judged
        assert!(!anchor_is_one_period_stale(877, Some(598), &doc));
        assert!(!anchor_is_one_period_stale(600, None, &doc));
        // the first period (the genesis anchor) and a document without a period: never stale
        assert!(!anchor_is_one_period_stale(200, Some(0), &doc));
        assert!(!anchor_is_one_period_stale(600, Some(298), &serde_json::json!({"version": 21})));
        // a difference other than one period is another fault, left alone
        assert!(!anchor_is_one_period_stale(1500, Some(598), &doc));
    }
    #[test]
    fn the_300_block_period_boundary_reconfigures_the_raster_worker_and_sets_the_knob_on_the_others() {
        // coordinate 17's document (version 18, digest d) at the boundaries 5698 and 5998: the same digest, a new anchor
        let a5698 = "876d04c608d2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        let a5998 = "1f2e3d4c5b6a99999999999999999999999999999999999999999999999999999";
        assert_eq!(anchor_move("d", Some(a5698), "d", Some(a5998)), AnchorMove::Period);
        assert_eq!(anchor_move("d", Some(a5698), "d", Some(a5698)), AnchorMove::None);
        assert_eq!(anchor_move("d", Some(a5698), "e", Some(a5698)), AnchorMove::Document);
        // the GL raster worker lists no knobs (its hello: "knobs": []): the boundary is a reconfigure
        assert!(anchor_change_needs_reconfigure(&[]));
        assert!(anchor_change_needs_reconfigure(&[knob("intensity")]));
        // the CUDA and Metal workers list r0d_anchor: the knob set suffices
        assert!(!anchor_change_needs_reconfigure(&[knob("intensity"), knob("r0d_anchor")]));
    }
}

pub fn coord_bound_stem(exe: &std::path::Path) -> Option<(String, u8)> {
    let stem = exe.file_stem()?.to_str()?;
    let (base, n) = stem.rsplit_once("-coord")?;
    let c: u8 = n.parse().ok()?;
    if !base.starts_with("igneum-worker-") { return None; }
    Some((base.to_string(), c))
}

/// A worker bound to a DOCUMENT VERSION (igneum-worker-<base>-v<N>): it serves every coordinate whose registered
/// document is version N, whatever the coordinate's number (the raster lane was coordinate 6 and became 17 at height
/// 5386 on 15 October 2026 when 6 retired; a successor carrying the same version keeps the worker). The two forms
/// live side by side: a coordinate-bound worker is found first, then the version-bound one.
pub fn version_bound_stem(exe: &std::path::Path) -> Option<(String, u64)> {
    let stem = exe.file_stem()?.to_str()?;
    let (base, n) = stem.rsplit_once("-v")?;
    let v: u64 = n.parse().ok()?;
    if !base.starts_with("igneum-worker-") || base.ends_with("-coord") { return None; }
    Some((base.to_string(), v))
}

#[cfg(test)]
mod coord_bound {
    #[test]
    fn the_name_binds_a_coordinate() {
        assert_eq!(super::coord_bound_stem(std::path::Path::new("/w/igneum-worker-cuda-coord5")), Some(("igneum-worker-cuda".to_string(), 5)));
        assert_eq!(super::coord_bound_stem(std::path::Path::new("/w/igneum-worker-cuda-coord5.exe")), Some(("igneum-worker-cuda".to_string(), 5)));
        assert_eq!(super::coord_bound_stem(std::path::Path::new("/w/igneum-worker-cuda")), None);
        assert_eq!(super::coord_bound_stem(std::path::Path::new("/w/igneum-worker-metal")), None);
    }
    #[test]
    fn the_pinned_coordinate_takes_its_own_worker_from_the_first_launch() {
        // F-E48 (14 October 2026): a slot pinned to coordinate 5 launches igneum-worker-cuda-coord5, with the base
        // launcher's environment and directory; no pin, another pin or another backend keeps the base worker
        let base = super::Launcher { exe: "/w/igneum-worker-cuda".into(), expected_sha256: None, args: vec![], env: vec![("CUDA_VISIBLE_DEVICES".into(), "1".into())], workdir: "/w".into() };
        let c5 = super::Launcher { exe: "/w/igneum-worker-cuda-coord5".into(), expected_sha256: Some("2cec0365".into()), args: vec![], env: vec![], workdir: "/".into() };
        let bound = vec![("igneum-worker-cuda".to_string(), 5u8, c5)];
        let l = super::pin_bound_launcher(&bound, Some(5), &base, "").expect("the bound worker");
        assert_eq!(l.exe, std::path::PathBuf::from("/w/igneum-worker-cuda-coord5"));
        assert_eq!(l.env, base.env);
        assert_eq!(l.workdir, base.workdir);
        assert_eq!(l.expected_sha256.as_deref(), Some("2cec0365"));
        assert!(super::pin_bound_launcher(&bound, None, &base, "").is_none());
        assert!(super::pin_bound_launcher(&bound, Some(4), &base, "").is_none());
        let metal = super::Launcher { exe: "/w/igneum-worker-metal".into(), expected_sha256: None, args: vec![], env: vec![], workdir: "/w".into() };
        assert!(super::pin_bound_launcher(&bound, Some(5), &metal, "").is_none());
    }
    #[test]
    fn the_refusal_names_the_served_set_or_its_absence() {
        // F-E46: the set printed; F-E49: a plain source named (the 5090 Windows host's read of 14 October 2026: "--coord 5" on the public
        // pool mined on the plain job with no word)
        assert_eq!(super::pin_refusal_words(5, &[0, 1, 2, 3, 4]), "coordinate 5 is pinned but the source serves coordinates 0, 1, 2, 3, 4 alone; nothing mines until the pin names a served coordinate");
        assert_eq!(super::pin_refusal_words(5, &[]), "coordinate 5 is pinned but this source serves no coordinates; nothing mines until the pin is cleared or the source serves it");
    }
}

pub fn device_ordinal(id: &str, idx: usize) -> usize {
    id.rsplit(':').next().and_then(|n| n.parse::<usize>().ok()).unwrap_or(idx)
}

#[cfg(test)]
mod straddle_tests {
    use super::*;
    #[test]
    fn an_invalid_result_inside_the_grace_of_a_period_transition_is_a_straddle_and_after_it_a_fault() {
        let now = Instant::now();
        // the transition still in progress: a straddle whatever the time
        assert!(straddles_transition(false, None, now));
        // no transition ever: a fault
        assert!(!straddles_transition(true, None, now));
        // ten seconds after the transition: a straddle; past the grace: a fault
        assert!(straddles_transition(true, Some(now - Duration::from_secs(10)), now));
        assert!(straddles_transition(true, Some(now - Duration::from_secs(TRANSITION_GRACE_S - 1)), now));
        assert!(!straddles_transition(true, Some(now - Duration::from_secs(TRANSITION_GRACE_S + 1)), now));
    }
}

#[cfg(test)]
mod f_e41 {
    #[test]
    fn ordinal_from_the_id_else_the_position() {
        assert_eq!(super::device_ordinal("cuda:1", 7), 1);
        assert_eq!(super::device_ordinal("cuda:0", 7), 0);
        assert_eq!(super::device_ordinal("GPU-abcd", 2), 2);
        assert_eq!(super::device_ordinal("", 3), 3);
    }
}

struct Slot {
    device: Device,
    labelled: Labelled,
    launcher: Launcher,
    /// The slot's base launcher (the worker for the coordinates without a bound worker), kept while a bound one runs.
    base_launcher: Option<Launcher>,
    enabled: bool,
    proc: Option<WorkerProcess>,
    state: SlotState,
    message: String,
    params: Params,
    knobs: Vec<Knob>,
    worker_name: String,
    worker_version: String,
    worker_note: String,
    /// The worker lists the `keyed-job` extension: it takes the job's secret under a keyed document.
    keyed_job: bool,
    /// How the worker is reached (security's rule: a keyed job goes only to a local transport).
    transport: Transport,
    /// The candidate ids this slot's worker carries with passing vectors.
    algos: Vec<String>,
    telemetry: Option<Telemetry>,
    last_telemetry: Instant,
    phase: String,
    epoch: Option<u64>,
    preparing: Duration,
    opened_at: Option<Instant>,
    /// Since when the slot has sat idle with no worker under a running layer (the reopen rule in the tick).
    idle_since: Option<Instant>,
    /// Linux: the NVIDIA power cap in the settings applied by nvidia-smi and read back (false = refused, the row says why)
    power_cap_applied: bool,
    /// F-E18: the row's own words for the cap: "cap N W applied" once the driver read it back, else
    /// "cap N W requested, not applied: <the reason>"; empty with no cap in the settings.
    power_cap_word: String,
    /// F-110: whether this slot's worker lifts the coordinate-1 pin (None until hashed once).
    f110_lift: Option<bool>,
    /// The name of the last `set` this supervisor sent the worker (a knob ack without a name answers it).
    last_set: String,
    /// VectorWork: this card's standing with the router, the rate meter of its current stint on a coordinate
    /// (fed like `reported`, reset at every switch), when the stint began, and the digest of the document the
    /// worker is configured on (the coordinate's), for the result check.
    route: router::CardRoute,
    /// The card's figures per lane this session, by coordinate; the tick accumulates the current lane's seconds and rate.
    lane_stats: BTreeMap<u8, LaneStat>,
    lane_tick_at: Option<Instant>,
    /// The card was closed because its worker refused a session document at configure (the document's fault, not the
    /// card's): a fresh pool session reopens it (reopens_on_fresh_session).
    document_fault: bool,
    stint: RateMeter,
    stint_since: Option<Instant>,
    /// F-E59: the settle line said once per stint.
    settle_said: bool,
    /// F-470 (the Windows proof hosts under 0.2.25 and 0.2.26, 17 October 2026): a programme build the worker runs on
    /// a set (the period's anchor) while the slot is mining; the stat clock is bounded by the build's allowance until
    /// the worker's first mining stat after it, never by the ten-second rule.
    build_until: Option<Instant>,
    /// F-476 (the second Windows proof host under the 0.2.29 re-cut, 17 October 2026): a launched worker whose hello
    /// came before the source registered any coordinate document is held here, unconfigured, until one registers
    /// (since when); it is never configured on the empty session document.
    awaiting_document: Option<Instant>,
    /// F-E64: quick exits in a row (the worker gone within QUICK_EXIT_S of its start), and the restart's back-off.
    quick_exits: u32,
    restart_after: Option<Instant>,
    coord_digest: Option<String>,
    /// The last programme anchor handed to the worker (set again only on a change).
    last_anchor: Option<String>,
    restarts: u32,
    hangs: VecDeque<Instant>,
    /// F-437 (re-cut on F-439): the card's last WORK on its lane (a stat reporting hashes, or an accepted share), the
    /// bounded settle's clock beside the stint's start; a hashing card with no share waits on the pool's step.
    last_result_at: Option<Instant>,
    /// F-562: the last RESULT the worker found on its lane (a share reaching the verifier, valid or not), the silent-worker
    /// watchdog's clock beside the stint's start; and the silent restarts per coordinate this card has had.
    last_share_at: Option<Instant>,
    silent_restarts: BTreeMap<u8, u32>,
    /// F-383: a launch in flight on its own thread (the generation, its start); the slot reads "opening" meanwhile.
    launch_pending: Option<(u64, Instant)>,
    /// F-471 (the second Linux proof host under 0.2.25, 17 October 2026): a launch waiting its turn at the launch gate
    /// (the generation); it spawns when a launch in flight ends, and its hello clock starts at that spawn.
    launch_queued: Option<u64>,
    invalids: VecDeque<Instant>,
    /// when the slot's last period (epoch) transition finished (the straddle grace counts from it)
    transition_done_at: Option<Instant>,
    /// a valid result has been checked on the slot's current document since its last configure (the GPU team's row (2) of
    /// 15 October 2026: the three invalids were the card's FIRST results on coordinate 16 one second after the
    /// switch from coordinate 1's document; the pinned session had none in 203)
    doc_valid_seen: bool,
    /// invalid results on the current document before any valid one (counted as straddled, never a refusal until
    /// SWITCH_INVALID_MAX of them)
    switch_invalids: u32,
    /// F-271: the run of results found late (the template moved on before the check) with no current result between:
    /// the slot's template id when the run last advanced and the templates it has outlived; two templates restart the slot.
    late_run: LateRun,
    proto_faults: VecDeque<Instant>,
    validated: RateMeter,
    reported: RateMeter,
    invalid_total: u64,
    active_job: Option<String>,
    chunk_index: u64,
    /// The job the slot's chunks are drawn from and how many it has taken of it (F-E5, 13 October 2026: a lane
    /// searched through on a long job was searched again from its start, every share re-found and resubmitted;
    /// the pool's 'duplicate' closures of the two-card Windows host and 1,000 're-found' holds on the mini).
    chunk_job: Option<String>,
    chunks_in_job: u64,
    lane_done_said: bool,
    measure_difficulty: f64,
    recent_valid: VecDeque<Instant>,
    tune: Option<TuneAcc>,
    last_tune: Option<StoredTune>,
    transition: Transition,
    /// The epoch the device's resident dataset is for.
    dataset_epoch: Option<u64>,
    /// The epoch prepared in the background and waiting (READY).
    next_epoch: Option<u64>,
    can_warm: bool,
    /// The job id whose first stat marks the end of a switch, and when it was sent.
    switch_pending: Option<(String, Instant)>,
    switch_samples: VecDeque<u64>,
    /// A transition in progress: when the device last mined and at what rate (for the lost-work charge).
    transition_started: Option<(Instant, f64, u64, u64, bool)>,
    /// Episodes of repeated invalid results (the restart at safe settings); a second within an hour stops the device.
    invalid_episodes: VecDeque<Instant>,
    /// Time not mining during the transition in progress (the cold path's gap).
    preparing_gap: Option<Duration>,
    /// A job was cancelled on a mining device: the next job dispatched is a switch sample.
    switch_armed: bool,
    /// Board-power samples over the validated window, for the energy figure.
    power: RateMeter,
    /// A background build in flight: when prepare was sent, hashes reported since.
    building: Option<(Instant, f64)>,
    /// The last background build's figures, for the transition record: (build ms, rate while building).
    last_build: Option<(u64, f64)>,
    /// The announced document a prepare was sent for (its digest), so the change can take the warm path.
    next_doc_digest: Option<String>,
    /// When the device last produced a validated result (or came to mining), for the no-results watchdog.
    last_validated_at: Option<Instant>,
    /// Episodes of hashing without results (restarts), for the stop on a repeat within an hour.
    no_result_episodes: VecDeque<Instant>,
    /// 0.2.11: this card's share of the session's submission cap, in proportion to its reported rate (the slow card
    /// beside a fast one no longer starves), and its budget of held results checked for the no-result watch.
    slot_pace: SubmitPace,
    held_verify_pace: SubmitPace,
    /// Since when the worker's own stats say it hashes nothing while it holds a job in the mining state (a
    /// worker that stopped after a cancel: the 5090 pod, 11 October); None while it reports hashes.
    idle_hashes_since: Option<Instant>,
    /// When the measurement target last stepped (doubled or halved): at most one step a minute.
    measure_stepped_at: Option<Instant>,
}

impl Slot {
    fn view(&mut self, settings: &Settings, store: &TuneStore, now_unix: u64, algo: &str, versions: &BTreeMap<u8, u64>) -> SlotView {
        let pinned = settings.pins.get(&self.device.fingerprint).cloned().unwrap_or_default();
        let key = TuneStore::key(&self.device.fingerprint, &self.worker_version, &self.device.driver, algo);
        let stored = store.get(&key).cloned().or_else(|| self.last_tune.clone());
        let tune = match (&self.tune, stored) {
            (Some(acc), stored) => Some(TuneView { running: true, chosen: stored.as_ref().map(|s| s.chosen.clone()).unwrap_or_default(), candidates: stored.as_ref().map_or(0, |s| s.candidates), stopped: stored.as_ref().map(|s| s.stopped.clone()), when_unix: stored.as_ref().map(|s| s.when_unix), fresh: false, current_candidate: Some(acc.current.clone()) }),
            (None, Some(s)) => Some(TuneView { running: false, chosen: s.chosen.clone(), candidates: s.candidates, stopped: Some(s.stopped.clone()), when_unix: Some(s.when_unix), fresh: store.is_fresh(&key, now_unix), current_candidate: None }),
            (None, None) => None,
        };
        SlotView {
            fingerprint: self.device.fingerprint.clone(),
            id: self.device.id.clone(),
            bus_id: self.device.bus_id.clone(),
            index: self.device.index,
            backend: self.device.backend.clone(),
            name: self.device.name.clone(),
            vendor: self.device.vendor.clone(),
            kind: self.device.kind.clone(),
            memory_total: self.device.memory_total,
            memory_free: self.device.memory_free,
            driver: self.device.driver.clone(),
            arch: self.device.arch.clone(),
            runtime: self.device.runtime.clone(),
            label: self.labelled.label,
            reason: self.labelled.reason.clone(),
            enabled: self.enabled,
            state: self.state,
            message: self.message.clone(),
            coord: self.route.coord,
            coord_word: match self.route.coord { Some(c) => format!("coordinate {c}{}", if self.route.probing { ", probing" } else { "" }), None => String::new() },
            lane_word: match self.route.coord { Some(c) => format!("{}{}", lane_word(versions.get(&c).copied(), Some(c)), if self.route.probing { ", probing" } else { "" }), None => String::new() },
            lanes: self.lane_stats.iter().map(|(c, l)| state::LaneView { coord: *c, word: lane_word(versions.get(c).copied(), Some(*c)), accepted: l.accepted, hps: l.hps, seconds: l.seconds, current: self.route.coord == Some(*c) }).collect(),
            phase: self.phase.clone(),
            epoch: self.epoch,
            algo: algo.to_string(),
            params: self.params.clone(),
            pinned,
            knobs: self.knobs.clone(),
            telemetry: self.telemetry.clone(),
            reports: self.telemetry.as_ref().map(|t| t.reports().into_iter().map(String::from).collect()).unwrap_or_default(),
            validated_hps: self.validated.rate(),
            reported_hps: self.reported.rate(),
            reported_60s_hps: self.reported.rate_over(Duration::from_secs(60)),
            reported_10s_hps: self.reported.rate_over(Duration::from_secs(10)),
            stint_s: if self.state == SlotState::Mining { self.stint.age() } else { 0.0 },
            power_60s_w: self.power.mean_over(Duration::from_secs(60)),
            invalid_results: self.invalid_total,
            restarts: self.restarts,
            worker: self.worker_name.clone(),
            worker_version: self.worker_version.clone(),
            worker_note: self.worker_note.clone(),
            tune,
            transition: self.transition.clone(),
            dataset_epoch: self.dataset_epoch,
            next_epoch: self.next_epoch,
            can_warm: self.can_warm,
            switch_median_ms: percentile(&self.switch_samples, 0.5),
            switch_p99_ms: percentile(&self.switch_samples, 0.99),
            switch_samples: self.switch_samples.len(),
            energy: energy_view(self.validated.rate(), self.power.mean().unwrap_or(0.0), "telemetry"),
            build_progress: self.telemetry.as_ref().and_then(|t| if t.build_in_flight == Some(true) { t.build_progress } else { None }),
            power_offer: devices::power_offer(&self.device),
            power_cap: self.power_cap_word.clone(),
        }
    }
}

/// The p-quantile of the samples (nearest rank), None when empty.
fn percentile(samples: &VecDeque<u64>, p: f64) -> Option<u64> {
    if samples.is_empty() {
        return None;
    }
    let mut v: Vec<u64> = samples.iter().copied().collect();
    v.sort_unstable();
    let rank = ((p * v.len() as f64).ceil() as usize).clamp(1, v.len());
    Some(v[rank - 1])
}

struct Core {
    cfg: Config,
    /// The lab pacing (job_target_under): EMBER_PACE_CHAIN_TARGET read once at construction.
    pace_chain_target: bool,
    settings: Settings,
    tune_store: TuneStore,
    journal: journal::Journal,
    slots: Vec<Slot>,
    mining: MiningState,
    paused_for: Option<PauseCause>,
    source: Option<Box<dyn WorkSource>>,
    verifier: Option<Arc<dyn Verifier>>,
    /// VectorWork (router.rs, ROUTER.md): one context per coordinate the session serves: its latest template, its
    /// document and digest, its verifier (built once per digest); empty on a one-form session.
    coords: BTreeMap<u8, CoordCtx>,
    /// F-496: the last lane worth and worker count said per coordinate ("worth|workers"), so the line is written once per
    /// coordinate and value, never per job.
    lane_facts_said: BTreeMap<u8, String>,
    /// F-564: the job ids whose cancel was said (one line per job, never per result).
    cancelled_said: std::collections::HashSet<String>,
    /// The coordinate table at the router's last decision and the head it was taken at (read_due), and the last
    /// reads for the damped view.
    router_last: (u64, BTreeMap<u8, router::CoordState>),
    router_history: Vec<BTreeMap<u8, router::CoordState>>,
    /// The verification queue: every result is checked on the verifier threads, never on the tick (section 10: a
    /// flood of results must not stall the supervisor); a full queue drops the result and counts it unverified.
    verify_tx: Option<SyncSender<VerifyReq>>,
    /// The measurement-only queue (the verify-queue cell): share-class results go on verify_tx and are checked first.
    verify_tx_measure: Option<SyncSender<VerifyReq>>,
    verify_rx: Option<Receiver<VerifyRes>>,
    /// F-383 (the second Octominer rig, 16 October 2026): a worker's launch runs on its own thread and the tick never
    /// waits for a hello (thirteen freezes of 47 to 430 s on eight cards, every share held through them). The thread
    /// sends the launched process (or the fault) here with the slot and the launch's generation; a result whose
    /// generation the slot no longer holds (closed or re-opened meanwhile) is ended at once.
    launch_tx: std::sync::mpsc::Sender<LaunchDone>,
    launch_rx: Receiver<LaunchDone>,
    launch_seq: u64,
    /// F-471: the launches allowed in flight at once (launch_cap_for the host's cores); the rest wait queued.
    launch_cap: usize,
    /// Results queued and not yet taken by a verifier thread (both classes), and the threads' running mean check time
    /// in microseconds: the queue's expected wait is depth times the mean over the threads.
    verify_depth: Arc<AtomicUsize>,
    verify_check_us: Arc<AtomicU64>,
    verify_threads: usize,
    verify_dropped_said: Option<Instant>,
    /// F-E46: the pin-not-served line said once a minute.
    pin_refused_said: Option<Instant>,
    /// F-E66: the pool-paced line said once a minute.
    paced_said: Option<Instant>,
    /// F-E66: the last run's rate written to the settings once a minute.
    last_rate_saved: Option<Instant>,
    /// F-E66: after the pool's bucket paced a share, the engine's submit cap (the pool's bucket) and until when.
    /// F-271 (16 October 2026): the pool's pacing is scoped to the SESSION it paced (the coordinate of the paced job),
    /// never the engine: a burst on the programme lane's session capped the bandwidth lane's results for a minute.
    pool_bucket_caps: BTreeMap<Option<u8>, (u32, Instant)>,
    /// F-E49 (0.2.12): the source has sent a template without a coordinate (a plain source), so an empty served set is
    /// final, not the start's; cleared with the coordinates on a session change.
    plain_template_seen: bool,
    /// F-E46 and F-E49: the pin names nothing this source serves; no slot is opened or given work while it holds.
    pin_refused: bool,
    /// F-E62 (0.2.15): when the session's first coordinate template arrived; a pool sends its coordinates one template
    /// at a time, so the pin is judged only once the served set has had PIN_GRACE_S to fill.
    first_coord_template_at: Option<Instant>,
    /// F-E60: the coordinate documents' self-checks in flight (each reports once); drained on the tick.
    coord_checks: Vec<Receiver<(u8, String, Result<u64, String>)>>,
    /// 0.2.22: a coordinate's verifier builds on its own thread (the raster document's reference check took 92 s on a cold
    /// start inside the tick on 16 October 2026); the newest template that waited on it registers when it reports
    coord_builds: BTreeMap<u8, CoordBuild>,
    /// the (coordinate, height) of the last stale-anchor template said (one line a boundary)
    stale_anchor_said: Option<(u8, u64)>,
    /// the document digests checked this session (one check per document)
    checked_digests: std::collections::BTreeSet<String>,
    /// 0.2.15: the chain's discipline registry as the engine last read it (registry.rs), the read thread's events,
    /// and the last served-set comparison said (once per change).
    registry_rx: Option<std::sync::mpsc::Receiver<registry::RegistryEv>>,
    registry: Option<registry::VectorView>,
    registry_docs: BTreeMap<u8, registry::CoordDoc>,
    /// F-290: the coordinates the registry retired; a job on one is refused so its context is never rebuilt.
    retired_coords: BTreeSet<u8>,
    registry_said: Option<String>,
    registry_failed_said: Option<Instant>,
    current: Option<JobTemplate>,
    /// F-E23: the current job's share target and since when it has stood (a new target resets it); the
    /// target-too-low warning waits until the target has stood TARGET_STOOD_FOR, since the pool's step climbs
    /// through a session's first minutes and results held back meanwhile are the climb, not a fault.
    share_target_since: Option<([u8; 32], Instant)>,
    target_warned: bool,
    sent: HashMap<String, Sent>,
    /// Every (job, nonce) submitted this session, by job (F-E5): a share is never submitted twice; pruned with `sent`.
    submitted_seen: HashMap<String, std::collections::HashSet<u64>>,
    sent_order: VecDeque<String>,
    /// Shares handed to the source and not yet answered: (job id, nonce) to (slot, met the boundary).
    /// Results sent to the source by (job, nonce): the device, whether the result met the block target on a block
    /// template, and whether it is a share of the share lane (never a block).
    /// Each submission awaiting the source's answer: the slot, whether a block, whether a lane share, the
    /// template's height and the pool's share step at the submit (the scene's share event on acceptance).
    /// F-469: the sixth field is the share's weight (router::share_weight of the job's share target) for the lane's measured rate.
    submitted: HashMap<(String, u64), (usize, bool, bool, u64, u8, f64)>,
    /// The last accepted shares, oldest first (RECENT_SHARES): the scene's feed through the snapshot.
    recent_shares: VecDeque<crate::state::ShareEvent>,
    /// The dev fee's job ids this session (devfee.rs): an accepted share on one is the fee's.
    dev_fee_jobs: std::collections::HashSet<String>,
    /// When each submission went out, for the no-answer watchdog.
    submitted_at: HashMap<(String, u64), Instant>,
    /// When the last submission went out and when the last outcome came back.
    last_submit: Option<Instant>,
    last_outcome: Option<Instant>,
    /// Since when submissions have gone unanswered (the source silent while results flow).
    unanswered_since: Option<Instant>,
    /// F-392 (the Octominer rig's one-card row on 0.2.23, 16 October 2026): the step watch's marks: the dial's time and
    /// the rate it declared, the last accepted share, the not-submitted count at that mark, the last step re-dial.
    dialed_at: Option<Instant>,
    declared_rate_hps: u64,
    last_accepted_at: Option<Instant>,
    not_submitted_at_mark: u64,
    step_redial_at: Option<Instant>,
    /// F-392: the session closed on a step mismatch stays closed until this instant (the pool's re-dial grace: a dial
    /// within two minutes resumes the standing step; after it the declared rate chooses the step).
    step_hold_until: Option<Instant>,
    /// F-E14 (the two-card Windows host, 22:13 to 22:26 UK, 13 October 2026: idle with both cards ready and the pool "connected", no job for
    /// thirteen minutes until the pool's roll): when the last job arrived from the source, or the source was connected;
    /// a connected source that sends no job for NO_WORK_AFTER is redialled and said, every NO_WORK_AFTER until work flows.
    last_job_at: Option<Instant>,
    no_work_redials: u32,
    /// The submission pacer (the cap's window and the spacing).
    submit_pace: SubmitPace,
    /// Lines from the verifier's warm threads, drained each tick.
    warm_tx: std::sync::mpsc::Sender<String>,
    warm_rx: std::sync::mpsc::Receiver<String>,
    totals: Totals,
    log: VecDeque<String>,
    /// The count of lines ever logged (`Supervisor::log_since` reads by it: the app drains the ring into its own
    /// log each tick, so the supervisor's stop reasons and source words are on disk).
    log_seq: u64,
    notices: Vec<Notice>,
    stages: Vec<Stage>,
    first_work_accepted: bool,
    started_at: Option<Instant>,
    /// The dev fee record's session stamp (unix seconds at its first line) and whether a write failed (said once).
    fee_record_start: u64,
    fee_record_failed: bool,
    last_tick: Instant,
    downtime: Duration,
    game_gone_since: Option<Instant>,
    /// The OS memory-pressure reading (the 8 GB class's running guard); replaced in tests by a driven source.
    pressure_source: Box<dyn Fn() -> Option<pressure::Level> + Send>,
    /// When the guard paused mining and the validated rate then, for the record on resume.
    pressure_paused_at: Option<(Instant, f64)>,
    /// The last level read, for the state line (None where the platform has no reading).
    memory_pressure: Option<pressure::Level>,
    probe: Box<dyn FullscreenProbe>,
    tune_running: bool,
    stop_flag: bool,
    transitions: VecDeque<TransitionRecord>,
    params_digest: String,
    /// (when, ms) of recent ticks, for the longest tick of the last minute.
    tick_times: VecDeque<(Instant, u64)>,
    /// The parameter document every worker is configured with now (the source's at start, then each template's).
    algo_params: serde_json::Value,
    /// The served programme anchor the session verifier was built with (F-E6); None when no job named one.
    session_anchor: Option<String>,
    /// The last bond key a template named that was not this miner's (said once).
    refused_bond_key: Option<[u8; 32]>,
    /// A template digest this miner could not match (said once; no work is taken on it until it changes).
    refused_digest: Option<String>,
    /// Verification latencies of the last results, ms.
    verify_latencies: VecDeque<u64>,
    /// (when, in time) of the last verified results: in time means its template was still current.
    verify_window: VecDeque<(Instant, bool)>,
    /// Since when most results have been checked too late for their template.
    late_since: Option<Instant>,
    /// Workers that did not start at the last inventory, with the reason (shown by `ember devices` and the app).
    launch_errors: Vec<String>,
    /// Since when the work source has reported a problem without a break.
    source_problem_since: Option<Instant>,
    /// The times of the session's recent reloads (source_lost).
    source_reloads: Vec<Instant>,
    /// The pool retry while the cards idle for an unreachable source: the next turn and the wait it doubles from.
    source_retry: Option<(Instant, Duration)>,
    /// The payout address's balance on the chain in IGN, set by the app after its eth_getBalance read (F-82).
    chain_balance_ign: Option<f64>,
    /// F-E20: the chain's figures for a pool estimate (the network's rate, the block time, the payees' IGN a
    /// block), read from the chain's feed by the supervisor's own poll (chain_figures_poll) so the command line
    /// carries them without the app.
    chain_figures: Option<economics::ChainFigures>,
    /// F-E25: the ledger's paid reading for the payout address on the job's chain, by the same poll.
    ledger_paid: Option<economics::LedgerPaid>,
    /// F-E24: the pointer's newer version, when one is read; the log says it once per version.
    update_offer: Option<state::UpdateOffer>,
    /// F-E33 (14 October 2026): the miner's work-authority key, loaded once at start and attached to EVERY source
    /// the supervisor builds (the re-dial after no work, the reconnect after unanswered submissions, the backup
    /// switch built a fresh source without it, so the re-dial's authorize carried no key or signature and the pool
    /// answered "unauthorised" until the app was restarted: the mini at 05:48 UK tonight).
    signer: Option<Arc<minerkey::MinerKey>>,
    /// F-E34b: the unverified count at the last no-result watch (a rise means the verifier dropped results).
    unverified_at_last_watch: u64,
    update_offer_said: String,
    /// Mining on the backup source since (None on the first source).
    on_backup_since: Option<Instant>,
    /// The last try of the first source while on the backup.
    primary_retry_at: Option<Instant>,
    /// The tick thread's last beat (read by the watchdog thread).
    tick_beat: Instant,
    /// The tick's named synchronous phase (shared with the watchdog; see Supervisor::sync_phase).
    sync_phase: Arc<Mutex<Option<(Instant, String)>>>,
    /// The longest phase of the tick in progress, for the timing line (name, ms).
    tick_phase_longest: Option<(String, u64)>,
    /// Why the tick failed or stalled, when it did (shown in the state; the CLI exits on it).
    tick_fault: Option<String>,
    tick_faults: u32,
}

fn lock(m: &Mutex<Core>) -> MutexGuard<'_, Core> {
    m.lock().unwrap_or_else(|e| e.into_inner())
}

/// The supervisor handle: cheap to clone, shared by the interface, the ticker and the tune thread.
/// One coordinate's context on a VectorWork session (router.rs): its latest template, the document its jobs carry
/// with its digest, and the verifier built for it once.
/// A coordinate's verifier build in flight: the newest template, document and anchor that wait on it.
struct CoordBuild {
    job: JobTemplate,
    doc: serde_json::Value,
    digest: String,
    anchor: Option<String>,
    version: u64,
    since: Instant,
    rx: Receiver<Result<Arc<dyn Verifier>, String>>,
}

struct CoordCtx {
    template: JobTemplate,
    doc: serde_json::Value,
    digest: String,
    verifier: Arc<dyn Verifier>,
    /// The period's programme anchor the verifier and the document carry (the notify's program_anchor, 64 hex); the
    /// verifier is rebuilt when it changes (F-E6, 13 October 2026: Ember's reference hashed with the document's own
    /// zero anchor and agreed with a worker that did the same, while the pool's reference used the served anchor).
    anchor: Option<String>,
    /// The height of the coordinate's last block as the pool states it, when it does.
    last_height: Option<u64>,
    /// F-384 (the second Octominer rig, 16 October 2026): a lane the pool has refreshed no job on for COORD_WITHDRAWN_S is
    /// PAUSED for the router (its context, verifier and the cards' memory kept), never dropped; its next job resumes it.
    paused_since: Option<Instant>,
    /// F-384's residual (the security review, 17 October 2026): after a same-pool re-dial the kept template is the OLD session's
    /// job until the lane's first new job; a slot is given no template from it meanwhile (it waits, never mines a job
    /// the pool let go).
    let_go: bool,
}

/// F-E20: the chain's public feed (the showcase's record of the public test network: the head, the hour's mean
/// block time, the network's rate estimated from the difficulty, the recent blocks' issuance and fund part).
pub const CHAIN_FEED_URL: &str = "https://build.igneum.network/feed/devnet.json";
pub const CHAIN_FIGURES_EVERY: Duration = Duration::from_secs(60);

/// The chain's figures from the feed's document: `hash_rate_hps`, `block_time_s_mean`, and the payees' IGN a block
/// as the mean of `payout_ign - fund_ign` over `recent_blocks`. None when any is missing or not a number.
pub fn chain_figures_from(doc: &serde_json::Value, now_unix: u64) -> Option<economics::ChainFigures> {
    let num = |v: Option<&serde_json::Value>| -> Option<f64> {
        match v? {
            serde_json::Value::Number(n) => n.as_f64(),
            serde_json::Value::String(t) => t.trim().parse::<f64>().ok(),
            _ => None,
        }
    };
    let network_hps = num(doc.get("hash_rate_hps")).filter(|x| *x > 0.0)?;
    let block_time_s = num(doc.get("block_time_s_mean")).filter(|x| *x > 0.0)?;
    let blocks = doc.get("recent_blocks")?.as_array()?;
    let pays: Vec<f64> = blocks.iter().filter_map(|b| Some(num(b.get("payout_ign"))? - num(b.get("fund_ign")).unwrap_or(0.0))).filter(|x| *x > 0.0).collect();
    if pays.is_empty() {
        return None;
    }
    let payees_ign_per_block = pays.iter().sum::<f64>() / pays.len() as f64;
    Some(economics::ChainFigures { network_hps, block_time_s, payees_ign_per_block, source: "the chain's feed".into(), read_at_unix: now_unix })
}

/// F-E25: the Live Work Ledger's miner view, per chain (the settlement team's path of 13 October 2026).
pub const LEDGER_URL_ROOT: &str = "https://build.igneum.network/ledger/";

/// The ledger's paid reading from a miner-view document: `paid_wei`, `blocks[]` (the last with `paid_wei`,
/// `carried`, `weight`, `list_total`), `ledger.record`, `read_at`. None when the document lacks them.
pub fn ledger_paid_from(doc: &serde_json::Value, chain_id: u64) -> Option<economics::LedgerPaid> {
    let wei = |v: Option<&serde_json::Value>| -> f64 {
        match v {
            Some(serde_json::Value::String(t)) => t.trim().parse::<u128>().map(|w| w as f64 / 1e18).unwrap_or(0.0),
            Some(serde_json::Value::Number(n)) => n.as_f64().map(|w| w / 1e18).unwrap_or(0.0),
            _ => 0.0,
        }
    };
    let u = |v: Option<&serde_json::Value>| -> u64 { v.and_then(|x| x.as_u64()).unwrap_or(0) };
    let paid_ign = wei(doc.get("paid_wei"));
    let blocks = doc.get("blocks")?.as_array()?;
    let last = blocks.iter().rev().find(|b| wei(b.get("paid_wei")) > 0.0)?;
    Some(economics::LedgerPaid {
        chain_id,
        paid_ign,
        blocks: blocks.len() as u64,
        last_height: u(last.get("height")),
        last_paid_ign: wei(last.get("paid_wei")),
        last_carried: u(last.get("carried")),
        last_weight: u(last.get("weight")),
        last_list_total: u(last.get("list_total")),
        record: doc.get("ledger").and_then(|l| l.get("record")).and_then(|r| r.as_str()).unwrap_or("").to_string(),
        read_at: doc.get("read_at").and_then(|r| r.as_str()).unwrap_or("").to_string(),
    })
}

fn read_ledger_paid(root: &str, chain_id: u64, address: &str) -> Option<economics::LedgerPaid> {
    let url = format!("{root}{chain_id}/miner-view/{}.json", address.trim().to_lowercase());
    let u = net::parse_url(&url).ok()?;
    let (status, _headers, body) = net::http_keepalive("GET", &u, None, &[], Duration::from_secs(20)).ok()?;
    if status != 200 {
        return None;
    }
    let doc: serde_json::Value = serde_json::from_slice(&body).ok()?;
    ledger_paid_from(&doc, chain_id)
}

/// F-E24: the signed pointer and how often it is read.
pub const POINTER_URL: &str = "https://build.igneum.network/releases/ember/latest/igneum-app-latest.json";
pub const UPDATE_OFFER_EVERY: Duration = Duration::from_secs(3600);

/// Three-part versions compared numerically; a version that does not parse never offers.
#[cfg(test)]
mod withdrawn_pointer {
    #[test]
    fn a_withdrawn_version_offers_nothing() {
        let doc = serde_json::json!({"version": "0.2.8", "directory": "ember/0.2.8", "withdrawn": true, "platforms": {"linux": {"url": "https://x/ember-linux.tar.gz", "sha256": "ab", "size": 1}, "mac": {"url": "https://x/Ember.dmg", "sha256": "cd", "size": 1}}});
        assert!(super::update_offer_from(&doc, "0.2.7", "auto", 0).is_none());
        let mut live = doc.clone(); live["withdrawn"] = serde_json::Value::Bool(false);
        assert!(super::update_offer_from(&live, "0.2.7", "auto", 0).is_some());
    }
}

pub fn version_newer(candidate: &str, running: &str) -> bool {
    let parse = |v: &str| -> Option<(u64, u64, u64)> {
        let core = v.trim().split(|c: char| c == '-' || c == '+').next()?;
        let mut it = core.split('.').map(|x| x.parse::<u64>().ok());
        Some((it.next()??, it.next()??, it.next()??))
    };
    match (parse(candidate), parse(running)) {
        (Some(c), Some(r)) => c > r,
        _ => false,
    }
}

/// The offer from the pointer's document for this host: Linux takes the `linux` entry (the archive), macOS the
/// `mac` entry, Windows the `windows` entry; None when the pointer's version is not newer or the entry is absent.
pub fn update_offer_from(doc: &serde_json::Value, running: &str, policy: &str, now_unix: u64) -> Option<state::UpdateOffer> {
    let version = doc.get("version")?.as_str()?.to_string();
    // the project's item of 14:1x UK, 14 October 2026 (after F-E44): a pointed version carrying "withdrawn": true offers nothing,
    // so an offered version is withdrawn without walking the pointer back (the store's latest never goes backwards)
    if doc.get("withdrawn").and_then(|w| w.as_bool()).unwrap_or(false) {
        return None;
    }
    if !version_newer(&version, running) {
        return None;
    }
    let key = if cfg!(target_os = "macos") { "mac" } else if cfg!(windows) { "windows" } else { "linux" };
    let e = doc.get("platforms")?.get(key)?;
    let url = e.get("url")?.as_str()?.to_string();
    let sha256 = e.get("sha256").and_then(|x| x.as_str()).unwrap_or("").to_string();
    let size = e.get("size").and_then(|x| x.as_u64()).unwrap_or(0);
    let directory = doc.get("directory").and_then(|x| x.as_str()).unwrap_or("").to_string();
    let file = url.rsplit('/').next().unwrap_or("ember.tar.gz").to_string();
    let command = if key == "linux" {
        format!("cd /tmp && curl -fsSLO {url} && echo '{sha256}  {file}' | sha256sum -c && tar xzf {file} && cd {} && ./install.sh --keep-config", file.trim_end_matches(".tar.gz"))
    } else if key == "mac" {
        format!("open {url} (the dmg; drag Ember to Applications)")
    } else {
        format!("download and run {url} (the Setup keeps the settings)")
    };
    Some(state::UpdateOffer { version, running: running.to_string(), url, sha256, size, directory, command, policy: policy.to_string(), read_at_unix: now_unix })
}

fn read_update_offer(url: &str, running: &str, policy: &str) -> Option<state::UpdateOffer> {
    let u = net::parse_url(url).ok()?;
    let (status, _headers, body) = net::http_keepalive("GET", &u, None, &[], Duration::from_secs(20)).ok()?;
    if status != 200 {
        return None;
    }
    let doc: serde_json::Value = serde_json::from_slice(&body).ok()?;
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
    update_offer_from(&doc, running, policy, now)
}

fn read_chain_figures(url: &str) -> Option<economics::ChainFigures> {
    let u = net::parse_url(url).ok()?;
    let (status, _headers, body) = net::http_keepalive("GET", &u, None, &[], Duration::from_secs(20)).ok()?;
    if status != 200 {
        return None;
    }
    let doc: serde_json::Value = serde_json::from_slice(&body).ok()?;
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
    chain_figures_from(&doc, now)
}

/// F-E37: the algorithm a dial carries, never empty: the caller's hint (the re-dial's copy of the old session's),
/// else the source still held, else the settings' choice, else the workers' (network_algo). The re-dial class
/// (F-E33, F-E34c, F-E37: a pool source rebuilt from the state just torn down) is closed by this and by the signer
/// kept on the core: the new source is built from a copy of the running one's identity taken before the teardown.
pub fn resolve_dial_algo(hint: Option<String>, held: Option<String>, chosen: Option<String>, workers: impl FnOnce() -> String) -> String {
    hint.filter(|a| !a.is_empty())
        .or_else(|| held.filter(|a| !a.is_empty()))
        .or_else(|| chosen.filter(|a| !a.is_empty()))
        .unwrap_or_else(workers)
}

/// F-E36: the preferred worker backend for a vendor; a card listed by two workers keeps the slot on this one.
pub fn preferred_backend(vendor: &str) -> &'static str {
    match vendor.to_ascii_lowercase().as_str() {
        "nvidia" => "cuda",
        "apple" => "metal",
        "amd" | "intel" => "opencl",
        _ => "",
    }
}

/// F-E36: one slot per physical card. Two slots are the same card when their vendor matches and their PCI bus id
/// (normalised) or, without one, their device address matches; the CPU and the stub are never merged. The kept
/// slot is the one on the vendor's preferred backend (or the first seen when neither is); the dropped view is
/// named in the log.
fn dedupe_slots_by_address(slots: Vec<Slot>, mut log: impl FnMut(&str)) -> Vec<Slot> {
    let key = |d: &Device| -> Option<String> {
        if d.kind == "cpu" || d.backend == "stub" || d.vendor.is_empty() {
            return None;
        }
        let addr = if d.bus_id.len() >= 7 && d.bus_id.contains(':') { devices::normalise_bus(&d.bus_id) } else if !d.bus_id.is_empty() { d.bus_id.clone() } else { return None };
        Some(format!("{}|{}", d.vendor.to_ascii_lowercase(), addr))
    };
    let mut kept: Vec<Slot> = Vec::new();
    for s in slots {
        let Some(k) = key(&s.device) else { kept.push(s); continue };
        if let Some(i) = kept.iter().position(|o| key(&o.device).as_deref() == Some(k.as_str())) {
            let pref = preferred_backend(&s.device.vendor);
            let new_preferred = !pref.is_empty() && s.device.backend == pref && kept[i].device.backend != pref;
            if new_preferred {
                let dropped = std::mem::replace(&mut kept[i], s);
                log(&format!("{}: listed by two workers ({} and {}); one card, kept on {} (the vendor's worker), the {} view dropped", kept[i].device.name, dropped.device.backend, kept[i].device.backend, kept[i].device.backend, dropped.device.backend));
            } else {
                log(&format!("{}: listed by two workers ({} and {}); one card, kept on {}, the {} view dropped", s.device.name, kept[i].device.backend, s.device.backend, kept[i].device.backend, s.device.backend));
            }
            continue;
        }
        kept.push(s);
    }
    kept
}

/// The pause cause's word for the log line (F-E22).
pub fn pause_cause_word(c: PauseCause) -> &'static str {
    match c {
        PauseCause::Rate => "the tariff schedule",
        PauseCause::Game => "a game in the foreground",
        PauseCause::Battery => "the battery",
        PauseCause::Sleep => "the machine's sleep",
        PauseCause::User => "the owner's pause",
        PauseCause::Heat => "the room at its set point",
        PauseCause::Memory => "the memory guard",
        _ => "the work source unreachable",
    }
}

#[derive(Clone)]
pub struct Supervisor {
    inner: Arc<Mutex<Core>>,
    /// The watchdog's mark when the core's lock is wedged (the tick holding it without returning).
    wedged: Arc<Mutex<Option<String>>>,
    /// When the tick took the lock, for the watchdog's judgement of a wedged tick (None between ticks).
    lock_held_since: Arc<Mutex<Option<Instant>>>,
    /// The tick's named synchronous phase while one runs (a worker's launch with its hello wait, a verifier's build
    /// with the reference's vectors check): the watchdog's bound for a tick in a phase is PHASE_BOUND, never TICK_STALL
    /// (the GPU team's row of 02:17 UK, 16 October 2026: the tick held 46 s and 149 s across two documents' verifier builds on
    /// a rented host's contended cores and the watchdog ended mining as a wedge; the engine was working, not wedged).
    sync_phase: Arc<Mutex<Option<(Instant, String)>>>,
}

/// A work source unreachable for this long hands over to the backup the user named.
pub const FAILOVER_AFTER: Duration = Duration::from_secs(60);
/// On the backup, the first source is tried again this often.
pub const PRIMARY_RETRY_EVERY: Duration = Duration::from_secs(600);

/// A node session's submission cap: the node's per-address cap of 256 shares a height over 20 s blocks is 12.8 a
/// second, so the lane's 20 a second met "address at cap" refusals (the Linux proof of 2aed47ce on an RTX 3060, 12
/// October 2026: 904 of 1,667 submissions refused so); a block is never held back.
pub const SUBMIT_CAP_PER_S: u32 = 12;
/// A pool session's submission cap: the node's per-address cap of 256 shares a height over 20 s blocks, about 12 a
/// second, which the pool cannot raise for a session (ecosystem's reading of the mini's first outside session,
/// 12 October 2026); the pool's disclosure will carry share_cap_per_height and block_seconds for the exact pace.
pub const POOL_SUBMIT_CAP_PER_S: u32 = 12;
/// F-E66: the public pool's own submit bucket (50 a second, a burst of 50; pool 0.1.47); the engine's cap only after
/// the pool paced a share, for a minute.
pub const POOL_BUCKET_PER_S: u32 = 50;
/// F-301's engine half: the coordinate a card opens on. Its routed coordinate when it has one; else, for a base worker,
/// the lowest coordinate whose document version is not bound to another worker (a bound-only version runs on its own
/// worker, never the base); else the lowest coordinate. None when the table is empty (the session document stands in).
/// F-478 (the Windows proof hosts under 0.2.29 to 0.2.33, 17 October 2026: a card mining the programme lane on the
/// version 21 worker was stopped by the row's source step and reopened; the slot kept that bound worker, the first
/// open took the LOWEST served coordinate for it, the raster lane 5 (version 18), and the version 21 worker was
/// configured on the raster document: "the full dataset needs 0.00 GB", no hash, no stat, the settle and the build's
/// allowance holding the card there, the watchdog relaunching the same worker on the same lane): the first open
/// takes a coordinate the worker's own version serves; a bound worker with none goes back to the base worker's rule.
pub fn first_coord_for_worker(versions: &[(u8, Option<u64>)], routed: Option<u8>, bound_version: Option<u64>) -> Option<u8> {
    match bound_version {
        None => first_coord_for_base(versions, routed, true),
        Some(bv) => {
            let serves = |c: u8| versions.iter().any(|(k, v)| *k == c && *v == Some(bv));
            if let Some(c) = routed {
                if serves(c) {
                    return Some(c);
                }
            }
            let mut sorted: Vec<&(u8, Option<u64>)> = versions.iter().filter(|(_, v)| *v == Some(bv)).collect();
            sorted.sort_by_key(|(c, _)| *c);
            sorted.first().map(|(c, _)| *c)
        }
    }
}

/// F-478: a coordinate whose document version is bound to a worker absent from the install is never a card's
/// candidate (the route and the probe alike): the lanes the install's workers cannot serve, from the served set.
pub fn coords_without_a_worker(versions: &[(u8, Option<u64>)], base_stem: &str, has_bound_worker: &dyn Fn(u8, Option<u64>) -> bool) -> Vec<u8> {
    versions.iter().filter(|(c, v)| coordinate_needs_absent_bound_worker_for(base_stem, *v, has_bound_worker(*c, *v))).map(|(c, _)| *c).collect()
}

pub fn first_coord_for_base(versions: &[(u8, Option<u64>)], routed: Option<u8>, is_base: bool) -> Option<u8> {
    if let Some(c) = routed {
        if versions.iter().any(|(k, _)| *k == c) {
            return Some(c);
        }
    }
    let mut sorted: Vec<&(u8, Option<u64>)> = versions.iter().collect();
    sorted.sort_by_key(|(c, _)| *c);
    if is_base {
        return sorted.iter().find(|(_, v)| !matches!(v, Some(v) if BOUND_ONLY_VERSIONS.contains(v))).map(|(c, _)| *c);
    }
    sorted.first().map(|(c, _)| *c)
}

/// F-301's engine half, the first open (the Octominer rig's RX 9060 XT, 16 October 2026, twice): a base worker whose only
/// known document is bound to another worker (no registered coordinate it serves and a bound-only session document) is not
/// opened yet; the slot waits and the tick opens it when a coordinate it serves registers.
pub fn base_open_waits(versions: &[(u8, Option<u64>)], routed: Option<u8>, is_base: bool, session_version: Option<u64>) -> bool {
    is_base && first_coord_for_base(versions, routed, is_base).is_none() && matches!(session_version, Some(v) if BOUND_ONLY_VERSIONS.contains(&v))
}

/// A card's refusal of a coordinate lifts when the coordinate registers a different document (a new digest, or the
/// coordinate's first registration); a re-registration of the same document (a period anchor change) keeps it.
pub fn refusal_lifts(registered_digest: Option<&str>, new_digest: &str) -> bool {
    registered_digest.map_or(true, |d| d != new_digest)
}

/// The verify-queue cell (0.2.24): the verifier threads for a host with `cores` logical processors, two to eight.
pub fn verify_threads_for(cores: usize) -> usize {
    cores.clamp(2, 8)
}

/// The verify-queue cell: whether a result queued now would be checked after `bound_ms` (the queue's depth times the
/// threads' mean check time over the thread count); an unmeasured check (0) never exceeds.
pub fn verify_wait_exceeds(depth: usize, check_us: u64, threads: usize, bound_ms: u64) -> bool {
    if check_us == 0 || threads == 0 {
        return false;
    }
    (depth as u64).saturating_mul(check_us) / (threads as u64) > bound_ms.saturating_mul(1000)
}

/// F-271: the templates a slot may outlive with only late results before it is restarted on a fresh job.
pub const LATE_TEMPLATES_RESTART: u32 = 2;

/// F-383: a launch's result, sent from its thread to the tick.
struct LaunchDone {
    slot: usize,
    gen: u64,
    result: Result<WorkerProcess, WorkerError>,
}

/// F-383: a launch's result is finished into its slot only while the slot still holds that launch's generation; a
/// slot closed or re-opened meanwhile ends the arriving process instead of taking it.
pub fn launch_arrival(pending: Option<u64>, gen: u64) -> bool {
    pending == Some(gen)
}

/// F-471 (the second Linux proof host under 0.2.25, 17 October 2026: eight launches off the tick at the same instant
/// on a two-core host, and no worker said hello within its silence wait; 34 launches failed over 64 and the start
/// sequence repeated every four to five minutes): the launches allowed in flight at once on a host with `cores`
/// logical processors, half the cores and at least one.
pub fn launch_cap_for(cores: usize) -> usize {
    (cores / 2).max(1)
}

/// F-471: whether another launch may spawn now with `in_flight` launches running under `cap`.
pub fn launch_may_spawn(in_flight: usize, cap: usize) -> bool {
    in_flight < cap.max(1)
}

/// F-475 (the second Linux proof host's all-cards row of 0.2.28, 17 October 2026: the gate's one slot went to the
/// NVIDIA cards' re-launches every time, lane move after lane move, in slot order and by newcomers spawning the moment
/// a slot was free, so the two AMD cards queued for the whole thirty minutes): the gate is a queue in ARRIVAL order.
/// A newcomer spawns at once only when nothing is queued and the gate has room; otherwise it queues behind the rest.
pub fn launch_spawns_now(in_flight: usize, cap: usize, queued: usize) -> bool {
    queued == 0 && launch_may_spawn(in_flight, cap)
}

/// F-475: the queued launch that goes next: the lowest generation (the earliest arrival), never the lowest slot.
pub fn next_queued_launch(queued: &[(usize, u64)]) -> Option<usize> {
    queued.iter().min_by_key(|(_, gen)| *gen).map(|(i, _)| *i)
}

/// F-384: a re-dial lands on the same pool when the new session's name equals the old one's (the same endpoint and
/// chain); its lanes' contexts and the cards' routes are kept.
pub fn same_session(old: Option<&str>, new: &str) -> bool {
    old.map_or(false, |o| !o.is_empty() && o == new)
}

/// F-384: a lane quiet for the 90 s rule is paused once (a paused lane is not paused again each tick).
pub fn lane_pauses(already_paused: bool) -> bool {
    !already_paused
}

/// F-437 (the Mac proof host on the raster lane, 17 October 2026 01:12 UK: the settle rule holding a card on a lane
/// without a bound) re-cut on F-439 (the same card was HASHING there at 880 attempts a second with its shares minutes
/// apart at the pool's step): the settle's clock reads the worker's WORK, never its shares: a card with no attempts
/// (no stat reporting hashes) for DEAD_LANE_S while the lane's jobs are fresh moves on and refuses that lane for the
/// session; a hashing card with no share waits on the pool's step (the pool's silence decay lowers it).
pub const DEAD_LANE_S: u64 = 300;
pub fn lane_dead(since_work_s: u64, lane_fresh: bool, probing: bool) -> bool {
    !probing && lane_fresh && since_work_s >= DEAD_LANE_S
}
/// 0.2.27 (the security review's residual on F-437): a card on its pinned coordinate is exempt from the bounded settle.
pub fn settle_exempt(pinned: Option<u8>, coord: u8) -> bool {
    pinned == Some(coord)
}

/// F-383's second half (the A5000 on the second Octominer rig, 16 October 2026: three "no stat for 10 s" judged across
/// frozen ticks closed a sound card): a tick that did not run for longer than the silence it judges counts no hang.
pub const HANG_SILENCE: Duration = Duration::from_secs(10);
pub fn tick_ran_late(dt: Duration) -> bool {
    dt > HANG_SILENCE
}

/// F-470 (the first Windows proof host's 0.2.25 row and the second's 0.2.25 and 0.2.26 rows, 17 October 2026: the
/// card at 100 percent and 292 W while the engine counted "no stat for 10 s" three times in an hour and closed it;
/// the last engine line each time the period's anchor change): a worker that takes the period's anchor rebuilds its
/// programme on the card and says nothing while it does, longer than the ten-second rule; the stat clock is bounded
/// by the build's allowance (the hello bound) from the set until the worker's first mining stat after it. The
/// silence a slot may show before a hang is counted: the build's allowance while one stands, the ten-second rule else.
/// F-476 (the second Windows proof host under the 0.2.29 re-cut, 17 October 2026: after the row's stop and start the
/// relaunched worker's hello came four seconds later, before the re-dialled source had registered a coordinate
/// document, and the configure went out on the empty session document: "the full dataset needs 0.00 GB", a worker at
/// 450 W hashing nothing, every watchdog relaunch the same; on 0.2.24 the launch inside the tick always followed the
/// dial): a worker is never configured on an empty document. With no coordinate document registered and a session
/// document that names no version, the launched worker waits, configured when the first document registers.
pub fn configure_waits_for_document(coord_doc_registered: bool, session_doc_names_version: bool) -> bool {
    !coord_doc_registered && !session_doc_names_version
}
/// The allowance is set at every configure (a fresh or re-dialled worker compiles and builds before its first stat;
/// the first Windows proof host's 0.2.25 log: the rule fired fifteen seconds into the compile after a source change)
/// and at every anchor set; it ends at the worker's first mining stat with hashes.
/// F-479 (the Windows proof hosts under 0.2.33, 17 October 2026: a worker configured on the wrong document stayed
/// "starting" for the whole window at full draw, no stat ever came, and no rule restarted it: the hang rule counts
/// mining states alone and the build's allowance widens their clock): a slot still opening with no stat once its
/// build's allowance has run out is restarted with the fault named. Pure: the allowance's end, the stat clock.
/// F-562 (the Mac proof host on the public pool's raster lane, 17 October 2026: the worker hashed at its full rate for 45
/// minutes and then 27 minutes more after a relaunch and found no result at all, nothing refused, nothing to submit; the
/// pool's pay under the chain's minimum rule stopped twice): the silence bound of a hashing worker, from the lane's
/// expected share interval (the share weight of the lane's share target over the card's rate) when known: six expected
/// intervals (a chance silence that long is one window in four hundred), never under SILENCE_FLOOR_S and with no cap
/// (the security review's read of 0f069109: a cap under six intervals refuses slow cards on hard lanes by chance); the floor
/// alone when the interval is not known.
pub const SILENCE_FLOOR_S: u64 = 600;
pub const SILENCE_INTERVALS: f64 = 6.0;
pub fn result_silence_bound(expected_share_s: Option<f64>) -> Duration {
    let s = match expected_share_s {
        Some(e) if e.is_finite() && e > 0.0 => (SILENCE_INTERVALS * e).max(SILENCE_FLOOR_S as f64),
        _ => SILENCE_FLOOR_S as f64,
    };
    Duration::from_secs(s as u64)
}

/// F-562: a mining slot whose worker reports work (a rate above zero) and has found no result for longer than the bound
/// since the later of its stint's start and its last result is silent: its worker is restarted with the fault named.
pub fn silent_while_hashing(mining: bool, rate: f64, since_stint_or_result: Duration, bound: Duration) -> bool {
    mining && rate > 0.0 && since_stint_or_result > bound
}

/// F-562: the silence clock's start: the later of the stint's start and the last result (a silent restart sets the last
/// result to its own time, so the restarted worker gets a full bound before it is judged again).
pub fn silence_clock(stint_since: Instant, last_share_at: Option<Instant>) -> Instant {
    match last_share_at {
        Some(t) if t > stint_since => t,
        _ => stint_since,
    }
}

/// F-562: after this many silent restarts on one coordinate the lane is refused for the card (its worker yields nothing
/// there; the router moves the card and coverage sends another).
pub const SILENT_RESTARTS_BEFORE_REFUSAL: u32 = 2;

/// F-564 (the Mac proof host on the public pool's raster lane, 17 October 2026: the worker found and emitted shares for
/// an hour and the app cancelled every one, silently, as a superseded job's): a share for a job of the slot's own
/// coordinate at the live template's height, or one height behind it, is still valid work and goes to the verifier
/// and the pool (the pool answers accepted or stale, in the open); a job of an earlier height, or of another
/// coordinate than the slot mines, is superseded and cancelled, and the cancel is said once per job.
pub const STILL_VALID_HEIGHTS: u64 = 1;
/// F-695 (the tee capture on the first Windows proof host, 18 October 2026): on a pool session the pool's notify
/// can step two heights inside one template interval (12,765 to 12,767 in 12.5 s, the height between never
/// notified) while a raster result's reference check takes seconds, so two valid shares were cancelled as late one
/// tick after they were found. The pool keeps a member's last POOL_JOBS_RETAINED jobs across every coordinate and
/// accepts a share on any of them whatever its height (its own "stale" is a job no longer in that list), so on a
/// pool session a result is still valid while its job is among the last POOL_JOBS_RETAINED distinct pool jobs the
/// session was sent, and the pool's answer is the word; a solo source keeps the one-height rule.
pub const POOL_JOBS_RETAINED: usize = 8;
/// The last `n` distinct pool job ids among the issued ids (newest last), for the pool's own retained window.
pub fn recent_pool_jobs<'a>(issued_newest_last: impl Iterator<Item = &'a str>, n: usize) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for id in issued_newest_last {
        let pool_id = id.split('.').next().unwrap_or(id).to_string();
        if !out.contains(&pool_id) {
            out.push(pool_id);
            if out.len() >= n { break; }
        }
    }
    out
}
pub fn superseded_job(sent_coord: Option<u8>, sent_height: u64, live_coord: Option<u8>, live_height: u64) -> bool {
    sent_coord != live_coord || sent_height + STILL_VALID_HEIGHTS < live_height
}

pub fn opening_past_allowance(opening: bool, build_until_passed: bool, since_last_stat: Duration) -> bool {
    opening && build_until_passed && since_last_stat > worker::HELLO_BOUND
}

pub fn hang_silence_bound(in_build: bool) -> Duration {
    if in_build { worker::HELLO_BOUND } else { HEARTBEAT_TIMEOUT }
}

/// F-271: the pool's bucket cap for a result, if the result's own session (its coordinate) was paced and the minute
/// has not passed; a pace on another session is not this one's.
pub fn paced_cap_for(caps: &BTreeMap<Option<u8>, (u32, Instant)>, coord: Option<u8>, now: Instant) -> Option<u32> {
    caps.get(&coord).and_then(|(cap, until)| if now < *until { Some(*cap) } else { None })
}

/// F-271: a slot's run of late results. `advance` is called for every late result with the slot's live template id;
/// it answers true once the run has outlived LATE_TEMPLATES_RESTART templates (the caller restarts the slot and
/// resets the run); a current result resets the run.
#[derive(Default, Clone, Debug)]
pub struct LateRun {
    template: Option<String>,
    outlived: u32,
}

/// F-290: the seconds a coordinate's jobs may go unrefreshed by the pool, while another coordinate's are fresh, before
/// it counts as withdrawn (the pool offers no job on an unpaid coordinate; a block's cadence is 12 s).
pub const COORD_WITHDRAWN_S: u64 = 90;

/// F-290 (the 2,860 succession, 16 October 2026: the router "restored" the retired coordinate 4 as live-but-empty every
/// few seconds, a worker relaunch each time, the dead lane's shares stale). The coordinates gone for this miner: retired by
/// the registry's retired_from at or below the head (the authority: the app, the pool and the ledger read it alike; final),
/// or withdrawn by the pool, which refreshes no job on an unpaid coordinate while it refreshes another's (it returns when a
/// job returns). Neither enters the router's table; neither is restored as empty. A job's last_height is never read as a
/// retirement: a live lane sits behind the head between its blocks.
pub fn retired_coords(retired_from: &BTreeMap<u8, u64>, issued: &BTreeMap<u8, Instant>, head: u64, now: Instant) -> BTreeSet<u8> {
    let mut out = BTreeSet::new();
    for (c, h) in retired_from {
        if head >= *h {
            out.insert(*c);
        }
    }
    let freshest = issued.values().map(|t| now.saturating_duration_since(*t)).min();
    if let Some(f) = freshest {
        if f < Duration::from_secs(COORD_WITHDRAWN_S / 3) {
            for (c, t) in issued {
                if now.saturating_duration_since(*t) > Duration::from_secs(COORD_WITHDRAWN_S) {
                    out.insert(*c);
                }
            }
        }
    }
    out
}

/// F-290 with the pool's pay list (0.1.76): the coordinates held that the list does not pay.
pub fn unpaid_coords(held: &[u8], paid: &[u8]) -> Vec<u8> {
    held.iter().filter(|c| !paid.contains(c)).cloned().collect()
}

pub fn late_run_advance(run: &mut LateRun, live: Option<&str>) -> bool {
    match (run.template.as_deref(), live) {
        (None, Some(l)) => {
            run.template = Some(l.to_string());
            run.outlived = 0;
        }
        (Some(r), Some(l)) if r != l => {
            run.outlived += 1;
            run.template = Some(l.to_string());
        }
        _ => {}
    }
    run.outlived >= LATE_TEMPLATES_RESTART
}
/// 0.2.11: held results a card may have checked a second (the no-result watch's view of a card the pace holds back).
pub const HELD_VERIFY_PER_S: u32 = 2;
/// F-E23: the share target must have stood this long before held-back results are called the pool's fault.
pub const TARGET_STOOD_FOR: Duration = Duration::from_secs(300);
/// F-110 (closed 14 October 2026): the OpenCL workers that carry the dataset-leak fix (gpu-b/design f7da4b90; the
/// proof on the rig's RX 5700 XT, 8 GB: version 15 across eight epochs, EXACT 101 of 101, the worker's RSS 416 MB
/// flat). The coordinate-1 pin on 8 GB AMD cards lifts on these files alone; a later pair is added here with its proof.
pub const F110_FIXED_OPENCL_WORKERS: &[&str] = &[
    "2ba5ecea504d17986a9a9ea6f7016303064b62fc57dcce9571f7f260604c03d0", // Linux, igneum-worker-opencl
    "660ea2d6318289ee7e15a4a0ac0c9f271949f9a029cacee70b2741b2920a85c6", // Windows, igneum-worker-opencl.exe
];
/// A pool session's submissions are spaced, never a burst: the cap released at one tick as twelve shares in the same
/// millisecond read as a flood to the pool's rate rule (an RTX 5090 on the public pool, 12 October 2026: 348
/// submitted in 101 s, at most 12 in any second, 204 refused as "rate").
pub const POOL_SUBMIT_SPACING: Duration = Duration::from_millis(85);
/// How long an enabled slot may sit idle with no worker under a running layer before the tick reopens it (the
/// mini on the public pool, 12 October 2026: an hour without a worker, a word or a refusal).
pub const IDLE_REOPEN_AFTER: Duration = Duration::from_secs(60);
/// The grace after a slot's period transition in which an invalid result is a straddle (the last batch under the
/// old programme), not a fault of the worker's hash.
pub const TRANSITION_GRACE_S: u64 = 30;
/// Invalid results on a document before any valid one are straddles of the switch up to this many; past it the
/// worker's hash on the document is wrong and the coordinate's refusal (F-E56) proceeds.
pub const SWITCH_INVALID_MAX: u32 = 20;
/// Whether an invalid result at `now` straddles the slot's period transition: the transition still in progress, or
/// finished within TRANSITION_GRACE_S.
pub fn straddles_transition(transition_current: bool, done_at: Option<Instant>, now: Instant) -> bool {
    !transition_current || done_at.map_or(false, |t| now.saturating_duration_since(t) < Duration::from_secs(TRANSITION_GRACE_S))
}

/// F-E64: a worker gone within this many seconds of its start is a quick exit.
pub const QUICK_EXIT_S: u64 = 30;
/// The rate a first session declares per enabled card when no earlier run's rate is stored (10 MH/s: under a 5090's
/// rate on the public pool's work, above an Apple card's; the pool's retarget settles either way within a few heights).
pub const FIRST_DIAL_NOMINAL_HPS_PER_CARD: u64 = 10_000_000;

/// The pool retry while it is unreachable: the first wait, doubled each silent turn up to the bound (the project's order
/// of 12 October 2026: 30 s, 60 s, up to five minutes; the card is not touched between turns).
pub const SOURCE_RETRY_FIRST: Duration = Duration::from_secs(5); // F-E14d (reliability's read of both PCs idle 90 s after a pool-side close, 13 October 2026): 5 s, then doubled
/// The backoff's ceiling (reliability's read of the two-card test machine, 02:35 UK 13 October 2026: a 300 s retry
/// that never connected for three hours is a dead worker; 120 s now, and every failed dial logs its own error).
pub const SOURCE_RETRY_MAX: Duration = Duration::from_secs(120);
/// A session that ends to RELOAD its document (the pool's chain or document changed, or a run of invalid refusals)
/// re-dials in this wait with no backoff: it is not unreachability. More than RELOADS_BEFORE_BACKOFF reloads inside
/// ten minutes fall back to the backoff (a pool refusing every share is not fixed by reconnecting).
pub const SOURCE_RELOAD_WAIT: Duration = Duration::from_secs(5);
pub const RELOADS_BEFORE_BACKOFF: usize = 3;

/// The submission pacer for a pool session: a second's window of at most `cap` submissions, and `spacing` between
/// any two. `admit` says whether a result found `now` goes out, counting it when it does.
#[derive(Debug, Default)]
pub struct SubmitPace {
    window: Option<(Instant, u32)>,
    last: Option<Instant>,
}

impl SubmitPace {
    pub fn admit(&mut self, now: Instant, cap: u32, spacing: Duration) -> bool {
        let (start, n) = match self.window {
            Some((t, n)) if now.duration_since(t) < Duration::from_secs(1) => (t, n),
            _ => (now, 0),
        };
        if n >= cap || self.last.map_or(false, |l| now.duration_since(l) < spacing) {
            self.window = Some((start, n));
            return false;
        }
        self.window = Some((start, n + 1));
        self.last = Some(now);
        true
    }
}

/// Whether a result is a block: never on the share lane (a share never becomes a block, rule 2.1a), and never on a
/// pool session (the pool's job names its share target as the boundary, so every result would read as a block; a
/// block through a pool is the pool's); on a node session, when the hash meets the block boundary.
pub fn counts_as_block(lane_share: bool, solo: bool, meets_boundary: bool) -> bool {
    !lane_share && solo && meets_boundary
}

/// A submission unanswered for this long, with the source reporting no problem, is a silent source.
pub const UNANSWERED_AFTER: Duration = Duration::from_secs(120);
/// F-301b's second face (the Linux proof host's one-card row on 0.2.22-1, 17 October 2026 01:25 UK: the session's first
/// document was the raster one, the lone card waited for a document its worker serves, the other lanes registered six
/// seconds later, and the card never reopened: the reopen path ran only once the engine was Mining, which it becomes
/// only once a card mines): a slot's reopen (a backed-off restart, the idle reopen) runs while the engine is Starting too.
pub fn reopen_allowed(mining: &MiningState) -> bool {
    matches!(mining, MiningState::Mining | MiningState::Starting)
}

/// F-392: a session mining with a reported rate whose results are refused as "target too low" for this long with no
/// accepted share is at a step the pool raised past the card (a first burst at the lowest step read as rate).
pub const STEP_SILENCE: Duration = Duration::from_secs(300);
/// F-392: from this long after a dial, a declared rate under a tenth of the reported one with no share since the dial
/// is the opening step's mismatch.
pub const STEP_OPENING: Duration = Duration::from_secs(120);
/// F-392: a step re-dial at most this often.
pub const STEP_REDIAL_EVERY: Duration = Duration::from_secs(600);
/// F-392: the pool resumes a worker's standing step on a re-dial within its grace (120 s, pool 0.1.79); the session
/// stays closed this long so the next dial opens at the declared rate's step.
pub const STEP_AWAY: Duration = Duration::from_secs(125);

/// F-392: the step mismatch, if any: the reported rate must be above zero; `since_mark` is the time since the later of
/// the dial and the last accepted share; `no_share_since_dial` whether no share was accepted on this session;
/// `refused_rose` whether results were refused as too-low-target since the mark. Returns the words of the mismatch.
pub fn step_mismatch(declared_hps: u64, reported_hps: u64, since_dial: Duration, since_mark: Duration, no_share_since_dial: bool, refused_rose: bool) -> Option<String> {
    if reported_hps == 0 {
        return None;
    }
    if since_mark >= STEP_SILENCE && refused_rose {
        return Some(format!("no share accepted for {} s while the card reports {:.2} MH/s and its results are refused as too-low-target", since_mark.as_secs(), reported_hps as f64 / 1e6));
    }
    if since_dial >= STEP_OPENING && no_share_since_dial && declared_hps.saturating_mul(10) < reported_hps {
        return Some(format!("the session was opened declaring {:.2} MH/s, under a tenth of the {:.2} MH/s the card reports, and no share was accepted since the dial", declared_hps as f64 / 1e6, reported_hps as f64 / 1e6));
    }
    None
}
/// F-E14: a connected source that has sent no job for this long is redialled (the pool's job stream stalls silently).
pub const NO_WORK_AFTER: Duration = Duration::from_secs(60);

/// How many accepted shares the snapshot carries for the scene (api/scene): about four minutes of a fast card's
/// shares at the pool's cap of twelve a second, hours of a slow one.
pub const RECENT_SHARES: usize = 256;

/// A mining worker whose every stat reports zero hashes on the job it holds for this long is restarted (its own
/// word, ahead of the averaged rate: a worker that stops after a cancel keeps a decaying rate for minutes).
pub const IDLE_HASHES_AFTER: Duration = Duration::from_secs(30);

/// The idle-worker verdict: zero-hash stats since `since` and no validated result since `last`, both longer ago
/// than IDLE_HASHES_AFTER (a fresh result within the bound means the stats lag, not the card).
pub fn idle_hashes_too_long(since: Instant, last_validated: Instant, now: Instant) -> bool {
    now.duration_since(since) > IDLE_HASHES_AFTER && now.duration_since(last_validated) > IDLE_HASHES_AFTER
}

/// A tick that has not beaten for this long while mining is a stall the watchdog names.
pub const TICK_STALL: Duration = Duration::from_secs(120);
/// A tick inside a named synchronous phase (a worker's launch, whose hello wait runs to worker::HELLO_BOUND; a
/// verifier's build) is not wedged until this long after the phase began: the phase's own bound plus the stall.
pub const PHASE_BOUND: Duration = Duration::from_secs(600 + 120);
/// The watchdog's rule: a lock held longer than the stall is a wedge, unless the tick is in a named phase that began
/// less than PHASE_BOUND ago (pure, for the test).
pub fn tick_is_wedged(held_for: Duration, phase: Option<(Duration, &str)>) -> bool {
    match phase {
        Some((since, _)) => since > PHASE_BOUND,
        None => held_for > TICK_STALL,
    }
}

impl Supervisor {
    pub fn new(cfg: Config) -> Supervisor {
        let (settings, settings_warning) = match Settings::load(&cfg.settings_path()) {
            Ok(s) => (s, None),
            Err(e) => (Settings::default(), Some(e.to_string())),
        };
        let tune_store = TuneStore::load(&cfg.tune_path());
        let journal = journal::Journal::open(cfg.journal_path());
        let (warm_tx_init, warm_rx_init) = std::sync::mpsc::channel::<String>();
        let (launch_tx_init, launch_rx_init) = std::sync::mpsc::channel::<LaunchDone>();
        let mut core = Core {
            cfg,
            pace_chain_target: pace_chain_target_from_env(),
            settings,
            tune_store,
            journal,
            slots: Vec::new(),
            mining: MiningState::Idle,
            paused_for: None,
            source: None,
            verifier: None,
            current: None,
            sent: HashMap::new(),
            sent_order: VecDeque::new(),
            submitted: HashMap::new(),
            submitted_seen: HashMap::new(),
            recent_shares: VecDeque::new(),
            dev_fee_jobs: std::collections::HashSet::new(),
            submitted_at: HashMap::new(),
            last_submit: None,
            last_outcome: None,
            unanswered_since: None,
            dialed_at: None,
            declared_rate_hps: 0,
            last_accepted_at: None,
            not_submitted_at_mark: 0,
            step_redial_at: None,
            step_hold_until: None,
            last_job_at: None,
            no_work_redials: 0,
            submit_pace: SubmitPace::default(),
            warm_tx: warm_tx_init,
            warm_rx: warm_rx_init,
            totals: Totals::default(),
            log: VecDeque::new(),
            log_seq: 0,
            notices: Vec::new(),
            stages: Vec::new(),
            first_work_accepted: false,
            started_at: None,
            fee_record_start: 0,
            fee_record_failed: false,
            last_tick: Instant::now(),
            downtime: Duration::ZERO,
            game_gone_since: None,
            pressure_source: Box::new(pressure::level),
            pressure_paused_at: None,
            memory_pressure: None,
            probe: Box::new(NoProbe),
            tune_running: false,
            stop_flag: false,
            transitions: VecDeque::new(),
            params_digest: "default".into(),
            tick_times: VecDeque::new(),
            algo_params: serde_json::Value::Null,
            session_anchor: None,
            refused_bond_key: None,
            refused_digest: None,
            verify_latencies: VecDeque::new(),
            verify_window: VecDeque::new(),
            late_since: None,
            launch_errors: Vec::new(),
            source_problem_since: None,
            source_reloads: Vec::new(),
            source_retry: None,
            chain_balance_ign: None,
            chain_figures: None,
            ledger_paid: None,
            update_offer: None,
            signer: None,
            unverified_at_last_watch: 0,
            update_offer_said: String::new(),
            share_target_since: None,
            target_warned: false,
            coords: BTreeMap::new(),
            lane_facts_said: BTreeMap::new(),
            cancelled_said: std::collections::HashSet::new(),
            router_last: (0, BTreeMap::new()),
            router_history: Vec::new(),
            on_backup_since: None,
            primary_retry_at: None,
            tick_beat: Instant::now(),
            sync_phase: Arc::new(Mutex::new(None)),
            tick_phase_longest: None,
            tick_fault: None,
            tick_faults: 0,
            verify_tx: None,
            verify_tx_measure: None,
            verify_rx: None,
            launch_tx: launch_tx_init,
            launch_rx: launch_rx_init,
            launch_seq: 0,
            launch_cap: launch_cap_for(thread::available_parallelism().map(|n| n.get()).unwrap_or(2)),
            verify_depth: Arc::new(AtomicUsize::new(0)),
            verify_check_us: Arc::new(AtomicU64::new(0)),
            verify_threads: 2,
            verify_dropped_said: None,
            pin_refused_said: None,
            paced_said: None,
            last_rate_saved: None,
            pool_bucket_caps: BTreeMap::new(),
            plain_template_seen: false,
            pin_refused: false,
            first_coord_template_at: None,
            coord_checks: Vec::new(),
            coord_builds: BTreeMap::new(),
            stale_anchor_said: None,
            checked_digests: std::collections::BTreeSet::new(),
            registry_rx: None,
            registry: None,
            registry_docs: BTreeMap::new(),
            retired_coords: BTreeSet::new(),
            registry_said: None,
            registry_failed_said: None,
        };
        // the dev fee's words (devfee.rs): the percent, the address and the sentence the start line and the app
        // carry; the counts come with the session (and the lines are set again after every session reset)
        fee_lines_into(&mut core.totals, &core.cfg.dev_fee);
        if let Some(w) = settings_warning {
            core.notice("warn", "Settings could not be read; defaults are in use.", &w);
        }
        let line = format!("{} {} started", core.cfg.app_name, core.cfg.version);
        core.log("app", &line);
        let _ = core.journal.record("app_opened", "");
        let sync_phase = core.sync_phase.clone();
        Supervisor { inner: Arc::new(Mutex::new(core)), wedged: Arc::new(Mutex::new(None)), lock_held_since: Arc::new(Mutex::new(None)), sync_phase }
    }

    pub fn set_fullscreen_probe(&self, p: Box<dyn FullscreenProbe>) {
        lock(&self.inner).probe = p;
    }

    pub fn settings(&self) -> Settings {
        lock(&self.inner).settings.clone()
    }

    /// Change settings through a closure; normalised, validated and saved, or refused with the reason and
    /// nothing changed.
    pub fn update_settings<F: FnOnce(&mut Settings) -> Result<(), String>>(&self, f: F) -> Result<(), SupError> {
        let mut c = lock(&self.inner);
        let mut next = c.settings.clone();
        f(&mut next).map_err(SupError::Message)?;
        next.normalise();
        next.validate()?;
        next.save(&c.cfg.settings_path())?;
        c.settings = next;
        c.apply_settings_to_slots();
        Ok(())
    }

    pub fn inventory(&self) -> Result<Vec<SlotView>, SupError> {
        let mut c = lock(&self.inner);
        c.inventory()?;
        Ok(c.slot_views())
    }

    pub fn start(&self) -> Result<(), SupError> {
        lock(&self.inner).start()
    }
    pub fn stop(&self) {
        lock(&self.inner).stop("stopped");
    }
    pub fn pause(&self) {
        lock(&self.inner).pause(PauseCause::User);
    }
    /// A pause with its cause named by the caller (the old base's engine: heat mode's rest); the schedule holds it
    /// as it holds a user's pause, and only the caller's resume lifts it.
    pub fn pause_for(&self, cause: PauseCause) {
        lock(&self.inner).pause(cause);
    }
    pub fn resume(&self) {
        lock(&self.inner).resume();
    }
    /// The payout address's chain balance in IGN for the Earnings lines (None: not read).
    pub fn set_chain_balance(&self, ign: Option<f64>) {
        lock(&self.inner).chain_balance_ign = ign;
    }
    /// F-E20: the chain's figures for the pool estimate, from the supervisor's own feed poll or a caller's read.
    pub fn set_chain_figures(&self, f: Option<economics::ChainFigures>) {
        lock(&self.inner).chain_figures = f;
    }
    /// F-E20: the supervisor's own read of the chain's feed for the pool estimate, every CHAIN_FIGURES_EVERY on a
    /// thread of its own (the command line has no app to push them); a feed that does not answer leaves the
    /// figures as they were, and the first read is immediate.
    pub fn start_chain_figures_poll(&self) {
        let inner = self.inner.clone();
        std::thread::Builder::new().name("chain-figures".into()).spawn(move || loop {
            if let Some(f) = read_chain_figures(CHAIN_FEED_URL) {
                lock(&inner).chain_figures = Some(f);
            }
            // F-E25: the ledger's paid reading for the payout address on the job's chain (the address lowercased,
            // the per-chain path), when a job has named a chain and an address is set
            let (chain_id, address) = {
                let c = lock(&inner);
                (c.current.as_ref().map(|j| j.chain_id).unwrap_or(0), c.settings.payout_address.clone().unwrap_or_default())
            };
            if chain_id > 0 && !address.is_empty() {
                if let Some(l) = read_ledger_paid(LEDGER_URL_ROOT, chain_id, &address) {
                    lock(&inner).ledger_paid = Some(l);
                }
            }
            std::thread::sleep(CHAIN_FIGURES_EVERY);
        }).ok();
        // F-E24: the signed pointer read once an hour; a newer version is an offer in /state and one log line,
        // with the one command for this host; the service never replaces itself
        let inner = self.inner.clone();
        std::thread::Builder::new().name("update-offer".into()).spawn(move || loop {
            let (running, policy) = {
                let c = lock(&inner);
                (c.cfg.version.clone(), format!("{:?}", c.settings.update_policy).to_lowercase())
            };
            if let Some(offer) = read_update_offer(POINTER_URL, &running, &policy) {
                let mut c = lock(&inner);
                if c.update_offer_said != offer.version {
                    c.update_offer_said = offer.version.clone();
                    let line = format!("an update is available: Ember {} (this is {}); take it with: {}", offer.version, running, offer.command);
                    c.log("app", &line);
                    if policy == "auto" {
                        c.log("app", "update policy auto: on a host with no window Ember does not replace itself; the command above, or the fleet's recorded update, takes it");
                    }
                }
                c.update_offer = Some(offer);
            } else {
                lock(&inner).update_offer = None;
            }
            std::thread::sleep(UPDATE_OFFER_EVERY);
        }).ok();
    }

    pub fn tick(&self) {
        lock(&self.inner).tick();
    }

    pub fn snapshot(&self) -> Snapshot {
        lock(&self.inner).snapshot()
    }


    pub fn journal_mark(&self, kind: &str, detail: &str) -> Result<(), SupError> {
        lock(&self.inner).journal.record(kind, detail).map_err(SupError::Message)
    }

    pub fn journal_export(&self) -> String {
        let c = lock(&self.inner);
        c.journal.export(&c.cfg.version)
    }

    pub fn log_tail(&self, n: usize) -> Vec<String> {
        let c = lock(&self.inner);
        let n = n.min(MAX_LOG_LINES);
        c.log.iter().rev().take(n).rev().cloned().collect()
    }

    /// The log lines after the `seq` returned last time (0 for all the ring holds), and the new seq: the app's
    /// tick drains them into its own log file (12 October 2026: the mini's Mac app stopped mining twice in one
    /// night with the reason in this ring and nowhere on disk). Lines the ring has already dropped are counted as
    /// lost in the first line returned.
    pub fn log_since(&self, seq: u64) -> (u64, Vec<String>) {
        let c = lock(&self.inner);
        let held = c.log.len() as u64;
        let first_held = c.log_seq.saturating_sub(held);
        let mut out = Vec::new();
        if seq < first_held {
            out.push(format!("({} supervisor lines before these were dropped from the ring)", first_held - seq));
        }
        let skip = seq.saturating_sub(first_held) as usize;
        out.extend(c.log.iter().skip(skip).cloned());
        (c.log_seq, out)
    }

    pub fn request_stop(&self) {
        let mut c = lock(&self.inner);
        c.stop("quit");
        c.stop_flag = true;
    }

    /// The ticker thread: four ticks a second until `request_stop`.
    pub fn spawn_ticker(&self) -> thread::JoinHandle<()> {
        let s = self.clone();
        thread::Builder::new().name("supervisor-tick".into()).spawn(move || s.run_forever()).expect("thread")
    }

    pub fn run_forever(&self) {
        loop {
            {
                let mut c = lock(&self.inner);
                if c.stop_flag {
                    return;
                }
                if let Ok(mut g) = self.lock_held_since.lock() {
                    *g = Some(Instant::now());
                }
                // a panic inside the tick is caught here: named in the state, mining stopped with the reason, the
                // thread alive to keep the state honest (F-25: a dead tick left the process alive and silent)
                let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| c.tick()));
                if let Err(e) = r {
                    let msg = e.downcast_ref::<String>().cloned().or_else(|| e.downcast_ref::<&str>().map(|x| x.to_string())).unwrap_or_else(|| "unknown".into());
                    c.tick_faults += 1;
                    c.tick_fault = Some(msg.clone());
                    let line = format!("the supervisor's tick failed: {msg}; mining stops with that reason (fault {})", c.tick_faults);
                    c.log("app", &line);
                    c.stop(&format!("the supervisor's tick failed: {msg}"));
                    c.notice("bad", "Mining stopped: the supervisor failed inside its own tick.", &format!("{msg}. The log has the time; switch the card off and on to try again."));
                }
                c.tick_beat = Instant::now();
                if let Ok(mut g) = self.lock_held_since.lock() {
                    *g = None;
                }
            }
            thread::sleep(Duration::from_millis(250));
        }
    }

    /// The process-level watchdog, on its own thread: a tick thread that stops beating (dead, or wedged in a wait)
    /// is a named stall; after two minutes without a beat while mining, the watchdog stops the workers by their
    /// pids and marks the state, so a supervisor that cannot tick never keeps hashing silently. The CLI exits on
    /// the mark and its service wrapper restarts it; the app shows it and offers Start.
    pub fn spawn_watchdog(&self) -> thread::JoinHandle<()> {
        let s = self.clone();
        thread::Builder::new().name("supervisor-watchdog".into()).spawn(move || loop {
            thread::sleep(Duration::from_secs(5));
            let stalled = {
                // try_lock: a tick wedged while holding the lock must not wedge the watchdog too
                match s.inner.try_lock() {
                    Ok(c) => {
                        if c.stop_flag {
                            return;
                        }
                        let mining = matches!(c.mining, MiningState::Mining | MiningState::Starting | MiningState::Paused);
                        mining && c.tick_beat.elapsed() > TICK_STALL
                    }
                    Err(std::sync::TryLockError::WouldBlock) => {
                        let held = s.lock_held_since.lock().ok().and_then(|g| *g).map(|t| t.elapsed());
                        let phase = s.sync_phase.lock().ok().and_then(|g| g.clone());
                        held.map_or(false, |h| tick_is_wedged(h, phase.as_ref().map(|(t, n)| (t.elapsed(), n.as_str()))))
                    }
                    Err(_) => false,
                }
            };
            if stalled {
                // the tick is gone or wedged: mark it where the CLI and the app read it, and end the workers by pid
                if let Ok(mut c) = s.inner.try_lock() {
                    let line = "the supervisor's tick has not run for two minutes; the workers are stopped by their pids and mining stops (the tick is dead or wedged)".to_string();
                    c.log("app", &line);
                    c.tick_fault = Some("the tick stopped beating for two minutes".into());
                    c.stop("the supervisor's tick stopped beating");
                    c.notice("bad", "Mining stopped: the supervisor stopped ticking.", "The workers were stopped. Press Start to try again; the log has the time.");
                } else {
                    // the lock is held by the wedged tick: the mark goes through the side channel the CLI reads
                    let phase = s.sync_phase.lock().ok().and_then(|g| g.clone()).map(|(t, n)| format!(" (in the phase: {n}, {} s in)", t.elapsed().as_secs())).unwrap_or_default();
                    if let Ok(mut g) = s.wedged.lock() {
                        *g = Some(format!("the supervisor's tick is wedged holding its lock for two minutes{phase}"));
                    }
                }
            }
        }).expect("thread")
    }

    /// The watchdog's side-channel mark when the core's lock itself is wedged (the CLI exits on it).
    pub fn wedged(&self) -> Option<String> {
        self.wedged.lock().ok().and_then(|g| g.clone())
    }

    /// Run the tuner on one device (by fingerprint) or every mining device, in a thread.
    pub fn tune(&self, fingerprint: Option<String>) -> Result<(), SupError> {
        {
            let mut c = lock(&self.inner);
            if c.tune_running {
                return Err("a tune is already running".to_string().into());
            }
            if !matches!(c.mining, MiningState::Mining) {
                return Err("start mining first; the tuner measures the device while it works".to_string().into());
            }
            let targets: Vec<String> = c.slots.iter().filter(|s| s.proc.is_some() && matches!(s.state, SlotState::Mining) && fingerprint.as_ref().map_or(true, |f| *f == s.device.fingerprint)).map(|s| s.device.fingerprint.clone()).collect();
            if targets.is_empty() {
                return Err("no mining device to tune".to_string().into());
            }
            if c.slots.iter().filter(|s| targets.contains(&s.device.fingerprint)).all(|s| s.knobs.is_empty()) {
                return Err("this worker declares no launch controls, so there is nothing to tune; it runs its own defaults".to_string().into());
            }
            c.tune_running = true;
            let s = self.clone();
            thread::Builder::new()
                .name("supervisor-tune".into())
                .spawn(move || {
                    for fp in targets {
                        s.run_tune(&fp);
                    }
                    lock(&s.inner).tune_running = false;
                })
                .map_err(|e| SupError::Message(e.to_string()))?;
        }
        Ok(())
    }

    fn run_tune(&self, fp: &str) {
        let (knobs, start, pins, mode, limits, safety, cfg) = {
            let mut c = lock(&self.inner);
            let Some(i) = c.slot_index(fp) else { return };
            if c.slots[i].knobs.is_empty() {
                return;
            }
            let algo = c.source.as_ref().map(|s| s.algo()).unwrap_or_default();
            let s = &mut c.slots[i];
            s.state = SlotState::Tuning;
            let mut acc = TuneAcc::new();
            acc.current = s.params.clone();
            s.tune = Some(acc);
            let key = TuneStore::key(&s.device.fingerprint, &s.worker_version, &s.device.driver, &algo);
            let hint = c.tune_store.get(&key).map(|t| t.chosen.clone()).unwrap_or_else(|| c.slots[i].params.clone());
            let s = &c.slots[i];
            let pins = c.settings.pins.get(fp).cloned().unwrap_or_default();
            let safety = Safety::for_device(s.device.slowdown_c);
            let out = (s.knobs.clone(), hint, pins, c.settings.mode, c.settings.limits.clone(), safety, c.cfg.tune.clone());
            let line = format!("tune started on {} ({})", s.device.name, c.settings.mode.label());
            c.log("app", &line);
            out
        };
        let t0 = Instant::now();
        let mut bench = LiveBench { sup: self.clone(), fp: fp.to_string(), settle: cfg.settle, window: cfg.window };
        let result = tuner::search(&knobs, &start, &pins, mode, &limits, &safety, &cfg, &mut bench, &mut || t0.elapsed());
        let mut c = lock(&self.inner);
        let algo = c.source.as_ref().map(|s| s.algo()).unwrap_or_default();
        let Some(i) = c.slot_index(fp) else { return };
        let stored = StoredTune { chosen: result.chosen.clone(), mode, when_unix: now_unix(), worker_version: c.slots[i].worker_version.clone(), driver: c.slots[i].device.driver.clone(), algo: algo.clone(), stopped: result.stopped.clone(), candidates: result.candidates.len() };
        let key = TuneStore::key(fp, &stored.worker_version, &stored.driver, &algo);
        c.tune_store.entries.insert(key, stored.clone());
        if let Err(e) = c.tune_store.save(&c.cfg.tune_path()) {
            let line = format!("tune store not saved: {e}");
            c.log("app", &line);
        }
        let name = c.slots[i].device.name.clone();
        let text = match &result.stopped {
            tuner::StopReason::Converged => format!("Tuned {name}: {}", params_text(&result.chosen)),
            tuner::StopReason::Budget => format!("Tuned {name} (time budget reached): {}", params_text(&result.chosen)),
            tuner::StopReason::Unstable { reason } => format!("Stopped tuning {name}: this device became unstable ({reason}); running at the last safe point {}", params_text(&result.chosen)),
            tuner::StopReason::BenchFailed { reason } => format!("Tuning {name} did not finish: {reason}"),
        };
        c.log("app", &text);
        c.notice("info", &text, "");
        let s = &mut c.slots[i];
        s.tune = None;
        s.last_tune = Some(stored);
        let current = s.params.clone();
        s.params = result.chosen.clone();
        let wanted = s.params.clone();
        if let Some(p) = s.proc.as_mut() {
            send_knobs(p, &current, &wanted);
        }
        if matches!(s.state, SlotState::Tuning) {
            s.state = SlotState::Mining;
            s.active_job = None;
        }
    }
}

/// Send every knob whose value differs from what the worker last acknowledged, one `set` per knob (the
/// protocol's shape); the worker answers each with `knob` and the acknowledged value lands in `params`.
fn send_knobs(p: &mut WorkerProcess, current: &Params, wanted: &Params) {
    for (k, v) in wanted {
        if current.get(k).map_or(true, |c| (c - v).abs() > 1e-9) {
            let _ = p.send(&ToWorker::Set(Set { knob: k.clone(), value: *v }));
        }
    }
}

/// The epoch a template is for: the source's statement when it makes one, else the verifier's derivation.
fn template_epoch(verifier: Option<&Arc<dyn Verifier>>, t: &JobTemplate) -> u64 {
    t.epoch.or_else(|| verifier.and_then(|v| v.epoch_at(t.height))).unwrap_or(0)
}

fn params_text(p: &Params) -> String {
    p.iter().map(|(k, v)| format!("{k} {v}")).collect::<Vec<_>>().join(", ")
}

/// The live bench: sets the worker's knobs, waits, reads what the tick thread collected.
struct LiveBench {
    sup: Supervisor,
    fp: String,
    settle: Duration,
    window: Duration,
}

impl Bench for LiveBench {
    fn measure(&mut self, params: &Params) -> Result<Measurement, String> {
        {
            let mut c = lock(&self.sup.inner);
            let i = c.slot_index(&self.fp).ok_or("the device is gone")?;
            if matches!(c.mining, MiningState::Idle | MiningState::Stopping) {
                return Err("mining stopped".into());
            }
            let s = &mut c.slots[i];
            if matches!(s.state, SlotState::Faulted | SlotState::Refused) {
                return Err(format!("the device is {}: {}", state_name(s.state), s.message));
            }
            let current = s.params.clone();
            let p = s.proc.as_mut().ok_or("no worker")?;
            send_knobs(p, &current, params);
            s.params = params.clone();
            if let Some(acc) = s.tune.as_mut() {
                acc.collecting = false;
                acc.reset();
                acc.current = params.clone();
            }
            c.cancel_slot_job(i);
        }
        thread::sleep(self.settle);
        {
            let mut c = lock(&self.sup.inner);
            let i = c.slot_index(&self.fp).ok_or("the device is gone")?;
            let s = &mut c.slots[i];
            let acc = s.tune.as_mut().ok_or("tune cancelled")?;
            acc.reset();
            acc.collecting = true;
            acc.window_started = Some(Instant::now());
        }
        thread::sleep(self.window);
        let mut c = lock(&self.sup.inner);
        let i = c.slot_index(&self.fp).ok_or("the device is gone")?;
        let s = &mut c.slots[i];
        let acc = s.tune.as_mut().ok_or("tune cancelled")?;
        acc.collecting = false;
        let secs = acc.window_started.map_or(self.window.as_secs_f64(), |t| t.elapsed().as_secs_f64());
        let m = acc.measurement(secs);
        if m.rate_hps > 0.0 {
            acc.difficulty = (m.rate_hps / 10.0).max(1000.0);
        }
        Ok(m)
    }
}

fn state_name(s: SlotState) -> &'static str {
    match s {
        SlotState::Off => "off",
        SlotState::Idle => "idle",
        SlotState::Opening => "opening",
        SlotState::Preparing => "preparing",
        SlotState::Mining => "mining",
        SlotState::Paused => "paused",
        SlotState::Tuning => "tuning",
        SlotState::Refused => "refused",
        SlotState::Faulted => "faulted",
    }
}

fn safe_dir_name(id: &str) -> String {
    id.chars().map(|c| if c.is_ascii_alphanumeric() || c == '-' || c == '_' { c } else { '_' }).collect()
}

impl Core {
    fn log(&mut self, src: &str, text: &str) {
        let line = format!("{} [{src}] {text}", chrono::Local::now().format("%H:%M:%S"));
        log::info!("{line}");
        self.log.push_back(line);
        self.log_seq += 1;
        while self.log.len() > MAX_LOG_LINES {
            self.log.pop_front();
        }
    }

    fn notice(&mut self, level: &str, text: &str, detail: &str) {
        self.notices.retain(|n| n.text != text);
        self.notices.push(Notice { level: level.into(), text: text.into(), detail: detail.into() });
        while self.notices.len() > 6 {
            self.notices.remove(0);
        }
    }

    fn slot_index(&self, fp: &str) -> Option<usize> {
        self.slots.iter().position(|s| s.device.fingerprint == fp)
    }

    /// The power cap applied to an NVIDIA card on Linux by nvidia-smi -pl and read back (the E1 operability study,
    /// 21:21 UK, 12 October 2026: a cap in the settings was a silent no-op on a rented box). It applies, or it refuses
    /// with the driver's own words and the command to run with root, and the row never reads a cap it did not set.
    /// Windows applies through the app's elevated step; macOS has no cap.
    fn apply_power_cap(&mut self, i: usize) {
        if cfg!(windows) || cfg!(target_os = "macos") {
            return;
        }
        let Some(cap) = self.settings.limits.power_cap_w else {
            self.slots[i].power_cap_word = String::new();
            return;
        };
        let (name, vendor, bus, index) = { let d = &self.slots[i].device; (d.name.clone(), d.vendor.clone(), d.bus_id.clone(), d.index) };
        if vendor != "nvidia" {
            return;
        }
        let id = if bus.len() >= 7 && bus.contains(':') { crate::devices::normalise_bus(&bus) } else { index.to_string() };
        let want = cap.round() as u64;
        let run = |prog: &str, args: &[String]| -> (bool, String) {
            match std::process::Command::new(prog).args(args).output() {
                Ok(o) => (o.status.success(), format!("{}{}", String::from_utf8_lossy(&o.stdout).trim(), String::from_utf8_lossy(&o.stderr).trim())),
                Err(e) => (false, format!("{prog} did not run: {e}")),
            }
        };
        let set_args: Vec<String> = vec!["-i".into(), id.clone(), "-pl".into(), want.to_string()];
        let (mut ok, mut words) = run("nvidia-smi", &set_args);
        let mut via = "";
        if !ok {
            // F-E18: the installer's root-owned helper (/usr/local/sbin/ember-powercap <card> <watts>, both arguments
            // checked, nothing else passed) with its sudoers line of no free arguments lets this user set the cap
            // alone as root; sudo -n never prompts, so a refusal comes back as words, never as a hang
            let sudo_args: Vec<String> = vec!["-n".into(), "/usr/local/sbin/ember-powercap".into(), id.clone(), want.to_string()];
            let (ok2, words2) = run("sudo", &sudo_args);
            if ok2 {
                ok = true;
                via = " through the ember-powercap helper";
            } else {
                let w2: String = words2.lines().last().unwrap_or("").chars().take(120).collect();
                words = format!("{}; sudo -n: {}", words.lines().last().unwrap_or(""), if w2.is_empty() { "refused" } else { w2.as_str() });
            }
        }
        let (_, back) = run("nvidia-smi", &["-i".into(), id.clone(), "--query-gpu=power.limit".into(), "--format=csv,noheader,nounits".into()]);
        let read: f64 = back.trim().parse().unwrap_or(0.0);
        if ok && (read - cap).abs() < 1.0 {
            let line = format!("{name}: power cap {want} W applied{via} (nvidia-smi -i {id} -pl {want}; read back {read:.0} W)");
            self.log("app", &line);
            self.slots[i].power_cap_applied = true;
            self.slots[i].power_cap_word = format!("cap {want} W applied");
        } else {
            let last: String = words.lines().last().unwrap_or("").chars().take(200).collect();
            let why = if ok {
                format!("the driver reports {read:.0} W after the call")
            } else if last.contains("Insufficient Permissions") || last.contains("not supported") || last.contains("No permission") {
                format!("the driver refused the call ({last}); in a container the host's driver rights are needed")
            } else if last.is_empty() {
                format!("the driver reports {read:.0} W")
            } else {
                last
            };
            let line = format!("{name}: power cap {want} W requested, NOT applied: {why}; run with root: sudo nvidia-smi -i {id} -pl {want}");
            self.log("app", &line);
            self.slots[i].power_cap_applied = false;
            self.slots[i].power_cap_word = format!("cap {want} W requested, not applied: {why}");
            self.slots[i].message = format!("power cap {want} W requested, not applied: {why}");
            self.notice("warn", &format!("{name}: the power cap is not applied."), &format!("{why}. Run with root: sudo nvidia-smi -i {id} -pl {want}"));
        }
    }

    fn apply_settings_to_slots(&mut self) {
        for i in 0..self.slots.len() {
            if self.slots[i].proc.is_some() {
                self.apply_power_cap(i);
            }
        }
        for s in &mut self.slots {
            s.enabled = self.settings.device_enabled(&s.device.fingerprint, s.labelled.default_enabled);
            if let Some(p) = self.settings.pins.get(&s.device.fingerprint) {
                let current = s.params.clone();
                for (k, v) in p {
                    s.params.insert(k.clone(), *v);
                }
                if matches!(s.state, SlotState::Mining | SlotState::Paused) {
                    let wanted = s.params.clone();
                    if let Some(proc_) = s.proc.as_mut() {
                        send_knobs(proc_, &current, &wanted);
                    }
                }
            }
        }
        // the card switch on a running layer (the 5090 Windows host, 13 October 2026 11:23 UK: a 5090 the guard had closed, switched
        // off and on through api/cards, stayed closed; the switch only flipped `enabled` and the layer restarts only
        // from idle): a live slot switched off closes, and an enabled slot without a worker (off, idle, or faulted
        // by the guard) opens again with its episodes cleared, while mining runs
        if matches!(self.mining, MiningState::Starting | MiningState::Mining) {
            for i in 0..self.slots.len() {
                let (enabled, live, state) = (self.slots[i].enabled, self.slots[i].proc.is_some(), self.slots[i].state);
                if !enabled && live {
                    self.close_slot(i, "switched off");
                    self.slots[i].state = SlotState::Off;
                } else if enabled && !live && matches!(state, SlotState::Off | SlotState::Idle | SlotState::Faulted) {
                    let name = self.slots[i].device.name.clone();
                    let line = format!("{name}: switched on; opening");
                    self.log("app", &line);
                    self.slots[i].no_result_episodes.clear();
                    self.slots[i].message.clear();
                    self.slots[i].state = SlotState::Idle;
                    self.open_slot(i);
                }
            }
        }
    }

    /// Launch every worker once for its `hello`, build the slots from its device list, shut it down.
    fn inventory(&mut self) -> Result<(), SupError> {
        if !matches!(self.mining, MiningState::Idle) {
            return Err("stop mining before detecting devices again".to_string().into());
        }
        // 0.2.11: a worker bound to one coordinate by its name (igneum-worker-<backend>-coord<N>) is kept aside for the
        // switch to that coordinate and never walks the inventory (its hello would list every card a second time)
        let (bound, launchers): (Vec<Launcher>, Vec<Launcher>) = self.cfg.launchers.clone().into_iter().partition(|l| coord_bound_stem(&l.exe).is_some() || version_bound_stem(&l.exe).is_some());
        let (by_coord, by_version): (Vec<Launcher>, Vec<Launcher>) = bound.into_iter().partition(|l| coord_bound_stem(&l.exe).is_some());
        self.cfg.coord_launchers = by_coord.into_iter().filter_map(|l| coord_bound_stem(&l.exe).map(|(base, c)| (base, c, l))).collect();
        self.cfg.version_launchers = by_version.into_iter().filter_map(|l| version_bound_stem(&l.exe).map(|(base, v)| (base, v, l))).collect();
        let version_lines: Vec<String> = self.cfg.version_launchers.iter().map(|(base, v, l)| format!("{}: the worker for every coordinate whose document is version {v}, beside {base}", l.exe.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default())).collect();
        for line in version_lines {
            self.log("app", &line);
        }
        let bound_lines: Vec<String> = self.cfg.coord_launchers.iter().map(|(base, c, l)| format!("{}: the worker for coordinate {c} alone, beside {base}", l.exe.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default())).collect();
        for line in bound_lines { self.log("app", &line); }
        if launchers.is_empty() {
            // the sentence names where it looked and what it saw (a worker file without the executable bit, or
            // with the wrong name or extension, is found this way)
            let where_ = self.cfg.searched.iter().map(|(d, names)| {
                let seen = if names.is_empty() { "empty or unreadable".to_string() } else { names.iter().take(12).cloned().collect::<Vec<_>>().join(", ") + if names.len() > 12 { ", ..." } else { "" } };
                format!("{} ({seen})", d.display())
            }).collect::<Vec<_>>().join("; ");
            self.notice("bad", "No worker is installed with this app.", &format!("No igneum-worker-* executable in {where_}."));
            return Err(format!("no worker installed: looked in {where_}").into());
        }
        let mut slots = Vec::new();
        self.launch_errors.clear();
        for p in worker::take_ignored() {
            let line = format!("{}: not named in workers.sha256; never launched (a file left by an earlier install is not a worker of this build)", p.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default());
            self.log("app", &line);
        }
        let mut uuids: Option<std::collections::HashMap<String, String>> = None;
        let no_uuids = std::collections::HashMap::new();
        // F-E36: the OpenCL worker opens ONE device per process (the first of the loader's enumeration without an
        // argument, which on a mixed host is an NVIDIA card already mined through CUDA), so the inventory walks its
        // devices first (`igneum-worker-opencl telemetry`, the GPU team's form) and launches one process per card that is
        // not NVIDIA with `--device <index>`; a walk that fails falls back to the plain launch and says so
        let mut launchers_expanded: Vec<Launcher> = Vec::new();
        for l in launchers {
            let stem = l.exe.file_stem().and_then(|x| x.to_str()).unwrap_or("").to_string();
            if stem.starts_with("igneum-worker-opencl") && l.args.is_empty() {
                match worker::opencl_device_indices(&l.exe) {
                    Ok(list) => {
                        let picked: Vec<(u32, String, String)> = list.iter().filter(|(_, vendor, _)| !vendor.to_ascii_lowercase().contains("nvidia")).cloned().collect();
                        let line = format!("{stem}: {} OpenCL device(s) on this host ({}); {} opened through it (NVIDIA cards mine through CUDA)", list.len(), list.iter().map(|(i, v, n)| format!("{i}: {n} [{v}]")).collect::<Vec<_>>().join("; "), picked.len());
                        self.log("app", &line);
                        for (idx, _vendor, _name) in picked {
                            let mut one = l.clone();
                            one.args = vec!["--device".into(), idx.to_string()];
                            one.workdir = l.workdir.join(format!("device-{idx}"));
                            launchers_expanded.push(one);
                        }
                        continue;
                    }
                    Err(e) => {
                        let line = format!("{stem}: the device walk failed ({e}); the worker opens its first device");
                        self.log("app", &line);
                    }
                }
            }
            launchers_expanded.push(l);
        }
        for l in launchers_expanded {
            let launched = self.in_phase(&format!("launching {} to list its devices (the hello wait)", l.exe.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default()), || l.launch());
            let mut p = match launched {
                Ok(p) => p,
                Err(e) => {
                    let name = l.exe.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default();
                    let line = format!("{name}: {e}");
                    self.log("app", &line);
                    self.launch_errors.push(line);
                    // the project's audit of the two-card Windows host's captures (15 October 2026): "The worker igneum-worker-opencl.exe did not start"
                    // stood on a machine whose every found card mined through the CUDA worker; the notice is given
                    // once the inventory is read, with what still mines, and only as a warning
                    continue;
                }
            };
            let hello = p.hello.clone();
            let _ = p.shutdown(Duration::from_secs(3));
            // the cards' intrinsic identities (NVIDIA: the GPU UUID by bus id from nvidia-smi), read once per
            // inventory and only when a worker lists an NVIDIA card; the fingerprint hashes the UUID
            if uuids.is_none() && hello.devices.iter().any(|hd| hd.vendor.as_deref() == Some("nvidia") || hello.backend == "cuda") {
                uuids = Some(devices::nvidia_uuids_by_bus());
            }
            for (idx, hd) in hello.devices.iter().enumerate() {
                let d = Device::from_hello_with(&hello.backend, idx as u32, hd, uuids.as_ref().unwrap_or(&no_uuids));
                let labelled = support_label(&d, &self.cfg.horizon);
                let enabled = self.settings.device_enabled(&d.fingerprint, labelled.default_enabled);
                let _ = self.journal.record("device_detected", &format!("{} {} {:.0}GB {:?}", d.vendor, d.backend, d.memory_total as f64 / devices::GIB as f64, labelled.label));
                let mut params = Params::new();
                for k in &hd.knobs {
                    params.insert(k.name.clone(), k.default);
                }
                let mut launcher = l.clone();
                launcher.workdir = l.workdir.join(safe_dir_name(&d.id));
                // F-E41 (the two-card Windows host, 14 October 2026: the 4070's slot reported the 5090's memory and idled at 30 W under "mining"):
                // a worker that lists several devices in one hello opens the device its COMMAND LINE names (the CUDA worker's
                // --device N; the configure message's device field is read by no worker), so every slot after a many-device
                // hello launches with its own ordinal; a launcher that already carries args (the OpenCL walk) is left alone
                // F-E44 (the 5090 Windows host, 14:0x UK, 14 October 2026): launched bare the CUDA worker is in protocol mode only with NO
                // argument (its first argument is a verb; "--device" alone printed the banner and exited before hello, both
                // cards stopped under the signed 0.2.8), so the slot's device goes after the worker's `protocol` verb, which
                // every stored CUDA worker since 8d2acfd9 carries; the OpenCL worker takes --device bare (its walk above)
                // the project's word of 14:3x UK after two wrong launch forms (a bare --device: "unknown command"; the verb form
                // raced builds): no device ARGUMENT at all; the slot's device is named in the worker's ENVIRONMENT, so every
                // stored worker sees one device as its ordinal 0 (the runtimes' own variables, set after the passthrough so
                // they override an owner's; the hello under them lists that one device as cuda:0 or hip:0)
                if hello.devices.len() > 1 && launcher.args.is_empty() {
                    let ord = device_ordinal(&hd.id, idx).to_string();
                    match hello.backend.as_str() {
                        "cuda" => launcher.env.push(("CUDA_VISIBLE_DEVICES".into(), ord)),
                        "hip" | "rocm" => { launcher.env.push(("HIP_VISIBLE_DEVICES".into(), ord.clone())); launcher.env.push(("ROCR_VISIBLE_DEVICES".into(), ord)); }
                        _ => {}
                    }
                }
                // F-E48 (the GPU team's read of 14 October 2026, 20:34 UK: a session pinned to coordinate 5 launched the base worker,
                // which refused the coordinate's document and died before the router could move it): a pinned coordinate
                // with a bound worker takes that worker from the first launch; the base one is kept for a change of pin
                let mut base_launcher = None;
                let pin_version = self.settings.coordinate_pin.and_then(|c| self.coords.get(&c)).and_then(|x| x.doc.get("version").and_then(|v| v.as_u64()));
                if let Some(b) = pin_bound_launcher_v(&self.cfg.coord_launchers, &self.cfg.version_launchers, self.settings.coordinate_pin, pin_version, &launcher, &d.name) {
                    self.log("app", &format!("{}: pinned to coordinate {}; its own worker {} is launched", d.name, self.settings.coordinate_pin.unwrap_or(0), b.exe.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default()));
                    base_launcher = Some(launcher.clone());
                    launcher = b;
                }
                let vectors = hello.algos.iter().map(|a| format!("{}{}", a.id, if a.vectors_ok { "" } else { " (vectors FAILED)" })).collect::<Vec<_>>().join(", ");
                // F-E67's lesson (15 October 2026): the worker's own version word is its build commit (b1772a13 printed
                // "build 1177b619" and the rig's read was taken for another file); the line names the binary by the sha256
                // the sign names beside it
                let worker_sha8 = worker::sha256_file(&launcher.exe).map(|h| h[..8].to_string()).unwrap_or_else(|_| "sha unread".into());
                let line = format!("found {} ({} {}, {:.1} GB, {}) via {} {} (sha256 {}): {}; fingerprint over the {}; algorithms: {vectors}", d.name, d.vendor, d.backend, d.memory_total as f64 / devices::GIB as f64, d.arch, hello.worker, hello.version, worker_sha8, labelled.reason, match d.identity.as_str() { "uuid" => "GPU UUID", "amdgpu-unique-id" => "amdgpu unique_id", "platform-serial" => "platform serial", _ => "bus id (no intrinsic id read: nvidia-smi absent or silent, or the worker offered none)" });
                self.log("app", &line);
                slots.push(Slot {
                    device: d,
                    labelled,
                    launcher,
                    base_launcher,
                    enabled,
                    proc: None,
                    state: if enabled { SlotState::Idle } else { SlotState::Off },
                    message: String::new(),
                    params,
                    knobs: hd.knobs.clone(),
                    worker_name: hello.worker.clone(),
                    worker_version: hello.version.clone(),
                    worker_note: hello.note.clone(),
                    keyed_job: hello.extensions.iter().any(|e| e == proto::EXT_KEYED_JOB),
                    // every worker is a child of this supervisor over its pipes; a worker over a socket (loopback
                    // or another machine) is a form to come and would name its transport here
                    transport: Transport::LocalPipes,
                    algos: hello.algos.iter().filter(|a| a.vectors_ok).map(|a| a.id.clone()).collect(),
                    slot_pace: SubmitPace::default(),
                    held_verify_pace: SubmitPace::default(),
                    telemetry: None,
                    last_telemetry: Instant::now(),
                    phase: String::new(),
                    epoch: None,
                    preparing: Duration::ZERO,
                    opened_at: None,
                    idle_since: None,
                    restarts: 0,
                    last_share_at: None,
                    silent_restarts: BTreeMap::new(),
                    power_cap_applied: false,
                    power_cap_word: String::new(),
                    f110_lift: None,
                    last_set: String::new(),
                    route: router::CardRoute::default(), lane_stats: BTreeMap::new(), lane_tick_at: None,
                    document_fault: false,
                    stint: RateMeter::new(Duration::from_secs(router::PROBE_S)),
                    stint_since: None,
                    settle_said: false,
                    build_until: None,
                    awaiting_document: None,
                    quick_exits: 0,
                    restart_after: None,
                    coord_digest: None,
                    last_anchor: None,
                    hangs: VecDeque::new(),
                    last_result_at: None,
                    launch_pending: None,
                    launch_queued: None,
                    invalids: VecDeque::new(),
                    transition_done_at: None,
                    doc_valid_seen: false,
                    switch_invalids: 0,
                    late_run: LateRun::default(),
                    proto_faults: VecDeque::new(),
                    validated: RateMeter::new(Duration::from_secs(600)),
                    reported: RateMeter::new(Duration::from_secs(600)),
                    invalid_total: 0,
                    active_job: None,
                    chunk_index: 0,
                    chunk_job: None,
                    chunks_in_job: 0,
                    lane_done_said: false,
                    measure_difficulty: 50_000.0,
                    recent_valid: VecDeque::new(),
                    tune: None,
                    last_tune: None,
                    transition: Transition::Current,
                    dataset_epoch: None,
                    next_epoch: None,
                    can_warm: false,
                    switch_pending: None,
                    switch_samples: VecDeque::new(),
                    transition_started: None,
                    invalid_episodes: VecDeque::new(),
                    preparing_gap: None,
                    switch_armed: false,
                    power: RateMeter::new(Duration::from_secs(600)),
                    building: None,
                    last_build: None,
                    next_doc_digest: None,
                    last_validated_at: None,
                    no_result_episodes: VecDeque::new(),
                    idle_hashes_since: None,
                    measure_stepped_at: None,
                });
            }
        }
        // F-E36 (the two-card Windows host on 0.2.7, 14 October 2026: the OpenCL worker beside the CUDA one listed the RTX 5090 a second
        // time, and the page showed the second view "stopped" while the card mined under CUDA): a card seen by two
        // workers is ONE slot, on the vendor's preferred worker (NVIDIA on CUDA, Apple on Metal, AMD and Intel on
        // OpenCL); the other view is dropped before anything launches, and the log names it
        let slots = dedupe_slots_by_address(slots, |line| self.log("app", line));
        self.slots = slots;
        self.apply_settings_to_slots();
        Ok(())
    }

    /// The candidate the installed workers carry that is not the stub (the first, when several), for the pool
    /// and the node; the stub's id when nothing else is installed.
    /// The algorithm hint for a pool before its first notify (a node's own always wins): the launch candidate
    /// when any enabled worker carries it, else the first candidate an enabled worker carries.
    fn network_algo(&self) -> String {
        const LAUNCH: &str = "igneumhash";
        let carried = |s: &Slot| s.worker_name != "igneum-worker-stub" && s.enabled;
        if self.slots.iter().any(|s| carried(s) && s.algos.iter().any(|a| a == LAUNCH)) {
            return LAUNCH.to_string();
        }
        for s in &self.slots {
            if carried(s) {
                if let Some(a) = s.algos.iter().find(|a| *a != stubhash::STUB_ALGO) {
                    return a.clone();
                }
            }
        }
        for s in &self.slots {
            if s.worker_name != "igneum-worker-stub" {
                if let Some(a) = s.algos.iter().find(|a| *a != stubhash::STUB_ALGO) {
                    return a.clone();
                }
            }
        }
        stubhash::STUB_ALGO.to_string()
    }

    fn host_allowance(&self) -> f64 {
        self.settings.costs.host_allowance_w.unwrap_or(if self.cfg.portable { 10.0 } else { 40.0 })
    }

    fn start(&mut self) -> Result<(), SupError> {
        if !matches!(self.mining, MiningState::Idle) {
            return Err("already running".to_string().into());
        }
        if self.slots.is_empty() {
            self.inventory()?;
        }
        self.start_registry();
        // F-E66: the first dial declares the last run's measured rate (the pool opens the session at that rate's step)
        if self.settings.last_rate_hps > 0 && work::LAST_RATE_HPS.load(std::sync::atomic::Ordering::Relaxed) == 0 {
            work::LAST_RATE_HPS.store(self.settings.last_rate_hps, std::sync::atomic::Ordering::Relaxed);
            self.log("app", &format!("the pool subscribe declares the last run's rate, {:.2} MH/s (the pool opens the session at that rate's step)", self.settings.last_rate_hps as f64 / 1e6));
        } else if work::LAST_RATE_HPS.load(std::sync::atomic::Ordering::Relaxed) == 0 {
            // the 5090 Windows host's fifteen-minute command-line read on 0.2.16 (15 October 2026): a fresh session with no stored rate
            // opened at the pool's lowest step and a 53 MH/s machine submitted every result for the minutes the target
            // took to settle; 23,746 shares answered stale against 139 accepted. A first session declares a nominal
            // rate per enabled card (the pool opens higher and settles down within a few heights; a slow card
            // submits fewer shares for those minutes, which costs nothing on the chain); the measured rate replaces it
            // at the first minute and is stored for the next run
            let cards = self.slots.iter().filter(|s| s.enabled).count().max(1) as u64;
            let nominal = FIRST_DIAL_NOMINAL_HPS_PER_CARD * cards;
            work::LAST_RATE_HPS.store(nominal, std::sync::atomic::Ordering::Relaxed);
            self.log("app", &format!("no stored rate from an earlier run: the pool subscribe declares a nominal {:.1} MH/s for {cards} card(s) until the first minute's measurement (a fresh session at the pool's lowest step floods it with results while its target settles)", nominal as f64 / 1e6));
        }
        let address = self.settings.payout_address.clone().ok_or_else(|| "Enter the address your rewards go to first.".to_string())?;
        let network_algo = self.settings.algo.clone().filter(|a| !a.is_empty()).unwrap_or_else(|| self.network_algo());
        let mut source = work::source_for_with_fee(&self.settings.work_source, &network_algo, self.cfg.dev_fee.clone().ok())?;
        source.set_receipts_dir(&self.cfg.data_dir.join("receipts"));
        if let Some(kp) = self.cfg.key_path.clone() {
            match minerkey::MinerKey::load_or_create(&kp) {
                Ok(k) => {
                    let line = format!("the miner's work-authority key is {} (its file never leaves {})", k.public_hex(), kp.parent().map(|p| p.display().to_string()).unwrap_or_default());
                    self.log("app", &line);
                    let k = Arc::new(k);
                    self.signer = Some(k.clone());
                    source.set_signer(k);
                }
                Err(e) => {
                    let line = format!("no work-authority key: {e}; shares go unsigned (a non-custodial pool will refuse them)");
                    self.log("app", &line);
                    self.signer = None;
                }
            }
        }
        source.connect(&address, &self.settings.worker_name).map_err(|e| format!("could not connect: {e}"))?;
        // the verifier is built on the same parameter document the workers get (the node's, verbatim); a
        // document without a version is refused before any device opens (security's F-14: never a default)
        let doc = source.params();
        if doc.is_null() && source.algo() == "igneumhash" {
            // a pool that serves no document cannot be mined on the candidate: the reference needs the document
            // (F-14) and every share is checked against it; the pool must send params in its subscribe result or
            // its notify
            source.disconnect();
            return Err(format!("{} names no parameter document for igneumhash (no params in the subscribe result or the job); mining is not started on a document the miner cannot see", source.name()).into());
        }
        if let serde_json::Value::Object(o) = &doc {
            if !o.contains_key("version") {
                source.disconnect();
                return Err(format!("the source's parameter document for {} has no version; mining is not started on it", source.algo()).into());
            }
        }
        let doc_text = if doc.is_null() { None } else { Some(doc.to_string()) };
        // the tag's digest: the source's own statement (a node's paramsDigest, P) when it makes one, else the
        // supervisor's digest of the canonical document
        self.params_digest = source.params_digest().unwrap_or_else(|| work::params_digest(&doc));
        self.algo_params = doc.clone();
        self.refused_digest = None;
        let verifier = verify::verifier_for_params(&source.algo(), doc_text.as_deref()).map_err(|e| format!("the reference refuses the source's parameter document for {}: {e}", source.algo()))?;
        // the keyed document (the hostile-key rule, SPEC 0.9 bit 10): the bond's secret must be in hand, and every
        // enabled worker must take it inside the job; a worker without the extension is left off with its reason
        // rather than fed a job it would hash unkeyed (the node would refuse every result)
        if let Some(s_rounds) = bond::keyed_rounds(&doc) {
            if self.cfg.bond_secret.is_none() {
                source.disconnect();
                return Err(format!("this network's document carries the bonded rounds (S = {s_rounds}): Ember needs the bond's secret from the wallet at start ({} or --bond-secret-stdin); mining is not started without it", bond::BOND_SK_ENV).into());
            }
            let mut lines = Vec::new();
            for s in self.slots.iter_mut().filter(|s| s.enabled) {
                let Some(why) = keyed_refusal(s.keyed_job, &s.transport) else { continue };
                s.enabled = false;
                s.state = SlotState::Off;
                s.message = format!("{} {why}", s.worker_name);
                lines.push(format!("{}: {}", s.device.name, s.message));
            }
            for line in &lines {
                self.log("app", line);
            }
            let line = format!("the document carries the bonded rounds (S = {s_rounds}); the bond's public key is {}", self.cfg.bond_secret.as_ref().map(|b| b.public_hex()).unwrap_or_default());
            self.log("app", &line);
        }
        let enabled: Vec<usize> = self.slots.iter().enumerate().filter(|(_, s)| s.enabled).map(|(i, _)| i).collect();
        if !self.slots.iter().any(|s| s.enabled && s.algos.iter().any(|a| *a == source.algo())) {
            let carried: Vec<String> = self.slots.iter().filter(|s| s.enabled).flat_map(|s| s.algos.clone()).collect();
            let line = format!("the source mines {}; the enabled workers carry {}", source.algo(), if carried.is_empty() { "nothing".to_string() } else { carried.join(", ") });
            self.log("app", &line);
            // F-191 (the first tester's screenshot, 15 October 2026: an Intel UHD alone, the banner in jargon): the words a miner reads
            let vendors: Vec<&str> = self.slots.iter().filter(|s| s.enabled).map(|s| s.device.vendor.as_str()).collect();
            let who = if vendors.iter().all(|v| *v == "intel") { "This Intel card has no worker in this version yet: Intel cards join with the Intel worker in a coming version; NVIDIA cards and Apple silicon mine today." }
                else if vendors.iter().all(|v| *v == "amd") { "This AMD card has no worker in this version yet: AMD cards join with their worker in a coming version; NVIDIA cards and Apple silicon mine today." }
                else { "The cards switched on have no worker in this version for the chain's work; NVIDIA cards and Apple silicon mine today." };
            self.notice("warn", who, &line);
        }
        if enabled.is_empty() {
            source.disconnect();
            // F-E17 (E3's two driver-580 pods and E2's CUDA 12.9 container, 14 October 2026): a worker that exited
            // before its hello is the cause the owner must read (cuInit's own words: a forward-compat libcuda ahead
            // of the driver's), not "no device is switched on", which is only for an honestly empty list
            if let Some(first) = self.launch_errors.first() {
                let more = if self.launch_errors.len() > 1 { format!(" (and {} more in the log)", self.launch_errors.len() - 1) } else { String::new() };
                return Err(format!("No card could start: {first}{more}. Fix the worker's start and mining starts on its own.").into());
            }
            return Err("No device is switched on. Turn one on under Your cards.".to_string().into());
        }
        if !self.launch_errors.is_empty() {
            let names: Vec<String> = self.launch_errors.iter().map(|l| l.split(':').next().unwrap_or("").to_string()).collect();
            let found: Vec<String> = self.slots.iter().filter(|s| s.enabled).map(|s| s.device.name.clone()).collect();
            let what = if found.is_empty() { "no card is mining".to_string() } else { format!("{} {} through the other workers", if found.len() == 1 { "this card mines" } else { "these cards mine" }, found.join(", ")) };
            self.notice("warn", &format!("The worker {} did not start; {what}.", names.join(", ")), &format!("{} A card only that worker can run (an AMD or Intel card under the OpenCL worker) is not listed until it starts.", self.launch_errors.join("; ")));
        }
        let verifier: Arc<dyn Verifier> = Arc::from(verifier);
        match reference_selfcheck(verifier.clone(), 1) {
            Ok(ms) => {
                self.totals.selfcheck_ms = ms;
                let line = format!("the reference checks the source's document ({}) in {ms} ms", &self.params_digest[..self.params_digest.len().min(12)]);
                self.log("app", &line);
            }
            Err(e) => {
                source.disconnect();
                return Err(format!("{e}; nothing is mined on a document the reference cannot check").into());
            }
        }
        self.source = Some(source);
        self.dialed_at = Some(Instant::now());
        self.declared_rate_hps = work::LAST_RATE_HPS.load(std::sync::atomic::Ordering::Relaxed);
        self.last_accepted_at = None;
        self.not_submitted_at_mark = self.totals.not_submitted;
        self.verifier = Some(verifier.clone());
        // the verifier threads: two, sharing one bounded queue
        let (tx, rx) = sync_channel::<VerifyReq>(VERIFY_QUEUE);
        let (txm, rxm) = sync_channel::<VerifyReq>(VERIFY_QUEUE);
        let (rtx, rrx) = sync_channel::<VerifyRes>(VERIFY_QUEUE * 4);
        let rx = Arc::new(Mutex::new(rx));
        let rxm = Arc::new(Mutex::new(rxm));
        // the verify-queue cell (0.2.24; the second Octominer rig, 16 October 2026: eight cards on a two-thread host sent
        // nothing for nine minutes at start, every checked result late for its 12 s template): the threads sized to the
        // host's cores, two to eight; share-class results taken first; the check time measured for the queue's bound
        let threads = verify_threads_for(thread::available_parallelism().map(|n| n.get()).unwrap_or(2));
        self.verify_threads = threads;
        self.verify_depth.store(0, AtomicOrdering::Relaxed);
        for n in 0..threads {
            let (rx, rxm, rtx) = (rx.clone(), rxm.clone(), rtx.clone());
            let (depth, check_us) = (self.verify_depth.clone(), self.verify_check_us.clone());
            let _ = thread::Builder::new().name(format!("verify-{n}")).spawn(move || loop {
                // a share-class result first; else a measurement-only one, waited for briefly so a share arriving meanwhile is next
                let req = match rx.lock() {
                    Ok(g) => g.try_recv(),
                    Err(_) => return,
                };
                let req = match req {
                    Ok(r) => r,
                    Err(std::sync::mpsc::TryRecvError::Disconnected) => return,
                    Err(std::sync::mpsc::TryRecvError::Empty) => match rxm.lock() {
                        Ok(g) => match g.recv_timeout(Duration::from_millis(20)) {
                            Ok(r) => r,
                            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => continue,
                            Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => return,
                        },
                        Err(_) => return,
                    },
                };
                depth.fetch_sub(1, AtomicOrdering::Relaxed);
                let started = Instant::now();
                let (valid, shares) = match &req.sk {
                    Some(sk) => match req.verifier.verify_keyed(req.height, &req.header, req.share.nonce, &req.mix, &req.hash, sk.bytes()) {
                        Ok(Some(sh)) => (true, Some(sh)),
                        _ => (false, None),
                    },
                    None => (req.verifier.verify(req.height, &req.header, req.share.nonce, &req.mix, &req.hash).unwrap_or(false), None),
                };
                let took = started.elapsed().as_micros() as u64;
                let prev = check_us.load(AtomicOrdering::Relaxed);
                check_us.store(if prev == 0 { took } else { (prev * 7 + took) / 8 }, AtomicOrdering::Relaxed);
                if rtx.send(VerifyRes { slot: req.slot, share: req.share, valid, shares, latency_ms: req.queued.elapsed().as_millis() as u64, held: req.held }).is_err() {
                    return;
                }
            });
        }
        self.verify_tx = Some(tx);
        self.verify_tx_measure = Some(txm);
        self.verify_rx = Some(rrx);
        // the verifier's warm-up (the dataset in host RAM) starts AFTER the slots' handshakes, never under them (the
        // E1 study's read, 20:32 UK, 12 October 2026: 24 verify-warm threads on a loaded host starved the engine for
        // the whole handshake wait and the worker, ready in 9 s, was judged silent)
        let warm_after_open = verifier.clone();
        self.mining = MiningState::Starting;
        self.paused_for = None;
        self.started_at = Some(Instant::now());
        self.totals = Totals::default();
        // the session's counters start again; the dev fee's lines are the configuration's, not the session's
        // (the mini's first mining minute, 11 October 2026, 23:3x UK: the fee read off and its line blank after
        // the start, with the manifest verified and the switch on)
        fee_lines_into(&mut self.totals, &self.cfg.dev_fee);
        self.downtime = Duration::ZERO;
        self.first_work_accepted = false;
        self.current = None;
        self.transitions.clear();
        self.verify_latencies.clear();
        self.verify_window.clear();
        self.late_since = None;
        self.tick_fault = None;
        self.tick_beat = Instant::now();
        self.source_problem_since = None;
        self.on_backup_since = None;
        self.primary_retry_at = None;
        self.submitted_at.clear();
        self.last_submit = None;
        self.last_outcome = None;
        self.unanswered_since = None;
        self.sent.clear();
        self.sent_order.clear();
        self.submitted.clear();
        self.stages = vec![
            Stage { name: "Opening the card".into(), status: "active".into(), detail: String::new() },
            Stage { name: "Building the dataset".into(), status: "pending".into(), detail: String::new() },
            Stage { name: "First results checked".into(), status: "pending".into(), detail: "every result is checked on the processor before it counts".into() },
            Stage { name: "First work accepted".into(), status: "pending".into(), detail: String::new() },
        ];
        let _ = self.journal.record("start_pressed", "");
        let name = self.source.as_ref().map(|s| s.name()).unwrap_or_default();
        let line = format!("mining started: {name}, {} device(s)", enabled.len());
        self.log("app", &line);
        for i in enabled {
            self.open_slot(i);
        }
        self.warm_verifier(warm_after_open, 1);
        Ok(())
    }

    fn safe_params(&self, i: usize) -> Params {
        let s = &self.slots[i];
        let algo = self.source.as_ref().map(|x| x.algo()).unwrap_or_default();
        let key = TuneStore::key(&s.device.fingerprint, &s.worker_version, &s.device.driver, &algo);
        let mut p = Params::new();
        for k in &s.knobs {
            p.insert(k.name.clone(), k.default);
        }
        if let Some(t) = self.tune_store.get(&key) {
            for (k, v) in &t.chosen {
                p.insert(k.clone(), *v);
            }
        }
        if let Some(pins) = self.settings.pins.get(&s.device.fingerprint) {
            for (k, v) in pins {
                p.insert(k.clone(), *v);
            }
        }
        p
    }

    fn open_slot(&mut self, i: usize) {
        let launcher = self.slots[i].launcher.clone();
        let name = self.slots[i].device.name.clone();
        // F-301's first open: the worker is not launched at all while its only document belongs to another worker
        {
            let is_base = self.slots[i].base_launcher.as_ref().map_or(true, |b| b.exe == self.slots[i].launcher.exe);
            let versions: Vec<(u8, Option<u64>)> = self.coords.iter().map(|(c, x)| (*c, x.doc.get("version").and_then(|v| v.as_u64()))).collect();
            let session_version = self.algo_params.get("version").and_then(|v| v.as_u64());
            if base_open_waits(&versions, self.slots[i].route.coord, is_base, session_version) {
                let w = "waits for a document its worker serves (the only one known is another worker's)".to_string();
                if self.slots[i].message != w {
                    self.log("app", &format!("{name}: {w}; the card opens when such a coordinate registers (F-301)"));
                    self.slots[i].message = w;
                }
                self.slots[i].state = SlotState::Idle;
                self.slots[i].restart_after = Some(Instant::now() + Duration::from_secs(5));
                return;
            }
        }
        // F-383 (the second Octominer rig, 16 October 2026): the launch runs on its own thread and the tick goes on;
        // the hello (or the fault) arrives on launch_rx and finish_open takes it up; a slot closed meanwhile ends the
        // arriving process. A launch already in flight for the slot is never doubled.
        if self.slots[i].launch_pending.is_some() || self.slots[i].launch_queued.is_some() {
            return;
        }
        self.launch_seq += 1;
        let gen = self.launch_seq;
        // F-471 (the second Linux proof host under 0.2.25, 17 October 2026): at most launch_cap launches in flight at
        // once; the rest wait queued in the slot's opening state and spawn as each hello or fault arrives, the hello
        // clock starting at the actual spawn, never at the queue
        let in_flight = self.launches_in_flight();
        let queued = self.slots.iter().filter(|s| s.launch_queued.is_some()).count();
        if !launch_spawns_now(in_flight, self.launch_cap, queued) {
            let exe = launcher.exe.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default();
            self.slots[i].launch_queued = Some(gen);
            self.slots[i].state = SlotState::Opening;
            self.slots[i].message = format!("waiting to launch {exe} ({in_flight} launches in flight, {} allowed at once)", self.launch_cap);
            self.slots[i].proc = None;
            let line = format!("{name}: launch queued behind {in_flight} in flight and {queued} queued ({} allowed at once on this host; F-471, F-475: the queue in arrival order)", self.launch_cap);
            self.log("app", &line);
            return;
        }
        self.spawn_launch(i, gen);
    }

    /// F-471: the launches running on their own threads now.
    fn launches_in_flight(&self) -> usize {
        self.slots.iter().filter(|s| s.launch_pending.is_some()).count()
    }

    /// F-471: queued launches spawned while the gate allows (the queue drained as each hello or fault arrives, in
    /// slot order).
    fn drain_launch_queue(&mut self) {
        // F-475: the earliest arrival goes first, never the lowest slot
        loop {
            if !launch_may_spawn(self.launches_in_flight(), self.launch_cap) {
                return;
            }
            let queued: Vec<(usize, u64)> = self.slots.iter().enumerate().filter_map(|(i, s)| s.launch_queued.map(|g| (i, g))).collect();
            let Some(i) = next_queued_launch(&queued) else { return };
            let Some(gen) = self.slots[i].launch_queued.take() else { return };
            self.spawn_launch(i, gen);
        }
    }

    /// F-383: the launch on its own thread (the half of open_slot the gate allows now).
    fn spawn_launch(&mut self, i: usize, gen: u64) {
        let launcher = self.slots[i].launcher.clone();
        let name = self.slots[i].device.name.clone();
        let tx = self.launch_tx.clone();
        let exe = launcher.exe.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default();
        self.slots[i].launch_queued = None;
        self.slots[i].launch_pending = Some((gen, Instant::now()));
        self.slots[i].state = SlotState::Opening;
        self.slots[i].message = format!("launching {exe} (the hello wait; a first run compiles its program and builds its dataset)");
        self.slots[i].proc = None;
        let line = format!("{name}: launching {exe} on its own thread (the tick never waits for a hello)");
        self.log("app", &line);
        if thread::Builder::new().name(format!("launch-{i}")).spawn(move || {
            let result = launcher.launch();
            let _ = tx.send(LaunchDone { slot: i, gen, result });
        }).is_err() {
            self.slots[i].launch_pending = None;
            self.slots[i].state = SlotState::Faulted;
            self.slots[i].message = "the launch thread could not start".into();
            let line = format!("{name}: worker did not start: the launch thread could not start");
            self.log("app", &line);
        }
    }

    /// F-383: the launch's result taken up by the tick (the half of open_slot that follows the hello).
    fn finish_open(&mut self, i: usize, launched: Result<WorkerProcess, WorkerError>) {
        // F-E34c (E3b's e3b-02 and e3b-06, 14 October 2026): a relaunch while the source holds no algorithm (a
        // re-dial that failed) refused the worker with "but the source needs " and the card sat down for the run;
        // the network's algorithm stands in until the source names one
        let algo = self.source.as_ref().map(|s| s.algo()).filter(|a| !a.is_empty()).unwrap_or_else(|| self.network_algo());
        let params = self.safe_params(i);
        let name = self.slots[i].device.name.clone();
        let mut p = match launched {
            Ok(p) => p,
            Err(e) => {
                self.slots[i].state = SlotState::Faulted;
                self.slots[i].message = e.to_string();
                let line = format!("{name}: worker did not start: {e}");
                self.log("app", &line);
                self.notice("bad", &format!("{name}: the worker did not start."), &e.to_string());
                return;
            }
        };
        match p.hello.algos.iter().find(|a| a.id == algo) {
            None => {
                let msg = format!("this worker carries {} but the source needs {algo}", p.hello.algos.iter().map(|a| a.id.clone()).collect::<Vec<_>>().join(", "));
                let _ = p.shutdown(Duration::from_secs(2));
                self.slots[i].state = SlotState::Refused;
                self.slots[i].message = msg.clone();
                let line = format!("{name}: {msg}");
                self.log("app", &line);
                return;
            }
            Some(a) if !a.vectors_ok => {
                let msg = format!("this worker's self-test for {algo} failed at start; it is not used");
                let _ = p.shutdown(Duration::from_secs(2));
                self.slots[i].state = SlotState::Faulted;
                self.slots[i].message = msg.clone();
                let line = format!("{name}: {msg}");
                self.log("app", &line);
                self.notice("bad", &format!("{name}: {msg}"), "");
                return;
            }
            Some(_) => {}
        }
        // F-476: no document yet (a relaunch whose hello overtook the re-dialled source's first document): the worker
        // is kept up and unconfigured until its coordinate's document registers; never the empty session document
        {
            let versions: Vec<(u8, Option<u64>)> = self.coords.iter().map(|(c, x)| (*c, x.doc.get("version").and_then(|v| v.as_u64()))).collect();
            // F-478: a bound worker whose version no served coordinate carries opens as the base worker (the slot's
            // launcher reverts), never on another version's document
            self.revert_launcher_without_a_lane(i, &versions);
            let bound_version = self.slot_bound_version(i);
            let registered = first_coord_for_worker(&versions, self.slots[i].route.coord, bound_version).is_some();
            let session_names = self.algo_params.get("version").and_then(|v| v.as_u64()).is_some();
            if configure_waits_for_document(registered, session_names) {
                let s = &mut self.slots[i];
                s.worker_version = p.hello.version.clone();
                s.worker_note = p.hello.note.clone();
                s.params = params;
                s.proc = Some(p);
                s.state = SlotState::Opening;
                s.message = "waiting for the pool's first document (the worker is up and takes it the moment it arrives)".into();
                s.last_telemetry = Instant::now();
                s.awaiting_document = Some(Instant::now());
                s.opened_at = None;
                s.active_job = None;
                s.telemetry = None;
                let line = format!("{name}: the worker is up before the pool's first document; it is configured when one registers (F-476)");
                self.log("app", &line);
                return;
            }
        }
        self.configure_launched(i, p, params);
    }

    /// F-478: the version the slot's current worker is bound to (igneum-worker-<backend>-v<N>), None for the base worker.
    fn slot_bound_version(&self, i: usize) -> Option<u64> {
        let s = &self.slots[i];
        let is_base = s.base_launcher.as_ref().map_or(true, |b| b.exe == s.launcher.exe);
        if is_base { None } else { version_bound_stem(&s.launcher.exe).map(|(_, v)| v) }
    }

    /// F-478: a slot left on a bound worker whose version no served coordinate carries goes back to its base worker
    /// before the open (the stop cleared the route and kept the worker; the base worker's rule then picks the lane).
    fn revert_launcher_without_a_lane(&mut self, i: usize, versions: &[(u8, Option<u64>)]) {
        if let Some(bv) = self.slot_bound_version(i) {
            if !versions.iter().any(|(_, v)| *v == Some(bv)) {
                if let Some(b) = self.slots[i].base_launcher.clone() {
                    let keep = self.slots[i].launcher.clone();
                    let mut next = b;
                    next.env = keep.env.clone(); next.workdir = keep.workdir.clone();
                    self.slots[i].launcher = next;
                    let line = format!("{}: no served coordinate carries version {bv}; the card opens on its base worker (F-478)", self.slots[i].device.name);
                    self.log("app", &line);
                }
            }
        }
    }

    /// F-476: the configure half of a launch (finish_open's tail), run at the hello when a document stands, else
    /// when the first coordinate document registers.
    fn configure_launched(&mut self, i: usize, mut p: WorkerProcess, params: Params) {
        let name = self.slots[i].device.name.clone();
        let algo = self.source.as_ref().map(|s| s.algo()).filter(|a| !a.is_empty()).unwrap_or_else(|| self.network_algo());
        let margin = self.cfg.horizon.margin_for(&self.slots[i].device);
        // VectorWork: a card opens on its coordinate's document, else the lowest coordinate's, never the session's
        // (the mini, 08:06 UK 13 October 2026: the session document was the last coordinate polled, which the Metal
        // worker refuses, and the card failed at every open)
        // F-301's engine half (the Octominer rig's RX 9060 XT, 16 October 2026: since the 2,860 succession the source's first
        // document is the raster one, bound to the GL worker, and the base OpenCL worker configured on it died at every
        // open): a base worker is never configured on a document whose version is bound to another worker; the first
        // configure takes the lowest coordinate the worker itself serves
        let versions: Vec<(u8, Option<u64>)> = self.coords.iter().map(|(c, x)| (*c, x.doc.get("version").and_then(|v| v.as_u64()))).collect();
        self.revert_launcher_without_a_lane(i, &versions);
        let bound_version = self.slot_bound_version(i);
        let first = first_coord_for_worker(&versions, self.slots[i].route.coord, bound_version);
        let algo_params = match first.and_then(|c| self.coords.get(&c)) {
            Some(ctx) => {
                let c = self.coords.iter().find(|(_, x)| x.digest == ctx.digest).map(|(c, _)| *c);
                self.slots[i].coord_digest = Some(ctx.digest.clone());
                if self.slots[i].route.coord.is_none() {
                    self.slots[i].route.coord = c;
                    self.slots[i].route.probing = true;
                    self.slots[i].stint = RateMeter::new(Duration::from_secs(router::PROBE_S));
                    self.slots[i].stint_since = Some(Instant::now());
                    self.slots[i].settle_said = false;
                }
                ctx.doc.clone()
            }
            None => self.algo_params.clone(),
        };
        let configure = ToWorker::Configure(Configure {
            device: self.slots[i].device.id.clone(),
            algo: algo.clone(),
            params: algo_params,
            stat_ms: STAT_MS,
            transition: WorkerTransition::Auto,
            memory_margin_bytes: margin,
        });
        if let Err(e) = p.send(&configure) {
            self.slots[i].state = SlotState::Faulted;
            self.slots[i].message = e.to_string();
            return;
        }
        let s = &mut self.slots[i];
        s.worker_version = p.hello.version.clone();
        s.worker_note = p.hello.note.clone();
        // the slot's knobs from its device's hello entry; under a per-slot visible-devices environment the hello
        // lists the one device as ordinal 0, so the first entry is the slot's
        let scoped = s.launcher.env.iter().any(|(k, _)| k.ends_with("_VISIBLE_DEVICES"));
        if let Some(hd) = p.hello.devices.iter().find(|d| d.id == s.device.id).or_else(|| if scoped { p.hello.devices.first() } else { None }) {
            s.knobs = hd.knobs.clone();
        }
        s.params = params;
        s.proc = Some(p);
        s.state = SlotState::Opening;
        s.message = String::new();
        s.last_telemetry = Instant::now();
        // F-470 (the first Windows proof host's 0.2.25 row, the engine log of 04:04Z: the ten-second rule fired fifteen
        // seconds into the worker's compile after a source change, three times): a configured worker compiles and
        // builds before its first stat; the stat clock takes the build's allowance until that stat, never the ten seconds
        s.build_until = Some(Instant::now() + worker::HELLO_BOUND);
        s.awaiting_document = None;
        s.opened_at = None;
        s.active_job = None;
        s.telemetry = None;
        let line = format!("{name}: configuring (margin {:.2} GB left to the driver and the display)", margin as f64 / devices::GIB as f64);
        self.log("app", &line);
        self.apply_power_cap(i);
    }

    fn close_slot(&mut self, i: usize, reason: &str) {
        self.slots[i].launch_pending = None; // F-383: a launch in flight arrives to a closed slot and is ended at the drain
        self.slots[i].launch_queued = None; // F-471: a queued launch never spawns for a closed slot
        self.slots[i].build_until = None; // F-470: a build's allowance ends with the worker
        self.slots[i].awaiting_document = None; // F-476
        if let Some(mut p) = self.slots[i].proc.take() {
            if let Err(e) = p.shutdown(Duration::from_secs(5)) {
                let msg = e.to_string();
                self.log("app", &msg);
            }
        }
        let s = &mut self.slots[i];
        s.active_job = None;
        s.telemetry = None;
        s.tune = None;
        if !matches!(s.state, SlotState::Faulted | SlotState::Refused | SlotState::Off) {
            s.state = if s.enabled { SlotState::Idle } else { SlotState::Off };
        }
        let line = format!("{}: closed ({reason})", s.device.name);
        self.log("app", &line);
    }

    fn restart_slot(&mut self, i: usize, reason: &str) {
        let line = format!("{}: restarting the worker: {reason}", self.slots[i].device.name);
        self.log("app", &line);
        {
            let s = &mut self.slots[i];
            // a restart during a transition is a recovery: the dataset is rebuilt cold and verified before resume
            let to = match s.transition.clone() {
                Transition::Preparing { to } | Transition::Ready { to } | Transition::Switching { to, .. } | Transition::Recovering { to } => Some(to),
                Transition::Current => self.current.as_ref().map(|t| template_epoch(self.verifier.as_ref(), t)),
            };
            if let Some(to) = to {
                let from = s.dataset_epoch.unwrap_or(to);
                if s.transition_started.is_none() {
                    s.transition_started = Some((Instant::now(), s.validated.rate(), from, to, false));
                    s.preparing_gap = None;
                }
                s.transition = Transition::Recovering { to };
            }
            s.dataset_epoch = None;
            s.next_epoch = None;
            s.switch_pending = None;
        }
        self.close_slot(i, reason);
        self.slots[i].restarts += 1;
        self.totals.worker_restarts += 1;
        if let Some(acc) = self.slots[i].tune.as_mut() {
            acc.hangs += 1;
        }
        if matches!(self.mining, MiningState::Mining | MiningState::Starting) && self.slots[i].enabled {
            self.open_slot(i);
        }
    }

    /// F-E11: the coordinate contexts and every card's route belong to the session that served them; a stop or a
    /// source switch clears them.
    fn clear_coordinate_state(&mut self) {
        self.coords.clear();
        self.plain_template_seen = false;
        self.first_coord_template_at = None;
        self.router_last = (0, BTreeMap::new());
        self.router_history.clear();
        self.session_anchor = None;
        for s in self.slots.iter_mut() {
            s.route = Default::default();
            s.stint_since = None;
            s.coord_digest = None;
            s.last_anchor = None;
            s.chunk_job = None;
            s.chunks_in_job = 0;
        }
    }

    fn stop(&mut self, reason: &str) {
        if matches!(self.mining, MiningState::Idle) {
            return;
        }
        self.mining = MiningState::Stopping;
        for i in 0..self.slots.len() {
            if self.slots[i].proc.is_some() {
                self.close_slot(i, reason);
            }
        }
        if let Some(s) = self.source.as_mut() {
            s.disconnect();
        }
        self.source = None;
        self.verifier = None;
        self.verify_tx = None;
        self.verify_tx_measure = None;
        self.verify_rx = None;
        self.verify_depth.store(0, AtomicOrdering::Relaxed);
        self.current = None;
        // F-E11 (13 October 2026): the coordinate contexts and every card's route belong to the session that served
        // them; a stop or a source switch clears them (the mini carried pool-b2's coordinate 2 onto the public pool,
        // whose notifies carry no coordinate, and mined a dead template: 0 accepted, 900 stale in fifteen minutes)
        self.clear_coordinate_state();
        self.mining = MiningState::Idle;
        self.paused_for = None;
        let line = format!("mining {reason}");
        self.log("app", &line);
    }

    fn cancel_slot_job(&mut self, i: usize) {
        let s = &mut self.slots[i];
        if let Some(job) = s.active_job.take() {
            if let Some(p) = s.proc.as_mut() {
                let _ = p.send(&ToWorker::Cancel(Cancel { job_id: job }));
            }
            // the next job replaces a job this device was mining: it is a switch sample
            s.switch_armed = matches!(s.state, SlotState::Mining);
        }
    }

    fn pause(&mut self, cause: PauseCause) {
        if !matches!(self.mining, MiningState::Mining | MiningState::Starting) {
            return;
        }
        self.mining = MiningState::Paused;
        self.paused_for = Some(cause);
        // F-E22 (E2, 14 October 2026): a stop writes its own line in Ember's log, with its cause and the clock
        let line = format!("mining paused at {} ({})", chrono::Local::now().format("%Y-%m-%d %H:%M:%S %Z"), pause_cause_word(cause));
        self.log("app", &line);
        for i in 0..self.slots.len() {
            if matches!(self.slots[i].state, SlotState::Mining | SlotState::Tuning) {
                self.cancel_slot_job(i);
                let has_release = self.slots[i].knobs.iter().any(|k| k.name == "release_dataset");
                if let Some(p) = self.slots[i].proc.as_mut() {
                    let _ = p.send(&ToWorker::Pause);
                    if cause == PauseCause::Memory || cause == PauseCause::User || cause == PauseCause::Source {
                        // the memory guard's pause releases the dataset and keeps the cache: the knob every worker
                        // parses (a worker without it answers so and keeps both; the Metal worker honours it). A
                        // user's pause releases it too (the project's read of the founder's laptop, 16:4x UK, 12 October
                        // 2026: the Metal worker kept the GPU busy after "pause"): the card idles to zero, the
                        // dataset is rebuilt on resume.
                        // F-E14 (the two-card Windows host's log): only a worker whose hello lists the knob is sent it; the CUDA worker answered "unknown knob" on every pause
                        if has_release {
                            let _ = p.send(&ToWorker::Set(proto::Set { knob: "release_dataset".into(), value: 1.0 }));
                            self.slots[i].last_set = "release_dataset".into();
                        }
                    }
                }
                self.slots[i].state = SlotState::Paused;
            }
        }
        if cause == PauseCause::Memory {
            let rate: f64 = self.slots.iter_mut().map(|s| s.validated.rate()).sum();
            self.pressure_paused_at = Some((Instant::now(), rate));
        }
        let why = match cause {
            PauseCause::User => "paused",
            PauseCause::Game => "paused for your game",
            PauseCause::Rate => "paused while electricity is dear",
            PauseCause::Battery => "paused on battery",
            PauseCause::Sleep => "paused for sleep",
            PauseCause::Memory => "paused under memory pressure (the dataset released, the cache kept)",
            PauseCause::Heat => "resting: the room is at the set point (heat mode)",
            PauseCause::Source => "pool unreachable; the cards idle until it answers",
        };
        self.log("app", why);
    }

    /// The work source answers nothing: the cards are idled (the dataset released, the worker kept, never relaunched)
    /// and the pool alone is retried on a backoff, 30 s doubling to five minutes, one event line per change; the
    /// cards are touched again only when the pool answers (the project's order of 17:2x UK, 12 October 2026, after the three-card test machine's
    /// fifty-four toasts in three hours: the NVIDIA app announces every program start on the card).
    fn source_lost(&mut self, now: Instant, problem: &str) {
        if !matches!(self.mining, MiningState::Mining | MiningState::Starting) {
            return;
        }
        self.pause(PauseCause::Source);
        // a reload (the session ended itself to re-read the document) re-dials at once, up to three in ten minutes
        self.source_reloads.retain(|t| now.duration_since(*t) < Duration::from_secs(600));
        // F-E30: the pool's own reconnect notice (a roll) re-dials at once too, under the same bound
        let reload = (problem.contains("reloading") || problem.contains(work::RECONNECT_WORDS)) && self.source_reloads.len() < RELOADS_BEFORE_BACKOFF;
        if reload {
            self.source_reloads.push(now);
        }
        let wait = if reload { SOURCE_RELOAD_WAIT } else { SOURCE_RETRY_FIRST };
        self.source_retry = Some((now + wait, wait));
        let line = if reload { format!("the pool session reloads in {} s ({problem})", wait.as_secs()) } else { format!("pool unreachable; trying again in {} s ({problem})", wait.as_secs()) };
        self.log("app", &line);
    }

    /// The backoff's turn: the pool is asked once; an answer resumes the cards, silence doubles the wait.
    fn retry_source(&mut self, now: Instant) {
        let Some((at, wait)) = self.source_retry else { return };
        if self.paused_for != Some(PauseCause::Source) {
            self.source_retry = None;
            return;
        }
        if now < at {
            return;
        }
        // the session is judged on its connected state at a LATER tick, never in the instant it is created (it says
        // "connecting" then): the pool's restart of 21:04 UK, 12 October 2026, left every 0.2.4 app paused with a
        // connected session and the backoff doubling, the founder's four machines idle on a pool that answered
        if let Some(s) = self.source.as_ref() {
            if s.connected() && s.problem().is_none() {
                self.source_retry = None;
                self.source_problem_since = None;
                self.notices.retain(|n| n.text != "The work source is not reachable.");
                self.totals.source_reconnects += 1;
                self.log("app", "the pool answers; mining resumes");
                self.resume();
                return;
            }
        }
        // the dial's turn: a session that could not be opened doubles the wait; one that opened is judged next tick
        let setting = if self.on_backup_since.is_some() { self.settings.work_source_backup.clone() } else { self.settings.work_source.clone() };
        match self.switch_source(&setting) {
            Ok(_) => {
                // the connected state settles within the dial's own timeout; the next ticks read it
                self.source_retry = Some((now + Duration::from_secs(5), wait));
            }
            Err(e) => {
                let next = (wait * 2).max(SOURCE_RETRY_FIRST).min(SOURCE_RETRY_MAX);
                self.source_retry = Some((now + next, next));
                let line = format!("pool unreachable; trying again in {} s (the dial: {e})", next.as_secs());
                self.log("app", &line);
            }
        }
    }

    fn resume(&mut self) {
        if !matches!(self.mining, MiningState::Paused) {
            return;
        }
        let was = self.paused_for;
        self.mining = MiningState::Mining;
        self.paused_for = None;
        let line = format!("mining resumed at {}{}", chrono::Local::now().format("%Y-%m-%d %H:%M:%S %Z"), was.map(|c| format!(" (after a pause for {})", pause_cause_word(c))).unwrap_or_default());
        self.log("app", &line);
        self.game_gone_since = None;
        for s in &mut self.slots {
            if matches!(s.state, SlotState::Paused) {
                if let Some(p) = s.proc.as_mut() {
                    let _ = p.send(&ToWorker::Resume);
                }
                s.state = SlotState::Mining;
                s.active_job = None;
            }
        }
        // a fresh pool session (the source dialled again and answering) reopens every card closed on the old
        // session's document (reopens_on_fresh_session); the relaunch takes the new session's document
        if was == Some(PauseCause::Source) {
            let reopen: Vec<usize> = (0..self.slots.len()).filter(|&i| reopens_on_fresh_session(&self.slots[i].state, self.slots[i].document_fault, self.slots[i].enabled)).collect();
            for i in reopen {
                let line = format!("{}: a fresh pool session; the card closed on the old session's document ({}) is reopened", self.slots[i].device.name, self.slots[i].message);
                self.log("app", &line);
                self.slots[i].document_fault = false;
                self.slots[i].message = String::new();
                self.slots[i].state = SlotState::Idle;
                self.restart_slot(i, "a fresh pool session");
            }
        }
        self.log("app", "resumed");
    }

    fn remember_sent(&mut self, id: String, sent: Sent) {
        self.sent.insert(id.clone(), sent);
        self.sent_order.push_back(id);
        while self.sent_order.len() > 256 {
            if let Some(old) = self.sent_order.pop_front() {
                self.sent.remove(&old);
                self.submitted_seen.retain(|j, _| !j.starts_with(&old));
            }
        }
    }

    /// Hand the slot its next nonce chunk of the current job (or a tuning job).
    fn dispatch(&mut self, i: usize) {
        let Some(template) = self.slot_template(i) else { return };
        // a coordinate slot whose worker is not yet configured on its coordinate's document takes no job of it
        if let Some(c) = self.slots[i].route.coord {
            if self.coords.get(&c).map(|x| x.digest.as_str()) != self.slots[i].coord_digest.as_deref() {
                return;
            }
        }
        let source_algo = self.source.as_ref().map(|x| x.algo()).unwrap_or_default();
        let lanes = self.slots.iter().filter(|s| s.enabled).count().max(1) as u64;
        let lane = self.slots.iter().take(i).filter(|s| s.enabled).count() as u64;
        // the source's range (a pool session's, or the whole space) split into one lane per enabled device
        let lane_size = (template.nonce_count / lanes).max(1);
        let chunk = NONCE_CHUNK.min(lane_size);
        let s = &mut self.slots[i];
        if s.proc.is_none() {
            return;
        }
        // F-E5: a lane is searched once per job; when the slot has taken its whole lane of this job it waits for the
        // next job instead of starting the lane again (the same nonces would be re-found and resubmitted)
        if s.chunk_job.as_deref() != Some(template.job_id.as_str()) {
            s.chunk_job = Some(template.job_id.clone());
            s.chunks_in_job = 0;
            s.lane_done_said = false;
        }
        if s.chunks_in_job.saturating_mul(chunk) >= lane_size {
            if !s.lane_done_said {
                s.lane_done_said = true;
                let name = s.device.name.clone();
                let line = format!("{name}: its nonce lane of job {} is searched through ({} nonces); it waits for the next job rather than search the lane again", template.job_id, lane_size);
                self.log("app", &line);
            }
            return;
        }
        let s = &mut self.slots[i];
        s.chunks_in_job += 1;
        let offset = s.chunk_index.wrapping_mul(chunk) % lane_size;
        let start = template.nonce_start.wrapping_add(lane.wrapping_mul(lane_size)).wrapping_add(offset);
        let count = chunk.min(lane_size - offset).max(1);
        s.chunk_index = s.chunk_index.wrapping_add(1);
        let (tuning, difficulty, share_target) = match s.tune.as_ref() {
            Some(acc) => (true, acc.difficulty, stubhash::target_for_difficulty(acc.difficulty)),
            None => {
                // the easier of the node's share target and the measurement target: results under the
                // measurement target feed the validated rate, those under the node's are submitted; on the
                // share lane the pacing rule (job_target_for); on a pool session the same rule with the
                // measurement target eased to POOL_RESULTS_PER_S (the pool's one target for every card)
                let pool = self.source.as_ref().map_or(false, |src| !src.economics().solo);
                let lane = template.share.is_some();
                let measure = stubhash::target_for_difficulty(if pool && !lane { pool_measure_difficulty(s.measure_difficulty) } else { s.measure_difficulty });
                let t = job_target_under(&measure, &template.share_target, lane || pool, self.pace_chain_target && lane);
                (false, stubhash::difficulty_for_target(&t), t)
            }
        };
        // the boundary the worker sees: on a pool session the job target itself (job_boundary_for)
        let pool_session = self.source.as_ref().map_or(false, |src| !src.economics().solo);
        let boundary = job_boundary_for(pool_session, &template.boundary, &share_target);
        // F-E36 (the two-card Windows host on 0.2.7, 14 October 2026): the id carried the worker's own device index, so the 5090 under CUDA
        // and the OpenCL worker's device 0 built the same id, the sent map held one slot for both, and the 5090's
        // share read "share for another device's job"; the slot index is unique across workers
        // the GPU team's reading of 14:0x UK, 15 October 2026 (the three invalids at the switch from coordinate 1 to 16, all
        // at one height): a pool names its jobs by height, the same id on every coordinate, so a result of the old
        // coordinate's last batch, emitted under its own job id after the cancel, found the NEW coordinate's record
        // under that id and was verified under the new document; the coordinate rides the id, so a result returns
        // to its own record and its own document (the digest check then cancels it as the old coordinate's work)
        let id = format!("{}.{}.{}{}{}", template.job_id, i, s.chunk_index, template.coord.map(|c| format!(".c{c}")).unwrap_or_default(), if tuning { ".t" } else { "" });
        let algo = source_algo;
        // the keyed document's secret rides the job to a worker that declared the extension, and to no other
        let keyed = bond::keyed_rounds(&self.algo_params).is_some();
        let sk = if job_takes_the_secret(keyed, s.keyed_job, &s.transport) { self.cfg.bond_secret.as_ref().map(|b| b.job_field()) } else { None };
        // VectorWork: the period's programme anchor rides the job as the worker's r0d_anchor knob, set before the job
        // and only on a change (the worker reads it at every programme build; under a period-1 document every job)
        // F-E9: the set goes only to a worker whose hello lists the knob; every worker reads the anchor from the
        // document itself since F-E6, so a worker without the knob needs no set and answers no fault
        if let Some(a) = template.program_anchor.as_deref() {
            if s.last_anchor.as_deref() != Some(a) {
                if let (Some(p), true) = (s.proc.as_mut(), s.knobs.iter().any(|k| k.name == "r0d_anchor")) {
                    let _ = p.send(&ToWorker::SetText(proto::SetText { knob: "r0d_anchor".into(), value: a.to_string() }));
                    // F-470: the worker rebuilds its programme on the set and may say nothing for longer than the
                    // ten-second rule; the stat clock takes the build's allowance until its first mining stat after it
                    s.build_until = Some(Instant::now() + worker::HELLO_BOUND);
                }
                // the GPU team's pool-l16 session of 15:0x UK, 15 October 2026 (pinned, no coordinate switch): three invalids at a
                // period boundary, the worker's last batch under the old anchor's programme tagged with the new job while
                // it recompiles; the anchor change is a switch for the straddle rule, so the invalids before the first
                // valid result under the new anchor are the switch's, never the coordinate's refusal
                if s.last_anchor.is_some() {
                    s.doc_valid_seen = false;
                    s.switch_invalids = 0;
                }
                s.last_anchor = Some(a.to_string());
                s.last_set = "r0d_anchor".into();
            }
        }
        let job = ToWorker::Job(Job { job_id: id.clone(), height: template.height, header: proto::hex_encode(&template.header), boundary: proto::hex_encode(&boundary), share_target: proto::hex_encode(&share_target), nonce_start: start, nonce_count: count, clean: true, algo: algo.clone(), sk });
        let sent = s.proc.as_mut().map(|p| p.send(&job)).unwrap_or(Err(worker::WorkerError::Gone));
        match sent {
            Ok(_) => {
                let was_mining = (s.switch_armed || s.active_job.is_some()) && matches!(s.state, SlotState::Mining) && matches!(s.transition, Transition::Current | Transition::Preparing { .. } | Transition::Ready { .. });
                s.switch_armed = false;
                s.active_job = Some(id.clone());
                if s.switch_pending.is_none() && was_mining {
                    // a job replacing a job on a mining device: the switch delay sample (the first job after an
                    // open, a prepare or a recovery is not one)
                    s.switch_pending = Some((id.clone(), Instant::now()));
                }
                // the epoch by the document the slot mines (a coordinate's verifier when it has one): the result
                // check compares with the same verifier (the pods on the test pool, 03:1x UK 13 October 2026: every
                // result cancelled as another epoch's when the session's document was not the coordinate's)
                let digest = self.slots[i].coord_digest.clone().unwrap_or_else(|| self.params_digest.clone());
                let epoch = template_epoch(self.verifier_for_digest(&digest).as_ref(), &template);
                // an epoch change at the first job of the new epoch: SWITCHING (warm when READY for it, cold otherwise)
                let s = &mut self.slots[i];
                if let Some(cur) = s.dataset_epoch {
                    if cur != epoch && !matches!(s.transition, Transition::Switching { .. } | Transition::Recovering { .. }) {
                        let warm = s.next_epoch == Some(epoch);
                        s.transition = Transition::Switching { from: cur, to: epoch };
                        s.transition_started = Some((Instant::now(), s.validated.rate(), cur, epoch, warm));
                        s.preparing_gap = None;
                        let line = format!("{}: epoch {cur} to {epoch}: switching ({})", s.device.name, if warm { "warm, the next dataset was prepared" } else { "cold, the dataset is rebuilt" });
                        self.log("app", &line);
                    }
                }
                self.remember_sent(id, Sent { template, algo, params_digest: digest, epoch, difficulty, tuning, slot: i });
            }
            Err(e) => {
                let line = format!("{}: job not sent: {e}", self.slots[i].device.name);
                self.log("app", &line);
            }
        }
    }

    /// A device that reports hashing but produces no validated result for twenty times the interval its
    /// measurement target promises (at least two minutes) is not mining whatever it reports: said, restarted,
    /// and stopped on a repeat within the hour (F-22: never silent). A reference check that is behind, a
    /// transition in progress or a paused device is not judged.
    fn watch_no_results(&mut self) {
        let now = Instant::now();
        if !matches!(self.mining, MiningState::Mining) {
            return;
        }
        // a slow reference check holds the rate-based verdict back (results exist, they are only late), never the
        // worker's own word that it hashes nothing
        let late = self.late_since.is_some();
        // F-E34b (E3b, 14 October 2026): a worker whose results cannot be counted is not judged on them: no current
        // job (the source re-dialling after a no-job gap), a source that is absent or reports a problem, or a
        // verifier dropping results unverified in this window (the flood at a low share target); the fast path on
        // the worker's own zero-hash word still runs
        let unverified_now = self.totals.unverified;
        let uncountable = self.current.is_none()
            || self.source.as_ref().map_or(true, |src| !src.connected() || src.problem().is_some())
            || unverified_now > self.unverified_at_last_watch;
        self.unverified_at_last_watch = unverified_now;
        for i in 0..self.slots.len() {
            let s = &mut self.slots[i];
            if !matches!(s.state, SlotState::Mining) || !matches!(s.transition, Transition::Current) || s.active_job.is_none() {
                // F-202's second face (the second desktop's 5090 on 0.2.19 and 0.2.20, 03:18 UK, 16 October 2026): a slot whose nonce lane
                // of the job is searched through waits for the next job by F-E5, and that wait is not hashing without
                // results: its no-result clock restarts while it waits, so the first chunk of the next job starts a
                // fresh count (a worker that holds a job and finds nothing is still caught: its clock runs with the job)
                if s.lane_done_said && s.active_job.is_none() {
                    s.last_validated_at = Some(now);
                }
                continue;
            }
            let reported = s.reported.rate();
            let Some(last) = s.last_validated_at else { continue };
            // the worker says it hashes nothing on the job it holds (zero hashes in every stat for IDLE_HASHES_AFTER):
            // the fast path, before the averaged rate and the measurement interval say anything
            if let Some(since) = s.idle_hashes_since {
                if idle_hashes_too_long(since, last, now) {
                    let job = s.active_job.clone().unwrap_or_default();
                    s.idle_hashes_since = None;
                    s.last_validated_at = Some(now);
                    let name = s.device.name.clone();
                    let line = format!("{name}: its stats report zero hashes for {:.0} s on job {job} while it holds it in the mining state (a worker that stopped after a cancel); restarted", now.duration_since(since).as_secs_f64());
                    self.log("app", &line);
                    self.restart_slot(i, "the worker reports hashing nothing on the job it holds");
                    continue;
                }
            }
            if reported <= 0.0 || late || uncountable {
                if uncountable {
                    // the clock restarts when the results can count again
                    s.last_validated_at = Some(now);
                }
                continue;
            }
            let expected_s = s.measure_difficulty / reported;
            let raw = (expected_s * 20.0).max(120.0);
            let (limit, _within_a_day) = secs_guarded(raw);
            if !raw.is_finite() {
                // a measurement interval that is not a number is itself the fault to name, not to convert: the
                // target is reset to the reported rate (one result a second), said once a minute. An interval
                // beyond a day is not that fault: the limit is a day and the watch goes on.
                if s.measure_stepped_at.map_or(true, |t| now.duration_since(t) > Duration::from_secs(60)) {
                    let line = format!("{}: the expected result interval is not a finite number (difficulty {}, rate {:.3} MH/s); the measurement target is reset", s.device.name, s.measure_difficulty, reported / 1e6);
                    s.measure_difficulty = reported.max(1000.0).min(MAX_MEASURE_DIFFICULTY);
                    s.measure_stepped_at = Some(now);
                    self.log("app", &line);
                }
                continue;
            }
            if now.duration_since(last) <= limit {
                continue;
            }
            s.no_result_episodes.push_back(now);
            while s.no_result_episodes.front().map_or(false, |t| now.duration_since(*t) > Duration::from_secs(3600)) {
                s.no_result_episodes.pop_front();
            }
            s.last_validated_at = Some(now);
            let name = s.device.name.clone();
            let line = format!("{name}: reports {:.3} MH/s but produced no result the reference accepts for {:.0} s (one every {:.1} s was expected at its measurement target); its results do not meet the target it was given, or it is not hashing what it reports", reported / 1e6, now.duration_since(last).as_secs_f64(), expected_s);
            self.log("app", &line);
            // F-E47 (the rig's RTX 2060, 14 October 2026: the worker's version 14 form on Turing verifies nothing): a card
            // stopped for no results on a coordinate marks it refused and takes the next one the source serves; it stops
            // only when no served coordinate is left
            if self.slots[i].no_result_episodes.len() >= 2 {
                if let Some(c) = self.slots[i].route.coord {
                    let others: Vec<u8> = self.coords.keys().copied().filter(|k| *k != c && !self.slots[i].route.refused.contains(k)).collect();
                    if !others.is_empty() {
                        if !self.slots[i].route.refused.contains(&c) { self.slots[i].route.refused.push(c); }
                        self.slots[i].no_result_episodes.clear();
                        let line = format!("{name}: coordinate {c} produces nothing this card's worker verifies; it is refused for this card and the router takes one of {:?}", others);
                        self.log("app", &line);
                        self.notice("warn", &format!("{name}: coordinate {c} is off this card (no verified result twice in an hour); the next served coordinate is taken."), "");
                        self.restart_slot(i, "the next served coordinate after no results");
                        continue;
                    }
                }
                self.close_slot(i, "hashing without results, twice in an hour");
                self.slots[i].state = SlotState::Faulted;
                self.slots[i].message = "stopped: it reports hashing but produces nothing the reference accepts (twice in an hour); switch the card off and on to try again after checking the card and the worker".into();
                self.notice("bad", &format!("{name} stopped: it reports hashing but finds nothing."), "Its results do not meet the target it was given, or it is not hashing what it reports. The log has the times. Mining on it does not resume by itself.");
            } else {
                self.notice("warn", &format!("{name} reports hashing but finds nothing; restarting its worker."), "If it happens again within the hour the device stops with the reason.");
                self.restart_slot(i, "hashing without results");
            }
        }
    }

    /// Automatic pool transitions (the plan's point 10): a first source unreachable for a minute hands over to
    /// the backup the user named; on the backup, the first source is tried again every ten minutes and taken
    /// back when it answers. Every move is logged and noticed; no move happens without a backup set.
    fn failover(&mut self) {
        let now = Instant::now();
        if matches!(self.settings.work_source_backup, WorkSourceSetting::Unset) || !matches!(self.mining, MiningState::Mining | MiningState::Starting | MiningState::Paused) || self.step_hold_until.is_some() {
            return;
        }
        match self.on_backup_since {
            None => {
                if self.source_problem_since.map_or(false, |t| now.duration_since(t) >= FAILOVER_AFTER) {
                    let setting = self.settings.work_source_backup.clone();
                    match self.switch_source_with(&setting, self.source.as_ref().map(|x| x.algo()).filter(|a| !a.is_empty())) {
                        Ok(name) => {
                            self.on_backup_since = Some(now);
                            self.primary_retry_at = Some(now);
                            self.source_problem_since = None;
                            self.totals.source_switches += 1;
                            let line = format!("the work source stayed unreachable for {} s; moved to the backup {name}", FAILOVER_AFTER.as_secs());
                            self.log("app", &line);
                            self.notice("warn", "Moved to the backup source.", &format!("{name}; the first source is tried again every {} minutes.", PRIMARY_RETRY_EVERY.as_secs() / 60));
                        }
                        Err(e) => {
                            let line = format!("the backup source could not be taken: {e}");
                            self.log("app", &line);
                            // not again before another minute
                            self.source_problem_since = Some(now);
                        }
                    }
                }
            }
            Some(_) => {
                if self.primary_retry_at.map_or(true, |t| now.duration_since(t) >= PRIMARY_RETRY_EVERY) {
                    self.primary_retry_at = Some(now);
                    let setting = self.settings.work_source.clone();
                    match self.switch_source_with(&setting, self.source.as_ref().map(|x| x.algo()).filter(|a| !a.is_empty())) {
                        Ok(name) => {
                            self.on_backup_since = None;
                            self.source_problem_since = None;
                            self.totals.source_switches += 1;
                            let line = format!("the first source answers again; moved back to {name}");
                            self.log("app", &line);
                            self.notice("info", "Back on the first source.", &name);
                        }
                        Err(e) => {
                            let line = format!("the first source is still not reachable ({e}); staying on the backup");
                            self.log("app", &line);
                        }
                    }
                }
            }
        }
    }

    /// Take another source: connect it first (the old one keeps serving until the new one answers), then cancel
    /// every device's job, drop the results in flight (their template is gone), and hand the templates over; the
    /// new source's document goes through take_template on its first template, so a document change is handled
    /// as any other. The algorithm must match the one the workers were configured for.
    /// F-E33: the miner's work-authority key on a source the supervisor builds, from the key kept at start or the
    /// key file when start did not load it (a source built before the key existed signs from here on).
    fn attach_signer(&mut self, src: &mut Box<dyn work::WorkSource>) {
        if self.signer.is_none() {
            if let Some(kp) = self.cfg.key_path.clone() {
                if let Ok(k) = minerkey::MinerKey::load_or_create(&kp) {
                    self.signer = Some(Arc::new(k));
                }
            }
        }
        if let Some(k) = self.signer.clone() {
            src.set_signer(k);
        }
    }

    fn switch_source(&mut self, setting: &WorkSourceSetting) -> Result<String, String> {
        self.switch_source_with(setting, None)
    }

    /// F-E37 (the two-card Windows host on 0.2.7 at 37f355bc, 07:50 UK, 14 October 2026): the re-dial after unanswered submissions took
    /// the engine's source away and then built the new one with the algorithm read from the source just taken,
    /// an empty string; the pool authorised the session, the session ended on the algorithm, the connect failed and
    /// the engine held NO source ("name empty") while it tried again every 120 s, the cards "mining" with nothing
    /// to mine. The algorithm now comes from the caller (the re-dial carries the old source's), else from the
    /// source still held, else from the workers (network_algo), never empty.
    fn switch_source_with(&mut self, setting: &WorkSourceSetting, algo_hint: Option<String>) -> Result<String, String> {
        let address = self.settings.payout_address.clone().ok_or_else(|| "no payout address".to_string())?;
        let algo = resolve_dial_algo(algo_hint, self.source.as_ref().map(|s| s.algo()), self.settings.algo.clone(), || self.network_algo());
        let mut new = work::source_for_with_fee(setting, &algo, self.cfg.dev_fee.clone().ok())?;
        new.set_receipts_dir(&self.cfg.data_dir.join("receipts"));
        // F-E33: the re-dialled source signs its authorize with the same key as the first dial
        self.attach_signer(&mut new);
        new.connect(&address, &self.settings.worker_name)?;
        if !new.algo().is_empty() && !algo.is_empty() && new.algo() != algo {
            let other = new.algo();
            new.disconnect();
            return Err(format!("the other source mines {other}; the workers run {algo}"));
        }
        let old_name = self.source.as_ref().map(|s| s.name());
        if let Some(mut old) = self.source.take() {
            old.disconnect();
        }
        let name = new.name();
        let same = same_session(old_name.as_deref(), &name);
        self.source = Some(new);
        self.dialed_at = Some(Instant::now());
        self.declared_rate_hps = work::LAST_RATE_HPS.load(std::sync::atomic::Ordering::Relaxed);
        self.last_accepted_at = None;
        self.not_submitted_at_mark = self.totals.not_submitted;
        self.current = None;
        // F-384 (the pool's three rolls of 17 October 2026, 00:02 to 00:09Z: every host cleared its lanes on each
        // client.reconnect, rebuilt every verifier and re-probed every lane with a relaunch each): a re-dial to the SAME
        // pool keeps the lanes' contexts and the cards' routes; the old session's jobs are let go, the next template on
        // each lane carries on; a different pool starts clean
        if same {
            for ctx in self.coords.values_mut() {
                ctx.let_go = true;
            }
            self.log("app", "the same pool re-dialled: the lanes' contexts and the cards' routes are kept; the old session's jobs are let go and the next templates carry on (F-384)");
        } else {
            self.clear_coordinate_state();
        }
        self.sent.clear();
        self.sent_order.clear();
        for i in 0..self.slots.len() {
            if matches!(self.slots[i].state, SlotState::Mining) {
                self.cancel_slot_job(i);
                self.slots[i].switch_pending = None;
            }
        }
        self.last_job_at = Some(Instant::now());
        Ok(name)
    }

    /// A submission the source has not answered for two minutes while the source reports no problem is a silent
    /// source (the class the protocol team named on 10 October: results flow, nothing is accepted or refused): said as a
    /// state, the source reconnected; a second two minutes of it stops mining with the reason. A block that is
    /// simply not due yet is not this: the state line shows the expected interval beside the submitted count.
    /// F-E14: a connected source that has sent no job for NO_WORK_AFTER while mining is a stalled job stream (the two-card Windows host sat
    /// idle thirteen minutes on 13 October 2026 with the pool "connected" and both cards ready); the source is
    /// redialled and the stall is said, again every NO_WORK_AFTER until a job arrives. The window reads the wait in
    /// `waiting_for_work_s`.
    fn waiting_for_work_s(&self, now: Instant) -> u64 {
        if !matches!(self.mining, MiningState::Mining | MiningState::Starting) { return 0; }
        let connected = self.source.as_ref().map_or(false, |s| s.connected());
        match self.last_job_at { Some(t) if connected => now.duration_since(t).as_secs(), _ => 0 }
    }
    fn watch_no_work(&mut self, now: Instant) {
        if self.step_hold_until.is_some() { return; } // F-392: the step hold owns the next dial
        if self.waiting_for_work_s(now) < NO_WORK_AFTER.as_secs() { return; }
        self.no_work_redials += 1;
        let n = self.no_work_redials;
        let line = format!("no job from the source for {} s while it reports connected (redial {n}); the source is redialled", NO_WORK_AFTER.as_secs());
        self.log("app", &line);
        self.notice("warn", "The pool is sending no work.", &format!("No job for {} s on a connected source; redialled ({n}).", NO_WORK_AFTER.as_secs()));
        let setting = self.settings.work_source.clone();
        let setting = if self.on_backup_since.is_some() { self.settings.work_source_backup.clone() } else { setting };
        match self.redial(&setting) {
            Ok(name) => { self.totals.source_reconnects += 1; let line = format!("redialled {name} after no work"); self.log("app", &line); }
            Err(e) => { let line = format!("the redial after no work failed: {e}; the retry runs"); self.log("app", &line); self.last_job_at = Some(now); }
        }
    }
    /// F-E14c (the two-card Windows host's log, 13 October 2026): a redial of a session the engine distrusts (silent, or workless) closes
    /// that session FIRST, then dials; the pool answers a second session under the same worker name "unauthorised"
    /// while the first is open. A user's switch keeps the old session until the new one answers (switch_source).
    fn redial(&mut self, setting: &WorkSourceSetting) -> Result<String, String> {
        // F-E37: the algorithm the old session mined rides into the new dial; the old source is closed first (F-E14c)
        let algo = self.source.as_ref().map(|s| s.algo()).filter(|a| !a.is_empty());
        if let Some(mut old) = self.source.take() {
            old.disconnect();
        }
        let r = self.switch_source_with(setting, algo);
        if r.is_err() && self.source.is_none() {
            // the engine holds no source after a failed re-dial: said plainly, and the retry path owns the next dial
            self.log("app", "the re-dial failed and no session is held; the cards wait for the next dial (the retry runs)");
        }
        r
    }
    /// F-392: the step watch beside the unanswered watch: a session the pool holds at a step past the card (its first
    /// burst at the lowest step read as rate, the raised step standing until a re-dial) is re-dialled declaring the
    /// reported rate, so the pool opens the new session at that rate's step; once per STEP_REDIAL_EVERY.
    fn watch_step(&mut self, now: Instant) {
        if !matches!(self.mining, MiningState::Mining) {
            return;
        }
        // the second half: the hold passed, the session is dialled declaring the reported rate
        if let Some(until) = self.step_hold_until {
            if now < until {
                return;
            }
            self.step_hold_until = None;
            let declared = work::LAST_RATE_HPS.load(std::sync::atomic::Ordering::Relaxed);
            let setting = self.settings.work_source.clone();
            let setting = if self.on_backup_since.is_some() { self.settings.work_source_backup.clone() } else { setting };
            match self.redial(&setting) {
                Ok(name) => { self.totals.source_reconnects += 1; let line = format!("redialled {name} declaring {:.2} MH/s after the step hold; the session opens at that rate's step (F-392)", declared as f64 / 1e6); self.log("app", &line); }
                Err(e) => { let line = format!("the step re-dial failed: {e}; the retry runs"); self.log("app", &line); }
            }
            return;
        }
        if self.source.is_none() {
            return;
        }
        let Some(dialed) = self.dialed_at else { return };
        if self.step_redial_at.map_or(false, |t| now.duration_since(t) < STEP_REDIAL_EVERY) {
            return;
        }
        let reported = self.totals.reported_hps as u64;
        let since_dial = now.duration_since(dialed);
        let mark = self.last_accepted_at.map_or(dialed, |t| t.max(dialed));
        let since_mark = now.duration_since(mark);
        let refused_rose = self.totals.not_submitted > self.not_submitted_at_mark;
        let Some(why) = step_mismatch(self.declared_rate_hps, reported, since_dial, since_mark, self.last_accepted_at.is_none(), refused_rose) else { return };
        self.step_redial_at = Some(now);
        work::LAST_RATE_HPS.store(reported, std::sync::atomic::Ordering::Relaxed);
        // the first half: the session is closed and stays closed past the pool's re-dial grace (a dial within it
        // resumes the standing step); the cards wait for the next dial
        if let Some(mut old) = self.source.take() {
            old.disconnect();
        }
        self.submitted_at.clear();
        self.unanswered_since = None;
        self.step_hold_until = Some(now + STEP_AWAY);
        let line = format!("the pool's step is past this machine: {why}; the session is closed for {} s (the pool's re-dial grace) and then re-dialled declaring {:.2} MH/s so it opens at that rate's step (F-392)", STEP_AWAY.as_secs(), reported as f64 / 1e6);
        self.log("app", &line);
        self.notice("warn", "The pool's share target ran past this machine.", &format!("The session is re-dialled in {} s declaring {:.2} MH/s.", STEP_AWAY.as_secs(), reported as f64 / 1e6));
    }
    fn watch_unanswered(&mut self, now: Instant) {
        if self.step_hold_until.is_some() { return; } // F-392: the step hold owns the next dial
        let oldest = self.submitted_at.values().min().copied();
        // F-E14b (the two-card Windows host's log, 22:10 to 22:21 UK, 13 October 2026): a pool that HOLDS some shares (the address at the
        // sealer's bound) while answering others is not silent; the old test took one held submission for silence and
        // redialled every two minutes into "unauthorised" (the pool refuses a second session under the same worker
        // name). Silent means no outcome of any kind for UNANSWERED_AFTER beside a submission that old.
        let answered_lately = self.last_outcome.map_or(false, |t| now.duration_since(t) <= UNANSWERED_AFTER);
        let silent = oldest.map_or(false, |t| now.duration_since(t) > UNANSWERED_AFTER) && !answered_lately;
        if !silent {
            self.unanswered_since = None;
            return;
        }
        match self.unanswered_since {
            None => {
                self.unanswered_since = Some(now);
                let n = self.submitted_at.len();
                let line = format!("the source has not answered {n} submission(s) for {} s while reporting no problem; the source is reconnected", UNANSWERED_AFTER.as_secs());
                self.log("app", &line);
                self.notice("warn", "The work source is not answering submissions.", &format!("{n} unanswered for {} s; reconnecting.", UNANSWERED_AFTER.as_secs()));
                let setting = self.settings.work_source.clone();
                let setting = if self.on_backup_since.is_some() { self.settings.work_source_backup.clone() } else { setting };
                match self.redial(&setting) {
                    Ok(name) => {
                        self.submitted_at.clear();
                        self.totals.source_reconnects += 1;
                        let line = format!("reconnected to {name}");
                        self.log("app", &line);
                    }
                    Err(e) => {
                        let line = format!("the reconnect failed: {e}");
                        self.log("app", &line);
                    }
                }
            }
            Some(t) if now.duration_since(t) > UNANSWERED_AFTER => {
                // never a stop (12 October 2026: the mini's Mac app went idle twice in one night at this site, a
                // socket loss the source never reported, the "Press Start" notice unseen; the pods beside it
                // redialled and mined on): the source is reconnected again every two minutes for as long as it
                // stays silent, the count named, mining kept so the first answer resumes the flow
                self.unanswered_since = Some(now);
                self.totals.source_reconnects += 1;
                let setting = self.settings.work_source.clone();
                let setting = if self.on_backup_since.is_some() { self.settings.work_source_backup.clone() } else { setting };
                let n = self.submitted_at.len();
                match self.redial(&setting) {
                    Ok(name) => {
                        self.submitted_at.clear();
                        let line = format!("the source still answers no submission ({n} pending) after a reconnect; reconnected again to {name} (reconnect {} this run); mining goes on", self.totals.source_reconnects);
                        self.log("app", &line);
                    }
                    Err(e) => {
                        let line = format!("the source still answers no submission ({n} pending); the reconnect failed ({e}); tried again in {} s", UNANSWERED_AFTER.as_secs());
                        self.log("app", &line);
                    }
                }
                self.notice("warn", "The work source is not answering submissions.", &format!("Reconnected {} times this run; mining continues and resumes on the first answer.", self.totals.source_reconnects));
            }
            _ => {}
        }
    }

    /// Build the verifier's full context for the epoch of `height` on its own thread (the dataset in host RAM
    /// where it fits); the line it returns goes to the log through the warm channel.
    /// A named synchronous phase of the tick (a launch's hello wait, a verifier's build): marked for the watchdog
    /// while it runs, its duration kept for the tick's timing line.
    fn in_phase<T>(&mut self, name: &str, f: impl FnOnce() -> T) -> T {
        let t0 = Instant::now();
        if let Ok(mut g) = self.sync_phase.lock() { *g = Some((t0, name.to_string())); }
        let out = f();
        if let Ok(mut g) = self.sync_phase.lock() { *g = None; }
        let ms = t0.elapsed().as_millis() as u64;
        if self.tick_phase_longest.as_ref().map_or(true, |(_, m)| ms > *m) { self.tick_phase_longest = Some((name.to_string(), ms)); }
        out
    }

    fn warm_verifier(&mut self, v: Arc<dyn Verifier>, height: u64) {
        let tx = self.warm_tx.clone();
        let _ = thread::Builder::new().name("verify-warm".into()).spawn(move || {
            if let Some(line) = v.warm(height) {
                let _ = tx.send(line);
            }
        });
    }

    fn note_verify_latency(&mut self, ms: u64) {
        self.verify_latencies.push_back(ms);
        while self.verify_latencies.len() > 200 {
            self.verify_latencies.pop_front();
        }
    }

    /// The reference check against the chain: when most results of the last minute were checked after their
    /// template moved on, the check is too slow for this chain; said as a state, and after two minutes of it
    /// mining stops with the reason rather than hashing for nothing (F-22).
    fn judge_verify_latency(&mut self) {
        let now = Instant::now();
        while self.verify_window.front().map_or(false, |(t, _)| now.duration_since(*t) > Duration::from_secs(60)) {
            self.verify_window.pop_front();
        }
        let total = self.verify_window.len();
        let late = self.verify_window.iter().filter(|(_, ok)| !ok).count();
        let median = {
            let mut v: Vec<u64> = self.verify_latencies.iter().copied().collect();
            v.sort_unstable();
            if v.is_empty() { 0 } else { v[v.len() / 2] }
        };
        self.totals.verify_median_ms = median;
        // too slow means nothing gets through: most results late and none in time over the minute (a chain whose
        // blocks are the miner's own results makes every following result late while the accepted ones keep
        // flowing, and that is not a fault)
        let in_time = total - late;
        let too_slow = total >= 10 && late * 2 > total && in_time == 0;
        if too_slow {
            if self.late_since.is_none() {
                self.late_since = Some(now);
                let line = format!("the reference check is too slow for this chain: {median} ms a result, {late} of {total} results in the last minute checked after their template moved on");
                self.log("app", &line);
                self.notice("bad", "The reference check is too slow for this chain.", &format!("{median} ms a result; most results are checked after their template has moved on and are dropped. Mining stops in two minutes if this does not clear."));
            } else if self.late_since.map_or(false, |t| now.duration_since(t) > Duration::from_secs(120)) {
                self.late_since = None;
                self.stop("the reference check is too slow for this chain: results cannot be checked before their template moves on");
                self.notice("bad", "Mining stopped: the reference check is too slow for this chain.", &format!("{median} ms a result. Nothing was submitted that was not checked. Press Start to try again; a faster reference or a larger cache may be needed."));
            }
        } else if self.late_since.is_some() && total >= 10 {
            self.late_since = None;
            self.notices.retain(|n| n.text != "The reference check is too slow for this chain.");
        }
    }

    /// A new template from the source. Its parameter document (the source's session document when the template
    /// names none) is matched by digest against the one every worker runs; a change rebuilds the verifier and
    /// reconfigures every worker on the new document (the bounded cold path: the node announces no change ahead);
    /// a document this miner cannot match is refused with the reference's reason and no work is taken on it
    /// (security's F-21: never mine a template on another document than the one it names). Returns whether the
    /// template was taken.
    fn take_template(&mut self, job: JobTemplate) -> bool {
        // F-290: a job on a coordinate the registry retired is never taken (its retirement is final)
        if let Some(c) = job.coord {
            if self.retired_coords.contains(&c) {
                return false;
            }
        }
        let doc = if job.params.is_null() { self.algo_params.clone() } else { job.params.clone() };
        let digest = job.params_digest.clone().unwrap_or_else(|| work::params_digest(&doc));
        // the keyed form: a template that names a bond key other than this miner's is never hashed (its attempts
        // need that identity's secret; the node's owner's check would refuse every share); said once per key
        if let Some(bk) = job.bond_key {
            let mine = self.cfg.bond_secret.as_ref().map(|b| b.public());
            if mine != Some(bk) {
                let line = format!("the template's bond key {} is not this miner's ({}); not mined", proto::hex_encode(&bk), mine.map(|m| proto::hex_encode(&m)).unwrap_or_else(|| "no bond secret in hand".into()));
                if self.refused_bond_key.as_ref() != Some(&bk) {
                    self.refused_bond_key = Some(bk);
                    self.log("app", &line);
                    self.notice("warn", "The template is under another bond's key.", &line);
                }
                return false;
            }
        }
        if let Some(c) = job.coord {
            return self.take_coord_template(c, job, doc, digest);
        }
        self.plain_template_seen = true;
        if digest != self.params_digest {
            if self.refused_digest.as_deref() == Some(digest.as_str()) {
                return false;
            }
            let algo = self.source.as_ref().map(|s| s.algo()).unwrap_or_default();
            let doc_text = if doc.is_null() { None } else { Some(doc.to_string()) };
            if algo != stubhash::STUB_ALGO && (doc_text.is_none() || doc.get("version").is_none()) {
                self.refuse_document(&job, &digest, "the template names no parameter document version");
                return false;
            }
            match verify::verifier_for_params(&algo, doc_text.as_deref()) {
                Err(e) => {
                    self.refuse_document(&job, &digest, &e.to_string());
                    return false;
                }
                Ok(v) => {
                    let v: Arc<dyn Verifier> = Arc::from(v);
                    match reference_selfcheck(v.clone(), job.height) {
                        Ok(ms) => {
                            self.totals.selfcheck_ms = ms;
                            let line = format!("the reference checks the new document in {ms} ms");
                            self.log("app", &line);
                        }
                        Err(e) => {
                            self.refuse_document(&job, &digest, &e);
                            return false;
                        }
                    }
                    let line = format!("the template at height {} names a new parameter document (digest {} replaces {}): every worker is reconfigured on it and rebuilds its dataset", job.height, &digest[..digest.len().min(12)], &self.params_digest[..self.params_digest.len().min(12)]);
                    self.log("app", &line);
                    self.notice("info", "The network changed the mining parameters.", "Every card rebuilds its dataset on the new document; the results in flight for the old one are dropped, never submitted.");
                    self.warm_verifier(v.clone(), job.height);
                    self.verifier = Some(v);
                    self.params_digest = digest;
                    self.algo_params = doc;
                    self.refused_digest = None;
                    for i in 0..self.slots.len() {
                        self.reconfigure_slot(i, job.height);
                    }
                }
            }
        }
        // F-E6 on a one-form session: a job naming a programme anchor other than the verifier's rebuilds the verifier
        // on the session document with that anchor (the workers take it by the set)
        if job.program_anchor.is_some() && job.program_anchor != self.session_anchor {
            let doc = Self::with_anchor(self.algo_params.clone(), job.program_anchor.as_deref());
            let algo = self.source.as_ref().map(|s| s.algo()).unwrap_or_default();
            let built = self.in_phase("building the session's verifier on the period's anchor", || verify::verifier_for_params(&algo, Some(&doc.to_string())));
            match built {
                Ok(v) => {
                    let line = format!("the period's anchor is {}; the verifier is rebuilt on it", job.program_anchor.as_deref().map(|a| &a[..12]).unwrap_or("none"));
                    self.log("app", &line);
                    self.verifier = Some(Arc::from(v));
                    self.session_anchor = job.program_anchor.clone();
                }
                Err(e) => {
                    let line = format!("the verifier could not take the period's anchor: {e}");
                    self.log("app", &line);
                }
            }
        }
        self.totals.share_lane = job.share.is_some();
        self.totals.shares_per_block = job.share.as_ref().map_or(0, |s| s.shares_per_block);
        // the lab switch is read again per job: the CLI sets its variable after the supervisor is built
        self.pace_chain_target = pace_chain_target_from_env();
        self.totals.share_pacing = if job.share.is_some() { share_pacing_word(self.pace_chain_target).to_string() } else { String::new() };
        self.note_share_target(&job.share_target);
        self.current = Some(job);
        true
    }

    /// A coordinate's template (VectorWork): its context is kept per coordinate, the verifier built once per
    /// digest; a document the reference cannot run marks the coordinate refused for every card (never the session).
    /// `current` follows the newest template for the height and the words; the dispatch reads each slot's own
    /// coordinate's template.
    fn take_coord_template(&mut self, c: u8, job: JobTemplate, doc: serde_json::Value, digest: String) -> bool {
        let algo = self.source.as_ref().map(|s| s.algo()).unwrap_or_default();
        // the pool restarted (the head fell): every coordinate context and the router's memory are reset, so the
        // old last heights do not read every coordinate as empty at once (the mini at 03:40 UK, 13 October 2026)
        if let Some(head) = self.coords.values().map(|x| x.template.height).max() {
            if job.height + 64 < head {
                let line = format!("the pool's head fell from {head} to {}: the coordinate contexts and the router's memory are reset", job.height);
                self.log("app", &line);
                self.coords.clear();
                self.coord_builds.clear();
        self.plain_template_seen = false;
        self.first_coord_template_at = None;
                self.router_last = (0, BTreeMap::new());
                self.router_history.clear();
                for s in self.slots.iter_mut() {
                    s.route.coord = None;
                    s.route.probing = false;
                    s.route.moved_at = 0;
                    s.route.window_start = 0;
                    s.route.switches_this_window = 0;
                    s.stint_since = None;
                }
            }
        }
        // a boundary template carrying the previous period's anchor is not mined (every share on it is refused by the
        // sealer); said once per height, the next template carries the period's own
        if anchor_is_one_period_stale(job.height, job.anchor_height, &doc) {
            if self.stale_anchor_said != Some((c, job.height)) {
                self.stale_anchor_said = Some((c, job.height));
                let line = format!("coordinate {c}: the template at height {} names the previous period's anchor (height {}); not mined until a template carries the period's own", job.height, job.anchor_height.unwrap_or(0));
                self.log("app", &line);
            }
            return false;
        }
        // F-E6: the notify's program_anchor is the period's key input; it is written into the document the verifier
        // and the worker read (the reference parses program_anchor from the document), so the three hash alike
        // F-290 with the pool's pay list (0.1.76): every context the list does not pay is dropped the moment the job
        // arrives (never restored; a coordinate returns with a job), the line once per coordinate, the cards routed
        // again; a job on a coordinate outside its own list is not taken
        if let Some(paid) = job.paid_coords.as_ref() {
            let held: Vec<u8> = self.coords.keys().cloned().collect();
            let gone = unpaid_coords(&held, paid);
            for g in &gone {
                self.coords.remove(g);
                self.coord_builds.remove(g);
                let line = format!("coordinate {g}: not in the chain's pay list the pool sent with this job ({paid:?}); dropped from the router's candidates, never restored (F-290)");
                self.log("app", &line);
            }
            if !gone.is_empty() {
                self.route_cards("a coordinate unpaid (F-290)");
            }
            if !paid.contains(&c) {
                return false;
            }
        }
        let doc = Self::with_anchor(doc, job.program_anchor.as_deref());
        let known = self.coords.get(&c).map(|x| x.digest == digest && x.anchor == job.program_anchor).unwrap_or(false);
        if !known {
            let doc_text = if doc.is_null() { None } else { Some(doc.to_string()) };
            if algo != stubhash::STUB_ALGO && (doc_text.is_none() || doc.get("version").is_none()) {
                self.refuse_coord(c, &digest, "the coordinate's template names no parameter document version");
                return false;
            }
            let version = doc.get("version").and_then(|x| x.as_u64()).unwrap_or(0);
            // 0.2.22: the verifier builds on its own thread (the reference's vectors check and its judgement of the document
            // took 92 s on a cold start of the raster lane, 16 October 2026, every tick held behind it); the newest template
            // waits on the build and registers the context when it reports (finish_coord_registration); a document or anchor
            // that moves while a build runs starts a new build and the old one's result is dropped with its receiver
            if let Some(b) = self.coord_builds.get_mut(&c) {
                if b.digest == digest && b.anchor == job.program_anchor {
                    b.job = job;
                    b.doc = doc;
                    return false;
                }
            }
            if self.coords.get(&c).map(|x| x.digest == digest).unwrap_or(false) {
                let line = format!("coordinate {c}: the period's anchor changed to {}; the verifier is rebuilt on it (the workers take it by the set)", job.program_anchor.as_deref().map(|a| &a[..12]).unwrap_or("none"));
                self.log("app", &line);
            }
            let (tx, rx) = std::sync::mpsc::channel::<Result<Arc<dyn Verifier>, String>>();
            let (algo_b, text_b) = (algo.clone(), doc_text.clone());
            let spawned = thread::Builder::new().name(format!("coord-{c}-verifier")).spawn(move || {
                let _ = tx.send(verify::verifier_for_params(&algo_b, text_b.as_deref()).map(Arc::from).map_err(|e| e.to_string()));
            });
            if let Err(e) = spawned {
                self.refuse_coord(c, &digest, &format!("the verifier build thread could not start: {e}"));
                return false;
            }
            let line = format!("coordinate {c}: building the verifier for its document (version {version}) on its own thread; the reference's vectors check and its judgement of the document run there");
            self.log("app", &line);
            self.coord_builds.insert(c, CoordBuild { job, doc, digest, anchor: None, version, since: Instant::now(), rx });
            if let Some(b) = self.coord_builds.get_mut(&c) {
                b.anchor = b.job.program_anchor.clone();
            }
            return false;
        } else if let Some(ctx) = self.coords.get_mut(&c) {
            ctx.template = job.clone();
            ctx.let_go = false;
            if job.coord_last_height.is_some() {
                ctx.last_height = job.coord_last_height;
            }
            let resumed = ctx.paused_since.take();
            self.say_lane_facts(c, &job);
            if let Some(since) = resumed {
                let line = format!("coordinate {c}: a fresh job after {} s paused; the lane resumes for the router with its context kept (F-384)", since.elapsed().as_secs());
                self.log("app", &line);
                self.route_cards("a lane resumed (F-384)");
            }
        }
        self.totals.share_lane = false;
        self.pace_chain_target = pace_chain_target_from_env();
        self.note_share_target(&job.share_target);
        self.current = Some(job);
        self.route_cards("a template");
        true
    }

    /// The document with the served programme anchor written in (0x and 64 hex) when a job names one; the document as
/// served otherwise (F-E6).
fn with_anchor(mut doc: serde_json::Value, anchor: Option<&str>) -> serde_json::Value {
    if let (Some(a), Some(o)) = (anchor, doc.as_object_mut()) {
        // the reference parses the anchor as 64 hex characters in quotes, no 0x (pow/ref igneumhash.cpp)
        o.insert("program_anchor".into(), serde_json::Value::String(a.trim_start_matches("0x").to_lowercase()));
    }
    doc
}

    /// F-290: a coordinate the succession retired, or the pool withdrew, leaves the router's candidates: its context is
    /// dropped, the line said once per coordinate, the cards on it routed again at once (the router moves a card whose
    /// coordinate is no longer in the table); a registry retirement is final (a later job on it is refused by
    /// take_template), a pool withdrawal ends when a job returns.
    fn drop_retired_coords(&mut self) {
        let head = self.coord_head();
        let retired_from: BTreeMap<u8, u64> = self.registry_docs.iter().filter_map(|(c, d)| d.retired_from.map(|h| (*c, h))).collect();
        let issued: BTreeMap<u8, Instant> = self.coords.iter().map(|(c, x)| (*c, x.template.issued)).collect();
        let gone = retired_coords(&retired_from, &issued, head, Instant::now());
        let mut dropped = false;
        for c in gone {
            if let Some(h) = retired_from.get(&c) {
                // the registry's retirement is final: the context goes and never returns
                if self.coords.remove(&c).is_some() {
                    dropped = true;
                    self.retired_coords.insert(c);
                    let why = format!("retired from block {h} by the registry{}", self.registry_docs.get(&c).and_then(|d| d.successor).map(|s| format!(", its successor coordinate {s}")).unwrap_or_default());
                    let line = format!("coordinate {c}: {why}; dropped from the router's candidates, never restored (F-290)");
                    self.log("app", &line);
                }
            } else if let Some(ctx) = self.coords.get_mut(&c) {
                // F-384: the 90 s rule is a PAUSE: the context stays (its verifier, the cards' memory of it), the router
                // leaves it out until its next job (a pool's roll, a lane's quiet minute); never a drop and a re-probe
                if lane_pauses(ctx.paused_since.is_some()) {
                    ctx.paused_since = Some(Instant::now());
                    dropped = true;
                    let line = format!("coordinate {c}: the pool has refreshed no job on it for {COORD_WITHDRAWN_S} s while another coordinate's jobs are fresh; paused for the router until its next job (F-384)");
                    self.log("app", &line);
                }
            }
        }
        if dropped {
            self.route_cards("a coordinate retired or paused (F-290, F-384)");
        }
    }

/// The router's table from the coordinate contexts (ROUTER.md section 2).
    fn coord_table(&self) -> BTreeMap<u8, router::CoordState> {
        self.coords.iter().filter(|(_, x)| x.paused_since.is_none()).map(|(c, x)| (*c, router::CoordState { coord: *c, digest: x.digest.clone(), difficulty: x.template.coord_difficulty.unwrap_or_else(|| stubhash::difficulty_for_target(&x.template.boundary)), last_height: x.last_height, lane_worth: x.template.lane_worth, lane_worth_unknown: x.template.lane_worth_unknown, lane_workers: x.template.lane_workers })).collect()
    }

    /// F-496 and F-543: the pool's lane worth and worker count on a coordinate job, logged once per coordinate and value
    /// (the proof rows read the lane and its worth from this line).
    fn say_lane_facts(&mut self, c: u8, job: &JobTemplate) {
        if job.lane_worth.is_none() && !job.lane_worth_unknown && job.lane_workers.is_none() {
            return;
        }
        let worth = match (job.lane_worth, job.lane_worth_unknown) { (Some(w), _) => format!("{w:.4e}"), (None, true) => "unknown".to_string(), (None, false) => "none".to_string() };
        let workers = job.lane_workers.map_or("none".to_string(), |n| n.to_string());
        let key = format!("{worth}|{workers}");
        if self.lane_facts_said.get(&c) == Some(&key) {
            return;
        }
        self.lane_facts_said.insert(c, key);
        let line = format!("coordinate {c}: lane_worth {worth}, lane_workers {workers} at height {} (the pool's accounting, one window behind; F-496, F-543)", job.height);
        self.log("app", &line);
    }

    /// The chain's head as the newest coordinate template states it.
    fn coord_head(&self) -> u64 {
        self.coords.values().map(|x| x.template.height).max().unwrap_or(0)
    }

    /// F-E46 and F-E49 (0.2.12): the pin gate. A pin names a coordinate this source does not serve (a set without it, or a
    /// plain source once its first template is seen; the start's empty set never refuses): every open slot is closed with
    /// the reason, nothing is reopened or given work while it holds, and the line is said once a minute. Returns true
    /// while the pin is refused.
    fn pin_refusal(&mut self) -> bool {
        let Some(p) = self.settings.coordinate_pin else { self.pin_refused = false; return false };
        let served: Vec<u8> = self.coords.keys().cloned().collect();
        if !served.is_empty() && self.first_coord_template_at.is_none() {
            self.first_coord_template_at = Some(Instant::now());
        }
        // F-E62 (the GPU team's pinned row of 15 October 2026, 09:24 UK: the pin was judged against the first template's
        // coordinate alone and the card closed for good nineteen seconds before its own coordinate's template came): a
        // served set still filling is not judged; the pin waits PIN_GRACE_S from the first coordinate template
        let filling = !served.contains(&p) && !served.is_empty() && self.first_coord_template_at.map_or(true, |t| t.elapsed() < Duration::from_secs(PIN_GRACE_S));
        let refused = if served.is_empty() { self.plain_template_seen } else { !served.contains(&p) && !filling };
        if !refused {
            if self.pin_refused {
                // the refusal lifts (the pinned coordinate's template came, or the pin changed): the closed cards reopen now
                self.log("app", &format!("coordinate {p} is served now; the cards pinned to it reopen"));
                for i in 0..self.slots.len() {
                    if self.slots[i].enabled && self.slots[i].proc.is_none() && matches!(self.slots[i].state, SlotState::Idle) {
                        self.slots[i].message.clear();
                        self.open_slot(i);
                    }
                }
            }
            self.pin_refused = false;
            return false;
        }
        let msg = pin_refusal_words(p, &served);
        if !self.pin_refused || self.pin_refused_said.map_or(true, |t| t.elapsed() > Duration::from_secs(60)) {
            self.pin_refused_said = Some(Instant::now());
            self.log("app", &msg);
            self.notice("bad", "The pinned coordinate is not served.", &msg);
        }
        self.pin_refused = true;
        for i in 0..self.slots.len() {
            if self.slots[i].proc.is_some() {
                self.close_slot(i, "the pinned coordinate is not served");
                self.slots[i].state = SlotState::Idle;
            }
            self.slots[i].message = msg.clone();
        }
        true
    }

    /// The router step (ROUTER.md sections 3 to 6): on a due read, or when `why` names a template or a probe's end,
    /// every open slot is given its decision; a switch is a reconfigure on the coordinate's document.
    fn route_cards(&mut self, why: &str) {
        if !matches!(self.mining, MiningState::Mining) {
            return;
        }
        if self.pin_refusal() {
            return;
        }
        if self.coords.is_empty() {
            return;
        }
        self.drop_retired_coords();
        // F-478: a lane whose worker for the card's class is absent from the install is never a candidate (the probe
        // never goes there; the switch's refusal of the same lane stays as the second guard), one line a card and lane
        {
            let versions: Vec<(u8, Option<u64>)> = self.coords.iter().map(|(c, x)| (*c, x.doc.get("version").and_then(|v| v.as_u64()))).collect();
            for i in 0..self.slots.len() {
                let base_stem = self.slots[i].base_launcher.as_ref().unwrap_or(&self.slots[i].launcher).exe.file_stem().map(|x| x.to_string_lossy().to_string()).unwrap_or_default();
                let cl = self.cfg.coord_launchers.clone(); let vl = self.cfg.version_launchers.clone(); let bs = base_stem.clone();
                let has_bound = move |c: u8, v: Option<u64>| bound_launcher_for_version(&cl, &vl, c, v, &bs).is_some();
                for c in coords_without_a_worker(&versions, &base_stem, &has_bound) {
                    if !self.slots[i].route.refused.contains(&c) {
                        self.slots[i].route.refused.push(c);
                        let v = versions.iter().find(|(k, _)| *k == c).and_then(|(_, v)| *v).unwrap_or(0);
                        let line = format!("{}: coordinate {c} ({}) needs its own worker {base_stem}-v{v}, not in this install; it is never this card's lane (F-478)", self.slots[i].device.name, lane_word(Some(v), Some(c)));
                        self.log("app", &line);
                    }
                }
            }
        }
        let table = self.coord_table();
        let head = self.coord_head();
        // a pinned coordinate: every card with a worker goes to it and stays (no probe, no window, no switch); a card
        // that refused it idles rather than mine another
        if let Some(p) = self.settings.coordinate_pin {
            if self.coords.contains_key(&p) {
                for i in 0..self.slots.len() {
                    let s = &self.slots[i];
                    if s.proc.is_some() && s.route.coord != Some(p) && !s.route.refused.contains(&p) {
                        self.switch_slot_to(i, p, false, "the pinned coordinate", head);
                    }
                }
            }
            return;
        }
        // the fleet's signal reads (the difficulty step, a new coordinate) and each card's own window read
        let signal = router::read_due(head, u64::MAX / 2, &table, &self.router_last.1);
        let any_window = self.slots.iter().any(|s| s.proc.is_some() && router::card_window_due(head, &s.route));
        let due = signal.or(if any_window { Some("the window") } else { None });
        let unrouted = self.slots.iter().any(|s| s.proc.is_some() && (s.route.coord.is_none() || (s.route.probing && s.stint_since.map_or(false, |t| t.elapsed() >= Duration::from_secs(router::PROBE_S)))));
        if due.is_none() && !unrouted && why != "a probe's end" {
            return;
        }
        let subsidy = self.source.as_ref().and_then(|s| s.economics().subsidy_ign).unwrap_or(1.0);
        // the probes that ended give their rate; F-469: a settled lane gives its measured share rate instead (the
        // accepted shares' weights at the pool's step over the stint), never the worker's reported rate alone
        let target_stood_s = self.share_target_since.as_ref().map_or(0, |(_, since)| since.elapsed().as_secs());
        let mut measured_lines: Vec<String> = Vec::new();
        for s in self.slots.iter_mut() {
            if let (Some(c), Some(t)) = (s.route.coord, s.stint_since) {
                if t.elapsed() >= Duration::from_secs(router::PROBE_S) {
                    let hash_rate = s.stint.rate();
                    let (work, shares) = s.lane_stats.get(&c).map_or((0.0, 0), |l| (l.stint_work, l.stint_shares));
                    let r = router::lane_rate(hash_rate, work, shares, t.elapsed().as_secs(), target_stood_s);
                    if r > 0.0 {
                        let was = s.route.rates.insert(c, r);
                        s.route.probing = false;
                        if shares >= router::MEASURED_SHARES_MIN && r != hash_rate && was.map_or(true, |w| (w - r).abs() / r.max(1.0) > 0.5) {
                            measured_lines.push(format!("{}: coordinate {c}'s rate for the router is its measured share rate {} (the worker reports {}; F-469)", s.device.name, state::rate_words(r), state::rate_words(hash_rate)));
                        }
                    }
                }
            }
        }
        for line in measured_lines {
            self.log("app", &line);
        }
        // the hysteresis's read count
        for s in self.slots.iter_mut() {
            if router::better_this_read(&s.route, &table, subsidy) { s.route.better_reads = s.route.better_reads.saturating_add(1); } else { s.route.better_reads = 0; }
        }
        // the per-card offset inside the window (the outside review's audit: no fleet-wide switching minute) and the
        // damped view of the difficulties (router.rs)
        let address = self.settings.payout_address.clone().unwrap_or_default();
        for s in self.slots.iter_mut() {
            s.route.offset = router::offset_blocks(&s.device.fingerprint, &address);
            if head >= s.route.window_start + router::WINDOW_BLOCKS {
                s.route.window_start = head - (head % router::WINDOW_BLOCKS);
                s.route.switches_this_window = 0;
            }
        }
        self.router_history.push(table.clone());
        if self.router_history.len() > router::DAMPING_READS {
            self.router_history.remove(0);
        }
        let view = router::damped(&self.router_history);
        let cards: Vec<(String, router::CardRoute)> = self.slots.iter().map(|s| (s.device.fingerprint.clone(), s.route.clone())).collect();
        let plan = router::restore_plan(&cards, &view, subsidy, head);
        for i in 0..self.slots.len() {
            if self.slots[i].proc.is_none() || !self.slots[i].enabled {
                continue;
            }
            let restore = plan.iter().find(|(k, _)| *k == i).map(|(_, c)| *c);
            let r = &self.slots[i].route;
            // a scheduled read is this card's only at its offset; a signalled read, a probe's end, a restoration
            // and the first choice are everyone's
            let scheduled = due == Some("the window") && r.coord.is_some() && !r.probing && restore.is_none();
            if scheduled && !router::card_window_due(head, r) {
                continue;
            }
            if scheduled {
                self.slots[i].route.last_window_read = head / router::WINDOW_BLOCKS;
            }
            // F-E59: a card still settling on its coordinate (no result yet, under SETTLE_S since the move) is not moved
            // by a restoration or a window read; the worker's compile and dataset build take longer than a window
            // the worker's compile of a coordinate's programme is 164 to 228 s on a cold cache (the GPU team's row of 09:14 UK),
            // so the settle holds while the slot is still opening or preparing, and SETTLE_S after it is ready
            // the settle invariant (router::settle_allows, the table in its doc): the card's state and the event decide
            let card_state = if self.slots[i].route.coord.is_none() {
                router::CardState::Refused // unrouted: nothing to hold
            } else if self.slots[i].stint.rate() > 0.0 {
                router::CardState::Mining
            } else if matches!(self.slots[i].state, SlotState::Opening) {
                router::CardState::Opening
            } else if matches!(self.slots[i].state, SlotState::Preparing) {
                router::CardState::Preparing
            } else if self.slots[i].route.probing {
                router::CardState::Probing
            } else {
                router::CardState::Ready
            };
            let event = if why == "a refused coordinate" {
                router::MoveEvent::CoordinateRefused
            } else if why == "a probe's end" {
                router::MoveEvent::ProbeEnd
            } else if restore.is_some() {
                router::MoveEvent::Restoration
            } else if due == Some("the window") {
                router::MoveEvent::WindowRead
            } else if due.is_some() {
                router::MoveEvent::SignalRead
            } else {
                router::MoveEvent::FirstChoice
            };
            let since_s = self.slots[i].stint_since.map_or(u64::MAX, |t| t.elapsed().as_secs());
            let mined_s = self.slots[i].stint.first.map_or(0, |t| t.elapsed().as_secs());
            let settling = !router::settle_allows_mined(card_state, event, since_s, mined_s);
            if settling {
                if self.slots[i].route.probing && !self.slots[i].settle_said {
                    self.slots[i].settle_said = true;
                    let line = format!("{}: settling on coordinate {} (the worker compiles and builds; no move until it has a result)", self.slots[i].device.name, self.slots[i].route.coord.unwrap_or(0));
                    self.log("app", &line);
                }
                continue;
            }
            let r = &self.slots[i].route;
            let Some(d) = router::decide(r, &view, subsidy, head, restore) else { continue };
            // the GPU team's rule (main, 23:34 UK, 13 October 2026, the plug-tune-play form): coordinate 1 (version 15, the 4 GiB
            // dataset) is OFF every AMD card with 8 GB of memory or less until F-110 is read (the rig froze twice at the
            // dataset build on the RX 6600 and the RX 5700 XT under rusticl; the 16 GB 9060 XT runs it clean); such a
            // card keeps its coordinate, or takes the probe's next one, never 1
            let small_amd = self.slots[i].device.vendor.eq_ignore_ascii_case("amd") && self.slots[i].device.memory_total <= 8 * devices::GIB;
            // F-110 CLOSED (the GPU team's proof on the rig's RX 5700 XT, 14 October 2026, the project's word of 05:2x UK): the pin lifts
            // only on the OpenCL worker that carries the leak fix (the shipped pair, by the file's sha256); an older
            // worker on the host keeps the pin and the log says which worker it is
            if small_amd && d.coord == 1 && !self.slot_worker_lifts_f110(i) {
                let line = format!("{}: coordinate 1 is off this card (an AMD card with {} GB; version 15's 4 GiB dataset leaked on 8 GB boards, F-110; the OpenCL worker here is not the fixed one, see F110_FIXED_OPENCL_WORKERS)", self.slots[i].device.name, (self.slots[i].device.memory_total as f64 / devices::GIB as f64).round());
                if self.slots[i].route.coord.is_none() { self.log("app", &line); }
                if self.slots[i].route.coord == Some(1) {
                    // a card routed there earlier (a pin, a stored route) leaves it for the chain's first coordinate
                    self.switch_slot_to(i, 0, false, "coordinate 1 is off 8 GB AMD cards (F-110)", head);
                }
                continue;
            }
            if self.slots[i].route.coord == Some(d.coord) && !d.probe {
                continue;
            }
            if !d.probe && self.slots[i].route.coord.is_some() {
                self.slots[i].route.switches_this_window = self.slots[i].route.switches_this_window.saturating_add(1);
            }
            self.switch_slot_to(i, d.coord, d.probe, &format!("{} ({why}, block {head})", d.reason), head);
        }
        if due.is_some() {
            self.router_last = (head, table);
        }
        for s in self.slots.iter_mut() {
            if s.proc.is_some() && s.route.coord.is_some() && !s.route.probing {
                // a card that took no scheduled read this tick (its coordinate unchanged) still marks the window read
                if router::card_window_due(head, &s.route) && due == Some("the window") {
                    s.route.last_window_read = head / router::WINDOW_BLOCKS;
                }
            }
        }
    }

    /// A card's move to a coordinate: its worker reconfigured on that coordinate's document (the share in flight
    /// finishes on the old job first: the cancel path drops the old job and the worker's next result is for the
    /// new), its stint meter reset, the move logged with its reason.
    fn switch_slot_to(&mut self, i: usize, c: u8, probe: bool, reason: &str, head: u64) {
        let Some(ctx) = self.coords.get(&c) else { return };
        let (doc, digest, height) = (ctx.doc.clone(), ctx.digest.clone(), ctx.template.height);
        let name = self.slots[i].device.name.clone();
        let from = self.slots[i].route.coord;
        {
            let s = &mut self.slots[i];
            // the probe's cost (reliability's steady read of 0.2.20 on the two-card Windows host, 03:57 UK, 16 October
            // 2026: the 5090 left the bandwidth lane 41 s into its stint, under the 45 s probe, so no rate was kept for it
            // and every signalled read after probed it again, a worker close and relaunch each way, about a minute at
            // zero every period): a lane the card mined long enough to read keeps its rate at the move off it, so the
            // router never probes a lane it already knows (router::rate_known_at_move, with its test)
            if let (Some(f), Some(t)) = (from, s.stint_since) {
                if f != c {
                    let rate = s.stint.rate();
                    if router::rate_known_at_move(t.elapsed().as_secs(), rate) && !s.route.rates.contains_key(&f) {
                        s.route.rates.insert(f, rate);
                    }
                }
            }
            s.route.coord = Some(c);
            s.route.probing = probe;
            s.route.better_reads = 0;
            // every move, a restoration included, holds for the window (the mini's two restorations in one second)
            if !probe { s.route.moved_at = head; }
            s.stint = RateMeter::new(Duration::from_secs(router::PROBE_S));
            s.stint_since = Some(Instant::now());
            s.settle_said = false;
            // F-469: the lane's measured share rate starts over with the stint
            let l = s.lane_stats.entry(c).or_default();
            l.stint_work = 0.0;
            l.stint_shares = 0;
        }
        let line = match from {
            Some(f) if f != c => format!("{name}: coordinate {f} to {c}: {reason}"),
            _ => format!("{name}: coordinate {c}: {reason}"),
        };
        self.log("app", &line);
        // 0.2.11: a worker bound to this coordinate (igneum-worker-<backend>-coord<N>) takes the slot's exe and the worker
        // is relaunched on it; a coordinate without one gets the base worker back
        let base_stem = self.slots[i].base_launcher.as_ref().unwrap_or(&self.slots[i].launcher).exe.file_stem().map(|x| x.to_string_lossy().to_string()).unwrap_or_default();
        let version = self.coords.get(&c).and_then(|x| x.doc.get("version").and_then(|v| v.as_u64()));
        let bound = bound_launcher_for_version(&self.cfg.coord_launchers, &self.cfg.version_launchers, c, version, &base_stem);
        let current_stem = self.slots[i].launcher.exe.file_stem().map(|x| x.to_string_lossy().to_string()).unwrap_or_default();
        let wanted = match &bound { Some(l) => l.exe.file_stem().map(|x| x.to_string_lossy().to_string()).unwrap_or_default(), None => base_stem.clone() };
        // a bound-only version (the raster lane's 18) with no bound worker in this install: the coordinate is refused for
        // the card and the router moves it on (coordinate_needs_absent_bound_worker); the base worker never takes it
        if coordinate_needs_absent_bound_worker_for(&base_stem, version, bound.is_some()) {
            let v = version.unwrap_or(0);
            let lane = lane_word(Some(v), Some(c));
            // the log carries the coordinate, the version and the worker's name; the card's line and the notice read the lane's name alone
            self.log("app", &format!("{name}: coordinate {c} (version {v}, {lane}) needs its own worker {base_stem}-v{v}, not in this install; the card mines the other lanes"));
            let w = format!("{lane} needs its own worker, not in this install; the card mines the other lanes");
            self.notice("warn", &format!("{name}: {lane} needs its own worker, not in this install."), "The card mines the other lanes meanwhile.");
            let s = &mut self.slots[i];
            if !s.route.refused.contains(&c) { s.route.refused.push(c); }
            s.route.coord = from;
            s.message = w;
            self.route_cards("the raster lane's worker is not in this install");
            return;
        }
        if wanted != current_stem {
            // the project's order of 15:2x UK, 15 October 2026 (the rig: the kernel driver and CUDA, no GL vendor library): before a
            // card is relaunched on the GL raster worker the host's GL and EGL libraries are looked for; absent, the
            // package is named in the card's words and the log, coordinate 6 is refused for the card and it stays on the
            // other lanes (never a fatal close); on Windows and macOS nothing is checked
            if wanted.starts_with(GL_BASE) {
                if let Some(need) = gllibs::check(&self.slots[i].device.vendor, &self.slots[i].device.driver) {
                    let w = gllibs::words(&need);
                    self.log("app", &format!("{name}: coordinate {c}: {w}"));
                    self.notice("warn", &format!("{name}: the raster lane needs the GL and EGL libraries."), &format!("Install {}. The card mines the other lanes meanwhile.", need.package));
                    let s = &mut self.slots[i];
                    if !s.route.refused.contains(&c) { s.route.refused.push(c); }
                    s.route.coord = from;
                    s.message = w;
                    self.route_cards("the raster lane's libraries are missing on this host");
                    return;
                }
            }
            if self.slots[i].base_launcher.is_none() { self.slots[i].base_launcher = Some(self.slots[i].launcher.clone()); }
            let keep = self.slots[i].launcher.clone();
            let mut next = match bound { Some(l) => l, None => self.slots[i].base_launcher.clone().unwrap_or(keep.clone()) };
            next.env = keep.env.clone(); next.workdir = keep.workdir.clone();
            gl_device_name_arg(&mut next, &name);
            self.slots[i].launcher = next;
            // F-681 (the first Windows proof host's raster row, 18 October 2026): the slot's measurement difficulty
            // follows the reported rate upward only (a pool session never steps it down), so a card moving from
            // the base worker at 53 MH/s to the raster lane's own worker at 520 attempts a second kept a job target
            // of one result in millions of attempts and found nothing in half an hour while the worker found shares
            // standalone; a worker change starts the measurement over from the slot's first value, and the new
            // worker's own rate raises it from there
            self.slots[i].measure_difficulty = 50_000.0;
            self.slots[i].recent_valid.clear();
            self.slots[i].measure_stepped_at = None;
            // F-696 (the tee capture on the first Windows proof host, 18 October 2026): the slot's reported-rate window
            // still held the base worker's samples (12 to 28 MH/s over ten minutes), and the next stat raised the
            // measurement straight back to that rate (one result in 1.5 million attempts for a worker at 860 a second);
            // the window starts over with the measurement, so the new worker's own stats are the only rate it reads
            self.slots[i].reported.reset();
            let line = format!("{name}: coordinate {c} runs on {wanted}; the worker is relaunched on it (the measurement target starts over for it)");
            self.log("app", &line);
            self.restart_slot(i, "the coordinate's own worker");
            return;
        }
        if self.slots[i].coord_digest.as_deref() != Some(digest.as_str()) {
            self.reconfigure_slot_on(i, height, doc, digest);
        }
    }

    /// The verifier for a document digest: a coordinate's when one carries it, else the session's.
    fn verifier_for_digest(&self, digest: &str) -> Option<Arc<dyn Verifier>> {
        if let Some(ctx) = self.coords.values().find(|x| x.digest == digest) {
            return Some(ctx.verifier.clone());
        }
        if digest == self.params_digest {
            return self.verifier.clone();
        }
        None
    }

    /// The template a slot mines: its coordinate's when it has one, else the session's current.
    fn slot_template(&self, i: usize) -> Option<JobTemplate> {
        if let Some(c) = self.slots[i].route.coord {
            // F-384's residual: a lane whose kept template belongs to a session the pool let go gives no work until its
            // first new job arrives; the slot waits for it
            return self.coords.get(&c).filter(|x| !x.let_go).map(|x| x.template.clone());
        }
        // a card the router has not placed takes no coordinate template (the mini, 02:4x UK 13 October 2026: an
        // unrouted card mined coordinate 3's template under the session's document and every result was cancelled)
        match self.current.as_ref() {
            Some(t) if t.coord.is_some() => None,
            other => other.cloned(),
        }
    }

    /// F-437: the bounded settle: a card on a chosen lane (its probe over) with no result for DEAD_LANE_S while the
    /// lane's jobs are fresh refuses that lane for the session and is routed again; said once per move.
    fn bounded_settle(&mut self, now: Instant) {
        let mut moved = false;
        for i in 0..self.slots.len() {
            let Some(c) = self.slots[i].route.coord else { continue };
            // the security review's residual (17 October 2026): the operator's pin is the stronger word; a pinned card is never
            // moved off its pin by the bound (it would refuse the pin and idle for the session)
            if settle_exempt(self.settings.coordinate_pin, c) {
                continue;
            }
            // the security review's residual (17 October 2026): a worker still preparing (a dataset build over five minutes on a
            // slow card) is not on the clock; the bound reads a worker that is mining or tuning
            if self.slots[i].route.probing || self.slots[i].proc.is_none() || !matches!(self.slots[i].state, SlotState::Mining | SlotState::Tuning) {
                continue;
            }
            let Some(start) = self.slots[i].stint_since else { continue };
            let mark = self.slots[i].last_result_at.map_or(start, |t| t.max(start));
            let since_work = now.saturating_duration_since(mark).as_secs();
            let lane_fresh = self.coords.get(&c).map_or(false, |x| x.paused_since.is_none() && now.saturating_duration_since(x.template.issued) < Duration::from_secs(COORD_WITHDRAWN_S));
            if !lane_dead(since_work, lane_fresh, false) {
                continue;
            }
            let name = self.slots[i].device.name.clone();
            let line = format!("{name}: no work on coordinate {c} in {since_work} s (no attempt reported) while its jobs are fresh; the card moves on and refuses the lane for this session (F-437)");
            self.log("app", &line);
            let s = &mut self.slots[i];
            if !s.route.refused.contains(&c) { s.route.refused.push(c); }
            s.route.coord = None;
            s.route.probing = false;
            s.coord_digest = None;
            s.stint_since = None;
            moved = true;
        }
        if moved {
            self.route_cards("a dead lane (F-437)");
        }
    }

    fn refuse_coord(&mut self, c: u8, digest: &str, why: &str) {
        let line = format!("coordinate {c}: its document cannot be matched by this miner ({why}); no card is routed to it (digest {})", &digest[..digest.len().min(12)]);
        self.log("app", &line);
        for s in self.slots.iter_mut() {
            if !s.route.refused.contains(&c) {
                s.route.refused.push(c);
            }
        }
        self.coords.remove(&c);
    }

    fn refuse_document(&mut self, job: &JobTemplate, digest: &str, why: &str) {
        let line = format!("the template at height {} names a parameter document this miner cannot match ({why}); no work is taken on it (digest {})", job.height, &digest[..digest.len().min(12)]);
        self.log("app", &line);
        self.notice("bad", "The network's mining parameters cannot be matched by this miner.", &format!("{why}. Nothing is mined until the network serves a document this miner's workers can run; an update may be needed."));
        self.refused_digest = Some(digest.to_string());
        self.current = None;
        for i in 0..self.slots.len() {
            if matches!(self.slots[i].state, SlotState::Mining) {
                self.cancel_slot_job(i);
            }
        }
    }

    /// Reconfigure one worker on the current document: its job is cancelled, the old dataset released, the new
    /// one built before the next job (RECOVERING with the gap charged); `ready` returns it to mining.
    fn reconfigure_slot(&mut self, i: usize, height: u64) {
        let (doc, digest) = match self.slots[i].route.coord.and_then(|c| self.coords.get(&c)) {
            Some(ctx) => (ctx.doc.clone(), ctx.digest.clone()),
            None => (self.algo_params.clone(), self.params_digest.clone()),
        };
        self.reconfigure_slot_on(i, height, doc, digest);
    }

    /// Reconfigure one worker on the given document (the session's, or a coordinate's).
    fn reconfigure_slot_on(&mut self, i: usize, height: u64, doc: serde_json::Value, digest: String) {
        let algo = self.source.as_ref().map(|s| s.algo()).unwrap_or_default();
        let verifier = self.verifier_for_digest(&digest);
        let to = verifier.as_ref().and_then(|v| v.epoch_at(height)).unwrap_or(0);
        let margin = self.cfg.horizon.margin_for(&self.slots[i].device);
        // the GPU team's row (2) of 15 October 2026 on 86c4e077 (the pool pod, the raster lane paid, no raster worker in the tar):
        // the card was moved to coordinate 6, its worker's configure on the version 18 document never came back, the
        // router moved it on to coordinate 1 at 13:46:31 and the slot "settled" there with no compile, no cache and no
        // result for the rest of the row: this function recorded the new document's digest and returned without a
        // configure because the slot was still Opening, so the slot believed itself on coordinate 1 while the worker sat
        // on coordinate 6's document. Now: a slot with no worker is relaunched on the new document; an Opening slot
        // takes the new configure (the worker's loop reads it after the one in flight); the digest is recorded only
        // when a configure is sent or a relaunch made
        if self.slots[i].proc.is_none() {
            if matches!(self.slots[i].state, SlotState::Off | SlotState::Faulted) {
                return;
            }
            self.slots[i].coord_digest = Some(digest.clone());
            self.restart_slot(i, "the coordinate's document changed with no worker running");
            return;
        }
        let s = &mut self.slots[i];
        if !matches!(s.state, SlotState::Mining | SlotState::Preparing | SlotState::Paused | SlotState::Tuning | SlotState::Opening) {
            return;
        }
        s.coord_digest = Some(digest.clone());
        let from = s.dataset_epoch.unwrap_or(to);
        if let Some(job) = s.active_job.take() {
            if let Some(p) = s.proc.as_mut() {
                let _ = p.send(&ToWorker::Cancel(Cancel { job_id: job }));
            }
        }
        // warm when the announced document was prepared beside mining (the worker keeps a prepared dataset whose
        // document matches the configure that follows); cold otherwise
        let warm = s.next_doc_digest.as_deref() == Some(digest.as_str()) && matches!(s.transition, Transition::Ready { .. });
        s.next_doc_digest = None;
        s.doc_valid_seen = false;
        s.switch_invalids = 0;
        s.transition = if warm { Transition::Switching { from, to } } else { Transition::Recovering { to } };
        s.transition_started = Some((Instant::now(), s.validated.rate(), from, to, warm));
        s.preparing_gap = None;
        s.dataset_epoch = None;
        s.next_epoch = None;
        s.switch_pending = None;
        s.building = None;
        s.tune = None;
        let configure = ToWorker::Configure(Configure { device: s.device.id.clone(), algo, params: doc, stat_ms: STAT_MS, transition: WorkerTransition::Auto, memory_margin_bytes: margin });
        let ok = s.proc.as_mut().map(|p| p.send(&configure).is_ok()).unwrap_or(false);
        if ok {
            s.state = SlotState::Opening;
            s.build_until = Some(Instant::now() + worker::HELLO_BOUND); // F-470: the rebuild's allowance
            s.message = if warm { "switching to the network's new parameters (the dataset was built beside mining)".into() } else { "rebuilding the dataset on the network's new parameters".into() };
        } else {
            self.restart_slot(i, "the worker did not take the new parameter document");
        }
    }

    /// The next epoch boundary above `height` within the lookahead, by the verifier's derivation.
    fn next_boundary(&self, height: u64) -> Option<(u64, u64)> {
        let v = self.verifier.as_ref()?;
        let here = v.epoch_at(height)?;
        for h in height + 1..=height + 256 {
            if let Some(e) = v.epoch_at(h) {
                if e != here {
                    return Some((h, e));
                }
            }
        }
        None
    }

    /// Ask every warm-capable device to prepare the next epoch when the boundary is near (PREPARING); the old
    /// dataset stays immutable, mining continues on it.
    fn prepare_ahead(&mut self) {
        let Some(t) = self.current.clone() else { return };
        // an announced parameter change within the lookahead: the next dataset is built on the new document
        // beside mining where two fit (the GPU team's sliced build), so the change is warm; the test in the lab names
        // the entry one epoch ahead and the node's nextParams fields carry it
        if let Some((doc, digest, at)) = t.next_params.clone() {
            let digest = digest.unwrap_or_else(|| work::params_digest(&doc));
            if digest != self.params_digest && at > t.height && at - t.height <= PREPARE_LOOKAHEAD_BLOCKS {
                let algo = self.source.as_ref().map(|s| s.algo()).unwrap_or_default();
                let to = verify::verifier_for_params(&algo, Some(&doc.to_string())).ok().and_then(|v| v.epoch_at(at)).unwrap_or(u64::MAX);
                for i in 0..self.slots.len() {
                    let s = &mut self.slots[i];
                    if !s.can_warm || !matches!(s.state, SlotState::Mining) || !matches!(s.transition, Transition::Current) || s.next_doc_digest.as_deref() == Some(digest.as_str()) {
                        continue;
                    }
                    let ok = s.proc.as_mut().map(|p| p.send(&ToWorker::Prepare(Prepare { height: at, params: Some(doc.clone()) })).is_ok()).unwrap_or(false);
                    if ok {
                        s.transition = Transition::Preparing { to };
                        s.next_doc_digest = Some(digest.clone());
                        s.building = Some((Instant::now(), 0.0));
                        let line = format!("{}: preparing the announced parameter document (digest {}) for height {at} in the background", s.device.name, &digest[..12]);
                        self.log("app", &line);
                    }
                }
                return;
            }
        }
        let Some((boundary, next)) = self.next_boundary(t.height) else { return };
        if boundary.saturating_sub(t.height) > PREPARE_LOOKAHEAD_BLOCKS {
            return;
        }
        for i in 0..self.slots.len() {
            let s = &mut self.slots[i];
            if !s.can_warm || !matches!(s.state, SlotState::Mining) || !matches!(s.transition, Transition::Current) || s.next_epoch == Some(next) || s.dataset_epoch == Some(next) {
                continue;
            }
            let ok = s.proc.as_mut().map(|p| p.send(&ToWorker::Prepare(Prepare { height: boundary, params: None })).is_ok()).unwrap_or(false);
            if ok {
                s.transition = Transition::Preparing { to: next };
                s.building = Some((Instant::now(), 0.0));
                let line = format!("{}: preparing epoch {next} in the background (boundary at height {boundary})", s.device.name);
                self.log("app", &line);
            }
        }
    }

    /// A transition is complete when the worker reports the new epoch as its dataset and mines on it.
    fn finish_transition(&mut self, i: usize, switch_ms: u64, digest: Option<String>) {
        let s = &mut self.slots[i];
        let Some((started, rate_before, from, to, warm)) = s.transition_started.take() else { return };
        let gap = s.preparing_gap.take().unwrap_or(Duration::ZERO);
        let gap_s = gap.as_secs_f64();
        let lost = rate_before * gap_s;
        let recovered = matches!(s.transition, Transition::Recovering { .. });
        s.transition = Transition::Current;
        s.transition_done_at = Some(Instant::now());
        s.dataset_epoch = Some(to);
        s.next_epoch = None;
        let rec = TransitionRecord { device: s.device.index, algo: self.source.as_ref().map(|x| x.algo()).unwrap_or_default(), params_digest: self.params_digest.clone(), from_epoch: from, to_epoch: to, path: if warm { "warm".into() } else { "cold".into() }, switch_ms, gap_s, lost_validated_hashes: lost, digest: digest.unwrap_or_else(|| "not reported by this worker".into()), recovered, at_unix: now_unix(), keyed_rounds: bond::keyed_rounds(&self.algo_params).unwrap_or(0), rate_before_hps: rate_before, rate_while_building_hps: s.last_build.map_or(0.0, |b| b.1), build_ms: s.last_build.map_or(0, |b| b.0), swap_wait_ms: s.telemetry.as_ref().and_then(|t| t.last_swap_wait_ms) };
        s.last_build = None;
        let line = format!("{}: epoch {from} to {to} done: {} path, switch {switch_ms} ms, gap {gap_s:.1} s, {:.0} validated hashes charged, started {:.1} s ago", self.slots[i].device.name, rec.path, lost, started.elapsed().as_secs_f64());
        self.log("app", &line);
        self.totals.transitions += 1;
        self.totals.transition_lost_hashes += lost;
        self.transitions.push_back(rec);
        while self.transitions.len() > 256 {
            self.transitions.pop_front();
        }
    }

    fn set_stage(&mut self, name: &str, status: &str, detail: &str) {
        if let Some(st) = self.stages.iter_mut().find(|s| s.name == name) {
            if st.status != "done" || status == "failed" {
                st.status = status.into();
                st.detail = detail.into();
            }
        }
    }

    fn tick(&mut self) {
        let now = Instant::now();
        let dt = now.duration_since(self.last_tick);
        self.last_tick = now;
        self.tick_inner(dt);
        // VectorWork: the router reads on its window, on the difficulty signal, at a probe's end (router.rs)
        if !self.coords.is_empty() {
            self.bounded_settle(now);
            let probe_ended = self.slots.iter().any(|s| s.route.probing && s.stint_since.map_or(false, |t| t.elapsed() >= Duration::from_secs(router::PROBE_S)));
            self.route_cards(if probe_ended { "a probe's end" } else { "the tick" });
        }
        // the share lane by node (a node source with several nodes: every share to every node, each node's answers
        // counted by its label)
        // the fee-waiver handshake's words (16.3): a pool session names the pool's answer beside the dev fee, a
        // node session the dev fee alone; the receipts' counts ride along
        let stub_source = matches!(self.settings.work_source, WorkSourceSetting::Stub);
        let dev_percent = self.totals.dev_fee_percent;
        let fee_read = self.source.as_mut().map(|src| {
            let on_a_pool = !src.economics().solo && !stub_source;
            let answer = src.pool_fee();
            let (answered, pool) = match &answer {
                Some(a) => (true, a.clone()),
                None => (false, None),
            };
            let words = devfee::fee_words(dev_percent, pool.as_ref(), on_a_pool && answered) + if on_a_pool && !answered { "; pool fee: waiting for the pool's answer" } else { "" };
            (words, pool, answered, src.receipts(), src.share_step())
        });
        if let Some((words, pool, answered, rc, step)) = fee_read {
            self.totals.share_step = step;
            if words != self.totals.fee_words {
                self.totals.fee_words = words.clone();
                self.totals.pool_fee = pool;
                self.totals.pool_fee_answered = answered;
                self.log("app", &words);
            }
            if rc != self.totals.receipts {
                self.totals.receipts = rc;
            }
        }
        // the share lane's node counts, and the dev fee's templates on a node session or a pool session alike
        // (16.3: on a pool the fee session's job stands in for one member template in a hundred)
        let pool_session = self.source.as_ref().map_or(false, |src| !src.economics().solo);
        if self.totals.share_lane || pool_session {
            if let Some(src) = self.source.as_mut() {
                let by_node = src.node_answers();
                if by_node != self.totals.share_nodes {
                    self.totals.share_nodes = by_node;
                }
                // the dev fee's templates and their job ids (devfee.rs): the accepted shares on them are the
                // fee's count in on_outcome
                let (n, jobs) = src.dev_fee_jobs();
                self.totals.dev_fee_templates = n;
                for j in jobs {
                    if self.dev_fee_jobs.insert(j.clone()) {
                        // the fee record (economics' ask of 12 October 2026): one line per fee template issued
                        let height = self.current.as_ref().filter(|c| c.job_id == j).map_or(0, |c| c.height);
                        let line = devfee::fee_record_line("template", &j, height, None, None, self.totals.dev_fee_percent, &self.totals.dev_fee_address);
                        self.fee_record(&line);
                    }
                }
                while self.dev_fee_jobs.len() > 256 {
                    let oldest = self.dev_fee_jobs.iter().next().cloned();
                    match oldest {
                        Some(k) => {
                            self.dev_fee_jobs.remove(&k);
                        }
                        None => break,
                    }
                }
            }
        }
        let ms = now.elapsed().as_millis() as u64;
        self.tick_times.push_back((now, ms));
        while self.tick_times.front().map_or(false, |(t, _)| t.elapsed() > Duration::from_secs(60)) {
            self.tick_times.pop_front();
        }
        self.totals.tick_max_ms = self.tick_times.iter().map(|(_, m)| *m).max().unwrap_or(0);
        let longest = self.tick_phase_longest.take();
        if ms > 1000 {
            let phase = longest.filter(|(_, pm)| *pm * 2 >= ms).map(|(n, pm)| format!("; {pm} ms of it {n}")).unwrap_or_default();
            let line = format!("the supervisor's tick took {ms} ms (every stat and share waits on it; over a second is a fault to find{phase})");
            self.log("app", &line);
        }
    }

    /// The running guard for the 8 GB class (security's headroom exception, the Apple auditor's gate-30): at the
    /// OS's warning level mining pauses (the dataset released, the cache kept), at normal it resumes; the pause
    /// counts as downtime on the state line and as a transition record (path "pressure") charged at the rate
    /// before it. The schedule never lifts this pause. Where the platform has no reading, nothing happens.
    fn watch_memory_pressure(&mut self) {
        let level = (self.pressure_source)();
        self.memory_pressure = level;
        let Some(level) = level else { return };
        match level {
            pressure::Level::Warning | pressure::Level::Critical => {
                if matches!(self.mining, MiningState::Mining | MiningState::Starting) {
                    let line = format!("memory pressure {} ({}): mining pauses, the dataset is released and the cache kept; it resumes at normal", level.word(), pressure::source_name());
                    self.log("app", &line);
                    self.notice("warn", "Paused under memory pressure.", &format!("The system reports {} memory pressure. Mining resumes when it returns to normal.", level.word()));
                    self.pause(PauseCause::Memory);
                }
            }
            pressure::Level::Normal => {
                if matches!(self.mining, MiningState::Paused) && self.paused_for == Some(PauseCause::Memory) {
                    let (gap_s, rate_before) = self.pressure_paused_at.take().map(|(t, r)| (t.elapsed().as_secs_f64(), r)).unwrap_or((0.0, 0.0));
                    let epoch = self.slots.iter().filter_map(|s| s.dataset_epoch).next().unwrap_or(0);
                    let rec = TransitionRecord { device: self.slots.first().map(|s| s.device.index).unwrap_or(0), algo: self.source.as_ref().map(|x| x.algo()).unwrap_or_default(), params_digest: self.params_digest.clone(), from_epoch: epoch, to_epoch: epoch, path: "pressure".into(), switch_ms: 0, gap_s, lost_validated_hashes: rate_before * gap_s, digest: format!("memory pressure pause ({})", pressure::source_name()), recovered: false, at_unix: SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0), keyed_rounds: bond::keyed_rounds(&self.algo_params).unwrap_or(0), rate_before_hps: rate_before, rate_while_building_hps: 0.0, build_ms: 0, swap_wait_ms: None };
                    self.totals.transitions += 1;
                    self.totals.transition_lost_hashes += rec.lost_validated_hashes;
                    self.transitions.push_back(rec);
                    while self.transitions.len() > 256 {
                        self.transitions.pop_front();
                    }
                    let line = format!("memory pressure normal: mining resumes after {gap_s:.1} s paused ({:.0} validated hashes charged at the rate before)", rate_before * gap_s);
                    self.log("app", &line);
                    self.notices.retain(|n| n.text != "Paused under memory pressure.");
                    self.resume();
                }
            }
        }
    }

    /// F-E60: the coordinate documents' self-checks that reported: a failed one refuses its coordinate (if the context
    /// still carries that digest), a passed one is the words; the finished receivers are dropped.
    /// The verifier builds that reported (0.2.22): each registers its coordinate's context with the newest template that
    /// waited on it, or refuses the coordinate with the reference's reason; a build whose thread ended without a result
    /// refuses the coordinate too (never a context without a verifier).
    fn drain_coord_builds(&mut self) {
        let mut done: Vec<(u8, CoordBuild, Result<Arc<dyn Verifier>, String>)> = Vec::new();
        let keys: Vec<u8> = self.coord_builds.keys().copied().collect();
        for c in keys {
            let r = match self.coord_builds.get(&c).map(|b| b.rx.try_recv()) {
                Some(Ok(r)) => r,
                Some(Err(std::sync::mpsc::TryRecvError::Disconnected)) => Err("the verifier build ended without a result".to_string()),
                _ => continue,
            };
            if let Some(b) = self.coord_builds.remove(&c) {
                done.push((c, b, r));
            }
        }
        for (c, b, r) in done {
            match r {
                Ok(v) => {
                    let line = format!("coordinate {c}: the verifier for its document (version {}) is built in {:.1} s", b.version, b.since.elapsed().as_secs_f64());
                    self.log("app", &line);
                    self.finish_coord_registration(c, b.job, b.doc, b.digest, v);
                }
                Err(e) => self.refuse_coord(c, &b.digest, &e),
            }
        }
    }

    /// The coordinate's context from a built verifier: the reference's self-check on its own thread, the context kept,
    /// the slots whose worker reads the anchor from the document alone reconfigured on an anchor change, and the newest
    /// template current (never behind a newer one) with the cards routed.
    fn finish_coord_registration(&mut self, c: u8, job: JobTemplate, doc: serde_json::Value, digest: String, v: Arc<dyn Verifier>) {
        // a worker without the anchor knob reads the anchor from its configure document alone (the GL raster
        // worker, 15 October 2026): every slot on this coordinate whose worker lists no r0d_anchor is
        // reconfigured on the anchored document once the context below is rebuilt
        let mut reconfigure_for_anchor: Vec<usize> = Vec::new();
        if self.coords.get(&c).map(|x| x.digest == digest && x.anchor != job.program_anchor).unwrap_or(false) {
            for i in 0..self.slots.len() {
                if self.slots[i].route.coord == Some(c) && self.slots[i].proc.is_some() && anchor_change_needs_reconfigure(&self.slots[i].knobs) {
                    reconfigure_for_anchor.push(i);
                }
            }
        }
        // F-E60 (the GPU team's rows of 15 October 2026: every tick 1.9 to 2.4 s with seven coordinate contexts, 4.5 s on
        // the first template): the reference's self-check of a coordinate's document ran on the tick thread, one per
        // document and per period anchor; it runs on its own thread now and a failed check refuses the coordinate
        // when it reports (the session's one document keeps its check on the tick: one, at the start)
        if !self.checked_digests.contains(&digest) {
            // one check per document (a digest checked once this session is not checked again on a re-registration
            // or a reopened context)
            self.checked_digests.insert(digest.clone());
            let (tx, rx) = std::sync::mpsc::channel::<(u8, String, Result<u64, String>)>();
            let (vc, hc, dc) = (v.clone(), job.height, digest.clone());
            let _ = thread::Builder::new().name("coord-selfcheck".into()).spawn(move || {
                let _ = tx.send((c, dc, reference_selfcheck(vc, hc)));
            });
            self.coord_checks.push(rx);
        }
        let line = format!("coordinate {c}: document {} (version {}) at height {}, difficulty {}", &digest[..digest.len().min(12)], doc.get("version").and_then(|x| x.as_u64()).unwrap_or(0), job.height, job.coord_difficulty.unwrap_or(0.0));
        self.log("app", &line);
        // a card's refusal of this coordinate lifts only when the coordinate's DOCUMENT changes (a refusal is the document's
        // or the install's); the raster lane's period anchor rebuilds the context every period, and a Windows install
        // without the raster worker was re-probing that lane, refusing it again and closing the card's worker to do it,
        // every 45 s (the first desktop's 5090, 16 October 2026)
        if refusal_lifts(self.coords.get(&c).map(|x| x.digest.as_str()), &digest) {
            for s in self.slots.iter_mut() {
                s.route.refused.retain(|r| *r != c);
            }
        }
        self.coords.insert(c, CoordCtx { paused_since: None, let_go: false, template: job.clone(), doc: doc.clone(), digest: digest.clone(), verifier: v, anchor: job.program_anchor.clone(), last_height: job.coord_last_height });
        self.say_lane_facts(c, &job);
        for i in reconfigure_for_anchor.drain(..) {
            let line = format!("{}: coordinate {c}: its worker takes the anchor from the document alone; reconfigured on the period's anchor", self.slots[i].device.name);
            self.log("app", &line);
            self.reconfigure_slot_on(i, job.height, doc.clone(), digest.clone());
        }
        // F-476: the workers held up for the pool's first document take this one now
        for i in 0..self.slots.len() {
            if self.slots[i].awaiting_document.is_some() {
                if let Some(p) = self.slots[i].proc.take() {
                    let params = self.slots[i].params.clone();
                    let line = format!("{}: the pool's first document registered (coordinate {c}); the waiting worker is configured on it (F-476)", self.slots[i].device.name);
                    self.log("app", &line);
                    self.configure_launched(i, p, params);
                } else {
                    self.slots[i].awaiting_document = None;
                }
            }
        }
        self.totals.share_lane = false;
        self.pace_chain_target = pace_chain_target_from_env();
        self.note_share_target(&job.share_target);
        // the newest template is current, never one behind a template that arrived while the build ran
        if self.current.as_ref().map_or(true, |j| j.height <= job.height) {
            self.current = Some(job);
        }
        self.route_cards("a template");
    }

    fn drain_coord_checks(&mut self) {
        let mut done = Vec::new();
        let mut i = 0;
        while i < self.coord_checks.len() {
            match self.coord_checks[i].try_recv() {
                Ok(r) => { done.push(r); self.coord_checks.swap_remove(i); }
                Err(std::sync::mpsc::TryRecvError::Disconnected) => { self.coord_checks.swap_remove(i); }
                Err(std::sync::mpsc::TryRecvError::Empty) => { i += 1; }
            }
        }
        for (c, digest, r) in done {
            let current = self.coords.get(&c).map(|x| x.digest == digest).unwrap_or(false);
            match r {
                Ok(ms) => {
                    self.totals.selfcheck_ms = ms;
                    self.log("app", &format!("coordinate {c}: the reference checks its document in {ms} ms"));
                }
                Err(e) if e.contains("did not return within") => {
                    // a slow check on a loaded host is not a wrong document (the worker's own configure is the check
                    // that counts); said, never refused
                    self.log("app", &format!("coordinate {c}: the reference's check of its document is slow ({e}); the document stands"));
                }
                Err(e) if current => {
                    self.refuse_coord(c, &digest, &e);
                    for i in 0..self.slots.len() {
                        if self.slots[i].route.coord == Some(c) {
                            self.slots[i].route.coord = None;
                        }
                    }
                }
                Err(_) => {}
            }
        }
    }

    /// 0.2.15: the registry read thread is started when the settings name a chain RPC (at start and on a settings
    /// change); nothing starts twice.
    fn start_registry(&mut self) {
        if self.registry_rx.is_some() {
            return;
        }
        let Some(url) = self.settings.registry_rpc.clone().filter(|u| !u.trim().is_empty()) else { return };
        let (tx, rx) = std::sync::mpsc::channel();
        match registry::spawn(url.trim().to_string(), tx) {
            Ok(()) => {
                self.registry_rx = Some(rx);
                self.log("app", &format!("registry: reading the chain's discipline registry from {} every {} s", url.trim(), registry::REFRESH_S));
            }
            Err(e) => self.log("app", &format!("registry: not read ({e})")),
        }
    }

    /// 0.2.15: the registry thread's events: a succession's words, each re-read document's compile verdict, the
    /// pool's served set against the chain's, once per change; a failed read said once a minute.
    fn drain_registry(&mut self) {
        let mut evs = Vec::new();
        if let Some(rx) = self.registry_rx.as_ref() {
            while let Ok(ev) = rx.try_recv() {
                evs.push(ev);
            }
        }
        for ev in evs {
            match ev {
                registry::RegistryEv::Failed(e) => {
                    if self.registry_failed_said.map_or(true, |t| t.elapsed() > Duration::from_secs(60)) {
                        self.registry_failed_said = Some(Instant::now());
                        self.log("app", &format!("registry: the read failed ({e}); tried again in {} s", registry::REFRESH_S));
                    }
                }
                registry::RegistryEv::View { view, docs, change, compiled } => {
                    self.registry_failed_said = None;
                    for w in registry::words(&view, &change, &docs) {
                        self.log("app", &w);
                    }
                    // 0.2.16: the chain-held registry's successions and activations ahead, said once per change
                    for w in registry::succession_words(&view, &docs) {
                        if !self.registry_docs.iter().any(|(c, d)| docs.get(c).map_or(false, |n| n.activation == d.activation && n.retired_from == d.retired_from && n.successor == d.successor)) || self.registry.is_none() {
                            self.log("app", &w);
                        }
                    }
                    for (c, verdict) in compiled {
                        let d = docs.get(&c);
                        let (name, digest, version) = d.map(|d| (d.name.clone(), d.digest[..d.digest.len().min(12)].to_string(), d.version)).unwrap_or_default();
                        let name = if name.is_empty() { view.names.get(c as usize).cloned().unwrap_or_default() } else { name };
                        match verdict {
                            Ok(()) => self.log("app", &format!("registry: coordinate {c} ({name}) document {digest} (version {version}) compiles on this build{}", if self.worker_serves_version(version) { "; a worker implements it" } else { "; NO shipped worker implements its version, so nothing here mines it" })),
                            Err(e) if e.starts_with("not read: ") => self.log("app", &format!("registry: coordinate {c} ({name}) is not read from the named readers ({})", &e["not read: ".len()..])),
                            Err(e) => self.log("app", &format!("registry: coordinate {c} ({name}) document {digest} (version {version}) does not compile on this build: {e}")),
                        }
                    }
                    let pool: Vec<u8> = self.coords.keys().cloned().collect();
                    let said = registry::served_words(&pool, &view);
                    if said != self.registry_said {
                        if let Some(w) = said.as_ref() {
                            self.log("app", w);
                        }
                        self.registry_said = said;
                    }
                    self.registry = Some(view);
                    self.registry_docs = docs;
                }
            }
        }
    }

    /// Whether a worker in the set declares the igneumhash version (the hello's algorithm list names versions as
    /// "igneumhash" alone today, so the answer is the set's known span: 8 to 16 for the base CUDA and Metal workers,
    /// 17 for a bound coordinate worker, 18 for the Metal worker 9aab3f13); a conservative read for the words alone.
    fn worker_serves_version(&self, version: u64) -> bool {
        let bound: Vec<u8> = self.cfg.coord_launchers.iter().map(|(_, c, _)| *c).collect();
        match version {
            8..=16 => true,
            17 => bound.contains(&5) || bound.contains(&7),
            // the raster lane (coordinate 6): the word is true once a worker bound to coordinate 6 is shipped
            _ if self.cfg.version_launchers.iter().any(|(_, v, _)| *v == version) => true,
            18 => self.slots.iter().any(|s| s.device.backend == "metal"),
            _ => false,
        }
    }

    fn tick_inner(&mut self, dt: Duration) {
        self.drain_registry();
        self.drain_coord_builds();
        self.drain_coord_checks();
        let now = Instant::now();

        // a pause under memory pressure is downtime (the guard's cost is counted, never hidden)
        if matches!(self.mining, MiningState::Paused) && self.paused_for == Some(PauseCause::Memory) {
            self.downtime += dt;
        }
        if matches!(self.mining, MiningState::Mining | MiningState::Starting) && !self.slots.iter().any(|s| matches!(s.state, SlotState::Mining | SlotState::Tuning | SlotState::Preparing)) {
            self.downtime += dt;
        }

        // 0. the verifier's warm lines
        while let Ok(line) = self.warm_rx.try_recv() {
            self.log("app", &line);
        }
        // 0. a slot left idle under a running layer is reopened (the mini on the public pool, 12 October 2026,
        // 03:03Z: the app's card re-detect rebuilt the slots while mining stood, nothing reopened them, and the
        // card read "waiting" with no worker, no word and no refusal for an hour: the class the plug-tune-play rule
        // forbids); the reopen waits IDLE_REOPEN_AFTER so a start or a restart in flight is never doubled
        if reopen_allowed(&self.mining) && self.current.is_some() {
            let now_t = Instant::now();
            for i in 0..self.slots.len() {
                let s = &mut self.slots[i];
                if s.enabled && matches!(s.state, SlotState::Idle) && s.proc.is_none() && !self.pin_refused {
                    // F-E64: a backed-off restart reopens the slot at its time, the idle reopen rule otherwise
                    if let Some(at) = s.restart_after {
                        if now_t >= at {
                            s.restart_after = None;
                            s.idle_since = None;
                            self.open_slot(i);
                        }
                        continue;
                    }
                    let since = *s.idle_since.get_or_insert(now_t);
                    if now_t.duration_since(since) >= IDLE_REOPEN_AFTER {
                        s.idle_since = None;
                        let line = format!("{}: the slot was left idle under a running layer for {} s; reopening it", s.device.name, IDLE_REOPEN_AFTER.as_secs());
                        self.log("app", &line);
                        self.totals.slot_reopens += 1;
                        self.open_slot(i);
                    }
                } else {
                    s.idle_since = None;
                }
            }
        }
        // F-383: launches that ended on their threads (the hello or the fault); a superseded launch's process is ended
        while let Ok(done) = self.launch_rx.try_recv() {
            let i = done.slot;
            if i < self.slots.len() && launch_arrival(self.slots[i].launch_pending.map(|(g, _)| g), done.gen) {
                let took = self.slots[i].launch_pending.take().map(|(_, t)| t.elapsed().as_millis() as u64).unwrap_or(0);
                if took > 1000 {
                    let line = format!("{}: the worker's launch took {took} ms on its own thread; the tick ran meanwhile", self.slots[i].device.name);
                    self.log("app", &line);
                }
                self.finish_open(i, done.result);
            } else if let Ok(mut p) = done.result {
                let _ = p.shutdown(Duration::from_secs(2));
                let who = self.slots.get(i).map(|s| s.device.name.clone()).unwrap_or_default();
                let line = format!("{who}: a launch that outlived its slot's close is ended (F-383)");
                self.log("app", &line);
            }
        }
        // F-471: the gate lets the next queued launches go
        self.drain_launch_queue();
        // F-476: a worker held for the pool's first document past the hello bound is ended with the fault named
        for i in 0..self.slots.len() {
            if self.slots[i].awaiting_document.map_or(false, |t| t.elapsed() > worker::HELLO_BOUND) {
                let name = self.slots[i].device.name.clone();
                self.close_slot(i, "no document from the pool within the hello bound");
                self.slots[i].state = SlotState::Faulted;
                self.slots[i].message = format!("the pool sent no document within {} s; the worker was ended (F-476)", worker::HELLO_BOUND.as_secs());
                let line = format!("{name}: no document from the pool within {} s; the waiting worker is ended (F-476)", worker::HELLO_BOUND.as_secs());
                self.log("app", &line);
            }
        }
        // 1. messages from every worker (bounded per tick: a worker that floods cannot hold the tick)
        for i in 0..self.slots.len() {
            for _ in 0..DRAIN_PER_TICK {
                let Some(msg) = self.slots[i].proc.as_ref().and_then(|p| p.try_recv()) else { break };
                self.handle(i, msg);
            }
            // results the worker's bounded queue dropped: never checked, counted with the unverified
            let dropped = self.slots[i].proc.as_ref().map_or(0, |p| p.take_dropped_results());
            self.totals.unverified += dropped;
        }
        // 1b. results the verifier threads have checked
        for _ in 0..DRAIN_PER_TICK {
            let Some(r) = self.verify_rx.as_ref().and_then(|rx| rx.try_recv().ok()) else { break };
            self.note_verify_latency(r.latency_ms);
            self.on_verified(r.slot, r.share, r.valid, r.shares, r.held);
        }

        // F-383's second half: a tick that did not run counts no hang; the stat clocks restart after a late tick
        if tick_ran_late(dt) {
            for s in self.slots.iter_mut() {
                if s.proc.is_some() {
                    s.last_telemetry = now;
                }
            }
            let line = format!("the tick ran late by {} ms; the stat clocks restart and no hang is counted against it", dt.as_millis());
            self.log("app", &line);
        }
        // 2. heartbeats
        for i in 0..self.slots.len() {
            let s = &self.slots[i];
            // a reconfigure's programme compile (version 14 on a 3060, minutes the first time) sends no stat: the
            // slot in a transition is given the hello bound, the worker's stderr moving the deadline as at a start
            let in_transition = matches!(s.transition, Transition::Recovering { .. } | Transition::Switching { .. }) && s.transition_started.map_or(false, |(t, ..)| now.duration_since(t) < worker::HELLO_BOUND);
            // F-479: a slot still opening with no stat once its build's allowance ran out is restarted with the fault named
            if s.proc.is_some() && s.awaiting_document.is_none() && opening_past_allowance(matches!(s.state, SlotState::Opening), s.build_until.map_or(false, |t| now >= t), now.duration_since(s.last_telemetry)) {
                let name = self.slots[i].device.name.clone();
                let line = format!("{name}: no stat within {} s of the configure; the worker is restarted (F-479)", worker::HELLO_BOUND.as_secs());
                self.log("app", &line);
                self.restart_slot(i, "no stat within the build's allowance");
                continue;
            }
            // F-562: a worker hashing at its rate with no result for the lane's silence bound is restarted with the fault
            // named; after SILENT_RESTARTS_BEFORE_REFUSAL on one coordinate the lane is refused for the card
            if self.slots[i].proc.is_some() && self.slots[i].awaiting_document.is_none() && matches!(self.slots[i].state, SlotState::Mining) {
                if let (Some(c), Some(since)) = (self.slots[i].route.coord, self.slots[i].stint_since) {
                    let rate = self.slots[i].stint.rate();
                    let clock = silence_clock(since, self.slots[i].last_share_at);
                    let expected = self.coords.get(&c).map(|x| router::share_weight(&x.template.share_target)).filter(|w| *w > 0.0 && rate > 0.0).map(|w| w / rate);
                    let bound = result_silence_bound(expected);
                    if silent_while_hashing(true, rate, now.duration_since(clock), bound) {
                        let name = self.slots[i].device.name.clone();
                        let n = self.slots[i].silent_restarts.entry(c).or_insert(0);
                        *n += 1;
                        let n = *n;
                        if n >= SILENT_RESTARTS_BEFORE_REFUSAL {
                            let line = format!("{name}: coordinate {c} yields no result on this card's worker after {n} silent restarts ({} s hashing with nothing found each time); the lane is refused for this card and the router moves it (F-562)", bound.as_secs());
                            self.log("app", &line);
                            if !self.slots[i].route.refused.contains(&c) {
                                self.slots[i].route.refused.push(c);
                            }
                            self.slots[i].last_share_at = Some(now); // the clock starts again for the next lane
                            self.restart_slot(i, "no result while hashing; the lane refused");
                            self.route_cards("a refused coordinate");
                        } else {
                            let line = format!("{name}: no result in {} s while hashing coordinate {c} at {} (the lane's expected share interval {}); the worker is restarted (F-562)", now.duration_since(clock).as_secs(), state::rate_words(rate), expected.map_or("unknown".to_string(), |e| format!("{e:.0} s")));
                            self.log("app", &line);
                            self.slots[i].last_share_at = Some(now); // the restarted worker gets a full bound (the security review's read of 0f069109)
                            self.restart_slot(i, "no result while hashing");
                        }
                        continue;
                    }
                }
            }
            let s = &self.slots[i];
            // F-470: a programme build on a set (the period's anchor) is given the build's allowance, never the ten-second rule
            let in_build = s.build_until.map_or(false, |t| now < t);
            if s.proc.is_some() && !in_transition && matches!(s.state, SlotState::Mining | SlotState::Tuning | SlotState::Paused | SlotState::Preparing) && now.duration_since(s.last_telemetry) > hang_silence_bound(in_build) {
                self.slots[i].hangs.push_back(now);
                while self.slots[i].hangs.front().map_or(false, |t| now.duration_since(*t) > Duration::from_secs(3600)) {
                    self.slots[i].hangs.pop_front();
                }
                if self.slots[i].hangs.len() >= 3 {
                    let name = self.slots[i].device.name.clone();
                    self.close_slot(i, "three hangs in an hour");
                    self.slots[i].state = SlotState::Faulted;
                    self.slots[i].message = "stopped after three hangs in an hour; switch the card off and on to try again".into();
                    self.notice("bad", &format!("{name} stopped after three hangs in an hour."), "");
                } else {
                    self.restart_slot(i, "no stat for 10 s");
                }
            }
        }

        // 3. work
        if matches!(self.mining, MiningState::Mining | MiningState::Starting) {
            let polled = self.source.as_mut().map(|s| s.poll());
            match polled {
                Some(Ok(Some(job))) => {
                    let job_coord = job.coord;
                    self.last_job_at = Some(now);
                    self.no_work_redials = 0;
                    if self.take_template(job) && !self.pin_refusal() {
                        for i in 0..self.slots.len() {
                            // a coordinate's new template cancels only the cards on that coordinate
                            if job_coord.is_some() && self.slots[i].route.coord != job_coord {
                                continue;
                            }
                            if matches!(self.slots[i].state, SlotState::Mining) {
                                self.cancel_slot_job(i);
                                self.slots[i].switch_pending = None;
                            }
                        }
                    }
                }
                Some(Err(e)) => {
                    self.notice("warn", "The work source is not answering.", &e);
                }
                _ => {}
            }
            self.prepare_ahead();
            self.judge_verify_latency();
            self.watch_no_results();
            let outcomes = self.source.as_mut().map(|s| s.outcomes()).unwrap_or_default();
            for (job_id, nonce, o) in outcomes {
                self.submitted_at.remove(&(job_id.clone(), nonce));
                self.last_outcome = Some(now);
                self.on_outcome(job_id, nonce, o);
            }
            self.watch_unanswered(now);
            self.watch_step(now);
            self.watch_no_work(now);
            if self.submitted.len() > 512 {
                // answers that never came: count them stale rather than hold them for ever
                let stale: Vec<_> = self.submitted.keys().take(self.submitted.len() - 256).cloned().collect();
                for k in stale {
                    self.submitted.remove(&k);
                    self.submitted_at.remove(&k);
                    self.totals.stale += 1;
                }
            }
            match self.source.as_ref().and_then(|s| s.problem()) {
                Some(p) => {
                    self.notice("warn", "The work source is not reachable.", &p);
                    if self.source_problem_since.is_none() {
                        self.source_problem_since = Some(now);
                    }
                    if self.on_backup_since.is_some() || matches!(self.settings.work_source_backup, WorkSourceSetting::Unset) {
                        // no backup to hand over to (or already on it): the cards idle and the pool is retried alone
                        self.source_lost(now, &p);
                    }
                }
                None => {
                    self.notices.retain(|n| n.text != "The work source is not reachable.");
                    self.source_problem_since = None;
                }
            }
            self.failover();
            for i in 0..self.slots.len() {
                if matches!(self.slots[i].state, SlotState::Mining | SlotState::Tuning) && self.slots[i].active_job.is_none() {
                    self.dispatch(i);
                }
            }
            // a notice clears the moment its cause clears (notices_still_standing): read on every tick against the cards
            if self.notices.iter().any(|n| n.level == "bad") {
                let mining_cards: Vec<String> = self.slots.iter().filter(|s| matches!(s.state, SlotState::Mining)).map(|s| s.device.name.clone()).collect();
                let kept = notices_still_standing(&self.notices, &mining_cards);
                if kept.len() != self.notices.len() {
                    self.notices = kept;
                }
            }
            if matches!(self.mining, MiningState::Starting) && self.slots.iter().any(|s| matches!(s.state, SlotState::Mining | SlotState::Preparing)) {
                self.mining = MiningState::Mining;
            }
            if matches!(self.mining, MiningState::Starting) && self.slots.iter().all(|s| !s.enabled || matches!(s.state, SlotState::Refused | SlotState::Faulted | SlotState::Off)) {
                self.notice("bad", "No device could start.", "Each card's row says why.");
                self.stop("no device could start");
            }
        }

        // 3a. the pool retry while the cards idle for an unreachable source (outside the mining block: the source is
        // not polled while paused, so the backoff is the only hand on it)
        self.retry_source(now);

        // 3b. the memory-pressure guard (before the schedule: a pause under pressure is never the schedule's to lift)
        self.watch_memory_pressure();

        // 4. the schedule
        if matches!(self.mining, MiningState::Mining | MiningState::Paused) {
            let local = chrono::Local::now();
            let fullscreen = if self.settings.gaming_pause.enabled && self.probe.available() { self.probe.other_app_fullscreen() } else { None };
            if fullscreen == Some(false) && self.paused_for == Some(PauseCause::Game) {
                if self.game_gone_since.is_none() {
                    self.game_gone_since = Some(now);
                }
            } else if fullscreen == Some(true) {
                self.game_gone_since = None;
            }
            // the battery condition (GPU engineer B): on battery, or macOS low power mode; absent fields decide nothing
            let battery_fields: Vec<bool> = self.slots.iter().filter_map(|s| s.telemetry.as_ref()).filter_map(|t| match (t.on_battery, t.low_power_mode) {
                (None, None) => None,
                (a, b) => Some(a.unwrap_or(false) || b.unwrap_or(false)),
            }).collect();
            let on_battery = if battery_fields.is_empty() { None } else { Some(battery_fields.iter().any(|b| *b)) };
            let m = Moment { minute_of_day: local.hour() * 60 + local.minute(), other_app_fullscreen: fullscreen, on_battery, game_gone_for: self.game_gone_since.map(|t| now.duration_since(t)), paused_for: self.paused_for };
            match decide(&self.settings.tariff, &self.settings.time_of_use, &self.settings.gaming_pause, self.settings.pause_on_battery, &m) {
                Decision::Pause(cause) => self.pause(cause),
                Decision::Resume => self.resume(),
                Decision::Run | Decision::StayPaused(_) => {}
            }
        }

        // 5. totals
        if let Some(t0) = self.started_at {
            self.totals.uptime_s = t0.elapsed().as_secs();
        }
        self.totals.downtime_s = self.downtime.as_secs();
        self.totals.preparing_s = self.slots.iter().map(|s| s.preparing.as_secs()).sum();
        let mut all: VecDeque<u64> = VecDeque::new();
        for s in &self.slots {
            all.extend(s.switch_samples.iter().copied());
        }
        self.totals.switch_median_ms = percentile(&all, 0.5);
        self.totals.switch_p99_ms = percentile(&all, 0.99);
        self.totals.switch_samples = all.len();
        self.totals.validated_hps = self.slots.iter_mut().map(|s| s.validated.rate()).sum();
        self.totals.reported_hps = self.slots.iter_mut().map(|s| s.reported.rate()).sum();
        // the Cards page's lane rows: the current lane's seconds and rate accumulate while the card mines on it
        let now_i = Instant::now();
        for s in self.slots.iter_mut() {
            let dt = s.lane_tick_at.map(|t| now_i.duration_since(t).as_secs_f64()).unwrap_or(0.0);
            s.lane_tick_at = Some(now_i);
            if s.state != SlotState::Mining { continue }
            if let Some(c) = s.route.coord {
                // the lane's own rate is the stint meter's (fed beside `reported`, reset at every switch), never the card's
                // whole-window meter: the mini's 0.2.20 sheet read "the programme lane 1.2 MH/s" during a 45 s probe
                // whose worker ran at 32 kH/s, the window still carrying the bandwidth lane's hashes (02:1x UK, 16 October 2026)
                let r = s.stint.rate();
                let l = s.lane_stats.entry(c).or_default();
                l.seconds += dt;
                if r > 0.0 { l.hps = r; }
            }
        }
        // the rate the next pool subscribe declares (the pool starts the share target at that rate's step); F-E66: kept
        // in the settings every minute while above zero, so the next run's FIRST dial declares it
        // F-469: the declared rate is each mining card's rate on its own lane as the router reads it (the measured
        // share rate once settled), the reported total only while no lane rate is known
        let lane_declared: f64 = self.slots.iter().filter(|s| s.proc.is_some() && s.state == SlotState::Mining).map(|s| s.route.coord.map_or(0.0, |c| s.route.rates.get(&c).copied().unwrap_or_else(|| s.lane_stats.get(&c).map_or(0.0, |l| l.hps)))).sum();
        let declared = if lane_declared > 0.0 { lane_declared } else { self.totals.reported_hps };
        if declared as u64 > 0 {
            work::LAST_RATE_HPS.store(declared as u64, std::sync::atomic::Ordering::Relaxed);
            if self.last_rate_saved.map_or(true, |t| t.elapsed() > Duration::from_secs(60)) {
                self.last_rate_saved = Some(Instant::now());
                self.settings.last_rate_hps = self.totals.reported_hps as u64;
                let _ = self.settings.save(&self.cfg.settings_path());
            }
        }
        self.totals.device_watts = self.slots.iter().filter_map(|s| s.telemetry.as_ref().and_then(|t| t.power_w)).sum();
        self.totals.last_submit_age_s = self.last_submit.map(|t| t.elapsed().as_secs());
        self.totals.last_outcome_age_s = self.last_outcome.map(|t| t.elapsed().as_secs());
        self.totals.expected_block_s = {
            let d = self.source.as_ref().map(|s| s.economics().difficulty).unwrap_or(0.0);
            let r = self.totals.validated_hps;
            if d > 0.0 && r > 0.0 && (d / r).is_finite() { Some(d / r) } else { None }
        };
        self.totals.energy = {
            let rate = self.totals.validated_hps;
            let dev: f64 = self.slots.iter_mut().filter_map(|s| s.power.mean()).sum();
            match self.settings.costs.wall_watts {
                Some(w) if w > 0.0 => energy_view(rate, w, "wall"),
                _ if dev > 0.0 => energy_view(rate, dev + self.settings.costs.host_allowance_w.unwrap_or(40.0), "estimated"),
                _ => None,
            }
        };
    }

    fn handle(&mut self, i: usize, msg: Inbound) {
        let name = self.slots[i].device.name.clone();
        match msg {
            Inbound::Log(line) => {
                let src = self.slots[i].worker_name.clone();
                self.log(&src, &line);
            }
            Inbound::Fault(f) => {
                let now = Instant::now();
                // F-306: a lane's own worker (the slot's launcher is not its base) that sends malformed output refuses that
                // lane for the card; the base worker comes back and the card mines the other lanes; the card is never closed
                let on_lane = self.slots[i].base_launcher.as_ref().map_or(false, |b| b.exe != self.slots[i].launcher.exe);
                if on_lane {
                    let c = self.slots[i].route.coord;
                    let lane = c.map(|c| format!("coordinate {c}")).unwrap_or_else(|| "its lane".to_string());
                    let line = format!("{name}: the lane's own worker sent malformed output ({f}); {lane} is refused for this card, the base worker comes back and the card mines the other lanes (F-306)");
                    self.log("app", &line);
                    self.notice("warn", &format!("{name}: {lane}'s worker failed on this card."), "The card mines the other lanes meanwhile.");
                    if let Some(c) = c {
                        if !self.slots[i].route.refused.contains(&c) { self.slots[i].route.refused.push(c); }
                    }
                    self.slots[i].route.coord = None;
                    self.slots[i].coord_digest = None;
                    if let Some(b) = self.slots[i].base_launcher.clone() {
                        let keep = self.slots[i].launcher.clone();
                        let mut next = b; next.env = keep.env.clone(); next.workdir = keep.workdir.clone();
                        self.slots[i].launcher = next;
                    }
                    self.restart_slot(i, "the lane's worker faulted (F-306)");
                    self.route_cards("a lane refused (F-306)");
                    return;
                }
                self.slots[i].proto_faults.push_back(now);
                while self.slots[i].proto_faults.front().map_or(false, |t| now.duration_since(*t) > Duration::from_secs(600)) {
                    self.slots[i].proto_faults.pop_front();
                }
                if self.slots[i].proto_faults.len() >= 2 {
                    self.close_slot(i, "malformed output twice in ten minutes");
                    self.slots[i].state = SlotState::Faulted;
                    self.slots[i].message = format!("the worker sent malformed output twice: {f}");
                    self.notice("bad", &format!("{name}: the worker sent malformed output twice and was stopped."), &f);
                } else {
                    self.restart_slot(i, &format!("malformed output: {f}"));
                }
            }
            Inbound::Exited => {
                if self.slots[i].proc.is_some() && !matches!(self.slots[i].state, SlotState::Idle | SlotState::Off) {
                    // F-E64 (the GPU team's row of 15 October 2026, 10:57 UK: a worker dying at once on a coordinate's document was
                    // restarted 60 times in ten minutes): a quick exit is counted; the third within QUICK_EXIT_S of its
                    // start on a coordinate refuses that coordinate for the card and the router moves on; the restarts
                    // back off (5, 15, 60 s) while the quick exits run
                    let quick = self.slots[i].opened_at.map_or(false, |t| t.elapsed() < Duration::from_secs(QUICK_EXIT_S));
                    if quick { self.slots[i].quick_exits += 1; } else { self.slots[i].quick_exits = 0; }
                    let name = self.slots[i].device.name.clone();
                    if quick && self.slots[i].quick_exits >= 3 {
                        if let Some(c) = self.slots[i].route.coord {
                            let line = format!("{name}: its worker exited at once three times on coordinate {c}; the coordinate is refused for this card and the router moves on");
                            self.log("app", &line);
                            if !self.slots[i].route.refused.contains(&c) { self.slots[i].route.refused.push(c); }
                            self.slots[i].route.coord = None;
                            self.slots[i].coord_digest = None;
                            self.slots[i].quick_exits = 0;
                            self.restart_slot(i, "the worker exited at once on its coordinate");
                            self.route_cards("a refused coordinate");
                        } else {
                            self.restart_slot(i, "the worker exited");
                        }
                    } else {
                        let wait = match self.slots[i].quick_exits { 0 => 0, 1 => 5, 2 => 15, _ => 60 };
                        if wait > 0 {
                            let line = format!("{name}: the worker exited {} s after its start; restarting in {wait} s", self.slots[i].opened_at.map_or(0, |t| t.elapsed().as_secs()));
                            self.log("app", &line);
                            self.slots[i].restart_after = Some(Instant::now() + Duration::from_secs(wait));
                            self.close_slot(i, "the worker exited at once");
                            self.slots[i].state = SlotState::Idle;
                        } else {
                            self.restart_slot(i, "the worker exited");
                        }
                    }
                }
            }
            Inbound::Msg(Envelope { body, .. }, arrived) => {
                // the project's rule of 18:0x UK, 14 October 2026: a message over the protocol's bound is truncated to it and
                // logged, never refused as malformed; a worker is never restarted on the shape of a log line
                let mut body = body;
                for (field, n) in proto::bound_from_worker(&mut body) {
                    let line = format!("{name}: the worker's {field} of {n} bytes cut to the protocol's bound; the rest is on its stderr");
                    self.log("app", &line);
                }
                if let Err(e) = proto::validate_from_worker(&body) {
                    self.handle(i, Inbound::Fault(e.to_string()));
                    return;
                }
                match body {
                    FromWorker::Hello(_) => {}
                    FromWorker::Ready(r) => {
                        // under a per-slot visible-devices environment the worker names its one device as ordinal 0
                        let scoped = self.slots[i].launcher.env.iter().any(|(k, _)| k.ends_with("_VISIBLE_DEVICES"));
                        if !r.device.is_empty() && r.device != self.slots[i].device.id && !(scoped && r.device.ends_with(":0")) {
                            let line = format!("FAULT F-E41 {}: the worker opened {} for the slot {} (the slot's card idles while another is driven twice)", self.slots[i].device.name, r.device, self.slots[i].device.id);
                            self.log("app", &line);
                        }
                        let s = &mut self.slots[i];
                        // the worker starts at its defaults; ask for the safe point one knob at a time
                        let defaults: Params = s.knobs.iter().map(|k| (k.name.clone(), k.default)).collect();
                        let wanted = s.params.clone();
                        if let Some(p) = s.proc.as_mut() {
                            send_knobs(p, &defaults, &wanted);
                        }
                        s.device.memory_free = r.budget.free_bytes;
                        s.can_warm = r.budget.can_warm;
                        s.state = if matches!(self.mining, MiningState::Paused) { SlotState::Paused } else { SlotState::Mining };
                        s.opened_at = Some(Instant::now());
                        s.last_telemetry = Instant::now();
                        s.last_validated_at = Some(Instant::now());
                        s.message = String::new();
                        let params = s.params.clone();
                        self.set_stage("Opening the card", "done", "");
                        self.set_stage("Building the dataset", "done", &format!("{:.2} GB fits cold{}", r.budget.fit_cold_bytes as f64 / devices::GIB as f64, if r.budget.can_warm { ", the next epoch can be prepared warm" } else { "" }));
                        self.set_stage("First results checked", "active", "");
                        let _ = self.journal.record("stage", "ready");
                        let line = format!("{name}: ready, {:.2} GB free of {:.2} GB, {}", r.budget.free_bytes as f64 / devices::GIB as f64, r.budget.total_bytes as f64 / devices::GIB as f64, params_text(&params));
                        self.log("app", &line);
                    }
                    FromWorker::Refuse(r) if self.slots[i].route.coord.is_some() && self.slots[i].coord_digest.is_some() => {
                        // VectorWork: the worker refuses a coordinate's document at configure (the Metal worker on
                        // DOC-v13, 03:06 UK 13 October 2026): the coordinate is refused for this card, the slot is
                        // reopened and the router routes the card on; never a failed card
                        let c = self.slots[i].route.coord.unwrap_or(0);
                        let name = self.slots[i].device.name.clone();
                        let line = format!("{name}: coordinate {c}: the worker refuses its document ({}); the coordinate is refused for this card and the router moves on", r.reason);
                        self.log("app", &line);
                        {
                            let s = &mut self.slots[i];
                            if !s.route.refused.contains(&c) { s.route.refused.push(c); }
                            s.route.rates.remove(&c);
                            s.route.probing = false;
                            s.route.coord = None;
                            s.stint_since = None;
                            s.coord_digest = None;
                        }
                        if self.settings.coordinate_pin == Some(c) {
                            // the pinned coordinate itself refused (the GPU team's row of 15 October 2026, 10:34 UK: the card was
                            // reopened against the pin every template for the rest of the window): the card idles with the
                            // words until the pin or the coordinate's document changes
                            let msg = format!("coordinate {c} is pinned and this card's worker refuses its document ({}); the card waits for the pin or the document to change", r.reason);
                            self.close_slot(i, "the pinned coordinate's document refused");
                            self.slots[i].state = SlotState::Idle;
                            self.slots[i].message = msg.clone();
                            self.slots[i].idle_since = Some(Instant::now() + Duration::from_secs(3600));
                            self.notice("bad", "The pinned coordinate's document is refused by the worker.", &msg);
                        } else {
                            self.close_slot(i, "the coordinate's document refused; reopening");
                            self.slots[i].state = SlotState::Idle;
                            self.open_slot(i);
                            self.route_cards("a refused coordinate");
                        }
                    }
                    FromWorker::Refuse(r) => {
                        let explain = refusal_sentence(&self.slots[i].device, r.need_bytes, r.have_bytes, r.term.as_deref(), &r.reason);
                        self.close_slot(i, "refused");
                        self.slots[i].state = SlotState::Refused;
                        self.slots[i].message = explain.clone();
                        self.set_stage("Opening the card", "failed", &explain);
                        let _ = self.journal.record("device_refused", r.term.as_deref().unwrap_or("refused"));
                        let _ = self.journal.record("failure_shown", "device_refused");
                        self.notice("bad", &format!("{name} cannot mine: {explain}"), "");
                        let line = format!("{name}: refused: {} ({})", r.reason, r.term.unwrap_or_default());
                        self.log("app", &line);
                    }
                    FromWorker::Prepared(p) => {
                        let line = format!("{name}: epoch {} prepared {} in {} ms ({:.2} GB){}", p.epoch, p.mode, p.ms, p.bytes as f64 / devices::GIB as f64, p.digest.as_ref().map(|d| format!(", digest {}", &d[..16])).unwrap_or_default());
                        self.log("app", &line);
                        let s = &mut self.slots[i];
                        if let Some((t0, h)) = s.building.take() {
                            let secs = t0.elapsed().as_secs_f64().max(0.001);
                            s.last_build = Some((t0.elapsed().as_millis() as u64, h / secs));
                        }
                        match s.transition.clone() {
                            Transition::Preparing { to } if to == p.epoch => {
                                s.transition = Transition::Ready { to };
                                s.next_epoch = Some(to);
                                let (ms, r) = s.last_build.unwrap_or((p.ms, 0.0));
                                let line = format!("{name}: epoch {to} built beside mining in {ms} ms at {:.3} MH/s reported while building (before: {:.3} MH/s)", r / 1e6, s.reported.rate() / 1e6);
                                self.log("app", &line);
                            }
                            Transition::Switching { to, .. } | Transition::Recovering { to } if to == p.epoch => {
                                // the cold path's build is done; the stat that reports mining on it completes the record
                                s.epoch = Some(p.epoch);
                            }
                            _ => {
                                if s.dataset_epoch.is_none() {
                                    s.dataset_epoch = Some(p.epoch);
                                }
                                s.epoch = Some(p.epoch);
                            }
                        }
                        self.set_stage("Building the dataset", "done", &format!("epoch {} in {:.1} s", p.epoch, p.ms as f64 / 1000.0));
                    }
                    FromWorker::Share(sh) => self.on_share(i, sh),
                    FromWorker::Stat(st) => {
                        let mut prepared_now = false;
                        let mut anchor_resent: Option<String> = None;
                        let s = &mut self.slots[i];
                        let since = s.last_telemetry.elapsed();
                        s.last_telemetry = Instant::now();
                        // F-470: the build's allowance ends at the worker's first mining stat with hashes after the set
                        if st.hashes > 0 && st.state == WorkerState::Mining && s.build_until.is_some() {
                            s.build_until = None;
                        }
                        s.reported.add(st.hashes as f64);
                        s.stint.add(st.hashes as f64);
                        if st.hashes > 0 { s.last_result_at = Some(Instant::now()); } // F-439: work is the settle's clock
                        // the worker's own word on whether it hashes: a mining worker holding a job that reports
                        // zero hashes stat after stat is idle, whatever its averaged rate still says
                        if st.hashes > 0 || st.state != WorkerState::Mining || s.active_job.is_none() {
                            s.idle_hashes_since = None;
                        } else if s.idle_hashes_since.is_none() {
                            s.idle_hashes_since = Some(Instant::now());
                        }
                        if let Some(p) = st.telemetry.power_w {
                            s.power.add(p);
                        }
                        // the measurement target follows the device's rate: about one validated result a second,
                        // so a fast card never floods the verifier (the earlier constant of 50,000 meant a hundred
                        // results a second from a 6 MH/s card)
                        {
                            let r = s.reported.rate();
                            if r > 0.0 && s.measure_difficulty * 2.0 < r {
                                s.measure_difficulty = (r * 1.0).min(MAX_MEASURE_DIFFICULTY);
                            }
                        }
                        if let Some((_, h)) = s.building.as_mut() {
                            *h += st.hashes as f64;
                        }
                        if let Some(acc) = s.tune.as_mut() {
                            acc.note_telemetry(&st.telemetry);
                        }
                        // the switch delay: the first stat naming the job sent after a template change
                        let mut switch_ms: Option<u64> = None;
                        if let Some((jid, at)) = s.switch_pending.clone() {
                            if st.job_id == jid && st.state == WorkerState::Mining {
                                let ms = arrived.saturating_duration_since(at).as_millis() as u64;
                                s.switch_samples.push_back(ms);
                                while s.switch_samples.len() > 1000 {
                                    s.switch_samples.pop_front();
                                }
                                s.switch_pending = None;
                                switch_ms = Some(ms);
                            }
                        }
                        if st.state == WorkerState::Preparing && s.transition_started.is_some() {
                            s.preparing_gap = Some(s.preparing_gap.unwrap_or(Duration::ZERO) + since.min(Duration::from_secs(60)));
                        }
                        if let Some(e) = st.epoch {
                            if s.epoch != Some(e) {
                                let line = format!("{name}: epoch {e}");
                                s.epoch = Some(e);
                                self.log("app", &line);
                            }
                            let s = &mut self.slots[i];
                            if s.dataset_epoch.is_none() && st.state == WorkerState::Mining {
                                s.dataset_epoch = Some(e);
                            }
                        }
                        // a transition completes at the first stat that reports mining on the target epoch: the
                        // stat's own epoch where the worker sends one, else the epoch its last `prepared` named
                        // (the 5090 pod, 11 October: a worker whose stats carry no epoch left a cold document
                        // change RECOVERING for good and wrote no record)
                        {
                            let s = &mut self.slots[i];
                            let reported_epoch = st.epoch.or(s.epoch);
                            let done = match (s.transition.clone(), reported_epoch) {
                                (Transition::Switching { to, .. } | Transition::Recovering { to }, Some(e)) => st.state == WorkerState::Mining && e == to,
                                _ => false,
                            };
                            if done {
                                let ms = switch_ms.or_else(|| s.transition_started.map(|(t, ..)| t.elapsed().as_millis() as u64)).unwrap_or(0);
                                self.finish_transition(i, ms, None);
                            }
                        }
                        if st.state == WorkerState::Preparing {
                            let detail = match st.phase.as_str() { "transition" => "moving to the next epoch", "compile" => "building the kernels", _ => "building the dataset on the device" };
                            let d = detail.to_string();
                            let n = self.stages.iter().position(|x| x.name == "Building the dataset");
                            if let Some(n) = n {
                                if self.stages[n].status != "done" {
                                    self.stages[n].status = "active".into();
                                    self.stages[n].detail = d;
                                }
                            }
                        }
                        let s = &mut self.slots[i];
                        match st.state {
                            WorkerState::Building => {
                                // F-470's worker half: the worker rebuilds the period's programme on the card and will be
                                // silent while it does; the build's allowance opens here (its first line, never from the
                                // supervisor's guess), the next mining stat closes it; shown as preparation meanwhile
                                s.build_until = Some(Instant::now() + worker::HELLO_BOUND);
                                if matches!(s.state, SlotState::Mining | SlotState::Opening) {
                                    s.state = SlotState::Preparing;
                                }
                                s.phase = if st.building.as_deref().unwrap_or("").is_empty() { "the period's programme".into() } else { st.building.clone().unwrap_or_default() };
                                s.preparing += since.min(Duration::from_secs(60));
                            }
                            WorkerState::Preparing => {
                                // not mining: the dataset, the kernels or the next epoch; shown as such, counted as preparation
                                if !matches!(s.state, SlotState::Preparing) && matches!(s.state, SlotState::Mining | SlotState::Opening) {
                                    s.state = SlotState::Preparing;
                                }
                                s.phase = if st.phase.is_empty() { "dataset".into() } else { st.phase.clone() };
                                s.preparing += since.min(Duration::from_secs(60));
                            }
                            WorkerState::Mining | WorkerState::Idle => {
                                if matches!(s.state, SlotState::Preparing) {
                                    s.state = SlotState::Mining;
                                    prepared_now = true;
                                    // the GPU team's pool-l16 session of 15:0x UK, 15 October 2026: the period's anchor changed while
                                    // the worker built its cache (two and a half minutes), the set went to it during the
                                    // prepare and was lost, and every result after it under the old anchor failed the
                                    // verifier; the last anchor is set again the moment the worker is ready (idempotent)
                                    if let (Some(a), true) = (s.last_anchor.clone(), s.knobs.iter().any(|k| k.name == "r0d_anchor")) {
                                        if let Some(p) = s.proc.as_mut() {
                                            let _ = p.send(&ToWorker::SetText(proto::SetText { knob: "r0d_anchor".into(), value: a.clone() }));
                                        }
                                        s.build_until = Some(Instant::now() + worker::HELLO_BOUND); // F-470
                                        anchor_resent = Some(a);
                                    }
                                }
                                s.phase.clear();
                                // the chunk is exhausted when the worker reports idle on it
                                if st.state == WorkerState::Idle && s.active_job.as_deref().map_or(false, |j| st.job_id.is_empty() || st.job_id == j) {
                                    s.active_job = None;
                                }
                            }
                            WorkerState::Paused => {}
                        }
                        s.telemetry = Some(st.telemetry);
                        if let Some(a) = anchor_resent.as_deref() {
                            let line = format!("{}: the period's anchor {} is set again now the worker is ready (a set during its prepare may be lost)", self.slots[i].device.name, &a[..a.len().min(12)]);
                            self.log("app", &line);
                        }
                        if prepared_now {
                            self.set_stage("Building the dataset", "done", "");
                        }
                    }
                    FromWorker::Fault(f) => {
                        let src = self.slots[i].worker_name.clone();
                        let line = format!("fault {} ({}): {}", f.code, if f.recoverable { "recoverable" } else { "fatal" }, f.message);
                        self.log(&src, &line);
                        if f.recoverable {
                            // F-E9 (13 October 2026): a recoverable fault on a knob (the Metal worker's "unknown knob" on the
                            // anchor set) leaves the job running; clearing the job re-dispatched the lane from its start
                            // (the re-found shares behind F-E5) and, under F-E5's guard, idled the card for the job
                            if f.code != "knob" {
                                self.slots[i].active_job = None;
                            }
                            if f.code == "protocol" {
                                self.handle(i, Inbound::Fault(format!("the worker rejected a line: {}", f.message)));
                            }
                        } else if f.code == "configure" && self.slots[i].route.coord.is_some() && self.coords.len() > 1 {
                            // F-E56 (the GPU team's row of 15 October 2026, 08:30 UK: the shipped CUDA worker refused coordinate 0's
                            // document at configure as fatal, the engine closed the one card and stopped the session within a
                            // second with "no device could start"): a worker that cannot take ONE coordinate's document has
                            // refused that coordinate, not the session; the card is routed to the next served coordinate as
                            // F-E47 routes a refused result, and only a card refused on every served coordinate is closed
                            let c = self.slots[i].route.coord.unwrap();
                            let served: Vec<u8> = self.coords.keys().cloned().collect();
                            if !self.slots[i].route.refused.contains(&c) {
                                self.slots[i].route.refused.push(c);
                            }
                            let left: Vec<u8> = served.iter().cloned().filter(|x| !self.slots[i].route.refused.contains(x)).collect();
                            if left.is_empty() {
                                self.close_slot(i, "fatal fault");
                                self.slots[i].state = SlotState::Faulted;
                                self.slots[i].document_fault = true;
                                self.slots[i].message = format!("{}; every served coordinate refused", f.message);
                                self.notice("bad", &format!("{name} stopped: its worker takes none of the coordinates this source serves"), &f.message);
                            } else {
                                let line = format!("{name}: its worker refused coordinate {c}'s document ({}); the router moves it to one of {}", f.message, left.iter().map(|x| x.to_string()).collect::<Vec<_>>().join(", "));
                                self.log("app", &line);
                                self.slots[i].active_job = None;
                                self.slots[i].route.coord = None;
                                self.slots[i].coord_digest = None;
                                self.restart_slot(i, "the worker refused the coordinate's document");
                            }
                        } else {
                            self.close_slot(i, "fatal fault");
                            self.slots[i].state = SlotState::Faulted;
                            // a configure refusal is the session document's fault: a fresh session reopens the card
                            self.slots[i].document_fault = f.code == "configure";
                            self.slots[i].message = f.message.clone();
                            self.notice("bad", &format!("{name} stopped: {}", f.message), "");
                        }
                    }
                    FromWorker::Bye => {
                        if !matches!(self.slots[i].state, SlotState::Idle | SlotState::Off) {
                            self.restart_slot(i, "the worker said bye");
                        }
                    }
                    FromWorker::Knob(a) => {
                        // an ack without the knob's name (the Metal worker's form) answers the last set this
                        // supervisor sent; `release_dataset` is not a launch parameter and is not kept
                        let name = if a.knob.is_empty() { self.slots[i].last_set.clone() } else { a.knob };
                        if !name.is_empty() && name != "release_dataset" {
                            self.slots[i].params.insert(name, a.value);
                        }
                    }
                    FromWorker::Unknown => {
                        // A kind this protocol version does not name: ignored, counted, named once per worker in
                        // the log. Never malformed and never a restart (the mini's parity row 4 lost its Metal
                        // worker to its `jobdone` line).
                        self.totals.unknown_messages += 1;
                        if self.totals.unknown_messages == 1 {
                            self.log(&name, "ignored a message of a kind this protocol version does not name (counted as unknown_messages)");
                        }
                    }
                }
            }
        }
    }

    /// F-564's remaining guards (18 October 2026, the first Windows proof host's raster row: three results cancelled with
    /// no line): every site that cancels a result says one line naming itself, once per job, so a card that finds
    /// results no share of which reaches the pool is read from its log and not guessed at.
    fn say_cancel(&mut self, i: usize, job_id: &str, site: &str) {
        let key = format!("{job_id}:{site}");
        if self.cancelled_said.insert(key) {
            let name = self.slots.get(i).map(|s| s.device.name.clone()).unwrap_or_default();
            let line = format!("{name}: a result for job {job_id} cancelled: {site}; {} cancelled this session (F-564)", self.totals.cancelled);
            self.log("app", &line);
            while self.cancelled_said.len() > 64 {
                if let Some(k) = self.cancelled_said.iter().next().cloned() { self.cancelled_said.remove(&k); } else { break; }
            }
        }
    }

    /// F-695: on a pool session a sent job is still valid while its pool job id is among the last POOL_JOBS_RETAINED
    /// distinct pool jobs issued to the session (the pool's own retained window); the height rule is a solo source's.
    fn job_still_retained(&self, sent_pool_job_id: &str) -> bool {
        let recent = recent_pool_jobs(self.sent_order.iter().rev().map(|s| s.as_str()), POOL_JOBS_RETAINED);
        recent.iter().any(|j| j == sent_pool_job_id)
    }

    /// Whether the source is a pool session (the pool answers stale itself): the still-valid window's kind.
    fn pool_session_now(&self) -> bool {
        self.source.as_ref().map_or(false, |src| !src.economics().solo)
    }

    fn on_share(&mut self, i: usize, sh: proto::Share) {
        let Some(sent) = self.sent.get(&sh.job_id).cloned() else {
            self.totals.cancelled += 1;
            self.say_cancel(i, &sh.job_id, "a job this session no longer holds (let go at a re-dial or past the held set)");
            return;
        };
        if !sent.tuning {
            let live = self.slot_template(i);
            // F-564: the template moved on; a job of the live height or one behind on the slot's own coordinate is still
            // valid work and goes on (the pool answers in the open); an earlier one is cancelled, said once per job
            let gone = match live.as_ref() {
                None => true,
                Some(c) => c.job_id != sent.template.job_id && (if self.pool_session_now() { sent.template.coord != c.coord || !self.job_still_retained(&sent.template.job_id) } else { superseded_job(sent.template.coord, sent.template.height, c.coord, c.height) }),
            };
            if gone {
                self.totals.cancelled += 1;
                if self.cancelled_said.insert(sh.job_id.clone()) {
                    let name = self.slots[i].device.name.clone();
                    let line = format!("{name}: a result for job {} (height {}{}) cancelled: the template moved on to {} (height {}); {} cancelled this session (F-564)", sh.job_id, sent.template.height, sent.template.coord.map_or(String::new(), |c| format!(", coordinate {c}")), live.as_ref().map_or("none".to_string(), |c| c.job_id.clone()), live.as_ref().map_or(0, |c| c.height), self.totals.cancelled);
                    self.log("app", &line);
                    while self.cancelled_said.len() > 64 {
                        if let Some(k) = self.cancelled_said.iter().next().cloned() { self.cancelled_said.remove(&k); } else { break; }
                    }
                }
                return;
            }
        }
        let live_digest = self.slots[i].coord_digest.clone().unwrap_or_else(|| self.params_digest.clone());
        let live_template = self.slot_template(i);
        let verifier = self.verifier_for_digest(&sent.params_digest);
        if sent.params_digest != live_digest || live_template.as_ref().map_or(true, |c| template_epoch(verifier.as_ref(), c) != sent.epoch) {
            // a result tagged with another parameter set or epoch than the slot's live template's: never submitted
            self.totals.cancelled += 1;
            self.say_cancel(i, &sh.job_id, "another parameter set or epoch than the slot's live template's");
            return;
        }
        if sent.slot != i {
            self.handle(i, Inbound::Fault(format!("share for another device's job {}", sh.job_id)));
            return;
        }
        let (Some(mix), Some(hash)) = (proto::hex_decode(&sh.mix), proto::hex_to_32(&sh.hash)) else {
            self.handle(i, Inbound::Fault("share fields".into()));
            return;
        };
        let Some(v) = verifier.as_ref() else { return };
        if v.algo() != sent.algo {
            // a share for a job issued under another algorithm version: never counted, never submitted
            self.totals.cancelled += 1;
            self.say_cancel(i, &sh.job_id, "a job issued under another algorithm version");
            return;
        }
        // a pool session's pace is applied before the check: the pool sets one share target for every card, so a
        // fast card's results arrive by the thousand a second and checking them all stretched the tick to 5 s
        // (an RTX 5090, 12 October 2026: 53,839 verified shares thrown away by the cap in 100 s); a pool's result
        // is never a block, so nothing held back here is lost
        // F-E66 (main, 15 October 2026, 12:1x UK: the founder's PCs were credited for 14 and 11 of about 100 MH/s because
        // the engine held results back at its own twelve-a-second bucket while the pool retargets each session from what
        // it submits): no client-side cap; every result that meets the job's difficulty is submitted, the pool's own
        // bucket (50 a second, its refusal "admission: rate", counted by the pool as the session's rate) paces a card
        // for its first seconds and the retarget lifts it off. The verify queue stays the bound on the tick (a full
        // queue drops the result and counts it unverified, never a stall).
        let solo = self.source.as_ref().map_or(false, |s| s.economics().solo);
        let mut held = false;
        // the security review's rule for the cut (15 October 2026): a result the pool paced is dropped, never resubmitted, and the
        // engine backs its submit rate off to the pool's bucket for a minute after such a refusal, so a fast card does not
        // amplify the pool's refusals into a flood while the retarget settles; no gate otherwise
        if !solo && !sent.tuning {
            if let Some(cap) = paced_cap_for(&self.pool_bucket_caps, sent.template.coord, Instant::now()) {
                {
                    let total: f64 = self.slots.iter_mut().map(|s| s.reported.rate().max(0.0)).sum();
                    let mine = self.slots[i].reported.rate().max(0.0);
                    let share_cap = if total > 0.0 { ((cap as f64 * mine / total).round() as u32).max(1) } else { cap };
                    let now = Instant::now();
                    if !self.slots[i].slot_pace.admit(now, share_cap, Duration::ZERO) || !self.submit_pace.admit(now, cap, Duration::ZERO) {
                        let name = self.slots[i].device.name.clone();
                        self.not_submitted(&name, cap);
                        if !self.slots[i].held_verify_pace.admit(now, HELD_VERIFY_PER_S, Duration::ZERO) {
                            return;
                        }
                        held = true;
                    }
                }
            } else {
                self.pool_bucket_caps.remove(&sent.template.coord);
            }
        }
        let sk = if v.keyed_rounds().is_some() { self.cfg.bond_secret.clone() } else { None };
        let share_class = stubhash::meets_target(&hash, &sent.template.share_target);
        let req = VerifyReq { slot: i, verifier: v.clone(), height: sent.template.height, header: sent.template.header, mix, hash, share: sh, queued: Instant::now(), sk, held, share_class };
        // the verify-queue cell: a measurement-only result whose expected wait (the queue's depth times the mean check over
        // the threads) would pass half the block interval is dropped at the door, counted unverified, so a checked result is
        // current; a share-class result is never dropped by the bound (it is checked first)
        let interval_s = self.source.as_ref().map_or(15.0, |s| s.economics().block_interval_s).clamp(6.0, 60.0);
        let depth = self.verify_depth.load(AtomicOrdering::Relaxed);
        let check_us = self.verify_check_us.load(AtomicOrdering::Relaxed);
        let too_late = !share_class && verify_wait_exceeds(depth, check_us, self.verify_threads, (interval_s * 500.0) as u64);
        let sent_to = if too_late { Some(Err(TrySendError::Full(req))) } else {
            self.verify_depth.fetch_add(1, AtomicOrdering::Relaxed);
            let r = if share_class { self.verify_tx.as_ref().map(|tx| tx.try_send(req)) } else { self.verify_tx_measure.as_ref().map(|tx| tx.try_send(req)) };
            if matches!(r, Some(Err(_))) { self.verify_depth.fetch_sub(1, AtomicOrdering::Relaxed); }
            r
        };
        match sent_to {
            Some(Err(TrySendError::Full(_))) => {
                // the verifier is behind: the result is not checked and not submitted; counted, said once a minute,
                // the measurement target stepped once a minute to the reported rate (one result a second) or
                // doubled, never per result (the 5090 at 170 MH/s, 11 October: a doubling per dropped result ran
                // the target to its cap within a second and the watchdog's reset fought it back, ten lines a second)
                self.totals.unverified += 1;
                let s = &mut self.slots[i];
                if s.measure_stepped_at.map_or(true, |t| t.elapsed() > Duration::from_secs(60)) {
                    let r = s.reported.rate();
                    s.measure_difficulty = (s.measure_difficulty * 2.0).max(r).min(MAX_MEASURE_DIFFICULTY);
                    s.measure_stepped_at = Some(Instant::now());
                }
                if self.verify_dropped_said.map_or(true, |t| t.elapsed() > Duration::from_secs(60)) {
                    self.verify_dropped_said = Some(Instant::now());
                    let line = format!("{}: results arrive faster than they can be checked; {} dropped unverified so far (the measurement target is raised)", self.slots[i].device.name, self.totals.unverified);
                    self.log("app", &line);
                }
            }
            _ => {}
        }
    }

    /// A result held back by the pace: counted, said once and then every thousandth.
    /// Appends one line of the dev fee's record (economics' ask of 12 October 2026, the 09:00 cut): the file
    /// `<data dir>/receipts/dev-fee-<session start unix>.jsonl`, one line per fee template issued on a node session
    /// and one per share accepted on it, so the one in a hundred reads from Ember's own count beside the chain's
    /// by-address count; a pool session's fee shares are in the receipts file (dev_fee_percent per receipt). A
    /// write that fails is logged once and never stops mining.
    fn fee_record(&mut self, line: &str) {
        if self.fee_record_start == 0 {
            self.fee_record_start = now_unix();
        }
        let dir = self.cfg.data_dir.join("receipts");
        let path = dir.join(format!("dev-fee-{}.jsonl", self.fee_record_start));
        let _ = std::fs::create_dir_all(&dir);
        let ok = std::fs::OpenOptions::new().create(true).append(true).open(&path).and_then(|mut f| {
            use std::io::Write as _;
            writeln!(f, "{line}")
        });
        if ok.is_err() && !self.fee_record_failed {
            self.fee_record_failed = true;
            self.log("app", &format!("the dev fee record could not be written at {}", path.display()));
        }
        if self.totals.dev_fee_record_file.is_empty() && ok.is_ok() {
            self.totals.dev_fee_record_file = path.to_string_lossy().into_owned();
        }
    }

    /// F-110: whether the slot's worker is one of the OpenCL workers that carry the dataset-leak fix (the file's
    /// sha256, hashed once per slot and kept); a worker that is not OpenCL never lifts the pin (the pin is about the
    /// OpenCL dataset build, and an AMD card runs on the OpenCL worker alone).
    fn slot_worker_lifts_f110(&mut self, i: usize) -> bool {
        if let Some(v) = self.slots[i].f110_lift {
            return v;
        }
        let exe = self.slots[i].launcher.exe.clone();
        let stem = exe.file_stem().and_then(|x| x.to_str()).unwrap_or("").to_string();
        let lift = stem.starts_with("igneum-worker-opencl") && match worker::sha256_file(&exe) {
            Ok(h) => F110_FIXED_OPENCL_WORKERS.iter().any(|f| f.eq_ignore_ascii_case(&h)),
            Err(_) => false,
        };
        self.slots[i].f110_lift = Some(lift);
        if lift {
            self.log("app", &format!("{}: the OpenCL worker carries the F-110 fix (the shipped pair); coordinate 1 is open to this card", self.slots[i].device.name));
        }
        lift
    }

    /// F-E23: a job's share target, and since when it has stood.
    fn note_share_target(&mut self, target: &[u8; 32]) {
        match &self.share_target_since {
            Some((t, _)) if t == target => {}
            _ => self.share_target_since = Some((*target, Instant::now())),
        }
    }

    fn not_submitted(&mut self, name: &str, cap: u32) {
        self.totals.not_submitted += 1;
        // F-E23 (E2, 14 October 2026: the warning on a 3060 at step 2 while the step climbed): no warning until the
        // share target has stood TARGET_STOOD_FOR; a climbing step holds results back by design
        let stood = self.share_target_since.as_ref().map_or(Duration::ZERO, |(_, since)| since.elapsed());
        if stood < TARGET_STOOD_FOR {
            if self.totals.not_submitted == 1 {
                self.log("app", &format!("{name}: results beyond {cap} a second are held back while the pool's share target settles (one step a height, about five minutes for a fast card; a warning only if the target stands {} minutes at this rate)", TARGET_STOOD_FOR.as_secs() / 60));
            }
            return;
        }
        if self.totals.not_submitted == 1 || self.totals.not_submitted % 1000 == 0 || !self.target_warned {
            self.target_warned = true;
            // the source's name in the words: a node session read "the pool's share target" on the GPU team's solo
            // session through the public RPC (12 October 2026)
            let solo = self.source.as_ref().map_or(false, |s| s.economics().solo);
            let who = if solo { "the node" } else { "the pool" };
            let line = format!("{name}: {who}'s share target is too low for this rate: results beyond {cap} a second are not submitted ({} so far); {who} should raise the target", self.totals.not_submitted);
            self.log("app", &line);
            let cap_word = if solo { "the node's cap of 256 shares a height" } else { "vardiff" };
            self.notice("warn", &format!("{}'s share target is too low for this rate.", if solo { "The node" } else { "The pool" }), &format!("Results beyond {cap} a second are not submitted; {who} should raise its share target ({cap_word})."));
        }
    }

    /// A result the verifier threads have checked: counted, and submitted when still current.
    fn on_verified(&mut self, i: usize, sh: proto::Share, valid: bool, shares: Option<Vec<u8>>, held: bool) {
        if let Some(s) = self.slots.get_mut(i) {
            s.last_share_at = Some(Instant::now()); // F-562: the worker found a result (valid or not): the silence clock restarts
            if let Some(c) = s.route.coord {
                s.silent_restarts.remove(&c); // a result on the lane clears its silent count: two silences hours apart never refuse it
            }
        }
        let Some(sent) = self.sent.get(&sh.job_id).cloned() else {
            self.totals.cancelled += 1;
            self.say_cancel(i, &sh.job_id, "a job this session no longer holds, after its check");
            return;
        };
        // F-564 (18 October 2026): the still-valid rule of the share path holds after the check too: a verified result for
        // a job of the slot's own coordinate at the live height or one behind goes on to the pool (which answers in the
        // open); only a job the template has left behind is late
        if !sent.tuning && self.slot_template(i).map_or(true, |c| c.job_id != sent.template.job_id && (if self.pool_session_now() { sent.template.coord != c.coord || !self.job_still_retained(&sent.template.job_id) } else { superseded_job(sent.template.coord, sent.template.height, c.coord, c.height) })) {
            // the template moved on while the result was being checked: cancelled work, never submitted; counted
            // apart as late, because a check slower than the chain's template interval is a state to show
            self.totals.cancelled += 1;
            self.totals.verify_late += 1;
            self.say_cancel(i, &sh.job_id, "the template moved on past its height while the result was being checked (late)");
            self.verify_window.push_back((Instant::now(), false));
            // F-271 (the Octominer rig's RX 9060 XT, 16 October 2026): a card whose every result is late for two templates is not
            // mining whatever it reports (901 cancelled, 553 late, nothing submitted for eight minutes): restarted on a
            // fresh job, said once per restart
            if i < self.slots.len() {
                let live = self.slot_template(i).map(|c| c.job_id.clone());
                if late_run_advance(&mut self.slots[i].late_run, live.as_deref()) {
                    let name = self.slots[i].device.name.clone();
                    let line = format!("{name}: every result since the last current one came back late for {LATE_TEMPLATES_RESTART} templates (the job was replaced before each check); restarted on a fresh job (F-271)");
                    self.log("app", &line);
                    self.slots[i].late_run = LateRun::default();
                    self.restart_slot(i, "every result late for two templates (F-271)");
                }
            }
            return;
        }
        self.verify_window.push_back((Instant::now(), true));
        if i < self.slots.len() {
            self.slots[i].late_run = LateRun::default();
        }
        if i >= self.slots.len() {
            return;
        }
        let (Some(mix), Some(hash)) = (proto::hex_decode(&sh.mix), proto::hex_to_32(&sh.hash)) else { return };
        let name = self.slots[i].device.name.clone();
        let now = Instant::now();
        let before_first_valid = !valid && !self.slots[i].doc_valid_seen && self.slots[i].switch_invalids < SWITCH_INVALID_MAX;
        if before_first_valid {
            self.slots[i].switch_invalids += 1;
        }
        if !valid && (before_first_valid || straddles_transition(matches!(self.slots[i].transition, Transition::Current), self.slots[i].transition_done_at, now)) {
            // the GPU team's row (2) of 15 October 2026 (pool-b2 unpinned, the 0.2.16 worker): three invalid results in ten
            // minutes, one at each period transition (the worker's recompile of the period's programme took 14 to
            // 25 s on the contended card), none when pinned without a transition, and the sealer accepting the same
            // worker's shares: a result of the last batch under the old programme carrying the new job's tag, not a
            // hash fault; counted apart, its bytes logged, never toward the coordinate's refusal (F-E56) or a restart
            self.totals.straddled += 1;
            if self.totals.straddled <= 6 {
                let why = if before_first_valid { "before the card's first valid result on this document (the switch)" } else { "across the period transition" };
                let line = format!("{name}: a result {why} failed the reference (counted as straddled, not invalid): height {} header {} nonce {} mix {} hash {} coordinate {:?}", sent.template.height, proto::hex_encode(&sent.template.header), sh.nonce, sh.mix, sh.hash, sent.template.coord);
                self.log("app", &line);
            }
            return;
        }
        if valid && !self.slots[i].doc_valid_seen {
            self.slots[i].doc_valid_seen = true;
            if self.slots[i].switch_invalids > 0 {
                let line = format!("{name}: the first valid result on this document after {} straddled at the switch", self.slots[i].switch_invalids);
                self.log("app", &line);
            }
        }
        if !valid {
            self.totals.invalid += 1;
            let s = &mut self.slots[i];
            s.invalid_total += 1;
            s.invalids.push_back(now);
            while s.invalids.front().map_or(false, |t| now.duration_since(*t) > Duration::from_secs(600)) {
                s.invalids.pop_front();
            }
            if let Some(acc) = s.tune.as_mut() {
                if acc.collecting {
                    acc.invalid += 1;
                }
            }
            let n = s.invalids.len();
            let line = format!("{name}: an invalid share (nonce {}); {n} in ten minutes", sh.nonce);
            self.log("app", &line);
            if n <= 3 {
                // the bytes of the first refusals, for the recompute on another machine (the test pool's coordinate
                // 3, 04:0x UK 13 October 2026: the worker and the reference disagree on one input, unseen)
                let line = format!("{name}: invalid detail: height {} header {} nonce {} mix {} hash {} document {} coordinate {}", sent.template.height, proto::hex_encode(&sent.template.header), sh.nonce, sh.mix, sh.hash, &sent.params_digest[..sent.params_digest.len().min(12)], sent.template.coord.map(|c| c.to_string()).unwrap_or_else(|| "none".into()));
                self.log("app", &line);
            }
            // VectorWork: a coordinate whose results fail the reference on this card (the worker's implementation of
            // that document disagrees with the reference's: the Metal worker on DOC-v11-w256 at 02:56 UK, 13 October
            // 2026) is refused for the card and the router moves on; never a restart, never a closed slot
            let sent_coord = sent.template.coord;
            if self.slots[i].route.refused.contains(&sent_coord.unwrap_or(255)) {
                // a late result of a coordinate already refused for this card: counted, never a second refusal
                self.slots[i].invalids.clear();
                return;
            }
            if n >= 3 && self.slots[i].tune.is_none() {
                if let Some(c) = self.slots[i].route.coord {
                    let s = &mut self.slots[i];
                    s.invalids.clear();
                    if !s.route.refused.contains(&c) {
                        s.route.refused.push(c);
                    }
                    s.route.rates.remove(&c);
                    s.route.probing = false;
                    s.route.coord = None;
                    s.stint_since = None;
                    let line = format!("{name}: coordinate {c}: this card's results on its document fail the reference (three invalid in ten minutes); the coordinate is refused for this card and the router moves on");
                    self.log("app", &line);
                    self.notice("warn", &format!("{name}: coordinate {c} is refused for this card."), "Its worker's results on that coordinate's document fail the reference here; the card mines the other coordinates.");
                    self.route_cards("a refused coordinate");
                    return;
                }
            }
            if n >= 3 && self.slots[i].tune.is_none() {
                let s = &mut self.slots[i];
                s.invalid_episodes.push_back(now);
                while s.invalid_episodes.front().map_or(false, |t| now.duration_since(*t) > Duration::from_secs(3600)) {
                    s.invalid_episodes.pop_front();
                }
                s.invalids.clear();
                if s.invalid_episodes.len() >= 2 {
                    // repeated verification failure (section 10): mining on this device stops; the user restarts it
                    self.close_slot(i, "repeated verification failures");
                    self.slots[i].state = SlotState::Faulted;
                    self.slots[i].message = "stopped: its results failed verification repeatedly (two episodes in an hour); switch the card off and on to try again after checking the card".into();
                    self.notice("bad", &format!("{name} stopped after repeated verification failures."), "Every invalid result is in the log. Mining on it does not resume by itself.");
                } else {
                    let safe = self.safe_params(i);
                    self.slots[i].params = safe;
                    self.restart_slot(i, "three invalid shares in ten minutes; back to the last safe settings");
                }
            }
            return;
        }
        if sent.tuning {
            if let Some(acc) = self.slots[i].tune.as_mut() {
                if acc.collecting {
                    acc.valid += 1;
                }
            }
        } else {
            let s = &mut self.slots[i];
            s.validated.add(sent.difficulty);
            s.last_validated_at = Some(now);
            s.recent_valid.push_back(now);
            while s.recent_valid.front().map_or(false, |t| now.duration_since(*t) > Duration::from_secs(60)) {
                s.recent_valid.pop_front();
            }
            if held {
                // checked for the watch alone; the pace held it back from the pool
                return;
            }
            // aim for about one validated result a second per device: a step at most once a minute (the window is
            // cleared at each step, so a step on every result would run away: the 5090 pod of 11 October halved
            // the target on each validated result while most results were late, to thousands of shares a second),
            // and never below a quarter of what the reported rate says (a result every four seconds at most)
            let n = s.recent_valid.len();
            // on a pool session the eased job target follows the reported rate alone (pool_measure_difficulty, ten
            // results a second): the steps by the verified count doubled the target each minute against those ten,
            // and the validated figure read a tenth of the card on the row pods (12 October 2026, 44c38951 and
            // 81db24e3: 4.3 of 35.2 MH/s, 3.1 of 38.6) while the shares and receipts were whole
            let pool_session = self.source.as_ref().map_or(false, |src| !src.economics().solo) && !sent.template.share.is_some();
            let stepped_recently = pool_session || s.measure_stepped_at.map_or(false, |t| now.duration_since(t) < Duration::from_secs(60));
            let floor = (s.reported.rate() / 4.0).max(1000.0);
            if n > 120 && !stepped_recently {
                s.measure_difficulty = (s.measure_difficulty * 2.0).min(MAX_MEASURE_DIFFICULTY);
                s.recent_valid.clear();
                s.measure_stepped_at = Some(now);
            } else if n < 20 && !stepped_recently && s.opened_at.map_or(false, |t| now.duration_since(t) > Duration::from_secs(90)) && s.measure_difficulty / 2.0 >= floor {
                s.measure_difficulty = (s.measure_difficulty / 2.0).max(floor);
                s.recent_valid.clear();
                s.measure_stepped_at = Some(now);
            }
            self.set_stage("First results checked", "done", "");
            self.set_stage("First work accepted", "active", "");
        }
        // a share at or under the pool's share target (the boundary when solo) is real work: submit it, tuning or not
        if stubhash::meets_target(&hash, &sent.template.share_target) {
            // on the share lane the result is a share whatever target it met: a share never becomes a block
            // (LAUNCH-GUARDS.md 2.9, rule 2.1a); the block search is its own template
            let lane_share = sent.template.share.is_some();
            // a block is a node session's: a pool's job names its share target as the boundary too, so on a pool
            // every result read as a block and bypassed the cap (the mini's first outside session, 12 October
            // 2026: 135 submissions a second, 12,086 refused by the pool's rate throttle in 90 s)
            let solo = self.source.as_ref().map_or(false, |s| s.economics().solo);
            let block = counts_as_block(lane_share, solo, stubhash::meets_target(&hash, &sent.template.boundary));
            // under a keyed document the submission carries the bond's public key and the S shares the check produced
            let keyed = match (shares, self.cfg.bond_secret.as_ref()) {
                (Some(shares), Some(b)) => Some(bond::KeyedProof { pk: b.public(), shares }),
                _ => None,
            };
            let share = Share { job_id: sent.template.job_id.clone(), nonce: sh.nonce, mix, hash, worker_name: self.settings.worker_name.clone(), keyed };
            // F-E5: a (job, nonce) the session already submitted is never sent again (a re-found share is the pool's
            // 'duplicate'; twenty in a minute closed the two-card Windows host's session on the public pool, 13 October 2026)
            if !self.submitted_seen.entry(share.job_id.clone()).or_default().insert(share.nonce) {
                self.totals.cancelled += 1;
                if self.totals.cancelled <= 3 || self.totals.cancelled % 1000 == 0 {
                    let line = format!("{name}: a share re-found on job {} (nonce {}) is not sent again", share.job_id, share.nonce);
                    self.log("app", &line);
                }
                return;
            }
            // the submission cap: a pool whose share target is far below the miner's rate would be flooded (and
            // refuse by rate); a block when solo is never held back
            // a pool session's slot was taken before the check (on_share); a node session's shares are paced here
            if !block && solo && !self.submit_pace.admit(Instant::now(), SUBMIT_CAP_PER_S, Duration::ZERO) {
                self.not_submitted(&name, SUBMIT_CAP_PER_S);
                return;
            }
            self.submitted.insert((share.job_id.clone(), share.nonce), (i, block, lane_share, sent.template.height, self.totals.share_step, router::share_weight(&sent.template.share_target)));
            self.submitted_at.insert((share.job_id.clone(), share.nonce), Instant::now());
            self.last_submit = Some(Instant::now());
            self.totals.submitted += 1;
            if let Some(Err(e)) = self.source.as_mut().map(|s| s.submit(&share)) {
                // F-E30: a share found inside a roll's re-dial window belongs to the old generation's job, which
                // the new generation answers "stale" (the pool team, 14 October 2026); it is counted as stale across
                // the roll, never a fault of the owner's and never a notice
                let rolling = self.source.as_ref().and_then(|s| s.problem()).map_or(false, |p| p.contains(work::RECONNECT_WORDS));
                if rolling || e.contains("the pool thread has gone") {
                    self.totals.stale += 1;
                    *self.totals.share_refused_reasons.entry("stale across the roll (the share was found inside the re-dial)".into()).or_insert(0) += 1;
                } else {
                    self.notice("warn", "Work could not be submitted.", &e);
                }
            }
        }
    }

    /// Outcomes the source reported since the last tick.
    fn on_outcome(&mut self, job_id: String, nonce: u64, outcome: SubmitOutcome) {
        let job_id_for_fee = job_id.clone();
        let Some((i, block, lane_share, share_height, share_step, share_weight)) = self.submitted.remove(&(job_id, nonce)) else { return };
        let name = self.slots.get(i).map(|s| s.device.name.clone()).unwrap_or_default();
        let solo = self.source.as_ref().map_or(false, |s| s.economics().solo);
        // the share lane's own counts: accepted into the node's pool, or refused by the node's reason (the record's
        // columns: shares accepted, refused by reason)
        if lane_share {
            let refused: Option<String> = match &outcome {
                SubmitOutcome::Accepted => None,
                SubmitOutcome::Stale => Some("stale".into()),
                SubmitOutcome::Superseded | SubmitOutcome::Held(_) => None,
                SubmitOutcome::Invalid(w) | SubmitOutcome::Rejected(w) => Some(w.chars().take(64).collect()),
            };
            match refused {
                None if matches!(outcome, SubmitOutcome::Accepted) => {
                    self.totals.shares_accepted += 1;
                    if self.dev_fee_jobs.contains(&job_id_for_fee) {
                        self.totals.dev_fee_shares_accepted += 1;
                        let line = devfee::fee_record_line("share", &job_id_for_fee, 0, Some(nonce), Some("accepted"), self.totals.dev_fee_percent, &self.totals.dev_fee_address);
                        self.fee_record(&line);
                    }
                }
                None => {}
                Some(why) => {
                    self.totals.shares_refused += 1;
                    *self.totals.share_refused_reasons.entry(why).or_insert(0) += 1;
                }
            }
        }
        match outcome {
            SubmitOutcome::Accepted => {
                self.totals.accepted += 1;
                self.last_accepted_at = Some(Instant::now());
                self.not_submitted_at_mark = self.totals.not_submitted;
                if let Some(s) = self.slots.get_mut(i) { s.last_result_at = Some(Instant::now()); if let Some(c) = s.route.coord { let l = s.lane_stats.entry(c).or_default(); l.accepted += 1; l.stint_work += share_weight; l.stint_shares += 1; } }
                if block {
                    self.totals.blocks_found += 1;
                }
                // the scene's share event: the fee session's job carries work::FEE_JOB_PREFIX; the weight is the
                // pool's last receipt's (the same step's weight, the receipt of this share arriving a frame later)
                let weight = self.source.as_mut().map_or(0, |s| s.receipts().last_weight);
                self.recent_shares.push_back(crate::state::ShareEvent { at_unix: now_unix(), height: share_height, step: share_step, weight, job: job_id_for_fee.trim_start_matches(work::FEE_JOB_PREFIX).chars().take(16).collect(), fee: job_id_for_fee.starts_with(work::FEE_JOB_PREFIX) });
                while self.recent_shares.len() > RECENT_SHARES {
                    self.recent_shares.pop_front();
                }
                // a pool's accepted share on a fee template (16.3): the fee's count and its record line (the share
                // lane's own path above does the same for a node session)
                if !lane_share && !solo && self.dev_fee_jobs.contains(&job_id_for_fee) {
                    self.totals.dev_fee_shares_accepted += 1;
                    let line = devfee::fee_record_line("share", &job_id_for_fee, 0, Some(nonce), Some("accepted"), self.totals.dev_fee_percent, &self.totals.dev_fee_address);
                    self.fee_record(&line);
                }
                if !self.first_work_accepted {
                    self.first_work_accepted = true;
                    self.set_stage("First work accepted", "done", "");
                    let _ = self.journal.record("first_work_accepted", if solo { "solo" } else { "pool" });
                    self.notice("info", "First work accepted.", "Your first validated work was accepted. The estimate line below has a cost next to it.");
                }
                let line = format!("{name}: {} accepted (nonce {nonce})", if block { "block" } else { "share" });
                self.log("app", &line);
            }
            SubmitOutcome::Stale => {
                self.totals.stale += 1;
                if self.totals.stale <= 5 || self.totals.stale % 100 == 0 {
                    let line = format!("{name}: stale work (the job had moved on); {} so far", self.totals.stale);
                    self.log("app", &line);
                }
            }
            SubmitOutcome::Superseded => {
                // a solo result beyond the one the chain takes per template: cancelled work, never sent, not a line
                // (the 5090 pod, 11 October: 12,873 such results logged as stale work at a hundred a line)
                self.totals.cancelled += 1;
            }
            SubmitOutcome::Held(case) if case == "rate" => {
                // F-E66: the pool's submit bucket paced this share (its retarget lifts the card off it within seconds);
                // counted as paced, said once per minute at most; the engine's submit rate backs off to the pool's bucket
                // for a minute (the security review's rule: no flood on the pool's refusals, nothing resubmitted)
                self.totals.paced_by_pool += 1;
                let paced_coord = self.sent.get(&job_id_for_fee).map(|s| s.template.coord).unwrap_or_else(|| self.slots.get(i).and_then(|s| s.route.coord));
                self.pool_bucket_caps.insert(paced_coord, (POOL_BUCKET_PER_S, Instant::now() + Duration::from_secs(60)));
                if self.totals.paced_by_pool == 1 || self.paced_said.map_or(true, |t| t.elapsed() > Duration::from_secs(60)) {
                    self.paced_said = Some(Instant::now());
                    let line = format!("{name}: the pool's submit bucket paced a share ({} so far); the pool retargets this session off it", self.totals.paced_by_pool);
                    self.log("app", &line);
                }
            }
            SubmitOutcome::Held(case) => {
                // the pool's admission bound (F-50): held, never rejected; the first and every hundredth are logged
                self.totals.held += 1;
                self.totals.held_case = case.clone();
                if self.totals.held <= 1 || self.totals.held % 100 == 0 {
                    let line = format!("{name}: the pool held the share before acceptance ({case}); {} so far; it clears as the chain carries", self.totals.held);
                    self.log("app", &line);
                }
            }
            SubmitOutcome::Invalid(why) => {
                self.totals.source_invalid += 1;
                if self.totals.source_invalid <= 5 || self.totals.source_invalid % 100 == 0 {
                    let line = format!("{name}: the source calls this work invalid ({why}); {} so far; the reference and the source's implementation disagree", self.totals.source_invalid);
                    self.log("app", &line);
                }
                if self.totals.source_invalid == 5 {
                    for s in self.slots.iter_mut().filter(|s| s.proc.is_some()) {
                        s.message = "the pool refuses this card's work as invalid; the two disagree on the chain or the document".into();
                    }
                    self.notice("bad", "The source refuses this work as invalid.", "The worker's proofs pass the reference here and fail at the source: the two implementations disagree (gate G0). Nothing to tune; report it.");
                }
            }
            SubmitOutcome::Rejected(why) => {
                self.totals.rejected += 1;
                let key = why.split(|c: char| c == ':' || c == '(' ).next().unwrap_or("").trim().to_string();
                let key = if key.is_empty() { "refused".to_string() } else { key.chars().take(32).collect() };
                *self.totals.reject_reasons.entry(key).or_insert(0) += 1;
                if self.totals.rejected <= 5 || self.totals.rejected % 100 == 0 {
                    let line = format!("{name}: work rejected: {why}; {} so far", self.totals.rejected);
                    self.log("app", &line);
                }
                if why == "unauthorised" || why == "bad_nonce" {
                    self.notice("warn", &format!("The pool rejected work: {why}."), "Check the payout address and worker name under Settings.");
                }
            }
        }
    }

    fn estimate(&mut self) -> (Option<Estimate>, Vec<Line>) {
        let Some(src) = self.source.as_ref() else { return (None, Vec::new()) };
        let e = src.economics();
        let inputs = Inputs {
            rate_hps: self.totals.validated_hps,
            reported_hps: self.totals.reported_hps,
            dev_fee_percent: self.cfg.dev_fee.as_ref().ok().map(|f| f.percent),
            balance_ign: self.chain_balance_ign,
            chain: self.chain_figures.clone(),
            ledger: self.ledger_paid.clone(),
            difficulty: e.difficulty,
            block_interval_s: e.block_interval_s,
            // the source reports the miner's part after the protocol allocation (igneum_blockReward.miner)
            subsidy_ign: e.subsidy_ign,
            miner_share: 1.0,
            fees_ign: e.fees_ign,
            pool_fee: e.pool_fee,
            fund_ign: e.fund_ign,
            no_pool_fee: self.settings.no_pool_fee,
            solo: e.solo,
            wall_watts: self.settings.costs.wall_watts,
            device_watts: self.totals.device_watts,
            host_allowance_w: self.host_allowance(),
            tariff: self.settings.tariff.clone(),
            price_per_ign: self.settings.costs.price_per_ign,
            other_per_day: self.settings.costs.other_per_day,
            currency: self.settings.costs.currency.clone(),
        };
        let est = economics::estimate(&inputs);
        let lines = economics::lines(&inputs, &est);
        (Some(est), lines)
    }

    /// The relation the machine mines, named from the served document (the founder's order of 14:17 UK, 13 October
    /// 2026: the relation named on the Mine and Settings pages): "IgneumHash version N" with the routed coordinate's
    /// name when the pool serves coordinates; empty until a document is held.
    fn relation_word(&self) -> String {
        let routed = self.slots.iter().filter(|s| s.enabled).find_map(|s| s.route.coord);
        let (doc, name) = match routed.and_then(|c| self.coords.get(&c)) {
            Some(ctx) => (ctx.doc.clone(), ctx.template.coord_name.clone()),
            None => match self.coords.values().next() {
                Some(ctx) => (ctx.doc.clone(), ctx.template.coord_name.clone()),
                None => (self.current.as_ref().map(|j| j.params.clone()).unwrap_or(serde_json::Value::Null), self.current.as_ref().and_then(|j| j.coord_name.clone())),
            },
        };
        let version = doc.get("version").and_then(|v| v.as_u64().or_else(|| v.as_str().and_then(|s| s.parse().ok()))).unwrap_or(0);
        if version == 0 {
            return String::new();
        }
        // the lane by its word, never the registry's name (the project's read of the 0.2.21-2 Settings sheet, 16 October 2026:
        // "IgneumHash version 15 (bandwidth-r19)" put a registry name in public text)
        let lane = lane_word(Some(version), routed);
        let _ = name;
        if lane.is_empty() {
            format!("IgneumHash version {version}")
        } else {
            format!("IgneumHash version {version}, {lane}")
        }
    }

    fn slot_views(&mut self) -> Vec<SlotView> {
        let now = now_unix();
        let algo = self.source.as_ref().map(|s| s.algo()).unwrap_or_else(|| self.network_algo());
        let settings = self.settings.clone();
        let store = self.tune_store.clone();
        let versions: BTreeMap<u8, u64> = self.coords.iter().filter_map(|(c, x)| x.doc.get("version").and_then(|v| v.as_u64()).map(|v| (*c, v))).collect();
        self.slots.iter_mut().map(|s| s.view(&settings, &store, now, &algo, &versions)).collect()
    }

    fn snapshot(&mut self) -> Snapshot {
        let (estimate, estimate_lines) = self.estimate();
        let devices = self.slot_views();
        let source = self.source.as_ref().map(|s| {
            let e = s.economics();
            SourceView { name: s.name(), algo: s.algo(), connected: s.connected(), problem: s.problem(), solo: e.solo, difficulty: e.difficulty, current_job: self.current.as_ref().map(|j| j.job_id.clone()), height: self.current.as_ref().map_or(0, |j| j.height), chain_id: self.current.as_ref().map_or(0, |j| j.chain_id), relation: self.relation_word(), waiting_for_work_s: self.waiting_for_work_s(Instant::now()) }
        });
        let stub = devices.iter().any(|d| d.backend == "stub") || matches!(self.settings.work_source, WorkSourceSetting::Stub) || self.cfg.launchers.iter().all(|l| l.exe.to_string_lossy().contains("igneum-worker-stub"));
        let over = self.settings.power_budget_w.and_then(|b| if self.totals.device_watts > b { Some(self.totals.device_watts - b) } else { None });
        Snapshot {
            app: self.cfg.app_name.clone(),
            version: self.cfg.version.clone(),
            stub,
            mining: self.mining,
            memory_pressure: self.memory_pressure.map(|l| l.word().to_string()),
            paused_for: self.paused_for,
            mode: self.settings.mode,
            limits: self.settings.limits.clone(),
            devices,
            totals: self.totals.clone(),
            source,
            estimate,
            estimate_lines,
            update_offer: self.update_offer.clone(),
            stages: self.stages.clone(),
            first_work_accepted: self.first_work_accepted,
            notices: self.notices.clone(),
            settings: self.settings.clone(),
            log_lines: self.log.len(),
            tune_running: self.tune_running,
            over_power_budget_w: over,
            pools_status: match pools::PUBLISHER_KEY {
                Some(_) => "signed list".into(),
                None => "no pool list is published yet; pools you add yourself are shown".into(),
            },
            transitions: self.transitions.iter().rev().take(32).rev().cloned().collect(),
            recent_shares: self.recent_shares.iter().cloned().collect(),
            params_digest: self.params_digest.clone(),
            tick_age_ms: self.tick_beat.elapsed().as_millis() as u64,
            tick_fault: self.tick_fault.clone(),
            launch_errors: self.launch_errors.clone(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn f_e37_a_dial_never_carries_an_empty_algorithm() {
        // the re-dial after no work and after unanswered submissions: the old session's algorithm rides the hint
        assert_eq!(resolve_dial_algo(Some("igneumhash".into()), None, None, || "stub".into()), "igneumhash");
        // the backup switch while a source is held: the held source's
        assert_eq!(resolve_dial_algo(None, Some("igneumhash".into()), None, || "stub".into()), "igneumhash");
        // the source torn down and no hint (the 07:50 UK shape): the settings' choice, else the workers', never ""
        assert_eq!(resolve_dial_algo(Some(String::new()), Some(String::new()), Some("igneumhash".into()), || "stub".into()), "igneumhash");
        assert_eq!(resolve_dial_algo(None, None, None, || "igneumhash".into()), "igneumhash");
    }

    #[test]
    fn f_e33_a_source_the_supervisor_builds_after_start_signs_with_the_same_key() {
        let dir = std::env::temp_dir().join(format!("ember-f-e33-{}", std::process::id()));
        let _ = std::fs::create_dir_all(&dir);
        let mut cfg = Config::new("ember", "test", &dir, dir.clone());
        cfg.key_path = Some(dir.join("miner.key"));
        let sup = Supervisor::new(cfg);
        let mut core = lock(&sup.inner);
        let mut fresh: Box<dyn work::WorkSource> = Box::new(work::PoolSource::new("127.0.0.1", 1, false, "test pool", "igneumhash"));
        assert!(!fresh.signs(), "a fresh pool source has no signer");
        core.attach_signer(&mut fresh);
        assert!(fresh.signs(), "the supervisor attaches its key to every source it builds (the re-dial's authorize is signed)");
        let mut again: Box<dyn work::WorkSource> = Box::new(work::PoolSource::new("127.0.0.1", 1, false, "test pool", "igneumhash"));
        core.attach_signer(&mut again);
        assert!(again.signs());
        assert!(core.signer.is_some(), "the key is kept on the core after the first attach");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_pool_result_is_never_a_block_and_the_cap_holds() {
        // the mini's first outside session (12 October 2026): the pool's boundary equals its share target, so every
        // result met it and bypassed the cap as "a block when solo"; a block is a node session's alone
        assert!(counts_as_block(false, true, true));
        assert!(!counts_as_block(false, false, true));
        assert!(!counts_as_block(true, true, true));
        assert!(!counts_as_block(false, true, false));
        assert!(POOL_SUBMIT_CAP_PER_S <= SUBMIT_CAP_PER_S);
        assert_eq!(SUBMIT_CAP_PER_S, 12, "256 a height over 20 s blocks, the node's per-address cap");
        assert_eq!(POOL_SUBMIT_CAP_PER_S, 12);
    }

    #[test]
    fn an_idle_slot_under_a_running_layer_is_reopened_after_a_minute() {
        // the mini on the public pool (12 October 2026, 03:03Z): the slots rebuilt while mining stood, the card
        // "waiting" with no worker for an hour; the tick reopens such a slot after IDLE_REOPEN_AFTER
        assert_eq!(IDLE_REOPEN_AFTER, Duration::from_secs(60));
        assert!(IDLE_REOPEN_AFTER > Duration::from_secs(30), "longer than a start or a restart in flight");
        let t = Totals::default();
        assert_eq!(t.slot_reopens, 0);
    }

    #[test]
    fn a_pool_session_spaces_its_shares_and_keeps_the_cap() {
        // an RTX 5090 on the public pool (12 October 2026): twelve shares in one burst at a tick read as a flood
        let t0 = Instant::now();
        let mut p = SubmitPace::default();
        assert!(p.admit(t0, 12, POOL_SUBMIT_SPACING));
        assert!(!p.admit(t0, 12, POOL_SUBMIT_SPACING), "the second share in the same instant waits");
        assert!(!p.admit(t0 + Duration::from_millis(40), 12, POOL_SUBMIT_SPACING));
        assert!(p.admit(t0 + Duration::from_millis(90), 12, POOL_SUBMIT_SPACING));
        // the cap within the second holds even when spaced: twenty attempts 90 ms apart span 1.71 s, so a cap of
        // five admits five in the first window (0 to 990 ms) and five in the second (from 1,080 ms)
        let mut q = SubmitPace::default();
        let mut admitted = 0;
        for k in 0..20u64 {
            if q.admit(t0 + Duration::from_millis(k * 90), 5, POOL_SUBMIT_SPACING) {
                admitted += 1;
            }
        }
        assert_eq!(admitted, 10, "five in each of the two windows, the rest held");
        assert!(!q.admit(t0 + Duration::from_millis(2000), 5, POOL_SUBMIT_SPACING), "the second window (from 1,080 ms) is full at 2,000 ms");
        assert!(q.admit(t0 + Duration::from_millis(2100), 5, POOL_SUBMIT_SPACING), "the third window admits again");
        // a node session: no spacing, the node's per-address pace of 12
        let mut n = SubmitPace::default();
        assert!((0..25).filter(|_| n.admit(t0, SUBMIT_CAP_PER_S, Duration::ZERO)).count() == 12);
    }

    #[test]
    fn a_pace_on_one_session_leaves_the_other_sessions_results_flowing() {
        // F-271 (the Octominer rig's RX 9060 XT, 16 October 2026): the programme lane's session (coordinate 4) was paced by the pool's
        // bucket; the engine capped every session for a minute and the bandwidth lane (coordinate 3) lost 12,512 results
        let t0 = Instant::now();
        let mut caps: std::collections::BTreeMap<Option<u8>, (u32, Instant)> = std::collections::BTreeMap::new();
        caps.insert(Some(4u8), (POOL_BUCKET_PER_S, t0 + Duration::from_secs(60)));
        assert_eq!(paced_cap_for(&caps, Some(4), t0 + Duration::from_secs(1)), Some(POOL_BUCKET_PER_S), "the paced session is capped");
        assert_eq!(paced_cap_for(&caps, Some(3), t0 + Duration::from_secs(1)), None, "another session's results are not");
        assert_eq!(paced_cap_for(&caps, None, t0 + Duration::from_secs(1)), None, "a job without a coordinate is not");
        assert_eq!(paced_cap_for(&caps, Some(4), t0 + Duration::from_secs(61)), None, "the minute passed: the cap is off");
    }

    #[test]
    fn every_result_late_for_two_templates_restarts_the_slot_and_a_current_result_clears_it() {
        // F-271: 553 late results across eight minutes of templates with nothing current between
        let mut run = LateRun::default();
        assert!(!late_run_advance(&mut run, Some("t1")), "the first late result starts the run");
        assert!(!late_run_advance(&mut run, Some("t1")), "more late results on the same template do not advance it");
        assert!(!late_run_advance(&mut run, Some("t2")), "one template outlived");
        assert!(late_run_advance(&mut run, Some("t3")), "two templates outlived: the slot is restarted");
        let mut again = LateRun::default();
        assert!(!late_run_advance(&mut again, Some("t1")));
        assert!(!late_run_advance(&mut again, Some("t2")));
        again = LateRun::default();   // a current result resets the run (on_verified's current branch)
        assert!(!late_run_advance(&mut again, Some("t3")), "after a current result the count starts again");
        assert!(!late_run_advance(&mut again, None), "no live template: nothing counted");
    }

    #[test]
    fn a_retired_coordinate_leaves_the_table_for_good_and_a_withdrawn_one_too() {
        // F-290 (the 2,860 succession, 16 October 2026): coordinate 4 retired from 2860 with 7 its successor; the router
        // restored 4 as "empty" at head 2875; the pool sent no job on 3 after the switch while 7's kept coming
        let now = Instant::now();
        let mut retired_from = BTreeMap::new();
        retired_from.insert(4u8, 2860u64);
        let mut issued = BTreeMap::new();
        issued.insert(4u8, now - Duration::from_secs(5));
        issued.insert(7u8, now - Duration::from_secs(5));
        issued.insert(3u8, now - Duration::from_secs(200));
        let r = retired_coords(&retired_from, &issued, 2875, now);
        assert!(r.contains(&4), "retired from 2860 at head 2875");
        assert!(!r.contains(&7), "the successor is live");
        assert!(r.contains(&3), "no job for 200 s while 7's is 5 s old: withdrawn by the pool");
        assert!(!retired_coords(&retired_from, &issued, 2859, now).contains(&4), "before its retirement height it is live");
        let mut quiet = BTreeMap::new();
        quiet.insert(3u8, now - Duration::from_secs(200));
        quiet.insert(7u8, now - Duration::from_secs(150));
        assert!(retired_coords(&BTreeMap::new(), &quiet, 2875, now).is_empty(), "every coordinate stale alike is a quiet pool, not a retirement");
    }

    #[test]
    fn a_refusal_lifts_on_a_new_document_and_survives_an_anchor_change() {
        // the first desktop's 5090 (16 October 2026): the raster coordinate, refused for want of its worker in the Windows
        // install, was probed again after every period anchor change (the same document re-registered), the card's worker
        // closed each time
        assert!(refusal_lifts(None, "d1"), "the first registration lifts nothing that stands");
        assert!(refusal_lifts(Some("d1"), "d2"), "a new document lifts the refusal");
        assert!(!refusal_lifts(Some("d1"), "d1"), "the same document under a new anchor keeps it");
    }

    #[test]
    fn a_base_worker_never_opens_on_a_document_bound_to_another_worker() {
        // F-301's engine half: the source's lowest coordinate after the 2,860 succession is 5 (version 18, the raster
        // document, the GL worker's); the OpenCL base worker opens on 6 (version 15), the GL lane worker on 5 as routed
        let v = vec![(5u8, Some(18u64)), (6, Some(15)), (7, Some(21))];
        assert_eq!(first_coord_for_base(&v, None, true), Some(6), "the lowest coordinate the base worker serves");
        // F-478: a bound worker opens on a coordinate of its own version, never the lowest served
        assert_eq!(first_coord_for_worker(&v, None, Some(21)), Some(7), "the version 21 worker reopens on the programme lane, not the raster lane 5");
        assert_eq!(first_coord_for_worker(&v, Some(7), Some(21)), Some(7));
        assert_eq!(first_coord_for_worker(&v, Some(5), Some(21)), Some(7), "a routed lane of another version is not taken");
        assert_eq!(first_coord_for_worker(&v, None, Some(18)), Some(5));
        assert_eq!(first_coord_for_worker(&v, None, Some(14)), None, "no served coordinate of its version: the caller reverts to the base worker");
        assert_eq!(first_coord_for_worker(&v, None, None), Some(6), "the base worker's rule unchanged");
        let none = |_c: u8, _v: Option<u64>| false;
        assert_eq!(coords_without_a_worker(&v, "igneum-worker-cuda", &none), vec![5], "the raster lane without its worker is no card's candidate");
        let all = |_c: u8, _v: Option<u64>| true;
        assert!(coords_without_a_worker(&v, "igneum-worker-cuda", &all).is_empty());
        assert_eq!(first_coord_for_base(&v, Some(5), false), Some(5), "a lane worker opens on its routed coordinate");
        assert_eq!(first_coord_for_base(&v, Some(7), true), Some(7), "a routed base worker keeps its coordinate");
        assert_eq!(first_coord_for_base(&[(5u8, Some(18u64))], None, true), None, "only a bound-only document served: a base worker has nothing to open on");
        assert!(base_open_waits(&[], None, true, Some(18)), "no coordinate yet and a raster session document: the base worker waits");
        assert!(!base_open_waits(&[], None, true, Some(21)), "a programme session document opens at once");
        assert!(base_open_waits(&[(5u8, Some(18u64))], None, true, Some(18)), "only the raster coordinate registered: it waits");
        assert!(!base_open_waits(&[(5u8, Some(18u64)), (6, Some(15))], None, true, Some(18)), "a coordinate it serves registered: it opens on 6");
        assert!(!base_open_waits(&[], Some(5), false, Some(18)), "a lane worker opens on its own document");
        assert_eq!(first_coord_for_base(&[], None, true), None);
        let before = vec![(2u8, Some(18u64)), (3, Some(15)), (4, Some(21))];
        assert_eq!(first_coord_for_base(&before, None, true), Some(3), "before the succession the same rule picks 3, which is what the card mined");
    }

    #[test]
    fn the_pools_pay_list_drops_every_coordinate_it_does_not_pay() {
        // 0.1.76 (F-290): the public test network's list [5, 6, 7] after the 2,860 succession against the six held
        assert_eq!(unpaid_coords(&[2, 3, 4, 5, 6, 7], &[5, 6, 7]), vec![2, 3, 4]);
        assert!(unpaid_coords(&[5, 6, 7], &[5, 6, 7]).is_empty());
        assert_eq!(unpaid_coords(&[2, 3, 4], &[2, 3, 4]), Vec::<u8>::new(), "before the succession the list pays them");
    }

    #[test]
    fn the_verify_queue_drops_a_measurement_result_that_would_be_checked_late() {
        // the second Octominer rig, 16 October 2026: 136 ms a check on two threads, a flood of results at start; at a
        // 12 s block (the bound 6 s) the 89th queued result would be checked after the template moved
        assert!(!verify_wait_exceeds(10, 136_000, 2, 6_000), "ten deep: 680 ms, checked in time");
        assert!(verify_wait_exceeds(100, 136_000, 2, 6_000), "a hundred deep: 6.8 s, past the bound");
        assert!(!verify_wait_exceeds(100, 136_000, 8, 6_000), "eight threads: 1.7 s, in time");
        assert!(!verify_wait_exceeds(500, 0, 2, 6_000), "no check measured yet: nothing dropped");
        assert_eq!(verify_threads_for(1), 2);
        assert_eq!(verify_threads_for(6), 6);
        assert_eq!(verify_threads_for(32), 8);
    }

    #[test]
    fn the_dev_fee_lines_survive_a_session_reset() {
        // the mini's first mining minute (11 October 2026): the start's Totals::default() wiped the fee's lines
        let fee = Ok(devfee::DevFee { percent: 1, address: [0x11u8; 20], address_hex: "0x1111111111111111111111111111111111111111".into(), source: "m".into(), manifest_sha256: "ab".repeat(32) });
        let mut t = Totals::default();
        fee_lines_into(&mut t, &fee);
        assert_eq!(t.dev_fee_percent, 1);
        assert!(t.dev_fee_words.contains("one template in 100"));
        t.shares_accepted = 9;
        t = Totals::default();
        assert_eq!(t.dev_fee_percent, 0);
        fee_lines_into(&mut t, &fee);
        assert_eq!(t.dev_fee_percent, 1);
        assert_eq!(t.dev_fee_address, "0x1111111111111111111111111111111111111111");
        assert_eq!(t.shares_accepted, 0);
        let off: Result<devfee::DevFee, devfee::Off> = Err(devfee::Off::ByOperator);
        fee_lines_into(&mut t, &off);
        assert_eq!(t.dev_fee_percent, 0);
        assert!(t.dev_fee_words.contains("--dev-fee 0"));
    }

    #[test]
    fn the_lab_switch_paces_at_the_chains_share_target_alone() {
        // the devnet8 stop-gap (the project's order of 11 October 2026): under the switch a share-lane job's target is
        // the chain's share target whether easier or harder than the measurement target; off the switch the
        // pacing rule; off the lane the switch changes nothing; the word names the pacing for the record
        let measure = stubhash::target_for_difficulty(8_000_000.0);
        let easy = stubhash::target_for_difficulty(340.0);
        let hard = stubhash::target_for_difficulty(1e12);
        assert_eq!(job_target_under(&measure, &easy, true, true), easy);
        assert_eq!(job_target_under(&measure, &hard, true, true), hard);
        assert_eq!(job_target_under(&measure, &easy, true, false), measure);
        assert_eq!(job_target_under(&measure, &easy, false, true), job_target_for(&measure, &easy, false));
        assert_eq!(job_target_under(&measure, &hard, false, true), job_target_for(&measure, &hard, false));
        assert!(share_pacing_word(true).starts_with("chain-target (lab"));
        assert!(share_pacing_word(false).starts_with("measurement"));
        // a pool session (an RTX 4090 on the public pool, 12 October 2026): the measurement difficulty eased to ten
        // results a second, and the job target that eased measurement target whatever the pool's share target
        assert_eq!(pool_measure_difficulty(50_000.0), 5_000.0);
        assert_eq!(pool_measure_difficulty(5.0), 1.0, "never under one hash");
        let eased = stubhash::target_for_difficulty(pool_measure_difficulty(50_000.0));
        assert!(eased > measure, "an eased target is easier (larger)");
        assert_eq!(job_target_under(&eased, &easy, true, false), eased);
        assert_eq!(job_target_under(&eased, &hard, true, false), eased);
        // the boundary the worker sees: the job target on a pool session (an RTX 5090, 12 October 2026: 234,554
        // results reported past the eased target under the pool's easy boundary), the chain's on a node session
        assert_eq!(job_boundary_for(true, &easy, &eased), eased);
        assert_eq!(job_boundary_for(false, &easy, &eased), easy);
    }

    #[test]
    fn a_job_names_its_card_by_fingerprint_and_refuses_an_absent_one() {
        // the founder's word of 11 October 2026: after the enclosure moves the 5080's job on the first Windows machine and the 4070's
        // row 08 on the second Windows machine both ran on that PC's 5090; a job binds by fingerprint and an absent card refuses the
        // start naming the fingerprint and the cards seen; the index form is refused; nothing falls to device 0
        let seen = vec![
            ("aaaa5090".to_string(), "NVIDIA GeForce RTX 5090".to_string(), 0u32),
            ("7367917a4070".to_string(), "NVIDIA GeForce RTX 4070".to_string(), 1u32),
        ];
        assert_eq!(devices_named_among(&["7367917a4070".to_string()], &seen), Ok(vec!["7367917a4070".to_string()]));
        assert_eq!(devices_named_among(&["7367917a4070".to_string(), "aaaa5090".to_string()], &seen), Ok(vec!["aaaa5090".to_string(), "7367917a4070".to_string()]));
        let absent = devices_named_among(&["bbbb5080".to_string()], &seen).unwrap_err();
        assert!(absent.contains("bbbb5080") && absent.contains("nothing starts") && absent.contains("RTX 5090 (index 0)") && absent.contains("RTX 4070 (index 1)"), "{absent}");
        // one absent among two named: the whole start refuses, not the found one alone
        let partial = devices_named_among(&["7367917a4070".to_string(), "bbbb5080".to_string()], &seen).unwrap_err();
        assert!(partial.contains("bbbb5080"), "{partial}");
        let index = devices_named_among(&["0".to_string()], &seen).unwrap_err();
        assert!(index.contains("not an index"), "{index}");
        let none = devices_named_among(&["7367917a4070".to_string()], &[]).unwrap_err();
        assert!(none.contains("the cards seen: none"), "{none}");
        assert!(devices_named_among(&[" ".to_string()], &seen).is_err());
    }

    #[test]
    fn a_keyed_job_goes_only_to_a_worker_on_this_machine() {
        // security's rule (SECURITY-GAPS.md, the project's order of 11 October 2026): the bond's secret never crosses a
        // plain socket; a worker on another machine is left off under a keyed document with the reason, a local
        // worker without the extension likewise, and the secret rides a job only under a keyed document to a local
        // worker with the extension; the share lane and the open seat (unkeyed documents) never carry it
        let pipes = Transport::LocalPipes;
        let lo = Transport::Loopback("127.0.0.1:4201".into());
        let far = Transport::Remote("10.0.0.9:4201".into());
        assert!(pipes.is_local() && lo.is_local() && !far.is_local());
        assert_eq!(keyed_refusal(true, &pipes), None);
        assert_eq!(keyed_refusal(true, &lo), None);
        let why = keyed_refusal(true, &far).expect("a remote worker is refused the keyed job");
        assert!(why.starts_with("keyed sealing runs only on this machine's cards"), "{why}");
        assert!(why.contains("another machine, over 10.0.0.9:4201"), "{why}");
        assert!(keyed_refusal(false, &pipes).expect("no extension").contains("no keyed-job extension"));
        assert!(job_takes_the_secret(true, true, &pipes));
        assert!(job_takes_the_secret(true, true, &lo));
        assert!(!job_takes_the_secret(true, true, &far), "never over a plain socket");
        assert!(!job_takes_the_secret(true, false, &pipes));
        assert!(!job_takes_the_secret(false, true, &pipes), "an unkeyed document (the share lane, the open seat) carries no secret");
        assert!(!job_takes_the_secret(false, true, &far));
    }

    #[test]
    fn the_share_lane_paces_the_job_target_to_the_measurement_target() {
        // the 4070 row on devnet7 (11 October 2026): D 54k made the share target a hash in 340, and the worker's
        // reporting, the 20 a second cap and the verifier decided the count; under the lane the job target is the
        // measurement target whether the share target is easier (a dev chain) or harder (launch), and off the lane
        // the easier of the two stands as before
        let measure = stubhash::target_for_difficulty(8_000_000.0);
        let easy = stubhash::target_for_difficulty(340.0);
        let hard = stubhash::target_for_difficulty(1e12);
        assert!(easy > measure && hard < measure, "targets are big-endian: a larger target is easier");
        assert_eq!(job_target_for(&measure, &easy, true), measure);
        assert_eq!(job_target_for(&measure, &hard, true), measure);
        assert_eq!(job_target_for(&measure, &easy, false), easy);
        assert_eq!(job_target_for(&measure, &hard, false), measure);
    }

    #[test]
    fn rate_meter_means_over_its_window() {
        let mut m = RateMeter::new(Duration::from_secs(600));
        assert_eq!(m.rate(), 0.0);
        m.add(1000.0);
        m.add(1000.0);
        let r = m.rate();
        assert!((r - 2000.0).abs() < 1.0, "{r}");
        m.reset();
        assert_eq!(m.rate(), 0.0);
    }

    #[test]
    fn tune_acc_measurement() {
        let mut a = TuneAcc::new();
        a.collecting = true;
        a.difficulty = 1000.0;
        a.valid = 450;
        a.note_telemetry(&Telemetry { power_w: Some(100.0), temp_c: Some(60.0), throttle: vec!["thermal".into()], ..Default::default() });
        a.note_telemetry(&Telemetry { power_w: Some(120.0), temp_c: Some(70.0), ..Default::default() });
        let m = a.measurement(45.0);
        assert!((m.rate_hps - 10_000.0).abs() < 1e-6);
        assert_eq!(m.power_w, Some(110.0));
        assert_eq!(m.temp_max_c, Some(70.0));
        assert!((m.throttle_frac - 0.5).abs() < 1e-9);
    }

    #[test]
    fn a_supervisor_without_workers_refuses_cleanly() {
        let dir = std::env::temp_dir().join(format!("igneum-sup-test-{}", std::process::id()));
        let cfg = Config::new("Ember", "0.0.0", &dir.join("nowhere"), dir.clone());
        let s = Supervisor::new(cfg);
        assert!(s.inventory().is_err());
        assert!(s.start().is_err());
        let snap = s.snapshot();
        assert_eq!(snap.mining, MiningState::Idle);
        assert!(s.update_settings(|st| {
            st.payout_address = Some("0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed".into());
            st.work_source = WorkSourceSetting::Stub;
            Ok(())
        })
        .is_ok());
        assert!(s.update_settings(|st| {
            st.payout_address = Some("nope".into());
            Ok(())
        })
        .is_err());
        assert_eq!(s.settings().payout_address.as_deref(), Some("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"));
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// Security's F-21: a template naming another parameter document than the session's must change the
    /// document every result is checked against (and every worker runs), and one naming a document the
    /// reference refuses must be refused, never mined on the old document. Without take_template, the digest
    /// stays the session's and the template is taken as it is.

    #[test]
    fn the_relation_line_names_the_lane_by_its_word_never_the_registry_name() {
        let dir = std::env::temp_dir().join(format!("igneum-sup-relation-{}", std::process::id()));
        let cfg = Config::new("Ember", "0.0.0", &dir.join("nowhere"), dir.clone());
        let s = Supervisor::new(cfg);
        let mut c = lock(&s.inner);
        assert_eq!(c.relation_word(), "");
        c.current = Some(JobTemplate { job_id: "t".into(), height: 10, header: [7u8; 32], boundary: [0xff; 32], share_target: [0xff; 32], nonce_start: 0, nonce_count: 10, issued: Instant::now(), template: "t".into(), epoch: None, params: serde_json::json!({"version": 15, "dataset_mib": 64}), params_digest: None, next_params: None, keyed_rounds: 0, bond_key: None, share: None, coord: None, coord_difficulty: None, coord_last_height: None, paid_coords: None, lane_worth: None, lane_worth_unknown: false, lane_workers: None, subsidy_ign: None, program_anchor: None, anchor_height: None, chain_id: 0, coord_name: Some("bandwidth-r19".into()) });
        assert_eq!(c.relation_word(), "IgneumHash version 15, the bandwidth lane");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_coordinate_template_registers_its_context_off_the_tick() {
        let dir = std::env::temp_dir().join(format!("igneum-sup-coord-build-{}", std::process::id()));
        let cfg = Config::new("Ember", "0.0.0", &dir.join("nowhere"), dir.clone());
        let s = Supervisor::new(cfg);
        let mut c = lock(&s.inner);
        let base = serde_json::json!({"version": 1, "dataset_mib": 64});
        c.source = Some(Box::new(work::SoloSource::new("http://127.0.0.1:1", "igneumhash").unwrap()));
        c.algo_params = base.clone();
        let mk = |height: u64, params: serde_json::Value| JobTemplate { job_id: format!("t{height}"), height, header: [7u8; 32], boundary: [0xff; 32], share_target: [0xff; 32], nonce_start: 0, nonce_count: u64::MAX, issued: Instant::now(), template: format!("{height}"), epoch: None, params, params_digest: None, next_params: None, keyed_rounds: 0, bond_key: None, share: None, coord: Some(2), coord_difficulty: Some(1.0), coord_last_height: None, paid_coords: None, lane_worth: None, lane_worth_unknown: false, lane_workers: None, subsidy_ign: None, program_anchor: None, anchor_height: None, chain_id: 0, coord_name: None };
        // the template is not taken on the tick: its verifier builds on its own thread
        assert!(!c.take_template(mk(10, base.clone())));
        assert!(!c.coords.contains_key(&2));
        assert!(c.coord_builds.contains_key(&2), "the build is recorded");
        // a newer template on the same document waits on the same build (no second thread)
        assert!(!c.take_template(mk(11, base.clone())));
        assert_eq!(c.coord_builds.get(&2).map(|b| b.job.height), Some(11));
        // the build reports: the context is registered with the newest template and it is current
        let t0 = Instant::now();
        while !c.coords.contains_key(&2) && t0.elapsed() < Duration::from_secs(60) {
            c.drain_coord_builds();
            std::thread::sleep(Duration::from_millis(50));
        }
        assert!(c.coords.contains_key(&2), "the context is registered once the build reports");
        assert!(c.coord_builds.is_empty());
        assert_eq!(c.coords.get(&2).map(|x| x.template.height), Some(11));
        assert_eq!(c.current.as_ref().map(|j| j.height), Some(11));
        // the same document again: taken on the tick, nothing rebuilt
        assert!(c.take_template(mk(12, base.clone())));
        assert!(c.coord_builds.is_empty());
        // a document the reference refuses: refused on the tick, no build
        assert!(!c.take_template(mk(13, serde_json::json!({"dataset_mib": 64}))));
        assert!(c.coord_builds.is_empty());
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    #[cfg(feature = "ignpow")]
    fn a_template_naming_a_new_document_changes_the_document_or_is_refused() {
        let dir = std::env::temp_dir().join(format!("igneum-sup-f21-{}", std::process::id()));
        let cfg = Config::new("Ember", "0.0.0", &dir.join("nowhere"), dir.clone());
        let s = Supervisor::new(cfg);
        let mut c = lock(&s.inner);
        let base = serde_json::json!({"version": 1, "dataset_mib": 64});
        c.source = Some(Box::new(work::SoloSource::new("http://127.0.0.1:1", "igneumhash").unwrap()));
        c.verifier = Some(Arc::from(verify::verifier_for_params("igneumhash", Some(&base.to_string())).unwrap()));
        c.params_digest = work::params_digest(&base);
        c.algo_params = base.clone();
        let mk = |height: u64, params: serde_json::Value| JobTemplate { job_id: format!("t{height}"), height, header: [7u8; 32], boundary: [0xff; 32], share_target: [0xff; 32], nonce_start: 0, nonce_count: u64::MAX, issued: Instant::now(), template: format!("{height}"), epoch: None, params, params_digest: None, next_params: None, keyed_rounds: 0, bond_key: None, share: None, coord: None, coord_difficulty: None, coord_last_height: None, paid_coords: None, lane_worth: None, lane_worth_unknown: false, lane_workers: None, subsidy_ign: None, program_anchor: None, anchor_height: None, chain_id: 0, coord_name: None };
        // the same document: taken, nothing changes
        assert!(c.take_template(mk(10, base.clone())));
        assert_eq!(c.params_digest, work::params_digest(&base));
        // a new document: taken on the new digest, the verifier follows it
        let bigger = serde_json::json!({"version": 1, "dataset_mib": 128, "epoch_blocks": 8});
        assert!(c.take_template(mk(1500, bigger.clone())));
        assert_eq!(c.params_digest, work::params_digest(&bigger));
        assert_eq!(c.algo_params, bigger);
        assert_eq!(c.verifier.as_ref().unwrap().epoch_at(1500), Some(1500 / 8), "the verifier derives epochs under the new document");
        // a result tagged with the old digest is never submitted
        let old_digest = work::params_digest(&base);
        assert_ne!(old_digest, c.params_digest);
        // a document the reference refuses (no version): refused, no template taken, said once
        assert!(!c.take_template(mk(1600, serde_json::json!({"dataset_mib": 64}))));
        assert!(c.current.is_none());
        assert!(c.refused_digest.is_some());
        assert!(!c.take_template(mk(1601, serde_json::json!({"dataset_mib": 64}))));
        assert_eq!(c.params_digest, work::params_digest(&bigger), "the refused document never replaces the running one");
        // the network serving the running document again: taken
        assert!(c.take_template(mk(1602, bigger.clone())));
        assert!(c.current.is_some());
        drop(c);
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// F-22's two directions: the worker and the verifier follow the template's document version both ways.
    #[cfg(feature = "ignpow")]
    fn hashes_agree_under(doc: &serde_json::Value) {
        let v = verify::verifier_for_params("igneumhash", Some(&doc.to_string())).unwrap();
        let header = [0x22u8; 32];
        let (mut mix, mut hash) = ([0u8; 32], [0u8; 32]);
        unsafe {
            let mut st = 0;
            let c = std::ffi::CString::new("igneumhash").unwrap();
            let pc = std::ffi::CString::new(doc.to_string()).unwrap();
            let ctx = igneum_pow_sys::ignpow_ctx_create(c.as_ptr(), pc.as_ptr(), 1, 0, &mut st);
            assert!(!ctx.is_null(), "{}", igneum_pow_sys::status_name(st));
            assert_eq!(igneum_pow_sys::ignpow_hash(ctx, 1, header.as_ptr(), 9, mix.as_mut_ptr(), hash.as_mut_ptr()), 0);
            igneum_pow_sys::ignpow_ctx_free(ctx);
        }
        assert!(v.verify(1, &header, 9, &mix, &hash).unwrap(), "the verifier agrees with the reference under {doc}");
    }

    #[cfg(feature = "ignpow")]
    fn drive_change(from: serde_json::Value, to: serde_json::Value) {
        let dir = std::env::temp_dir().join(format!("igneum-sup-f22-{}-{}", std::process::id(), work::params_digest(&to).chars().take(6).collect::<String>()));
        let cfg = Config::new("Ember", "0.0.0", &dir.join("nowhere"), dir.clone());
        let s = Supervisor::new(cfg);
        let mut c = lock(&s.inner);
        c.source = Some(Box::new(work::SoloSource::new("http://127.0.0.1:1", "igneumhash").unwrap()));
        c.verifier = Some(Arc::from(verify::verifier_for_params("igneumhash", Some(&from.to_string())).unwrap()));
        c.params_digest = work::params_digest(&from);
        c.algo_params = from.clone();
        let mk = |height: u64, params: serde_json::Value| JobTemplate { job_id: format!("t{height}"), height, header: [7u8; 32], boundary: [0xff; 32], share_target: [0xff; 32], nonce_start: 0, nonce_count: u64::MAX, issued: Instant::now(), template: format!("{height}"), epoch: None, params, params_digest: None, next_params: None, keyed_rounds: 0, bond_key: None, share: None, coord: None, coord_difficulty: None, coord_last_height: None, paid_coords: None, lane_worth: None, lane_worth_unknown: false, lane_workers: None, subsidy_ign: None, program_anchor: None, anchor_height: None, chain_id: 0, coord_name: None };
        assert!(c.take_template(mk(100, from.clone())));
        assert!(c.take_template(mk(2780, to.clone())), "the template naming the other document is taken");
        assert_eq!(c.params_digest, work::params_digest(&to));
        assert_eq!(c.algo_params, to);
        assert!(c.totals.selfcheck_ms < SELFCHECK_BOUND.as_millis() as u64, "the self-check returned");
        hashes_agree_under(&to);
        drop(c);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    #[cfg(feature = "ignpow")]
    fn a_new_worker_on_the_old_document_mines_it() {
        // the chain's version 1 document in force after a version 4 one was announced: version 1 is mined
        drive_change(serde_json::json!({"version": 4, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 256}), serde_json::json!({"version": 1, "dataset_mib": 64, "rounds": 128}));
    }

    #[test]
    #[cfg(feature = "ignpow")]
    fn an_old_worker_on_the_new_document_mines_it() {
        // the width entry: version 1 in force, version 4 at 256-byte items arrives: version 4 is mined
        drive_change(serde_json::json!({"version": 1, "dataset_mib": 64, "rounds": 128}), serde_json::json!({"version": 4, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 256}));
    }

    /// F-25: the interval arithmetic never converts a non-finite value, and the measurement difficulty never
    /// reaches infinity however many results are dropped.
    #[test]
    fn a_non_finite_interval_is_clamped_not_converted() {
        let (d, finite) = secs_guarded(f64::INFINITY);
        assert!(!finite);
        assert_eq!(d, Duration::from_secs(86_400));
        let (d, finite) = secs_guarded(f64::NAN);
        assert!(!finite);
        assert_eq!(d, Duration::from_secs(86_400));
        let (d, finite) = secs_guarded(-1.0);
        assert!(!finite);
        assert_eq!(d, Duration::from_secs(86_400));
        let (d, finite) = secs_guarded(120.0);
        assert!(finite);
        assert_eq!(d, Duration::from_secs(120));
        let mut diff = 50_000.0f64;
        for _ in 0..4096 {
            diff = (diff * 2.0).min(MAX_MEASURE_DIFFICULTY);
        }
        assert!(diff.is_finite() && diff <= MAX_MEASURE_DIFFICULTY);
        assert!((diff / 1.0e-9).is_finite(), "the expected interval stays finite at any rate above zero");
    }

    /// A worker whose stats say zero hashes on the job it holds is restarted after IDLE_HASHES_AFTER, ahead of the
    /// averaged-rate watchdog (the 5090 pod worker that stopped after a cancel, 11 October); a validated result
    /// inside the bound holds the verdict back.
    #[test]
    fn zero_hash_stats_on_a_held_job_are_an_idle_worker_after_the_bound() {
        let now = Instant::now();
        let long_ago = now - IDLE_HASHES_AFTER - Duration::from_secs(1);
        let just_now = now - Duration::from_secs(1);
        assert!(idle_hashes_too_long(long_ago, long_ago, now));
        assert!(!idle_hashes_too_long(just_now, long_ago, now), "zero hashes for a second are not a verdict");
        assert!(!idle_hashes_too_long(long_ago, just_now, now), "a fresh validated result holds the verdict back");
        assert!(!idle_hashes_too_long(now - IDLE_HASHES_AFTER, long_ago, now), "the bound itself is not yet past");
    }

    /// A notice clears the moment its cause clears (the project's rule of 19:2x UK, 15 October 2026): the pure rule
    /// notices_still_standing, read on every tick against the cards that mine.
    /// The watchdog's judgement (the GPU team's row of 02:17 UK, 16 October 2026): a tick in a named phase is not a wedge
    /// before the phase's bound; a tick in no phase is a wedge past the stall.
    #[test]
    fn a_tick_in_a_named_phase_is_not_wedged_before_the_phases_bound() {
        assert!(!tick_is_wedged(Duration::from_secs(149), Some((Duration::from_secs(149), "building the verifier"))));
        assert!(!tick_is_wedged(Duration::from_secs(700), Some((Duration::from_secs(700), "launching a worker"))));
        assert!(tick_is_wedged(Duration::from_secs(800), Some((Duration::from_secs(721), "launching a worker"))));
        assert!(tick_is_wedged(Duration::from_secs(121), None));
        assert!(!tick_is_wedged(Duration::from_secs(119), None));
    }

    #[test]
    fn a_bound_only_version_without_its_worker_refuses_the_coordinate() {
        // version 18 (the raster lane) with no -v18 worker in the install: refused for the card
        assert!(coordinate_needs_absent_bound_worker(Some(18), false));
        // with the bound worker present (Linux gl-v18, macOS metal-v18): taken
        assert!(!coordinate_needs_absent_bound_worker(Some(18), true));
        // the base lanes' versions are the base worker's whether or not a bound worker exists
        for v in [8u64, 15, 21] {
            assert!(!coordinate_needs_absent_bound_worker(Some(v), false), "{v}");
            assert!(!coordinate_needs_absent_bound_worker(Some(v), true), "{v}");
        }
        // a document without a version is not a bound-only one
        assert!(!coordinate_needs_absent_bound_worker(None, false));
        // 0.2.20: the Metal base worker takes version 21 (the GPU team's shares read gb-f189__apple-m6-mini__shares-1); the
        // 0.2.19 gate (version 21 bound-only on Metal) is lifted; the raster lane's 18 stays bound-only on every base
        assert!(!coordinate_needs_absent_bound_worker_for("igneum-worker-metal", Some(21), false));
        assert!(!coordinate_needs_absent_bound_worker_for("igneum-worker-metal", Some(21), true));
        assert!(coordinate_needs_absent_bound_worker_for("igneum-worker-metal", Some(18), false));
        assert!(!coordinate_needs_absent_bound_worker_for("igneum-worker-cuda", Some(21), false));
        assert!(coordinate_needs_absent_bound_worker_for("igneum-worker-cuda", Some(18), false));
        assert!(!coordinate_needs_absent_bound_worker_for("igneum-worker-metal", Some(15), false));
    }

    #[test]
    fn a_card_closed_on_a_configure_refusal_reopens_on_a_fresh_session() {
        // the 7322 frame on a 7320 session: the worker refused the document at configure, the card closed; the
        // pool's roll and its reconnect dial a fresh session: the card reopens
        assert!(reopens_on_fresh_session(&SlotState::Faulted, true, true));
        // a card closed for its own reason (hangs, malformed output) stays closed
        assert!(!reopens_on_fresh_session(&SlotState::Faulted, false, true));
        // a card the owner switched off stays off
        assert!(!reopens_on_fresh_session(&SlotState::Faulted, true, false));
        // a card that is not closed is not touched
        for st in [SlotState::Idle, SlotState::Mining, SlotState::Paused, SlotState::Refused, SlotState::Off, SlotState::Preparing] {
            assert!(!reopens_on_fresh_session(&st, true, true), "{st:?}");
        }
    }

    #[test]
    fn a_notice_clears_the_moment_its_cause_clears() {
        let n = |level: &str, text: &str| Notice { level: level.into(), text: text.into(), detail: String::new() };
        let before = vec![
            n("bad", "No device could start."),
            n("bad", "Apple M6: the worker did not start."),
            n("bad", "NVIDIA GeForce RTX 5090 stopped: it reports hashing but finds nothing."),
            n("bad", "The pinned coordinate is not served."),
            n("warn", "The work source is not reachable."),
            n("bad", "The source refuses this work as invalid."),
        ];
        // no card mines: nothing clears (the causes stand)
        assert_eq!(notices_still_standing(&before, &[]), before);
        // the M6 mines: the whole-engine notices and the M6's own go; the 5090's, the warning and the source's stand
        let kept = notices_still_standing(&before, &["Apple M6".to_string()]);
        assert_eq!(kept.iter().map(|x| x.text.as_str()).collect::<Vec<_>>(), vec![
            "NVIDIA GeForce RTX 5090 stopped: it reports hashing but finds nothing.",
            "The work source is not reachable.",
            "The source refuses this work as invalid.",
        ]);
        // both mine: the 5090's goes too
        let kept = notices_still_standing(&before, &["Apple M6".to_string(), "NVIDIA GeForce RTX 5090".to_string()]);
        assert_eq!(kept.iter().map(|x| x.text.as_str()).collect::<Vec<_>>(), vec!["The work source is not reachable.", "The source refuses this work as invalid."]);
        // a name that is a prefix of another card's never clears the other's notice
        let two = vec![n("bad", "NVIDIA GeForce RTX 5090 Ti: the worker did not start.")];
        assert_eq!(notices_still_standing(&two, &["NVIDIA GeForce RTX 5090".to_string()]), two);
    }

    /// The memory-pressure guard (security's headroom exception for the 8 GB class, 11 October 2026): warning
    /// pauses mining under PauseCause::Memory, critical keeps it paused, normal resumes it with a transition
    /// record (path "pressure") charged at the rate before; the pause counts as downtime; the schedule never
    /// lifts it.
    #[test]
    fn memory_pressure_pauses_at_warning_holds_at_critical_and_resumes_at_normal() {
        use std::sync::atomic::{AtomicU8, Ordering};
        let dir = std::env::temp_dir().join(format!("igneum-sup-pressure-{}", std::process::id()));
        let cfg = Config::new("Ember", "0.0.0", &dir.join("nowhere"), dir.clone());
        let s = Supervisor::new(cfg);
        let level = Arc::new(AtomicU8::new(1));
        {
            let mut c = lock(&s.inner);
            let l = level.clone();
            c.pressure_source = Box::new(move || match l.load(Ordering::SeqCst) {
                1 => Some(pressure::Level::Normal),
                2 => Some(pressure::Level::Warning),
                4 => Some(pressure::Level::Critical),
                _ => None,
            });
            c.mining = MiningState::Mining;
            c.started_at = Some(Instant::now());
        }
        // normal: nothing happens
        lock(&s.inner).watch_memory_pressure();
        assert!(matches!(lock(&s.inner).mining, MiningState::Mining));
        // warning: paused under the guard
        level.store(2, Ordering::SeqCst);
        lock(&s.inner).watch_memory_pressure();
        {
            let c = lock(&s.inner);
            assert!(matches!(c.mining, MiningState::Paused), "{:?}", c.mining);
            assert_eq!(c.paused_for, Some(PauseCause::Memory));
            assert!(c.pressure_paused_at.is_some());
            assert_eq!(c.memory_pressure, Some(pressure::Level::Warning));
        }
        // the pause is downtime, and the schedule stays paused on it
        {
            let mut c = lock(&s.inner);
            let before = c.downtime;
            c.tick_inner(Duration::from_secs(3));
            assert!(c.downtime >= before + Duration::from_secs(3), "{:?}", c.downtime);
            assert!(matches!(c.mining, MiningState::Paused));
            let m = Moment { minute_of_day: 0, other_app_fullscreen: None, on_battery: None, game_gone_for: None, paused_for: Some(PauseCause::Memory) };
            assert!(matches!(decide(&c.settings.tariff, &c.settings.time_of_use, &c.settings.gaming_pause, c.settings.pause_on_battery, &m), Decision::StayPaused(PauseCause::Memory)));
        }
        // critical: still paused, no second record
        level.store(4, Ordering::SeqCst);
        lock(&s.inner).watch_memory_pressure();
        {
            let c = lock(&s.inner);
            assert!(matches!(c.mining, MiningState::Paused));
            assert_eq!(c.totals.transitions, 0);
        }
        // normal: resumed, one record charged at the rate before (zero here: nothing validated)
        level.store(1, Ordering::SeqCst);
        lock(&s.inner).watch_memory_pressure();
        {
            let c = lock(&s.inner);
            assert!(matches!(c.mining, MiningState::Mining), "{:?}", c.mining);
            assert_eq!(c.paused_for, None);
            assert_eq!(c.totals.transitions, 1);
            let rec = c.transitions.back().expect("the pressure record");
            assert_eq!(rec.path, "pressure");
            assert!(rec.gap_s >= 0.0 && rec.lost_validated_hashes >= 0.0);
            assert!(c.pressure_paused_at.is_none());
        }
        // no reading on the platform: nothing happens either way
        level.store(0, Ordering::SeqCst);
        lock(&s.inner).watch_memory_pressure();
        assert!(matches!(lock(&s.inner).mining, MiningState::Mining));
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// F-25: a tick that panics is caught, named in the state, and mining stops with the reason; the thread lives.
    #[test]
    fn a_tick_panic_is_named_and_stops_mining() {
        let dir = std::env::temp_dir().join(format!("igneum-sup-f25-{}", std::process::id()));
        let cfg = Config::new("Ember", "0.0.0", &dir.join("nowhere"), dir.clone());
        let s = Supervisor::new(cfg);
        {
            let mut c = lock(&s.inner);
            c.mining = MiningState::Mining;
            c.tick_faults = 0;
        }
        // the same guard as run_forever's, around a tick that panics
        {
            let mut c = lock(&s.inner);
            let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                let _ = &mut *c;
                panic!("cannot convert float seconds to Duration: value is either too big or NaN");
            }));
            assert!(r.is_err());
            if let Err(e) = r {
                let msg = e.downcast_ref::<&str>().map(|x| x.to_string()).unwrap_or_default();
                c.tick_faults += 1;
                c.tick_fault = Some(msg.clone());
                c.stop(&format!("the supervisor's tick failed: {msg}"));
            }
            c.tick_beat = Instant::now();
        }
        let snap = s.snapshot();
        assert!(snap.tick_fault.as_deref().unwrap_or("").contains("NaN"));
        assert!(!matches!(snap.mining, MiningState::Mining), "mining is not left running after a tick fault");
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// F-25: the watchdog reads the tick's beat from its own thread; a beat older than the stall bound while
    /// mining is a stall.
    #[test]
    fn a_dead_tick_is_a_named_stall() {
        let dir = std::env::temp_dir().join(format!("igneum-sup-f25b-{}", std::process::id()));
        let cfg = Config::new("Ember", "0.0.0", &dir.join("nowhere"), dir.clone());
        let s = Supervisor::new(cfg);
        {
            let mut c = lock(&s.inner);
            c.mining = MiningState::Mining;
            c.tick_beat = Instant::now() - TICK_STALL - Duration::from_secs(1);
        }
        // the watchdog's judgement, as its thread makes it
        let stalled = {
            let c = s.inner.try_lock().expect("free");
            matches!(c.mining, MiningState::Mining) && c.tick_beat.elapsed() > TICK_STALL
        };
        assert!(stalled);
        let snap = s.snapshot();
        assert!(snap.tick_age_ms > TICK_STALL.as_millis() as u64);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn the_power_offer_names_the_cohorts_row_and_never_a_setting() {
        let o = devices::power_offer_for("AMD Radeon RX 9070 XT", "amd").expect("the 9070 XT has a row");
        assert!(o.contains("70 percent") && o.contains("never sets a power limit"));
        assert!(devices::power_offer_for("NVIDIA GeForce RTX 3060", "nvidia").is_none(), "no row, no offer");
    }

    /// The plan's point 10, automatic pool transitions: the backup setting is validated, and the switch takes a
    /// reachable source, refuses an unreachable one, and hands the templates over clean.
    #[test]
    fn the_backup_source_is_validated_and_taken_when_it_answers() {
        let mut st = Settings::default();
        st.work_source = WorkSourceSetting::PoolCustom { host: "a.example".into(), port: 3333, tls: false };
        st.work_source_backup = WorkSourceSetting::PoolCustom { host: "a.example".into(), port: 3333, tls: false };
        assert!(st.validate().is_err(), "the same source is no backup");
        st.work_source_backup = WorkSourceSetting::Stub;
        assert!(st.validate().is_err(), "the stub is nobody's backup");
        st.work_source_backup = WorkSourceSetting::PoolCustom { host: "b.example".into(), port: 3333, tls: false };
        assert!(st.validate().is_ok());
        let dir = std::env::temp_dir().join(format!("igneum-sup-failover-{}", std::process::id()));
        let cfg = Config::new("Ember", "0.0.0", &dir.join("nowhere"), dir.clone());
        let s = Supervisor::new(cfg);
        let mut c = lock(&s.inner);
        c.settings.payout_address = Some("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed".into());
        // a backup nobody answers is refused and the first source stays
        let mut first = work::source_for(&WorkSourceSetting::Stub, "stub-v0").unwrap();
        first.connect("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", "w").unwrap();
        c.source = Some(first);
        let r = c.switch_source(&WorkSourceSetting::Solo { rpc_url: "http://127.0.0.1:1".into() });
        assert!(r.is_err(), "an unreachable source is not taken");
        assert!(c.source.as_ref().map(|x| x.name()).unwrap_or_default().contains("stub"));
        // a source that answers is taken, the templates handed over clean
        c.current = Some(JobTemplate { job_id: "old".into(), height: 1, header: [1u8; 32], boundary: [0xff; 32], share_target: [0xff; 32], nonce_start: 0, nonce_count: 10, issued: Instant::now(), template: "old".into(), epoch: None, params: serde_json::Value::Null, params_digest: None, next_params: None, keyed_rounds: 0, bond_key: None, share: None, coord: None, coord_difficulty: None, coord_last_height: None, paid_coords: None, lane_worth: None, lane_worth_unknown: false, lane_workers: None, subsidy_ign: None, program_anchor: None, anchor_height: None, chain_id: 0, coord_name: None });
        let r = c.switch_source(&WorkSourceSetting::Stub);
        assert!(r.is_ok());
        assert!(c.current.is_none() && c.sent.is_empty());
        drop(c);
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// A submission unanswered for the bound while the source reports no problem is named and the source
    /// reconnected; a block that is not due yet is not that (the expected interval is on the line instead).
    #[test]
    fn an_unanswered_submission_is_a_named_stall_and_a_reconnect() {
        let dir = std::env::temp_dir().join(format!("igneum-sup-silent-{}", std::process::id()));
        let cfg = Config::new("Ember", "0.0.0", &dir.join("nowhere"), dir.clone());
        let s = Supervisor::new(cfg);
        let mut c = lock(&s.inner);
        c.settings.payout_address = Some("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed".into());
        c.settings.work_source = WorkSourceSetting::Stub;
        let mut src = work::source_for(&WorkSourceSetting::Stub, "stub-v0").unwrap();
        src.connect("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", "w").unwrap();
        c.source = Some(src);
        c.mining = MiningState::Mining;
        let now = Instant::now();
        // nothing submitted: nothing to judge
        c.watch_unanswered(now);
        assert!(c.unanswered_since.is_none());
        // a fresh submission: not yet
        c.submitted_at.insert(("j".into(), 1), now);
        c.watch_unanswered(now);
        assert!(c.unanswered_since.is_none());
        // one older than the bound with no answer: named, the source reconnected (the stub reconnects at once)
        c.submitted_at.insert(("j".into(), 2), now - UNANSWERED_AFTER - Duration::from_secs(1));
        c.watch_unanswered(now);
        assert!(c.unanswered_since.is_some());
        assert_eq!(c.totals.source_reconnects, 1);
        assert!(c.submitted_at.is_empty(), "the reconnect clears the unanswered set");
        assert!(c.log.iter().any(|l| l.contains("not answered")));
        // silent again for another two minutes after the reconnect: reconnected again, never stopped (12 October
        // 2026: the mini's Mac app went idle here twice in one night on a socket loss the source never reported)
        let later = now + UNANSWERED_AFTER + Duration::from_secs(2);
        c.submitted_at.insert(("j".into(), 3), now);
        c.watch_unanswered(later);
        assert!(matches!(c.mining, MiningState::Mining), "mining goes on");
        assert_eq!(c.totals.source_reconnects, 2);
        assert!(c.unanswered_since.is_some(), "still watched");
        assert!(c.log.iter().any(|l| l.contains("reconnected again")));
        assert!(!c.notices.iter().any(|n| n.text.starts_with("Mining stopped")));
        drop(c);
        let _ = std::fs::remove_dir_all(&dir);
    }
    /// F-E14b: a pool that holds one share while answering others is not a silent source: no redial.
    #[test]
    fn a_held_share_beside_answered_ones_is_not_a_stall() {
        let now = Instant::now();
        let dir = std::env::temp_dir().join(format!("igneum-sup-held-{}", std::process::id()));
        let cfg = Config::new("Ember", "0.0.0", &dir.join("nowhere"), dir.clone());
        let s = Supervisor::new(cfg);
        let mut c = lock(&s.inner);
        c.submitted_at.insert(("j".into(), 7), now - UNANSWERED_AFTER - Duration::from_secs(5));
        c.last_outcome = Some(now - Duration::from_secs(3));
        c.watch_unanswered(now);
        assert!(c.unanswered_since.is_none(), "a held share beside fresh answers is the pool holding, not silence");
        assert_eq!(c.totals.source_reconnects, 0);
        c.last_outcome = Some(now - UNANSWERED_AFTER - Duration::from_secs(1));
        c.watch_unanswered(now);
        assert!(c.unanswered_since.is_some(), "no answer of any kind for the bound beside an old submission is silence");
    }

    #[test]
    fn safe_dir_names() {
        assert_eq!(safe_dir_name("0000:01:00.0"), "0000_01_00_0");
        assert_eq!(safe_dir_name("stub:abc-1"), "stub_abc-1");
    }
}

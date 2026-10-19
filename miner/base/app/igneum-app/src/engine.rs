//! The supervisor: starts the node, waits until it is synced, starts one miner per card, reads every line they print
//! into the state the dashboard shows, restarts what crashes, and stops everything in order (miners first, then the
//! node) on quit. The command lines are the ones today's launchers use (packaging/mac/app/igneum-miner.sh and
//! proto-cuda/windows-app/igneum-common.ps1).

use crate::config::{Packaged, Runtime, Settings};
use crate::detect::Bins;
use crate::keys;
use crate::procs::{self, Line, Proc, Source};
use crate::state::{CardState, Rings, State};
use serde_json::{json, Value};
use std::io::Write;
use std::path::PathBuf;
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

pub const VERSION: &str = env!("CARGO_PKG_VERSION");
/// The preview mark (state.preview): set by IGNEUM_PREVIEW at build time on an internal build only.
pub const PREVIEW: Option<&str> = option_env!("IGNEUM_PREVIEW");
/// miner-ui-5: a signing streak breaks when no VOTE line comes for this long (the devnet's presence window, 20
/// checkpoints of 30 s; the chain-side fact is `getFinalityWeights.keys[].participation`, this is the local reading)
pub const LADDER_GAP_S: f64 = 600.0;
/// How long after a refused worker-layer start the engine tries it again while running.
pub const LAYER_RETRY: Duration = Duration::from_secs(30);
const POW_EPOCH_BLOCKS: u64 = 3_600;

#[derive(Clone, Debug, PartialEq)]
pub struct CardChoice {
    pub key: String,
    pub enabled: bool,
    pub identities: u32,
    pub power_pct: Option<u32>,
}

pub enum Cmd {
    Detect,
    /// the payout address's balance (eth_getBalance, hex) or why it could not be read
    BalanceRead(Result<String, String>),
    /// the node's finality fields from igneum_getProvingStatus (None: the node carries none, or did not answer)
    FinalityStatus(Option<crate::ember::FinalityStatus>),
    ApplyCards(Vec<CardChoice>),
    /// the prover asks for the under-12 GB cards' miners to be held off (true) or given back (false); idempotent
    /// driver-check (7 October 2026): the row's Install click, the Restart now click, the install thread's reports
    DriverInstall(String),
    DriverRestart,
    Driver(crate::drivers::Event),
    /// one enumeration of the cards finished (the first, or a re-detection: src/hotplug.rs)
    Detected(crate::detect::Detection),
    Start,
    Pause(String),
    Resume,
    RestartMiners(String),
    CheckUpdate,
    InstallUpdate,
    AutoUpdate(bool),
    OpenUpdateFile,
    /// the over-the-air updater's threads report here (src/ota.rs)
    Ota(crate::ota::Event),
    /// the remote-job runner's threads report here (src/jobrun.rs)
    Job(crate::jobrun::Event),
    JobsAllow(bool),
    JobsCheck,
    /// (card index, the ask's sequence, the result): a result whose sequence is not the slot's current ask is ignored (MF-14)
    WorkerBuilt(usize, u64, Result<PathBuf, String>),
    /// local minus the latest block's timestamp, seconds (from the node's EVM RPC)
    ClockSample(f64),
    /// the node readiness probe answered (src/execrpc.rs probe): did the RPC answer, does the exec follower hold a record
    ExecProbe { answered: bool, has_record: bool },
    /// local minus an HTTPS Date header, seconds; None when the check failed
    ClockHttps(Option<f64>),
    ClockCheck,
    ClockSync,
    ClockSynced(Result<String, String>),
    /// the elevated nvidia-smi -pl step finished: (what was asked, how it ran, the limits read back per device)
    PowerApplied(String, Result<(), String>, std::collections::HashMap<String, f64>),
    /// the window host ran the elevated command line (Windows): Ok or the reason
    ElevatedDone(Result<(), String>),
    /// the user asked for the cap again (the Retry button)
    ApplyPower,
    /// the efficiency sweep (src/sweep.rs): start on one card (by key), stop, pin or unpin a card's cap, the toggle
    SweepStart(String),
    SweepStop,
    SweepPin(String, bool),
    SweepEnable(bool),
    /// Settings > Power control (config.rs power_control): on asks for administrator rights once, at that moment
    PowerControl(bool),
    /// Ember Tune's probe of a card (src/ember.rs): the vendor's clock range, the driver, and how settings reach the
    /// card (direct, the elevated helper, the AMD helper, or measure only)
    TuneProbe(usize, Result<TuneProbe, String>),
    /// the elevated helper process ended (Ok at `quit`, Err when the prompt was cancelled or it failed)
    SweepHelperDone(Result<(), String>),
    /// a tune request (by number) was carried out: what the vendor tool printed, or why it refused
    TuneSet(u64, Result<String, String>),
    /// POST /api/tune-progress from a measurement engine beside this app: the card's live tune state
    TuneProgress(Value),
    /// Ember 2: Settings > goal, electricity price, the hill-climb switch
    TuneGoal(Option<String>, Option<f64>, Option<bool>),
    /// one card's goal override (key, goal; "" = back to the global goal)
    TuneCardGoal(String, String),
    /// Ember Tune tiers (8 October 2026): apply a tier (efficiency | balanced | max) to one card (key) or every card
    /// (None, which also sets the fleet tier) through the cap and lock path, no re-search
    TuneTier(Option<String>, String),
    /// Ember Heat (src/heat.rs): the region code and the typed price (Settings > Electricity, the first-run step)
    Region(Option<String>, Option<f64>, Option<String>),
    /// the work source (step 3): kind ("node" | "pool") and its address
    Source(String, String),
    /// Ember Heat: the switch, the set point, the schedule text with the window's clock offset, a typed room reading
    Heat(HeatPatch),
    /// restart the node with the verifier decided again (src/verifier.rs): the trust setting changed, or the
    /// prover found a host that was not there when the node started
    RestartNode(String),
    /// quit, with its source (the log names it: C35, 5 October 2026, two unexplained quits)
    Quit(&'static str),
    /// ui-ota (src/uiota.rs): the download thread's result; the server served a bundle's index.html (its version);
    /// the page's health ping (None) or its first-paint error; Settings > Use the built-in interface
    UiOta(crate::uiota::Event),
    UiPageLoaded(String),
    UiHealth(Option<String>),
    UiBuiltin(bool),
}

/// What POST /api/heat may change; every field optional, the engine keeps the rest.
#[derive(Clone, Debug, Default)]
pub struct HeatPatch {
    pub on: Option<bool>,
    pub set_c: Option<f64>,
    /// the schedule as typed ("" clears it) and the window's minutes east of UTC
    pub schedule: Option<(String, i32)>,
    /// a room reading the miner typed now, degrees
    pub room_c: Option<f64>,
}

/// The heat state before the loop has run (and whenever the mode is off).
fn heat_state_off(on: bool, set_c: f64) -> crate::state::HeatState {
    crate::state::HeatState { on, phase: if on { "waiting".into() } else { "off".into() }, set_c, room_source: "none".into(), note: crate::heat::words(on, "waiting", set_c, &crate::heat::Reading::none(), 0.0, 0.0), period_s: crate::heat::PERIOD_S, offset_c: crate::heat::OFFSET_DEFAULT_C, ..Default::default() }
}

pub struct Shared {
    pub token: String,
    pub state: Mutex<State>,
    pub rings: Mutex<Rings>,
    pub settings: Mutex<Settings>,
    pub settings_path: PathBuf,
    pub wallet_path: PathBuf,
    pub runtime: Runtime,
    pub packaged: Packaged,
    cmd_tx: Mutex<Sender<Cmd>>,
    /// ui-ota: the folder the server serves the dashboard from (None = the files embedded in the binary)
    ui_dir: Mutex<Option<PathBuf>>,
    pub started: Instant,
    /// first-block-21: the run's start on the wall clock (unix s), for the dashboard's "a card is for this run" rule;
    /// `started` is monotonic and stops across a sleep, so the two are kept apart
    pub started_unix: f64,
    engine_log: Mutex<Option<std::fs::File>>,
    /// the app log's path: the one the uploader sends (main.rs names it; the engine must not guess the stamp)
    pub log_path: PathBuf,
    port: std::sync::atomic::AtomicU16,
    /// set once `state_json` has logged a serialisation error (one line, not one per poll)
    state_error_logged: std::sync::atomic::AtomicBool,
    /// miner-ui-5: the machine's own ladder record (src/ladder.rs), `<app dir>/ladder.json`
    pub ladder: Mutex<crate::ladder::Ladder>,
    pub ladder_path: PathBuf,
    /// an earlier release: the quit guard is armed once per process (src/quitguard.rs)
    pub quit_armed: std::sync::atomic::AtomicBool,
}

impl Shared {
    pub fn new(token: String, runtime: Runtime, packaged: Packaged, mut settings: Settings, cmd_tx: Sender<Cmd>, engine_log: Option<std::fs::File>, log_path: PathBuf) -> Shared {
        let settings_path = runtime.app_dir.join("settings.json");
        let wallet_path = runtime.app_dir.join("wallet.json");
        let ladder_path = runtime.app_dir.join("ladder.json");
        // first-block-21: a block card is for the run that raised it, and the first-block flag is reconciled with
        // the persisted lifetime count (a record from before the flag whose first block already came takes it)
        let mut ladder = crate::ladder::Ladder::load(&ladder_path);
        if ladder.start_run(settings.accepted_total) {
            ladder.save(&ladder_path);
        }
        // v0.2.1: the first-block card is once per payout address, kept in settings.json; an install that showed it under
        // the machine rule (the ladder's flag) takes the mark for its current address once, so the card does not come again
        {
            let addr = settings.address.to_ascii_lowercase();
            if ladder.first_block_shown && !addr.is_empty() && !settings.first_block_shown.contains_key(&addr) {
                settings.first_block_shown.insert(addr, crate::config::FirstBlockMark { hash: ladder.first_block_hash.clone(), at: ladder.first_block_at });
                settings.save(&settings_path);
            }
        }
        let mut st = State { version: VERSION.into(), preview: PREVIEW.unwrap_or("").into(), worker_layer: crate::sup::layer_on(), edition: crate::edition::name().into(), product: crate::edition::product_name().into(), channel: crate::edition::channel().into(), ..Default::default() };
        st.network = runtime.network.clone();
        st.network_name = format!("chain {}", crate::config::CHAIN_ID);
        st.network_pending = String::new(); // the chain is pinned (config::CHAIN_ID): nothing is ever pending
        st.chain = crate::config::CHAIN_NAME.into();
        st.host = runtime.host.clone();
        st.display_name = if settings.display_name.is_empty() { runtime.host.clone() } else { settings.display_name.clone() };
        st.machine_id = runtime.machine_id.clone();
        st.node_dir = runtime.node_dir.display().to_string();
        st.log_dir = runtime.log_dir.display().to_string();
        st.setup_done = settings.setup_done;
        st.phase = if settings.setup_done { "dashboard".into() } else { "welcome".into() };
        st.node.state = "stopped".into();
        st.node.tip_age_s = -1.0;
        st.mining.state = "idle".into();
        st.mining.paused = settings.paused;
        st.mining.accepted_total = settings.accepted_total;
        st.mining.fee_total = settings.fee_total;
        st.address = address_state(&settings, &wallet_path);
        st.settings = crate::state::SettingsState { identities: settings.identities, fleet_console_url: settings.fleet_console_url.clone(), fleet_token_set: !settings.fleet_console_token.is_empty(), vote: settings.vote, start_at_login: crate::platform::start_at_login_is_on(), auto_update: settings.auto_update, remote_jobs: settings.remote_jobs, prove: settings.prove, sweep: settings.sweep, power_control: settings.power_control, power_note: String::new(), tuning_off: false, tuning_note: String::new(), tune_goal: settings.tune_goal.clone(), tune_tier: settings.tune_tier.clone(), power_price_pence: settings.power_price_pence, tune_climb: settings.tune_climb, region: settings.region.clone(), currency: settings.currency.clone(), network: settings.network.clone(), source_kind: settings.source_kind.clone(), source_url: settings.source_url.clone(), heat_on: settings.heat_on, heat_set_c: settings.heat_set_c, heat_schedule: crate::heat::schedule_text(&settings.heat_schedule), heat_tz_min: settings.heat_tz_min, heat_room_c: settings.heat_room_c, heat_room_at: settings.heat_room_at, heat_offset_c: settings.heat_offset_c, tune_period_s: crate::ember::PERIOD_S, dev_fee: settings.dev_fee, proof_verify_trust: settings.proof_verify_trust, profile_public: settings.profile_public, ui_builtin: settings.ui_builtin, prove_instead: settings.prove_instead };
        st.heat = heat_state_off(settings.heat_on, settings.heat_set_c);
        st.dev_fee = crate::state::DevFeeState { on: settings.dev_fee, percent: if settings.dev_fee { 1 } else { 0 }, ..Default::default() };
        st.live_page = packaged.live_page.clone();
        st.finality.message = "waiting for the miner".into();
        st.clock.hint = crate::platform::clock_hint().into();
        st.clock.severity = "none".into();
        st.program.message = "waiting for work".into();
        Shared {
            token,
            state: Mutex::new(st),
            rings: Mutex::new(Rings::new()),
            settings: Mutex::new(settings),
            settings_path,
            wallet_path,
            runtime,
            packaged,
            cmd_tx: Mutex::new(cmd_tx),
            ui_dir: Mutex::new(None),
            started: Instant::now(),
            started_unix: crate::platform::unix_now_f(),
            engine_log: Mutex::new(engine_log),
            quit_armed: std::sync::atomic::AtomicBool::new(false),
            log_path,
            port: std::sync::atomic::AtomicU16::new(0),
            state_error_logged: std::sync::atomic::AtomicBool::new(false),
            ladder: Mutex::new(ladder),
            ladder_path,
        }
    }

    /// miner-ui-5: the ladder record to disk (a few KB; after a block, a lock or a paid shard)
    pub fn save_ladder(&self) {
        self.ladder.lock().unwrap().save(&self.ladder_path);
    }
    /// miner-ui-5: a paid shard record of ours (src/prover.rs) joins the ladder's shard list
    pub fn ladder_shard_paid(&self, block: u64, shard: u64, wei: u128) {
        self.ladder.lock().unwrap().on_shard_paid(crate::platform::unix_now_f(), block, shard, wei);
        self.save_ladder();
    }

    /// The first line of every upload, so the console's Machines tab parses it without guessing:
    /// IGNEUM-APP version=<v> machine=<id8> platform=<os> node=<igneumd version>. Logged at every engine start too,
    /// so the restart after an OTA apply carries it again.
    pub fn upload_header(&self) -> String {
        let node = self.state.lock().unwrap().node.version.clone();
        format!("IGNEUM-APP version={} machine={} platform={} channel={} edition={} node={}", VERSION, self.runtime.id8(), crate::manifest::platform_name(), crate::edition::channel(), crate::edition::name(), if node.is_empty() { "unknown".to_string() } else { node.replace(' ', "_") })
    }

    pub fn set_port(&self, p: u16) {
        self.port.store(p, std::sync::atomic::Ordering::Relaxed);
        self.state.lock().unwrap().page.port = p;
    }
    /// The page's listener as asked for (the fixed port and the LAN choice), for Settings and the installers.
    pub fn set_page(&self, wanted: u16, lan: bool) {
        let mut st = self.state.lock().unwrap();
        st.page.wanted = wanted;
        st.page.lan = lan;
    }
    pub fn port(&self) -> u16 {
        self.port.load(std::sync::atomic::Ordering::Relaxed)
    }

    /// A Shared over a scratch folder for the unit tests that need one (src/uiota.rs): no node, no window, a channel
    /// nobody reads (send drops the command), the defaults everywhere else.
    #[cfg(test)]
    pub fn for_tests(dir: PathBuf) -> Arc<Shared> {
        let _ = std::fs::create_dir_all(&dir);
        let (tx, _rx) = channel();
        let runtime = crate::config::Runtime { network: "devnet".into(), rpc_port: 0, p2p_port: 0, peers: vec![], unsynced_mining: false, devnet_suffix: None, node_dir: dir.join("node"), app_dir: dir.clone(), log_dir: dir.join("logs"), status_secs: 30, sweep_only: true, host: "test".into(), machine_id: "0123456789abcdef".into() };
        Arc::new(Shared::new("t".into(), runtime, crate::config::Packaged::default(), Settings::default(), tx, None, dir.join("app.log")))
    }
    /// ui-ota: what the server serves the dashboard from; None = the embedded files
    pub fn ui_dir(&self) -> Option<PathBuf> {
        self.ui_dir.lock().unwrap().clone()
    }
    pub fn set_ui_dir(&self, d: Option<PathBuf>) {
        *self.ui_dir.lock().unwrap() = d;
    }
    pub fn send(&self, c: Cmd) {
        let _ = self.cmd_tx.lock().unwrap().send(c);
    }

    pub fn log(&self, text: &str) {
        let text = &crate::platform::redact(text);
        let stamp = crate::platform::unix_now_f();
        if let Some(f) = self.engine_log.lock().unwrap().as_mut() {
            let _ = writeln!(f, "{stamp:.0} {text}");
        }
        self.rings.lock().unwrap().log("app", false, text);
    }

    pub fn event(&self, kind: &str, text: &str) {
        let text = &crate::platform::redact(text);
        self.rings.lock().unwrap().event(kind, text);
        self.log(&format!("[{kind}] {text}"));
    }

    pub fn state_json(&self) -> Value {
        let mut st = self.state.lock().unwrap().clone();
        st.now = crate::platform::unix_now_f();
        st.uptime_s = self.started.elapsed().as_secs();
        st.started_at = self.started_unix;
        st.events = self.rings.lock().unwrap().events.iter().cloned().collect();
        st.ladder = {
            let mut ld = self.ladder.lock().unwrap();
            // earned-windows-26: a block that landed while the reward was unread is priced on the first read that has it
            if let Some(wei) = crate::chainfacts::cached_reward_wei() {
                let lifetime = self.settings.lock().unwrap().accepted_total;
                let filled = ld.backfill_earned(lifetime, wei);
                let priced = ld.price_blocks(wei);
                if filled || priced > 0 { ld.save(&self.ladder_path); }
            }
            ld.state(st.now, LADDER_GAP_S)
        };
        match serde_json::to_value(st) {
            Ok(v) => v,
            Err(e) => {
                // never a silent "{}": the dashboard and every PC playbook read this reply
                let msg = format!("state_json: {e}");
                if !self.state_error_logged.swap(true, std::sync::atomic::Ordering::Relaxed) {
                    self.log(&format!("[error] {msg}"));
                }
                json!({ "error": msg, "version": env!("CARGO_PKG_VERSION") })
            }
        }
    }

    /// A short state for the window host (menu bar, tray).
    pub fn wrapper_state(&self) -> Value {
        let st = self.state.lock().unwrap();
        json!({
            "phase": st.phase, "mining": st.mining.state, "paused": st.mining.paused,
            "hash_total": st.mining.hash_total, "accepted_total": st.mining.accepted_total,
            "node": st.node.state, "blocks": st.node.blocks, "peers": st.node.peers, "quitting": st.quitting,
            "update": st.update.available,
        })
    }

    pub fn save_settings(&self) {
        self.settings.lock().unwrap().save(&self.settings_path);
    }

    /// The recommended defaults put back (the founder's order of 11:0x UK, 12 October 2026: a fresh install runs
    /// at its best with no hand): updates on, remote jobs on, the tune on at Balanced, the climb off, Power
    /// control off, proving off, the dev fee on, the public pool as the source, every card on, the built-in
    /// interface off, not paused. The address, the names, the counters and the heat settings stay.
    pub fn reset_recommended(&self) {
        {
            let mut s = self.settings.lock().unwrap();
            let d = crate::config::Settings::default();
            s.auto_update = d.auto_update;
            s.remote_jobs = d.remote_jobs;
            s.sweep = d.sweep;
            s.tune_goal = d.tune_goal.clone();
            s.tune_tier = d.tune_tier.clone();
            s.tune_climb = d.tune_climb;
            s.power_control = d.power_control;
            s.prove = d.prove;
            s.prove_instead = d.prove_instead;
            s.dev_fee = d.dev_fee;
            s.source_kind = d.source_kind.clone();
            s.source_url = d.source_url.clone();
            s.ui_builtin = d.ui_builtin;
            s.paused = d.paused;
            s.proof_verify_trust = d.proof_verify_trust;
            for c in s.cards.values_mut() {
                c.enabled = true;
                c.pinned = false;
            }
            s.save(&self.settings_path);
        }
        self.log("settings reset to the recommended defaults (the window's Reset to recommended)");
        // the cards' switches and the tune reach the engine through their own commands
        let choices: Vec<CardChoice> = self.state.lock().unwrap().mining.cards.iter().map(|c| CardChoice { key: c.key.clone(), enabled: true, identities: c.identities, power_pct: None }).collect();
        if !choices.is_empty() {
            self.send(Cmd::ApplyCards(choices));
        }
        self.send(Cmd::SweepEnable(true));
        self.send(Cmd::AutoUpdate(true));
        self.send(Cmd::PowerControl(false));
        self.send(Cmd::Resume);
    }

    /// First-run setup: the payout address (a generated key, or one the user pasted) and the identity count.
    pub fn setup(&self, mode: &str, address: Option<&str>, identities: Option<u32>) -> Result<Value, String> {
        let mut out = json!({ "ok": true });
        {
            let mut s = self.settings.lock().unwrap();
            if let Some(n) = identities {
                s.identities = n;
            }
            match mode {
                "paste" => {
                    let a = address.ok_or("address missing")?.trim().to_string();
                    if !keys::valid_address(&a) {
                        return Err("an address is 0x followed by 40 hex characters".into());
                    }
                    s.address = a.to_ascii_lowercase();
                    s.address_source = "pasted".into();
                    s.key_saved = true;
                }
                _ => {
                    let wallet = match keys::load(&self.wallet_path) {
                        Some(w) => w,
                        None => {
                            let w = keys::generate();
                            keys::save(&self.wallet_path, &w).map_err(|e| format!("could not write the wallet file: {e}"))?;
                            w
                        }
                    };
                    s.address = wallet.address.clone();
                    s.address_source = "generated".into();
                    s.key_saved = false;
                    out["private_key"] = json!(wallet.private_key);
                }
            }
            s.save(&self.settings_path);
            out["address"] = json!(s.address);
            out["display"] = json!(keys::checksum(&s.address));
            out["source"] = json!(s.address_source);
            out["wallet_file"] = json!(self.wallet_path.display().to_string());
            let mut st = self.state.lock().unwrap();
            st.address = address_state(&s, &self.wallet_path);
            st.settings.identities = s.identities;
        }
        self.event("ok", &format!("rewards go to {}", self.state.lock().unwrap().address.display));
        Ok(out)
    }

    pub fn key_saved(&self) {
        self.settings.lock().unwrap().key_saved = true;
        self.save_settings();
        self.state.lock().unwrap().address.key_saved = true;
    }

    pub fn reveal_key(&self) -> Result<Value, String> {
        let w = keys::load(&self.wallet_path).ok_or("no wallet file: the address was pasted, not made here")?;
        self.log("the private key was shown in the window (settings > reveal)");
        Ok(json!({ "ok": true, "address": w.address, "display": keys::checksum(&w.address), "private_key": w.private_key, "wallet_file": self.wallet_path.display().to_string() }))
    }

    /// 0.2.8, the Fleet item: the console URL (normalised to end with "/") and the window's token; an empty token
    /// clears the held one; the page's state carries the URL and whether a token is held, never the token.
    pub fn set_fleet(&self, url: Option<&str>, token: Option<&str>) -> Result<Value, String> {
        let mut s = self.settings.lock().unwrap();
        if let Some(u) = url {
            let u = u.trim();
            if !u.is_empty() && !(u.starts_with("https://") || u.starts_with("http://")) {
                return Err("the console URL starts with https:// (or http:// on your own network)".into());
            }
            // the security review's read of 0.2.8 (14 October 2026): a plaintext console URL off the owner's own network puts
            // the window's token and the console's page in a stranger's hands; http:// is for loopback, a .local, .lan
            // or .home name, or a private address alone
            if u.starts_with("http://") && !crate::server::own_network_host(u) {
                return Err("http:// is for a console on your own network (localhost, a .local name or a private address); use https:// for a host on the internet".into());
            }
            s.fleet_console_url = if u.is_empty() || u.ends_with('/') { u.to_string() } else { format!("{u}/") };
        }
        if let Some(t) = token {
            s.fleet_console_token = t.trim().to_string();
        }
        s.save(&self.settings_path);
        let url = s.fleet_console_url.clone();
        let set = !s.fleet_console_token.is_empty();
        drop(s);
        {
            let mut st = self.state.lock().unwrap();
            st.settings.fleet_console_url = url.clone();
            st.settings.fleet_token_set = set;
        }
        // the secrets order (the product team's fault of record, 16 October 2026: the token directory stood whole in the log and in
        // the Settings captures sent for the audit): the log names the console's host and the token's last four alone
        self.log(&format!("fleet console: {}{}", if url.is_empty() { "off".to_string() } else { fleet_url_for_log(&url) }, if set { ", a token held" } else { ", no token" }));
        Ok(json!({ "ok": true, "fleet_console_url": url, "fleet_token_set": set }))
    }

    pub fn apply_settings(&self, identities: Option<u32>, vote: Option<bool>, login: Option<bool>, address: Option<&str>, display_name: Option<&str>, dev_fee: Option<bool>, proof_verify_trust: Option<bool>, profile_public: Option<bool>) -> Result<Value, String> {
        let mut restart = Vec::new();
        let mut restart_node: Option<String> = None;
        {
            let mut s = self.settings.lock().unwrap();
            if let Some(n) = display_name {
                let n: String = n.trim().chars().take(40).collect();
                s.display_name = n.clone();
                self.state.lock().unwrap().display_name = if n.is_empty() { self.runtime.host.clone() } else { n };
            }
            if let Some(n) = identities {
                if n != s.identities {
                    s.identities = n;
                    restart.push(format!("{n} identities"));
                }
            }
            if let Some(v) = vote {
                if v != s.vote {
                    s.vote = v;
                    restart.push(if v { "voting on".into() } else { "voting off".into() });
                }
            }
            if let Some(v) = dev_fee {
                if v != s.dev_fee {
                    s.dev_fee = v;
                    restart.push(if v { "dev fee on (1 block in 100)".into() } else { "dev fee off".into() });
                }
            }
            if let Some(v) = proof_verify_trust {
                if v != s.proof_verify_trust {
                    s.proof_verify_trust = v;
                    restart_node = Some(if v { "proof trust mode on (devnet only)".into() } else { "proof trust mode off".into() });
                }
            }
            if let Some(v) = profile_public {
                if v != s.profile_public {
                    s.profile_public = v;
                    self.event("info", if v { "public profile on: your key ids, blocks, weight rank and card model may be shown on igneum.network" } else { "public profile off: the address page stays unlisted" });
                }
            }
            if let Some(a) = address {
                let a = a.trim().to_ascii_lowercase();
                if !a.is_empty() && a != s.address {
                    if !keys::valid_address(&a) {
                        return Err("an address is 0x followed by 40 hex characters".into());
                    }
                    let generated = keys::load(&self.wallet_path).map(|w| w.address == a).unwrap_or(false);
                    s.address = a.clone();
                    s.address_source = if generated { "generated".into() } else { "pasted".into() };
                    s.key_saved = true;
                    restart.push(format!("rewards to {}", keys::checksum(&a)));
                }
            }
            s.save(&self.settings_path);
            let mut st = self.state.lock().unwrap();
            st.settings.identities = s.identities;
            st.settings.vote = s.vote;
            st.settings.dev_fee = s.dev_fee;
            st.settings.proof_verify_trust = s.proof_verify_trust;
            st.settings.profile_public = s.profile_public;
            st.dev_fee.on = s.dev_fee;
            st.dev_fee.percent = if s.dev_fee { 1 } else { 0 };
            st.address = address_state(&s, &self.wallet_path);
        }
        if let Some(on) = login {
            crate::platform::set_start_at_login(on)?;
            self.state.lock().unwrap().settings.start_at_login = crate::platform::start_at_login_is_on();
            self.event("info", if on { "starts at login from now on" } else { "no longer starts at login" });
        }
        if !restart.is_empty() {
            self.send(Cmd::RestartMiners(restart.join(", ")));
        }
        if let Some(why) = restart_node.clone() {
            self.send(Cmd::RestartNode(why));
        }
        Ok(json!({ "ok": true, "restart": !restart.is_empty(), "restart_node": restart_node.is_some() }))
    }
}

fn address_state(s: &Settings, wallet_path: &std::path::Path) -> crate::state::AddressState {
    crate::state::AddressState {
        value: s.address.clone(),
        display: if s.address.is_empty() { String::new() } else { keys::checksum(&s.address) },
        source: s.address_source.clone(),
        key_saved: s.key_saved,
        wallet_file: if s.address_source == "generated" { wallet_path.display().to_string() } else { String::new() },
        balance_wei: None,
        balance_age_s: -1.0,
        balance_note: String::new(),
        price_gbp_per_ign: None,
    }
}

/// The node's consensus params digest out of its own line ("Consensus params digest: <64 hex> (exchanged in the p2p
/// handshake; ...)"). None for any other line, including the WARN lines that quote a peer's digest.
pub fn digest_from_line(line: &str) -> Option<String> {
    let rest = line.split("Consensus params digest:").nth(1)?.trim_start();
    let hex: String = rest.chars().take_while(|c| c.is_ascii_hexdigit()).collect();
    (hex.len() == 64).then(|| hex.to_ascii_lowercase())
}

/// The planned rule changes in an override file: every `<name>_activation_daa` key, lowest height first, with plain
/// words for the name ("fees_v1" -> "Fees v1"). Keys that are not activation heights are left out.
pub fn switches_of(v: &Value) -> Vec<crate::state::ConsensusSwitch> {
    let mut out: Vec<crate::state::ConsensusSwitch> = v
        .as_object()
        .map(|o| {
            o.iter()
                .filter_map(|(k, val)| {
                    let stem = k.strip_suffix("_activation_daa")?;
                    let daa = val.as_u64()?;
                    let mut words: Vec<String> = stem.split('_').map(|w| w.to_string()).collect();
                    if let Some(first) = words.first_mut() {
                        let mut c = first.chars();
                        if let Some(f) = c.next() {
                            *first = f.to_ascii_uppercase().to_string() + c.as_str();
                        }
                    }
                    Some(crate::state::ConsensusSwitch { key: k.clone(), name: words.join(" "), daa })
                })
                .collect()
        })
        .unwrap_or_default();
    out.sort_by_key(|s| (s.daa, s.key.clone()));
    out
}

/// One value out of `key=value` tokens of a log line.
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

fn fmt_uptime(s: u64) -> String {
    format!("{:02}:{:02}:{:02}", s / 3600, s % 3600 / 60, s % 60)
}

fn port_open(port: u16) -> bool {
    std::net::TcpStream::connect_timeout(&std::net::SocketAddr::from(([127, 0, 0, 1], port)), Duration::from_millis(800)).is_ok()
}

fn jitter_secs(seed: u64) -> u64 {
    // 5 to 60 s, from the clock: enough spread for a restart, no rng needed
    5 + (seed.wrapping_mul(2654435761) >> 7) % 56
}

/// The `--prepare-packs` directory the miner gets, relative to the app data folder (its cwd), in the platform's
/// separator: `packs\prepare` on Windows, `packs/prepare` elsewhere. Every worker gets it (program class v3).
pub(crate) fn prepare_packs_arg() -> String {
    if cfg!(windows) { "packs\\prepare".to_string() } else { "packs/prepare".to_string() }
}

/// Seconds after a resume before every enabled card must be mining (a worker takes 10 to 60 s to its first STATUS
/// line with a hash rate; the pack export before it a few seconds more).
pub(crate) const RESUME_CHECK_SECS: u64 = 90;

/// What the resume rule reads from a miner slot.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct ResumeSlot {
    /// the watchdog marked the card faulted
    pub faulted: bool,
    /// a worker process is alive on the slot
    pub live: bool,
}

/// The resume rule: every slot without a live worker is re-armed, faulted or not. (The rule before 5 October 2026
/// re-armed only faulted slots; `stop_miners("paused")` had cleared every slot's `restart_at`, so a healthy paused
/// card never restarted: the second Windows machine at 21:25:11Z, the Mac that afternoon.)
pub(crate) fn slots_to_rearm_on_resume(slots: &[ResumeSlot]) -> Vec<usize> {
    slots.iter().enumerate().filter(|(_, s)| !s.live).map(|(i, _)| i).collect()
}

/// The check RESUME_CHECK_SECS after a resume: every enabled, present, non-faulted card must report a hash rate
/// above 0 or its name is returned with its state (the caller logs one line per card).
pub(crate) fn resume_check(cards: &[CardState]) -> Vec<String> {
    cards
        .iter()
        .filter(|c| c.enabled && c.present() && c.state != "faulted" && c.hash_now <= 0.0)
        .map(|c| format!("{} is not mining {RESUME_CHECK_SECS} s after resume (state {}, pid {}{})", c.name, c.state, c.pid, if c.message.is_empty() { String::new() } else { format!(", {}", c.message) }))
        .collect()
}

struct MinerSlot {
    card: usize,
    label: String,
    proc: Option<Proc>,
    worker: PathBuf,
    restart_at: Option<Instant>,
    starts: u32,
    restarts: u32,
    building: bool,
    /// MF-14: when the export or build was asked, and its sequence (a result from an earlier ask is ignored)
    building_since: Option<Instant>,
    build_seq: u64,
    needs_rebuild: bool,
    prepared: bool, // the pack is exported (and the worker built) for the next start
    last_status: Option<Instant>,
    error_at: Option<Instant>,
    /// the per-card watchdog (src/watchdog.rs): restarts on the ladder, never a permanent fault
    watch: crate::watchdog::CardWatch,
    /// pack refusals (src/watchdog.rs): the pack is exported again before the restart, capped per epoch
    pack_rebuilds: crate::watchdog::PackRebuilds,
    /// the next pack export is forced (a worker refused the pack); otherwise an export under 60 s old is reused (MF-4)
    pack_force: bool,
}

pub struct Engine {
    shared: Arc<Shared>,
    bins: Bins,
    rx: Receiver<Cmd>,
    lines_rx: Receiver<Line>,
    lines_tx: Sender<Line>,
    node: Option<Proc>,
    node_external: bool,
    /// the merge check (src/merge.rs): the network's sinks as last seen, when one of our blocks last appeared in the
    /// network's view, the merge depth from the node's params, when mining with a synced node began, the last poll
    /// the ports are held by a node this app refused (other rules or network): re-checked, the app takes the ports when it leaves
    node_refused: bool,
    /// when the other node's port stopped answering (engine seconds), None while it answers
    external_gone_since: Option<f64>,
    node_info_at: Instant,
    node_restart_at: Option<Instant>,
    /// the last host-setup plan said (src/verifier.rs HostSetup), so a held node start says each step once
    host_setup_said: Option<String>,
    memory_hold_said: bool,   // the fresh-sync memory hold, said once as an event (9 October 2026)
    /// resume rule (5 October 2026): when due, every enabled card must be mining or its name goes to the log
    resume_check_at: Option<Instant>,
    node_started_at: Instant,
    node_starts: u32,
    node_restarts: u32,
    node_log: Option<PathBuf>,
    node_last_reading: Option<Instant>,
    sync_prev: Option<u64>,
    sync_stable_since: Option<Instant>,
    last_sync_check: Instant,
    no_peer_warned: bool,
    watch: Option<Proc>,
    watch_retry_at: Instant,
    miners: Vec<MinerSlot>,
    running: bool,
    quitting: bool,
    keep_awake: Option<crate::platform::KeepAwake>,
    last_awake: Instant,
    last_status: Instant,
    last_upload: Instant,
    upload_thread: Option<std::thread::JoinHandle<()>>,
    ota: crate::ota::Updater,
    /// driver-check: the manifest's table as last read from <app data>/drivers.json (None = none published)
    drivers_table: Option<crate::drivers::Table>,
    driver_busy: bool,
    /// the vendor's cards held off for a driver install, put back as they were when it ends
    driver_held: Vec<CardChoice>,
    /// the interface over the air (src/uiota.rs)
    uiota: crate::uiota::UiOta,
    /// the worker layer (src/sup.rs, docs/product-a/OLD-BASE.md step 2): the restart's supervisor owns the cards
    /// (protocol v0 workers, the in-process verifier, the transition state machine, the keyed share path, pools and
    /// failover); built beside the old miner path in this piece, ticked from the engine's tick
    sup: igneum_supervisor::Supervisor,
    /// The supervisor's log drained into the app's log each tick (Supervisor::log_since): the last seq read.
    sup_log_seq: u64,
    jobs: crate::jobrun::Jobs,
    /// a remote job has the miners stopped; they restart when the runner lets go
    job_hold: bool,
    /// F14: the running version is unsupported and automatic updates are off (src/ota.rs sets update.hold_mining)
    unsupported_hold: bool,
    stamp: String,
    label_base: String,
    wrapper: bool,
    last_state_print: Instant,
    detected: bool,
    /// hot-plug (src/hotplug.rs): an enumeration thread is running; one more was asked for meanwhile; when the
    /// next periodic one is due; when the app log last carried the card list
    detect_busy: bool,
    detect_again: bool,
    detect_next: Instant,
    last_cards_line: Instant,
    clock_samples: Vec<f64>,
    clock_node_at: Option<Instant>,
    clock_node_behind: f64,
    clock_https: Option<(f64, Instant)>,
    clock_next_https: Instant,
    clock_last_sample: Instant,
    last_accepted: Option<Instant>,
    telemetry: Option<Proc>,
    telemetry_retry_at: Instant,
    amd_telemetry: Option<Proc>,
    amd_telemetry_retry_at: Instant,
    power_busy: bool,
    power_restore_pending: bool,
    /// a cap the card did not take: how many times in a row, and when to ask again (cap_refusal_plan)
    cap_refusals: u32,
    cap_retry_at: Option<Instant>,
    /// an elevated step handed to the window host: (command line, what, requested watts per device, since)
    power_via_host: Option<(String, String, std::collections::HashMap<String, f64>, Instant)>,
    /// the manifest's consensus override changed: restart the node at a safe moment
    node_override_restart: bool,
    /// this node build refused the override file (an older igneumd without the field): start without it
    node_override_unusable: bool,
    stability: std::collections::HashMap<usize, Stability>,
    last_stability: Instant,
    last_settings_save: Instant,
    last_error_event: Instant,
    // the efficiency sweep (src/sweep.rs): one card at a time
    sweep: Option<crate::ember::Run>,
    /// who asked for the quit (the log's `quit:` line names it)
    quit_source: &'static str,
    /// the over-the-air updater never runs: IGNEUM_APP_NO_OTA=1 or --sweep (a second engine beside the installed app)
    no_ota: bool,
    /// the Igneum Power Helper task is registered (src/powertask.rs): once, ever; None = not asked yet
    power_task: Option<bool>,
    /// the running tune helper is the task (quit ends it; no SweepHelperDone comes from a thread)
    sweep_helper_is_task: bool,
    /// the request number the vendor tool last carried out (the run's acknowledgement)
    tune_acked: Option<u64>,
    /// cards whose confirm check found a better neighbour: the full plan runs next
    tune_full_due: std::collections::HashSet<usize>,
    /// Miner UI 4: the balance read (every 30 s while the node runs; 60 s after a failure)
    balance_next: Option<Instant>,
    balance_busy: bool,
    balance_at: f64,
    /// The payout address's balance at this session's first read (wei), the share lane's credit baseline; None
    /// until the first read and again at every start
    balance_base: Option<u128>,
    /// F-E20: the supervisor's chain-figures poll started once for this engine's life.
    chain_poll_started: bool,
    /// A worker layer that refused to start (the pool's subscribe answering no document within the connect's wait,
    /// the mini on 12 October 2026 at 09:31 UK under the pool's roll) is tried again from the tick while the engine
    /// runs: the next try's time; None while the layer is up or the engine stopped.
    layer_retry_at: Option<Instant>,
    /// a sweep waiting for the cap-mode probe: (card index, forced)
    sweep_pending: Option<(usize, bool)>,
    /// how caps are set: Some(true) directly, Some(false) through the elevated helper, None = not probed yet
    sweep_direct: Option<bool>,
    /// the elevated helper is running (its thread reports SweepHelperDone when it ends)
    sweep_helper: bool,
    sweep_seq: u64,
    /// cards asked for by hand (Sweep now) or by --sweep, by key
    sweep_queue: Vec<String>,
    sweep_next_check: Instant,
    /// after an abort, a card waits before the scheduler tries it again
    sweep_retry: std::collections::HashMap<usize, Instant>,
    sweep_attempts: std::collections::HashMap<usize, u32>,
    /// the node watchdog (src/watchdog.rs): a silent node of ours is restarted
    node_watch: crate::watchdog::NodeWatch,
    /// the watchdogs' clock origin (they take seconds)
    t0: Instant,
    /// Ember Heat (src/heat.rs): the loop, whether the miners are stopped for a rest and since when, the cards'
    /// draw the last time they heated, the last hour of (unix s, heating) samples for the duty readout, the last
    /// HEAT log line's time, and the typed reading the loop last saw (a new one may teach the idle offset)
    heat: crate::heat::Controller,
    heat_rest: bool,
    heat_rest_since: Option<Instant>,
    heat_full_w: f64,
    heat_samples: std::collections::VecDeque<(f64, bool)>,
    heat_log_at: f64,
    heat_room_seen: f64,
    /// the last half minute of (unix s, coolest card sensor) for the cooling-tail slope (heat::room)
    heat_card_samples: std::collections::VecDeque<(f64, f64)>,
    /// node readiness (the founder, 7 October 2026): a worker never starts before the node is synced AND its execution layer
    /// reports an executed tip (igneum_getExecStatus); probed every 5 s off the engine thread
    exec_ready: bool,
    exec_probe_busy: bool,
    exec_probe_next: Instant,
    /// the last time the node's RPC answered anything (a sign of life for the node watchdog)
    exec_answered_at: Option<Instant>,
    /// the node has been read as synced at least once since its start: before that its catch-up never counts
    node_settled: bool,
    /// fault reports sent to the log intake in the last hour (the cap)
    fault_reports: Vec<Instant>,
    /// MF-6: the job that took the miners hold, when, and its cap in seconds
    job_hold_owner: Option<String>,
    /// the one log line about the ignored override on a suffixed devnet (node_override_file)
    override_ignored_logged: std::sync::atomic::AtomicBool,
    /// the choices that give the under-12 GB cards' miners back when the prover stops (settings.prove_instead)
    job_hold_since: Option<Instant>,
    job_hold_cap_s: f64,
    /// MF-7: the orphan sweep (igneum-miner processes this engine does not track) runs at this time next
    orphan_sweep_next: Instant,
}

/// The fleet console URL as the log and the window show it: the console's host and the path up to the token directory,
/// the token reduced to its last four characters (a token under nine characters is dropped whole). The token directory
/// is the console's access secret (the secrets order: no secret printed, committed or sent; the log goes into support messages).
pub fn fleet_url_for_log(url: &str) -> String {
    let trimmed = url.trim_end_matches('/');
    let Some(slash) = trimmed.rfind('/') else { return "...".to_string() };
    let head = &trimmed[..slash];
    if !head.contains("://") { return "...".to_string() }
    let token = &trimmed[slash + 1..];
    let tail = if token.len() >= 9 { &token[token.len() - 4..] } else { "" };
    format!("{head}/...{tail}{}", if url.ends_with('/') { "/" } else { "" })
}

impl Engine {
    pub fn new(shared: Arc<Shared>, bins: Bins, rx: Receiver<Cmd>, wrapper: bool) -> Engine {
        let no_ota = shared.runtime.sweep_only || std::env::var("IGNEUM_APP_NO_OTA").map(|v| v == "1").unwrap_or(false);
        let (lines_tx, lines_rx) = channel();
        let now = Instant::now();
        let stamp = {
            let t = crate::platform::unix_now();
            // yyyymmdd-hhmmss in UTC, without a date crate
            let days = t / 86400;
            let (y, m, d) = civil_from_days(days as i64);
            format!("{y:04}{m:02}{d:02}-{:02}{:02}{:02}", t % 86400 / 3600, t % 3600 / 60, t % 60)
        };
        // labels never carry the hostname: two cloned PCs with the same COMPUTERNAME would sign with the same keys
        let label_base = format!("{}-{}", if cfg!(target_os = "macos") { "mac" } else { "win" }, shared.runtime.id8());
        let ota = crate::ota::Updater::new(&shared);
        let uiota = crate::uiota::UiOta::new(&shared);
        let jobs = crate::jobrun::Jobs::new(&shared);
        // the dev fee (ED-05): the settings' switch (default on) against the signed manifest beside the workers
        let dev_fee_on = shared.settings.lock().map(|s| s.dev_fee).unwrap_or(true);
        let sup = crate::sup::build_with_dev_fee(VERSION, &bins.dir, &shared.runtime.app_dir, dev_fee_on);
        Engine {
            shared,
            bins,
            rx,
            lines_rx,
            lines_tx,
            sup,
            sup_log_seq: 0,
            node: None,
            node_external: false,
            node_refused: false,
            external_gone_since: None,
            node_info_at: Instant::now() - Duration::from_secs(25),
            node_restart_at: None,
            host_setup_said: None,
            memory_hold_said: false,
            resume_check_at: None,
            node_started_at: now,
            node_starts: 0,
            node_restarts: 0,
            node_log: None,
            node_last_reading: None,
            sync_prev: None,
            sync_stable_since: None,
            last_sync_check: now,
            no_peer_warned: false,
            watch: None,
            watch_retry_at: now,
            miners: Vec::new(),
            running: false,
            quitting: false,
            keep_awake: None,
            last_awake: now,
            last_status: now,
            last_upload: now,
            upload_thread: None,
            drivers_table: ota.drivers_table(),
            driver_busy: false,
            driver_held: Vec::new(),
            ota,
            uiota,
            jobs,
            job_hold: false,
            unsupported_hold: false,
            stamp,
            label_base,
            wrapper,
            last_state_print: now,
            detected: false,
            detect_busy: false,
            detect_again: false,
            detect_next: now + Duration::from_secs(crate::hotplug::POLL_S),
            last_cards_line: now,
            clock_samples: Vec::new(),
            clock_node_at: None,
            clock_node_behind: 0.0,
            clock_https: None,
            clock_next_https: now + Duration::from_secs(5),
            clock_last_sample: now,
            last_accepted: None,
            telemetry: None,
            telemetry_retry_at: now,
            amd_telemetry: None,
            amd_telemetry_retry_at: now,
            power_busy: false,
            power_restore_pending: false,
            cap_refusals: 0,
            cap_retry_at: None,
            power_via_host: None,
            node_override_restart: false,
            node_override_unusable: false,
            stability: std::collections::HashMap::new(),
            last_stability: now,
            last_settings_save: now,
            last_error_event: now - Duration::from_secs(600),
            sweep: None,
            quit_source: "unknown",
            no_ota,
            power_task: None,
            sweep_helper_is_task: false,
            tune_acked: None,
            tune_full_due: std::collections::HashSet::new(),
            balance_next: None,
            balance_busy: false,
            balance_at: 0.0,
            balance_base: None,
            chain_poll_started: false,
            layer_retry_at: None,
            sweep_pending: None,
            sweep_direct: None,
            sweep_helper: false,
            sweep_seq: 0,
            sweep_queue: Vec::new(),
            sweep_next_check: now + Duration::from_secs(20),
            sweep_retry: std::collections::HashMap::new(),
            sweep_attempts: std::collections::HashMap::new(),
            node_watch: crate::watchdog::NodeWatch::new(),
            exec_ready: false,
            exec_probe_busy: false,
            exec_probe_next: now,
            exec_answered_at: None,
            node_settled: false,
            fault_reports: Vec::new(),
            job_hold_owner: None,
            override_ignored_logged: std::sync::atomic::AtomicBool::new(false),
            job_hold_since: None,
            job_hold_cap_s: 3600.0,
            orphan_sweep_next: now,
            t0: now,
            heat: crate::heat::Controller::new(),
            heat_rest: false,
            heat_rest_since: None,
            heat_full_w: 0.0,
            heat_samples: std::collections::VecDeque::new(),
            heat_log_at: 0.0,
            heat_room_seen: 0.0,
            heat_card_samples: std::collections::VecDeque::new(),
        }
    }

    fn st(&self) -> std::sync::MutexGuard<'_, State> {
        self.shared.state.lock().unwrap()
    }

    /// Seconds on the watchdogs' clock.
    fn secs(&self, now: Instant) -> f64 {
        now.duration_since(self.t0).as_secs_f64()
    }

    pub fn run(mut self) {
        let setup_done = self.shared.settings.lock().unwrap().setup_done;
        self.shared.log(&format!(
            "Ember {} on {} (machine id {}), {} (run {}-{})",
            VERSION,
            self.shared.runtime.host,
            self.shared.runtime.machine_id,
            self.st().chain,
            self.label_base,
            self.stamp
        ));
        self.shared.log("Nobody from Igneum will ever ask for your seed. Nothing is bought or sold on this network.");
        self.shared.log(&format!(
            "node: RPC 127.0.0.1:{}, p2p 0.0.0.0:{}, peers {}, data {}",
            self.shared.runtime.rpc_port,
            self.shared.runtime.p2p_port,
            if self.shared.runtime.peers.is_empty() { "none".to_string() } else { self.shared.runtime.peers.join(" ") },
            self.shared.runtime.node_dir.display()
        ));
        let v = crate::detect::node_version(&self.bins.node);
        self.st().node.version = v.clone();
        self.shared.log(&self.shared.upload_header());
        // MF-11 (7 October 2026): the first act after an update is the read-back line, so the intake shows the new
        // version up before anything else happens; the shipper's rollout table reads it per machine
        if let Some(from) = self.ota.updated_from() {
            self.shared.log(&format!("update-return: app {VERSION} up after the update from {from} (node {v}, machine {})", self.shared.runtime.id8()));
            // the founder's rule (8 October 2026, 18:0x UK): an update never leaves mining off, no click after an install.
            // A pause the previous version saved (a job's api/pause, the user's own) does not outlive the update
            let (paused, by) = { let st = self.shared.settings.lock().unwrap(); (st.paused, st.paused_by.clone()) };
            if let Some(line) = resume_after_update(Some(&from), paused, &by) {
                self.shared.settings.lock().unwrap().paused = false;
                self.shared.save_settings();
                self.st().mining.paused = false;
                self.shared.log(&format!("update-return: {line}"));
                self.shared.event("ok", &line);
            }
        }
        // a PC that lost power or hard-reset is a fault row at its next start, not a silence (the second Windows machine, 7 October 2026)
        if let Some(line) = crate::bootcheck::unexpected_restart() {
            self.shared.log(&format!("FAULT pc-restart: {line}"));
            self.shared.event("error", &format!("This PC restarted without a shutdown: {line}. Mining resumed; if it happens again check the power supply and the wall."));
        }
        // which intake and which downloads folder this build reports to and checks (fingerprints, never the values;
        // rotation phase 2 reads this line from every machine's upload: docs/plans/rotation-phase-2.md)
        self.shared.log(&self.shared.packaged.describe());
        if let Some(w) = self.shared.packaged.edition_mismatch() {
            self.shared.log(&w);
            let mut st = self.shared.state.lock().unwrap();
            st.update.error = w;
            st.update.status = "error".into();
        }
        self.shared.log(&format!("node binary: {} ({v})", self.bins.node.display()));
        if self.ota.needs_rollback() {
            // this version died twice before it was healthy: the helper puts the previous one back
            match self.ota.launch_rollback(&self.shared, self.host_pid()) {
                Ok(()) => {
                    self.quitting = true;
                    self.st().quitting = true;
                }
                Err(e) => self.shared.event("error", &format!("rollback not possible: {e}")),
            }
        }
        self.shared.send(Cmd::Detect);
        if self.shared.runtime.sweep_only {
            self.shared.log("--sweep: the efficiency sweep runs on every supported card as soon as it mines; the table goes to stdout and this log; the engine quits after it");
        }
        if self.no_ota {
            self.shared.log("updates: off for this engine (IGNEUM_APP_NO_OTA or --sweep): it never downloads or installs, whatever the manifest says (C35)");
        }
        if (setup_done || self.shared.runtime.sweep_only) && !self.quitting {
            self.shared.send(Cmd::Start);
        }
        loop {
            while let Ok(c) = self.rx.try_recv() {
                self.command(c);
            }
            while let Ok(l) = self.lines_rx.try_recv() {
                self.line(l);
            }
            if self.quitting {
                self.shutdown();
                return;
            }
            self.tick();
            std::thread::sleep(Duration::from_millis(250));
        }
    }

    fn command(&mut self, c: Cmd) {
        match c {
            Cmd::Detect => {
                // the first run, /api/detect, the host's "detect" on a device change, and the minute poll all land
                // here; one enumeration at a time, and a request during one runs once more after it
                if self.detect_busy {
                    self.detect_again = true;
                    return;
                }
                self.detect_busy = true;
                if !self.detected {
                    self.st().detecting = true;
                }
                let bins = self.bins.clone();
                let shared = self.shared.clone();
                std::thread::spawn(move || {
                    let d = crate::detect::detect(&bins);
                    shared.send(Cmd::Detected(d));
                });
            }
            Cmd::Detected(d) => {
                self.detect_busy = false;
                self.detect_next = Instant::now() + Duration::from_secs(crate::hotplug::POLL_S);
                if self.detected {
                    self.merge_detection(d);
                } else {
                    self.first_detection(d);
                }
                if self.detect_again {
                    self.detect_again = false;
                    self.shared.send(Cmd::Detect);
                }
                // driver-check: every card's driver against the manifest's table, at first run and every detection
                self.refresh_driver_offers();
                // a card the driver install took away is back: its worker returns
                self.driver_release_held(true);
            }
            Cmd::ApplyCards(choices) => self.apply_cards(choices),
            Cmd::DriverInstall(vendor) => self.driver_install(&vendor),
            Cmd::DriverRestart => match crate::drivers::restart_now() {
                Ok(()) => self.shared.event("info", "restarting Windows in 20 s to finish the driver install (your click)"),
                Err(e) => self.shared.event("error", &format!("restart: {e}")),
            },
            Cmd::Driver(ev) => self.driver_event(ev),
            Cmd::Start => self.start(),
            Cmd::Pause(by) => {
                if !self.running {
                    return;
                }
                { let mut st = self.shared.settings.lock().unwrap(); st.paused = true; st.paused_by = by; }
                self.shared.save_settings();
                self.st().mining.paused = true;
                self.stop_miners("paused");
                if crate::sup::layer_on() {
                    self.sup.pause();
                }
                self.shared.event("info", "mining paused; the card is idle, the pool session stays open");
            }
            Cmd::Resume => {
                self.shared.settings.lock().unwrap().paused = false;
                self.shared.save_settings();
                self.st().mining.paused = false;
                if crate::sup::layer_on() {
                    self.sup.resume();
                }
                self.shared.event("ok", "mining resumed");
                // the resume rule (5 October 2026, the second Windows machine at 21:25:11Z and the Mac that afternoon): stop_miners("paused")
                // cleared every slot's restart_at and this arm re-armed only FAULTED slots, so a card whose worker had
                // simply been stopped stayed "off" at 0 MH/s until the app was relaunched. Now every slot without a
                // live worker is re-armed (a faulted one reset first), its pack is exported again before the start
                // (prepared = false: the hour may have turned while paused), and a check 90 s later names any
                // enabled card that is not mining (`resume_check`).
                let views: Vec<ResumeSlot> = self.miners.iter().map(|m| ResumeSlot { faulted: m.watch.watchdog_restarts() > 0, live: m.proc.is_some() }).collect();
                let now = Instant::now();
                for i in slots_to_rearm_on_resume(&views) {
                    let m = &mut self.miners[i];
                    // a user action: the ladder starts over
                    m.watch.event(0.0, crate::watchdog::Event::Reset);
                    m.restart_at = Some(now);
                    m.prepared = false;
                }
                self.resume_check_at = Some(now + Duration::from_secs(RESUME_CHECK_SECS));
                if !self.running {
                    self.start();
                }
            }
            Cmd::RestartMiners(why) => {
                self.shared.event("info", &format!("settings changed ({why}); the miners restart"));
                self.stop_miners("settings changed");
                if crate::sup::layer_on() {
                    self.restart_layer("settings changed");
                }
                for m in self.miners.iter_mut() {
                    // a user action: the ladder starts over
                    m.watch.event(0.0, crate::watchdog::Event::Reset);
                    m.restart_at = Some(Instant::now());
                }
            }
            Cmd::RestartNode(why) => {
                if self.node_external {
                    self.shared.event("info", &format!("{why}; the node is external, so the app cannot restart it"));
                } else if self.node.is_some() || self.node_restart_at.is_some() {
                    self.shared.event("info", &format!("{why}; the node restarts"));
                    self.restart_node(&why, Duration::from_secs(2));
                }
            }
            Cmd::CheckUpdate => {
                if self.no_ota {
                    self.shared.event("info", "updates are off for this engine (a measurement run: IGNEUM_APP_NO_OTA or --sweep)");
                } else {
                    self.ota.check_now(&self.shared);
                }
            }
            Cmd::InstallUpdate => self.ota.install_now(&self.shared),
            Cmd::AutoUpdate(on) => self.ota.set_auto(&self.shared, on),
            Cmd::OpenUpdateFile => {
                if let Err(e) = self.ota.open_file() {
                    self.shared.event("error", &format!("could not open the update file: {e}"));
                }
            }
            Cmd::Ota(ev) => self.ota.event(&self.shared, ev),
            Cmd::UiOta(ev) => self.uiota.event(&self.shared, ev),
            Cmd::UiPageLoaded(v) => self.uiota.page_loaded(&v, Instant::now()),
            Cmd::UiHealth(err) => self.uiota.health(&self.shared, err.as_deref()),
            Cmd::UiBuiltin(on) => self.uiota.set_builtin(&self.shared, on),
            Cmd::Job(ev) => {
                if let Some(a) = self.jobs.event(&self.shared, ev) {
                    self.job_action(a);
                }
            }
            Cmd::JobsAllow(on) => self.jobs.set_allowed(&self.shared, on),
            Cmd::JobsCheck => {
                if let Some(a) = self.jobs.check_now(&self.shared) {
                    self.job_action(a);
                }
            }
            Cmd::WorkerBuilt(card, seq, result) => {
                if let Some(m) = self.miners.iter_mut().find(|m| m.card == card) {
                    if seq != m.build_seq {
                        // a late answer from an export the wait rule already gave up on (MF-14): nothing to apply
                        self.shared.log(&format!("miner {}: a late export result (ask {seq}, now {}) ignored", m.label, m.build_seq));
                        return;
                    }
                    m.building = false;
                    m.building_since = None;
                    match result {
                        Ok(p) => {
                            if p != m.worker {
                                self.shared.event("build", "GPU worker built from source");
                            }
                            m.worker = p;
                            m.needs_rebuild = false;
                            m.prepared = true;
                            m.restart_at = Some(Instant::now());
                        }
                        Err(e) => {
                            // MF-14: on the ladder, the reason on the row, never a flat five minutes
                            let attempt = m.watch.watchdog_restarts().saturating_add(1);
                            let delay = crate::watchdog::retry_delay_s(attempt);
                            self.shared.event("error", &format!("worker build failed: {e}; retry in {delay} s"));
                            m.restart_at = Some(Instant::now() + Duration::from_secs(delay));
                            if let Some(c) = self.st().mining.cards.get_mut(card) {
                                c.state = "restarting".into();
                                c.restart_in_s = delay;
                                c.message = format!("{e}; retry in {delay} s");
                            }
                        }
                    }
                }
            }
            Cmd::ExecProbe { answered, has_record } => {
                self.exec_probe_busy = false;
                if answered {
                    self.exec_answered_at = Some(Instant::now());
                }
                if has_record && !self.exec_ready {
                    self.shared.log("node readiness: the execution layer reports an executed tip; the workers may start once the node is synced");
                }
                if !has_record && self.exec_ready {
                    self.shared.log("node readiness: the execution layer reports no executed tip any more (a reset or a restart); the workers wait for it");
                }
                self.exec_ready = has_record;
            }
            Cmd::ClockSample(d) => {
                self.clock_samples.push(d);
                if self.clock_samples.len() > 9 {
                    self.clock_samples.remove(0);
                }
                self.resolve_clock();
            }
            Cmd::ClockHttps(r) => {
                if let Some(d) = r {
                    self.clock_https = Some((d, Instant::now()));
                    self.shared.log(&format!("clock check (https): local time is {:+.1} s against dl.igneum.network", d));
                } else {
                    self.shared.log("clock check (https): no answer from dl.igneum.network");
                }
                self.resolve_clock();
            }
            Cmd::ClockCheck => {
                self.clock_next_https = Instant::now() + Duration::from_secs(600);
                if let Some(f) = std::env::var("IGNEUM_APP_FAKE_SKEW").ok().and_then(|v| v.parse::<f64>().ok()) {
                    self.command(Cmd::ClockHttps(Some(f)));
                    return;
                }
                let shared = self.shared.clone();
                std::thread::spawn(move || {
                    let r = crate::update::https_time("https://dl.igneum.network/").map(|t| crate::platform::unix_now_f() - t);
                    shared.send(Cmd::ClockHttps(r));
                });
            }
            Cmd::ClockSync => {
                if self.st().clock.syncing {
                    return;
                }
                self.st().clock.syncing = true;
                self.shared.event("info", "asking the system to set its clock from a time server");
                let shared = self.shared.clone();
                std::thread::spawn(move || {
                    let r = crate::platform::sync_clock();
                    shared.send(Cmd::ClockSynced(r));
                });
            }
            Cmd::ClockSynced(r) => {
                let mut st = self.st();
                st.clock.syncing = false;
                match &r {
                    Ok(t) => st.clock.sync_result = t.clone(),
                    Err(e) => st.clock.sync_result = format!("could not set the clock: {e}"),
                }
                drop(st);
                match r {
                    Ok(t) => self.shared.event("ok", &format!("clock: {t}; checking again")),
                    Err(e) => self.shared.event("error", &format!("clock: {e}")),
                }
                // the sources answer again: forget the old samples and re-check
                self.clock_samples.clear();
                self.clock_node_at = None;
                self.clock_https = None;
                self.resolve_clock();
                self.clock_next_https = Instant::now() + Duration::from_secs(6);
            }
            Cmd::PowerApplied(what, r, readback) => {
                self.power_task = None; // the one elevated step may have registered the task: ask again next time
                self.power_busy = false;
                self.power_via_host = None;
                // the truth is what nvidia-smi reads back, not whether the prompt said yes
                let mut applied = Vec::new();
                let mut missing = Vec::new();
                {
                    let mut st = self.st();
                    for c in st.mining.cards.iter_mut().filter(|c| c.vendor == "nvidia" && c.enabled && c.present() && c.power_default_w > 0.0) {
                        let want = requested_watts(c);
                        let got = readback.get(&c.device).copied().unwrap_or(c.power_limit_w);
                        if got > 0.0 {
                            c.power_limit_w = got;
                        }
                        if got > 0.0 && (got - want).abs() < 1.0 {
                            c.power_applied = true;
                            c.power_note = format!("power cap: {} W applied", want as u64);
                            applied.push(format!("{} {} W", c.name, want as u64));
                        } else {
                            c.power_applied = false;
                            c.power_note = format!("power cap NOT applied (needs the administrator prompt): card reports {} W, wanted {} W", got as u64, want as u64);
                            missing.push(format!("{} reports {} W, wanted {} W", c.name, got as u64, want as u64));
                        }
                    }
                }
                if !applied.is_empty() {
                    self.shared.event("ok", &format!("GPU power cap in force: {}", applied.join(", ")));
                }
                if !missing.is_empty() {
                    let why = match &r {
                        Ok(()) => "the step ran but the card did not take it".to_string(),
                        Err(e) => e.clone(),
                    };
                    if why.starts_with("power control: helper not running") {
                        self.shared.event("error", &short(&why, 400));
                    }
                    self.shared.event("error", &format!("GPU power cap NOT applied ({why}): {}. Retry from the card tile.", missing.join("; ")));
                    // never left as "cap NOT applied" (the project's rule, 7 October 2026): asked again on the ladder, then a FAULT line home
                    self.cap_refusals += 1;
                    match cap_refusal_plan(self.cap_refusals) {
                        Some(wait) => {
                            self.cap_retry_at = Some(Instant::now() + wait);
                            self.shared.log(&format!("power cap: refusal {} ({why}); asking again in {} s", self.cap_refusals, wait.as_secs()));
                        }
                        None => {
                            self.cap_retry_at = None;
                            let power_control = self.shared.settings.lock().unwrap().power_control;
                            self.shared.log(&format!("FAULT power-cap: {} ({why}); refused {} times, the card runs at the limit it reports; Power control is {}", missing.join("; "), self.cap_refusals, if power_control { "on" } else { "off" }));
                        }
                    }
                } else if !applied.is_empty() {
                    self.cap_refusals = 0;
                    self.cap_retry_at = None;
                }
                if applied.is_empty() && missing.is_empty() {
                    self.shared.log(&format!("power cap: nothing to read back for {what}"));
                }
                match power_control_after_prompt(&r) {
                    Some(note) => self.power_control_off(note),
                    None if !applied.is_empty() => self.st().settings.power_note = "administrator rights given; the cap and the sweep run".into(),
                    None => {}
                }
            }
            Cmd::ElevatedDone(r) => {
                if let Some((line, what, want, _)) = self.power_via_host.take() {
                    self.shared.log(&format!("window host ran the elevated step ({}): {line}", match &r { Ok(()) => "ok".to_string(), Err(e) => e.clone() }));
                    self.finish_power(what, r, want);
                }
            }
            Cmd::ApplyPower => {
                if self.sweep.is_some() || self.sweep_pending.is_some() {
                    self.shared.event("info", "a sweep is setting the caps right now; retry once it is done");
                    return;
                }
                for c in self.st().mining.cards.iter_mut().filter(|c| c.vendor == "nvidia") {
                    c.power_applied = false; // force the step again
                }
                self.apply_power_limits("retry");
            }
            Cmd::SweepStart(key) => {
                let (name, ok, why) = {
                    let st = self.st();
                    match st.mining.cards.iter().find(|c| c.key == key) {
                        Some(c) if !matches!(c.vendor.as_str(), "nvidia" | "amd" | "apple") => (c.name.clone(), false, "no power or clock control for this card".to_string()),
                        Some(c) if !c.enabled => (c.name.clone(), false, "the card is switched off".to_string()),
                        Some(c) => (c.name.clone(), true, String::new()),
                        None => (key.clone(), false, "no such card".to_string()),
                    }
                };
                if !ok {
                    self.shared.event("error", &format!("{name}: no tune: {why}"));
                } else if !self.sweep_queue.contains(&key) {
                    self.sweep_queue.push(key);
                    self.sweep_next_check = Instant::now();
                    self.shared.event("info", &format!("{name}: tune queued; it starts once the card has mined for {} s with over {} s to the hour boundary", crate::ember::STABLE_S, crate::ember::NEEDS_S));
                }
            }
            Cmd::SweepStop => {
                self.sweep_queue.clear();
                if self.sweep.is_some() || self.sweep_pending.is_some() {
                    self.sweep_abort("stopped from the dashboard");
                }
            }
            Cmd::SweepPin(key, pinned) => {
                let name = {
                    let mut settings = self.shared.settings.lock().unwrap();
                    let mut st = self.st();
                    let Some(c) = st.mining.cards.iter_mut().find(|c| c.key == key) else { return };
                    c.pinned = pinned;
                    let e = settings.cards.entry(key.clone()).or_insert_with(|| crate::config::CardPref { enabled: c.enabled, identities: c.identities, ..Default::default() });
                    e.pinned = pinned;
                    if pinned {
                        e.power_pct = c.power_pct;
                    }
                    c.name.clone()
                };
                self.shared.save_settings();
                self.shared.event("info", &if pinned { format!("{name}: cap pinned; the sweep records but does not change it") } else { format!("{name}: the sweep chooses the cap again from its next run") });
            }
            Cmd::SweepEnable(on) => {
                let allowed = self.elevation_allowed();
                self.shared.settings.lock().unwrap().sweep = on;
                self.shared.save_settings();
                self.st().settings.sweep = on;
                self.shared.event("info", if on && allowed { "Ember Tune on: every card is tuned for MH per watt once after install, then weekly" } else if on { "Ember Tune on: AMD cards are tuned; NVIDIA cards measure only until Power control is on in Settings" } else { "Ember Tune off; Tune now on a card still runs one" });
            }
            Cmd::PowerControl(on) => {
                if on && cfg!(windows) && !self.power_task_registered() {
                    // the right was not taken at install: say so, never ask (the founder, 7 October 2026)
                    let note = crate::rights::missing_note("power-helper-task");
                    self.shared.event("info", &format!("Power control: {note}"));
                    self.st().settings.power_note = note;
                }
                {
                    let mut s = self.shared.settings.lock().unwrap();
                    s.power_control = on;
                    s.sweep = on; // implied
                }
                self.shared.save_settings();
                {
                    let mut st = self.st();
                    st.settings.power_control = on;
                    st.settings.sweep = on;
                    st.settings.power_note = if on { "asking Windows for administrator rights once".into() } else { String::new() };
                }
                if on {
                    self.shared.event("info", "power control on: Windows asks for administrator rights once; the cap and the sweep need them");
                    for c in self.st().mining.cards.iter_mut().filter(|c| c.vendor == "nvidia") {
                        c.power_applied = false; // the one prompt sets every cap now
                    }
                    self.apply_power_limits("power control on");
                    if !self.power_busy {
                        self.st().settings.power_note = "on; no NVIDIA card needs a cap right now".into();
                    }
                } else {
                    self.power_control_off("power control off; the cap and the sweep do not run and nothing asks for administrator rights");
                }
            }
            Cmd::TuneProbe(idx, r) => self.sweep_probe_known(idx, r),
            Cmd::SweepHelperDone(r) => {
                self.sweep_helper = false;
                if let Err(e) = &r {
                    self.shared.log(&format!("sweep helper: {e}"));
                    if self.sweep.is_some() || self.sweep_pending.is_some() {
                        self.sweep_abort(&format!("the elevated helper did not run ({e})"));
                    }
                } else if self.sweep.is_some() {
                    self.sweep_abort("the elevated helper exited early");
                } else {
                    self.shared.log("sweep helper: exited");
                }
            }
            Cmd::TuneProgress(v) => self.tune_progress(&v),
            Cmd::FinalityStatus(f) => {
                let mut st = self.st();
                match f {
                    Some(f) => {
                        st.finality.node_active = f.active;
                        st.finality.reason = f.reason;
                        st.finality.held_by = f.held_by;
                        st.finality.provisional = f.provisional;
                        st.finality.cause_source = "node".into();
                        if let Some(since) = f.paused_since {
                            st.finality.paused_since = since;
                        }
                    }
                    None => {
                        // a node without the fields: the log line (parse_finality_line) may still have set a cause
                        st.finality.node_active = None;
                        if st.finality.cause_source != "node-line" {
                            st.finality.cause_source.clear();
                        }
                    }
                }
            }
            Cmd::BalanceRead(r) => {
                self.balance_busy = false;
                match r {
                    Ok(hex) => match crate::ember::wei_from_hex(&hex) {
                        Some(wei) => {
                            // the share lane's credit: the balance's rise over this session's first reading, and the
                            // head it was last seen at; pending while shares are accepted and nothing has risen (the
                            // baseline is taken before the state guard: the guard borrows the engine)
                            let now_wei: Option<u128> = wei.parse().ok();
                            let base = now_wei.map(|n| *self.balance_base.get_or_insert(n));
                            let mut st = self.st();
                            if let (Some(n), Some(base)) = (now_wei, base) {
                                if n > base {
                                    let credit = n - base;
                                    if st.mining.credit_wei != credit.to_string() {
                                        st.mining.credit_wei = credit.to_string();
                                        st.mining.credit_block = st.node.daa;
                                    }
                                }
                            }
                            st.mining.credit_pending = st.mining.share_lane && st.mining.shares_accepted > 0 && st.mining.credit_wei.is_empty();
                            st.address.balance_wei = Some(wei);
                            st.address.balance_note.clear();
                            drop(st);
                            // the Earnings lines carry the chain's balance beside the estimate (F-82)
                            self.sup.set_chain_balance(now_wei.map(|n| n as f64 / 1e18));
                            self.balance_at = crate::platform::unix_now_f();
                            self.balance_next = Some(Instant::now() + Duration::from_secs(30));
                        }
                        None => {
                            self.st().address.balance_note = format!("the node answered {hex} for the balance, not a quantity");
                            self.balance_next = Some(Instant::now() + Duration::from_secs(60));
                        }
                    },
                    Err(e) => {
                        self.st().address.balance_note = e;
                        self.balance_next = Some(Instant::now() + Duration::from_secs(60));
                    }
                }
            }
            Cmd::TuneTier(key, tier) => {
                if key.is_none() {
                    {
                        let mut s = self.shared.settings.lock().unwrap();
                        s.tune_tier = tier.clone();
                    }
                    self.st().settings.tune_tier = tier.clone();
                }
                let targets: Vec<usize> = {
                    let st = self.st();
                    st.mining.cards.iter().enumerate().filter(|(_, c)| key.as_ref().map(|k| &c.key == k).unwrap_or(true)).map(|(i, _)| i).collect()
                };
                let mut applied = Vec::new();
                for idx in targets {
                    let (name, device, found, stale) = {
                        let st = self.st();
                        let Some(c) = st.mining.cards.get(idx) else { continue };
                        let stale = crate::ember::tiers_stale(&c.tiers_class, &c.program_class).then(|| crate::ember::stale_text(&c.name, &c.tiers_class, &c.program_class));
                        (c.name.clone(), c.device.clone(), crate::ember::tier_from_json(&c.tiers, &tier), stale)
                    };
                    if let Some(why) = stale {
                        self.shared.event("warn", &why);
                        continue;
                    }
                    let Some((mut point, limit_w, mhs, w)) = found else {
                        self.shared.event("warn", &format!("{name}: not measured yet: the search runs 2 min into steady mining"));
                        continue;
                    };
                    // tiers-table-26: a table row for an AMD card carries the lane's core offset; the knob sends an absolute clock
                    {
                        let st = self.st();
                        if let Some(c) = st.mining.cards.get(idx) {
                            if point.clock_mhz == 0 {
                                if let Some(row) = c.tiers.iter().find(|t| t.get("id").and_then(|v| v.as_str()) == Some(tier.as_str())) {
                                    if let Some(clock) = crate::tiertable::clock_from_offset(row, c.amd_stock_mhz) {
                                        point.clock_mhz = clock;
                                    }
                                }
                            }
                        }
                    }
                    let unix = crate::platform::unix_now();
                    {
                        let mut settings = self.shared.settings.lock().unwrap();
                        let mut st = self.st();
                        let Some(c) = st.mining.cards.get_mut(idx) else { continue };
                        c.power_pct = point.power_pct;
                        c.clock_cap_mhz = point.clock_mhz;
                        c.mem_cap_mhz = point.mem_mhz;
                        c.tier = tier.clone();
                        c.tier_at = unix as f64;
                        if c.vendor == "nvidia" && limit_w > 0.0 {
                            c.power_limit_w = limit_w;
                            c.power_applied = true;
                            c.power_note = format!("power cap: {} W held by the {} tier", limit_w as u64, tier);
                        }
                        let e = settings.cards.entry(c.key.clone()).or_insert_with(|| crate::config::CardPref { enabled: c.enabled, identities: c.identities, ..Default::default() });
                        e.tier = tier.clone();
                        e.tier_at = unix;
                        e.power_pct = point.power_pct;
                        e.sweep_clock_mhz = point.clock_mhz;
                        e.sweep_mem_mhz = point.mem_mhz;
                    }
                    self.tune_apply(idx, &device, &crate::ember::Step { point, watts: if limit_w > 0.0 { limit_w } else { w }, kind: crate::ember::Kind::Confirm });
                    self.shared.event("ok", &format!("{name}: {} tier applied, {:.1} MH/s at {:.0} W ({})", tier, mhs, w, point_words(&point)));
                    applied.push(name);
                }
                self.shared.save_settings();
                if applied.is_empty() {
                    self.shared.event("warn", &format!("tier {tier}: no card has a measured set yet"));
                }
            }
            Cmd::TuneCardGoal(key, goal) => {
                {
                    let mut s = self.shared.settings.lock().unwrap();
                    let (enabled, identities) = self.st().mining.cards.iter().find(|c| c.key == key).map(|c| (c.enabled, c.identities)).unwrap_or((true, 1));
                    let e = s.cards.entry(key.clone()).or_insert_with(|| crate::config::CardPref { enabled, identities, ..Default::default() });
                    e.tune_goal = goal.clone();
                }
                self.shared.save_settings();
                let name = {
                    let mut st = self.st();
                    let c = st.mining.cards.iter_mut().find(|c| c.key == key);
                    c.map(|c| {
                        c.tune_goal = goal.clone();
                        c.name.clone()
                    })
                };
                if let Some(name) = name {
                    self.shared.event("info", &format!("{name}: tune goal {}", if goal.is_empty() { "follows the global setting".to_string() } else { goal.clone() }));
                }
            }
            Cmd::TuneGoal(goal, price, climb) => {
                {
                    let mut s = self.shared.settings.lock().unwrap();
                    if let Some(g) = &goal {
                        s.tune_goal = g.clone();
                    }
                    if let Some(p) = price {
                        s.power_price_pence = p;
                    }
                    if let Some(c) = climb {
                        s.tune_climb = c;
                    }
                }
                self.shared.save_settings();
                let s = self.shared.settings.lock().unwrap().clone();
                let mut st = self.st();
                st.settings.tune_goal = s.tune_goal.clone();
                st.settings.power_price_pence = s.power_price_pence;
                st.settings.tune_climb = s.tune_climb;
                drop(st);
                self.shared.event("info", &format!("tune goal: {}{}{}", s.tune_goal, if s.power_price_pence > 0.0 { format!(", electricity {:.1} p/kWh", s.power_price_pence) } else { String::new() }, if s.tune_climb { ", hill-climb on" } else { "" }));
            }
            Cmd::Source(kind, url) => {
                // the work source (docs/product-a/OLD-BASE.md step 3): a node by URL or a pool by host:port on the
                // pinned chain; refused with the reason when it does not parse, else saved and the layer restarted
                let fallback = crate::sup::node_url(self.shared.runtime.rpc_port);
                match crate::sup::source_setting(&kind, &url, &fallback) {
                    Err(why) => {
                        self.shared.event("warn", &format!("source: {why}"));
                        self.st().network_error = why;
                    }
                    Ok(_) => {
                        {
                            let mut s = self.shared.settings.lock().unwrap();
                            s.source_kind = kind.clone();
                            s.source_url = url.trim().to_string();
                        }
                        self.shared.save_settings();
                        {
                            let mut st = self.st();
                            st.network_error.clear();
                            st.settings.source_kind = kind.clone();
                            st.settings.source_url = url.trim().to_string();
                        }
                        self.shared.event("info", &format!("source: {kind} {}", url.trim()));
                        if crate::sup::layer_on() && self.running {
                            self.restart_layer("the source changed");
                        }
                    }
                }
            }
            Cmd::Region(region, price, currency) => {
                let (r, p, c) = {
                    let mut s = self.shared.settings.lock().unwrap();
                    if let Some(r) = &region {
                        s.region = r.chars().filter(|c| c.is_ascii_alphanumeric() || *c == '-').take(16).collect();
                    }
                    if let Some(p) = price {
                        s.power_price_pence = p;
                    }
                    if let Some(c) = &currency {
                        // an ISO 4217 code: three ASCII letters, upper-cased; anything else means "follow the region"
                        let code: String = c.chars().filter(|ch| ch.is_ascii_alphabetic()).take(3).collect::<String>().to_ascii_uppercase();
                        s.currency = if code.len() == 3 { code } else { String::new() };
                    }
                    (s.region.clone(), s.power_price_pence, s.currency.clone())
                };
                self.shared.save_settings();
                {
                    let mut st = self.st();
                    st.settings.region = r.clone();
                    st.settings.power_price_pence = p;
                    st.settings.currency = c.clone();
                }
                self.shared.event("info", &format!("electricity: region {}{}{}", if r.is_empty() { "not chosen".to_string() } else { r }, if p > 0.0 { format!(", {p:.2} per kWh (typed, never fetched)") } else { String::new() }, if c.is_empty() { String::new() } else { format!(", currency {c}") }));
            }
            Cmd::Heat(patch) => {
                let mut bad: Option<String> = None;
                let (on, set_c, sched, tz, room_c, room_at, was_on) = {
                    let mut s = self.shared.settings.lock().unwrap();
                    let was_on = s.heat_on;
                    if let Some(c) = patch.set_c {
                        if (crate::heat::SET_MIN_C..=crate::heat::SET_MAX_C).contains(&c) {
                            s.heat_set_c = c;
                        } else {
                            bad = Some(format!("the set point is {:.0} to {:.0} degrees", crate::heat::SET_MIN_C, crate::heat::SET_MAX_C));
                        }
                    }
                    if let Some((text, tz)) = &patch.schedule {
                        match crate::heat::parse_schedule(text) {
                            Ok(slots) => {
                                s.heat_schedule = slots;
                                s.heat_tz_min = *tz;
                            }
                            Err(e) => bad = Some(e),
                        }
                    }
                    if let Some(c) = patch.room_c {
                        if (-20.0..=50.0).contains(&c) {
                            s.heat_room_c = c;
                            s.heat_room_at = crate::platform::unix_now_f();
                        } else {
                            bad = Some("a room reading is -20 to 50 degrees".into());
                        }
                    }
                    if let Some(on) = patch.on {
                        s.heat_on = on;
                    }
                    (s.heat_on, s.heat_set_c, crate::heat::schedule_text(&s.heat_schedule), s.heat_tz_min, s.heat_room_c, s.heat_room_at, was_on)
                };
                self.shared.save_settings();
                {
                    let mut st = self.st();
                    st.settings.heat_on = on;
                    st.settings.heat_set_c = set_c;
                    st.settings.heat_schedule = sched.clone();
                    st.settings.heat_tz_min = tz;
                    st.settings.heat_room_c = room_c;
                    st.settings.heat_room_at = room_at;
                }
                if let Some(b) = bad {
                    self.shared.event("error", &format!("heat mode: {b}"));
                }
                if on != was_on {
                    self.heat.reset();
                    self.heat_samples.clear();
                    self.shared.event("info", &if on { format!("heat mode on: holding {set_c:.1} °C{}; the cards heat for a share of every {} minutes and rest for the rest", if sched.is_empty() { String::new() } else { format!(" (schedule {sched})") }, (crate::heat::PERIOD_S / 60.0) as u64) } else { "heat mode off: the cards mine whenever the node is synced".to_string() });
                    if !on {
                        self.heat_release("heat mode off");
                    }
                } else if on {
                    self.shared.log(&format!("heat: settings set={set_c:.1} schedule=\"{sched}\" tz={tz} room={room_c:.1}@{room_at:.0}"));
                }
            }
            Cmd::TuneSet(seq, r) => match r {
                Ok(text) => {
                    self.tune_acked = Some(seq);
                    if !text.contains("All done") && text != "helper" {
                        self.shared.log(&format!("tune: request {seq} answered: {}", short(&text.replace('\n', " "), 200)));
                    }
                }
                Err(text) => {
                    self.shared.log(&format!("tune: request {seq} refused: {}", short(&text.replace('\n', " "), 300)));
                    if text.starts_with("power control: helper not running") {
                        // the helper's own exit reason, or what no reason means: shown, never a silent toggle (the first Windows machine, 7 October 2026)
                        self.shared.event("error", &short(&text, 400));
                    }
                    if let Some(line) = helper_fault_line(&text) {
                        // the Power Helper task did not answer (no heartbeat, no log line inside its window): a FAULT line
                        // home, and its registration is read again before the next cap (the second Windows machine, 7 October 2026: every tune
                        // request refused since the 14:35Z boot, the cap left "NOT applied")
                        self.shared.log(&line);
                        self.power_task = None;
                    }
                    if self.sweep.as_ref().map(|r| r.seq == seq).unwrap_or(false) {
                        self.sweep_abort("the card refused the setting (see the log)");
                    } else if self.sweep.is_none() {
                        // the restore after a stopped tune was refused (the second Windows machine, 7 October 2026: request 0 back to 70% never ran):
                        // the card may sit at the vendor default; the cap path asks again and reports a FAULT on its ladder
                        self.shared.log(&format!("FAULT power-cap: the restore after the tune was refused ({}); re-applying the cap", short(&text.replace('\n', " "), 120)));
                        for c in self.st().mining.cards.iter_mut().filter(|c| c.vendor == "nvidia") {
                            c.power_applied = false;
                        }
                        self.apply_power_limits("tune restore refused");
                    }
                }
            },
            Cmd::Quit(source) => {
                self.shared.log(&format!("quit requested by {source}"));
                self.quit_source = source;
                self.quitting = true;
                self.st().quitting = true;
                // an earlier release: the bound over the whole quit, whatever the node does (the second Windows machine, 21:24 UK)
                crate::quitguard::arm(self.shared.clone(), exit_line(self.quit_source));
            }
        }
    }

    // ---- start, node ------------------------------------------------------------------------------------------

    /// The first enumeration: the list as detected, the saved choices applied, the setup screen's notes.
    /// driver-check (7 October 2026): each card's driver_os against the manifest's table; the row's offer and the
    /// platform word. Runs at every detection and whenever the table changes; never installs anything by itself.
    fn refresh_driver_offers(&mut self) {
        let platform = crate::drivers::platform_word();
        let table = self.drivers_table.clone();
        let mut st = self.st();
        st.drivers.platform = platform.into();
        st.drivers.table_updated = table.as_ref().map(|t| t.updated.clone()).unwrap_or_default();
        st.drivers.dry_run = table.as_ref().map(|t| t.dry_run).unwrap_or(false) || std::env::var("IGNEUM_DRIVER_DRY_RUN").map(|v| v == "1").unwrap_or(false);
        if st.drivers.status.is_empty() {
            st.drivers.status = "idle".into();
        }
        for c in st.mining.cards.iter_mut() {
            c.driver_offer = crate::drivers::offer_for(&c.vendor, &c.driver_os, table.as_ref(), platform);
        }
        let offers: Vec<String> = st.mining.cards.iter().filter_map(|c| c.driver_offer.as_ref().filter(|o| o.status == "missing" || o.status == "old").map(|o| format!("{}: {}", c.name, o.text))).collect();
        drop(st);
        for o in offers {
            self.shared.log(&format!("driver check: {o}"));
        }
    }

    /// The row's Install click: one vendor, one install at a time; the miners keep mining through it.
    fn driver_install(&mut self, vendor: &str) {
        if self.driver_busy {
            self.shared.event("error", "a driver install is already running");
            return;
        }
        let Some(e) = self.drivers_table.as_ref().and_then(|t| t.entry(vendor)).cloned() else {
            self.shared.event("error", &format!("no driver on offer for {vendor} in the manifest's table"));
            return;
        };
        if crate::drivers::platform_word() != "windows" {
            self.shared.event("error", "the one-click driver install is for Windows; the row names the package elsewhere");
            return;
        }
        let dry = self.drivers_table.as_ref().map(|t| t.dry_run).unwrap_or(false) || std::env::var("IGNEUM_DRIVER_DRY_RUN").map(|v| v == "1").unwrap_or(false);
        self.driver_busy = true;
        {
            let mut st = self.st();
            let d = &mut st.drivers;
            d.status = "downloading".into();
            d.vendor = vendor.into();
            d.version = e.version.clone();
            d.progress = 0.0;
            d.message = format!("downloading {} {} from {}", vendor.to_ascii_uppercase(), e.version, e.url.split("//").nth(1).and_then(|r| r.split('/').next()).unwrap_or("the vendor"));
            d.error.clear();
            d.reboot_required = false;
            d.dry_run = dry;
            d.started_at = crate::platform::unix_now_f();
            d.finished_at = 0.0;
        }
        self.shared.event("info", &format!("driver install started: {} {} ({} MB) from the vendor's server{}", vendor.to_ascii_uppercase(), e.version, (e.size + 512 * 1024) / (1024 * 1024), if dry { ", dry run" } else { "" }));
        // the second Windows machine, 7 October 2026, 16:26Z: the Intel installer's display reset took the machine down while the app's own
        // worker mined on the Arc in the Razer Core X V2 (a power cycle). The vendor's cards the app reads as external
        // (drivertable::install_hold_keys: since an earlier release every card of the vendor) stop before the installer starts; the other vendors' cards keep mining; each held card
        // comes back as it was when the installer ends, or when the card itself returns if the install took it away.
        let (off, restore): (Vec<CardChoice>, Vec<CardChoice>) = {
            let st = self.st();
            let keys = crate::drivertable::install_hold_keys(st.mining.cards.iter().map(|c| (c.key.as_str(), c.vendor.as_str(), c.kind.as_str(), c.enabled)), vendor);
            let live: Vec<crate::jobrun::LiveCard> = st.mining.cards.iter().map(|c| (c.key.clone(), c.enabled, c.identities, c.power_pct, c.present())).collect();
            crate::jobrun::cards_off_choices(&keys, &live)
        };
        if !off.is_empty() {
            self.shared.event("info", &format!("driver install: the worker on {} stops for the install (every {} card; the display resets, and a card in an external enclosure can take the machine for a minute); the other vendors' cards keep mining", off.iter().map(|c| c.key.as_str()).collect::<Vec<_>>().join(", "), vendor.to_ascii_uppercase()));
            self.apply_cards(off);
        }
        self.driver_held = restore;
        let dir = self.shared.runtime.app_dir.join("drivers");
        crate::drivers::start_install(&self.shared, e, dir, dry);
    }

    /// The cards held off for a driver install go back as they were: the ones present now, at once; a card the install
    /// took away (Windows re-enumerates an eGPU through the reset) waits for its return (`returned` = the call from a
    /// detection) and goes back then. A hold that nothing returns stays until the next install replaces it.
    fn driver_release_held(&mut self, returned: bool) {
        if self.driver_held.is_empty() || (returned && self.driver_busy) {
            return;
        }
        let held = std::mem::take(&mut self.driver_held);
        let (now, later): (Vec<CardChoice>, Vec<CardChoice>) = {
            let st = self.st();
            held.into_iter().partition(|ch| st.mining.cards.iter().any(|c| c.key == ch.key && c.present()))
        };
        if !now.is_empty() {
            self.shared.event("info", &format!("driver install: the worker on {} is back{}", now.iter().map(|c| c.key.as_str()).collect::<Vec<_>>().join(", "), if returned { " (the card returned)" } else { "" }));
            self.apply_cards(now);
        }
        if !later.is_empty() && !returned {
            self.shared.event("info", &format!("driver install: {} not listed after the install; its worker comes back when the card does", later.iter().map(|c| c.key.as_str()).collect::<Vec<_>>().join(", ")));
        }
        self.driver_held = later;
    }

    fn driver_event(&mut self, ev: crate::drivers::Event) {
        match ev {
            crate::drivers::Event::Progress(p, text) => {
                let mut st = self.st();
                st.drivers.progress = p;
                st.drivers.message = text;
                st.drivers.status = if p < 0.6 { "downloading".into() } else if p < 0.7 { "verifying".into() } else { "installing".into() };
            }
            crate::drivers::Event::Installed(r) => {
                self.driver_busy = false;
                self.driver_release_held(false);
                let (status, text, reboot, err) = match r {
                    Ok((code, reboot, text)) => (if reboot { "reboot" } else if code == 0 { "done" } else { "error" }, text.clone(), reboot, if code == 0 || reboot { String::new() } else { text }),
                    Err(e) => ("error", e.clone(), false, e),
                };
                {
                    let mut st = self.st();
                    st.drivers.status = status.into();
                    st.drivers.message = text.clone();
                    st.drivers.error = err.clone();
                    st.drivers.reboot_required = reboot;
                    st.drivers.progress = 1.0;
                    st.drivers.finished_at = crate::platform::unix_now_f();
                }
                self.shared.event(if err.is_empty() { "info" } else { "error" }, &format!("driver install: {text}"));
                // the rows re-read their versions at the next detection (a minute at most); ask for one now
                if err.is_empty() {
                    self.shared.send(Cmd::Detect);
                }
            }
        }
    }

    fn first_detection(&mut self, d: crate::detect::Detection) {
        self.detected = true;
        let crate::detect::Detection { cards, notes, dropped, .. } = d;
        let names: Vec<String> = cards.iter().map(|c| format!("{} ({}{}{})", c.name, c.worker, if c.code != c.name { format!(", {}", c.code) } else { String::new() }, if c.problem.is_empty() { String::new() } else { format!(", {}", c.problem) })).collect();
        self.shared.log(&format!("GPUs: {}", if names.is_empty() { "none usable".to_string() } else { names.join("; ") }));
        for n in &notes {
            self.shared.log(&format!("detection: {n}"));
        }
        for n in &dropped {
            self.shared.log(&format!("detection: duplicate OpenCL platform entry {n}"));
        }
        let prefs = self.shared.settings.lock().unwrap().cards.clone();
        let mut st = self.st();
        st.detecting = false;
        st.detect_message = notes.join(". ");
        st.mining.cards = cards;
        for c in st.mining.cards.iter_mut() {
            if let Some(p) = crate::hotplug::pref_for(&prefs, c) {
                crate::hotplug::apply_pref(c, p);
            }
        }
        let supported: Vec<String> = st.mining.cards.iter().filter(|c| c.sweep_supported && c.enabled).map(|c| c.key.clone()).collect();
        let unsupported: Vec<String> = st.mining.cards.iter().filter(|c| !c.sweep_supported).map(|c| format!("{}: {}", c.name, c.sweep_note)).collect();
        let line = crate::hotplug::cards_line(&st.mining.cards);
        drop(st);
        self.shared.log(&line);
        self.last_cards_line = Instant::now();
        for u in &unsupported {
            self.shared.log(&format!("efficiency sweep {u}"));
        }
        if self.shared.runtime.sweep_only {
            if supported.is_empty() {
                self.sweep_say("SWEEP none reason=no_supported_card");
                self.shared.send(Cmd::Quit("the --sweep run (no supported card)"));
            } else {
                self.sweep_queue = supported;
            }
        }
        if self.running {
            self.plan_miners();
        }
    }

    /// A later enumeration (src/hotplug.rs): new cards start, lost cards stop, faulty cards are marked; a list
    /// that changed nothing touches nothing (no running worker ever restarts because of a re-detection).
    fn merge_detection(&mut self, d: crate::detect::Detection) {
        let now = crate::platform::unix_now_f();
        let old = self.st().mining.cards.clone();
        let diff = crate::hotplug::diff(&old, &d.cards, &|c| d.listed(c));
        if diff.is_quiet() {
            return;
        }
        for n in &d.dropped {
            self.shared.log(&format!("detection: duplicate OpenCL platform entry {n}"));
        }
        let prefs = self.shared.settings.lock().unwrap().cards.clone();
        let mut new_nvidia = false;
        for i in diff.removed.iter().copied() {
            let name = old[i].name.clone();
            self.drop_worker(i, "the card was removed");
            if let Some(c) = self.st().mining.cards.get_mut(i) {
                crate::hotplug::mark_removed(c, now);
            }
            self.shared.event("warn", &format!("Card removed: {name}; its worker stopped"));
        }
        for (i, problem) in diff.errored.iter() {
            let name = old[*i].name.clone();
            self.drop_worker(*i, &format!("the card reports {problem}"));
            if let Some(c) = self.st().mining.cards.get_mut(*i) {
                crate::detect::mark_unusable(c, problem);
                c.added_at = now;
            }
            self.shared.event("warn", &format!("{name}: not usable ({problem}); its worker stopped. {}", crate::detect::PROBLEM_HINT));
        }
        for (i, fresh) in diff.moved.iter() {
            let driver_changed = self.st().mining.cards.get(*i).map(|c| !fresh.platform.is_empty() && c.platform != fresh.platform).unwrap_or(false);
            if driver_changed {
                // MF-4: a card held after a self-test failure tries again the minute its driver changes
                let now_i = Instant::now();
                for m in self.miners.iter_mut().filter(|m| m.card == *i) {
                    if m.watch.driver_hold() {
                        m.watch.event(0.0, crate::watchdog::Event::DriverChanged);
                        m.restart_at = Some(now_i);
                        self.shared.event("info", &format!("{}: driver changed; its worker tries again now", fresh.name));
                    }
                }
            }
            if let Some(c) = self.st().mining.cards.get_mut(*i) {
                // an empty device from a re-detect never replaces a set one (the Mac's Metal detect gave "" for a
                // card running as device 0, 12 October 2026)
                if fresh.device.is_empty() && !c.device.is_empty() {
                    self.shared.log(&format!("{}: the re-detect names no device; keeping {}", c.name, c.device));
                } else {
                    self.shared.log(&format!("{}: device {} is now {} (the running worker keeps its device; the next start uses the new one)", c.name, c.device, fresh.device));
                    c.device = fresh.device.clone();
                }
                c.key = fresh.key.clone();
                c.bus = fresh.bus.clone();
                c.name = fresh.name.clone();
                c.platform = fresh.platform.clone();
                if fresh.vram_mb > 0 {
                    c.vram_mb = fresh.vram_mb;
                }
                if !fresh.detail.is_empty() {
                    c.detail = fresh.detail.clone();
                }
            }
        }
        let mut placed: Vec<(usize, CardState)> = Vec::new();
        for (i, fresh) in diff.recovered.into_iter().chain(diff.revived.into_iter()) {
            placed.push((i, fresh));
        }
        let mut next = self.st().mining.cards.len();
        for fresh in diff.added.into_iter() {
            placed.push((next, fresh));
            next += 1;
        }
        for (i, mut fresh) in placed {
            let pref = crate::hotplug::pref_for(&prefs, &fresh).cloned();
            crate::hotplug::settle_new(&mut fresh, i, pref.as_ref(), now);
            let (kind, text) = crate::hotplug::added_words(&fresh);
            new_nvidia |= fresh.vendor == "nvidia" && fresh.enabled;
            let mut st = self.st();
            if i < st.mining.cards.len() {
                st.mining.cards[i] = fresh;
            } else {
                st.mining.cards.push(fresh);
            }
            drop(st);
            self.shared.event(kind, &text);
        }
        let line = crate::hotplug::cards_line(&self.st().mining.cards);
        self.shared.log(&line);
        self.last_cards_line = Instant::now();
        if self.running {
            self.plan_miners();
            if new_nvidia {
                self.apply_power_limits("new card");
            }
        }
    }

    /// Stops the worker on card `idx` (quit, then up to 8 s) and drops its slot; the sweep on it, if any, is aborted.
    fn drop_worker(&mut self, idx: usize, why: &str) {
        if self.sweep.as_ref().map(|r| r.card == idx).unwrap_or(false) {
            self.sweep_abort(why);
        }
        let Some(pos) = self.miners.iter().position(|m| m.card == idx) else { return };
        let label = self.miners[pos].label.clone();
        if let Some(mut p) = self.miners[pos].proc.take() {
            self.shared.log(&format!("stopping miner {label} ({why})"));
            p.write_stdin("quit\n");
            p.stop(8);
        }
        self.miners.remove(pos);
        self.stability.remove(&idx);
    }

    fn start(&mut self) {
        if self.running {
            return;
        }
        self.running = true;
        {
            let mut s = self.shared.settings.lock().unwrap();
            s.setup_done = true;
            s.save(&self.shared.settings_path);
        }
        {
            let mut st = self.st();
            st.setup_done = true;
            st.phase = "dashboard".into();
            st.mining.state = if st.mining.paused { "paused".into() } else { "waiting".into() };
        }
        if self.keep_awake.is_none() {
            self.keep_awake = Some(crate::platform::KeepAwake::start());
            self.shared.log("keeping the machine awake while the app runs");
        }
        let _ = std::fs::create_dir_all(&self.shared.runtime.node_dir);
        let _ = std::fs::create_dir_all(&self.shared.runtime.log_dir);
        if crate::sup::layer_on() {
            // the worker layer: the supervisor takes the payout address and the solo source and starts its workers
            // (a paused engine starts it paused: the supervisor's own pause keeps the slots idle)
            let (address, kind, surl) = { let s = self.shared.settings.lock().unwrap(); (s.address.clone(), s.source_kind.clone(), s.source_url.clone()) };
            let url = crate::sup::node_url(self.shared.runtime.rpc_port);
            match crate::sup::source_setting(&kind, &surl, &url).and_then(|src| crate::sup::configure(&self.sup, &address, src, &self.label_base)) {
                Ok(()) => match self.sup.start() {
                    Ok(()) => {
                        self.shared.log(&format!("worker layer: started on {}", if surl.trim().is_empty() { url.clone() } else { format!("{kind} {surl}") }));
                        // F-E20: the chain's figures for the Earnings estimate (the supervisor's own feed poll), once
                        if !self.chain_poll_started {
                            self.chain_poll_started = true;
                            self.sup.start_chain_figures_poll();
                        }
                        // a new session: the share lane's credit counts from this session's first balance reading
                        self.balance_base = None;
                        {
                            let mut st = self.st();
                            st.mining.credit_wei.clear();
                            st.mining.credit_block = 0;
                            st.mining.credit_pending = false;
                        }
                        if self.st().mining.paused {
                            self.sup.pause();
                        }
                        self.layer_retry_at = None;
                    }
                    Err(e) => {
                        self.shared.event("error", &format!("worker layer: not started: {e}; tried again in {} s", LAYER_RETRY.as_secs()));
                        self.layer_retry_at = Some(Instant::now() + LAYER_RETRY);
                    }
                },
                Err(e) => self.shared.event("error", &format!("worker layer: settings refused: {e}")),
            }
        }
        self.apply_power_limits("start");
        self.start_node();
        self.plan_miners();
    }

    fn set_node_mode(&self, source: &str, mode: &str, reason: &str) {
        let mut st = self.st();
        st.node.source = source.into();
        st.node.mode = mode.into();
        st.node.mode_reason = reason.into();
    }

    /// The other node left its ports for TAKEOVER_WAIT_S: the app starts its own node on them and says so.
    fn take_over_ports(&mut self) {
        self.start_node();
    }

    /// The worker layer restarted (a settings change, a remote job's ask): the workers end by their pids and start
    /// again under the engine's address and source; a paused engine comes back paused.
    fn restart_layer(&mut self, why: &str) {
        self.sup.stop();
        let (address, kind, surl) = { let s = self.shared.settings.lock().unwrap(); (s.address.clone(), s.source_kind.clone(), s.source_url.clone()) };
        let url = crate::sup::node_url(self.shared.runtime.rpc_port);
        // the chain reads follow the source (step 3) whatever the workers do: a node source is read directly (the
        // balance, the live blocks, the chain facts), a pool leaves the fallback. The mini's install of 11 October
        // 2026 (the project's second app fault): the reads moved to the source only when the layer's start succeeded, so a
        // refused start ("No device is switched on") left the Earnings balance on the app's own RPC port for good
        self.start_node();
        match crate::sup::source_setting(&kind, &surl, &url).and_then(|src| crate::sup::configure(&self.sup, &address, src, &self.label_base)).and_then(|()| self.sup.start().map_err(|e| e.to_string())) {
            Ok(()) => {
                self.shared.log(&format!("worker layer: restarted ({why}) on {}", if surl.trim().is_empty() { url.clone() } else { format!("{kind} {surl}") }));
                if self.st().mining.paused {
                    self.sup.pause();
                }
                self.layer_retry_at = None;
            }
            Err(e) => {
                self.shared.event("error", &format!("worker layer: not restarted ({why}): {e}; tried again in {} s", LAYER_RETRY.as_secs()));
                self.layer_retry_at = Some(Instant::now() + LAYER_RETRY);
            }
        }
    }

    /// Ember runs no node (docs/product-a/OLD-BASE.md, step 2): the node words say where the work comes from; the
    /// supervisor's work source (src/sup.rs) is a node the user names or a pool. The old node supervision is gone.
    fn start_node(&mut self) {
        // the node the engine reads (its balance, the live blocks, the chain facts) is the one the user named as the
        // work source when the source is a node; a pool source leaves the reads on the fallback URL
        let (kind, surl) = { let s = self.shared.settings.lock().unwrap(); (s.source_kind.clone(), s.source_url.clone()) };
        let fallback = crate::sup::node_url(self.shared.runtime.rpc_port);
        let url = if kind == "node" && !surl.trim().is_empty() { surl.trim().to_string() } else { fallback };
        crate::execrpc::set_node_url(Some(url.clone()));
        let mut st = self.st();
        st.node.state = "synced".into();
        st.node.synced = true;
        st.node.source = "external".into();
        st.node.message = format!("no node runs inside this build; the chain is read through {url}");
    }

    fn stop_node(&mut self) {}

    fn start_watch(&mut self) {}

    fn plan_miners(&mut self) {
        if crate::sup::layer_on() {
            // the worker layer owns the cards (src/sup.rs): no miner slot of the old path is planned, the
            // supervisor's slots are read onto the cards at every tick
            return;
        }
        let cards = self.st().mining.cards.clone();
        let id8 = self.shared.runtime.id8();
        for c in cards.iter() {
            if !c.enabled || !c.present() || self.miners.iter().any(|m| m.card == c.index) {
                continue;
            }
            let prefix = match c.vendor.as_str() {
                "apple" => "mac".to_string(),
                v => v.to_string(),
            };
            // <platform>-<machine id8>-<card n>: the miner derives the vote keys from this label (and -1..N per identity)
            let label = format!("{prefix}-{id8}-{}", c.index + 1);
            let worker = match c.worker.as_str() {
                "Metal" => self.bins.metal.clone(),
                "CUDA" => self.bins.cuda.clone(),
                _ => self.bins.opencl.clone(),
            };
            let needs_rebuild = worker.is_none();
            self.miners.push(MinerSlot {
                card: c.index,
                label,
                proc: None,
                worker: worker.unwrap_or_default(),
                restart_at: Some(Instant::now()),
                starts: 0,
                restarts: 0,
                building: false,
                building_since: None,
                build_seq: 0,
                needs_rebuild,
                prepared: false,
                last_status: None,
                error_at: None,
                watch: crate::watchdog::CardWatch::new(),
                pack_rebuilds: crate::watchdog::PackRebuilds::new(),
                pack_force: false,
            });
        }
        if self.miners.is_empty() {
            let mut st = self.st();
            if !cards.is_empty() || self.detected {
                st.mining.state = "idle".into();
            }
        }
    }

    fn start_miner(&mut self, _i: usize) {}

    fn stop_miners(&mut self, why: &str) {
        let any = self.miners.iter().any(|m| m.proc.is_some());
        if any {
            self.shared.log(&format!("stopping the miners ({why})"));
        }
        let t = self.secs(Instant::now());
        for m in self.miners.iter_mut() {
            if let Some(mut p) = m.proc.take() {
                p.write_stdin("quit\n");
                p.stop(8);
            }
            m.restart_at = None;
            m.watch.event(t, crate::watchdog::Event::Stopped);
        }
        if any {
            // MF-7: nothing of this engine's keeps hashing after a stop
            self.sweep_orphan_miners("after a stop");
        }
        let mut st = self.st();
        let paused = st.mining.paused;
        for c in st.mining.cards.iter_mut() {
            if c.enabled && c.present() {
                c.state = if paused { "off".into() } else { "waiting".into() };
                c.hash_now = 0.0;
                c.pid = 0;
            }
        }
        st.mining.hash_total = 0.0;
        st.mining.state = if st.mining.paused { "paused".into() } else if self.running { "waiting".into() } else { "stopped".into() };
    }

    /// Ahead-of-time workers (Windows): export this hour's program pack from the node for the prebuilt worker, or,
    /// with no prebuilt worker, build one from source (today's launcher path; it then has no prepare support and is
    /// rebuilt at every hour boundary). Runs off the supervisor thread; WorkerBuilt starts the miner.
    fn prepare_worker(&mut self, _i: usize) {}

    fn apply_cards(&mut self, choices: Vec<CardChoice>) {
        let mut changed = Vec::new();
        let mut power_changed = false;
        let mut pin_aborts = false;
        {
            let mut settings = self.shared.settings.lock().unwrap();
            let mut st = self.st();
            for ch in &choices {
                let Some(c) = st.mining.cards.iter_mut().find(|c| c.key == ch.key) else { continue };
                if !c.present() {
                    continue; // a removed or faulty card has no switch; its saved choice waits for it
                }
                let identities = ch.identities.clamp(1, 64);
                let enabled = ch.enabled && c.kind != "unknown";
                let power_pct = ch.power_pct.map(|p| p.clamp(crate::sweep::MIN_PCT, 100)).unwrap_or(c.power_pct);
                if c.vendor == "nvidia" && power_pct != c.power_pct {
                    c.power_pct = power_pct;
                    c.power_applied = false;
                    power_changed = true;
                    // a cap set by hand is pinned: the sweep records MH per watt but leaves it (unpin on the tile)
                    c.pinned = true;
                    if self.sweep.as_ref().map(|r| r.card == c.index).unwrap_or(false) {
                        pin_aborts = true;
                    }
                }
                let prev = settings.cards.get(&ch.key).cloned().unwrap_or_default();
                settings.cards.insert(ch.key.clone(), crate::config::CardPref { enabled, identities, power_pct: if c.vendor == "nvidia" { power_pct } else { 0 }, pinned: c.pinned, ..prev });
                if c.enabled != enabled || c.identities != identities {
                    c.enabled = enabled;
                    c.identities = identities;
                    c.reason = if !enabled && c.kind == "integrated" { crate::detect::INTEGRATED_REASON.into() } else { String::new() };
                    if !enabled {
                        c.state = "off".into();
                        c.hash_now = 0.0;
                        c.message = String::new();
                    }
                    changed.push((c.index, c.name.clone(), enabled, identities));
                }
            }
            settings.save(&self.shared.settings_path);
        }
        if crate::sup::layer_on() && !choices.is_empty() {
            // the worker layer: EVERY choice reaches the supervisor's settings by its slot's fingerprint (sup.rs
            // device_keys_for; the mini's install of 11 October 2026: a card the engine already held as on, which
            // the supervisor held off by its cohort default, could not be switched on), and a layer that is not up
            // (its last start refused: "No device is switched on") starts again on the switch
            let snap = self.sup.snapshot();
            // the supervisor's slots are built by its inventory inside start(), and a start with no enabled device
            // refuses before any slot exists (the mini, 22:28 UK: "the card switch named no slot the supervisor
            // holds"), so a card off by its cohort default could never be switched on: an empty snapshot takes the
            // inventory first (the workers' hello, a few seconds), as the command line does before its start
            let devices: Vec<igneum_supervisor::state::SlotView> = if snap.devices.is_empty() {
                match self.sup.inventory() {
                    Ok(v) => v,
                    Err(e) => {
                        self.shared.event("warn", &format!("worker layer: the workers could not be listed for the card switch: {e}"));
                        Vec::new()
                    }
                }
            } else {
                snap.devices.clone()
            };
            let keys: Vec<(String, bool)> = {
                let st = self.st();
                let slots: Vec<crate::sup::SlotFacts> = devices.iter().map(crate::sup::SlotFacts::from).collect();
                let wanted: Vec<(String, bool)> = choices.iter().map(|ch| (ch.key.clone(), ch.enabled && st.mining.cards.iter().any(|c| c.key == ch.key && c.kind != "unknown"))).collect();
                crate::sup::device_keys_for(&wanted, &st.mining.cards, &slots)
            };
            if keys.is_empty() {
                self.shared.event("warn", "worker layer: the card switch named no slot the supervisor holds (the workers list no such card)");
            } else if let Err(e) = crate::sup::set_devices(&self.sup, &keys) {
                self.shared.event("error", &format!("worker layer: the card switch was refused: {e}"));
            } else if matches!(snap.mining, igneum_supervisor::state::MiningState::Idle) && keys.iter().any(|(_, on)| *on) && self.running {
                self.restart_layer("a card switched on");
            }
        }
        for (idx, name, enabled, identities) in changed {
            if let Some(pos) = self.miners.iter().position(|m| m.card == idx) {
                if let Some(mut p) = self.miners[pos].proc.take() {
                    p.write_stdin("quit\n");
                    p.stop(8);
                }
                if enabled {
                    self.miners[pos].restart_at = Some(Instant::now());
                    self.miners[pos].prepared = false;
                    self.miners[pos].watch.event(0.0, crate::watchdog::Event::Reset);
                    self.shared.event("info", &format!("{name}: {identities} identit{}; its worker restarts", if identities == 1 { "y" } else { "ies" }));
                } else {
                    self.miners.remove(pos);
                    self.shared.event("info", &format!("{name}: switched off"));
                }
            } else if enabled {
                self.shared.event("ok", &format!("{name}: switched on, {identities} identit{}", if identities == 1 { "y" } else { "ies" }));
            }
        }
        if pin_aborts {
            self.sweep_abort("the cap was set by hand");
        }
        if self.running {
            self.plan_miners();
            if power_changed {
                self.apply_power_limits("settings");
            }
        }
    }

    /// NVIDIA cards: cap the power at power_pct of the default limit through nvidia-smi -pl, all cards in one
    /// elevated step (one administrator prompt). The limit before the app touched it is restored on quit.
    fn apply_power_limits(&mut self, why: &str) {
        if self.power_busy {
            return;
        }
        if self.shared.runtime.sweep_only {
            self.shared.log(&format!("power cap ({why}): not touched under --sweep; the tune sets every limit itself"));
            return;
        }
        if self.sweep.is_some() || self.sweep_pending.is_some() {
            // the sweep owns the caps until it ends; it applies the chosen one itself
            self.shared.log(&format!("power cap ({why}): deferred, a sweep is running"));
            return;
        }
        let allowed = self.elevation_allowed();
        let (cmds, what, held) = power_cap_plan(&mut self.st().mining.cards, allowed, &crate::platform::tool("nvidia-smi").display().to_string());
        if held > 0 {
            self.shared.log(&format!("power cap ({why}): not asked, Power control is off in Settings ({held} card(s) would need it)"));
        }
        if cmds.is_empty() {
            return;
        }
        self.power_busy = true;
        self.power_restore_pending = true;
        self.shared.log(&format!("power cap ({why}): {}", cmds.join(" & ")));
        let what = what.join(", ");
        // once, ever (src/powertask.rs): a registered task sets the caps with no prompt; the readback judges it
        if cfg!(windows) && self.power_task_registered() {
            let pairs: Vec<(String, u64)> = self.st().mining.cards.iter().filter(|c| c.vendor == "nvidia" && c.enabled && c.present() && c.power_default_w > 0.0 && !c.power_applied).map(|c| (c.device.clone(), requested_watts(c).round() as u64)).collect();
            let dir = crate::powertask::helper_dir();
            let shared = self.shared.clone();
            self.shared.log("power cap: through the Igneum Power Helper task (no prompt)");
            std::thread::spawn(move || {
                // the helper skips what the file held before its start, so the command lines go in only after its heartbeat
                // (ensure_running), and a start with no heartbeat comes back as the helper's own exit reason
                let r = crate::powertask::ensure_running(&dir, Duration::from_secs(12)).and_then(|_| {
                    let _ = std::fs::create_dir_all(&dir);
                    let mut seq = crate::powertask::wire_seq();
                    let mut text = String::new();
                    for (dev, w) in &pairs {
                        seq += 1;
                        text.push_str(&format!("{seq} dev {dev}\n"));
                        seq += 1;
                        text.push_str(&format!("{seq} pl {w}\n"));
                    }
                    std::fs::write(dir.join("cmd.txt"), text).map_err(|e| e.to_string())
                });
                std::thread::sleep(Duration::from_secs(6));
                let back: std::collections::HashMap<String, f64> = crate::detect::nvidia_power_limits().into_iter().map(|(k, v)| (k, v.1)).collect();
                shared.send(Cmd::PowerApplied(what, r, back));
            });
            return;
        }
        if cfg!(windows) {
            // an earlier release (main, 7 October 2026, the first Windows machine): no elevated step of the engine's own on Windows, ever; without the task
            // the cap is this event and nothing else (the installer's rights step registers the task)
            let reason = helper_unavailable_event(HELPER_TASK_MISSING);
            self.shared.event("error", &reason);
            self.shared.log(&format!("power cap ({why}): {reason}"));
            self.power_busy = false;
            self.power_restore_pending = false;
            return;
        }
        let line = cmds.join(" & ");
        let shared = self.shared.clone();
        std::thread::spawn(move || {
            let r = crate::platform::run_elevated(&line);
            std::thread::sleep(Duration::from_millis(800));
            let back: std::collections::HashMap<String, f64> = crate::detect::nvidia_power_limits().into_iter().map(|(k, v)| (k, v.1)).collect();
            shared.send(Cmd::PowerApplied(what, r, back));
        });
    }

    /// After an elevated step (ours or the host's): read the limits back and judge.
    fn finish_power(&mut self, what: String, r: Result<(), String>, _want: std::collections::HashMap<String, f64>) {
        let shared = self.shared.clone();
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(800));
            let back: std::collections::HashMap<String, f64> = crate::detect::nvidia_power_limits().into_iter().map(|(k, v)| (k, v.1)).collect();
            shared.send(Cmd::PowerApplied(what, r, back));
        });
    }

    /// On quit: the limits the cards had before the app (one more prompt; nvidia-smi limits do not survive a reboot anyway).
    fn restore_power_limits(&mut self) {
        if !self.power_restore_pending {
            return;
        }
        let st = self.st();
        let cmds: Vec<String> = st
            .mining
            .cards
            .iter()
            .filter(|c| c.vendor == "nvidia" && c.present() && c.power_applied && c.power_before_w > 0.0)
            .map(|c| format!("\"{}\" -i {} -pl {}", crate::platform::tool("nvidia-smi").display(), c.device, c.power_before_w.round() as u64))
            .collect();
        drop(st);
        if cmds.is_empty() {
            return;
        }
        // no administrator prompt on quit (5 October 2026: the app never asks on its own); the limits reset at the
        // next reboot, and the next start with Power control on sets them again
        self.shared.log(&format!("GPU power limits left as set (they reset at the next reboot; no prompt on quit): {}", cmds.join(" & ")));
    }

    /// Is the Igneum Power Helper task registered (src/powertask.rs)? Asked once per run and after every elevated step.
    fn power_task_registered(&mut self) -> bool {
        if self.power_task.is_none() {
            self.power_task = Some(crate::powertask::registered());
        }
        self.power_task.unwrap_or(false)
    }

    /// Power control (config.rs power_control): may the engine ask for administrator rights for the cap or the sweep?
    /// The elevated PC sweep job (--sweep) sets caps directly and counts as allowed.
    fn elevation_allowed(&self) -> bool {
        // an earlier release (src/rights.rs): on Windows the engine never raises a prompt; Power control works through the Power Helper task
        // the installer's rights step registered, and without that task the switch is a notice, not a prompt
        if cfg!(windows) && self.power_task != Some(true) {
            return false;
        }
        elevation_allowed(self.shared.settings.lock().unwrap().power_control, self.shared.runtime.sweep_only)
    }

    /// Power control off, with the reason beside the switch and in the feed; a running or queued sweep ends.
    fn power_control_off(&mut self, note: &str) {
        {
            let mut s = self.shared.settings.lock().unwrap();
            s.power_control = false;
            s.sweep = false;
        }
        self.shared.save_settings();
        {
            let mut st = self.st();
            st.settings.power_control = false;
            st.settings.sweep = false;
            st.settings.power_note = note.to_string();
        }
        self.sweep_queue.clear();
        if self.sweep.is_some() || self.sweep_pending.is_some() {
            self.sweep_abort("power control is off");
        }
        if cfg!(windows) && self.power_task_registered() {
            // the kill switch: the task unregisters itself (elevated) and exits; nothing is left behind
            let hdir = crate::powertask::helper_dir();
            let _ = std::fs::create_dir_all(&hdir);
            let shared = self.shared.clone();
            std::thread::spawn(move || {
                // a `remove` present when the helper starts is skipped like any stale line, so the heartbeat first, then the line
                match crate::powertask::ensure_running(&hdir, Duration::from_secs(12)) {
                    Ok(()) => {
                        let _ = std::fs::write(hdir.join("cmd.txt"), format!("{} remove\n", crate::powertask::wire_seq()));
                        shared.log("power control off: the Igneum Power Helper task removes itself");
                    }
                    Err(e) => {
                        shared.log(&format!("power control off: the task could not be started to remove itself ({e}); remove it in Task Scheduler"));
                        shared.event("error", &e);
                    }
                }
            });
            self.power_task = None;
        }
        self.shared.event(if note.starts_with("power control off:") { "error" } else { "info" }, note);
    }

    /// nvidia-smi -l 5: power draw, GPU and memory temperature, the limit in force, every 5 s, as a child whose
    /// lines come through the same channel as the miners'.
    fn tick_telemetry(&mut self, now: Instant) {
        self.tick_amd_telemetry(now);
        if cfg!(target_os = "macos") || self.st().mining.cards.iter().all(|c| c.vendor != "nvidia") {
            return;
        }
        if let Some(t) = self.telemetry.as_mut() {
            if !t.alive() {
                self.telemetry = None;
                self.telemetry_retry_at = now + Duration::from_secs(30);
            }
        }
        if self.telemetry.is_none() && now >= self.telemetry_retry_at {
            let args: Vec<String> = ["--query-gpu=index,power.draw,temperature.gpu,temperature.memory,power.limit,clocks.gr,clocks.mem", "--format=csv,noheader,nounits", "-l", "5"].iter().map(|s| s.to_string()).collect();
            let log = self.shared.runtime.log_dir.join(format!("gpu-{}.log", self.stamp));
            match procs::spawn(Source::Telemetry, &crate::platform::tool("nvidia-smi"), &args, None, &log, &self.lines_tx, &[]) {
                Ok(p) => self.telemetry = Some(p),
                Err(_) => self.telemetry_retry_at = now + Duration::from_secs(300),
            }
        }
        if now.duration_since(self.last_stability) >= Duration::from_secs(300) {
            self.last_stability = now;
            self.stability_line();
        }
        if let Some((line, what, want, since)) = self.power_via_host.clone() {
            if now.duration_since(since) > Duration::from_secs(150) {
                // no second prompt through PowerShell (5 October 2026): an unanswered prompt is a refusal
                self.power_via_host = None;
                self.shared.log(&format!("the window host did not answer the elevated step in 150 s: {line}"));
                self.finish_power(what, Err("the administrator prompt was not answered in 150 s".into()), want);
            }
        }
    }

    /// AMD cards: igneum-gpu-telemetry -l 5 (ADLX on Windows, the amdgpu sysfs on Linux; proto-opencl/gpu-telemetry.c),
    /// restarted 30 s after it ends, 300 s after it could not start. Nothing on macOS or without an AMD card.
    fn tick_amd_telemetry(&mut self, now: Instant) {
        if cfg!(target_os = "macos") || self.st().mining.cards.iter().all(|c| c.vendor != "amd") {
            return;
        }
        let Some(exe) = self.bins.telemetry.clone() else { return };
        if let Some(t) = self.amd_telemetry.as_mut() {
            if !t.alive() {
                self.amd_telemetry = None;
                self.amd_telemetry_retry_at = now + Duration::from_secs(30);
            }
        }
        if self.amd_telemetry.is_none() && now >= self.amd_telemetry_retry_at {
            let args: Vec<String> = vec!["-l".into(), "5".into()];
            let log = self.shared.runtime.log_dir.join(format!("gpu-amd-{}.log", self.stamp));
            match procs::spawn(Source::AmdTelemetry, &exe, &args, None, &log, &self.lines_tx, &[]) {
                Ok(p) => self.amd_telemetry = Some(p),
                Err(_) => self.amd_telemetry_retry_at = now + Duration::from_secs(300),
            }
        }
    }

    /// One `amd ...` line of igneum-gpu-telemetry to the matching card. The helper lists cards in ADLX (or sysfs)
    /// order with their kind; the app's AMD cards come from the OpenCL worker's list, which has no bus. They are
    /// matched by kind (integrated, discrete) and ordinal within the kind, which is exact for one card of each kind
    /// (the first Windows machine: the gfx1036 and the 9070 XT) and approximate for two discrete AMD cards of one model.
    fn amd_telemetry_line(&mut self, text: &str) {
        let Some(s) = parse_amd_telemetry(text) else { return };
        let mut st = self.st();
        let mut nth = 0usize;
        let mut target: Option<usize> = None;
        for c in st.mining.cards.iter() {
            if c.vendor != "amd" || c.kind != s.kind {
                continue;
            }
            if nth == s.ordinal_in_kind {
                target = Some(c.index);
                break;
            }
            nth += 1;
        }
        let Some(idx) = target else { return };
        let Some(c) = st.mining.cards.iter_mut().find(|c| c.index == idx) else { return };
        if s.watts > 0.0 {
            c.power_w = s.watts;
        }
        if s.temp_c > 0.0 {
            c.temp_gpu = s.temp_c;
        }
        c.fan_pct = s.fan_pct.max(0.0);
        c.fan_rpm = s.fan_rpm.max(0.0);
        c.mclk_mhz = s.mclk_mhz.max(0.0);
        c.gclk_mhz = s.gclk_mhz.max(0.0);
        c.util_pct = s.util_pct.max(0.0);
        c.amd_ordinal = s.ordinal as i64;
        if s.gmax_mhz > 0.0 {
            c.clock_cap_mhz = s.gmax_mhz as u32;
        }
        c.telemetry_at = crate::platform::unix_now_f();
        let (idx, draw, gclk, mclk, temp) = (c.index, s.watts, s.gclk_mhz, s.mclk_mhz, s.temp_c);
        drop(st);
        if let Some(run) = self.sweep.as_mut() {
            if run.card == idx {
                run.sample_telemetry(draw.max(0.0), gclk.max(0.0), mclk.max(0.0), temp.max(0.0));
            }
        }
    }

    /// "index, draw, gpu temp, mem temp, limit" every 5 s.
    fn telemetry_line(&mut self, text: &str) {
        let p: Vec<&str> = text.split(',').map(|s| s.trim()).collect();
        if p.len() < 5 {
            return;
        }
        let f = |s: &str| s.parse::<f64>().unwrap_or(0.0);
        let (draw, tgpu, tmem, limit) = (f(p[1]), f(p[2]), f(p[3]), f(p[4]));
        let (gclk, mclk) = (p.get(5).map(|v| f(v)).unwrap_or(0.0), p.get(6).map(|v| f(v)).unwrap_or(0.0));
        let sweeping = self.sweep.is_some() || self.sweep_pending.is_some();
        let mut st = self.st();
        let Some(c) = st.mining.cards.iter_mut().find(|c| c.vendor == "nvidia" && c.device == p[0]) else { return };
        c.power_w = draw;
        c.temp_gpu = tgpu;
        c.temp_mem = tmem;
        if gclk > 0.0 {
            c.gclk_mhz = gclk;
        }
        if mclk > 0.0 {
            c.mclk_mhz = mclk;
        }
        if limit > 0.0 {
            c.power_limit_w = limit;
            // the readback is the truth: a cap the elevated step set after the judgement (or one set by hand
            // outside the app) that matches what was asked counts as applied
            if !sweeping && !c.power_applied && c.enabled && c.power_default_w > 0.0 && (limit - requested_watts(c)).abs() < 1.0 {
                c.power_applied = true;
                c.power_note = format!("power cap: {} W in force (read back)", limit as u64);
            }
        }
        c.telemetry_at = crate::platform::unix_now_f();
        let idx = c.index;
        let mining = c.state == "mining";
        drop(st);
        if let Some(run) = self.sweep.as_mut() {
            if run.card == idx {
                run.sample_telemetry(draw, gclk, mclk, tgpu);
            }
        }
        if mining {
            let s = self.stability.entry(idx).or_default();
            s.draws.push(draw);
            if s.draws.len() > 20_000 {
                s.draws.remove(0);
            }
            s.max_tgpu = s.max_tgpu.max(tgpu);
            s.max_tmem = s.max_tmem.max(tmem);
        }
    }

    /// One line per card: power draw p95 and the hottest memory and GPU readings this session, for the log drawer,
    /// so a crash can be read back against the numbers.
    fn stability_line(&mut self) {
        let cards = self.st().mining.cards.clone();
        for (idx, s) in self.stability.iter() {
            if s.draws.is_empty() {
                continue;
            }
            let Some(c) = cards.iter().find(|c| c.index == *idx) else { continue };
            let mut v = s.draws.clone();
            v.sort_by(|a, b| a.partial_cmp(b).unwrap());
            let p95 = v[((v.len() as f64 * 0.95) as usize).min(v.len() - 1)];
            self.shared.log(&format!(
                "stability: {}: draw p95 {:.0} W, max {:.0} W, limit {:.0} W ({}), max GPU {:.0} C, max memory {:.0} C, {} samples",
                c.name,
                p95,
                v[v.len() - 1],
                c.power_limit_w,
                if c.power_applied { format!("cap in force, {}% of default", c.power_pct) } else { "cap NOT applied".to_string() },
                s.max_tgpu,
                s.max_tmem,
                v.len()
            ));
        }
    }

    // ---- Ember Tune (src/ember.rs): every card tuned for MH per watt ---------------------------------------------

    /// A TUNE line: the app log, and stdout under --sweep (the PC job reads it there).
    fn sweep_say(&self, line: &str) {
        self.shared.log(line);
        if self.shared.runtime.sweep_only {
            println!("{line}");
            let _ = std::io::stdout().flush();
        }
    }

    fn sweep_dir(&self) -> PathBuf {
        self.shared.runtime.app_dir.join("sweep")
    }

    /// Where this engine's helper reads its commands: the task's own folder when the task is the helper, else this
    /// engine's sweep folder (the prompted helper script was handed that path on its command line).
    fn helper_cmd_dir(&self) -> PathBuf {
        if self.sweep_helper_is_task {
            crate::powertask::helper_dir()
        } else {
            self.sweep_dir()
        }
    }

    /// The manifest's tuning object (<app data>/tuning.json, written by the updater from the signed manifest), or
    /// None: the Ember settings (kill switch, thresholds) and the fleet priors live under it.
    fn tuning_object(&self) -> Option<Value> {
        let p = self.ota.tuning_path()?;
        std::fs::read_to_string(p).ok().and_then(|t| serde_json::from_str(&t).ok())
    }

    /// Every 10 s: drive the running tune, else start one that is due. One card at a time. Never while a remote
    /// job holds the GPU, never under a pause, never inside the last 10 minutes before the hour boundary, never
    /// while the manifest's kill switch is off. A card is due once after install, then every period, and again
    /// after a driver or program-class change; a pinned card is skipped; "Tune now" queues one regardless.
    fn tick_sweep(&mut self, now: Instant) {
        if self.sweep.is_some() {
            self.sweep_drive(now);
            return;
        }
        if self.sweep_pending.is_some() || now < self.sweep_next_check {
            return;
        }
        self.sweep_next_check = now + Duration::from_secs(10);
        if self.quitting || !self.running || self.power_busy || self.job_hold || self.jobs.holds_miners() || self.heat_rest {
            return;
        }
        let tuning = self.tuning_object();
        let ember = crate::ember::settings_of(tuning.as_ref());
        let (auto_on, paused, power_control) = {
            let s = self.shared.settings.lock().unwrap();
            let st = self.st();
            (s.sweep, st.mining.paused, elevation_allowed(s.power_control, self.shared.runtime.sweep_only))
        };
        {
            let mut st = self.st();
            st.settings.tuning_off = !ember.enabled;
            st.settings.tuning_note = if ember.enabled { String::new() } else { "tuning paused fleet-wide by the signed manifest".into() };
            st.settings.tune_period_s = ember.period_s;
        }
        if paused || !ember.enabled {
            return;
        }
        let unix = crate::platform::unix_now();
        let (eta_known, eta_s) = {
            let st = self.st();
            (st.node.daa > 0 && st.program.boundary_daa > 0, st.program.eta_s)
        };
        let boundary_ok = !eta_known || eta_s > crate::ember::NEEDS_S;
        let prefs = self.shared.settings.lock().unwrap().cards.clone();
        let cards = self.st().mining.cards.clone();
        let mut pick: Option<(usize, bool)> = None;
        for c in cards.iter() {
            if !(c.enabled && matches!(c.vendor.as_str(), "nvidia" | "amd" | "apple")) {
                continue;
            }
            // Apple silicon reports no draw under the worker layer: a baseline row there would have no readings
            if crate::sup::layer_on() && c.vendor == "apple" {
                continue;
            }
            let forced = self.sweep_queue.contains(&c.key);
            let due = match prefs.get(&c.key) {
                None => true,
                Some(p) => p.sweep_at == 0 || unix.saturating_sub(p.sweep_at) >= ember.period_s || crate::ember::tiers_stale(&p.tiers_class, &c.program_class) || (!c.driver.is_empty() && !p.sweep_driver.is_empty() && crate::ember::driver_major(&c.driver) != crate::ember::driver_major(&p.sweep_driver)) || (!c.program_class.is_empty() && !p.sweep_class.is_empty() && c.program_class != p.sweep_class),
            };
            let auto = auto_on || !c.tune_control;
            // a measure-only card (Apple, NVIDIA without Power control) measures its baseline once a period even
            // with the switch off: the row should say what the card does
            if !forced && !(auto && !c.pinned && due) {
                continue;
            }
            if c.tune_control && c.vendor == "nvidia" && !power_control && !forced {
                // measure-only until Power control is on; the baseline plan covers it below
            }
            // (E) 6 October 2026, the first Windows machine 18:18Z: three "Tune now" requests sat behind the hour's back-off from the
            // 17:55 failures and the rows said "queued" with no start; an explicit request now clears the back-off,
            // and a held card's row says when the retry comes
            if let Some(t) = self.sweep_retry.get(&c.index).copied() {
                if now < t {
                    if forced {
                        self.sweep_retry.remove(&c.index);
                    } else {
                        if c.sweep_pct == 0 || c.sweep_note.starts_with("tuning stopped") {
                            let note = crate::ember::retry_note((t - now).as_secs(), &c.sweep_note);
                            if let Some(cc) = self.st().mining.cards.get_mut(c.index) {
                                cc.sweep_note = note;
                            }
                        }
                        continue;
                    }
                }
            }
            // under the worker layer the card's own stint is the steady clock (the old path read the miner's process age and
            // its STATUS line; with no miner there the tune waited for steady mining for ever)
            let stable = if crate::sup::layer_on() {
                c.state == "mining" && c.stint_s >= crate::ember::STABLE_S as f64 && c.hash_now > 0.0
            } else {
                self.miners.iter().find(|m| m.card == c.index).map(|m| m.proc.as_ref().map(|p| p.started.elapsed() >= Duration::from_secs(crate::ember::STABLE_S)).unwrap_or(false) && m.last_status.is_some()).unwrap_or(false)
            };
            let wait = if c.state != "mining" {
                Some(format!("tuning: waits for the card to mine ({})", c.state))
            } else if !stable {
                Some(format!("tuning: waits for {} s of steady mining", crate::ember::STABLE_S))
            } else if !boundary_ok {
                Some(format!("tuning: starts after the hour boundary ({eta_s} s)"))
            } else {
                None
            };
            match wait {
                Some(w) => {
                    if forced || c.sweep_pct == 0 {
                        if let Some(cc) = self.st().mining.cards.get_mut(c.index) {
                            cc.sweep_note = w;
                        }
                    }
                }
                None => {
                    pick = Some((c.index, forced));
                    break;
                }
            }
        }
        if let Some((idx, forced)) = pick {
            // the class flip: the first pick after the flip says why it re-measures, once
            let flip = {
                let mut st = self.st();
                match st.mining.cards.get_mut(idx) {
                    Some(c) if crate::ember::tiers_stale(&c.tiers_class, &c.program_class) && c.tiers_remeasure_at == 0.0 => {
                        c.tiers_remeasure_at = unix as f64;
                        Some(crate::ember::stale_text(&c.name, &c.tiers_class, &c.program_class))
                    }
                    _ => None,
                }
            };
            if let Some(line) = flip {
                self.shared.event("info", &line);
            }
            self.sweep_begin(idx, forced);
        } else if self.shared.runtime.sweep_only && self.sweep_queue.is_empty() && self.sweep_pending.is_none() {
            self.shared.send(Cmd::Quit("the --sweep run (every card done)"));
        }
    }

    /// Step 1 of a tune: the probe. NVIDIA: the vendor's maximum core clock and driver (nvidia-smi query), and
    /// whether this process may set limits (one `-pl` at the current limit: "All done" = direct; else the elevated
    /// helper, only with Power control on; else measure only). AMD: the helper's `--tune` line (the clock and power
    /// ranges in force; no elevation). Apple: measure only.
    fn sweep_begin(&mut self, idx: usize, forced: bool) {
        let Some(c) = self.st().mining.cards.get(idx).cloned() else { return };
        self.sweep_pending = Some((idx, forced));
        self.sweep_queue.retain(|k| k != &c.key);
        *self.sweep_attempts.entry(idx).or_default() += 1;
        if let Some(cc) = self.st().mining.cards.get_mut(idx) {
            cc.sweep_state = "running".into();
            cc.sweep_note = "tuning: reading what the card allows".into();
        }
        let shared = self.shared.clone();
        let allowed = self.elevation_allowed();
        match c.vendor.as_str() {
            "nvidia" => {
                let smi = crate::platform::tool("nvidia-smi");
                let device = c.device.clone();
                let current = if c.power_limit_w > 0.0 { c.power_limit_w } else { c.power_default_w }.round() as u64;
                let known_direct = self.sweep_direct;
                std::thread::spawn(move || {
                    let q = crate::detect::run_timeout(std::process::Command::new(&smi).args(["-i", &device, "--query-gpu=clocks.max.gr,driver_version,clocks.mem,clocks.max.mem", "--format=csv,noheader,nounits"]), None, Duration::from_secs(10)).unwrap_or_default();
                    let p: Vec<&str> = q.trim().split(',').map(|s| s.trim()).collect();
                    let clock_max = p.first().and_then(|s| s.parse::<f64>().ok()).unwrap_or(0.0) as u32;
                    let driver = p.get(1).map(|s| s.to_string()).unwrap_or_default();
                    // Ember 2: the memory clock now (the default under load) and the vendor's maximum
                    let mem_default = p.get(2).and_then(|s| s.parse::<f64>().ok()).unwrap_or(0.0) as u32;
                    let mem_max = p.get(3).and_then(|s| s.parse::<f64>().ok()).unwrap_or(0.0) as u32;
                    let direct = match known_direct {
                        Some(d) => Some(d),
                        None if allowed || current > 0 => crate::detect::run_timeout(std::process::Command::new(&smi).args(["-i", &device, "-pl", &current.to_string()]), None, Duration::from_secs(20)).map(|out| out.contains("All done")),
                        None => Some(false),
                    };
                    shared.send(Cmd::TuneProbe(idx, Ok(TuneProbe { clock_max_mhz: clock_max, clock_min_mhz: 0, driver, direct: direct.unwrap_or(false), amd_ordinal: -1, mem_default_mhz: mem_default, mem_max_mhz: mem_max, ..Default::default() })));
                });
            }
            "amd" => {
                let Some(exe) = self.bins.telemetry.clone() else {
                    self.sweep_pending = None;
                    self.shared.send(Cmd::TuneProbe(idx, Ok(TuneProbe { clock_max_mhz: 0, clock_min_mhz: 0, driver: c.driver.clone(), direct: false, amd_ordinal: -1, ..Default::default() })));
                    return;
                };
                let ordinal = c.amd_ordinal;
                let driver = c.driver.clone();
                std::thread::spawn(move || {
                    let out = crate::detect::run_timeout(std::process::Command::new(&exe).arg("--tune"), None, Duration::from_secs(20)).unwrap_or_default();
                    let t = out.lines().filter_map(parse_amd_tune).find(|t| ordinal < 0 || t.ordinal as i64 == ordinal);
                    shared.send(Cmd::TuneProbe(idx, Ok(match t {
                        // the first Windows machine's 9070 XT (ember-tune-pc1-1, 22:30 UTC): `gmax 0 gmax_range -500 1000`, an OFFSET from
                        // the stock clock, not MHz. an earlier release (src/ember.rs amd_knob): the line rides whole in `amd_tune`,
                        // and the knob turns the offset range into a ladder from the card's stock clock under load
                        Some(t) if t.ok => TuneProbe { clock_max_mhz: if t.gmax_min >= 0.0 && t.gmax_max > 0.0 { t.gmax_max as u32 } else { 0 }, clock_min_mhz: if t.gmax_min > 0.0 { t.gmax_min as u32 } else { 0 }, driver, direct: true, amd_ordinal: t.ordinal as i64, plimit_min: t.plimit_min, plimit_max: t.plimit_max, mem_default_mhz: 0, mem_max_mhz: 0, amd_tune: Some(crate::ember::AmdTuneShape { ok: true, error: String::new(), gmax: t.gmax, gmax_min: t.gmax_min, gmax_max: t.gmax_max, plimit_min: t.plimit_min, plimit_max: t.plimit_max }) },
                        Some(t) => TuneProbe { clock_max_mhz: 0, clock_min_mhz: 0, driver, direct: false, amd_ordinal: t.ordinal as i64, amd_tune: Some(crate::ember::AmdTuneShape { ok: false, error: t.error.clone(), ..Default::default() }), ..Default::default() },
                        None => TuneProbe { clock_max_mhz: 0, clock_min_mhz: 0, driver, direct: false, amd_ordinal: -1, ..Default::default() },
                    })));
                });
            }
            _ => {
                self.shared.send(Cmd::TuneProbe(idx, Ok(TuneProbe { clock_max_mhz: 0, clock_min_mhz: 0, driver: c.driver.clone(), direct: false, amd_ordinal: -1, ..Default::default() })));
            }
        }
    }

    /// Step 2: the plan from what the probe found (full, confirm from the fleet prior, or baseline), the helper
    /// when NVIDIA needs it, then the run.
    fn sweep_probe_known(&mut self, idx: usize, r: Result<TuneProbe, String>) {
        let Some((pidx, forced)) = self.sweep_pending else { return };
        if pidx != idx {
            return;
        }
        let probe = match r {
            Ok(p) => p,
            Err(e) => {
                self.sweep_abort(&format!("cannot read the card's limits: {e}"));
                return;
            }
        };
        let Some(c) = self.st().mining.cards.get(idx).cloned() else {
            self.sweep_pending = None;
            return;
        };
        // NVIDIA control: this process is elevated (direct), or Power control is on so the one-prompt helper may run.
        // The --sweep job alone never counts: it must not raise a prompt on a PC with nobody there (5 October 2026).
        // a registered Igneum Power Helper task (src/powertask.rs) is permission too: it sets limits with no prompt,
        // so an unelevated measurement engine (Power control forced off) still controls NVIDIA through it
        let power_control = probe.direct || self.shared.settings.lock().unwrap().power_control || (cfg!(windows) && self.power_task_registered());
        // AMD's power limit is a percent offset from the default (ADLX): the plan's watts scale becomes a percent
        // scale (default 100, floor 100 + plimit_min, ceiling 100 + plimit_max), tune_apply sends pct - 100
        // an earlier release: the AMD knob (src/ember.rs amd_knob): the limits from the tune line and the stock clock under load, or
        // "not available (<reason>)" and nothing set
        let amd_knob = if c.vendor == "amd" { Some(crate::ember::amd_knob(probe.amd_tune.as_ref(), c.gclk_mhz, cfg!(target_os = "linux"))) } else { None };
        let mut limits = if let Some(Ok(k)) = amd_knob.as_ref() {
            self.shared.log(&format!("tune: AMD knob for {}: stock {} MHz, clock {} to {} MHz{}, power {} to {} percent", c.name, k.stock_mhz, k.clock_min_mhz, k.clock_max_mhz, if k.gmax_offset { " (offsets)" } else { "" }, 100.0 + k.plimit_min, 100.0 + k.plimit_max));
            crate::ember::amd_limits(k)
        } else if c.vendor == "amd" && probe.direct && probe.plimit_max >= probe.plimit_min && probe.plimit_min > -100.0 {
            crate::ember::Limits { power_default_w: 100.0, power_min_w: 100.0 + probe.plimit_min, power_max_w: 100.0 + probe.plimit_max, clock_max_mhz: probe.clock_max_mhz, clock_min_mhz: probe.clock_min_mhz, mem_default_mhz: 0, mem_max_mhz: 0, power_ceiling_w: 0.0, prior_knee_mhz: crate::ember::knee_clock_prior(&c.name) }
        } else {
            crate::ember::Limits { power_default_w: c.power_default_w, power_min_w: c.power_min_w, power_max_w: c.power_max_w, clock_max_mhz: probe.clock_max_mhz, clock_min_mhz: probe.clock_min_mhz, mem_default_mhz: probe.mem_default_mhz, mem_max_mhz: probe.mem_max_mhz, power_ceiling_w: 0.0, prior_knee_mhz: crate::ember::knee_clock_prior(&c.name) }
        };
        // the tuner's ceiling (the project's rule, 7 October 2026, the second Windows machine): the card's measured efficient point, or the cap it runs
        // at, unless Power control is on in Settings and the user raised the cap above the default; watts only (NVIDIA)
        if c.vendor != "amd" || !probe.direct {
            let settings_power_control = self.shared.settings.lock().unwrap().power_control;
            let at = crate::ember::Point { clock_mhz: c.clock_cap_mhz, power_pct: c.power_pct, mem_mhz: c.mem_cap_mhz };
            limits.power_ceiling_w = crate::ember::power_ceiling(&limits, &c.name, at, settings_power_control);
            self.shared.log(&format!("tune: ceiling {:.0} W for {} ({}; Power control {})", limits.ceiling(), c.name, crate::ember::efficient_watts(&c.name).map(|w| format!("measured efficient point {w:.0} W")).unwrap_or_else(|| "no measured point, the cap it runs at".into()), if settings_power_control { "on" } else { "off" }));
        }
        let control = match c.vendor.as_str() {
            "nvidia" => crate::ember::control_reason("nvidia", &limits, &c.device, power_control, false),
            "amd" => match amd_knob.as_ref() {
                Some(Err(reason)) => Some(format!("measure only: {reason}")),
                _ => crate::ember::control_reason("amd", &limits, &c.device, power_control, probe.direct && probe.amd_ordinal >= 0),
            },
            v => crate::ember::control_reason(v, &limits, &c.device, power_control, false),
        };
        if c.vendor == "nvidia" {
            self.sweep_direct = Some(probe.direct);
            // the approved step that made this engine elevated registers the Igneum Power Helper here too (run 5,
            // 6 October 2026: a --sweep engine skipped the cap path where the registration lived, so the one
            // approved click registered nothing); no prompt: this process already holds the rights
            if probe.direct && cfg!(windows) && !self.power_task_registered() {
                let installed: Vec<PathBuf> = crate::powertask::install_candidates();
                if let Ok(exe) = std::env::current_exe() {
                    let target = crate::powertask::task_exe(&exe, &installed);
                    // V6-06: the registration script travels inline, never as a file under the sweep folder
                    {
                        let mut p = std::process::Command::new(crate::platform::tool("powershell"));
                        p.args(crate::platform::encoded_command_args(&crate::powertask::register_script(&target)));
                        crate::platform::quiet(&mut p);
                        let ok = p.status().map(|s| s.success()).unwrap_or(false);
                        self.power_task = None;
                        self.sweep_say(&format!("TUNE helper registered={} action=protected copy of {}", ok, target.display()));
                    }
                }
            }
        }
        {
            let mut st = self.st();
            if let Some(cc) = st.mining.cards.get_mut(idx) {
                cc.clock_max_mhz = probe.clock_max_mhz;
                cc.clock_min_mhz = probe.clock_min_mhz;
                if !probe.driver.is_empty() {
                    cc.driver = probe.driver.clone();
                }
                if probe.amd_ordinal >= 0 {
                    cc.amd_ordinal = probe.amd_ordinal;
                }
                if let Some(Ok(k)) = amd_knob.as_ref() {
                    cc.clock_max_mhz = k.clock_max_mhz;
                    cc.clock_min_mhz = k.clock_min_mhz;
                    cc.amd_stock_mhz = k.stock_mhz;
                    cc.amd_gmax_offset = k.gmax_offset;
                }
                cc.tune_control = control.is_none();
            }
        }
        let tuning = self.tuning_object();
        let ember = crate::ember::settings_of(tuning.as_ref());
        let before = crate::ember::Point { clock_mhz: c.clock_cap_mhz, power_pct: if c.power_pct == 0 { 80 } else { c.power_pct }, mem_mhz: c.mem_cap_mhz };
        let before_w = if c.power_limit_w > 0.0 { c.power_limit_w } else { requested_watts(&c) };
        let key = crate::ember::prior_key(&c.name, &probe.driver, &c.program_class);
        let prior = crate::ember::prior_of(tuning.as_ref(), &key, ember.min_samples);
        let full_due = self.tune_full_due.remove(&idx);
        let (goal, climb_on) = {
            let s = self.shared.settings.lock().unwrap();
            // the card's own goal when set, else the global one
            (crate::ember::goal_for(s.cards.get(&c.key).map(|p| p.tune_goal.as_str()).unwrap_or(""), &s.tune_goal), s.tune_climb)
        };
        let plan = if let Some(why) = control.as_ref() {
            if let Some(cc) = self.st().mining.cards.get_mut(idx) {
                cc.sweep_note = why.clone();
            }
            crate::ember::Plan::baseline(&limits, before, ember.tolerance_pct)
        } else if climb_on || prior.as_ref().map(|p| p.point.mem_mhz > 0).unwrap_or(false) {
            // Ember 2: the hill-climb from the fleet prior (or the card's point) toward the goal
            let start = prior.as_ref().map(|p| p.point).unwrap_or(before);
            crate::ember::Plan::climb(&limits, start, goal, ember.tolerance_pct)
        } else if let (Some(p), false, 0) = (prior.as_ref(), full_due, c.sweep_pct) {
            crate::ember::Plan::confirm(&limits, p.point, before, ember.tolerance_pct)
        } else {
            crate::ember::Plan::full(&limits, before, ember.tolerance_pct)
        };
        if plan.is_empty() {
            self.sweep_abort("no steps: the card reported neither a default power limit nor a maximum clock");
            return;
        }
        if c.vendor == "nvidia" && control.is_none() && !probe.direct && !self.sweep_helper {
            if let Err(e) = self.sweep_helper_start(&c) {
                self.sweep_abort(&format!("the elevated helper could not start: {e}"));
                return;
            }
        }
        let label = self.miners.iter().find(|m| m.card == idx).map(|m| m.label.clone()).unwrap_or_else(|| format!("card-{idx}"));
        let now = Instant::now();
        let kind = plan.kind;
        let steps = plan.len();
        let run = crate::ember::Run::new(idx, &c.device, &label, plan, before_w, forced, crate::ember::Timing::from_env(), now);
        self.sweep_pending = None;
        self.sweep_say(&format!(
            "TUNE start card={label} name={} plan={} steps={} clock_max={} clock_min={} default={:.0} min={:.0} max={:.0} before_clock={} before_pct={} before={:.0} mode={} driver={} class={} prior={}",
            c.name.replace(' ', "_"),
            kind.name(),
            steps,
            limits.clock_max_mhz,
            limits.clock_floor(),
            limits.power_default_w,
            limits.power_min_w,
            limits.power_max_w,
            before.clock_mhz,
            before.power_pct,
            before_w,
            if control.is_some() { "measure" } else if c.vendor == "amd" { "adlx" } else if probe.direct { "direct" } else { "helper" },
            probe.driver.replace(' ', "_"),
            if c.program_class.is_empty() { "v2" } else { &c.program_class },
            prior.as_ref().map(|p| format!("{}mhz/{}pct/{}samples", p.point.clock_mhz, p.point.power_pct, p.samples)).unwrap_or_else(|| "none".into())
        ));
        let what = match kind {
            crate::ember::PlanKind::Full => format!("{steps} steps over the power limit and the core clock, {} s each on the live program", (run.timing.settle + run.timing.hold).as_secs()),
            crate::ember::PlanKind::Confirm => format!("the fleet prior ({} samples) and one neighbour, {} s each", prior.as_ref().map(|p| p.samples).unwrap_or(0), (run.timing.settle + run.timing.hold).as_secs()),
            crate::ember::PlanKind::Baseline => format!("measuring the card as it runs ({})", control.clone().unwrap_or_default()),
            crate::ember::PlanKind::Climb => format!("the hill-climb: memory up, core down, up to {steps} probes of {} s", (run.timing.settle + run.timing.hold).as_secs()),
        };
        self.shared.event("info", &format!("{}: tuning started: {what}", c.name));
        self.sweep = Some(run);
        self.sweep_drive(now);
    }

    /// The elevated helper (src/sweep.rs helper_script_*): one administrator prompt; it polls <app>/sweep/cmd.txt.
    fn sweep_helper_start(&mut self, c: &CardState) -> Result<(), String> {
        let task = cfg!(windows) && self.power_task_registered();
        let power_control = self.shared.settings.lock().unwrap().power_control;
        let route = match helper_route(cfg!(windows), task, power_control) {
            HelperRoute::Unavailable(reason) => {
                // an earlier release (main, 7 October 2026, the first Windows machine): the engine never runs an elevated helper of its own on Windows; a
                // missing task is this event and nothing else (no script written, no process, no prompt)
                if cfg!(windows) {
                    self.shared.event("error", &reason);
                }
                return Err(reason);
            }
            r => r,
        };
        let dir = self.sweep_dir();
        std::fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
        let _ = std::fs::write(dir.join("cmd.txt"), "");
        if route == HelperRoute::Task {
            // once, ever: the registered task is the helper; it reads ITS command file (powertask::helper_dir, never
            // under a scratch IGNEUM_APP_DATA), no prompt; the heartbeat first, then the dev line (a line present at the
            // helper's start is never a command)
            let hdir = crate::powertask::helper_dir();
            let _ = std::fs::create_dir_all(&hdir);
            if let Err(e) = crate::powertask::ensure_running(&hdir, Duration::from_secs(12)) {
                self.shared.event("error", &short(&e, 400));
                return Err(e);
            }
            let _ = std::fs::write(hdir.join("cmd.txt"), format!("{} dev {}\n", crate::powertask::wire_seq(), c.device));
            self.shared.log("tune helper: the Igneum Power Helper task (no prompt)");
            self.sweep_helper = true;
            self.sweep_helper_is_task = true;
            return Ok(());
        }
        let restore = (if c.power_limit_w > 0.0 { c.power_limit_w } else { c.power_default_w }).round() as u64;
        let smi = crate::platform::tool("nvidia-smi").display().to_string();
        let script = dir.join("helper.sh");
        std::fs::write(&script, crate::sweep::helper_script_unix()).map_err(|e| e.to_string())?;
        let line = format!("sh \"{}\" \"{}\" \"{}\" {} {}", script.display(), dir.display(), smi, c.device, restore);
        self.shared.log(&format!("tune helper (administrator prompt): {line}"));
        self.sweep_helper = true;
        self.sweep_helper_is_task = false;
        let shared = self.shared.clone();
        std::thread::spawn(move || {
            let r = crate::platform::run_elevated(&line);
            shared.send(Cmd::SweepHelperDone(r));
        });
        Ok(())
    }

    fn sweep_helper_quit(&mut self) {
        if self.sweep_helper {
            let _ = std::fs::write(self.helper_cmd_dir().join("cmd.txt"), format!("{} quit\n", crate::powertask::wire_seq()));
            if self.sweep_helper_is_task {
                // the task exits on quit and reports nothing back; the next tune starts it again
                self.sweep_helper = false;
                self.sweep_helper_is_task = false;
            }
        }
    }

    /// Sets a point on the card. NVIDIA: `-pl <W>` and `-lgc 0,<MHz>` (`-rgc` for unlocked), directly when this
    /// process is elevated, else through the helper's command file (`<seq> pl <W>`, `<seq> lgc <MHz>`, `<seq> rgc`).
    /// AMD: igneum-gpu-telemetry `--card N --set-plimit <offset>` and `--set-gmax <MHz>` (`--reset` for the default
    /// point). The result comes back as Cmd::TuneSet(seq, ..): the acknowledgement the run waits for.
    fn tune_apply(&mut self, idx: usize, device: &str, step: &crate::ember::Step) {
        let seq = self.sweep.as_ref().map(|r| r.seq).unwrap_or(0);
        let card = self.st().mining.cards.get(idx).cloned();
        let Some(c) = card else { return };
        let w = step.watts.round() as u64;
        let clock = step.point.clock_mhz;
        let mem = step.point.mem_mhz;
        let shared = self.shared.clone();
        self.tune_acked = None;
        match c.vendor.as_str() {
            "nvidia" if self.sweep_direct == Some(true) => {
                let smi = crate::platform::tool("nvidia-smi");
                let device = device.to_string();
                let power_only = c.power_default_w > 0.0;
                std::thread::spawn(move || {
                    let mut ok = true;
                    let mut text = String::new();
                    if power_only && w > 0 {
                        let out = crate::detect::run_timeout(std::process::Command::new(&smi).args(["-i", &device, "-pl", &w.to_string()]), None, Duration::from_secs(20)).unwrap_or_else(|| "nvidia-smi did not answer".into());
                        ok &= out.contains("All done") || out.contains("Power limit for GPU");
                        text.push_str(out.trim());
                    }
                    let out = if clock > 0 {
                        crate::detect::run_timeout(std::process::Command::new(&smi).args(["-i", &device, "-lgc", &format!("0,{clock}")]), None, Duration::from_secs(20)).unwrap_or_else(|| "nvidia-smi did not answer".into())
                    } else {
                        crate::detect::run_timeout(std::process::Command::new(&smi).args(["-i", &device, "-rgc"]), None, Duration::from_secs(20)).unwrap_or_else(|| "nvidia-smi did not answer".into())
                    };
                    ok &= out.contains("All done") || out.to_ascii_lowercase().contains("clocks set") || out.to_ascii_lowercase().contains("reset");
                    text.push(' ');
                    text.push_str(out.trim());
                    // Ember 2: the memory clock, locked to one value (`-lmc m,m`) or reset (`-rmc`)
                    let out = if mem > 0 {
                        crate::detect::run_timeout(std::process::Command::new(&smi).args(["-i", &device, "-lmc", &format!("{mem},{mem}")]), None, Duration::from_secs(20)).unwrap_or_else(|| "nvidia-smi did not answer".into())
                    } else {
                        crate::detect::run_timeout(std::process::Command::new(&smi).args(["-i", &device, "-rmc"]), None, Duration::from_secs(20)).unwrap_or_else(|| "nvidia-smi did not answer".into())
                    };
                    ok &= out.contains("All done") || out.to_ascii_lowercase().contains("clocks set") || out.to_ascii_lowercase().contains("reset") || out.to_ascii_lowercase().contains("not supported");
                    text.push(' ');
                    text.push_str(out.trim());
                    shared.send(Cmd::TuneSet(seq, if ok { Ok(text) } else { Err(text) }));
                });
            }
            "nvidia" => {
                if !self.sweep_helper {
                    // measure only: nothing is set; the run notices the missing acknowledgement and measures
                    return;
                }
                let dev = device.to_string();
                // (F) the wire numbers come from the one monotonic space every writer uses (powertask::wire_seq);
                // `seq` (the run's request index) stays the engine's own id for the acknowledgement
                let wire = crate::powertask::wire_seq();
                let cmd = format!("{} dev {dev}\n{} pl {w}\n{} {}\n{} {}\n", wire, wire + 1, wire + 2, if clock > 0 { format!("lgc {clock}") } else { "rgc".to_string() }, wire + 3, if mem > 0 { format!("lmc {mem}") } else { "rmc".to_string() });
                let dir = self.helper_cmd_dir();
                let is_task = self.sweep_helper_is_task;
                // 6 October 2026, the first Windows machine: (B) the task's helper had exited while the engine's flag still said it ran,
                // so commands went to nobody and a blind 4-second sleep called them acknowledged; (C) the first
                // request of the first tune was written in the same second the helper started, and the helper
                // skipped it as stale. Now, in the task case: the helper's heartbeat says whether it runs; the task
                // is started and a fresh heartbeat awaited BEFORE the command is written; and the acknowledgement
                // is the helper's own log line for this sequence (`<seq>3 nvidia-smi ...`), read for up to 15 s;
                // a missing line is a failure with that reason
                let want = format!(" {} nvidia-smi", wire + 3);
                let log_file = dir.join("helper.log");
                std::thread::spawn(move || {
                    if is_task {
                        if let Err(e) = crate::powertask::ensure_running(&dir, Duration::from_secs(12)) {
                            shared.send(Cmd::TuneSet(seq, Err(e)));
                            return;
                        }
                    }
                    let _ = std::fs::create_dir_all(&dir);
                    let _ = std::fs::write(dir.join("cmd.txt"), cmd);
                    let until = Instant::now() + Duration::from_secs(if is_task { 15 } else { 6 });
                    loop {
                        std::thread::sleep(Duration::from_millis(500));
                        let text = std::fs::read_to_string(&log_file).unwrap_or_default();
                        if let Some(line) = text.lines().rev().find(|l| l.contains(&want)) {
                            shared.send(Cmd::TuneSet(seq, Ok(format!("helper: {}", line.trim()))));
                            return;
                        }
                        if Instant::now() >= until {
                            shared.send(Cmd::TuneSet(seq, Err(format!("the helper did not run sequence {seq} within {} s (no line in {})", if is_task { 15 } else { 6 }, log_file.display()))));
                            return;
                        }
                    }
                });
            }
            "amd" => {
                let Some(exe) = self.bins.telemetry.clone() else { return };
                // (D) 6 October 2026, the first Windows machine 17:57Z: a measure-only card (the probe gave no tune line) was still sent a
                // set, which the helper refused and the tune stopped; measure only means nothing is set, the run
                // notices the missing acknowledgement and measures
                if !crate::ember::may_set("amd", c.tune_control, c.amd_ordinal) {
                    return;
                }
                let n = c.amd_ordinal.to_string();
                // the step's limit on the AMD scale is a percent (the probe's Limits); the offset is that minus 100
                let offset = if c.power_default_w <= 0.0 { step.watts.round() as i64 - 100 } else { step.point.power_pct as i64 - 100 };
                let unlocked = clock == 0 && offset == 0;
                // an earlier release: a clock cap goes as the offset from stock when the driver speaks offsets (the 9070 XT), else MHz
                let gmax = crate::ember::amd_gmax_arg(if clock > 0 { clock } else { c.clock_max_mhz }, c.amd_stock_mhz, c.amd_gmax_offset);
                std::thread::spawn(move || {
                    let run = |args: &[&str]| crate::detect::run_timeout(std::process::Command::new(&exe).args(args), None, Duration::from_secs(20)).unwrap_or_else(|| "igneum-gpu-telemetry did not answer".into());
                    let mut text = String::new();
                    let mut ok = true;
                    if unlocked {
                        let out = run(&["--card", &n, "--reset"]);
                        ok &= out.lines().any(|l| l.starts_with("tune ") && l.contains(" ok"));
                        text.push_str(out.trim());
                    } else {
                        if gmax != 0 || clock > 0 {
                            let out = run(&["--card", &n, "--set-gmax", &gmax.to_string()]);
                            ok &= out.lines().any(|l| l.starts_with("tune ") && l.contains(" ok"));
                            text.push_str(out.trim());
                        }
                        let out = run(&["--card", &n, "--set-plimit", &offset.to_string()]);
                        ok &= out.lines().any(|l| l.starts_with("tune ") && l.contains(" ok"));
                        text.push(' ');
                        text.push_str(out.trim());
                    }
                    shared.send(Cmd::TuneSet(seq, if ok { Ok(text) } else { Err(text) }));
                });
            }
            _ => {}
        }
        self.shared.log(&format!("tune: {} MHz, {}% ({w} W), memory {} requested on device {device} (request {seq})", if clock > 0 { clock.to_string() } else { "unlocked".into() }, step.point.power_pct, if mem > 0 { format!("{mem} MHz") } else { "default".into() }));
    }

    /// A measurement engine's progress for one card (POST /api/tune-progress, forwarded by the job playbook from the
    /// engine's `TUNE progress` lines): the installed app shows the step, the live rate and draw and the time left
    /// instead of a bare "off" while a job holds its miners. `done` ends it: the row reads the tuned line.
    fn tune_progress(&mut self, v: &Value) {
        let key = v.get("key").and_then(|k| k.as_str()).unwrap_or("");
        let mut st = self.st();
        let Some(c) = st.mining.cards.iter_mut().find(|c| c.key == key || (!key.is_empty() && c.name.replace(' ', "_") == key)) else { return };
        let n = |k: &str| v.get(k).and_then(|x| x.as_f64()).unwrap_or(0.0);
        if v.get("done").and_then(|d| d.as_bool()).unwrap_or(false) {
            c.sweep_state = "idle".into();
            c.state = if self.job_hold { "held".into() } else { c.state.clone() };
            c.tune_step = 0;
            c.tune_steps = 0;
            c.tune_eta_s = 0;
            if n("mhs") > 0.0 && n("watts") > 0.0 {
                c.sweep_mhs = n("mhs");
                c.sweep_watts = n("watts");
                c.sweep_eff = n("mhs") / n("watts");
                c.sweep_pct = n("power_pct") as u32;
                c.tune_clock_mhz = n("clock_mhz") as u32;
                c.tune_source = v.get("plan").and_then(|p| p.as_str()).unwrap_or("full").into();
                c.tune_line = crate::ember::tuned_line(n("mhs"), n("watts"), n("mhs") / n("watts"));
                c.sweep_at = crate::platform::unix_now_f();
            }
            c.sweep_note = v.get("note").and_then(|x| x.as_str()).unwrap_or("").to_string();
            return;
        }
        c.state = "tuning".into();
        c.sweep_state = "running".into();
        c.sweep_note = v.get("note").and_then(|x| x.as_str()).unwrap_or("tuning").to_string();
        c.tune_step = n("step") as u32;
        c.tune_steps = n("of") as u32;
        c.tune_eta_s = n("eta_s") as i64;
        c.tune_plan = v.get("plan").and_then(|p| p.as_str()).unwrap_or("").into();
        if n("mhs") > 0.0 {
            c.hash_now = n("mhs");
        }
        if n("watts") > 0.0 {
            c.power_w = n("watts");
        }
        c.message = String::new();
    }

    /// Faults and conditions first, then the state machine.
    fn sweep_drive(&mut self, now: Instant) {
        let Some((idx, device, started, seq)) = self.sweep.as_ref().map(|r| (r.card, r.device.clone(), r.started, r.seq)) else { return };
        let card = self.st().mining.cards.get(idx).cloned();
        let Some(c) = card else {
            self.sweep_abort("the card disappeared");
            return;
        };
        let slot_error = self.miners.iter().find(|m| m.card == idx).and_then(|m| m.error_at).map(|t| t > started).unwrap_or(false);
        let fault = if self.quitting {
            Some("the app is quitting".to_string())
        } else if self.job_hold || self.jobs.holds_miners() {
            Some("a remote job took the GPU".into())
        } else if self.heat_rest {
            Some("heat mode rested the card".into())
        } else if self.st().mining.paused {
            Some("mining paused".into())
        } else if c.state != "mining" && c.state != "tuning" {
            Some(format!("the card left mining ({})", if c.state == "starting" || c.state == "restarting" { "worker restart, likely the hour boundary" } else { &c.state }))
        } else if slot_error {
            Some("the worker reported an error".into())
        } else if c.temp_gpu >= 90.0 {
            Some(format!("GPU at {:.0} C", c.temp_gpu))
        } else {
            None
        };
        if let Some(why) = fault {
            self.sweep_abort(&why);
            return;
        }
        let acked = self.tune_acked == Some(seq);
        let rb = crate::ember::Readback { limit_w: if c.vendor == "nvidia" { c.power_limit_w } else { 0.0 }, acked };
        // the clock readback during a hold: a core clock well over the cap means the cap did not take
        if let Some(run) = self.sweep.as_mut() {
            if matches!(run.phase, crate::ember::Phase::Holding { .. }) {
                if let Some(step) = run.current.as_ref() {
                    if step.point.clock_mhz > 0 && c.gclk_mhz > step.point.clock_mhz as f64 * 1.05 && c.vendor == "nvidia" && c.tune_control {
                        run.mark_unapplied();
                    }
                }
            }
        }
        let outs = self.sweep.as_mut().map(|r| r.tick(now, rb)).unwrap_or_default();
        let label = self.sweep.as_ref().map(|r| r.label.clone()).unwrap_or_default();
        for o in outs {
            match o {
                crate::ember::Out::Apply(step) => self.tune_apply(idx, &device, &step),
                crate::ember::Out::Row(row) => {
                    let i = self.sweep.as_ref().map(|r| r.rows.len().saturating_sub(1)).unwrap_or(0);
                    self.sweep_say(&row.line(&label, i));
                    if let Some(cc) = self.st().mining.cards.get_mut(idx) {
                        if row.usable() {
                            cc.sweep_note = format!("tuning: step {} done · {:.3} MH/W", i + 1, row.eff);
                        }
                    }
                }
                crate::ember::Out::Finished(row) => {
                    self.sweep_finish(row);
                    return;
                }
                crate::ember::Out::Failed(e) => {
                    self.sweep_abort(&e);
                    return;
                }
            }
        }
        if let Some((words, step, of, eta, plan)) = self.sweep.as_ref().map(|r| (r.words(now), r.rows.len() as u32 + 1, r.plan.len() as u32, r.eta_s(now), r.plan.kind.name())) {
            let (key, mhs, watts) = self.st().mining.cards.get(idx).map(|c| (c.key.clone(), c.hash_now, c.power_w)).unwrap_or_default();
            if let Some(cc) = self.st().mining.cards.get_mut(idx) {
                cc.sweep_note = words.clone();
                cc.state = if cc.state == "mining" { "tuning".into() } else { cc.state.clone() };
                cc.tune_step = step;
                cc.tune_steps = of;
                cc.tune_eta_s = eta;
                cc.tune_plan = plan.into();
                // the clock search's own step count while a clock step runs (the UI's "locking clocks: step 4 of 9")
                if let Some(r) = self.sweep.as_ref() {
                    let power_steps = r.rows.iter().filter(|x| x.point.clock_mhz == 0 && x.mark.is_some()).count() as u32;
                    let on_clock = r.current.as_ref().map(|s| s.kind == crate::ember::Kind::Clock).unwrap_or(false);
                    cc.lock_step = if on_clock { step.saturating_sub(power_steps) } else { 0 };
                    cc.lock_steps = if on_clock { of.saturating_sub(power_steps) } else { 0 };
                }
            }
            if self.shared.runtime.sweep_only {
                // the job playbook forwards this to the installed app's /api/tune-progress
                println!("TUNE progress key={} step={step} of={of} eta_s={eta} mhs={mhs:.2} watts={watts:.1} plan={plan} note={}", key.replace(' ', "_"), words.replace(' ', "_"));
                let _ = std::io::stdout().flush();
            }
        }
    }

    /// The chosen point is in force: record it on the card and in the settings, write the fleet record, say so,
    /// let the helper go. A confirm check that found a better neighbour queues the full plan.
    fn sweep_finish(&mut self, row: crate::ember::Row) {
        let Some(run) = self.sweep.take() else { return };
        // Ember Tune tiers: an automatic tune lands on the card's tier (its own, else the fleet's; balanced from install),
        // the tier's row of THIS search, instead of the goal's choice; the first time, the strip gets its one line
        let (row, tier_applied, first_tier) = {
            let settings = self.shared.settings.lock().unwrap();
            let st = self.st();
            match st.mining.cards.get(run.card) {
                Some(c) if !c.pinned && run.plan.kind != crate::ember::PlanKind::Baseline && c.vendor != "apple" && run.plan.limits.clock_max_mhz > 0 && c.tune_control => {
                    let want = settings.cards.get(&c.key).map(|p| p.tier.clone()).filter(|t| !t.is_empty()).unwrap_or_else(|| settings.tune_tier.clone());
                    let want = if crate::ember::TIER_IDS.contains(&want.as_str()) { want } else { "balanced".to_string() };
                    match crate::ember::tier_row(&crate::ember::tiers(&run.rows), &want) {
                        Some(r) => (r, Some(want), c.tier_at == 0.0),
                        None => (row, None, false),
                    }
                }
                _ => (row, None, false),
            }
        };
        let idx = run.card;
        let unix = crate::platform::unix_now();
        let kind = run.plan.kind;
        let full_due = run.full_due();
        let (name, pinned, control, vendor, driver, class, key) = {
            let mut settings = self.shared.settings.lock().unwrap();
            let mut st = self.st();
            let Some(c) = st.mining.cards.get_mut(idx) else { return };
            c.sweep_pct = row.point.power_pct;
            c.sweep_eff = row.eff;
            c.sweep_watts = row.watts;
            c.sweep_mhs = row.mhs;
            c.sweep_at = unix as f64;
            c.sweep_state = "idle".into();
            if c.state == "tuning" {
                c.state = "mining".into();
            }
            c.tune_step = 0;
            c.tune_steps = 0;
            c.tune_eta_s = 0;
            c.tune_clock_mhz = row.point.clock_mhz;
            c.tune_mem_mhz = row.point.mem_mhz;
            c.tune_source = kind.name().into();
            c.tune_line = crate::ember::result_line(kind, row.mhs, row.watts, row.eff);
            c.tune_curve = run.rows.iter().map(|r| r.json()).collect();
            // Ember Tune tiers (8 October 2026): the three rows of this search; a card with no lever carries its stock row
            if kind == crate::ember::PlanKind::Baseline || c.vendor == "apple" || run.plan.limits.clock_max_mhz == 0 || !c.tune_control {
                c.tiers = crate::ember::stock_tier(&row).iter().map(|t| t.json()).collect();
                c.tier_note = if c.vendor == "apple" { "no lever: Apple silicon sets its own clocks and power".into() } else if !c.tune_control { "no lever: measure only until Power control is on".into() } else if run.plan.limits.clock_max_mhz == 0 { "no lever: no maximum clock reported".into() } else { "no lever: the search measured the stock point only".into() };
                c.tier = "max".into();
            } else {
                let t = crate::ember::tiers(&run.rows);
                let new: Vec<serde_json::Value> = t.iter().map(|x| x.json()).collect();
                if crate::ember::tiers_stale(&c.tiers_class, &c.program_class) {
                    c.knee_loss_pct = crate::ember::knee_loss_pct(&c.name, &c.program_class, &c.tiers, &new);
                }
                c.tiers = new;
                c.tier_note = String::new();
            }
            c.tiers_table = false;
            c.tiers_class = c.program_class.clone();
            c.tiers_remeasure_at = 0.0;
            // the core-clock knob's result (7 October 2026): the chosen lock against the cap point, and why it stopped
            if kind == crate::ember::PlanKind::Baseline {
                c.lock_note = if c.vendor == "apple" || run.plan.limits.clock_max_mhz == 0 { "no lever".into() } else { c.sweep_note.clone() };
            } else {
                let lr = crate::ember::lock_result(&run.plan, &run.rows, &row);
                c.lock_mhz = lr.lock_mhz;
                c.lock_mhs = lr.lock_mhs;
                c.lock_w = lr.lock_w;
                c.lock_mhw = lr.lock_mhw;
                c.unlocked_mhs = lr.unlocked_mhs;
                c.unlocked_w = lr.unlocked_w;
                c.lock_at = unix as f64;
                c.lock_note = lr.lock_note;
            }
            c.lock_step = 0;
            c.lock_steps = 0;
            let control = c.tune_control;
            if kind == crate::ember::PlanKind::Baseline {
                c.sweep_note = if control { String::new() } else { c.sweep_note.clone() };
            } else if c.pinned {
                c.sweep_note = format!("tuning: best {} · {:.3} MH/W; your setting stays pinned", point_words(&row.point), row.eff);
            } else {
                c.power_pct = row.point.power_pct;
                c.clock_cap_mhz = row.point.clock_mhz;
                c.mem_cap_mhz = row.point.mem_mhz;
                if let Some(t) = &tier_applied {
                    c.tier = t.clone();
                    c.tier_at = unix as f64;
                }
                if c.vendor == "nvidia" {
                    c.power_limit_w = row.limit;
                    c.power_applied = true;
                    c.power_note = format!("power cap: {} W held by the tune (best MH per watt)", row.limit as u64);
                }
                c.sweep_note = if full_due { "tuning: a neighbour beat the fleet prior; the full tune runs next".into() } else { String::new() };
            }
            let e = settings.cards.entry(c.key.clone()).or_insert_with(|| crate::config::CardPref { enabled: c.enabled, identities: c.identities, ..Default::default() });
            e.sweep_at = unix;
            e.sweep_pct = row.point.power_pct;
            e.sweep_eff = row.eff;
            e.sweep_watts = row.watts;
            e.sweep_mhs = row.mhs;
            e.sweep_clock_mhz = row.point.clock_mhz;
            e.sweep_mem_mhz = row.point.mem_mhz;
            if let Some(t) = &tier_applied {
                e.tier = t.clone();
                e.tier_at = unix;
            }
            e.tiers = c.tiers.clone();
            e.tiers_class = c.program_class.clone();
            // Miner UI 4: the row's sub-line reads "(the ladder's floor)" when the chosen clock is the lowest step
            let floor_point = row.point.clock_mhz > 0 && row.point.clock_mhz <= run.plan.limits.clock_floor();
            e.sweep_floor = floor_point;
            c.tune_floor = floor_point;
            e.sweep_driver = c.driver.clone();
            e.sweep_class = c.program_class.clone();
            e.sweep_source = kind.name().into();
            if kind == crate::ember::PlanKind::Full {
                if let Some(b) = run.rows.first() {
                    e.sweep_before_watts = b.watts;
                    e.sweep_before_mhs = b.mhs;
                    c.tune_before_watts = b.watts;
                    c.tune_before_mhs = b.mhs;
                }
            }
            if !c.pinned && kind != crate::ember::PlanKind::Baseline {
                e.power_pct = row.point.power_pct;
            }
            (c.name.clone(), c.pinned, control, c.vendor.clone(), c.driver.clone(), c.program_class.clone(), c.key.clone())
        };
        let _ = key;
        self.shared.save_settings();
        // 6 October 2026, run 6: the 5090's chosen clock sat on the ladder's floor (1,854 MHz = the old 60%), so a
        // chosen point at the floor says so: it is the lowest step measured, not the optimum (Ember 2's climb walks on)
        let floor = row.point.clock_mhz > 0 && row.point.clock_mhz <= run.plan.limits.clock_floor();
        self.sweep_say(&format!("TUNE chosen card={} clock={} cap={} mem={} limit={:.0} watts={:.1} mhs={:.2} eff={:.4} plan={}{}{}", run.label, row.point.clock_mhz, row.point.power_pct, row.point.mem_mhz, row.limit, row.watts, row.mhs, row.eff, kind.name(), if pinned { " pinned=1" } else { "" }, if floor { " floor=1 note=floor,_not_optimum" } else { "" }));
        let before = run.rows.first().filter(|_| kind == crate::ember::PlanKind::Full).cloned();
        let record = crate::ember::record_json(crate::platform::unix_now_f(), &crate::config::fingerprint8(&self.shared.runtime.machine_id), VERSION, crate::manifest::platform_name(), &name, &vendor, &driver, &class, kind, &run.rows, Some(&row), before.as_ref(), floor);
        self.sweep_say(&format!("TUNE {record}"));
        let line = crate::ember::result_line(kind, row.mhs, row.watts, row.eff);
        match kind {
            crate::ember::PlanKind::Baseline => self.shared.event("info", &format!("{name}: {line}{}", if control { String::new() } else { " (measure only; no control on this card)".into() })),
            _ if pinned => self.shared.event("ok", &format!("{name}: best point {} ({line}); your setting stays pinned", point_words(&row.point))),
            _ => self.shared.event("ok", &format!("{name}: {line}, {} held", point_words(&row.point))),
        }
        if let Some(t) = &tier_applied {
            if !pinned {
                self.tune_apply(idx, &run.device, &crate::ember::Step { point: row.point, watts: row.limit, kind: crate::ember::Kind::Confirm });
            }
            if first_tier {
                let stock_w = run.rows.first().map(|r| r.watts).unwrap_or(0.0);
                self.shared.event("ok", &crate::ember::tier_set_line(&name, t, &row, stock_w));
            }
        }
        if pinned && kind != crate::ember::PlanKind::Baseline {
            let p = self.st().mining.cards.get(idx).map(|c| crate::ember::Point { clock_mhz: c.clock_cap_mhz, power_pct: c.power_pct, mem_mhz: c.mem_cap_mhz }).unwrap_or(run.before);
            let w = self.st().mining.cards.get(idx).map(requested_watts).unwrap_or(run.before_w);
            self.tune_apply(idx, &run.device, &crate::ember::Step { point: p, watts: w, kind: crate::ember::Kind::Confirm });
        }
        self.sweep_helper_quit();
        if full_due && !pinned {
            self.tune_full_due.insert(idx);
            let k = self.st().mining.cards.get(idx).map(|c| c.key.clone());
            if let Some(k) = k {
                self.sweep_queue.push(k);
            }
            self.sweep_retry.insert(idx, Instant::now() + Duration::from_secs(60));
        }
        self.upload_logs(false);
        if self.shared.runtime.sweep_only && self.sweep_queue.is_empty() {
            self.shared.send(Cmd::Quit("the --sweep run (every card done)"));
        }
    }

    /// Stops a tune: the point goes back to what it was, the log says why, the card waits an hour before the
    /// scheduler tries again (a forced tune under --sweep retries twice).
    fn sweep_abort(&mut self, why: &str) {
        let pending = self.sweep_pending.take();
        let run = self.sweep.take();
        let (idx, label, device, before, before_w, forced, touched) = match (&run, pending) {
            (Some(r), _) => (r.card, r.label.clone(), r.device.clone(), r.before, r.before_w, r.forced, r.seq > 0 && r.plan.kind != crate::ember::PlanKind::Baseline),
            (None, Some((idx, forced))) => {
                let c = self.st().mining.cards.get(idx).cloned();
                let (label, device, w, p) = c.map(|c| (format!("card-{idx}"), c.device.clone(), if c.power_limit_w > 0.0 { c.power_limit_w } else { requested_watts(&c) }, crate::ember::Point { clock_mhz: c.clock_cap_mhz, power_pct: c.power_pct, mem_mhz: c.mem_cap_mhz })).unwrap_or_default();
                (idx, label, device, p, w, forced, false)
            }
            _ => return,
        };
        self.sweep_say(&format!("TUNE aborted card={label} reason={}", why.replace(' ', "_")));
        let name = {
            let mut st = self.st();
            match st.mining.cards.get_mut(idx) {
                Some(c) => {
                    c.sweep_state = "idle".into();
                    if c.state == "tuning" {
                        c.state = "mining".into();
                    }
                    c.tune_step = 0;
                    c.tune_steps = 0;
                    c.tune_eta_s = 0;
                    c.sweep_note = format!("tuning stopped: {why}");
                    if touched {
                        c.power_pct = before.power_pct;
                        c.clock_cap_mhz = before.clock_mhz;
                        c.power_applied = false;
                        c.power_note = format!("cap restored to {} W after the tune stopped", before_w as u64);
                    }
                    c.name.clone()
                }
                None => label.clone(),
            }
        };
        if touched && !device.is_empty() {
            self.sweep = None;
            self.tune_restore(idx, &device, before, before_w);
        }
        self.sweep_helper_quit();
        self.shared.event(if touched { "error" } else { "info" }, &format!("{name}: tuning stopped ({why}){}", if touched { format!("; back to {} and {} W", point_words(&before), before_w as u64) } else { String::new() }));
        let attempts = self.sweep_attempts.get(&idx).copied().unwrap_or(0);
        if forced && self.shared.runtime.sweep_only && attempts < 3 && !self.quitting {
            let key = self.st().mining.cards.get(idx).map(|c| c.key.clone());
            if let Some(key) = key {
                self.sweep_queue.push(key);
            }
            self.sweep_retry.insert(idx, Instant::now() + Duration::from_secs(30));
        } else {
            self.sweep_retry.insert(idx, Instant::now() + Duration::from_secs(3600));
            if self.shared.runtime.sweep_only && self.sweep_queue.is_empty() {
                self.shared.send(Cmd::Quit("the --sweep run (every card done)"));
            }
        }
    }

    /// Puts a card back on a point outside a run (the abort path): the same commands as a step, with no run to
    /// acknowledge them.
    fn tune_restore(&mut self, idx: usize, device: &str, point: crate::ember::Point, watts: f64) {
        let saved = self.sweep.take();
        self.tune_apply(idx, device, &crate::ember::Step { point, watts, kind: crate::ember::Kind::Confirm });
        self.sweep = saved;
    }

    // ---- the tick ----------------------------------------------------------------------------------------------

    fn tick(&mut self) {
        let now = Instant::now();
        // the worker layer's tick (src/sup.rs): the supervisor drives its workers, verifies their results and keeps
        // its own watchdog; its slots' live words are laid over the engine's detected cards and the fleet's state
        // word and rate follow (the old miner path, when kept with IGNEUM_APP_OLD_MINER=1, writes the same fields)
        self.sup.tick();
        // a layer that refused to start is tried again while the engine runs (the no-manual-inputs rule: the pool's
        // document arriving a minute late must not leave the app idle until a hand restarts it)
        if self.running && self.layer_retry_at.map_or(false, |t| now >= t) {
            self.layer_retry_at = None;
            self.restart_layer("the earlier start was refused");
        }
        // the supervisor's own lines (its stop reasons, its source's words, each slot's fate) into the app's log
        // file as they come: the mini's Mac app stopped mining twice on 12 October 2026 with the reason in the
        // supervisor's ring and nowhere a reading could find it
        {
            let (seq, lines) = self.sup.log_since(self.sup_log_seq);
            if seq != self.sup_log_seq {
                self.sup_log_seq = seq;
                for l in lines {
                    // the supervisor's line carries its own clock; the app's log adds one (the project's read of 13 October 2026: the
                    // time printed twice), so the supervisor's is dropped here
                    let l = l.get(9..).filter(|_| l.len() > 9 && l.as_bytes()[2] == b':' && l.as_bytes()[5] == b':' && l.as_bytes()[8] == b' ').unwrap_or(&l);
                    self.shared.log(&format!("sup: {l}"));
                }
            }
        }
        let mut tune_feed: Option<f64> = None;
        if crate::sup::layer_on() && self.running {
            let snap = self.sup.snapshot();
            let mut st = self.st();
            let (word, rate) = crate::sup::overlay_snapshot(&mut st.mining.cards, &snap);
            // the knee under the worker layer (0.2.22): the sweeping card's 10 s reported rate feeds its run, counted
            // while holding once the hold has outlasted the window (the old path fed the miner's STATUS line; nothing fed
            // the layer, so every plan measured no rate and the Tune page hid its controls)
            tune_feed = self.sweep.as_ref().map(|r| r.card).and_then(|card| st.mining.cards.iter().find(|c| c.index == card && c.state == "mining" && c.hash_10s > 0.0).map(|c| c.hash_10s));
            let joins: Vec<String> = st.mining.cards.iter_mut().filter(|c| !c.slot_join_line.is_empty()).map(|c| std::mem::take(&mut c.slot_join_line)).collect();
            for l in &joins {
                self.shared.log(l);
            }
            if st.mining.state != "paused" || word == "paused" {
                st.mining.state = word.into();
            }
            st.mining.hash_total = rate;
            st.mining.accepted_session = snap.totals.accepted;
            st.mining.rejected_session = snap.totals.rejected + snap.totals.invalid + snap.totals.source_invalid;
            st.mining.held_session = snap.totals.held;
            st.mining.cancelled_session = snap.totals.cancelled;
            st.mining.held_case = snap.totals.held_case.clone();
            if st.mining.notices != snap.notices {
                st.mining.notices = snap.notices.clone();
            }
            if st.mining.shares.len() != snap.recent_shares.len() || st.mining.shares.last() != snap.recent_shares.last() {
                st.mining.shares = snap.recent_shares.clone();
            }
            // the work source's state for the pill and the card lines (0.2.3): the settings name it, the supervisor
            // says whether it answers
            {
                let (kind, surl) = { let s = self.shared.settings.lock().unwrap(); (s.source_kind.clone(), s.source_url.clone()) };
                let src = snap.source.as_ref();
                let next = crate::state::SourceState {
                    kind: if kind.trim().is_empty() { "pool".into() } else { kind },
                    url: surl,
                    name: src.map(|v| v.name.clone()).unwrap_or_default(),
                    connected: src.map_or(false, |v| v.connected),
                    problem: src.and_then(|v| v.problem.clone()).unwrap_or_default(),
                    height: src.map_or(0, |v| v.height),
                    chain_id: src.map_or(0, |v| v.chain_id),
                    relation: src.map(|v| v.relation.clone()).unwrap_or_default(),
                    waiting_for_work_s: src.map_or(0, |v| v.waiting_for_work_s),
                };
                if st.source != next {
                    st.source = next;
                }
            }
            // the share lane's counts (the window's Mine totals carry them while the lane is on)
            st.mining.share_lane = snap.totals.share_lane;
            st.mining.shares_accepted = snap.totals.shares_accepted;
            st.mining.shares_refused = snap.totals.shares_refused;
            st.mining.shares_per_block = snap.totals.shares_per_block;
            if st.mining.share_reasons != snap.totals.share_refused_reasons {
                st.mining.share_reasons = snap.totals.share_refused_reasons.clone();
            }
            // the dev fee as the supervisor resolved it (ED-05): on with the rate and the address from the signed
            // manifest, or off with the reason; the fee's own counts
            st.dev_fee.on = snap.totals.dev_fee_percent > 0;
            st.dev_fee.percent = u32::from(snap.totals.dev_fee_percent);
            st.dev_fee.address = snap.totals.dev_fee_address.clone();
            st.dev_fee.line = snap.totals.dev_fee_words.clone();
            st.dev_fee.templates = snap.totals.dev_fee_templates;
            st.dev_fee.shares_accepted = snap.totals.dev_fee_shares_accepted;
            st.dev_fee.fee_words = snap.totals.fee_words.clone();
            st.dev_fee.receipts_total = snap.totals.receipts.total;
            st.dev_fee.receipts_v1_verified = snap.totals.receipts.v1_verified;
            st.dev_fee.receipts_v1_failed = snap.totals.receipts.v1_failed;
            st.dev_fee.receipts_file = snap.totals.receipts.file.clone();
        }
        if let (Some(mhs), Some(run)) = (tune_feed, self.sweep.as_mut()) {
            run.sample_rate_after(mhs, Duration::from_secs(10), now);
        }
        if now.duration_since(self.last_awake) >= Duration::from_secs(60) {
            self.last_awake = now;
            crate::platform::keep_awake_tick();
        }
        if self.cap_retry_at.map(|t| now >= t).unwrap_or(false) && !self.power_busy && self.sweep.is_none() && self.sweep_pending.is_none() {
            self.cap_retry_at = None;
            for c in self.st().mining.cards.iter_mut().filter(|c| c.vendor == "nvidia" && !c.power_applied) {
                c.power_applied = false;
            }
            self.apply_power_limits("retry after a refusal");
        }
        if self.running {
            self.tick_watch(now);
            self.tick_exec_probe(now);
            if now >= self.orphan_sweep_next {
                self.orphan_sweep_next = now + Duration::from_secs(60);
                self.sweep_orphan_miners("the minute sweep");
            }
            self.tick_miners(now);
            if let Some(at) = self.resume_check_at {
                if now >= at {
                    self.resume_check_at = None;
                    let (cards, paused) = { let st = self.st(); (st.mining.cards.clone(), st.mining.paused) };
                    if !paused {
                        for line in resume_check(&cards) {
                            self.shared.log(&format!("resume: {line}"));
                            self.shared.event("error", &format!("resume: {line}"));
                        }
                    }
                }
            }
            self.tick_telemetry(now);
            self.tick_sweep(now);
        }
        self.tick_heat(now);
        self.derive(now);
        self.tick_balance(now);
        if now.duration_since(self.last_status) >= Duration::from_secs(self.shared.runtime.status_secs as u64) {
            self.last_status = now;
            if self.running {
                self.status_line();
            }
        }
        if now.duration_since(self.last_upload) >= Duration::from_secs(60) {
            self.last_upload = now;
            self.upload_logs(false);
        }
        self.tick_update();
        self.tick_jobs();
        if now >= self.clock_next_https {
            self.command(Cmd::ClockCheck);
        }
        if self.clock_node_at.map(|t| now.duration_since(t) > Duration::from_secs(90)).unwrap_or(false) {
            // the node stopped complaining: the clock (or the peers) changed
            self.clock_node_at = None;
            self.resolve_clock();
        }
        if self.wrapper && now.duration_since(self.last_state_print) >= Duration::from_secs(3) {
            self.last_state_print = now;
            println!("STATE {}", self.shared.wrapper_state());
            let _ = std::io::stdout().flush();
        }
        if now.duration_since(self.last_settings_save) >= Duration::from_secs(120) {
            self.last_settings_save = now;
            self.shared.save_settings();
        }
        // hot-plug (src/hotplug.rs): enumerate again every POLL_S, and keep the card list in the log for the console
        if self.detected && !self.detect_busy && !self.quitting && now >= self.detect_next {
            self.detect_next = now + Duration::from_secs(crate::hotplug::POLL_S);
            self.shared.send(Cmd::Detect);
        }
        if self.detected && now.duration_since(self.last_cards_line) >= Duration::from_secs(crate::hotplug::CARDS_LINE_S) {
            self.last_cards_line = now;
            let line = crate::hotplug::cards_line(&self.st().mining.cards);
            self.shared.log(&line);
        }
    }

    /// The over-the-air updater (src/ota.rs): checks, downloads and stages on its own threads; this tick hands it what
    /// a safe moment needs and applies when it says so.
    fn tick_update(&mut self) {
        let ctx = {
            let st = self.st();
            crate::ota::Ctx {
                node_synced: st.node.synced && st.clock.severity != "block",
                // Horizon frontier lane: finality paused = a synced node with no checkpoint lock for FINALITY_PAUSE_S
                // (the last LOCK line's age; or, when none was ever seen this run, the engine's own uptime)
                finality_paused: st.finality.paused,
                boundary_eta_s: if st.node.daa > 0 && st.program.boundary_daa > 0 { Some(st.program.eta_s) } else { None },
                // a remote job in progress counts as busy: no update applies under it (src/jobrun.rs)
                miner_busy: self.miners.iter().any(|m| m.building) || st.mining.cards.iter().any(|c| c.enabled && c.state == "starting"),
                // an earlier release: no card mining or starting and nothing building: the staged update applies at once on a synced node
                miner_idle: !self.miners.iter().any(|m| m.building) && !st.mining.cards.iter().any(|c| c.enabled && (c.state == "mining" || c.state == "starting")),
                // a remote job in progress holds the update, urgent or not (src/manifest.rs safe_to_apply; the first Windows machine, 6 October 2026)
                job_active: self.jobs.active(),
                daa: st.node.daa,
            }
        };
        // C35 (the first Windows machine, 5 October 2026, 22:31 UTC): a second engine started by a measurement job found itself under the
        // manifest's min_supported_version ("urgent" beats auto_update = false), ran the per-user installer, and the
        // installer's PrepareToInstall quit the INSTALLED app through its api/quit. A second engine never updates.
        if !self.no_ota {
            if let Some(crate::ota::Action::Apply) = self.ota.tick(&self.shared, &ctx) {
                self.apply_update();
            }
        }
        if let Some(ui) = self.ota.take_ui_change() {
            self.uiota.consider(&self.shared, ui.as_ref(), VERSION);
        }
        self.uiota.tick(&self.shared, Instant::now());
        if let Some(p) = self.ota.take_override_change() {
            self.shared.log(&format!("consensus override changed ({}); the node restarts with it at a safe moment", p.display()));
            self.node_override_restart = true;
        }
        if self.ota.take_drivers_change() {
            self.drivers_table = self.ota.drivers_table();
            self.refresh_driver_offers();
        }
        if let Some(p) = self.ota.take_tuning_change() {
            // no restart: a worker started before this reads the file at its next prepare only if it was started with
            // the path, so a miner without it is restarted at the next hour boundary by the usual path (exit 42 or
            // restart); a running worker that has the path picks the change up at the next prepare by itself
            self.shared.log(&format!("kernel tuning changed ({}); the workers read it at their next hourly prepare", p.display()));
        }
        if self.node_override_restart && self.node.is_some() && !self.node_external {
            // between hourly boundaries unless the switch is close
            let (eta, daa) = { let st = self.st(); (st.program.eta_s, st.node.daa) };
            let urgent = crate::manifest::fork_is_close(Some(self.ota.override_daa()).filter(|h| *h > 0), daa);
            let safe = eta > crate::manifest::BOUNDARY_GUARD_S && eta < 3600 - crate::manifest::BOUNDARY_GUARD_S;
            if urgent || safe || self.st().node.daa == 0 {
                self.node_override_restart = false;
                self.st().node.override_restart_wait = String::new();
                self.shared.event("info", "restarting the node with the new consensus parameters");
                self.stop_miners("consensus parameters changed");
                self.stop_node();
                self.start_node();
                for m in self.miners.iter_mut() {
                    m.restart_at = Some(Instant::now() + Duration::from_secs(5));
                }
            } else {
                self.st().node.override_restart_wait = format!("node restarts with the new consensus parameters after the hour boundary ({} s)", eta);
            }
        }
    }

    /// The remote-job runner (src/jobrun.rs): polls and runs on its own threads; this tick starts queued jobs and
    /// does what a job asks of the engine (miners stopped and held, a restart, the updater).
    fn tick_jobs(&mut self) {
        if self.quitting {
            return;
        }
        if let Some(a) = self.jobs.tick(&self.shared) {
            self.job_action(a);
        }
        let release = if self.job_hold {
            // MF-6: the hold belongs to the job that took it, releases when that job is gone (whatever runs next)
            // or at its own cap; the runner's own flag is read too
            let held_s = self.job_hold_since.map(|t| t.elapsed().as_secs_f64()).unwrap_or(0.0);
            let active = self.jobs.active_id();
            crate::jobrun::hold_release(self.job_hold_owner.as_deref(), active.as_deref(), held_s, self.job_hold_cap_s).or(if !self.jobs.holds_miners() { Some("the runner released the miners") } else { None })
        } else {
            None
        };
        if let Some(why) = release {
            self.job_hold = false;
            self.job_hold_owner = None;
            self.job_hold_since = None;
            self.shared.log(&format!("job hold released: {why}"));
            for c in self.st().mining.cards.iter_mut().filter(|c| c.state == "held" || c.state == "tuning") {
                c.state = "off".into();
                c.sweep_state = if c.sweep_state == "running" { "idle".into() } else { c.sweep_state.clone() };
                c.tune_step = 0;
                c.tune_steps = 0;
                c.tune_eta_s = 0;
            }
            if self.running {
                self.shared.event("info", "job finished; the miners restart");
                for m in self.miners.iter_mut() {
                    m.restart_at = Some(Instant::now());
                }
            }
        }
    }

    // ---- Ember Heat (src/heat.rs) ------------------------------------------------------------------------------

    /// The heat loop, every tick: the reading, the decision, the rest or the release, the state and the log line.
    fn tick_heat(&mut self, now: Instant) {
        let s = self.shared.settings.lock().unwrap().clone();
        let unix = crate::platform::unix_now_f();
        if !s.heat_on {
            if self.heat_rest {
                self.heat_release("heat mode off");
            }
            let mut st = self.st();
            if st.heat.on || st.heat.phase != "off" {
                st.heat = heat_state_off(false, s.heat_set_c);
            }
            return;
        }
        let (cards, paused, synced) = {
            let st = self.st();
            (st.mining.cards.clone(), st.mining.paused, st.node.synced && st.clock.severity != "block")
        };
        let present: Vec<&CardState> = cards.iter().filter(|c| c.enabled && c.present()).collect();
        // the coolest card with a sensor reading under a minute old stands for the room once the cards have rested
        let card_c = present.iter().filter(|c| c.temp_gpu > 0.0 && unix - c.telemetry_at < 60.0).map(|c| c.temp_gpu).fold(0.0f64, |a, b| if a == 0.0 { b } else { a.min(b) });
        let rest_for = self.heat_rest_since.map(|t| now.duration_since(t).as_secs_f64()).unwrap_or(0.0);
        if card_c > 0.0 {
            self.heat_card_samples.push_back((unix, card_c));
        }
        while self.heat_card_samples.front().map(|(t, _)| unix - t > 30.0).unwrap_or(false) {
            self.heat_card_samples.pop_front();
        }
        let card_slope = crate::heat::slope_of(self.heat_card_samples.iter().copied());
        // a new typed reading, taken while the cards rest, teaches the idle offset
        if s.heat_room_at > 0.0 && s.heat_room_at != self.heat_room_seen {
            self.heat_room_seen = s.heat_room_at;
            if let Some(off) = crate::heat::learned_offset(card_c, rest_for, s.heat_room_c) {
                self.shared.settings.lock().unwrap().heat_offset_c = off;
                self.shared.save_settings();
                self.st().settings.heat_offset_c = off;
                self.shared.log(&format!("heat: idle offset learned: card {card_c:.1} minus room {:.1} = {off:.1} degrees", s.heat_room_c));
            }
        }
        let offset = self.shared.settings.lock().unwrap().heat_offset_c;
        let typed = if s.heat_room_at > 0.0 { Some((s.heat_room_c, s.heat_room_at)) } else { None };
        let reading = crate::heat::room(unix, typed, card_c, card_slope, rest_for, offset);
        let set_c = crate::heat::set_point_at(s.heat_set_c, &s.heat_schedule, crate::heat::minute_of_day(unix, s.heat_tz_min));
        let d = self.heat.step(unix, set_c, reading);
        if d.new_period {
            self.shared.log(&format!("heat: period duty={:.2} set={set_c:.1} room={} src={} integral={:.2}", d.duty, if reading.source == crate::heat::Source::None { "-".to_string() } else { format!("{:.1}", reading.room_c) }, reading.source.name(), self.heat.integral));
        }
        let can_run = self.running && !paused && !present.is_empty() && !self.job_hold && !self.jobs.holds_miners() && !self.quitting;
        if can_run && !d.heating && !self.heat_rest {
            self.heat_rest(set_c, &reading);
        } else if (d.heating || !can_run) && self.heat_rest {
            self.heat_release(if d.heating { "heating again" } else { "the cards are not ours to rest" });
        }
        let heating_now = cards.iter().any(|c| c.state == "mining");
        let heat_w: f64 = cards.iter().filter(|c| c.state == "mining").map(|c| c.power_w.max(0.0)).sum();
        if heating_now && heat_w > 0.0 {
            self.heat_full_w = heat_w;
        }
        self.heat_samples.push_back((unix, heating_now));
        while self.heat_samples.front().map(|(t, _)| unix - t > 3600.0).unwrap_or(false) {
            self.heat_samples.pop_front();
        }
        let duty_hour = if self.heat_samples.len() > 1 { self.heat_samples.iter().filter(|(_, h)| *h).count() as f64 / self.heat_samples.len() as f64 } else { 0.0 };
        let phase = if !self.running || paused { "paused" } else if present.is_empty() { "waiting" } else if self.heat_rest { "resting" } else if !synced && !heating_now { "waiting" } else { "heating" };
        let hash: f64 = cards.iter().filter(|c| c.state == "mining").map(|c| c.hash_now).sum();
        {
            let mut st = self.st();
            let h = &mut st.heat;
            h.on = true;
            h.phase = phase.into();
            h.set_c = set_c;
            h.room_c = reading.room_c;
            h.room_source = reading.source.name().into();
            h.room_error_c = reading.error_c;
            h.room_age_s = reading.age_s;
            h.duty = d.duty;
            h.duty_hour = duty_hour;
            h.heat_w = heat_w;
            h.full_w = self.heat_full_w;
            h.heat_avg_w = d.duty * self.heat_full_w;
            h.until_s = d.until_s;
            h.period_s = crate::heat::PERIOD_S;
            h.offset_c = if offset > 0.0 { offset } else { crate::heat::OFFSET_DEFAULT_C };
            h.offset_learned = offset > 0.0;
            h.note = crate::heat::words(true, phase, set_c, &reading, d.duty, d.until_s);
        }
        if unix - self.heat_log_at >= crate::heat::LOG_EVERY_S {
            self.heat_log_at = unix;
            self.shared.log(&crate::heat::log_line(unix, phase, set_c, &reading, d.duty, heat_w, hash, d.until_s));
        }
    }

    /// The rest: the miners stop, the rows say why, nothing restarts them until the release.
    fn heat_rest(&mut self, set_c: f64, reading: &crate::heat::Reading) {
        self.heat_rest = true;
        self.heat_rest_since = Some(Instant::now());
        self.stop_miners("heat mode: the room is at the set point");
        if crate::sup::layer_on() {
            // the worker layer rests under its own cause: the schedule holds it, only the release below lifts it
            self.sup.pause_for(igneum_supervisor::schedule::PauseCause::Heat);
        }
        let msg = match reading.source {
            crate::heat::Source::None => format!("resting to read the room, then holding {set_c:.1} °C (heat mode)"),
            _ => format!("resting: the room is {:.1} °C, holding {set_c:.1} °C (heat mode)", reading.room_c),
        };
        for c in self.st().mining.cards.iter_mut().filter(|c| c.enabled && c.present() && c.state != "faulted") {
            c.state = "resting".into();
            c.message = msg.clone();
        }
    }

    /// The release: the rows go back to off, every slot is re-armed, the workers come back with a fresh pack check.
    fn heat_release(&mut self, why: &str) {
        self.heat_rest = false;
        self.heat_rest_since = None;
        for c in self.st().mining.cards.iter_mut().filter(|c| c.state == "resting") {
            c.state = "off".into();
            c.message = String::new();
        }
        if self.running {
            self.shared.log(&format!("heat: release ({why}); the miners restart"));
            if crate::sup::layer_on() {
                self.sup.resume();
            }
            let now = Instant::now();
            for m in self.miners.iter_mut() {
                // no permanent fault (7 October 2026): every slot restarts; a ladder delay in force stays as scheduled
                if m.restart_at.map(|at| at <= now).unwrap_or(true) {
                    m.restart_at = Some(now);
                }
                m.prepared = false;
            }
        }
    }

    fn job_action(&mut self, a: crate::jobrun::Action) {
        use crate::jobrun::Action;
        match a {
            Action::StopMiners(why) => {
                self.job_hold = true;
                self.job_hold_owner = self.jobs.active_id();
                self.job_hold_since = Some(Instant::now());
                self.job_hold_cap_s = (self.jobs.active_cap_minutes().max(1) * 60) as f64;
                self.stop_miners(&why);
                for c in self.st().mining.cards.iter_mut().filter(|c| c.enabled) {
                    // never a bare "off" at 0 MH/s: the row says why (a job holds the card)
                    c.state = "held".into();
                    c.message = format!("held for a remote job: {}", short(&why, 80));
                }
                self.jobs.miners_stopped(&self.shared);
            }
            Action::ApplyCards(choices) => {
                // the signed `cards` kind (7 October 2026): through the app's own card path, persisted, read back
                let names: Vec<String> = choices.iter().map(|c| format!("{} enabled={} identities={}", c.key, c.enabled, c.identities)).collect();
                self.shared.event("info", &format!("remote job: card settings: {}", names.join("; ")));
                let keys: Vec<String> = choices.iter().map(|c| c.key.clone()).collect();
                self.apply_cards(choices);
                let readback: Vec<(String, bool, u32, u32)> = self.st().mining.cards.iter().filter(|c| keys.contains(&c.key)).map(|c| (c.key.clone(), c.enabled, c.identities, c.power_pct)).collect();
                if let Some(next) = self.jobs.cards_applied(&self.shared, readback) {
                    self.job_action(next);
                }
            }
            Action::CardsOff(keys) => {
                // `--cards-off` (6 October 2026): the runner switches the job's cards through the app's own card path
                // and keeps the exact choices that put them back; the script never touches /api/cards
                let live: Vec<crate::jobrun::LiveCard> = self.st().mining.cards.iter().map(|c| (c.key.clone(), c.enabled, c.identities, c.power_pct, c.present())).collect();
                let (off, restore) = crate::jobrun::cards_off_choices(&keys, &live);
                if off.is_empty() {
                    self.shared.event("info", &format!("remote job asked for cards off ({}) but no present card matched; nothing switched", keys.join(",")));
                } else {
                    self.shared.event("info", &format!("remote job: cards off for the job: {}", off.iter().map(|c| c.key.as_str()).collect::<Vec<_>>().join(", ")));
                    self.apply_cards(off);
                }
                if let Some(next) = self.jobs.cards_off_done(&self.shared, restore) {
                    self.job_action(next);
                }
            }
            Action::RestoreCards(choices) => {
                self.shared.event("info", &format!("remote job: cards restored: {}", choices.iter().map(|c| format!("{} enabled={} identities={}", c.key, c.enabled, c.identities)).collect::<Vec<_>>().join("; ")));
                self.apply_cards(choices);
            }
            Action::RestartMiners => {
                self.stop_miners("remote job: restart miners");
                if crate::sup::layer_on() {
                    self.restart_layer("remote job: restart miners");
                }
                for m in self.miners.iter_mut() {
                    m.restart_at = Some(Instant::now());
                }
            }
            Action::RestartNode => {
                if self.node_external {
                    self.shared.event("info", "remote job asked for a node restart, but the node is external; nothing done");
                } else {
                    self.restart_node("restart asked by a remote job", Duration::from_secs(3));
                }
            }
            Action::RestartApp => {
                // v0.2.0 (the first Windows machine, 6 and 8 October 2026): under the window host the engine exits with a plain EXIT and the
                // host's ladder starts it again in 10 s; a detached helper raced that ladder for the single instance and
                // lost twice, the app down for hours. The helper only when no host runs (--boot, a headless start).
                match restart_plan(self.wrapper) {
                    RestartPlan::HostLadder => self.shared.event("info", "remote job: the app restarts (the miners stop, then the app; the window opens it again in about 10 s)"),
                    RestartPlan::Helper => {
                        self.shared.event("info", "remote job: the app restarts (the miners stop, then the app, then it opens again)");
                        if let Err(e) = crate::jobrun::spawn_relaunch_helper(&self.shared) {
                            self.shared.event("error", &format!("remote job: the app cannot restart by itself ({e}); it keeps running"));
                            return;
                        }
                    }
                }
                self.quit_source = "remote job: app restart";
                self.quitting = true;
                self.st().quitting = true;
            }
            Action::UpdateNow => {
                self.shared.event("info", "remote job: update check now; a newer version installs at once");
                self.ota.install_now(&self.shared);
            }
        }
    }

    /// Hands over to the update helper, then leaves through the quit path (miners first, then the node, the last
    /// log upload, EXIT for the window). The helper waits for this process to end before it swaps the app.
    fn apply_update(&mut self) {
        if self.quitting {
            return;
        }
        let v = self.ota.version();
        match self.ota.launch_apply(&self.shared, self.host_pid()) {
            Ok(crate::ota::Launch::QuitNow) => {
                {
                    let mut st = self.st();
                    st.update.applying = true;
                    st.update.status = "applying".into();
                    st.update.wait = String::new();
                    st.quitting = true;
                }
                self.shared.event("info", &format!("installing Ember {v}: the miners stop, then the app closes and opens again"));
                self.quitting = true;
            }
            Ok(crate::ota::Launch::InstallerRunning) => {
                // Windows: the installer stops this engine itself (api/quit) once it may run; until then we mine
                {
                    let mut st = self.st();
                    st.update.applying = true;
                    st.update.status = "applying".into();
                    st.update.wait = "the installer is starting; if Windows asks for permission the miners keep running until it is given".into();
                }
                self.shared.event("info", &format!("installing Ember {v}: the installer runs first; the miners keep running until it is allowed to, then it stops them, the app closes and opens again"));
            }
            Err(e) => {
                self.shared.event("error", &format!("the update could not start: {e}"));
                let mut st = self.st();
                st.update.error = e;
                st.update.status = "error".into();
            }
        }
    }

    /// The window host's pid when the engine runs under one (macOS: the helper asks it to quit).
    fn host_pid(&self) -> u32 {
        #[cfg(unix)]
        {
            if self.wrapper { unsafe { libc::getppid() as u32 } } else { 0 }
        }
        #[cfg(not(unix))]
        {
            0
        }
    }

    fn tick_watch(&mut self, _now: Instant) {}

    fn tick_miners(&mut self, _now: Instant) {}

    fn restart_node(&mut self, why: &str, delay: Duration) {
        self.stop_miners(why);
        self.node_restart_at = Some(Instant::now() + delay);
        for m in self.miners.iter_mut() {
            m.restart_at = Some(Instant::now() + delay + Duration::from_secs(5));
        }
    }

    fn sweep_orphan_miners(&mut self, _why: &str) {}

    fn tick_exec_probe(&mut self, now: Instant) {
        let node_up = self.node_external || self.node.is_some();
        if !node_up || self.exec_probe_busy || now < self.exec_probe_next {
            return;
        }
        self.exec_probe_busy = true;
        self.exec_probe_next = now + Duration::from_secs(5);
        let port = self.shared.runtime.evm_port();
        let shared = self.shared.clone();
        std::thread::spawn(move || {
            let (answered, has_record) = crate::execrpc::probe(port);
            shared.send(Cmd::ExecProbe { answered, has_record });
        });
    }

    /// One fault line to the log intake, the moment it happens (the founder, 7 October 2026: the team sees it before the
    /// user): the card, the class, the reason and the app and node versions. At most 60 an hour, off the engine
    /// thread; the same line is in the app log either way.
    fn fault_report(&mut self, card: Option<&str>, class: &str, reason: &str) {
        let now = Instant::now();
        self.fault_reports.retain(|t| now.duration_since(*t) < Duration::from_secs(3600));
        let line = format!("FAULT class={class} card=\"{}\" app={} reason=\"{}\"", card.unwrap_or("node"), VERSION, crate::platform::redact(reason).replace('"', "'"));
        self.shared.log(&line);
        let p = &self.shared.packaged;
        if p.upload_url.is_empty() || p.upload_key.is_empty() || self.fault_reports.len() >= 60 {
            return;
        }
        self.fault_reports.push(now);
        let (url, key, machine, run_id) = (p.upload_url.clone(), p.upload_key.clone(), format!("{}-{}", self.shared.runtime.host, self.shared.runtime.id8()), format!("{}-{}", self.label_base, self.stamp));
        let label = format!("fault-{}", self.label_base);
        let text = format!("{}\n{:.3} {line}", self.shared.upload_header(), crate::platform::unix_now_f());
        std::thread::spawn(move || {
            crate::update::upload_text(&url, &key, &label, &machine, &run_id, &text);
        });
    }

    fn tick_balance(&mut self, now: Instant) {
        if self.balance_busy || self.balance_next.map(|t| now < t).unwrap_or(false) {
            return;
        }
        let (addr, node_up) = {
            let st = self.st();
            (st.address.value.clone(), matches!(st.node.state.as_str(), "syncing" | "synced"))
        };
        if addr.len() != 42 || !node_up {
            self.balance_next = Some(now + Duration::from_secs(10));
            return;
        }
        self.balance_busy = true;
        self.balance_next = Some(now + Duration::from_secs(30));
        let shared = self.shared.clone();
        std::thread::spawn(move || {
            let r = crate::execrpc::call(shared.runtime.evm_port(), "eth_getBalance", serde_json::json!([addr, "latest"]), Duration::from_secs(8)).and_then(|v| v.as_str().map(|s| s.to_string()).ok_or_else(|| "eth_getBalance: not a string".to_string()));
            shared.send(Cmd::BalanceRead(r));
            // the same 30-s cadence carries the node's finality fields (the node lane, an earlier release); a node without them
            // answers without the keys and the engine's own rule stands
            let raw = crate::execrpc::call(shared.runtime.evm_port(), "igneum_getProvingStatus", serde_json::json!([]), Duration::from_secs(8)).ok();
            // review B F04 (8 October 2026): the newest lock's kind ("certified" | "recovery") and its why, when the node
            // carries them (the node lane's lockKind); a recovery lock is labelled "recovery" on every surface, never "final"
            if let Some(v) = raw.as_ref() {
                let kind = v.get("lockKind").and_then(|x| x.as_str()).unwrap_or("").to_string();
                let why = v.get("lockWhy").and_then(|x| x.as_str()).unwrap_or("").to_string();
                let mut st = shared.state.lock().unwrap();
                st.finality.lock_kind = kind;
                st.finality.lock_why = why;
            }
            let f = raw.as_ref().and_then(crate::ember::parse_finality_status);
            shared.send(Cmd::FinalityStatus(f));
        });
    }

    /// Derived fields: ages, the hash total, the program countdown, the finality age, the mining state word.
    fn derive(&mut self, now: Instant) {
        let unix = crate::platform::unix_now_f();
        let mut st = self.st();
        st.node.last_reading_age_s = self.node_last_reading.map(|t| now.duration_since(t).as_secs_f64()).unwrap_or(-1.0);
        let mut total = 0.0;
        for c in st.mining.cards.iter_mut() {
            if c.state == "mining" {
                total += c.hash_now;
            }
            // live hash per watt (the number next to the draw on the tile); needs a draw reading under a minute old
            c.eff_mhw = if c.state == "mining" && c.power_w > 1.0 && c.hash_now > 0.0 && unix - c.telemetry_at < 60.0 { c.hash_now / c.power_w } else { 0.0 };
        }
        st.mining.hash_total = total;
        // Miner UI 4: the fleet's draw and its £ a day (settings.power_price_pence; 0 when no price is set)
        st.mining.watts_total = crate::ember::fleet_watts(st.mining.cards.iter().map(|c| (c.state.as_str(), c.power_w, c.telemetry_at)), unix, 60.0);
        st.mining.pounds_per_day = if st.settings.power_price_pence > 0.0 { crate::ember::pounds_per_day(st.mining.watts_total, st.settings.power_price_pence) } else { 0.0 };
        st.address.balance_age_s = if self.balance_at > 0.0 { unix - self.balance_at } else { -1.0 };
        crate::hotplug::age(&mut st.mining.cards, unix);
        let cut = unix - 3600.0;
        st.mining.found.retain(|t| *t > cut);
        if st.node.daa > 0 {
            let computed = (st.node.daa / POW_EPOCH_BLOCKS + 1) * POW_EPOCH_BLOCKS;
            let boundary = if st.program.boundary_daa > st.node.daa { st.program.boundary_daa } else { computed };
            st.program.boundary_daa = boundary;
            st.program.eta_s = boundary as i64 - st.node.daa as i64;
            st.program.epoch_index = st.node.daa / POW_EPOCH_BLOCKS;
            st.program.message = if st.program.prepared {
                "next program compiled and ready".into()
            } else if st.program.prepare_sent {
                "next program compiling".into()
            } else {
                "next program not known yet".into()
            };
        }
        if st.finality.last_lock > 0 {
            st.finality.age_s = unix - st.finality.last_lock_at;
            st.finality.message = String::new();
        }
        // Horizon polish Q83/Q84: finality paused = a synced node with no checkpoint lock for FINALITY_PAUSE_S (the
        // last lock's age, else the engine's uptime); since the last lock (else the start); one sentence everywhere
        let gap = if st.finality.last_lock > 0 { st.finality.age_s } else { st.uptime_s as f64 };
        // the node's own word wins when it carries one (an earlier release, finalityActive); else the engine's 15-minute rule
        let paused = match st.finality.node_active {
            Some(active) => !active,
            None => st.node.synced && gap > crate::manifest::FINALITY_PAUSE_S,
        };
        if paused && !st.finality.paused && st.finality.cause_source != "node" {
            st.finality.paused_since = if st.finality.last_lock > 0 { st.finality.last_lock_at } else { unix - st.uptime_s as f64 };
        }
        st.finality.paused = paused;
        st.finality.line = if paused { crate::ember::finality_paused_line(st.finality.paused_since, &st.finality.reason, &st.finality.held_by) } else { String::new() };
        if paused {
            st.finality.message = st.finality.line.clone();
        }
        if self.running {
            let mining_now = st.mining.cards.iter().any(|c| c.state == "mining");
            let any_slot = !self.miners.is_empty();
            st.mining.state = if st.mining.paused {
                "paused".into()
            } else if mining_now {
                "mining".into()
            } else if !any_slot {
                if self.detected { "idle".into() } else { "waiting".into() }
            } else {
                "waiting".into()
            };
        }
    }

    // ---- lines -------------------------------------------------------------------------------------------------

    fn line(&mut self, l: Line) {
        let tag = l.src.tag();
        self.shared.rings.lock().unwrap().log(&tag, l.stderr, &l.text);
        match l.src {
            Source::Watch => {
                if l.text.contains(" node1 blocks=") {
                    self.watch_line(&l.text);
                }
            }
            Source::Miner(card) => self.miner_line(card, l.stderr, &l.text),
            Source::Node => {}
            Source::Telemetry => {
                if !l.stderr {
                    self.telemetry_line(&l.text);
                }
            }
            Source::AmdTelemetry => {
                if !l.stderr {
                    self.amd_telemetry_line(&l.text);
                }
            }
        }
    }

    /// igneumd's own lines. One matters for the user: relayed blocks refused as "too far into the future", which is
    /// this machine's clock running behind the network (the second Windows machine, 4 October 2026: 60 s slow after a power cut, 0 blocks).
    fn resolve_clock(&mut self) {
        let now = Instant::now();
        let mut skew: Option<f64> = None;
        let mut source = "";
        // 1. the node's own refusal: a lower bound on how far behind we are
        if let Some(t) = self.clock_node_at {
            if now.duration_since(t) <= Duration::from_secs(90) {
                skew = Some(-self.clock_node_behind.max(10.0));
                source = "node";
            }
        }
        // 2. the median of local-minus-block-time over the last samples: behind only (a stalled chain reads as ahead)
        if self.clock_samples.len() >= 3 {
            let mut v = self.clock_samples.clone();
            v.sort_by(|a, b| a.partial_cmp(b).unwrap());
            let median = v[v.len() / 2];
            if median < -5.0 && skew.map(|s| median < s).unwrap_or(true) {
                skew = Some(median);
                source = "blocks";
            }
        }
        // 3. the HTTPS Date header: either direction, 1 s resolution plus the request latency
        if let Some((d, t)) = self.clock_https {
            if now.duration_since(t) <= Duration::from_secs(1200) && d.abs() > 5.0 && skew.map(|s| d.abs() > s.abs()).unwrap_or(true) {
                skew = Some(d);
                source = "https";
            }
        }
        let mut st = self.st();
        let was = st.clock.severity.clone();
        st.clock.checked_at = crate::platform::unix_now_f();
        match skew {
            Some(d) if d.abs() > 10.0 => {
                st.clock.severity = "block".into();
                st.clock.skew_s = d;
                st.clock.source = source.into();
                let n = d.abs().round() as i64;
                st.clock.message = if d < 0.0 {
                    format!("Your clock is about {n} seconds behind the network{}; mining cannot start until it is fixed.", if source == "node" { " (at least)" } else { "" })
                } else {
                    format!("Your clock is about {n} seconds ahead of the network; mining cannot start until it is fixed.")
                };
            }
            Some(d) => {
                st.clock.severity = "warn".into();
                st.clock.skew_s = d;
                st.clock.source = source.into();
                let n = d.abs().round() as i64;
                st.clock.message = format!("Your clock is about {n} seconds {} the network. Over 10 s the node refuses blocks.", if d < 0.0 { "behind" } else { "ahead of" });
            }
            None => {
                st.clock.severity = "none".into();
                st.clock.skew_s = 0.0;
                st.clock.source = String::new();
                st.clock.message = String::new();
            }
        }
        let is = st.clock.severity.clone();
        let msg = st.clock.message.clone();
        drop(st);
        if is != was {
            match is.as_str() {
                "block" => {
                    self.shared.event("error", &msg);
                    if self.miners.iter().any(|m| m.proc.is_some()) {
                        self.stop_miners("clock over the consensus bound");
                        for m in self.miners.iter_mut() {
                            m.restart_at = Some(Instant::now());
                        }
                    }
                    if crate::sup::layer_on() && self.running {
                        // the old path restarted its miners on a clock fault (a fresh template under the corrected
                        // clock); the worker layer does the same through its restart
                        self.restart_layer("clock over the consensus bound");
                    }
                }
                "warn" => self.shared.event("info", &msg),
                _ => self.shared.event("ok", "clock agrees with the network again"),
            }
        }
    }

    fn watch_line(&mut self, _text: &str) {}

    fn miner_line(&mut self, _card: usize, _stderr: bool, _text: &str) {}

    fn race_line(&mut self, card: usize, card_name: &str, text: &str) {
        let Some(body) = text.split(" worker: race ").nth(1) else { return };
        let Some(r) = parse_race(body) else { return };
        let (vendor, worker, power_limit_w, power_w, power_pct) = {
            let mut st = self.st();
            match st.mining.cards.get_mut(card) {
                Some(c) => {
                    c.variant = r.winner.clone();
                    c.race_mhs = r.mhs;
                    c.race_gain_pct = r.gain_pct;
                    c.race_variants = r.variants.len() as u32;
                    if !r.driver.is_empty() && c.driver.is_empty() {
                        c.driver = r.driver.clone();
                    }
                    c.program_class = crate::ember::program_class(r.loads as u32, r.wide as u32);
                    (c.vendor.clone(), c.worker.clone(), c.power_limit_w, c.power_w, c.power_pct)
                }
                None => (String::new(), String::new(), 0.0, 0.0, 0),
            }
        };
        let record = serde_json::json!({
            "ts": crate::platform::unix_now_f().round(),
            "machine": self.shared.runtime.id8(),
            "app": VERSION,
            "card": r.device,
            "vendor": vendor,
            "worker": worker,
            "driver": r.driver,
            "arch": r.arch,
            "epoch": r.epoch,
            "loads": r.loads,
            "wide": r.wide,
            "variants": r.variants,
            "winner": r.winner,
            "mhs": r.mhs,
            "base_mhs": r.base_mhs,
            "gain_pct": r.gain_pct,
            "power_limit_w": power_limit_w,
            "power_w": power_w,
            "power_pct": power_pct,
            "mh_per_w": if power_w > 0.0 { (r.mhs / power_w * 1000.0).round() / 1000.0 } else { 0.0 },
            "total_ms": r.total_ms,
            "pinned": r.pinned,
            "tuned": r.tuned,
            "notes": r.notes,
        });
        self.shared.log(&format!("TUNING {record}"));
        if !r.winner.is_empty() {
            self.shared.event("build", &format!("{card_name} kernel race: {} at {:.1} MH/s ({:+.1}% over base, {} variants, {:.0} s)", r.winner, r.mhs, r.gain_pct, r.variants.len(), r.total_ms / 1000.0));
        }
    }

    // ---- status, uploads, shutdown ---------------------------------------------------------------------------

    fn status_line(&mut self) {
        let st = self.st();
        let node_txt = match st.node.state.as_str() {
            "synced" | "syncing" => format!("node {} blocks, {} peers, {}", st.node.blocks, st.node.peers, st.node.state),
            s => format!("node {s}"),
        };
        let miner_txt = if st.mining.cards.is_empty() {
            "no GPU miner".to_string()
        } else {
            format!("accepted {} blocks ({} this run, dev fee {}{}), {}, {}", st.mining.accepted_total, st.mining.accepted_session, st.mining.fee_session, if st.mining.cancelled_session > 0 { format!(", cancelled {}", st.mining.cancelled_session) } else { String::new() }, igneum_supervisor::state::rate_words(st.mining.hash_total * 1e6), st.mining.state)
        };
        let up = fmt_uptime(self.shared.started.elapsed().as_secs());
        drop(st);
        self.shared.log(&format!("status: {miner_txt} | {node_txt} | up {up}"));
    }

    fn upload_logs(&mut self, wait: bool) {
        let p = &self.shared.packaged;
        if p.upload_url.is_empty() || p.upload_key.is_empty() || !self.running {
            return;
        }
        if let Some(t) = self.upload_thread.take() {
            if t.is_finished() || wait {
                let _ = t.join();
            } else {
                self.upload_thread = Some(t);
                return;
            }
        }
        let mut files: Vec<(String, PathBuf)> = Vec::new();
        files.push((self.label_base.clone(), self.shared.log_path.clone()));
        if let Some(n) = &self.node_log {
            files.push((format!("nodelog-{}", self.label_base), n.clone()));
        }
        for m in &self.miners {
            if let Some(pr) = &m.proc {
                files.push((format!("miner-{}", m.label), pr.log_path.clone()));
            }
        }
        // machine = "<hostname>-<id8>": readable in tools/logs.mjs and distinct for cloned PCs
        let (url, key, machine, run_id) = (p.upload_url.clone(), p.upload_key.clone(), format!("{}-{}", self.shared.runtime.host, self.shared.runtime.id8()), format!("{}-{}", self.label_base, self.stamp));
        let header = self.shared.upload_header();
        let t = std::thread::spawn(move || {
            for (label, path) in files {
                crate::update::upload_log(&url, &key, &label, &machine, &run_id, &path, &header);
            }
        });
        if wait {
            // an earlier release: bounded (the quit's last step); an intake that does not answer never holds the exit
            let until = Instant::now() + Duration::from_secs(crate::quitguard::UPLOAD_WAIT_S);
            while !t.is_finished() && Instant::now() < until {
                std::thread::sleep(Duration::from_millis(100));
            }
            if t.is_finished() {
                let _ = t.join();
            } else {
                self.shared.log(&format!("the last log upload did not finish in {} s; leaving without it", crate::quitguard::UPLOAD_WAIT_S));
            }
        } else {
            self.upload_thread = Some(t);
        }
    }

    fn shutdown(&mut self) {
        self.shared.log(&format!("quit: stopping the miners, then closing (source: {})", self.quit_source));
        if let Some(a) = self.jobs.abort(&self.shared, "the app is quitting") {
            // the job's cards back before the exit (an app update under a job left the 9070 XT off on 6 October 2026)
            self.job_action(a);
        }
        if self.sweep.is_some() || self.sweep_pending.is_some() {
            self.sweep_abort("the app is quitting");
        }
        self.sweep_helper_quit();
        crate::quitguard::set_stage(&self.shared, crate::quitguard::Stage::Miners);
        self.stop_miners("quit");
        self.stability_line();
        if let Some(mut t) = self.telemetry.take() {
            t.stop(2);
        }
        if let Some(mut t) = self.amd_telemetry.take() {
            t.stop(2);
        }
        if self.shared.runtime.sweep_only {
            // the measurement leaves the chosen cap in force for the app that takes the card back
            self.shared.log("--sweep: the chosen caps stay in force (not restored)");
        } else {
            self.restore_power_limits();
        }
        if let Some(mut w) = self.watch.take() {
            w.stop(2);
        }
        // a quit that quits: the worker layer's workers are ended by their pids before the node's stage (the quit
        // guard's rule: no process of the miner's outlives the engine)
        if crate::sup::layer_on() {
            self.sup.stop();
        }
        crate::quitguard::set_stage(&self.shared, crate::quitguard::Stage::Node);
        self.stop_node();
        if let Some(mut k) = self.keep_awake.take() {
            k.stop();
        }
        crate::quitguard::set_stage(&self.shared, crate::quitguard::Stage::Logs);
        let st = self.st();
        let summary = format!(
            "SUMMARY after {}: node started {} time(s), restarts {}, {} accepted blocks this run ({} lifetime); data stays in {}",
            fmt_uptime(self.shared.started.elapsed().as_secs()),
            self.node_starts,
            self.node_restarts,
            st.mining.accepted_session,
            st.mining.accepted_total,
            self.shared.runtime.node_dir.display()
        );
        drop(st);
        self.shared.log(&summary);
        self.shared.save_settings();
        // the second Windows machine record #1789 (8 October 2026, 21:24 UK): the old engine ended inside the last upload with no EXIT line on
        // its stdout, and the host read a plain death and started a new engine 35 s later. The host's word comes
        // first: it only closes its handles on it, the upload finishes on its own, and nothing in the upload can turn
        // a quit into a restart
        self.print_exit_lines();
        self.upload_logs(true);
        self.shared.log("stopped");
    }

    /// The last lines for the window host (STATE quit, then "EXIT" or "EXIT update"): written, never println!-ed, so a
    /// pipe the host has already closed is a dropped line and not a panic.
    fn print_exit_lines(&self) {
        if !self.wrapper {
            return;
        }
        let mut out = std::io::stdout().lock();
        let _ = writeln!(out, "STATE {}", json!({ "phase": "quit", "quitting": true }));
        let _ = writeln!(out, "{}", exit_line(self.quit_source));
        let _ = out.flush();
    }
}

/// The last line the engine prints to its window host. "EXIT update" when an installer or the app's own OTA asked for
/// the quit: the host then quits itself so the installer can replace it too, and never starts the old exe again under
/// the installer (the second Windows machine, 7 October 2026, 20:54 UK: a job's silent an earlier release install quit the engine through api/quit, the
/// an earlier release host's restart ladder started the old engine 10 s later, Inno could not replace the locked host, and every
/// file stayed an earlier release). A plain "EXIT" for every other quit (the tray, a crash of the node the engine gave up on).
/// The founder's rule (8 October 2026): an update never leaves mining off. On the first start after an update, a saved
/// pause is cleared and said; on any other start a pause stays what the user or a job made it. The words, or None.
pub fn resume_after_update(updated_from: Option<&str>, paused: bool, paused_by: &str) -> Option<String> {
    match updated_from {
        // the project's ruling of 14 October 2026: a job's pause (the api from a script) outlives the update; the user's clears
        Some(from) if paused && paused_by == "job" => {
            eprintln!("update-return: mining stays paused under {VERSION}: a job paused it under {from} and a job's pause outlives an update");
            None
        }
        Some(from) if paused => Some(format!("mining was paused by the user under {from}; the update to {VERSION} resumes it (an update never leaves mining off)")),
        _ => None,
    }
}

/// How a remote `restart --what app` brings the app back (v0.2.0). Under the window host: the engine's plain EXIT and the
/// host's restart ladder (10 s), nothing else started. With no host: the detached relaunch helper (src/jobrun.rs).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RestartPlan {
    HostLadder,
    Helper,
}

pub fn restart_plan(under_host: bool) -> RestartPlan {
    if under_host { RestartPlan::HostLadder } else { RestartPlan::Helper }
}

pub fn exit_line(quit_source: &str) -> &'static str {
    if quit_source.contains("api/quit") || quit_source.contains("installer") || quit_source.contains("update") {
        "EXIT update"
    } else {
        "EXIT"
    }
}

/// The watts a card's cap asks for: power_pct of the default limit, inside the card's min and max.
/// the founder, 5 October 2026: "if we don't have to ask then don't ask". The NVIDIA power cap and the efficiency sweep need
/// administrator rights (one UAC prompt on Windows, pkexec on Linux); the engine builds an elevated command only when
/// Power control is on in Settings, or when it is itself the elevated PC sweep job (--sweep).
fn elevation_allowed(power_control: bool, sweep_only: bool) -> bool {
    // C35 (5 October 2026, 22:30 UTC): the unattended --sweep job on the first Windows machine counted as allowed and raised the one
    // administrator prompt nobody was there to answer; an elevated job sets limits directly without asking (the tune
    // probe's `direct`), so the flag adds nothing and Power control alone decides
    let _ = sweep_only;
    power_control
}

/// The notice when the one prompt was refused, cancelled or not answered: Power control goes back off, no retries.
pub const POWER_CONTROL_REFUSED: &str = "power control off: administrator rights were not given";

/// After the elevated step: Some(notice) when the administrator prompt was refused, cancelled or timed out (the
/// words platform::run_elevated and the window host use), None when rights were given, even if a card then
/// disagreed with the readback.
fn power_control_after_prompt(r: &Result<(), String>) -> Option<&'static str> {
    match r {
        Err(e) if e.contains("administrator prompt") || e.contains("refused") || e.contains("cancel") => Some(POWER_CONTROL_REFUSED),
        _ => None,
    }
}

/// The nvidia-smi -pl lines one elevated step runs, the human list of what they set, and how many cards were held
/// back because Power control is off (their note says so). Nothing is built when not allowed.
fn power_cap_plan(cards: &mut [CardState], allowed: bool, smi: &str) -> (Vec<String>, Vec<String>, usize) {
    let mut cmds = Vec::new();
    let mut what = Vec::new();
    let mut held = 0;
    for c in cards.iter_mut().filter(|c| c.vendor == "nvidia" && c.enabled && c.present() && c.power_default_w > 0.0) {
        let pct = if c.power_pct == 0 { 80 } else { c.power_pct.clamp(crate::sweep::MIN_PCT, 100) };
        c.power_pct = pct;
        let watts = requested_watts(c);
        if (c.power_limit_w - watts).abs() < 1.0 && c.power_applied {
            continue;
        }
        if !allowed {
            c.power_note = "power cap not set: Power control is off in Settings".into();
            held += 1;
            continue;
        }
        cmds.push(format!("\"{smi}\" -i {} -pl {}", c.device, watts as u64));
        what.push(format!("{} {} W ({}% of {} W)", c.name, watts as u64, pct, c.power_default_w as u64));
        c.power_note = "setting the power cap (administrator prompt)".into();
    }
    (cmds, what, held)
}

/// What a tune's probe found for a card (Cmd::TuneProbe).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct TuneProbe {
    pub clock_max_mhz: u32,
    pub clock_min_mhz: u32,
    pub driver: String,
    /// NVIDIA: this process sets limits itself (elevated); AMD: the helper answered its `--tune` line with ok
    pub direct: bool,
    pub amd_ordinal: i64,
    /// AMD: the power offset range in percent from the `tune` line (the first Windows machine's 9070 XT: -30 to 10)
    pub plimit_min: f64,
    pub plimit_max: f64,
    /// Ember 2, NVIDIA: the memory clock under load and the vendor's maximum (nvidia-smi clocks.mem, clocks.max.mem)
    pub mem_default_mhz: u32,
    pub mem_max_mhz: u32,
    /// an earlier release, AMD: the whole tune line for the knob (src/ember.rs amd_knob); None when no line came
    pub amd_tune: Option<crate::ember::AmdTuneShape>,
}

/// One `tune` line of igneum-gpu-telemetry --tune:
/// `tune <ordinal> name "<name>" gmax <MHz> gmax_range <min> <max> plimit <offset %> plimit_range <min> <max> factory 0|1 ok|<error>`.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct AmdTune {
    pub ordinal: usize,
    pub name: String,
    pub gmax: f64,
    pub gmax_min: f64,
    pub gmax_max: f64,
    pub plimit: f64,
    pub plimit_min: f64,
    pub plimit_max: f64,
    pub factory: bool,
    pub ok: bool,
    pub error: String,
}

pub fn parse_amd_tune(line: &str) -> Option<AmdTune> {
    let line = line.trim();
    if !line.starts_with("tune ") {
        return None;
    }
    let (head, rest) = line.split_once(" name \"")?;
    let (name, tail) = rest.split_once('"')?;
    let ordinal: usize = head.split_whitespace().nth(1)?.parse().ok()?;
    let tp: Vec<&str> = tail.split_whitespace().collect();
    let at = |key: &str| tp.iter().position(|p| *p == key);
    let num = |i: usize| tp.get(i).and_then(|v| if *v == "-" { Some(-1.0) } else { v.parse::<f64>().ok() }).unwrap_or(-1.0);
    let g = at("gmax")?;
    let gr = at("gmax_range")?;
    let pl = at("plimit")?;
    let pr = at("plimit_range")?;
    let fi = at("factory")?;
    let verdict = tp.get(fi + 2).copied().unwrap_or("");
    Some(AmdTune { ordinal, name: name.to_string(), gmax: num(g + 1), gmax_min: num(gr + 1), gmax_max: num(gr + 2), plimit: num(pl + 1), plimit_min: num(pr + 1), plimit_max: num(pr + 2), factory: tp.get(fi + 1).copied() == Some("1"), ok: verdict == "ok", error: if verdict == "ok" { String::new() } else { tp[fi + 2..].join(" ") } })
}

/// "2,472 MHz at 100%" or "100%" for the feed.
fn point_words(p: &crate::ember::Point) -> String {
    if p.clock_mhz > 0 { format!("{} MHz at {}%", p.clock_mhz, p.power_pct) } else { format!("{}% (clock unlocked)", p.power_pct) }
}

/// A refused tune request that names the Power Helper (no heartbeat, no log line inside the window): the FAULT line the
/// engine logs, so a helper that stopped answering is a fault row and not a quiet "cap NOT applied"; None for any other refusal.
/// Which helper a tune or a cap goes through. Windows: the Igneum Power Helper task or nothing (never an elevated
/// process of the engine's own: the project's rule, 7 October 2026, after the first Windows machine's an earlier release engine fell to its legacy prompt path);
/// elsewhere: the unix helper script when Power control is on.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum HelperRoute {
    Task,
    Unix,
    Unavailable(String),
}

pub const HELPER_TASK_MISSING: &str = "the Igneum Power Helper task is not registered, or its exe is gone";

/// The one event the user sees when there is no helper to go through: the reason and the one step that fixes it.
pub fn helper_unavailable_event(reason: &str) -> String {
    format!("power control: helper not available ({reason}): run the installer's rights step")
}

pub fn helper_route(windows: bool, task_registered: bool, power_control: bool) -> HelperRoute {
    if windows {
        if task_registered { HelperRoute::Task } else { HelperRoute::Unavailable(helper_unavailable_event(HELPER_TASK_MISSING)) }
    } else if power_control {
        HelperRoute::Unix
    } else {
        HelperRoute::Unavailable("Power control is off in Settings".to_string())
    }
}

pub fn helper_fault_line(refusal: &str) -> Option<String> {
    let t = refusal.to_ascii_lowercase();
    if t.contains("helper") && (t.contains("heartbeat") || t.contains("did not run") || t.contains("no line")) {
        Some(format!("FAULT power-helper: the Igneum Power Helper task did not answer ({}); its registration is read again before the next cap, and a missing or stale task is registered again through the one approved step when Power control is on", short(&refusal.replace('\n', " "), 200)))
    } else {
        None
    }
}

/// After a cap the card did not take: when to ask again (2 min, then 10 min), and None on the third refusal, when the
/// engine writes the FAULT line instead and waits for a setting change or the card tile (the project's rule, 7 October 2026:
/// a refused limit is re-applied or reported, never left as "cap NOT applied").
pub fn cap_refusal_plan(refusals: u32) -> Option<Duration> {
    match refusals {
        0 | 1 => Some(Duration::from_secs(120)),
        2 => Some(Duration::from_secs(600)),
        _ => None,
    }
}

fn requested_watts(c: &CardState) -> f64 {
    let pct = if c.power_pct == 0 { 80 } else { c.power_pct.clamp(crate::sweep::MIN_PCT, 100) };
    let mut w = c.power_default_w * pct as f64 / 100.0;
    if c.power_min_w > 0.0 {
        w = w.max(c.power_min_w);
    }
    if c.power_max_w > 0.0 {
        w = w.min(c.power_max_w);
    }
    w.round()
}

/// Per-card session statistics for the stability line.
#[derive(Default)]
struct Stability {
    draws: Vec<f64>,
    max_tgpu: f64,
    max_tmem: f64,
}

/// One `igneum-miner watch` reading.
pub struct Reading {
    pub blocks: u64,
    pub headers: u64,
    pub peers: u64,
    /// getInfo's is_synced, when the line carries one
    pub flag: Option<bool>,
}

/// Is the node synced? Any of: a private test node; our block accepted in the last minute; the node's own flag;
/// caught up (headers within 2 of blocks, a peer, more than genesis) and the count moving or stable for 60 s.
/// A false flag never overrides caught-up-and-moving: getInfo said false on the second Windows machine while it mined (4 October 2026).
pub fn sync_decision(r: &Reading, moving: bool, stable_s: f64, accepted_recent: bool, private: bool) -> bool {
    if private || accepted_recent || r.flag == Some(true) {
        return true;
    }
    let caught_up = r.peers > 0 && r.headers <= r.blocks + 2 && r.blocks > 1;
    caught_up && (moving || stable_s >= 60.0)
}

/// The tip age at which a node that still says "synced" is treated as frozen (ledger N4, 6 October 2026: a home
/// miner's one same-digest peer relayed nothing for 53 minutes while the app read "mining, synced").
pub const TIP_FROZEN_S: f64 = 120.0;

/// N4: what "synced" means on every surface. The old decision, AND the tip moved in the last 120 s (the watch
/// line's tip_age_s, the sink header's age; -1 = the node does not report it yet, which gates nothing so an older node
/// keeps today's words), AND at least one peer (the handshake refuses a peer with another digest, so `peers` already
/// counts peers on our chain). Returns (synced, cause): the cause is "" when synced, else "frozen" (a peer, caught up,
/// no new block for over 120 s), "no peers", "syncing", or "behind" (the node's own flag false while the count moves).
pub fn sync_decision_v2(r: &Reading, moving: bool, stable_s: f64, accepted_recent: bool, private: bool, tip_age_s: f64) -> (bool, &'static str) {
    if private {
        return (true, "");
    }
    let frozen = tip_age_s > TIP_FROZEN_S && !accepted_recent;
    if r.peers == 0 && !accepted_recent {
        return (false, "no peers");
    }
    if frozen {
        return (false, "frozen");
    }
    if sync_decision(r, moving, stable_s, accepted_recent, private) {
        return (true, "");
    }
    (false, if r.headers > r.blocks + 2 { "syncing" } else { "behind" })
}

/// The miner's stall exit (the node lane, ca3-v4-0316): code 45, or a tail line starting "STALLED '".
pub const STALL_EXIT_CODE: i32 = 45;
pub fn is_stall_exit(code: i32, tail: &[String]) -> bool {
    code == STALL_EXIT_CODE || tail.iter().any(|l| l.contains(" STALLED '") || l.starts_with("STALLED '"))
}

/// "...block timestamp is 1791112663000 but maximum timestamp allowed is 1791112600000" -> at least 63 s behind.
fn behind_from_warning(text: &str) -> Option<f64> {
    let nums: Vec<f64> = text.split(|c: char| !c.is_ascii_digit()).filter(|s| s.len() >= 12).filter_map(|s| s.parse::<f64>().ok()).collect();
    if nums.len() < 2 {
        return None;
    }
    let behind = (nums[nums.len() - 2] - nums[nums.len() - 1]) / 1000.0;
    if behind > 0.0 { Some(behind) } else { None }
}

/// A worker's race line after "race " (docs/design/miner-tuning.md section 3), parsed into the record's fields.
#[derive(Debug, Default, PartialEq)]
pub struct RaceParsed {
    pub epoch: String,
    pub device: String,
    pub driver: String,
    pub arch: String,
    pub loads: u64,
    pub wide: u64,
    /// variant name -> MH/s (null when the variant was discarded or not timed)
    pub variants: serde_json::Map<String, Value>,
    pub winner: String,
    pub mhs: f64,
    pub base_mhs: f64,
    pub gain_pct: f64,
    pub total_ms: f64,
    pub pinned: bool,
    pub tuned: bool,
    pub notes: String,
}

pub fn parse_race(body: &str) -> Option<RaceParsed> {
    let (main, notes) = match body.split_once(" | ") {
        Some((m, n)) => (m, n),
        None => (body, ""),
    };
    let t: Vec<&str> = main.split_whitespace().collect();
    let after = |key: &str| t.iter().position(|x| *x == key).and_then(|i| t.get(i + 1)).map(|s| s.to_string());
    let num = |key: &str| after(key).and_then(|v| v.trim_end_matches('%').parse::<f64>().ok());
    let epoch = t.first()?.to_string();
    if epoch.len() < 8 || !epoch.chars().all(|c| c.is_ascii_hexdigit()) {
        return None;
    }
    let mut r = RaceParsed { epoch, device: after("device").unwrap_or_default(), driver: after("driver").unwrap_or_default(), arch: after("arch").unwrap_or_default(), loads: num("loads").unwrap_or(0.0) as u64, wide: num("wide").unwrap_or(0.0) as u64, ..Default::default() };
    let n = num("variants").unwrap_or(0.0) as usize;
    if let Some(i) = t.iter().position(|x| *x == "variants") {
        for tok in t.iter().skip(i + 2).take(n) {
            if let Some((name, rest)) = tok.split_once('=') {
                let mhs = rest.split('/').next().and_then(|m| m.parse::<f64>().ok());
                r.variants.insert(name.to_string(), mhs.map(Value::from).unwrap_or(Value::Null));
            }
        }
    }
    r.winner = after("winner").unwrap_or_default();
    r.mhs = t.iter().position(|x| *x == "winner").and_then(|i| t.get(i + 2)).and_then(|v| v.parse::<f64>().ok()).unwrap_or(0.0);
    r.base_mhs = num("base").unwrap_or(0.0);
    r.gain_pct = num("gain").unwrap_or(0.0);
    r.total_ms = num("total").unwrap_or(0.0);
    r.pinned = main.contains(" pinned by tuning");
    r.tuned = main.contains(" tuned order");
    r.notes = notes.to_string();
    Some(r)
}

#[cfg(test)]
mod prepare_packs_tests {
    use super::*;

    /// Program class v3 (5 October 2026): the Metal worker needs the prepare directory too, in the platform's
    /// separator, relative to the app data folder the miner runs in.
    #[test]
    fn every_worker_gets_the_prepare_directory_in_the_platform_form() {
        let arg = prepare_packs_arg();
        if cfg!(windows) {
            assert_eq!(arg, "packs\\prepare");
        } else {
            assert_eq!(arg, "packs/prepare", "the Mac's Metal worker gets a forward-slash path");
        }
        assert!(!arg.contains(' ') && !arg.starts_with('/'), "relative, no space: the miner splits --worker-args on spaces and the data folder may carry one");
    }
}

#[cfg(test)]
mod resume_tests {
    use super::*;

    fn card(name: &str, enabled: bool, state: &str, hash: f64) -> CardState {
        CardState { name: name.into(), vendor: "nvidia".into(), kind: "discrete".into(), enabled, state: state.into(), hash_now: hash, ..Default::default() }
    }

    /// The state machine: paused (every slot stopped, restart_at cleared) -> resumed -> every slot without a live
    /// worker is re-armed, faulted or not.
    #[test]
    fn resume_rearms_every_stopped_slot() {
        let slots = [ResumeSlot { faulted: false, live: false }, ResumeSlot { faulted: true, live: false }, ResumeSlot { faulted: false, live: true }];
        assert_eq!(slots_to_rearm_on_resume(&slots), vec![0, 1], "the healthy stopped slot and the faulted one restart; the live one is left alone");
    }

    /// The known-failed case (the second Windows machine, 5 October 2026, 21:25:11Z, app an earlier release: `[ok] mining resumed`, then `0.00 MH/s,
    /// waiting` for 20 minutes): one healthy slot, stopped by the pause, not faulted. The old rule re-armed only
    /// faulted slots and returned nothing for it; the new rule returns it.
    #[test]
    fn the_pc2_resume_of_21_25_11z_restarts_under_the_new_rule_and_not_the_old() {
        let old_rule = |slots: &[ResumeSlot]| -> Vec<usize> { slots.iter().enumerate().filter(|(_, s)| s.faulted).map(|(i, _)| i).collect() };
        let pc2 = [ResumeSlot { faulted: false, live: false }];
        assert!(old_rule(&pc2).is_empty(), "the 0.1.9 rule left the 5090's slot unarmed: this is the defect");
        assert_eq!(slots_to_rearm_on_resume(&pc2), vec![0]);
    }

    /// The check 90 s after a resume names every enabled card without a hash rate, and nothing else.
    #[test]
    fn resume_check_names_the_cards_not_mining() {
        let cards = [card("NVIDIA GeForce RTX 5090", true, "off", 0.0), card("AMD Radeon(TM) Graphics", true, "mining", 3.4), card("Intel UHD", false, "off", 0.0), card("RTX 3060", true, "faulted", 0.0)];
        let lines = resume_check(&cards);
        assert_eq!(lines.len(), 1, "{lines:?}");
        assert!(lines[0].starts_with("NVIDIA GeForce RTX 5090 is not mining 90 s after resume (state off"), "{}", lines[0]);
        assert!(resume_check(&[card("RTX 5090", true, "mining", 118.9)]).is_empty());
    }
}

#[cfg(test)]
mod tests {

    /// the second Windows machine, 7 October 2026, 20:54 UK, known-failed first: before an earlier release every quit printed the same "EXIT", so the host
    /// could not tell an installer's stop from a death and started the old engine again under the installer
    #[test]
    fn an_installers_quit_tells_the_host_not_to_restart() {
        assert_eq!(super::exit_line("POST /api/quit (a local caller with the token: the installer, the OTA apply, or a script that read app.url)"), "EXIT update");
        assert_eq!(super::exit_line("the window host (quit on stdin: the tray menu or the installer)"), "EXIT update", "the installer's quit through the host's stdin counts too");
        assert_eq!(super::exit_line("the --sweep run (every card done)"), "EXIT");
        assert_eq!(super::exit_line("unknown"), "EXIT");
        // the host's side, read from its source: an "EXIT update" line ends the window instead of the restart ladder, and the
        // engine-gone branch asks the same question before it schedules a restart
        let host = include_str!("../../windows/host.cpp");
        assert!(host.contains("g_exitForUpdate = true"), "the host remembers an update exit");
        assert!(host.contains("if (g_exitForUpdate) { DestroyWindow(hwnd); return 0; }"), "the engine-gone branch quits the window under an installer, never restarts");
        assert!(host.contains("line->rfind(\"EXIT\", 0) == 0"), "the host reads EXIT with or without a reason");
    }

    /// The installer's stop step ends the window host first, by its path, so no restart ladder can start the old engine while
    /// the files are replaced (known-failed first: the an earlier release script quit the engine, waited, and ended names only afterwards)
    #[test]
    fn the_installers_stop_step_ends_the_host_first_by_path() {
        let s = include_str!("../../../packaging/windows/stop-igneum.ps1");
        let host = s.find("Ember.exe").expect("the host by its file name");
        let quit = s.find("api/quit").expect("the quit through the API");
        assert!(host < quit, "the host is ended before the engine is asked to quit");
        assert!(s.contains("ExecutablePath") && s.contains("Win32_Process"), "processes are matched by their path under the install folder, never by a bare name alone");
        assert!(s.contains("Stop-Process -Id"), "ended by pid");
    }

    /// Known-failed first (main, 7 October 2026, after the first Windows machine's an earlier release engine wrote its PowerShell helper into the sweep folder and ran it
    /// elevated, the prompt path, when it judged the task absent): the engine never runs an elevated helper of its own on
    /// Windows. A missing task is one event and no process; the legacy branch is gone from the source.
    #[test]
    fn the_engine_never_runs_an_elevated_helper_of_its_own_on_windows() {
        let s = include_str!("engine.rs");
        // the literals are built in pieces so this test's own text never matches
        // (the --sweep engine's registration of the task from a process a job already elevated stays: no prompt is raised there)
        for gone in [["helper", ".ps1"].concat(), ["helper_script_", "windows"].concat(), ["println!(\"ELEV", "ATE "].concat()] {
            assert_eq!(s.matches(gone.as_str()).count(), 0, "the legacy elevated branch is deleted: {gone} is still in engine.rs");
        }
        assert_eq!(super::helper_route(true, false, true), super::HelperRoute::Unavailable("power control: helper not available (the Igneum Power Helper task is not registered, or its exe is gone): run the installer's rights step".into()));
        assert_eq!(super::helper_route(true, false, false), super::helper_route(true, false, true), "Power control on or off: without the task there is nothing to run and nothing to ask");
        assert_eq!(super::helper_route(true, true, false), super::HelperRoute::Task, "the task registered: the task, whatever the switch says");
        assert_eq!(super::helper_route(false, false, true), super::HelperRoute::Unix);
        assert_eq!(super::helper_route(false, false, false), super::HelperRoute::Unavailable("Power control is off in Settings".into()));
        // the tune path: an unavailable route is the event and a return before any file or process
        let start = s.find("fn sweep_helper_start").unwrap();
        let body = &s[start..start + 1800];
        let un = body.find("HelperRoute::Unavailable(reason) =>").expect("the route is matched first");
        let ev = body.find("self.shared.event(\"error\", &reason)").expect("the event");
        let ret = body.find("return Err(reason);").expect("and the return");
        let first_write = body.find("std::fs::write").unwrap();
        assert!(un < ev && ev < ret && ret < first_write, "event, return, and only then any file");
        // the cap path: on Windows, after the task branch, the same event and nothing elevated
        let cap = s.find("fn apply_power_limits").unwrap();
        let cap_body = &s[cap..cap + 6000];
        assert!(cap_body.contains("helper_unavailable_event(HELPER_TASK_MISSING)") && cap_body.contains("self.power_busy = false;"));
        let elevated = cap_body.find("run_elevated").unwrap();
        let guard = cap_body.find("if cfg!(windows) {\n            // an earlier release").unwrap();
        assert!(guard < elevated, "the Windows return comes before the unix elevated step");
    }

    /// The two tune lines the first Windows machine answered on 8 October 2026 (igneum-gpu-telemetry 1d8e055d, the AMD knob's tool): the
    /// integrated Radeon with no interface reads "not available", the RX 9070 XT reads the offset knob.
    #[test]
    fn pc1s_two_tune_lines_give_one_refusal_and_one_knob() {
        let igpu = super::parse_amd_tune("tune 0 name \"AMD Radeon(TM) Graphics\" gmax - gmax_range - - plimit - plimit_range - - factory 0 ok").unwrap();
        assert!(igpu.ok && igpu.gmax_max < 0.0 && igpu.plimit_max < 0.0, "{igpu:?}");
        let shape = |t: &super::AmdTune| crate::ember::AmdTuneShape { ok: t.ok, error: t.error.clone(), gmax: t.gmax, gmax_min: t.gmax_min, gmax_max: t.gmax_max, plimit_min: t.plimit_min, plimit_max: t.plimit_max };
        assert!(crate::ember::amd_knob(Some(&shape(&igpu)), 2100.0, false).unwrap_err().contains("no tuning interface"));
        let xt = super::parse_amd_tune("tune 1 name \"AMD Radeon RX 9070 XT\" gmax 0 gmax_range -500 1000 plimit 0 plimit_range -30 10 factory 1 ok").unwrap();
        let k = crate::ember::amd_knob(Some(&shape(&xt)), 3292.0, false).unwrap();
        assert_eq!((k.stock_mhz, k.gmax_offset, k.clock_max_mhz, k.clock_min_mhz, k.plimit_min, k.plimit_max), (3292, true, 3292, 2792, -30.0, 10.0), "the live clock under load (3,292 MHz in the read-back sample) is the stock the offsets apply to");
        assert_eq!(crate::ember::amd_gmax_arg(2992, 3292, true), -300);
    }

    /// Known-failed first (the founder's rule, 8 October 2026, 18:0x UK: the earlier off jobs left both PCs paused, and the
    /// an earlier release update would have started paused, a click owed after an install): the first start after an update clears a
    /// saved pause and says so; an ordinary start keeps the pause the user or a job set.
    #[test]
    fn the_first_start_after_an_update_never_leaves_mining_paused() {
        let line = super::resume_after_update(Some("0.1.26"), true, "user").unwrap();
        assert!(line.starts_with("mining was paused by the user under 0.1.26; the update to ") && line.ends_with(" resumes it (an update never leaves mining off)"), "{line}");
        assert_eq!(super::resume_after_update(Some("0.1.26"), false, "user"), None, "nothing to resume");
        assert_eq!(super::resume_after_update(None, true, "user"), None, "an ordinary start keeps the user's pause");
        // the project's ruling of 14 October 2026: a job's pause outlives the update, the user's does not
        assert_eq!(super::resume_after_update(Some("0.2.12"), true, "job"), None, "a job's pause outlives an update");
        assert!(super::resume_after_update(Some("0.2.12"), true, "user").unwrap().contains("paused by the user"));
        assert!(super::resume_after_update(Some("0.2.12"), true, "").unwrap().contains("resumes it"), "an unmarked pause (saved before 0.2.13) is the user's");
        let e = include_str!("engine.rs");
        let rb = e.find("update-return: app {VERSION} up after the update from {from}").unwrap();
        let body = &e[rb..rb + 900];
        assert!(body.contains("resume_after_update(Some(&from), paused, &by)") && body.contains("settings.lock().unwrap().paused = false") && body.contains("self.shared.save_settings()") && body.contains("st().mining.paused = false"), "the start path clears and saves the pause right after the read-back line");
    }

    /// Known-failed first (the first Windows machine, 6 and 8 October 2026: a remote app restart left the app down for hours, "the relaunch
    /// helper did not bring it back"): under the window host the restart is the host's ladder after a plain EXIT, never a
    /// helper racing it; the helper only with no host; the quit source reads as a plain EXIT, not an update exit.
    #[test]
    fn a_remote_app_restart_under_the_host_uses_the_hosts_ladder_and_no_helper() {
        assert_eq!(super::restart_plan(true), super::RestartPlan::HostLadder);
        assert_eq!(super::restart_plan(false), super::RestartPlan::Helper);
        assert_eq!(super::exit_line("remote job: app restart"), "EXIT", "a plain EXIT: the host restarts the engine (an update exit would end the host)");
        let j = include_str!("jobrun.rs");
        let arm = j.find("\"restart\" | \"update-now\" =>").unwrap();
        assert!(!j[arm..arm + 1800].contains("spawn_relaunch_helper(shared)"), "the runner no longer starts the helper itself");
        let e = include_str!("engine.rs");
        let ra = e.find("Action::RestartApp => {").unwrap();
        let body = &e[ra..ra + 1500];
        assert!(body.contains("restart_plan(self.wrapper)") && body.contains("RestartPlan::HostLadder =>") && body.contains("crate::jobrun::spawn_relaunch_helper(&self.shared)"), "the engine decides by the host");
        assert!(body.contains("self.quit_source = \"remote job: app restart\";"));
    }

    /// the second Windows machine, 7 October 2026, 14:39Z: "the helper did not run sequence 1 within 15 s (no line in ...helper.log)" is a FAULT line
    #[test]
    fn a_helper_that_does_not_answer_is_a_fault_line() {
        let l = super::helper_fault_line("the helper did not run sequence 1 within 15 s (no line in C:\\x\\helper.log)").unwrap();
        assert!(l.starts_with("FAULT power-helper: ") && l.contains("did not run sequence 1"), "{l}");
        assert!(super::helper_fault_line("the Igneum Power Helper task gave no heartbeat within 12 s of its start").is_some());
        assert_eq!(super::helper_fault_line("nvidia-smi: Setting applications clocks is not supported"), None, "a card's own refusal is not the helper's");
    }

    /// A refused cap is asked again at 2 and 10 minutes, then reported as a FAULT line (never left as "cap NOT applied")
    #[test]
    fn a_refused_cap_climbs_the_retry_ladder_then_faults() {
        assert_eq!(super::cap_refusal_plan(1), Some(std::time::Duration::from_secs(120)));
        assert_eq!(super::cap_refusal_plan(2), Some(std::time::Duration::from_secs(600)));
        assert_eq!(super::cap_refusal_plan(3), None, "the third refusal is the FAULT line");
        assert_eq!(super::cap_refusal_plan(7), None);
    }
    #[test]
    fn power_control_off_builds_no_elevated_command() {
        // the decision (the founder, 5 October 2026): off = the app never asks; the elevated PC sweep job is the exception
        assert!(!super::elevation_allowed(false, false));
        assert!(super::elevation_allowed(true, false));
        assert!(!super::elevation_allowed(false, true), "the --sweep job alone never asks (C35)");
        let mut cards = vec![
            super::CardState { vendor: "nvidia".into(), enabled: true, device: "0".into(), name: "RTX 5090".into(), power_default_w: 575.0, power_limit_w: 575.0, power_pct: 80, ..Default::default() },
            super::CardState { vendor: "amd".into(), enabled: true, device: "1".into(), name: "RX 9070 XT".into(), power_default_w: 300.0, power_limit_w: 300.0, power_pct: 80, ..Default::default() },
        ];
        let (cmds, what, held) = super::power_cap_plan(&mut cards, false, "nvidia-smi");
        assert!(cmds.is_empty() && what.is_empty(), "{cmds:?}");
        assert_eq!(held, 1);
        assert_eq!(cards[0].power_note, "power cap not set: Power control is off in Settings");
        let (cmds, what, held) = super::power_cap_plan(&mut cards, true, "nvidia-smi");
        assert_eq!(cmds, vec!["\"nvidia-smi\" -i 0 -pl 460".to_string()]);
        assert_eq!(what, vec!["RTX 5090 460 W (80% of 575 W)".to_string()]);
        assert_eq!(held, 0);
        // a cap already in force asks for nothing either way
        cards[0].power_limit_w = 460.0;
        cards[0].power_applied = true;
        assert!(super::power_cap_plan(&mut cards, true, "nvidia-smi").0.is_empty());
    }

    #[test]
    fn a_refused_prompt_switches_power_control_off_with_the_notice() {
        let refused = Err("the administrator prompt was refused, cancelled or timed out (exit 251)".to_string());
        assert_eq!(super::power_control_after_prompt(&refused), Some("power control off: administrator rights were not given"));
        assert_eq!(super::power_control_after_prompt(&Err("the administrator prompt was cancelled".into())), Some(super::POWER_CONTROL_REFUSED));
        assert_eq!(super::power_control_after_prompt(&Err("elevated fail: the administrator prompt was cancelled".into())), Some(super::POWER_CONTROL_REFUSED));
        assert_eq!(super::power_control_after_prompt(&Err("the administrator prompt was not answered in 150 s".into())), Some(super::POWER_CONTROL_REFUSED));
        // rights given: the step ran, whatever the card then said
        assert_eq!(super::power_control_after_prompt(&Ok(())), None);
        assert_eq!(super::power_control_after_prompt(&Err("the elevated step exited with code 2".into())), None);
    }

    use super::{is_stall_exit, sync_decision_v2, digest_from_line, parse_race, switches_of, sync_decision, Reading};

    #[test]
    fn digest_comes_from_the_nodes_own_line_only() {
        let own = "2026-10-05 17:58:13.001+01:00 [INFO ] Consensus params digest: 1f4b44255fcd2ea8f75664ed47200f409186ddd2292960c9e2cf95bbbdc11505 (exchanged in the p2p handshake; a peer with another digest is refused)";
        assert_eq!(digest_from_line(own).as_deref(), Some("1f4b44255fcd2ea8f75664ed47200f409186ddd2292960c9e2cf95bbbdc11505"));
        let peer = "[WARN ] Refusing peer 188.245.5.161:26611: consensus params digest mismatch, local 72532d35 remote a6da35e8 (the peer's override file, environment or build differs)";
        assert_eq!(digest_from_line(peer), None);
        assert_eq!(digest_from_line("Consensus params digest: abc"), None);
        assert_eq!(digest_from_line("[INFO ] Processed 100 blocks"), None);
    }

    #[test]
    fn switches_are_the_activation_heights_lowest_first_in_plain_words() {
        let v: serde_json::Value = serde_json::from_str(r#"{"difficulty_v2_activation_daa":33000,"fees_v1_activation_daa":210000,"finality_v3_activation_daa":135200,"proving_v0_activation_daa":84100,"max_block_mass":500000}"#).unwrap();
        let s = switches_of(&v);
        assert_eq!(s.iter().map(|x| (x.name.as_str(), x.daa)).collect::<Vec<_>>(), vec![("Difficulty v2", 33000), ("Proving v0", 84100), ("Finality v3", 135200), ("Fees v1", 210000)]);
        assert_eq!(s[3].key, "fees_v1_activation_daa");
        assert!(switches_of(&serde_json::json!({})).is_empty());
        assert!(switches_of(&serde_json::json!(null)).is_empty());
    }

    #[test]
    fn race_line_parses() {
        let line = "a1b2c3d4e5f60718 device NVIDIA_GeForce_RTX_5090 driver 581.4 arch sm_120 loads 128 wide 0 variants 4 base=115.900/40r/1w u2=117.200/40r/1w ldg=- w4=118.300/40r/4w winner w4 118.300 base 115.900 gain +2.07% compile 1234 bench 9876 total 11110 ms tuned order | ldg: compile: identifier __ldg undefined";
        let r = parse_race(line).unwrap();
        assert_eq!(r.epoch, "a1b2c3d4e5f60718");
        assert_eq!(r.device, "NVIDIA_GeForce_RTX_5090");
        assert_eq!((r.driver.as_str(), r.arch.as_str(), r.loads, r.wide), ("581.4", "sm_120", 128, 0));
        assert_eq!(r.variants.len(), 4);
        assert_eq!(r.variants["w4"], 118.3);
        assert!(r.variants["ldg"].is_null());
        assert_eq!((r.winner.as_str(), r.mhs, r.base_mhs, r.gain_pct, r.total_ms), ("w4", 118.3, 115.9, 2.07, 11110.0));
        assert!(r.tuned && !r.pinned);
        assert_eq!(r.notes, "ldg: compile: identifier __ldg undefined");
        // the Metal shape, pinned, no notes
        let r = parse_race("0000000000000000 device Apple_M5_Max driver macos-26.0.1 arch metal loads 128 wide 0 variants 2 base=-/1024t/32w u2=9.800/1024t/32w winner u2 9.800 base 0.000 gain +0.00% compile 300 bench 10 total 310 ms pinned by tuning").unwrap();
        assert!(r.pinned && r.winner == "u2" && r.variants["base"].is_null());
        assert!(parse_race("not a race line").is_none());
    }

    fn r(blocks: u64, headers: u64, peers: u64, flag: Option<bool>) -> Reading {
        Reading { blocks, headers, peers, flag }
    }

    /// the second Windows machine, 4 October 2026: syncing, then every relayed block refused (clock), then the catch-up burst, then steady.
    #[test]
    fn sync_state_machine() {
        // syncing: headers run ahead of blocks
        assert!(!sync_decision(&r(0, 500, 1, Some(false)), false, 0.0, false, false));
        // blocks refused for a slow clock: headers climb, blocks do not, nothing accepted
        assert!(!sync_decision(&r(0, 600, 1, Some(false)), false, 0.0, false, false));
        assert!(!sync_decision(&r(0, 700, 1, Some(false)), false, 120.0, false, false));
        // the clock is fixed: the catch-up burst, blocks moving but still behind the headers
        assert!(!sync_decision(&r(46, 700, 1, Some(false)), true, 0.0, false, false));
        assert!(!sync_decision(&r(400, 700, 1, Some(false)), true, 0.0, false, false));
        // caught up and moving: synced, whatever getInfo's flag says
        assert!(sync_decision(&r(699, 700, 1, Some(false)), true, 0.0, false, false));
        assert!(sync_decision(&r(700, 700, 1, None), true, 0.0, false, false));
        // steady: one reading with the same count (a quiet 10 s) is still synced once stable for a minute
        assert!(!sync_decision(&r(700, 700, 1, Some(false)), false, 10.0, false, false));
        assert!(sync_decision(&r(700, 700, 1, Some(false)), false, 60.0, false, false));
        // our block accepted in the last minute: proof of sync even with a stale reading
        assert!(sync_decision(&r(0, 700, 1, Some(false)), false, 0.0, true, false));
        // the node's own flag
        assert!(sync_decision(&r(650, 700, 1, Some(true)), false, 0.0, false, false));
        // no peer: never synced from the numbers alone
        assert!(!sync_decision(&r(700, 700, 0, None), true, 100.0, false, false));
        // a private test node
        assert!(sync_decision(&r(0, 0, 0, None), false, 0.0, false, true));
    }

    /// Ledger N4 (6 October 2026): a home miner's node kept one same-digest peer that relayed nothing for 53 minutes;
    /// today's rule said "synced" the whole time. The known-failed case first, then the fix.
    #[test]
    fn n4_a_frozen_tip_is_never_synced() {
        // the frozen tip: caught up, one peer, the count stable for an hour, getInfo's flag true. Today: synced.
        let frozen = r(133_000, 133_000, 1, Some(true));
        assert!(sync_decision(&frozen, false, 3_180.0, false, false), "the old rule says synced on the frozen tip (the N4 failure)");
        // the fix: the tip age gates it
        let (ok, cause) = sync_decision_v2(&frozen, false, 3_180.0, false, false, 3_180.0);
        assert!(!ok);
        assert_eq!(cause, "frozen");
        // the same node moving: synced
        assert_eq!(sync_decision_v2(&frozen, true, 0.0, false, false, 4.0), (true, ""));
        // exactly at the bound is still synced; one second over is not
        assert_eq!(sync_decision_v2(&frozen, false, 60.0, false, false, 120.0), (true, ""));
        assert_eq!(sync_decision_v2(&frozen, false, 60.0, false, false, 121.0), (false, "frozen"));
        // an older node that does not report the age (-1) keeps today's words
        assert_eq!(sync_decision_v2(&frozen, false, 60.0, false, false, -1.0), (true, ""));
        // our own block accepted in the last minute proves the tip moves, whatever the reported age
        assert_eq!(sync_decision_v2(&frozen, false, 60.0, true, false, 900.0), (true, ""));
        // no peer at all: "no peers", not "frozen" and never synced
        assert_eq!(sync_decision_v2(&r(133_000, 133_000, 0, Some(true)), false, 600.0, false, false, 500.0), (false, "no peers"));
        // still downloading: "syncing"
        assert_eq!(sync_decision_v2(&r(100, 700, 1, Some(false)), true, 0.0, false, false, 2.0), (false, "syncing"));
        // caught up but the flag false and the count not yet stable: "behind"
        assert_eq!(sync_decision_v2(&r(700, 700, 1, Some(false)), false, 10.0, false, false, 5.0), (false, "behind"));
        // a private test node is always synced
        assert_eq!(sync_decision_v2(&r(0, 0, 0, None), false, 0.0, false, true, 9_999.0), (true, ""));
    }

    #[test]
    fn n4_the_stall_exit_is_code_45_or_the_stalled_line() {
        assert!(is_stall_exit(45, &[]));
        assert!(is_stall_exit(1, &["1791327000.123 STALLED 'nvidia-1ccfe586-1': no new template for 300 s (tip d3dc2f78, daa 197752); the node has fallen off the network or stopped moving; hashing and voting paused, exiting with code 45 so the launcher restarts (and after a second stall, restarts the node)".to_string()]));
        assert!(!is_stall_exit(1, &["worker fault: rejected nonce".to_string()]));
        assert!(!is_stall_exit(42, &[]));
    }

    #[test]
    fn node_warning() {
        let l = "2026-10-04 11:02:11.123+00:00 [WARN ] HandleRelayInvsFlow flow error: the block timestamp is too far into the future: block timestamp is 1791112663000 but maximum timestamp allowed is 1791112600000";
        assert_eq!(super::behind_from_warning(l), Some(63.0));
        assert_eq!(super::behind_from_warning("no numbers here"), None);
    }
}

/// A worker line that names a self-test failure (the vectors, the cache check or the source check did not pass):
/// `error 0 self-test FAIL ...`, `vectors 3 of 96 FAIL`, `Source check FAIL`, `self-test failed`.
pub(crate) fn is_self_test_failure(text: &str) -> bool {
    let t = text.to_ascii_lowercase();
    (t.contains("self-test") || t.contains("selftest") || t.contains("source check") || t.contains("vectors")) && (t.contains("fail") || t.contains("mismatch"))
}

fn short(s: &str, n: usize) -> String {
    if s.chars().count() <= n { s.to_string() } else { format!("{}...", s.chars().take(n).collect::<String>()) }
}

fn tail_of(path: &std::path::Path, n: usize) -> Vec<String> {
    let Ok(text) = std::fs::read_to_string(path) else { return vec![] };
    let lines: Vec<&str> = text.lines().filter(|l| !l.trim().is_empty()).collect();
    lines.iter().rev().take(n).rev().map(|s| s.to_string()).collect()
}

/// Days since 1970-01-01 to a civil date (Howard Hinnant's algorithm).
fn civil_from_days(z: i64) -> (i64, u32, u32) {
    let z = z + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    (if m <= 2 { y + 1 } else { y }, m, d)
}

/// One pack export at a time (5 October 2026). prepare_worker runs on a thread per card, so two cards starting
/// together ran two `igneum-miner export-pack` processes into the same folder; across an epoch change they
/// interleaved and the first Windows machine's packs\devnet was left with one epoch's program.h and the other's seeds.txt, which every
/// OpenCL worker start then refused ("the epoch seed bytes do not give the pack's IGNEUM_SEEDW_INIT") until the next
/// export. The second export of a pair rewrites the same pack, which is harmless.
static EXPORT_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());
/// MF-14: who holds the export now (the miner label and since when) and the last export's error, for the row of a
/// card that waits behind it.
static EXPORT_HOLDER: std::sync::Mutex<Option<(String, Instant)>> = std::sync::Mutex::new(None);
static EXPORT_LAST_ERROR: std::sync::Mutex<Option<String>> = std::sync::Mutex::new(None);

/// What blocks `label`'s export now, in plain words, or empty.
fn export_blocker(label: &str) -> String {
    if let Some((who, since)) = EXPORT_HOLDER.lock().unwrap_or_else(|e| e.into_inner()).clone() {
        if who != label {
            return format!("another card's export holds the pack lock ({who}, {} s)", since.elapsed().as_secs());
        }
        return format!("this card's export is still running ({} s)", since.elapsed().as_secs());
    }
    EXPORT_LAST_ERROR.lock().unwrap_or_else(|e| e.into_inner()).clone().map(|e| format!("the last export failed: {e}")).unwrap_or_default()
}
/// When the pack was last exported: an export under `EXPORT_REUSE_S` old is reused unless forced (MF-4, 7 October
/// 2026: a card whose worker failed every few seconds exported on every restart and held two healthy cards in
/// "loading the program" past the watchdog).
static LAST_EXPORT: std::sync::Mutex<Option<Instant>> = std::sync::Mutex::new(None);
const EXPORT_REUSE_S: u64 = 60;

/// Exports this hour's program pack from the node to <app data>\packs\devnet (the prebuilt workers read it with --pack).
/// One export per minute serves every card; `force` (a refused pack) exports again now.
#[allow(unused_variables)]
fn export_pack(shared: &Arc<Shared>, bins: &Bins, force: bool, label: &str) -> Result<(), String> {
    let _one_at_a_time = EXPORT_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    *EXPORT_HOLDER.lock().unwrap_or_else(|e| e.into_inner()) = Some((label.to_string(), Instant::now()));
    let r = export_pack_locked(shared, bins, force);
    *EXPORT_HOLDER.lock().unwrap_or_else(|e| e.into_inner()) = None;
    *EXPORT_LAST_ERROR.lock().unwrap_or_else(|e| e.into_inner()) = r.as_ref().err().cloned();
    r
}

fn export_pack_locked(shared: &Arc<Shared>, bins: &Bins, force: bool) -> Result<(), String> {
    let pack = shared.runtime.app_dir.join("packs").join("devnet");
    let _ = std::fs::create_dir_all(&pack);
    if !force && pack.join("seeds.txt").exists() {
        let fresh = LAST_EXPORT.lock().unwrap_or_else(|e| e.into_inner()).map(|t| t.elapsed() < Duration::from_secs(EXPORT_REUSE_S)).unwrap_or(false);
        if fresh {
            shared.log("export-pack: reusing the pack exported under a minute ago");
            return Ok(());
        }
    }
    *LAST_EXPORT.lock().unwrap_or_else(|e| e.into_inner()) = Some(Instant::now());
    let out = crate::detect::run_timeout(std::process::Command::new(&bins.miner).args(["export-pack", &shared.runtime.rpc_url(), &pack.display().to_string()]), None, Duration::from_secs(120)).unwrap_or_default();
    shared.log(&format!("export-pack: {}", out.lines().last().unwrap_or("no output")));
    if pack.join("seeds.txt").exists() { Ok(()) } else { Err("export-pack wrote no seeds.txt (is the node reachable?)".into()) }
}

/// Today's Windows path when no prebuilt worker ships: export the program pack, run proto-cuda\build.bat (or
/// proto-opencl\build.bat) in the MSVC environment, and use the exe it writes. Untested from the Mac.
#[allow(unused_variables)]
fn build_worker_from_source(shared: &Arc<Shared>, bins: &Bins, vendor: &str) -> Result<PathBuf, String> {
    #[cfg(not(windows))]
    {
        Err("no GPU worker is installed".into())
    }
    #[cfg(windows)]
    {
        use std::process::Command;
        let _one_at_a_time = EXPORT_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let pack = shared.runtime.app_dir.join("packs").join("devnet");
        let _ = std::fs::create_dir_all(&pack);
        let out = crate::detect::run_timeout(Command::new(&bins.miner).args(["export-pack", &shared.runtime.rpc_url(), &pack.display().to_string()]), None, Duration::from_secs(120)).unwrap_or_default();
        shared.log(&format!("export-pack: {}", out.lines().last().unwrap_or("")));
        if !pack.join("seeds.txt").exists() {
            return Err("export-pack wrote no seeds.txt (is the node reachable?)".into());
        }
        let (sub, exe, cmd) = if vendor == "nvidia" {
            ("proto-cuda", "igneum-bench-cuda-devnet.exe", "build.bat devnet sm_120")
        } else {
            ("proto-opencl", "igneum-bench-cl-devnet.exe", "build.bat devnet")
        };
        // the sources ship under Program Files (read-only, R4.3.3); the build runs under the app data folder
        let src = bins.dir.join(sub);
        if !src.join("build.bat").exists() {
            return Err(format!("{sub}\\build.bat is not installed; the prebuilt worker is missing too"));
        }
        let dir = shared.runtime.app_dir.join("build").join(sub);
        let _ = std::fs::create_dir_all(&dir);
        if let Ok(rd) = std::fs::read_dir(&src) {
            for e in rd.flatten() {
                if e.path().is_file() {
                    let _ = std::fs::copy(e.path(), dir.join(e.file_name()));
                }
            }
        }
        // the pack where build.bat expects it
        let dest = dir.join("packs").join("devnet");
        let _ = std::fs::create_dir_all(&dest);
        if let Ok(rd) = std::fs::read_dir(&pack) {
            for e in rd.flatten() {
                let _ = std::fs::copy(e.path(), dest.join(e.file_name()));
            }
        }
        let mut vcvars = None;
        for pf in ["C:\\Program Files\\Microsoft Visual Studio", "C:\\Program Files (x86)\\Microsoft Visual Studio"] {
            if let Ok(years) = std::fs::read_dir(pf) {
                for y in years.flatten() {
                    if let Ok(eds) = std::fs::read_dir(y.path()) {
                        for e in eds.flatten() {
                            let p = e.path().join("VC").join("Auxiliary").join("Build").join("vcvarsall.bat");
                            if p.exists() {
                                vcvars = Some(p);
                            }
                        }
                    }
                }
            }
        }
        let vc = vcvars.ok_or("Visual Studio with the MSVC v143 x64 component is not installed (vcvarsall.bat not found)")?;
        let line = format!("\"{}\" x64 -vcvars_ver=14.30 >nul 2>&1 && cd /d \"{}\" && {}", vc.display(), dir.display(), cmd);
        shared.log(&format!("building the {sub} worker: cmd /s /c \"{line}\""));
        let out = crate::detect::run_timeout(Command::new(crate::platform::tool("cmd")).args(["/s", "/c", &format!("\"{line}\"")]), None, Duration::from_secs(900)).unwrap_or_default();
        for l in out.lines().rev().take(8).collect::<Vec<_>>().into_iter().rev() {
            shared.log(&format!("  build: {l}"));
        }
        let p = dir.join(exe);
        if p.exists() { Ok(p) } else { Err(format!("{exe} was not written; see the log")) }
    }
}

/// One `amd` line of igneum-gpu-telemetry (proto-opencl/gpu-telemetry.c): the fields the card row carries.
/// `ordinal_in_kind` is the line's rank among the lines of the same kind in one sample: the helper's `amd N` is
/// the rank over all kinds, so the parser tracks the kinds it has seen through `AmdTelemetrySample::new`
/// per sample; a single line parses with the rank 0 when it is the first of its kind in its `amd N` ordering.
#[derive(Debug, Clone, PartialEq)]
pub struct AmdTelemetry {
    pub ordinal: usize,
    pub ordinal_in_kind: usize,
    pub bus: String,
    pub kind: String,
    pub name: String,
    pub watts: f64,
    pub temp_c: f64,
    pub fan_rpm: f64,
    pub fan_pct: f64,
    pub mclk_mhz: f64,
    pub gclk_mhz: f64,
    pub util_pct: f64,
    /// the limits in force (an earlier release lines; -1.0 on older helpers): 100 + the power offset, the max GPU clock
    pub plimit_pct: f64,
    pub gmax_mhz: f64,
    pub source: String,
}

/// Parses `amd <n> bus <b> kind <k> name "<name>" watts <w> temp_c <t> fan_rpm <r> fan_pct <p> mclk_mhz <m>
/// gclk_mhz <g> util_pct <u> source <s>`; a `-` value reads as -1.0. Anything else (info, end) gives None.
/// With one card per kind (the common case) `ordinal_in_kind` is 0 for the discrete card and 0 for the integrated
/// one whatever their `amd N`; with several discrete cards the helper's order within the kind is kept: the rank is
/// the number of earlier lines of the same kind, which the helper encodes by listing kinds contiguously (ADLX lists
/// GPUs in a fixed order, so the rank of a card is stable across samples).
pub fn parse_amd_telemetry(line: &str) -> Option<AmdTelemetry> {
    let line = line.trim();
    if !line.starts_with("amd ") {
        return None;
    }
    let (head, rest) = line.split_once(" name \"")?;
    let (name, tail) = rest.split_once('"')?;
    let hp: Vec<&str> = head.split_whitespace().collect();
    if hp.len() < 6 || hp[2] != "bus" || hp[4] != "kind" {
        return None;
    }
    let ordinal: usize = hp[1].parse().ok()?;
    let tp: Vec<&str> = tail.split_whitespace().collect();
    let num = |key: &str| -> Option<f64> {
        let i = tp.iter().position(|p| *p == key)?;
        let v = tp.get(i + 1)?;
        if *v == "-" { Some(-1.0) } else { v.parse::<f64>().ok() }
    };
    let watts = num("watts")?;
    let temp_c = num("temp_c")?;
    let fan_rpm = num("fan_rpm")?;
    let fan_pct = num("fan_pct")?;
    let mclk_mhz = num("mclk_mhz")?;
    let gclk_mhz = num("gclk_mhz")?;
    let util_pct = num("util_pct")?;
    let plimit_pct = num("plimit_pct").unwrap_or(-1.0);
    let gmax_mhz = num("gmax_mhz").unwrap_or(-1.0);
    let source = tp.iter().position(|p| *p == "source").and_then(|i| tp.get(i + 1)).map(|s| s.to_string()).unwrap_or_default();
    let kind = hp[5].to_string();
    // the rank within the kind: the helper lists one integrated card at most and it comes first when present
    // (ADLX order on every PC seen so far), so a discrete card's rank is its ordinal minus the integrated ones before it
    let ordinal_in_kind = if kind == "discrete" && ordinal > 0 { ordinal - 1 } else if kind == "discrete" { 0 } else { 0 };
    Some(AmdTelemetry { ordinal, ordinal_in_kind, bus: hp[3].to_string(), kind, name: name.to_string(), watts, temp_c, fan_rpm, fan_pct, mclk_mhz, gclk_mhz, util_pct, plimit_pct, gmax_mhz, source })
}

#[cfg(test)]
mod amd_telemetry_tests {
    use super::*;

    #[test]
    fn a_sysfs_line_from_the_fixture_parses() {
        // proto-opencl/gpu-telemetry.c on the Mac against a fixture tree, 5 October 2026
        let l = "amd 0 bus 0000:0c:00.0 kind discrete name \"AMD Radeon RX 9070 XT\" watts 287.0 temp_c 61.0 fan_rpm 1180 fan_pct 30 mclk_mhz 1258 gclk_mhz 2450 util_pct 90 source sysfs";
        let s = parse_amd_telemetry(l).unwrap();
        assert_eq!((s.ordinal, s.ordinal_in_kind, s.bus.as_str(), s.kind.as_str(), s.name.as_str()), (0, 0, "0000:0c:00.0", "discrete", "AMD Radeon RX 9070 XT"));
        assert_eq!((s.watts, s.temp_c, s.fan_rpm, s.fan_pct, s.mclk_mhz, s.gclk_mhz, s.util_pct), (287.0, 61.0, 1180.0, 30.0, 1258.0, 2450.0, 90.0));
        assert_eq!(s.source, "sysfs");
    }

    #[test]
    fn a_dash_reads_as_unknown_and_other_lines_give_none() {
        let l = "amd 1 bus 98 kind discrete name \"AMD Radeon RX 9070 XT\" watts 250.3 temp_c 58.0 fan_rpm 900 fan_pct - mclk_mhz 1258 gclk_mhz 2460 util_pct 97.5 source adlx";
        let s = parse_amd_telemetry(l).unwrap();
        assert_eq!(s.fan_pct, -1.0);
        assert_eq!(s.ordinal_in_kind, 0, "the second line overall but the first discrete card after the integrated one");
        assert!(parse_amd_telemetry("end 3.2 ms 2 card(s)").is_none());
        assert!(parse_amd_telemetry("info adlx: ADLXHelper_Initialize returned 1").is_none());
        assert!(parse_amd_telemetry("amd 0 bus - kind - name \"x\" watts").is_none());
    }

    /// The an earlier release helper's lines (the tuning commands, usage header of proto-opencl/gpu-telemetry.c): the sample
    /// line carries the limits in force, the `tune` line the ranges a card allows. An older line (no plimit_pct,
    /// no gmax_mhz) still parses with -1 there.
    #[test]
    fn the_tune_line_and_the_limits_in_force_parse() {
        let l = "amd 1 bus 98 kind discrete name \"AMD Radeon RX 9070 XT\" watts 198.9 temp_c 64.0 fan_rpm 657 fan_pct 25 mclk_mhz 2505 gclk_mhz 3290 util_pct 100 plimit_pct 100 gmax_mhz 3100 source adlx";
        let s = parse_amd_telemetry(l).unwrap();
        assert_eq!((s.plimit_pct, s.gmax_mhz, s.gclk_mhz), (100.0, 3100.0, 3290.0));
        let old = "amd 0 bus 0000:0c:00.0 kind discrete name \"AMD Radeon RX 9070 XT\" watts 287.0 temp_c 61.0 fan_rpm 1180 fan_pct 30 mclk_mhz 1258 gclk_mhz 2450 util_pct 90 source sysfs";
        assert_eq!(parse_amd_telemetry(old).unwrap().plimit_pct, -1.0);
        let t = parse_amd_tune("tune 1 name \"AMD Radeon RX 9070 XT\" gmax 3100 gmax_range 500 3400 plimit 0 plimit_range -30 15 factory 1 ok").unwrap();
        assert_eq!((t.ordinal, t.name.as_str(), t.gmax, t.gmax_min, t.gmax_max, t.plimit, t.plimit_min, t.plimit_max, t.factory, t.ok), (1, "AMD Radeon RX 9070 XT", 3100.0, 500.0, 3400.0, 0.0, -30.0, 15.0, true, true));
        let e = parse_amd_tune("tune 0 name \"x\" gmax - gmax_range - - plimit - plimit_range - - factory 0 ADLX: GetManualGraphicsTuning returned 2").unwrap();
        assert!(!e.ok);
        assert_eq!(e.error, "ADLX: GetManualGraphicsTuning returned 2");
        assert_eq!(e.gmax_max, -1.0);
        assert!(parse_amd_tune("end 3.2 ms 2 card(s)").is_none());
        assert!(parse_amd_tune("tune 0 name \"x\" gmax 1").is_none());
    }

    #[test]
    fn the_perfcounter_fallback_line_parses() {
        let l = "amd 0 bus luid_0x00000000_0x0000D4E3 kind - name \"-\" watts - temp_c - fan_rpm - fan_pct - mclk_mhz - gclk_mhz - util_pct 100 source perfcounter";
        let s = parse_amd_telemetry(l).unwrap();
        assert_eq!((s.watts, s.util_pct, s.source.as_str()), (-1.0, 100.0, "perfcounter"));
    }
}

#[cfg(test)]
mod fleet_url_tests {
    use super::fleet_url_for_log;
    #[test]
    fn the_log_names_the_host_and_the_tokens_last_four_alone() {
        assert_eq!(fleet_url_for_log("https://build.igneum.network/_fleet/08605bedda365cd2b9f6ef496a67952e/"), "https://build.igneum.network/_fleet/...952e/");
        assert_eq!(fleet_url_for_log("https://host/_fleet/abcd/"), "https://host/_fleet/.../");
        assert_eq!(fleet_url_for_log("https://host/_fleet/abcd"), "https://host/_fleet/...");
        assert_eq!(fleet_url_for_log("https://host/console/"), "https://host/.../");
        assert_eq!(fleet_url_for_log("nonsense"), "...");
    }
}

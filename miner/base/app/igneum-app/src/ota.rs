//! Over-the-air updates of the app (and the node, miner and workers inside it). the founder's rule: every app updates
//! itself and downloads the update without being asked. This is also how a consensus upgrade (a height-activated
//! rule such as difficulty v2) reaches every node before its activation height.
//!
//! The loop, driven from the engine's tick:
//!   check (on start, then every ten minutes with jitter (an earlier release; hourly before)): fetch igneum-app-latest.json and its .sig
//!   (the public release store's, config::DEFAULT_UPDATE_MANIFEST at build.igneum.network/releases/ember/latest/, unless
//!   the package or IGNEUM_APP_UPDATE_MANIFEST names another), verify the release key's SSH signature (namespace
//!   igneum-release) with the key compiled into src/manifest.rs, parse, compare versions
//!   -> download (curl with resume into <app data>/app/updates/, then size and sha256 against the manifest)
//!   -> stage (macOS: mount the DMG or unpack the zip, copy the new bundle next to the running one, check its
//!      engine answers --version with the manifest's version; Windows: the installer is the staged artefact)
//!   -> ready: with auto_update (settings, default on) the engine applies at the next safe moment (node synced,
//!      no hourly boundary within 3 minutes, no worker starting), at once when a consensus activation is within
//!      1,800 blocks or the version is below min_supported_version, or when the user clicks Install now
//!   -> apply: the engine stops the miners, then the node, writes update-pending.json, starts a detached helper
//!      (ota-apply.sh / ota-apply.ps1 in the app data folder) and exits. macOS: the helper waits for the window to
//!      quit, moves the old bundle to "Ember.app.previous", the new one in, and opens it. Windows: the helper
//!      runs the per-user Inno installer /VERYSILENT FIRST, with the engine still mining; the installer stops the
//!      engine itself (api/quit), replaces the files and relaunches the app with /IGNOTA=1. An administrator prompt
//!      (an install still in Program Files) that nobody answers leaves the machine mining: the update is deferred.
//!   Machines take turns: each applies at its own turn, up to three minutes after the update is ready (machine id
//!   modulo 180 seconds; F-E52 replaced the hourly slot on 14 October 2026), and never while
//!   the network lost over 30% of its identities in 10 minutes (/api/live).
//!   -> rollback: the helper restores the previous bundle when the new app does not start twice; the new engine
//!      counts its own starts in update-pending.json and, on the third start without 90 healthy seconds, restores
//!      the previous version (macOS: the .previous bundle; Windows: the previous installer kept in updates/).
//!
//! Environment (tests): IGNEUM_APP_UPDATE_MANIFEST overrides the manifest URL from igneum-app.json,
//! IGNEUM_APP_UPDATE_CHECK_SECS the check interval (240 s), IGNEUM_APP_UPDATE_FIRST_SECS the delay of the first check.

use crate::engine::{Cmd, Shared};
use crate::manifest::{self, Manifest, Moment, PlatformEntry};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::Arc;
use std::time::{Duration, Instant};

/// A manifest published this long before the engine started is a catch-up: the hourly rollout slot does not apply.
const CATCH_UP_AFTER_S: u64 = 3600;
const HEALTHY_AFTER_S: u64 = 90;
pub const CHECK_EVERY_S: u64 = 240;
const RETRY_AFTER_ERROR_S: u64 = 600;

pub enum Event {
    /// The manifest fetched, verified and parsed (or why not).
    Checked(Result<Manifest, String>),
    /// The installer or disk image on disk, size and sha256 checked.
    Downloaded(Result<PathBuf, String>),
    /// macOS: the new bundle staged next to the running one. Windows: the installer path again.
    Staged(Result<PathBuf, String>),
    /// The network's identity count from /api/live (state.miners_10m); None when the site did not answer.
    Live(Option<u64>),
}

/// What the engine must do now.
pub enum Action {
    Apply,
}

/// What launch_apply started.
#[allow(dead_code)] // one variant per platform
pub enum Launch {
    /// macOS: the helper waits for this engine to exit; the engine leaves through its quit path now.
    QuitNow,
    /// Windows: the installer runs first, while the engine keeps mining; the installer stops the engine itself
    /// (api/quit) once it is allowed to run. The engine stays up and watches update-result.json for a deferral.
    InstallerRunning,
}

pub struct Ctx {
    pub node_synced: bool,
    /// the network's finality is paused (engine: synced and no lock for manifest::FINALITY_PAUSE_S)
    pub finality_paused: bool,
    pub boundary_eta_s: Option<i64>,
    pub miner_busy: bool,
    /// No card mines or starts (an earlier release): a staged update applies at once on a synced node.
    pub miner_idle: bool,
    /// A remote job is running (src/jobrun.rs): the install holds, urgent or not.
    pub job_active: bool,
    pub daa: u64,
}

/// What the old engine leaves for the new one (update-pending.json) and the helper's answer (update-result.json).
#[derive(Clone, Default)]
struct Pending {
    from: String,
    to: String,
    at: f64,
    starts: u32,
    previous_installer: String,
}

pub struct Updater {
    manifest_url: String,
    current: String,
    app_dir: PathBuf,
    dir: PathBuf,
    auto: bool,
    manifest: Option<Manifest>,
    /// driver-check: the table was written or removed since the engine last looked
    drivers_changed: bool,
    entry: Option<PlatformEntry>,
    file: Option<PathBuf>,
    staged: Option<PathBuf>,
    busy: bool,
    next_check: Instant,
    ready_since: Option<Instant>,
    /// when the node last went from unsynced to synced (None while it is not): manifest::SYNCED_APPLY_BOUND_S counts from it
    synced_since: Option<Instant>,
    last_safe_check: Instant,
    install_asked: bool,
    pending: Option<Pending>,
    started: Instant,
    healthy_marked: bool,
    jitter: u64,
    /// versions whose apply failed or that were rolled back: never re-applied automatically (R4.3.6)
    failed_versions: Vec<String>,
    /// the last manifest's min_supported_version, kept across restarts (updates/manifest.json): the rollback floor
    min_supported: String,
    /// macOS: the digest of the staged bundle at stage time, re-checked right before the swap (R4.3.5)
    staged_digest: String,
    /// the consensus override file written from the manifest, when it changed since the last take
    override_changed: Option<PathBuf>,
    override_daa: u64,
    /// the per-card tuning file written from the manifest, when it changed since the last take
    tuning_changed: Option<PathBuf>,
    /// Windows: the installer was started and the engine is still up (it stops us when it may run)
    apply_launched: Option<Instant>,
    /// the administrator prompt was not answered: no automatic retry before this (Install now still works)
    deferred_until: Option<Instant>,
    /// this machine's turn: seconds after an update is ready before it applies (manifest::stagger_seconds of the machine id)
    slot: u64,
    /// When this engine started (unix seconds): an update published more than an hour before it is a catch-up, not a
    /// rollout, and skips the hourly slot (the founder's morning of 6 October 2026: the first Windows machine came up after the an earlier release publish and
    /// sat on "installs at the next safe moment" until he pressed Install now).
    started_unix: u64,
    catch_up_logged: bool,
    /// identity counts from /api/live over the last 10 minutes, sampled while an update is ready
    live_samples: Vec<(Instant, u64)>,
    live_next: Instant,
    live_busy: bool,
    live_api: String,
    /// ui-ota: the interface entry of the last verified manifest, handed to src/uiota.rs once per check
    /// (Some(None) = the channel was withdrawn: the kill switch)
    ui_change: Option<Option<manifest::UiEntry>>,
}

impl Updater {
    pub fn new(shared: &Arc<Shared>) -> Updater {
        let env = |k: &str| std::env::var(k).ok().filter(|v| !v.is_empty());
        let manifest_url = env("IGNEUM_APP_UPDATE_MANIFEST").unwrap_or_else(|| shared.packaged.update_manifest.clone());
        let app_dir = shared.runtime.app_dir.clone();
        let dir = app_dir.join("updates");
        let _ = std::fs::create_dir_all(&dir);
        let jitter = shared.runtime.machine_id.bytes().fold(0u64, |a, b| a.wrapping_mul(31).wrapping_add(b as u64));
        let first = env("IGNEUM_APP_UPDATE_FIRST_SECS").and_then(|v| v.parse().ok()).unwrap_or(20 + jitter % 30);
        let auto = shared.settings.lock().unwrap().auto_update;
        let now = Instant::now();
        let mut u = Updater {
            manifest_url,
            current: crate::engine::VERSION.to_string(),
            app_dir,
            dir,
            auto,
            manifest: None,
            drivers_changed: false,
            entry: None,
            file: None,
            staged: None,
            busy: false,
            next_check: now + Duration::from_secs(first),
            ready_since: None,
            synced_since: None,
            last_safe_check: now,
            install_asked: false,
            pending: None,
            started: now,
            healthy_marked: false,
            jitter,
            failed_versions: Vec::new(),
            min_supported: String::new(),
            staged_digest: String::new(),
            override_changed: None,
            override_daa: 0,
            tuning_changed: None,
            apply_launched: None,
            deferred_until: None,
            slot: manifest::stagger_seconds(&shared.runtime.id8()),
            started_unix: crate::platform::unix_now(),
            catch_up_logged: false,
            live_samples: Vec::new(),
            live_next: now,
            live_busy: false,
            live_api: env("IGNEUM_APP_LIVE_API").unwrap_or_else(|| live_api_from(&shared.packaged.live_page)),
            ui_change: None,
        };
        shared.log(&format!("update turn: {} s after an update is ready (machine id {})", u.slot, shared.runtime.id8()));
        #[cfg(windows)]
        {
            // the login entry follows the install folder (a per-user install replaces one in Program Files)
            if crate::platform::start_at_login_is_on() {
                let _ = crate::platform::set_start_at_login(true);
            }
            // a measurement engine (--sweep) uses the installed app's node and asks for nothing: the rule is the
            // installed app's (the dry run of 6 October 2026 raised a second UAC prompt from here)
            if !shared.runtime.sweep_only {
                firewall_first_run(shared);
                boot_task_first_run(shared);
            }
        }
        u.failed_versions = std::fs::read_to_string(u.failed_path()).ok().and_then(|t| serde_json::from_str::<Vec<String>>(&t).ok()).unwrap_or_default();
        // the cached manifest: the rollback floor and the consensus override are known before the first check
        if let Ok(text) = std::fs::read_to_string(u.dir.join("manifest.json")) {
            if let Ok(m) = manifest::parse(&text) {
                u.min_supported = m.min_supported_version.clone();
                u.write_override(shared, &m);
                u.write_tuning(shared, &m);
                u.write_drivers(shared, &m);
            }
        }
        u.settle_previous(shared);
        u.publish(shared);
        u
    }

    fn failed_path(&self) -> PathBuf {
        self.app_dir.join("failed-versions.json")
    }

    fn remember_failed(&mut self, shared: &Arc<Shared>, ver: &str) {
        if ver.is_empty() || self.failed_versions.iter().any(|v| v == ver) {
            return;
        }
        self.failed_versions.push(ver.to_string());
        let _ = std::fs::write(self.failed_path(), serde_json::to_string(&self.failed_versions).unwrap_or_default());
        shared.log(&format!("update: {ver} is marked failed; it will not be applied again by itself (Install now still can)"));
    }

    /// The consensus override from the manifest: consensus.override written as is, or
    /// {"difficulty_v2_activation_daa": activation_height} when only the height is given. The engine takes the
    /// path with take_override_change() and restarts the node at a safe moment.
    fn write_override(&mut self, shared: &Arc<Shared>, m: &Manifest) {
        // F14: the public miner takes its consensus parameters from its node's rules, never from a manifest; a compromised
        // update key activates nothing there. The lab build (our fleet) takes the override as before.
        if !crate::edition::manifest_override_allowed() {
            if m.override_params.is_some() || m.activation_height.is_some() {
                shared.log("manifest: a consensus override is carried; the public build takes its rules from the node and ignores it");
            }
            return;
        }
        let obj = match (&m.override_params, m.activation_height) {
            (Some(o), _) => o.clone(),
            (None, Some(h)) => json!({ "difficulty_v2_activation_daa": h }),
            (None, None) => return,
        };
        let path = self.app_dir.join("override.json");
        let text = obj.to_string();
        let same = std::fs::read_to_string(&path).ok().as_deref() == Some(text.as_str());
        if !same {
            if let Err(e) = std::fs::write(&path, &text) {
                shared.log(&format!("could not write {}: {e}", path.display()));
                return;
            }
            shared.event("info", &format!("consensus parameters from the signed manifest: {text}"));
            self.override_changed = Some(path.clone());
        }
        self.override_daa = m.activation_height.or_else(|| obj.get("difficulty_v2_activation_daa").and_then(|v| v.as_u64())).unwrap_or(0);
        shared.state.lock().unwrap().node.consensus_switch_daa = self.override_daa;
    }

    /// The per-card tuning from the manifest (docs/design/miner-tuning.md): `tuning` written as is to
    /// <app data>/tuning.json; the GPU workers read it at their next prepare through IGNEUM_TUNING_FILE (the engine
    /// passes the path to every miner it starts). A manifest without tuning removes the file: the workers race
    /// every variant again.
    fn write_tuning(&mut self, shared: &Arc<Shared>, m: &Manifest) {
        let path = self.app_dir.join("tuning.json");
        match &m.tuning {
            Some(t) => {
                let text = t.to_string();
                if std::fs::read_to_string(&path).ok().as_deref() == Some(text.as_str()) {
                    return;
                }
                if let Err(e) = std::fs::write(&path, &text) {
                    shared.log(&format!("could not write {}: {e}", path.display()));
                    return;
                }
                let cards = t.get("cards").and_then(|c| c.as_object()).map(|c| c.len()).unwrap_or(0);
                shared.event("info", &format!("kernel tuning from the signed manifest: {cards} card model(s), updated {}", t.get("updated").and_then(|u| u.as_str()).unwrap_or("?")));
                self.tuning_changed = Some(path);
            }
            None => {
                if path.is_file() && std::fs::remove_file(&path).is_ok() {
                    shared.log("the manifest carries no kernel tuning any more; tuning.json removed (the workers race every variant again)");
                    self.tuning_changed = Some(path);
                }
            }
        }
    }

    /// <app data>/drivers.json: the manifest's per-vendor driver table (src/drivers.rs), written as is; the engine
    /// re-reads it and re-evaluates every card's offer on a change. A manifest without a table removes the file.
    fn write_drivers(&mut self, shared: &Arc<Shared>, m: &Manifest) {
        let path = self.app_dir.join("drivers.json");
        match &m.drivers {
            Some(t) => {
                let text = t.to_string();
                if std::fs::read_to_string(&path).ok().as_deref() == Some(text.as_str()) {
                    return;
                }
                if let Err(e) = std::fs::write(&path, &text) {
                    shared.log(&format!("could not write {}: {e}", path.display()));
                    return;
                }
                let n = t.get("vendors").and_then(|c| c.as_object()).map(|c| c.len()).unwrap_or(0);
                shared.event("info", &format!("driver table from the signed manifest: {n} vendor(s), updated {}", t.get("updated").and_then(|u| u.as_str()).unwrap_or("?")));
                self.drivers_changed = true;
            }
            None => {
                if path.is_file() && std::fs::remove_file(&path).is_ok() {
                    shared.log("the manifest carries no driver table any more; drivers.json removed");
                    self.drivers_changed = true;
                }
            }
        }
    }

    /// The driver table changed (written or removed), once per change.
    pub fn take_drivers_change(&mut self) -> bool {
        std::mem::take(&mut self.drivers_changed)
    }

    pub fn drivers_table(&self) -> Option<crate::drivers::Table> {
        let text = std::fs::read_to_string(self.app_dir.join("drivers.json")).ok()?;
        let v: serde_json::Value = serde_json::from_str(&text).ok()?;
        crate::drivers::Table::parse(&v).ok()
    }

    /// The tuning file, once per change (the engine only logs it: the workers read the file at their next prepare).
    /// ui-ota: the interface entry of the last check, once (None until a check has run)
    pub fn take_ui_change(&mut self) -> Option<Option<manifest::UiEntry>> {
        self.ui_change.take()
    }
    pub fn take_tuning_change(&mut self) -> Option<PathBuf> {
        self.tuning_changed.take()
    }

    pub fn tuning_path(&self) -> Option<PathBuf> {
        let p = self.app_dir.join("tuning.json");
        if p.is_file() { Some(p) } else { None }
    }

    /// The override file to start the node with, once per change.
    pub fn take_override_change(&mut self) -> Option<PathBuf> {
        self.override_changed.take()
    }

    pub fn override_path(&self) -> Option<PathBuf> {
        let p = self.app_dir.join("override.json");
        if p.is_file() { Some(p) } else { None }
    }

    pub fn override_daa(&self) -> u64 {
        self.override_daa
    }

    // ---- the files the old engine, the helper and the new engine pass around -------------------------------------

    fn pending_path(&self) -> PathBuf {
        self.app_dir.join("update-pending.json")
    }
    fn result_path(&self) -> PathBuf {
        self.app_dir.join("update-result.json")
    }

    fn read_pending(&self) -> Option<Pending> {
        let v: Value = serde_json::from_str(&std::fs::read_to_string(self.pending_path()).ok()?).ok()?;
        let s = |k: &str| v.get(k).and_then(|x| x.as_str()).unwrap_or("").to_string();
        Some(Pending { from: s("from"), to: s("to"), at: v.get("at").and_then(|x| x.as_f64()).unwrap_or(0.0), starts: v.get("starts").and_then(|x| x.as_u64()).unwrap_or(0) as u32, previous_installer: s("previous_installer") })
    }

    fn write_pending(&self, p: &Pending) {
        let v = json!({ "from": p.from, "to": p.to, "at": p.at, "starts": p.starts, "previous_installer": p.previous_installer, "platform": manifest::platform_name() });
        let _ = std::fs::write(self.pending_path(), v.to_string());
    }

    /// On start: did we just update (or fail to)? Reports it, counts this start, and asks for a rollback when the
    /// new version keeps dying before it is healthy.
    fn settle_previous(&mut self, shared: &Arc<Shared>) {
        let pending = self.read_pending();
        if let Ok(text) = std::fs::read_to_string(self.result_path()) {
            let _ = std::fs::remove_file(self.result_path());
            if let Ok(v) = serde_json::from_str::<Value>(&text) {
                let ok = v.get("ok").and_then(|x| x.as_bool()).unwrap_or(false);
                let err = v.get("error").and_then(|x| x.as_str()).unwrap_or("").to_string();
                let rolled_back = v.get("rolled_back").and_then(|x| x.as_bool()).unwrap_or(false);
                let deferred = v.get("deferred").and_then(|x| x.as_bool()).unwrap_or(false);
                let ver = v.get("version").and_then(|x| x.as_str()).unwrap_or("").to_string();
                // an earlier release: the helper says how the app came back (ok, rolled-back, relaunched, installer-failed) and after how long
                let ret = v.get("return").and_then(|x| x.as_str()).unwrap_or("");
                let ready_s = v.get("ready_s").and_then(|x| x.as_i64()).unwrap_or(-1);
                if !ret.is_empty() {
                    shared.log(&format!("update-return: the helper reports '{ret}' for {ver}{}", if ready_s >= 0 { format!(", an engine answered after {ready_s} s") } else { ", no engine answered inside its window".to_string() }));
                }
                if !ok && deferred {
                    // the installer never ran (nobody answered the administrator prompt): not a failure, it tries again
                    shared.log(&format!("OTA: the update to {ver} was deferred before this start ({err}); it tries again in this machine's slot"));
                    let _ = std::fs::remove_file(self.pending_path());
                } else if !ok {
                    let mut st = shared.state.lock().unwrap();
                    st.update.error = err.clone();
                    st.update.status = "error".into();
                    if rolled_back {
                        st.update.rolled_back = format!("{ver}: {err}");
                    }
                    drop(st);
                    shared.event("error", &format!("update to {ver} failed: {err}"));
                    let _ = std::fs::remove_file(self.pending_path());
                    self.remember_failed(shared, &ver);
                    return;
                }
            }
        }
        let Some(mut p) = pending else { return };
        if p.to == self.current {
            // we are the new version
            p.starts += 1;
            self.write_pending(&p);
            if p.starts == 1 {
                shared.event("ok", &format!("updated to Ember {} from {}", p.to, p.from));
                shared.state.lock().unwrap().update.updated_from = p.from.clone();
            } else {
                shared.log(&format!("start {} of {} since the update from {}; healthy after {HEALTHY_AFTER_S} s", p.starts, p.to, p.from));
            }
            self.pending = Some(p);
        } else if p.from == self.current {
            // the old version runs again: the helper restored it, or the installer never ran
            shared.event("error", &format!("the update to {} did not take; still on {}", p.to, p.from));
            let _ = std::fs::remove_file(self.pending_path());
            self.remember_failed(shared, &p.to);
        } else {
            let _ = std::fs::remove_file(self.pending_path());
        }
    }

    /// True when this (new) version has died twice before reaching HEALTHY_AFTER_S: the engine rolls back and exits.
    pub fn needs_rollback(&self) -> bool {
        self.pending.as_ref().map(|p| p.starts >= 3).unwrap_or(false)
    }

    /// The version this engine replaced, on the first start after an update (MF-11: the read-back line the engine logs
    /// as its first act, "app <version> up after the update from <from>"); None on any other start.
    pub fn updated_from(&self) -> Option<String> {
        self.pending.as_ref().filter(|p| p.starts == 1 && p.to == self.current).map(|p| p.from.clone())
    }

    // ---- state for the dashboard -------------------------------------------------------------------------------------

    fn publish(&self, shared: &Arc<Shared>) {
        let mut st = shared.state.lock().unwrap();
        let u = &mut st.update;
        u.auto = self.auto;
        if let Some(m) = &self.manifest {
            u.version = m.version.clone();
            u.notes = m.notes.clone();
            u.channel = m.channel.clone();
            u.published_at = m.published_at.clone();
            u.activation_height = m.activation_height.unwrap_or(0);
            u.unsupported = manifest::unsupported(m, &self.current);
            u.min_supported = m.min_supported_version.clone();
        }
        if let Some(e) = &self.entry {
            u.url = e.url.clone();
            u.size = e.size;
        }
        u.available = self.entry.is_some();
        u.downloaded = self.file.is_some();
        u.ready = self.staged.is_some();
        u.file = self.file.as_ref().map(|p| p.display().to_string()).unwrap_or_default();
        if u.status != "applying" && u.status != "manual" && u.status != "error" && u.status != "deferred" {
            u.status = if self.staged.is_some() {
                "ready".into()
            } else if self.file.is_some() {
                "staging".into()
            } else if self.entry.is_some() {
                if self.busy { "downloading".into() } else { "available".into() }
            } else if self.manifest.is_some() {
                "current".into()
            } else if u.status.is_empty() {
                "unknown".into()
            } else {
                u.status.clone()
            };
        }
    }

    fn set_error(&mut self, shared: &Arc<Shared>, e: &str) {
        shared.log(&format!("update: {e}"));
        let mut st = shared.state.lock().unwrap();
        st.update.error = e.to_string();
        st.update.status = "error".into();
        st.update.applying = false;
        st.update.wait = String::new();
    }

    fn clear_error(&self, shared: &Arc<Shared>) {
        let mut st = shared.state.lock().unwrap();
        st.update.error = String::new();
        if st.update.status == "error" {
            st.update.status = "unknown".into();
        }
    }

    pub fn set_auto(&mut self, shared: &Arc<Shared>, on: bool) {
        self.auto = on;
        shared.settings.lock().unwrap().auto_update = on;
        shared.settings.lock().unwrap().save(&shared.settings_path);
        shared.state.lock().unwrap().settings.auto_update = on;
        shared.event("info", if on { "updates install by themselves at the next safe moment" } else { "updates download but wait for Install now" });
        self.publish(shared);
    }

    // ---- the tick ----------------------------------------------------------------------------------------------------

    pub fn tick(&mut self, shared: &Arc<Shared>, ctx: &Ctx) -> Option<Action> {
        let now = Instant::now();
        // the new version is healthy once it has run this long: the update is complete, the leftovers can go
        if !self.healthy_marked && self.pending.is_some() && now.duration_since(self.started) >= Duration::from_secs(HEALTHY_AFTER_S) {
            self.healthy_marked = true;
            let p = self.pending.take().unwrap();
            let _ = std::fs::remove_file(self.pending_path());
            let _ = std::fs::remove_file(self.result_path()); // the helper's "ok" lands after this engine started
            shared.log(&format!("update to {} complete (from {}); keeping the previous version for a rollback", p.to, p.from));
            self.tidy(&p);
        }
        if now >= self.next_check && !self.busy && self.staged.is_none() {
            self.start_check(shared);
        }
        if self.busy {
            // download progress from the .part file
            if let Some(e) = &self.entry {
                if self.file.is_none() {
                    let part = self.dir.join(format!("{}.part", file_name(&e.url)));
                    let have = std::fs::metadata(&part).map(|m| m.len()).unwrap_or(0);
                    let mut st = shared.state.lock().unwrap();
                    if st.update.status == "downloading" && e.size > 0 {
                        st.update.progress = (have as f64 / e.size as f64).min(1.0);
                    }
                }
            }
            return None;
        }
        let urgent = self.urgent(ctx);
        {
            let mut st = shared.state.lock().unwrap();
            if st.update.urgent != urgent {
                st.update.urgent = urgent;
            }
            st.update.urgent_text = if !urgent {
                String::new()
            } else if let Some(m) = &self.manifest {
                if manifest::unsupported(m, &self.current) {
                    format!("Ember {} is no longer supported; the network needs {} or newer.", self.current, m.min_supported_version)
                } else {
                    let h = m.activation_height.unwrap_or(0);
                    format!("Consensus upgrade at height {h}{}: the node is {} blocks away. Installing {} now.", if m.deadline_note.is_empty() { String::new() } else { format!(" ({})", m.deadline_note) }, h.saturating_sub(ctx.daa), m.version)
                }
            } else {
                String::new()
            };
        }
        if self.staged.is_none() {
            return None;
        }
        if now.duration_since(self.last_safe_check) < Duration::from_secs(3) {
            return None;
        }
        self.last_safe_check = now;
        // Windows: the installer was started with the engine still mining; it stops us when it may run. Until then
        // watch for the helper's verdict (an unanswered administrator prompt), never the other way round.
        if let Some(t) = self.apply_launched {
            match self.read_result() {
                Some((false, err, _)) => {
                    let _ = std::fs::remove_file(self.result_path());
                    self.defer(shared, &err);
                }
                Some((true, ..)) => {} // the installer is in: it stops this engine any moment now
                None if now.duration_since(t) >= Duration::from_secs(15 * 60) => self.defer(shared, "no answer from the installer in 15 minutes"),
                None => {}
            }
            return None;
        }
        if let Some(u) = self.deferred_until {
            if now < u && !self.install_asked {
                return None;
            }
            self.deferred_until = None;
            shared.state.lock().unwrap().update.status = "ready".into();
        }
        // the network guard: /api/live every 60 s while an update waits
        if !self.live_api.is_empty() && !self.live_busy && now >= self.live_next {
            self.live_next = now + Duration::from_secs(60);
            self.live_busy = true;
            let url = self.live_api.clone();
            let shared2 = shared.clone();
            std::thread::spawn(move || shared2.send(Cmd::Ota(Event::Live(fetch_live_identities(&url)))));
        }
        self.live_samples.retain(|(t, _)| now.duration_since(*t) <= Duration::from_secs(600));
        let network_drop_pct = {
            let max = self.live_samples.iter().map(|(_, n)| *n).max().unwrap_or(0);
            match self.live_samples.last() {
                Some((_, cur)) if max > 0 && self.live_samples.len() >= 2 => (max.saturating_sub(*cur)) as f64 * 100.0 / max as f64,
                _ => 0.0,
            }
        };
        let catch_up = self.manifest.as_ref().and_then(|m| manifest::unix_from_rfc3339(&m.published_at)).map(|p| p + CATCH_UP_AFTER_S <= self.started_unix).unwrap_or(false);
        if catch_up && !self.catch_up_logged {
            self.catch_up_logged = true;
            shared.log(&format!("update: {} was published over an hour before this start, so it installs at the first safe moment (no turn to wait for)", self.version()));
        }
        let ready_for = self.ready_since.map(|t| now.duration_since(t).as_secs()).unwrap_or(0);
        let slot_ok = ready_for >= self.slot || catch_up || std::env::var("IGNEUM_APP_UPDATE_NO_SLOT").map(|v| v == "1").unwrap_or(false);
        if ctx.node_synced {
            self.synced_since.get_or_insert(now);
        } else {
            self.synced_since = None;
        }
        let synced_for = self.synced_since.map(|t| now.duration_since(t).as_secs()).unwrap_or(0);
        let manifest_urgent = self.manifest.as_ref().map(|m| m.urgent).unwrap_or(false);
        let moment = Moment { node_synced: ctx.node_synced, boundary_eta_s: ctx.boundary_eta_s, miner_busy: ctx.miner_busy, job_active: ctx.job_active, ready_for_s: ready_for, urgent: urgent || self.install_asked, slot_ok, network_drop_pct , auto_update_off: !self.auto, install_asked: self.install_asked, finality_paused: ctx.finality_paused, manifest_urgent, miner_idle: ctx.miner_idle, synced_for_s: synced_for };
        if !self.auto && !self.install_asked {
            // F14: off stays off, urgent or not; an unsupported version pauses mining (the engine reads update.hold_mining)
            let mut st = shared.state.lock().unwrap();
            st.update.wait = if urgent { "automatic updates are off: this version waits for Install now; mining is paused while it is unsupported".into() } else { "waiting for Install now (automatic updates are off)".into() };
            st.update.hold_mining = st.update.unsupported;
            return None;
        }
        let v = self.version();
        if self.failed_versions.iter().any(|f| f == &v) && !self.install_asked {
            shared.state.lock().unwrap().update.wait = format!("{v} failed to install before; it waits for Install now");
            return None;
        }
        match manifest::safe_to_apply(&moment) {
            Ok(()) => Some(Action::Apply),
            Err(why) => {
                let why = if why.contains("machine's turn") { format!("{why}: {} s after it is ready", self.slot) } else { why };
                shared.state.lock().unwrap().update.wait = why;
                None
            }
        }
    }

    /// The helper's verdict file: (ok, error, deferred).
    fn read_result(&self) -> Option<(bool, String, bool)> {
        let v: Value = serde_json::from_str(&std::fs::read_to_string(self.result_path()).ok()?).ok()?;
        Some((
            v.get("ok").and_then(|x| x.as_bool()).unwrap_or(false),
            v.get("error").and_then(|x| x.as_str()).unwrap_or("").to_string(),
            v.get("deferred").and_then(|x| x.as_bool()).unwrap_or(false),
        ))
    }

    /// Windows: the installer could not run (the administrator prompt was declined, timed out, or nobody was there).
    /// The engine never stopped, so mining goes on; the update waits for Install now, the next start, or 6 hours.
    fn defer(&mut self, shared: &Arc<Shared>, err: &str) {
        self.apply_launched = None;
        self.install_asked = false;
        self.deferred_until = Some(Instant::now() + Duration::from_secs(6 * 3600));
        let _ = std::fs::remove_file(self.pending_path());
        let v = self.version();
        {
            let mut st = shared.state.lock().unwrap();
            st.update.applying = false;
            st.update.status = "deferred".into();
            st.update.wait = "waits for the next time someone is at this PC (Windows asks for permission); mining continues".into();
        }
        shared.event("info", &format!("OTA: administrator approval not given for Ember {v} ({err}); the update waits for the next time someone is at this PC; mining continues"));
    }

    fn urgent(&self, ctx: &Ctx) -> bool {
        let Some(m) = &self.manifest else { return false };
        if self.entry.is_none() {
            return false;
        }
        manifest::unsupported(m, &self.current) || manifest::fork_is_close(m.activation_height, ctx.daa)
    }

    /// After a completed update: the downloads of older versions go; the installer of the version now running stays
    /// on Windows (the rollback target of the next update); a failed bundle from an earlier rollback goes on macOS.
    fn tidy(&self, _p: &Pending) {
        if let Ok(rd) = std::fs::read_dir(&self.dir) {
            for e in rd.flatten() {
                let name = e.file_name().to_string_lossy().into_owned();
                let keep = cfg!(windows) && name.contains(&self.current) && name.ends_with(".exe");
                if !keep && name != "manifest.json" && name != "manifest.json.sig" {
                    let _ = std::fs::remove_file(e.path());
                }
            }
        }
        if let Some(b) = crate::platform::bundle_path() {
            let failed = PathBuf::from(format!("{}.failed", b.display()));
            if failed.exists() {
                let _ = std::fs::remove_dir_all(&failed);
            }
        }
    }

    // ---- check -------------------------------------------------------------------------------------------------------

    pub fn check_now(&mut self, shared: &Arc<Shared>) {
        if self.busy {
            return;
        }
        self.clear_error(shared);
        self.start_check(shared);
    }

    fn start_check(&mut self, shared: &Arc<Shared>) {
        let every = std::env::var("IGNEUM_APP_UPDATE_CHECK_SECS").ok().and_then(|v| v.parse().ok()).unwrap_or(CHECK_EVERY_S);
        self.next_check = Instant::now() + Duration::from_secs(every + self.jitter % (every / 6 + 1));
        // a bundle run from its mounted disk image has no writable place to update (the project's read of the founder's
        // Mac, 16:4x UK, 12 October 2026): the plain words, no fetch
        if on_disk_image() {
            let mut st = shared.state.lock().unwrap();
            st.update.error = "Ember is running from its disk image; move it to Applications to update".into();
            st.update.status = "error".into();
            return;
        }
        if self.manifest_url.is_empty() {
            let mut st = shared.state.lock().unwrap();
            st.update.error = "no update manifest configured in this build".into();
            st.update.status = "error".into();
            st.update.checked_at = crate::platform::unix_now_f();
            return;
        }
        self.busy = true;
        shared.state.lock().unwrap().update.status = "checking".into();
        let url = self.manifest_url.clone();
        let dir = self.dir.clone();
        let shared2 = shared.clone();
        std::thread::spawn(move || {
            let r = fetch_manifest(&url, &dir);
            shared2.send(Cmd::Ota(Event::Checked(r)));
        });
    }

    pub fn event(&mut self, shared: &Arc<Shared>, ev: Event) {
        self.busy = false;
        match ev {
            Event::Checked(r) => {
                shared.state.lock().unwrap().update.checked_at = crate::platform::unix_now_f();
                match r {
                    Err(e) => {
                        self.next_check = Instant::now() + Duration::from_secs(RETRY_AFTER_ERROR_S);
                        if self.manifest.is_none() {
                            // a check that could not be made (the store dark, no network, a refused manifest) is not
                            // a failed update: the window says the check is tried again (the founder's "update
                            // failed" of 12 October 2026 was this class, read on the MacBook); status "retrying"
                            shared.log(&format!("update check: {e}; trying again in {} s", RETRY_AFTER_ERROR_S));
                            let mut st = shared.state.lock().unwrap();
                            st.update.error = e.clone();
                            st.update.status = "retrying".into();
                            st.update.retry_s = RETRY_AFTER_ERROR_S;
                            st.update.applying = false;
                            st.update.wait = String::new();
                        } else {
                            shared.log(&format!("update check: {e}; keeping the last manifest"));
                        }
                    }
                    Ok(m) => {
                        self.clear_error(shared);
                        shared.log(&format!("update check: manifest {} verified, signed by {}", m.version, m.signer));
                        let entry = if manifest::newer(&m.version, &self.current) { m.this_platform().cloned() } else { None };
                        let changed = self.entry != entry;
                        if entry.is_none() {
                            if manifest::newer(&m.version, &self.current) {
                                shared.log(&format!("update check: {} is published but has no {} build yet", m.version, manifest::platform_name()));
                            } else {
                                shared.log(&format!("update check: {} is current (manifest {})", self.current, m.version));
                                // an asked install (update-now) covers this one check: nothing newer, so the ask is spent.
                                // Left true it made the NEXT manifest urgent hours later (the first Windows machine, 6 October 2026, 17:52:54Z).
                                if self.install_asked {
                                    self.install_asked = false;
                                    shared.log("update check: Install now asked and nothing newer is published; the ask is spent (the next manifest takes the usual slot)");
                                }
                            }
                            self.entry = None;
                            self.file = None;
                            self.staged = None;
                            self.ready_since = None;
                        }
                        self.manifest = Some(m.clone());
                        self.min_supported = m.min_supported_version.clone();
                        self.write_override(shared, &m);
                        self.write_tuning(shared, &m);
                        self.write_drivers(shared, &m);
                        self.ui_change = Some(m.ui.clone());
                        if let Some(e) = entry {
                            if changed {
                                self.file = None;
                                self.staged = None;
                                self.ready_since = None;
                                shared.event("info", &format!("Ember {} is available: downloading ({} MB){}", m.version, e.size / 1_000_000, if m.notes.is_empty() { String::new() } else { format!(". {}", m.notes) }));
                            }
                            self.entry = Some(e);
                            if self.staged.is_none() {
                                self.start_download(shared);
                            }
                        }
                        self.publish(shared);
                    }
                }
            }
            Event::Downloaded(r) => match r {
                Err(e) => {
                    self.set_error(shared, &format!("download failed: {e}"));
                    self.next_check = Instant::now() + Duration::from_secs(RETRY_AFTER_ERROR_S);
                }
                Ok(p) => {
                    self.clear_error(shared);
                    shared.log(&format!("update: {} downloaded and verified", p.display()));
                    self.file = Some(p.clone());
                    self.publish(shared);
                    self.start_stage(shared, p);
                }
            },
            Event::Live(n) => {
                self.live_busy = false;
                if let Some(n) = n {
                    self.live_samples.push((Instant::now(), n));
                }
                return;
            }
            Event::Staged(r) => match r {
                Err(e) if e.starts_with("manual:") => {
                    let mut st = shared.state.lock().unwrap();
                    st.update.status = "manual".into();
                    st.update.wait = e.trim_start_matches("manual:").trim().to_string();
                    drop(st);
                    shared.event("info", &format!("update downloaded; {}", e.trim_start_matches("manual:").trim()));
                }
                Err(e) => {
                    self.set_error(shared, &format!("could not prepare the update: {e}"));
                    self.file = None;
                    self.next_check = Instant::now() + Duration::from_secs(RETRY_AFTER_ERROR_S);
                }
                Ok(p) => {
                    self.clear_error(shared);
                    #[cfg(target_os = "macos")]
                    {
                        self.staged_digest = manifest::digest_dir(&p).unwrap_or_default();
                        shared.log(&format!("update: staged bundle digest {}", self.staged_digest));
                    }
                    self.staged = Some(p);
                    self.ready_since = Some(Instant::now());
                    let v = self.manifest.as_ref().map(|m| m.version.clone()).unwrap_or_default();
                    {
                        let mut st = shared.state.lock().unwrap();
                        st.update.wait = if self.auto { "installs at the next safe moment".into() } else { "waiting for Install now".into() };
                        st.update.progress = 1.0;
                    }
                    shared.event("ok", &format!("Ember {v} is ready; {}", if self.auto { "it installs at the next safe moment" } else { "automatic updates are off, so it waits for Install now" }));
                    self.publish(shared);
                }
            },
        }
        self.publish(shared);
    }

    fn start_download(&mut self, shared: &Arc<Shared>) {
        let Some(e) = self.entry.clone() else { return };
        self.busy = true;
        {
            let mut st = shared.state.lock().unwrap();
            st.update.status = "downloading".into();
            st.update.progress = 0.0;
        }
        let dir = self.dir.clone();
        let shared2 = shared.clone();
        std::thread::spawn(move || {
            let r = download(&e, &dir);
            shared2.send(Cmd::Ota(Event::Downloaded(r)));
        });
    }

    fn start_stage(&mut self, shared: &Arc<Shared>, file: PathBuf) {
        let Some(e) = self.entry.clone() else { return };
        let version = self.manifest.as_ref().map(|m| m.version.clone()).unwrap_or_default();
        self.busy = true;
        shared.state.lock().unwrap().update.status = "staging".into();
        let dir = self.dir.clone();
        let shared2 = shared.clone();
        std::thread::spawn(move || {
            let r = stage(&e, &file, &dir, &version);
            shared2.send(Cmd::Ota(Event::Staged(r)));
        });
    }

    // ---- the user's buttons --------------------------------------------------------------------------------------------

    /// Install now: a ready update applies at once; a downloaded one as soon as it is staged; else a check runs.
    pub fn install_now(&mut self, shared: &Arc<Shared>) {
        self.install_asked = true;
        self.deferred_until = None;
        self.last_safe_check = Instant::now() - Duration::from_secs(10);
        if self.apply_launched.is_some() {
            return; // the installer is already up (its prompt may be waiting on the screen)
        }
        if self.staged.is_some() {
            shared.state.lock().unwrap().update.wait = "installing now".into();
            return;
        }
        if self.busy {
            return;
        }
        self.clear_error(shared);
        if let Some(f) = self.file.clone() {
            self.start_stage(shared, f);
        } else if self.entry.is_some() {
            self.start_download(shared);
        } else {
            self.start_check(shared);
        }
    }

    /// The manual path: open the downloaded disk image or installer for the user.
    pub fn open_file(&self) -> Result<(), String> {
        let f = self.file.as_ref().ok_or("nothing downloaded yet")?;
        #[cfg(target_os = "macos")]
        let r = Command::new(crate::platform::tool("open")).arg(f).spawn();
        #[cfg(windows)]
        let r = crate::platform::quiet(&mut Command::new(crate::platform::tool("cmd"))).args(["/c", "start", "", &f.display().to_string()]).spawn();
        #[cfg(not(any(target_os = "macos", windows)))]
        let r = Command::new("xdg-open").arg(f).spawn();
        r.map(|_| ()).map_err(|e| e.to_string())
    }

    // ---- apply -------------------------------------------------------------------------------------------------------

    pub fn version(&self) -> String {
        self.manifest.as_ref().map(|m| m.version.clone()).unwrap_or_default()
    }

    /// The engine's own IGNEUM_APP_* environment (a test on a private devnet) for the helper's relaunch; "" when
    /// there is none, and the helper opens the bundle through LaunchServices.
    #[cfg(target_os = "macos")]
    fn write_env_file(&self) -> String {
        let vars: Vec<String> = std::env::vars().filter(|(k, _)| k.starts_with("IGNEUM_APP_")).map(|(k, v)| format!("{k}={v}")).collect();
        if vars.is_empty() {
            return String::new();
        }
        let p = self.app_dir.join("ota-relaunch.env");
        if std::fs::write(&p, vars.join("\n") + "\n").is_ok() { p.display().to_string() } else { String::new() }
    }

    /// Writes update-pending.json and the helper, starts the helper detached. macOS: the engine exits right after
    /// (Launch::QuitNow). Windows: the installer runs first while the engine keeps mining and stops the engine itself
    /// once it may run (Launch::InstallerRunning); an unanswered administrator prompt never strands the machine
    /// (4 October 2026: two unattended PCs sat stopped at a prompt for an hour). `host_pid` is the window host.
    pub fn launch_apply(&mut self, shared: &Arc<Shared>, host_pid: u32) -> Result<Launch, String> {
        let staged = self.staged.clone().ok_or("no update is ready")?;
        let to = self.version();
        // R4.3.5: what is about to be swapped in is re-verified now, not only when it was downloaded
        let entry = self.entry.clone().ok_or("no manifest entry")?;
        if let Some(f) = &self.file {
            let sum = manifest::sha256_file(f).map_err(|e| format!("cannot hash the download: {e}"))?;
            if sum != entry.sha256 {
                self.staged = None;
                self.file = None;
                return Err("the downloaded file no longer matches the manifest's sha256; it is discarded".into());
            }
        }
        #[cfg(target_os = "macos")]
        {
            let d = manifest::digest_dir(&staged).map_err(|e| format!("cannot digest the staged app: {e}"))?;
            if d != self.staged_digest || d.is_empty() {
                let _ = std::fs::remove_dir_all(&staged);
                self.staged = None;
                return Err("the staged app changed since it was verified; it is discarded".into());
            }
        }
        let previous_installer = if cfg!(windows) { self.dir.join(installer_name_for(&self.current)).to_string_lossy().into_owned() } else { String::new() };
        let previous_installer = if Path::new(&previous_installer).is_file() { previous_installer } else { String::new() };
        self.write_pending(&Pending { from: self.current.clone(), to: to.clone(), at: crate::platform::unix_now_f(), starts: 0, previous_installer });
        let _ = std::fs::remove_file(self.result_path());
        let result = self.result_path();
        #[cfg(target_os = "macos")]
        {
            let app = crate::platform::bundle_path().ok_or("not running from Ember.app")?;
            let script = self.app_dir.join("ota-apply.sh");
            std::fs::write(&script, MAC_HELPER).map_err(|e| format!("cannot write the helper: {e}"))?;
            let env_file = self.write_env_file();
            let args = ["apply".to_string(), std::process::id().to_string(), host_pid.to_string(), app.display().to_string(), staged.display().to_string(), to.clone(), result.display().to_string(), env_file, self.staged_digest.clone()];
            shared.log(&format!("update: starting the helper: bash {} {}", script.display(), args.join(" ")));
            spawn_detached(Command::new(crate::platform::tool("nohup")).arg(crate::platform::tool("bash")).arg(&script).args(&args))?;
            Ok(Launch::QuitNow)
        }
        #[cfg(windows)]
        {
            let _ = host_pid;
            let install_dir = std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.to_path_buf())).ok_or("cannot find the install folder")?;
            let script = self.app_dir.join("ota-apply.ps1");
            std::fs::write(&script, WIN_HELPER).map_err(|e| format!("cannot write the helper: {e}"))?;
            let mut c = Command::new(crate::platform::tool("powershell"));
            c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File"]).arg(&script).args([
                "-Mode", "apply", "-EnginePid", &std::process::id().to_string(), "-Installer", &staged.display().to_string(), "-Version", &to, "-Result", &result.display().to_string(), "-InstallDir", &install_dir.display().to_string(), "-Sha256", &entry.sha256,
            ]);
            // an earlier release (MF-11): the helper owns the return. It keeps the exe set beside the app, launches the app itself after
            // the installer, polls the new engine's api/state, restores the kept set when nothing answers, and posts one line
            // to the intake either way (the key it needs is in igneum-app.json beside the exe; nothing on the command line).
            c.args(["-Previous", &previous_dir_for(&install_dir).display().to_string(), "-Machine", &shared.runtime.id8(), "-Intake", &shared.packaged.upload_url, "-AppDir", &self.app_dir.display().to_string()]);
            let per_user = !under_program_files(&install_dir);
            shared.log(&format!("update: starting the installer first, the miners keep running: {} ({})", script.display(), if per_user { "per-user install, no administrator prompt" } else { "install in Program Files: Windows asks for administrator approval" }));
            if !per_user {
                shared.log(&format!("OTA: waiting for administrator approval for Ember {to}; mining continues until it is given"));
            }
            spawn_detached(&mut c)?;
            self.apply_launched = Some(Instant::now());
            Ok(Launch::InstallerRunning)
        }
        #[cfg(not(any(target_os = "macos", windows)))]
        {
            let _ = (shared, host_pid, staged, result);
            Err("automatic apply is not supported on this platform".into())
        }
    }

    /// The new version failed to start twice: restore the previous one through the helper and exit.
    pub fn launch_rollback(&mut self, shared: &Arc<Shared>, host_pid: u32) -> Result<(), String> {
        let p = self.pending.clone().ok_or("no update pending")?;
        // R4.3.6: never below the network's minimum; this version stays and is marked failed so it is not re-applied
        if !self.min_supported.is_empty() && manifest::newer(&self.min_supported, &p.from) {
            let _ = std::fs::remove_file(self.pending_path());
            self.pending = None;
            return Err(format!("not rolling back to {}: the network needs {} or newer; staying on {}", p.from, self.min_supported, p.to));
        }
        self.remember_failed(shared, &p.to);
        let result = self.result_path();
        shared.event("error", &format!("Ember {} did not stay up twice; restoring {}", p.to, p.from));
        #[cfg(target_os = "macos")]
        {
            let app = crate::platform::bundle_path().ok_or("not running from Ember.app")?;
            let script = self.app_dir.join("ota-apply.sh");
            std::fs::write(&script, MAC_HELPER).map_err(|e| format!("cannot write the helper: {e}"))?;
            let env_file = self.write_env_file();
            let args = ["rollback".to_string(), std::process::id().to_string(), host_pid.to_string(), app.display().to_string(), String::new(), p.to.clone(), result.display().to_string(), env_file, String::new()];
            spawn_detached(Command::new(crate::platform::tool("nohup")).arg(crate::platform::tool("bash")).arg(&script).args(&args))?;
            Ok(())
        }
        #[cfg(windows)]
        {
            let _ = host_pid;
            if p.previous_installer.is_empty() || !Path::new(&p.previous_installer).is_file() {
                return Err("no previous installer kept; reinstall from igneum.network".into());
            }
            let install_dir = std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.to_path_buf())).ok_or("cannot find the install folder")?;
            let script = self.app_dir.join("ota-apply.ps1");
            std::fs::write(&script, WIN_HELPER).map_err(|e| format!("cannot write the helper: {e}"))?;
            let sha = manifest::sha256_file(Path::new(&p.previous_installer)).unwrap_or_default();
            let mut c = Command::new(crate::platform::tool("powershell"));
            c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File"]).arg(&script).args([
                "-Sha256", &sha, "-Mode", "rollback", "-EnginePid", &std::process::id().to_string(), "-Installer", &p.previous_installer, "-Version", &p.to, "-Result", &result.display().to_string(), "-InstallDir", &install_dir.display().to_string(),
                "-Previous", &previous_dir_for(&install_dir).display().to_string(), "-Machine", &shared.runtime.id8(), "-Intake", &shared.packaged.upload_url, "-AppDir", &self.app_dir.display().to_string(),
            ]);
            spawn_detached(&mut c)?;
            Ok(())
        }
        #[cfg(not(any(target_os = "macos", windows)))]
        {
            let _ = (host_pid, result);
            Err("rollback is not supported on this platform".into())
        }
    }
}

// ---- the threads ---------------------------------------------------------------------------------------------------

/// https://igneum.network/live -> https://igneum.network/api/live; "" when the build carries no live page.
/// Whether this engine runs from a mounted disk image (macOS: a path under /Volumes/ that is not the startup volume).
pub fn on_disk_image() -> bool {
    let Some(exe) = std::env::current_exe().ok() else { return false };
    let s = exe.to_string_lossy();
    cfg!(target_os = "macos") && s.starts_with("/Volumes/")
}

pub fn live_api_from(live_page: &str) -> String {
    let Some(rest) = live_page.strip_prefix("https://") else { return String::new() };
    let host = rest.split('/').next().unwrap_or("");
    if host.is_empty() { String::new() } else { format!("https://{host}/api/live") }
}

/// The network's identity count over the last 10 minutes from /api/live (state.miners_10m).
fn fetch_live_identities(url: &str) -> Option<u64> {
    let out = crate::detect::run_timeout(Command::new(crate::platform::tool("curl")).args(["-fsS", "--max-time", "10", url]), None, Duration::from_secs(12))?;
    let v: Value = serde_json::from_str(out.trim()).ok()?;
    v.get("state")?.get("miners_10m")?.as_u64()
}

/// Windows: an install under Program Files was made by the administrator installer (an earlier release and earlier).
#[cfg(windows)]
fn under_program_files(dir: &Path) -> bool {
    let d = dir.to_string_lossy().to_ascii_lowercase();
    ["ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"].iter().filter_map(|k| std::env::var(k).ok()).any(|pf| !pf.is_empty() && d.starts_with(&pf.to_ascii_lowercase()))
}

/// Windows, per-user installs: the inbound firewall rule for igneumd.exe needs administrator approval once. Asked on
/// the first run only, in a thread; declined or unanswered, the node still dials out and mines (other nodes cannot
/// dial in), and it is never asked again. The administrator installer of an earlier release and earlier added the rule itself.
/// Windows: the boot task (src/boot.rs) registered at every engine start when it is missing, in a thread, as the
/// user's own task (no prompt). An account Windows refuses gets it in the one approved step with the Power Helper.
#[cfg(windows)]
fn boot_task_first_run(shared: &Arc<Shared>) {
    let Some(exe) = std::env::current_exe().ok() else { return };
    let exe = crate::boot::task_exe(&exe);
    if !exe.is_file() {
        return;
    }
    let root = crate::platform::fixed_data_root();
    let shared = shared.clone();
    std::thread::spawn(move || match crate::boot::ensure_registered(&exe, &root) {
        Ok(true) => shared.log(&format!("boot start: the task '{}' is registered: the engine starts at boot without a logon ({} --launch --data-root {})", crate::boot::TASK_NAME, exe.display(), root.display())),
        Ok(false) => {}
        Err(e) => shared.log(&format!("boot start: not registered ({e}); the app starts at logon only until Power control's one approved step registers it")),
    });
}

#[cfg(windows)]
fn firewall_first_run(shared: &Arc<Shared>) {
    let flag = shared.runtime.app_dir.join("firewall-rule.json");
    if flag.exists() {
        return;
    }
    // an earlier release: the rule is the installer's rights step (src/rights.rs); the app never prompts at runtime. A manifest that
    // holds the right ends it here; a manifest that lacks it (or none: an install before an earlier release) is one log line.
    if crate::rights::held(&shared.runtime.app_dir, "firewall-node") {
        let _ = std::fs::write(&flag, json!({ "source": "rights", "at": crate::platform::unix_now() }).to_string());
        return;
    }
    shared.log(&format!("firewall: inbound rule for igneumd.exe {}", crate::rights::missing_note("firewall-node")));
    return;
    #[allow(unreachable_code)]
    let Some(install_dir) = std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.to_path_buf())) else { return };
    if under_program_files(&install_dir) {
        let _ = std::fs::write(&flag, json!({ "source": "installer", "at": crate::platform::unix_now() }).to_string());
        return;
    }
    let node = install_dir.join("igneumd.exe");
    if !node.is_file() {
        return;
    }
    let script = shared.runtime.app_dir.join("firewall-rule.ps1");
    let text = format!(
        "$rule = 'advfirewall firewall add rule name=\"Ember node\" dir=in action=allow enable=yes profile=private,domain protocol=TCP program=\"{}\"'\n$p = Start-Process -FilePath netsh.exe -ArgumentList $rule -Verb RunAs -Wait -PassThru -WindowStyle Hidden\nexit $p.ExitCode\n",
        node.display()
    );
    if std::fs::write(&script, text).is_err() {
        return;
    }
    let shared = shared.clone();
    std::thread::spawn(move || {
        shared.log("firewall: asking once for administrator approval of the inbound rule for igneumd.exe (mining does not wait for it)");
        let mut c = Command::new(crate::platform::tool("powershell"));
        c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File"]).arg(&script);
        crate::platform::quiet(&mut c);
        let ok = c.status().map(|s| s.success()).unwrap_or(false);
        let _ = std::fs::write(&flag, json!({ "source": "first-run", "ok": ok, "at": crate::platform::unix_now() }).to_string());
        if ok {
            shared.log("firewall: inbound rule added for igneumd.exe");
        } else {
            shared.log("firewall: no inbound rule (administrator approval not given); the node dials out and mines without it, other nodes cannot dial in; not asked again");
        }
    });
}

fn file_name(url: &str) -> String {
    let name = url.rsplit('/').next().unwrap_or("update").split('?').next().unwrap_or("update");
    let clean: String = name.chars().filter(|c| c.is_ascii_alphanumeric() || *c == '.' || *c == '-' || *c == '_').collect();
    if clean.is_empty() { "update".into() } else { clean }
}

fn installer_name_for(version: &str) -> String {
    format!("Ember-Setup-{version}.exe")
}

/// Windows: the folder the helper keeps the running exe set in before the installer runs, beside the app
/// (`<install dir>.previous`, so `...\Programs\Ember.previous`). A rollback copies it back over the install folder.
#[allow(dead_code)]
fn previous_dir_for(install_dir: &Path) -> PathBuf {
    let name = install_dir.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_else(|| "Ember".into());
    install_dir.with_file_name(format!("{name}.previous"))
}

// ---- the return after a Windows install (MF-11, an earlier release) ------------------------------------------------------------
//
// the second Windows machine took the an earlier release update-now at 10:34Z on 7 October 2026 and was silent from 10:46Z. Until an earlier release the Windows helper's
// job ended when the installer exited 0: the installer's own [Run] entry relaunched the app, nobody checked that an engine
// answered, the window host never restarted an engine that died, and a second launch deferred to a surviving host through
// its single-instance mutex. The sequence below is what the helper does from the installer's exit on, written once here
// so a test can drive it with injected exit codes and answers, and mirrored line for line by WIN_HELPER's PowerShell.

/// How long the helper waits for an engine to answer api/state after a launch, and how often it asks.
pub const RETURN_READY_S: u64 = 120;
pub const RETURN_POLL_S: u64 = 3;
/// The installer's marker (packaging/windows/Ember.iss SetMarker): no relaunch while it is there and younger than
/// this; an older one is a stale marker (an installer that died) and is ignored with a FAULT line.
pub const INSTALL_MARKER: &str = "install-running.flag";
pub const INSTALL_MARKER_STALE_S: u64 = 15 * 60;

/// May the app be launched again now? false while an installer is running (its marker present and fresh). the second Windows machine,
/// 7 October 2026, 20:54 UK: a job's silent install quit the engine, the window host started the old engine 10 s later,
/// and every file stayed an earlier release because the host held them. `marker_age_s` is None when there is no marker.
pub fn relaunch_allowed(marker_age_s: Option<u64>) -> bool {
    match marker_age_s {
        None => true,
        Some(age) => age > INSTALL_MARKER_STALE_S,
    }
}

/// The marker's age in seconds under the app dir, None when absent.
pub fn install_marker_age(app_dir: &Path) -> Option<u64> {
    let m = std::fs::metadata(app_dir.join(INSTALL_MARKER)).ok()?;
    let t = m.modified().ok()?.duration_since(std::time::UNIX_EPOCH).ok()?.as_secs();
    Some(crate::platform::unix_now().saturating_sub(t))
}

/// One step of the helper after the installer exits.
#[derive(Debug, Clone, PartialEq)]
pub enum ReturnStep {
    /// start igneum-app.exe --launch from the install folder (a detached process; the helper outlives the old engine)
    Launch,
    /// poll app.url + api/state for RETURN_READY_S; `want` is the version that must answer ("" = any engine)
    WaitReady { want: String },
    /// quit through the API, then end what is left by name (the installer's own stop order)
    StopAll,
    /// copy the kept exe set (`<install dir>.previous`) back over the install folder
    RestorePrevious,
    /// the result file for the next engine and the one intake line
    Done { ok: bool, rolled_back: bool, fault: bool, how: &'static str },
}

/// What a WaitReady step saw: the version that answered, or nobody.
pub type Answer = Option<String>;

/// The helper's sequence from the installer's exit code on. `answers` is consulted once per WaitReady, in order (the test
/// injects them; the PowerShell asks the engine). `previous_kept` says whether the exe set was copied aside before the
/// installer ran.
pub fn return_sequence(installer_exit: i32, version: &str, previous_kept: bool, mut answers: impl FnMut(usize) -> Answer) -> Vec<ReturnStep> {
    let mut steps = vec![ReturnStep::Launch];
    let want = if installer_exit == 0 { version.to_string() } else { String::new() };
    steps.push(ReturnStep::WaitReady { want: want.clone() });
    match answers(0) {
        Some(v) if want.is_empty() || v == want => {
            steps.push(if installer_exit == 0 { ReturnStep::Done { ok: true, rolled_back: false, fault: false, how: "ok" } } else { ReturnStep::Done { ok: false, rolled_back: false, fault: true, how: "installer-failed" } });
            return steps;
        }
        _ => {}
    }
    // nothing (or the wrong engine) answered inside the window: back to the kept version
    steps.push(ReturnStep::StopAll);
    if previous_kept {
        steps.push(ReturnStep::RestorePrevious);
        steps.push(ReturnStep::Launch);
        steps.push(ReturnStep::WaitReady { want: String::new() });
        let how = if answers(1).is_some() { "rolled-back" } else { "rolled-back-silent" };
        steps.push(ReturnStep::Done { ok: false, rolled_back: true, fault: true, how });
    } else {
        steps.push(ReturnStep::Launch);
        steps.push(ReturnStep::Done { ok: false, rolled_back: false, fault: true, how: "relaunched" });
    }
    steps
}

/// The helper before an earlier release, for the record: the installer's exit code alone decided the result and nothing was asked
/// of the new engine (the known-failed shape of MF-11).
pub fn legacy_return_sequence(installer_exit: i32) -> Vec<ReturnStep> {
    if installer_exit == 0 { vec![ReturnStep::Done { ok: true, rolled_back: false, fault: false, how: "" }] } else { vec![ReturnStep::Launch, ReturnStep::Done { ok: false, rolled_back: false, fault: false, how: "" }] }
}

#[cfg(test)]
mod return_tests {
    use super::*;
    /// Known failed first (8 October 2026, an earlier release): the hourly check made a user wait up to an hour for an entry the
    /// fleet published (the an earlier release entry reached a machine after 14 minutes only because its api was poked).
    /// Ten minutes, the first check after start unchanged, the staged update still applied at the next safe moment.
    #[test]
    fn the_manifest_check_runs_every_ten_minutes() {
        assert_eq!(CHECK_EVERY_S, 240, "F-E52: the check every four minutes");
        assert_eq!(CHECK_EVERY_S / 6 + 1, 41, "the jitter window stays a sixth of the interval");
    }
    fn done(steps: &[ReturnStep]) -> &ReturnStep {
        steps.last().unwrap()
    }
    fn answers(list: &[Answer]) -> impl FnMut(usize) -> Answer + '_ {
        move |i| list.get(i).cloned().flatten()
    }

    /// Known-failed first: the helper before an earlier release reported ok on the installer's exit 0 with nobody answering.
    #[test]
    fn the_legacy_helper_reports_ok_with_no_engine_up() {
        let steps = legacy_return_sequence(0);
        assert_eq!(steps, vec![ReturnStep::Done { ok: true, rolled_back: false, fault: false, how: "" }]);
        assert!(!steps.iter().any(|s| matches!(s, ReturnStep::WaitReady { .. })), "nothing was asked of the new engine");
        assert!(!steps.iter().any(|s| matches!(s, ReturnStep::Launch)), "the launch was the installer's, not the helper's");
    }

    #[test]
    fn installer_ok_and_the_new_engine_answers_is_ok() {
        let steps = return_sequence(0, "0.1.21", true, answers(&[Some("0.1.21".into())]));
        assert_eq!(steps, vec![ReturnStep::Launch, ReturnStep::WaitReady { want: "0.1.21".into() }, ReturnStep::Done { ok: true, rolled_back: false, fault: false, how: "ok" }]);
    }

    #[test]
    fn installer_ok_and_nobody_answers_restores_the_previous_exe_set() {
        let steps = return_sequence(0, "0.1.21", true, answers(&[None, Some("0.1.20".into())]));
        assert_eq!(steps, vec![
            ReturnStep::Launch, ReturnStep::WaitReady { want: "0.1.21".into() }, ReturnStep::StopAll, ReturnStep::RestorePrevious, ReturnStep::Launch, ReturnStep::WaitReady { want: String::new() },
            ReturnStep::Done { ok: false, rolled_back: true, fault: true, how: "rolled-back" },
        ]);
    }

    #[test]
    fn the_old_engine_still_answering_after_exit_0_is_not_the_new_one() {
        // the installer said 0 but never replaced the running engine (files in use): the old version answers, the window
        // ends in a rollback to the kept set, which is the same version, and a FAULT line says so
        let steps = return_sequence(0, "0.1.21", true, answers(&[Some("0.1.20".into()), Some("0.1.20".into())]));
        assert!(matches!(done(&steps), ReturnStep::Done { ok: false, rolled_back: true, fault: true, .. }));
    }

    #[test]
    fn nobody_answers_twice_is_still_reported() {
        let steps = return_sequence(0, "0.1.21", true, answers(&[None, None]));
        assert_eq!(*done(&steps), ReturnStep::Done { ok: false, rolled_back: true, fault: true, how: "rolled-back-silent" });
    }

    #[test]
    fn without_a_kept_set_the_helper_relaunches_what_is_there_and_reports() {
        let steps = return_sequence(0, "0.1.21", false, answers(&[None]));
        assert_eq!(steps, vec![ReturnStep::Launch, ReturnStep::WaitReady { want: "0.1.21".into() }, ReturnStep::StopAll, ReturnStep::Launch, ReturnStep::Done { ok: false, rolled_back: false, fault: true, how: "relaunched" }]);
    }

    #[test]
    fn a_failed_installer_relaunches_the_old_version_and_reports_a_fault() {
        // exit 5 (cancelled) or 8 (files in use, a restart wanted): any engine answering is the old one, kept, with a FAULT line
        for code in [1, 5, 8] {
            let steps = return_sequence(code, "0.1.21", true, answers(&[Some("0.1.20".into())]));
            assert_eq!(steps, vec![ReturnStep::Launch, ReturnStep::WaitReady { want: String::new() }, ReturnStep::Done { ok: false, rolled_back: false, fault: true, how: "installer-failed" }], "exit {code}");
        }
        let steps = return_sequence(5, "0.1.21", true, answers(&[None, Some("0.1.20".into())]));
        assert!(matches!(done(&steps), ReturnStep::Done { ok: false, rolled_back: true, fault: true, .. }));
    }

    #[test]
    fn every_sequence_launches_before_it_waits_and_ends_in_a_done() {
        for code in [0, 1, 5, 8] {
            for kept in [true, false] {
                for a in [vec![None, None], vec![Some("0.1.21".to_string()), None], vec![None, Some("0.1.20".to_string())]] {
                    let steps = return_sequence(code, "0.1.21", kept, answers(&a));
                    assert_eq!(steps[0], ReturnStep::Launch);
                    assert!(matches!(steps[1], ReturnStep::WaitReady { .. }));
                    assert!(matches!(done(&steps), ReturnStep::Done { .. }));
                    let fault = matches!(done(&steps), ReturnStep::Done { fault: true, .. });
                    let ok = matches!(done(&steps), ReturnStep::Done { ok: true, .. });
                    assert!(ok != fault, "a result is ok or a fault, never neither: {steps:?}");
                }
            }
        }
    }

    /// an earlier release: an update applied with nobody logged on (the boot engine) returns headless: the helper's Launch runs
    /// `igneum-app.exe --launch`, which with no interactive session runs the engine itself, so the same sequence returns
    /// the app without a logon (the known-failed form first: before an earlier release that launch opened the window host, which has
    /// no desktop in session 0, and nothing mined until a logon)
    #[test]
    fn after_an_update_with_no_logon_the_relaunch_is_headless() {
        assert_eq!(crate::boot::legacy_launch_mode(true), crate::boot::LaunchMode::Host);
        assert_eq!(crate::boot::launch_mode(None, true), crate::boot::LaunchMode::Headless);
        let steps = return_sequence(0, "0.1.22", true, |_| Some("0.1.22".into()));
        assert_eq!(steps[0], ReturnStep::Launch, "the helper's launch is the same line; the session decides what it runs");
        assert!(matches!(steps.last().unwrap(), ReturnStep::Done { ok: true, .. }));
    }

    /// Tonight's shape on the second Windows machine (7 October 2026, 20:54 UK), known-failed first: an installer running, the engine gone, and the
    /// relaunch went ahead; an earlier release holds while the marker is there and resumes when it clears
    #[test]
    fn no_relaunch_while_an_installer_runs_and_a_relaunch_when_it_clears() {
        assert!(relaunch_allowed(None), "the old rule in effect: nothing held the relaunch (no marker existed)");
        assert!(!relaunch_allowed(Some(0)), "an installer just wrote its marker: hold");
        assert!(!relaunch_allowed(Some(600)), "ten minutes into a slow install: still held");
        assert!(relaunch_allowed(Some(INSTALL_MARKER_STALE_S + 1)), "a marker an installer left behind when it died: ignored");
        assert!(relaunch_allowed(None), "the marker cleared at the installer's end: relaunch");
        let h = WIN_HELPER;
        let wait = h.find("function WaitInstallerClear()").expect("the helper waits");
        let launch = h.find("function Launch()").unwrap();
        assert!(wait < launch && h[launch..].contains("WaitInstallerClear"), "every launch of the helper waits for the installer first");
        assert!(h.contains("install-running.flag") && h.contains("-gt 900"), "the same marker and the same stale rule as relaunch_allowed");
        // the installer writes and clears it, and the host holds its restart on it
        let iss = include_str!("../../../packaging/windows/Ember.iss");
        // the clear step in either shape: the one-line `if CurStep = ssDone then ClearMarker`, or (install-detach-25) the
        // block `if CurStep = ssDone then begin ... ClearMarker` within the next lines
        let clear_at_done = iss.find("if CurStep = ssDone then").map_or(false, |i| iss[i..].lines().take(6).any(|l| l.contains("ClearMarker")));
        assert!(iss.contains("install-running.flag") && iss.contains("SetMarker;") && clear_at_done && iss.contains("SetupMutex=EmberSetup"));
        let host = include_str!("../../windows/host.cpp");
        assert!(host.contains("install-running.flag") && host.contains("installerRunning()"), "the host's restart ladder holds while the marker is there");
    }

    /// The PowerShell mirrors the sequence: every step has its line, in order, and the installer is told the helper relaunches.
    #[test]
    fn the_windows_helper_carries_every_step() {
        let h = WIN_HELPER;
        let at = |s: &str| h.find(s).unwrap_or_else(|| panic!("WIN_HELPER lacks '{s}'"));
        assert!(at("/IGNOTA=2") > 0, "the installer must not relaunch (IGNOTA=1 is the old helpers' path)");
        assert!(h.contains(&format!("$ReadyS = {RETURN_READY_S}")) && h.contains(&format!("$PollS = {RETURN_POLL_S}")));
        let robocopy_keep = at("robocopy $InstallDir $Previous");
        let installer = at("Start-Process -FilePath $Installer"); // console: a test marker, not a spawn (the helper line above it carries the comment)
        let launch = at("function Launch()");
        let wait = at("function WaitReady(");
        let stop = at("function StopAll()");
        let restore = at("robocopy $Previous $InstallDir");
        let report = at("function Report(");
        assert!(launch < installer && wait < installer && stop < installer && report < installer, "the functions are defined before the installer runs");
        assert!(robocopy_keep < installer && restore < installer, "the keep and the restore are functions defined before the installer line");
        assert!(at("$kept = KeepPrevious") < installer, "the exe set is kept before the installer runs");
        assert!(at("Comeback $code $kept") > installer, "the return runs after the installer");
        for marker in ["'ok'", "'installer-failed'", "'rolled-back'", "'rolled-back-silent'", "'relaunched'"] {
            assert!(h.contains(marker), "the helper never writes return={marker}");
        }
        assert!(h.contains("FAULT update-return:"), "the fault line");
        assert!(h.contains("update-return: ok"), "the ok line");
        assert!(h.contains("api/state"), "readiness is the engine's own answer");
        assert!(!h.contains("-UploadKey"), "no key travels on a command line (R4.3.8)");
    }
}

pub(crate) fn curl(args: &[&str], limit: Duration) -> Result<(), String> {
    let mut c = Command::new(crate::platform::tool("curl"));
    c.args(args);
    let out = crate::detect::run_timeout(&mut c, None, limit).ok_or("curl is not available")?;
    // run_timeout folds stdout and stderr; with -sS only errors are printed
    let t = out.trim();
    if t.is_empty() { Ok(()) } else { Err(t.lines().last().unwrap_or("curl failed").to_string()) }
}

/// Fetches the manifest and its signature into <updates>/manifest.json(.sig), verifies, parses.
fn fetch_manifest(url: &str, dir: &Path) -> Result<Manifest, String> {
    let mf = dir.join("manifest.json.new");
    let sf = dir.join("manifest.json.sig.new");
    let _ = std::fs::remove_file(&mf);
    let _ = std::fs::remove_file(&sf);
    curl(&["-fsSL", "--max-time", "20", "-o", &mf.display().to_string(), url], Duration::from_secs(25)).map_err(|e| if e.contains("404") { "no manifest at the update URL yet".to_string() } else { format!("manifest: {e}") })?;
    curl(&["-fsSL", "--max-time", "20", "-o", &sf.display().to_string(), &format!("{url}.sig")], Duration::from_secs(25)).map_err(|e| format!("manifest signature: {e}"))?;
    let bytes = std::fs::read(&mf).map_err(|e| e.to_string())?;
    let sig = std::fs::read_to_string(&sf).map_err(|e| e.to_string())?;
    // F14: each build verifies against its own root (the release root on the public miner, the lab root on the lab
    // build) and reads its own channel; a manifest of the other channel is refused before anything is staged
    let mut m = manifest::verify_and_parse(&bytes, sig.trim(), crate::edition::ota_key())?;
    // the rotation list's supersession rule: a manifest written after its signer's mark is refused; the time judged
    // is written_utc alone (never published_at, the signer's own claim), and a manifest without it is refused
    if let Some(signer) = manifest::release_signer_of(sig.trim()) {
        let written = serde_json::from_slice::<Value>(&bytes).ok().and_then(|v| v.get("written_utc").and_then(|w| w.as_str()).map(|s| s.to_string())).unwrap_or_default();
        manifest::superseded_check(&signer, &written)?;
        m.signer = format!("{} of the compiled list, written {written}", signer.fingerprint);
    } else {
        m.signer = format!("the release root {}", manifest::fingerprint(crate::edition::ota_key()));
    }
    if !crate::edition::channel_accepted(&m.channel) {
        return Err(format!("the manifest is for channel '{}' and this is the {} build ({}); refused", m.channel, crate::edition::name(), crate::edition::channel()));
    }
    let _ = std::fs::rename(&mf, dir.join("manifest.json"));
    let _ = std::fs::rename(&sf, dir.join("manifest.json.sig"));
    Ok(m)
}

/// Downloads the platform's file with resume, checks size and sha256, renames .part to the final name.
fn download(e: &PlatformEntry, dir: &Path) -> Result<PathBuf, String> {
    let name = file_name(&e.url);
    let final_path = dir.join(&name);
    let part = dir.join(format!("{name}.part"));
    if final_path.is_file() && std::fs::metadata(&final_path).map(|m| m.len()).unwrap_or(0) == e.size && manifest::sha256_file(&final_path).map(|s| s == e.sha256).unwrap_or(false) {
        return Ok(final_path);
    }
    let _ = std::fs::remove_file(&final_path);
    let have = std::fs::metadata(&part).map(|m| m.len()).unwrap_or(0);
    if have > e.size {
        let _ = std::fs::remove_file(&part);
    }
    if have != e.size {
        // -C - resumes a partial file; --retry covers a dropped connection; 2 hours for a slow line
        curl(&["-fsSL", "--retry", "3", "--retry-delay", "5", "-C", "-", "--max-time", "7200", "-o", &part.display().to_string(), &e.url], Duration::from_secs(7260))?;
    }
    let got = std::fs::metadata(&part).map(|m| m.len()).unwrap_or(0);
    if got != e.size {
        let _ = std::fs::remove_file(&part);
        return Err(format!("size mismatch: got {got} bytes, the manifest says {}", e.size));
    }
    let sum = manifest::sha256_file(&part).map_err(|e| e.to_string())?;
    if sum != e.sha256 {
        let _ = std::fs::remove_file(&part);
        return Err("sha256 mismatch: the file is not what the manifest signed".into());
    }
    std::fs::rename(&part, &final_path).map_err(|e| e.to_string())?;
    Ok(final_path)
}

/// macOS: the new bundle next to the running one (same volume, so the swap is two renames); Windows: nothing to do.
#[allow(unused_variables)]
fn stage(e: &PlatformEntry, file: &Path, dir: &Path, version: &str) -> Result<PathBuf, String> {
    #[cfg(target_os = "macos")]
    {
        let app = crate::platform::bundle_path().ok_or("manual: the engine is not running from Ember.app; open the downloaded disk image and drag the app to Applications")?;
        let parent = app.parent().ok_or("no parent folder")?;
        let staged = parent.join(".Ember.app.new");
        let _ = std::fs::remove_dir_all(&staged);
        // writable? a user-owned /Applications is; a managed Mac may not be
        if std::fs::create_dir(&staged).is_err() {
            return Err(format!("manual: {} is not writable; open the downloaded disk image and drag the app over the old one", parent.display()));
        }
        let _ = std::fs::remove_dir(&staged);
        let work = dir.join("unpack");
        let _ = std::fs::remove_dir_all(&work);
        std::fs::create_dir_all(&work).map_err(|e| e.to_string())?;
        let source: PathBuf;
        let mut mounted: Option<PathBuf> = None;
        if e.kind == "dmg" {
            let mnt = work.join("mnt");
            std::fs::create_dir_all(&mnt).map_err(|e| e.to_string())?;
            let out = crate::detect::run_timeout(Command::new(crate::platform::tool("hdiutil")).args(["attach", "-nobrowse", "-readonly", "-noautoopen", "-noverify", "-mountpoint", &mnt.display().to_string(), &file.display().to_string()]), None, Duration::from_secs(120)).unwrap_or_default();
            if !mnt.join("Ember.app").is_dir() {
                return Err(format!("the disk image has no Ember.app ({})", out.lines().last().unwrap_or("hdiutil said nothing")));
            }
            mounted = Some(mnt.clone());
            source = mnt.join("Ember.app");
        } else {
            let out = crate::detect::run_timeout(Command::new(crate::platform::tool("ditto")).args(["-x", "-k", &file.display().to_string(), &work.display().to_string()]), None, Duration::from_secs(300)).unwrap_or_default();
            let found = find_app(&work).ok_or(format!("the zip has no Ember.app ({})", out.lines().last().unwrap_or("")))?;
            source = found;
        }
        let r = (|| -> Result<(), String> {
            let out = crate::detect::run_timeout(Command::new(crate::platform::tool("ditto")).arg(&source).arg(&staged), None, Duration::from_secs(300)).unwrap_or_default();
            if !staged.join("Contents/MacOS/igneum-app").is_file() {
                return Err(format!("copy failed: {}", out.lines().last().unwrap_or("")));
            }
            // the quarantine flag comes off only after the file this bundle came from verified again, now
            let again = manifest::sha256_file(file).map_err(|e| e.to_string())?;
            if again != e.sha256 {
                return Err("the download changed while it was being unpacked; discarded".into());
            }
            let _ = crate::platform::quiet(&mut Command::new(crate::platform::tool("xattr"))).args(["-dr", "com.apple.quarantine"]).arg(&staged).output();
            let v = crate::detect::run_timeout(Command::new(staged.join("Contents/MacOS/igneum-app")).arg("--version"), None, Duration::from_secs(20)).unwrap_or_default();
            let want = format!("igneum-app {version}");
            if v.trim() != want {
                return Err(format!("the new engine answers '{}' to --version, the manifest says {version}", v.trim()));
            }
            Ok(())
        })();
        if let Some(m) = mounted {
            let _ = crate::platform::quiet(&mut Command::new(crate::platform::tool("hdiutil"))).args(["detach", "-force", &m.display().to_string()]).output();
        }
        let _ = std::fs::remove_dir_all(&work);
        if let Err(err) = r {
            let _ = std::fs::remove_dir_all(&staged);
            return Err(err);
        }
        Ok(staged)
    }
    #[cfg(windows)]
    {
        if e.kind != "inno-setup" {
            return Err(format!("kind '{}' is not an installer", e.kind));
        }
        Ok(file.to_path_buf())
    }
    #[cfg(not(any(target_os = "macos", windows)))]
    {
        Err("manual: no automatic install on this platform".into())
    }
}

#[cfg(target_os = "macos")]
fn find_app(dir: &Path) -> Option<PathBuf> {
    let rd = std::fs::read_dir(dir).ok()?;
    for e in rd.flatten() {
        let p = e.path();
        if p.file_name().map(|n| n == "Ember.app").unwrap_or(false) && p.is_dir() {
            return Some(p);
        }
        if p.is_dir() {
            if let Some(f) = find_app(&p) {
                return Some(f);
            }
        }
    }
    None
}

/// Starts a process that outlives the engine (stdio closed, own session on unix, no window on Windows).
fn spawn_detached(c: &mut Command) -> Result<(), String> {
    use std::process::Stdio;
    c.stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null());
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        c.process_group(0);
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        // CREATE_NO_WINDOW only. With DETACHED_PROCESS as well (an earlier release to an earlier release) powershell.exe has no console to
        // hide and exits during start-up before the first line of ota-apply.ps1 runs: the first Windows update over
        // the air (an earlier release to an earlier release, 4 October 2026) sat on "the installer is starting" with nothing logged, while the
        // same helper launched by a remote job ran at once (docs/bugs.md). CREATE_NO_WINDOW gives it a hidden
        // console, and the child is not tied to this process's lifetime, so it still outlives the engine.
        c.creation_flags(0x0800_0000); // CREATE_NO_WINDOW
    }
    c.spawn().map(|_| ()).map_err(|e| format!("cannot start the helper: {e}"))
}

#[cfg(target_os = "macos")]
const MAC_HELPER: &str = r#"#!/bin/bash
# Ember update helper, written by the engine (src/ota.rs). Not for running by hand.
#   bash ota-apply.sh apply|rollback <engine pid> <host pid|0> <app bundle> <staged bundle> <version> <result json> [env file]
# apply: waits for the engine (it exits right after starting this), asks the window to quit, moves the running
# bundle to "<app>.previous" and the staged one in, opens the new app; if the new app does not start twice, puts the
# previous one back. rollback: the previous bundle back, the failed one aside. Writes <result json> for the engine.
MODE="$1"; EPID="$2"; HPID="$3"; APP="$4"; NEW="$5"; VER="$6"; RESULT="$7"; ENVF="${8:-}"; DIGEST="${9:-}"
LOG="$(dirname "$RESULT")/ota-apply.log"
exec >>"$LOG" 2>&1
echo "$(date -u +%FT%TZ) $MODE: engine $EPID host $HPID app '$APP' new '$NEW' version $VER"
gone() { ! kill -0 "$1" 2>/dev/null; }
wait_gone() { local p="$1" n="$2"; while [ "$n" -gt 0 ] && ! gone "$p"; do sleep 0.5; n=$((n-1)); done; gone "$p"; }
result() { printf '{"ok":%s,"version":"%s","error":"%s","rolled_back":%s,"at":%s}\n' "$1" "$VER" "$2" "$3" "$(date +%s)" > "$RESULT.tmp" && mv "$RESULT.tmp" "$RESULT"; }
PREV="$APP.previous"
FAILED="$APP.failed"
ENGINE="$APP/Contents/MacOS/igneum-app"
# the same digest the engine computed when it staged the bundle (src/manifest.rs digest_dir): every regular file,
# byte-sorted relative path, "path\nsha256\n" per file, sha256 of the whole
digest_dir() { (cd "$1" && /usr/bin/find . -type f -print | LC_ALL=C /usr/bin/sort | while IFS= read -r f; do printf '%s\n%s\n' "${f#./}" "$(/usr/bin/shasum -a 256 "$f" | /usr/bin/cut -d' ' -f1)"; done) | /usr/bin/shasum -a 256 | /usr/bin/cut -d' ' -f1; }
# the new engine must ANSWER api/state with the version it was asked for before this helper reports ok (9 October 2026,
# the founder's rule: no machine needs a hand to recover, update or mine; the an earlier release self-update closed the app on both
# house PCs and relaunched neither, and a process check alone cannot tell a dead engine from a live one). The engine
# writes app.url beside the result file; the helper polls it for IGNEUM_OTA_READY_S seconds (120) every
# IGNEUM_OTA_POLL_S (3), and reads "version" from the answer.
READY_S="${IGNEUM_OTA_READY_S:-120}"; POLL_S="${IGNEUM_OTA_POLL_S:-3}"; URLF="$(dirname "$RESULT")/app.url"
engine_answers() { local t0=$(date +%s) u v; while [ $(( $(date +%s) - t0 )) -lt "$READY_S" ]; do u="$(tr -d '[:space:]' < "$URLF" 2>/dev/null)"; if [ -n "$u" ]; then v="$(/usr/bin/curl -s -m 3 "${u}api/state" 2>/dev/null | /usr/bin/python3 -c 'import json,sys; print(json.load(sys.stdin).get("version",""))' 2>/dev/null)"; if [ -n "$v" ]; then if [ -z "$VER" ] || [ "$v" = "$VER" ]; then echo "the engine answers api/state as $v"; return 0; else echo "an engine answers as $v, not $VER; waiting"; fi; fi; fi; sleep "$POLL_S"; done; echo "no engine answered api/state within $READY_S s"; return 1; }
# a test run carries its private-devnet environment to the relaunch (open -n cannot); a normal run goes through LaunchServices;
# IGNEUM_OTA_LAUNCH_CMD is the test seam (tools/ci/ota-mac-helper-check.sh runs the helper over a fake bundle pair with it)
launch() { if [ -n "${IGNEUM_OTA_LAUNCH_CMD:-}" ]; then "$IGNEUM_OTA_LAUNCH_CMD" "$APP"; elif [ -n "$ENVF" ] && [ -f "$ENVF" ]; then (set -a; . "$ENVF"; set +a; /usr/bin/nohup "$APP/Contents/MacOS/Ember" >/dev/null 2>&1 &); else /usr/bin/open -n "$APP"; fi; }
wait_gone "$EPID" 240 || { echo "engine $EPID still running after 120 s; ending it"; kill -9 "$EPID" 2>/dev/null; sleep 1; }
if [ -n "$HPID" ] && [ "$HPID" != 0 ] && ! gone "$HPID"; then
  /usr/bin/osascript -e 'tell application id "network.igneum.ember" to quit' >/dev/null 2>&1 || kill -TERM "$HPID" 2>/dev/null
  wait_gone "$HPID" 80 || { echo "window $HPID still running after 40 s; ending it"; kill -9 "$HPID" 2>/dev/null; sleep 1; }
fi
# anything else from this bundle (a stray engine of an older run)
pkill -f "$APP/Contents/MacOS" 2>/dev/null; sleep 0.5
case "$MODE" in
  apply)
    [ -d "$NEW" ] || { result false "the staged app is missing" false; launch; exit 1; }
    if [ -n "$DIGEST" ]; then
      have="$(digest_dir "$NEW")"
      if [ "$have" != "$DIGEST" ]; then echo "digest mismatch: staged $have, verified $DIGEST"; rm -rf "$NEW"; result false "the staged app changed since it was verified; not installed" false; launch; exit 1; fi
      echo "staged bundle digest verified"
    else
      echo "no digest given; not installing an unverified bundle"; result false "no digest for the staged app" false; launch; exit 1
    fi
    rm -rf "$PREV"
    mv "$APP" "$PREV" || { result false "could not move the old app aside" false; launch; exit 1; }
    mv "$NEW" "$APP" || { mv "$PREV" "$APP"; result false "could not move the new app in" false; launch; exit 1; }
    /usr/bin/xattr -dr com.apple.quarantine "$APP" 2>/dev/null   # only a bundle whose digest just verified
    echo "swapped; opening $APP"
    launch || echo "open failed"
    if engine_answers; then result true "" false; echo "$VER is running and answering"; exit 0; fi
    echo "the new engine did not answer api/state within $READY_S s; opening it once more"
    launch || true
    if engine_answers; then result true "" false; echo "$VER is running and answering (second try)"; exit 0; fi
    echo "the new engine did not answer twice; restoring the previous version"
    pkill -f "$APP/Contents/MacOS" 2>/dev/null; sleep 1
    rm -rf "$FAILED"; mv "$APP" "$FAILED" && mv "$PREV" "$APP"
    launch
    VER=""; if engine_answers; then echo "the previous version is back and answering"; else echo "the previous version did not answer either; opened once more"; launch || true; fi
    result false "Ember did not answer api/state twice after the update; the previous version was restored" true
    ;;
  rollback)
    [ -d "$PREV" ] || { result false "no previous version kept to restore" false; launch; exit 1; }
    rm -rf "$FAILED"; mv "$APP" "$FAILED" && mv "$PREV" "$APP"
    launch
    VER=""; engine_answers || { echo "the previous version did not answer; opened once more"; launch || true; }
    result false "Ember did not stay up twice; the previous version was restored" true
    ;;
  *) echo "unknown mode $MODE"; exit 2 ;;
esac
"#;

/// The Windows helper. Not cfg-gated so the return test above can read it on every platform.
#[allow(dead_code)]
const WIN_HELPER: &str = r#"# Ember update helper, written by the engine (src/ota.rs). Not for running by hand.
#   powershell -File ota-apply.ps1 -Mode apply|rollback -EnginePid <pid> -Installer <setup exe> -Version <v> -Result <json> -InstallDir <folder> -Sha256 <hex> -Previous <folder> -Machine <id8> -Intake <url> -AppDir <app data>
# The installer runs FIRST, while the engine keeps mining (4 October 2026: two unattended PCs sat stopped at an
# administrator prompt nobody could click). A per-user installer (an earlier release and later, PrivilegesRequired=lowest) needs no
# prompt; an older administrator installer raises one through ShellExecute. Only when the installer actually runs does
# its PrepareToInstall step stop the engine (api/quit: miners first, then the node) and replace the files. A declined,
# timed-out or unanswered prompt leaves the engine running: the result says deferred:true and the engine shows "waits for
# the next time someone is at this PC".
# From an earlier release (MF-11, 7 October 2026) this helper owns the return: it keeps the running exe set beside the app before the
# installer runs, starts the app itself afterwards (/IGNOTA=2 tells the installer not to), waits for an engine to answer
# api/state with the new version, restores the kept set when nothing answers inside the window, and posts one line to
# the log intake either way (the key is read from igneum-app.json beside the exe, never from the command line). The
# sequence is src/ota.rs return_sequence(), tested there with injected exit codes; this file mirrors it step for step.
param([string]$Mode, [int]$EnginePid, [string]$Installer, [string]$Version, [string]$Result, [string]$InstallDir, [string]$Sha256 = '', [string]$Previous = '', [string]$Machine = '', [string]$Intake = '', [string]$AppDir = '')
$log = Join-Path (Split-Path -Parent $Result) 'ota-apply.log'
if (-not $AppDir) { $AppDir = Split-Path -Parent $Result }
$ReadyS = 120
$PollS = 3
function Log([string]$t) { Add-Content -Path $log -Value ("{0} {1}" -f (Get-Date -Format s), $t) }
function Done([bool]$ok, [string]$err, [bool]$rb, [bool]$deferred, [string]$ret, [int]$readyS) {
    $o = @{ ok = $ok; version = $Version; error = $err; rolled_back = $rb; deferred = $deferred; at = [int][double](Get-Date -UFormat %s); 'return' = $ret; ready_s = $readyS }
    ($o | ConvertTo-Json -Compress) | Set-Content -Path $Result -Encoding ASCII
}
function EngineAlive() { return [bool](Get-Process -Id $EnginePid -ErrorAction SilentlyContinue) }
function AppUrl() {
    $f = Join-Path $AppDir 'app.url'
    if (Test-Path $f) { return (Get-Content $f -Raw).Trim() }
    return ''
}
function AppVersion() {
    # the engine's own answer: GET <app.url>api/state and its version field (the URL file is rewritten by every start)
    $u = AppUrl
    if (-not $u) { return '' }
    try { $r = Invoke-RestMethod -Uri ($u + 'api/state') -TimeoutSec 5 -UseBasicParsing; return [string]$r.version } catch { return '' }
}
function WaitReady([string]$want, [int]$limitS) {
    # the seconds until an engine answered with the wanted version (any version when $want is empty); -1 when none did
    $t0 = Get-Date
    while (((Get-Date) - $t0).TotalSeconds -lt $limitS) {
        $v = AppVersion
        if ($v -and (($want -eq '') -or ($v -eq $want))) { return [int]((Get-Date) - $t0).TotalSeconds }
        Start-Sleep -Seconds $PollS
    }
    return -1
}
function WaitInstallerClear() {
    # no relaunch while another installer runs (its marker beside app.url, written by the installer's PrepareToInstall and
    # removed at its end); a marker older than 15 min is an installer that died: ignored with a FAULT line (src/ota.rs relaunch_allowed)
    $m = Join-Path $AppDir 'install-running.flag'
    $t0 = Get-Date
    while (Test-Path $m) {
        $age = ((Get-Date) - (Get-Item $m).LastWriteTime).TotalSeconds
        if ($age -gt 900) { Log ('FAULT update-return: a stale installer marker (' + [int]$age + ' s old) was ignored'); break }
        if (((Get-Date) - $t0).TotalMinutes -gt 20) { Log 'FAULT update-return: an installer marker stayed 20 min; relaunching anyway'; break }
        Log 'relaunch held: another installer is running (install-running.flag present)'
        Start-Sleep -Seconds 5
    }
}
function Launch() {
    $exe = Join-Path $InstallDir 'igneum-app.exe'
    if (-not (Test-Path $exe)) { Log ("nothing to start: " + $exe + " is missing"); return $false }
    WaitInstallerClear
    Log ("starting " + $exe + " --launch")
    # console: igneum-app.exe is a windows-subsystem program (no console); -WindowStyle Hidden would hide the window host it opens
    Start-Process -FilePath $exe -ArgumentList '--launch' -WorkingDirectory $InstallDir | Out-Null
    return $true
}
function StopAll() {
    # the quit through the API first (miners, then the node), then whatever is left by name: the installer's own order
    $u = AppUrl
    if ($u) { try { Invoke-WebRequest -Uri ($u + 'api/quit') -Method POST -Body '{}' -ContentType 'application/json' -UseBasicParsing -TimeoutSec 5 | Out-Null } catch { } }
    $deadline = (Get-Date).AddSeconds(40)
    while ((Get-Date) -lt $deadline) {
        if (@(Get-Process -Name 'igneum-app', 'igneumd' -ErrorAction SilentlyContinue).Count -eq 0) { break }
        Start-Sleep -Milliseconds 500
    }
    foreach ($n in @('Ember', 'igneum-app', 'igneum-miner', 'igneum-worker-cuda', 'igneum-worker-opencl', 'igneumd')) {
        Get-Process -Name $n -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue }
    }
    Start-Sleep -Seconds 1
}
function Report([string]$line) {
    # one line to the log intake, label fault-win-<id8>, as site/api/log.mjs expects; the key is the one the installed
    # app carries (igneum-app.json beside the exe, or the kept copy); nothing to post with is logged, never fatal
    Log $line
    if (-not $Intake -or -not $Machine) { return }
    $cfg = Join-Path $InstallDir 'igneum-app.json'
    if (-not (Test-Path $cfg) -and $Previous) { $cfg = Join-Path $Previous 'igneum-app.json' }
    if (-not (Test-Path $cfg)) { Log 'no igneum-app.json to read the upload key from; the line stays in this log'; return }
    try {
        $key = [string](Get-Content $cfg -Raw | ConvertFrom-Json).upload_key
        if (-not $key) { Log 'igneum-app.json carries no upload key'; return }
        $o = @{ label = ('fault-win-' + $Machine); machine = ($env:COMPUTERNAME + '-' + $Machine); run_id = ('update-return-' + $Version); lines = ("IGNEUM-APP version=" + $Version + " machine=" + $Machine + " platform=windows node=?`n" + $line) }
        $bytes = [Text.Encoding]::UTF8.GetBytes(($o | ConvertTo-Json -Compress))
        Invoke-RestMethod -Method Post -Uri $Intake -Headers @{ 'x-igneum-key' = $key } -ContentType 'application/json; charset=utf-8' -Body $bytes -TimeoutSec 30 | Out-Null
        Log 'intake line posted'
    } catch { Log ('intake post failed: ' + $_.Exception.Message) }
}
function KeepPrevious() {
    # the running exe set beside the app, what a rollback restores; packs, build and dist are rebuilt or unneeded
    if (-not $Previous) { return $false }
    try {
        & robocopy $InstallDir $Previous /MIR /XD packs build dist /R:1 /W:1 /NFL /NDL /NJH /NJS /NP | Out-Null
        if ($LASTEXITCODE -lt 8 -and (Test-Path (Join-Path $Previous 'igneum-app.exe'))) { Log ("previous version kept at " + $Previous); return $true }
        Log ("robocopy could not keep the previous version (exit " + $LASTEXITCODE + ")")
    } catch { Log ('could not keep the previous version: ' + $_.Exception.Message) }
    return $false
}
function RestorePrevious() {
    if (-not $Previous -or -not (Test-Path (Join-Path $Previous 'igneum-app.exe'))) { return $false }
    try {
        & robocopy $Previous $InstallDir /MIR /XD packs build dist /R:2 /W:2 /NFL /NDL /NJH /NJS /NP | Out-Null
        if ($LASTEXITCODE -lt 8) { Log 'previous version restored over the install folder'; return $true }
        Log ("robocopy could not restore the previous version (exit " + $LASTEXITCODE + ")")
    } catch { Log ('could not restore the previous version: ' + $_.Exception.Message) }
    return $false
}
function Comeback([int]$code, [bool]$kept) {
    # src/ota.rs return_sequence(): Launch, WaitReady, then Done or StopAll, RestorePrevious, Launch, WaitReady, Done
    if (-not (EngineAlive)) { Launch | Out-Null } else { Log 'the engine is still up after the installer (it did not stop it)' }
    $want = ''
    if ($code -eq 0) { $want = $Version }
    $ready = WaitReady $want $ReadyS
    if ($ready -ge 0) {
        if ($code -eq 0) {
            Done $true '' $false $false 'ok' $ready
            Report ("update-return: ok " + $Version + " up in " + $ready + " s")
            exit 0
        }
        Done $false ("the installer exited with code " + $code + " (see ota-setup.log); the previous version answers again") $false $false 'installer-failed' $ready
        Report ("FAULT update-return: the installer of " + $Version + " exited with code " + $code + "; the previous version answered again after " + $ready + " s")
        exit 1
    }
    Log ("no engine answered api/state with '" + $want + "' inside " + $ReadyS + " s; back to the previous version")
    StopAll
    if ($kept -and (RestorePrevious)) {
        Launch | Out-Null
        $r2 = WaitReady '' $ReadyS
        if ($r2 -ge 0) {
            Done $false ("Ember " + $Version + " did not answer within " + $ReadyS + " s; the previous version was restored") $true $false 'rolled-back' $r2
            Report ("FAULT update-return: " + $Version + " did not answer inside " + $ReadyS + " s after the install; the previous exe set was restored and answers after " + $r2 + " s")
            exit 1
        }
        Done $false ("Ember " + $Version + " did not answer within " + $ReadyS + " s; the previous version was restored but did not answer either") $true $false 'rolled-back-silent' -1
        Report ("FAULT update-return: " + $Version + " did not answer inside " + $ReadyS + " s; the previous exe set was restored and did not answer inside " + $ReadyS + " s either; a hand start is needed on this PC")
        exit 1
    }
    Launch | Out-Null
    Done $false ("Ember " + $Version + " did not answer within " + $ReadyS + " s; no kept version to restore; what is installed was started again") $false $false 'relaunched' -1
    Report ("FAULT update-return: " + $Version + " did not answer inside " + $ReadyS + " s and no previous exe set was kept; what is installed was started again")
    exit 1
}
Log "$Mode : engine $EnginePid installer '$Installer' version $Version previous '$Previous' (the engine keeps mining until the installer runs)"
if (-not (Test-Path $Installer)) { Log 'installer missing'; Done $false 'the downloaded installer is missing' $false $false '' -1; exit 1 }
# the installer is hashed again right before it runs (R4.3.5)
if (-not $Sha256) { Log 'no sha256 given'; Done $false 'no sha256 for the installer; not run' $false $false '' -1; exit 1 }
$have = (Get-FileHash -Path $Installer -Algorithm SHA256).Hash.ToLower()
if ($have -ne $Sha256.ToLower()) { Log "sha256 mismatch: $have"; Remove-Item -Path $Installer -Force -ErrorAction SilentlyContinue; Done $false 'the installer changed since it was verified; not run' $false $false '' -1; exit 1 }
Log 'installer sha256 verified'
$kept = $false
if ($Mode -eq 'apply') { $kept = KeepPrevious }
$setupLog = Join-Path (Split-Path -Parent $Result) 'ota-setup.log'
$setupArgs = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/CLOSEAPPLICATIONS', '/IGNOTA=2', ('/LOG="' + $setupLog + '"'))
$code = -1
try {
    # no -Verb RunAs: a per-user installer just runs; an administrator installer makes Windows ask, and a declined or
    # timed-out prompt comes back here as an exception with the engine still mining
    # console: the Inno Setup installer is a GUI program (no console), /VERYSILENT shows nothing
    $p = Start-Process -FilePath $Installer -ArgumentList $setupArgs -Wait -PassThru
    $code = $p.ExitCode
} catch {
    $msg = $_.Exception.Message
    Log ("installer did not run: " + $msg)
    Log 'OTA: waiting for administrator approval; the engine keeps mining; the update waits for the next time someone is at this PC'
    Done $false ("waiting for administrator approval (" + $msg + ")") $false $true '' -1
    if (-not (EngineAlive)) { Launch | Out-Null }
    exit 1
}
Log ("installer exit " + $code)
if ($Mode -eq 'rollback') {
    # the kept installer of the previous version ran: the same return, reported as the rollback it is
    if (-not (EngineAlive)) { Launch | Out-Null }
    $r = WaitReady '' $ReadyS
    Done $false ("Ember " + $Version + " did not stay up twice; the previous version was reinstalled") $true $false 'rolled-back' $r
    Report ("FAULT update-return: " + $Version + " did not stay up twice; the previous version was reinstalled (installer exit " + $code + ", answered after " + $r + " s)")
    exit 1
}
Comeback $code $kept
"#;
